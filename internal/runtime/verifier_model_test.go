package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/session"
)

func TestRenderModelVerifierPrompt(t *testing.T) {
	node := &session.TaskGraphNode{
		ID:            "n1",
		Type:          session.NodeTypeModel,
		Goal:          "analyze data",
		ResultSummary: "The data shows a 15% increase.",
		Acceptance:    session.Acceptance{Criteria: "must mention percentage"},
		EvidenceRefs:  []session.EvidenceRef{{ToolName: "file.read", Summary: "read 100 rows"}},
	}
	prompt := renderModelVerifierPrompt(node)
	if !strings.Contains(prompt, "analyze data") {
		t.Fatal("prompt missing goal")
	}
	if !strings.Contains(prompt, "must mention percentage") {
		t.Fatal("prompt missing criteria")
	}
	if !strings.Contains(prompt, "The data shows") {
		t.Fatal("prompt missing result summary")
	}
	if !strings.Contains(prompt, "file.read") {
		t.Fatal("prompt missing evidence")
	}
}

func TestParseModelVerifierOutput_Passed(t *testing.T) {
	raw := `{"status":"passed","reason":"output contains the required percentage","confidence":"high"}`
	node := &session.TaskGraphNode{ID: "n1", Type: session.NodeTypeModel}
	result := parseModelVerifierOutput(raw, node)
	if result.Status != session.VerificationPassed {
		t.Fatalf("expected passed, got %q: %s", result.Status, result.Reason)
	}
	if result.Confidence != "high" {
		t.Fatalf("expected high confidence, got %q", result.Confidence)
	}
}

func TestParseModelVerifierOutput_Failed(t *testing.T) {
	raw := `{"status":"failed","reason":"output does not mention the required file name","missing":["file name"],"confidence":"medium"}`
	node := &session.TaskGraphNode{ID: "n1", Type: session.NodeTypeModel}
	result := parseModelVerifierOutput(raw, node)
	if result.Status != session.VerificationFailed {
		t.Fatalf("expected failed, got %q", result.Status)
	}
	if len(result.Missing) == 0 || result.Missing[0] != "file name" {
		t.Fatalf("expected missing file name, got %v", result.Missing)
	}
}

func TestParseModelVerifierOutput_MissingString(t *testing.T) {
	raw := `{"status":"blocked","reason":"need more evidence","missing":"file content preview","confidence":"medium"}`
	node := &session.TaskGraphNode{ID: "n1", Type: session.NodeTypeTool}
	result := parseModelVerifierOutput(raw, node)
	if result.Status != session.VerificationBlocked {
		t.Fatalf("expected blocked, got %q", result.Status)
	}
	if len(result.Missing) != 1 || result.Missing[0] != "file content preview" {
		t.Fatalf("expected string missing to be preserved, got %#v", result.Missing)
	}
}

func TestParseModelVerifierOutput_Blocked(t *testing.T) {
	raw := `{"status":"blocked","reason":"cannot determine if output satisfies criteria","confidence":"low"}`
	node := &session.TaskGraphNode{ID: "n1", Type: session.NodeTypeModel}
	result := parseModelVerifierOutput(raw, node)
	if result.Status != session.VerificationBlocked {
		t.Fatalf("expected blocked, got %q", result.Status)
	}
}

func TestParseModelVerifierOutput_NeedsInput(t *testing.T) {
	raw := `{"status":"needs_input","reason":"human must confirm before proceeding","confidence":"high"}`
	node := &session.TaskGraphNode{ID: "n1", Type: session.NodeTypeModel}
	result := parseModelVerifierOutput(raw, node)
	if result.Status != session.VerificationNeedsInput {
		t.Fatalf("expected needs_input, got %q", result.Status)
	}
}

func TestParseModelVerifierOutput_MalformedJSON(t *testing.T) {
	raw := `not json at all`
	node := &session.TaskGraphNode{ID: "n1", Type: session.NodeTypeModel, Acceptance: session.Acceptance{Criteria: "must work"}}
	result := parseModelVerifierOutput(raw, node)
	if result.Status != session.VerificationBlocked {
		t.Fatalf("expected blocked for malformed JSON, got %q", result.Status)
	}
	if result.Confidence != "low" {
		t.Fatalf("expected low confidence, got %q", result.Confidence)
	}
	if !strings.Contains(result.Reason, "valid JSON") {
		t.Fatalf("reason should mention JSON error, got %q", result.Reason)
	}
}

func TestParseModelVerifierOutput_EmptyJSON(t *testing.T) {
	node := &session.TaskGraphNode{ID: "n1", Type: session.NodeTypeModel, Acceptance: session.Acceptance{Criteria: "must work"}}
	result := parseModelVerifierOutput("{}", node)
	if result.Status != session.VerificationBlocked {
		t.Fatalf("expected blocked for empty/invalid status, got %q", result.Status)
	}
}

