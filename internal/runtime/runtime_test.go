package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/memory"
	"github.com/dongping/mateway/internal/schedule"
	"github.com/dongping/mateway/internal/secret"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/tool"
)

func TestRuntimeNoActiveTaskCreatesNewTask(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "done"}, rt.Tools)

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "summarize the project"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Text != "done" {
		t.Fatalf("reply = %q", resp.Reply.Text)
	}
	state := loadState(t, rt, "cli:test")
	if len(state.Tasks) != 1 || state.Tasks[0].Goal != "summarize the project" {
		t.Fatalf("expected one new task, got %#v", state.Tasks)
	}
	if state.ActiveTask != "" || state.Tasks[0].Status != "completed" {
		t.Fatalf("expected completed task with no active task, got %#v", state.Tasks[0])
	}
}

func TestRuntimeNewResolvesConfiguredDefaultModel(t *testing.T) {
	cfg := &config.Root{
		App:   config.AppConfig{Home: t.TempDir()},
		Model: config.ModelSelection{Default: "primary"},
		Agents: config.AgentsConfig{
			Default: "main",
			Profiles: []config.AgentProfileConfig{{
				ID:   "main",
				Name: "Main",
			}},
		},
		Models: []config.ModelConfig{{
			Name:    "primary",
			Enabled: true,
			APIKey:  "test-key",
		}},
	}
	rt := New(cfg)
	if _, ok := rt.Model.(HeuristicModel); ok {
		t.Fatal("runtime default model should use configured model before heuristic fallback")
	}
	agent := rt.Pool.AgentForSession("cli:test")
	if agent == nil {
		t.Fatal("expected default pool agent")
	}
	if _, ok := agent.Model.(HeuristicModel); ok {
		t.Fatal("default pool agent should use configured model before heuristic fallback")
	}
}

func TestRuntimeSystemContextUsesConfiguredTimezone(t *testing.T) {
	cfg := &config.Root{Scheduler: config.SchedulerConfig{Timezone: "UTC"}}
	text := buildRuntimeSystemContext(cfg, config.AgentProfileConfig{})
	if !strings.Contains(text, " UTC\n") {
		t.Fatalf("expected UTC timezone in runtime context, got:\n%s", text)
	}
	if strings.Contains(text, "Asia/Shanghai") {
		t.Fatalf("runtime context should use configured timezone, got:\n%s", text)
	}
	if strings.Contains(text, "Task freshness policy:") || strings.Contains(text, "Available skills:") {
		t.Fatalf("default runtime context should not include triggered sections or skill catalog, got:\n%s", text)
	}
}

func TestRuntimeSystemContextAddsTriggeredSectionsAndSelectedSkills(t *testing.T) {
	cfg := config.DefaultRoot()
	contract := session.TaskContract{
		Summary:       "publish latest market report",
		RequiresTools: true,
		RequiredSkills: []session.RequiredSkill{
			{Name: "publish-skill", Path: "/workspace/skills/publish-skill/SKILL.md", Reason: "publish workflow"},
		},
		RequiredEvidence: []session.TaskEvidenceContract{{Kind: "current_external_fact", Tool: "web.search", Description: "latest market data"}},
	}
	text := buildRuntimeSystemContextForTask(&cfg, config.AgentProfileConfig{}, "publish latest report by email", contract)
	for _, want := range []string{"Task freshness policy:", "Connector gap policy:", "Selected task skills:", "publish-skill"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in triggered runtime context, got:\n%s", want, text)
		}
	}
	if strings.Contains(text, "software-install") {
		t.Fatalf("runtime context should not inject unrelated default skills, got:\n%s", text)
	}
}

func TestRuntimeActiveTaskSteersNewMessageIntoExistingTask(t *testing.T) {
	rt := newTestRuntime(t)
	state := session.State{Key: "cli:test"}
	task := state.StartTask("prepare release notes")
	if err := rt.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	model := &captureUserModel{text: "captured"}
	rt.Pool.agents["main"] = agentcore.NewAgent(model, rt.Tools)

	if _, err := rt.Handle(context.Background(), inbound("cli:test", "include the migration note")); err != nil {
		t.Fatal(err)
	}
	updated := loadState(t, rt, "cli:test")
	if len(updated.Tasks) != 1 || updated.Tasks[0].ID != task.ID {
		t.Fatalf("expected steering to reuse active task, got %#v", updated.Tasks)
	}
	if !strings.Contains(model.lastUser, "Active task:") ||
		!strings.Contains(model.lastUser, "prepare release notes") ||
		!strings.Contains(model.lastUser, "include the migration note") {
		t.Fatalf("expected merged active-task user message, got %q", model.lastUser)
	}
}

func TestRuntimeNewArchivesAndClearsSession(t *testing.T) {
	rt := newTestRuntime(t)
	state := session.State{Key: "cli:test"}
	task := state.StartTask("old task")
	state.Messages = []agentcore.Message{{Role: agentcore.RoleUser, Content: "old"}}
	state.Pending = &session.PendingAction{Kind: "memory_proposal_review", TaskID: task.ID, ProposalID: "prop_old"}
	if err := rt.Store.Save(state); err != nil {
		t.Fatal(err)
	}

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "/new"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Style != "session_reset" || !strings.Contains(resp.Reply.Text, "session") {
		t.Fatalf("expected reset reply, got %#v", resp.Reply)
	}
	updated := loadState(t, rt, "cli:test")
	if len(updated.Messages) != 0 || len(updated.Tasks) != 0 || updated.Pending != nil || updated.ActiveTask != "" {
		t.Fatalf("expected cleared state, got %#v", updated)
	}
	archives, err := rt.Store.ListArchives("cli:test")
	if err != nil {
		t.Fatal(err)
	}
	if len(archives) != 1 {
		t.Fatalf("expected one archive, got %#v", archives)
	}
}

func TestRuntimeAfterNewDoesNotAutoRecallArchivedTask(t *testing.T) {
	rt := newTestRuntime(t)
	state := session.State{Key: "cli:test"}
	old := state.StartTask("summarize README")
	old.Status = "completed"
	old.Summary = "README was summarized."
	if err := rt.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Handle(context.Background(), inbound("cli:test", "/new")); err != nil {
		t.Fatal(err)
	}
	rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "started as new task"}, rt.Tools)

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "continue the previous README task"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Style == "archive_recall_pending" {
		t.Fatalf("runtime should not auto recall archived task, got %#v", resp.Reply)
	}
	updated := loadState(t, rt, "cli:test")
	if len(updated.Tasks) != 1 || updated.Tasks[0].ID == old.ID || updated.Tasks[0].Goal != "continue the previous README task" {
		t.Fatalf("expected a normal new task, got %#v", updated.Tasks)
	}
}

func TestRuntimeStopsWhenModelReturnsNoToolCallWithoutReview(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "plain answer"}, rt.Tools)

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "just answer"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	trace := string(data)
	if strings.Contains(trace, "completion_review_hook") || strings.Contains(trace, "followup_hook") || strings.Contains(trace, "pending_intent_hook") {
		t.Fatalf("unexpected removed hook in trace:\n%s", trace)
	}
	state := loadState(t, rt, "cli:test")
	if len(state.Tasks) != 1 || state.Tasks[0].Status != "completed" {
		t.Fatalf("expected immediate completion, got %#v", state.Tasks)
	}
}

func TestRuntimeTraceIncludesIdentity(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "done"}, rt.Tools)
	msg := inbound("feishu:acct:thread", "inspect")
	msg.Channel = "feishu"
	msg.ID = "msg-1"
	msg.ThreadID = "thread-1"
	msg.UserID = "user-1"
	msg.Metadata = map[string]string{"account_id": "acct", "peer_id": "thread-1", "message_type": "text"}

	resp, err := rt.Handle(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	trace := string(data)
	for _, want := range []string{
		`"session_key":"feishu:acct:thread"`,
		`"channel":"feishu"`,
		`"account_id":"acct"`,
		`"agent_id":"main"`,
		`"message_id":"msg-1"`,
		`"task_id":"task-`,
	} {
		if !strings.Contains(trace, want) {
			t.Fatalf("trace missing %s:\n%s", want, trace)
		}
	}
}

func TestRuntimeActionTaskPausesForPlanReview(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "terminal.run", content: "release found"})
	rt.Tools = registry
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{ID: "call_1", Name: "terminal.run", Args: map[string]any{"command": "git tag --sort=-creatordate | head -5"}}}},
		{Role: agentcore.RoleAssistant, Content: "done"},
	}}, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"search task","requires_tools":true,"required_tools":["terminal.run"],"required_evidence":[{"kind":"runtime_state","tool":"terminal.run","description":"git tags"}],"plan_items":[{"id":"plan-1","title":"check git tags","status":"pending","tool":"terminal.run","criteria":"collect release tags"}],"expected_outcome":"answer","completion_policy":"use evidence"}`}
	resp, err := rt.Handle(context.Background(), inbound("cli:plan-review", "search latest release"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Style != channel.StyleInputRequired || !strings.Contains(resp.Reply.Text, "Reply 1 to execute") {
		t.Fatalf("expected plan review reply, got %#v", resp.Reply)
	}
	if strings.Contains(resp.Reply.Text, "search evidence") {
		t.Fatalf("plan review should not show detailed execution steps, got %q", resp.Reply.Text)
	}
	state := loadState(t, rt, "cli:plan-review")
	if state.Pending == nil || state.Pending.Kind != session.PendingKindTaskPlanConfirm {
		t.Fatalf("expected task plan pending, got %#v", state.Pending)
	}
	if len(state.Tasks) != 1 || len(state.Tasks[0].Execution.TraceRefs) == 0 || state.Tasks[0].Execution.TraceRefs[len(state.Tasks[0].Execution.TraceRefs)-1].Phase != tracePhasePlanReview {
		t.Fatalf("expected plan_review trace ref, got %#v", state.Tasks)
	}
	resp, err = rt.Handle(context.Background(), inbound("cli:plan-review", "1"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	trace := string(data)
	if !strings.Contains(trace, `"control_text":"1"`) || !strings.Contains(trace, `"effective_task_goal":"search latest release"`) {
		t.Fatalf("expected control and effective goal in execute trace, got:\n%s", trace)
	}
	state = loadState(t, rt, "cli:plan-review")
	if refs := state.Tasks[0].Execution.TraceRefs; len(refs) < 2 || refs[len(refs)-1].Phase != tracePhaseExecute {
		t.Fatalf("expected execute trace ref, got %#v", refs)
	}
}

func TestRuntimeShowsPlanItemsDuringExecutionProgress(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "terminal.run", content: "release found"})
	rt.Tools = registry
	rt.ContractModel = contractJSONModel{json: `{"summary":"search task","requires_tools":true,"required_tools":["terminal.run"],"required_evidence":[{"kind":"runtime_state","tool":"terminal.run","description":"search evidence"}],"plan_items":[{"id":"plan-1","title":"search models","status":"pending","tool":"terminal.run","criteria":"collect candidates"}],"expected_outcome":"answer","completion_policy":"use evidence"}`}
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{ID: "call_1", Name: "terminal.run", Args: map[string]any{"command": "echo release"}}}},
		{Role: agentcore.RoleAssistant, Content: "done"},
	}}, rt.Tools)
	var progress []channel.OutboundMessage
	rt.ProgressSink = func(msg channel.OutboundMessage) {
		progress = append(progress, msg)
	}
	resp, err := rt.Handle(context.Background(), inbound("cli:plan-progress", "check singbox service status"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Style == channel.StyleInputRequired {
		if _, err := rt.Handle(context.Background(), inbound("cli:plan-progress", "1")); err != nil {
			t.Fatal(err)
		}
	}
	found := false
	for _, msg := range progress {
		for _, step := range msg.Progress {
			if step.Title == "search models" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("expected plan item in execution progress, got %#v", progress)
	}
}

func TestRuntimePlanReviewIsStructuredEnglish(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	// The runtime does not detect user language. The plan review message is
	// structured English; the system prompt and the model localize the
	// actual execution reply for the user.
	rt := newTestRuntime(t)
	rt.ContractModel = contractJSONModel{json: `{"summary":"检查 singbox 状态","requires_tools":true,"required_tools":["terminal.run"],"required_evidence":[{"kind":"runtime_state","tool":"terminal.run","description":"服务状态"}],"plan_items":[{"id":"plan-1","title":"检查服务状态","status":"pending","tool":"terminal.run","criteria":"获取服务状态"}],"expected_outcome":"状态报告","completion_policy":"使用终端证据"}`}
	resp, err := rt.Handle(context.Background(), inbound("cli:plan-lang", "帮我检查 singbox 状态"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Reply.Text, "Task plan") || !strings.Contains(resp.Reply.Text, "Reply 1 to execute") {
		t.Fatalf("expected English plan review chrome, got %q", resp.Reply.Text)
	}
	if !strings.Contains(resp.Reply.Text, "terminal.run") {
		t.Fatalf("plan review should mention required tool, got %q", resp.Reply.Text)
	}
}

func TestTraceSummaryReportsIdentityAndIncomplete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	if err := os.WriteFile(path, []byte(
		`{"type":"request","trace_id":"trace-1","session_key":"feishu:acct:thread","channel":"feishu","account_id":"acct","agent_id":"main","task_id":"task-1","message_id":"msg-1"}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	summary, err := SummarizeTrace(path)
	if err != nil {
		t.Fatal(err)
	}
	if summary.SessionKey != "feishu:acct:thread" || summary.AccountID != "acct" || summary.AgentID != "main" || summary.TaskID != "task-1" {
		t.Fatalf("summary identity mismatch: %#v", summary)
	}
	if summary.RuntimeDone || summary.GatewayDone {
		t.Fatalf("expected incomplete trace summary, got %#v", summary)
	}
}

func TestRuntimeDestructiveTerminalRunIsBlocked(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "terminal.run",
			Args: map[string]any{"command": "rm -rf /tmp/mateway-danger-test"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "blocked"},
	}}, rt.Tools)

	if _, err := rt.Handle(context.Background(), inbound("cli:test", "delete tmp")); err != nil {
		t.Fatal(err)
	}
	state := loadState(t, rt, "cli:test")
	if len(state.Tasks) != 1 || len(state.Tasks[0].Execution.Events) == 0 {
		t.Fatalf("expected execution events, got %#v", state.Tasks)
	}
	found := false
	for _, event := range state.Tasks[0].Execution.Events {
		if event.Type == "tool_blocked" && event.Tool == "terminal.run" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected destructive terminal block event, got %#v", state.Tasks[0].Execution.Events)
	}
}

func TestRuntimeTerminalShellCommandRunsWithoutApproval(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "terminal.run",
			Args: map[string]any{"command": "echo approved; echo done"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "approved"},
	}}, rt.Tools)

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "run shell command"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed {
		t.Fatalf("expected shell command task to complete, got %#v", resp)
	}
}

func TestRuntimeContinuesWhenAssistantPromisesActionWithoutTool(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, Content: "I will check now."},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "file.read",
			Args: map[string]any{"path": rt.Config.App.Home},
		}}},
		{Role: agentcore.RoleAssistant, Content: "checked"},
	}}, rt.Tools)

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "review files in ~/.mateway"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Text != "checked" {
		t.Fatalf("reply = %q", resp.Reply.Text)
	}
	if len(resp.Reply.Progress) != 0 {
		t.Fatalf("completed reply should not include progress, got %#v", resp.Reply.Progress)
	}
	data, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "deliverable_gate_followup") {
		t.Fatalf("expected deliverable gate trace, got:\n%s", string(data))
	}
}

func TestRuntimeTaskContractForcesToolEvidenceBeforeCompletion(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "terminal.run", content: "Beijing clear; Yiwu light rain."})
	rt.Tools = registry
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, Content: "I will check the weather and then advise."},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "terminal.run",
			Args: map[string]any{"command": "curl -s weather.api/beijing,yiwu"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "Weather checked. Travel looks acceptable with rain precautions."},
	}}, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"weather travel advice","requires_tools":true,"required_tools":["terminal.run"],"required_evidence":[{"kind":"runtime_state","tool":"terminal.run","description":"current weather for Beijing and Yiwu"}],"expected_outcome":"travel recommendation","completion_policy":"use terminal evidence before final answer"}`}

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "check weather for Beijing and Yiwu tomorrow"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Style == channel.StyleInputRequired {
		resp, err = rt.Handle(context.Background(), inbound("cli:test", "1"))
		if err != nil {
			t.Fatal(err)
		}
	}
	if resp.Failed || !strings.Contains(resp.Reply.Text, "Weather checked") {
		t.Fatalf("expected contract repair to complete with tool evidence, got %#v", resp)
	}
	data, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	trace := string(data)
	if !strings.Contains(trace, "task_contract_unsatisfied") || !strings.Contains(trace, "task_contract_satisfied") {
		t.Fatalf("expected contract lifecycle trace, got:\n%s", trace)
	}
	state := loadState(t, rt, "cli:test")
	if len(state.Tasks[0].Execution.TraceRefs) == 0 {
		t.Fatalf("expected trace refs, got %#v", state.Tasks[0].Execution.TraceRefs)
	}
	firstTrace, err := os.ReadFile(state.Tasks[0].Execution.TraceRefs[0].TracePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(firstTrace), "task_contract_created") {
		t.Fatalf("expected contract creation in plan review trace, got:\n%s", string(firstTrace))
	}
	if state.Tasks[0].Execution.Contract == nil || !state.Tasks[0].Execution.Contract.RequiresTools {
		t.Fatalf("expected stored task contract, got %#v", state.Tasks[0].Execution.Contract)
	}
	if len(state.Tasks[0].Execution.TraceRefs) < 2 {
		t.Fatalf("expected plan and execute trace refs, got %#v", state.Tasks[0].Execution.TraceRefs)
	}
	if got := state.Tasks[0].Execution.Contract.PlanItems[0].Status; got != "completed" {
		t.Fatalf("expected plan item completed, got %q contract=%#v", got, state.Tasks[0].Execution.Contract)
	}
}

func TestRuntimeUnsatisfiedContractReplyIsPartial(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "web.search", content: "partial market evidence"})
	registry.Register(runtimeNamedTool{name: "file.write", content: "written"})
	rt.Tools = registry
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "web.search",
			Args: map[string]any{"query": "Nasdaq last three days"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "I have the key data. I will write the document next."},
	}}, rt.Tools)
	rt.Config.Execution.MaxContractFollowups = 1
	rt.ContractModel = contractJSONModel{json: `{"summary":"write market document","requires_tools":true,"required_tools":["web.search","file.write"],"required_evidence":[{"kind":"local_file","tool":"file.write","description":"markdown document"}],"plan_items":[{"id":"plan-1","title":"search market data","status":"pending","tool":"web.search","criteria":"collect data"},{"id":"plan-2","title":"write markdown document","status":"pending","tool":"file.write","criteria":"save markdown document"}],"expected_outcome":"written document","completion_policy":"must write the file before final answer"}`}

	planResp, err := rt.Handle(context.Background(), inbound("cli:test", "整理纳指近三天走势并写成文档"))
	if err != nil {
		t.Fatal(err)
	}
	if planResp.Reply.Style != channel.StyleInputRequired {
		t.Fatalf("expected plan review, got %#v", planResp)
	}
	resp, err := rt.Handle(context.Background(), inbound("cli:test", "1"))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Failed || resp.Reply.Style != channel.StylePartial {
		t.Fatalf("expected unsatisfied contract to return partial failure, got %#v", resp)
	}
	state := loadState(t, rt, "cli:test")
	if state.Tasks[0].Status != "failed" {
		t.Fatalf("expected task failed, got %#v", state.Tasks[0])
	}
	if strings.TrimSpace(resp.Reply.Text) == "" {
		t.Fatal("expected partial reply text")
	}
}

func TestTaskContractPromptIncludesDiscoveredSkills(t *testing.T) {
	skills := []discoveredSkill{{
		Name:        "feishu-notify",
		Description: "Use when creating a Feishu/Lark cloud document.",
		Stage:       "execution",
		Priority:    "80",
		Path:        "/tmp/workspace/skills/feishu-notify/SKILL.md",
	}}
	prompt := renderTaskContractPrompt("整理成文档，发到我的飞书云文档", "", agentcore.NewToolRegistry(), skills)
	for _, want := range []string{"feishu-notify", "Skill names are instructional", "execution_hint"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected contract prompt to mention %q, got:\n%s", want, prompt)
		}
	}
}

func TestTaskContractPromptDoesNotHardCodeFeishuSkill(t *testing.T) {
	prompt := renderTaskContractPrompt("整理成文档，发到我的飞书云文档", "", agentcore.NewToolRegistry(), nil)
	if strings.Contains(prompt, "feishu-notify") {
		t.Fatalf("contract prompt should only mention discovered skills, got:\n%s", prompt)
	}
}

func TestFallbackContractForActionTaskRequiresTools(t *testing.T) {
	contract := fallbackTaskContract("查看纳斯达克指数情况，近三天的走势，将其整理成文档，发到我的飞书云文档", "")
	if !contract.RequiresTools {
		t.Fatalf("expected action fallback contract to require tools, got %#v", contract)
	}
	for _, want := range []string{"web.search", "file.write", "terminal.run"} {
		found := false
		for _, got := range contract.RequiredTools {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected fallback contract to require %s, got %v", want, contract.RequiredTools)
		}
	}
	if len(contract.PlanItems) < 3 {
		t.Fatalf("expected fallback contract plan items for search/write/publish, got %#v", contract.PlanItems)
	}
	if !shouldPauseForTaskPlan(contract, contractStrategyReviewRequired) {
		t.Fatalf("expected fallback action contract to pause for plan review, got %#v", contract)
	}
}

func TestRuntimeContextIncludesSkillsBeyondOldLimit(t *testing.T) {
	rt := newTestRuntime(t)
	ws := t.TempDir()
	rt.Config.App.Workspace = ws
	for i := 0; i < 13; i++ {
		dir := filepath.Join(ws, "skills", fmt.Sprintf("high-%02d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		content := fmt.Sprintf("---\nname: high-%02d\ndescription: High priority skill.\npriority: 90\n---\n", i)
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	feishuDir := filepath.Join(ws, "skills", "feishu-notify")
	if err := os.MkdirAll(feishuDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(feishuDir, "SKILL.md"), []byte("---\nname: feishu-notify\ndescription: Create Feishu/Lark cloud documents.\npriority: 80\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	text := skillsPrompt(skillsForRuntimeContext(rt.Config, "main"))
	if !strings.Contains(text, "feishu-notify") {
		t.Fatalf("expected runtime context skills to include feishu-notify beyond old 12-skill limit, got:\n%s", text)
	}
}

func TestSkillValidationUsesRuntimeContextSkillSet(t *testing.T) {
	rt := newTestRuntime(t)
	ws := t.TempDir()
	rt.Config.App.Workspace = ws
	for i := 0; i < 13; i++ {
		dir := filepath.Join(ws, "skills", fmt.Sprintf("high-%02d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		content := fmt.Sprintf("---\nname: high-%02d\ndescription: High priority skill.\npriority: 90\n---\n", i)
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	feishuPath := filepath.Join(ws, "skills", "feishu-notify", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(feishuPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(feishuPath, []byte("---\nname: feishu-notify\ndescription: Create Feishu/Lark cloud documents.\npriority: 80\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "ok"})
	registry.Register(runtimeNamedTool{name: "terminal.run", content: "ok"})
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"file.read", "terminal.run"},
		RequiredSkills: []session.RequiredSkill{
			{Name: "feishu-notify", Path: feishuPath, Reason: "create Feishu cloud doc"},
		},
		RequiredEvidence: []session.TaskEvidenceContract{
			{Kind: "local_file", Tool: "file.read", Description: "read " + feishuPath},
		},
		PlanItems: []session.TaskPlanItem{
			{ID: "plan-1", Title: "read feishu-notify SKILL.md", Status: "pending", Tool: "file.read", Criteria: "read " + feishuPath},
			{ID: "plan-2", Title: "create Feishu doc", Status: "pending", Tool: "terminal.run", Criteria: "run helper from feishu-notify skill"},
		},
	}
	validation := validateContractTools(contract, registry, skillsForRuntimeContext(rt.Config, "main"))
	if !validation.IsValid() {
		t.Fatalf("expected feishu-notify beyond old limit to validate, got invalid tools=%v skills=%v", validation.InvalidTools, validation.InvalidSkills)
	}
}

func TestRuntimeTaskContractStrengthensServerStatusToTerminalRun(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "terminal.run", content: "sing-box.service active"})
	registry.Register(runtimeNamedTool{name: "web.search", content: "latest sing-box release v0.6.0"})
	rt.Tools = registry
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, Content: "I cannot access the server directly."},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "terminal.run",
			Args: map[string]any{"command": "ssh overseas 'systemctl status sing-box --no-pager'"},
		}}},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_2",
			Name: "web.search",
			Args: map[string]any{"query": "sing-box release"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "sing-box is active."},
	}}, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"Check the status of the sing-box project (current releases/repo state)","requires_tools":true,"required_tools":["web.search"],"required_evidence":[{"kind":"current_external_fact","tool":"web.search","description":"latest sing-box release"}],"expected_outcome":"latest version status","completion_policy":"use web evidence before final answer"}`}

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "不用脚本，你也可以直接访问国外服务器吧，给我去看看singbox状态"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Style != channel.StyleInputRequired {
		t.Fatalf("expected plan review before execution, got %#v", resp)
	}
	resp, err = rt.Handle(context.Background(), inbound("cli:test", "1"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed || !strings.Contains(resp.Reply.Text, "active") {
		t.Fatalf("expected terminal-backed repair to complete, got %#v", resp)
	}
	state := loadState(t, rt, "cli:test")
	contract := state.Tasks[0].Execution.Contract
	if contract == nil || !containsString(contract.RequiredTools, "terminal.run") {
		t.Fatalf("expected terminal.run in strengthened contract, got %#v", contract)
	}
	if strings.Contains(strings.ToLower(contract.Summary), "github") || strings.Contains(strings.ToLower(contract.Summary), "release") {
		t.Fatalf("contract summary should keep user target, got %#v", contract)
	}
}

func TestRuntimeToolBlockedMarksPlanItemBlocked(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	rt.ContractModel = contractJSONModel{json: `{"summary":"delete tmp","requires_tools":true,"required_tools":["terminal.run"],"required_evidence":[{"kind":"mutation","tool":"terminal.run","description":"delete command"}],"plan_items":[{"id":"plan-1","title":"delete tmp","status":"pending","tool":"terminal.run","criteria":"run delete command"}],"expected_outcome":"deleted","completion_policy":"use terminal evidence"}`}
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{ID: "call_1", Name: "terminal.run", Args: map[string]any{"command": "rm -rf /tmp/mateway-danger-test"}}}},
		{Role: agentcore.RoleAssistant, Content: "blocked by policy"},
	}}, rt.Tools)
	if _, err := rt.Handle(context.Background(), inbound("cli:blocked-plan", "delete tmp")); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Handle(context.Background(), inbound("cli:blocked-plan", "1")); err != nil {
		t.Fatal(err)
	}
	state := loadState(t, rt, "cli:blocked-plan")
	contract := state.Tasks[0].Execution.Contract
	if contract == nil || len(contract.PlanItems) == 0 || contract.PlanItems[0].Status != "blocked" {
		t.Fatalf("expected blocked plan item, got %#v", contract)
	}
}

