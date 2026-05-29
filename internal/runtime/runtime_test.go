package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/model"
)

func TestRuntimeAsk(t *testing.T) {
	cfg := &config.Root{App: config.AppConfig{Home: t.TempDir()}}
	rt := New(cfg)
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{
		ID: "1", Channel: "cli", SessionKey: "cli:test", Text: "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Text != "收到：hello" {
		t.Fatalf("reply = %q", resp.Reply.Text)
	}
}

func TestRuntimeRecordsTaskTreeForToolExecution(t *testing.T) {
	cfg := &config.Root{App: config.AppConfig{Home: t.TempDir()}, Agents: config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}}}
	rt := New(cfg)
	file := filepath.Join(t.TempDir(), "hello.txt")
	if err := os.WriteFile(file, []byte("hello file"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "1", Channel: "cli", SessionKey: "cli:test", Text: "/read " + file})
	if err != nil {
		t.Fatal(err)
	}
	state, err := rt.Store.Load("cli:test")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Tasks) != 1 {
		t.Fatalf("expected one task, got %#v", state.Tasks)
	}
	if len(state.Tasks[0].Steps) != 1 || state.Tasks[0].Steps[0].Tool != "file.read" {
		t.Fatalf("expected file.read step, got %#v", state.Tasks[0].Steps)
	}
	if state.Tasks[0].Steps[0].Status != "accepted" {
		t.Fatalf("expected accepted step, got %#v", state.Tasks[0].Steps[0])
	}
	if state.Tasks[0].Steps[0].Evidence["acceptance"] != "accepted" {
		t.Fatalf("expected acceptance evidence, got %#v", state.Tasks[0].Steps[0].Evidence)
	}
	if state.Tasks[0].Status != "completed" {
		t.Fatalf("expected completed task, got %q", state.Tasks[0].Status)
	}
}

func TestRuntimeConfirmationFollowupExecutesPendingTool(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Root{
		App:      config.AppConfig{Home: home},
		Security: config.SecurityConfig{RequireApprovalForRiskyTool: true},
		Agents:   config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}},
	}
	rt := New(cfg)
	target := filepath.Join(home, "out.txt")
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "1", Channel: "cli", SessionKey: "cli:test", Text: "/write " + target + " hi"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Style != "approval_pending" && !contains(resp.Reply.Text, "确认") {
		t.Fatalf("expected confirmation response, got %#v", resp.Reply)
	}
	state, err := rt.Store.Load("cli:test")
	if err != nil {
		t.Fatal(err)
	}
	if state.Pending == nil || state.Pending.Kind != "confirm_tool" {
		t.Fatalf("expected pending confirm tool, got %#v", state.Pending)
	}
	resp, err = rt.Handle(context.Background(), channel.InboundMessage{ID: "2", Channel: "cli", SessionKey: "cli:test", Text: "确认"})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(resp.Reply.Text, "wrote") {
		t.Fatalf("expected write result, got %#v", resp.Reply)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hi" {
		t.Fatalf("written content = %q", string(data))
	}
}

func TestRuntimeAssistantQuestionFollowupContinuesTask(t *testing.T) {
	cfg := &config.Root{App: config.AppConfig{Home: t.TempDir()}, Agents: config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}}}
	rt := New(cfg)
	rt.Pool.agents["main"] = agentcore.NewAgent(staticModel{text: "请补充主题。"}, rt.Tools)
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "1", Channel: "cli", SessionKey: "cli:test", Text: "帮我写一个报告"})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(resp.Reply.Text, "请补充") {
		t.Fatalf("expected input request, got %#v", resp.Reply)
	}
	state, _ := rt.Store.Load("cli:test")
	if state.Pending == nil || state.Pending.Kind != "user_input" {
		t.Fatalf("expected user input pending, got %#v", state.Pending)
	}
	resp, err = rt.Handle(context.Background(), channel.InboundMessage{ID: "2", Channel: "cli", SessionKey: "cli:test", Text: "天气"})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(resp.Reply.Text, "请补充") {
		t.Fatalf("expected continued response, got %#v", resp.Reply)
	}
}

