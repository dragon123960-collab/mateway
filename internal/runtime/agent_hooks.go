package runtime

import (
	"context"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/tool"
)

func (rt Runtime) hooksForState(state *session.State, msg channel.InboundMessage, taskID, userText string, trace *traceRecorder, steering []agentcore.Message) agentcore.Hooks {
	steeringSent := false
	hooks := agentcore.Hooks{
		Emit: func(ctx context.Context, event agentcore.Event) error {
			if err := trace.emit(ctx, event); err != nil {
				return err
			}
			switch event.Type {
			case agentcore.EventModelStart:
				rt.emitProgress(msg, *state, taskID, channel.ProgressStep{
					Title:   "model",
					Status:  "thinking",
					Summary: "waiting for model output",
				})
			case agentcore.EventMessageStart:
				if summary := summarizeAssistantToolActivity(event.Message); summary != "" {
					rt.emitProgress(msg, *state, taskID, channel.ProgressStep{
						Title:      "model",
						Status:     "thinking",
						Summary:    summary,
						DurationMS: event.Duration.Milliseconds(),
					})
				}
			case agentcore.EventToolExecutionProgress:
				rt.emitProgress(msg, *state, taskID, channel.ProgressStep{
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
				_ = trace.write(map[string]any{"type": "tool_blocked", "task_id": taskID, "tool": input.ToolCall.Name, "reason": policy.Reason})
				rt.emitProgress(msg, *state, taskID, channel.ProgressStep{Tool: input.ToolCall.Name, Status: "blocked", Summary: policy.Reason})
				return agentcore.BeforeToolCallResult{Block: true, Reason: policy.Reason}, nil
			}
			if policy.RequireApproval {
				approved, reason := rt.approveToolCall(context.Background(), input, policy, trace)
				if !approved {
					state.AddExecutionEvent(taskID, session.ExecutionEvent{
						Type:    "tool_blocked",
						Status:  "failed",
						Tool:    input.ToolCall.Name,
						Summary: reason,
						Evidence: map[string]any{
							"reason": reason,
						},
					})
					_ = trace.write(map[string]any{"type": "approval_rejected", "task_id": taskID, "tool": input.ToolCall.Name, "reason": reason})
					rt.emitProgress(msg, *state, taskID, channel.ProgressStep{Tool: input.ToolCall.Name, Status: "blocked", Summary: reason})
					return agentcore.BeforeToolCallResult{Block: true, Reason: reason}, nil
				}
				_ = trace.write(map[string]any{"type": "approval_granted", "task_id": taskID, "tool": input.ToolCall.Name})
				return agentcore.BeforeToolCallResult{Context: tool.WithApprovalToken(ctx, policy.ApprovalToken)}, nil
			}
			rt.emitProgress(msg, *state, taskID, channel.ProgressStep{Tool: input.ToolCall.Name, Status: "running", Summary: summarizeToolCall(input.ToolCall)})
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
					StepID:   observe.TaskStep.ID,
					Summary:  observe.TaskStep.Summary,
					Evidence: evidence,
				})
				switch observe.TaskStep.Status {
				case "accepted":
				case "failed", "suspect":
				}
				rt.emitProgress(msg, *state, taskID, progressStepFromExecutionEvent(session.ExecutionEvent{
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
	hooks.ShouldStopAfterTurn = func(_ context.Context, turn agentcore.TurnContext) (bool, error) {
		contract := taskContractFromState(*state, taskID)
		currentTask := taskFromState(*state, taskID)
		validation := validateTaskContract(contract, currentTask)
		if contract.RequiresTools && !validation.Satisfied {
			_ = trace.write(map[string]any{"type": "task_contract_unsatisfied", "task_id": taskID, "missing": validation.Missing})
			if followupSent {
				return true, nil
			}
			followupSent = true
			followUps = append(followUps, agentcore.Message{
				Role:    agentcore.RoleUser,
				Content: taskContractFollowup(validation.Missing),
			})
			return false, nil
		}
		if contract.RequiresTools {
			_ = trace.write(map[string]any{"type": "task_contract_satisfied", "task_id": taskID})
		}
		if followupSent || turnHasToolEvidence(turn) || !needsAction(userText) || !looksLikeUnexecutedAction(turn.Message.Content) {
			return false, nil
		}
		followupSent = true
		if len(followUps) == 0 {
			followUps = append(followUps, agentcore.Message{
				Role:    agentcore.RoleUser,
				Content: "You promised an action but did not execute any tool. Continue now with the smallest safe tool call, or state the concrete blocker that prevents execution.",
			})
			_ = trace.write(map[string]any{"type": "deliverable_gate_followup", "task_id": taskID, "reason": "unexecuted_commitment"})
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

var runtimeToolTimeout = func(cfg *config.Root, toolName string) time.Duration {
	_ = cfg
	switch strings.TrimSpace(toolName) {
	case "project.index", "file.read":
		return 30 * time.Second
	case "terminal.run":
		return 120 * time.Second
	default:
		return 60 * time.Second
	}
}

func (rt Runtime) approveToolCall(ctx context.Context, input agentcore.BeforeToolCallContext, policy ToolPolicyHookResult, trace *traceRecorder) (bool, string) {
	reason := strings.TrimSpace(policy.Reason)
	if reason == "" {
		reason = "tool call requires approval"
	}
	if rt.Hooks.ApproveToolCall == nil {
		return false, reason
	}
	_ = trace.write(map[string]any{"type": "approval_requested", "tool": input.ToolCall.Name, "reason": reason})
	decision, err := rt.Hooks.ApproveToolCall(ctx, ApprovalRequest{ToolCall: input.ToolCall, Tool: input.Tool, Reason: reason})
	if err != nil {
		return false, err.Error()
	}
	if !decision.Approved {
		if text := strings.TrimSpace(decision.Reason); text != "" {
			return false, text
		}
		return false, "tool call was rejected"
	}
	return true, ""
}

var runtimeToolProgressInterval = func(cfg *config.Root, toolName string) time.Duration {
	_ = cfg
	if strings.TrimSpace(toolName) == "" {
		return 0
	}
	return 30 * time.Second
}

func acceptToolResult(tool agentcore.Tool, result agentcore.ToolResult) (string, map[string]any) {
	evidence := map[string]any{}
	for key, value := range result.Evidence {
		evidence[key] = value
	}
	if tool != nil {
		contract := agentcore.ContractFor(tool)
		risk := tool.Risk()
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