func TestRuntimeCompletesMultiplePlanItemsWithSameTool(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "terminal.run", content: "search evidence"})
	rt.Tools = registry
	rt.ContractModel = contractJSONModel{json: `{"summary":"compare coding models","requires_tools":true,"required_tools":["terminal.run"],"required_evidence":[{"kind":"runtime_state","tool":"terminal.run","description":"current coding model evidence"}],"plan_items":[{"id":"plan-1","title":"search domestic models","status":"pending","tool":"terminal.run","criteria":"collect candidates"},{"id":"plan-2","title":"search benchmarks","status":"pending","tool":"terminal.run","criteria":"collect benchmark data"},{"id":"plan-3","title":"compare access","status":"pending","tool":"terminal.run","criteria":"collect availability info"}],"expected_outcome":"recommendation","completion_policy":"use evidence"}`}
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{ID: "call_1", Name: "terminal.run", Args: map[string]any{"command": "echo domestic"}}}},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{ID: "call_2", Name: "terminal.run", Args: map[string]any{"command": "echo benchmarks"}}}},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{ID: "call_3", Name: "terminal.run", Args: map[string]any{"command": "echo access"}}}},
		{Role: agentcore.RoleAssistant, Content: "Qwen Coder, DeepSeek Coder, Doubao Seed Code."},
	}}, rt.Tools)
	resp, err := rt.Handle(context.Background(), inbound("cli:multi-plan", "check singbox service status"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Style == channel.StyleInputRequired {
		resp, err = rt.Handle(context.Background(), inbound("cli:multi-plan", "1"))
		if err != nil {
			t.Fatal(err)
		}
	}
	if resp.Failed || resp.Reply.Style == channel.StylePartial {
		t.Fatalf("expected completion, got %#v", resp)
	}
	state := loadState(t, rt, "cli:multi-plan")
	for _, item := range state.Tasks[0].Execution.Contract.PlanItems {
		if item.Status != "completed" {
			t.Fatalf("expected all plan items completed, got %#v", state.Tasks[0].Execution.Contract.PlanItems)
		}
	}
}

func TestRuntimeCompletesNoToolPlanItemOnFinalAnswer(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "terminal.run", content: "clear, light rain"})
	rt.Tools = registry
	rt.ContractModel = contractJSONModel{json: `{"summary":"travel plan","requires_tools":true,"required_tools":["terminal.run"],"required_evidence":[{"kind":"runtime_state","tool":"terminal.run","description":"weather and traffic info"}],"plan_items":[{"id":"plan-1","title":"check weather and traffic","status":"pending","tool":"terminal.run","criteria":"collect current info"},{"id":"plan-2","title":"output travel plan","status":"pending","criteria":"summarize evidence"}],"expected_outcome":"travel plan","completion_policy":"use evidence"}`}
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{ID: "call_1", Name: "terminal.run", Args: map[string]any{"command": "echo weather"}}}},
		{Role: agentcore.RoleAssistant, Content: "Travel tomorrow, bring umbrella."},
	}}, rt.Tools)
	resp, err := rt.Handle(context.Background(), inbound("cli:no-tool-plan", "check singbox service status"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Style == channel.StyleInputRequired {
		resp, err = rt.Handle(context.Background(), inbound("cli:no-tool-plan", "1"))
		if err != nil {
			t.Fatal(err)
		}
	}
	if resp.Failed {
		t.Fatalf("expected completion, got %#v", resp)
	}
	state := loadState(t, rt, "cli:no-tool-plan")
	contract := state.Tasks[0].Execution.Contract
	if contract == nil || len(contract.PlanItems) != 2 {
		t.Fatalf("expected stored plan items, got %#v", contract)
	}
	for _, item := range contract.PlanItems {
		if item.Status != "completed" {
			t.Fatalf("expected all plan items completed, got %#v", contract.PlanItems)
		}
	}
}

func TestRuntimeTaskContractAllowsNoToolTask(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "Mateway is a local agent runtime."}, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"explain Mateway","requires_tools":false,"required_tools":[],"required_evidence":[],"expected_outcome":"short explanation","completion_policy":"answer directly"}`}

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "explain what Mateway is"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed || resp.Reply.Text != "Mateway is a local agent runtime." {
		t.Fatalf("expected no-tool task to complete, got %#v", resp)
	}
}

func TestRuntimeTaskContractParseFailureFallsBack(t *testing.T) {
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "terminal.run", content: "ok"})
	rt.Tools = registry
	rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "plain answer"}, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `not json`}

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "use test tool"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Style == channel.StyleInputRequired {
		resp, err = rt.Handle(context.Background(), inbound("cli:test", "1"))
		if err != nil {
			t.Fatal(err)
		}
	}
	// After parse failure, fallback is used. The fallback may have
	// RequiresTools=false (direct) or RequiresTools=true (if strengthened).
	// Either way, the task should complete or fail gracefully.
	if resp.Reply.Text == "" {
		t.Fatalf("expected non-empty reply, got %#v", resp)
	}
	data, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "task_contract_parse_failed") {
		t.Fatalf("expected parse failure trace, got:\n%s", string(data))
	}
}

func TestRuntimeProgressSinkEmitsToolStartAndEnd(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "file.read",
			Args: map[string]any{"path": rt.Config.App.Home},
		}}},
		{Role: agentcore.RoleAssistant, Content: "checked"},
	}}, rt.Tools)
	var updates []channel.OutboundMessage
	rt.ProgressSink = func(msg channel.OutboundMessage) {
		updates = append(updates, msg)
	}

	if _, err := rt.Handle(context.Background(), inbound("cli:test", "review files in ~/.mateway")); err != nil {
		t.Fatal(err)
	}
	if len(updates) < 2 {
		t.Fatalf("expected start and end progress updates, got %#v", updates)
	}
	foundRunning := false
	for _, update := range updates {
		if update.Style != "processing" || len(update.Progress) == 0 {
			continue
		}
		step := update.Progress[len(update.Progress)-1]
		if step.Tool == "file.read" && step.Status == "running" {
			foundRunning = true
		}
	}
	if !foundRunning {
		t.Fatalf("expected running file.read progress update, got %#v", updates)
	}
	foundAccepted := false
	for _, update := range updates {
		if len(update.Progress) == 0 {
			continue
		}
		step := update.Progress[len(update.Progress)-1]
		if step.Tool == "file.read" && step.Status == "accepted" {
			foundAccepted = true
		}
	}
	if !foundAccepted {
		t.Fatalf("expected accepted file.read progress, got %#v", updates)
	}
}

func TestRuntimeProgressSinkEmitsModelThinking(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	started := make(chan struct{})
	release := make(chan struct{})
	rt.Pool.agents["main"] = agentcore.NewAgent(blockingModel{
		started: started,
		release: release,
		text:    "done",
	}, rt.Tools)
	updates := make(chan channel.OutboundMessage, 4)
	rt.ProgressSink = func(msg channel.OutboundMessage) {
		updates <- msg
	}
	done := make(chan error, 1)
	go func() {
		_, err := rt.Handle(context.Background(), inbound("cli:test", "think then answer"))
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("model did not start")
	}
	select {
	case update := <-updates:
		if len(update.Progress) == 0 {
			t.Fatalf("expected progress update, got %#v", update)
		}
		step := update.Progress[len(update.Progress)-1]
		if step.Title != "model" || step.Status != "thinking" || step.Summary != "waiting for model output" {
			t.Fatalf("expected model wait progress before model returned, got %#v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("expected model wait progress before model returned")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeProgressSinkDoesNotEmitFinalTextAsModelProgress(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "final answer should not be progress"}, rt.Tools)
	var updates []channel.OutboundMessage
	rt.ProgressSink = func(msg channel.OutboundMessage) {
		updates = append(updates, msg)
	}
	if _, err := rt.Handle(context.Background(), inbound("cli:test", "answer directly")); err != nil {
		t.Fatal(err)
	}
	for _, update := range updates {
		if len(update.Progress) == 0 {
			continue
		}
		step := update.Progress[len(update.Progress)-1]
		if strings.Contains(step.Summary, "final answer should not be progress") {
			t.Fatalf("final text leaked into progress: %#v", updates)
		}
	}
}

func TestRuntimeProgressSinkEmitsLongRunningToolProgress(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeSlowTool{delay: 80 * time.Millisecond})
	rt.Tools = registry
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "test.runtime_slow",
			Args: map[string]any{"text": "ok"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "checked"},
	}}, rt.Tools)
	var updates []channel.OutboundMessage
	rt.ProgressSink = func(msg channel.OutboundMessage) {
		updates = append(updates, msg)
	}
	oldInterval := runtimeToolProgressInterval
	runtimeToolProgressInterval = func(*config.Root, string) time.Duration {
		return 20 * time.Millisecond
	}
	defer func() { runtimeToolProgressInterval = oldInterval }()

	if _, err := rt.Handle(context.Background(), inbound("cli:test", "run slow tool")); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, update := range updates {
		if len(update.Progress) == 0 {
			continue
		}
		step := update.Progress[len(update.Progress)-1]
		if step.Tool == "test.runtime_slow" && step.Status == "running" && step.DurationMS > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected long-running progress update, got %#v", updates)
	}
}

func TestRuntimeToolTimeoutTraceDistinctFromActivityTimeout(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeSlowTool{delay: 80 * time.Millisecond})
	rt.Tools = registry
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "test.runtime_slow",
			Args: map[string]any{"text": "ok"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "tool timed out"},
	}}, rt.Tools)
	oldTimeout := runtimeToolTimeout
	runtimeToolTimeout = func(*config.Root, string) time.Duration {
		return 10 * time.Millisecond
	}
	defer func() { runtimeToolTimeout = oldTimeout }()

	resp, err := rt.Handle(context.Background(), inbound("cli:tool-timeout", "run slow tool"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	trace := string(data)
	if !strings.Contains(trace, `"type":"tool_timeout"`) {
		t.Fatalf("expected tool_timeout trace event, got:\n%s", trace)
	}
	if strings.Contains(trace, `"type":"task_inactivity_timeout"`) {
		t.Fatalf("tool timeout must not be reported as activity timeout, got:\n%s", trace)
	}
}

func TestRuntimeActivityTimeoutTraceDistinctFromToolTimeout(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	rt.Config.Execution.InactivityTimeout = "20ms"
	started := make(chan struct{})
	release := make(chan struct{})
	rt.Pool.agents["main"] = agentcore.NewAgent(blockingModel{
		started: started,
		release: release,
		text:    "late",
	}, rt.Tools)
	defer close(release)

	done := make(chan Response, 1)
	errs := make(chan error, 1)
	go func() {
		resp, err := rt.Handle(context.Background(), inbound("cli:activity-timeout", "think too long"))
		if err != nil {
			errs <- err
			return
		}
		done <- resp
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("model did not start")
	}
	var resp Response
	select {
	case err := <-errs:
		t.Fatalf("unexpected error: %v", err)
	case resp = <-done:
	case <-time.After(time.Second):
		t.Fatal("runtime did not return after activity timeout")
	}
	if !resp.Failed || !strings.Contains(strings.ToLower(resp.Reply.Text), "inactivity timeout") {
		t.Fatalf("expected activity timeout blocker, got %#v", resp)
	}
	data, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	trace := string(data)
	if !strings.Contains(trace, `"type":"task_inactivity_timeout"`) {
		t.Fatalf("expected task_inactivity_timeout trace event, got:\n%s", trace)
	}
	if strings.Contains(trace, `"type":"tool_timeout"`) {
		t.Fatalf("activity timeout must not be reported as tool timeout, got:\n%s", trace)
	}
}

func TestRuntimeCancelledContextReturnsInterruptedReply(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	started := make(chan struct{})
	rt.Pool.agents["main"] = agentcore.NewAgent(cancelledModel{started: started}, rt.Tools)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Response, 1)
	errs := make(chan error, 1)
	go func() {
		resp, err := rt.Handle(ctx, inbound("cli:test", "long task"))
		if err != nil {
			errs <- err
			return
		}
		done <- resp
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("model did not start")
	}
	cancel()
	select {
	case err := <-errs:
		t.Fatalf("unexpected error: %v", err)
	case resp := <-done:
		if !resp.Failed || !strings.Contains(resp.Reply.Text, "interrupted") {
			t.Fatalf("expected interrupted reply, got %#v", resp)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime did not return after cancellation")
	}
	state := loadState(t, rt, "cli:test")
	if len(state.Tasks) != 1 || state.ActiveTask != state.Tasks[0].ID || state.Tasks[0].Status != "failed" {
		t.Fatalf("expected interrupted task to remain active failed, got %#v", state.Tasks)
	}
}

func TestDefaultRegistryContainsPiStyleTools(t *testing.T) {
	registry := tool.NewRegistry(&config.Root{App: config.AppConfig{Home: t.TempDir()}})
	for _, name := range []string{
		"file.read", "file.write", "file.delete", "file.read", "terminal.run", "web.search", "web.fetch", "secret.set",
		"schedule.manage", "schedule.manage", "schedule.manage", "schedule.manage", "schedule.manage", "schedule.manage", "schedule.manage",
		"task.search", "task.resume",
	} {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("expected default tool %s", name)
		}
	}
	for _, name := range []string{"script.run"} {
		if _, ok := registry.Get(name); ok {
			t.Fatalf("did not expect default tool %s", name)
		}
	}
}

func TestAgentPoolFiltersToolsByProfileAccess(t *testing.T) {
	cfg := &config.Root{
		App: config.AppConfig{Home: t.TempDir()},
		Agents: config.AgentsConfig{
			Default: "main",
			Profiles: []config.AgentProfileConfig{{
				ID: "main",
				Tools: config.AccessListConfig{
					Deny: []string{"terminal.run"},
				},
			}},
		},
	}
	pool := NewAgentPool(cfg)
	agent := pool.AgentForSession("cli:default")
	if _, ok := agent.Tools.Get("terminal.run"); ok {
		t.Fatal("terminal.run should be filtered by profile deny list")
	}
	if _, ok := agent.Tools.Get("file.read"); !ok {
		t.Fatal("file.read should remain enabled")
	}
}

func TestTerminalRunEnvSecretsInjectsWithoutLeakingEvidence(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Root{App: config.AppConfig{Home: home}}
	if err := (secret.Store{Home: home}).Set("service/token", "super-secret-value"); err != nil {
		t.Fatal(err)
	}
	registry := tool.NewRegistry(cfg)
	terminal, ok := registry.Get("terminal.run")
	if !ok {
		t.Fatal("terminal.run missing")
	}
	result := terminal.Run(context.Background(), agentcore.ToolCall{
		ID:   "call_1",
		Name: "terminal.run",
		Args: map[string]any{
			"command":     `printf '%s' "$SERVICE_TOKEN"`,
			"env_secrets": []any{map[string]any{"id": "service/token", "env": "SERVICE_TOKEN"}},
		},
	})
	if result.IsError {
		t.Fatalf("terminal.run failed: %s", result.Content)
	}
	if result.Content != "super-secret-value" {
		t.Fatalf("secret was not injected, content %q", result.Content)
	}
	if fmt.Sprint(result.Evidence) == "" || strings.Contains(fmt.Sprint(result.Evidence), "super-secret-value") {
		t.Fatalf("evidence leaked secret value: %#v", result.Evidence)
	}
	if !strings.Contains(fmt.Sprint(result.Evidence), "service/token") || !strings.Contains(fmt.Sprint(result.Evidence), "SERVICE_TOKEN") {
		t.Fatalf("evidence should include secret id/env only, got %#v", result.Evidence)
	}
}

func TestTerminalRunRejectsKnownSecretLiteralInCommand(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Root{App: config.AppConfig{Home: home}}
	if err := (secret.Store{Home: home}).Set("service/token", "literal-secret"); err != nil {
		t.Fatal(err)
	}
	terminal, _ := tool.NewRegistry(cfg).Get("terminal.run")
	result := terminal.Run(context.Background(), agentcore.ToolCall{
		ID:   "call_1",
		Name: "terminal.run",
		Args: map[string]any{"command": "echo literal-secret"},
	})
	if !result.IsError || !strings.Contains(result.Content, "env_secrets") {
		t.Fatalf("expected secret literal block with env_secrets guidance, got %#v", result)
	}
}

func TestScheduleToolsManageTasksWithoutPendingReview(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	runAt := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "schedule.manage",
			Args: map[string]any{"action": "create", "run_at": runAt, "text": "say hello", "session_key": "cli:scheduled"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "scheduled"},
	}}, rt.Tools)

	if _, err := rt.Handle(context.Background(), inbound("cli:test", "schedule this")); err != nil {
		t.Fatal(err)
	}
	state := loadState(t, rt, "cli:test")
	if state.Pending != nil {
		t.Fatalf("schedule create should not create pending review, got %#v", state.Pending)
	}
	tasks, err := (schedule.Store{Home: rt.home()}).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != "active" {
		t.Fatalf("expected active schedule, got %#v", tasks)
	}
	id := tasks[0].ID
	for _, call := range []agentcore.ToolCall{
		{ID: "pause", Name: "schedule.manage", Args: map[string]any{"action": "pause", "id": id}},
		{ID: "resume", Name: "schedule.manage", Args: map[string]any{"action": "resume", "id": id}},
		{ID: "run", Name: "schedule.manage", Args: map[string]any{"action": "run_now", "id": id}},
		{ID: "update", Name: "schedule.manage", Args: map[string]any{"action": "update", "id": id, "text": "say updated"}},
		{ID: "delete", Name: "schedule.manage", Args: map[string]any{"action": "delete", "id": id}},
	} {
		toolDef, ok := rt.Tools.Get(call.Name)
		if !ok {
			t.Fatalf("missing %s", call.Name)
		}
		if result := toolDef.Run(context.Background(), call); result.IsError {
			t.Fatalf("%s failed: %#v", call.Name, result)
		}
	}
}

func TestTaskSearchAndResumeFindCurrentAndArchivedTasks(t *testing.T) {
	rt := newTestRuntime(t)
	state := session.State{Key: "cli:test"}
	current := state.StartTask("current deployment checklist")
	current.Summary = "Deploy API and worker."
	if err := rt.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	archived := session.State{Key: "cli:test"}
	old := archived.StartTask("archived README summary")
	old.Status = "completed"
	old.Summary = "README summarized."
	if _, err := rt.Store.Archive(archived); err != nil {
		t.Fatal(err)
	}

	searchTool, _ := rt.Tools.Get("task.search")
	result := searchTool.Run(context.Background(), agentcore.ToolCall{ID: "search", Name: "task.search", Args: map[string]any{"query": "README", "session_key": "cli:test"}})
	if result.IsError || !strings.Contains(result.Content, "archived README summary") {
		t.Fatalf("expected archived result, got %#v", result)
	}
	resumeTool, _ := rt.Tools.Get("task.resume")
	resume := resumeTool.Run(context.Background(), agentcore.ToolCall{ID: "resume", Name: "task.resume", Args: map[string]any{"session_key": "cli:test", "archive_id": result.Evidence["candidates"].([]map[string]any)[0]["archive_id"], "task_id": old.ID}})
	if resume.IsError || !strings.Contains(resume.Content, old.Summary) {
		t.Fatalf("expected resume context, got %#v", resume)
	}
	currentResult := searchTool.Run(context.Background(), agentcore.ToolCall{ID: "search2", Name: "task.search", Args: map[string]any{"query": "deployment", "session_key": "cli:test"}})
	if currentResult.IsError || !strings.Contains(currentResult.Content, current.ID) {
		t.Fatalf("expected current result, got %#v", currentResult)
	}
}

func TestMemoryProposalReviewAcceptsOnlyNumericChoices(t *testing.T) {
	rt := newTestRuntime(t)
	proposal, err := (memory.ProposalStore{Home: rt.home()}).Create(memory.CreateProposalInput{
		Type:       "experience",
		Scope:      "agent",
		Title:      "Remember numeric review",
		Body:       "Use numeric memory proposal review choices.",
		Sources:    []string{"test"},
		Confidence: "low",
	})
	if err != nil {
		t.Fatal(err)
	}
	state := session.State{Key: "cli:test"}
	task := state.StartTask("review memory proposal")
	state.Pending = &session.PendingAction{Kind: "memory_proposal_review", TaskID: task.ID, ProposalID: proposal.ID}
	if err := rt.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "treated as task input"}, rt.Tools)

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "save"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Style != "input_required" || !strings.Contains(resp.Reply.Text, "1") || !strings.Contains(resp.Reply.Text, "2") {
		t.Fatalf("expected numeric choice prompt, got %#v", resp.Reply)
	}
	afterText := loadState(t, rt, "cli:test")
	if afterText.Pending == nil || afterText.Pending.ProposalID != proposal.ID {
		t.Fatalf("non-numeric text should leave memory proposal pending, got %#v", afterText.Pending)
	}
	state.Pending = &session.PendingAction{Kind: "memory_proposal_review", TaskID: task.ID, ProposalID: proposal.ID}
	if err := rt.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	resp, err = rt.Handle(context.Background(), inbound("cli:test", "1"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Style != "completed" {
		t.Fatalf("expected numeric commit reply, got %#v", resp.Reply)
	}
	if _, err := os.Stat(filepath.Join(rt.home(), "workspace", "memory")); err != nil {
		t.Fatalf("expected committed memory under workspace memory: %v", err)
	}
}

func TestRuntimeFailedIterationLimitCanContinueActiveTask(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	rt.Config.Execution.MaxIterations = intPtrTest(1)
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "file.read",
			Args: map[string]any{"path": "."},
		}}},
	}}, rt.Tools)

	if _, err := rt.Handle(context.Background(), inbound("cli:test", "inspect project")); err != nil {
		t.Fatal(err)
	}
	state := loadState(t, rt, "cli:test")
	if len(state.Tasks) != 1 || state.ActiveTask != state.Tasks[0].ID || state.Tasks[0].Status != "failed" {
		t.Fatalf("expected failed task to remain active, got active=%q tasks=%#v", state.ActiveTask, state.Tasks)
	}

	model := &captureUserModel{text: "continued"}
	rt.Pool.agents["main"] = agentcore.NewAgent(model, rt.Tools)
	if _, err := rt.Handle(context.Background(), inbound("cli:test", "continue")); err != nil {
		t.Fatal(err)
	}
	state = loadState(t, rt, "cli:test")
	if len(state.Tasks) != 1 {
		t.Fatalf("continue should reuse active failed task, got %#v", state.Tasks)
	}
	if !strings.Contains(model.lastUser, "inspect project") || !strings.Contains(model.lastUser, "continue") {
		t.Fatalf("expected continuation to steer into failed task, got %q", model.lastUser)
	}
}

func TestRuntimeInputRequestKeepsTaskActiveForUserContinuation(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "I need you to authorize Lark first. Please reply when authorization is complete."}, rt.Tools)

	if _, err := rt.Handle(context.Background(), inbound("cli:test", "create a Lark document from /tmp/source.md")); err != nil {
		t.Fatal(err)
	}
	state := loadState(t, rt, "cli:test")
	if len(state.Tasks) != 1 || state.ActiveTask != state.Tasks[0].ID || state.Tasks[0].Status != "await_user_input" {
		t.Fatalf("expected input request to keep task active, active=%q tasks=%#v", state.ActiveTask, state.Tasks)
	}

	model := &captureUserModel{text: "continuing"}
	rt.Pool.agents["main"] = agentcore.NewAgent(model, rt.Tools)
	if _, err := rt.Handle(context.Background(), inbound("cli:test", "开通了")); err != nil {
		t.Fatal(err)
	}
	state = loadState(t, rt, "cli:test")
	if len(state.Tasks) != 1 {
		t.Fatalf("expected continuation to reuse original task, got %#v", state.Tasks)
	}
	if !strings.Contains(model.lastUser, "create a Lark document from /tmp/source.md") || !strings.Contains(model.lastUser, "开通了") {
		t.Fatalf("expected user continuation to include original task and new input, got %q", model.lastUser)
	}
}

func TestRuntimeContinuationOfferKeepsTaskActive(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "Found 2 files. If you'd like, I can read either one."}, rt.Tools)

	if _, err := rt.Handle(context.Background(), inbound("cli:test", "list readmes")); err != nil {
		t.Fatal(err)
	}
	state := loadState(t, rt, "cli:test")
	if len(state.Tasks) != 1 || state.ActiveTask != state.Tasks[0].ID || state.Tasks[0].Status != "await_user_input" {
		t.Fatalf("expected continuation offer to keep task active, active=%q tasks=%#v", state.ActiveTask, state.Tasks)
	}

	model := &captureUserModel{text: "reading"}
	rt.Pool.agents["main"] = agentcore.NewAgent(model, rt.Tools)
	if _, err := rt.Handle(context.Background(), inbound("cli:test", "yes")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(model.lastUser, "list readmes") || !strings.Contains(model.lastUser, "yes") {
		t.Fatalf("expected short confirmation to steer into offer task, got %q", model.lastUser)
	}
}

func TestRuntimeIndependentRequestAfterContinuationOfferStartsNewTask(t *testing.T) {
	rt := newTestRuntime(t)
	state := session.State{Key: "cli:test"}
	task := state.StartTask("list readmes")
	state.AwaitUserInputActiveTaskWithSummary("Found files. If you'd like, I can read one.", "trace-one", "/tmp/trace-one.jsonl")
	if task.Status != "await_user_input" {
		t.Fatalf("setup failed: %#v", task)
	}
	if err := rt.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	model := &captureUserModel{text: "new task done"}
	rt.Pool.agents["main"] = agentcore.NewAgent(model, rt.Tools)

	if _, err := rt.Handle(context.Background(), inbound("cli:test", "Now list every yaml file under ~/.mateway/config")); err != nil {
		t.Fatal(err)
	}
	updated := loadState(t, rt, "cli:test")
	if len(updated.Tasks) != 2 || updated.Tasks[1].Goal != "Now list every yaml file under ~/.mateway/config" {
		t.Fatalf("expected independent request to create new task, got %#v", updated.Tasks)
	}
	if strings.Contains(model.lastUser, "Active task:") {
		t.Fatalf("independent request should not be merged into previous offer, got %q", model.lastUser)
	}
}

func TestRuntimeIndependentRequestAfterFailedTaskStartsNewTask(t *testing.T) {
	rt := newTestRuntime(t)
	state := session.State{Key: "cli:test"}
	state.StartTask("看trace")
	state.BlockActiveTask("failed")
	if err := rt.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	model := &captureUserModel{text: "memory checked"}
	rt.Pool.agents["main"] = agentcore.NewAgent(model, rt.Tools)

	if _, err := rt.Handle(context.Background(), inbound("cli:test", "现在检查一下你有哪些记忆，是否记得远程国外服务器地址")); err != nil {
		t.Fatal(err)
	}
	updated := loadState(t, rt, "cli:test")
	if len(updated.Tasks) != 2 || updated.Tasks[1].Goal != "现在检查一下你有哪些记忆，是否记得远程国外服务器地址" {
		t.Fatalf("expected independent memory request to create new task, got %#v", updated.Tasks)
	}
	if strings.Contains(model.lastUser, "看trace") {
		t.Fatalf("new request should not be steered into failed trace task, got %q", model.lastUser)
	}
}

func TestRuntimeProgressSinkDoesNotReplayHistoricalTaskEvents(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	state := session.State{Key: "cli:test"}
	task := state.StartTask("inspect memory")
	state.AddExecutionEvent(task.ID, session.ExecutionEvent{Type: "task_contract_unsatisfied", Status: "failed", Summary: "old missing trace"})
	if err := rt.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "file.read",
			Args: map[string]any{"path": rt.home()},
		}}},
		{Role: agentcore.RoleAssistant, Content: "done"},
	}}, rt.Tools)
	var updates []channel.OutboundMessage
	rt.ProgressSink = func(msg channel.OutboundMessage) {
		updates = append(updates, msg)
	}

	if _, err := rt.Handle(context.Background(), inbound("cli:test", "continue")); err != nil {
		t.Fatal(err)
	}
	if len(updates) == 0 {
		t.Fatal("expected progress updates")
	}
	for _, update := range updates {
		for _, step := range update.Progress {
			if strings.Contains(step.Summary, "old missing trace") || step.Title == "task_contract_unsatisfied" {
				t.Fatalf("progress replayed historical event: %#v", updates)
			}
		}
	}
}

func TestRuntimeProgressHidesContractFollowupButKeepsEvent(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	rt.Config.Execution.MaxContractFollowups = 4
	registry := agentcore.NewToolRegistry()
	// failCount=1 means the first call fails, the second succeeds. This
	// produces one contract_followup event followed by a satisfied contract
	// so the task ends as completed (not followup_limit).
	registry.Register(&runtimeFailingTool{
		name:      "file.edit",
		errorText: "old_string not found in file",
		isError:   true,
		evidence:  map[string]any{"path": "/tmp/f.txt", "matches": 0},
		failCount: 1,
	})
	rt.Tools = registry
	rt.ContractModel = contractJSONModel{json: `{"summary":"edit file","requires_tools":true,"required_tools":["file.edit"],"expected_outcome":"file edited"}`}
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "file.edit",
			Args: map[string]any{"path": "/tmp/f.txt", "old_string": "old", "new_string": "new"},
		}}},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_2",
			Name: "file.edit",
			Args: map[string]any{"path": "/tmp/f.txt", "old_string": "found", "new_string": "new"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "done"},
	}}, rt.Tools)

	var updates []channel.OutboundMessage
	rt.ProgressSink = func(msg channel.OutboundMessage) {
		updates = append(updates, msg)
	}
	planResp, err := rt.Handle(context.Background(), inbound("cli:test", "edit file"))
	if err != nil {
		t.Fatal(err)
	}
	if planResp.Reply.Style != channel.StyleInputRequired {
		t.Fatalf("expected plan review, got %#v", planResp.Reply)
	}
	if _, err := rt.Handle(context.Background(), inbound("cli:test", "1")); err != nil {
		t.Fatal(err)
	}

	// The task execution events must still record contract_followup so the
	// runtime post-loop can re-derive followupCount from the task itself.
	state := loadState(t, rt, "cli:test")
	if state.Tasks[0].Status != "completed" {
		t.Fatalf("expected completed task, got %#v", state.Tasks[0])
	}
	followupCount := 0
	for _, ev := range state.Tasks[0].Execution.Events {
		if ev.Type == "contract_followup" {
			followupCount++
		}
	}
	if followupCount == 0 {
		t.Fatalf("expected contract_followup execution event to be recorded, got %#v", state.Tasks[0].Execution.Events)
	}

	// Progress sink output and the public progress steps helper must hide
	// the contract_followup title. The event is still on the task; only the
	// rendered progress is filtered.
	for _, update := range updates {
		for _, step := range update.Progress {
			if step.Title == "contract_followup" {
				t.Fatalf("progress step leaked contract_followup title: %#v", step)
			}
		}
	}
	steps := progressStepsForTask(state, state.Tasks[0].ID)
	for _, step := range steps {
		if step.Title == "contract_followup" {
			t.Fatalf("progressStepsForTask leaked contract_followup title: %#v", step)
		}
	}
}

func TestRuntimeEmptyActionPromiseDoesNotCompleteTask(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, Content: "Confirming authorization:"},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "file.read",
			Args: map[string]any{"path": rt.Config.App.Home},
		}}},
		{Role: agentcore.RoleAssistant, Content: "authorization checked"},
	}}, rt.Tools)

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "check authorization files"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Text != "authorization checked" || resp.Failed {
		t.Fatalf("expected same-turn repair to complete, got resp=%#v", resp)
	}
	state := loadState(t, rt, "cli:test")
	if len(state.Tasks) != 1 || state.ActiveTask != "" || state.Tasks[0].Status != "completed" {
		t.Fatalf("expected repaired task to complete, active=%q tasks=%#v", state.ActiveTask, state.Tasks)
	}
	data, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "deliverable_gate_followup") {
		t.Fatalf("expected deliverable gate trace, got:\n%s", string(data))
	}
}

func TestRuntimeEmptyActionPromiseFallsBackAfterOneRepair(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, Content: "Confirming authorization:"},
		{Role: agentcore.RoleAssistant, Content: "Still confirming:"},
	}}, rt.Tools)

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "check authorization files"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Style != "partial" || !resp.Failed {
		t.Fatalf("expected repeated empty action promise to be partial, got resp=%#v", resp)
	}
	state := loadState(t, rt, "cli:test")
	if len(state.Tasks) != 1 || state.ActiveTask != state.Tasks[0].ID || state.Tasks[0].Status != "failed" {
		t.Fatalf("expected repeated empty action promise to keep failed task active, active=%q tasks=%#v", state.ActiveTask, state.Tasks)
	}
	data, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "deliverable_gate_followup") != 1 {
		t.Fatalf("expected exactly one deliverable gate follow-up, got:\n%s", string(data))
	}
}

func TestPreviousTaskContextSupportsContinuityJudgment(t *testing.T) {
	state := session.State{Key: "cli:test"}
	first := state.StartTask("create a Lark document from /tmp/source.md")
	state.CompleteActiveTaskWithSummary("Waiting for authorization.", "trace-one", "/tmp/trace-one.jsonl")
	second := state.StartTask("开通了")

	prompt := appendPreviousTaskContext("Base prompt.", state, second.ID, "开通了")
	if !strings.Contains(prompt, "Continuity judgment:") {
		t.Fatalf("expected previous task context, got %q", prompt)
	}
	if !strings.Contains(prompt, "appears to confirm a blocker from a prior task") {
		t.Fatalf("expected continuity guidance for blocker confirmation, got %q", prompt)
	}
	if !strings.Contains(prompt, first.Goal) || !strings.Contains(prompt, "Waiting for authorization.") {
		t.Fatalf("expected previous task goal and summary, got %q", prompt)
	}
	if strings.Contains(prompt, second.Goal+"\n  status") {
		t.Fatalf("current task should not be included as previous context, got %q", prompt)
	}
}

func TestRuntimeNewTaskReceivesPreviousTaskContinuityContext(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	state := session.State{Key: "cli:test"}
	state.StartTask("create a Lark document from /tmp/source.md")
	state.CompleteActiveTaskWithSummary("Waiting for authorization.", "trace-one", "/tmp/trace-one.jsonl")
	if err := rt.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	model := &capturePromptModel{text: "done"}
	rt.Pool.agents["main"] = agentcore.NewAgent(model, rt.Tools)

	if _, err := rt.Handle(context.Background(), inbound("cli:test", "ok")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(model.systemPrompt, "create a Lark document from /tmp/source.md") ||
		!strings.Contains(model.systemPrompt, "Waiting for authorization.") {
		t.Fatalf("expected previous task context in system prompt, got %q", model.systemPrompt)
	}
	updated := loadState(t, rt, "cli:test")
	if len(updated.Tasks) != 2 || updated.Tasks[1].Goal != "ok" {
		t.Fatalf("expected completed previous task to create a new task, got %#v", updated.Tasks)
	}
}

func TestIndependentTaskDoesNotReceivePreviousTaskContinuityContext(t *testing.T) {
	rt := newTestRuntime(t)
	state := session.State{Key: "cli:test"}
	state.StartTask("create a Lark document from /tmp/source.md")
	state.CompleteActiveTaskWithSummary("Waiting for authorization.", "trace-one", "/tmp/trace-one.jsonl")
	if err := rt.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	model := &capturePromptModel{text: "done"}
	rt.Pool.agents["main"] = agentcore.NewAgent(model, rt.Tools)

	if _, err := rt.Handle(context.Background(), inbound("cli:test", "summarize this repository architecture")); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(model.systemPrompt, "Continuity judgment:") || strings.Contains(model.systemPrompt, "create a Lark document from /tmp/source.md") {
		t.Fatalf("independent task should not receive previous task context, got %q", model.systemPrompt)
	}
}

func TestRuntimeFollowupReusesAwaitingTask(t *testing.T) {
	rt := newTestRuntime(t)
	state := session.State{Key: "cli:followup"}
	task := state.StartTask("create a Lark document from /tmp/source.md")
	state.AwaitUserInputActiveTaskWithSummary("Waiting for authorization.", "trace-one", "/tmp/trace-one.jsonl")
	if err := rt.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	model := &captureUserModel{text: "continued"}
	rt.Pool.agents["main"] = agentcore.NewAgent(model, rt.Tools)
	if _, err := rt.Handle(context.Background(), inbound("cli:followup", "ok")); err != nil {
		t.Fatal(err)
	}
	updated := loadState(t, rt, "cli:followup")
	if len(updated.Tasks) != 1 || updated.Tasks[0].ID != task.ID {
		t.Fatalf("expected followup to reuse awaiting task, got %#v", updated.Tasks)
	}
	if !strings.Contains(model.lastUser, "create a Lark document") || !strings.Contains(model.lastUser, "ok") {
		t.Fatalf("expected merged followup input, got %q", model.lastUser)
	}
}

func TestCompactMessagesForStorageDropsSystemTruncatesToolAndKeepsRecent(t *testing.T) {
	var messages []agentcore.Message
	messages = append(messages, agentcore.Message{Role: agentcore.RoleSystem, Content: "system"})
	for i := 0; i < storedRecentMessagesLimit+5; i++ {
		messages = append(messages, agentcore.Message{Role: agentcore.RoleUser, Content: "user"})
	}
	messages = append(messages, agentcore.Message{Role: agentcore.RoleTool, Content: strings.Repeat("x", storedToolContentLimit+500)})

	out, stats := compactMessagesForStorage(redactMessagesForStorage(messages))
	if len(out) != storedRecentMessagesLimit {
		t.Fatalf("expected recent limit, got %d", len(out))
	}
	for _, msg := range out {
		if msg.Role == agentcore.RoleSystem {
			t.Fatalf("system message persisted: %#v", out)
		}
		if msg.Role == agentcore.RoleTool && !strings.Contains(msg.Content, "truncated") {
			t.Fatalf("expected truncated tool content, got %d chars", len(msg.Content))
		}
	}
	if stats.DroppedSystem != 1 || stats.TruncatedTools != 1 || stats.DroppedOld == 0 {
		t.Fatalf("unexpected stats %#v", stats)
	}
}

func TestCompactToolResultForModelAddsHeadroom(t *testing.T) {
	home := t.TempDir()
	result := compactToolResultForModel(agentcore.ToolCall{ID: "call_1", Name: "file.read"}, agentcore.ToolResult{
		ToolCallID: "call_1",
		Content:    strings.Repeat("x", modelToolContentLimit+500),
		Evidence:   map[string]any{"path": "/tmp/result"},
	}, home, "trace-1")
	if len(result.Content) > modelToolContentLimit+320 || !strings.Contains(result.Content, "truncated") {
		t.Fatalf("expected compacted model tool result, got %d chars", len(result.Content))
	}
	if result.Evidence["model_content_truncated"] != true || result.Evidence["model_content_limit"] != modelToolContentLimit {
		t.Fatalf("expected truncation evidence, got %#v", result.Evidence)
	}
	rawRef, ok := result.Evidence["raw_ref"].(string)
	if !ok || !strings.HasPrefix(rawRef, "tool-result:") {
		t.Fatalf("expected raw_ref evidence, got %#v", result.Evidence)
	}
	rawPath, ok := result.Evidence["raw_path"].(string)
	if !ok {
		t.Fatalf("expected raw_path evidence, got %#v", result.Evidence)
	}
	data, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != modelToolContentLimit+500 {
		t.Fatalf("expected stored raw content, got %d bytes", len(data))
	}
}

func TestCompactToolResultForModelCompactsLogs(t *testing.T) {
	content := strings.Join([]string{
		strings.Repeat("boot\n", 400),
		"warning: slow path",
		strings.Repeat("noise\n", 1200),
		"fatal: command timed out",
		strings.Repeat("done\n", 400),
	}, "\n")
	result := compactToolResultForModel(agentcore.ToolCall{Name: "terminal.run"}, agentcore.ToolResult{
		ToolCallID: "call_1",
		Content:    content,
	}, t.TempDir(), "trace-1")
	if !strings.Contains(result.Content, "priority lines") || !strings.Contains(result.Content, "fatal: command timed out") {
		t.Fatalf("expected priority log lines, got %q", result.Content)
	}
	if result.Evidence["model_content_compressor"] != "log" {
		t.Fatalf("expected log compressor evidence, got %#v", result.Evidence)
	}
}

func TestCompactToolResultForModelCompactsHTML(t *testing.T) {
	content := "<html><head><style>.x{}</style><script>alert(1)</script></head><body><h1>Title</h1><p>" + strings.Repeat("body ", 3000) + "</p></body></html>"
	result := compactToolResultForModel(agentcore.ToolCall{Name: "web.fetch"}, agentcore.ToolResult{
		ToolCallID: "call_1",
		Content:    content,
	}, t.TempDir(), "trace-1")
	if strings.Contains(result.Content, "<script") || !strings.Contains(result.Content, "Title") {
		t.Fatalf("expected html text extraction, got %q", result.Content[:minInt(len(result.Content), 200)])
	}
	if result.Evidence["model_content_compressor"] != "html_text" {
		t.Fatalf("expected html compressor evidence, got %#v", result.Evidence)
	}
}

func newTestRuntime(t *testing.T) Runtime {
	t.Helper()
	home := t.TempDir()
	cfg := &config.Root{
		App:       config.AppConfig{Home: home},
		Execution: config.ExecutionConfig{MaxIterations: intPtrTest(8), InactivityTimeout: "0s"},
		Agents:    config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}},
		Scheduler: config.SchedulerConfig{Enabled: true, Timezone: "UTC"},
	}
	rt := New(cfg)
	rt.ContractModel = contractJSONModel{json: `{"summary":"test task","requires_tools":false,"required_tools":[],"required_evidence":[],"expected_outcome":"answer directly","completion_policy":"answer directly"}`}
	return rt
}

func skipLegacyAgentLoopTest(t *testing.T) {
	t.Helper()
	t.Skip("legacy linear AgentCore loop test; Task Graph is now the runtime main path")
}

func inbound(sessionKey, text string) channel.InboundMessage {
	return channel.InboundMessage{ID: "msg", Channel: "cli", SessionKey: sessionKey, Text: text}
}

func loadState(t *testing.T, rt Runtime, key string) session.State {
	t.Helper()
	state, err := rt.Store.Load(key)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func evidenceIntValue(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func intPtrTest(value int) *int {
	return &value
}

type staticTextModel struct {
	text string
}

func (m staticTextModel) Next(context.Context, agentcore.Context) (agentcore.Message, error) {
	return agentcore.Message{Role: agentcore.RoleAssistant, Content: m.text}, nil
}

type toolCallingModel struct{}

func (m toolCallingModel) Next(context.Context, agentcore.Context) (agentcore.Message, error) {
	return agentcore.Message{
		Role:    agentcore.RoleAssistant,
		Content: "tool call ignored by model node",
		ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "terminal.run",
			Args: map[string]any{"cmd": "echo should-not-run"},
		}},
	}, nil
}

type blockingModel struct {
	started chan<- struct{}
	release <-chan struct{}
	text    string
}

func (m blockingModel) Next(ctx context.Context, _ agentcore.Context) (agentcore.Message, error) {
	close(m.started)
	select {
	case <-ctx.Done():
		return agentcore.Message{}, ctx.Err()
	case <-m.release:
		return agentcore.Message{Role: agentcore.RoleAssistant, Content: m.text}, nil
	}
}

type cancelledModel struct {
	started chan<- struct{}
}

func (m cancelledModel) Next(ctx context.Context, _ agentcore.Context) (agentcore.Message, error) {
	close(m.started)
	<-ctx.Done()
	return agentcore.Message{}, ctx.Err()
}

type captureUserModel struct {
	text     string
	lastUser string
}

func (m *captureUserModel) Next(_ context.Context, ctx agentcore.Context) (agentcore.Message, error) {
	for i := len(ctx.Messages) - 1; i >= 0; i-- {
		if ctx.Messages[i].Role == agentcore.RoleUser {
			m.lastUser = ctx.Messages[i].Content
			break
		}
	}
	return agentcore.Message{Role: agentcore.RoleAssistant, Content: m.text}, nil
}

type contractJSONModel struct {
	json string
}

func (m contractJSONModel) Next(context.Context, agentcore.Context) (agentcore.Message, error) {
	return agentcore.Message{Role: agentcore.RoleAssistant, Content: m.json}, nil
}

type panicModel struct {
	t *testing.T
}

func (m panicModel) Next(context.Context, agentcore.Context) (agentcore.Message, error) {
	m.t.Helper()
	m.t.Fatal("model should not be called")
	return agentcore.Message{}, nil
}

type dynamicContractModel struct {
	gen func() string
}

func (m *dynamicContractModel) Next(context.Context, agentcore.Context) (agentcore.Message, error) {
	return agentcore.Message{Role: agentcore.RoleAssistant, Content: m.gen()}, nil
}

func mustTrace(t *testing.T) *traceRecorder {
	t.Helper()
	cfg := config.DefaultRoot()
	tr, err := newTraceRecorder(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

type capturePromptModel struct {
	text         string
	systemPrompt string
}

func (m *capturePromptModel) Next(_ context.Context, ctx agentcore.Context) (agentcore.Message, error) {
	m.systemPrompt = ctx.SystemPrompt
	return agentcore.Message{Role: agentcore.RoleAssistant, Content: m.text}, nil
}

type sequenceModel struct {
	messages []agentcore.Message
	index    int
}

func (m *sequenceModel) Next(context.Context, agentcore.Context) (agentcore.Message, error) {
	if len(m.messages) == 0 {
		return agentcore.Message{Role: agentcore.RoleAssistant, Content: "done"}, nil
	}
	index := m.index
	if index >= len(m.messages) {
		index = len(m.messages) - 1
	}
	m.index++
	return m.messages[index], nil
}

type runtimeSlowTool struct {
	delay time.Duration
}

func (runtimeSlowTool) Name() string        { return "test.runtime_slow" }
func (runtimeSlowTool) Description() string { return "test runtime slow" }
func (runtimeSlowTool) Schema() agentcore.Schema {
	return agentcore.Schema{Required: []string{"text"}}
}
func (runtimeSlowTool) Risk() agentcore.Risk { return agentcore.RiskSafeRead }
func (runtimeSlowTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{ParallelMode: "read_only_ok"}
}
func (t runtimeSlowTool) Run(ctx context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	timer := time.NewTimer(t.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return agentcore.ToolResult{ToolCallID: call.ID, Content: ctx.Err().Error(), IsError: true}
	case <-timer.C:
		return agentcore.ToolResult{ToolCallID: call.ID, Content: fmt.Sprint(call.Args["text"])}
	}
}

type runtimeNamedTool struct {
	name     string
	content  string
	evidence map[string]any
}

func (t runtimeNamedTool) Name() string      { return t.name }
func (runtimeNamedTool) Description() string { return "test named tool" }
func (runtimeNamedTool) Schema() agentcore.Schema {
	return agentcore.Schema{}
}
func (runtimeNamedTool) Risk() agentcore.Risk { return agentcore.RiskSafeRead }
func (runtimeNamedTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{ParallelMode: "read_only_ok", Evidence: "test evidence", Acceptance: "accepted when content is returned"}
}
func (t runtimeNamedTool) Run(_ context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	ev := t.evidence
	if ev == nil {
		ev = map[string]any{"test": true}
	}
	return agentcore.ToolResult{ToolCallID: call.ID, Content: t.content, Evidence: ev}
}

type captureCommandTool struct {
	name     string
	content  string
	commands []string
}

func (t *captureCommandTool) Name() string        { return t.name }
func (t *captureCommandTool) Description() string { return "test command capture tool" }
func (t *captureCommandTool) Schema() agentcore.Schema {
	return agentcore.Schema{Required: []string{"command"}}
}
func (t *captureCommandTool) Risk() agentcore.Risk { return agentcore.RiskGuardedMutation }
func (t *captureCommandTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{ParallelMode: "never", Evidence: "captured command", Acceptance: "accepted when command is captured"}
}
func (t *captureCommandTool) Run(_ context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	cmd, _ := call.Args["command"].(string)
	t.commands = append(t.commands, cmd)
	return agentcore.ToolResult{ToolCallID: call.ID, Content: t.content, Evidence: map[string]any{"command": cmd}}
}
func (t *captureCommandTool) LastCommand() string {
	if len(t.commands) == 0 {
		return ""
	}
	return t.commands[len(t.commands)-1]
}

type runtimeFailingTool struct {
	name      string
	errorText string
	isError   bool
	evidence  map[string]any
	failCount int
	calls     int
}

func (t runtimeFailingTool) Name() string      { return t.name }
func (runtimeFailingTool) Description() string { return "test failing tool" }
func (runtimeFailingTool) Schema() agentcore.Schema {
	return agentcore.Schema{}
}
func (t runtimeFailingTool) Risk() agentcore.Risk {
	if t.isError {
		return agentcore.RiskGuardedMutation
	}
	return agentcore.RiskSafeRead
}
func (runtimeFailingTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{ParallelMode: "read_only_ok"}
}
func (t *runtimeFailingTool) Run(_ context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	t.calls++
	if t.failCount > 0 && t.calls > t.failCount {
		content := "success after retry"
		if call.Args != nil {
			if path, ok := call.Args["path"].(string); ok {
				content = fmt.Sprintf("edited %s", path)
			}
		}
		return agentcore.ToolResult{ToolCallID: call.ID, Content: content, Evidence: t.evidence}
	}
	return agentcore.ToolResult{ToolCallID: call.ID, Content: t.errorText, IsError: t.isError, Evidence: t.evidence}
}

func TestFileEditMultiMatchRetryNotFinal(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "line one\nline two"})
	registry.Register(&runtimeFailingTool{
		name:      "file.edit",
		errorText: "old_string found 2 times in file; use replace_all=true to replace all, or provide more surrounding context in old_string to make it unique",
		isError:   true,
		evidence:  map[string]any{"path": "/tmp/f.txt", "matches": 2},
		failCount: 1,
	})
	rt.Tools = registry
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "file.edit",
			Args: map[string]any{"path": "/tmp/f.txt", "old_string": "line", "new_string": "replaced"},
		}}},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_2",
			Name: "file.edit",
			Args: map[string]any{"path": "/tmp/f.txt", "old_string": "line", "new_string": "replaced one\nline two", "replace_all": true},
		}}},
		{Role: agentcore.RoleAssistant, Content: "done"},
	}}, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"edit file","requires_tools":true,"required_tools":["file.edit"],"expected_outcome":"file edited"}`}

	planResp, err := rt.Handle(context.Background(), inbound("cli:test", "edit /tmp/f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if planResp.Reply.Style != channel.StyleInputRequired {
		t.Fatalf("expected plan review, got %#v", planResp)
	}

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "1"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed {
		t.Fatalf("expected retry success, got failed: %#v", resp)
	}
	if !strings.Contains(resp.Reply.Text, "done") {
		t.Fatalf("expected final done, got %q", resp.Reply.Text)
	}

	state := loadState(t, rt, "cli:test")
	task := findTaskByGoal(state, "edit /tmp/f.txt")
	accepted := acceptedTools(task)
	if !accepted["file.edit"] {
		t.Fatal("expected file.edit to be accepted after retry")
	}
	trace, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	traceStr := string(trace)
	if !strings.Contains(traceStr, "contract_followup_sent") {
		t.Fatal("expected contract_followup_sent trace")
	}
	if !strings.Contains(traceStr, "task_contract_satisfied") {
		t.Fatal("expected task_contract_satisfied trace")
	}
}

