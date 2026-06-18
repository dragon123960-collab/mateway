package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/session"
)

type staticPlannerModel struct {
	json string
}

func (m staticPlannerModel) Next(_ context.Context, _ agentcore.Context) (agentcore.Message, error) {
	return agentcore.Message{Role: agentcore.RoleAssistant, Content: m.json}, nil
}

type delayedPlannerModel struct {
	delay time.Duration
	json  string
}

func (m delayedPlannerModel) Next(ctx context.Context, _ agentcore.Context) (agentcore.Message, error) {
	timer := time.NewTimer(m.delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return agentcore.Message{Role: agentcore.RoleAssistant, Content: m.json}, nil
	case <-ctx.Done():
		return agentcore.Message{}, ctx.Err()
	}
}

func testUnifiedPlanJSON(goal, acceptance string, tools, skills []string, nodeJSONs ...string) string {
	if acceptance == "" {
		acceptance = "done"
	}
	nodes := make([]json.RawMessage, 0, len(nodeJSONs))
	for _, node := range nodeJSONs {
		nodes = append(nodes, json.RawMessage(node))
	}
	plan := map[string]any{
		"task": map[string]any{
			"goal":       goal,
			"risk":       "low",
			"acceptance": acceptance,
			"required_capabilities": map[string]any{
				"tools":       tools,
				"skills":      skills,
				"human_gates": []string{},
			},
			"final_output": map[string]any{
				"text":       true,
				"structured": []string{},
			},
		},
		"nodes": nodes,
	}
	data, err := json.Marshal(plan)
	if err != nil {
		panic(fmt.Sprintf("marshal test unified plan: %v", err))
	}
	return string(data)
}

type plannerVerifierModel struct {
	planJSON string
	text     string
}

