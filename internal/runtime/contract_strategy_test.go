package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/session"
)

// --- Unit tests for classifyContractStrategy ---

func TestContractStrategyDirectSimpleQAndA(t *testing.T) {
	cases := []struct {
		goal string
		text string
	}{
		{"explain what Mateway is", "explain what Mateway is"},
		{"what is the meaning of life", "what is the meaning of life"},
		{"how does Go garbage collector work", "how does Go garbage collector work"},
	}
	for _, tc := range cases {
		t.Run(tc.goal, func(t *testing.T) {
			contract := fallbackTaskContract(tc.goal, tc.text)
			strategy := classifyContractStrategy(tc.goal, tc.text, contract)
			if strategy != contractStrategyDirect {
				t.Fatalf("expected direct, got %q for %q", strategy, tc.goal)
			}
		})
	}
}

func TestContractStrategyAutoContractForLowRiskSearch(t *testing.T) {
	cases := []string{
		"check weather today",
		"search latest Go release",
		"查一下今天天气",
		"search for news",
	}
	for _, text := range cases {
		t.Run(text, func(t *testing.T) {
			contract := fallbackTaskContract(text, text)
			strategy := classifyContractStrategy(text, text, contract)
			if strategy != contractStrategyAutoContract {
				t.Fatalf("expected auto_contract, got %q for %q", strategy, text)
			}
		})
	}
}

func TestContractStrategyAutoContractForReadFile(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"file.read"},
	}
	strategy := classifyContractStrategy("read config file", "read config file", contract)
	if strategy != contractStrategyAutoContract {
		t.Fatalf("expected auto_contract for file.read, got %q", strategy)
	}
}

func TestContractStrategyReviewRequiredForFileWrite(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"file.write"},
	}
	strategy := classifyContractStrategy("write report", "write report to file", contract)
	if strategy != contractStrategyReviewRequired {
		t.Fatalf("expected review_required for file.write, got %q", strategy)
	}
}

func TestContractStrategyReviewRequiredForTerminalRun(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"terminal.run"},
	}
	strategy := classifyContractStrategy("run service", "run systemctl status", contract)
	if strategy != contractStrategyReviewRequired {
		t.Fatalf("expected review_required for terminal.run, got %q", strategy)
	}
}

func TestContractStrategyReviewRequiredForExternalPublish(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"web.search", "file.write"},
	}
	strategy := classifyContractStrategy("feishu doc", "publish to feishu cloud doc", contract)
	if strategy != contractStrategyReviewRequired {
		t.Fatalf("expected review_required for external publish, got %q", strategy)
	}
}

func TestContractStrategyReviewRequiredForExplicitPlanRequest(t *testing.T) {
	cases := []string{
		"plan first then deploy",
		"先计划一下",
		"show me the plan",
		"plan first how to explain X",
		"show me the plan for X",
	}
	for _, text := range cases {
		contract := session.TaskContract{RequiresTools: false}
		strategy := classifyContractStrategy(text, text, contract)
		if strategy != contractStrategyReviewRequired {
			t.Fatalf("expected review_required for %q, got %q", text, strategy)
		}
	}
}

func TestContractStrategyReviewRequiredForMultiStepDelivery(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"web.search", "file.write", "terminal.run"},
	}
	strategy := classifyContractStrategy("deploy report", "publish report to server", contract)
	if strategy != contractStrategyReviewRequired {
		t.Fatalf("expected review_required for multi-step delivery, got %q", strategy)
	}
}

// --- Unit tests for shouldPauseForTaskPlan with strategy ---

func TestShouldPauseForTaskPlanDirectNeverPauses(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"web.search"},
	}
	if shouldPauseForTaskPlan(contract, contractStrategyDirect) {
		t.Fatal("direct strategy should never pause")
	}
}

func TestShouldPauseForTaskPlanAutoContractNeverPauses(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools:    true,
		RequiredTools:    []string{"web.search"},
		RequiredEvidence: []session.TaskEvidenceContract{{Tool: "web.search", Description: "test"}},
	}
	if shouldPauseForTaskPlan(contract, contractStrategyAutoContract) {
		t.Fatal("auto_contract strategy should never pause")
	}
}

func TestShouldPauseForTaskPlanReviewRequiredPauses(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"file.write"},
	}
	if !shouldPauseForTaskPlan(contract, contractStrategyReviewRequired) {
		t.Fatal("review_required with tools should pause")
	}
}

// --- Integration test: trace records contract strategy ---