func TestFileEditEmptyOldStringFailsWithGuidance(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "content"})
	registry.Register(&runtimeFailingTool{
		name:      "file.edit",
		errorText: "old_string must not be empty",
		isError:   true,
		evidence:  map[string]any{"path": "/tmp/f.txt"},
		failCount: 1,
	})
	rt.Tools = registry
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, Content: "I will edit the file."},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "file.edit",
			Args: map[string]any{"path": "/tmp/f.txt", "old_string": "", "new_string": "x"},
		}}},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_2",
			Name: "file.edit",
			Args: map[string]any{"path": "/tmp/f.txt", "old_string": "content", "new_string": "fixed"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "fixed"},
	}}, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"edit file","requires_tools":true,"required_tools":["file.edit"],"expected_outcome":"file edited"}`}

	planResp, err := rt.Handle(context.Background(), inbound("cli:test", "edit /tmp/f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if planResp.Reply.Style != channel.StyleInputRequired {
		t.Fatalf("expected plan review, got %#v", planResp)
	}

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "1"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed {
		t.Fatalf("expected retry success, got failed: %#v", resp)
	}

	state := loadState(t, rt, "cli:test")
	task := findTaskByGoal(state, "edit /tmp/f.txt")
	accepted := acceptedTools(task)
	if !accepted["file.edit"] {
		t.Fatal("expected file.edit to be accepted after retry with correct old_string")
	}
	trace, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	traceStr := string(trace)
	if !strings.Contains(traceStr, "contract_followup_sent") {
		t.Fatal("expected contract_followup_sent trace for missing evidence follow-up")
	}
	if !strings.Contains(traceStr, "task_contract_satisfied") {
		t.Fatal("expected task_contract_satisfied trace after retry")
	}
}

func TestFileEditBinaryRejectsWithFallbackGuidance(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(&runtimeFailingTool{
		name:      "file.edit",
		errorText: "file appears to be binary",
		isError:   true,
		evidence:  map[string]any{"path": "/tmp/f.bin", "bytes": 4},
	})
	registry.Register(runtimeNamedTool{name: "terminal.run", content: "binary: application/octet-stream"})
	rt.Tools = registry
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, Content: "I will check the file."},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "file.edit",
			Args: map[string]any{"path": "/tmp/f.bin", "old_string": "x", "new_string": "y"},
		}}},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_2",
			Name: "terminal.run",
			Args: map[string]any{"command": "file /tmp/f.bin"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "binary inspected"},
	}}, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"inspect binary","requires_tools":true,"required_tools":["terminal.run"],"expected_outcome":"binary inspected"}`}

	planResp, err := rt.Handle(context.Background(), inbound("cli:test", "inspect the file /tmp/f.bin"))
	if err != nil {
		t.Fatal(err)
	}
	var resp Response
	if planResp.Reply.Style == channel.StyleInputRequired {
		resp, err = rt.Handle(context.Background(), inbound("cli:test", "1"))
		if err != nil {
			t.Fatal(err)
		}
	} else {
		resp = planResp
	}
	if resp.Failed {
		t.Fatalf("expected fallback success, got failed: %#v", resp)
	}
	if !strings.Contains(resp.Reply.Text, "binary inspected") {
		t.Fatalf("expected binary inspected in final reply, got %q", resp.Reply.Text)
	}

	trace, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	traceStr := string(trace)
	// The model calls file.edit (fails, binary) then terminal.run (succeeds).
	// The trace should record tool failure classification and contract satisfaction.
	if !strings.Contains(traceStr, "tool_failures_classified") {
		t.Fatal("expected tool_failures_classified trace after file.edit binary rejection")
	}
	if !strings.Contains(traceStr, "task_contract_satisfied") {
		t.Fatal("expected task_contract_satisfied trace after terminal.run fallback")
	}
	if !strings.Contains(resp.Reply.Text, "binary inspected") {
		t.Fatalf("expected binary inspected in final reply, got: %q", resp.Reply.Text)
	}
}

