package runtime

import (
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/session"
)

func TestEvaluateLoopEndSatisfiedContractEndsNaturally(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"web.search"},
	}
	task := session.TaskNode{ID: "task-1", Status: "running"}
	task.Steps = []session.TaskStep{{Tool: "web.search", Status: "accepted", Accepted: true}}
	decision := EvaluateLoopEnd(LoopEndInput{
		Contract:     contract,
		Task:         task,
		UserText:     "search weather",
		TurnMessage:  agentcore.Message{Role: agentcore.RoleAssistant, Content: "Weather is clear."},
		MaxFollowups: 4,
	})
	if !decision.ContractSatisfied {
		t.Fatalf("expected contract satisfied, got %#v", decision)
	}
	if decision.ShouldFollowUp {
		t.Fatalf("expected no follow-up, got %#v", decision)
	}
	if decision.StopLoopNow {
		t.Fatalf("expected loop to be allowed to end naturally, not force stop, got %#v", decision)
	}
	if decision.BlockerKind != "" {
		t.Fatalf("expected no blocker, got kind=%q reason=%q", decision.BlockerKind, decision.BlockerReason)
	}
}

func TestEvaluateLoopEndMissingEvidenceFollowsUp(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"web.search"},
	}
	task := session.TaskNode{ID: "task-1", Status: "running"}
	decision := EvaluateLoopEnd(LoopEndInput{
		Contract:     contract,
		Task:         task,
		UserText:     "search weather",
		TurnMessage:  agentcore.Message{Role: agentcore.RoleAssistant, Content: "I will search now."},
		MaxFollowups: 4,
	})
	if decision.ContractSatisfied {
		t.Fatalf("expected contract unsatisfied, got %#v", decision)
	}
	if !decision.ShouldFollowUp {
		t.Fatalf("expected follow-up, got %#v", decision)
	}
	if decision.StopLoopNow {
		t.Fatalf("expected loop to continue, got %#v", decision)
	}
	if decision.FollowupReason != "contract_unsatisfied" {
		t.Fatalf("expected contract_unsatisfied follow-up reason, got %q", decision.FollowupReason)
	}
	if !strings.Contains(decision.FollowupMessage, "web.search") {
		t.Fatalf("expected follow-up to mention missing tool, got %q", decision.FollowupMessage)
	}
	if len(decision.MissingEvidence) == 0 {
		t.Fatalf("expected missing evidence list to be populated, got %#v", decision)
	}
}

func TestEvaluateLoopEndUnavailableToolForceStopsWithBlockerText(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"terminal.run"},
		Summary:       "check service",
	}
	task := session.TaskNode{ID: "task-1", Status: "running"}
	full := agentcore.NewToolRegistry()
	full.Register(runtimeNamedTool{name: "terminal.run", content: "ok"})
	agent := agentcore.NewToolRegistry()
	decision := EvaluateLoopEnd(LoopEndInput{
		Contract:         contract,
		Task:             task,
		UserText:         "check service",
		TurnMessage:      agentcore.Message{Role: agentcore.RoleAssistant, Content: "Will run."},
		UnavailableTools: map[string]string{"terminal.run": "denied by profile"},
		MaxFollowups:     4,
		AgentRegistry:    agent,
		FullRegistry:     full,
	})
	if !decision.StopLoopNow {
		t.Fatalf("expected loop to stop on unavailable tool, got %#v", decision)
	}
	if decision.ShouldFollowUp {
		t.Fatalf("expected no follow-up on unavailable tool, got %#v", decision)
	}
	if decision.BlockerKind != completionBlockerUnavailableTool {
		t.Fatalf("expected unavailable_tool blocker, got %q", decision.BlockerKind)
	}
	if !strings.Contains(decision.BlockerReason, "terminal.run") {
		t.Fatalf("expected blocker reason to name the tool, got %q", decision.BlockerReason)
	}
	if !strings.Contains(decision.BlockerText, "denied by profile") {
		t.Fatalf("expected blocker text to explain reason, got %q", decision.BlockerText)
	}
}