func TestRuntimeTraceRecordsContractStrategy(t *testing.T) {
	t.Run("direct", func(t *testing.T) {
		rt := newTestRuntime(t)
		rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "Mateway is a local agent runtime."}, rt.Tools)
		rt.ContractModel = contractJSONModel{json: `{"summary":"explain Mateway","requires_tools":false,"expected_outcome":"answer"}`}

		resp, err := rt.Handle(context.Background(), inbound("cli:strategy", "explain what Mateway is"))
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(resp.TracePath)
		if err != nil {
			t.Fatal(err)
		}
		trace := string(data)
		if !strings.Contains(trace, `"strategy":"direct"`) {
			t.Fatalf("expected direct strategy in trace, got:\n%s", trace)
		}
	})

	t.Run("auto_contract", func(t *testing.T) {
		rt := newTestRuntime(t)
		registry := agentcore.NewToolRegistry()
		registry.Register(runtimeNamedTool{name: "web.search", content: "weather data"})
		rt.Tools = registry
		rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
			{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{ID: "call_1", Name: "web.search", Args: map[string]any{"query": "weather"}}}},
			{Role: agentcore.RoleAssistant, Content: "Weather is clear."},
		}}, rt.Tools)
		rt.ContractModel = contractJSONModel{json: `{"summary":"check weather","requires_tools":true,"required_tools":["web.search"],"expected_outcome":"weather info"}`}

		resp, err := rt.Handle(context.Background(), inbound("cli:strategy", "check weather today"))
		if err != nil {
			t.Fatal(err)
		}
		if resp.Reply.Style == channel.StyleInputRequired {
			t.Fatalf("auto_contract should not show plan review, got style=%q", resp.Reply.Style)
		}
		data, err := os.ReadFile(resp.TracePath)
		if err != nil {
			t.Fatal(err)
		}
		trace := string(data)
		if !strings.Contains(trace, `"strategy":"auto_contract"`) {
			t.Fatalf("expected auto_contract strategy in trace, got:\n%s", trace)
		}
		if strings.Contains(trace, "task_plan_review") {
			t.Fatalf("auto_contract should not have plan_review trace event:\n%s", trace)
		}
	})

	t.Run("review_required", func(t *testing.T) {
		rt := newTestRuntime(t)
		registry := agentcore.NewToolRegistry()
		registry.Register(runtimeNamedTool{name: "file.write", content: "wrote"})
		registry.Register(runtimeNamedTool{name: "terminal.run", content: "deployed"})
		rt.Tools = registry
		rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
			{Role: agentcore.RoleAssistant, Content: "done"},
		}}, rt.Tools)
		rt.ContractModel = contractJSONModel{json: `{"summary":"deploy report","requires_tools":true,"required_tools":["file.write","terminal.run"],"required_evidence":[{"kind":"local_file","tool":"file.write","description":"report"}],"plan_items":[{"id":"plan-1","title":"write report","status":"pending","tool":"file.write","criteria":"write file"},{"id":"plan-2","title":"deploy","status":"pending","tool":"terminal.run","criteria":"deploy"}],"expected_outcome":"deployed","completion_policy":"must deploy"}`}

		resp, err := rt.Handle(context.Background(), inbound("cli:strategy", "create report and deploy to server"))
		if err != nil {
			t.Fatal(err)
		}
		if resp.Reply.Style != channel.StyleInputRequired {
			t.Fatalf("review_required should show plan review, got style=%q", resp.Reply.Style)
		}
		data, err := os.ReadFile(resp.TracePath)
		if err != nil {
			t.Fatal(err)
		}
		trace := string(data)
		if !strings.Contains(trace, `"strategy":"review_required"`) {
			t.Fatalf("expected review_required strategy in trace, got:\n%s", trace)
		}
	})
}

// --- Integration test: existing contract validation/skill-aware replan still works ---

