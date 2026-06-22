package session

import (
	"strings"
	"testing"
)

func TestVerifyNode_ToolNode_Passed(t *testing.T) {
	node := &TaskGraphNode{
		ID:            "read",
		Type:          NodeTypeTool,
		Goal:          "read file",
		Status:        NodeStatusRunning,
		Executor:      "file.read",
		ResultSummary: "file contents: hello world",
		EvidenceRefs:  []EvidenceRef{{Kind: "tool", ToolName: "file.read", Summary: "read ok"}},
	}
	result := VerifyNode(node)
	if result.Status != VerificationPassed {
		t.Fatalf("expected passed, got %q: %s", result.Status, result.Reason)
	}
}

func TestVerifyNode_ToolNode_WithCriteria_Passed(t *testing.T) {
	node := &TaskGraphNode{
		ID:            "read",
		Type:          NodeTypeTool,
		Goal:          "read file",
		Status:        NodeStatusRunning,
		Executor:      "file.read",
		ResultSummary: "found the answer",
		EvidenceRefs:  []EvidenceRef{{Kind: "tool", ToolName: "file.read", Summary: "read ok"}},
		Acceptance:    Acceptance{Criteria: "must contain the answer"},
	}
	result := VerifyNode(node)
	if result.Status != VerificationPassed {
		t.Fatalf("expected passed with criteria, got %q: %s", result.Status, result.Reason)
	}
}

func TestVerifyNode_ToolNode_NoEvidence(t *testing.T) {
	node := &TaskGraphNode{
		ID:            "read",
		Type:          NodeTypeTool,
		Goal:          "read file",
		Status:        NodeStatusRunning,
		Executor:      "file.read",
		ResultSummary: "something",
	}
	result := VerifyNode(node)
	if result.Status != VerificationFailed {
		t.Fatalf("expected failed for no evidence, got %q", result.Status)
	}
	if !strings.Contains(result.Reason, "no evidence") {
		t.Fatalf("reason should mention evidence, got %q", result.Reason)
	}
}

func TestVerifyNode_ToolNode_FailureReason(t *testing.T) {
	node := &TaskGraphNode{
		ID:            "read",
		Type:          NodeTypeTool,
		Goal:          "read file",
		Status:        NodeStatusFailed,
		Executor:      "file.read",
		FailureReason: "file not found",
	}
	result := VerifyNode(node)
	if result.Status != VerificationFailed {
		t.Fatalf("expected failed for failure reason, got %q", result.Status)
	}
	if !strings.Contains(result.Reason, "file not found") {
		t.Fatalf("reason should contain failure, got %q", result.Reason)
	}
}

func TestVerifyNode_ToolNode_TimedOut(t *testing.T) {
	node := &TaskGraphNode{
		ID:            "read",
		Type:          NodeTypeTool,
		Goal:          "read file",
		Status:        NodeStatusFailed,
		Executor:      "file.read",
		FailureReason: "tool timed out after 30s",
	}
	result := VerifyNode(node)
	if result.Status != VerificationRetry {
		t.Fatalf("expected retry for timeout, got %q", result.Status)
	}
	if !result.Retryable {
		t.Fatal("expected timeout to be retryable")
	}
	if len(result.Missing) == 0 || !strings.Contains(result.Missing[0], "deadline") {
		t.Fatalf("expected deadline missing entry, got %v", result.Missing)
	}
}

func TestVerifyNode_ToolNode_Blocked(t *testing.T) {
	node := &TaskGraphNode{
		ID:            "read",
		Type:          NodeTypeTool,
		Goal:          "read file",
		Status:        NodeStatusBlocked,
		Executor:      "file.read",
		FailureReason: "tool blocked by policy",
	}
	result := VerifyNode(node)
	if result.Status != VerificationBlocked {
		t.Fatalf("expected blocked, got %q", result.Status)
	}
}

func TestVerifyNode_ToolNode_AwaitingInput(t *testing.T) {
	node := &TaskGraphNode{
		ID:     "review",
		Type:   NodeTypeTool,
		Goal:   "review",
		Status: NodeStatusAwaitingInput,
	}
	result := VerifyNode(node)
	if result.Status != VerificationNeedsInput {
		t.Fatalf("expected needs_input, got %q", result.Status)
	}
}

