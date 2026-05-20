package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/model"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/skill"
	"github.com/dongping/mateway/internal/tool"
)

type fakePlanner struct {
	plan                      model.Plan
	planCalls                 int
	repairCalls               int
	lastPlanSkillPrompt       string
	lastSynthesizeSkillPrompt string
	lastPlanUser              string
	followupDecision          model.FollowupDecision
	followupErr               error
}

func (f *fakePlanner) PlanJSON(ctx context.Context, user string, tools []tool.Definition, skillPrompt string) (model.Plan, error) {
	f.planCalls++
	f.lastPlanUser = user
	f.lastPlanSkillPrompt = skillPrompt
	return f.plan, nil
}

func (f *fakePlanner) RepairPlanJSON(ctx context.Context, user string, plan model.Plan, results []model.ToolResult, tools []tool.Definition, skillPrompt string) (model.Plan, error) {
	f.repairCalls++
	f.lastPlanSkillPrompt = skillPrompt
	return model.Plan{Summary: "repaired", Steps: []model.PlanStep{{ID: "r1", Tool: "time.now", Args: map[string]string{}}}}, nil
}

func (f *fakePlanner) Synthesize(ctx context.Context, user string, plan model.Plan, results []model.ToolResult, skillPrompt string) (string, error) {
	f.lastSynthesizeSkillPrompt = skillPrompt
	return "done", nil
}

func (f *fakePlanner) ResolveFollowupJSON(ctx context.Context, prompt string) (model.FollowupDecision, error) {
	if f.followupErr != nil {
		return model.FollowupDecision{}, f.followupErr
	}
	if strings.TrimSpace(f.followupDecision.Kind) == "" {
		return model.FollowupDecision{Kind: "new_task", ResolvedQuery: "", Confidence: 0.99}, nil
	}
	return f.followupDecision, nil
}

func TestRuntimeRepairsOnce(t *testing.T) {
	fp := &fakePlanner{plan: model.Plan{Summary: "bad", Steps: []model.PlanStep{{ID: "s1", Tool: "missing.tool", Args: map[string]string{}}}}}
	reg := tool.NewRegistry()
	tool.RegisterBuiltins(reg)
	rt := Runtime{Model: fp, Tools: reg, ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Text: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if fp.repairCalls != 1 {
		t.Fatalf("expected one repair call, got %d", fp.repairCalls)
	}
	if resp.Failed {
		t.Fatalf("expected repaired response")
	}
}

func TestRuntimeStopsForConfirmation(t *testing.T) {
	fp := &fakePlanner{plan: model.Plan{Summary: "write", Steps: []model.PlanStep{{ID: "s1", Tool: "file.write", Args: map[string]string{"path": "README.md", "content": "x"}}}}}
	reg := tool.NewRegistry()
	tool.RegisterBuiltins(reg)
	rt := Runtime{Model: fp, Tools: reg, ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Text: "write"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.AwaitConfirm {
		t.Fatalf("expected await confirm")
	}
}

func TestRuntimeIgnoresModelConfirmedArgForGuardedTool(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "out.txt")
	fp := &fakePlanner{plan: model.Plan{Summary: "write", Steps: []model.PlanStep{{
		ID: "s1", Tool: "file.write", Args: map[string]string{"path": target, "content": "x", "confirmed": "true"},
	}}}}
	rt := Runtime{Model: fp, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: root}, MaxSteps: 6}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Text: "write"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.AwaitConfirm {
		t.Fatalf("expected await confirm despite model confirmed arg, got %#v", resp)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("expected file not to be written before user approval, stat err=%v", err)
	}
}

func TestRuntimeIgnoresModelConfirmedArgForDangerousCommand(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "out.txt")
	fp := &fakePlanner{plan: model.Plan{Summary: "shell", Steps: []model.PlanStep{{
		ID: "s1", Tool: "shell.run", Args: map[string]string{"command": "echo x > out.txt", "confirmed": "true"},
	}}}}
	rt := Runtime{Model: fp, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: root}, MaxSteps: 6}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Text: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.AwaitConfirm {
		t.Fatalf("expected await confirm despite model confirmed arg, got %#v", resp)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("expected command not to write before user approval, stat err=%v", err)
	}
}