func TestRuntimeSkipsConfirmationWhenSecurityAllowsRiskyTools(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Root{
		App:      config.AppConfig{Home: home},
		Security: config.SecurityConfig{RequireApprovalForRiskyTool: false},
		Agents:   config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}},
	}
	rt := New(cfg)
	target := filepath.Join(home, "out.txt")
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "1", Channel: "cli", SessionKey: "cli:test", Text: "/write " + target + " hi"})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(resp.Reply.Text, "wrote") {
		t.Fatalf("expected direct write result, got %#v", resp.Reply)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "hi" {
		t.Fatalf("write failed data=%q err=%v", string(data), err)
	}
}

func TestRuntimeStartsNewTaskAfterCompletedTask(t *testing.T) {
	cfg := &config.Root{App: config.AppConfig{Home: t.TempDir()}, Agents: config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}}}
	rt := New(cfg)
	if _, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "1", Channel: "cli", SessionKey: "cli:test", Text: "第一个任务"}); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "2", Channel: "cli", SessionKey: "cli:test", Text: "完全不同的新请求"}); err != nil {
		t.Fatal(err)
	}
	state, err := rt.Store.Load("cli:test")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Tasks) != 2 {
		t.Fatalf("expected two tasks, got %#v", state.Tasks)
	}
}

func TestRuntimeExplicitFollowupReusesRecentTask(t *testing.T) {
	cfg := &config.Root{App: config.AppConfig{Home: t.TempDir()}, Agents: config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}}}
	rt := New(cfg)
	if _, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "1", Channel: "cli", SessionKey: "cli:test", Text: "请总结 README"}); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "2", Channel: "cli", SessionKey: "cli:test", Text: "再补充一下工具"}); err != nil {
		t.Fatal(err)
	}
	state, err := rt.Store.Load("cli:test")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Tasks) != 1 {
		t.Fatalf("expected followup to reuse task, got %#v", state.Tasks)
	}
	if !contains(lastUserContent(state.Messages), "Original task: 请总结 README") {
		t.Fatalf("expected resolved followup text, got %q", lastUserContent(state.Messages))
	}
}

func TestRuntimeShortQuestionFollowupReusesCompletedTask(t *testing.T) {
	cfg := &config.Root{App: config.AppConfig{Home: t.TempDir()}, Agents: config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}}}
	rt := New(cfg)
	if _, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "1", Channel: "cli", SessionKey: "cli:test", Text: "查看北京天气"}); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "2", Channel: "cli", SessionKey: "cli:test", Text: "天津呢"}); err != nil {
		t.Fatal(err)
	}
	state, err := rt.Store.Load("cli:test")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Tasks) != 1 {
		t.Fatalf("expected short followup to reuse task, got %#v", state.Tasks)
	}
	if !contains(lastUserContent(state.Messages), "Original task: 查看北京天气") || !contains(lastUserContent(state.Messages), "Additional request: 天津呢") {
		t.Fatalf("expected resolved short followup text, got %q", lastUserContent(state.Messages))
	}
}

func TestRuntimeOrdinalFollowupReactivatesHistoricalTask(t *testing.T) {
	cfg := &config.Root{App: config.AppConfig{Home: t.TempDir()}, Agents: config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}}}
	rt := New(cfg)
	if _, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "1", Channel: "cli", SessionKey: "cli:test", Text: "请总结 README"}); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "2", Channel: "cli", SessionKey: "cli:test", Text: "完全不同的新请求"}); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "3", Channel: "cli", SessionKey: "cli:test", Text: "回到第一个任务，补充目录结构"}); err != nil {
		t.Fatal(err)
	}
	state, err := rt.Store.Load("cli:test")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Tasks) != 2 {
		t.Fatalf("expected no new task for ordinal followup, got %#v", state.Tasks)
	}
	if state.ActiveTask != state.Tasks[0].ID {
		t.Fatalf("expected active task %q, got %q", state.Tasks[0].ID, state.ActiveTask)
	}
	if !contains(lastUserContent(state.Messages), "Original task: 请总结 README") {
		t.Fatalf("expected historical resolved text, got %q", lastUserContent(state.Messages))
	}
}