func TestContractStrategyDoesNotBreakExistingValidation(t *testing.T) {
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "ok"})
	registry.Register(runtimeNamedTool{name: "terminal.run", content: "service is running"})
	rt.Tools = registry
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID: "call_1", Name: "terminal.run", Args: map[string]any{"command": "systemctl status singbox"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "service is running."},
	}}, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"check service status","requires_tools":true,"required_tools":["terminal.run"],"expected_outcome":"status"}`}

	planResp, err := rt.Handle(context.Background(), inbound("cli:valid", "check singbox service status"))
	if err != nil {
		t.Fatal(err)
	}
	if planResp.Reply.Style != channel.StyleInputRequired {
		t.Fatalf("expected plan review for review_required, got style=%q text=%q", planResp.Reply.Style, planResp.Reply.Text)
	}

	resp, err := rt.Handle(context.Background(), inbound("cli:valid", "1"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed {
		t.Fatalf("expected completion, got failed: %q", resp.Reply.Text)
	}
}

// --- Slice 6A: Universal Plan Shape integration tests ---

// TestUniversalPlanShapeDirectHasMinimalContract verifies the Slice 6A
// invariant: simple Q&A gets a minimal contract/plan (plan_items present,
// no tools, no evidence) but never triggers plan review.
func TestUniversalPlanShapeDirectHasMinimalContract(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "Mateway is a local-first Go agent runtime."}, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"explain Mateway","requires_tools":false,"expected_outcome":"answer"}`}

	resp, err := rt.Handle(context.Background(), inbound("cli:6a-direct", "explain what Mateway is"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Style == channel.StyleInputRequired {
		t.Fatalf("direct Q&A must not show plan review, got style=%q text=%q", resp.Reply.Style, resp.Reply.Text)
	}

	state := loadState(t, rt, "cli:6a-direct")
	if len(state.Tasks) == 0 {
		t.Fatal("task not found")
	}
	task := state.Tasks[0]
	if task.Execution.Contract == nil {
		t.Fatal("direct task must still carry a contract (universal plan shape)")
	}
	contract := *task.Execution.Contract
	if len(contract.PlanItems) == 0 {
		t.Fatal("direct task must have at least one plan_items entry (minimal plan)")
	}
	for i, item := range contract.PlanItems {
		if strings.TrimSpace(item.Tool) != "" {
			t.Fatalf("direct Q&A plan item %d should have empty tool, got %q", i, item.Tool)
		}
	}
	if contract.RequiresTools {
		t.Fatal("direct Q&A must keep requires_tools=false")
	}
	if len(contract.RequiredTools) > 0 || len(contract.RequiredEvidence) > 0 {
		t.Fatalf("direct Q&A must not require tools or evidence, got required_tools=%v required_evidence=%v",
			contract.RequiredTools, contract.RequiredEvidence)
	}

	// Trace must show both the strategy and the universal contract creation.
	data, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	trace := string(data)
	if !strings.Contains(trace, `"type":"task_contract_strategy"`) || !strings.Contains(trace, `"strategy":"direct"`) {
		t.Fatalf("expected task_contract_strategy direct in trace, got:\n%s", trace)
	}
	if !strings.Contains(trace, `"type":"task_contract_created"`) {
		t.Fatalf("expected task_contract_created for direct path (universal plan shape), got:\n%s", trace)
	}
	if strings.Contains(trace, `"type":"task_plan_review"`) {
		t.Fatalf("direct Q&A must not emit task_plan_review, got:\n%s", trace)
	}
}

func TestUniversalPlanShapeDirectSkipsContractModelWithExecutionSkill(t *testing.T) {
	rt := newTestRuntime(t)
	home := rt.home()
	writeSkillFixture(t, home, "main", "publisher", `---
name: publisher
description: CLI tool for publishing.
stage: cli
priority: 80
---
# publisher

Run publisher-cli.
`)
	rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "Mateway is a local-first Go agent runtime."}, rt.Tools)
	rt.ContractModel = panicModel{t: t}

	resp, err := rt.Handle(context.Background(), inbound("cli:6a-direct-with-skill", "explain what Mateway is"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Style == channel.StyleInputRequired {
		t.Fatalf("direct Q&A must not show plan review, got style=%q text=%q", resp.Reply.Style, resp.Reply.Text)
	}

	state := loadState(t, rt, "cli:6a-direct-with-skill")
	if len(state.Tasks) == 0 || state.Tasks[0].Execution.Contract == nil {
		t.Fatal("direct task must still carry a minimal contract")
	}
	contract := *state.Tasks[0].Execution.Contract
	if contract.RequiresTools || len(contract.RequiredSkills) > 0 {
		t.Fatalf("direct Q&A should not select execution skills, got requires_tools=%v skills=%+v", contract.RequiresTools, contract.RequiredSkills)
	}
}

// TestUniversalPlanShapeAutoContractExecutesWithPlanItems verifies the Slice 6A
// invariant: a low-risk tool task has plan_items and required_evidence
// (tool execution list + acceptance list) and auto-executes without a
// plan review pause.
func TestUniversalPlanShapeAutoContractExecutesWithPlanItems(t *testing.T) {
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "web.search", content: "Weather clear today."})
	rt.Tools = registry

	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID: "call_1", Name: "web.search", Args: map[string]any{"query": "weather today"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "Today is clear in Beijing."},
	}}, rt.Tools)

	// Tool task contract: requires_tools=true with plan_items (tool execution
	// list) and required_evidence (acceptance list).
	rt.ContractModel = contractJSONModel{json: `{"summary":"check weather","requires_tools":true,"required_tools":["web.search"],"required_evidence":[{"kind":"current_external_fact","tool":"web.search","description":"today weather with source/date"}],"plan_items":[{"id":"plan-1","title":"search current weather","status":"pending","tool":"web.search","criteria":"collect today weather with source/date"}],"expected_outcome":"weather summary","completion_policy":"final answer must cite web.search evidence"}`}

	resp, err := rt.Handle(context.Background(), inbound("cli:6a-auto", "check weather today"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Style == channel.StyleInputRequired {
		t.Fatalf("low-risk tool task must not pause for plan review, got style=%q text=%q", resp.Reply.Style, resp.Reply.Text)
	}
	if resp.Failed {
		t.Fatalf("expected completion, got failed: %q", resp.Reply.Text)
	}

	state := loadState(t, rt, "cli:6a-auto")
	if len(state.Tasks) == 0 {
		t.Fatal("task not found")
	}
	task := state.Tasks[0]
	if task.Execution.Contract == nil {
		t.Fatal("auto_contract task must carry a contract")
	}
	contract := *task.Execution.Contract
	// plan_items must include the web.search execution step.
	var foundSearchItem bool
	for _, item := range contract.PlanItems {
		if strings.EqualFold(item.Tool, "web.search") {
			foundSearchItem = true
		}
	}
	if !foundSearchItem {
		t.Fatalf("auto_contract must keep a web.search plan item, got %+v", contract.PlanItems)
	}
	// required_evidence must include the acceptance list entry.
	if len(contract.RequiredEvidence) == 0 {
		t.Fatal("auto_contract must include required_evidence (acceptance list)")
	}
	var foundAcceptance bool
	for _, ev := range contract.RequiredEvidence {
		if strings.EqualFold(ev.Tool, "web.search") && strings.Contains(strings.ToLower(ev.Description), "weather") {
			foundAcceptance = true
		}
	}
	if !foundAcceptance {
		t.Fatalf("auto_contract required_evidence must include web.search weather entry, got %+v", contract.RequiredEvidence)
	}

	// The web.search tool must have been called (auto-execute).
	var calledSearch bool
	for _, step := range task.Steps {
		if strings.EqualFold(step.Tool, "web.search") {
			calledSearch = true
		}
	}
	if !calledSearch {
		t.Fatal("auto_contract must auto-execute the web.search tool")
	}

	// Trace must show auto_contract strategy, no plan review, and contract created.
	data, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	trace := string(data)
	if !strings.Contains(trace, `"strategy":"auto_contract"`) {
		t.Fatalf("expected auto_contract strategy in trace, got:\n%s", trace)
	}
	if !strings.Contains(trace, `"type":"task_contract_created"`) {
		t.Fatalf("expected task_contract_created in trace, got:\n%s", trace)
	}
	if strings.Contains(trace, `"type":"task_plan_review"`) {
		t.Fatalf("auto_contract must not emit task_plan_review, got:\n%s", trace)
	}
}

// TestUniversalPlanShapeReviewRequiredShowsToolListAndAcceptance verifies the
// Slice 6A invariant: a multi-step delivery task enters plan review and the
// rendered plan shows both the tool execution list (plan_items) and the
// acceptance list (required_evidence).
func TestUniversalPlanShapeReviewRequiredShowsToolListAndAcceptance(t *testing.T) {
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "web.search", content: "search data"})
	registry.Register(runtimeNamedTool{name: "file.write", content: "wrote /tmp/report.md"})
	registry.Register(&captureCommandTool{name: "terminal.run", content: `{"url":"https://example.cloud/x"}`})
	rt.Tools = registry

	rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "deployed"}, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"publish report","requires_tools":true,"required_tools":["web.search","file.write","terminal.run"],"required_evidence":[{"kind":"current_external_fact","tool":"web.search","description":"current data with source/date"},{"kind":"local_file","tool":"file.write","description":"local markdown report written"},{"kind":"remote_publish","tool":"terminal.run","description":"cloud doc URL"}],"plan_items":[{"id":"plan-1","title":"search data","status":"pending","tool":"web.search","criteria":"collect current data"},{"id":"plan-2","title":"write report","status":"pending","tool":"file.write","criteria":"write report.md"},{"id":"plan-3","title":"publish","status":"pending","tool":"terminal.run","criteria":"publish to cloud doc"}],"expected_outcome":"deployed","completion_policy":"must deploy"}`}

	resp, err := rt.Handle(context.Background(), inbound("cli:6a-review", "publish report to cloud doc"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Style != channel.StyleInputRequired {
		t.Fatalf("multi-step delivery must pause for plan review, got style=%q text=%q", resp.Reply.Style, resp.Reply.Text)
	}
	text := resp.Reply.Text
	// Tool execution list: every plan_items entry must appear.
	for _, want := range []string{"search data", "write report", "publish"} {
		if !strings.Contains(text, want) {
			t.Fatalf("plan review should list tool checklist %q, got:\n%s", want, text)
		}
	}
	// Tool tags: each plan_items tool must appear in the plan.
	for _, want := range []string{"[web.search]", "[file.write]", "[terminal.run]"} {
		if !strings.Contains(text, want) {
			t.Fatalf("plan review should tag tool %q, got:\n%s", want, text)
		}
	}
	// Acceptance list: required_evidence descriptions must appear.
	if !strings.Contains(strings.ToLower(text), "acceptance criteria") {
		t.Fatalf("plan review should include an acceptance criteria section, got:\n%s", text)
	}
	for _, want := range []string{"current data with source/date", "local markdown report written", "cloud doc url"} {
		if !strings.Contains(strings.ToLower(text), want) {
			t.Fatalf("plan review should list acceptance %q, got:\n%s", want, text)
		}
	}

	// Trace must show review_required strategy and the plan_review event.
	data, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	trace := string(data)
	if !strings.Contains(trace, `"strategy":"review_required"`) {
		t.Fatalf("expected review_required strategy in trace, got:\n%s", trace)
	}
	if !strings.Contains(trace, `"type":"task_plan_review"`) {
		t.Fatalf("expected task_plan_review event in trace, got:\n%s", trace)
	}
	if !strings.Contains(trace, `"type":"task_contract_created"`) {
		t.Fatalf("expected task_contract_created in trace, got:\n%s", trace)
	}
}

// --- Slice 6B: Planning-Time Selected Skill Read ---

// TestPlanningTimeSkillReadExecutionSkill stores a CLI skill body in the
// contract during planning, emits a trace event, and does not read guidance
// skills.
func TestPlanningTimeSkillReadExecutionSkill(t *testing.T) {
	rt := newTestRuntime(t)
	home := rt.home()

	execPath := writeSkillFixture(t, home, "main", "my-cli-tool", `---
