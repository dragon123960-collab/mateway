package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/session"
)

type staticPlannerModel struct {
	json string
}

func (m staticPlannerModel) Next(_ context.Context, _ agentcore.Context) (agentcore.Message, error) {
	return agentcore.Message{Role: agentcore.RoleAssistant, Content: m.json}, nil
}

func TestParseGraphPlannerOutput_SimpleModel(t *testing.T) {
	raw := `{"goal":"answer question","risk":"low","nodes":[{"id":"answer","type":"model","goal":"answer the question"}],"task_acceptance":"correct answer"}`
	out, err := parseGraphPlannerOutput(raw)
	if err != nil {
		t.Fatal(err)
	}
	if out.Goal != "answer question" {
		t.Fatalf("goal: %q", out.Goal)
	}
	if out.Risk != "low" {
		t.Fatalf("risk: %q", out.Risk)
	}
	if len(out.Nodes) != 1 || out.Nodes[0].Type != "model" {
		t.Fatalf("nodes: %+v", out.Nodes)
	}
}

func TestParseGraphPlannerOutput_JSONWithMarkdown(t *testing.T) {
	raw := "```json\n{\"goal\":\"test\",\"risk\":\"low\",\"nodes\":[],\"task_acceptance\":\"done\"}\n```"
	out, err := parseGraphPlannerOutput(raw)
	if err != nil {
		t.Fatal(err)
	}
	if out.Goal != "test" {
		t.Fatalf("goal: %q", out.Goal)
	}
}

func TestParseGraphPlannerOutput_ExtraText(t *testing.T) {
	raw := "Here is the plan: {\"goal\":\"test\",\"risk\":\"low\",\"nodes\":[],\"task_acceptance\":\"done\"} Hope that helps."
	out, err := parseGraphPlannerOutput(raw)
	if err != nil {
		t.Fatal(err)
	}
	if out.Goal != "test" {
		t.Fatalf("goal: %q", out.Goal)
	}
}

func TestParseGraphPlannerOutput_InvalidJSON(t *testing.T) {
	_, err := parseGraphPlannerOutput("not json at all")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestConvertPlannerOutput_SimpleModel(t *testing.T) {
	out := GraphPlannerOutput{
		Goal: "answer question",
		Risk: "low",
		Nodes: []GraphPlannerNode{
			{ID: "answer", Type: "model", Goal: "answer the question", Acceptance: "correct answer"},
		},
		TaskAcceptance: "user is satisfied",
	}
	g, err := convertPlannerOutput(out, "task-1")
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	if g.TaskID != "task-1" {
		t.Fatalf("task ID: %q", g.TaskID)
	}
	if g.Status != session.GraphStatusPlanned {
		t.Fatalf("status: %q", g.Status)
	}
	if len(g.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(g.Nodes))
	}
	n := g.Nodes[0]
	if n.ID != "answer" {
		t.Fatalf("node ID: %q", n.ID)
	}
	if n.Status != session.NodeStatusPending {
		t.Fatalf("node status: %q", n.Status)
	}
	if n.Acceptance.Criteria != "correct answer" {
		t.Fatalf("acceptance: %q", n.Acceptance.Criteria)
	}
}

func TestConvertPlannerOutput_ToolNodes(t *testing.T) {
	out := GraphPlannerOutput{
		Goal: "collect and analyze",
		Risk: "medium",
		Nodes: []GraphPlannerNode{
			{ID: "read", Type: "tool", Goal: "read files", Executor: "file.read", Outputs: []string{"files"}, Acceptance: "files read"},
			{ID: "analyze", Type: "model", Goal: "analyze", Depends: []string{"read"}, Inputs: []string{"files"}, Acceptance: "analysis done"},
		},
	}
	g, err := convertPlannerOutput(out, "task-2")
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	if len(g.Nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(g.Nodes))
	}
	if g.Nodes[0].Executor != "file.read" {
		t.Fatalf("executor: %q", g.Nodes[0].Executor)
	}
	if len(g.Nodes[1].Depends) != 1 || g.Nodes[1].Depends[0] != "read" {
		t.Fatalf("depends: %v", g.Nodes[1].Depends)
	}
}

