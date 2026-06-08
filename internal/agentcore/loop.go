package agentcore

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dongping/mateway/internal/util"
)

func Run(ctx context.Context, cfg Config, messages []Message) (Result, error) {
	if cfg.Model == nil {
		return Result{}, fmt.Errorf("agentcore model is required")
	}
	transcript := append([]Message(nil), messages...)
	if err := emit(ctx, cfg.Hooks, Event{Type: EventAgentStart}); err != nil {
		return Result{}, err
	}
	iteration := 0
	malformedAttempts := 0
	for {
		if cfg.MaxIterations > 0 && iteration >= cfg.MaxIterations {
			result := Result{
				Messages:   transcript,
				FinalText:  "Maximum agent iterations reached; execution stopped to avoid an infinite loop.",
				Iterations: iteration,
				StopReason: "max_iterations_exceeded",
			}
			return finish(ctx, cfg.Hooks, result)
		}
		iteration++
		if err := emit(ctx, cfg.Hooks, Event{Type: EventTurnStart, Iteration: iteration}); err != nil {
			return Result{}, err
		}
		steering, err := drainMessages(ctx, cfg.Hooks.GetSteeringMessages)
		if err != nil {
			return Result{}, err
		}
		transcript = append(transcript, steering...)

		modelStart := time.Now()
		if err := emit(ctx, cfg.Hooks, Event{Type: EventModelStart, Iteration: iteration}); err != nil {
			return Result{}, err
		}
		assistant, err := cfg.Model.Next(ctx, Context{
			SystemPrompt: cfg.SystemPrompt,
			Messages:     transcript,
			Tools:        toolsForContext(cfg.Tools),
		})
		modelDuration := time.Since(modelStart)
		if err != nil {
			return Result{}, err
		}
		if assistant.Role == "" {
			assistant.Role = RoleAssistant
		}
		if err := emit(ctx, cfg.Hooks, Event{Type: EventMessageStart, Message: assistant, Iteration: iteration, Duration: modelDuration}); err != nil {
			return Result{}, err
		}
		transcript = append(transcript, assistant)
		if err := emit(ctx, cfg.Hooks, Event{Type: EventMessageEnd, Message: assistant, Iteration: iteration}); err != nil {
			return Result{}, err
		}

		if len(assistant.ToolCalls) == 0 {
			if looksLikeMalformedToolCall(assistant.Content) && malformedAttempts < 2 {
				malformedAttempts++
				transcript = append(transcript, Message{
					Role:    RoleUser,
					Content: "Your previous tool call block was malformed and was not executed. Either emit one valid [TOOL_CALL] JSON block, or stop using tools and answer from the evidence already available.",
				})
				if err := emit(ctx, cfg.Hooks, Event{Type: EventTurnEnd, Message: assistant, Iteration: iteration}); err != nil {
					return Result{}, err
				}
				continue
			}
			if looksLikeMalformedToolCall(assistant.Content) {
				return synthesizeMalformedToolCall(ctx, cfg, transcript, iteration)
			}
			malformedAttempts = 0
			turn := TurnContext{Message: assistant, Messages: transcript, Iteration: iteration}
			stop, err := shouldStopAfterTurn(ctx, cfg.Hooks, turn)
			if err != nil {
				return Result{}, err
			}
			followUps, err := drainMessages(ctx, cfg.Hooks.GetFollowUpMessages)
			if err != nil {
				return Result{}, err
			}
			if !stop && len(followUps) > 0 {
				transcript = append(transcript, followUps...)
				continue
			}
			result := Result{
				Messages:   transcript,
				FinalText:  strings.TrimSpace(assistant.Content),
				Iterations: iteration,
			}
			return finish(ctx, cfg.Hooks, result)
		}

		if cfg.Tools == nil {
			return Result{}, fmt.Errorf("assistant requested tools but no registry is configured")
		}
		malformedAttempts = 0
		toolResults, stopReason, err := executeToolCalls(ctx, cfg, assistant, iteration)
		if err != nil {
			return Result{}, err
		}
		for _, result := range toolResults {
			transcript = append(transcript, Message{Role: RoleTool, Content: result.Content, ToolCallID: result.ToolCallID})
		}
		turn := TurnContext{Message: assistant, ToolResults: toolResults, Messages: transcript, Iteration: iteration}
		if update, err := prepareNextTurn(ctx, cfg.Hooks, turn); err != nil {
			return Result{}, err
		} else if update.Messages != nil {
			transcript = update.Messages
		}
		stop, err := shouldStopAfterTurn(ctx, cfg.Hooks, turn)
		if err != nil {
			return Result{}, err
		}
		if err := emit(ctx, cfg.Hooks, Event{Type: EventTurnEnd, Message: assistant, Iteration: iteration}); err != nil {
			return Result{}, err
		}
		if stopReason != "" || stop {
			result := Result{Messages: transcript, FinalText: finalTextForStoppedTurn(assistant, toolResults), Iterations: iteration, StopReason: stopReason}
			return finish(ctx, cfg.Hooks, result)
		}
	}
}