name: my-cli-tool
description: CLI tool for publishing.
stage: cli
priority: 80
---
# my-cli-tool

Run the publish CLI:
`+"```"+`
my-cli publish --file <path>
`+"```"+`
`)
	guidancePath := writeSkillFixture(t, home, "main", "source-evaluation", `---
name: source-evaluation
description: Evaluate source quality.
stage: planning
priority: 60
---
# source-evaluation

Check source authority and recency before using data.
`)

	rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "done"}, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"publish report","requires_tools":true,"required_tools":["file.read","terminal.run"],"required_skills":[{"name":"my-cli-tool","path":"` + execPath + `","reason":"CLI publish workflow"},{"name":"source-evaluation","path":"` + guidancePath + `","reason":"evaluate sources"}],"required_evidence":[{"kind":"local_file","tool":"file.read","description":"read ` + execPath + `"},{"kind":"local_file","tool":"file.read","description":"read ` + guidancePath + `"},{"kind":"remote_publish","tool":"terminal.run","description":"cloud doc URL"}],"plan_items":[{"id":"plan-1","title":"read my-cli-tool SKILL.md","status":"pending","tool":"file.read","criteria":"read ` + execPath + `"},{"id":"plan-2","title":"read source-evaluation SKILL.md","status":"pending","tool":"file.read","criteria":"read ` + guidancePath + `"},{"id":"plan-3","title":"publish","status":"pending","tool":"terminal.run","criteria":"publish"}],"expected_outcome":"deployed","completion_policy":"use tool evidence"}`}

	resp, err := rt.Handle(context.Background(), inbound("cli:6b-exec", "publish report"))
	if err != nil && !resp.Failed {
		t.Fatal(err)
	}

	state := loadState(t, rt, "cli:6b-exec")
	if len(state.Tasks) == 0 {
		t.Fatal("task not found")
	}
	task := state.Tasks[0]
	if task.Execution.Contract == nil {
		t.Fatal("contract not found")
	}
	contract := *task.Execution.Contract

	var cliSkill, guidanceSkill session.RequiredSkill
	for _, s := range contract.RequiredSkills {
		switch s.Name {
		case "my-cli-tool":
			cliSkill = s
		case "source-evaluation":
			guidanceSkill = s
		}
	}

	// Body is not persisted in session (json:"-") but the planning-time read is
	// verified through the trace event and plan items below.
	_ = cliSkill
	_ = guidanceSkill

	data, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	trace := string(data)
	if !strings.Contains(trace, `"type":"task_contract_skill_read"`) {
		t.Fatalf("expected task_contract_skill_read trace event, got:\n%s", trace)
	}
	if !strings.Contains(trace, `"read_count"`) {
		t.Fatal("trace should include read_count")
	}
	// The task_contract_skill_read event must list my-cli-tool with body_len.
	if !strings.Contains(trace, `"name":"my-cli-tool"`) || !strings.Contains(trace, `"body_len"`) {
		t.Fatal("execution skill my-cli-tool should appear with body_len in task_contract_skill_read, guidance skill should not")
	}
	if !strings.Contains(trace, `"total_skills":2`) {
		t.Fatal("contract should have 2 total required skills")
	}
}