func TestParseModelVerifierOutput_InvalidStatus(t *testing.T) {
	raw := `{"status":"completed","reason":"done","confidence":"high"}`
	node := &session.TaskGraphNode{ID: "n1", Type: session.NodeTypeModel, Acceptance: session.Acceptance{Criteria: "must work"}}
	result := parseModelVerifierOutput(raw, node)
	if result.Status != session.VerificationBlocked {
		t.Fatalf("expected blocked for invalid status, got %q", result.Status)
	}
}

func TestParseModelVerifierOutput_NoStatusKey(t *testing.T) {
	raw := `{"reason":"looks good","confidence":"high"}`
	node := &session.TaskGraphNode{ID: "n1", Type: session.NodeTypeModel, Acceptance: session.Acceptance{Criteria: "must work"}}
	result := parseModelVerifierOutput(raw, node)
	if result.Status != session.VerificationBlocked {
		t.Fatalf("expected blocked for missing status key, got %q", result.Status)
	}
}

func TestParseModelVerifierOutput_DefaultConfidence(t *testing.T) {
	raw := `{"status":"passed","reason":"correct"}`
	node := &session.TaskGraphNode{ID: "n1", Type: session.NodeTypeModel}
	result := parseModelVerifierOutput(raw, node)
	if result.Status != session.VerificationPassed {
		t.Fatalf("expected passed, got %q", result.Status)
	}
	if result.Confidence != "low" {
		t.Fatalf("expected default low confidence, got %q", result.Confidence)
	}
}

func TestParseModelVerifierOutput_OutsideJSONBlock(t *testing.T) {
	raw := "Here is my analysis:\n```json\n{\"status\":\"passed\",\"reason\":\"criteria met\",\"confidence\":\"high\"}\n```\nHope that helps."
	node := &session.TaskGraphNode{ID: "n1", Type: session.NodeTypeModel}
	result := parseModelVerifierOutput(raw, node)
	if result.Status != session.VerificationPassed {
		t.Fatalf("expected passed from JSON block, got %q: %s", result.Status, result.Reason)
	}
	if result.Confidence != "high" {
		t.Fatalf("expected high confidence, got %q", result.Confidence)
	}
}

func TestModelVerifier_StaticModel_PassesEnglishCriteria(t *testing.T) {
	rt := newTestRuntime(t)
	json := `{"status":"passed","reason":"output contains the required data","confidence":"high"}`
	rt.Model = staticTextModel{text: json}

	node := &session.TaskGraphNode{
		ID:            "n1",
		Type:          session.NodeTypeModel,
		Goal:          "analyze report",
		ResultSummary: "Quarterly revenue increased 15%.",
		Acceptance:    session.Acceptance{Criteria: "must mention revenue change"},
		EvidenceRefs:  []session.EvidenceRef{{Kind: "tool", ToolName: "file.read", Summary: "read report"}},
	}
	result := rt.verifyNodeWithModel(t.Context(), "test-graph", node, nil)
	if result.Status != session.VerificationPassed {
		t.Fatalf("expected passed for English criteria, got %q: %s", result.Status, result.Reason)
	}
	if result.Confidence != "high" {
		t.Fatalf("expected high confidence, got %q", result.Confidence)
	}
}

func TestModelVerifier_StaticModel_PassesChineseCriteria(t *testing.T) {
	rt := newTestRuntime(t)
	json := `{"status":"passed","reason":"输出包含所需的数据分析","confidence":"high"}`
	rt.Model = staticTextModel{text: json}

	node := &session.TaskGraphNode{
		ID:            "n1",
		Type:          session.NodeTypeModel,
		Goal:          "分析报告",
		ResultSummary: "本季度收入增长了15%。",
		Acceptance:    session.Acceptance{Criteria: "必须包含收入变化"},
		EvidenceRefs:  []session.EvidenceRef{{Kind: "tool", ToolName: "file.read", Summary: "读取报告"}},
	}
	result := rt.verifyNodeWithModel(t.Context(), "test-graph", node, nil)
	if result.Status != session.VerificationPassed {
		t.Fatalf("expected passed for Chinese criteria, got %q: %s", result.Status, result.Reason)
	}
}

func TestModelVerifier_StaticModel_CriteriaNotSatisfied(t *testing.T) {
	rt := newTestRuntime(t)
	json := `{"status":"blocked","reason":"output does not mention user count as required","missing":["user count"],"confidence":"medium"}`
	rt.Model = staticTextModel{text: json}

	node := &session.TaskGraphNode{
		ID:            "n1",
		Type:          session.NodeTypeModel,
		Goal:          "summarize users",
		ResultSummary: "The system is running normally.",
		Acceptance:    session.Acceptance{Criteria: "must include number of active users"},
	}
	result := rt.verifyNodeWithModel(t.Context(), "test-graph", node, nil)
	if result.Status != session.VerificationBlocked {
		t.Fatalf("expected blocked, got %q: %s", result.Status, result.Reason)
	}
	if len(result.Missing) == 0 || result.Missing[0] != "user count" {
		t.Fatalf("expected missing user count, got %v", result.Missing)
	}
}

