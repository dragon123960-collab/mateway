package runtime

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/session"
	toolpkg "github.com/dongping/mateway/internal/tool"
)

func (rt Runtime) hooksForState(state *session.State, msg channel.InboundMessage, taskID, userText string, trace *traceRecorder, steering []agentcore.Message) agentcore.Hooks {
	steeringSent := false
	progressEventOffset := taskExecutionEventCount(*state, taskID)
	hooks := agentcore.Hooks{
		Emit: func(ctx context.Context, event agentcore.Event) error {
			if err := trace.emit(ctx, event); err != nil {
				return err
			}
			switch event.Type {
			case agentcore.EventModelStart:
				rt.emitProgress(msg, *state, taskID, progressEventOffset, channel.ProgressStep{
					Title:   "model",
					Status:  "thinking",
					Summary: "waiting for model output",
				})
			case agentcore.EventMessageStart:
				if summary := summarizeAssistantToolActivity(event.Message); summary != "" {
					rt.emitProgress(msg, *state, taskID, progressEventOffset, channel.ProgressStep{
						Title:      "model",
						Status:     "thinking",
						Summary:    summary,
						DurationMS: event.Duration.Milliseconds(),
					})
				}
			case agentcore.EventToolExecutionProgress:
				rt.emitProgress(msg, *state, taskID, progressEventOffset, channel.ProgressStep{
					Title:      event.ToolCall.Name,
					Status:     "running",
					Tool:       event.ToolCall.Name,
					Summary:    summarizeToolCall(event.ToolCall),
					DurationMS: event.Duration.Milliseconds(),
				})
			}
			return nil
		},
		ToolTimeout: func(input agentcore.ToolExecutionContext) time.Duration {
			return runtimeToolTimeout(rt.Config, input.ToolCall.Name)
		},
		ToolProgressInterval: func(input agentcore.ToolExecutionContext) time.Duration {
			return runtimeToolProgressInterval(rt.Config, input.ToolCall.Name)
		},
		GetSteeringMessages: func(context.Context) ([]agentcore.Message, error) {
			if steeringSent {
				return nil, nil
			}
			steeringSent = true
			return append([]agentcore.Message(nil), steering...), nil
		},
		BeforeToolCall: func(ctx context.Context, input agentcore.BeforeToolCallContext) (agentcore.BeforeToolCallResult, error) {
			policy := rt.Hooks.toolPolicy(context.Background(), ToolPolicyHookInput{ToolCall: input.ToolCall, Tool: input.Tool, Config: rt.Config}, trace)
			if policy.Block {
				state.AddExecutionEvent(taskID, session.ExecutionEvent{
					Type:    "tool_blocked",
					Status:  "failed",
					Tool:    input.ToolCall.Name,
					Summary: policy.Reason,
					Evidence: map[string]any{
						"reason": policy.Reason,
					},
				})
				updatePlanItemsForToolResult(state, taskID, input.ToolCall.Name, "blocked", policy.Reason)
				_ = trace.write(map[string]any{"type": "tool_blocked", "task_id": taskID, "tool": input.ToolCall.Name, "reason": policy.Reason})
				rt.emitProgress(msg, *state, taskID, progressEventOffset, channel.ProgressStep{Tool: input.ToolCall.Name, Status: "blocked", Summary: policy.Reason})
				return agentcore.BeforeToolCallResult{Block: true, Reason: policy.Reason}, nil
			}
			markPlanItemRunning(state, taskID, input.ToolCall.Name)
			rt.emitProgress(msg, *state, taskID, progressEventOffset, channel.ProgressStep{Tool: input.ToolCall.Name, Status: "running", Summary: summarizeToolCall(input.ToolCall)})
			return agentcore.BeforeToolCallResult{}, nil
		},
		AfterToolCall: func(_ context.Context, input agentcore.AfterToolCallContext) (agentcore.AfterToolCallResult, error) {
			observe := rt.Hooks.observe(context.Background(), ObserveHookInput{
				Kind:       "tool_result",
				State:      *state,
				TaskID:     taskID,
				ToolCall:   input.ToolCall,
				Tool:       input.Tool,
				ToolResult: input.ToolResult,
			}, trace)
			if observe.TaskStep != nil {
				state.AddStep(taskID, *observe.TaskStep)
				stepID := observe.TaskStep.ID
				if stepID == "" {
					ct := taskFromState(*state, taskID)
					if len(ct.Steps) > 0 {
						stepID = ct.Steps[len(ct.Steps)-1].ID
					}
				}
				evidence := map[string]any{
					"accepted": observe.TaskStep.Accepted,
					"mutation": observe.TaskStep.Mutation,
					"risk":     observe.TaskStep.Risk,
				}
				for key, value := range input.ToolResult.Evidence {
					switch key {
					case "elapsed_ms", "timed_out", "deadline_ms", "output_truncated":
						evidence[key] = value
					}
				}
				state.AddExecutionEvent(taskID, session.ExecutionEvent{
					Type:     "tool_result",
					Status:   observe.TaskStep.Status,
					Tool:     input.ToolCall.Name,
					StepID:   stepID,
					Summary:  observe.TaskStep.Summary,
					Evidence: evidence,
				})
				updatePlanItemsForToolResult(state, taskID, input.ToolCall.Name, observe.TaskStep.Status, observe.TaskStep.Summary)
				switch observe.TaskStep.Status {
				case "accepted":
				case "failed", "suspect":
				}
				rt.emitProgress(msg, *state, taskID, progressEventOffset, progressStepFromExecutionEvent(session.ExecutionEvent{
					Type:     "tool_result",
					Status:   observe.TaskStep.Status,
					Tool:     input.ToolCall.Name,
					Summary:  observe.TaskStep.Summary,
					Evidence: evidence,
				}))
			}
			result := compactToolResultForModel(input.ToolCall, input.ToolResult, rt.home(), trace.id)
			return agentcore.AfterToolCallResult{ToolResult: &result}, nil
		},
	}
	var followUps []agentcore.Message
	followupSent := false
	contractFollowups := 0
	hooks.ShouldStopAfterTurn = func(_ context.Context, turn agentcore.TurnContext) (bool, error) {
		contract := taskContractFromState(*state, taskID)
		currentTask := taskFromState(*state, taskID)
		unavailable := checkUnavailableContractTools(rt, msg, contract)
		decision := EvaluateLoopEnd(LoopEndInput{
			Contract:         contract,
			Task:             currentTask,
			UserText:         userText,
			TurnMessage:      turn.Message,
			TurnToolResults:  turn.ToolResults,
			TurnToolCalls:    turn.Message.ToolCalls,
			UnavailableTools: unavailable,
			FollowupCount:    contractFollowups,
			MaxFollowups:     rt.Config.Execution.MaxContractFollowupsValue(),
			DeliveryGateSent: followupSent,
			AgentRegistry:    agentToolsForMessage(rt, msg),
			FullRegistry:     rt.Tools,
		})
		if contract.RequiresTools && !decision.ContractSatisfied {
			_ = trace.write(map[string]any{
				"type":    "task_contract_unsatisfied",
				"task_id": taskID,
				"missing": decision.MissingEvidence,
			})
			if len(decision.FailureCategories) > 0 {
				_ = trace.write(map[string]any{
					"type":               "tool_failures_classified",
					"task_id":            taskID,
					"failure_categories": decision.FailureCategories,
				})
			}
		}
		if decision.StopLoopNow {
			switch decision.BlockerKind {
			case completionBlockerUnavailableTool:
				names := make([]string, 0, len(unavailable))
				for name := range unavailable {
					names = append(names, name)
				}
				sort.Strings(names)
				traceReasons := make(map[string]string, len(unavailable))
				for name, reason := range unavailable {
					traceReasons[name] = reason
				}
				_ = trace.write(map[string]any{
					"type":                "contract_tool_unavailable",
					"task_id":             taskID,
					"unavailable":         names,
					"unavailable_reasons": traceReasons,
					"blocker_text":        decision.BlockerReason,
				})
			case completionBlockerFollowupLimit:
				_ = trace.write(map[string]any{
					"type":           "task_contract_followup_limit",
					"task_id":        taskID,
					"attempts_total": decision.FollowupAttempts,
					"missing":        decision.MissingEvidence,
					"blocker_text":   decision.BlockerReason,
				})
			}
			return true, nil
		}
		if decision.ShouldFollowUp {
			contractFollowups++
			followUps = append(followUps, agentcore.Message{
				Role:    agentcore.RoleUser,
				Content: decision.FollowupMessage,
			})
			if decision.FollowupReason == "unexecuted_commitment" {
				followupSent = true
				_ = trace.write(map[string]any{
					"type":    "deliverable_gate_followup",
					"task_id": taskID,
					"reason":  decision.FollowupReason,
				})
			} else {
				_ = trace.write(map[string]any{
					"type":    "contract_followup_sent",
					"task_id": taskID,
					"attempt": contractFollowups,
					"missing": decision.MissingEvidence,
				})
				// Record a task execution event so the runtime can re-derive
				// followupCount post-loop without consulting the hook.
				state.AddExecutionEvent(taskID, session.ExecutionEvent{
					Type:     "contract_followup",
					Status:   "running",
					Summary:  decision.FollowupReason,
					Evidence: map[string]any{"attempt": contractFollowups, "missing": decision.MissingEvidence},
				})
			}
			return false, nil
		}
		if contract.RequiresTools {
			_ = trace.write(map[string]any{"type": "task_contract_satisfied", "task_id": taskID})
		}
		return false, nil
	}
	hooks.GetFollowUpMessages = func(context.Context) ([]agentcore.Message, error) {
		out := followUps
		followUps = nil
		return out, nil
	}
	return hooks
}