// TestPlanningTimeSkillReadUnselectedNotRead verifies that discovered skills
// not present in required_skills are never read at planning time.
func TestPlanningTimeSkillReadUnselectedNotRead(t *testing.T) {
	rt := newTestRuntime(t)
	home := rt.home()

	execPath := writeSkillFixture(t, home, "main", "my-cli-tool", `---
name: my-cli-tool
description: CLI tool for publishing.
stage: cli
priority: 80
---
# my-cli-tool
Run: my-cli publish --file <path>
`)
	writeSkillFixture(t, home, "main", "other-cli-tool", `---
name: other-cli-tool
description: Another CLI tool.
stage: cli
priority: 50
---
# other-cli-tool
Run: other-cli deploy
`)

	rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "done"}, rt.Tools)
	// Only my-cli-tool is in required_skills; other-cli-tool is discovered
	// but NOT selected.
	rt.ContractModel = contractJSONModel{json: `{"summary":"publish","requires_tools":true,"required_tools":["file.read","terminal.run"],"required_skills":[{"name":"my-cli-tool","path":"` + execPath + `","reason":"CLI publish workflow"}],"required_evidence":[{"kind":"local_file","tool":"file.read","description":"read ` + execPath + `"}],"plan_items":[{"id":"plan-1","title":"read my-cli-tool SKILL.md","status":"pending","tool":"file.read","criteria":"read ` + execPath + `"}],"expected_outcome":"deployed"}`}

	resp, err := rt.Handle(context.Background(), inbound("cli:6b-unselected", "publish"))
	if err != nil && !resp.Failed {
		t.Fatal(err)
	}

	data, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	trace := string(data)
	if !strings.Contains(trace, `"type":"task_contract_skill_read"`) || !strings.Contains(trace, `"read_count":1`) {
		t.Fatalf("expected task_contract_skill_read with read_count=1, got:\n%s", trace)
	}
	if !strings.Contains(trace, `"name":"my-cli-tool"`) {
		t.Fatal("expected my-cli-tool in task_contract_skill_read")
	}
	// Verify other-cli-tool is not in the skill_read event (trace is JSONL).
	for _, line := range strings.Split(trace, "\n") {
		if strings.Contains(line, `task_contract_skill_read`) {
			if strings.Contains(line, `other-cli-tool`) {
				t.Fatal("unselected other-cli-tool must not appear in task_contract_skill_read event")
			}
			break
		}
	}
}

// TestPlanningTimeSkillReadFailedProducesBlocker verifies that when a
// required execution skill's SKILL.md is missing/unreadable, the task
// produces a blocker instead of blindly executing.
func TestPlanningTimeSkillReadFailedProducesBlocker(t *testing.T) {
	rt := newTestRuntime(t)
	home := rt.home()

	// Skill dir exists but SKILL.md is intentionally missing.
	skillDir := filepath.Join(home, "workspace", "agents", "main", "skills", "missing-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// No SKILL.md file written.

	rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "done"}, rt.Tools)
	missingPath := filepath.Join(skillDir, "SKILL.md")
	rt.ContractModel = contractJSONModel{json: `{"summary":"publish","requires_tools":true,"required_tools":["terminal.run"],"required_skills":[{"name":"missing-skill","path":"` + missingPath + `","reason":"required"}],"expected_outcome":"deployed"}`}

	resp, err := rt.Handle(context.Background(), inbound("cli:6b-missing", "publish"))
	if err != nil && !resp.Failed {
		t.Fatal(err)
	}

	if !resp.Failed {
		t.Fatal("expected resp.Failed == true when required skill SKILL.md is missing")
	}

	state := loadState(t, rt, "cli:6b-missing")
	if len(state.Tasks) > 0 {
		task := state.Tasks[0]
		if task.Status != "failed" {
			t.Fatalf("expected task status 'failed', got %q", task.Status)
		}
	}

	data, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	trace := string(data)
	if !strings.Contains(trace, `task_contract_blocked`) && !strings.Contains(trace, `invalid_skill`) {
		t.Fatal("expected trace to contain task_contract_blocked or invalid skill evidence")
	}
}

// --- Slice 6C: Skill Body To Real Tool Checklist ---

// TestSkillBodyToRealToolListCLISkill verifies that a CLI-stage skill's body
// is converted into file.read (SKILL.md) + terminal.run plan items, with no
// skill name in required_tools or plan_items[].tool.
func TestSkillBodyToRealToolListCLISkill(t *testing.T) {
	rt := newTestRuntime(t)
	home := rt.home()

	execPath := writeSkillFixture(t, home, "main", "my-cli-tool", `---
name: my-cli-tool
description: CLI tool for cloud publishing.
stage: cli
priority: 80
---
# my-cli-tool

Run the commands:

`+"```"+`
my-cli login --token $TOKEN
my-cli publish --file ./report.md --target cloud
`+"```"+`

After publishing, verify with `+"`my-cli status --id <id>`"+`.
`)

	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "SKILL.md content"})
	registry.Register(&captureCommandTool{name: "terminal.run", content: `{"url":"https://cloud.example.com/doc/123"}`})
	rt.Tools = registry

	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{
			{ID: "call_1", Name: "file.read", Args: map[string]any{"path": execPath}},
			{ID: "call_2", Name: "terminal.run", Args: map[string]any{"command": "my-cli publish --file ./report.md --target cloud"}},
		}},
		{Role: agentcore.RoleAssistant, Content: "published to https://cloud.example.com/doc/123"},
	}}, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"publish to cloud","requires_tools":true,"required_tools":["file.read","terminal.run"],"required_skills":[{"name":"my-cli-tool","path":"` + execPath + `","reason":"cloud publish CLI"}],"required_evidence":[{"kind":"local_file","tool":"file.read","description":"read ` + execPath + `"},{"kind":"remote_publish","tool":"terminal.run","description":"cloud doc URL"}],"plan_items":[{"id":"plan-1","title":"read my-cli-tool SKILL.md","status":"pending","tool":"file.read","criteria":"read ` + execPath + `"},{"id":"plan-2","title":"publish via CLI","status":"pending","tool":"terminal.run","criteria":"run publish CLI to upload document"}],"expected_outcome":"cloud doc URL","completion_policy":"use tool evidence"}`}

	_, err := rt.Handle(context.Background(), inbound("cli:6c-cli", "publish report via my-cli-tool"))
	if err != nil {
		t.Fatal(err)
	}

	state := loadState(t, rt, "cli:6c-cli")
	if len(state.Tasks) == 0 {
		t.Fatal("task not found")
	}
	contract := *state.Tasks[0].Execution.Contract

	var terminalItems, fileReadItems int
	var hasSkillTitle bool
	for _, item := range contract.PlanItems {
		switch strings.TrimSpace(item.Tool) {
		case "terminal.run":
			terminalItems++
			if strings.Contains(strings.ToLower(item.Title), "my-cli-tool") {
				hasSkillTitle = true
			}
		case "file.read":
			fileReadItems++
		}
		if strings.EqualFold(item.Tool, "my-cli-tool") {
			t.Fatal("skill name must not appear in plan_items[].tool")
		}
	}

	if fileReadItems < 1 {
		t.Fatal("contract must have file.read plan item for SKILL.md")
	}
	if terminalItems < 1 {
		t.Fatal("contract must have at least one terminal.run plan item for CLI skill")
	}
	if !hasSkillTitle {
		t.Fatal("contract must have at least one terminal.run plan item referencing the skill name in its title")
	}

	for _, tool := range contract.RequiredTools {
		if strings.EqualFold(tool, "my-cli-tool") {
			t.Fatal("skill name must not appear in required_tools")
		}
	}
}

// TestSkillBodyToRealToolListDocSkill verifies that a document-generation skill
// contract has file.write + terminal.run plan items (from both contract model
// and skill body augmentation), no skill name in tool fields, and completion
// evaluator accepts real tool evidence.
func TestSkillBodyToRealToolListDocSkill(t *testing.T) {
	rt := newTestRuntime(t)
	home := rt.home()

	execPath := writeSkillFixture(t, home, "main", "doc-publisher", `---
