package runtime

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/session"
)

// Blocker kinds returned by the completion evaluator. They label the unified
// reason shared by loop-time follow-up decisions and the post-loop final
// classification, so hook and runtime code agree on why the loop stopped.
const (
	completionBlockerNone                 = ""
	completionBlockerContractUnsatisfied  = "contract_unsatisfied"
	completionBlockerUnavailableTool      = "unavailable_tool"
	completionBlockerFollowupLimit        = "followup_limit"
	completionBlockerUnexecutedCommitment = "unexecuted_commitment"
	completionBlockerEmptyActionPromise   = "empty_action_promise"
	completionBlockerInputRequest         = "input_request"
	completionBlockerStopReason           = "stop_reason"
)

// LoopEndInput holds the facts the evaluator needs at the end of a turn.
type LoopEndInput struct {
	Contract         session.TaskContract
	Task             session.TaskNode
	UserText         string
	TurnMessage      agentcore.Message
	TurnToolResults  []agentcore.ToolResult
	TurnToolCalls    []agentcore.ToolCall
	UnavailableTools map[string]string
	FollowupCount    int
	MaxFollowups     int
	DeliveryGateSent bool
	AgentRegistry    *agentcore.ToolRegistry
	FullRegistry     *agentcore.ToolRegistry
}

// LoopEndDecision is the evaluator's loop-time output. Hooks translate it into
// the ShouldStopAfterTurn / GetFollowUpMessages pair without re-deriving the
// completion logic. StopLoopNow is the only path that forces the AgentCore
// loop to terminate immediately; the absence of a follow-up is what lets a
// contract-satisfied turn end naturally on the next iteration.
type LoopEndDecision struct {
	ContractSatisfied bool
	StopLoopNow       bool
	ShouldFollowUp    bool
	FollowupMessage   string
	FollowupReason    string
	FailureCategories []string

	BlockerKind      string
	BlockerReason    string
	BlockerText      string
	MissingEvidence  []string
	UnavailableTools map[string]string
	FollowupAttempts int
}

// EvaluateLoopEnd unifies contract follow-up and the deliverable gate for one
// turn. It is a pure function over LoopEndInput; the hook uses its output to
// drive the AgentCore loop.
func EvaluateLoopEnd(in LoopEndInput) LoopEndDecision {
	decision := LoopEndDecision{
		UnavailableTools: cloneStringMap(in.UnavailableTools),
	}
	validation := validateTaskContract(in.Contract, in.Task)
	decision.MissingEvidence = validation.Missing
	decision.ContractSatisfied = !in.Contract.RequiresTools || validation.Satisfied

	if in.Contract.RequiresTools && !validation.Satisfied {
		failures := classifyTurnFailures(in.TurnToolResults, in.TurnToolCalls)
		decision.FailureCategories = failureCategories(failures)
		if len(in.UnavailableTools) > 0 {
			decision.StopLoopNow = true
			decision.BlockerKind = completionBlockerUnavailableTool
			decision.BlockerReason = summarizeUnavailableTools(in.UnavailableTools)
			decision.BlockerText = renderContractBlockerText(in.Contract, validation, in.AgentRegistry, in.FullRegistry)
			return decision
		}
		if in.FollowupCount >= in.MaxFollowups {
			decision.StopLoopNow = true
			decision.BlockerKind = completionBlockerFollowupLimit
			decision.BlockerReason = fmt.Sprintf("task contract could not be satisfied after %d attempts; missing: %s", in.FollowupCount, strings.Join(validation.Missing, ", "))
			decision.FollowupAttempts = in.FollowupCount
			decision.BlockerText = renderContractBlockerText(in.Contract, validation, in.AgentRegistry, in.FullRegistry)
			return decision
		}
		decision.ShouldFollowUp = true
		decision.FollowupReason = "contract_unsatisfied"
		decision.FollowupMessage = taskContractFollowupWithGuidance(validation.Missing, failures, in.Contract, acceptedTools(in.Task))
		return decision
	}

	if shouldRunDeliveryGate(in) {
		decision.ShouldFollowUp = true
		decision.FollowupReason = "unexecuted_commitment"
		decision.FollowupMessage = "You promised an action but did not execute any tool. Continue now with the smallest safe tool call, or state the concrete blocker that prevents execution."
		return decision
	}
	return decision
}