func synthesizeMalformedToolCall(ctx context.Context, cfg Config, transcript []Message, iteration int) (Result, error) {
	transcript = append(transcript, Message{
		Role:    RoleUser,
		Content: "The last tool call block was malformed and cannot be executed. Do not call more tools. Provide the best final answer from the existing evidence and state what remains unverified.",
	})
	modelStart := time.Now()
	if err := emit(ctx, cfg.Hooks, Event{Type: EventModelStart, Iteration: iteration + 1}); err != nil {
		return Result{}, err
	}
	assistant, err := cfg.Model.Next(ctx, Context{SystemPrompt: cfg.SystemPrompt, Messages: transcript, Tools: nil})
	modelDuration := time.Since(modelStart)
	if err != nil {
		return Result{}, err
	}
	if assistant.Role == "" {
		assistant.Role = RoleAssistant
	}
	if len(assistant.ToolCalls) > 0 || looksLikeMalformedToolCall(assistant.Content) {
		assistant.ToolCalls = nil
		assistant.Content = "Malformed tool call output; execution stopped. Existing tool results are preserved in the trace."
	}
	if err := emit(ctx, cfg.Hooks, Event{Type: EventMessageStart, Message: assistant, Iteration: iteration + 1, Duration: modelDuration}); err != nil {
		return Result{}, err
	}
	transcript = append(transcript, assistant)
	if err := emit(ctx, cfg.Hooks, Event{Type: EventMessageEnd, Message: assistant, Iteration: iteration + 1}); err != nil {
		return Result{}, err
	}
	return finish(ctx, cfg.Hooks, Result{
		Messages:   transcript,
		FinalText:  strings.TrimSpace(assistant.Content),
		Iterations: iteration,
	})
}

func finalTextForStoppedTurn(message Message, toolResults []ToolResult) string {
	if text := strings.TrimSpace(message.Content); text != "" {
		return text
	}
	for i := len(toolResults) - 1; i >= 0; i-- {
		if text := strings.TrimSpace(toolResults[i].Content); text != "" {
			return text
		}
	}
	return ""
}

var firstNonEmptyString = util.FirstNonEmptyString

func toolsForContext(registry *ToolRegistry) []Tool {
	if registry == nil {
		return nil
	}
	return registry.List()
}

func prepareAndExecuteTool(ctx context.Context, cfg Config, message Message, call ToolCall) (ToolResult, bool, error) {
	tool, ok := cfg.Tools.Get(call.Name)
	if !ok {
		return cfg.Tools.Execute(ctx, call), false, nil
	}
	if before := cfg.Hooks.BeforeToolCall; before != nil {
		result, err := before(ctx, BeforeToolCallContext{Message: message, ToolCall: call, Tool: tool})
		if err != nil {
			return ToolResult{}, false, err
		}
		if result.Block {
			reason := strings.TrimSpace(result.Reason)
			if reason == "" {
				reason = "tool execution blocked"
			}
			return ToolResult{ToolCallID: call.ID, Content: reason, IsError: true}, true, nil
		}
		if result.Context != nil {
			ctx = result.Context
		}
	}
	return cfg.Tools.Execute(ctx, call), false, nil
}