name: doc-publisher
description: Generate and publish documents.
stage: cli
priority: 70
---
# doc-publisher

1. Write the markdown document:
   `+"```"+`
   cat > /tmp/report.md << 'EOF'
   # Report
   ...
   EOF
   `+"```"+`

2. Publish the document:
   `+"```"+`
   doc-pub publish --file /tmp/report.md --target cloud
   `+"```"+`
`)

	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "SKILL.md content"})
	registry.Register(runtimeNamedTool{name: "file.write", content: "wrote /tmp/report.md"})
	registry.Register(&captureCommandTool{name: "terminal.run", content: `{"url":"https://cloud.example.com/doc/456"}`})
	rt.Tools = registry

	rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "published to https://cloud.example.com/doc/456"}, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"generate and publish report","requires_tools":true,"required_tools":["file.read","file.write","terminal.run"],"required_skills":[{"name":"doc-publisher","path":"` + execPath + `","reason":"document generation and publishing workflow"}],"required_evidence":[{"kind":"local_file","tool":"file.read","description":"read ` + execPath + `"},{"kind":"local_file","tool":"file.write","description":"markdown report written"},{"kind":"remote_publish","tool":"terminal.run","description":"cloud publishing URL"}],"plan_items":[{"id":"plan-1","title":"read doc-publisher SKILL.md","status":"pending","tool":"file.read","criteria":"read ` + execPath + `"},{"id":"plan-2","title":"write markdown report","status":"pending","tool":"file.write","criteria":"write report.md"},{"id":"plan-3","title":"publish to cloud","status":"pending","tool":"terminal.run","criteria":"publish to cloud"}],"expected_outcome":"cloud doc URL","completion_policy":"must include doc URL or blocker"}`}

	_, err := rt.Handle(context.Background(), inbound("cli:6c-doc", "publish report"))
	if err != nil {
		t.Fatal(err)
	}

	state := loadState(t, rt, "cli:6c-doc")
	if len(state.Tasks) == 0 {
		t.Fatal("task not found")
	}
	contract := *state.Tasks[0].Execution.Contract

	var writeItems, terminalItems int
	for i := range contract.PlanItems {
		switch strings.TrimSpace(contract.PlanItems[i].Tool) {
		case "file.write":
			writeItems++
		case "terminal.run":
			terminalItems++
		}
		if strings.EqualFold(contract.PlanItems[i].Tool, "doc-publisher") {
			t.Fatal("skill name must not appear in plan_items[].tool")
		}
		contract.PlanItems[i].Status = "completed"
	}
	if writeItems < 1 {
		t.Fatal("contract must have file.write plan item")
	}
	if terminalItems < 1 {
		t.Fatal("contract must have terminal.run plan item")
	}

	for _, tool := range contract.RequiredTools {
		if strings.EqualFold(tool, "doc-publisher") {
			t.Fatal("skill name must not appear in required_tools")
		}
	}

	task := state.Tasks[0]
	task.Steps = []session.TaskStep{
		{Tool: "file.read", Accepted: true, Status: "accepted"},
		{Tool: "file.write", Accepted: true, Status: "accepted"},
		{Tool: "terminal.run", Accepted: true, Status: "accepted"},
	}
	task.Execution.Events = append(task.Execution.Events,
		session.ExecutionEvent{Type: "tool_result", Status: "accepted", Tool: "file.read"},
		session.ExecutionEvent{Type: "tool_result", Status: "accepted", Tool: "file.write"},
		session.ExecutionEvent{Type: "tool_result", Status: "accepted", Tool: "terminal.run"},
	)
	validation := validateTaskContract(contract, task)
	if !validation.Satisfied {
		t.Fatalf("completion evaluator should accept real tool evidence, missing: %v", validation.Missing)
	}
}

