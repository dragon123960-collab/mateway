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
	rt := newTestRuntime(t)
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, Content: "I will check now."},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "project.index",
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
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "web.search", content: "Beijing clear; Yiwu light rain."})
	rt.Tools = registry
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, Content: "I will check the weather and then advise."},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "web.search",
			Args: map[string]any{"query": "Beijing Yiwu weather tomorrow 2026-06-09"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "Weather checked. Travel looks acceptable with rain precautions."},
	}}, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"weather travel advice","requires_tools":true,"required_tools":["web.search"],"required_evidence":[{"kind":"current_external_fact","tool":"web.search","description":"current weather for Beijing and Yiwu"}],"expected_outcome":"travel recommendation","completion_policy":"use web evidence before final answer"}`}

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "help me decide whether to travel tomorrow from Beijing to Yiwu"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed || !strings.Contains(resp.Reply.Text, "Weather checked") {
		t.Fatalf("expected contract repair to complete with tool evidence, got %#v", resp)
	}
	data, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	trace := string(data)
	if !strings.Contains(trace, "task_contract_created") || !strings.Contains(trace, "task_contract_unsatisfied") || !strings.Contains(trace, "task_contract_satisfied") {
		t.Fatalf("expected contract lifecycle trace, got:\n%s", trace)
	}
	state := loadState(t, rt, "cli:test")
	if state.Tasks[0].Execution.Contract == nil || !state.Tasks[0].Execution.Contract.RequiresTools {
		t.Fatalf("expected stored task contract, got %#v", state.Tasks[0].Execution.Contract)
	}
}

func TestRuntimeTaskContractStrengthensServerStatusToTerminalRun(t *testing.T) {
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "terminal.run", content: "sing-box.service active"})
	rt.Tools = registry
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, Content: "I cannot access the server directly."},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "terminal.run",
			Args: map[string]any{"command": "ssh overseas 'systemctl status sing-box --no-pager'"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "sing-box is active."},
	}}, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"Check the status of the sing-box project (current releases/repo state)","requires_tools":true,"required_tools":["web.search"],"required_evidence":[{"kind":"current_external_fact","tool":"web.search","description":"latest sing-box release"}],"expected_outcome":"latest version status","completion_policy":"use web evidence before final answer"}`}

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "不用脚本，你也可以直接访问国外服务器吧，给我去看看singbox状态"))
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
	rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "plain answer"}, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `not json`}

	resp, err := rt.Handle(context.Background(), inbound("cli:test", "answer plainly"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed || resp.Reply.Text != "plain answer" {
		t.Fatalf("expected parse failure fallback, got %#v", resp)
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
	rt := newTestRuntime(t)
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "project.index",
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
		if step.Tool == "project.index" && step.Status == "running" {
			foundRunning = true
		}
	}
	if !foundRunning {
		t.Fatalf("expected running project.index progress update, got %#v", updates)
	}
	foundAccepted := false
	for _, update := range updates {
		if len(update.Progress) == 0 {
			continue
		}
		step := update.Progress[len(update.Progress)-1]
		if step.Tool == "project.index" && step.Status == "accepted" {
			foundAccepted = true
		}
	}
	if !foundAccepted {
		t.Fatalf("expected accepted project.index progress, got %#v", updates)
	}
}

func TestRuntimeProgressSinkEmitsModelThinking(t *testing.T) {
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

func TestRuntimeCancelledContextReturnsInterruptedReply(t *testing.T) {
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
		"file.read", "file.write", "file.delete", "project.index", "terminal.run", "web.search", "web.fetch", "secret.set",
		"schedule.create", "schedule.list", "schedule.update", "schedule.pause", "schedule.resume", "schedule.delete", "schedule.run_now",
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
	rt := newTestRuntime(t)
	runAt := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "schedule.create",
			Args: map[string]any{"run_at": runAt, "text": "say hello", "session_key": "cli:scheduled"},
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
		{ID: "pause", Name: "schedule.pause", Args: map[string]any{"id": id}},
		{ID: "resume", Name: "schedule.resume", Args: map[string]any{"id": id}},
		{ID: "run", Name: "schedule.run_now", Args: map[string]any{"id": id}},
		{ID: "update", Name: "schedule.update", Args: map[string]any{"id": id, "text": "say updated"}},
		{ID: "delete", Name: "schedule.delete", Args: map[string]any{"id": id}},
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
	rt := newTestRuntime(t)
	rt.Config.Execution.MaxIterations = intPtrTest(1)
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "project.index",
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
			Name: "project.index",
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

func TestRuntimeEmptyActionPromiseDoesNotCompleteTask(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, Content: "Confirming authorization:"},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID:   "call_1",
			Name: "project.index",
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

	prompt := appendPreviousTaskContext("Base prompt.", state, second.ID)
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
	if !strings.Contains(model.systemPrompt, "Continuity judgment:") {
		t.Fatalf("expected continuity judgment in system prompt, got %q", model.systemPrompt)
	}
	if !strings.Contains(model.systemPrompt, "create a Lark document from /tmp/source.md") ||
		!strings.Contains(model.systemPrompt, "Waiting for authorization.") {
		t.Fatalf("expected previous task context in system prompt, got %q", model.systemPrompt)
	}
	updated := loadState(t, rt, "cli:test")
	if len(updated.Tasks) != 2 || updated.Tasks[1].Goal != "ok" {
		t.Fatalf("expected soft continuation to create a new task, got %#v", updated.Tasks)
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

func intPtrTest(value int) *int {
	return &value
}

type staticTextModel struct {
	text string
}

func (m staticTextModel) Next(context.Context, agentcore.Context) (agentcore.Message, error) {
	return agentcore.Message{Role: agentcore.RoleAssistant, Content: m.text}, nil
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
	name    string
	content string
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
	return agentcore.ToolResult{ToolCallID: call.ID, Content: t.content, Evidence: map[string]any{"test": true}}
}