func TestRuntimeAmbiguousHistoricalFollowupAsksClarify(t *testing.T) {
	cfg := &config.Root{App: config.AppConfig{Home: t.TempDir()}, Agents: config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}}}
	rt := New(cfg)
	if _, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "1", Channel: "cli", SessionKey: "cli:test", Text: "请总结 README"}); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "2", Channel: "cli", SessionKey: "cli:test", Text: "请总结 docs/总规划.md"}); err != nil {
		t.Fatal(err)
	}
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "3", Channel: "cli", SessionKey: "cli:test", Text: "回到之前那个总结任务"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Style != "clarify" {
		t.Fatalf("expected clarify response, got %#v", resp.Reply)
	}
	state, err := rt.Store.Load("cli:test")
	if err != nil {
		t.Fatal(err)
	}
	if state.Pending == nil || state.Pending.Kind != "user_input" {
		t.Fatalf("expected pending clarification, got %#v", state.Pending)
	}
}

func TestRuntimeDifferentSessionsDoNotShareFollowupContext(t *testing.T) {
	cfg := &config.Root{App: config.AppConfig{Home: t.TempDir()}, Agents: config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}}}
	rt := New(cfg)
	if _, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "1", Channel: "cli", SessionKey: "cli:a", Text: "请总结 README"}); err != nil {
		t.Fatal(err)
	}
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "2", Channel: "cli", SessionKey: "cli:b", Text: "刚才那个项目是什么"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Style != "clarify" {
		t.Fatalf("expected no cross-session context and clarify, got %#v", resp.Reply)
	}
	state, err := rt.Store.Load("cli:b")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Tasks) != 1 {
		t.Fatalf("expected only session b task, got %#v", state.Tasks)
	}
}

func TestRuntimeWritesTrace(t *testing.T) {
	cfg := &config.Root{App: config.AppConfig{Home: t.TempDir()}, Agents: config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}}}
	rt := New(cfg)
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "1", Channel: "cli", SessionKey: "cli:test", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.TraceID == "" || resp.TracePath == "" {
		t.Fatalf("expected trace info, got %#v", resp)
	}
	if _, err := os.Stat(resp.TracePath); err != nil {
		t.Fatalf("expected trace file: %v", err)
	}
	summary, err := SummarizeTrace(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Events == 0 {
		t.Fatalf("expected trace events, got %#v", summary)
	}
}

func TestRuntimeSanitizesToolCallBlocks(t *testing.T) {
	cfg := &config.Root{App: config.AppConfig{Home: t.TempDir()}, Agents: config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}}}
	rt := New(cfg)
	rt.Pool.agents["main"] = agentcore.NewAgent(staticModel{text: "before [ TOOL_CALL]\n{}\n[/ TOOL_CALL] after"}, rt.Tools)
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "1", Channel: "cli", SessionKey: "cli:test", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if contains(resp.Reply.Text, "TOOL_CALL") {
		t.Fatalf("unsanitized reply = %q", resp.Reply.Text)
	}
}