func TestVerifyNode_ToolNode_CriteriaBlocked(t *testing.T) {
	node := &TaskGraphNode{
		ID:           "read",
		Type:         NodeTypeTool,
		Goal:         "read file",
		Status:       NodeStatusRunning,
		Executor:     "file.read",
		EvidenceRefs: []EvidenceRef{{Kind: "tool", ToolName: "file.read", Summary: "read ok"}},
		Acceptance:   Acceptance{Criteria: "must contain specific data"},
	}
	result := VerifyNode(node)
	if result.Status != VerificationPassed {
		t.Fatalf("execution hard checks pass (evidence present), criteria handled by model verifier, got %q", result.Status)
	}
}

func TestVerifyNode_ModelNode_Passed(t *testing.T) {
	node := &TaskGraphNode{
		ID:            "answer",
		Type:          NodeTypeModel,
		Goal:          "answer the question",
		Status:        NodeStatusRunning,
		ResultSummary: "The answer is 42.",
	}
	result := VerifyNode(node)
	if result.Status != VerificationPassed {
		t.Fatalf("expected passed, got %q: %s", result.Status, result.Reason)
	}
}

func TestVerifyNode_ModelNode_RequiresConcreteURLRejectsPlaceholder(t *testing.T) {
	node := &TaskGraphNode{
		ID:            "publish",
		Type:          NodeTypeSkill,
		Mode:          NodeModeSkill,
		Goal:          "publish document and return URL",
		Status:        NodeStatusCompleted,
		ResultSummary: "[open document](feishu_doc_url)",
		Output:        map[string]any{"url": ""},
		Acceptance:    Acceptance{Criteria: "must return a link"},
	}
	result := VerifyNode(node)
	if result.Status != VerificationFailed {
		t.Fatalf("expected failed for placeholder URL, got %#v", result)
	}
	if !strings.Contains(result.Reason, "concrete URL") {
		t.Fatalf("unexpected reason: %q", result.Reason)
	}
}

func TestVerifyNode_ModelNode_RequestingConfirmationBeforeMutationNeedsInput(t *testing.T) {
	node := &TaskGraphNode{
		ID:            "publish",
		Type:          NodeTypeSkill,
		Mode:          NodeModeSkill,
		Goal:          "publish document and return URL",
		Status:        NodeStatusCompleted,
		ResultSummary: "Dry-run passed. Need human_confirm approval before create mutation.",
		Output:        map[string]any{"url": true},
		Acceptance:    Acceptance{Criteria: "must return a link"},
	}
	result := VerifyNode(node)
	if result.Status != VerificationNeedsInput {
		t.Fatalf("expected needs_input, got %#v", result)
	}
	if !result.RequiresHumanConfirmation {
		t.Fatalf("expected structured human confirmation requirement, got %#v", result)
	}
	if !strings.Contains(result.Reason, "confirmation") {
		t.Fatalf("unexpected reason: %q", result.Reason)
	}
	ApplyNodeVerification(node, result)
	if node.Input["requires_human_confirmation"] != true {
		t.Fatalf("expected node input confirmation gate, got %#v", node.Input)
	}
}

func TestVerifyNode_ModelNode_RequiresConcreteURLAcceptsHTTPURL(t *testing.T) {
	node := &TaskGraphNode{
		ID:            "publish",
		Type:          NodeTypeSkill,
		Mode:          NodeModeSkill,
		Goal:          "publish document and return URL",
		Status:        NodeStatusCompleted,
		ResultSummary: "created https://sample.feishu.cn/docx/abc123",
		Output:        map[string]any{"url": "https://sample.feishu.cn/docx/abc123"},
		Acceptance:    Acceptance{Criteria: "must return a link"},
	}
	result := VerifyNode(node)
	if result.Status != VerificationPassed {
		t.Fatalf("expected passed for concrete URL, got %#v", result)
	}
}