func TestModelVerifier_MalformedOutput_ConservativeBlocked(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = staticTextModel{text: "not valid json at all, just some text"}

	node := &session.TaskGraphNode{
		ID:            "n1",
		Type:          session.NodeTypeModel,
		Goal:          "analyze",
		ResultSummary: "some output",
		Acceptance:    session.Acceptance{Criteria: "must be correct"},
	}
	result := rt.verifyNodeWithModel(t.Context(), "test-graph", node, nil)
	if result.Status == session.VerificationPassed {
		t.Fatal("malformed output must not result in passed")
	}
	if result.Status != session.VerificationBlocked {
		t.Fatalf("expected blocked for malformed, got %q", result.Status)
	}
	if result.Confidence != "low" {
		t.Fatalf("expected low confidence, got %q", result.Confidence)
	}
}

func TestModelVerifier_NoModelConfigured(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = nil

	node := &session.TaskGraphNode{
		ID:            "n1",
		Type:          session.NodeTypeModel,
		Goal:          "analyze",
		ResultSummary: "some output",
		Acceptance:    session.Acceptance{Criteria: "must be correct"},
	}
	result := rt.verifyNodeWithModel(t.Context(), "test-graph", node, nil)
	if result.Status != session.VerificationBlocked {
		t.Fatalf("expected blocked when no model, got %q", result.Status)
	}
	if !strings.Contains(result.Reason, "no model") {
		t.Fatalf("reason should mention no model, got %q", result.Reason)
	}
}

func TestModelVerifier_DoesNotExecuteTools(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = staticTextModel{text: `{"status":"passed","reason":"looks good","confidence":"high"}`}

	toolCallDetected := false
	rt.Tools.Register(&detectExecutionTool{detected: &toolCallDetected})

	node := &session.TaskGraphNode{
		ID:            "n1",
		Type:          session.NodeTypeModel,
		Goal:          "test",
		ResultSummary: "result",
		Acceptance:    session.Acceptance{Criteria: "test"},
	}
	result := rt.verifyNodeWithModel(t.Context(), "test-graph", node, nil)
	if result.Status != session.VerificationPassed {
		t.Fatalf("expected passed, got %q", result.Status)
	}
	if toolCallDetected {
		t.Fatal("model verifier must not execute tools")
	}
}

func TestModelVerifier_HardCheckFails_DoesNotCallModel(t *testing.T) {
	rt := newTestRuntime(t)
	modelCalled := false
	rt.Model = captureModel{next: func(ctx context.Context, c agentcore.Context) (agentcore.Message, error) {
		modelCalled = true
		return agentcore.Message{Role: agentcore.RoleAssistant, Content: `{"status":"passed","reason":"ok","confidence":"high"}`}, nil
	}}

	node := &session.TaskGraphNode{
		ID:            "n1",
		Type:          session.NodeTypeTool,
		Goal:          "read file",
		Status:        session.NodeStatusFailed,
		FailureReason: "no such file",
	}
	result := session.VerifyNode(node)
	if result.Status != session.VerificationFailed {
		t.Fatalf("hard check should fail, got %q", result.Status)
	}
	session.ApplyNodeVerification(node, result)

	if modelCalled {
		t.Fatal("model verifier should not be called when hard check fails")
	}
}

func TestModelVerifier_TruncatedOutput_ConservativeBlocked(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = staticTextModel{text: `{"statu`}

	node := &session.TaskGraphNode{
		ID:            "n1",
		Type:          session.NodeTypeModel,
		Goal:          "analyze",
		ResultSummary: "output",
		Acceptance:    session.Acceptance{Criteria: "must work"},
	}
	result := rt.verifyNodeWithModel(t.Context(), "test-graph", node, nil)
	if result.Status == session.VerificationPassed {
		t.Fatal("truncated output must not pass")
	}
	if result.Status != session.VerificationBlocked {
		t.Fatalf("expected blocked for truncated, got %q", result.Status)
	}
}

func TestRenderModelVerifierPromptIncludesNodeOutputText(t *testing.T) {
	node := &session.TaskGraphNode{
		ID:            "n1",
		Type:          session.NodeTypeModel,
		Goal:          "write report",
		ResultSummary: "short summary",
		Output:        map[string]any{"text": "full final report body"},
		Acceptance:    session.Acceptance{Criteria: "report body present"},
	}
	prompt := renderModelVerifierPrompt(node)
	if !strings.Contains(prompt, "Node Output Text: full final report body") {
		t.Fatalf("prompt missing node output text:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Do not fail solely because trace display was truncated") {
		t.Fatalf("prompt missing truncation guidance:\n%s", prompt)
	}
}

type detectExecutionTool struct {
	detected *bool
}

func (t *detectExecutionTool) Name() string             { return "detect" }
func (t *detectExecutionTool) Description() string      { return "detect execution" }
func (t *detectExecutionTool) Schema() agentcore.Schema { return agentcore.Schema{} }
func (t *detectExecutionTool) Risk() agentcore.Risk     { return agentcore.RiskSafeRead }
func (t *detectExecutionTool) Run(ctx context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	*t.detected = true
	return agentcore.ToolResult{Content: "executed"}
}