func TestContractFollowupLimitProducesBlockedTask(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	rt.Config.Execution.MaxContractFollowups = 2
	registry := agentcore.NewToolRegistry()
	registry.Register(&runtimeFailingTool{
		name:      "file.edit",
		errorText: "old_string not found in file",
		isError:   true,
		evidence:  map[string]any{"path": "/tmp/f.txt", "matches": 0},
	})
	rt.Tools = registry
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "file.edit",
			Args: map[string]any{"path": "/tmp/f.txt", "old_string": "old", "new_string": "new"},
		}}},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_2",
			Name: "file.edit",
			Args: map[string]any{"path": "/tmp/f.txt", "old_string": "old", "new_string": "new"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "I give up."},
	}}, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"edit non-existent text","requires_tools":true,"required_tools":["file.edit"],"expected_outcome":"file edited"}`}

	planResp, err := rt.Handle(context.Background(), inbound("cli:test", "edit /tmp/f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if planResp.Reply.Style != channel.StyleInputRequired {
		t.Fatalf("expected plan review, got %#v", planResp)
	}
	resp, err := rt.Handle(context.Background(), inbound("cli:test", "1"))
	if err != nil {
		t.Fatal(err)
	}
	state := loadState(t, rt, "cli:test")
	if state.ActiveTask == "" {
		t.Fatalf("expected active task to remain")
	}
	trace, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	traceStr := string(trace)
	if !strings.Contains(traceStr, "task_contract_followup_limit") {
		t.Fatalf("expected followup limit trace, got:\n%s", traceStr)
	}
	if !strings.Contains(traceStr, "file.edit") {
		t.Fatalf("expected file.edit in trace, got:\n%s", traceStr)
	}
	if !strings.Contains(traceStr, "attempts_total") {
		t.Fatalf("expected attempts_total in followup limit trace, got:\n%s", traceStr)
	}
	attemptsCount := countTraceAttempts(traceStr)
	if attemptsCount < 2 {
		t.Fatalf("expected at least 2 followup attempts (MaxContractFollowups=2), got %d attempts", attemptsCount)
	}
	if !strings.Contains(traceStr, "blocker_text") {
		t.Fatalf("expected blocker_text in trace, trace:\n%s", traceStr)
	}
	if !resp.Failed {
		t.Fatal("expected resp.Failed=true when contract followup limit is reached")
	}
	if !strings.Contains(resp.Reply.Text, "blocked") {
		t.Fatalf("expected blocker in reply text, got: %q", resp.Reply.Text)
	}
}

func countTraceAttempts(traceStr string) int {
	count := 0
	for _, line := range strings.Split(traceStr, "\n") {
		if strings.Contains(line, "contract_followup_sent") {
			count++
		}
	}
	return count
}

func findTaskByGoal(state session.State, goal string) session.TaskNode {
	for _, task := range state.Tasks {
		if task.Goal == goal {
			return task
		}
	}
	return session.TaskNode{}
}

func evidenceCount(evidence map[string]any) int {
	if evidence == nil {
		return 0
	}
	switch v := evidence["count"].(type) {
	case int:
		return v
	case float64:
		return int(v)
	default:
		return 0
	}
}

func TestContractToolsBypassVisibleToolBudget(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	rt.Config.Execution.ContextBudget.MaxVisibleTools = 2
	registry := agentcore.NewToolRegistry()
	for _, name := range []string{"file.read", "web.search", "terminal.run", "schedule.manage", "task.search"} {
		registry.Register(runtimeNamedTool{name: name, content: "ok"})
	}
	rt.Tools = registry
	capture := &captureToolsModel{text: "done"}
	rt.Pool.agents["main"] = agentcore.NewAgent(capture, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"multi-tool task","requires_tools":true,"required_tools":["file.read","terminal.run","web.search"],"required_evidence":[{"kind":"external_fact","tool":"web.search","description":"search"}],"expected_outcome":"answer"}`}

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "check singbox service status"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Style == channel.StyleInputRequired {
		resp, err = rt.Handle(context.Background(), inbound("cli:test", "1"))
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(capture.toolNames) < 3 {
		t.Fatalf("expected at least 3 contract tools visible (max=2), got %d: %v", len(capture.toolNames), capture.toolNames)
	}
	for _, want := range []string{"file.read", "terminal.run", "web.search"} {
		if !containsString(capture.toolNames, want) {
			t.Fatalf("expected %s in visible tools, got %v", want, capture.toolNames)
		}
	}
}

func TestMissingContractToolProducesBlocker(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "content"})
	rt.Tools = registry
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, Content: "I cannot complete this task."},
	}}, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"test","requires_tools":true,"required_tools":["file.read","nonexistent.tool"],"expected_outcome":"done"}`}

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "use nonexistent tool"))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Failed {
		t.Fatal("expected resp.Failed=true for invalid contract tool")
	}
	if !strings.Contains(resp.Reply.Text, "nonexistent.tool") {
		t.Fatalf("expected nonexistent.tool in blocker, got: %q", resp.Reply.Text)
	}
	state := loadState(t, rt, "cli:test")
	task := findTaskByGoal(state, "use nonexistent tool")
	if task.Execution.Status != "failed" {
		t.Fatalf("expected failed execution status, got %q", task.Execution.Status)
	}
	foundEvent := false
	for _, event := range task.Execution.Events {
		if event.Type == "task_contract_invalid" {
			foundEvent = true
			if event.Status != "failed" {
				t.Fatalf("expected failed invalid contract event, got %#v", event)
			}
			if !evidenceListContains(event.Evidence["invalid_tools"], "nonexistent.tool") {
				t.Fatalf("expected invalid_tools evidence to include nonexistent.tool, got %#v", event.Evidence)
			}
		}
	}
	if !foundEvent {
		t.Fatalf("expected task_contract_invalid execution event, got %#v", task.Execution.Events)
	}

	trace, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	traceStr := string(trace)
	if !strings.Contains(traceStr, "task_contract_blocked") {
		t.Fatalf("expected task_contract_blocked trace, got:\n%s", traceStr)
	}
}

func evidenceListContains(value any, want string) bool {
	switch values := value.(type) {
	case []string:
		return containsString(values, want)
	case []any:
		for _, item := range values {
			if s, ok := item.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}

func TestProfileDeniedContractToolProducesBlocker(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	home := t.TempDir()
	cfg := &config.Root{
		App: config.AppConfig{Home: home},
		Execution: config.ExecutionConfig{
			MaxIterations:        intPtrTest(8),
			InactivityTimeout:    "0s",
			MaxContractFollowups: 2,
		},
		Agents: config.AgentsConfig{
			Default: "main",
			Profiles: []config.AgentProfileConfig{{
				ID: "main",
				Tools: config.AccessListConfig{
					Deny: []string{"terminal.run"},
				},
			}},
		},
	}
	rt := New(cfg)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "ok"})
	registry.Register(runtimeNamedTool{name: "terminal.run", content: "ok"})
	rt.Tools = registry
	rt.Pool = NewAgentPool(cfg)
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, Content: "done"},
	}}, rt.Pool.agents["main"].Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"check status","requires_tools":true,"required_tools":["terminal.run"],"expected_outcome":"status"}`}

	planResp, err := rt.Handle(context.Background(), inbound("cli:test", "check server status"))
	if err != nil {
		t.Fatal(err)
	}
	if planResp.Reply.Style != channel.StyleInputRequired {
		t.Fatalf("expected plan review, got %#v", planResp)
	}

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "1"))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Failed {
		t.Fatal("expected resp.Failed=true for profile-denied contract tool")
	}
	if !strings.Contains(resp.Reply.Text, "denied by profile") {
		t.Fatalf("expected 'denied by profile' in reply, got: %q", resp.Reply.Text)
	}
	if !strings.Contains(resp.Reply.Text, "terminal.run") {
		t.Fatalf("expected denied tool name in reply, got: %q", resp.Reply.Text)
	}

	trace, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	traceStr := string(trace)
	if !strings.Contains(traceStr, "contract_tool_unavailable") {
		t.Fatalf("expected contract_tool_unavailable trace, got:\n%s", traceStr)
	}
}

func TestCompletedTaskClearsActiveState(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "terminal.run", content: "service is running"})
	rt.Tools = registry
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, Content: "I will check the service."},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "terminal.run",
			Args: map[string]any{"command": "systemctl status"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "service is running."},
	}}, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"check service","requires_tools":true,"required_tools":["terminal.run"],"expected_outcome":"status confirmed"}`}

	planResp, err := rt.Handle(context.Background(), inbound("cli:test", "check if service is running"))
	if err != nil {
		t.Fatal(err)
	}
	if planResp.Reply.Style != channel.StyleInputRequired {
		t.Fatalf("expected plan review, got %#v", planResp)
	}

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "1"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed {
		t.Fatalf("expected completed task, got failed: %#v", resp)
	}

	state := loadState(t, rt, "cli:test")
	if state.ActiveTask != "" {
		t.Fatalf("expected ActiveTask to be empty after completion, got %q", state.ActiveTask)
	}
	task := findTaskByGoal(state, "check if service is running")
	if task.Status != "completed" {
		t.Fatalf("expected task status completed, got %q", task.Status)
	}
}

func TestUnexecutedPromiseDoesNotCompleteTask(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, Content: "I will check the status."},
		{Role: agentcore.RoleAssistant, Content: "Let me look into that."},
	}}, rt.Tools)

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "check the status"))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Failed {
		t.Fatal("expected unexecuted promise to fail, not complete")
	}

	state := loadState(t, rt, "cli:test")
	task := findTaskByGoal(state, "check the status")
	if task.Status == "completed" {
		t.Fatal("expected task NOT to be completed on unexecuted promise")
	}

	trace, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	traceStr := string(trace)
	if !strings.Contains(traceStr, "deliverable_gate_followup") {
		t.Fatal("expected deliverable_gate_followup trace for unexecuted promise")
	}
}

func TestScheduleManageEffectiveRiskMatchesTaskStepEvidence(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	actions := map[string]struct {
		risk     string
		mutation bool
	}{
		"list":   {risk: "safe_read", mutation: false},
		"delete": {risk: "dangerous", mutation: true},
		"create": {risk: "guarded_mutation", mutation: true},
	}
	for action, want := range actions {
		t.Run(action, func(t *testing.T) {
			rt := newTestRuntime(t)
			registry := agentcore.NewToolRegistry()
			registry.Register(runtimeNamedTool{name: "schedule.manage", content: "ok"})
			rt.Tools = registry
			rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
				{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
					ID:   "call_1",
					Name: "schedule.manage",
					Args: map[string]any{"action": action},
				}}},
				{Role: agentcore.RoleAssistant, Content: "done"},
			}}, rt.Tools)
			rt.ContractModel = contractJSONModel{json: `{"summary":"manage schedule","requires_tools":true,"required_tools":["schedule.manage"],"expected_outcome":"action completed"}`}

			planResp, err := rt.Handle(context.Background(), inbound("cli:test", "schedule "+action))
			if err != nil {
				t.Fatal(err)
			}
			if planResp.Reply.Style != channel.StyleInputRequired {
				t.Fatalf("expected plan review, got %#v", planResp)
			}
			resp, err := rt.Handle(context.Background(), inbound("cli:test", "1"))
			if err != nil {
				t.Fatal(err)
			}
			_ = resp
			state := loadState(t, rt, "cli:test")
			task := findTaskByGoal(state, "schedule "+action)
			if len(task.Steps) == 0 {
				t.Fatal("expected at least one task step")
			}
			step := task.Steps[len(task.Steps)-1]
			if step.Risk != want.risk {
				t.Fatalf("schedule.manage action=%s: expected Risk=%q, got %q", action, want.risk, step.Risk)
			}
			if step.Mutation != want.mutation {
				t.Fatalf("schedule.manage action=%s: expected Mutation=%v, got %v", action, want.mutation, step.Mutation)
			}
			evidenceRisk, _ := step.Evidence["risk"].(string)
			if evidenceRisk != want.risk {
				t.Fatalf("schedule.manage action=%s: expected Evidence[risk]=%q, got %q", action, want.risk, evidenceRisk)
			}
			evidenceMutation, _ := step.Evidence["mutation"].(bool)
			if evidenceMutation != want.mutation {
				t.Fatalf("schedule.manage action=%s: expected Evidence[mutation]=%v, got %v", action, want.mutation, evidenceMutation)
			}
		})
	}
}

func TestToolStepEvidenceSchemaForSuccessAndFailure(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "content"})
	registry.Register(&runtimeFailingTool{
		name:      "file.edit",
		errorText: "old_string not found in file",
		isError:   true,
		evidence:  map[string]any{"path": "/tmp/f.txt"},
	})
	rt.Tools = registry
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, Content: "I will read the file."},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "file.read",
			Args: map[string]any{"path": "/tmp/f.txt"},
		}}},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_2",
			Name: "file.edit",
			Args: map[string]any{"path": "/tmp/f.txt", "old_string": "x", "new_string": "y"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "done"},
	}}, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"read and edit","requires_tools":true,"required_tools":["file.read","file.edit"],"expected_outcome":"both attempted"}`}

	planResp, err := rt.Handle(context.Background(), inbound("cli:test", "read and edit"))
	if err != nil {
		t.Fatal(err)
	}
	if planResp.Reply.Style != channel.StyleInputRequired {
		t.Fatalf("expected plan review, got %#v", planResp)
	}
	resp, err := rt.Handle(context.Background(), inbound("cli:test", "1"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp
	state := loadState(t, rt, "cli:test")
	task := findTaskByGoal(state, "read and edit")
	if len(task.Steps) < 2 {
		t.Fatalf("expected at least 2 task steps, got %d", len(task.Steps))
	}
	for _, step := range task.Steps {
		if step.Tool == "" {
			t.Fatal("expected Tool field in task step")
		}
		if step.Status == "" {
			t.Fatalf("expected Status field in task step for %s", step.Tool)
		}
		if step.Evidence == nil {
			t.Fatalf("expected non-nil Evidence in task step for %s", step.Tool)
		}
		if step.Risk == "" {
			t.Fatalf("expected Risk field in task step for %s", step.Tool)
		}
		if step.Accepted && step.Status != "accepted" {
			t.Fatalf("Accepted=true but Status=%q for %s", step.Status, step.Tool)
		}
		if step.Status == "accepted" {
			if !step.Accepted {
				t.Fatalf("Status=accepted but Accepted=false for %s", step.Tool)
			}
			if step.Evidence["acceptance"] != "accepted" {
				t.Fatalf("expected Evidence[acceptance]=accepted for %s, got %v", step.Tool, step.Evidence["acceptance"])
			}
		}
		if step.Status == "failed" {
			if step.Evidence["acceptance"] != "failed" {
				t.Fatalf("expected Evidence[acceptance]=failed for %s, got %v", step.Tool, step.Evidence["acceptance"])
			}
		}
	}
	successStep := task.Steps[0]
	if successStep.Tool != "file.read" || successStep.Status != "accepted" {
		t.Fatalf("expected file.read accepted, got tool=%s status=%s", successStep.Tool, successStep.Status)
	}
	failedStep := task.Steps[1]
	if failedStep.Tool != "file.edit" || failedStep.Status != "failed" {
		t.Fatalf("expected file.edit failed, got tool=%s status=%s", failedStep.Tool, failedStep.Status)
	}
	if failedStep.Evidence["path"] != "/tmp/f.txt" {
		t.Fatalf("expected evidence path preserved for failed step, got %v", failedStep.Evidence["path"])
	}
}

func TestProfileProposalRecordsRequiresReviewEvidence(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{
		name:    "file.edit",
		content: "profile proposal created",
		evidence: map[string]any{
			"path":            ".mateway/agents/main.yaml",
			"requires_review": true,
			"proposal_id":     "proposal-1",
		},
	})
	rt.Tools = registry
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "file.edit",
			Args: map[string]any{"path": ".mateway/agents/main.yaml", "old_string": "old", "new_string": "new"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "done"},
	}}, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"edit profile","requires_tools":true,"required_tools":["file.edit"],"expected_outcome":"profile updated"}`}

	planResp, err := rt.Handle(context.Background(), inbound("cli:test", "edit agent profile"))
	if err != nil {
		t.Fatal(err)
	}
	if planResp.Reply.Style != channel.StyleInputRequired {
		t.Fatalf("expected plan review, got %#v", planResp)
	}
	resp, err := rt.Handle(context.Background(), inbound("cli:test", "1"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp
	state := loadState(t, rt, "cli:test")
	task := findTaskByGoal(state, "edit agent profile")
	if len(task.Steps) == 0 {
		t.Fatal("expected at least one task step")
	}
	step := task.Steps[len(task.Steps)-1]
	if v, ok := step.Evidence["requires_review"]; !ok || v != true {
		t.Fatalf("expected requires_review=true for profile edit, got evidence=%v", step.Evidence)
	}
}

func TestTraceAndTaskStepShareToolResultFacts(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "terminal.run", content: "service is running"})
	rt.Tools = registry
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, Content: "I will check the service."},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "terminal.run",
			Args: map[string]any{"command": "systemctl status"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "service is running."},
	}}, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"check service","requires_tools":true,"required_tools":["terminal.run"],"expected_outcome":"status confirmed"}`}

	planResp, err := rt.Handle(context.Background(), inbound("cli:test", "check singbox service status"))
	if err != nil {
		t.Fatal(err)
	}
	var resp Response
	if planResp.Reply.Style == channel.StyleInputRequired {
		resp, err = rt.Handle(context.Background(), inbound("cli:test", "1"))
		if err != nil {
			t.Fatal(err)
		}
	} else {
		resp = planResp
	}
	state := loadState(t, rt, "cli:test")
	task := findTaskByGoal(state, "check singbox service status")
	if len(task.Steps) == 0 || len(task.Execution.Events) == 0 {
		t.Fatal("expected task step and execution event")
	}
	step := task.Steps[len(task.Steps)-1]
	var toolResultEvent *session.ExecutionEvent
	for i := range task.Execution.Events {
		if task.Execution.Events[i].Type == "tool_result" && task.Execution.Events[i].StepID == step.ID {
			toolResultEvent = &task.Execution.Events[i]
			break
		}
	}
	if toolResultEvent == nil {
		t.Fatalf("expected tool_result execution event matching step %s", step.ID)
	}
	if toolResultEvent.Status != step.Status {
		t.Fatalf("execution event status %q != task step status %q", toolResultEvent.Status, step.Status)
	}
	if toolResultEvent.Tool != step.Tool {
		t.Fatalf("execution event tool %q != task step tool %q", toolResultEvent.Tool, step.Tool)
	}
	if toolResultEvent.Summary != step.Summary {
		t.Fatalf("execution event summary %q != task step summary %q", toolResultEvent.Summary, step.Summary)
	}

	trace, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	traceStr := string(trace)
	if !strings.Contains(traceStr, "tool_execution_end") {
		t.Fatal("expected tool_execution_end in trace")
	}
	if !strings.Contains(traceStr, "service is running") {
		t.Fatal("expected tool result content in trace")
	}
}