func TestRuntimeHandlesDanglingToolCallFinalText(t *testing.T) {
	cfg := &config.Root{App: config.AppConfig{Home: t.TempDir()}, Agents: config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}}}
	rt := New(cfg)
	rt.Pool.agents["main"] = agentcore.NewAgent(staticModel{text: "准备验证文件\n[TOOL_CALL]\n{\"id\":\"call_1\""}, rt.Tools)
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "1", Channel: "cli", SessionKey: "cli:test", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if contains(resp.Reply.Text, "TOOL_CALL") || !contains(resp.Reply.Text, "工具调用格式无效") {
		t.Fatalf("reply did not recover from dangling tool call: %q", resp.Reply.Text)
	}

	cfg = &config.Root{App: config.AppConfig{Home: t.TempDir()}, Agents: config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}}}
	rt = New(cfg)
	rt.Pool.agents["main"] = agentcore.NewAgent(staticModel{text: "[TOOL_CALL]\n{\"id\":\"call_1\""}, rt.Tools)
	resp, err = rt.Handle(context.Background(), channel.InboundMessage{ID: "1", Channel: "cli", SessionKey: "cli:test", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(resp.Reply.Text, "工具调用格式无效") {
		t.Fatalf("expected diagnostic fallback, got %q", resp.Reply.Text)
	}
}

func TestRuntimeDangerousTerminalCommandRequiresConfirmationEvenWhenRiskyAllowed(t *testing.T) {
	cfg := &config.Root{
		App:      config.AppConfig{Home: t.TempDir()},
		Security: config.SecurityConfig{RequireApprovalForRiskyTool: false},
		Agents:   config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}},
	}
	rt := New(cfg)
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "1", Channel: "cli", SessionKey: "cli:test", Text: "/run rm -rf /tmp/mateway-danger-test"})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(resp.Reply.Text, "破坏性") {
		t.Fatalf("expected dangerous command confirmation, got %#v", resp.Reply)
	}
}

func TestRuntimePendingConfirmationWritesTrace(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Root{
		App:      config.AppConfig{Home: home},
		Security: config.SecurityConfig{RequireApprovalForRiskyTool: true},
		Agents:   config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}},
	}
	rt := New(cfg)
	target := filepath.Join(home, "out.txt")
	if _, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "1", Channel: "cli", SessionKey: "cli:test", Text: "/write " + target + " hi"}); err != nil {
		t.Fatal(err)
	}
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "2", Channel: "cli", SessionKey: "cli:test", Text: "确认"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.TracePath == "" {
		t.Fatalf("expected pending response trace path, got %#v", resp)
	}
	data, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(data), `"type":"pending_confirmed"`) || !contains(string(data), `"type":"tool_execution_end"`) {
		t.Fatalf("expected pending/tool trace events, got %s", data)
	}
}

func TestRuntimeModelErrorReturnsFriendlyFailure(t *testing.T) {
	cfg := &config.Root{App: config.AppConfig{Home: t.TempDir()}, Agents: config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}}}
	rt := New(cfg)
	rt.Pool.agents["main"] = agentcore.NewAgent(errorModel{err: context.DeadlineExceeded}, rt.Tools)
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "1", Channel: "cli", SessionKey: "cli:test", Text: "查看天津天气"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Failed || resp.Reply.Style != "error" || contains(resp.Reply.Text, "api.minimaxi.com") || !contains(resp.Reply.Text, "超时") {
		t.Fatalf("expected friendly timeout failure, got %#v", resp)
	}
	state, err := rt.Store.Load("cli:test")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Tasks) != 1 || state.Tasks[0].Status != "failed" {
		t.Fatalf("expected failed task state, got %#v", state.Tasks)
	}
}

func TestAgentPoolPreparesConfiguredAgents(t *testing.T) {
	cfg := &config.Root{
		App:   config.AppConfig{Home: t.TempDir()},
		Model: config.ModelSelection{Default: "missing"},
		Agents: config.AgentsConfig{
			Default: "main",
			Profiles: []config.AgentProfileConfig{
				{ID: "main", Model: config.ModelSelection{Default: "missing"}},
				{ID: "ops", Model: config.ModelSelection{Default: "missing"}},
			},
			Bindings: []config.AgentBindingConfig{{Channel: "feishu", AgentID: "ops"}},
		},
	}
	pool := NewAgentPool(cfg)
	if len(pool.agents) != 2 {
		t.Fatalf("expected 2 prebuilt agents, got %d", len(pool.agents))
	}
	agent := pool.AgentForSession("feishu:thread")
	if agent == nil {
		t.Fatal("expected agent")
	}
	if _, ok := agent.Model.(HeuristicModel); !ok {
		t.Fatalf("expected fallback heuristic model, got %T", agent.Model)
	}
}