func TestVerifyNode_ModelNode_UnfinishedToolCallTextFails(t *testing.T) {
	node := &TaskGraphNode{
		ID:            "answer",
		Type:          NodeTypeSubtask,
		Goal:          "compose final answer",
		Status:        NodeStatusCompleted,
		ResultSummary: `I will call web.search to gather the data. <tool_call>web.search("query")`,
		Acceptance:    Acceptance{Criteria: "final answer produced"},
	}
	result := VerifyNode(node)
	if result.Status != VerificationFailed {
		t.Fatalf("expected failed for unfinished tool-call text, got %q: %s", result.Status, result.Reason)
	}
	if !result.Retryable {
		t.Fatal("unfinished output should be retryable")
	}
	if !strings.Contains(result.Reason, "unfinished") {
		t.Fatalf("reason should mention unfinished output, got %q", result.Reason)
	}
}

func TestVerifyNode_ModelNode_FailedToolEvidenceOnlyFails(t *testing.T) {
	node := &TaskGraphNode{
		ID:            "verify-script",
		Type:          NodeTypeSubtask,
		Goal:          "verify script",
		Status:        NodeStatusCompleted,
		ResultSummary: "destructive terminal command is blocked",
		EvidenceRefs: []EvidenceRef{
			{Kind: "tool", ToolName: "terminal.run", Summary: "destructive terminal command is blocked", IsError: true},
		},
	}
	result := VerifyNode(node)
	if result.Status != VerificationFailed {
		t.Fatalf("expected failed for failed tool evidence only, got %q: %s", result.Status, result.Reason)
	}
	if !result.Retryable {
		t.Fatal("failed tool evidence should be retryable")
	}
	if result.Confidence != "hard" {
		t.Fatalf("failed tool evidence should be hard verifier result, got %q", result.Confidence)
	}
}

func TestVerifyNode_ModelNode_FailedToolEvidenceWithExplanationStillFails(t *testing.T) {
	node := &TaskGraphNode{
		ID:            "create-script",
		Type:          NodeTypeSubtask,
		Goal:          "create script",
		Status:        NodeStatusCompleted,
		ResultSummary: "The requested file could not be created because the path policy rejected the write. No partial file was created.",
		EvidenceRefs: []EvidenceRef{
			{Kind: "tool", ToolName: "file.write", Summary: "path is outside allowed roots", IsError: true},
		},
	}
	result := VerifyNode(node)
	if result.Status != VerificationFailed {
		t.Fatalf("expected failed for failed tool evidence despite explanatory text, got %q: %s", result.Status, result.Reason)
	}
	if result.Confidence != "hard" {
		t.Fatalf("expected hard verifier result, got %q", result.Confidence)
	}
}

func TestVerifyNode_ModelNode_BlockedToolEvidenceBlocks(t *testing.T) {
	node := &TaskGraphNode{
		ID:            "create-script",
		Type:          NodeTypeSubtask,
		Goal:          "create script",
		Status:        NodeStatusCompleted,
		ResultSummary: "The requested path is blocked by policy.",
		EvidenceRefs: []EvidenceRef{
			{Kind: "tool", ToolName: "terminal.run", Summary: "path is outside allowed roots", IsError: true, Blocked: true},
		},
	}
	result := VerifyNode(node)
	if result.Status != VerificationBlocked {
		t.Fatalf("expected blocked for blocked tool evidence, got %q: %s", result.Status, result.Reason)
	}
	if result.Confidence != "hard" {
		t.Fatalf("expected hard verifier result, got %q", result.Confidence)
	}
}

func TestVerifyNode_ModelNode_FailedThenSuccessfulToolEvidencePasses(t *testing.T) {
	node := &TaskGraphNode{
		ID:            "verify-script",
		Type:          NodeTypeSubtask,
		Goal:          "verify script",
		Status:        NodeStatusCompleted,
		ResultSummary: "script verified successfully",
		EvidenceRefs: []EvidenceRef{
			{Kind: "tool", ToolName: "terminal.run", Summary: "first attempt timed out", IsError: true},
			{Kind: "tool", ToolName: "terminal.run", Summary: "script verified successfully", IsError: false},
		},
	}
	result := VerifyNode(node)
	if result.Status != VerificationPassed {
		t.Fatalf("expected passed after successful tool evidence, got %q: %s", result.Status, result.Reason)
	}
}

