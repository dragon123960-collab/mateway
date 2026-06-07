package agentcore

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
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
				FinalText:  "已达到最大执行轮数，任务已停止，避免无限循环。",
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
		if iteration == 1 && cfg.Tools != nil {
			transcript = append(transcript, Message{
				Role:    RoleUser,
				Content: "Are you sure this is complete? If the task requires tools, use them now.",
			})
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
		assistant.Content = "工具调用格式无效，已停止执行。已有工具结果已保留在 trace 中；请重试或把任务说得更具体。"
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

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

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
	Call ToolCall
	Tool Tool
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
		}
		prepared = append(prepared, preparedToolCall{Call: call, Tool: tool})
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
			toolStart := time.Now()
			results[i] = cfg.Tools.Execute(ctx, item.Call)
			durations[i] = time.Since(toolStart)
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
	toolStart := time.Now()
	result, blocked, err := prepareAndExecuteTool(ctx, cfg, message, call)
	toolDuration := time.Since(toolStart)
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