func taskExecutionEventCount(state session.State, taskID string) int {
	task := taskFromState(state, taskID)
	return len(task.Execution.Events)
}

var runtimeToolTimeout = func(cfg *config.Root, toolName string) time.Duration {
	_ = cfg
	switch strings.TrimSpace(toolName) {
	case "file.read":
		return 30 * time.Second
	case "terminal.run":
		return 120 * time.Second
	default:
		return 60 * time.Second
	}
}

var runtimeToolProgressInterval = func(cfg *config.Root, toolName string) time.Duration {
	_ = cfg
	if strings.TrimSpace(toolName) == "" {
		return 0
	}
	return 30 * time.Second
}

func acceptToolResult(tool agentcore.Tool, call agentcore.ToolCall, result agentcore.ToolResult) (string, map[string]any) {
	evidence := map[string]any{}
	for key, value := range result.Evidence {
		evidence[key] = value
	}
	if tool != nil {
		contract := agentcore.ContractFor(tool)
		risk := toolpkg.EffectiveRisk(tool, call)
		evidence["risk"] = string(risk)
		evidence["mutation"] = risk == agentcore.RiskGuardedMutation || risk == agentcore.RiskDangerous
		if contract.Acceptance != "" {
			evidence["acceptance_criteria"] = contract.Acceptance
		}
		if contract.Evidence != "" {
			evidence["evidence_contract"] = contract.Evidence
		}
	}
	if result.IsError {
		evidence["acceptance"] = "failed"
		return "failed", evidence
	}
	if len(result.Evidence) == 0 && strings.TrimSpace(result.Content) == "" {
		evidence["acceptance"] = "suspect"
		return "suspect", evidence
	}
	evidence["acceptance"] = "accepted"
	return "accepted", evidence
}

func checkUnavailableContractTools(rt Runtime, msg channel.InboundMessage, contract session.TaskContract) map[string]string {
	fullRegistry := rt.Tools
	agentRegistry := rt.Tools
	if agent := rt.Pool.AgentForMessage(msg); agent != nil && agent.Tools != nil {
		agentRegistry = agent.Tools
	}
	return checkContractToolAvailability(agentRegistry, fullRegistry, contract)
}

func agentToolsForMessage(rt Runtime, msg channel.InboundMessage) *agentcore.ToolRegistry {
	if agent := rt.Pool.AgentForMessage(msg); agent != nil && agent.Tools != nil {
		return agent.Tools
	}
	return rt.Tools
}