func TestVerifyNode_ModelNode_NoOutput(t *testing.T) {
	node := &TaskGraphNode{
		ID:     "answer",
		Type:   NodeTypeModel,
		Goal:   "answer the question",
		Status: NodeStatusFailed,
	}
	result := VerifyNode(node)
	if result.Status != VerificationFailed {
		t.Fatalf("expected failed for no output, got %q", result.Status)
	}
	if !strings.Contains(result.Reason, "no output") {
		t.Fatalf("reason should mention output, got %q", result.Reason)
	}
}

func TestVerifyNode_ModelNode_FailureReason(t *testing.T) {
	node := &TaskGraphNode{
		ID:            "answer",
		Type:          NodeTypeModel,
		Goal:          "answer",
		Status:        NodeStatusFailed,
		FailureReason: "model timeout",
	}
	result := VerifyNode(node)
	if result.Status != VerificationFailed {
		t.Fatalf("expected failed for model error, got %q", result.Status)
	}
}

func TestVerifyNode_ModelNode_CriteriaBlocked(t *testing.T) {
	node := &TaskGraphNode{
		ID:            "answer",
		Type:          NodeTypeModel,
		Goal:          "answer",
		Status:        NodeStatusRunning,
		ResultSummary: "x",
		Acceptance:    Acceptance{Criteria: "must be a complete sentence"},
	}
	result := VerifyNode(node)
	if result.Status != VerificationPassed {
		t.Fatalf("execution hard checks pass (has output), criteria handled by model verifier, got %q", result.Status)
	}
}

func TestVerifyNode_SkillNode_Passed(t *testing.T) {
	node := &TaskGraphNode{
		ID:            "skill",
		Type:          NodeTypeSkill,
		Goal:          "run skill",
		Status:        NodeStatusRunning,
		ResultSummary: "skill completed successfully",
	}
	result := VerifyNode(node)
	if result.Status != VerificationPassed {
		t.Fatalf("expected passed, got %q: %s", result.Status, result.Reason)
	}
}

func TestVerifyNode_HumanNode_AwaitingInput(t *testing.T) {
	node := &TaskGraphNode{
		ID:     "review",
		Type:   NodeTypeHumanReview,
		Goal:   "review deployment",
		Status: NodeStatusAwaitingInput,
	}
	result := VerifyNode(node)
	if result.Status != VerificationNeedsInput {
		t.Fatalf("expected needs_input, got %q", result.Status)
	}
}

func TestVerifyNode_HumanNode_Completed(t *testing.T) {
	node := &TaskGraphNode{
		ID:     "review",
		Type:   NodeTypeHumanReview,
		Goal:   "review deployment",
		Status: NodeStatusCompleted,
	}
	result := VerifyNode(node)
	if result.Status != VerificationPassed {
		t.Fatalf("expected passed for completed human node, got %q", result.Status)
	}
}

func TestVerifyNode_HumanConfirm_Completed(t *testing.T) {
	node := &TaskGraphNode{
		ID:     "confirm",
		Type:   NodeTypeHumanConfirm,
		Goal:   "confirm deployment",
		Status: NodeStatusCompleted,
	}
	result := VerifyNode(node)
	if result.Status != VerificationPassed {
		t.Fatalf("expected passed for completed human confirm, got %q", result.Status)
	}
}

func TestVerifyNode_HumanNode_Failed(t *testing.T) {
	node := &TaskGraphNode{
		ID:            "review",
		Type:          NodeTypeHumanReview,
		Goal:          "review deployment",
		Status:        NodeStatusFailed,
		FailureReason: "user rejected",
	}
	result := VerifyNode(node)
	if result.Status != VerificationFailed {
		t.Fatalf("expected failed for rejected human node, got %q", result.Status)
	}
}

func TestVerifyNode_NilNode(t *testing.T) {
	result := VerifyNode(nil)
	if result.Status != VerificationFailed {
		t.Fatalf("expected failed for nil node, got %q", result.Status)
	}
}

func TestVerifyNode_UnknownType(t *testing.T) {
	node := &TaskGraphNode{
		ID:     "bad",
		Type:   "unknown_type",
		Goal:   "x",
		Status: NodeStatusRunning,
	}
	result := VerifyNode(node)
	if result.Status != VerificationFailed {
		t.Fatalf("expected failed for unknown type, got %q", result.Status)
	}
}