func TestRuntimeApprovalReplyExecutesConfirmedMutation(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "out.txt")
	fp := &fakePlanner{
		plan: model.Plan{Summary: "write", Steps: []model.PlanStep{{
			ID: "s1", Tool: "file.write", Args: map[string]string{"path": target, "content": "ok"},
		}}},
	}
	store := session.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err := store.Save(session.State{
		SessionKey:   "cli:cli",
		ActiveTaskID: "task-write",
		TaskOrder:    []string{"task-write"},
		Tasks: map[string]session.TaskState{
			"task-write": {
				ID:            "task-write",
				Status:        session.TaskAwaitConfirm,
				UserText:      "写文件",
				ResolvedQuery: "写文件",
				PendingApproval: &session.PendingApproval{
					ApprovalType:    "boolean_confirm",
					Prompt:          "是否写文件？",
					RequestedAction: "写文件",
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Model: fp, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: root}, MaxSteps: 6, Sessions: store}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:cli", Text: "同意"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.AwaitConfirm || resp.Failed {
		t.Fatalf("expected approved mutation to run, got %#v", resp)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "ok" {
		t.Fatalf("unexpected file content %q", data)
	}
}

type fakeGeneratorPlanner struct {
	fakePlanner
	text string
}

func (f *fakeGeneratorPlanner) Generate(ctx context.Context, system string, messages []model.Message) (string, error) {
	return f.text, nil
}

func TestRuntimeInjectsChineseSummarySkill(t *testing.T) {
	fp := &fakeGeneratorPlanner{
		fakePlanner: fakePlanner{
			plan: model.Plan{Summary: "search", Steps: []model.PlanStep{{ID: "s1", Tool: "web.search", Args: map[string]string{"query": "MiniMax"}}}},
		},
		text: "1. 这是中文总结\n来源：https://example.com",
	}
	reg := tool.NewRegistry()
	reg.Register(tool.Definition{
		Name:        "web.search",
		Description: "stub web search",
		Risk:        tool.RiskSafeRead,
		ArgsSchema:  map[string]string{"query": "search query"},
		Run: func(ctx context.Context, call tool.Call) tool.Result {
			return tool.Result{
				OK:     true,
				Output: "Search results for: MiniMax\n\nMiniMax article\nhttps://example.com\nEnglish summary",
				Evidence: map[string]any{
					"kind":         "web_search",
					"provider":     "stub",
					"query":        call.Args["query"],
					"result_count": 1,
				},
			}
		},
	})
	customSkills := skill.NewRegistry()
	customSkills.Register(skill.Definition{
		Name: "chinese-summary", Description: "zh search summary", Stage: skill.StageSynthesis, WhenUserLanguage: "zh-CN", WhenResultKinds: []string{"web_search"}, Instruction: "请输出自然中文总结",
	})
	rt := Runtime{Model: fp, Tools: reg, Skills: customSkills, ToolCtx: tool.Context{ProjectRoot: ".", Search: tool.SearchConfig{}}, MaxSteps: 6}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Text: "请搜索 MiniMax 并中文总结"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fp.lastPlanSkillPrompt, "当前日期：") || !strings.Contains(fp.lastPlanSkillPrompt, "用户时区：") {
		t.Fatalf("expected plan prompt to include date/timezone context, got %q", fp.lastPlanSkillPrompt)
	}
	if !strings.Contains(fp.lastPlanSkillPrompt, "当前环境：") || !strings.Contains(fp.lastPlanSkillPrompt, "操作系统:") {
		t.Fatalf("expected plan prompt to include environment context, got %q", fp.lastPlanSkillPrompt)
	}
	if !strings.Contains(fp.lastSynthesizeSkillPrompt, "chinese-summary") {
		t.Fatalf("expected chinese-summary to be injected, got %q", fp.lastSynthesizeSkillPrompt)
	}
	if !strings.Contains(fp.lastSynthesizeSkillPrompt, "请输出自然中文总结") {
		t.Fatalf("expected chinese summary instruction in synth prompt, got %q", fp.lastSynthesizeSkillPrompt)
	}
	if resp.Reply.Text != "done" {
		t.Fatalf("expected synthesized reply, got %q", resp.Reply.Text)
	}
}

func TestBuildModelContextPromptIncludesUserButNotHeartbeat(t *testing.T) {
	workspace := t.TempDir()
	agentDir := filepath.Join(workspace, "agents", "main")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(agentDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("soul.md", "Soul content")
	write("agent.md", "Agent content")
	write("user.md", "User content")
	write("memory.md", "Memory content")
	write("tools.md", "Tools content")
	write("heartbeat.md", "Heartbeat content")
	prompt := buildModelContextPrompt("test task", skill.StagePlanning, nil, nil, tool.Context{Workspace: workspace})
	if !strings.Contains(prompt, "user.md:\nUser content") {
		t.Fatalf("expected user.md content in prompt, got %q", prompt)
	}
	if strings.Contains(prompt, "Heartbeat content") || strings.Contains(prompt, "heartbeat.md") {
		t.Fatalf("expected heartbeat to stay out of normal prompt, got %q", prompt)
	}
}

func TestRuntimeInputRequiredDoesNotDumpToolProcess(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "ask more", Steps: []model.PlanStep{
			{ID: "s1", Tool: "time.now", Args: map[string]string{}},
			{ID: "s2", Tool: "user.ask", Args: map[string]string{"question": "你更想看国内还是全球趋势？"}},
		}},
	}
	reg := tool.NewBuiltinRegistry()
	rt := Runtime{Model: fp, Tools: reg, ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Text: "搜集现在ai应用的最新趋势和走向"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.AwaitUserInput {
		t.Fatalf("expected input required")
	}
	if resp.Reply.Text != "你更想看国内还是全球趋势？" {
		t.Fatalf("expected direct question reply, got %q", resp.Reply.Text)
	}
	if strings.Contains(resp.Reply.Text, "step-1") || strings.Contains(resp.Reply.Text, "time.now") {
		t.Fatalf("expected no tool process in reply, got %q", resp.Reply.Text)
	}
}

func TestRuntimePersistsSessionAndTaskState(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "say hi", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now", Args: map[string]string{}}}},
	}
	store := session.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	rt := Runtime{
		Model:    fp,
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{ProjectRoot: "."},
		MaxSteps: 6,
		Sessions: store,
	}
	rt.Logger.Quiet = true
	msg := channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:cli", Text: "现在几点"}
	resp, err := rt.Handle(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(resp.Reply.Text) == "" {
		t.Fatalf("expected non-empty reply")
	}
	if strings.TrimSpace(resp.TraceID) == "" {
		t.Fatalf("expected response trace id")
	}
	st, err := store.Load("cli:cli")
	if err != nil {
		t.Fatal(err)
	}
	if st.TurnCount != 1 {
		t.Fatalf("expected turn count 1, got %d", st.TurnCount)
	}
	if st.LastTask == nil {
		t.Fatalf("expected last task persisted")
	}
	if st.LastTask.Status != "completed" || st.LastTask.PlanSummary != "say hi" {
		t.Fatalf("unexpected last task %#v", st.LastTask)
	}
	if st.LastTask.ResolvedQuery != "现在几点" {
		t.Fatalf("expected resolved query persisted, got %#v", st.LastTask)
	}
	if len(st.RecentTurns) != 2 {
		t.Fatalf("expected user and assistant turns, got %#v", st.RecentTurns)
	}
}

func TestRuntimeUsesResolvedQueryForFollowup(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "followup", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now", Args: map[string]string{}}}},
		followupDecision: model.FollowupDecision{
			Kind:          "active_followup",
			TargetTaskID:  "task-ai",
			ResolvedQuery: "搜集现在AI应用的最新趋势和走向，优先最近 12 个月，输出中文总结",
			Reason:        "继续当前任务",
			Confidence:    0.91,
		},
	}
	store := session.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err := store.Save(session.State{
		SessionKey:   "cli:cli",
		ActiveTaskID: "task-ai",
		TaskOrder:    []string{"task-ai"},
		Tasks: map[string]session.TaskState{
			"task-ai": {
				ID:            "task-ai",
				UserText:      "搜集现在AI应用的最新趋势和走向",
				ResolvedQuery: "搜集现在AI应用的最新趋势和走向，优先最近 12 个月，输出中文总结",
				Topic:         "AI 应用趋势",
				Status:        session.TaskOpen,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{
		Model:    fp,
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{ProjectRoot: "."},
		MaxSteps: 6,
		Sessions: store,
	}
	rt.Logger.Quiet = true
	_, err := rt.Handle(context.Background(), channel.InboundMessage{
		Channel:    "cli",
		ThreadID:   "cli",
		UserID:     "local",
		SessionKey: "cli:cli",
		Text:       "继续",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fp.lastPlanUser != "搜集现在AI应用的最新趋势和走向，优先最近 12 个月，输出中文总结" {
		t.Fatalf("expected resolved query for planning, got %q", fp.lastPlanUser)
	}
	st, err := store.Load("cli:cli")
	if err != nil {
		t.Fatal(err)
	}
	if st.LastTask == nil || st.LastTask.ResolvedQuery != "搜集现在AI应用的最新趋势和走向，优先最近 12 个月，输出中文总结" {
		t.Fatalf("expected resolved query to remain followup-aware, got %#v", st.LastTask)
	}
}

func TestRuntimeRuleFollowupBeatsLowConfidenceModelAmbiguity(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "followup", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now", Args: map[string]string{}}}},
		followupDecision: model.FollowupDecision{
			Kind:       "ambiguous",
			Reason:     "模型低置信",
			Confidence: 0.2,
		},
	}
	store := session.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err := store.Save(session.State{
		SessionKey:   "cli:cli",
		ActiveTaskID: "task-ai",
		TaskOrder:    []string{"task-ai"},
		Tasks: map[string]session.TaskState{
			"task-ai": {
				ID:            "task-ai",
				UserText:      "搜集现在AI应用的最新趋势和走向",
				ResolvedQuery: "搜集现在AI应用的最新趋势和走向，优先最近 12 个月，输出中文总结",
				Topic:         "AI 应用趋势",
				Status:        session.TaskOpen,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{
		Model:    fp,
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{ProjectRoot: "."},
		MaxSteps: 6,
		Sessions: store,
	}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{
		Channel:    "cli",
		ThreadID:   "cli",
		UserID:     "local",
		SessionKey: "cli:cli",
		Text:       "继续上一轮",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.AwaitUserInput {
		t.Fatalf("expected rule followup to avoid clarification, got %#v", resp)
	}
	if fp.planCalls != 1 {
		t.Fatalf("expected planning to run once, got %d", fp.planCalls)
	}
	if !strings.Contains(fp.lastPlanUser, "搜集现在AI应用的最新趋势和走向，优先最近 12 个月，输出中文总结") {
		t.Fatalf("expected active task resolved query in planning input, got %q", fp.lastPlanUser)
	}
}

func TestRuntimeSlotFillKeepsTaskWaitingWhenFieldsRemain(t *testing.T) {
	fp := &fakePlanner{}
	store := session.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err := store.Save(session.State{
		SessionKey:   "cli:cli",
		ActiveTaskID: "task-ops",
		TaskOrder:    []string{"task-ops"},
		Tasks: map[string]session.TaskState{
			"task-ops": {
				ID:               "task-ops",
				Status:           session.TaskAwaitUserInput,
				UserText:         "登录远程服务器并安装 nginx",
				ResolvedQuery:    "登录远程服务器并安装 nginx",
				PendingFields:    map[string]string{"ip": "", "password": ""},
				PendingQuestions: []string{"还需要补充这些信息：ip、password"},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Model: fp, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6, Sessions: store}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:cli", Text: "ip=192.168.1.10"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.AwaitUserInput {
		t.Fatalf("expected more input required, got %#v", resp)
	}
	st, err := store.Load("cli:cli")
	if err != nil {
		t.Fatal(err)
	}
	task := st.Tasks["task-ops"]
	if _, ok := task.PendingFields["password"]; !ok {
		t.Fatalf("expected password still pending, got %#v", task.PendingFields)
	}
}

func TestRuntimeApprovalReplyReusesCurrentTask(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "install", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now", Args: map[string]string{}}}},
	}
	store := session.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err := store.Save(session.State{
		SessionKey:   "cli:cli",
		ActiveTaskID: "task-install",
		TaskOrder:    []string{"task-install"},
		Tasks: map[string]session.TaskState{
			"task-install": {
				ID:            "task-install",
				Status:        session.TaskAwaitConfirm,
				UserText:      "安装 docker",
				ResolvedQuery: "安装 docker",
				PendingApproval: &session.PendingApproval{
					ApprovalType:    "boolean_confirm",
					Prompt:          "是否安装 docker？",
					RequestedAction: "安装 docker",
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Model: fp, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6, Sessions: store}
	rt.Logger.Quiet = true
	_, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:cli", Text: "同意"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fp.lastPlanUser, "安装 docker") {
		t.Fatalf("expected approval to continue original task, got %q", fp.lastPlanUser)
	}
}

func TestRuntimeApprovalRejectionCancelsCurrentTask(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "should not run", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now", Args: map[string]string{}}}},
	}
	store := session.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err := store.Save(session.State{
		SessionKey:   "cli:cli",
		ActiveTaskID: "task-install",
		TaskOrder:    []string{"task-install"},
		Tasks: map[string]session.TaskState{
			"task-install": {
				ID:            "task-install",
				Status:        session.TaskAwaitConfirm,
				UserText:      "安装 docker",
				ResolvedQuery: "安装 docker",
				PendingApproval: &session.PendingApproval{
					ApprovalType:    "boolean_confirm",
					Prompt:          "是否安装 docker？",
					RequestedAction: "安装 docker",
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Model: fp, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6, Sessions: store}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:cli", Text: "取消"})
	if err != nil {
		t.Fatal(err)
	}
	if fp.planCalls != 0 {
		t.Fatalf("expected rejection not to plan, got %d", fp.planCalls)
	}
	if !strings.Contains(resp.Reply.Text, "取消") {
		t.Fatalf("expected cancellation reply, got %q", resp.Reply.Text)
	}
	st, err := store.Load("cli:cli")
	if err != nil {
		t.Fatal(err)
	}
	if st.Tasks["task-install"].Status != session.TaskAbandoned {
		t.Fatalf("expected task abandoned, got %#v", st.Tasks["task-install"])
	}
}

func TestRuntimeHistoricalContinuationCreatesNewTask(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "history", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now", Args: map[string]string{}}}},
		followupDecision: model.FollowupDecision{
			Kind:          "historical_continuation",
			SourceTaskID:  "task-old",
			ResolvedQuery: "继续昨天的 AI 趋势讨论，并补成文档结论",
			Reason:        "命中历史任务",
			Confidence:    0.93,
		},
	}
	store := session.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err := store.Save(session.State{
		SessionKey: "cli:cli",
		TaskOrder:  []string{"task-old"},
		Tasks: map[string]session.TaskState{
			"task-old": {
				ID:            "task-old",
				Status:        session.TaskCompleted,
				Topic:         "AI 趋势",
				ResolvedQuery: "昨天的 AI 趋势讨论",
				Artifacts:     []session.Artifact{{Kind: "file", Path: "/tmp/ai-trend.md"}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Model: fp, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6, Sessions: store}
	rt.Logger.Quiet = true
	_, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:cli", Text: "接着昨天的讨论"})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Load("cli:cli")
	if err != nil {
		t.Fatal(err)
	}
	if st.ActiveTaskID == "task-old" {
		t.Fatalf("expected a continuation task, got active old task")
	}
	task := st.Tasks[st.ActiveTaskID]
	if task.ContinuationOfTaskID != "task-old" {
		t.Fatalf("expected continuation of task-old, got %#v", task)
	}
}

func TestRuntimeHistoricalContinuationClarifiesMissingSourceTask(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "should not plan", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now", Args: map[string]string{}}}},
		followupDecision: model.FollowupDecision{
			Kind:          "historical_continuation",
			SourceTaskID:  "missing",
			ResolvedQuery: "继续昨天的讨论",
			Reason:        "命中历史任务",
			Confidence:    0.93,
		},
	}
	store := session.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err := store.Save(session.State{SessionKey: "cli:cli", Tasks: map[string]session.TaskState{}}); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Model: fp, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6, Sessions: store}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:cli", Text: "接着昨天的讨论"})
	if err != nil {
		t.Fatal(err)
	}
	if fp.planCalls != 0 {
		t.Fatalf("expected missing source task to clarify before planning, got %d", fp.planCalls)
	}
	if !resp.AwaitUserInput || !strings.Contains(resp.Reply.Text, "没找到") {
		t.Fatalf("expected clarification for missing source task, got %#v", resp)
	}
}

func TestRuntimeOpenFollowupClarifiesMissingTargetTask(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "should not plan", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now", Args: map[string]string{}}}},
		followupDecision: model.FollowupDecision{
			Kind:          "open_task_followup",
			TargetTaskID:  "missing",
			ResolvedQuery: "继续安装任务",
			Reason:        "命中 open task",
			Confidence:    0.93,
		},
	}
	store := session.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err := store.Save(session.State{SessionKey: "cli:cli", Tasks: map[string]session.TaskState{}}); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Model: fp, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6, Sessions: store}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:cli", Text: "继续安装"})
	if err != nil {
		t.Fatal(err)
	}
	if fp.planCalls != 0 {
		t.Fatalf("expected missing target task to clarify before planning, got %d", fp.planCalls)
	}
	if !resp.AwaitUserInput || !strings.Contains(resp.Reply.Text, "没找到") {
		t.Fatalf("expected clarification for missing target task, got %#v", resp)
	}
}

func TestRuntimeAmbiguousFollowupClarifies(t *testing.T) {
	fp := &fakePlanner{
		followupDecision: model.FollowupDecision{
			Kind:       "ambiguous",
			Reason:     "任务目标不明确",
			Confidence: 0.91,
		},
	}
	rt := Runtime{Model: fp, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6, Sessions: session.NewFileStore(filepath.Join(t.TempDir(), "sessions"))}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:cli", Text: "给我装一下"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.AwaitUserInput || !strings.Contains(resp.Reply.Text, "不能确定") {
		t.Fatalf("expected clarification reply, got %#v", resp)
	}
}

func TestRuntimeDirectlyAnswersArtifactFileLookup(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "should not plan", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now", Args: map[string]string{}}}},
	}
	store := session.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	yesterday := time.Now().AddDate(0, 0, -1)
	if err := store.Save(session.State{
		SessionKey: "cli:cli",
		TaskOrder:  []string{"task-doc"},
		Tasks: map[string]session.TaskState{
			"task-doc": {
				ID:            "task-doc",
				Status:        session.TaskCompleted,
				Topic:         "AI 趋势文档",
				ResolvedQuery: "整理 AI 趋势文档",
				Artifacts: []session.Artifact{{
					Kind:    "file",
					Path:    "/tmp/ai-trend.md",
					Label:   "AI 趋势文档",
					Summary: "AI 趋势文档",
				}},
				UpdatedAt: yesterday,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Model: fp, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6, Sessions: store}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:cli", Text: "昨天那个文档放哪了"})
	if err != nil {
		t.Fatal(err)
	}
	if fp.planCalls != 0 {
		t.Fatalf("expected direct artifact answer without planning, got plan calls %d", fp.planCalls)
	}
	if !strings.Contains(resp.Reply.Text, "/tmp/ai-trend.md") {
		t.Fatalf("expected artifact path in reply, got %q", resp.Reply.Text)
	}
	st, err := store.Load("cli:cli")
	if err != nil {
		t.Fatal(err)
	}
	if st.TurnCount != 1 || len(st.RecentTurns) != 2 {
		t.Fatalf("expected direct answer saved as conversation only, got %#v", st)
	}
	if st.ActiveTaskID != "task-doc" {
		t.Fatalf("expected direct answer not to create a new task, got active task %q", st.ActiveTaskID)
	}
}

func TestRuntimeDirectlyAnswersArtifactLinkLookup(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "should not plan", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now", Args: map[string]string{}}}},
	}
	store := session.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err := store.Save(session.State{
		SessionKey: "cli:cli",
		TaskOrder:  []string{"task-link"},
		Tasks: map[string]session.TaskState{
			"task-link": {
				ID:            "task-link",
				Status:        session.TaskCompleted,
				Topic:         "MiniMax 文档",
				ResolvedQuery: "搜索 MiniMax 文档",
				Artifacts: []session.Artifact{{
					Kind:      "link",
					SourceURL: "https://example.com/minimax",
					Label:     "MiniMax 文档",
					Summary:   "MiniMax 文档链接",
				}},
				UpdatedAt: time.Now().Add(-30 * time.Minute),
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Model: fp, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6, Sessions: store}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:cli", Text: "刚才那个链接给我"})
	if err != nil {
		t.Fatal(err)
	}
	if fp.planCalls != 0 {
		t.Fatalf("expected direct artifact answer without planning, got plan calls %d", fp.planCalls)
	}
	if !strings.Contains(resp.Reply.Text, "https://example.com/minimax") {
		t.Fatalf("expected artifact link in reply, got %q", resp.Reply.Text)
	}
}

func TestRuntimeDirectlyAnswersOlderReferencedArtifactLink(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "should not plan", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now", Args: map[string]string{}}}},
	}
	store := session.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err := store.Save(session.State{
		SessionKey: "cli:cli",
		TaskOrder:  []string{"task-link"},
		Tasks: map[string]session.TaskState{
			"task-link": {
				ID:            "task-link",
				Status:        session.TaskCompleted,
				Topic:         "MiniMax 文档",
				ResolvedQuery: "搜索 MiniMax 文档",
				Artifacts: []session.Artifact{{
					Kind:      "link",
					SourceURL: "https://example.com/minimax",
					Label:     "MiniMax 文档",
					Summary:   "MiniMax 文档链接",
				}},
				UpdatedAt: time.Now().AddDate(0, 0, -10),
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Model: fp, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6, Sessions: store}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:cli", Text: "上次那个链接发我"})
	if err != nil {
		t.Fatal(err)
	}
	if fp.planCalls != 0 {
		t.Fatalf("expected direct artifact answer without planning, got plan calls %d", fp.planCalls)
	}
	if !strings.Contains(resp.Reply.Text, "https://example.com/minimax") {
		t.Fatalf("expected artifact link in reply, got %q", resp.Reply.Text)
	}
}

func TestRuntimeArtifactLookupFallsBackToPlanningWithoutMatch(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "fallback", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now", Args: map[string]string{}}}},
	}
	store := session.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	rt := Runtime{Model: fp, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6, Sessions: store}
	rt.Logger.Quiet = true
	_, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:cli", Text: "昨天那个文档放哪了"})
	if err != nil {
		t.Fatal(err)
	}
	if fp.planCalls != 1 {
		t.Fatalf("expected fallback planning when no artifact matches, got %d", fp.planCalls)
	}
}

func TestRuntimeArtifactLookupDoesNotInterceptGenericDocumentRequest(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "generic doc", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now", Args: map[string]string{}}}},
	}
	store := session.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err := store.Save(session.State{
		SessionKey: "cli:cli",
		TaskOrder:  []string{"task-doc"},
		Tasks: map[string]session.TaskState{
			"task-doc": {
				ID:            "task-doc",
				Status:        session.TaskCompleted,
				Topic:         "历史文档",
				ResolvedQuery: "整理历史文档",
				Artifacts:     []session.Artifact{{Kind: "file", Path: "/tmp/history.md", Label: "历史文档"}},
				UpdatedAt:     time.Now(),
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Model: fp, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6, Sessions: store}
	rt.Logger.Quiet = true
	_, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:cli", Text: "帮我找一下文档"})
	if err != nil {
		t.Fatal(err)
	}
	if fp.planCalls != 1 {
		t.Fatalf("expected generic document request to go through planning, got %d", fp.planCalls)
	}
}

func TestCollectArtifactsExtractsSearchLinksAndQuery(t *testing.T) {
	results := []model.ToolResult{{
		Tool:   "web.search",
		Output: "Search results for: AI trends\n\n1. Top AI Trends 2026\nhttps://example.com/ai-trends\nSummary line",
		Evidence: map[string]any{
			"kind": "web_search",
		},
	}}
	artifacts := collectArtifacts(results)
	if len(artifacts) < 2 {
		t.Fatalf("expected extracted artifacts, got %#v", artifacts)
	}
	foundURL := false
	foundQuery := false
	for _, artifact := range artifacts {
		if artifact.SourceURL == "https://example.com/ai-trends" {
			foundURL = true
		}
		if artifact.Kind == "search_query" && strings.Contains(artifact.Summary, "AI trends") {
			foundQuery = true
		}
	}
	if !foundURL || !foundQuery {
		t.Fatalf("expected url and query artifacts, got %#v", artifacts)
	}
}

func TestSummarizeTasksForFollowupPrefersRelevantHistory(t *testing.T) {
	tasks := []session.TaskState{
		{ID: "a", Topic: "Docker 安装", ResolvedQuery: "安装 docker", UpdatedAt: time.Now().Add(-time.Hour)},
		{ID: "b", Topic: "AI 趋势", ResolvedQuery: "昨天的 AI 趋势讨论", Artifacts: []session.Artifact{{Kind: "file", Path: "/tmp/ai-trend.md", Summary: "AI 趋势文档"}}, UpdatedAt: time.Now()},
	}
	summaries := summarizeTasksForFollowup("昨天那个 AI 趋势文档放哪里了", tasks, "", 2)
	if len(summaries) != 2 {
		t.Fatalf("expected 2 summaries, got %#v", summaries)
	}
	if summaries[0]["id"] != "b" {
		t.Fatalf("expected AI trend task ranked first, got %#v", summaries)
	}
}

func TestDefaultSanitizerRemovesPromptEchoAndNormalizes(t *testing.T) {
	s := DefaultSanitizer{ReplyLimit: 200}
	reply := s.Sanitize(channel.OutboundMessage{
		Style: "reply",
		Text:  "Selected skills:\n- chinese-summary: desc\n请输出中文\n\nUser task:\nfoo\n\n实际结论\n\n\n第二段",
	})
	if strings.Contains(reply.Text, "Selected skills") || strings.Contains(reply.Text, "User task:") {
		t.Fatalf("expected prompt echo removed, got %q", reply.Text)
	}
	if reply.Text != "实际结论\n\n第二段" {
		t.Fatalf("unexpected sanitized reply %q", reply.Text)
	}
}

func TestDefaultSanitizerProvidesFallbackText(t *testing.T) {
	s := DefaultSanitizer{}
	reply := s.Sanitize(channel.OutboundMessage{Style: "approval_pending", Text: "   "})
	if reply.Text != "需要确认后才能继续。" {
		t.Fatalf("unexpected fallback reply %q", reply.Text)
	}
}
