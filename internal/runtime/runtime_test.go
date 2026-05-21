package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/memory"
	"github.com/dongping/mateway/internal/model"
	"github.com/dongping/mateway/internal/schedule"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/skill"
	"github.com/dongping/mateway/internal/tool"
)

type fakePlanner struct {
	plan                      model.Plan
	repairPlan                model.Plan
	planCalls                 int
	repairCalls               int
	lastPlanSkillPrompt       string
	lastSynthesizeSkillPrompt string
	lastPlanUser              string
	followupDecision          model.FollowupDecision
	followupErr               error
	followupCalls             int
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
	if strings.TrimSpace(f.repairPlan.Summary) != "" || len(f.repairPlan.Steps) > 0 {
		return f.repairPlan, nil
	}
	return model.Plan{Summary: "repaired", Steps: []model.PlanStep{{ID: "r1", Tool: "time.now", Args: map[string]string{}}}}, nil
}

func (f *fakePlanner) Synthesize(ctx context.Context, user string, plan model.Plan, results []model.ToolResult, skillPrompt string) (string, error) {
	f.lastSynthesizeSkillPrompt = skillPrompt
	return "done", nil
}

func (f *fakePlanner) ResolveFollowupJSON(ctx context.Context, prompt string) (model.FollowupDecision, error) {
	f.followupCalls++
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

func TestRuntimeCreatesSkillCandidateAfterSuccessfulPatternThreshold(t *testing.T) {
	workspace := t.TempDir()
	fp := &fakePlanner{plan: model.Plan{Summary: "review release notes", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now", Args: map[string]string{}}}}}
	reg := tool.NewRegistry()
	reg.Register(tool.TimeNow())
	rt := Runtime{
		Config: &config.Root{
			App: config.AppConfig{Workspace: workspace},
			Agents: config.AgentsConfig{
				Default:  "main",
				Profiles: []config.AgentProfileConfig{{ID: "main"}},
			},
			Learning: config.LearningConfig{
				Enabled: true,
				SkillCrystallization: config.SkillCrystallizationConfig{
					Enabled:            true,
					SuccessThreshold:   1,
					RequireUserConfirm: true,
				},
			},
		},
		Model:    fp,
		Tools:    reg,
		ToolCtx:  tool.Context{ProjectRoot: ".", Workspace: workspace},
		MaxSteps: 6,
		Sessions: session.NewFileStore(filepath.Join(workspace, "sessions")),
		Memory:   memory.NewStore(workspace),
	}
	rt.Logger.Quiet = true

	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:learning", Text: "review release notes"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Reply.Text, "proposed skill candidate") {
		t.Fatalf("expected learning prompt in reply, got %q", resp.Reply.Text)
	}
	matches, err := filepath.Glob(filepath.Join(workspace, "memory", "agents", "main", "inbox", "skill-candidate-*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected one skill candidate, got %v", matches)
	}
}

func TestRuntimeCreatesMemoryProposalAfterGroundedSuccessfulTask(t *testing.T) {
	workspace := t.TempDir()
	doc := filepath.Join(workspace, "project.md")
	if err := os.WriteFile(doc, []byte("# Project\n\nMateway uses reviewed Markdown memory proposals."), 0o644); err != nil {
		t.Fatal(err)
	}
	fp := &fakePlanner{plan: model.Plan{Summary: "read project memory", Steps: []model.PlanStep{{
		ID: "s1", Tool: "file.summary", Args: map[string]string{"path": doc},
	}}}}
	rt := Runtime{
		Config: &config.Root{
			App:    config.AppConfig{Workspace: workspace},
			Memory: config.MemoryConfig{Enabled: true},
			Agents: config.AgentsConfig{
				Default:  "main",
				Profiles: []config.AgentProfileConfig{{ID: "main"}},
			},
		},
		Model:    fp,
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{ProjectRoot: workspace, Workspace: workspace, AllowedRoots: []string{workspace}},
		MaxSteps: 6,
		Sessions: session.NewFileStore(filepath.Join(workspace, "sessions")),
		Memory:   memory.NewStore(workspace),
	}
	rt.Logger.Quiet = true
	if _, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:memory-proposal", Text: "Summarize project memory direction"}); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(workspace, "memory", "agents", "main", "inbox", "memory-proposal-*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected one memory proposal, got %v", matches)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "status: proposed") || !strings.Contains(text, "trace:") || !strings.Contains(text, "file:"+doc) {
		t.Fatalf("unexpected memory proposal:\n%s", text)
	}
	if !strings.Contains(text, "file:"+doc+":1-") {
		t.Fatalf("expected file line evidence in memory proposal:\n%s", text)
	}
}

func TestRuntimeSkipsMemoryProposalWithoutEvidence(t *testing.T) {
	workspace := t.TempDir()
	fp := &fakePlanner{plan: model.Plan{Summary: "time only", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now", Args: map[string]string{}}}}}
	rt := Runtime{
		Config: &config.Root{
			App:    config.AppConfig{Workspace: workspace},
			Memory: config.MemoryConfig{Enabled: true},
			Agents: config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}},
		},
		Model:    fp,
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{ProjectRoot: workspace, Workspace: workspace},
		MaxSteps: 6,
		Sessions: session.NewFileStore(filepath.Join(workspace, "sessions")),
		Memory:   memory.NewStore(workspace),
	}
	rt.Logger.Quiet = true
	if _, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:no-memory-proposal", Text: "What time is it?"}); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(workspace, "memory", "agents", "main", "inbox", "memory-proposal-*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected no memory proposal, got %v", matches)
	}
}