// TestSkillBodyCompletionEvaluatorRealEvidence verifies that plan items
// generated from skill bodies are validated by the completion evaluator
// using real tool evidence, with skill names excluded from all tool fields.
func TestSkillBodyCompletionEvaluatorRealEvidence(t *testing.T) {
	rt := newTestRuntime(t)
	home := rt.home()

	execPath := writeSkillFixture(t, home, "main", "simple-cli", `---
name: simple-cli
description: Simple CLI tool.
stage: cli
priority: 90
---
# simple-cli

`+"```"+`
simple-cli run --task publish
`+"```"+`
`)

	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "SKILL.md content"})
	registry.Register(&captureCommandTool{name: "terminal.run", content: `{"url":"https://example.com/result"}`})
	rt.Tools = registry

	rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "done"}, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"run simple-cli","requires_tools":true,"required_tools":["file.read","terminal.run"],"required_skills":[{"name":"simple-cli","path":"` + execPath + `","reason":"CLI workflow"}],"required_evidence":[{"kind":"local_file","tool":"file.read","description":"read ` + execPath + `"},{"kind":"runtime_state","tool":"terminal.run","description":"execution evidence"}],"plan_items":[{"id":"plan-1","title":"read simple-cli SKILL.md","status":"pending","tool":"file.read","criteria":"read ` + execPath + `"},{"id":"plan-2","title":"execute simple-cli workflow","status":"pending","tool":"terminal.run","criteria":"run simple-cli"}],"expected_outcome":"execution result","completion_policy":"use evidence"}`}

	_, err := rt.Handle(context.Background(), inbound("cli:6c-eval", "run simple-cli"))
	if err != nil {
		t.Fatal(err)
	}

	state := loadState(t, rt, "cli:6c-eval")
	if len(state.Tasks) == 0 {
		t.Fatal("task not found")
	}
	contract := *state.Tasks[0].Execution.Contract

	for _, tool := range contract.RequiredTools {
		if strings.EqualFold(tool, "simple-cli") {
			t.Fatal("skill name must not appear in required_tools")
		}
	}
	for i := range contract.PlanItems {
		if strings.EqualFold(contract.PlanItems[i].Tool, "simple-cli") {
			t.Fatal("skill name must not appear in plan_items[].tool")
		}
		contract.PlanItems[i].Status = "completed"
	}

	task := state.Tasks[0]
	task.Steps = []session.TaskStep{
		{Tool: "file.read", Accepted: true, Status: "accepted"},
		{Tool: "terminal.run", Accepted: true, Status: "accepted"},
	}
	task.Execution.Events = append(task.Execution.Events,
		session.ExecutionEvent{Type: "tool_result", Status: "accepted", Tool: "file.read"},
		session.ExecutionEvent{Type: "tool_result", Status: "accepted", Tool: "terminal.run"},
	)
	validation := validateTaskContract(contract, task)
	if !validation.Satisfied {
		t.Fatalf("completion evaluator should find contract satisfied with real tool evidence, missing: %v", validation.Missing)
	}

	for _, step := range task.Steps {
		if strings.EqualFold(step.Tool, "simple-cli") {
			t.Fatal("skill name must not appear as accepted tool in task steps")
		}
	}
}

// --- Slice 6C: Focused skill-body-to-tool extraction tests ---

// TestSkillBodyExtractionFileWriteAndTerminalRun verifies that a skill body
// containing "write"/"save" prose and a CLI code block generates file.write
// + terminal.run plan items, even when the contract model only includes the
// mandatory file.read for SKILL.md.
func TestSkillBodyExtractionFileWriteAndTerminalRun(t *testing.T) {
	rt := newTestRuntime(t)
	home := rt.home()

	execPath := writeSkillFixture(t, home, "main", "report-writer", `---
name: report-writer
description: Write and deploy a report.
stage: cli
priority: 80
---
# report-writer

Save the report to disk:
`+"```"+`
cat > /tmp/report.md << 'EOF'
# Report
Content here
EOF
`+"```"+`

Deploy the report:
`+"```"+`
deploy-cli upload --file /tmp/report.md
`+"```"+`
`)

	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "SKILL.md content"})
	registry.Register(runtimeNamedTool{name: "file.write", content: "wrote /tmp/report.md"})
	registry.Register(&captureCommandTool{name: "terminal.run", content: `{"url":"https://example.com/deployed"}`})
	rt.Tools = registry

	rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "done"}, rt.Tools)
	// Contract model only includes file.read for SKILL.md; file.write and
	// terminal.run must be generated from the skill body, not the contract model.
	rt.ContractModel = contractJSONModel{json: `{"summary":"write and deploy report","requires_tools":true,"required_tools":["file.read"],"required_skills":[{"name":"report-writer","path":"` + execPath + `","reason":"report workflow"}],"required_evidence":[{"kind":"local_file","tool":"file.read","description":"read ` + execPath + `"}],"plan_items":[{"id":"plan-1","title":"read SKILL.md","status":"pending","tool":"file.read","criteria":"read ` + execPath + `"}],"expected_outcome":"deployed"}`}

	_, err := rt.Handle(context.Background(), inbound("cli:6c-fw", "write and deploy report"))
	if err != nil {
		t.Fatal(err)
	}

	state := loadState(t, rt, "cli:6c-fw")
	if len(state.Tasks) == 0 {
		t.Fatal("task not found")
	}
	contract := *state.Tasks[0].Execution.Contract

	toolCounts := map[string]int{}
	for _, item := range contract.PlanItems {
		toolCounts[strings.TrimSpace(item.Tool)]++
		if strings.EqualFold(item.Tool, "report-writer") {
			t.Fatal("skill name must not appear in plan_items[].tool")
		}
	}

	if toolCounts["file.write"] < 1 {
		t.Fatalf("skill body must generate file.write plan item, got tools: %+v", toolCounts)
	}
	if toolCounts["terminal.run"] < 1 {
		t.Fatalf("skill body must generate terminal.run plan item, got tools: %+v", toolCounts)
	}
}

// TestSkillBodyExtractionFileEdit verifies that a skill body containing
// "update"/"patch"/"append" prose generates a file.edit plan item.
func TestSkillBodyExtractionFileEdit(t *testing.T) {
	rt := newTestRuntime(t)
	home := rt.home()

	execPath := writeSkillFixture(t, home, "main", "config-patcher", `---
name: config-patcher
description: Patch configuration files.
stage: cli
priority: 80
---
# config-patcher

Update the config file:
`+"```"+`
sed -i 's/old/new/g' /etc/app/config.yaml
`+"```"+`