func TestApplyNodeVerification_Passed(t *testing.T) {
	node := &TaskGraphNode{
		ID:     "n1",
		Type:   NodeTypeModel,
		Goal:   "answer",
		Status: NodeStatusRunning,
	}
	ApplyNodeVerification(node, NodeVerificationResult{Status: VerificationPassed, Reason: "looks good"})
	if node.Status != NodeStatusCompleted {
		t.Fatalf("expected completed, got %q", node.Status)
	}
	if !node.Acceptance.Verified {
		t.Fatal("expected verified=true")
	}
	if node.Acceptance.Reason != "looks good" {
		t.Fatalf("expected reason, got %q", node.Acceptance.Reason)
	}
	if node.VerifiedAt.IsZero() {
		t.Fatal("expected VerifiedAt to be set")
	}
}

func TestApplyNodeVerification_Failed(t *testing.T) {
	node := &TaskGraphNode{
		ID:     "n1",
		Type:   NodeTypeModel,
		Goal:   "answer",
		Status: NodeStatusRunning,
	}
	ApplyNodeVerification(node, NodeVerificationResult{Status: VerificationFailed, Reason: "no output"})
	if node.Status != NodeStatusFailed {
		t.Fatalf("expected failed, got %q", node.Status)
	}
	if node.FailureReason != "no output" {
		t.Fatalf("expected failure reason, got %q", node.FailureReason)
	}
}

func TestApplyNodeVerification_Blocked(t *testing.T) {
	node := &TaskGraphNode{
		ID:     "n1",
		Type:   NodeTypeModel,
		Goal:   "answer",
		Status: NodeStatusRunning,
	}
	ApplyNodeVerification(node, NodeVerificationResult{Status: VerificationBlocked, Reason: "missing evidence"})
	if node.Status != NodeStatusBlocked {
		t.Fatalf("expected blocked, got %q", node.Status)
	}
	if node.FailureReason != "missing evidence" {
		t.Fatalf("expected failure reason, got %q", node.FailureReason)
	}
}

func TestApplyNodeVerification_NeedsInput(t *testing.T) {
	node := &TaskGraphNode{
		ID:     "n1",
		Type:   NodeTypeModel,
		Goal:   "answer",
		Status: NodeStatusRunning,
	}
	ApplyNodeVerification(node, NodeVerificationResult{Status: VerificationNeedsInput})
	if node.Status != NodeStatusAwaitingInput {
		t.Fatalf("expected awaiting_input, got %q", node.Status)
	}
}

func TestApplyNodeVerification_PreservesSkipped(t *testing.T) {
	node := &TaskGraphNode{
		ID:     "n1",
		Type:   NodeTypeModel,
		Goal:   "answer",
		Status: NodeStatusSkipped,
	}
	ApplyNodeVerification(node, NodeVerificationResult{Status: VerificationPassed})
	if node.Status != NodeStatusSkipped {
		t.Fatalf("expected skipped to be preserved, got %q", node.Status)
	}
}

func TestApplyNodeVerification_NilNode(t *testing.T) {
	ApplyNodeVerification(nil, NodeVerificationResult{Status: VerificationPassed})
}

func TestApplyNodeVerification_DoesNotOverwriteReason(t *testing.T) {
	node := &TaskGraphNode{
		ID:     "n1",
		Type:   NodeTypeTool,
		Goal:   "read",
		Status: NodeStatusRunning,
		Acceptance: Acceptance{
			Criteria: "must work",
			Reason:   "already reviewed manually",
		},
	}
	ApplyNodeVerification(node, NodeVerificationResult{Status: VerificationPassed, Reason: "also good"})
	if node.Acceptance.Reason != "already reviewed manually" {
		t.Fatalf("expected existing reason preserved, got %q", node.Acceptance.Reason)
	}
}

