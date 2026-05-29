package agentcore

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func Run(ctx context.Context, cfg Config, messages []Message) (Result, error) {
	if cfg.Model == nil {
		return Result{}, fmt.Errorf("agentcore model is required")
	}
	maxIterations := cfg.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 8
	}

	transcript := append([]Message(nil), messages...)
	if err := emit(ctx, cfg.Hooks, Event{Type: EventAgentStart}); err != nil {
		return Result{}, err
	}
	for iteration := 1; iteration <= maxIterations; iteration++ {
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
			if looksLikeMalformedToolCall(assistant.Content) && iteration < maxIterations {
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
		toolResults := make([]ToolResult, 0, len(assistant.ToolCalls))
		terminate := false
		for _, call := range assistant.ToolCalls {
			if err := emit(ctx, cfg.Hooks, Event{Type: EventToolExecutionStart, Message: assistant, ToolCall: call, Iteration: iteration}); err != nil {
				return Result{}, err
			}
			toolStart := time.Now()
			result, blocked, err := prepareAndExecuteTool(ctx, cfg, assistant, call)
			toolDuration := time.Since(toolStart)
			if err != nil {
				return Result{}, err
			}
			if blocked {
				terminate = true
			}
			after, err := afterToolCall(ctx, cfg, assistant, call, result)
			if err != nil {
				return Result{}, err
			}
			if after.ToolResult != nil {
				result = *after.ToolResult
			}
			if after.Terminate {
				terminate = true
			}
			if err := emit(ctx, cfg.Hooks, Event{Type: EventToolExecutionEnd, Message: assistant, ToolCall: call, ToolResult: result, Iteration: iteration, Duration: toolDuration}); err != nil {
				return Result{}, err
			}
			toolResults = append(toolResults, result)
			transcript = append(transcript, Message{
				Role:       RoleTool,
				Content:    result.Content,
				ToolCallID: call.ID,
			})
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
		if terminate || stop {
			result := Result{Messages: transcript, FinalText: finalTextForStoppedTurn(assistant, toolResults), Iterations: iteration}
			return finish(ctx, cfg.Hooks, result)
		}
		if iteration == maxIterations {
			return synthesizeAfterToolBudget(ctx, cfg, transcript, iteration)
		}
	}

	result := Result{
		Messages:   transcript,
		FinalText:  "达到最大工具循环次数，已停止。",
		Iterations: maxIterations,
	}
	return finish(ctx, cfg.Hooks, result)
}

func synthesizeAfterToolBudget(ctx context.Context, cfg Config, transcript []Message, iteration int) (Result, error) {
	transcript = append(transcript, Message{
		Role:    RoleUser,
		Content: "Tool budget reached. Do not call more tools. Synthesize the final answer from the existing tool results, and clearly state any remaining uncertainty.",
	})
	modelStart := time.Now()
	assistant, err := cfg.Model.Next(ctx, Context{
		SystemPrompt: cfg.SystemPrompt,
		Messages:     transcript,
		Tools:        nil,
	})
	modelDuration := time.Since(modelStart)
	if err != nil {
		return Result{}, err
	}
	if assistant.Role == "" {
		assistant.Role = RoleAssistant
	}
	if len(assistant.ToolCalls) > 0 {
		assistant.ToolCalls = nil
		if strings.TrimSpace(assistant.Content) == "" {
			assistant.Content = "达到最大工具循环次数，已停止。"
		}
	}
	if err := emit(ctx, cfg.Hooks, Event{Type: EventMessageStart, Message: assistant, Iteration: iteration + 1, Duration: modelDuration}); err != nil {
		return Result{}, err
	}
	transcript = append(transcript, assistant)
	if err := emit(ctx, cfg.Hooks, Event{Type: EventMessageEnd, Message: assistant, Iteration: iteration + 1}); err != nil {
		return Result{}, err
	}
	result := Result{
		Messages:   transcript,
		FinalText:  strings.TrimSpace(assistant.Content),
		Iterations: iteration,
	}
	return finish(ctx, cfg.Hooks, result)
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