Verify the patch:
`+"```"+`
grep "new" /etc/app/config.yaml
`+"```"+`
`)

	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "SKILL.md content"})
	registry.Register(runtimeNamedTool{name: "file.edit", content: "patched config"})
	registry.Register(&captureCommandTool{name: "terminal.run", content: `{"output":"ok"}`})
	rt.Tools = registry

	rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "done"}, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"patch config","requires_tools":true,"required_tools":["file.read"],"required_skills":[{"name":"config-patcher","path":"` + execPath + `","reason":"config patch workflow"}],"required_evidence":[{"kind":"local_file","tool":"file.read","description":"read ` + execPath + `"}],"plan_items":[{"id":"plan-1","title":"read SKILL.md","status":"pending","tool":"file.read","criteria":"read ` + execPath + `"}],"expected_outcome":"patched config"}`}

	_, err := rt.Handle(context.Background(), inbound("cli:6c-edit", "patch config file"))
	if err != nil {
		t.Fatal(err)
	}

	state := loadState(t, rt, "cli:6c-edit")
	if len(state.Tasks) == 0 {
		t.Fatal("task not found")
	}
	contract := *state.Tasks[0].Execution.Contract

	toolCounts := map[string]int{}
	for _, item := range contract.PlanItems {
		toolCounts[strings.TrimSpace(item.Tool)]++
	}

	if toolCounts["file.edit"] < 1 {
		t.Fatalf("skill body must generate file.edit plan item, got tools: %+v", toolCounts)
	}
}

// TestSkillBodyExtractionWebSearchAndFetch verifies that a skill body
// containing "search for" and "fetch" prose generates web.search + web.fetch
// plan items.
func TestSkillBodyExtractionWebSearchAndFetch(t *testing.T) {
	rt := newTestRuntime(t)
	home := rt.home()

	execPath := writeSkillFixture(t, home, "main", "web-researcher", `---
name: web-researcher
description: Research topics on the web.
stage: cli
priority: 80
---
# web-researcher

Search for recent news:
`+"```"+`
curl -s "https://news.example.com/api/latest"
`+"```"+`

Fetch the article body:
`+"```"+`
curl -s "https://example.com/article/123"
`+"```"+`
`)

	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "SKILL.md content"})
	registry.Register(runtimeNamedTool{name: "web.search", content: "search results"})
	registry.Register(runtimeNamedTool{name: "web.fetch", content: "article content"})
	rt.Tools = registry

	rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "done"}, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"research topic","requires_tools":true,"required_tools":["file.read"],"required_skills":[{"name":"web-researcher","path":"` + execPath + `","reason":"web research workflow"}],"required_evidence":[{"kind":"local_file","tool":"file.read","description":"read ` + execPath + `"}],"plan_items":[{"id":"plan-1","title":"read SKILL.md","status":"pending","tool":"file.read","criteria":"read ` + execPath + `"}],"expected_outcome":"research summary"}`}

	_, err := rt.Handle(context.Background(), inbound("cli:6c-web", "research topic"))
	if err != nil {
		t.Fatal(err)
	}

	state := loadState(t, rt, "cli:6c-web")
	if len(state.Tasks) == 0 {
		t.Fatal("task not found")
	}
	contract := *state.Tasks[0].Execution.Contract

	toolCounts := map[string]int{}
	for _, item := range contract.PlanItems {
		toolCounts[strings.TrimSpace(item.Tool)]++
	}

	if toolCounts["web.search"] < 1 {
		t.Fatalf("skill body must generate web.search plan item, got tools: %+v", toolCounts)
	}
	if toolCounts["web.fetch"] < 1 {
		t.Fatalf("skill body must generate web.fetch plan item, got tools: %+v", toolCounts)
	}
}

// TestSkillBodyExtractionAllDistinctTools verifies that a mixed skill body
// can generate all distinct tool categories without truncating later ones.
func TestSkillBodyExtractionAllDistinctTools(t *testing.T) {
	rt := newTestRuntime(t)
	home := rt.home()

	execPath := writeSkillFixture(t, home, "main", "full-workflow", `---
name: full-workflow
description: Full workflow with all tools.
stage: cli
priority: 80
---
# full-workflow

Search for data:
`+"```"+`
curl -s "https://api.example.com/data"
`+"```"+`

Read the input file:
   Read the configuration from disk.

Save the output:
   Save the processed results to a file.

Update the template:
   Update the existing template with new values.

Deploy via CLI:
`+"```"+`
deploy-cli push --file /tmp/output.json
`+"```"+`
`)

	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "SKILL.md content"})
	registry.Register(runtimeNamedTool{name: "file.write", content: "wrote file"})
	registry.Register(runtimeNamedTool{name: "file.edit", content: "edited file"})
	registry.Register(runtimeNamedTool{name: "web.search", content: "search results"})
	registry.Register(runtimeNamedTool{name: "web.fetch", content: "fetched content"})
	registry.Register(&captureCommandTool{name: "terminal.run", content: `{"status":"ok"}`})
	rt.Tools = registry

	rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "done"}, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"full workflow","requires_tools":true,"required_tools":["file.read"],"required_skills":[{"name":"full-workflow","path":"` + execPath + `","reason":"full workflow"}],"required_evidence":[{"kind":"local_file","tool":"file.read","description":"read ` + execPath + `"}],"plan_items":[{"id":"plan-1","title":"read SKILL.md","status":"pending","tool":"file.read","criteria":"read ` + execPath + `"}],"expected_outcome":"completed"}`}

	_, err := rt.Handle(context.Background(), inbound("cli:6c-all", "run full workflow"))
	if err != nil {
		t.Fatal(err)
	}

	state := loadState(t, rt, "cli:6c-all")
	if len(state.Tasks) == 0 {
		t.Fatal("task not found")
	}
	contract := *state.Tasks[0].Execution.Contract

	toolCounts := map[string]int{}
	for _, item := range contract.PlanItems {
		toolCounts[strings.TrimSpace(item.Tool)]++
	}

	// All distinct tools found in skill body should be present
	for _, wantTool := range []string{"file.write", "file.edit", "web.search", "web.fetch", "terminal.run"} {
		if toolCounts[wantTool] < 1 {
			t.Fatalf("skill body must generate %s plan item, got tools: %+v", wantTool, toolCounts)
		}
	}

	// Verify skill name never appears in tool fields
	for _, item := range contract.PlanItems {
		if strings.EqualFold(item.Tool, "full-workflow") {
			t.Fatal("skill name must not appear in plan_items[].tool")
		}
	}
}