func TestSecretLikeEvidenceIsRedactedEverywhere(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	home := t.TempDir()
	cfg := &config.Root{
		App: config.AppConfig{Home: home},
		Execution: config.ExecutionConfig{
			MaxIterations:     intPtrTest(8),
			InactivityTimeout: "0s",
		},
		Agents: config.AgentsConfig{
			Default:  "main",
			Profiles: []config.AgentProfileConfig{{ID: "main"}},
		},
	}
	rt := New(cfg)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{
		name:    "terminal.run",
		content: "command completed",
		evidence: map[string]any{
			"command": "echo hello",
			"secret":  "sk-1234567890abcdef",
		},
	})
	rt.Tools = registry
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "terminal.run",
			Args: map[string]any{"command": "echo hello"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "done"},
	}}, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"test","requires_tools":true,"required_tools":["terminal.run"],"expected_outcome":"done"}`}

	planResp, err := rt.Handle(context.Background(), inbound("cli:test", "check singbox service status"))
	if err != nil {
		t.Fatal(err)
	}
	if planResp.Reply.Style != channel.StyleInputRequired {
		t.Fatalf("expected plan review, got %#v", planResp)
	}
	resp, err := rt.Handle(context.Background(), inbound("cli:test", "1"))
	if err != nil {
		t.Fatal(err)
	}
	state := loadState(t, rt, "cli:test")
	task := findTaskByGoal(state, "check singbox service status")
	if len(task.Steps) == 0 {
		t.Fatal("expected at least one task step")
	}
	step := task.Steps[0]
	if strings.Contains(step.Summary, "sk-1234567890abcdef") {
		t.Fatalf("secret not redacted in task step summary: %q", step.Summary)
	}
	if step.Evidence != nil {
		if v, ok := step.Evidence["secret"]; ok && v == "sk-1234567890abcdef" {
			t.Fatalf("secret not redacted in task step evidence: %v", step.Evidence)
		}
	}
	for _, ev := range task.Execution.Events {
		if ev.Summary != "" && strings.Contains(ev.Summary, "sk-1234567890abcdef") {
			t.Fatalf("secret not redacted in execution event summary: %q", ev.Summary)
		}
	}

	trace, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	traceStr := string(trace)
	if strings.Contains(traceStr, "sk-1234567890abcdef") {
		t.Fatal("secret not redacted in trace")
	}
	if strings.Contains(resp.Reply.Text, "sk-1234567890abcdef") {
		t.Fatal("secret not redacted in final reply")
	}
}

func TestCLITUIRenderTaskStepWithoutRiskRecalculation(t *testing.T) {
	schTool := runtimeNamedTool{name: "schedule.manage", content: "listed 3 items"}
	call := agentcore.ToolCall{ID: "call_1", Name: "schedule.manage", Args: map[string]any{"action": "list"}}
	result := schTool.Run(context.Background(), call)
	if result.IsError {
		t.Fatal("expected successful schedule.manage list")
	}
	status, evidence := acceptToolResult(schTool, call, result)
	risk := string(tool.EffectiveRisk(schTool, call))

	if status != "accepted" {
		t.Fatalf("expected accepted, got %q", status)
	}
	if evidence["risk"] != risk {
		t.Fatalf("evidence[risk]=%q != EffectiveRisk=%q", evidence["risk"], risk)
	}
	if evidence["mutation"] != false {
		t.Fatal("expected mutation=false for schedule.manage list")
	}
	if risk != "safe_read" {
		t.Fatalf("expected safe_read risk for schedule.manage list, got %q", risk)
	}
}

func TestOpenTaskReceivesNextMessageAsSteering(t *testing.T) {
	for _, status := range []string{"running", "await_user_input", "resuming"} {
		t.Run(status, func(t *testing.T) {
			rt := newTestRuntime(t)
			state := session.State{Key: "cli:test"}
			task := state.StartTask("original analysis goal")
			task.Status = status
			state.ActiveTask = task.ID
			if err := rt.Store.Save(state); err != nil {
				t.Fatal(err)
			}
			model := &captureUserModel{text: "steered"}
			rt.Pool.agents["main"] = agentcore.NewAgent(model, rt.Tools)

			if _, err := rt.Handle(context.Background(), inbound("cli:test", "add more detail")); err != nil {
				t.Fatal(err)
			}
			updated := loadState(t, rt, "cli:test")
			if len(updated.Tasks) != 1 || updated.Tasks[0].ID != task.ID {
				t.Fatalf("status=%s: expected steering to reuse existing task, got %d tasks", status, len(updated.Tasks))
			}
			if !strings.Contains(model.lastUser, task.Goal) {
				t.Fatalf("status=%s: expected steering merge, got %q", status, model.lastUser)
			}
		})
	}

	t.Run("failed", func(t *testing.T) {
		rt := newTestRuntime(t)
		state := session.State{Key: "cli:test"}
		task := state.StartTask("original analysis goal")
		task.Status = "failed"
		state.ActiveTask = task.ID
		if err := rt.Store.Save(state); err != nil {
			t.Fatal(err)
		}
		rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "new task"}, rt.Tools)

		if _, err := rt.Handle(context.Background(), inbound("cli:test", "add more detail")); err != nil {
			t.Fatal(err)
		}
		updated := loadState(t, rt, "cli:test")
		if len(updated.Tasks) != 2 {
			t.Fatalf("status=failed: expected new graph to create new task, got %d tasks", len(updated.Tasks))
		}
		if updated.ActiveTask != "" {
			t.Fatalf("status=failed: expected no active task after new graph + single-turn model")
		}
	})
}

func TestCompletedTaskClearsActiveAndDoesNotImplicitlyResume(t *testing.T) {
	rt := newTestRuntime(t)
	state := session.State{Key: "cli:test"}
	state.StartTask("check system status")
	state.CompleteActiveTaskWithSummary("system is running", "trace-one", "/tmp/trace-one.jsonl")
	if err := rt.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "new task result"}, rt.Tools)

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "list running processes"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed {
		t.Fatalf("new task should not fail: %#v", resp)
	}
	updated := loadState(t, rt, "cli:test")
	if len(updated.Tasks) != 2 {
		t.Fatalf("expected two tasks, got %d: %#v", len(updated.Tasks), updated.Tasks)
	}
	if updated.Tasks[1].ID == updated.Tasks[0].ID {
		t.Fatal("new message should create new task, not resume completed one")
	}
	if updated.ActiveTask != "" {
		t.Fatalf("ActiveTask should be empty after completed task + new task with single-turn model, got %q", updated.ActiveTask)
	}
}

func TestNewCommandResetsTaskContext(t *testing.T) {
	rt := newTestRuntime(t)
	state := session.State{Key: "cli:test"}
	task := state.StartTask("old analysis")
	task.Summary = "partial analysis done."
	state.Messages = []agentcore.Message{{Role: agentcore.RoleUser, Content: "old"}}
	if err := rt.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	newResp, err := rt.Handle(context.Background(), inbound("cli:test", "/new"))
	if err != nil {
		t.Fatal(err)
	}
	if newResp.Reply.Style != "session_reset" {
		t.Fatalf("expected session_reset, got %#v", newResp.Reply)
	}
	resetTrace, err := os.ReadFile(newResp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(resetTrace), "session_reset") {
		t.Fatalf("trace should contain session_reset after /new, got:\n%s", string(resetTrace))
	}

	rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "fresh start"}, rt.Tools)
	resp, err := rt.Handle(context.Background(), inbound("cli:test", "new task description"))
	if err != nil {
		t.Fatal(err)
	}
	updated := loadState(t, rt, "cli:test")
	if len(updated.Tasks) != 1 || updated.Tasks[0].Goal != "new task description" {
		t.Fatalf("expected fresh task after /new, got %#v", updated.Tasks)
	}
	if updated.Tasks[0].ID == task.ID {
		t.Fatalf("new task should not reuse archived task ID")
	}
	trace, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(trace), "partial analysis done") {
		t.Fatalf("trace should not contain old task summary after /new")
	}
}

func TestPreviousTaskWeakContextDoesNotAutoResume(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	state := session.State{Key: "cli:test"}
	first := state.StartTask("create a Lark document from /tmp/source.md")
	state.CompleteActiveTaskWithSummary("Waiting for authorization.", "trace-one", "/tmp/trace-one.jsonl")
	if err := rt.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	model := &captureUserModel{text: "started fresh"}
	rt.Pool.agents["main"] = agentcore.NewAgent(model, rt.Tools)

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "list my workspace files"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(model.lastUser, "list my workspace files") {
		t.Fatalf("expected new task's user text standalone, got %q", model.lastUser)
	}
	if strings.Contains(model.lastUser, "Active task:") || strings.Contains(model.lastUser, first.Goal) {
		t.Fatalf("weak context should not merge previous task goal, got %q", model.lastUser)
	}
	updated := loadState(t, rt, "cli:test")
	if len(updated.Tasks) != 2 {
		t.Fatalf("expected new separate task, got %d: %#v", len(updated.Tasks), updated.Tasks)
	}
	if updated.ActiveTask == first.ID {
		t.Fatalf("weak context should NOT auto-resume completed task %s, got ActiveTask=%q", first.ID, updated.ActiveTask)
	}
	if updated.Tasks[1].ID == first.ID {
		t.Fatalf("new message should create new task, not resume completed one")
	}
	trace, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(trace), "task_recall_context_injected") {
		t.Fatalf("expected task_recall_context_injected trace for previous task context:\n%s", string(trace))
	}
}

func TestExplicitResumeReturnsEvidence(t *testing.T) {
	rt := newTestRuntime(t)
	state := session.State{Key: "cli:test"}
	task := state.StartTask("deploy checklist")
	task.Summary = "deployed API."
	task.Status = "completed"
	if err := rt.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	searchTool, ok := rt.Tools.Get("task.search")
	if !ok {
		t.Fatal("task.search not registered")
	}
	searchResult := searchTool.Run(context.Background(), agentcore.ToolCall{
		ID: "search_1", Name: "task.search",
		Args: map[string]any{"query": "deploy", "session_key": "cli:test"},
	})
	if searchResult.IsError {
		t.Fatalf("task.search failed: %s", searchResult.Content)
	}
	candidates, _ := searchResult.Evidence["candidates"].([]map[string]any)
	if len(candidates) == 0 {
		t.Fatal("expected at least one candidate")
	}
	archiveID, _ := candidates[0]["archive_id"].(string)

	resumeTool, ok := rt.Tools.Get("task.resume")
	if !ok {
		t.Fatal("task.resume not registered")
	}
	resumeResult := resumeTool.Run(context.Background(), agentcore.ToolCall{
		ID: "resume_1", Name: "task.resume",
		Args: map[string]any{
			"session_key": "cli:test",
			"archive_id":  archiveID,
			"task_id":     task.ID,
		},
	})
	if resumeResult.IsError {
		t.Fatalf("task.resume failed: %s", resumeResult.Content)
	}
	if resumeResult.Evidence == nil {
		t.Fatal("task.resume result missing evidence")
	}
	if tid, _ := resumeResult.Evidence["task_id"].(string); tid != task.ID {
		t.Fatalf("resume evidence: expected task_id=%q, got %q", task.ID, tid)
	}
	if goal, _ := resumeResult.Evidence["goal"].(string); !strings.Contains(strings.ToLower(goal), "deploy") {
		t.Fatalf("resume evidence: expected goal containing 'deploy', got %q", goal)
	}
	if sid, _ := resumeResult.Evidence["session_key"].(string); sid == "" {
		t.Fatal("resume evidence: session_key should not be empty")
	}
	if !strings.Contains(resumeResult.Content, task.Summary) {
		t.Fatalf("resume content should contain task summary, got: %s", resumeResult.Content)
	}
}

func TestTaskSearchResumeEvidenceForArchivedAndCurrentSessions(t *testing.T) {
	rt := newTestRuntime(t)

	state := session.State{Key: "cli:test"}
	current := state.StartTask("current deployment checklist")
	current.Summary = "Deploy API and worker."
	if err := rt.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	archived := session.State{Key: "cli:test"}
	old := archived.StartTask("archived README summary")
	old.Status = "completed"
	old.Summary = "README summarized."
	if _, err := rt.Store.Archive(archived); err != nil {
		t.Fatal(err)
	}

	searchTool, _ := rt.Tools.Get("task.search")
	resumeTool, _ := rt.Tools.Get("task.resume")

	archivedResult := searchTool.Run(context.Background(), agentcore.ToolCall{
		ID: "search_archived", Name: "task.search",
		Args: map[string]any{"query": "README", "session_key": "cli:test"},
	})
	if archivedResult.IsError || !strings.Contains(archivedResult.Content, "archived README summary") {
		t.Fatalf("expected archived task in search, got %#v", archivedResult)
	}
	count := evidenceCount(archivedResult.Evidence)
	if count == 0 {
		t.Fatal("expected count > 0 for archived search")
	}

	currentResult := searchTool.Run(context.Background(), agentcore.ToolCall{
		ID: "search_current", Name: "task.search",
		Args: map[string]any{"query": "deployment", "session_key": "cli:test"},
	})
	if currentResult.IsError || !strings.Contains(currentResult.Content, current.ID) {
		t.Fatalf("expected current task in search, got %#v", currentResult)
	}
	count2 := evidenceCount(currentResult.Evidence)
	if count2 == 0 {
		t.Fatal("expected count > 0 for current search")
	}

	candidates, _ := archivedResult.Evidence["candidates"].([]map[string]any)
	if len(candidates) == 0 {
		t.Fatal("expected archived candidates")
	}
	candidate := candidates[0]
	resumeResult := resumeTool.Run(context.Background(), agentcore.ToolCall{
		ID: "resume_archived", Name: "task.resume",
		Args: map[string]any{
			"session_key": candidate["session_key"],
			"archive_id":  candidate["archive_id"],
			"task_id":     candidate["task_id"],
		},
	})
	if resumeResult.IsError {
		t.Fatalf("resume from archived failed: %s", resumeResult.Content)
	}
	if tid, _ := resumeResult.Evidence["task_id"].(string); tid != old.ID {
		t.Fatalf("resume evidence: expected task_id=%q, got %q", old.ID, tid)
	}
	if !strings.Contains(resumeResult.Content, old.Summary) {
		t.Fatalf("resume content should contain archived task summary: %s", resumeResult.Content)
	}

	currentCandidates, _ := currentResult.Evidence["candidates"].([]map[string]any)
	if len(currentCandidates) == 0 {
		t.Fatal("expected current candidates")
	}
	currentCandidate := currentCandidates[0]
	cResume := resumeTool.Run(context.Background(), agentcore.ToolCall{
		ID: "resume_current", Name: "task.resume",
		Args: map[string]any{
			"session_key": currentCandidate["session_key"],
			"task_id":     currentCandidate["task_id"],
		},
	})
	if cResume.IsError {
		t.Fatalf("resume from current failed: %s", cResume.Content)
	}
	if !strings.Contains(cResume.Content, current.Summary) {
		t.Fatalf("resume content should contain current task summary: %s", cResume.Content)
	}
}

func TestContractPromptSortsSkillsByPriorityAndName(t *testing.T) {
	skills := []discoveredSkill{
		{Name: "agent-browser", Description: "General web browsing and data collection.", Priority: "90", Path: "/tmp/skills/agent-browser/SKILL.md", Scope: "shared"},
		{Name: "feishu-notify", Description: "Send Feishu/Lark messages and create cloud documents.", Priority: "80", Path: "/tmp/skills/feishu-notify/SKILL.md", Scope: "shared"},
		{Name: "git-helper", Description: "Git commit, branch, and merge workflows.", Priority: "70", Path: "/tmp/skills/git-helper/SKILL.md", Scope: "shared"},
	}
	sortDiscoveredSkills(skills)

	if skills[0].Priority != "90" || skills[0].Name != "agent-browser" {
		t.Fatalf("expected highest priority skill first, got %s (pri=%s)", skills[0].Name, skills[0].Priority)
	}
	if skills[1].Priority != "80" || skills[1].Name != "feishu-notify" {
		t.Fatalf("expected second priority skill next, got %s (pri=%s)", skills[1].Name, skills[1].Priority)
	}
}

func TestContractPromptIncludesStructuredAvailableSkills(t *testing.T) {
	skills := []discoveredSkill{{
		Name:        "feishu-notify",
		Description: "Send Feishu/Lark messages.",
		Stage:       "cli",
		Priority:    "80",
		Path:        "/tmp/skills/feishu-notify/SKILL.md",
		Scope:       "shared",
	}}
	prompt := renderTaskContractPrompt("发飞书消息", "", agentcore.NewToolRegistry(), skills)
	for _, want := range []string{
		"Available skills:",
		"Skill names are instructional references",
		"name: feishu-notify",
		"description: Send Feishu/Lark messages.",
		"stage: cli",
		"priority: 80",
		"path: /tmp/skills/feishu-notify/SKILL.md",
		"scope: shared",
		"execution_hint: read SKILL.md with file.read, then execute via terminal.run",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected structured skills prompt to contain %q, got:\n%s", want, prompt)
		}
	}
	if !strings.Contains(prompt, "Do NOT put skill names in required_tools") {
		t.Fatalf("expected warning about skill names not being tools, got:\n%s", prompt)
	}
}

func TestContractValidationRejectsSkillNameAsTool(t *testing.T) {
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "ok"})
	registry.Register(runtimeNamedTool{name: "terminal.run", content: "ok"})
	skills := []discoveredSkill{
		{Name: "agent-browser", Description: "Web browsing skill.", Path: "/tmp/skills/agent-browser/SKILL.md"},
	}
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"file.read", "agent-browser"},
		PlanItems: []session.TaskPlanItem{
			{ID: "plan-1", Title: "browse web", Status: "pending", Tool: "agent-browser"},
		},
	}
	validation := validateContractTools(contract, registry, skills)
	if len(validation.InvalidTools) != 1 {
		t.Fatalf("expected 1 invalid tool, got %d: %v", len(validation.InvalidTools), validation.InvalidTools)
	}
	if !validation.HasSkillNameMismatch {
		t.Fatal("expected skill name mismatch flag")
	}
	if validation.InvalidReason() != "skill name used as tool" {
		t.Fatalf("expected skill name reason, got %q", validation.InvalidReason())
	}
}

func TestRepairContractSkillUsageRemovesSkillToolsAndAddsReadItems(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"web.search", "agent-browser", "terminal.run"},
		RequiredSkills: []session.RequiredSkill{
			{Name: "fresh-search", Path: "/tmp/skills/fresh-search/SKILL.md"},
			{Name: "agent-browser", Path: "/tmp/skills/agent-browser/SKILL.md"},
		},
		RequiredEvidence: []session.TaskEvidenceContract{
			{Kind: "current_external_fact", Tool: "web.search", Description: "market data"},
		},
		PlanItems: []session.TaskPlanItem{
			{ID: "plan-1", Title: "browse", Status: "pending", Tool: "agent-browser", Criteria: "use browser skill"},
		},
	}
	repaired := repairContractSkillUsage(contract, []discoveredSkill{
		{Name: "fresh-search", Path: "/tmp/skills/fresh-search/SKILL.md"},
		{Name: "agent-browser", Path: "/tmp/skills/agent-browser/SKILL.md"},
	})
	for _, tool := range repaired.RequiredTools {
		if tool == "agent-browser" {
			t.Fatalf("expected skill name removed from required tools, got %v", repaired.RequiredTools)
		}
	}
	if !contractHasFileReadPlanItemForSkill(repaired, "fresh-search", "/tmp/skills/fresh-search/SKILL.md") {
		t.Fatalf("expected file.read plan item for fresh-search, got %#v", repaired.PlanItems)
	}
	if !contractHasFileReadPlanItemForSkill(repaired, "agent-browser", "/tmp/skills/agent-browser/SKILL.md") {
		t.Fatalf("expected file.read plan item for agent-browser, got %#v", repaired.PlanItems)
	}
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "web.search", content: "ok"})
	registry.Register(runtimeNamedTool{name: "file.read", content: "ok"})
	registry.Register(runtimeNamedTool{name: "terminal.run", content: "ok"})
	validation := validateContractTools(repaired, registry, nil)
	if len(validation.InvalidTools) != 0 || validation.HasSkillNameMismatch {
		t.Fatalf("expected repaired contract to avoid skill/tool mismatch, got %#v", validation)
	}
}

func TestContractReplanMapsSkillToRealTools(t *testing.T) {
	rt := newTestRuntime(t)
	cfg := rt.Config
	cfg.App.Workspace = t.TempDir()
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "ok"})
	registry.Register(runtimeNamedTool{name: "terminal.run", content: "ok"})
	registry.Register(runtimeNamedTool{name: "web.search", content: "ok"})
	rt.Tools = registry

	callCount := 0
	rt.ContractModel = &dynamicContractModel{
		gen: func() string {
			callCount++
			if callCount == 1 {
				return `{"summary":"browse stock data","requires_tools":true,"required_tools":["agent-browser"],"plan_items":[{"id":"plan-1","title":"browse","status":"pending","tool":"agent-browser"}],"expected_outcome":"stock data","completion_policy":"use tool evidence"}`
			}
			return `{"summary":"browse stock data","requires_tools":true,"required_tools":["web.search","file.read","terminal.run"],"required_skills":[{"name":"agent-browser","path":"/tmp/skills/agent-browser/SKILL.md","reason":"needed for browsing"}],"plan_items":[{"id":"plan-1","title":"search","status":"pending","tool":"web.search"}],"expected_outcome":"stock data","completion_policy":"use tool evidence"}`
		},
	}

	state := session.State{Key: "cli:test"}
	task := state.StartTask("browse stock data")
	if err := rt.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	contract := rt.ensureTaskContract(context.Background(), inbound("cli:test", "browse stock data"), &state, task, "browse stock data", nil, mustTrace(t))
	if callCount < 2 {
		t.Fatal("expected replan to be triggered when skill name is used as tool, got single call")
	}
	if contract.RequiresTools && len(contract.RequiredTools) > 0 {
		for _, name := range contract.RequiredTools {
			if name == "agent-browser" {
				t.Fatalf("expected replan to remove agent-browser from required_tools, got %v", contract.RequiredTools)
			}
		}
	}
	for _, item := range contract.PlanItems {
		if item.Tool == "agent-browser" {
			t.Fatalf("expected replan to remove agent-browser from plan_items[].tool, got item %q with tool %q", item.ID, item.Tool)
		}
	}
}

func TestMissingToolBlockerIsStructuredAndDeduped(t *testing.T) {
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "ok"})
	rt.Tools = registry
	contract := session.TaskContract{
		Summary:         "查纳斯达克走势",
		RequiresTools:   true,
		RequiredTools:   []string{"web.search", "web.search", "agent-browser"},
		ExpectedOutcome: "走势分析",
	}
	validation := taskContractValidation{
		Satisfied: false,
		Missing:   []string{"tool:web.search", "tool:agent-browser"},
	}
	msg := inbound("cli:test", "查纳斯达克近三天走势")
	blocker := contractBlockerText(contract, validation, rt, msg)
	if !strings.Contains(blocker, "web.search") {
		t.Fatalf("blocker should mention web.search, got: %s", blocker)
	}
	if !strings.Contains(blocker, "agent-browser") {
		t.Fatalf("blocker should mention agent-browser, got: %s", blocker)
	}
	if !strings.Contains(blocker, "Task contract could not be satisfied") {
		t.Fatalf("blocker should use the structured English template, got: %s", blocker)
	}
	if strings.Contains(blocker, "查看") || strings.Contains(blocker, "近三天") {
		t.Fatalf("blocker should not echo the user-language request, got: %s", blocker)
	}
}

func TestTraceRecordsSelectedContractSkills(t *testing.T) {
	rt := newTestRuntime(t)
	ws := t.TempDir()
	cfg := rt.Config
	cfg.App.Workspace = ws
	skillDir := filepath.Join(ws, "skills", "test-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: test-skill\ndescription: Test skill for trace verification.\npriority: 50\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "ok"})
	rt.Tools = registry
	rt.ContractModel = contractJSONModel{json: `{"summary":"test","requires_tools":false,"expected_outcome":"done"}`}

	state := session.State{Key: "cli:test"}
	task := state.StartTask("test task with skill")
	if err := rt.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	trace := mustTrace(t)
	_ = rt.ensureTaskContract(context.Background(), inbound("cli:test", "test task with skill"), &state, task, "test task with skill", nil, trace)

	data, err := os.ReadFile(trace.path)
	if err != nil {
		t.Fatal(err)
	}
	traceStr := string(data)
	if !strings.Contains(traceStr, "task_contract_skills_selected") {
		t.Fatalf("expected task_contract_skills_selected trace, got:\n%s", traceStr)
	}
	if !strings.Contains(traceStr, "test-skill") {
		t.Fatalf("expected test-skill in trace, got:\n%s", traceStr)
	}
}

func TestPlanReviewDisplaysRequiredSkills(t *testing.T) {
	// The runtime no longer maintains a Chinese plan review branch; the
	// structured English template is the only path.
	contract := session.TaskContract{
		Summary: "check market and write report",
		RequiredSkills: []session.RequiredSkill{
			{Name: "feishu-notify", Reason: "create Feishu cloud doc"},
			{Name: "fresh-search", Reason: "search market data"},
		},
	}
	text := renderTaskPlanForReview(contract, "")
	if !strings.Contains(text, "Required skills:") {
		t.Fatalf("expected Required skills in plan review, got:\n%s", text)
	}
	if !strings.Contains(text, "feishu-notify") || !strings.Contains(text, "create Feishu cloud doc") {
		t.Fatalf("expected feishu-notify with reason, got:\n%s", text)
	}
	if !strings.Contains(text, "fresh-search") || !strings.Contains(text, "search market data") {
		t.Fatalf("expected fresh-search with reason, got:\n%s", text)
	}
}

func TestPlanReviewDisplaysRequiredSkillsEnglish(t *testing.T) {
	contract := session.TaskContract{
		Summary: "check Nasdaq and create doc",
		RequiredSkills: []session.RequiredSkill{
			{Name: "feishu-notify", Reason: "create Feishu cloud doc"},
		},
	}
	text := renderTaskPlanForReviewEN(contract, false)
	if !strings.Contains(text, "Required skills:") {
		t.Fatalf("expected Required skills in plan review, got:\n%s", text)
	}
	if !strings.Contains(text, "feishu-notify") || !strings.Contains(text, "create Feishu cloud doc") {
		t.Fatalf("expected feishu-notify with reason, got:\n%s", text)
	}
}

func TestTaskContractCreatedTraceIncludesRequiredSkills(t *testing.T) {
	rt := newTestRuntime(t)
	rt.ContractModel = contractJSONModel{json: `{"summary":"test","requires_tools":true,"required_tools":["file.read"],"required_skills":[{"name":"feishu-notify","path":"/tmp/skills/feishu-notify/SKILL.md","reason":"create Feishu cloud doc"}],"expected_outcome":"done"}`}
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "ok"})
	rt.Tools = registry

	state := session.State{Key: "cli:test"}
	task := state.StartTask("test task")
	if err := rt.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	trace := mustTrace(t)
	rt.ensureTaskContract(context.Background(), inbound("cli:test", "test task"), &state, task, "test task", nil, trace)

	data, err := os.ReadFile(trace.path)
	if err != nil {
		t.Fatal(err)
	}
	traceStr := string(data)
	if !strings.Contains(traceStr, "task_contract_created") {
		t.Fatal("expected task_contract_created trace")
	}
	if !strings.Contains(traceStr, "required_skills") || !strings.Contains(traceStr, "feishu-notify") {
		t.Fatalf("expected required_skills in trace, got:\n%s", traceStr)
	}
}

func TestSkillSelectionTraceIncludesOmittedCount(t *testing.T) {
	rt := newTestRuntime(t)
	ws := t.TempDir()
	cfg := rt.Config
	cfg.App.Workspace = ws
	for i := 0; i < 30; i++ {
		dir := filepath.Join(ws, "skills", fmt.Sprintf("skill-%d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		content := fmt.Sprintf("---\nname: skill-%d\ndescription: Test skill %d.\npriority: %d\n---\n", i, i, i)
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "ok"})
	rt.Tools = registry
	rt.ContractModel = contractJSONModel{json: `{"summary":"test","requires_tools":false,"expected_outcome":"done"}`}

	state := session.State{Key: "cli:test"}
	task := state.StartTask("test task")
	if err := rt.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	trace := mustTrace(t)
	rt.ensureTaskContract(context.Background(), inbound("cli:test", "test task"), &state, task, "test task", nil, trace)

	data, err := os.ReadFile(trace.path)
	if err != nil {
		t.Fatal(err)
	}
	traceStr := string(data)
	if !strings.Contains(traceStr, "task_contract_skills_selected") {
		t.Fatal("expected task_contract_skills_selected trace")
	}
	if !strings.Contains(traceStr, "discovered_count") || !strings.Contains(traceStr, "omitted_count") || !strings.Contains(traceStr, "selected_count") {
		t.Fatalf("expected count fields in trace, got:\n%s", traceStr)
	}
	if !strings.Contains(traceStr, `"omitted_count":6`) {
		t.Fatalf("expected 6 omitted skills (30 total - 24 limit), got:\n%s", traceStr)
	}
}

func TestTaskPlanReviewTraceIncludesRequiredFields(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "ok"})
	registry.Register(runtimeNamedTool{name: "terminal.run", content: "ok"})
	rt.Tools = registry
	rt.ContractModel = contractJSONModel{json: `{"summary":"test","requires_tools":true,"required_tools":["file.read"],"required_skills":[{"name":"feishu-notify","path":"/tmp/skills/feishu-notify/SKILL.md","reason":"create Feishu doc"}],"required_evidence":[{"kind":"local_file","tool":"file.read","description":"read /tmp/skills/feishu-notify/SKILL.md"}],"plan_items":[{"id":"plan-1","title":"read feishu-notify SKILL.md","status":"pending","tool":"file.read","criteria":"read /tmp/skills/feishu-notify/SKILL.md"}],"expected_outcome":"done"}`}
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, Content: "done"},
	}}, rt.Tools)

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "test plan review trace"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Style != channel.StyleInputRequired {
		t.Fatalf("expected plan review, got %#v", resp.Reply)
	}

	trace, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	traceStr := string(trace)
	if !strings.Contains(traceStr, "task_plan_review") {
		t.Fatal("expected task_plan_review trace")
	}
	if !strings.Contains(traceStr, "required_tools") {
		t.Fatal("expected required_tools in task_plan_review trace")
	}
	if !strings.Contains(traceStr, "required_skills") {
		t.Fatal("expected required_skills in task_plan_review trace")
	}
	if !strings.Contains(traceStr, "plan_item_count") {
		t.Fatal("expected plan_item_count in task_plan_review trace")
	}
}

func TestFeishuCloudDocContractRequiresFeishuNotifyAndTerminalRun(t *testing.T) {
	rt := newTestRuntime(t)
	ws := t.TempDir()
	cfg := rt.Config
	cfg.App.Workspace = ws
	skillDir := filepath.Join(ws, "skills", "feishu-notify")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: feishu-notify\ndescription: Send Feishu messages and create cloud documents.\npriority: 80\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "ok"})
	registry.Register(runtimeNamedTool{name: "file.write", content: "ok"})
	registry.Register(runtimeNamedTool{name: "terminal.run", content: "ok"})
	registry.Register(runtimeNamedTool{name: "web.search", content: "ok"})
	rt.Tools = registry
	rt.ContractModel = contractJSONModel{json: `{"summary":"create Feishu cloud doc for Nasdaq report","requires_tools":true,"required_tools":["file.read","file.write","terminal.run","web.search"],"required_skills":[{"name":"feishu-notify","path":"/workspace/skills/feishu-notify/SKILL.md","reason":"create Feishu cloud document"}],"required_evidence":[{"kind":"local_file","tool":"file.read","description":"read /workspace/skills/feishu-notify/SKILL.md"},{"kind":"local_file","tool":"file.write","description":"write markdown document"}],"expected_outcome":"report on Nasdaq trend published to Feishu cloud doc"}`}

	contract := rt.ensureTaskContract(context.Background(), inbound("cli:test", "查看纳指走势，发到飞书云文档"), &session.State{Key: "cli:test"}, &session.TaskNode{ID: "task-1", Goal: "查看纳指走势，发到飞书云文档", Status: "running"}, "查看纳指走势，发到飞书云文档", nil, mustTrace(t))

	if len(contract.RequiredSkills) == 0 {
		t.Fatal("expected feishu-notify in required_skills")
	}
	found := false
	for _, s := range contract.RequiredSkills {
		if s.Name == "feishu-notify" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected feishu-notify in required_skills, got %v", contract.RequiredSkills)
	}
	hasTerminal := false
	for _, t := range contract.RequiredTools {
		if t == "terminal.run" {
			hasTerminal = true
			break
		}
	}
	if !hasTerminal {
		t.Fatalf("expected terminal.run in required_tools, got %v", contract.RequiredTools)
	}
}

func TestFeishuBotReadyDoesNotRequireUserLogin(t *testing.T) {
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "ok"})
	registry.Register(runtimeNamedTool{name: "terminal.run", content: "ok"})

	contract := session.TaskContract{
		Summary:       "create Feishu cloud document",
		RequiresTools: true,
		RequiredTools: []string{"file.read", "terminal.run"},
		RequiredSkills: []session.RequiredSkill{
			{Name: "feishu-notify", Path: "/tmp/skills/feishu-notify/SKILL.md", Reason: "create Feishu cloud doc"},
		},
		RequiredEvidence: []session.TaskEvidenceContract{
			{Kind: "local_file", Tool: "file.read", Description: "read /tmp/skills/feishu-notify/SKILL.md"},
		},
		PlanItems: []session.TaskPlanItem{
			{ID: "plan-1", Title: "read feishu-notify SKILL.md", Status: "pending", Tool: "file.read", Criteria: "read /tmp/skills/feishu-notify/SKILL.md to get helper command"},
			{ID: "plan-2", Title: "create document via helper", Status: "pending", Tool: "terminal.run", Criteria: "run helper script from skill"},
		},
	}
	validation := validateContractTools(contract, registry, []discoveredSkill{
		{Name: "feishu-notify", Path: "/tmp/skills/feishu-notify/SKILL.md"},
	})
	if !validation.IsValid() {
		t.Fatalf("contract should be valid with feishu-notify + file.read + terminal.run: invalid tools=%v invalid skills=%v", validation.InvalidTools, validation.InvalidSkills)
	}
	for _, tool := range contract.RequiredTools {
		if tool == "lark-cli" || tool == "feishu" {
			t.Fatal("contract should not require a Feishu-specific runtime tool")
		}
	}
}

func TestContractReplanFeedbackRequiresSkillReadPlanItem(t *testing.T) {
	contract := session.TaskContract{
		Summary:       "create Feishu cloud document",
		RequiresTools: true,
		RequiredTools: []string{"file.read", "terminal.run"},
		RequiredSkills: []session.RequiredSkill{
			{Name: "feishu-notify", Path: "/tmp/skills/feishu-notify/SKILL.md", Reason: "create Feishu cloud doc"},
		},
		RequiredEvidence: []session.TaskEvidenceContract{
			{Kind: "local_file", Tool: "file.read", Description: "read /tmp/skills/feishu-notify/SKILL.md"},
		},
		PlanItems: []session.TaskPlanItem{
			{ID: "plan-1", Title: "create document via helper", Status: "pending", Tool: "terminal.run", Criteria: "run helper script from skill"},
		},
	}
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "ok"})
	registry.Register(runtimeNamedTool{name: "terminal.run", content: "ok"})

	validation := validateContractTools(contract, registry, []discoveredSkill{
		{Name: "feishu-notify", Path: "/tmp/skills/feishu-notify/SKILL.md"},
	})
	if validation.IsValid() {
		t.Fatal("expected contract to be invalid without a file.read plan item for the required skill")
	}
	feedback := contractReplanFeedback(validation, contract)
	for _, want := range []string{"plan_items", "tool=file.read", "SKILL.md", "file.read plan item"} {
		if !strings.Contains(feedback, want) {
			t.Fatalf("expected replan feedback to mention %q, got: %s", want, feedback)
		}
	}
}

func TestFeishuNotifyWrapperCommandSelected(t *testing.T) {
	skill := discoveredSkill{
		Name:        "feishu-notify",
		Description: "Send Feishu messages and create cloud documents via lark-cli.",
		Stage:       "cli",
		Priority:    "80",
		Path:        "/tmp/skills/feishu-notify/SKILL.md",
		Scope:       "shared",
	}
	hint := executionHint(skill)
	if !strings.Contains(hint, "terminal.run") {
		t.Fatalf("CLI skill execution hint should mention terminal.run, got: %s", hint)
	}

	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "ok"})
	registry.Register(runtimeNamedTool{name: "terminal.run", content: "ok"})

	prompt := renderTaskContractPrompt("创建飞书云文档", "", registry, []discoveredSkill{skill})
	if !strings.Contains(prompt, "execution_hint") {
		t.Fatal("contract prompt should include execution_hint")
	}
	if !strings.Contains(prompt, "terminal.run") {
		t.Fatal("contract prompt should mention terminal.run for CLI skill")
	}
}

func TestContractFollowupSuggestsAlternativeEvidenceForBotProtection(t *testing.T) {
	missing := []string{"tool:web.fetch"}
	failures := map[string]FailureInfo{
		"web.fetch": {
			Category: FailureBlocked,
			Reason:   "bot protection or JS challenge page",
			Guidance: "use web.search result summaries, official data API, or terminal.run API call; do not treat challenge page body as useful content",
		},
	}
	guidance := taskContractFollowupWithGuidance(missing, failures, session.TaskContract{}, nil)
	if !strings.Contains(guidance, "search") && !strings.Contains(guidance, "API") && !strings.Contains(guidance, "alternative") {
		t.Fatalf("follow-up should suggest alternative evidence for bot protection, got: %s", guidance)
	}
}

func TestSearchEvidenceCanSatisfyMarketDataWhenContractAllowsSearch(t *testing.T) {
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "web.search", content: "ok"})
	registry.Register(runtimeNamedTool{name: "file.read", content: "ok"})

	contract := session.TaskContract{
		Summary:       "查纳斯达克走势",
		RequiresTools: true,
		RequiredTools: []string{"web.search"},
		RequiredEvidence: []session.TaskEvidenceContract{
			{Kind: "current_external_fact", Tool: "web.search", Description: "Nasdaq index values with date and source"},
		},
	}
	validation := validateContractTools(contract, registry, nil)
	if !validation.IsValid() {
		t.Fatalf("contract with search-based market data should be valid: %v", validation.InvalidTools)
	}
	for _, tool := range contract.RequiredTools {
		if tool == "web.fetch" {
			t.Fatal("contract should not hard-require web.fetch when web.search with date/value/source is sufficient")
		}
	}
}

func TestFeishuNotifyIsDiscoveredWhenInstalled(t *testing.T) {
	ws := t.TempDir()
	cfg := config.DefaultRoot()
	cfg.App.Workspace = ws
	skillDir := filepath.Join(ws, "skills", "feishu-notify")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: feishu-notify\ndescription: Send Feishu messages and create cloud documents.\npriority: 80\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	skills := discoverSkillsForAgent(&cfg, "main", 24)
	found := false
	for _, s := range skills {
		if s.Name == "feishu-notify" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected feishu-notify in discovered skills, got %v", skillNames(skills))
	}
}

func TestSkillDiscoveryUsesHeaderEvenWhenBodyMentionsToken(t *testing.T) {
	ws := t.TempDir()
	cfg := config.DefaultRoot()
	cfg.App.Workspace = ws
	skillDir := filepath.Join(ws, "skills", "feishu-notify")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: feishu-notify\ndescription: Create Feishu/Lark cloud documents.\npriority: 80\n---\n\nUse --parent-token when needed.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	skills := discoverSkillsForAgent(&cfg, "main", 24)
	if len(skills) != 1 || skills[0].Name != "feishu-notify" {
		t.Fatalf("expected skill discovery to parse safe header despite token in body, got %#v", skills)
	}
}

func skillNames(skills []discoveredSkill) []string {
	var names []string
	for _, s := range skills {
		names = append(names, s.Name)
	}
	return names
}

func TestSkillReadThenWrongCommandFollowupGuidesNextTool(t *testing.T) {
	contract := session.TaskContract{
		Summary:       "create Feishu cloud document",
		RequiresTools: true,
		RequiredTools: []string{"file.read", "terminal.run"},
		RequiredSkills: []session.RequiredSkill{
			{Name: "feishu-notify", Path: "/tmp/skills/feishu-notify/SKILL.md", Reason: "create Feishu cloud doc"},
		},
		PlanItems: []session.TaskPlanItem{
			{ID: "plan-1", Title: "read feishu-notify SKILL.md", Status: "completed", Tool: "file.read", Criteria: "read /tmp/skills/feishu-notify/SKILL.md"},
			{ID: "plan-2", Title: "create document via helper", Status: "pending", Tool: "terminal.run", Criteria: "run helper script from skill"},
		},
	}
	missing := []string{"plan:create document via helper"}
	failures := map[string]FailureInfo{}
	guidance := taskContractFollowupWithGuidance(missing, failures, contract, nil)
	if !strings.Contains(guidance, "skill:feishu-notify read") {
		t.Fatalf("follow-up should note that skill was read compactly, got: %s", guidance)
	}
	if !strings.Contains(guidance, "terminal.run") {
		t.Fatalf("follow-up should guide toward terminal.run, got: %s", guidance)
	}
}

func TestSkillReadFollowupMatchesRequiredSkillExactly(t *testing.T) {
	contract := session.TaskContract{
		Summary:       "create Feishu cloud document",
		RequiresTools: true,
		RequiredTools: []string{"file.read", "terminal.run"},
		RequiredSkills: []session.RequiredSkill{
			{Name: "feishu-notify", Path: "/tmp/skills/feishu-notify/SKILL.md", Reason: "create Feishu cloud doc"},
		},
		PlanItems: []session.TaskPlanItem{
			{ID: "plan-1", Title: "read other-skill SKILL.md", Status: "completed", Tool: "file.read", Criteria: "read /tmp/skills/other-skill/SKILL.md"},
			{ID: "plan-2", Title: "create document via helper", Status: "pending", Tool: "terminal.run", Criteria: "run helper script from skill"},
		},
	}
	guidance := taskContractFollowupWithGuidance([]string{"plan:create document via helper"}, nil, contract, nil)
	if strings.Contains(guidance, "skill:feishu-notify read") {
		t.Fatalf("follow-up should not treat another skill as feishu-notify read, got: %s", guidance)
	}
	if !strings.Contains(guidance, "skill:feishu-notify needs file.read SKILL.md") {
		t.Fatalf("follow-up should ask to read the required skill first, got: %s", guidance)
	}
}

func TestFeishuDocTaskReadsSkillWritesMarkdownAndUsesBotHelper(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	terminal := &captureCommandTool{name: "terminal.run", content: `{"url":"https://example.feishu.cn/docx/mock"}`}
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "web.search", content: "Nasdaq Composite 2026-06-09 17800; 2026-06-10 17920; 2026-06-11 18010"})
	registry.Register(runtimeNamedTool{name: "file.read", content: "Use scripts/feishu.docs.create with --markdown-file <absolute path> --as bot. Bot auth may be ready even when user auth is missing."})
	registry.Register(runtimeNamedTool{name: "file.write", content: "wrote /tmp/nasdaq.md"})
	registry.Register(terminal)
	rt.Tools = registry
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_search",
			Name: "web.search",
			Args: map[string]any{"query": "Nasdaq Composite last three trading days"},
		}}},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_read_skill",
			Name: "file.read",
			Args: map[string]any{"path": "/tmp/skills/feishu-notify/SKILL.md"},
		}}},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_write",
			Name: "file.write",
			Args: map[string]any{"path": "/tmp/nasdaq.md", "content": "# Nasdaq\n\nLast three days."},
		}}},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_create",
			Name: "terminal.run",
			Args: map[string]any{"command": "python3 /tmp/skills/feishu-notify/scripts/feishu.docs.create --title \"Nasdaq last three days\" --markdown-file /tmp/nasdaq.md --as bot"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "已创建飞书云文档：https://example.feishu.cn/docx/mock"},
	}}, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"查看纳斯达克近三天走势并发到飞书云文档","requires_tools":true,"required_tools":["web.search","file.read","file.write","terminal.run"],"required_skills":[{"name":"feishu-notify","path":"/tmp/skills/feishu-notify/SKILL.md","reason":"创建飞书云文档"}],"required_evidence":[{"kind":"current_external_fact","tool":"web.search","description":"Nasdaq last three trading days with source/date"},{"kind":"local_file","tool":"file.read","description":"read /tmp/skills/feishu-notify/SKILL.md"},{"kind":"local_file","tool":"file.write","description":"write markdown report before uploading"},{"kind":"remote_publish","tool":"terminal.run","description":"create Feishu cloud doc via feishu-notify helper using --markdown-file and --as bot"}],"plan_items":[{"id":"plan-1","title":"search Nasdaq data","status":"pending","tool":"web.search","criteria":"collect Nasdaq last three trading days"},{"id":"plan-2","title":"read feishu-notify SKILL.md","status":"pending","tool":"file.read","criteria":"read /tmp/skills/feishu-notify/SKILL.md"},{"id":"plan-3","title":"write local markdown report","status":"pending","tool":"file.write","criteria":"write markdown report to an absolute local path"},{"id":"plan-4","title":"create Feishu cloud document","status":"pending","tool":"terminal.run","criteria":"run feishu.docs.create --markdown-file <path> --as bot"}],"expected_outcome":"飞书云文档创建完成并返回链接","completion_policy":"final answer must include the created Feishu doc URL or a concrete blocker"}`}

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "查看纳斯达克指数情况，近三天的走势，将其整理成文档，发到我的飞书云文档"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Style != channel.StyleInputRequired {
		t.Fatalf("expected plan review, got %#v", resp.Reply)
	}
	resp, err = rt.Handle(context.Background(), inbound("cli:test", "1"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed {
		t.Fatalf("expected Feishu doc task to complete, got %#v", resp.Reply)
	}
	cmd := terminal.LastCommand()
	for _, want := range []string{"feishu.docs.create", "--markdown-file", "/tmp/nasdaq.md", "--as bot"} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("expected terminal command to contain %q, got %q", want, cmd)
		}
	}
	state := loadState(t, rt, "cli:test")
	contract := state.Tasks[0].Execution.Contract
	if contract == nil {
		t.Fatal("expected stored contract")
	}
	for _, item := range contract.PlanItems {
		if item.Status != "completed" {
			t.Fatalf("expected all plan items completed, got %#v", contract.PlanItems)
		}
	}
}