func TestVerifyTaskGraph_AllPassed(t *testing.T) {
	g := &TaskGraph{
		ID:     "g1",
		TaskID: "t1",
		Nodes: []TaskGraphNode{
			{ID: "a", Type: NodeTypeTool, Goal: "read", Status: NodeStatusRunning, Executor: "file.read", ResultSummary: "ok", EvidenceRefs: []EvidenceRef{{Kind: "tool"}}},
			{ID: "b", Type: NodeTypeModel, Goal: "analyze", Status: NodeStatusRunning, ResultSummary: "analysis done"},
		},
	}
	result := VerifyTaskGraph(g)
	if result.Status != GraphStatusCompleted {
		t.Fatalf("expected completed, got %q", result.Status)
	}
	if len(result.NodeResults) != 2 {
		t.Fatalf("expected 2 node results, got %d", len(result.NodeResults))
	}
}

func TestVerifyTaskGraph_MixedResults(t *testing.T) {
	g := &TaskGraph{
		ID:     "g2",
		TaskID: "t2",
		Nodes: []TaskGraphNode{
			{ID: "a", Type: NodeTypeTool, Goal: "read", Status: NodeStatusRunning, Executor: "file.read", ResultSummary: "ok", EvidenceRefs: []EvidenceRef{{Kind: "tool"}}},
			{ID: "b", Type: NodeTypeModel, Goal: "analyze", Status: NodeStatusFailed, FailureReason: "model error"},
		},
	}
	result := VerifyTaskGraph(g)
	if result.Status != GraphStatusFailed {
		t.Fatalf("expected failed, got %q", result.Status)
	}
	if len(result.MissingNodes) != 1 || result.MissingNodes[0] != "b" {
		t.Fatalf("expected missing node b, got %v", result.MissingNodes)
	}
}

func TestVerifyTaskGraph_Blocked(t *testing.T) {
	g := &TaskGraph{
		ID:     "g3",
		TaskID: "t3",
		Nodes: []TaskGraphNode{
			{ID: "a", Type: NodeTypeTool, Goal: "read", Status: NodeStatusBlocked, Executor: "file.read", FailureReason: "tool blocked by policy"},
		},
	}
	result := VerifyTaskGraph(g)
	if result.Status != GraphStatusBlocked {
		t.Fatalf("expected blocked, got %q", result.Status)
	}
}

func TestVerifyTaskGraph_AwaitingInput(t *testing.T) {
	g := &TaskGraph{
		ID:     "g4",
		TaskID: "t4",
		Nodes: []TaskGraphNode{
			{ID: "a", Type: NodeTypeTool, Goal: "read", Status: NodeStatusRunning, Executor: "file.read", ResultSummary: "ok", EvidenceRefs: []EvidenceRef{{Kind: "tool"}}},
			{ID: "b", Type: NodeTypeHumanReview, Goal: "review", Status: NodeStatusAwaitingInput},
		},
	}
	result := VerifyTaskGraph(g)
	if result.Status != GraphStatusAwaitingInput {
		t.Fatalf("expected awaiting_input, got %q", result.Status)
	}
}

func TestVerifyTaskGraph_PendingAndRunning(t *testing.T) {
	g := &TaskGraph{
		ID:     "g5",
		TaskID: "t5",
		Nodes: []TaskGraphNode{
			{ID: "a", Type: NodeTypeTool, Goal: "read", Status: NodeStatusRunning, Executor: "file.read", ResultSummary: "ok", EvidenceRefs: []EvidenceRef{{Kind: "tool"}}},
			{ID: "b", Type: NodeTypeModel, Goal: "analyze", Status: NodeStatusPending},
		},
	}
	result := VerifyTaskGraph(g)
	if result.Status != GraphStatusRunning {
		t.Fatalf("expected running (pending model node), got %q", result.Status)
	}
}

func TestVerifyTaskGraph_BlockedNodeTakesPriorityOverPendingDownstream(t *testing.T) {
	g := &TaskGraph{
		ID:     "g-blocked-pending",
		TaskID: "t-blocked-pending",
		Nodes: []TaskGraphNode{
			{ID: "write", Type: NodeTypeTool, Goal: "write file", Status: NodeStatusBlocked, Executor: "file.write", FailureReason: "policy denied"},
			{ID: "summarize", Type: NodeTypeModel, Goal: "summarize", Status: NodeStatusPending, Depends: []string{"write"}},
		},
	}
	result := VerifyTaskGraph(g)
	if result.Status != GraphStatusBlocked {
		t.Fatalf("expected blocked to take priority over pending downstream, got %q", result.Status)
	}
}