func TestRuntimeUsesPoolAgent(t *testing.T) {
	cfg := &config.Root{
		App: config.AppConfig{Home: t.TempDir()},
		Agents: config.AgentsConfig{
			Default:  "main",
			Profiles: []config.AgentProfileConfig{{ID: "main"}},
		},
	}
	rt := New(cfg)
	rt.Pool.agents["main"] = agentcore.NewAgent(staticModel{text: "from pool"}, rt.Tools)
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{
		ID: "1", Channel: "cli", SessionKey: "cli:test", Text: "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Text != "from pool" {
		t.Fatalf("reply = %q", resp.Reply.Text)
	}
}

func TestAgentPoolSkipsEnabledModelWithoutAPIKey(t *testing.T) {
	cfg := &config.Root{
		App:   config.AppConfig{Home: t.TempDir()},
		Model: config.ModelSelection{Default: "minimax"},
		Models: []config.ModelConfig{{
			Name: "minimax", API: "anthropic", APIBase: "https://example.test", Model: "demo", Enabled: true,
		}},
		Agents: config.AgentsConfig{
			Default:  "main",
			Profiles: []config.AgentProfileConfig{{ID: "main", Model: config.ModelSelection{Default: "minimax"}}},
		},
	}
	pool := NewAgentPool(cfg)
	agent := pool.AgentForSession("cli:test")
	if _, ok := agent.Model.(HeuristicModel); !ok {
		t.Fatalf("expected missing-key model to fallback heuristic, got %T", agent.Model)
	}
}

func TestAgentPoolBuildsModelFallbackChain(t *testing.T) {
	t.Setenv("MATEWAY_ONE_KEY", "one")
	t.Setenv("MATEWAY_TWO_KEY", "two")
	cfg := &config.Root{
		App:   config.AppConfig{Home: t.TempDir()},
		Model: config.ModelSelection{Default: "one", Fallbacks: []string{"two"}},
		Models: []config.ModelConfig{
			{Name: "one", API: "anthropic", APIBase: "https://one.example", Model: "one-model", APIKeyEnv: "ONE_KEY", Enabled: true},
			{Name: "two", API: "openai", APIBase: "https://two.example/v1", Model: "two-model", APIKeyEnv: "TWO_KEY", Enabled: true},
		},
		Agents: config.AgentsConfig{
			Default:  "main",
			Profiles: []config.AgentProfileConfig{{ID: "main", Model: config.ModelSelection{Default: "one", Fallbacks: []string{"two"}}}},
		},
	}
	pool := NewAgentPool(cfg)
	agent := pool.AgentForSession("cli:test")
	model, ok := agent.Model.(model.AgentModel)
	if !ok {
		t.Fatalf("expected AgentModel, got %T", agent.Model)
	}
	if model.Client.Config.Name != "one" || len(model.Fallbacks) != 1 || model.Fallbacks[0].Config.Name != "two" {
		t.Fatalf("unexpected fallback chain: %#v", model)
	}
}

func TestBuildRuntimeSystemContextIncludesEnvironmentAndWorkspaceProfile(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	agentDir := filepath.Join(workspace, "agents", "main")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "user.md"), []byte("默认使用中文。"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "memory.md"), []byte("用户偏好：回答先给结论。"), 0o644); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(workspace, "skills", "fresh-search")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: fresh-search\ndescription: Prefer fresh official sources.\nstage: planning\npriority: 8\n---\n# Fresh Search"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Root{
		App:      config.AppConfig{Home: home, Workspace: workspace},
		Security: config.SecurityConfig{EnforceWorkspacePaths: true},
		Search:   config.SearchConfig{ProviderOrder: []string{"searxng"}},
	}
	text := buildRuntimeSystemContext(cfg, config.AgentProfileConfig{ID: "main"})
	for _, want := range []string{"Runtime context:", "Current date:", "Asia/Shanghai", "Operating system:", "Executable environment:", "Task freshness policy:", "use the current date above exactly", "Connector gap policy:", "missing connector", "verification commands", "verify the required executable", "needs real-time", "Workspace profile context:", "默认使用中文", "用户偏好：回答先给结论。", "searxng", "Discovered skills:", "fresh-search", "Guidance:", "Prefer fresh official sources"} {
		if !contains(text, want) {
			t.Fatalf("context missing %q:\n%s", want, text)
		}
	}
}