func TestConvertPlannerOutput_RiskNormalization(t *testing.T) {
	tests := []struct {
		input string
	}{
		{"low"}, {"medium"}, {"high"},
		{"LOW"}, {"Medium"}, {"HIGH"},
		{"  low  "},
	}
	for _, tt := range tests {
		out := GraphPlannerOutput{Goal: "x", Risk: tt.input, Nodes: []GraphPlannerNode{
			{ID: "n1", Type: "model", Goal: "x"},
		}}
		_, err := convertPlannerOutput(out, "t")
		if err != nil {
			t.Fatalf("input=%q: %v", tt.input, err)
		}
	}
}

func TestConvertPlannerOutput_ValidatesOutput(t *testing.T) {
	out := GraphPlannerOutput{
		Goal: "test",
		Nodes: []GraphPlannerNode{
			{ID: "n1", Type: "invalid_type", Goal: "x"},
		},
	}
	_, err := convertPlannerOutput(out, "task-3")
	if err == nil {
		t.Fatal("expected validation error for invalid type")
	}
	if !strings.Contains(err.Error(), "graph validation failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConvertPlannerOutput_CycleDetected(t *testing.T) {
	out := GraphPlannerOutput{
		Goal: "test",
		Nodes: []GraphPlannerNode{
			{ID: "a", Type: "model", Goal: "a", Depends: []string{"b"}},
			{ID: "b", Type: "model", Goal: "b", Depends: []string{"a"}},
		},
	}
	_, err := convertPlannerOutput(out, "task-4")
	if err == nil {
		t.Fatal("expected cycle error")
	}
}

func TestConvertPlannerOutput_ToolWithoutExecutor(t *testing.T) {
	out := GraphPlannerOutput{
		Goal: "test",
		Nodes: []GraphPlannerNode{
			{ID: "n1", Type: "tool", Goal: "do thing"},
		},
	}
	_, err := convertPlannerOutput(out, "task-5")
	if err == nil {
		t.Fatal("expected validation error for tool without executor")
	}
}

func TestConvertPlannerOutput_UnknownDependency(t *testing.T) {
	out := GraphPlannerOutput{
		Goal: "test",
		Nodes: []GraphPlannerNode{
			{ID: "n1", Type: "model", Goal: "step 1"},
			{ID: "n2", Type: "model", Goal: "step 2", Depends: []string{"missing-node"}},
		},
	}
	_, err := convertPlannerOutput(out, "task-dep")
	if err == nil {
		t.Fatal("expected validation error for unknown dependency")
	}
	if !strings.Contains(err.Error(), "graph validation failed") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "depends on unknown node") {
		t.Fatalf("expected unknown node message, got: %v", err)
	}
}

func TestConvertPlannerOutput_EmptyNodeID_FilledIn(t *testing.T) {
	out := GraphPlannerOutput{
		Goal: "test",
		Risk: "low",
		Nodes: []GraphPlannerNode{
			{ID: "", Type: "model", Goal: "first"},
			{ID: "", Type: "model", Goal: "second"},
		},
	}
	g, err := convertPlannerOutput(out, "task-6")
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	if g.Nodes[0].ID != "node-1" {
		t.Fatalf("expected node-1, got %q", g.Nodes[0].ID)
	}
	if g.Nodes[1].ID != "node-2" {
		t.Fatalf("expected node-2, got %q", g.Nodes[1].ID)
	}
}

func TestConvertPlannerOutput_NodeIDNormalization(t *testing.T) {
	out := GraphPlannerOutput{
		Goal: "test",
		Risk: "low",
		Nodes: []GraphPlannerNode{
			{ID: "  Collect Files  ", Type: "model", Goal: "first"},
			{ID: "Analyze Results", Type: "model", Goal: "second"},
		},
	}
	g, err := convertPlannerOutput(out, "task-7")
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	if g.Nodes[0].ID != "collect-files" {
		t.Fatalf("expected collect-files, got %q", g.Nodes[0].ID)
	}
	if g.Nodes[1].ID != "analyze-results" {
		t.Fatalf("expected analyze-results, got %q", g.Nodes[1].ID)
	}
}

func TestConvertPlannerOutput_InputsOutputsToMap(t *testing.T) {
	out := GraphPlannerOutput{
		Goal: "test",
		Risk: "low",
		Nodes: []GraphPlannerNode{
			{ID: "n1", Type: "tool", Goal: "read", Executor: "file.read", Inputs: []string{"path", "  "}, Outputs: []string{"data"}},
		},
	}
	g, err := convertPlannerOutput(out, "task-8")
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	n := g.Nodes[0]
	if n.Input == nil || n.Input["path"] != true || len(n.Input) != 1 {
		t.Fatalf("input map: %v", n.Input)
	}
	if n.Output == nil || n.Output["data"] != true || len(n.Output) != 1 {
		t.Fatalf("output map: %v", n.Output)
	}
}