func TestSearchEvidenceSatisfiesMarketDataContractEndToEnd(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "web.search", content: "2026-06-11 Nasdaq Composite: 17,862.31 (+0.52%), S&P 500: 5,358.12 (+0.31%)"})
	registry.Register(runtimeNamedTool{name: "file.read", content: "ok"})
	registry.Register(runtimeNamedTool{name: "file.write", content: "ok"})
	rt.Tools = registry
	rt.ContractModel = contractJSONModel{json: `{"summary":"查纳斯达克走势并写入文档","requires_tools":true,"required_tools":["web.search","file.write"],"required_evidence":[{"kind":"current_external_fact","tool":"web.search","description":"Nasdaq index values with date and source"}],"plan_items":[{"id":"plan-1","title":"search Nasdaq data","status":"pending","tool":"web.search","criteria":"get current Nasdaq index values"},{"id":"plan-2","title":"write markdown","status":"pending","tool":"file.write","criteria":"write report to file"}],"expected_outcome":"Nasdaq report written"}`}

	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID: "call_search", Name: "web.search", Args: map[string]any{"query": "Nasdaq index today"},
		}}},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID: "call_write", Name: "file.write", Args: map[string]any{"path": "/tmp/report.md", "content": "# Nasdaq Report\n2026-06-11: 17,862.31"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "Nasdaq report written to /tmp/report.md."},
	}}, rt.Tools)

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "查看纳指走势，写入文档"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Style == channel.StyleInputRequired {
		resp2, err := rt.Handle(context.Background(), inbound("cli:test", "1"))
		if err != nil {
			t.Fatal(err)
		}
		if resp2.Failed {
			t.Fatalf("task should not fail with search evidence: %v", resp2.Reply.Text)
		}
		return
	}
	if resp.Failed {
		t.Fatalf("task should not fail with search evidence: %v", resp.Reply.Text)
	}
}