func TestVerifyNode_PendingNode_ReturnsPending(t *testing.T) {
	node := &TaskGraphNode{
		ID:     "n1",
		Type:   NodeTypeModel,
		Goal:   "answer",
		Status: NodeStatusPending,
	}
	result := VerifyNode(node)
	if result.Status != VerificationPending {
		t.Fatalf("expected pending, got %q", result.Status)
	}
}

func TestVerifyNode_RunningNode_ReturnsPending(t *testing.T) {
	node := &TaskGraphNode{
		ID:     "n1",
		Type:   NodeTypeModel,
		Goal:   "answer",
		Status: NodeStatusRunning,
	}
	result := VerifyNode(node)
	if result.Status != VerificationPending {
		t.Fatalf("expected pending, got %q", result.Status)
	}
}

func TestVerifyNode_SkippedNode_ReturnsPassed(t *testing.T) {
	node := &TaskGraphNode{
		ID:     "n1",
		Type:   NodeTypeModel,
		Goal:   "answer",
		Status: NodeStatusSkipped,
	}
	result := VerifyNode(node)
	if result.Status != VerificationPassed {
		t.Fatalf("expected passed for skipped, got %q", result.Status)
	}
}

func TestVerifyNode_ToolNode_CriteriaMismatch(t *testing.T) {
	node := &TaskGraphNode{
		ID:            "read",
		Type:          NodeTypeTool,
		Goal:          "read README",
		Status:        NodeStatusRunning,
		Executor:      "file.read",
		ResultSummary: "some unrelated text",
		EvidenceRefs:  []EvidenceRef{{Kind: "tool", ToolName: "file.read", Summary: "read ok"}},
		Acceptance:    Acceptance{Criteria: "must contain version number"},
	}
	result := VerifyNode(node)
	if result.Status != VerificationPassed {
		t.Fatalf("execution hard checks pass (evidence + summary), model verifier handles criteria semantics, got %q", result.Status)
	}
}

func TestVerifyNode_ModelNode_CriteriaMismatch(t *testing.T) {
	node := &TaskGraphNode{
		ID:            "answer",
		Type:          NodeTypeModel,
		Goal:          "analyze data",
		Status:        NodeStatusRunning,
		ResultSummary: "not the analysis you asked for",
		Acceptance:    Acceptance{Criteria: "output must contain specific conclusion"},
	}
	result := VerifyNode(node)
	if result.Status != VerificationPassed {
		t.Fatalf("execution hard checks pass (has output), model verifier handles criteria semantics, got %q", result.Status)
	}
}

func TestVerifyNode_ToolNode_CriteriaMatched(t *testing.T) {
	node := &TaskGraphNode{
		ID:            "read",
		Type:          NodeTypeTool,
		Goal:          "read config",
		Status:        NodeStatusRunning,
		Executor:      "file.read",
		ResultSummary: "config file contains version 2.0 and port 8080",
		EvidenceRefs:  []EvidenceRef{{Kind: "tool", ToolName: "file.read", Summary: "read ok"}},
		Acceptance:    Acceptance{Criteria: "must contain version"},
	}
	result := VerifyNode(node)
	if result.Status != VerificationPassed {
		t.Fatalf("expected passed, got %q: %s", result.Status, result.Reason)
	}
}

func TestVerifyNode_ToolNode_CriteriaNotVerified(t *testing.T) {
	node := &TaskGraphNode{
		ID:            "read",
		Type:          NodeTypeTool,
		Goal:          "read config",
		Status:        NodeStatusCompleted,
		Executor:      "file.read",
		ResultSummary: "some output",
		EvidenceRefs:  []EvidenceRef{{Kind: "tool", ToolName: "file.read", Summary: "read ok"}},
		Acceptance:    Acceptance{Criteria: "must contain something", Verified: false},
	}
	result := VerifyNode(node)
	if result.Status != VerificationPassed {
		t.Fatalf("execution hard checks pass (evidence + output), Verified enforcement is at task-level, got %q", result.Status)
	}
}