func TestEvaluateLoopEndFollowupLimitForceStopsWithBlockerText(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"file.edit"},
		Summary:       "edit file",
	}
	task := session.TaskNode{ID: "task-1", Status: "running"}
	full := agentcore.NewToolRegistry()
	full.Register(runtimeNamedTool{name: "file.edit", content: "ok"})
	decision := EvaluateLoopEnd(LoopEndInput{
		Contract:      contract,
		Task:          task,
		UserText:      "edit file",
		TurnMessage:   agentcore.Message{Role: agentcore.RoleAssistant, Content: "Trying again."},
		FollowupCount: 2,
		MaxFollowups:  2,
		FullRegistry:  full,
	})
	if !decision.StopLoopNow {
		t.Fatalf("expected loop to stop at followup limit, got %#v", decision)
	}
	if decision.ShouldFollowUp {
		t.Fatalf("expected no follow-up when at limit, got %#v", decision)
	}
	if decision.BlockerKind != completionBlockerFollowupLimit {
		t.Fatalf("expected followup_limit blocker, got %q", decision.BlockerKind)
	}
	if !strings.Contains(decision.BlockerReason, "file.edit") {
		t.Fatalf("expected blocker reason to name the missing tool, got %q", decision.BlockerReason)
	}
	if decision.FollowupAttempts != 2 {
		t.Fatalf("expected followup attempts to be carried through, got %d", decision.FollowupAttempts)
	}
	if !strings.Contains(decision.BlockerText, "file.edit") {
		t.Fatalf("expected blocker text to include the tool name, got %q", decision.BlockerText)
	}
}

func TestEvaluateLoopEndUnexecutedCommitmentFollowsUp(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"file.read"},
	}
	task := session.TaskNode{ID: "task-1", Status: "running"}
	task.Steps = []session.TaskStep{{Tool: "file.read", Status: "accepted", Accepted: true}}
	decision := EvaluateLoopEnd(LoopEndInput{
		Contract:     contract,
		Task:         task,
		UserText:     "read the README",
		TurnMessage:  agentcore.Message{Role: agentcore.RoleAssistant, Content: "I will read it now."},
		MaxFollowups: 4,
	})
	if !decision.ContractSatisfied {
		t.Fatalf("expected contract satisfied, got %#v", decision)
	}
	if !decision.ShouldFollowUp {
		t.Fatalf("expected deliverable gate follow-up, got %#v", decision)
	}
	if decision.FollowupReason != "unexecuted_commitment" {
		t.Fatalf("expected unexecuted_commitment reason, got %q", decision.FollowupReason)
	}
}

func TestEvaluateLoopEndSkipsDeliveryGateWithToolEvidence(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"file.read"},
	}
	task := session.TaskNode{ID: "task-1", Status: "running"}
	task.Steps = []session.TaskStep{{Tool: "file.read", Status: "accepted", Accepted: true}}
	decision := EvaluateLoopEnd(LoopEndInput{
		Contract:    contract,
		Task:        task,
		UserText:    "check files",
		TurnMessage: agentcore.Message{Role: agentcore.RoleAssistant, Content: "I will check now."},
		TurnToolResults: []agentcore.ToolResult{
			{ToolCallID: "call_1", Content: "ok"},
		},
		MaxFollowups: 4,
	})
	if !decision.ContractSatisfied {
		t.Fatalf("expected contract satisfied, got %#v", decision)
	}
	if decision.ShouldFollowUp {
		t.Fatalf("expected no follow-up when tool evidence exists, got %#v", decision)
	}
	if decision.StopLoopNow {
		t.Fatalf("expected loop to be allowed to end naturally, got %#v", decision)
	}
}

func TestEvaluateLoopEndDeliveryGateOnlyOnce(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"file.read"},
	}
	task := session.TaskNode{ID: "task-1", Status: "running"}
	task.Steps = []session.TaskStep{{Tool: "file.read", Status: "accepted", Accepted: true}}
	decision := EvaluateLoopEnd(LoopEndInput{
		Contract:         contract,
		Task:             task,
		UserText:         "check files",
		TurnMessage:      agentcore.Message{Role: agentcore.RoleAssistant, Content: "I will check now."},
		DeliveryGateSent: true,
		MaxFollowups:     4,
	})
	if decision.ShouldFollowUp {
		t.Fatalf("expected no follow-up when delivery gate already sent, got %#v", decision)
	}
	if decision.StopLoopNow {
		t.Fatalf("expected loop to be allowed to end naturally, got %#v", decision)
	}
}

func TestEvaluateFinalCompleted(t *testing.T) {
	contract := session.TaskContract{RequiresTools: true, RequiredTools: []string{"web.search"}}
	task := session.TaskNode{ID: "task-1", Status: "running"}
	task.Steps = []session.TaskStep{{Tool: "web.search", Status: "accepted", Accepted: true}}
	classification := EvaluateFinal(FinalInput{
		Contract:  contract,
		Task:      task,
		FinalText: "Weather is clear.",
	})
	if classification.State != "completed" {
		t.Fatalf("expected completed, got %#v", classification)
	}
	if classification.BlockerKind != "" {
		t.Fatalf("expected no blocker, got %q", classification.BlockerKind)
	}
}