func (m plannerVerifierModel) Next(_ context.Context, ctx agentcore.Context) (agentcore.Message, error) {
	if strings.Contains(ctx.SystemPrompt, "verification judge") {
		return agentcore.Message{Role: agentcore.RoleAssistant, Content: `{"status":"passed","reason":"criteria satisfied","missing":[],"confidence":"high"}`}, nil
	}
	if strings.Contains(ctx.SystemPrompt, "TaskGraphPlan") || strings.Contains(ctx.SystemPrompt, "sub-task") {
		return agentcore.Message{Role: agentcore.RoleAssistant, Content: m.planJSON}, nil
	}
	text := m.text
	if text == "" {
		text = m.planJSON
	}
	return agentcore.Message{Role: agentcore.RoleAssistant, Content: text}, nil
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

func TestConvertPlannerOutput_StructuredToolInput(t *testing.T) {
	out := GraphPlannerOutput{
		Goal: "write file",
		Risk: "medium",
		Nodes: []GraphPlannerNode{
			{
				ID:       "write",
				Type:     "tool",
				Goal:     "write hello file",
				Executor: "file.write",
				Input: map[string]any{
					"path":    "/tmp/hello.txt",
					"content": "hello task graph",
				},
			},
		},
	}
	g, err := convertPlannerOutput(out, "task-structured")
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	input := g.Nodes[0].Input
	if input["path"] != "/tmp/hello.txt" || input["content"] != "hello task graph" {
		t.Fatalf("structured input not preserved: %#v", input)
	}
}

func TestConvertPlannerOutput_ArgsAliasForToolInput(t *testing.T) {
	out := GraphPlannerOutput{
		Goal: "run command",
		Risk: "medium",
		Nodes: []GraphPlannerNode{
			{
				ID:       "run",
				Type:     "tool",
				Goal:     "run script",
				Executor: "terminal.run",
				Args: map[string]any{
					"command":         "bash /tmp/script.sh",
					"timeout_seconds": float64(30),
				},
			},
		},
	}
	g, err := convertPlannerOutput(out, "task-args")
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	input := g.Nodes[0].Input
	if input["command"] != "bash /tmp/script.sh" || input["timeout_seconds"] != float64(30) {
		t.Fatalf("args alias not preserved: %#v", input)
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
	g, err := planGraphWithModel(t.Context(), staticPlannerModel{json: json}, "test prompt", "task-1", time.Minute, nil)
	if err != nil {
		t.Fatalf("planGraphWithModel failed: %v", err)
	}
	if len(g.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(g.Nodes))
	}
}

func TestPlanGraphWithModel_InvalidJSON(t *testing.T) {
	_, err := planGraphWithModel(t.Context(), staticPlannerModel{json: "not json"}, "test prompt", "task-1", time.Minute, nil)
	if err == nil {
		t.Fatal("expected error for invalid model output")
	}
}

func TestPlanGraphWithModel_InvalidGraph(t *testing.T) {
	json := `{"goal":"test","risk":"low","nodes":[{"id":"n1","type":"invalid","goal":"x"}],"task_acceptance":"done"}`
	_, err := planGraphWithModel(t.Context(), staticPlannerModel{json: json}, "test prompt", "task-1", time.Minute, nil)
	if err == nil || !strings.Contains(err.Error(), "graph validation failed") {
		t.Fatalf("expected validation error, got: %v", err)
	}
}

func TestPlanGraphWithModel_UnknownDependency(t *testing.T) {
	json := `{"goal":"test","risk":"low","nodes":[{"id":"a","type":"model","goal":"a"},{"id":"b","type":"model","goal":"b","depends":["missing"]}],"task_acceptance":"done"}`
	_, err := planGraphWithModel(t.Context(), staticPlannerModel{json: json}, "test prompt", "task-unk", time.Minute, nil)
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
	if !strings.Contains(prompt, "Required input keys: path, content") {
		t.Fatal("expected tool required input keys in prompt")
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

func TestRenderGraphPlannerPrompt_HumanConfirmationRule(t *testing.T) {
	rt := newTestRuntime(t)
	prompt := renderGraphPlannerPrompt("write file after approval", "write file after approval", rt.Tools, nil)
	for _, want := range []string{
		"confirmation, approval, review, or permission",
		"human_confirm or human_review",
		"never use a model node to ask for approval",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestGraphPlannerOutput_JSONRoundTrip(t *testing.T) {
	original := GraphPlannerOutput{
		Goal: "test",
		Risk: "low",
		Nodes: []GraphPlannerNode{
			{ID: "n1", Type: "model", Goal: "answer", Depends: nil, Executor: "", Input: map[string]any{"context": "a"}, Outputs: []string{"b"}, Acceptance: "done"},
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
	if parsed.Nodes[0].Input["context"] != "a" {
		t.Fatalf("structured input round-trip failed: %#v", parsed.Nodes[0].Input)
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

func TestParseTaskGraphPlan_SimpleDirectQA(t *testing.T) {
	raw := `{
		"task": {
			"goal": "answer a question",
			"risk": "low",
			"acceptance": "correct answer given",
			"required_capabilities": {"tools": [], "skills": [], "human_gates": []},
			"final_output": {"text": true, "structured": []}
		},
		"nodes": [
			{
				"id": "answer",
				"type": "subtask",
				"mode": "direct",
				"goal": "answer the question",
				"depends": [],
				"acceptance": "correct answer"
			}
		]
	}`
	plan, err := parseTaskGraphPlan(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if plan.Task.Goal != "answer a question" {
		t.Fatalf("task goal: %q", plan.Task.Goal)
	}
	if plan.Task.Risk != "low" {
		t.Fatalf("task risk: %q", plan.Task.Risk)
	}
	if plan.Task.Acceptance != "correct answer given" {
		t.Fatalf("task acceptance: %q", plan.Task.Acceptance)
	}
	if len(plan.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(plan.Nodes))
	}
	n := plan.Nodes[0]
	if n.Type != "subtask" {
		t.Fatalf("node type: %q", n.Type)
	}
	if n.Mode != "direct" {
		t.Fatalf("node mode: %q", n.Mode)
	}
	if n.Goal != "answer the question" {
		t.Fatalf("node goal: %q", n.Goal)
	}
	if n.Acceptance != "correct answer" {
		t.Fatalf("node acceptance: %q", n.Acceptance)
	}
}

func TestParseTaskGraphPlan_ReactSubtasks(t *testing.T) {
	raw := `{
		"task": {
			"goal": "analyze codebase",
			"risk": "medium",
			"acceptance": "architecture analysis complete",
			"required_capabilities": {
				"tools": ["file.read", "terminal.run"],
				"skills": [],
				"human_gates": []
			},
			"final_output": {"text": true, "structured": ["architecture_summary"]}
		},
		"nodes": [
			{
				"id": "explore",
				"type": "subtask",
				"mode": "react",
				"goal": "explore repository structure",
				"depends": [],
				"allowed_tools": ["file.read"],
				"inputs": ["repo_path"],
				"outputs": ["file_list"],
				"acceptance": "all directories and entrypoints listed"
			},
			{
				"id": "analyze-deps",
				"type": "subtask",
				"mode": "react",
				"goal": "analyze dependencies",
				"depends": ["explore"],
				"allowed_tools": ["file.read", "terminal.run"],
				"inputs": ["file_list"],
				"outputs": ["dependency_graph"],
				"acceptance": "internal and external dependencies mapped"
			},
			{
				"id": "synthesize",
				"type": "subtask",
				"mode": "direct",
				"goal": "synthesize findings",
				"depends": ["analyze-deps"],
				"outputs": ["final_report"],
				"acceptance": "architecture summary with risks"
			}
		]
	}`
	plan, err := parseTaskGraphPlan(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(plan.Nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(plan.Nodes))
	}
	if len(plan.Task.RequiredCapabilities.Tools) != 2 {
		t.Fatalf("expected 2 required tools, got %d", len(plan.Task.RequiredCapabilities.Tools))
	}
	if plan.Nodes[0].Mode != "react" {
		t.Fatalf("first node should be react, got %q", plan.Nodes[0].Mode)
	}
	if len(plan.Nodes[0].AllowedTools) != 1 || plan.Nodes[0].AllowedTools[0] != "file.read" {
		t.Fatalf("first node allowed_tools: %v", plan.Nodes[0].AllowedTools)
	}
	if plan.Nodes[2].Mode != "direct" {
		t.Fatalf("third node should be direct, got %q", plan.Nodes[2].Mode)
	}
}

func TestParseTaskGraphPlan_WithHumanConfirm(t *testing.T) {
	raw := `{
		"task": {
			"goal": "write file with confirmation",
			"risk": "high",
			"acceptance": "file written after user approval",
			"required_capabilities": {
				"tools": ["file.write"],
				"skills": [],
				"human_gates": ["confirm-before-write"]
			},
			"final_output": {"text": true, "structured": ["file_path"]}
		},
		"nodes": [
			{
				"id": "prepare",
				"type": "subtask",
				"mode": "react",
				"goal": "prepare file content",
				"depends": [],
				"allowed_tools": ["file.read"],
				"outputs": ["content"],
				"acceptance": "content ready for review"
			},
			{
				"id": "confirm-write",
				"type": "human_confirm",
				"goal": "confirm file write",
				"depends": ["prepare"],
				"acceptance": "user approved the write"
			},
			{
				"id": "write",
				"type": "subtask",
				"mode": "react",
				"goal": "write the file",
				"depends": ["confirm-write"],
				"allowed_tools": ["file.write"],
				"acceptance": "file written successfully"
			}
		]
	}`
	plan, err := parseTaskGraphPlan(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(plan.Nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(plan.Nodes))
	}
	if plan.Task.Risk != "high" {
		t.Fatalf("expected high risk, got %q", plan.Task.Risk)
	}
	if len(plan.Task.RequiredCapabilities.HumanGates) != 1 {
		t.Fatalf("expected 1 human gate, got %d", len(plan.Task.RequiredCapabilities.HumanGates))
	}
	if plan.Nodes[1].Type != "human_confirm" {
		t.Fatalf("second node should be human_confirm, got %q", plan.Nodes[1].Type)
	}
}

func TestParseTaskGraphPlan_MissingTaskGoal(t *testing.T) {
	raw := `{
		"task": {"goal": "", "risk": "low", "acceptance": "x",
			"required_capabilities": {"tools": [], "skills": [], "human_gates": []},
			"final_output": {"text": true, "structured": []}},
		"nodes": [{"id": "n1", "type": "subtask", "mode": "direct", "goal": "x", "acceptance": "x"}]
	}`
	_, err := parseTaskGraphPlan(raw)
	if err == nil {
		t.Fatal("expected error for empty task goal")
	}
}

func TestParseTaskGraphPlan_NoNodes(t *testing.T) {
	raw := `{
		"task": {"goal": "x", "risk": "low", "acceptance": "x",
			"required_capabilities": {"tools": [], "skills": [], "human_gates": []},
			"final_output": {"text": true, "structured": []}},
		"nodes": []
	}`
	_, err := parseTaskGraphPlan(raw)
	if err == nil {
		t.Fatal("expected error for empty nodes")
	}
}

func TestConvertTaskGraphPlan_SimpleDirect(t *testing.T) {
	plan := TaskGraphPlan{
		Task: TaskPlanLevel{
			Goal:       "answer a question",
			Risk:       "low",
			Acceptance: "correct answer given",
			RequiredCapabilities: TaskPlanCapabilities{
				Tools:      nil,
				Skills:     nil,
				HumanGates: nil,
			},
			FinalOutput: TaskPlanFinalOutput{Text: true},
		},
		Nodes: []TaskPlanNode{
			{ID: "answer", Type: "subtask", Mode: "direct", Goal: "answer the question", Acceptance: "correct answer"},
		},
	}
	g, err := convertTaskGraphPlan(plan, "task-1")
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
	if n.Type != session.NodeTypeSubtask {
		t.Fatalf("node type: %q (expected %q)", n.Type, session.NodeTypeSubtask)
	}
	if n.Mode != session.NodeModeDirect {
		t.Fatalf("node mode: %q (expected %q)", n.Mode, session.NodeModeDirect)
	}
	if n.Acceptance.Criteria != "correct answer" {
		t.Fatalf("acceptance: %q", n.Acceptance.Criteria)
	}
}

func TestConvertTaskGraphPlan_ReactWithAllowedTools(t *testing.T) {
	plan := TaskGraphPlan{
		Task: TaskPlanLevel{
			Goal:       "analyze repo",
			Risk:       "medium",
			Acceptance: "analysis done",
			RequiredCapabilities: TaskPlanCapabilities{
				Tools: []string{"file.read", "terminal.run"},
			},
			FinalOutput: TaskPlanFinalOutput{Text: true},
		},
		Nodes: []TaskPlanNode{
			{
				ID: "explore", Type: "subtask", Mode: "react", Goal: "explore",
				AllowedTools: []string{"file.read"},
				Outputs:      []string{"files"},
				Acceptance:   "files listed",
			},
			{
				ID: "report", Type: "subtask", Mode: "direct", Goal: "report",
				Depends:    []string{"explore"},
				Acceptance: "report done",
			},
		},
	}
	g, err := convertTaskGraphPlan(plan, "task-2")
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	if len(g.Nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(g.Nodes))
	}
	n0 := g.Nodes[0]
	if n0.Mode != session.NodeModeReact {
		t.Fatalf("first node mode: %q", n0.Mode)
	}
	if len(n0.AllowedTools) != 1 || n0.AllowedTools[0] != "file.read" {
		t.Fatalf("first node allowed_tools: %v", n0.AllowedTools)
	}
	n1 := g.Nodes[1]
	if len(n1.Depends) != 1 || n1.Depends[0] != "explore" {
		t.Fatalf("second node depends: %v", n1.Depends)
	}
}

func TestConvertTaskGraphPlan_HumanConfirmNode(t *testing.T) {
	plan := TaskGraphPlan{
		Task: TaskPlanLevel{
			Goal:       "write file",
			Risk:       "high",
			Acceptance: "file written after approval",
			RequiredCapabilities: TaskPlanCapabilities{
				Tools:      []string{"file.write"},
				HumanGates: []string{"confirm-before-write"},
			},
			FinalOutput: TaskPlanFinalOutput{Text: true},
		},
		Nodes: []TaskPlanNode{
			{ID: "prepare", Type: "subtask", Mode: "react", Goal: "prepare", AllowedTools: []string{"file.read"}, Acceptance: "ready"},
			{ID: "confirm", Type: "human_confirm", Goal: "confirm write", Depends: []string{"prepare"}, Acceptance: "user approved"},
			{ID: "write", Type: "subtask", Mode: "react", Goal: "write", Depends: []string{"confirm"}, AllowedTools: []string{"file.write"}, Acceptance: "written"},
		},
	}
	g, err := convertTaskGraphPlan(plan, "task-3")
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	if len(g.Nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(g.Nodes))
	}
	confirmNode := g.Nodes[1]
	if confirmNode.Type != session.NodeTypeHumanConfirm {
		t.Fatalf("expected human_confirm, got %q", confirmNode.Type)
	}
	if confirmNode.Mode != session.NodeModeHuman {
		t.Fatalf("human nodes should use human mode, got %q", confirmNode.Mode)
	}
}

func TestConvertTaskGraphPlan_NormalizesHumanMode(t *testing.T) {
	plan := TaskGraphPlan{
		Task: TaskPlanLevel{
			Goal:       "review risky action",
			Risk:       "high",
			Acceptance: "user confirmed",
			FinalOutput: TaskPlanFinalOutput{
				Text: true,
			},
		},
		Nodes: []TaskPlanNode{
			{
				ID:         "human-confirm-plan",
				Type:       "human_confirm",
				Mode:       "direct",
				Goal:       "confirm before writing",
				Acceptance: "user confirms",
			},
		},
	}
	g, err := convertTaskGraphPlan(plan, "task-human-normalize")
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	if len(g.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(g.Nodes))
	}
	n := g.Nodes[0]
	if n.Type != session.NodeTypeHumanConfirm {
		t.Fatalf("node type: %q", n.Type)
	}
	if n.Mode != session.NodeModeHuman {
		t.Fatalf("human mode should be normalized to %q, got %q", session.NodeModeHuman, n.Mode)
	}
}

func TestConvertTaskGraphPlan_DefaultModeReact(t *testing.T) {
	plan := TaskGraphPlan{
		Task: TaskPlanLevel{
			Goal:                 "do something",
			Risk:                 "medium",
			RequiredCapabilities: TaskPlanCapabilities{},
			FinalOutput:          TaskPlanFinalOutput{Text: true},
		},
		Nodes: []TaskPlanNode{
			{ID: "explore", Type: "subtask", Mode: "", Goal: "explore", AllowedTools: []string{"file.read"}, Acceptance: "done"},
		},
	}
	g, err := convertTaskGraphPlan(plan, "task-4")
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	n := g.Nodes[0]
	if n.Mode != session.NodeModeReact {
		t.Fatalf("empty mode should default to react, got %q", n.Mode)
	}
}

func TestConvertTaskGraphPlan_EmptyIDsFilledIn(t *testing.T) {
	plan := TaskGraphPlan{
		Task: TaskPlanLevel{
			Goal:                 "test",
			Risk:                 "low",
			RequiredCapabilities: TaskPlanCapabilities{},
			FinalOutput:          TaskPlanFinalOutput{Text: true},
		},
		Nodes: []TaskPlanNode{
			{ID: "", Type: "subtask", Mode: "direct", Goal: "first", Acceptance: "done"},
			{ID: "", Type: "subtask", Mode: "direct", Goal: "second", Acceptance: "done"},
		},
	}
	g, err := convertTaskGraphPlan(plan, "task-5")
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

func TestConvertTaskGraphPlan_CycleDetection(t *testing.T) {
	plan := TaskGraphPlan{
		Task: TaskPlanLevel{
			Goal:                 "test",
			Risk:                 "low",
			RequiredCapabilities: TaskPlanCapabilities{},
			FinalOutput:          TaskPlanFinalOutput{Text: true},
		},
		Nodes: []TaskPlanNode{
			{ID: "a", Type: "subtask", Mode: "direct", Goal: "a", Depends: []string{"b"}, Acceptance: "done"},
			{ID: "b", Type: "subtask", Mode: "direct", Goal: "b", Depends: []string{"a"}, Acceptance: "done"},
		},
	}
	_, err := convertTaskGraphPlan(plan, "task-cycle")
	if err == nil {
		t.Fatal("expected cycle error")
	}
}

func TestConvertTaskGraphPlan_SubtaskMissingMode(t *testing.T) {
	plan := TaskGraphPlan{
		Task: TaskPlanLevel{
			Goal:                 "test",
			Risk:                 "low",
			RequiredCapabilities: TaskPlanCapabilities{},
			FinalOutput:          TaskPlanFinalOutput{Text: true},
		},
		Nodes: []TaskPlanNode{
			{ID: "n1", Type: "subtask", Goal: "do something", Acceptance: "done"},
		},
	}
	g, err := convertTaskGraphPlan(plan, "task-6")
	if err != nil {
		t.Fatalf("should default mode to react: %v", err)
	}
	if g.Nodes[0].Mode != session.NodeModeReact {
		t.Fatalf("expected default react mode, got %q", g.Nodes[0].Mode)
	}
}

func TestTaskContractFromPlan_Basic(t *testing.T) {
	plan := TaskGraphPlan{
		Task: TaskPlanLevel{
			Goal:       "analyze repository",
			Risk:       "medium",
			Acceptance: "complete architecture report",
			RequiredCapabilities: TaskPlanCapabilities{
				Tools:  []string{"file.read", "terminal.run"},
				Skills: []string{"repo-analyzer"},
			},
			FinalOutput: TaskPlanFinalOutput{Text: true, Structured: []string{"report"}},
		},
		Nodes: []TaskPlanNode{
			{ID: "explore", Type: "subtask", Mode: "react", Goal: "explore codebase", AllowedTools: []string{"file.read"}, Acceptance: "files listed"},
			{ID: "report", Type: "subtask", Mode: "direct", Goal: "write report", Depends: []string{"explore"}, Acceptance: "report written"},
		},
	}
	contract := taskContractFromPlan(plan)
	if contract.Summary != "analyze repository" {
		t.Fatalf("summary: %q", contract.Summary)
	}
	if contract.ExpectedOutcome != "complete architecture report" {
		t.Fatalf("expected_outcome: %q", contract.ExpectedOutcome)
	}
	if !contract.RequiresTools {
		t.Fatal("expected requires_tools=true")
	}
	if len(contract.RequiredTools) != 2 {
		t.Fatalf("expected 2 required tools, got %d", len(contract.RequiredTools))
	}
	if len(contract.RequiredSkills) != 1 {
		t.Fatalf("expected 1 required skill, got %d", len(contract.RequiredSkills))
	}
	if len(contract.PlanItems) != 2 {
		t.Fatalf("expected 2 plan items, got %d", len(contract.PlanItems))
	}
	if contract.PlanItems[0].Tool != "file.read" {
		t.Fatalf("first plan item tool: %q", contract.PlanItems[0].Tool)
	}
}

func TestTaskContractFromPlan_NoTools(t *testing.T) {
	plan := TaskGraphPlan{
		Task: TaskPlanLevel{
			Goal:       "answer a question",
			Risk:       "low",
			Acceptance: "answer given",
			RequiredCapabilities: TaskPlanCapabilities{
				Tools:  nil,
				Skills: nil,
			},
			FinalOutput: TaskPlanFinalOutput{Text: true},
		},
		Nodes: []TaskPlanNode{
			{ID: "answer", Type: "subtask", Mode: "direct", Goal: "answer", Acceptance: "answer given"},
		},
	}
	contract := taskContractFromPlan(plan)
	if contract.RequiresTools {
		t.Fatal("expected requires_tools=false for no-tool plan")
	}
	if len(contract.RequiredTools) != 0 {
		t.Fatalf("expected empty required_tools, got %v", contract.RequiredTools)
	}
}

func TestValidatePlanTools_AllValid(t *testing.T) {
	rt := newTestRuntime(t)
	plan := TaskGraphPlan{
		Task: TaskPlanLevel{
			Goal: "read files",
			Risk: "low",
			RequiredCapabilities: TaskPlanCapabilities{
				Tools: []string{"file.read"},
			},
		},
		Nodes: []TaskPlanNode{
			{ID: "n1", Type: "subtask", Mode: "react", Goal: "read", AllowedTools: []string{"file.read"}, Acceptance: "done"},
		},
	}
	errs := validatePlanTools(plan, rt.Tools, nil)
	if !errs.IsValid() {
		t.Fatalf("expected valid, got: %v", errs)
	}
}

func TestValidatePlanTools_UnknownTool(t *testing.T) {
	rt := newTestRuntime(t)
	plan := TaskGraphPlan{
		Task: TaskPlanLevel{
			Goal: "bad task",
			Risk: "low",
			RequiredCapabilities: TaskPlanCapabilities{
				Tools: []string{"nonexistent"},
			},
		},
		Nodes: []TaskPlanNode{
			{ID: "n1", Type: "subtask", Mode: "react", Goal: "bad", AllowedTools: []string{"nonexistent"}, Acceptance: "done"},
		},
	}
	errs := validatePlanTools(plan, rt.Tools, nil)
	if errs.IsValid() {
		t.Fatal("expected error for unknown tool")
	}
}

func TestValidatePlanTools_NilRegistry(t *testing.T) {
	plan := TaskGraphPlan{
		Task: TaskPlanLevel{
			Goal:                 "anything",
			Risk:                 "low",
			RequiredCapabilities: TaskPlanCapabilities{},
		},
		Nodes: []TaskPlanNode{
			{ID: "n1", Type: "subtask", Mode: "react", Goal: "x", AllowedTools: []string{"anything"}, Acceptance: "done"},
		},
	}
	errs := validatePlanTools(plan, nil, nil)
	if !errs.IsValid() {
		t.Fatalf("nil registry should skip validation, got: %v", errs)
	}
}

func TestValidatePlanTools_SkillNameInAllowedTools(t *testing.T) {
	rt := newTestRuntime(t)
	skill := discoveredSkill{Name: "my-skill", Path: "/path/to/SKILL.md"}
	plan := TaskGraphPlan{
		Task: TaskPlanLevel{
			Goal:                 "use skill",
			Risk:                 "low",
			RequiredCapabilities: TaskPlanCapabilities{},
		},
		Nodes: []TaskPlanNode{
			{ID: "n1", Type: "subtask", Mode: "react", Goal: "do thing", AllowedTools: []string{"my-skill"}, Acceptance: "done"},
		},
	}
	errs := validatePlanTools(plan, rt.Tools, []discoveredSkill{skill})
	if errs.IsValid() {
		t.Fatal("expected error for skill name in allowed_tools")
	}
	if !strings.Contains(errs.Error(), "skill name") || !strings.Contains(errs.Error(), "allowed_tools") {
		t.Fatalf("expected 'skill name...allowed_tools' error, got: %v", errs)
	}
}

func TestValidatePlanTools_SkillNameInRequiredTools(t *testing.T) {
	rt := newTestRuntime(t)
	skill := discoveredSkill{Name: "my-skill", Path: "/path/to/SKILL.md"}
	plan := TaskGraphPlan{
		Task: TaskPlanLevel{
			Goal: "use skill",
			Risk: "low",
			RequiredCapabilities: TaskPlanCapabilities{
				Tools: []string{"my-skill"},
			},
		},
		Nodes: []TaskPlanNode{
			{ID: "n1", Type: "subtask", Mode: "react", Goal: "x", AllowedTools: []string{"file.read"}, Acceptance: "done"},
		},
	}
	errs := validatePlanTools(plan, rt.Tools, []discoveredSkill{skill})
	if errs.IsValid() {
		t.Fatal("expected error for skill name in required_capabilities.tools")
	}
	if !strings.Contains(errs.Error(), "skill name") || !strings.Contains(errs.Error(), "required_capabilities.tools") {
		t.Fatalf("expected 'skill name...required_capabilities.tools' error, got: %v", errs)
	}
}

func TestValidatePlanTools_UnknownSkillInCapabilities(t *testing.T) {
	rt := newTestRuntime(t)
	plan := TaskGraphPlan{
		Task: TaskPlanLevel{
			Goal: "use unknown skill",
			Risk: "low",
			RequiredCapabilities: TaskPlanCapabilities{
				Skills: []string{"nonexistent-skill"},
			},
		},
		Nodes: []TaskPlanNode{
			{ID: "n1", Type: "subtask", Mode: "react", Goal: "x", AllowedTools: []string{"file.read"}, Acceptance: "done"},
		},
	}
	errs := validatePlanTools(plan, rt.Tools, nil)
	if errs.IsValid() {
		t.Fatal("expected error for unknown skill in required_capabilities.skills")
	}
	if !strings.Contains(errs.Error(), "unknown skill") {
		t.Fatalf("expected 'unknown skill' error, got: %v", errs)
	}
}

func TestValidatePlanTools_UnknownSkillInNode(t *testing.T) {
	rt := newTestRuntime(t)
	plan := TaskGraphPlan{
		Task: TaskPlanLevel{
			Goal:                 "use unknown skill in node",
			Risk:                 "low",
			RequiredCapabilities: TaskPlanCapabilities{},
		},
		Nodes: []TaskPlanNode{
			{ID: "n1", Type: "skill", Mode: "skill", Goal: "x", Skill: "nonexistent-skill", Acceptance: "done"},
		},
	}
	errs := validatePlanTools(plan, rt.Tools, nil)
	if errs.IsValid() {
		t.Fatal("expected error for unknown skill in node.skill")
	}
	if !strings.Contains(errs.Error(), "unknown skill") {
		t.Fatalf("expected 'unknown skill' error, got: %v", errs)
	}
}

func TestValidatePlanTools_WorkflowSkillCannotBeSingleNode(t *testing.T) {
	rt := newTestRuntime(t)
	plan := TaskGraphPlan{
		Task: TaskPlanLevel{Goal: "use workflow skill", Risk: "low"},
		Nodes: []TaskPlanNode{
			{ID: "n1", Type: "skill", Mode: "skill", Goal: "run workflow", Skill: "workflow-skill", Acceptance: "done"},
		},
	}
	errs := validatePlanTools(plan, rt.Tools, []discoveredSkill{{
		Name:        "workflow-skill",
		Granularity: "workflow",
		Stage:       "execution",
	}})
	if errs.IsValid() {
		t.Fatal("expected error for workflow skill used as a single skill node")
	}
	if !strings.Contains(errs.Error(), "granularity=workflow") {
		t.Fatalf("expected granularity error, got: %v", errs)
	}
}

func TestValidatePlanTools_PlanningSkillCannotBeExecuted(t *testing.T) {
	rt := newTestRuntime(t)
	plan := TaskGraphPlan{
		Task: TaskPlanLevel{Goal: "use planning skill", Risk: "low"},
		Nodes: []TaskPlanNode{
			{ID: "n1", Type: "skill", Mode: "skill", Goal: "run planning skill", Skill: "planning-skill", Acceptance: "done"},
		},
	}
	errs := validatePlanTools(plan, rt.Tools, []discoveredSkill{{
		Name:        "planning-skill",
		Granularity: "subtask",
		Stage:       "planning",
	}})
	if errs.IsValid() {
		t.Fatal("expected error for planning skill used as an executable skill node")
	}
	if !strings.Contains(errs.Error(), "stage=planning") {
		t.Fatalf("expected stage error, got: %v", errs)
	}
}

func TestValidatePlanTools_SkillAllowedToolsMustRespectMetadata(t *testing.T) {
	rt := newTestRuntime(t)
	plan := TaskGraphPlan{
		Task: TaskPlanLevel{Goal: "use skill", Risk: "low"},
		Nodes: []TaskPlanNode{
			{ID: "n1", Type: "skill", Mode: "skill", Goal: "run skill", Skill: "source-skill", AllowedTools: []string{"web.search", "terminal.run"}, Acceptance: "done"},
		},
	}
	errs := validatePlanTools(plan, rt.Tools, []discoveredSkill{{
		Name:         "source-skill",
		Granularity:  "subtask",
		Stage:        "execution",
		AllowedTools: []string{"web.search"},
	}})
	if errs.IsValid() {
		t.Fatal("expected error for skill node allowed_tools outside metadata")
	}
	if !strings.Contains(errs.Error(), "not allowed by skill metadata") {
		t.Fatalf("expected metadata allowed_tools error, got: %v", errs)
	}
}

func TestTaskContractFromPlan_SkillNodeDoesNotRequireSkillMDReadEvidence(t *testing.T) {
	plan := TaskGraphPlan{
		Task: TaskPlanLevel{
			Goal:       "use skill",
			Acceptance: "skill result accepted",
			RequiredCapabilities: TaskPlanCapabilities{
				Skills: []string{"source-skill"},
			},
		},
		Nodes: []TaskPlanNode{
			{ID: "n1", Type: "skill", Mode: "skill", Goal: "run skill", Skill: "source-skill", Acceptance: "skill completed"},
		},
	}
	contract := taskContractFromPlan(plan)
	for _, item := range contract.PlanItems {
		if item.Tool == "file.read" {
			t.Fatalf("skill node should not be converted into SKILL.md file.read plan item: %#v", item)
		}
	}
	for _, evidence := range contract.RequiredEvidence {
		if evidence.Tool == "file.read" {
			t.Fatalf("skill node should not require SKILL.md file.read evidence: %#v", evidence)
		}
	}
}

func TestConvertTaskGraphPlan_PersistsSkillInInput(t *testing.T) {
	plan := TaskGraphPlan{
		Task: TaskPlanLevel{
			Goal:                 "use skill",
			Risk:                 "low",
			RequiredCapabilities: TaskPlanCapabilities{},
			FinalOutput:          TaskPlanFinalOutput{Text: true},
		},
		Nodes: []TaskPlanNode{
			{ID: "n1", Type: "skill", Mode: "skill", Goal: "run skill", Skill: "repo-analyzer", Acceptance: "done"},
		},
	}
	g, err := convertTaskGraphPlan(plan, "task-skill")
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	if len(g.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(g.Nodes))
	}
	n := g.Nodes[0]
	skillVal, ok := n.Input["skill"]
	if !ok {
		t.Fatal("skill not persisted in node.Input")
	}
	if skillVal != "repo-analyzer" {
		t.Fatalf("expected skill='repo-analyzer', got %v", skillVal)
	}
	if n.Type != session.NodeTypeSkill || n.Mode != session.NodeModeSkill || n.Executor != "repo-analyzer" {
		t.Fatalf("skill node not normalized correctly: type=%q mode=%q executor=%q", n.Type, n.Mode, n.Executor)
	}
}

func TestConvertTaskGraphPlanWithSkills_InheritsSkillAllowedTools(t *testing.T) {
	plan := TaskGraphPlan{
		Task: TaskPlanLevel{
			Goal:        "use skill",
			Risk:        "low",
			FinalOutput: TaskPlanFinalOutput{Text: true},
		},
		Nodes: []TaskPlanNode{
			{ID: "n1", Type: "subtask", Mode: "react", Goal: "run skill", Skill: "source-evaluation", Acceptance: "done"},
		},
	}
	g, err := convertTaskGraphPlanWithSkills(plan, "task-skill-tools", []discoveredSkill{{
		Name:         "source-evaluation",
		AllowedTools: []string{"file.read"},
	}})
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	n := g.Nodes[0]
	if n.Type != session.NodeTypeSkill || n.Mode != session.NodeModeSkill || n.Executor != "source-evaluation" {
		t.Fatalf("skill node not normalized correctly: %#v", n)
	}
	if len(n.AllowedTools) != 1 || n.AllowedTools[0] != "file.read" {
		t.Fatalf("expected skill allowed tools inherited, got %v", n.AllowedTools)
	}
}

func TestRenderUnifiedPlannerPrompt_IncludesSections(t *testing.T) {
	rt := newTestRuntime(t)
	prompt := renderUnifiedPlannerPrompt("test goal", "", "", rt.Tools, nil)
	for _, want := range []string{
		"User task:",
		"test goal",
		"Available tools:",
		"Guidance:",
		"Break the task into verifiable sub-task nodes",
		"mode direct",
		"mode react",
		"human_confirm",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestRenderUnifiedPlannerPrompt_UserTextDiffers(t *testing.T) {
	rt := newTestRuntime(t)
	prompt := renderUnifiedPlannerPrompt("goal", "current message", "", rt.Tools, nil)
	if !strings.Contains(prompt, "Current user message:") {
		t.Fatal("expected Current user message section")
	}
}

func TestRenderUnifiedPlannerPrompt_IncludesPlannerContext(t *testing.T) {
	rt := newTestRuntime(t)
	prompt := renderUnifiedPlannerPrompt("answer name", "你叫什么", "From soul.md:\n你是 小代", rt.Tools, nil)
	if !strings.Contains(prompt, "Planner context:") {
		t.Fatal("expected planner context section")
	}
	if !strings.Contains(prompt, "你是 小代") {
		t.Fatalf("expected planner context to include profile identity, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Do not treat it as completed evidence") {
		t.Fatalf("expected planner context boundary guidance, got:\n%s", prompt)
	}
}

func TestRenderUnifiedPlannerPrompt_IncludesSkillExecutionMetadata(t *testing.T) {
	rt := newTestRuntime(t)
	skills := []discoveredSkill{{
		Name:         "feishu-notify",
		Description:  "Create Feishu documents.",
		Stage:        "execution",
		GraphType:    "react",
		Granularity:  "subtask",
		AllowedTools: []string{"terminal.run"},
		Inputs:       []string{"title", "markdown_file"},
		Outputs:      []string{"document_url"},
		Usage:        "Use terminal.run with the helper wrapper and return the created URL.",
		Entrypoints:  []string{"python3 /skill/scripts/feishu.docs.create --title <title>"},
		Success:      []string{"Return the created document URL."},
		Path:         "/tmp/skills/feishu-notify/SKILL.md",
	}}
	prompt := renderUnifiedPlannerPrompt("publish document", "", "", rt.Tools, skills)
	for _, want := range []string{
		"name: feishu-notify",
		"type: react",
		"allowed_tools: terminal.run",
		"usage: Use terminal.run with the helper wrapper",
		"entrypoints: python3 /skill/scripts/feishu.docs.create",
		"success_criteria: Return the created document URL.",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestPlanWithUnifiedPlanner_SimpleQA(t *testing.T) {
	json := `{
		"task": {
			"goal": "answer question",
			"risk": "low",
			"acceptance": "correct answer",
			"required_capabilities": {"tools": [], "skills": [], "human_gates": []},
			"final_output": {"text": true, "structured": []}
		},
		"nodes": [
			{"id": "answer", "type": "subtask", "mode": "direct", "goal": "answer the question", "acceptance": "correct"}
		]
	}`
	plan, raw, err := planWithUnifiedPlanner(t.Context(), staticPlannerModel{json: json}, "test prompt", "task-1", time.Minute, nil)
	if err != nil {
		t.Fatalf("planWithUnifiedPlanner failed: %v", err)
	}
	if raw == "" {
		t.Fatal("raw JSON should not be empty")
	}
	if len(plan.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(plan.Nodes))
	}
	if plan.Nodes[0].Mode != "direct" {
		t.Fatalf("expected direct mode, got %q", plan.Nodes[0].Mode)
	}
}

func TestPlanWithUnifiedPlanner_InvalidJSON(t *testing.T) {
	_, _, err := planWithUnifiedPlanner(t.Context(), staticPlannerModel{json: "not json"}, "test prompt", "task-1", time.Minute, nil)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestPlanWithUnifiedPlanner_MissingGoal(t *testing.T) {
	json := `{
		"task": {
			"goal": "",
			"risk": "low",
			"acceptance": "x",
			"required_capabilities": {"tools": [], "skills": [], "human_gates": []},
			"final_output": {"text": true, "structured": []}
		},
		"nodes": [
			{"id": "n1", "type": "subtask", "mode": "direct", "goal": "x", "acceptance": "x"}
		]
	}`
	_, _, err := planWithUnifiedPlanner(t.Context(), staticPlannerModel{json: json}, "test prompt", "task-1", time.Minute, nil)
	if err == nil {
		t.Fatal("expected error for missing task goal")
	}
}

func TestPlanWithUnifiedPlanner_UsesConfiguredTimeout(t *testing.T) {
	json := testUnifiedPlanJSON(
		"answer question",
		"done",
		nil,
		nil,
		`{"id":"answer","type":"subtask","mode":"direct","goal":"answer the question","acceptance":"done"}`,
	)
	plan, _, err := planWithUnifiedPlanner(
		t.Context(),
		delayedPlannerModel{delay: 25 * time.Millisecond, json: json},
		"test prompt",
		"task-1",
		200*time.Millisecond,
		nil,
	)
	if err != nil {
		t.Fatalf("planner should use configured timeout instead of a short fixed deadline: %v", err)
	}
	if len(plan.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(plan.Nodes))
	}
}

func TestPlanTaskGraphUnified_Integration(t *testing.T) {
	json := `{
		"task": {
			"goal": "analyze repo",
			"risk": "medium",
			"acceptance": "full analysis done",
			"required_capabilities": {
				"tools": ["file.read"],
				"skills": [],
				"human_gates": []
			},
			"final_output": {"text": true, "structured": ["report"]}
		},
		"nodes": [
			{"id": "explore", "type": "subtask", "mode": "react", "goal": "explore repo", "allowed_tools": ["file.read"], "acceptance": "files listed"},
			{"id": "analyze", "type": "subtask", "mode": "react", "goal": "analyze code", "depends": ["explore"], "allowed_tools": ["file.read"], "acceptance": "patterns found"},
			{"id": "report", "type": "subtask", "mode": "direct", "goal": "write report", "depends": ["analyze"], "acceptance": "report written"}
		]
	}`
	rt := newTestRuntime(t)
	task := &session.TaskNode{ID: "task-integration", Goal: "analyze repo"}
	plan, contract, err := rt.planTaskGraphUnified(t.Context(), task, "analyze repo", "", staticPlannerModel{json: json}, rt.Tools, nil, nil)
	if err != nil {
		t.Fatalf("planTaskGraphUnified failed: %v", err)
	}
	if len(plan.Nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(plan.Nodes))
	}
	if plan.Nodes[0].Mode != "react" {
		t.Fatalf("first node mode: %q", plan.Nodes[0].Mode)
	}
	if plan.Nodes[2].Mode != "direct" {
		t.Fatalf("third node mode: %q", plan.Nodes[2].Mode)
	}
	if contract.Summary != "analyze repo" {
		t.Fatalf("contract summary: %q", contract.Summary)
	}
	if contract.ExpectedOutcome != "full analysis done" {
		t.Fatalf("contract expected_outcome: %q", contract.ExpectedOutcome)
	}
	if len(contract.RequiredTools) != 1 || contract.RequiredTools[0] != "file.read" {
		t.Fatalf("contract required_tools: %v", contract.RequiredTools)
	}
}

func TestRuntimeHandle_PlannerFailureDoesNotUseContractFallback(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = contractJSONModel{json: `not json`}
	rt.ContractModel = panicModel{t: t}
	rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "should not execute"}, rt.Tools)

	resp, err := rt.Handle(t.Context(), inbound("cli:01-no-contract-fallback", "answer a question"))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Failed {
		t.Fatalf("expected planner failure response, got %#v", resp)
	}

	state := loadState(t, rt, "cli:01-no-contract-fallback")
	if len(state.Tasks) == 0 {
		t.Fatal("expected task state to exist")
	}
	if state.Tasks[0].Graph != nil {
		t.Fatalf("planner failure should not attach fallback graph: %#v", state.Tasks[0].Graph)
	}

	data, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	trace := string(data)
	if strings.Contains(trace, "graph_planner_fallback") {
		t.Fatalf("trace should not contain graph planner fallback:\n%s", trace)
	}
	if strings.Contains(trace, "task_contract_parse_failed") || strings.Contains(trace, "task_contract_created") {
		t.Fatalf("trace should not contain old contract planning events:\n%s", trace)
	}
}

func TestRuntimeHandle_PlannerUsesAgentToolRegistry(t *testing.T) {
	rt := newTestRuntime(t)
	fullRegistry := agentcore.NewToolRegistry()
	fullRegistry.Register(runtimeNamedTool{name: "file.read", content: "ok"})
	rt.Tools = fullRegistry

	agentRegistry := agentcore.NewToolRegistry()
	rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "should not execute"}, agentRegistry)

	planJSON := `{
		"task": {
			"goal": "read a file",
			"risk": "low",
			"acceptance": "file read",
			"required_capabilities": {"tools": ["file.read"], "skills": [], "human_gates": []},
			"final_output": {"text": true, "structured": []}
		},
		"nodes": [
			{"id": "read", "type": "subtask", "mode": "react", "goal": "read file", "allowed_tools": ["file.read"], "acceptance": "file read"}
		]
	}`
	rt.Model = plannerVerifierModel{planJSON: planJSON}
	rt.ContractModel = panicModel{t: t}

	resp, err := rt.Handle(t.Context(), inbound("cli:01-agent-tools", "read a file"))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Failed {
		t.Fatalf("expected failure because file.read is not in agent registry, got %#v", resp)
	}
	if strings.TrimSpace(resp.Reply.Text) == "" {
		t.Fatalf("expected non-empty planner failure reply, got %#v", resp)
	}

	state := loadState(t, rt, "cli:01-agent-tools")
	if len(state.Tasks) == 0 {
		t.Fatal("expected task state to exist")
	}
	if state.Tasks[0].Graph != nil {
		t.Fatalf("invalid planner output should not attach graph: %#v", state.Tasks[0].Graph)
	}

	data, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	trace := string(data)
	if !strings.Contains(trace, "unified_planner_invalid_tools") {
		t.Fatalf("trace missing invalid tools event:\n%s", trace)
	}
	if strings.Contains(trace, "graph_attached") {
		t.Fatalf("trace should not attach graph after validation failure:\n%s", trace)
	}
}

func TestRuntimeHandle_PlannerOutputLandsInSession(t *testing.T) {
	rt := newTestRuntime(t)
	planJSON := `{
		"task": {
			"goal": "answer a question",
			"risk": "low",
			"acceptance": "correct answer given",
			"required_capabilities": {"tools": [], "skills": [], "human_gates": []},
			"final_output": {"text": true, "structured": []}
		},
		"nodes": [
			{"id": "answer", "type": "subtask", "mode": "direct", "goal": "answer question", "acceptance": "correct"}
		]
	}`
	rt.Model = plannerVerifierModel{planJSON: planJSON}
	rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "graph answer"}, rt.Tools)

	resp, err := rt.Handle(t.Context(), inbound("cli:01-e2e", "answer a question"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed {
		t.Fatalf("e2e task should not fail, got: %q", resp.Reply.Text)
	}

	state := loadState(t, rt, "cli:01-e2e")
	if len(state.Tasks) == 0 {
		t.Fatal("no tasks in session")
	}
	task := state.Tasks[0]
	if task.Graph == nil || len(task.Graph.Nodes) == 0 {
		t.Fatal("graph was not persisted")
	}
	if task.Graph.Nodes[0].Type != session.NodeTypeSubtask {
		t.Fatalf("expected subtask node, got %q", task.Graph.Nodes[0].Type)
	}
	if task.Graph.Nodes[0].Mode != session.NodeModeDirect {
		t.Fatalf("expected direct mode, got %q", task.Graph.Nodes[0].Mode)
	}

	if task.Execution.Contract == nil {
		t.Fatal("task acceptance contract was not persisted")
	}
	contract := *task.Execution.Contract
	if contract.Summary != "answer a question" {
		t.Fatalf("contract summary: %q", contract.Summary)
	}
	if contract.ExpectedOutcome != "correct answer given" {
		t.Fatalf("contract acceptance: %q", contract.ExpectedOutcome)
	}

	data, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	trace := string(data)
	if !strings.Contains(trace, `"type":"unified_plan_generated"`) {
		t.Fatalf("trace missing unified_plan_generated, got:\n%s", trace)
	}
	if !strings.Contains(trace, `"type":"unified_plan_validated"`) {
		t.Fatalf("trace missing unified_plan_validated, got:\n%s", trace)
	}
	if !strings.Contains(trace, `"type":"graph_attached"`) {
		t.Fatalf("trace missing graph_attached, got:\n%s", trace)
	}
	if !strings.Contains(trace, `"type":"task_contract_created"`) {
		t.Fatalf("trace missing task_contract_created, got:\n%s", trace)
	}
}

func TestRuntimeHandle_ComplexTaskReactWithAllowedTools(t *testing.T) {
	rt := newTestRuntime(t)
	planJSON := `{
		"task": {
			"goal": "analyze repo structure",
			"risk": "medium",
			"acceptance": "full analysis with entrypoints and risks",
			"required_capabilities": {
				"tools": ["file.read"],
				"skills": [],
				"human_gates": []
			},
			"final_output": {"text": true, "structured": ["architecture_summary"]}
		},
		"nodes": [
			{
				"id": "explore",
				"type": "subtask",
				"mode": "react",
				"goal": "explore repository",
				"depends": [],
				"allowed_tools": ["file.read"],
				"inputs": ["repo_path"],
				"outputs": ["file_list"],
				"acceptance": "all directories and entrypoints listed"
			},
			{
				"id": "analyze",
				"type": "subtask",
				"mode": "react",
				"goal": "analyze code patterns",
				"depends": ["explore"],
				"allowed_tools": ["file.read"],
				"inputs": ["file_list"],
				"outputs": ["patterns"],
				"acceptance": "architectural patterns and risks identified"
			},
			{
				"id": "report",
				"type": "subtask",
				"mode": "direct",
				"goal": "write final report",
				"depends": ["analyze"],
				"outputs": ["report"],
				"acceptance": "report covers entrypoints, patterns, and risks"
			}
		]
	}`
	rt.Model = plannerVerifierModel{planJSON: planJSON}
	rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "graph report done"}, rt.Tools)

	resp, err := rt.Handle(t.Context(), inbound("cli:01-complex", "analyze repo structure"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed {
		t.Fatalf("complex e2e task should not fail, got: %q", resp.Reply.Text)
	}

	state := loadState(t, rt, "cli:01-complex")
	if len(state.Tasks) == 0 {
		t.Fatal("no tasks in session")
	}
	task := state.Tasks[0]
	if task.Graph == nil || len(task.Graph.Nodes) != 3 {
		t.Fatalf("expected 3 nodes in graph, got %d", len(task.Graph.Nodes))
	}

	exploreNode := task.Graph.Nodes[0]
	if exploreNode.Type != session.NodeTypeSubtask {
		t.Fatalf("node 0 type: expected subtask, got %q", exploreNode.Type)
	}
	if exploreNode.Mode != session.NodeModeReact {
		t.Fatalf("node 0 mode: expected react, got %q", exploreNode.Mode)
	}
	if len(exploreNode.AllowedTools) != 1 || exploreNode.AllowedTools[0] != "file.read" {
		t.Fatalf("node 0 allowed_tools: %v", exploreNode.AllowedTools)
	}

	if len(exploreNode.Depends) != 0 {
		t.Fatalf("node 0 depends: should be nil, got %v", exploreNode.Depends)
	}

	analyzeNode := task.Graph.Nodes[1]
	if analyzeNode.Type != session.NodeTypeSubtask || analyzeNode.Mode != session.NodeModeReact {
		t.Fatalf("node 1: expected subtask/react, got %q/%q", analyzeNode.Type, analyzeNode.Mode)
	}
	if len(analyzeNode.Depends) != 1 || analyzeNode.Depends[0] != "explore" {
		t.Fatalf("node 1 depends: %v", analyzeNode.Depends)
	}

	reportNode := task.Graph.Nodes[2]
	if reportNode.Type != session.NodeTypeSubtask || reportNode.Mode != session.NodeModeDirect {
		t.Fatalf("node 2: expected subtask/direct, got %q/%q", reportNode.Type, reportNode.Mode)
	}
	if len(reportNode.Depends) != 1 || reportNode.Depends[0] != "analyze" {
		t.Fatalf("node 2 depends: %v", reportNode.Depends)
	}

	if task.Execution.Contract == nil {
		t.Fatal("task acceptance contract was not persisted")
	}
	contract := *task.Execution.Contract
	if contract.Summary != "analyze repo structure" {
		t.Fatalf("contract summary: %q", contract.Summary)
	}
	if !contract.RequiresTools || len(contract.RequiredTools) == 0 {
		t.Fatal("contract should require tools for complex task")
	}

	data, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	trace := string(data)
	if !strings.Contains(trace, `"type":"unified_plan_generated"`) {
		t.Fatalf("trace missing unified_plan_generated, got:\n%s", trace)
	}
	if !strings.Contains(trace, `"type":"unified_plan_validated"`) {
		t.Fatalf("trace missing unified_plan_validated, got:\n%s", trace)
	}
	if !strings.Contains(trace, `"type":"graph_attached"`) {
		t.Fatalf("trace missing graph_attached, got:\n%s", trace)
	}
}