func executeToolCalls(ctx context.Context, cfg Config, message Message, iteration int) ([]ToolResult, string, error) {
	if shouldRunToolCallsInParallel(cfg, message.ToolCalls) {
		prepared, blocked, err := prepareParallelToolCalls(ctx, cfg, message, iteration)
		if err != nil {
			return nil, "", err
		}
		if blocked != nil {
			return []ToolResult{*blocked}, "tool_execution_blocked", nil
		}
		return executePreparedToolCallsParallel(ctx, cfg, message, iteration, prepared)
	}
	return executeToolCallsSerial(ctx, cfg, message, iteration)
}

func executeToolCallsSerial(ctx context.Context, cfg Config, message Message, iteration int) ([]ToolResult, string, error) {
	toolResults := make([]ToolResult, 0, len(message.ToolCalls))
	stopReason := ""
	for _, call := range message.ToolCalls {
		result, reason, err := executeOneToolCall(ctx, cfg, message, call, iteration)
		if err != nil {
			return nil, "", err
		}
		if reason != "" {
			stopReason = reason
		}
		toolResults = append(toolResults, result)
	}
	return toolResults, stopReason, nil
}

type preparedToolCall struct {
	Call    ToolCall
	Tool    Tool
	Context context.Context
}

func prepareParallelToolCalls(ctx context.Context, cfg Config, message Message, iteration int) ([]preparedToolCall, *ToolResult, error) {
	prepared := make([]preparedToolCall, 0, len(message.ToolCalls))
	for _, call := range message.ToolCalls {
		tool, ok := cfg.Tools.Get(call.Name)
		if !ok {
			return nil, nil, fmt.Errorf("parallel tool call %q was not found after eligibility check", call.Name)
		}
		if before := cfg.Hooks.BeforeToolCall; before != nil {
			toolStart := time.Now()
			result, err := before(ctx, BeforeToolCallContext{Message: message, ToolCall: call, Tool: tool})
			toolDuration := time.Since(toolStart)
			if err != nil {
				return nil, nil, err
			}
			if result.Block {
				reason := strings.TrimSpace(result.Reason)
				if reason == "" {
					reason = "tool execution blocked"
				}
				blocked := ToolResult{ToolCallID: call.ID, Content: reason, IsError: true}
				if err := emit(ctx, cfg.Hooks, Event{Type: EventToolExecutionStart, Message: message, ToolCall: call, Iteration: iteration}); err != nil {
					return nil, nil, err
				}
				after, err := afterToolCall(ctx, cfg, message, call, blocked)
				if err != nil {
					return nil, nil, err
				}
				if after.ToolResult != nil {
					blocked = *after.ToolResult
				}
				if err := emit(ctx, cfg.Hooks, Event{Type: EventToolExecutionEnd, Message: message, ToolCall: call, ToolResult: blocked, Iteration: iteration, Duration: toolDuration}); err != nil {
					return nil, nil, err
				}
				return nil, &blocked, nil
			}
			if result.Context != nil {
				ctx = result.Context
			}
		}
		prepared = append(prepared, preparedToolCall{Call: call, Tool: tool, Context: ctx})
	}
	return prepared, nil, nil
}

func executePreparedToolCallsParallel(ctx context.Context, cfg Config, message Message, iteration int, prepared []preparedToolCall) ([]ToolResult, string, error) {
	limit := cfg.MaxParallelTools
	if limit <= 0 {
		limit = 4
	}
	results := make([]ToolResult, len(prepared))
	durations := make([]time.Duration, len(prepared))
	var wg sync.WaitGroup
	sem := make(chan struct{}, limit)
	for i, item := range prepared {
		if err := emit(ctx, cfg.Hooks, Event{Type: EventToolExecutionStart, Message: message, ToolCall: item.Call, Iteration: iteration}); err != nil {
			return nil, "", err
		}
		wg.Add(1)
		go func(i int, item preparedToolCall) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			callCtx := ctx
			if item.Context != nil {
				callCtx = item.Context
			}
			result, duration := executeToolWithControls(callCtx, cfg, message, item.Call, item.Tool, iteration, func(execCtx context.Context) ToolResult {
				return cfg.Tools.Execute(execCtx, item.Call)
			})
			results[i] = result
			durations[i] = duration
		}(i, item)
	}
	wg.Wait()
	stopReason := ""
	for i, item := range prepared {
		result := results[i]
		after, err := afterToolCall(ctx, cfg, message, item.Call, result)
		if err != nil {
			return nil, "", err
		}
		if after.ToolResult != nil {
			result = *after.ToolResult
		}
		if after.Terminate {
			stopReason = firstNonEmptyString(after.StopReason, "tool_execution_stopped")
		}
		results[i] = result
		if err := emit(ctx, cfg.Hooks, Event{Type: EventToolExecutionEnd, Message: message, ToolCall: item.Call, ToolResult: result, Iteration: iteration, Duration: durations[i]}); err != nil {
			return nil, "", err
		}
	}
	return results, stopReason, nil
}