func TestEvaluateFinalStopReason(t *testing.T) {
	contract := session.TaskContract{RequiresTools: true, RequiredTools: []string{"web.search"}}
	task := session.TaskNode{ID: "task-1", Status: "running"}
	classification := EvaluateFinal(FinalInput{
		Contract:   contract,
		Task:       task,
		StopReason: "max_iterations_exceeded",
	})
	if classification.State != "failed" {
		t.Fatalf("expected failed, got %#v", classification)
	}
	if classification.BlockerKind != completionBlockerStopReason {
		t.Fatalf("expected stop_reason blocker, got %q", classification.BlockerKind)
	}
	if classification.BlockerReason != "max_iterations_exceeded" {
		t.Fatalf("expected stop reason to be carried, got %q", classification.BlockerReason)
	}
}

func TestEvaluateFinalContractUnsatisfied(t *testing.T) {
	contract := session.TaskContract{RequiresTools: true, RequiredTools: []string{"web.search", "file.write"}}
	task := session.TaskNode{ID: "task-1", Status: "running"}
	classification := EvaluateFinal(FinalInput{
		Contract:  contract,
		Task:      task,
		FinalText: "I have key data, will continue later.",
	})
	if classification.State != "failed" {
		t.Fatalf("expected failed, got %#v", classification)
	}
	if classification.BlockerKind != completionBlockerContractUnsatisfied {
		t.Fatalf("expected contract_unsatisfied blocker, got %q", classification.BlockerKind)
	}
	if len(classification.MissingEvidence) == 0 {
		t.Fatalf("expected missing evidence to be carried, got %#v", classification)
	}
	if classification.BlockerText == "" {
		t.Fatalf("expected blocker text to be filled, got %#v", classification)
	}
}

func TestEvaluateFinalNarrowsToUnavailableTool(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"terminal.run"},
		Summary:       "check service",
	}
	task := session.TaskNode{ID: "task-1", Status: "running"}
	full := agentcore.NewToolRegistry()
	full.Register(runtimeNamedTool{name: "terminal.run", content: "ok"})
	classification := EvaluateFinal(FinalInput{
		Contract:         contract,
		Task:             task,
		FinalText:        "the model gave up without calling tools",
		UnavailableTools: map[string]string{"terminal.run": "denied by profile"},
		AgentRegistry:    agentcore.NewToolRegistry(),
		FullRegistry:     full,
	})
	if classification.State != "failed" {
		t.Fatalf("expected failed, got %#v", classification)
	}
	if classification.BlockerKind != completionBlockerUnavailableTool {
		t.Fatalf("expected unavailable_tool blocker, got %q", classification.BlockerKind)
	}
	if !strings.Contains(classification.BlockerText, "denied by profile") {
		t.Fatalf("expected blocker text to explain reason, got %q", classification.BlockerText)
	}
	if got := classification.UnavailableTools["terminal.run"]; got != "denied by profile" {
		t.Fatalf("expected UnavailableTools to map terminal.run -> denied by profile, got %q", got)
	}
}

func TestEvaluateFinalNarrowsToFollowupLimit(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"file.edit"},
		Summary:       "edit file",
	}
	task := session.TaskNode{ID: "task-1", Status: "running"}
	classification := EvaluateFinal(FinalInput{
		Contract:      contract,
		Task:          task,
		FinalText:     "I give up.",
		FollowupCount: 2,
		MaxFollowups:  2,
	})
	if classification.State != "failed" {
		t.Fatalf("expected failed, got %#v", classification)
	}
	if classification.BlockerKind != completionBlockerFollowupLimit {
		t.Fatalf("expected followup_limit blocker, got %q", classification.BlockerKind)
	}
	if !strings.Contains(classification.BlockerReason, "file.edit") {
		t.Fatalf("expected blocker reason to name the tool, got %q", classification.BlockerReason)
	}
}

func TestEvaluateFinalInputRequest(t *testing.T) {
	contract := session.TaskContract{RequiresTools: false}
	task := session.TaskNode{ID: "task-1", Status: "running"}
	classification := EvaluateFinal(FinalInput{
		Contract:  contract,
		Task:      task,
		FinalText: "I need you to authorize Lark first. Please reply when authorization is complete.",
	})
	if classification.State != "await_user_input" {
		t.Fatalf("expected await_user_input, got %#v", classification)
	}
	if classification.BlockerKind != completionBlockerInputRequest {
		t.Fatalf("expected input_request blocker, got %q", classification.BlockerKind)
	}
}