func TestRuntimeContextHookFailureWritesWarningAndContinues(t *testing.T) {
	cfg := &config.Root{App: config.AppConfig{Home: t.TempDir()}, Agents: config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}}}
	rt := New(cfg)
	rt.Hooks.Providers = []HookProvider{panicHookProvider{}}
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "1", Channel: "cli", SessionKey: "cli:test", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Text != "收到：hello" {
		t.Fatalf("reply = %q", resp.Reply.Text)
	}
	data, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(data), `"type":"hook_warning"`) || !contains(string(data), `"provider":"panic_provider"`) {
		t.Fatalf("expected hook warning trace, got %s", data)
	}
}

func TestRuntimeContextHookTimeoutWritesWarningAndContinues(t *testing.T) {
	cfg := &config.Root{App: config.AppConfig{Home: t.TempDir()}, Agents: config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}}}
	rt := New(cfg)
	rt.Hooks = RuntimeHooks{Providers: []HookProvider{blockingHookProvider{}}, Timeout: time.Nanosecond}
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "1", Channel: "cli", SessionKey: "cli:test", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Text != "收到：hello" {
		t.Fatalf("reply = %q", resp.Reply.Text)
	}
	data, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(data), `"type":"hook_warning"`) || !contains(string(data), `"provider":"blocking_provider"`) {
		t.Fatalf("expected timeout hook warning trace, got %s", data)
	}
}

func TestRuntimeContextHookInjectsStaticContextAsSystemMessage(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	agentDir := filepath.Join(workspace, "agents", "main")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "memory.md"), []byte("偏好：保持简短。"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Root{
		App:    config.AppConfig{Home: home, Workspace: workspace},
		Agents: config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}},
	}
	rt := New(cfg)
	model := &captureRuntimeContextModel{}
	rt.Pool.agents["main"] = agentcore.NewAgent(model, rt.Tools)
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "1", Channel: "cli", SessionKey: "cli:test", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Text != "ok" {
		t.Fatalf("reply = %q", resp.Reply.Text)
	}
	if !contains(model.systemMessages, "static_runtime_context") || !contains(model.systemMessages, "偏好：保持简短。") {
		t.Fatalf("expected injected static context, got %q", model.systemMessages)
	}
	data, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(data), `"type":"hook_event"`) || !contains(string(data), `"sections":["static_runtime_context"]`) {
		t.Fatalf("expected context hook trace event, got %s", data)
	}
}