func executeOneToolCall(ctx context.Context, cfg Config, message Message, call ToolCall, iteration int) (ToolResult, string, error) {
	if err := emit(ctx, cfg.Hooks, Event{Type: EventToolExecutionStart, Message: message, ToolCall: call, Iteration: iteration}); err != nil {
		return ToolResult{}, "", err
	}
	tool, _ := cfg.Tools.Get(call.Name)
	var blocked bool
	var execErr error
	result, toolDuration := executeToolWithControls(ctx, cfg, message, call, tool, iteration, func(execCtx context.Context) ToolResult {
		var runResult ToolResult
		runResult, blocked, execErr = prepareAndExecuteTool(execCtx, cfg, message, call)
		return runResult
	})
	err := execErr
	if err != nil {
		return ToolResult{}, "", err
	}
	after, err := afterToolCall(ctx, cfg, message, call, result)
	if err != nil {
		return ToolResult{}, "", err
	}
	if after.ToolResult != nil {
		result = *after.ToolResult
	}
	stopReason := ""
	if blocked {
		stopReason = "tool_execution_blocked"
	}
	if after.Terminate {
		stopReason = firstNonEmptyString(after.StopReason, "tool_execution_stopped")
	}
	if err := emit(ctx, cfg.Hooks, Event{Type: EventToolExecutionEnd, Message: message, ToolCall: call, ToolResult: result, Iteration: iteration, Duration: toolDuration}); err != nil {
		return ToolResult{}, "", err
	}
	return result, stopReason, nil
}

func executeToolWithControls(ctx context.Context, cfg Config, message Message, call ToolCall, tool Tool, iteration int, run func(context.Context) ToolResult) (ToolResult, time.Duration) {
	deadline := toolTimeout(cfg.Hooks, message, call, tool)
	execCtx := ctx
	cancel := func() {}
	if deadline > 0 {
		execCtx, cancel = context.WithTimeout(ctx, deadline)
	}
	defer cancel()
	stopProgress := startToolProgress(ctx, cfg, message, call, iteration, deadline, toolProgressInterval(cfg.Hooks, message, call, tool))
	defer stopProgress()
	toolStart := time.Now()
	result := run(execCtx)
	duration := time.Since(toolStart)
	return enrichToolTimeoutResult(result, call, execCtx, duration, deadline), duration
}

func toolTimeout(hooks Hooks, message Message, call ToolCall, tool Tool) time.Duration {
	if hooks.ToolTimeout == nil {
		return 0
	}
	timeout := hooks.ToolTimeout(ToolExecutionContext{Message: message, ToolCall: call, Tool: tool})
	if timeout < 0 {
		return 0
	}
	return timeout
}

func toolProgressInterval(hooks Hooks, message Message, call ToolCall, tool Tool) time.Duration {
	if hooks.ToolProgressInterval == nil {
		return 0
	}
	interval := hooks.ToolProgressInterval(ToolExecutionContext{Message: message, ToolCall: call, Tool: tool})
	if interval < 0 {
		return 0
	}
	return interval
}