func TestEvaluateFinalUnexecutedCommitment(t *testing.T) {
	contract := session.TaskContract{RequiresTools: true, RequiredTools: []string{"file.read"}}
	task := session.TaskNode{ID: "task-1", Status: "running"}
	task.Steps = []session.TaskStep{{Tool: "file.read", Status: "accepted", Accepted: true}}
	classification := EvaluateFinal(FinalInput{
		Contract:  contract,
		Task:      task,
		FinalText: "Confirming authorization:",
	})
	if classification.State != "failed" {
		t.Fatalf("expected failed, got %#v", classification)
	}
	if classification.BlockerKind != completionBlockerUnexecutedCommitment {
		t.Fatalf("expected unexecuted_commitment blocker, got %q", classification.BlockerKind)
	}
}

func TestEvaluateFinalDoesNotAutoAuditEvidence(t *testing.T) {
	contract := session.TaskContract{RequiresTools: true, RequiredTools: []string{"web.search"}}
	task := session.TaskNode{ID: "task-1", Status: "running"}
	task.Steps = []session.TaskStep{{Tool: "web.search", Status: "accepted", Accepted: true}}
	classification := EvaluateFinal(FinalInput{
		Contract:  contract,
		Task:      task,
		FinalText: "Done. Report at https://example.com/report.md",
	})
	if classification.State != "completed" {
		t.Fatalf("expected completed when contract satisfied and final text carries deliverable, got %#v", classification)
	}
}

func TestEvaluateFinalStopReasonTakesPrecedenceOverContract(t *testing.T) {
	contract := session.TaskContract{RequiresTools: true, RequiredTools: []string{"web.search"}}
	task := session.TaskNode{ID: "task-1", Status: "running"}
	classification := EvaluateFinal(FinalInput{
		Contract:   contract,
		Task:       task,
		FinalText:  "any",
		StopReason: "max_iterations_exceeded",
	})
	if classification.BlockerKind != completionBlockerStopReason {
		t.Fatalf("expected stop_reason to take precedence, got %q", classification.BlockerKind)
	}
}

func TestEvaluateLoopEndFailureCategoriesForGuidance(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"file.edit"},
	}
	task := session.TaskNode{ID: "task-1", Status: "running"}
	results := []agentcore.ToolResult{
		{ToolCallID: "call_1", Content: "old_string not found in file", IsError: true},
	}
	calls := []agentcore.ToolCall{{ID: "call_1", Name: "file.edit"}}
	decision := EvaluateLoopEnd(LoopEndInput{
		Contract:        contract,
		Task:            task,
		UserText:        "edit file",
		TurnMessage:     agentcore.Message{Role: agentcore.RoleAssistant, Content: "Will retry."},
		TurnToolResults: results,
		TurnToolCalls:   calls,
		MaxFollowups:    4,
	})
	if !decision.ShouldFollowUp {
		t.Fatalf("expected follow-up, got %#v", decision)
	}
	if len(decision.FailureCategories) == 0 {
		t.Fatalf("expected failure categories to be populated, got %#v", decision)
	}
	if decision.FailureCategories[0] != "retryable" {
		t.Fatalf("expected retryable category for old_string not found, got %v", decision.FailureCategories)
	}
	if !strings.Contains(decision.FollowupMessage, "file.read") {
		t.Fatalf("expected follow-up to suggest file.read, got %q", decision.FollowupMessage)
	}
}

func TestMissingToolsCoveredByUnavailable(t *testing.T) {
	if missingToolsCoveredByUnavailable([]string{"plan:read"}, map[string]string{"terminal.run": "x"}) {
		t.Fatal("plan entries should not be required to be covered")
	}
	if !missingToolsCoveredByUnavailable([]string{"tool:terminal.run"}, map[string]string{"terminal.run": "denied by profile"}) {
		t.Fatal("tool entry should be covered")
	}
	if missingToolsCoveredByUnavailable([]string{"tool:terminal.run", "tool:web.search"}, map[string]string{"terminal.run": "x"}) {
		t.Fatal("every tool entry must be covered")
	}
	if missingToolsCoveredByUnavailable([]string{}, map[string]string{"terminal.run": "x"}) {
		t.Fatal("empty missing should not be considered covered")
	}
}