func TestConvertPlannerOutput_DependsNormalization(t *testing.T) {
	out := GraphPlannerOutput{
		Goal: "test",
		Risk: "low",
		Nodes: []GraphPlannerNode{
			{ID: "a", Type: "model", Goal: "a"},
			{ID: "b", Type: "model", Goal: "b", Depends: []string{"a", "a", "  a  "}},
			{ID: "c", Type: "model", Goal: "c", Depends: []string{"  "}},
		},
	}
	g, err := convertPlannerOutput(out, "task-9")
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	if len(g.Nodes[1].Depends) != 1 || g.Nodes[1].Depends[0] != "a" {
		t.Fatalf("depends dedup failed: %v", g.Nodes[1].Depends)
	}
	if g.Nodes[2].Depends != nil {
		t.Fatalf("empty depends should be nil: %v", g.Nodes[2].Depends)
	}
}

func TestConvertPlannerOutput_IncludesHumanNode(t *testing.T) {
	out := GraphPlannerOutput{
		Goal: "risky deploy",
		Risk: "high",
		Nodes: []GraphPlannerNode{
			{ID: "prepare", Type: "tool", Goal: "prepare", Executor: "bash"},
			{ID: "review", Type: "human_review", Goal: "review deployment plan"},
			{ID: "deploy", Type: "tool", Goal: "deploy", Executor: "bash", Depends: []string{"review"}},
		},
	}
	g, err := convertPlannerOutput(out, "task-10")
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	if len(g.Nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(g.Nodes))
	}
	if g.Nodes[1].Type != session.NodeTypeHumanReview {
		t.Fatalf("expected human_review, got %q", g.Nodes[1].Type)
	}
}

func TestPlanGraphWithModel_SimpleQA(t *testing.T) {
	json := `{"goal":"answer question","risk":"low","nodes":[{"id":"answer","type":"model","goal":"answer the question","acceptance":"correct"}],"task_acceptance":"done"}`
	g, err := planGraphWithModel(t.Context(), staticPlannerModel{json: json}, "test prompt", "task-1", nil)
	if err != nil {
		t.Fatalf("planGraphWithModel failed: %v", err)
	}
	if len(g.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(g.Nodes))
	}
}

func TestPlanGraphWithModel_InvalidJSON(t *testing.T) {
	_, err := planGraphWithModel(t.Context(), staticPlannerModel{json: "not json"}, "test prompt", "task-1", nil)
	if err == nil {
		t.Fatal("expected error for invalid model output")
	}
}

func TestPlanGraphWithModel_InvalidGraph(t *testing.T) {
	json := `{"goal":"test","risk":"low","nodes":[{"id":"n1","type":"invalid","goal":"x"}],"task_acceptance":"done"}`
	_, err := planGraphWithModel(t.Context(), staticPlannerModel{json: json}, "test prompt", "task-1", nil)
	if err == nil || !strings.Contains(err.Error(), "graph validation failed") {
		t.Fatalf("expected validation error, got: %v", err)
	}
}

func TestPlanGraphWithModel_UnknownDependency(t *testing.T) {
	json := `{"goal":"test","risk":"low","nodes":[{"id":"a","type":"model","goal":"a"},{"id":"b","type":"model","goal":"b","depends":["missing"]}],"task_acceptance":"done"}`
	_, err := planGraphWithModel(t.Context(), staticPlannerModel{json: json}, "test prompt", "task-unk", nil)
	if err == nil {
		t.Fatal("expected error for unknown dependency")
	}
}

func TestValidatePlannerToolExecutors_AllValid(t *testing.T) {
	rt := newTestRuntime(t)
	g := session.TaskGraph{
		ID:     "g1",
		TaskID: "t1",
		Nodes: []session.TaskGraphNode{
			{ID: "n1", Type: session.NodeTypeTool, Goal: "read", Executor: "file.read", Status: session.NodeStatusPending},
			{ID: "n2", Type: session.NodeTypeModel, Goal: "think", Status: session.NodeStatusPending},
		},
	}
	errs := validatePlannerToolExecutors(g, rt.Tools)
	if !errs.IsValid() {
		t.Fatalf("expected valid executors, got: %v", errs)
	}
}