func TestRuntimeAppendsInboxReminderForPendingItems(t *testing.T) {
	workspace := t.TempDir()
	mem := memory.NewStore(workspace)
	if _, err := mem.Propose(memory.ProposalInput{
		AgentID: "main",
		Title:   "Pending Memory",
		Body:    "This proposal is waiting for review.",
		Sources: []string{"manual"},
	}); err != nil {
		t.Fatal(err)
	}
	fp := &fakePlanner{plan: model.Plan{Summary: "simple grounded task", Steps: []model.PlanStep{{
		ID: "s1", Tool: "config.summary", Args: map[string]string{},
	}}}}
	rt := Runtime{
		Config: &config.Root{
			App:    config.AppConfig{Workspace: workspace},
			Memory: config.MemoryConfig{Enabled: true},
			Agents: config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}},
		},
		Model:    fp,
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{ProjectRoot: workspace, Workspace: workspace},
		MaxSteps: 6,
		Sessions: session.NewFileStore(filepath.Join(workspace, "sessions")),
		Memory:   mem,
	}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:inbox-reminder", Text: "Show config summary"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Reply.Text, "Inbox reminder:") || !strings.Contains(resp.Reply.Text, "memory proposal") {
		t.Fatalf("expected inbox reminder, got %q", resp.Reply.Text)
	}
}

func TestRuntimeDoesNotAppendInboxReminderToControlReply(t *testing.T) {
	workspace := t.TempDir()
	mem := memory.NewStore(workspace)
	if _, err := mem.Propose(memory.ProposalInput{
		AgentID: "main",
		Title:   "Pending Memory",
		Body:    "This proposal is waiting for review.",
		Sources: []string{"manual"},
	}); err != nil {
		t.Fatal(err)
	}
	fp := &fakePlanner{plan: model.Plan{Summary: "write file", Steps: []model.PlanStep{{
		ID: "s1", Tool: "file.write", Args: map[string]string{"path": filepath.Join(workspace, "out.txt"), "content": "x"},
	}}}}
	rt := Runtime{
		Config: &config.Root{
			App:    config.AppConfig{Workspace: workspace},
			Memory: config.MemoryConfig{Enabled: true},
			Agents: config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}},
		},
		Model:    fp,
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{ProjectRoot: workspace, Workspace: workspace, AllowedRoots: []string{workspace}},
		MaxSteps: 6,
		Sessions: session.NewFileStore(filepath.Join(workspace, "sessions")),
		Memory:   mem,
	}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:inbox-control", Text: "Write file"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.AwaitConfirm {
		t.Fatalf("expected confirmation")
	}
	if strings.Contains(resp.Reply.Text, "Inbox reminder:") {
		t.Fatalf("expected no inbox reminder on control reply, got %q", resp.Reply.Text)
	}
}