func TestCompletionEvaluatorContractSatisfiedNoExtraFollowup(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "web.search", content: "Weather clear."})
	rt.Tools = registry
	rt.ContractModel = contractJSONModel{json: `{"summary":"search","requires_tools":true,"required_tools":["web.search"],"expected_outcome":"answer"}`}
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "web.search",
			Args: map[string]any{"query": "weather"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "Weather is clear."},
	}}, rt.Tools)

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "check weather"))
	if err != nil {
		t.Fatal(err)
	}
	// auto_contract skips plan review; model runs directly.
	if resp.Reply.Style == channel.StyleInputRequired {
		resp, err = rt.Handle(context.Background(), inbound("cli:test", "1"))
		if err != nil {
			t.Fatal(err)
		}
	}
	if resp.Failed {
		t.Fatalf("expected completion, got failed: %q", resp.Reply.Text)
	}
	trace, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	traceStr := string(trace)
	if !strings.Contains(traceStr, "task_contract_satisfied") {
		t.Fatalf("expected task_contract_satisfied trace, got:\n%s", traceStr)
	}
	if strings.Contains(traceStr, "contract_followup_sent") {
		t.Fatalf("expected no contract_followup_sent after contract satisfied, got:\n%s", traceStr)
	}
	if strings.Contains(traceStr, "task_contract_unsatisfied") {
		t.Fatalf("expected no task_contract_unsatisfied after contract satisfied, got:\n%s", traceStr)
	}
}

func TestCompletionEvaluatorUnavailableToolFastBlockerNoFollowup(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Root{
		App: config.AppConfig{Home: home},
		Execution: config.ExecutionConfig{
			MaxIterations:        intPtrTest(8),
			InactivityTimeout:    "0s",
			MaxContractFollowups: 2,
		},
		Agents: config.AgentsConfig{
			Default: "main",
			Profiles: []config.AgentProfileConfig{{
				ID: "main",
				Tools: config.AccessListConfig{
					Deny: []string{"terminal.run"},
				},
			}},
		},
	}
	rt := New(cfg)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "ok"})
	registry.Register(runtimeNamedTool{name: "terminal.run", content: "ok"})
	rt.Tools = registry
	rt.Pool = NewAgentPool(cfg)
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, Content: "done"},
	}}, rt.Pool.agents["main"].Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"check service","requires_tools":true,"required_tools":["terminal.run"],"expected_outcome":"status confirmed"}`}

	planResp, err := rt.Handle(context.Background(), inbound("cli:test", "check singbox service status"))
	if err != nil {
		t.Fatal(err)
	}
	if planResp.Reply.Style == channel.StyleInputRequired {
		resp, err := rt.Handle(context.Background(), inbound("cli:test", "1"))
		if err != nil {
			t.Fatal(err)
		}
		if !resp.Failed {
			t.Fatalf("expected failed response for unavailable tool, got %#v", resp)
		}
		resp2 := resp
		trace, err := os.ReadFile(resp2.TracePath)
		if err != nil {
			t.Fatal(err)
		}
		traceStr := string(trace)
		if !strings.Contains(traceStr, "contract_tool_unavailable") {
			t.Fatalf("expected contract_tool_unavailable trace, got:\n%s", traceStr)
		}
		if strings.Contains(traceStr, "contract_followup_sent") {
			t.Fatalf("unavailable tool should not send contract follow-ups, got:\n%s", traceStr)
		}
		if !strings.Contains(resp2.Reply.Text, "terminal.run") {
			t.Fatalf("expected blocker to mention terminal.run, got: %q", resp2.Reply.Text)
		}
		if !strings.Contains(resp2.Reply.Text, "denied by profile") {
			t.Fatalf("expected blocker to explain the unavailability reason, got: %q", resp2.Reply.Text)
		}
		return
	}
	// auto_contract/direct: model runs directly, may fail
	resp := planResp
	trace, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	traceStr := string(trace)
	if !strings.Contains(traceStr, "contract_tool_unavailable") && resp.Failed {
		// ok: blocker from unavailable tool
	}
	if resp.Failed && !strings.Contains(resp.Reply.Text, "terminal.run") {
		t.Fatalf("expected blocker to mention terminal.run, got: %q", resp.Reply.Text)
	}
}

func TestCompletionEvaluatorMissingEvidenceRecoverableFollowup(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(&runtimeFailingTool{
		name:      "file.edit",
		errorText: "old_string not found in file",
		isError:   true,
		evidence:  map[string]any{"path": "/tmp/f.txt", "matches": 0},
		failCount: 1,
	})
	rt.Tools = registry
	rt.ContractModel = contractJSONModel{json: `{"summary":"edit file","requires_tools":true,"required_tools":["file.edit"],"expected_outcome":"file edited"}`}
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "file.edit",
			Args: map[string]any{"path": "/tmp/f.txt", "old_string": "old", "new_string": "new"},
		}}},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_2",
			Name: "file.edit",
			Args: map[string]any{"path": "/tmp/f.txt", "old_string": "found", "new_string": "new"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "done"},
	}}, rt.Tools)

	planResp, err := rt.Handle(context.Background(), inbound("cli:test", "edit file"))
	if err != nil {
		t.Fatal(err)
	}
	if planResp.Reply.Style != channel.StyleInputRequired {
		t.Fatalf("expected plan review, got %#v", planResp.Reply)
	}
	resp, err := rt.Handle(context.Background(), inbound("cli:test", "1"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed {
		t.Fatalf("expected recoverable missing evidence to converge after followup, got: %q", resp.Reply.Text)
	}
	trace, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	traceStr := string(trace)
	if !strings.Contains(traceStr, "contract_followup_sent") {
		t.Fatalf("expected contract_followup_sent for recoverable missing evidence, got:\n%s", traceStr)
	}
	if !strings.Contains(traceStr, "task_contract_satisfied") {
		t.Fatalf("expected task_contract_satisfied after recoverable followup, got:\n%s", traceStr)
	}
}

func TestCompletionEvaluatorFinalReplyHasDeliverableNoDefaultAudit(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "web.search", content: "ok"})
	registry.Register(runtimeNamedTool{name: "file.write", content: "wrote /tmp/report.md"})
	rt.Tools = registry
	rt.ContractModel = contractJSONModel{json: `{"summary":"write report","requires_tools":true,"required_tools":["web.search","file.write"],"required_evidence":[{"kind":"current_external_fact","tool":"web.search","description":"current data"},{"kind":"local_file","tool":"file.write","description":"markdown report"}],"plan_items":[{"id":"plan-1","title":"search","status":"pending","tool":"web.search","criteria":"collect data"},{"id":"plan-2","title":"write","status":"pending","tool":"file.write","criteria":"write report"}],"expected_outcome":"report at /tmp/report.md","completion_policy":"final answer must include the deliverable path"}`}
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_search",
			Name: "web.search",
			Args: map[string]any{"query": "data"},
		}}},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_write",
			Name: "file.write",
			Args: map[string]any{"path": "/tmp/report.md", "content": "data"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "Report written to https://example.com/report.md and /tmp/report.md."},
	}}, rt.Tools)

	planResp, err := rt.Handle(context.Background(), inbound("cli:test", "write a report"))
	if err != nil {
		t.Fatal(err)
	}
	if planResp.Reply.Style != channel.StyleInputRequired {
		t.Fatalf("expected plan review, got %#v", planResp.Reply)
	}
	resp, err := rt.Handle(context.Background(), inbound("cli:test", "1"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed {
		t.Fatalf("expected completion, got failed: %q", resp.Reply.Text)
	}
	if !strings.Contains(resp.Reply.Text, "/tmp/report.md") && !strings.Contains(resp.Reply.Text, "https://example.com/report.md") {
		t.Fatalf("expected deliverable URL/path in final reply, got: %q", resp.Reply.Text)
	}
	for _, banned := range []string{"plan-1 evidence", "required_skills evidence", "plan-2 evidence", "required_tools evidence"} {
		if strings.Contains(strings.ToLower(resp.Reply.Text), banned) {
			t.Fatalf("final reply should not auto-audit evidence, got %q in %q", banned, resp.Reply.Text)
		}
	}
}

func TestCompletionEvaluatorFollowupLimitBlockerIncludesToolAndReason(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	rt.Config.Execution.MaxContractFollowups = 2
	registry := agentcore.NewToolRegistry()
	registry.Register(&runtimeFailingTool{
		name:      "file.edit",
		errorText: "old_string not found in file",
		isError:   true,
		evidence:  map[string]any{"path": "/tmp/f.txt", "matches": 0},
	})
	rt.Tools = registry
	rt.ContractModel = contractJSONModel{json: `{"summary":"edit file","requires_tools":true,"required_tools":["file.edit"],"expected_outcome":"file edited"}`}
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "file.edit",
			Args: map[string]any{"path": "/tmp/f.txt", "old_string": "old", "new_string": "new"},
		}}},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_2",
			Name: "file.edit",
			Args: map[string]any{"path": "/tmp/f.txt", "old_string": "old", "new_string": "new"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "I give up."},
	}}, rt.Tools)

	planResp, err := rt.Handle(context.Background(), inbound("cli:test", "edit file"))
	if err != nil {
		t.Fatal(err)
	}
	if planResp.Reply.Style != channel.StyleInputRequired {
		t.Fatalf("expected plan review, got %#v", planResp.Reply)
	}
	resp, err := rt.Handle(context.Background(), inbound("cli:test", "1"))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Failed {
		t.Fatal("expected failed response at followup limit")
	}
	if !strings.Contains(resp.Reply.Text, "file.edit") {
		t.Fatalf("expected blocker to include tool name, got: %q", resp.Reply.Text)
	}
	if !strings.Contains(resp.Reply.Text, "blocked") {
		t.Fatalf("expected blocker to state the task is blocked, got: %q", resp.Reply.Text)
	}
	trace, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	traceStr := string(trace)
	if !strings.Contains(traceStr, "task_contract_followup_limit") {
		t.Fatalf("expected followup limit trace event, got:\n%s", traceStr)
	}
	if !strings.Contains(traceStr, `"attempts_total":2`) {
		t.Fatalf("expected attempts_total=2 in followup limit trace, got:\n%s", traceStr)
	}
}

func TestCompletionEvaluatorProgressDoesNotLeakFinalText(t *testing.T) {
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "web.search", content: "ok"})
	rt.Tools = registry
	rt.ContractModel = contractJSONModel{json: `{"summary":"search","requires_tools":true,"required_tools":["web.search"],"expected_outcome":"answer"}`}
	final := "secret deliverable summary that should never appear in progress"
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "web.search",
			Args: map[string]any{"query": "data"},
		}}},
		{Role: agentcore.RoleAssistant, Content: final},
	}}, rt.Tools)

	var updates []channel.OutboundMessage
	rt.ProgressSink = func(msg channel.OutboundMessage) {
		updates = append(updates, msg)
	}
	resp, err := rt.Handle(context.Background(), inbound("cli:test", "check"))
	if err != nil {
		t.Fatal(err)
	}
	// auto_contract skips plan review; model runs directly.
	if resp.Reply.Style == channel.StyleInputRequired {
		if _, err := rt.Handle(context.Background(), inbound("cli:test", "1")); err != nil {
			t.Fatal(err)
		}
	}
	for _, update := range updates {
		for _, step := range update.Progress {
			if strings.Contains(step.Summary, final) {
				t.Fatalf("final text leaked into progress step: %#v", step)
			}
		}
	}
}

func TestRuntimePostLoopUnavailableToolEvidenceIsToolNameMap(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	// The post-loop must re-derive the unavailable tool check from its own
	// inputs and emit an Evidence["unavailable"] that is a real
	// map[toolName]reason, not a list of missing entries.
	home := t.TempDir()
	cfg := &config.Root{
		App: config.AppConfig{Home: home},
		Execution: config.ExecutionConfig{
			MaxIterations:        intPtrTest(8),
			InactivityTimeout:    "0s",
			MaxContractFollowups: 2,
		},
		Agents: config.AgentsConfig{
			Default: "main",
			Profiles: []config.AgentProfileConfig{{
				ID: "main",
				Tools: config.AccessListConfig{
					Deny: []string{"terminal.run"},
				},
			}},
		},
	}
	rt := New(cfg)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "ok"})
	registry.Register(runtimeNamedTool{name: "terminal.run", content: "ok"})
	rt.Tools = registry
	rt.Pool = NewAgentPool(cfg)
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, Content: "done"},
	}}, rt.Pool.agents["main"].Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"check service","requires_tools":true,"required_tools":["terminal.run"],"expected_outcome":"status"}`}

	planResp, err := rt.Handle(context.Background(), inbound("cli:test", "check singbox service status"))
	if err != nil {
		t.Fatal(err)
	}
	if planResp.Reply.Style == channel.StyleInputRequired {
		resp, err := rt.Handle(context.Background(), inbound("cli:test", "1"))
		if err != nil {
			t.Fatal(err)
		}
		if !resp.Failed {
			t.Fatalf("expected failed response, got %#v", resp)
		}
		if !strings.Contains(resp.Reply.Text, "denied by profile") || !strings.Contains(resp.Reply.Text, "terminal.run") {
			t.Fatalf("expected blocker in reply to mention denial reason and tool, got: %q", resp.Reply.Text)
		}
		state := loadState(t, rt, "cli:test")
		if len(state.Tasks) == 0 {
			t.Fatal("expected a recorded task")
		}
		var blockerEvent *session.ExecutionEvent
		for i, ev := range state.Tasks[0].Execution.Events {
			if ev.Type == "task_contract_unsatisfied" {
				blockerEvent = &state.Tasks[0].Execution.Events[i]
			}
		}
		if blockerEvent == nil {
			t.Fatalf("expected a single task_contract_unsatisfied execution event, got %#v", state.Tasks[0].Execution.Events)
		}
		if blockerEvent.Evidence == nil {
			t.Fatal("expected execution event evidence to be populated")
		}
		if got, ok := blockerEvent.Evidence["blocker_kind"].(string); !ok || got != completionBlockerUnavailableTool {
			t.Fatalf("expected evidence.blocker_kind=%q, got %v", completionBlockerUnavailableTool, blockerEvent.Evidence["blocker_kind"])
		}
		unavailable, ok := blockerEvent.Evidence["unavailable"].(map[string]any)
		if !ok {
			t.Fatalf("expected evidence.unavailable to be a map[string]any, got %T", blockerEvent.Evidence["unavailable"])
		}
		if reason, ok := unavailable["terminal.run"].(string); !ok || reason != "denied by profile" {
			t.Fatalf("expected unavailable[terminal.run] = denied by profile, got %#v", unavailable)
		}
		return
	}
	// direct/auto_contract: model runs directly
	resp := planResp
	if !resp.Failed {
		t.Fatalf("expected failed response for unavailable tool, got %#v", resp)
	}
	if !strings.Contains(resp.Reply.Text, "terminal.run") {
		t.Fatalf("expected blocker to mention terminal.run, got: %q", resp.Reply.Text)
	}
}

func TestRuntimePostLoopDetectsFollowupLimitFromInputs(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	// Loop ends because MaxContractFollowups was reached. The post-loop should
	// classify the blocker as followup_limit (not contract_unsatisfied) and
	// the reply text should match the loop-time blocker.
	rt := newTestRuntime(t)
	rt.Config.Execution.MaxContractFollowups = 2
	registry := agentcore.NewToolRegistry()
	registry.Register(&runtimeFailingTool{
		name:      "file.edit",
		errorText: "old_string not found in file",
		isError:   true,
		evidence:  map[string]any{"path": "/tmp/f.txt", "matches": 0},
	})
	rt.Tools = registry
	rt.ContractModel = contractJSONModel{json: `{"summary":"edit file","requires_tools":true,"required_tools":["file.edit"],"expected_outcome":"file edited"}`}
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "file.edit",
			Args: map[string]any{"path": "/tmp/f.txt", "old_string": "old", "new_string": "new"},
		}}},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_2",
			Name: "file.edit",
			Args: map[string]any{"path": "/tmp/f.txt", "old_string": "old", "new_string": "new"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "I give up."},
	}}, rt.Tools)

	planResp, err := rt.Handle(context.Background(), inbound("cli:test", "edit file"))
	if err != nil {
		t.Fatal(err)
	}
	if planResp.Reply.Style != channel.StyleInputRequired {
		t.Fatalf("expected plan review, got %#v", planResp.Reply)
	}
	resp, err := rt.Handle(context.Background(), inbound("cli:test", "1"))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Failed {
		t.Fatal("expected failed response at followup limit")
	}
	state := loadState(t, rt, "cli:test")
	foundLimit := false
	for _, ev := range state.Tasks[0].Execution.Events {
		if ev.Type == "task_contract_unsatisfied" {
			kind, _ := ev.Evidence["blocker_kind"].(string)
			if kind == completionBlockerFollowupLimit {
				foundLimit = true
				if !strings.Contains(ev.Summary, "file.edit") {
					t.Fatalf("expected followup_limit event summary to name the tool, got %q", ev.Summary)
				}
				if attempts := evidenceIntValue(ev.Evidence["attempts_total"]); attempts != 2 {
					t.Fatalf("expected attempts_total=2 in evidence, got %v", ev.Evidence["attempts_total"])
				}
			}
		}
	}
	if !foundLimit {
		t.Fatalf("expected task_contract_unsatisfied event with blocker_kind=followup_limit, got %#v", state.Tasks[0].Execution.Events)
	}
	// The runtime derives followupCount from contract_followup execution
	// events rather than from any state the hook passed through.
	followupCount := 0
	for _, ev := range state.Tasks[0].Execution.Events {
		if ev.Type == "contract_followup" {
			followupCount++
		}
	}
	if followupCount != 2 {
		t.Fatalf("expected 2 contract_followup events, got %d", followupCount)
	}
	if !strings.Contains(resp.Reply.Text, "file.edit") {
		t.Fatalf("expected reply to mention the missing tool, got: %q", resp.Reply.Text)
	}
}