func TestCountContractFollowupEvents(t *testing.T) {
	task := session.TaskNode{ID: "task-1"}
	task.Execution.Events = []session.ExecutionEvent{
		{Type: "contract_followup", Summary: "contract_unsatisfied"},
		{Type: "tool_result", Tool: "file.edit", Status: "accepted"},
		{Type: "contract_followup", Summary: "contract_unsatisfied"},
	}
	if got := countContractFollowupEvents(task); got != 2 {
		t.Fatalf("expected 2 contract_followup events, got %d", got)
	}
}

func TestBuildBlockedTaskEvidenceUnavailable(t *testing.T) {
	classification := FinalClassification{
		BlockerKind:      completionBlockerUnavailableTool,
		BlockerReason:    "required tools not available: terminal.run",
		MissingEvidence:  []string{"tool:terminal.run"},
		UnavailableTools: map[string]string{"terminal.run": "denied by profile"},
	}
	evidence := buildBlockedTaskEvidence(classification, 0)
	unavailable, ok := evidence["unavailable"].(map[string]string)
	if !ok {
		t.Fatalf("expected evidence.unavailable to be map[string]string, got %T", evidence["unavailable"])
	}
	if unavailable["terminal.run"] != "denied by profile" {
		t.Fatalf("expected terminal.run -> denied by profile, got %#v", unavailable)
	}
	if evidence["attempts_total"] != nil {
		t.Fatalf("expected no attempts_total for non-followup blocker, got %v", evidence["attempts_total"])
	}
}

func TestBuildBlockedTaskEvidenceFollowupLimit(t *testing.T) {
	classification := FinalClassification{
		BlockerKind:     completionBlockerFollowupLimit,
		BlockerReason:   "followup limit reached",
		MissingEvidence: []string{"tool:file.edit"},
	}
	evidence := buildBlockedTaskEvidence(classification, 3)
	if evidence["attempts_total"] != 3 {
		t.Fatalf("expected attempts_total=3 for followup_limit, got %v", evidence["attempts_total"])
	}
	if _, ok := evidence["unavailable"].(map[string]string); ok {
		t.Fatalf("expected no unavailable evidence for followup_limit, got %#v", evidence["unavailable"])
	}
	if evidence["blocker_kind"] != completionBlockerFollowupLimit {
		t.Fatalf("expected blocker_kind=followup_limit, got %v", evidence["blocker_kind"])
	}
}

func TestLoopEndDecisionDoesNotCarryLoopBlockerPointer(t *testing.T) {
	// LoopEndDecision is the value returned by EvaluateLoopEnd; it must not
	// hold a pointer to runtime-internal state. This guards against
	// re-introducing the hook->runtime backchannel.
	satisfiedTask := session.TaskNode{ID: "task-1"}
	satisfiedTask.Steps = []session.TaskStep{{Tool: "web.search", Status: "accepted", Accepted: true}}
	decision := EvaluateLoopEnd(LoopEndInput{
		Contract:     session.TaskContract{RequiresTools: true, RequiredTools: []string{"web.search"}},
		Task:         satisfiedTask,
		UserText:     "search",
		TurnMessage:  agentcore.Message{Role: agentcore.RoleAssistant, Content: "Weather clear."},
		MaxFollowups: 4,
	})
	if !decision.ContractSatisfied {
		t.Fatalf("expected contract satisfied, got %#v", decision)
	}
	if decision.StopLoopNow {
		t.Fatalf("expected loop to end naturally, got %#v", decision)
	}
	// Re-using the LoopEndDecision in another call must not mutate prior
	// callers; the evaluator is pure and stateless.
	decision2 := EvaluateLoopEnd(LoopEndInput{
		Contract:         session.TaskContract{RequiresTools: true, RequiredTools: []string{"missing.tool"}},
		Task:             session.TaskNode{ID: "task-1"},
		UserText:         "search",
		TurnMessage:      agentcore.Message{Role: agentcore.RoleAssistant, Content: "I will run."},
		UnavailableTools: map[string]string{"missing.tool": "tool not registered"},
		MaxFollowups:     4,
		AgentRegistry:    agentcore.NewToolRegistry(),
		FullRegistry:     agentcore.NewToolRegistry(),
	})
	if decision2.BlockerKind != completionBlockerUnavailableTool {
		t.Fatalf("expected second call to identify unavailable_tool, got %#v", decision2)
	}
	if !decision.ContractSatisfied {
		t.Fatalf("expected first decision to remain unchanged, got %#v", decision)
	}
}