func TestRuntimeRepairsUngroundedProjectSummaryBeforeSynthesis(t *testing.T) {
	root := t.TempDir()
	doc := filepath.Join(root, "docs", "测试文档.md")
	if err := os.MkdirAll(filepath.Dir(doc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(doc, []byte("# 测试文档\n\n目标：验证上下文、工具证据和安全确认。"), 0o644); err != nil {
		t.Fatal(err)
	}
	fp := &fakePlanner{
		plan: model.Plan{Summary: "generic", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now", Args: map[string]string{}}}},
		repairPlan: model.Plan{Summary: "read tests", Steps: []model.PlanStep{{
			ID: "r1", Tool: "file.summary", Args: map[string]string{"path": doc},
		}}},
	}
	rt := Runtime{Model: fp, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: root, AllowedRoots: []string{root}}, MaxSteps: 6}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Text: "请总结当前 Mateway 的测试目标，控制在两句话"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed {
		t.Fatalf("expected repaired grounded response, got %#v", resp)
	}
	if fp.repairCalls != 1 {
		t.Fatalf("expected repair for missing project evidence, got %d", fp.repairCalls)
	}
	if len(resp.Results) != 1 || resp.Results[0].Tool != "file.summary" {
		t.Fatalf("expected repaired plan to read document evidence, got %#v", resp.Results)
	}
}

func TestRuntimeBlocksUngroundedProjectSummaryAfterRepair(t *testing.T) {
	fp := &fakePlanner{
		plan:       model.Plan{Summary: "generic", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now", Args: map[string]string{}}}},
		repairPlan: model.Plan{Summary: "still generic", Steps: []model.PlanStep{{ID: "r1", Tool: "time.now", Args: map[string]string{}}}},
	}
	rt := Runtime{Model: fp, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Text: "请总结当前 Mateway 的测试目标，控制在两句话"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Failed {
		t.Fatalf("expected ungrounded project summary to fail instead of generic synthesis, got %#v", resp)
	}
	if fp.repairCalls != 1 {
		t.Fatalf("expected one repair attempt, got %d", fp.repairCalls)
	}
	if strings.Contains(resp.Reply.Text, "done") {
		t.Fatalf("expected generic synthesis to be blocked, got %q", resp.Reply.Text)
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

func TestRuntimeIgnoresModelRequiresConfirmForSafeCommand(t *testing.T) {
	fp := &fakePlanner{plan: model.Plan{Summary: "pwd", Steps: []model.PlanStep{{
		ID: "s1", Tool: "shell.run", Args: map[string]string{"command": "pwd"}, RequiresConfirm: true,
	}}}}
	rt := Runtime{Model: fp, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Text: "run pwd"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.AwaitConfirm {
		t.Fatalf("expected safe command not to await confirm, got %#v", resp)
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
	if !strings.Contains(fp.lastPlanSkillPrompt, "Current date:") || !strings.Contains(fp.lastPlanSkillPrompt, "User timezone:") {
		t.Fatalf("expected plan prompt to include date/timezone context, got %q", fp.lastPlanSkillPrompt)
	}
	if !strings.Contains(fp.lastPlanSkillPrompt, "Current environment:") || !strings.Contains(fp.lastPlanSkillPrompt, "operating_system:") {
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

func TestRuntimeInjectsShortMemoryIntoModelContext(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "continue task", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now", Args: map[string]string{}}}},
	}
	store := session.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now()
	if err := store.Save(session.State{
		SessionKey:   "cli:memory",
		Channel:      "cli",
		UserID:       "local",
		ThreadID:     "cli",
		ActiveTaskID: "task-1",
		TaskOrder:    []string{"task-1"},
		Tasks: map[string]session.TaskState{
			"task-1": {
				ID:            "task-1",
				Status:        session.TaskOpen,
				Topic:         "项目复盘",
				UserText:      "总结当前项目",
				ResolvedQuery: "总结当前项目并列出下一步",
				PlanSummary:   "read project docs",
				Artifacts: []session.Artifact{{
					Kind:  "file",
					Path:  "/tmp/report.md",
					Label: "项目复盘文档",
				}},
				UpdatedAt: now,
			},
		},
		RecentTurns: []session.Turn{
			{Role: "user", Text: "上一轮问题", At: now.Add(-time.Minute)},
			{Role: "assistant", Text: "上一轮回答", At: now},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Model: fp, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6, Sessions: store}
	rt.Logger.Quiet = true
	_, err := rt.Handle(context.Background(), channel.InboundMessage{
		Channel:    "cli",
		ThreadID:   "cli",
		UserID:     "local",
		SessionKey: "cli:memory",
		Text:       "继续",
	})
	if err != nil {
		t.Fatal(err)
	}
	prompt := fp.lastPlanSkillPrompt
	if !strings.Contains(prompt, "Short memory:") {
		t.Fatalf("expected short memory section, got %q", prompt)
	}
	if !strings.Contains(prompt, "Recent turns:") || !strings.Contains(prompt, "上一轮问题") {
		t.Fatalf("expected recent turns in short memory, got %q", prompt)
	}
	if !strings.Contains(prompt, "Active task:") || !strings.Contains(prompt, "task-1") || !strings.Contains(prompt, "项目复盘") {
		t.Fatalf("expected active task in short memory, got %q", prompt)
	}
	if !strings.Contains(prompt, "Known artifacts:") || !strings.Contains(prompt, "/tmp/report.md") {
		t.Fatalf("expected artifact summary in short memory, got %q", prompt)
	}
}

func TestRuntimeInjectsRelevantLongMemoryIntoModelContext(t *testing.T) {
	workspace := t.TempDir()
	mem := memory.NewStore(workspace)
	proposal, err := mem.Propose(memory.ProposalInput{
		AgentID: "main",
		Title:   "Mateway Memory Direction",
		Body:    "Mateway keeps durable memory in Markdown files and only commits reviewed proposals.",
		Sources: []string{"manual"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Commit(memory.CommitInput{AgentID: "main", Proposal: proposal.ID}); err != nil {
		t.Fatal(err)
	}
	fp := &fakePlanner{plan: model.Plan{Summary: "answer memory question", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now", Args: map[string]string{}}}}}
	rt := Runtime{
		Config: &config.Root{
			App:    config.AppConfig{Workspace: workspace},
			Agents: config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}},
		},
		Model:    fp,
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{ProjectRoot: ".", Workspace: workspace},
		MaxSteps: 6,
		Sessions: session.NewFileStore(filepath.Join(workspace, "sessions")),
		Memory:   mem,
	}
	rt.Logger.Quiet = true
	_, err = rt.Handle(context.Background(), channel.InboundMessage{
		Channel:    "cli",
		ThreadID:   "cli",
		UserID:     "local",
		SessionKey: "cli:long-memory",
		Text:       "How does Mateway store memory?",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fp.lastPlanSkillPrompt, "Relevant long memory:") {
		t.Fatalf("expected long memory section, got %q", fp.lastPlanSkillPrompt)
	}
	if !strings.Contains(fp.lastPlanSkillPrompt, "durable memory in Markdown files") {
		t.Fatalf("expected long memory snippet, got %q", fp.lastPlanSkillPrompt)
	}
	if !strings.Contains(fp.lastPlanSkillPrompt, "lines:") {
		t.Fatalf("expected long memory line evidence, got %q", fp.lastPlanSkillPrompt)
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

func TestRuntimeRuleFollowupStrengthensStructuralInstruction(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "followup", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now", Args: map[string]string{}}}},
	}
	store := session.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err := store.Save(session.State{
		SessionKey:   "cli:cli",
		ActiveTaskID: "task-next",
		TaskOrder:    []string{"task-next"},
		Tasks: map[string]session.TaskState{
			"task-next": {
				ID:            "task-next",
				UserText:      "总结当前下一步最值得做的一项工作",
				ResolvedQuery: "总结当前下一步最值得做的一项工作",
				Topic:         "下一步工作",
				Status:        session.TaskOpen,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Model: fp, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6, Sessions: store}
	rt.Logger.Quiet = true
	_, err := rt.Handle(context.Background(), channel.InboundMessage{
		Channel:    "cli",
		ThreadID:   "cli",
		UserID:     "local",
		SessionKey: "cli:cli",
		Text:       "继续上一轮，把刚才那项工作拆成三个可执行小步骤。",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fp.lastPlanUser, "current additional request has the highest priority") || !strings.Contains(fp.lastPlanUser, "三个可执行小步骤") {
		t.Fatalf("expected strengthened followup instruction, got %q", fp.lastPlanUser)
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
	if !strings.Contains(resp.Reply.Text, "Canceled") {
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

func TestRuntimePendingConfirmAllowsClearIndependentNewTask(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "summary", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now", Args: map[string]string{}}}},
		followupDecision: model.FollowupDecision{
			Kind:       "ambiguous",
			Reason:     "would otherwise be ambiguous",
			Confidence: 0.94,
		},
	}
	store := session.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err := store.Save(session.State{
		SessionKey:   "cli:cli",
		ActiveTaskID: "task-delete",
		TaskOrder:    []string{"task-delete"},
		Tasks: map[string]session.TaskState{
			"task-delete": {
				ID:            "task-delete",
				Status:        session.TaskAwaitConfirm,
				UserText:      "删除临时目录",
				ResolvedQuery: "删除临时目录",
				PendingApproval: &session.PendingApproval{
					ApprovalType:    "boolean_confirm",
					Prompt:          "是否删除临时目录？",
					RequestedAction: "删除临时目录",
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Model: fp, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6, Sessions: store}
	rt.Logger.Quiet = true
	msg := "请总结当前 Mateway 的测试目标，控制在两句话"
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:cli", Text: msg})
	if err != nil {
		t.Fatal(err)
	}
	if resp.AwaitUserInput {
		t.Fatalf("expected clear new request not to ask for clarification, got %#v", resp)
	}
	if fp.followupCalls != 0 {
		t.Fatalf("expected pending-confirm new task rule to avoid model followup, got %d calls", fp.followupCalls)
	}
	if fp.planCalls != 1 {
		t.Fatalf("expected new task to enter planning, got %d calls", fp.planCalls)
	}
	if fp.lastPlanUser != msg {
		t.Fatalf("expected planner to receive independent request, got %q", fp.lastPlanUser)
	}
	st, err := store.Load("cli:cli")
	if err != nil {
		t.Fatal(err)
	}
	if st.ActiveTaskID == "task-delete" {
		t.Fatalf("expected active task to switch away from pending confirmation")
	}
	if st.Tasks["task-delete"].Status != session.TaskAwaitConfirm {
		t.Fatalf("expected original pending task to remain pending but inactive, got %#v", st.Tasks["task-delete"])
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
	if !resp.AwaitUserInput || !strings.Contains(resp.Reply.Text, "could not find") {
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
	if !resp.AwaitUserInput || !strings.Contains(resp.Reply.Text, "could not find") {
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
	if !resp.AwaitUserInput || !strings.Contains(resp.Reply.Text, "not sure") {
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

func TestRuntimeScheduleRequestAsksForMissingFields(t *testing.T) {
	home := t.TempDir()
	fp := &fakePlanner{}
	rt := Runtime{
		Config:   &config.Root{App: config.AppConfig{Home: home, Workspace: home}},
		Model:    fp,
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{Home: home, Workspace: home, ProjectRoot: home},
		MaxSteps: 6,
		Sessions: session.NewFileStore(filepath.Join(home, "sessions")),
		Memory:   memory.NewStore(home),
	}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:schedule", Text: "每天帮我收集 AI 最新趋势文章"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.AwaitUserInput || resp.Reply.Style != "input_required" {
		t.Fatalf("expected input_required, got %#v", resp)
	}
	if fp.planCalls != 0 {
		t.Fatalf("expected schedule request handled before planning, got %d calls", fp.planCalls)
	}
	st, err := rt.Sessions.Load("cli:schedule")
	if err != nil {
		t.Fatal(err)
	}
	task := session.ActiveTask(st)
	if task == nil || task.Status != session.TaskAwaitUserInput {
		t.Fatalf("expected awaiting schedule task, got %#v", st)
	}
	if _, ok := task.PendingFields["daily_at"]; !ok {
		t.Fatalf("expected daily_at pending, got %#v", task.PendingFields)
	}
}

func TestRuntimeScheduleRequestCreatesProposal(t *testing.T) {
	home := t.TempDir()
	fp := &fakePlanner{}
	rt := Runtime{
		Config:   &config.Root{App: config.AppConfig{Home: home, Workspace: home}},
		Model:    fp,
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{Home: home, Workspace: home, ProjectRoot: home},
		MaxSteps: 6,
		Sessions: session.NewFileStore(filepath.Join(home, "sessions")),
		Memory:   memory.NewStore(home),
	}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:schedule-ready", Text: "每天 9点 帮我收集 AI 最新趋势文章"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.AwaitUserInput || !strings.Contains(resp.Reply.Text, "Schedule proposal written") {
		t.Fatalf("expected schedule proposal reply, got %#v", resp)
	}
	if fp.planCalls != 0 {
		t.Fatalf("expected schedule request handled before planning, got %d calls", fp.planCalls)
	}
	items, err := schedule.NewStore(home).ListProposals(schedule.ProposalStatusProposed)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Schedule != "daily@09:00" {
		t.Fatalf("expected one 09:00 proposal, got %#v", items)
	}
}

func TestRuntimeScheduleProposalApprovalEnablesTask(t *testing.T) {
	home := t.TempDir()
	fp := &fakePlanner{}
	rt := Runtime{
		Config:   &config.Root{App: config.AppConfig{Home: home, Workspace: home}},
		Model:    fp,
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{Home: home, Workspace: home, ProjectRoot: home},
		MaxSteps: 6,
		Sessions: session.NewFileStore(filepath.Join(home, "sessions")),
		Memory:   memory.NewStore(home),
	}
	rt.Logger.Quiet = true
	msg := channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:schedule-confirm", Text: "每天 9点 帮我收集 AI 最新趋势文章"}
	resp, err := rt.Handle(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.AwaitConfirm || resp.Reply.Style != "approval_pending" {
		t.Fatalf("expected approval pending, got %#v", resp)
	}
	confirm := msg
	confirm.ID = "confirm"
	confirm.Text = "好"
	resp, err = rt.Handle(context.Background(), confirm)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Reply.Text, "Schedule enabled") {
		t.Fatalf("expected enabled reply, got %q", resp.Reply.Text)
	}
	tasks, err := schedule.NewStore(home).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != schedule.StatusActive {
		t.Fatalf("expected active schedule task, got %#v", tasks)
	}
}

func TestRuntimeScheduleProposalRejectionRejectsProposal(t *testing.T) {
	home := t.TempDir()
	fp := &fakePlanner{}
	rt := Runtime{
		Config:   &config.Root{App: config.AppConfig{Home: home, Workspace: home}},
		Model:    fp,
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{Home: home, Workspace: home, ProjectRoot: home},
		MaxSteps: 6,
		Sessions: session.NewFileStore(filepath.Join(home, "sessions")),
		Memory:   memory.NewStore(home),
	}
	rt.Logger.Quiet = true
	msg := channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:schedule-reject", Text: "每天 9点 帮我收集 AI 最新趋势文章"}
	if _, err := rt.Handle(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	reject := msg
	reject.ID = "reject"
	reject.Text = "不要"
	resp, err := rt.Handle(context.Background(), reject)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Reply.Text, "rejected") {
		t.Fatalf("expected rejected reply, got %q", resp.Reply.Text)
	}
	items, err := schedule.NewStore(home).ListProposals(schedule.ProposalStatusRejected)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected rejected proposal, got %#v", items)
	}
	tasks, err := schedule.NewStore(home).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected no active task, got %#v", tasks)
	}
}

func TestRuntimeScheduleRequestCreatesWeeklyAndIntervalProposals(t *testing.T) {
	home := t.TempDir()
	fp := &fakePlanner{}
	rt := Runtime{
		Config:   &config.Root{App: config.AppConfig{Home: home, Workspace: home}},
		Model:    fp,
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{Home: home, Workspace: home, ProjectRoot: home},
		MaxSteps: 6,
		Sessions: session.NewFileStore(filepath.Join(home, "sessions")),
		Memory:   memory.NewStore(home),
	}
	rt.Logger.Quiet = true
	if _, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:schedule-weekly", Text: "每周五 9点 帮我汇总 open issues"}); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:schedule-interval", Text: "每隔2小时 帮我检查接口状态"}); err != nil {
		t.Fatal(err)
	}
	items, err := schedule.NewStore(home).ListProposals(schedule.ProposalStatusProposed)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected two proposals, got %#v", items)
	}
	foundWeekly := false
	foundInterval := false
	for _, item := range items {
		if item.Schedule == "weekly:friday@09:00" {
			foundWeekly = true
		}
		if item.Schedule == "interval:2h" {
			foundInterval = true
		}
	}
	if !foundWeekly || !foundInterval {
		t.Fatalf("expected weekly and interval proposals, got %#v", items)
	}
}

func TestRuntimeScheduleRequestCreatesWorkdayAndMonthlyProposals(t *testing.T) {
	home := t.TempDir()
	rt := Runtime{
		Config:   &config.Root{App: config.AppConfig{Home: home, Workspace: home}},
		Model:    &fakePlanner{},
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{Home: home, Workspace: home, ProjectRoot: home},
		MaxSteps: 6,
		Sessions: session.NewFileStore(filepath.Join(home, "sessions")),
		Memory:   memory.NewStore(home),
	}
	rt.Logger.Quiet = true
	if _, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:schedule-workday", Text: "工作日 9点 帮我检查 AI 趋势"}); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:schedule-monthly", Text: "每月15号 9点 帮我整理账单"}); err != nil {
		t.Fatal(err)
	}
	items, err := schedule.NewStore(home).ListProposals(schedule.ProposalStatusProposed)
	if err != nil {
		t.Fatal(err)
	}
	foundWorkday := false
	foundMonthly := false
	for _, item := range items {
		if item.Schedule == "weekly:monday,tuesday,wednesday,thursday,friday@09:00" {
			foundWorkday = true
		}
		if item.Schedule == "monthly:15@09:00" {
			foundMonthly = true
		}
	}
	if !foundWorkday || !foundMonthly {
		t.Fatalf("expected workday and monthly proposals, got %#v", items)
	}
}

func TestRuntimeScheduleDeleteRequiresConfirmation(t *testing.T) {
	home := t.TempDir()
	store := schedule.NewStore(home)
	if _, _, err := store.Create(schedule.CreateInput{ID: "ai-trends", Title: "AI Trends", Prompt: "Collect AI trends.", DailyAt: "09:00"}); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{
		Config:   &config.Root{App: config.AppConfig{Home: home, Workspace: home}},
		Model:    &fakePlanner{},
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{Home: home, Workspace: home, ProjectRoot: home},
		MaxSteps: 6,
		Sessions: session.NewFileStore(filepath.Join(home, "sessions")),
		Memory:   memory.NewStore(home),
	}
	rt.Logger.Quiet = true
	msg := channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:schedule-delete", Text: "删除 ai-trends 定时任务"}
	resp, err := rt.Handle(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.AwaitConfirm || resp.Reply.Style != "approval_pending" {
		t.Fatalf("expected approval pending, got %#v", resp)
	}
	if tasks, err := store.List(); err != nil || len(tasks) != 1 {
		t.Fatalf("expected task still present before confirmation, tasks=%#v err=%v", tasks, err)
	}
	confirm := msg
	confirm.ID = "confirm-delete"
	confirm.Text = "确认"
	resp, err = rt.Handle(context.Background(), confirm)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Reply.Text, "deleted") {
		t.Fatalf("expected deleted reply, got %q", resp.Reply.Text)
	}
	tasks, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected task deleted, got %#v", tasks)
	}
}

func TestRuntimeSchedulePauseAndResumeRequireConfirmation(t *testing.T) {
	home := t.TempDir()
	store := schedule.NewStore(home)
	if _, _, err := store.Create(schedule.CreateInput{ID: "ai-trends", Title: "AI Trends", Prompt: "Collect AI trends.", DailyAt: "09:00"}); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{
		Config:   &config.Root{App: config.AppConfig{Home: home, Workspace: home}},
		Model:    &fakePlanner{},
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{Home: home, Workspace: home, ProjectRoot: home},
		MaxSteps: 6,
		Sessions: session.NewFileStore(filepath.Join(home, "sessions")),
		Memory:   memory.NewStore(home),
	}
	rt.Logger.Quiet = true
	msg := channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:schedule-pause", Text: "暂停 ai-trends 定时任务"}
	if _, err := rt.Handle(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	confirm := msg
	confirm.ID = "confirm-pause"
	confirm.Text = "好"
	if _, err := rt.Handle(context.Background(), confirm); err != nil {
		t.Fatal(err)
	}
	task, _, err := store.Show("ai-trends")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != schedule.StatusPaused {
		t.Fatalf("expected paused, got %#v", task)
	}
	resume := msg
	resume.SessionKey = "cli:schedule-resume"
	resume.ID = "resume"
	resume.Text = "恢复 ai-trends 定时任务"
	if _, err := rt.Handle(context.Background(), resume); err != nil {
		t.Fatal(err)
	}
	confirmResume := resume
	confirmResume.ID = "confirm-resume"
	confirmResume.Text = "好"
	if _, err := rt.Handle(context.Background(), confirmResume); err != nil {
		t.Fatal(err)
	}
	task, _, err = store.Show("ai-trends")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != schedule.StatusActive {
		t.Fatalf("expected active, got %#v", task)
	}
}

func TestRuntimeScheduleUpdateRequiresConfirmation(t *testing.T) {
	home := t.TempDir()
	store := schedule.NewStore(home)
	if _, _, err := store.Create(schedule.CreateInput{ID: "ai-trends", Title: "AI Trends", Prompt: "Collect AI trends.", DailyAt: "09:00"}); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{
		Config:   &config.Root{App: config.AppConfig{Home: home, Workspace: home}},
		Model:    &fakePlanner{},
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{Home: home, Workspace: home, ProjectRoot: home},
		MaxSteps: 6,
		Sessions: session.NewFileStore(filepath.Join(home, "sessions")),
		Memory:   memory.NewStore(home),
	}
	rt.Logger.Quiet = true
	msg := channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:schedule-update", Text: "把 ai-trends 定时任务改成 10点"}
	resp, err := rt.Handle(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.AwaitConfirm || !strings.Contains(resp.Reply.Text, "daily@10:00") {
		t.Fatalf("expected update confirmation, got %#v", resp)
	}
	task, _, err := store.Show("ai-trends")
	if err != nil {
		t.Fatal(err)
	}
	if schedule.Summary(task.Schedule) != "daily@09:00" {
		t.Fatalf("expected unchanged before confirmation, got %#v", task)
	}
	confirm := msg
	confirm.ID = "confirm-update"
	confirm.Text = "确认"
	resp, err = rt.Handle(context.Background(), confirm)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Reply.Text, "updated") {
		t.Fatalf("expected updated reply, got %q", resp.Reply.Text)
	}
	task, _, err = store.Show("ai-trends")
	if err != nil {
		t.Fatal(err)
	}
	if schedule.Summary(task.Schedule) != "daily@10:00" {
		t.Fatalf("expected updated schedule, got %#v", task)
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
	if reply.Text != "Confirmation is required before continuing." {
		t.Fatalf("unexpected fallback reply %q", reply.Text)
	}
}

func TestDefaultSanitizerStripsToolCallEcho(t *testing.T) {
	s := DefaultSanitizer{}
	reply := s.Sanitize(channel.OutboundMessage{
		Style: "reply",
		Text:  "[TOOL_CALL]\n{\"tool\":\"file.read\",\"args\":{\"path\":\"README.md\"}}\n[/TOOL_CALL]\n\n这是最终结论。",
	})
	if strings.Contains(reply.Text, "TOOL_CALL") || strings.Contains(reply.Text, "file.read") {
		t.Fatalf("expected tool call echo stripped, got %q", reply.Text)
	}
	if reply.Text != "这是最终结论。" {
		t.Fatalf("unexpected reply %q", reply.Text)
	}
}

func TestDefaultSanitizerStripsMiniMaxToolCallEcho(t *testing.T) {
	s := DefaultSanitizer{}
	reply := s.Sanitize(channel.OutboundMessage{
		Style: "reply",
		Text: `<minimax:tool_call>
file.read args: {"path": "/Users/dongping/project/mateway/docs/测试文档.md"} risk: "safe_read" requires_confirm: false
<minimax:tool_call>
file.read args: {"path": "/Users/dongping/project/mateway/docs/进度.md"} risk: "safe_read" requires_confirm: false
</minimax:tool_call>

当前 Mateway 的测试目标是验证 Agent 在复杂对话中保持上下文。`,
	})
	if strings.Contains(reply.Text, "minimax:tool_call") || strings.Contains(reply.Text, "file.read") || strings.Contains(reply.Text, "requires_confirm") {
		t.Fatalf("expected minimax tool call echo stripped, got %q", reply.Text)
	}
	if !strings.Contains(reply.Text, "当前 Mateway 的测试目标") {
		t.Fatalf("expected final answer preserved, got %q", reply.Text)
	}
}

func TestDefaultSanitizerStripsBareJSONToolPlan(t *testing.T) {
	s := DefaultSanitizer{}
	reply := s.Sanitize(channel.OutboundMessage{
		Style: "reply",
		Text: `[
  {
    "id": "step-2",
    "goal": "查看测试文档内容",
    "tool": "file.read",
    "args": {"path": "/tmp/测试文档.md"},
    "risk": "safe_read",
    "requires_confirm": false
  }
]`,
	})
	if strings.Contains(reply.Text, `"tool"`) || strings.Contains(reply.Text, "file.read") {
		t.Fatalf("expected bare json tool plan stripped, got %q", reply.Text)
	}
	if reply.Text != "Done." {
		t.Fatalf("unexpected fallback %q", reply.Text)
	}
}

func TestRuntimeFailureHidesModelTransportError(t *testing.T) {
	rt := Runtime{}
	resp := rt.failure(channel.InboundMessage{Channel: "cli", ID: "x"}, nil, nil, fmt.Errorf(`plan failed: Post "https://api.minimaxi.com/anthropic/v1/messages": unexpected EOF`))
	if strings.Contains(resp.Reply.Text, "api.minimaxi.com") || strings.Contains(resp.Reply.Text, "unexpected EOF") {
		t.Fatalf("expected transport detail hidden, got %q", resp.Reply.Text)
	}
	if !strings.Contains(resp.Reply.Text, "model service") {
		t.Fatalf("expected user-facing model service error, got %q", resp.Reply.Text)
	}
}