func shouldRunDeliveryGate(in LoopEndInput) bool {
	if in.DeliveryGateSent {
		return false
	}
	if in.TurnMessage.Role != agentcore.RoleAssistant {
		return false
	}
	if !needsAction(in.UserText) {
		return false
	}
	if !looksLikeUnexecutedAction(in.TurnMessage.Content) {
		return false
	}
	if len(in.TurnToolResults) > 0 {
		return false
	}
	if len(in.TurnToolCalls) > 0 {
		return false
	}
	return true
}

func summarizeUnavailableTools(unavailable map[string]string) string {
	if len(unavailable) == 0 {
		return ""
	}
	names := make([]string, 0, len(unavailable))
	for name := range unavailable {
		names = append(names, name)
	}
	sort.Strings(names)
	return fmt.Sprintf("required tools not available: %s", strings.Join(names, ", "))
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// FinalInput holds the facts the post-loop evaluator needs. The runtime
// re-derives unavailable tools and counts follow-up events from the task's
// own execution events; it does not consume any state from the hook.
type FinalInput struct {
	Contract         session.TaskContract
	Task             session.TaskNode
	FinalText        string
	StopReason       string
	UnavailableTools map[string]string
	AgentRegistry    *agentcore.ToolRegistry
	FullRegistry     *agentcore.ToolRegistry
	// FollowupCount and MaxFollowups let the post-loop detect a follow-up
	// limit when the loop ended because the model produced a final text
	// instead of stopping via ShouldStopAfterTurn.
	FollowupCount int
	MaxFollowups  int
}

// FinalClassification is the unified post-loop final state. Callers use it
// to decide between completed / failed / await_user_input without re-deriving
// completion semantics, and BlockerText is the user-facing blocker text the
// runtime appends to the final reply.
type FinalClassification struct {
	State            string
	BlockerKind      string
	BlockerReason    string
	BlockerText      string
	MissingEvidence  []string
	UnavailableTools map[string]string
}

// EvaluateFinal classifies the post-loop final state from the same completion
// facts the loop-time evaluator uses. It re-derives the blocker kind from
// the contract, the task, the final text, the unavailable tool map, and the
// follow-up count; the runtime must not pass any loop-time blocker outcome
// through this function so loop and post-loop can stay in sync.
func EvaluateFinal(in FinalInput) FinalClassification {
	if strings.TrimSpace(in.StopReason) != "" {
		return FinalClassification{
			State:         "failed",
			BlockerKind:   completionBlockerStopReason,
			BlockerReason: in.StopReason,
		}
	}
	validation := validateTaskContract(in.Contract, in.Task)
	if in.Contract.RequiresTools && !validation.Satisfied {
		if kind, reason, text, unavailable := classifyContractBlockerKind(in, validation); kind != "" {
			return FinalClassification{
				State:            "failed",
				BlockerKind:      kind,
				BlockerReason:    reason,
				BlockerText:      text,
				MissingEvidence:  validation.Missing,
				UnavailableTools: unavailable,
			}
		}
		text := renderContractBlockerText(in.Contract, validation, in.AgentRegistry, in.FullRegistry)
		return FinalClassification{
			State:           "failed",
			BlockerKind:     completionBlockerContractUnsatisfied,
			BlockerReason:   strings.Join(validation.Missing, "; "),
			BlockerText:     text,
			MissingEvidence: validation.Missing,
		}
	}
	if looksLikeInputRequest(in.FinalText) {
		return FinalClassification{
			State:       "await_user_input",
			BlockerKind: completionBlockerInputRequest,
		}
	}
	if looksLikeEmptyActionPromise(in.FinalText) || looksLikeUnexecutedCommitment(in.FinalText) {
		validation := taskContractValidation{Satisfied: false}
		text := renderContractBlockerText(in.Contract, validation, in.AgentRegistry, in.FullRegistry)
		return FinalClassification{
			State:         "failed",
			BlockerKind:   completionBlockerUnexecutedCommitment,
			BlockerReason: summarize(in.FinalText),
			BlockerText:   text,
		}
	}
	return FinalClassification{State: "completed"}
}

// classifyContractBlockerKind narrows contract_unsatisfied to the more
// specific loop-time blocker kinds (unavailable_tool / followup_limit) when
// the post-loop inputs can confirm the same cause. It returns the empty kind
// when no narrowing applies.
func classifyContractBlockerKind(in FinalInput, validation taskContractValidation) (string, string, string, map[string]string) {
	if !missingToolsCoveredByUnavailable(validation.Missing, in.UnavailableTools) {
		if in.FollowupCount > 0 && in.FollowupCount >= in.MaxFollowups {
			reason := fmt.Sprintf("task contract could not be satisfied after %d attempts; missing: %s", in.FollowupCount, strings.Join(validation.Missing, ", "))
			text := renderContractBlockerText(in.Contract, validation, in.AgentRegistry, in.FullRegistry)
			return completionBlockerFollowupLimit, reason, text, nil
		}
		return "", "", "", nil
	}
	unavailable := pickUnavailableForMissing(validation.Missing, in.UnavailableTools)
	reason := summarizeUnavailableTools(unavailable)
	text := renderContractBlockerText(in.Contract, validation, in.AgentRegistry, in.FullRegistry)
	return completionBlockerUnavailableTool, reason, text, unavailable
}

// missingToolsCoveredByUnavailable reports whether every "tool:<name>" entry
// in the missing list maps to an entry in the unavailable map. Other missing
// kinds (plan:..., evidence:...) are not required to be covered.
func missingToolsCoveredByUnavailable(missing []string, unavailable map[string]string) bool {
	if len(unavailable) == 0 {
		return false
	}
	covered := false
	for _, m := range missing {
		name := toolNameFromMissing(m)
		if name == "" {
			continue
		}
		if _, ok := unavailable[name]; !ok {
			return false
		}
		covered = true
	}
	return covered
}

// pickUnavailableForMissing returns the unavailable entries that match the
// "tool:<name>" entries in missing. Other missing kinds are skipped.
func pickUnavailableForMissing(missing []string, unavailable map[string]string) map[string]string {
	if len(missing) == 0 || len(unavailable) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, m := range missing {
		name := toolNameFromMissing(m)
		if name == "" {
			continue
		}
		if reason, ok := unavailable[name]; ok {
			out[name] = reason
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// countContractFollowupEvents returns the number of "contract_followup"
// events recorded for the given task, which the hook writes once per
// follow-up. The runtime uses this to keep followupCount authoritative even
// when the loop ends without going through ShouldStopAfterTurn.
func countContractFollowupEvents(task session.TaskNode) int {
	count := 0
	for _, ev := range task.Execution.Events {
		if ev.Type == "contract_followup" {
			count++
		}
	}
	return count
}

// buildBlockedTaskEvidence assembles the evidence map for a blocked task
// execution event. UnavailableTools is stored as map[string]string (tool
// name -> reason) so downstream consumers do not have to re-derive the
// registry lookup to find the denied tool. followupAttempts is included
// only when the blocker kind is followup_limit.
func buildBlockedTaskEvidence(classification FinalClassification, followupAttempts int) map[string]any {
	evidence := map[string]any{
		"blocker_kind": classification.BlockerKind,
		"missing":      classification.MissingEvidence,
	}
	if len(classification.UnavailableTools) > 0 {
		evidence["unavailable"] = classification.UnavailableTools
	}
	if classification.BlockerKind == completionBlockerFollowupLimit {
		evidence["attempts_total"] = followupAttempts
	}
	return evidence
}