func TestRuntimePostLoopStopReasonIsNotOverriddenByContract(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	rt.Config.Execution.MaxIterations = intPtrTest(1)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "terminal.run", content: "ok"})
	rt.Tools = registry
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "terminal.run",
			Args: map[string]any{"command": "ls"},
		}}},
	}}, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"iterate","requires_tools":true,"required_tools":["terminal.run"],"expected_outcome":"done"}`}

	planResp, err := rt.Handle(context.Background(), inbound("cli:test", "check singbox service status"))
	if err != nil {
		t.Fatal(err)
	}
	if planResp.Reply.Style == channel.StyleInputRequired {
		resp, err := rt.Handle(context.Background(), inbound("cli:test", "1"))
		if err != nil {
			t.Fatal(err)
		}
		if !resp.Failed {
			t.Fatalf("expected failed response, got %#v", resp)
		}
		state := loadState(t, rt, "cli:test")
		if len(state.Tasks) == 0 {
			t.Fatal("expected task to be recorded")
		}
		if !strings.Contains(state.Tasks[0].Status, "failed") {
			t.Fatalf("expected failed task, got status=%q", state.Tasks[0].Status)
		}
		return
	}
	// direct/auto_contract: model runs directly
	resp := planResp
	if !resp.Failed {
		t.Fatalf("expected failed response, got %#v", resp)
	}
}

func TestCompletionEvaluatorSecretRedactionStaysIntact(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{
		name:    "web.search",
		content: "ok",
		evidence: map[string]any{
			"api_key": "sk-1234567890abcdef",
		},
	})
	rt.Tools = registry
	rt.ContractModel = contractJSONModel{json: `{"summary":"search","requires_tools":true,"required_tools":["web.search"],"expected_outcome":"answer"}`}
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "web.search",
			Args: map[string]any{"query": "data"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "search done"},
	}}, rt.Tools)
	planResp, err := rt.Handle(context.Background(), inbound("cli:test", "check"))
	if err != nil {
		t.Fatal(err)
	}
	// auto_contract skips plan review; model runs directly.
	resp := planResp
	if resp.Reply.Style == channel.StyleInputRequired {
		resp, err = rt.Handle(context.Background(), inbound("cli:test", "1"))
		if err != nil {
			t.Fatal(err)
		}
	}
	state := loadState(t, rt, "cli:test")
	task := state.Tasks[0]
	if len(task.Steps) == 0 {
		t.Fatal("expected at least one step")
	}
	step := task.Steps[0]
	if step.Evidence != nil {
		if v, ok := step.Evidence["api_key"]; ok && v == "sk-1234567890abcdef" {
			t.Fatalf("secret not redacted in step evidence: %v", step.Evidence)
		}
	}
	trace, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(trace), "sk-1234567890abcdef") {
		t.Fatal("secret not redacted in trace")
	}
	if strings.Contains(resp.Reply.Text, "sk-1234567890abcdef") {
		t.Fatal("secret not redacted in reply")
	}
}

func TestValidationExecutionSkillMissingFileReadFails(t *testing.T) {
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "ok"})
	registry.Register(runtimeNamedTool{name: "terminal.run", content: "ok"})

	skills := []discoveredSkill{
		{Name: "my-execution-skill", Path: "/tmp/skills/my-execution-skill/SKILL.md"},
	}

	tests := []struct {
		name     string
		contract session.TaskContract
	}{
		{
			name: "missing both evidence and plan item",
			contract: session.TaskContract{
				RequiresTools: true,
				RequiredTools: []string{"file.read"},
				RequiredSkills: []session.RequiredSkill{
					{Name: "my-execution-skill", Path: "/tmp/skills/my-execution-skill/SKILL.md", Reason: "needed"},
				},
			},
		},
		{
			name: "missing plan item only",
			contract: session.TaskContract{
				RequiresTools: true,
				RequiredTools: []string{"file.read"},
				RequiredSkills: []session.RequiredSkill{
					{Name: "my-execution-skill", Path: "/tmp/skills/my-execution-skill/SKILL.md", Reason: "needed"},
				},
				RequiredEvidence: []session.TaskEvidenceContract{
					{Kind: "local_file", Tool: "file.read", Description: "read /tmp/skills/my-execution-skill/SKILL.md"},
				},
			},
		},
		{
			name: "missing evidence only",
			contract: session.TaskContract{
				RequiresTools: true,
				RequiredTools: []string{"file.read"},
				RequiredSkills: []session.RequiredSkill{
					{Name: "my-execution-skill", Path: "/tmp/skills/my-execution-skill/SKILL.md", Reason: "needed"},
				},
				PlanItems: []session.TaskPlanItem{
					{ID: "plan-1", Title: "read my-execution-skill SKILL.md", Status: "pending", Tool: "file.read", Criteria: "read /tmp/skills/my-execution-skill/SKILL.md"},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			validation := validateContractTools(tc.contract, registry, skills)
			if validation.IsValid() {
				t.Fatal("expected validation to fail for skill without file.read evidence/plan item")
			}
			if len(validation.InvalidSkills) == 0 {
				t.Fatal("expected InvalidSkills to contain the skill name")
			}
		})
	}
}

func TestGuidanceSkillNotBlocking(t *testing.T) {
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "web.search", content: "ok"})
	registry.Register(runtimeNamedTool{name: "file.read", content: "ok"})

	skills := []discoveredSkill{
		{Name: "fresh-search", Description: "Search guidance skill.", Stage: "planning", Path: "/tmp/skills/fresh-search/SKILL.md"},
		{Name: "source-evaluation", Description: "Source evaluation guidance skill.", Stage: "synthesis", Path: "/tmp/skills/source-evaluation/SKILL.md"},
	}

	for _, s := range skills {
		if hint := executionHint(s); hint != "" {
			t.Fatalf("guidance skill %q should have empty execution_hint, got %q", s.Name, hint)
		}
	}

	contract := session.TaskContract{
		Summary:          "check weather",
		RequiresTools:    true,
		RequiredTools:    []string{"web.search"},
		ExpectedOutcome:  "weather report",
		CompletionPolicy: "use web evidence before final answer",
		RequiredEvidence: []session.TaskEvidenceContract{
			{Kind: "current_external_fact", Tool: "web.search", Description: "current weather for requested city"},
		},
		PlanItems: []session.TaskPlanItem{
			{ID: "plan-1", Title: "search weather", Status: "pending", Tool: "web.search", Criteria: "collect current weather data"},
		},
	}

	validation := validateContractTools(contract, registry, skills)
	if !validation.IsValid() {
		t.Fatalf("guidance skills not in required_skills should not block: invalid_tools=%v invalid_skills=%v", validation.InvalidTools, validation.InvalidSkills)
	}

	repaired := repairContractSkillUsage(contract, skills)
	if len(repaired.RequiredSkills) != 0 {
		t.Fatal("repair should not add guidance skills to required_skills")
	}
}

func TestUnregisteredToolPersistsAsBlockerAfterFailedReplan(t *testing.T) {
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "ok"})
	rt.Tools = registry

	callCount := 0
	rt.ContractModel = &dynamicContractModel{
		gen: func() string {
			callCount++
			return `{"summary":"test","requires_tools":true,"required_tools":["fictional-tool","file.read"],"plan_items":[{"id":"plan-1","title":"do stuff","status":"pending","tool":"fictional-tool"}],"expected_outcome":"done"}`
		},
	}

	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, Content: "done"},
	}}, rt.Tools)

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "run test"))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Failed {
		t.Fatal("expected task to fail/block with unregistered tool name that persists after replan")
	}
}

func TestSelectedRequiredSkillInPlanReviewTrace(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "ok"})
	registry.Register(runtimeNamedTool{name: "terminal.run", content: "ok"})
	rt.Tools = registry

	rt.ContractModel = contractJSONModel{json: `{"summary":"test","requires_tools":true,"required_tools":["file.read"],"required_skills":[{"name":"my-skill","path":"/tmp/skills/my-skill/SKILL.md","reason":"needed"}],"required_evidence":[{"kind":"local_file","tool":"file.read","description":"read /tmp/skills/my-skill/SKILL.md"}],"plan_items":[{"id":"plan-1","title":"read my-skill SKILL.md","status":"pending","tool":"file.read","criteria":"read /tmp/skills/my-skill/SKILL.md"}],"expected_outcome":"done"}`}
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, Content: "done"},
	}}, rt.Tools)

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "test plan review trace"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Style != channel.StyleInputRequired {
		t.Fatalf("expected plan review, got %#v", resp.Reply)
	}

	traceStr := readTrace(t, resp.TracePath)
	if !strings.Contains(traceStr, "task_plan_review") {
		t.Fatal("expected task_plan_review trace")
	}
	if !strings.Contains(traceStr, "required_skills") {
		t.Fatal("expected required_skills in task_plan_review trace")
	}
	if !strings.Contains(traceStr, "my-skill") {
		t.Fatal("expected selected required skill 'my-skill' in task_plan_review trace")
	}
}

func readTrace(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSkillReadPlanItemOrdering(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"file.read", "terminal.run"},
		RequiredSkills: []session.RequiredSkill{
			{Name: "my-cli-skill", Path: "/tmp/skills/my-cli-skill/SKILL.md", Reason: "needed for CLI workflow"},
		},
		PlanItems: []session.TaskPlanItem{
			{ID: "plan-1", Title: "execute via CLI", Status: "pending", Tool: "terminal.run", Criteria: "run the CLI command"},
		},
	}
	skills := []discoveredSkill{
		{Name: "my-cli-skill", Path: "/tmp/skills/my-cli-skill/SKILL.md", Stage: "cli"},
	}

	repaired := repairContractSkillUsage(contract, skills)

	var skillReadIdx, execIdx int = -1, -1
	for i, item := range repaired.PlanItems {
		if strings.EqualFold(strings.TrimSpace(item.Tool), "file.read") &&
			strings.Contains(strings.ToLower(item.Title), "my-cli-skill") {
			skillReadIdx = i
		}
		if strings.EqualFold(strings.TrimSpace(item.Tool), "terminal.run") {
			execIdx = i
		}
	}

	if skillReadIdx < 0 {
		t.Fatal("expected skill read plan item for my-cli-skill")
	}
	if execIdx < 0 {
		t.Fatal("expected execution plan item with terminal.run")
	}
	if skillReadIdx >= execIdx {
		t.Fatalf("skill read plan item (index %d) should precede execution item (index %d)", skillReadIdx, execIdx)
	}
}

func TestSkillReadBlocksExecutionToolBeforeRead(t *testing.T) {
	skipLegacyAgentLoopTest(t)
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "Use scripts/helper with --flag"})
	registry.Register(runtimeNamedTool{name: "terminal.run", content: "execution complete"})
	rt.Tools = registry

	skillPath := "/tmp/skills/my-cli-skill/SKILL.md"
	rt.ContractModel = contractJSONModel{json: `{"summary":"run skill-based CLI","requires_tools":true,"required_tools":["file.read","terminal.run"],"required_skills":[{"name":"my-cli-skill","path":"` + skillPath + `","reason":"needed for CLI workflow"}],"required_evidence":[{"kind":"local_file","tool":"file.read","description":"read ` + skillPath + `"},{"kind":"runtime_state","tool":"terminal.run","description":"execution evidence"}],"plan_items":[{"id":"plan-1","title":"read my-cli-skill SKILL.md","status":"pending","tool":"file.read","criteria":"read ` + skillPath + `"},{"id":"plan-2","title":"execute via CLI","status":"pending","tool":"terminal.run","criteria":"run the CLI command"}],"expected_outcome":"execution result","completion_policy":"use evidence"}`}

	// Model calls terminal.run FIRST — it should be blocked because skill hasn't been read yet.
	// Then file.read, then terminal.run again (succeeds). The first terminal.run must not be accepted.
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_term_first",
			Name: "terminal.run",
			Args: map[string]any{"command": "scripts/helper --flag"},
		}}},
		{Role: agentcore.RoleUser, Content: "required skill my-cli-skill must be read before execution tools. Call file.read for its SKILL.md first."},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_read_skill",
			Name: "file.read",
			Args: map[string]any{"path": skillPath},
		}}},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_term_second",
			Name: "terminal.run",
			Args: map[string]any{"command": "scripts/helper --flag"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "execution complete via CLI."},
	}}, rt.Tools)

	resp, err := rt.Handle(context.Background(), inbound("cli:skill-order", "run the CLI"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Style == channel.StyleInputRequired {
		resp, err = rt.Handle(context.Background(), inbound("cli:skill-order", "1"))
		if err != nil {
			t.Fatal(err)
		}
	}
	if resp.Failed {
		t.Fatalf("expected completion, got %#v", resp.Reply)
	}

	state := loadState(t, rt, "cli:skill-order")
	if len(state.Tasks) == 0 {
		t.Fatal("task not found")
	}
	task := state.Tasks[0]

	var stepsWithOrder []string
	for _, step := range task.Steps {
		stepsWithOrder = append(stepsWithOrder, step.Tool)
	}

	// The first terminal.run must NOT be accepted (it was blocked before skill read).
	// After retry and skill read, the second terminal.run should be accepted.
	firstTermIdx := -1
	secondTermIdx := -1
	readIdx := -1
	termCount := 0
	for i, s := range stepsWithOrder {
		if s == "file.read" && readIdx < 0 {
			readIdx = i
		}
		if s == "terminal.run" {
			if termCount == 0 {
				firstTermIdx = i
			} else if termCount == 1 {
				secondTermIdx = i
			}
			termCount++
		}
	}

	if readIdx < 0 {
		t.Fatal("expected file.read step in task steps")
	}
	if firstTermIdx < 0 || secondTermIdx < 0 {
		t.Fatalf("expected two terminal.run steps, got first=%d second=%d steps=%v", firstTermIdx, secondTermIdx, stepsWithOrder)
	}
	if readIdx <= firstTermIdx {
		t.Fatalf("file.read (index %d) should come after first terminal.run (index %d) — the first call is blocked, steps=%v", readIdx, firstTermIdx, stepsWithOrder)
	}
	if readIdx >= secondTermIdx {
		t.Fatalf("file.read (index %d) should precede second terminal.run (index %d), steps=%v", readIdx, secondTermIdx, stepsWithOrder)
	}

	// Verify first terminal.run was blocked and not accepted
	blockedEvents := 0
	for _, event := range task.Execution.Events {
		if event.Tool == "terminal.run" && event.Status == "failed" && event.Type == "tool_blocked" {
			blockedEvents++
		}
	}
	if blockedEvents == 0 {
		t.Fatal("expected at least one blocked terminal.run event before skill was read")
	}

	skillReadEvidenceFound := false
	for _, step := range task.Steps {
		if step.Tool != "file.read" {
			continue
		}
		if step.Evidence != nil {
			if acceptance, ok := step.Evidence["acceptance"]; ok && acceptance == "accepted" {
				skillReadEvidenceFound = true
			}
		}
	}
	if !skillReadEvidenceFound {
		t.Fatal("expected file.read task step with accepted evidence")
	}

	contract := task.Execution.Contract
	if contract == nil {
		t.Fatal("expected stored contract")
	}
	for _, item := range contract.PlanItems {
		if item.Tool == "terminal.run" && item.Status != "completed" {
			t.Fatalf("expected terminal.run plan item completed, got status=%q", item.Status)
		}
	}
}

func TestFileReadPlanItemMatchesSkillPath(t *testing.T) {
	skillPath := "/tmp/skills/my-skill/SKILL.md"
	otherPath := "/tmp/skills/other-skill/SKILL.md"

	contract := session.TaskContract{
		RequiresTools: true,
		RequiredSkills: []session.RequiredSkill{
			{Name: "my-skill", Path: skillPath, Reason: "needed"},
		},
		PlanItems: []session.TaskPlanItem{
			{ID: "plan-1", Title: "read my-skill SKILL.md", Status: "pending", Tool: "file.read", Criteria: "read " + skillPath},
			{ID: "plan-2", Title: "read other-skill SKILL.md", Status: "pending", Tool: "file.read", Criteria: "read " + otherPath},
			{ID: "plan-3", Title: "execute via CLI", Status: "pending", Tool: "terminal.run", Criteria: "run the CLI command"},
		},
	}

	item := findFileReadPlanItemForPath(&contract, skillPath)
	if item == nil {
		t.Fatal("expected to find plan item for my-skill path")
	}
	if item.ID != "plan-1" {
		t.Fatalf("expected plan-1 for my-skill path, got plan item id=%q", item.ID)
	}

	item = findFileReadPlanItemForPath(&contract, otherPath)
	if item == nil {
		t.Fatal("expected to find plan item for other-skill path")
	}
	if item.ID != "plan-2" {
		t.Fatalf("expected plan-2 for other-skill path, got plan item id=%q", item.ID)
	}

	item = findFileReadPlanItemForPath(&contract, "/tmp/unknown/file.txt")
	if item != nil {
		t.Fatal("expected no match for unknown path")
	}
}

func TestUpdatePlanItemForFileReadPathCompletesCorrectItem(t *testing.T) {
	skillPath := "/tmp/skills/my-skill/SKILL.md"
	otherPath := "/tmp/skills/other-skill/SKILL.md"

	state := session.State{Key: "cli:skill-path"}
	task := state.StartTask("run skill-based CLI")
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredSkills: []session.RequiredSkill{
			{Name: "my-skill", Path: skillPath},
		},
		PlanItems: []session.TaskPlanItem{
			{ID: "plan-1", Title: "read my-skill SKILL.md", Status: "pending", Tool: "file.read", Criteria: "read " + skillPath},
			{ID: "plan-2", Title: "read other-skill SKILL.md", Status: "pending", Tool: "file.read", Criteria: "read " + otherPath},
			{ID: "plan-3", Title: "execute via CLI", Status: "pending", Tool: "terminal.run", Criteria: "run"},
		},
	}
	task.Execution.Contract = &contract
	state.SetTaskContract(task.ID, contract)

	updatePlanItemForFileReadPath(&state, task.ID, skillPath, "accepted", "SKILL.md read")

	if updated := state.TaskByID(task.ID); updated != nil {
		for _, item := range updated.Execution.Contract.PlanItems {
			switch item.ID {
			case "plan-1":
				if item.Status != "completed" {
					t.Fatalf("plan-1 (my-skill) should be completed after reading its path, got status=%q", item.Status)
				}
			case "plan-2":
				if item.Status != "pending" {
					t.Fatalf("plan-2 (other-skill) should remain pending, got status=%q", item.Status)
				}
			case "plan-3":
				if item.Status != "pending" {
					t.Fatalf("plan-3 (terminal.run) should remain pending, got status=%q", item.Status)
				}
			}
		}
	}
}

func TestCompletionEvaluatorRequiresRealToolEvidence(t *testing.T) {
	contract := session.TaskContract{
		Summary:       "use skill to publish",
		RequiresTools: true,
		RequiredTools: []string{"file.read", "terminal.run"},
		RequiredSkills: []session.RequiredSkill{
			{Name: "publish-skill", Path: "/tmp/skills/publish-skill/SKILL.md", Reason: "needed for publish workflow"},
		},
		RequiredEvidence: []session.TaskEvidenceContract{
			{Kind: "local_file", Tool: "file.read", Description: "read /tmp/skills/publish-skill/SKILL.md"},
			{Kind: "remote_publish", Tool: "terminal.run", Description: "publish via CLI"},
		},
		PlanItems: []session.TaskPlanItem{
			{ID: "plan-1", Title: "read publish-skill SKILL.md", Status: "completed", Tool: "file.read", Criteria: "read /tmp/skills/publish-skill/SKILL.md"},
			{ID: "plan-2", Title: "publish via CLI", Status: "completed", Tool: "terminal.run", Criteria: "run publish command"},
		},
	}

	// Simulate the task having executed file.read and terminal.run as real tools.
	task := session.TaskNode{
		Steps: []session.TaskStep{
			{Tool: "file.read", Accepted: true},
			{Tool: "terminal.run", Accepted: true},
		},
		Execution: session.ExecutionFrame{
			Contract: &contract,
		},
	}

	validation := validateTaskContract(contract, task)
	if !validation.Satisfied {
		t.Fatalf("expected contract satisfied with real tool evidence, got missing=%v", validation.Missing)
	}

	// Verify accepted tools do not include skill names.
	accepted := acceptedTools(task)
	if accepted["publish-skill"] {
		t.Fatal("skill name publish-skill should not appear in accepted tools")
	}
	if !accepted["file.read"] {
		t.Fatal("real tool file.read should appear in accepted tools")
	}
	if !accepted["terminal.run"] {
		t.Fatal("real tool terminal.run should appear in accepted tools")
	}
}

func TestSkillNameNotInAcceptedTools(t *testing.T) {
	// Construct a task where every step/event uses only real tool names.
	task := session.TaskNode{
		Steps: []session.TaskStep{
			{Tool: "file.read", Accepted: true},
			{Tool: "terminal.run", Accepted: true},
			{Tool: "web.search", Accepted: true},
			{Tool: "file.write", Status: "accepted"},
		},
		Execution: session.ExecutionFrame{
			Events: []session.ExecutionEvent{
				{Type: "tool_result", Status: "accepted", Tool: "file.read"},
				{Type: "tool_result", Status: "accepted", Tool: "terminal.run"},
			},
		},
	}

	accepted := acceptedTools(task)

	// Real tools should appear.
	if !accepted["file.read"] {
		t.Fatal("file.read should be in accepted tools")
	}
	if !accepted["terminal.run"] {
		t.Fatal("terminal.run should be in accepted tools")
	}
	if !accepted["web.search"] {
		t.Fatal("web.search should be in accepted tools")
	}
	if !accepted["file.write"] {
		t.Fatal("file.write should be in accepted tools")
	}

	// Skill names must never appear — they are not real tools.
	skillLikeNames := []string{"publish-skill", "feishu-notify", "fresh-search", "agent-browser", "my-skill"}
	for _, name := range skillLikeNames {
		if accepted[name] {
			t.Fatalf("skill name %q must not appear in accepted tools", name)
		}
	}
}

func TestSkillReadCompletedFollowupEncouragesRealTools(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"file.read", "terminal.run"},
		RequiredSkills: []session.RequiredSkill{
			{Name: "publish-skill", Path: "/tmp/skills/publish-skill/SKILL.md", Reason: "needed for publish workflow"},
		},
		PlanItems: []session.TaskPlanItem{
			{ID: "plan-1", Title: "read publish-skill SKILL.md", Status: "completed", Tool: "file.read", Criteria: "read /tmp/skills/publish-skill/SKILL.md"},
			{ID: "plan-2", Title: "publish via CLI", Status: "pending", Tool: "terminal.run", Criteria: "run publish command"},
		},
	}

	missing := []string{"tool:terminal.run"}
	accepted := map[string]bool{"file.read": true}

	followup := taskContractFollowupWithGuidance(missing, nil, contract, accepted)

	// When skill is read, it should encourage using real tools.
	if !strings.Contains(followup, "terminal.run") {
		t.Fatal("followup should mention terminal.run as the real tool to execute")
	}
	if !strings.Contains(followup, "skill:publish-skill read") {
		t.Fatal("followup should acknowledge skill has been read")
	}
	if strings.Contains(followup, "publish-skill") && strings.Contains(followup, "must be read") {
		t.Fatal("followup should not say skill must be read when it's already completed")
	}
}

func TestContractPlanItemToolNeverSkillName(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"file.read", "terminal.run"},
		RequiredSkills: []session.RequiredSkill{
			{Name: "my-skill", Path: "/tmp/skills/my-skill/SKILL.md", Reason: "needed"},
		},
		RequiredEvidence: []session.TaskEvidenceContract{
			{Kind: "local_file", Tool: "file.read", Description: "read /tmp/skills/my-skill/SKILL.md"},
		},
		PlanItems: []session.TaskPlanItem{
			{ID: "plan-1", Title: "read skill", Status: "pending", Tool: "file.read", Criteria: "read /tmp/skills/my-skill/SKILL.md"},
			{ID: "plan-2", Title: "execute", Status: "pending", Tool: "terminal.run", Criteria: "run command"},
		},
	}
	skills := []discoveredSkill{
		{Name: "my-skill", Path: "/tmp/skills/my-skill/SKILL.md"},
	}

	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "ok"})
	registry.Register(runtimeNamedTool{name: "terminal.run", content: "ok"})

	validation := validateContractTools(contract, registry, skills)
	if !validation.IsValid() {
		t.Fatalf("contract should be valid when plan items use real tool names, got invalid_tools=%v invalid_skills=%v", validation.InvalidTools, validation.InvalidSkills)
	}

	for _, item := range contract.PlanItems {
		toolName := strings.TrimSpace(item.Tool)
		for _, s := range skills {
			if strings.EqualFold(toolName, strings.TrimSpace(s.Name)) {
				t.Fatalf("plan item tool %q matches skill name %q — skill names must never appear as tool in plan_items", toolName, s.Name)
			}
		}
	}
}

func TestPlanItemFallbackForBlocked(t *testing.T) {
	contract := session.TaskContract{
		PlanItems: []session.TaskPlanItem{
			{ID: "plan-1", Title: "read SKILL.md", Tool: "file.read", Status: "completed"},
			{ID: "plan-2", Title: "execute", Tool: "terminal.run", Status: "blocked"},
			{ID: "plan-3", Title: "report", Tool: "file.write", Status: "pending"},
		},
	}

	item := planItemForTool(&contract, "terminal.run", "pending")
	if item == nil {
		t.Fatal("expected planItemForTool to find blocked terminal.run as fallback")
	}
	if item.ID != "plan-2" {
		t.Fatalf("expected plan-2 (blocked terminal.run), got %q", item.ID)
	}

	item = planItemForTool(&contract, "file.read", "pending")
	if item != nil {
		t.Fatalf("completed file.read should not be found via pending search, got id=%q", item.ID)
	}
}

func TestSkillStepReadAccepted(t *testing.T) {
	skillPath := "/tmp/skills/my-skill/SKILL.md"

	tests := []struct {
		name  string
		steps []session.TaskStep
		want  bool
	}{
		{name: "empty steps", want: false},
		{
			name: "accepted file.read matching path in summary",
			steps: []session.TaskStep{
				{Tool: "file.read", Accepted: true, Summary: "read " + skillPath},
			},
			want: true,
		},
		{
			name: "accepted file.read but no path match",
			steps: []session.TaskStep{
				{Tool: "file.read", Accepted: true, Summary: "read config file"},
				{Tool: "terminal.run", Accepted: true, Summary: "run command"},
			},
			want: false,
		},
		{
			name: "not accepted file.read should not count",
			steps: []session.TaskStep{
				{Tool: "file.read", Accepted: false, Summary: "read " + skillPath},
			},
			want: false,
		},
		{
			name: "accepted file.read matches skill name when path is empty",
			steps: []session.TaskStep{
				{Tool: "file.read", Accepted: true, Summary: "read my-skill SKILL.md"},
			},
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := session.RequiredSkill{Name: "my-skill", Path: skillPath}
			if tc.name == "accepted file.read matches skill name when path is empty" {
				s.Path = ""
			}
			got := skillStepReadAccepted(s, tc.steps)
			if got != tc.want {
				t.Fatalf("skillStepReadAccepted() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRequiredSkillReadCompletedWithSteps(t *testing.T) {
	skillPath := "/tmp/skills/my-skill/SKILL.md"
	skill := session.RequiredSkill{Name: "my-skill", Path: skillPath}

	contract := session.TaskContract{
		RequiredSkills: []session.RequiredSkill{skill},
		PlanItems: []session.TaskPlanItem{
			{ID: "plan-1", Tool: "file.read", Status: "pending", Criteria: "read " + skillPath},
			{ID: "plan-2", Tool: "terminal.run", Status: "running", Criteria: "execute"},
		},
	}

	if requiredSkillReadCompletedWithSteps(nil, contract, skill) {
		t.Fatal("expected false with no completed plan item and no steps")
	}

	steps := []session.TaskStep{
		{Tool: "file.read", Accepted: true, Summary: "read " + skillPath},
	}
	if !requiredSkillReadCompletedWithSteps(steps, contract, skill) {
		t.Fatal("expected true when steps have accepted file.read")
	}

	contract2 := contract
	contract2.PlanItems[0].Status = "completed"
	if !requiredSkillReadCompletedWithSteps(nil, contract2, skill) {
		t.Fatal("expected true when plan item completed")
	}
}
