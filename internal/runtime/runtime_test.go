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
	"github.com/dongping/mateway/internal/secret"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/tool"
)

func TestRuntimeNoActiveTaskCreatesNewTask(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = plannerVerifierModel{planJSON: testUnifiedPlanJSON(
		"summarize the project",
		"done",
		nil,
		nil,
		`{"id":"answer","type":"subtask","mode":"direct","goal":"summarize the project","acceptance":"done"}`,
	), text: "done"}
	rt.Pool.agents["main"] = agentcore.NewAgent(rt.Model, rt.Tools)

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

func TestRuntimePlannerTimeoutKeepsTaskAwaitingInput(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = deadlineModel{}
	rt.Pool.agents["main"] = agentcore.NewAgent(rt.Model, rt.Tools)

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "summarize the project"))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Failed {
		t.Fatalf("expected failed response for transient planner error, got %#v", resp)
	}
	state := loadState(t, rt, "cli:test")
	if len(state.Tasks) != 1 {
		t.Fatalf("expected one task, got %#v", state.Tasks)
	}
	if state.Tasks[0].Status != "await_user_input" {
		t.Fatalf("planner timeout should keep task resumable, got %#v", state.Tasks[0])
	}
	if state.ActiveTask != state.Tasks[0].ID {
		t.Fatalf("expected active task to remain resumable, active=%q task=%q", state.ActiveTask, state.Tasks[0].ID)
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

func TestRuntimeSystemContextAddsLocalTonightFreshnessPolicy(t *testing.T) {
	cfg := config.DefaultRoot()
	cfg.Scheduler.Timezone = "Asia/Shanghai"
	contract := session.TaskContract{
		Summary:       "find tonight's games",
		RequiresTools: true,
		RequiredEvidence: []session.TaskEvidenceContract{{
			Kind:        "current_external_fact",
			Tool:        "web.search",
			Description: "current schedule",
		}},
	}
	text := buildRuntimeSystemContextForTask(&cfg, config.AgentProfileConfig{}, "what games are tonight", contract)
	for _, want := range []string{
		"Asia/Shanghai",
		"local evening-to-late-night window",
		"do not include events that already happened earlier this morning",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in runtime context, got:\n%s", want, text)
		}
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
	rt.Model = plannerVerifierModel{planJSON: testUnifiedPlanJSON(
		"just answer",
		"plain answer",
		nil,
		nil,
		`{"id":"answer","type":"subtask","mode":"direct","goal":"answer","acceptance":"answered"}`,
	), text: "plain answer"}
	rt.Pool.agents["main"] = agentcore.NewAgent(rt.Model, rt.Tools)

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

func TestTraceSummaryReportsModelCallStages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	for _, event := range []map[string]any{
		{"type": "model_call_start", "model_stage": "planner"},
		{"type": "model_call_end", "model_stage": "planner"},
		{"type": "model_call_start", "model_stage": "node_direct"},
		{"type": "model_call_failed", "model_stage": "node_direct"},
		{"type": "model_call_skipped", "model_stage": "node_verifier"},
		{"type": "model_call_skipped", "model_stage": "finalizer"},
	} {
		if err := AppendTraceEvent(path, event); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := SummarizeTrace(path)
	if err != nil {
		t.Fatal(err)
	}
	if summary.ModelCallStarts != 2 || summary.ModelCallEnds != 1 || summary.ModelCallFailures != 1 || summary.ModelCallSkips != 2 {
		t.Fatalf("unexpected model call totals: %#v", summary)
	}
	if got := summary.ModelStages["planner"]; got.Starts != 1 || got.Ends != 1 {
		t.Fatalf("unexpected planner stage summary: %#v", got)
	}
	if got := summary.ModelStages["node_direct"]; got.Starts != 1 || got.Failures != 1 {
		t.Fatalf("unexpected node_direct stage summary: %#v", got)
	}
	if got := summary.ModelStages["node_verifier"]; got.Skips != 1 {
		t.Fatalf("unexpected node_verifier stage summary: %#v", got)
	}
	names := strings.Join(summary.ModelStageNames(), ",")
	if names != "finalizer,node_direct,node_verifier,planner" {
		t.Fatalf("unexpected sorted stage names: %q", names)
	}
}

func TestRuntimeHandle_CompletedTaskReferenceInjectsGraphContext(t *testing.T) {
	rt := newTestRuntime(t)
	model := &completedReferenceModel{}
	rt.Model = model

	first, err := rt.Handle(t.Context(), inbound("cli:completed-ref", "简要说明 runtime 包职责"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Failed {
		t.Fatalf("first task failed: %s", first.Reply.Text)
	}

	second, err := rt.Handle(t.Context(), inbound("cli:completed-ref", "基于刚才的结果，用一句话总结。"))
	if err != nil {
		t.Fatal(err)
	}
	if second.Failed {
		t.Fatalf("second task failed: %s", second.Reply.Text)
	}
	if !strings.Contains(second.Reply.Text, "调度 TaskGraph") {
		t.Fatalf("second reply did not use referenced context: %q", second.Reply.Text)
	}
	if !strings.Contains(model.secondNodeContext, "[referenced_task_context]") || !strings.Contains(model.secondNodeContext, "调度 TaskGraph") {
		t.Fatalf("second node did not receive referenced task context:\n%s", model.secondNodeContext)
	}
	if !strings.Contains(model.secondNodeContext, "Final output:") {
		t.Fatalf("second node should receive final output context:\n%s", model.secondNodeContext)
	}
	state := loadState(t, rt, "cli:completed-ref")
	if len(state.Tasks) < 2 {
		t.Fatalf("expected two tasks, got %#v", state.Tasks)
	}
	if refs := state.Tasks[1].Execution.ContextRefs; len(refs) != 1 || refs[0] != state.Tasks[0].ID {
		t.Fatalf("expected second task context refs to point to first task, got %v", refs)
	}
	data, err := os.ReadFile(second.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	trace := string(data)
	if !strings.Contains(trace, `"type":"context_refs_attached"`) || !strings.Contains(trace, `"type":"context_refs_loaded"`) {
		t.Fatalf("trace missing context refs events:\n%s", trace)
	}
}

func TestRuntimeHandle_NewGraphCarriesRecentCompletedContext(t *testing.T) {
	rt := newTestRuntime(t)
	model := &completedReferenceModel{}
	rt.Model = model

	first, err := rt.Handle(t.Context(), inbound("cli:recent-context", "简要说明 runtime 包职责"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Failed {
		t.Fatalf("first task failed: %s", first.Reply.Text)
	}

	second, err := rt.Handle(t.Context(), inbound("cli:recent-context", "写一个新的总结标题"))
	if err != nil {
		t.Fatal(err)
	}
	if second.Failed {
		t.Fatalf("second task failed: %s", second.Reply.Text)
	}
	if !strings.Contains(model.secondNodeContext, "[referenced_task_context]") {
		t.Fatalf("new graph did not receive recent completed task context:\n%s", model.secondNodeContext)
	}
	state := loadState(t, rt, "cli:recent-context")
	if refs := state.Tasks[1].Execution.ContextRefs; len(refs) != 1 || refs[0] != state.Tasks[0].ID {
		t.Fatalf("expected new graph to carry recent completed task context, got %v", refs)
	}
	data, err := os.ReadFile(second.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	trace := string(data)
	if !strings.Contains(trace, `"action":"new_graph"`) || !strings.Contains(trace, `"type":"context_refs_attached"`) {
		t.Fatalf("trace should show new_graph with context refs attached:\n%s", trace)
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
		writeRuntimeSkillMetadata(t, dir, "execution", "prompt", "subtask")
	}
	feishuDir := filepath.Join(ws, "skills", "feishu-notify")
	if err := os.MkdirAll(feishuDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(feishuDir, "SKILL.md"), []byte("---\nname: feishu-notify\ndescription: Create Feishu/Lark cloud documents.\npriority: 80\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRuntimeSkillMetadata(t, feishuDir, "execution", "prompt", "subtask")

	text := skillsPrompt(skillsForRuntimeContext(rt.Config, "main"))
	if !strings.Contains(text, "feishu-notify") {
		t.Fatalf("expected runtime context skills to include feishu-notify beyond old 12-skill limit, got:\n%s", text)
	}
}

func TestSkillsPromptExplainsWorkspaceLookupOrder(t *testing.T) {
	text := skillsPrompt([]discoveredSkill{{
		Name:        "demo",
		Description: "Demo workflow.",
		Path:        "/home/me/.mateway/workspace/agents/main/skills/demo/SKILL.md",
		Scope:       "agent",
	}})
	for _, want := range []string{
		"read the exact Location path below",
		"agent workspace first, then shared workspace",
		"workspace/agents/<agent>/skills/<skill>/SKILL.md",
		"workspace/skills/<skill>/SKILL.md",
		"Do not create task artifacts inside workspace/skills",
		"Do not guess skill paths under the current project/repository root",
		"/home/me/.mateway/workspace/agents/main/skills/demo/SKILL.md",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected skills prompt to contain %q, got:\n%s", want, text)
		}
	}
}

func TestEnsureGraphForTaskUsesWorkflowLaneForWorkflowSkill(t *testing.T) {
	rt, workspace := newTestRuntimeWithWorkspace(t)
	skillDir := filepath.Join(workspace, "skills", "creator-ppt-production-studio")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: creator-ppt-production-studio
description: Produce creator PPT packages and HTML PPT decks.
---
# Creator PPT Production Studio
`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRuntimeWorkflowSkillMetadata(t, skillDir)

	state := &session.State{}
	task := state.EnsureTask("使用 creator-ppt-production-studio：选题 Cursor，新建小红书口播稿和 HTML PPT")
	trace := newTestTraceRecorder(t)

	if err := rt.ensureGraphForTask(t.Context(), inbound("cli:test", task.Goal), state, task, task.Goal, trace); err != nil {
		t.Fatal(err)
	}
	if task.Graph == nil || task.Graph.Lane != workflowLane {
		t.Fatalf("expected workflow lane graph, got %#v", task.Graph)
	}
	if len(task.Graph.Nodes) != 5 {
		t.Fatalf("expected 5 workflow lane nodes, got %d", len(task.Graph.Nodes))
	}
	if task.Graph.Nodes[0].ID != "load-workflow-skill" {
		t.Fatalf("first node should load skill, got %q", task.Graph.Nodes[0].ID)
	}
	review := task.Graph.NodeByID("review-workflow-gate")
	if review == nil || review.Type != session.NodeTypeHumanReview {
		t.Fatalf("expected human review gate, got %#v", review)
	}
	if !strings.Contains(review.Acceptance.Criteria, "口播稿") || !strings.Contains(review.Acceptance.Criteria, "选择") {
		t.Fatalf("expected business review question, got %q", review.Acceptance.Criteria)
	}
	for _, want := range []string{
		filepath.Join("outputs"),
		"xiaohongshu-script.md",
		"slide-outline.md",
		filepath.Join(skillDir, "assets", "style-catalog", "index.html"),
	} {
		if !strings.Contains(review.Acceptance.Criteria, want) {
			t.Fatalf("expected review question to contain %q, got %q", want, review.Acceptance.Criteria)
		}
	}
	contract := state.TaskByID(task.ID).Execution.Contract
	if contract == nil || len(contract.RequiredSkills) != 1 || contract.RequiredSkills[0].Path != filepath.Join(skillDir, "SKILL.md") {
		t.Fatalf("expected workflow skill contract with exact skill path, got %#v", contract)
	}
	data, err := os.ReadFile(trace.path)
	if err != nil {
		t.Fatal(err)
	}
	if traceText := string(data); !strings.Contains(traceText, `"type":"task_lane_selected"`) || !strings.Contains(traceText, `"lane":"workflow"`) {
		t.Fatalf("expected workflow task_lane_selected in trace, got:\n%s", traceText)
	}
}

func TestWorkflowLanePPTPlanSeparatesDraftReviewAndDeckGeneration(t *testing.T) {
	workspace := t.TempDir()
	skill := discoveredSkill{
		Name:         "creator-ppt-production-studio",
		Path:         filepath.Join(workspace, "skills", "creator-ppt-production-studio", "SKILL.md"),
		Granularity:  "workflow",
		Outputs:      []string{"xiaohongshu_script_path", "slide_outline_path", "deck_horizontal_path", "deck_vertical_path"},
		AllowedTools: []string{"file.read", "file.write", "file.edit", "web.search", "web.fetch"},
		HumanGates:   []string{"review generated scripts and slide outline before deck generation", "choose or confirm PPT style before deck generation"},
	}
	g, contract := buildWorkflowLaneGraph("task-ppt", "使用 creator-ppt-production-studio：生成小红书口播稿，HTML PPT", workspace, skill)
	draft := g.NodeByID("draft-workflow-artifacts")
	review := g.NodeByID("review-workflow-gate")
	finalize := g.NodeByID("finalize-workflow-artifacts")
	if draft == nil || review == nil || finalize == nil {
		t.Fatalf("expected draft/review/finalize nodes, got %#v", g.NodeIDs())
	}
	if strings.Contains(strings.ToLower(draft.Goal), "final decks") && !strings.Contains(strings.ToLower(draft.Goal), "do not generate final decks") {
		t.Fatalf("draft node should defer final decks, got %q", draft.Goal)
	}
	if !strings.Contains(strings.ToLower(finalize.Goal), "html ppt") && !strings.Contains(strings.ToLower(finalize.Goal), "deck") {
		t.Fatalf("finalize node should generate decks, got %q", finalize.Goal)
	}
	if !strings.Contains(review.Acceptance.Criteria, "口播稿") || !strings.Contains(review.Acceptance.Criteria, "风格") {
		t.Fatalf("review gate should be business-specific, got %q", review.Acceptance.Criteria)
	}
	for _, want := range []string{
		filepath.Join(workspace, "outputs"),
		"xiaohongshu-script.md",
		"slide-outline.md",
		filepath.Join(filepath.Dir(skill.Path), "assets", "style-catalog", "index.html"),
	} {
		if !strings.Contains(review.Acceptance.Criteria, want) {
			t.Fatalf("expected review question to contain %q, got %q", want, review.Acceptance.Criteria)
		}
	}
	if len(contract.FinalOutput) != 4 {
		t.Fatalf("expected metadata outputs carried into contract, got %#v", contract.FinalOutput)
	}
}

func TestWorkflowLaneNeedsRepairDoesNotAppendTaskRepairNode(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Config.Execution.MaxRepairRounds = intPtrTest(2)
	rt.Model = &dispatchModel{taskVerifier: taskVerifierSequence("needs_repair")}
	g := newTestGraph(session.TaskGraphNode{
		ID:            "final",
		Type:          session.NodeTypeSubtask,
		Mode:          session.NodeModeDirect,
		Goal:          "final",
		Status:        session.NodeStatusCompleted,
		ResultSummary: "partial",
		Output:        map[string]any{"text": "partial"},
		Acceptance:    session.Acceptance{Criteria: "done", Verified: true},
	})
	g.Lane = workflowLane
	task := &session.TaskNode{ID: g.TaskID, Goal: "workflow", Graph: g}
	state := &session.State{Tasks: []session.TaskNode{*task}, ActiveTask: task.ID}
	state.SetTaskContract(g.TaskID, session.TaskContract{FinalOutput: []string{"missing_path"}, TaskAcceptance: "must produce missing path"})

	resp, err := rt.runGraphTask(t.Context(), inbound("cli:test", "workflow"), state, &state.Tasks[0], "workflow", newTestTraceRecorder(t))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Failed {
		t.Fatalf("expected blocked response when workflow repair is disabled, got %#v", resp)
	}
	if len(state.Tasks[0].Graph.Nodes) != 1 {
		t.Fatalf("workflow lane must not append task repair nodes, got %d", len(state.Tasks[0].Graph.Nodes))
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
		writeRuntimeSkillMetadata(t, dir, "execution", "prompt", "subtask")
	}
	feishuPath := filepath.Join(ws, "skills", "feishu-notify", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(feishuPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(feishuPath, []byte("---\nname: feishu-notify\ndescription: Create Feishu/Lark cloud documents.\npriority: 80\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRuntimeSkillMetadata(t, filepath.Dir(feishuPath), "execution", "prompt", "subtask")
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

func TestRuntimeTaskContractAllowsNoToolTask(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = plannerVerifierModel{planJSON: testUnifiedPlanJSON(
		"explain what Mateway is",
		"short explanation",
		nil,
		nil,
		`{"id":"answer","type":"subtask","mode":"direct","goal":"explain Mateway","acceptance":"answered"}`,
	), text: "Mateway is a local agent runtime."}
	rt.Pool.agents["main"] = agentcore.NewAgent(rt.Model, rt.Tools)
	rt.ContractModel = panicModel{t: t}

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "explain what Mateway is"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed || resp.Reply.Text != "Mateway is a local agent runtime." {
		t.Fatalf("expected no-tool task to complete, got %#v", resp)
	}
}

func TestRuntimePlannerParseFailureDoesNotFallback(t *testing.T) {
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "terminal.run", content: "ok"})
	rt.Tools = registry
	rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "plain answer"}, rt.Tools)
	rt.Model = contractJSONModel{json: `not json`}
	rt.ContractModel = panicModel{t: t}

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
	if !resp.Failed {
		t.Fatalf("planner parse failure should fail instead of falling back, got %#v", resp)
	}
	data, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	trace := string(data)
	if !strings.Contains(trace, "unified_planner_failed") {
		t.Fatalf("expected unified planner failure trace, got:\n%s", trace)
	}
	if strings.Contains(trace, "task_contract_parse_failed") || strings.Contains(trace, "graph_planner_fallback") {
		t.Fatalf("old planner fallback trace should not appear, got:\n%s", trace)
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

func TestRuntimeProgressSinkEmitsGraphNodeProgressOnly(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = plannerVerifierModel{planJSON: testUnifiedPlanJSON(
		"collect facts",
		"answer with facts",
		[]string{"web.search"},
		nil,
		`{"id":"search","type":"subtask","mode":"react","goal":"Collect current facts","acceptance":"search completed","allowed_tools":["web.search"]}`,
	), text: "final answer"}
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "web.search", content: "result"})
	rt.Tools = registry
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "web.search",
			Args: map[string]any{"query": "current facts"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "facts collected"},
	}}, rt.Tools)

	var updates []channel.OutboundMessage
	rt.ProgressSink = func(msg channel.OutboundMessage) {
		updates = append(updates, msg)
	}
	if _, err := rt.Handle(context.Background(), inbound("cli:test", "collect facts")); err != nil {
		t.Fatal(err)
	}
	var sawPlanRunning, sawPlanDone, sawNodeRunning, sawNodeDone bool
	for _, update := range updates {
		for _, step := range update.Progress {
			switch {
			case step.Title == "Plan" && step.Status == "running":
				sawPlanRunning = true
			case step.Title == "Plan" && step.Status == "completed":
				sawPlanDone = true
			case step.Title == "Collect current facts" && step.Status == "running":
				sawNodeRunning = true
			case step.Title == "Collect current facts" && step.Status == "completed":
				sawNodeDone = true
			case step.Tool == "web.search" && step.Status == "running":
				t.Fatalf("tool-level progress should not be user-visible: %#v", updates)
			case step.Tool == "web.search" && step.Status == "completed":
				t.Fatalf("tool-level progress should not be user-visible: %#v", updates)
			}
		}
	}
	if !sawPlanRunning || !sawPlanDone || !sawNodeRunning || !sawNodeDone {
		t.Fatalf("missing progress updates planRunning=%v planDone=%v nodeRunning=%v nodeDone=%v updates=%#v",
			sawPlanRunning, sawPlanDone, sawNodeRunning, sawNodeDone, updates)
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

func TestRuntimeExplicitNewAfterContinuationOfferStartsNewTask(t *testing.T) {
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

	if _, err := rt.Handle(context.Background(), inbound("cli:test", "/new Now list every yaml file under ~/.mateway/config")); err != nil {
		t.Fatal(err)
	}
	updated := loadState(t, rt, "cli:test")
	if len(updated.Tasks) != 2 || updated.Tasks[1].Goal != "/new Now list every yaml file under ~/.mateway/config" {
		t.Fatalf("expected explicit /new request to create new task, got %#v", updated.Tasks)
	}
	if strings.Contains(model.lastUser, "Active task:") {
		t.Fatalf("explicit /new request should not be merged into previous offer, got %q", model.lastUser)
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

type deadlineModel struct{}

func (m deadlineModel) Next(context.Context, agentcore.Context) (agentcore.Message, error) {
	return agentcore.Message{}, context.DeadlineExceeded
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

type completedReferenceModel struct {
	plannerCalls      int
	nodeCalls         int
	secondNodeContext string
}

func (m *completedReferenceModel) Next(_ context.Context, ctx agentcore.Context) (agentcore.Message, error) {
	if strings.Contains(ctx.SystemPrompt, "TaskGraphPlan") || strings.Contains(ctx.SystemPrompt, "task graph planner") {
		m.plannerCalls++
		goal := "summarize runtime package"
		answer := "runtime 负责调度 TaskGraph 并执行 node"
		if m.plannerCalls > 1 {
			goal = "summarize previous result"
			answer = "基于刚才结果总结 runtime 如何调度 TaskGraph"
		}
		return agentcore.Message{Role: agentcore.RoleAssistant, Content: testUnifiedPlanJSON(
			goal,
			"answer uses available context",
			nil,
			nil,
			fmt.Sprintf(`{"id":"answer","type":"subtask","mode":"direct","goal":%q,"depends":[],"outputs":["text"],"acceptance":"answer uses available context"}`, answer),
		)}, nil
	}
	if strings.Contains(ctx.SystemPrompt, "verification judge") {
		return agentcore.Message{Role: agentcore.RoleAssistant, Content: `{"status":"passed","reason":"ok","confidence":"high"}`}, nil
	}
	m.nodeCalls++
	var combined strings.Builder
	for _, msg := range ctx.Messages {
		combined.WriteString(msg.Content)
		combined.WriteString("\n")
	}
	if m.nodeCalls == 1 {
		return agentcore.Message{Role: agentcore.RoleAssistant, Content: "runtime 负责调度 TaskGraph 并执行 node"}, nil
	}
	m.secondNodeContext = combined.String()
	return agentcore.Message{Role: agentcore.RoleAssistant, Content: "runtime 负责调度 TaskGraph 并执行 node。"}, nil
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

func TestCompletedTaskClearsActiveAndDoesNotImplicitlyResume(t *testing.T) {
	rt := newTestRuntime(t)
	state := session.State{Key: "cli:test"}
	state.StartTask("check system status")
	state.CompleteActiveTaskWithSummary("system is running", "trace-one", "/tmp/trace-one.jsonl")
	if err := rt.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	rt.Model = plannerVerifierModel{planJSON: testUnifiedPlanJSON(
		"list running processes",
		"new task result",
		nil,
		nil,
		`{"id":"answer","type":"subtask","mode":"direct","goal":"list running processes","acceptance":"answered"}`,
	), text: "new task result"}
	rt.Pool.agents["main"] = agentcore.NewAgent(rt.Model, rt.Tools)

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
	writeRuntimeSkillMetadata(t, skillDir, "execution", "prompt", "subtask")
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
	writeRuntimeSkillMetadata(t, skillDir, "execution", "prompt", "subtask")
	skills := discoverSkillsForAgent(&cfg, "main", 24)
	if len(skills) != 1 || skills[0].Name != "feishu-notify" {
		t.Fatalf("expected skill discovery to parse safe header despite token in body, got %#v", skills)
	}
}

func TestSkillDiscoveryIgnoresRawSkillWithoutMetadata(t *testing.T) {
	ws := t.TempDir()
	cfg := config.DefaultRoot()
	cfg.App.Workspace = ws
	skillDir := filepath.Join(ws, "skills", "raw")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: raw\ndescription: Raw skill.\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if skills := discoverSkillsForAgent(&cfg, "main", 24); len(skills) != 0 {
		t.Fatalf("raw skill without metadata must not be discovered, got %#v", skills)
	}
}

func TestSkillDiscoveryAgentScopedOverridesShared(t *testing.T) {
	ws := t.TempDir()
	cfg := config.DefaultRoot()
	cfg.App.Workspace = ws
	shared := filepath.Join(ws, "skills", "demo")
	agent := filepath.Join(ws, "agents", "main", "skills", "demo")
	for _, dir := range []string{shared, agent} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeRuntimeSkillMetadata(t, dir, "execution", "prompt", "subtask")
	}
	if err := os.WriteFile(filepath.Join(shared, "SKILL.md"), []byte("---\nname: demo\ndescription: Shared.\npriority: 10\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agent, "SKILL.md"), []byte("---\nname: demo\ndescription: Agent scoped.\npriority: 5\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	skills := discoverSkillsForAgent(&cfg, "main", 24)
	if len(skills) != 1 {
		t.Fatalf("expected override to leave one skill, got %#v", skills)
	}
	if skills[0].Scope != "agent" || skills[0].Description != "Agent scoped." {
		t.Fatalf("expected agent-scoped skill override, got %#v", skills[0])
	}
}

func skillNames(skills []discoveredSkill) []string {
	var names []string
	for _, s := range skills {
		names = append(names, s.Name)
	}
	return names
}

func TestUnavailablePlannerToolFastBlockerNoContractFollowup(t *testing.T) {
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
	rt.Model = plannerVerifierModel{planJSON: testUnifiedPlanJSON(
		"check service",
		"status confirmed",
		[]string{"terminal.run"},
		nil,
		`{"id":"check","type":"subtask","mode":"react","goal":"check service","allowed_tools":["terminal.run"],"acceptance":"status confirmed"}`,
	), text: "done"}
	rt.Pool.agents["main"] = agentcore.NewAgent(rt.Model, rt.Pool.agents["main"].Tools)
	rt.ContractModel = panicModel{t: t}

	planResp, err := rt.Handle(context.Background(), inbound("cli:test", "check singbox service status"))
	if err != nil {
		t.Fatal(err)
	}
	resp := planResp
	trace, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	traceStr := string(trace)
	if !resp.Failed {
		t.Fatalf("expected failed response for unavailable planner tool, got %#v", resp)
	}
	if !strings.Contains(traceStr, "unified_planner_invalid_tools") {
		t.Fatalf("expected unified_planner_invalid_tools trace, got:\n%s", traceStr)
	}
	if strings.Contains(traceStr, "contract_followup_sent") {
		t.Fatalf("unavailable planner tool should not send contract follow-ups, got:\n%s", traceStr)
	}
}

func TestProgressDoesNotLeakFinalText(t *testing.T) {
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

func TestUnregisteredPlannerToolBlocksBeforeGraphAttach(t *testing.T) {
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "ok"})
	rt.Tools = registry

	rt.Model = plannerVerifierModel{planJSON: testUnifiedPlanJSON(
		"test",
		"done",
		[]string{"fictional-tool", "file.read"},
		nil,
		`{"id":"do-stuff","type":"subtask","mode":"react","goal":"do stuff","allowed_tools":["fictional-tool"],"acceptance":"done"}`,
	), text: "done"}
	rt.Pool.agents["main"] = agentcore.NewAgent(rt.Model, rt.Tools)
	rt.ContractModel = panicModel{t: t}

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "run test"))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Failed {
		t.Fatal("expected task to fail/block with unregistered planner tool")
	}
	data, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	trace := string(data)
	if !strings.Contains(trace, "unified_planner_invalid_tools") || !strings.Contains(trace, "fictional-tool") {
		t.Fatalf("expected invalid planner tool trace, got:\n%s", trace)
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

func TestLegacyContractValidationRequiresRealToolEvidence(t *testing.T) {
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

func writeRuntimeSkillMetadata(t *testing.T, dir, stage, graphType, granularity string) {
	t.Helper()
	metadataDir := filepath.Join(dir, ".mateway")
	if err := os.MkdirAll(metadataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf(`adapter_version: "2"
source: "test"
installed_at: "2026-06-17T00:00:00Z"
tool_runtime: "mateway"
graph:
  mode: "adapted"
  type: "%s"
  stage: "%s"
  granularity: "%s"
`, graphType, stage, granularity)
	if err := os.WriteFile(filepath.Join(metadataDir, "metadata.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeRuntimeWorkflowSkillMetadata(t *testing.T, dir string) {
	t.Helper()
	metadataDir := filepath.Join(dir, ".mateway")
	if err := os.MkdirAll(metadataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `adapter_version: "2"
source: "test"
installed_at: "2026-06-17T00:00:00Z"
tool_runtime: "mateway"
graph:
  mode: "adapted"
  type: "prompt"
  stage: "execution"
  granularity: "workflow"
  outputs:
    - xiaohongshu_script_path
    - slide_outline_path
    - deck_horizontal_path
    - deck_vertical_path
    - production_metadata_path
  allowed_tools:
    - file.read
    - file.write
    - file.edit
    - web.search
    - web.fetch
  human_gates:
    - review generated scripts and slide outline before deck generation
    - choose or confirm PPT style before deck generation
  usage: "Draft script and outline, pause for review, then generate HTML PPT."
`
	if err := os.WriteFile(filepath.Join(metadataDir, "metadata.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