func TestRuntimeContextHookInjectsMemorySafeRead(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	memoryPath := filepath.Join(workspace, "memory", "agents", "main", "experience", "read-local-readme.md")
	if err := os.MkdirAll(filepath.Dir(memoryPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memoryPath, []byte(`---
type: experience
scope: agent
owner_agent: main
visibility: private
status: active
sources:
  - trace:readme
confidence: high
created_at: 2026-05-29
updated_at: 2026-05-29
schema_version: 1
---
Use file.read when inspecting local README files.
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Root{
		App:    config.AppConfig{Home: home, Workspace: workspace},
		Agents: config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}},
	}
	rt := New(cfg)
	model := &captureRuntimeContextModel{}
	rt.Pool.agents["main"] = agentcore.NewAgent(model, rt.Tools)
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "1", Channel: "cli", SessionKey: "cli:test", Text: "请总结 README"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Text != "ok" {
		t.Fatalf("reply = %q", resp.Reply.Text)
	}
	if !contains(model.systemMessages, "memory_safe_read") || !contains(model.systemMessages, "trace:readme") {
		t.Fatalf("expected memory context, got %q", model.systemMessages)
	}
	data, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(data), `"provider":"memory_safe_read"`) || !contains(string(data), `"memory_refs":["agents/main/experience/read-local-readme.md"]`) {
		t.Fatalf("expected memory refs trace, got %s", data)
	}
}

func TestRuntimeMemorySafeReadFailureContinues(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	cfg := &config.Root{
		App:    config.AppConfig{Home: home, Workspace: workspace},
		Memory: config.MemoryConfig{Root: filepath.Join(home, "missing", "memory")},
		Agents: config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}},
	}
	rt := New(cfg)
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "1", Channel: "cli", SessionKey: "cli:test", Text: "请总结 README"})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(resp.Reply.Text, "收到：请总结 README") {
		t.Fatalf("reply = %q", resp.Reply.Text)
	}
	data, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(data), `"type":"hook_warning"`) || !contains(string(data), `"provider":"memory_safe_read"`) {
		t.Fatalf("expected memory hook warning, got %s", data)
	}
}

func TestDiscoverSkillsReadsSkillFrontMatter(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	skillDir := filepath.Join(workspace, "agents", "main", "skills", "source-evaluation")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: source-evaluation\ndescription: Rank sources.\nstage: synthesis\npriority: 6\n---\n# Source Evaluation"), 0o644); err != nil {
		t.Fatal(err)
	}
	skills := discoverSkills(&config.Root{App: config.AppConfig{Home: home, Workspace: workspace}}, 10)
	if len(skills) != 1 || skills[0].Name != "source-evaluation" || skills[0].Stage != "synthesis" {
		t.Fatalf("unexpected skills %#v", skills)
	}
}

func TestDiscoverSkillsPrefersAgentSpecificSkill(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	sharedDir := filepath.Join(workspace, "skills", "research")
	agentDir := filepath.Join(workspace, "agents", "main", "skills", "research")
	if err := os.MkdirAll(sharedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sharedDir, "SKILL.md"), []byte("---\nname: research\ndescription: Shared research.\n---"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "SKILL.md"), []byte("---\nname: research\ndescription: Agent research.\n---"), 0o644); err != nil {
		t.Fatal(err)
	}
	skills := discoverSkills(&config.Root{App: config.AppConfig{Home: home, Workspace: workspace}}, 10)
	if len(skills) != 1 || skills[0].Description != "Agent research." {
		t.Fatalf("expected agent-specific skill to win, got %#v", skills)
	}
}

type staticModel struct {
	text string
}

type errorModel struct {
	err error
}

type panicHookProvider struct{}

func (panicHookProvider) Name() string { return "panic_provider" }

func (panicHookProvider) ContextHook(context.Context, ContextHookInput) (ContextHookResult, error) {
	panic("boom")
}

type blockingHookProvider struct{}

func (blockingHookProvider) Name() string { return "blocking_provider" }

func (blockingHookProvider) ContextHook(ctx context.Context, _ ContextHookInput) (ContextHookResult, error) {
	<-ctx.Done()
	return ContextHookResult{}, ctx.Err()
}

type captureRuntimeContextModel struct {
	systemMessages string
}

func contains(text, want string) bool {
	return strings.Contains(text, want)
}

func lastUserContent(messages []agentcore.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == agentcore.RoleUser {
			return messages[i].Content
		}
	}
	return ""
}

func (m staticModel) Next(context.Context, agentcore.Context) (agentcore.Message, error) {
	return agentcore.Message{Role: agentcore.RoleAssistant, Content: m.text}, nil
}

func (m errorModel) Next(context.Context, agentcore.Context) (agentcore.Message, error) {
	return agentcore.Message{}, m.err
}

func (m *captureRuntimeContextModel) Next(_ context.Context, ctx agentcore.Context) (agentcore.Message, error) {
	var parts []string
	for _, msg := range ctx.Messages {
		if msg.Role == agentcore.RoleSystem {
			parts = append(parts, msg.Content)
		}
	}
	m.systemMessages = strings.Join(parts, "\n\n")
	return agentcore.Message{Role: agentcore.RoleAssistant, Content: "ok"}, nil
}