func startToolProgress(ctx context.Context, cfg Config, message Message, call ToolCall, iteration int, deadline, interval time.Duration) func() {
	if interval <= 0 || cfg.Hooks.Emit == nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		timer := time.NewTimer(interval)
		defer timer.Stop()
		start := time.Now()
		for {
			select {
			case <-timer.C:
				elapsed := time.Since(start)
				result := ToolResult{
					ToolCallID: call.ID,
					Evidence: map[string]any{
						"elapsed_ms":  elapsed.Milliseconds(),
						"deadline_ms": deadline.Milliseconds(),
					},
				}
				_ = emit(ctx, cfg.Hooks, Event{Type: EventToolExecutionProgress, Message: message, ToolCall: call, ToolResult: result, Iteration: iteration, Duration: elapsed})
				timer.Reset(interval)
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return func() { close(done) }
}

func enrichToolTimeoutResult(result ToolResult, call ToolCall, ctx context.Context, duration, deadline time.Duration) ToolResult {
	if result.ToolCallID == "" {
		result.ToolCallID = call.ID
	}
	if result.Evidence == nil {
		result.Evidence = map[string]any{}
	}
	if _, ok := result.Evidence["elapsed_ms"]; !ok {
		result.Evidence["elapsed_ms"] = duration.Milliseconds()
	}
	if deadline > 0 {
		if _, ok := result.Evidence["deadline_ms"]; !ok {
			result.Evidence["deadline_ms"] = deadline.Milliseconds()
		}
	}
	if ctx.Err() == context.DeadlineExceeded {
		result.IsError = true
		result.Evidence["timed_out"] = true
		if strings.TrimSpace(result.Content) == "" || strings.Contains(result.Content, context.DeadlineExceeded.Error()) {
			result.Content = fmt.Sprintf("tool %q timed out after %s", call.Name, deadline)
		}
	} else if ctx.Err() == context.Canceled {
		result.IsError = true
		if _, ok := result.Evidence["cancelled"]; !ok {
			result.Evidence["cancelled"] = true
		}
	}
	return result
}

func shouldRunToolCallsInParallel(cfg Config, calls []ToolCall) bool {
	if cfg.MaxParallelTools == 1 || len(calls) < 2 || cfg.Tools == nil {
		return false
	}
	for _, call := range calls {
		tool, ok := cfg.Tools.Get(call.Name)
		if !ok {
			return false
		}
		if tool.Risk() != RiskSafeRead {
			return false
		}
		contract := ContractFor(tool)
		if strings.TrimSpace(contract.ParallelMode) != "read_only_ok" {
			return false
		}
	}
	return true
}

func afterToolCall(ctx context.Context, cfg Config, message Message, call ToolCall, result ToolResult) (AfterToolCallResult, error) {
	if cfg.Hooks.AfterToolCall == nil {
		return AfterToolCallResult{}, nil
	}
	tool := Tool(nil)
	if cfg.Tools != nil {
		tool, _ = cfg.Tools.Get(call.Name)
	}
	return cfg.Hooks.AfterToolCall(ctx, AfterToolCallContext{Message: message, ToolCall: call, Tool: tool, ToolResult: result})
}

func shouldStopAfterTurn(ctx context.Context, hooks Hooks, turn TurnContext) (bool, error) {
	if hooks.ShouldStopAfterTurn == nil {
		return false, nil
	}
	return hooks.ShouldStopAfterTurn(ctx, turn)
}

func prepareNextTurn(ctx context.Context, hooks Hooks, turn TurnContext) (NextTurnUpdate, error) {
	if hooks.PrepareNextTurn == nil {
		return NextTurnUpdate{}, nil
	}
	return hooks.PrepareNextTurn(ctx, turn)
}

func drainMessages(ctx context.Context, fn func(context.Context) ([]Message, error)) ([]Message, error) {
	if fn == nil {
		return nil, nil
	}
	return fn(ctx)
}

func emit(ctx context.Context, hooks Hooks, event Event) error {
	if hooks.Emit == nil {
		return nil
	}
	return hooks.Emit(ctx, event)
}

func finish(ctx context.Context, hooks Hooks, result Result) (Result, error) {
	if err := emit(ctx, hooks, Event{Type: EventAgentEnd}); err != nil {
		return Result{}, err
	}
	return result, nil
}

func looksLikeMalformedToolCall(text string) bool {
	return strings.Contains(strings.ToUpper(text), "[TOOL_CALL]")
}