func TestValidatePlannerToolExecutors_UnknownTool(t *testing.T) {
	rt := newTestRuntime(t)
	g := session.TaskGraph{
		ID:     "g2",
		TaskID: "t2",
		Nodes: []session.TaskGraphNode{
			{ID: "n1", Type: session.NodeTypeTool, Goal: "bad", Executor: "nonexistent.tool", Status: session.NodeStatusPending},
		},
	}
	errs := validatePlannerToolExecutors(g, rt.Tools)
	if errs.IsValid() {
		t.Fatal("expected error for unknown tool executor")
	}
	if !strings.Contains(errs.Error(), "unknown tool executor") {
		t.Fatalf("unexpected error: %v", errs)
	}
}

func TestValidatePlannerToolExecutors_EmptyExecutor(t *testing.T) {
	rt := newTestRuntime(t)
	g := session.TaskGraph{
		ID:     "g3",
		TaskID: "t3",
		Nodes: []session.TaskGraphNode{
			{ID: "n1", Type: session.NodeTypeTool, Goal: "no exec", Executor: "", Status: session.NodeStatusPending},
		},
	}
	errs := validatePlannerToolExecutors(g, rt.Tools)
	if !errs.IsValid() {
		t.Fatalf("expected no error for empty executor (validator catches), got: %v", errs)
	}
}

func TestValidatePlannerToolExecutors_NilRegistry(t *testing.T) {
	g := session.TaskGraph{
		ID:     "g4",
		TaskID: "t4",
		Nodes: []session.TaskGraphNode{
			{ID: "n1", Type: session.NodeTypeTool, Goal: "x", Executor: "anything", Status: session.NodeStatusPending},
		},
	}
	errs := validatePlannerToolExecutors(g, nil)
	if !errs.IsValid() {
		t.Fatalf("nil registry should skip validation, got: %v", errs)
	}
}

func TestRenderGraphPlannerPrompt_IncludesTools(t *testing.T) {
	rt := newTestRuntime(t)
	prompt := renderGraphPlannerPrompt("test goal", "", rt.Tools, nil)
	if !strings.Contains(prompt, "Available tools:") {
		t.Fatal("expected Available tools section")
	}
	if !strings.Contains(prompt, "User task:") {
		t.Fatal("expected User task section")
	}
	if !strings.Contains(prompt, "test goal") {
		t.Fatal("expected user goal in prompt")
	}
}

func TestRenderGraphPlannerPrompt_UserTextDiffers(t *testing.T) {
	rt := newTestRuntime(t)
	prompt := renderGraphPlannerPrompt("test goal", "current message", rt.Tools, nil)
	if !strings.Contains(prompt, "Current user message:") {
		t.Fatal("expected Current user message section")
	}
}

func TestRenderGraphPlannerPrompt_UserTextSameAsGoal(t *testing.T) {
	rt := newTestRuntime(t)
	prompt := renderGraphPlannerPrompt("test goal", "test goal", rt.Tools, nil)
	if strings.Contains(prompt, "Current user message:") {
		t.Fatal("should not have duplicate sections when userText == goal")
	}
}

func TestGraphPlannerOutput_JSONRoundTrip(t *testing.T) {
	original := GraphPlannerOutput{
		Goal: "test",
		Risk: "low",
		Nodes: []GraphPlannerNode{
			{ID: "n1", Type: "model", Goal: "answer", Depends: nil, Executor: "", Inputs: []string{"a"}, Outputs: []string{"b"}, Acceptance: "done"},
		},
		TaskAcceptance: "task is done",
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var parsed GraphPlannerOutput
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Goal != original.Goal || len(parsed.Nodes) != 1 || parsed.Nodes[0].Acceptance != "done" {
		t.Fatal("JSON round-trip failed")
	}
}

func TestNormalizeDepends_PreservesUnknown(t *testing.T) {
	deps := []string{"a", "missing"}
	result := normalizeDepends(deps)
	if len(result) != 2 {
		t.Fatalf("expected 2 deps, got %d: %v", len(result), result)
	}
	if result[0] != "a" || result[1] != "missing" {
		t.Fatalf("expected [a missing], got %v", result)
	}
}