func TestVerifyNode_ModelNode_CriteriaNotVerified(t *testing.T) {
	node := &TaskGraphNode{
		ID:            "answer",
		Type:          NodeTypeModel,
		Goal:          "answer",
		Status:        NodeStatusCompleted,
		ResultSummary: "some answer",
		Acceptance:    Acceptance{Criteria: "must be correct", Verified: false},
	}
	result := VerifyNode(node)
	if result.Status != VerificationPassed {
		t.Fatalf("execution hard checks pass (has output), Verified enforcement is at task-level, got %q", result.Status)
	}
}

func TestVerifyTaskGraph_WithContract_UnsatisfiedDoesNotOverrideGraph(t *testing.T) {
	g := &TaskGraph{
		ID:     "g1",
		TaskID: "t1",
		Nodes: []TaskGraphNode{
			{ID: "a", Type: NodeTypeModel, Goal: "think", Status: NodeStatusRunning, ResultSummary: "done"},
		},
	}
	contract := &TaskContract{
		RequiredTools:    []string{"file.read"},
		RequiredEvidence: []TaskEvidenceContract{{Kind: "tool", Tool: "file.read", Description: "read file"}},
	}
	result := VerifyTaskGraphWithContract(g, contract)
	if result.Status != GraphStatusCompleted {
		t.Fatalf("graph-native verification should not be failed by legacy contract, got %q: %s", result.Status, result.Reason)
	}
	if len(result.MissingNodes) != 0 {
		t.Fatalf("legacy contract gaps should not be surfaced as missing graph nodes, got %v", result.MissingNodes)
	}
}

func TestVerifyTaskGraph_WithContract_Satisfied(t *testing.T) {
	g := &TaskGraph{
		ID:     "g2",
		TaskID: "t2",
		Nodes: []TaskGraphNode{
			{ID: "read", Type: NodeTypeTool, Goal: "read", Status: NodeStatusCompleted, Executor: "file.read", ResultSummary: "ok", EvidenceRefs: []EvidenceRef{{Kind: "tool"}}},
			{ID: "analyze", Type: NodeTypeModel, Goal: "analyze", Status: NodeStatusCompleted, ResultSummary: "analysis done"},
		},
	}
	contract := &TaskContract{
		RequiredTools: []string{"file.read"},
	}
	result := VerifyTaskGraphWithContract(g, contract)
	if result.Status != GraphStatusCompleted {
		t.Fatalf("expected completed for satisfied contract, got %q: %s", result.Status, result.Reason)
	}
}

func TestVerifyTaskGraph_WithContract_NilContract(t *testing.T) {
	g := &TaskGraph{
		ID:     "g3",
		TaskID: "t3",
		Nodes: []TaskGraphNode{
			{ID: "a", Type: NodeTypeModel, Goal: "x", Status: NodeStatusRunning, ResultSummary: "done"},
		},
	}
	result := VerifyTaskGraphWithContract(g, nil)
	if result.Status != GraphStatusCompleted {
		t.Fatalf("expected completed with nil contract, got %q", result.Status)
	}
}

func TestVerifyTaskGraph_CriteriaNodeNotVerified(t *testing.T) {
	g := &TaskGraph{
		ID:     "g-criteria",
		TaskID: "t-criteria",
		Nodes: []TaskGraphNode{
			{
				ID:            "a",
				Type:          NodeTypeTool,
				Goal:          "read file",
				Status:        NodeStatusCompleted,
				Executor:      "file.read",
				ResultSummary: "file contents",
				EvidenceRefs:  []EvidenceRef{{Kind: "tool", ToolName: "file.read", Summary: "read ok"}},
				Acceptance:    Acceptance{Criteria: "must be verified", Verified: false},
			},
		},
	}
	result := VerifyTaskGraphWithContract(g, nil)
	if result.Status != GraphStatusBlocked {
		t.Fatalf("expected blocked for unverified criteria node, got %q", result.Status)
	}
	if len(result.NodeResults) != 1 {
		t.Fatalf("expected 1 node result, got %d", len(result.NodeResults))
	}
}

func TestVerifyTaskGraph_EmptyGraph(t *testing.T) {
	g := &TaskGraph{
		ID:     "g6",
		TaskID: "t6",
		Nodes:  []TaskGraphNode{},
	}
	result := VerifyTaskGraph(g)
	if result.Status != GraphStatusCompleted {
		t.Fatalf("expected completed for empty graph, got %q", result.Status)
	}
}
