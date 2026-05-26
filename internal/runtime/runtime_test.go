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
	synthesizeText            string
	stepAcceptText            string
	finalAcceptText           string
	planCalls                 int
	lastPlanTools             []string
	repairCalls               int
	lastRepairTools           []string
	finalAcceptCalls          int
	firstPlanSkillPrompt      string
	lastPlanSkillPrompt       string
	lastRepairSkillPrompt     string
	lastSynthesizeSkillPrompt string
	lastPlanUser              string
	lastStepAcceptUser        string
	followupDecision          model.FollowupDecision
	followupErr               error
	followupCalls             int
}

type finalSequencePlanner struct {
	*fakePlanner
	responses []string
}

func resolveDownloadPlaceholderCommand(command, sourceURL string, ctx tool.Context) string {
	command = strings.TrimSpace(command)
	if !strings.Contains(command, "<下载URL>") {
		return command
	}
	releaseURL := strings.TrimRight(strings.TrimSpace(sourceURL), "/") + "/releases/download/v0.0.0/lark-cli_darwin-arm64.tar.gz"
	installScript := "tmpdir=$(mktemp -d) && curl -L -o \"$tmpdir/lark-cli.tar.gz\" '" + releaseURL + "' && tar -xzf \"$tmpdir/lark-cli.tar.gz\" -C \"$tmpdir\" && install \"$tmpdir/lark-cli\" '/usr/local/bin/lark'"
	return installScript
}

func TestPlanVerifierRejectsTerminalCommandWithPlaceholder(t *testing.T) {
	plan := model.Plan{Summary: "download cli", Steps: []model.PlanStep{{
		ID:   "s1",
		Tool: "terminal.run",
		Args: map[string]string{"command": "curl -L -o /usr/local/bin/lark '<下载URL>'"},
	}}}
	got := verifyPlanContract(plan, tool.NewBuiltinRegistry(), "安装 lark cli", taskUnderstanding{})
	if !got.Blocking() || !containsVerificationError(got.Errors, "unresolved download placeholder") {
		t.Fatalf("expected placeholder command error, got %#v", got)
	}
}

func (f *fakePlanner) PlanJSON(ctx context.Context, user string, tools []tool.Definition, skillPrompt string) (model.Plan, error) {
	f.planCalls++
	f.lastPlanUser = user
	if f.planCalls == 1 {
		f.firstPlanSkillPrompt = skillPrompt
	}
	f.lastPlanSkillPrompt = skillPrompt
	f.lastPlanTools = toolNames(tools)
	return f.plan, nil
}

func (f *fakePlanner) RepairPlanJSON(ctx context.Context, user string, plan model.Plan, results []model.ToolResult, tools []tool.Definition, skillPrompt string) (model.Plan, error) {
	f.repairCalls++
	f.lastPlanSkillPrompt = skillPrompt
	f.lastRepairSkillPrompt = skillPrompt
	f.lastRepairTools = toolNames(tools)
	if strings.TrimSpace(f.repairPlan.Summary) != "" || len(f.repairPlan.Steps) > 0 {
		return f.repairPlan, nil
	}
	return model.Plan{Summary: "repaired", Steps: []model.PlanStep{{ID: "r1", Tool: "time.now", Args: map[string]string{}}}}, nil
}

func (f *fakePlanner) Synthesize(ctx context.Context, user string, plan model.Plan, results []model.ToolResult, skillPrompt string) (string, error) {
	f.lastSynthesizeSkillPrompt = skillPrompt
	if strings.TrimSpace(f.synthesizeText) != "" {
		return f.synthesizeText, nil
	}
	return "done", nil
}

func (f *fakePlanner) AcceptStepJSON(ctx context.Context, user string, step model.PlanStep, result model.ToolResult) (string, error) {
	f.lastStepAcceptUser = user
	if strings.TrimSpace(f.stepAcceptText) != "" {
		return f.stepAcceptText, nil
	}
	return `{"status":"pass","reason":"ok"}`, nil
}

func (f *fakePlanner) AcceptFinalJSON(ctx context.Context, user string, plan model.Plan, results []model.ToolResult) (string, error) {
	f.finalAcceptCalls++
	if strings.TrimSpace(f.finalAcceptText) != "" {
		return f.finalAcceptText, nil
	}
	if anyFailed(results) {
		return `{"status":"rejected","reason":"step failed"}`, nil
	}
	return `{"status":"accepted","reason":"looks complete"}`, nil
}

func (f *finalSequencePlanner) AcceptFinalJSON(ctx context.Context, user string, plan model.Plan, results []model.ToolResult) (string, error) {
	f.finalAcceptCalls++
	if len(f.responses) == 0 {
		return `{"status":"accepted","reason":"looks complete"}`, nil
	}
	next := f.responses[0]
	f.responses = f.responses[1:]
	return next, nil
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

func TestRuntimeRepairPromptIncludesVerifierGuidance(t *testing.T) {
	fp := &fakePlanner{plan: model.Plan{Summary: "bad", Steps: []model.PlanStep{{ID: "s1", Tool: "file.read", Args: map[string]string{}}}}}
	reg := tool.NewRegistry()
	reg.Register(tool.FileRead())
	rt := Runtime{Model: fp, Tools: reg, ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6}
	rt.Logger.Quiet = true
	_, err := rt.Handle(context.Background(), channel.InboundMessage{Text: "请读取 README"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fp.lastPlanSkillPrompt, "Repair guidance:") || !strings.Contains(fp.lastPlanSkillPrompt, "missing required arg path") {
		t.Fatalf("expected repair guidance in repair prompt, got %q", fp.lastPlanSkillPrompt)
	}
}

func TestStepAcceptanceTaskLoadsToolSpecificAcceptanceSpec(t *testing.T) {
	def := tool.FilePatch()
	task := buildStepAcceptanceTask("编辑 README", model.PlanStep{
		ID:   "s1",
		Tool: "file.patch",
		Goal: "Patch README title",
	}, def, NewAcceptanceRegistry())
	for _, want := range []string{
		"Pass criteria:",
		"Tool acceptance prompt:",
		"replace-style patch result",
	} {
		if !strings.Contains(strings.ToLower(task), strings.ToLower(want)) {
			t.Fatalf("expected step acceptance task to contain %q, got %q", want, task)
		}
	}
}

func TestDerivedAcceptanceSpecRefSelectsPatchScenes(t *testing.T) {
	def := tool.FilePatch()
	if got := derivedAcceptanceSpecRef(model.PlanStep{Tool: "file.patch", Args: map[string]string{"append": "hello"}}, def); got != "file.patch/append" {
		t.Fatalf("expected append scene, got %q", got)
	}
	if got := derivedAcceptanceSpecRef(model.PlanStep{Tool: "file.patch", Args: map[string]string{"old": "a", "new": "b"}}, def); got != "file.patch/replace" {
		t.Fatalf("expected replace scene, got %q", got)
	}
}

func TestDerivedAcceptanceSpecRefSelectsTerminalScenes(t *testing.T) {
	def := tool.TerminalRun()
	if got := derivedAcceptanceSpecRef(model.PlanStep{Tool: "terminal.run", Goal: "run tests", Args: map[string]string{"command": "go test ./..."}}, def); got != "terminal.run/test" {
		t.Fatalf("expected test scene, got %q", got)
	}
	if got := derivedAcceptanceSpecRef(model.PlanStep{Tool: "terminal.run", Goal: "build app", Args: map[string]string{"command": "go build ./..."}}, def); got != "terminal.run/build" {
		t.Fatalf("expected build scene, got %q", got)
	}
	if got := derivedAcceptanceSpecRef(model.PlanStep{Tool: "terminal.run", Goal: "check status", Args: map[string]string{"command": "ps aux"}}, def); got != "terminal.run/diagnostic" {
		t.Fatalf("expected diagnostic scene, got %q", got)
	}
}

func TestDerivedAcceptanceSpecRefSelectsWebSearchScenes(t *testing.T) {
	def := tool.WebSearch()
	if got := derivedAcceptanceSpecRef(model.PlanStep{Tool: "web.search", Goal: "latest AI news"}, def); got != "web.search/fresh_info" {
		t.Fatalf("expected fresh scene, got %q", got)
	}
	if got := derivedAcceptanceSpecRef(model.PlanStep{Tool: "web.search", Goal: "background on AI agents"}, def); got != "web.search/background_info" {
		t.Fatalf("expected background scene, got %q", got)
	}
}

func TestCodeAcceptanceUsesRegistryForFileSummaryEvidence(t *testing.T) {
	def := tool.FileSummary()
	accept := codeAcceptStep(model.PlanStep{
		ID:               "s1",
		Tool:             "file.summary",
		ExpectedEvidence: []string{"file path"},
	}, model.ToolResult{
		StepID: "s1",
		Tool:   "file.summary",
		OK:     true,
		Output: "File summary\n\n- path: README.md",
		Evidence: map[string]any{
			"kind": "file_summary",
			"path": "README.md",
		},
	}, def, NewAcceptanceRegistry())
	if accept.Status != AcceptanceUsable || !strings.Contains(accept.Reason, "missing preview or headings evidence") {
		t.Fatalf("expected registry-based suspect acceptance, got %#v", accept)
	}
}

func TestCodeAcceptanceUsesRegistryForProjectIndexEvidence(t *testing.T) {
	def := tool.ProjectIndex()
	accept := codeAcceptStep(model.PlanStep{
		ID:   "s1",
		Tool: "project.index",
	}, model.ToolResult{
		StepID: "s1",
		Tool:   "project.index",
		OK:     true,
		Output: "Project index",
		Evidence: map[string]any{
			"kind": "project_index",
			"path": "/tmp/project",
		},
	}, def, NewAcceptanceRegistry())
	if accept.Status != AcceptanceUsable || !strings.Contains(accept.Reason, "missing project structure count evidence") {
		t.Fatalf("expected registry-based suspect for project.index, got %#v", accept)
	}
}

func TestCodeAcceptanceUsesRegistryForTerminalEvidence(t *testing.T) {
	def := tool.TerminalRun()
	accept := codeAcceptStep(model.PlanStep{
		ID:               "s1",
		Tool:             "terminal.run",
		ExpectedEvidence: []string{"exit code"},
	}, model.ToolResult{
		StepID: "s1",
		Tool:   "terminal.run",
		OK:     true,
		Output: "process seems fine",
		Evidence: map[string]any{
			"kind": "terminal",
		},
	}, def, NewAcceptanceRegistry())
	if accept.Status != AcceptanceHardFail || !strings.Contains(accept.Reason, "missing terminal execution evidence") {
		t.Fatalf("expected registry-based hard fail, got %#v", accept)
	}
}

func TestCodeAcceptanceUsesRegistryForFileReadLineEvidence(t *testing.T) {
	def := tool.FileRead()
	accept := codeAcceptStep(model.PlanStep{
		ID:               "s1",
		Tool:             "file.read",
		ExpectedEvidence: []string{"file path"},
	}, model.ToolResult{
		StepID: "s1",
		Tool:   "file.read",
		OK:     true,
		Output: "hello",
		Evidence: map[string]any{
			"kind": "file_read",
			"path": "README.md",
		},
	}, def, NewAcceptanceRegistry())
	if accept.Status != AcceptanceUsable || !strings.Contains(accept.Reason, "missing line range evidence") {
		t.Fatalf("expected registry-based suspect for file.read, got %#v", accept)
	}
}

func TestCodeAcceptanceUsesRegistryForFileWriteBytesEvidence(t *testing.T) {
	def := tool.FileWrite()
	accept := codeAcceptStep(model.PlanStep{
		ID:               "s1",
		Tool:             "file.write",
		ExpectedEvidence: []string{"file path"},
	}, model.ToolResult{
		StepID: "s1",
		Tool:   "file.write",
		OK:     true,
		Output: "wrote README.md",
		Evidence: map[string]any{
			"kind": "file_write",
			"path": "README.md",
		},
	}, def, NewAcceptanceRegistry())
	if accept.Status != AcceptanceUsable || !strings.Contains(accept.Reason, "missing bytes written evidence") {
		t.Fatalf("expected registry-based suspect for file.write, got %#v", accept)
	}
}

func TestCodeAcceptanceUsesRegistryForWebSearchEvidence(t *testing.T) {
	def := tool.WebSearch()
	accept := codeAcceptStep(model.PlanStep{
		ID:               "s1",
		Tool:             "web.search",
		ExpectedEvidence: []string{"query"},
	}, model.ToolResult{
		StepID: "s1",
		Tool:   "web.search",
		OK:     true,
		Output: "Search results",
		Evidence: map[string]any{
			"kind": "web_search",
		},
	}, def, NewAcceptanceRegistry())
	if accept.Status != AcceptanceHardFail || !strings.Contains(accept.Reason, "missing web search execution evidence") {
		t.Fatalf("expected registry-based hard fail for web.search, got %#v", accept)
	}
}

func TestCodeAcceptanceUsesRegistryForSoftwareInstallEvidence(t *testing.T) {
	def := tool.SoftwareInstall()
	accept := codeAcceptStep(model.PlanStep{
		ID:   "s1",
		Tool: "software.install",
	}, model.ToolResult{
		StepID: "s1",
		Tool:   "software.install",
		OK:     true,
		Output: "installed",
		Evidence: map[string]any{
			"kind": "software_install",
		},
	}, def, NewAcceptanceRegistry())
	if accept.Status != AcceptanceHardFail || !strings.Contains(accept.Reason, "missing software install verification evidence") {
		t.Fatalf("expected registry-based hard fail for software.install, got %#v", accept)
	}
}

func TestCodeAcceptanceUsesRegistryForSoftwareSearchEvidence(t *testing.T) {
	def := tool.SoftwareSearch()
	accept := codeAcceptStep(model.PlanStep{
		ID:   "s1",
		Tool: "software.search",
	}, model.ToolResult{
		StepID: "s1",
		Tool:   "software.search",
		OK:     true,
		Output: "results",
		Evidence: map[string]any{
			"kind": "software_search",
		},
	}, def, NewAcceptanceRegistry())
	if accept.Status != AcceptanceHardFail || !strings.Contains(accept.Reason, "missing web search execution evidence") {
		t.Fatalf("expected registry-based hard fail for software.search, got %#v", accept)
	}
}

func TestCodeAcceptanceUsesRegistryForSkillSearchEvidence(t *testing.T) {
	def := tool.SkillSearch()
	accept := codeAcceptStep(model.PlanStep{
		ID:   "s1",
		Tool: "skill.search",
	}, model.ToolResult{
		StepID: "s1",
		Tool:   "skill.search",
		OK:     true,
		Output: "results",
		Evidence: map[string]any{
			"kind": "skill_search",
		},
	}, def, NewAcceptanceRegistry())
	if accept.Status != AcceptanceHardFail || !strings.Contains(accept.Reason, "missing skill search evidence") {
		t.Fatalf("expected registry-based hard fail for skill.search, got %#v", accept)
	}
}

func TestCodeAcceptanceUsesRegistryForSkillInstallEvidence(t *testing.T) {
	def := tool.SkillInstall()
	accept := codeAcceptStep(model.PlanStep{
		ID:   "s1",
		Tool: "skill.install",
	}, model.ToolResult{
		StepID: "s1",
		Tool:   "skill.install",
		OK:     true,
		Output: "installed",
		Evidence: map[string]any{
			"kind": "skill_install",
			"name": "browser-helper",
		},
	}, def, NewAcceptanceRegistry())
	if accept.Status != AcceptanceHardFail || !strings.Contains(accept.Reason, "missing skill install evidence") {
		t.Fatalf("expected registry-based hard fail for skill.install, got %#v", accept)
	}
}

func TestCodeAcceptanceUsesRegistryForMemorySearchEvidence(t *testing.T) {
	def := tool.MemorySearch()
	accept := codeAcceptStep(model.PlanStep{
		ID:   "s1",
		Tool: "memory.search",
	}, model.ToolResult{
		StepID: "s1",
		Tool:   "memory.search",
		OK:     true,
		Output: "memory result",
		Evidence: map[string]any{
			"kind": "memory_search",
			"path": "memory.md",
		},
	}, def, NewAcceptanceRegistry())
	if accept.Status != AcceptanceUsable || !strings.Contains(accept.Reason, "missing line range evidence") {
		t.Fatalf("expected registry-based suspect for memory.search, got %#v", accept)
	}
}

func TestMemorySearchNoMatchIsUsable(t *testing.T) {
	def := tool.MemorySearch()
	accept := codeAcceptStep(model.PlanStep{
		ID:   "s1",
		Tool: "memory.search",
	}, model.ToolResult{
		StepID: "s1",
		Tool:   "memory.search",
		OK:     false,
		Error:  "no matching long memory found",
		Output: "Memory search results for: schedule\nNo matching long memory found.",
		Evidence: map[string]any{
			"kind":         "memory_search",
			"result_count": 0,
		},
	}, def, NewAcceptanceRegistry())
	if accept.Status != AcceptanceUsable {
		t.Fatalf("expected no-match memory search to be usable, got %#v", accept)
	}
}

func TestCodeAcceptanceUsesRegistryForMemoryIndexEvidence(t *testing.T) {
	def := tool.MemoryIndex()
	accept := codeAcceptStep(model.PlanStep{
		ID:   "s1",
		Tool: "memory.index",
	}, model.ToolResult{
		StepID: "s1",
		Tool:   "memory.index",
		OK:     true,
		Output: "Memory index",
		Evidence: map[string]any{
			"kind": "memory_index",
			"path": "index.json",
		},
	}, def, NewAcceptanceRegistry())
	if accept.Status != AcceptanceUsable || !strings.Contains(accept.Reason, "missing entry count evidence") {
		t.Fatalf("expected registry-based suspect for memory.index, got %#v", accept)
	}
}

func TestCodeAcceptanceUsesRegistryForScheduleCreateEvidence(t *testing.T) {
	def := tool.ScheduleCreate()
	accept := codeAcceptStep(model.PlanStep{
		ID:   "s1",
		Tool: "schedule.create",
	}, model.ToolResult{
		StepID: "s1",
		Tool:   "schedule.create",
		OK:     true,
		Output: "created",
		Evidence: map[string]any{
			"kind":    "schedule_create",
			"task_id": "daily-report",
			"status":  "active",
		},
	}, def, NewAcceptanceRegistry())
	if accept.Status != AcceptanceHardFail || !strings.Contains(accept.Reason, "missing schedule task evidence") {
		t.Fatalf("expected registry-based hard fail for schedule.create, got %#v", accept)
	}
}

func TestCodeAcceptanceUsesRegistryForScheduleListEvidence(t *testing.T) {
	def := tool.ScheduleList()
	accept := codeAcceptStep(model.PlanStep{
		ID:   "s1",
		Tool: "schedule.list",
	}, model.ToolResult{
		StepID: "s1",
		Tool:   "schedule.list",
		OK:     true,
		Output: "Schedule tasks",
		Evidence: map[string]any{
			"kind": "schedule_list",
		},
	}, def, NewAcceptanceRegistry())
	if accept.Status != AcceptanceUsable || !strings.Contains(accept.Reason, "missing task count evidence") {
		t.Fatalf("expected registry-based suspect for schedule.list, got %#v", accept)
	}
}

func TestCodeAcceptanceUsesRegistryForScheduleDeleteEvidence(t *testing.T) {
	def := tool.ScheduleDelete()
	accept := codeAcceptStep(model.PlanStep{
		ID:   "s1",
		Tool: "schedule.delete",
	}, model.ToolResult{
		StepID: "s1",
		Tool:   "schedule.delete",
		OK:     true,
		Output: "Deleted schedule task",
		Evidence: map[string]any{
			"kind":    "schedule_delete",
			"task_id": "daily-report",
		},
	}, def, NewAcceptanceRegistry())
	if accept.Status != AcceptanceHardFail || !strings.Contains(accept.Reason, "missing delete evidence") {
		t.Fatalf("expected registry-based hard fail for schedule.delete, got %#v", accept)
	}
}

func TestRuntimeLLMStepAcceptanceUsesToolSpecificSpec(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "summarize file", Understanding: model.UnderstandingJSON{RiskLevel: "safe_read"}, Steps: []model.PlanStep{{
			ID: "s1", Tool: "file.summary", Args: map[string]string{"path": "README.md"}, ExpectedEvidence: []string{"file path"},
		}}},
		stepAcceptText: `{"status":"pass","reason":"ok"}`,
	}
	reg := tool.NewRegistry()
	reg.Register(tool.Definition{
		Name:        "file.summary",
		Description: "Summarize file",
		Metadata: tool.Metadata{
			AcceptanceMode:    tool.AcceptanceCodeLLM,
			AcceptanceSpecRef: "file.summary/default",
		},
		Run: func(ctx context.Context, call tool.Call) tool.Result {
			return tool.Result{OK: true, Output: "No results found for README.md", Evidence: map[string]any{"kind": "file_summary", "path": "README.md", "headings": []string{"README"}}}
		},
	})
	rt := Runtime{Model: fp, Tools: reg, ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6, Acceptors: NewAcceptanceRegistry()}
	rt.Logger.Quiet = true
	if _, err := rt.Handle(context.Background(), channel.InboundMessage{Text: "总结 README"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Pass criteria:",
		"Tool acceptance prompt:",
		"summary reflects the requested file",
	} {
		if !strings.Contains(strings.ToLower(fp.lastStepAcceptUser), strings.ToLower(want)) {
			t.Fatalf("expected step acceptance user text to contain %q, got %q", want, fp.lastStepAcceptUser)
		}
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

func TestRuntimeSkipsTaskMemoryProposalForPlainFileSummaryRead(t *testing.T) {
	workspace := t.TempDir()
	doc := filepath.Join(workspace, "project.md")
	if err := os.WriteFile(doc, []byte("# README\n\nThis is a simple project overview."), 0o644); err != nil {
		t.Fatal(err)
	}
	fp := &fakePlanner{plan: model.Plan{Summary: "read readme", Steps: []model.PlanStep{{
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
	if _, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:no-memory-proposal-file-summary", Text: "Summarize the README"}); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(workspace, "memory", "agents", "main", "inbox", "memory-proposal-*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected no memory proposal for plain file summary read, got %v", matches)
	}
}

func TestRuntimeSkipsTaskMemoryProposalForPlainFileReadSummary(t *testing.T) {
	workspace := t.TempDir()
	doc := filepath.Join(workspace, "README.md")
	if err := os.WriteFile(doc, []byte("# README\n\nThis is a simple project overview."), 0o644); err != nil {
		t.Fatal(err)
	}
	fp := &fakePlanner{plan: model.Plan{Summary: "read readme", Steps: []model.PlanStep{{
		ID: "s1", Tool: "file.read", Args: map[string]string{"path": doc},
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
	if _, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:no-memory-proposal-file-read", Text: "Read README.md and summarize what this project can do right now."}); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(workspace, "memory", "agents", "main", "inbox", "memory-proposal-*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected no memory proposal for plain file read summary, got %v", matches)
	}
}

func TestRuntimeSkipsTaskMemoryProposalWhenSkillCandidateGenerated(t *testing.T) {
	workspace := t.TempDir()
	doc := filepath.Join(workspace, "project.md")
	if err := os.WriteFile(doc, []byte("# Project\n\nlearning input"), 0o644); err != nil {
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
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{ProjectRoot: workspace, Workspace: workspace, AllowedRoots: []string{workspace}},
		MaxSteps: 6,
		Sessions: session.NewFileStore(filepath.Join(workspace, "sessions")),
		Memory:   memory.NewStore(workspace),
	}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{
		Channel:    "cli",
		ThreadID:   "cli",
		UserID:     "local",
		SessionKey: "cli:learning-over-memory-proposal",
		Text:       "review release notes",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Reply.Text, "proposed skill candidate") {
		t.Fatalf("expected learning prompt in reply, got %q", resp.Reply.Text)
	}
	skillMatches, err := filepath.Glob(filepath.Join(workspace, "memory", "agents", "main", "inbox", "skill-candidate-*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(skillMatches) != 1 {
		t.Fatalf("expected one skill candidate, got %v", skillMatches)
	}
	memoryMatches, err := filepath.Glob(filepath.Join(workspace, "memory", "agents", "main", "inbox", "memory-proposal-*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(memoryMatches) != 0 {
		t.Fatalf("expected no task memory proposal when skill candidate is generated, got %v", memoryMatches)
	}
}

func TestRuntimeSkipsLearningForTestLikeSession(t *testing.T) {
	workspace := t.TempDir()
	reg := tool.NewBuiltinRegistry()
	fp := &fakePlanner{plan: model.Plan{Summary: "read project memory", Steps: []model.PlanStep{{
		ID: "s1", Tool: "file.summary", Args: map[string]string{"path": filepath.Join(workspace, "project.md")},
	}}}}
	if err := os.WriteFile(filepath.Join(workspace, "project.md"), []byte("# Project\n\nlearning input"), 0o644); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{
		Config: &config.Root{
			App:    config.AppConfig{Workspace: workspace},
			Memory: config.MemoryConfig{Enabled: true},
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
		ToolCtx:  tool.Context{ProjectRoot: workspace, Workspace: workspace, AllowedRoots: []string{workspace}},
		MaxSteps: 6,
		Sessions: session.NewFileStore(filepath.Join(workspace, "sessions")),
		Memory:   memory.NewStore(workspace),
	}
	rt.Logger.Quiet = true

	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "test-thread", UserID: "local", SessionKey: "test:cli-learning", Text: "review release notes"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(resp.Reply.Text, "proposed skill candidate") {
		t.Fatalf("expected no learning prompt for test-like session, got %q", resp.Reply.Text)
	}
	matches, err := filepath.Glob(filepath.Join(workspace, "memory", "agents", "main", "inbox", "skill-candidate-*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected no skill candidate for test-like session, got %v", matches)
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
	if !strings.Contains(resp.Reply.Text, "Inbox 提醒：") || !strings.Contains(resp.Reply.Text, "记忆候选") {
		t.Fatalf("expected inbox reminder, got %q", resp.Reply.Text)
	}
}

func TestRuntimeAppendsInboxReminderToNormalMutationReply(t *testing.T) {
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
	if resp.AwaitConfirm || resp.Failed {
		t.Fatalf("expected normal mutation reply, got %#v", resp)
	}
	if !strings.Contains(resp.Reply.Text, "Inbox 提醒：") {
		t.Fatalf("expected inbox reminder on normal reply, got %q", resp.Reply.Text)
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
	fp := &fakePlanner{plan: model.Plan{Summary: "ask", Steps: []model.PlanStep{{
		ID: "s1", Tool: "user.ask", Args: map[string]string{"question": "Need input"},
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
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:inbox-control-ask", Text: "Ask"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.AwaitUserInput {
		t.Fatalf("expected user input control reply, got %#v", resp)
	}
	if strings.Contains(resp.Reply.Text, "Inbox 提醒：") {
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

func TestRuntimeFileWriteRunsWithoutConfirmation(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "out.txt")
	fp := &fakePlanner{plan: model.Plan{Summary: "write", Steps: []model.PlanStep{{ID: "s1", Tool: "file.write", Args: map[string]string{"path": "README.md", "content": "x"}}}}}
	fp.plan.Steps[0].Args["path"] = target
	reg := tool.NewRegistry()
	tool.RegisterBuiltins(reg)
	rt := Runtime{Model: fp, Tools: reg, ToolCtx: tool.Context{ProjectRoot: root}, MaxSteps: 6}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Text: "write"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.AwaitConfirm || resp.Failed {
		t.Fatalf("expected write without confirmation, got %#v", resp)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "x" {
		t.Fatalf("expected written file, data=%q err=%v", data, err)
	}
}

func TestRuntimeSkillInstallReturnsCompletionWithoutConfirmation(t *testing.T) {
	fp := &fakePlanner{plan: model.Plan{Summary: "install skill", Steps: []model.PlanStep{{
		ID: "s1", Tool: "skill.install", Args: map[string]string{"name": "agent browser"},
	}}}, synthesizeText: "Skill installed: agent browser"}
	reg := tool.NewRegistry()
	reg.Register(tool.Definition{
		Name:        "skill.install",
		Description: "fake skill installer",
		Risk:        tool.RiskGuardedMutation,
		ArgsSchema:  map[string]string{"name": "skill name"},
		Run: func(ctx context.Context, call tool.Call) tool.Result {
			return tool.Result{
				OK:       true,
				Output:   "Skill installed: agent browser",
				Evidence: map[string]any{"kind": "skill_install", "name": call.Args["name"], "target_path": filepath.Join(call.Context.Workspace, "skills", "agent-browser", "SKILL.md")},
			}
		},
	})
	rt := Runtime{Model: fp, Tools: reg, ToolCtx: tool.Context{ProjectRoot: ".", Workspace: t.TempDir()}, MaxSteps: 6}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Text: "安装一下 agent browser 这个技能并测试使用"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.AwaitConfirm || resp.Failed {
		t.Fatalf("expected skill install without confirmation, got %#v", resp)
	}
	if len(resp.Results) != 1 || !resp.Results[0].OK || resp.Results[0].Tool != "skill.install" {
		t.Fatalf("expected skill install result, got %#v", resp.Results)
	}
}

func TestRuntimeIgnoresModelConfirmedArgForFileWritePolicy(t *testing.T) {
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
	if resp.AwaitConfirm || resp.Failed {
		t.Fatalf("expected file write without confirmation, got %#v", resp)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "x" {
		t.Fatalf("expected file to be written by policy, data=%q err=%v", data, err)
	}
}

func TestRuntimeIgnoresModelConfirmedArgForDestructiveCommand(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "out.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fp := &fakePlanner{plan: model.Plan{Summary: "shell", Steps: []model.PlanStep{{
		ID: "s1", Tool: "terminal.run", Args: map[string]string{"command": "rm out.txt", "confirmed": "true"},
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
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected file not to be deleted before user approval, stat err=%v", err)
	}
}

func TestRuntimeIgnoresModelRequiresConfirmForSafeCommand(t *testing.T) {
	fp := &fakePlanner{plan: model.Plan{Summary: "pwd", Steps: []model.PlanStep{{
		ID: "s1", Tool: "terminal.run", Args: map[string]string{"command": "pwd"}, RequiresConfirm: true,
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

func TestRuntimeDoesNotRewriteShellPlanByKeyword(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Mateway"), 0o644); err != nil {
		t.Fatal(err)
	}
	fp := &fakePlanner{plan: model.Plan{Summary: "project overview", Steps: []model.PlanStep{{
		ID: "s1", Goal: "run find command for directory structure", Tool: "terminal.run", Args: map[string]string{"command": "find . -maxdepth 2 -type f"}, RequiresConfirm: false,
	}}}}
	rt := Runtime{Model: fp, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: root}, MaxSteps: 6}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Text: "请运行 find . -maxdepth 2 -type f"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.AwaitConfirm {
		t.Fatalf("expected project overview to avoid shell confirmation, got %#v", resp)
	}
	if len(resp.Plan.Steps) != 1 || resp.Plan.Steps[0].Tool != "terminal.run" {
		t.Fatalf("expected original model-planned terminal.run to remain, got %#v", resp.Plan)
	}
	if len(resp.Results) != 1 || !resp.Results[0].OK || resp.Results[0].Tool != "terminal.run" {
		t.Fatalf("expected terminal.run result, got %#v", resp.Results)
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

func TestRuntimeApprovalReplyOnlyApprovesCurrentDestructiveStep(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "out.txt")
	second := filepath.Join(root, "second.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fp := &fakePlanner{
		plan: model.Plan{Summary: "delete twice", Steps: []model.PlanStep{
			{ID: "s1", Tool: "terminal.run", Args: map[string]string{"command": "rm " + shellQuoteForTest(target)}},
			{ID: "s2", Tool: "terminal.run", Args: map[string]string{"command": "rm " + shellQuoteForTest(second)}},
		}},
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
				UserText:      "删除两个文件",
				ResolvedQuery: "删除两个文件",
				PendingApproval: &session.PendingApproval{
					ApprovalType:    "boolean_confirm",
					Prompt:          "是否删除第一个文件？",
					RequestedAction: "step:s1",
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Model: fp, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: root, Workspace: root, AllowedRoots: []string{root}}, MaxSteps: 6, Sessions: store}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:cli", Text: "确认"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.AwaitConfirm {
		t.Fatalf("expected second destructive step to require confirmation, got %#v", resp)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("expected first file to be deleted, err=%v", err)
	}
	if _, err := os.Stat(second); err != nil {
		t.Fatalf("expected second file not deleted yet, err=%v", err)
	}
}

type fakeGeneratorPlanner struct {
	fakePlanner
	text string
}

func shellQuoteForTest(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
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
	if !strings.Contains(fp.lastSynthesizeSkillPrompt, "Skills context:") {
		t.Fatalf("expected structured skills context in synth prompt, got %q", fp.lastSynthesizeSkillPrompt)
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

func TestBuildModelContextPromptKeepsOnlyNonDuplicatedPlanningContext(t *testing.T) {
	prompt := buildModelContextPrompt(
		"请总结 README 并告诉我什么时候用 file.summary",
		"planning",
		[]skill.Match{{
			Definition: skill.Definition{
				Name:           "doc-review",
				Description:    "Review docs",
				UseFor:         []string{"documentation review"},
				Produces:       []string{"review summary"},
				AcceptanceMode: "llm_default",
				ParallelMode:   "forbid",
			},
			Reason: "when_contains",
		}},
		[]tool.Definition{{
			Name:        "file.summary",
			Description: "Summarize one file",
			Metadata: tool.Metadata{
				WhenToUse:      []string{"before reading full file"},
				WhenNotToUse:   []string{"editing files"},
				OutputContract: []string{"file path", "preview lines"},
				AcceptanceMode: tool.AcceptanceCodeLLM,
				ParallelMode:   tool.ParallelReadOnlyOK,
				ResourceScope:  "filesystem:path",
			},
		}},
		tool.Context{},
		promptContextOptions{
			Understanding: taskUnderstanding{
				Goal:            "请总结 README 并告诉我什么时候用 file.summary",
				Capabilities:    []string{"inspect_file"},
				CompletionDraft: []string{"summarize the relevant file content"},
				EvidenceHints:   []string{"file path, headings, preview lines"},
				RiskLevel:       "safe_read",
				NeedsGrounding:  true,
			},
		},
	)
	for _, want := range []string{
		"Task understanding:",
		"completion_draft: summarize the relevant file content",
		"evidence_hints: file path, headings, preview lines",
		"risk_level: safe_read",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected prompt to contain %q, got:\n%s", want, prompt)
		}
	}
	for _, banned := range []string{
		"Selected skills:",
		"Available tools:",
		"use_for: documentation review",
		"when_to_use: before reading full file",
		"Current user request:",
	} {
		if strings.Contains(prompt, banned) {
			t.Fatalf("expected prompt to omit duplicated prompt layer %q, got:\n%s", banned, prompt)
		}
	}
	for _, want := range []string{
		"package_managers:",
		"key_tooling:",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected planning environment summary to contain %q, got:\n%s", want, prompt)
		}
	}
}

func TestComposeCandidateToolsPrefersInstallAndSearchMatches(t *testing.T) {
	candidates := composeCandidateTools([]tool.Definition{
		{Name: "web.search", Description: "web", Metadata: tool.Metadata{WhenToUse: []string{"latest information"}}},
		{Name: "software.search", Description: "software", Metadata: tool.Metadata{WhenToUse: []string{"find install command"}}},
		{Name: "software.install", Description: "install", Metadata: tool.Metadata{WhenToUse: []string{"install cli software"}}},
		{Name: "project.index", Description: "project", Metadata: tool.Metadata{WhenToUse: []string{"repository map"}}},
	}, taskUnderstanding{
		Goal:         "帮我安装 lark cli，先找安装命令再安装",
		Capabilities: []string{"install_software", "search_web"},
	}, planningCandidateBudget)
	if len(candidates) == 0 {
		t.Fatal("expected candidates")
	}
	names := []string{candidates[0].Name}
	for _, def := range candidates[1:] {
		names = append(names, def.Name)
	}
	if !containsAll(names, []string{"software.search", "software.install"}) {
		t.Fatalf("expected install-related candidates, got %v", names)
	}
}

func TestUnderstandInfersCapabilitiesAndGrounding(t *testing.T) {
	fp := &fakePlanner{plan: model.Plan{Summary: "noop", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now", Args: map[string]string{}}}}}
	rt := Runtime{Model: fp, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6}
	rt.Logger.Quiet = true
	loop := NewAgentLoop(rt, channel.InboundMessage{Text: "请搜索最新的 lark cli 安装方式并帮我安装"})
	loop.state.resolvedQuery = "请搜索最新的 lark cli 安装方式并帮我安装"
	loop.state.understanding = loop.fallbackUnderstanding()
	if !containsAll(loop.state.understanding.Capabilities, []string{"install_software", "search_web"}) {
		t.Fatalf("expected install and search capabilities, got %v", loop.state.understanding.Capabilities)
	}
	if !loop.state.understanding.NeedsGrounding {
		t.Fatalf("expected grounding requirement")
	}
	if !loop.state.understanding.NeedsMutation {
		t.Fatalf("expected mutation requirement")
	}
	if len(loop.state.understanding.CompletionDraft) == 0 || len(loop.state.understanding.EvidenceHints) == 0 {
		t.Fatalf("expected completion draft and evidence hints, got %#v", loop.state.understanding)
	}
	if loop.state.understanding.RiskLevel != "guarded_mutation" {
		t.Fatalf("expected guarded mutation risk, got %q", loop.state.understanding.RiskLevel)
	}
}

func TestMergeUnderstandingFromPlanPrefersModelOutput(t *testing.T) {
	got := mergeUnderstandingFromPlan(model.UnderstandingJSON{
		Goal:                 "install lark cli",
		Subtasks:             []string{"find install method"},
		Constraints:          []string{"latest stable"},
		CompletionCriteria:   []string{"installed cli is verified"},
		EvidenceExpectations: []string{"install command", "verify command"},
		RiskLevel:            "guarded_mutation",
		ToolNeeds:            []string{"software.search", "software.install"},
	}, taskUnderstanding{
		Goal:            "fallback goal",
		Capabilities:    []string{"search_web"},
		EvidenceHints:   []string{"query"},
		CompletionDraft: []string{"fallback completion"},
		RiskLevel:       "safe_read",
	})
	if got.Goal != "install lark cli" || got.RiskLevel != "guarded_mutation" {
		t.Fatalf("expected model output to win, got %#v", got)
	}
	if len(got.Capabilities) != 2 || got.Capabilities[0] != "software.search" {
		t.Fatalf("expected tool needs to map into capabilities, got %#v", got)
	}
}

func TestComposeCandidateToolsPrefersProjectInspectionFromUnderstanding(t *testing.T) {
	candidates := composeCandidateTools([]tool.Definition{
		{Name: "web.search", Description: "web", Metadata: tool.Metadata{WhenToUse: []string{"latest information"}}},
		{Name: "project.index", Description: "project", Metadata: tool.Metadata{WhenToUse: []string{"repository map"}}},
		{Name: "file.summary", Description: "summary", Metadata: tool.Metadata{WhenToUse: []string{"summarize one file"}}},
		{Name: "terminal.run", Description: "terminal", Metadata: tool.Metadata{WhenToUse: []string{"run diagnostics"}}},
	}, taskUnderstanding{
		Goal:           "请概览这个仓库的结构，再看看 README",
		Capabilities:   []string{"inspect_project", "inspect_file"},
		NeedsGrounding: true,
	}, planningCandidateBudget)
	names := []string{}
	for _, def := range candidates {
		names = append(names, def.Name)
	}
	if !containsAll(names, []string{"project.index", "file.summary"}) {
		t.Fatalf("expected project/file candidates, got %v", names)
	}
}

func TestPlanningUsesCandidateToolSubset(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "project overview", Understanding: model.UnderstandingJSON{Goal: "inspect project", RiskLevel: "safe_read"}, Steps: []model.PlanStep{{
			ID: "s1", Tool: "project.index", Args: map[string]string{},
		}}},
	}
	reg := tool.NewRegistry()
	reg.Register(tool.Definition{Name: "web.search", Description: "web", Metadata: tool.Metadata{WhenToUse: []string{"latest information"}}, Risk: tool.RiskSafeRead, Run: func(ctx context.Context, call tool.Call) tool.Result {
		return tool.Result{OK: true, Output: "web", Evidence: map[string]any{"kind": "web_search"}}
	}})
	reg.Register(tool.Definition{Name: "project.index", Description: "project", Metadata: tool.Metadata{WhenToUse: []string{"repository map"}}, Risk: tool.RiskSafeRead, Run: func(ctx context.Context, call tool.Call) tool.Result {
		return tool.Result{OK: true, Output: "project", Evidence: map[string]any{"kind": "project_index"}}
	}})
	reg.Register(tool.Definition{Name: "terminal.run", Description: "terminal", Metadata: tool.Metadata{WhenToUse: []string{"diagnostics"}}, Risk: tool.RiskSafeRead, Run: func(ctx context.Context, call tool.Call) tool.Result {
		return tool.Result{OK: true, Output: "terminal", Evidence: map[string]any{"kind": "terminal"}}
	}})
	reg.Register(tool.Definition{Name: "memory.search", Description: "memory", Metadata: tool.Metadata{WhenToUse: []string{"long memory"}}, Risk: tool.RiskSafeRead, Run: func(ctx context.Context, call tool.Call) tool.Result {
		return tool.Result{OK: true, Output: "memory", Evidence: map[string]any{"kind": "memory_search"}}
	}})
	rt := Runtime{Model: fp, Tools: reg, ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6, Acceptors: NewAcceptanceRegistry()}
	rt.Logger.Quiet = true
	if _, err := rt.Handle(context.Background(), channel.InboundMessage{Text: "请概览这个项目结构"}); err != nil {
		t.Fatal(err)
	}
	if len(fp.lastPlanTools) == 0 || len(fp.lastPlanTools) > planningCandidateBudget {
		t.Fatalf("expected planning candidate subset, got %v", fp.lastPlanTools)
	}
	if !containsAll(fp.lastPlanTools, []string{"project.index"}) {
		t.Fatalf("expected project.index in planning candidates, got %v", fp.lastPlanTools)
	}
	if !strings.Contains(fp.lastPlanSkillPrompt, "Candidate tools for this request") {
		t.Fatalf("expected recommendation guidance in prompt, got %q", fp.lastPlanSkillPrompt)
	}
}

func TestBuildFinalAcceptanceTaskStaysMinimal(t *testing.T) {
	text := buildFinalAcceptanceTask("请安装 lark cli", taskUnderstanding{
		CompletionDraft: []string{"identify the install method and verify the install result"},
		EvidenceHints:   []string{"install command and verify command output"},
		RiskLevel:       "guarded_mutation",
	})
	for _, want := range []string{
		"User task: 请安装 lark cli",
		"Completion criteria: identify the install method and verify the install result",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected final acceptance task to contain %q, got %q", want, text)
		}
	}
	for _, banned := range []string{
		"Evidence hints:",
		"Risk level:",
	} {
		if strings.Contains(text, banned) {
			t.Fatalf("expected final acceptance task to stay minimal, got %q", text)
		}
	}
}

func TestBuildFinalAcceptancePromptUsesStageContext(t *testing.T) {
	text := buildFinalAcceptancePrompt("请安装 lark cli", taskUnderstanding{
		Goal:            "install lark cli safely",
		CompletionDraft: []string{"identify the install method and verify the install result"},
		RiskLevel:       "guarded_mutation",
	})
	for _, want := range []string{
		"Current stage:",
		promptStageFinalAcceptance,
		"Task understanding:",
		"Acceptance task:",
		"User task: 请安装 lark cli",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected final acceptance prompt to contain %q, got %q", want, text)
		}
	}
	if strings.Contains(text, "Current environment:") {
		t.Fatalf("expected final acceptance prompt to omit environment, got %q", text)
	}
}

func TestBuildStepAcceptancePromptUsesStageContext(t *testing.T) {
	text := buildStepAcceptancePrompt("编辑 README", model.PlanStep{
		ID:   "s1",
		Tool: "file.patch",
		Goal: "Patch README title",
	}, tool.FilePatch(), NewAcceptanceRegistry())
	for _, want := range []string{
		"Current stage:",
		promptStageStepAcceptance,
		"Acceptance task:",
		"User task: 编辑 README",
		"Step goal: Patch README title",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected step acceptance prompt to contain %q, got %q", want, text)
		}
	}
	if strings.Contains(text, "Current environment:") {
		t.Fatalf("expected step acceptance prompt to omit environment, got %q", text)
	}
}

func TestRuntimeSkipsFinalLLMAcceptanceForSimpleCodeAcceptedRead(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "time", Understanding: model.UnderstandingJSON{Goal: "get time", RiskLevel: "safe_read"}, Steps: []model.PlanStep{{
			ID: "s1", Tool: "time.now", Args: map[string]string{},
		}}},
	}
	rt := Runtime{Model: fp, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6, Acceptors: NewAcceptanceRegistry()}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Text: "现在几点"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed {
		t.Fatalf("expected success, got %#v", resp)
	}
	if fp.finalAcceptCalls != 0 {
		t.Fatalf("expected no final llm acceptance for simple read, got %d", fp.finalAcceptCalls)
	}
}

func TestRuntimeUsesFinalLLMAcceptanceForMutation(t *testing.T) {
	root := t.TempDir()
	fp := &fakePlanner{
		plan: model.Plan{Summary: "write", Understanding: model.UnderstandingJSON{Goal: "write file", RiskLevel: "guarded_mutation"}, Steps: []model.PlanStep{{
			ID: "s1", Tool: "file.write", Args: map[string]string{"path": filepath.Join(root, "out.txt"), "content": "ok"}, Risk: string(tool.RiskGuardedMutation),
		}}},
	}
	rt := Runtime{Model: fp, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: root, Workspace: root, AllowedRoots: []string{root}}, MaxSteps: 6, Acceptors: NewAcceptanceRegistry()}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Text: "写一个文件"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed {
		t.Fatalf("expected success, got %#v", resp)
	}
	if fp.finalAcceptCalls != 1 {
		t.Fatalf("expected final llm acceptance for mutation, got %d", fp.finalAcceptCalls)
	}
}

func TestRuntimeProposesSkillImprovementAfterSuccessfulRepair(t *testing.T) {
	root := t.TempDir()
	doc := filepath.Join(root, "docs", "memory.md")
	if err := os.MkdirAll(filepath.Dir(doc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(doc, []byte("# Memory\n\nMateway keeps proposals review-only before commit.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(root, "skills", "doc-review")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillText := `---
name: doc-review
description: Review docs
stage: planning
when_contains: [总结, 文档, memory]
---

Check doc evidence before summarizing.
`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillText), 0o644); err != nil {
		t.Fatal(err)
	}
	fp := &fakePlanner{
		plan: model.Plan{
			Summary: "generic",
			Steps: []model.PlanStep{{
				ID:   "s1",
				Tool: "time.now",
				Args: map[string]string{},
			}},
		},
		repairPlan: model.Plan{
			Summary: "read doc, verify evidence, then summarize",
			Steps: []model.PlanStep{{
				ID:   "s1",
				Tool: "file.summary",
				Args: map[string]string{"path": doc},
			}},
		},
		synthesizeText: "done",
	}
	rt := Runtime{
		Model:     fp,
		Tools:     tool.NewBuiltinRegistry(),
		Skills:    skill.NewBuiltinRegistry(),
		ToolCtx:   tool.Context{ProjectRoot: root, Workspace: root, AllowedRoots: []string{root}},
		MaxSteps:  6,
		Acceptors: NewAcceptanceRegistry(),
		Config: &config.Root{
			App:    config.AppConfig{Workspace: root},
			Agents: config.AgentsConfig{Default: "main"},
			Learning: config.LearningConfig{
				Enabled:              true,
				SkillCrystallization: config.SkillCrystallizationConfig{Enabled: true, SuccessThreshold: 3, RequireUserConfirm: true},
			},
		},
		Sessions: session.NewFileStore(filepath.Join(root, "run", "sessions")),
		Memory:   memory.NewStore(root),
	}
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{
		SessionKey: "cli:skill-improve",
		Channel:    "cli",
		Text:       "请总结当前 memory 设计，控制在两句话",
	})
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if resp.Failed {
		t.Fatalf("expected successful repaired response, got %#v", resp)
	}
	matches, err := filepath.Glob(filepath.Join(root, "memory", "agents", "main", "inbox", "skill-improvement-*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected one skill improvement proposal, got %v", matches)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"type: skill_improvement",
		"# Proposed Skill Improvement: doc-review",
		"Repair reason:",
		"Previous plan summary: generic",
		"Repaired plan summary: read doc, verify evidence, then summarize",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected skill improvement proposal to contain %q, got:\n%s", want, text)
		}
	}
}

func TestDirectSkillPromoteCommand(t *testing.T) {
	workspace := t.TempDir()
	store := memory.NewStore(workspace)
	cfg := memory.LearningConfig{Enabled: true, SuccessThreshold: 1, RequireUserConfirm: true}
	path, err := store.ProcessTask(memory.TaskOutcome{
		AgentID:     "main",
		TraceID:     "trace-1",
		TaskID:      "task-1",
		Intent:      "review latest release notes",
		PlanSummary: "review release notes",
		Tools:       []string{"web.search", "file.summary"},
		Success:     true,
		FinishedAt:  time.Now(),
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	rt := Runtime{
		Model:    &fakePlanner{},
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{ProjectRoot: workspace, Workspace: workspace, AllowedRoots: []string{workspace}},
		MaxSteps: 6,
		Memory:   store,
	}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{
		SessionKey: "cli:skill-promote",
		Channel:    "cli",
		Text:       "mateway skill promote --proposal " + path.CandidatePath + " --name release-review",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed || !strings.Contains(resp.Reply.Text, "Skill promoted:") || !strings.Contains(resp.Reply.Text, "next planning turn") {
		t.Fatalf("expected direct skill promote success, got %#v", resp)
	}
	if _, err := os.Stat(filepath.Join(workspace, "skills", "release-review", "SKILL.md")); err != nil {
		t.Fatalf("expected promoted skill file: %v", err)
	}
}

func TestDirectMemoryReviewCommand(t *testing.T) {
	workspace := t.TempDir()
	longDir := filepath.Join(workspace, "memory", "agents", "main", "long")
	if err := os.MkdirAll(longDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := `---
type: decision
scope: agent
status: active
sources:
  - manual
confidence: medium
created_at: 2026-03-01
updated_at: 2026-03-01
---

# Stale Memory

Old decision note.
`
	if err := os.WriteFile(filepath.Join(longDir, "decision-stale-memory.md"), []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{
		Model:    &fakePlanner{},
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{ProjectRoot: workspace, Workspace: workspace, AllowedRoots: []string{workspace}},
		MaxSteps: 6,
		Memory:   memory.NewStore(workspace),
	}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{
		SessionKey: "cli:memory-review",
		Channel:    "cli",
		Text:       "mateway memory review --review stale --kind decision",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed || !strings.Contains(resp.Reply.Text, "Long memory review queue") || !strings.Contains(resp.Reply.Text, "Stale Memory") {
		t.Fatalf("expected direct memory review success, got %#v", resp)
	}
	if !strings.Contains(resp.Reply.Text, "下一步：") || !strings.Contains(resp.Reply.Text, "--proposal") {
		t.Fatalf("expected review next-step hint, got %q", resp.Reply.Text)
	}

	resp, err = rt.Handle(context.Background(), channel.InboundMessage{
		SessionKey: "cli:memory-review-proposal",
		Channel:    "cli",
		Text:       "mateway memory review --review stale --proposal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed || !strings.Contains(resp.Reply.Text, "Long memory review proposal written:") {
		t.Fatalf("expected direct memory review proposal success, got %#v", resp)
	}
	if !strings.Contains(resp.Reply.Text, "mateway memory list --area inbox --status proposed") {
		t.Fatalf("expected proposal follow-up hint, got %q", resp.Reply.Text)
	}
}

func TestDirectMemoryHelpCommand(t *testing.T) {
	rt := Runtime{
		Model:    &fakePlanner{},
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{ProjectRoot: ".", Workspace: t.TempDir()},
		MaxSteps: 6,
	}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{
		SessionKey: "cli:memory-help",
		Channel:    "cli",
		Text:       "mateway memory",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed || !strings.Contains(resp.Reply.Text, "第一批一等 memory 直达命令") {
		t.Fatalf("expected memory help output, got %#v", resp)
	}
	if !strings.Contains(resp.Reply.Text, "命令帮助：`mateway memory`") {
		t.Fatalf("expected help header, got %q", resp.Reply.Text)
	}
	if !strings.Contains(resp.Reply.Text, "commit`、`reject` 会修改 memory 状态") {
		t.Fatalf("expected confirmation boundary in help, got %q", resp.Reply.Text)
	}
}

func TestDirectMemoryErrorUsesUnfinishedHeader(t *testing.T) {
	rt := Runtime{
		Model:    &fakePlanner{},
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{ProjectRoot: ".", Workspace: t.TempDir()},
		MaxSteps: 6,
	}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{
		SessionKey: "cli:memory-error",
		Channel:    "cli",
		Text:       "mateway memory show",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed || !strings.Contains(resp.Reply.Text, "命令未完成：`mateway memory show`") {
		t.Fatalf("expected unfinished command header, got %#v", resp)
	}
	if strings.Contains(resp.Reply.Text, "下一步：") {
		t.Fatalf("expected usage error to omit next-step hint, got %q", resp.Reply.Text)
	}
}

func TestDirectMemoryUnknownSubcommandDoesNotFallBackToPlanner(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "should not plan", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now"}}},
	}
	rt := Runtime{
		Model:    fp,
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{ProjectRoot: ".", Workspace: t.TempDir()},
		MaxSteps: 6,
	}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{
		SessionKey: "cli:memory-unknown",
		Channel:    "cli",
		Text:       "mateway memory wat",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fp.planCalls != 0 {
		t.Fatalf("expected unknown direct command not to call planner, got %d", fp.planCalls)
	}
	if resp.Failed || !strings.Contains(resp.Reply.Text, "命令未完成：`mateway memory wat`") || !strings.Contains(resp.Reply.Text, "unknown memory command") {
		t.Fatalf("expected direct unknown command error, got %#v", resp)
	}
}

func TestDirectKnownNamespacesDoNotFallBackToPlannerOnUsageErrors(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "should not plan", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now"}}},
	}
	rt := Runtime{
		Model:    fp,
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{ProjectRoot: ".", Home: t.TempDir(), Workspace: t.TempDir()},
		MaxSteps: 6,
	}
	rt.Logger.Quiet = true
	cases := []struct {
		text string
		want string
	}{
		{text: "mateway schedule wat", want: "unknown schedule command"},
		{text: "mateway heartbeat wat", want: "unknown heartbeat command"},
		{text: "mateway trace", want: "用法：`mateway trace show <trace_id> [--raw]`"},
	}
	for _, tc := range cases {
		resp, err := rt.Handle(context.Background(), channel.InboundMessage{
			SessionKey: "cli:known-namespace-" + strings.ReplaceAll(tc.text, " ", "-"),
			Channel:    "cli",
			Text:       tc.text,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(resp.Reply.Text, tc.want) {
			t.Fatalf("expected %q for %q, got %q", tc.want, tc.text, resp.Reply.Text)
		}
	}
	if fp.planCalls != 0 {
		t.Fatalf("expected known namespace usage errors not to call planner, got %d", fp.planCalls)
	}
}

func TestDirectTopLevelMatewayCommandsDoNotFallBackToPlanner(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "should not plan", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now"}}},
	}
	rt := Runtime{
		Model:    fp,
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{ProjectRoot: ".", Home: t.TempDir(), Workspace: t.TempDir()},
		MaxSteps: 6,
	}
	rt.Logger.Quiet = true
	cases := []struct {
		text string
		want string
	}{
		{text: "mateway doctor", want: "请在本机终端运行"},
		{text: "mateway ask hello", want: "不会嵌套执行"},
		{text: "mateway init", want: "会初始化本机"},
		{text: "mateway nope", want: "unknown mateway command"},
	}
	for _, tc := range cases {
		resp, err := rt.Handle(context.Background(), channel.InboundMessage{
			SessionKey: "cli:top-command-" + strings.ReplaceAll(tc.text, " ", "-"),
			Channel:    "cli",
			Text:       tc.text,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(resp.Reply.Text, tc.want) {
			t.Fatalf("expected %q for %q, got %q", tc.want, tc.text, resp.Reply.Text)
		}
	}
	if fp.planCalls != 0 {
		t.Fatalf("expected mateway top-level commands not to call planner, got %d", fp.planCalls)
	}
}

func TestDirectSkillPromoteAddsNextStepHint(t *testing.T) {
	workspace := t.TempDir()
	store := memory.NewStore(workspace)
	cfg := memory.LearningConfig{Enabled: true, SuccessThreshold: 1, RequireUserConfirm: true}
	path, err := store.ProcessTask(memory.TaskOutcome{
		AgentID:     "main",
		TraceID:     "trace-1",
		TaskID:      "task-1",
		Intent:      "review latest release notes",
		PlanSummary: "review release notes",
		Tools:       []string{"web.search", "file.summary"},
		Success:     true,
		FinishedAt:  time.Now(),
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	rt := Runtime{
		Model:    &fakePlanner{},
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{ProjectRoot: workspace, Workspace: workspace, AllowedRoots: []string{workspace}},
		MaxSteps: 6,
		Memory:   store,
	}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{
		SessionKey: "cli:skill-promote-hint",
		Channel:    "cli",
		Text:       "mateway skill promote --proposal " + path.CandidatePath + " --name release-review",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed || !strings.Contains(resp.Reply.Text, "Skill promoted:") {
		t.Fatalf("expected direct skill promote success, got %#v", resp)
	}
	if !strings.Contains(resp.Reply.Text, "mateway skill list") || !strings.Contains(resp.Reply.Text, "下一次 planning turn 会自动重载") {
		t.Fatalf("expected promote next-step hint, got %q", resp.Reply.Text)
	}
}

func TestDirectMemoryListAndShowRouteSkillCandidateToPromote(t *testing.T) {
	workspace := t.TempDir()
	store := memory.NewStore(workspace)
	cfg := memory.LearningConfig{Enabled: true, SuccessThreshold: 1, RequireUserConfirm: true}
	path, err := store.ProcessTask(memory.TaskOutcome{
		AgentID:     "main",
		TraceID:     "trace-1",
		TaskID:      "task-1",
		Intent:      "review latest release notes",
		PlanSummary: "review release notes",
		Tools:       []string{"web.search", "file.summary"},
		Success:     true,
		FinishedAt:  time.Now(),
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	candidateID := strings.TrimSuffix(filepath.Base(path.CandidatePath), filepath.Ext(path.CandidatePath))
	rt := Runtime{
		Model:    &fakePlanner{},
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{ProjectRoot: workspace, Workspace: workspace, AllowedRoots: []string{workspace}},
		MaxSteps: 6,
		Memory:   store,
	}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{
		SessionKey: "cli:skill-candidate-list",
		Channel:    "cli",
		Text:       "mateway memory list --area inbox --status proposed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed || !strings.Contains(resp.Reply.Text, "mateway skill promote --proposal "+candidateID) {
		t.Fatalf("expected skill candidate list to suggest promote, got %#v", resp)
	}
	if strings.Contains(resp.Reply.Text, "mateway memory commit --proposal "+candidateID) {
		t.Fatalf("expected skill candidate list not to suggest memory commit, got %q", resp.Reply.Text)
	}

	resp, err = rt.Handle(context.Background(), channel.InboundMessage{
		SessionKey: "cli:skill-candidate-show",
		Channel:    "cli",
		Text:       "mateway memory show " + candidateID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed || !strings.Contains(resp.Reply.Text, "mateway skill promote --proposal "+candidateID) {
		t.Fatalf("expected skill candidate show to suggest promote, got %#v", resp)
	}
	if strings.Contains(resp.Reply.Text, "mateway memory commit --proposal "+candidateID) {
		t.Fatalf("expected skill candidate show not to suggest memory commit, got %q", resp.Reply.Text)
	}
}

func TestRuntimeRechecksFinalAcceptanceAfterRepair(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "first", Understanding: model.UnderstandingJSON{Goal: "write file", RiskLevel: "guarded_mutation"}, Steps: []model.PlanStep{{
			ID: "s1", Tool: "time.now", Args: map[string]string{}, Risk: string(tool.RiskGuardedMutation),
		}}},
		repairPlan: model.Plan{Summary: "repaired", Understanding: model.UnderstandingJSON{Goal: "write file", RiskLevel: "guarded_mutation"}, Steps: []model.PlanStep{{
			ID: "s1", Tool: "time.now", Args: map[string]string{}, Risk: string(tool.RiskGuardedMutation),
		}}},
	}
	fp2 := &finalSequencePlanner{fakePlanner: fp, responses: []string{
		`{"status":"rejected","reason":"needs repair"}`,
		`{"status":"accepted","reason":"repair completed"}`,
	}}
	rt := Runtime{Model: fp2, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6, Acceptors: NewAcceptanceRegistry()}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Text: "执行并验收"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed {
		t.Fatalf("expected repaired final acceptance to succeed, got %#v", resp)
	}
	if fp2.repairCalls != 1 {
		t.Fatalf("expected one repair, got %d", fp2.repairCalls)
	}
	if fp2.finalAcceptCalls != 2 {
		t.Fatalf("expected final acceptance before and after repair, got %d", fp2.finalAcceptCalls)
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
	prompt := fp.firstPlanSkillPrompt
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

func TestRepairUsesFocusedShortMemory(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "initial", Understanding: model.UnderstandingJSON{Goal: "continue task", RiskLevel: "safe_read"}, Steps: []model.PlanStep{{
			ID: "s1", Tool: "web.search", Args: map[string]string{"query": "latest updates"},
		}}},
		repairPlan: model.Plan{Summary: "repair", Understanding: model.UnderstandingJSON{Goal: "continue task", RiskLevel: "safe_read"}, Steps: []model.PlanStep{{
			ID: "r1", Tool: "time.now", Args: map[string]string{},
		}}},
	}
	store := session.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now()
	if err := store.Save(session.State{
		SessionKey:   "cli:repair-memory",
		Channel:      "cli",
		UserID:       "local",
		ThreadID:     "cli",
		ActiveTaskID: "task-1",
		TaskOrder:    []string{"task-2", "task-1"},
		Tasks: map[string]session.TaskState{
			"task-1": {
				ID:            "task-1",
				Status:        session.TaskOpen,
				Topic:         "项目复盘",
				UserText:      "总结当前项目",
				ResolvedQuery: "总结当前项目并列出下一步",
				PlanSummary:   "read project docs",
				StepOrder:     []string{"s1", "s2"},
				StepStates: map[string]session.StepState{
					"s1": {ID: "s1", Tool: "file.read", Status: "passed", AcceptanceStatus: "passed", ResultSummary: "read README"},
					"s2": {ID: "s2", Tool: "web.search", Status: "failed", ResultError: "timeout"},
				},
				Artifacts: []session.Artifact{{
					Kind:  "file",
					Path:  "/tmp/report.md",
					Label: "项目复盘文档",
				}},
				UpdatedAt: now,
			},
			"task-2": {
				ID:            "task-2",
				Status:        session.TaskOpen,
				Topic:         "旁路线索",
				UserText:      "查一下旧问题",
				ResolvedQuery: "查一下旧问题",
				PlanSummary:   "inspect old issue",
				Artifacts: []session.Artifact{{
					Kind:  "file",
					Path:  "/tmp/other.md",
					Label: "旧问题记录",
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
	if _, err := rt.Handle(context.Background(), channel.InboundMessage{
		Channel:    "cli",
		ThreadID:   "cli",
		UserID:     "local",
		SessionKey: "cli:repair-memory",
		Text:       "继续当前任务",
	}); err != nil {
		t.Fatal(err)
	}
	prompt := fp.lastRepairSkillPrompt
	if !strings.Contains(prompt, "Short memory:") || !strings.Contains(prompt, "Current step focus:") || !strings.Contains(prompt, "s2 status=failed tool=web.search") {
		t.Fatalf("expected focused repair short memory, got %q", prompt)
	}
	if strings.Contains(prompt, "Current execution progress:") {
		t.Fatalf("expected repair prompt to omit duplicated execution progress, got %q", prompt)
	}
	for _, banned := range []string{
		"Recent turns:",
		"Open tasks:",
		"/tmp/other.md",
	} {
		if strings.Contains(prompt, banned) {
			t.Fatalf("expected repair short memory to omit %q, got %q", banned, prompt)
		}
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

func TestRuntimeSkipsSourceTypeLongMemoryInPromptInjection(t *testing.T) {
	workspace := t.TempDir()
	longDir := filepath.Join(workspace, "memory", "agents", "main", "long")
	if err := os.MkdirAll(longDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(longDir, "search-source.md")
	sourceText := `---
type: source
scope: agent
owner_agent: main
visibility: private
status: active
tags: [memory]
aliases: []
sources:
  - manual
confidence: low
created_at: 2026-05-25
updated_at: 2026-05-25
---

# Search Source

This source note mentions durable memory in Markdown files.
`
	if err := os.WriteFile(sourcePath, []byte(sourceText), 0o644); err != nil {
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
		Memory:   memory.NewStore(workspace),
	}
	rt.Logger.Quiet = true
	if _, err := rt.Handle(context.Background(), channel.InboundMessage{
		Channel:    "cli",
		ThreadID:   "cli",
		UserID:     "local",
		SessionKey: "cli:long-memory-source",
		Text:       "How does Mateway store memory?",
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fp.lastPlanSkillPrompt, "Relevant long memory:") {
		t.Fatalf("expected source-type long memory to stay out of prompt injection, got %q", fp.lastPlanSkillPrompt)
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
	if len(st.LastTask.StepOrder) != 1 || st.LastTask.StepOrder[0] != "s1" {
		t.Fatalf("expected step order persisted, got %#v", st.LastTask.StepOrder)
	}
	if len(st.LastTask.StepStates) != 1 || st.LastTask.StepStates["s1"].Status != "passed" {
		t.Fatalf("expected step states persisted, got %#v", st.LastTask.StepStates)
	}
	if st.LastTask.ResolvedQuery != "现在几点" {
		t.Fatalf("expected resolved query persisted, got %#v", st.LastTask)
	}
	if len(st.RecentTurns) != 2 {
		t.Fatalf("expected user and assistant turns, got %#v", st.RecentTurns)
	}
}

func TestRuntimeReusesPassedStepResults(t *testing.T) {
	calls := 0
	reg := tool.NewRegistry()
	reg.Register(tool.Definition{
		Name: "fake.read",
		Run: func(ctx context.Context, call tool.Call) tool.Result {
			calls++
			return tool.Result{OK: true, Output: "fresh output", Evidence: map[string]any{"kind": "file_read", "path": "README.md"}}
		},
	})
	rt := Runtime{Tools: reg, ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6}
	plan := model.Plan{Summary: "reuse", Steps: []model.PlanStep{{ID: "s1", Tool: "fake.read", Args: map[string]string{}}}}
	results, control := rt.executePlan(context.Background(), "trace", plan, false, "", map[string]session.StepState{
		"s1": {
			ID:            "s1",
			Tool:          "fake.read",
			Status:        "passed",
			ResultOK:      true,
			ResultSummary: "cached output",
			Evidence:      map[string]any{"kind": "file_read", "path": "README.md"},
		},
	}, nil)
	if control != "" {
		t.Fatalf("expected no control, got %q", control)
	}
	if calls != 0 {
		t.Fatalf("expected tool not to rerun, got %d calls", calls)
	}
	if len(results) != 1 || results[0].Output != "cached output" {
		t.Fatalf("expected cached result reuse, got %#v", results)
	}
}

func TestRuntimeReusesUsableStepResults(t *testing.T) {
	calls := 0
	reg := tool.NewRegistry()
	reg.Register(tool.Definition{
		Name: "fake.read",
		Run: func(ctx context.Context, call tool.Call) tool.Result {
			calls++
			return tool.Result{OK: true, Output: "fresh output", Evidence: map[string]any{"kind": "file_read", "path": "README.md"}}
		},
	})
	rt := Runtime{Tools: reg, ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6}
	plan := model.Plan{Summary: "reuse", Steps: []model.PlanStep{{ID: "s1", Tool: "fake.read", Args: map[string]string{}}}}
	results, control := rt.executePlan(context.Background(), "trace", plan, false, "", map[string]session.StepState{
		"s1": {
			ID:            "s1",
			Tool:          "fake.read",
			Status:        "usable",
			ResultOK:      true,
			ResultSummary: "usable output",
			Evidence:      map[string]any{"kind": "file_read", "path": "README.md"},
		},
	}, nil)
	if control != "" {
		t.Fatalf("expected no control, got %q", control)
	}
	if calls != 0 {
		t.Fatalf("expected tool not to rerun, got %d calls", calls)
	}
	if len(results) != 1 || results[0].Output != "usable output" {
		t.Fatalf("expected usable result reuse, got %#v", results)
	}
}

func TestRuntimeSkipsStepWhenDependencyFailed(t *testing.T) {
	patchCalls := 0
	reg := tool.NewRegistry()
	reg.Register(tool.Definition{
		Name: "web.search",
		Run: func(ctx context.Context, call tool.Call) tool.Result {
			return tool.Result{OK: false, Error: "timeout", Output: "timeout"}
		},
	})
	reg.Register(tool.Definition{
		Name: "file.patch",
		Run: func(ctx context.Context, call tool.Call) tool.Result {
			patchCalls++
			return tool.Result{OK: true, Output: "patched", Evidence: map[string]any{"kind": "file_patch", "path": "ai-courses.md"}}
		},
	})
	rt := Runtime{Tools: reg, ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6}
	plan := model.Plan{Summary: "search then patch", Steps: []model.PlanStep{
		{ID: "s1", Tool: "web.search", Args: map[string]string{"query": "AI courses"}},
		{ID: "s2", Tool: "file.patch", Args: map[string]string{"path": "ai-courses.md", "append": "x"}, DependsOn: []string{"s1"}},
	}}
	results, control := rt.executePlan(context.Background(), "trace", plan, false, "", nil, nil)
	if control != "" {
		t.Fatalf("expected no control, got %q", control)
	}
	if patchCalls != 0 {
		t.Fatalf("expected dependent patch not to run, got %d calls", patchCalls)
	}
	if len(results) != 2 {
		t.Fatalf("expected search failure and skipped patch, got %#v", results)
	}
	if results[1].Error != "dependency_failed" || results[1].OK {
		t.Fatalf("expected dependency_failed result, got %#v", results[1])
	}
}

func TestRuntimeFollowupResumesTaskWithExistingStepStates(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "followup", Steps: []model.PlanStep{
			{ID: "s1", Tool: "time.now", Args: map[string]string{}},
			{ID: "s2", Tool: "time.now", Args: map[string]string{}},
		}},
		followupDecision: model.FollowupDecision{
			Kind:          "active_followup",
			TargetTaskID:  "task-ai",
			ResolvedQuery: "继续当前 AI 任务",
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
				ID:              "task-ai",
				UserText:        "当前 AI 任务",
				ResolvedQuery:   "当前 AI 任务",
				Topic:           "AI 任务",
				Status:          session.TaskOpen,
				ExecutionStatus: "executing",
				StepOrder:       []string{"s1", "s2"},
				StepStates: map[string]session.StepState{
					"s1": {ID: "s1", Tool: "time.now", Status: "passed", ResultOK: true, ResultSummary: "cached", Evidence: map[string]any{"kind": "time"}},
				},
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
		Text:       "继续",
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Load("cli:cli")
	if err != nil {
		t.Fatal(err)
	}
	if st.LastTask == nil || len(st.LastTask.StepStates) != 2 {
		t.Fatalf("expected resumed task step states, got %#v", st.LastTask)
	}
}

func TestRuntimeEmptySessionTreatsDiagnoseRequestAsNewTask(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "diagnose", Steps: []model.PlanStep{{ID: "s1", Tool: "terminal.run", Args: map[string]string{"command": "pwd"}}}},
		followupDecision: model.FollowupDecision{
			Kind:       "ambiguous",
			Reason:     "would otherwise be ambiguous",
			Confidence: 0,
		},
	}
	store := session.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	rt := Runtime{Model: fp, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6, Sessions: store}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{
		Channel:    "cli",
		ThreadID:   "cli",
		UserID:     "local",
		SessionKey: "cli:openclaw-diagnose",
		Text:       "帮我看看 openclaw 是不是卡住了",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.AwaitUserInput {
		t.Fatalf("expected empty-session diagnose request to be treated as new task, got %#v", resp)
	}
	if fp.followupCalls != 0 {
		t.Fatalf("expected no model followup call on empty session, got %d", fp.followupCalls)
	}
}

func TestBuildModelContextPromptIncludesCurrentExecutionProgress(t *testing.T) {
	task := &session.TaskState{
		ExecutionStatus: "executing",
		StepOrder:       []string{"s1", "s2"},
		StepStates: map[string]session.StepState{
			"s1": {ID: "s1", Tool: "file.read", Status: "passed", AcceptanceStatus: "passed", ResultSummary: "read README"},
			"s2": {ID: "s2", Tool: "terminal.run", Status: "pending"},
		},
	}
	prompt := buildModelContextPrompt("继续当前任务", "planning", nil, nil, tool.Context{}, promptContextOptions{
		CurrentTask: task,
	})
	for _, want := range []string{
		"Current execution progress:",
		"execution_status: executing",
		"completed_steps should not be repeated",
		"s1 status=passed tool=file.read",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected prompt to contain %q, got %q", want, prompt)
		}
	}
}

func TestBuildModelContextPromptShrinksSynthesisContext(t *testing.T) {
	prompt := buildModelContextPrompt("请总结结果", skill.StageSynthesis, []skill.Match{{
		Definition: skill.Definition{Name: "chinese-summary", Instruction: "请输出自然中文总结"},
	}}, []tool.Definition{{
		Name:        "web.search",
		Description: "Search the web",
	}}, tool.Context{}, promptContextOptions{
		ShortMemory: "recent context",
		LongMemory:  "durable memory",
		Understanding: taskUnderstanding{
			Goal: "总结结果",
		},
	})
	for _, banned := range []string{
		"Current date:",
		"Current environment:",
		"Short memory:",
		"Relevant long memory:",
		"Task understanding:",
		"Available tools:",
		"Selected skills:",
		"Current user request:",
	} {
		if strings.Contains(prompt, banned) {
			t.Fatalf("expected synthesis prompt to omit %q, got:\n%s", banned, prompt)
		}
	}
	for _, want := range []string{
		"Core objective:",
		"Current stage:",
		string(skill.StageSynthesis),
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected synthesis prompt to keep %q, got:\n%s", want, prompt)
		}
	}
}

func TestBuildModelContextPromptShrinksRepairUnderstanding(t *testing.T) {
	prompt := buildModelContextPrompt("继续修复任务", skill.StagePlanningRepair, nil, nil, tool.Context{}, promptContextOptions{
		Understanding: taskUnderstanding{
			Goal:            "finish the remaining fix",
			Constraints:     []string{"previous step timed out", "missing required path"},
			Capabilities:    []string{"search_web", "patch_file"},
			CompletionDraft: []string{"obtain the missing path", "complete the patch safely"},
			EvidenceHints:   []string{"query", "provider", "line range"},
			RiskLevel:       "guarded_mutation",
			NeedsGrounding:  true,
			NeedsMutation:   true,
		},
	})
	for _, want := range []string{
		"Task understanding:",
		"remaining_goal: finish the remaining fix",
		"failure_reason: previous step timed out | missing required path",
		"completion_delta: obtain the missing path | complete the patch safely",
		"risk_level: guarded_mutation",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected repair prompt to contain %q, got:\n%s", want, prompt)
		}
	}
	for _, banned := range []string{
		"capabilities:",
		"evidence_hints:",
		"needs_grounding:",
		"needs_mutation:",
		"completion_draft:",
		"- goal:",
	} {
		if strings.Contains(prompt, banned) {
			t.Fatalf("expected repair prompt to omit %q, got:\n%s", banned, prompt)
		}
	}
}

func TestBuildStageModelPromptSeparatesSkillsContext(t *testing.T) {
	text := buildStageModelPrompt("Current stage:\nplanning", []skill.Definition{{
		Name:        "doc-review",
		Description: "Review docs",
		Instruction: "Focus on docs.",
	}})
	for _, want := range []string{
		"Current stage:",
		"Skills context:",
		"Selected skills:",
		"doc-review",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected structured model prompt to contain %q, got %q", want, text)
		}
	}
}

func TestRepairUsesWiderCandidateToolSubset(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "initial", Understanding: model.UnderstandingJSON{Goal: "diagnose local tool", RiskLevel: "safe_read"}, Steps: []model.PlanStep{{
			ID: "s1", Tool: "terminal.run", Args: map[string]string{"command": "missing-cmd"}, Risk: string(tool.RiskSafeRead),
		}}},
		repairPlan: model.Plan{Summary: "repair", Understanding: model.UnderstandingJSON{Goal: "diagnose local tool", RiskLevel: "safe_read"}, Steps: []model.PlanStep{{
			ID: "r1", Tool: "time.now", Args: map[string]string{},
		}}},
	}
	reg := tool.NewRegistry()
	for _, name := range []string{"time.now", "terminal.run", "project.index", "web.search", "web.fetch", "file.read", "file.summary", "memory.search", "config.summary"} {
		def, _ := tool.NewBuiltinRegistry().Get(name)
		reg.Register(def)
	}
	rt := Runtime{Model: fp, Tools: reg, ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6, Acceptors: NewAcceptanceRegistry()}
	rt.Logger.Quiet = true
	if _, err := rt.Handle(context.Background(), channel.InboundMessage{Text: "帮我诊断一下当前工具为什么不可用"}); err != nil {
		t.Fatal(err)
	}
	if len(fp.lastPlanTools) == 0 || len(fp.lastPlanTools) > planningCandidateBudget {
		t.Fatalf("expected planning subset <= %d, got %v", planningCandidateBudget, fp.lastPlanTools)
	}
	if len(fp.lastRepairTools) == 0 || len(fp.lastRepairTools) > repairCandidateBudget {
		t.Fatalf("expected repair subset <= %d, got %v", repairCandidateBudget, fp.lastRepairTools)
	}
	if len(fp.lastRepairTools) <= len(fp.lastPlanTools) {
		t.Fatalf("expected repair candidates wider than planning, plan=%v repair=%v", fp.lastPlanTools, fp.lastRepairTools)
	}
	if strings.Contains(fp.lastRepairSkillPrompt, "Relevant long memory:") {
		t.Fatalf("expected repair prompt to omit long memory, got %q", fp.lastRepairSkillPrompt)
	}
}

func TestRepairUsesWiderSkillSubset(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "initial", Understanding: model.UnderstandingJSON{Goal: "search latest updates", RiskLevel: "safe_read"}, Steps: []model.PlanStep{{
			ID: "s1", Tool: "web.search", Args: map[string]string{"query": "latest updates"},
		}}},
		repairPlan: model.Plan{Summary: "repair", Understanding: model.UnderstandingJSON{Goal: "search latest updates", RiskLevel: "safe_read"}, Steps: []model.PlanStep{{
			ID: "r1", Tool: "time.now", Args: map[string]string{},
		}}},
		finalAcceptText: `{"status":"accepted","reason":"ok"}`,
	}
	reg := tool.NewRegistry()
	reg.Register(tool.Definition{Name: "web.search", Description: "web", Risk: tool.RiskSafeRead, Run: func(ctx context.Context, call tool.Call) tool.Result {
		return tool.Result{OK: false, Error: "timeout", Output: "timeout"}
	}})
	reg.Register(tool.TimeNow())
	skills := skill.NewRegistry()
	for i, name := range []string{"skill-a", "skill-b", "skill-c", "skill-d", "skill-e"} {
		skills.Register(skill.Definition{Name: name, Stage: skill.StagePlanning, Priority: i + 1, WhenContains: []string{"latest"}})
	}
	rt := Runtime{Model: fp, Tools: reg, Skills: skills, ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6, Acceptors: NewAcceptanceRegistry()}
	rt.Logger.Quiet = true
	if _, err := rt.Handle(context.Background(), channel.InboundMessage{Text: "search latest updates"}); err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(fp.firstPlanSkillPrompt, "- skill-"); count != 2 {
		t.Fatalf("expected 2 planning skills in prompt block, got %d\n%s", count, fp.firstPlanSkillPrompt)
	}
	if !strings.Contains(fp.firstPlanSkillPrompt, "Skills context:") {
		t.Fatalf("expected planning prompt to separate skills context, got %q", fp.firstPlanSkillPrompt)
	}
	if count := strings.Count(fp.lastRepairSkillPrompt, "- skill-"); count != 4 {
		t.Fatalf("expected 4 repair skills in prompt block, got %d\n%s", count, fp.lastRepairSkillPrompt)
	}
	if !strings.Contains(fp.lastRepairSkillPrompt, "Skills context:") {
		t.Fatalf("expected repair prompt to separate skills context, got %q", fp.lastRepairSkillPrompt)
	}
}

func TestRuntimeRepairKeepsSuccessfulPriorResults(t *testing.T) {
	callsA := 0
	callsB := 0
	reg := tool.NewRegistry()
	reg.Register(tool.Definition{
		Name: "fake.a",
		Run: func(ctx context.Context, call tool.Call) tool.Result {
			callsA++
			return tool.Result{OK: true, Output: "result-a", Evidence: map[string]any{"kind": "file_read", "path": "a.txt"}}
		},
	})
	reg.Register(tool.Definition{
		Name: "fake.b",
		Run: func(ctx context.Context, call tool.Call) tool.Result {
			callsB++
			if callsB == 1 {
				return tool.Result{OK: false, Output: "failed", Error: "boom"}
			}
			return tool.Result{OK: true, Output: "result-b", Evidence: map[string]any{"kind": "file_read", "path": "b.txt"}}
		},
	})
	fp := &fakePlanner{
		plan: model.Plan{Summary: "two steps", Steps: []model.PlanStep{
			{ID: "s1", Tool: "fake.a", Args: map[string]string{}},
			{ID: "s2", Tool: "fake.b", Args: map[string]string{}},
		}},
		repairPlan: model.Plan{Summary: "repair second step", Steps: []model.PlanStep{
			{ID: "s1", Tool: "fake.a", Args: map[string]string{}},
			{ID: "s2", Tool: "fake.b", Args: map[string]string{}},
		}},
	}
	rt := Runtime{Model: fp, Tools: reg, ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Text: "run two-step task"})
	if err != nil {
		t.Fatal(err)
	}
	if callsA != 1 {
		t.Fatalf("expected first step not to rerun during repair, got %d calls", callsA)
	}
	if callsB != 2 {
		t.Fatalf("expected second step to rerun once, got %d calls", callsB)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("expected merged results for both steps, got %#v", resp.Results)
	}
	if resp.Results[0].StepID != "s1" || resp.Results[1].StepID != "s2" {
		t.Fatalf("expected both step results preserved, got %#v", resp.Results)
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

func TestRuntimeRuleFollowupUsesCompletedActiveTask(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "followup", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now", Args: map[string]string{}}}},
		followupDecision: model.FollowupDecision{
			Kind:       "ambiguous",
			Reason:     "模型不确定",
			Confidence: 0.2,
		},
	}
	store := session.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err := store.Save(session.State{
		SessionKey:   "cli:cli",
		ActiveTaskID: "task-system",
		TaskOrder:    []string{"task-system"},
		Tasks: map[string]session.TaskState{
			"task-system": {
				ID:            "task-system",
				UserText:      "总结当前 Mateway 已经形成闭环的三块能力",
				ResolvedQuery: "总结当前 Mateway 已经形成闭环的三块能力",
				Topic:         "当前功能体系",
				Status:        session.TaskCompleted,
				UpdatedAt:     time.Now().Add(-5 * time.Minute),
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Model: fp, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6, Sessions: store}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{
		Channel:    "cli",
		ThreadID:   "cli",
		UserID:     "local",
		SessionKey: "cli:cli",
		Text:       "继续上一轮，把它拆成三条验收检查项。",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.AwaitUserInput {
		t.Fatalf("expected completed active task followup to avoid clarification, got %#v", resp)
	}
	if fp.followupCalls != 0 {
		t.Fatalf("expected rule followup before model followup, got %d calls", fp.followupCalls)
	}
	if !strings.Contains(fp.lastPlanUser, "三条验收检查项") || !strings.Contains(fp.lastPlanUser, "当前 Mateway 已经形成闭环") {
		t.Fatalf("expected completed task context in planning input, got %q", fp.lastPlanUser)
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

func TestRuntimePendingConfirmBlocksIndependentNewTask(t *testing.T) {
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
	if !resp.AwaitUserInput || !strings.Contains(resp.Reply.Text, "等待确认") {
		t.Fatalf("expected pending approval clarification, got %#v", resp)
	}
	if fp.followupCalls != 0 {
		t.Fatalf("expected pending approval block to avoid model followup, got %d calls", fp.followupCalls)
	}
	if fp.planCalls != 0 {
		t.Fatalf("expected pending approval block before planning, got %d calls", fp.planCalls)
	}
	st, err := store.Load("cli:cli")
	if err != nil {
		t.Fatal(err)
	}
	if st.ActiveTaskID != "task-delete" {
		t.Fatalf("expected active task to remain pending confirmation, got %q", st.ActiveTaskID)
	}
	if st.Tasks["task-delete"].Status != session.TaskAwaitConfirm {
		t.Fatalf("expected original pending task to remain pending but inactive, got %#v", st.Tasks["task-delete"])
	}
}

func TestRuntimePendingConfirmCanBeReplacedByNewInstallMethod(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "install with brew", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now", Args: map[string]string{}}}},
	}
	store := session.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err := store.Save(session.State{
		SessionKey:   "cli:cli",
		ActiveTaskID: "task-install-go",
		TaskOrder:    []string{"task-install-go"},
		Tasks: map[string]session.TaskState{
			"task-install-go": {
				ID:            "task-install-go",
				Status:        session.TaskAwaitConfirm,
				UserText:      "安装 larkcli",
				ResolvedQuery: "安装 larkcli",
				PendingApproval: &session.PendingApproval{
					ApprovalType:    "boolean_confirm",
					Prompt:          "go install github.com/larksuite/cli/cmd/lark@latest",
					RequestedAction: "go install github.com/larksuite/cli/cmd/lark@latest",
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Model: fp, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6, Sessions: store}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:cli", Text: "换homebrew安"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.AwaitUserInput {
		t.Fatalf("expected replacement request to replan, got %#v", resp)
	}
	if fp.planCalls != 1 || !strings.Contains(fp.lastPlanUser, "homebrew") {
		t.Fatalf("expected replanning with homebrew request, calls=%d user=%q", fp.planCalls, fp.lastPlanUser)
	}
	st, err := store.Load("cli:cli")
	if err != nil {
		t.Fatal(err)
	}
	if st.Tasks["task-install-go"].Status != session.TaskAwaitConfirm {
		t.Fatalf("expected old pending task suspended, got %#v", st.Tasks["task-install-go"])
	}
	if st.ActiveTaskID == "task-install-go" {
		t.Fatalf("expected active task to move to replacement task")
	}
}

func TestRuntimePendingConfirmAuthQuestionDoesNotReplaceByKeyword(t *testing.T) {
	fp := &fakePlanner{}
	store := session.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err := store.Save(session.State{
		SessionKey:   "cli:cli",
		ActiveTaskID: "task-install-go",
		TaskOrder:    []string{"task-install-go"},
		Tasks: map[string]session.TaskState{
			"task-install-go": {
				ID:            "task-install-go",
				Status:        session.TaskAwaitConfirm,
				UserText:      "安装 larkcli",
				ResolvedQuery: "安装 larkcli",
				PendingApproval: &session.PendingApproval{
					ApprovalType:    "boolean_confirm",
					Prompt:          "go install github.com/larksuite/cli/cmd/lark@latest",
					RequestedAction: "go install github.com/larksuite/cli/cmd/lark@latest",
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Model: fp, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6, Sessions: store}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:cli", Text: "git 认证失败是怎么回事"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.AwaitUserInput || fp.planCalls != 0 {
		t.Fatalf("expected pending approval clarification without replanning, resp=%#v calls=%d", resp, fp.planCalls)
	}
}

func containsAll(items []string, want []string) bool {
	set := map[string]struct{}{}
	for _, item := range items {
		set[item] = struct{}{}
	}
	for _, target := range want {
		if _, ok := set[target]; !ok {
			return false
		}
	}
	return true
}

func TestRuntimeCanResumeSuspendedPendingApproval(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "install lark", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now", Args: map[string]string{}}}},
	}
	store := session.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err := store.Save(session.State{
		SessionKey:   "cli:cli",
		ActiveTaskID: "task-auth",
		TaskOrder:    []string{"task-install", "task-auth"},
		Tasks: map[string]session.TaskState{
			"task-install": {
				ID:            "task-install",
				Status:        session.TaskAwaitConfirm,
				UserText:      "安装 larkcli",
				ResolvedQuery: "安装 larkcli",
				PendingApproval: &session.PendingApproval{
					ApprovalType:    "boolean_confirm",
					Prompt:          "go install github.com/larksuite/cli/cmd/lark@latest",
					RequestedAction: "go install github.com/larksuite/cli/cmd/lark@latest",
				},
			},
			"task-auth": {
				ID:            "task-auth",
				Status:        session.TaskCompleted,
				UserText:      "诊断 git 认证",
				ResolvedQuery: "诊断 git 认证",
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Model: fp, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6, Sessions: store}
	rt.Logger.Quiet = true
	_, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:cli", Text: "继续刚才安装，确认"})
	if err != nil {
		t.Fatal(err)
	}
	if fp.planCalls != 1 || !strings.Contains(fp.lastPlanUser, "安装 larkcli") {
		t.Fatalf("expected suspended install task to resume, calls=%d user=%q", fp.planCalls, fp.lastPlanUser)
	}
}

func TestRuntimeSoftwareInstallRunsWhenPlannerSelectsInstallTool(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "install lark", Steps: []model.PlanStep{{
			ID:   "s1",
			Tool: "software.install",
			Args: map[string]string{
				"name":       "larksuite/cli",
				"method":     "npx",
				"command":    "npx @larksuite/cli@latest install",
				"executable": "lark-cli",
				"source_url": "https://github.com/larksuite/cli",
			},
		}}},
	}
	registry := tool.NewBuiltinRegistry()
	registry.Register(tool.Definition{
		Name: "software.install",
		Metadata: tool.Metadata{
			AcceptanceMode: tool.AcceptanceCodeOnly,
			ResourceScope:  "software:install",
		},
		ArgsSchema: map[string]string{"command": "install command"},
		Run: func(ctx context.Context, call tool.Call) tool.Result {
			return tool.Result{
				OK:     true,
				Output: "installed lark-cli",
				Evidence: map[string]any{
					"kind":           "software_install",
					"verified":       true,
					"verify_command": "command -v lark-cli",
				},
			}
		},
	})
	rt := Runtime{Model: fp, Tools: registry, ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Text: "安装 lark cli"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.AwaitConfirm || resp.Failed {
		t.Fatalf("expected install execution without confirmation, got %#v", resp)
	}
	if len(resp.Plan.Steps) != 1 || resp.Plan.Steps[0].Tool != "software.install" {
		t.Fatalf("expected software.install normalization, got %#v", resp.Plan.Steps)
	}
	if len(resp.Results) != 1 || resp.Results[0].Tool != "software.install" {
		t.Fatalf("expected software.install result, got %#v", resp.Results)
	}
}

func TestRuntimeReloadsWorkspaceSkillsAfterInstall(t *testing.T) {
	workspace := t.TempDir()
	skillDir := filepath.Join(workspace, "skills", "agent-browser")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillBody := `---
name: agent-browser
description: Browser automation CLI for AI agents.
stage: planning
priority: 9
when_contains: [打开, 网页, 网站, 截图, browser, screenshot]
---

# agent-browser

Before browser actions, load the real workflow with:

agent-browser skills get core
`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillBody), 0o644); err != nil {
		t.Fatal(err)
	}
	fp := &fakePlanner{
		plan: model.Plan{Summary: "use browser", Steps: []model.PlanStep{{
			ID: "s1", Tool: "terminal.run", Args: map[string]string{"command": "printf ok"},
		}}},
	}
	rt := Runtime{
		Model:    fp,
		Tools:    tool.NewBuiltinRegistry(),
		Skills:   skill.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{ProjectRoot: workspace, Workspace: workspace, AllowedRoots: []string{workspace}},
		MaxSteps: 6,
	}
	rt.Logger.Quiet = true
	if _, err := rt.Handle(context.Background(), channel.InboundMessage{
		Channel:    "cli",
		ThreadID:   "cli",
		UserID:     "local",
		SessionKey: "cli:reload-skill",
		Text:       "帮我打开百度并截图",
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fp.lastPlanSkillPrompt, "agent-browser") {
		t.Fatalf("expected runtime to reload installed workspace skill, got %q", fp.lastPlanSkillPrompt)
	}
	if !strings.Contains(fp.lastPlanSkillPrompt, "agent-browser skills get core") {
		t.Fatalf("expected skill instruction in planning prompt, got %q", fp.lastPlanSkillPrompt)
	}
}

func TestRuntimeDoesNotReusePassedStepsFromFailedTaskOnRetry(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "use browser", Steps: []model.PlanStep{{
			ID: "step-1", Tool: "terminal.run", Args: map[string]string{"command": "printf fresh-run"},
		}}},
		finalAcceptText: `{"status":"accepted","reason":"ok"}`,
	}
	calls := 0
	reg := tool.NewRegistry()
	reg.Register(tool.Definition{
		Name: "terminal.run",
		Metadata: tool.Metadata{
			AcceptanceMode: tool.AcceptanceCodeOnly,
			ResourceScope:  "terminal:command",
		},
		ArgsSchema: map[string]string{"command": "command"},
		Run: func(ctx context.Context, call tool.Call) tool.Result {
			calls++
			return tool.Result{
				OK:     true,
				Output: "fresh-run",
				Evidence: map[string]any{
					"kind":      "terminal",
					"exit_code": 0,
					"stdout":    "fresh-run",
				},
			}
		},
	})
	store := session.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now()
	if err := store.Save(session.State{
		SessionKey:   "cli:retry-failed",
		Channel:      "cli",
		UserID:       "local",
		ThreadID:     "cli",
		ActiveTaskID: "task-failed",
		TaskOrder:    []string{"task-failed"},
		Tasks: map[string]session.TaskState{
			"task-failed": {
				ID:              "task-failed",
				TraceID:         "task-failed",
				Topic:           "打开百度并截图",
				UserText:        "帮我打开百度并截图",
				ResolvedQuery:   "帮我打开百度并截图",
				PlanSummary:     "old failed browser run",
				ToolNames:       []string{"terminal.run"},
				Status:          session.TaskFailed,
				Failed:          true,
				ExecutionStatus: "failed",
				StepOrder:       []string{"step-1"},
				StepStates: map[string]session.StepState{
					"step-1": {
						ID:            "step-1",
						Tool:          "terminal.run",
						Status:        "passed",
						ResultOK:      true,
						ResultSummary: "old-version-only-output",
						Evidence: map[string]any{
							"kind":      "terminal",
							"exit_code": 0,
							"stdout":    "old-version-only-output",
						},
					},
					"step-2": {
						ID:          "step-2",
						Tool:        "terminal.run",
						Status:      "failed",
						ResultOK:    false,
						ResultError: "step_verification_failed",
					},
				},
				UpdatedAt:  now,
				StartedAt:  now.Add(-time.Minute),
				FinishedAt: now.Add(-time.Second),
			},
		},
		RecentTurns: []session.Turn{
			{Role: "user", Text: "帮我打开百度并截图", At: now.Add(-time.Minute)},
			{Role: "assistant", Text: "任务失败了", At: now.Add(-time.Second)},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{
		Model:    fp,
		Tools:    reg,
		ToolCtx:  tool.Context{ProjectRoot: "."},
		MaxSteps: 6,
		Sessions: store,
	}
	rt.Logger.Quiet = true
	if _, err := rt.Handle(context.Background(), channel.InboundMessage{
		Channel:    "cli",
		ThreadID:   "cli",
		UserID:     "local",
		SessionKey: "cli:retry-failed",
		Text:       "帮我打开百度并截图",
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("expected retry to execute terminal.run again instead of reusing passed step from failed task, calls=%d", calls)
	}
}

func TestRuntimeDoesNotReusePassedOnlyStepsWhenTaskStatusIsFailed(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "use browser", Steps: []model.PlanStep{{
			ID: "step-1", Tool: "terminal.run", Args: map[string]string{"command": "printf rerun-open"},
		}, {
			ID: "step-2", Tool: "terminal.run", Args: map[string]string{"command": "printf rerun-shot"},
		}}},
		finalAcceptText: `{"status":"accepted","reason":"ok"}`,
	}
	calls := 0
	reg := tool.NewRegistry()
	reg.Register(tool.Definition{
		Name: "terminal.run",
		Metadata: tool.Metadata{
			AcceptanceMode: tool.AcceptanceCodeOnly,
			ResourceScope:  "terminal:command",
		},
		ArgsSchema: map[string]string{"command": "command"},
		Run: func(ctx context.Context, call tool.Call) tool.Result {
			calls++
			out := call.Args["command"]
			return tool.Result{
				OK:     true,
				Output: out,
				Evidence: map[string]any{
					"kind":      "terminal",
					"exit_code": 0,
					"stdout":    out,
				},
			}
		},
	})
	store := session.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now()
	if err := store.Save(session.State{
		SessionKey:   "cli:retry-failed-passed-only",
		Channel:      "cli",
		UserID:       "local",
		ThreadID:     "cli",
		ActiveTaskID: "task-failed",
		TaskOrder:    []string{"task-failed"},
		Tasks: map[string]session.TaskState{
			"task-failed": {
				ID:              "task-failed",
				TraceID:         "task-failed",
				Topic:           "打开百度并截图",
				UserText:        "帮我打开百度并截图",
				ResolvedQuery:   "帮我打开百度并截图",
				PlanSummary:     "old failed browser run",
				ToolNames:       []string{"terminal.run"},
				Status:          session.TaskFailed,
				Failed:          true,
				ExecutionStatus: "failed",
				StepOrder:       []string{"step-1"},
				StepStates: map[string]session.StepState{
					"step-1": {
						ID:            "step-1",
						Tool:          "terminal.run",
						Status:        "passed",
						ResultOK:      true,
						ResultSummary: "old-verify-output",
						Evidence: map[string]any{
							"kind":      "terminal",
							"exit_code": 0,
							"stdout":    "old-verify-output",
						},
					},
				},
				UpdatedAt:  now,
				StartedAt:  now.Add(-time.Minute),
				FinishedAt: now.Add(-time.Second),
			},
		},
		RecentTurns: []session.Turn{
			{Role: "user", Text: "帮我打开百度并截图", At: now.Add(-time.Minute)},
			{Role: "assistant", Text: "任务失败了", At: now.Add(-time.Second)},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{
		Model:    fp,
		Tools:    reg,
		ToolCtx:  tool.Context{ProjectRoot: "."},
		MaxSteps: 6,
		Sessions: store,
	}
	rt.Logger.Quiet = true
	if _, err := rt.Handle(context.Background(), channel.InboundMessage{
		Channel:    "cli",
		ThreadID:   "cli",
		UserID:     "local",
		SessionKey: "cli:retry-failed-passed-only",
		Text:       "帮我打开百度并截图",
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("expected failed task retry to execute both new steps instead of reusing stale passed step, calls=%d", calls)
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

func TestRuntimeDirectlyAnswersGenericRecentArtifactPathLookup(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "should not plan", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now", Args: map[string]string{}}}},
	}
	store := session.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err := store.Save(session.State{
		SessionKey:   "cli:cli",
		ActiveTaskID: "task-doc",
		TaskOrder:    []string{"task-doc"},
		Tasks: map[string]session.TaskState{
			"task-doc": {
				ID:            "task-doc",
				Status:        session.TaskCompleted,
				Topic:         "功能体系",
				ResolvedQuery: "阅读 docs/当前功能体系.md",
				Artifacts: []session.Artifact{{
					Kind:  "file",
					Path:  "/Users/dongping/project/mateway/docs/当前功能体系.md",
					Label: "当前功能体系",
				}},
				UpdatedAt: time.Now().Add(-20 * time.Minute),
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Model: fp, Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{ProjectRoot: "."}, MaxSteps: 6, Sessions: store}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:cli", Text: "刚才那个功能体系文档路径发我"})
	if err != nil {
		t.Fatal(err)
	}
	if fp.planCalls != 0 {
		t.Fatalf("expected direct artifact answer without planning, got plan calls %d", fp.planCalls)
	}
	if !strings.Contains(resp.Reply.Text, "/Users/dongping/project/mateway/docs/当前功能体系.md") {
		t.Fatalf("expected artifact path in reply, got %q", resp.Reply.Text)
	}
}

func TestRuntimeDirectlyExecutesMatewayMemoryListCommand(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "should not plan", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now"}}},
	}
	workspace := t.TempDir()
	store := memory.NewStore(workspace)
	proposal, err := store.Propose(memory.ProposalInput{
		AgentID: "main",
		Scope:   "agent",
		Type:    "note",
		Title:   "Inbox note",
		Body:    "remember this",
	})
	if err != nil {
		t.Fatal(err)
	}
	rt := Runtime{
		Model:    fp,
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{ProjectRoot: ".", Workspace: workspace},
		MaxSteps: 6,
		Sessions: session.NewFileStore(filepath.Join(t.TempDir(), "sessions")),
		Memory:   store,
	}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{
		Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:cli",
		Text: "mateway memory list --area inbox --status proposed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fp.planCalls != 0 {
		t.Fatalf("expected direct memory list without planning, got plan calls %d", fp.planCalls)
	}
	if !strings.Contains(resp.Reply.Text, "已执行命令：`mateway memory list --area inbox --status proposed`") {
		t.Fatalf("expected executed command header, got %q", resp.Reply.Text)
	}
	if !strings.Contains(resp.Reply.Text, "Inbox note") || !strings.Contains(resp.Reply.Text, "proposed") {
		t.Fatalf("expected memory list output, got %q", resp.Reply.Text)
	}
	if !strings.Contains(resp.Reply.Text, "mateway memory show "+proposal.ID) || !strings.Contains(resp.Reply.Text, "mateway memory commit --proposal "+proposal.ID) {
		t.Fatalf("expected actionable inbox workflow hints, got %q", resp.Reply.Text)
	}
}

func TestRuntimeDirectGatewayStatusReturnsCommandNote(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "should not plan", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now"}}},
	}
	rt := Runtime{
		Model:    fp,
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{ProjectRoot: ".", Workspace: t.TempDir()},
		MaxSteps: 6,
	}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{
		Channel:    "cli",
		ThreadID:   "cli",
		UserID:     "local",
		SessionKey: "cli:gateway-note",
		Text:       "mateway gateway status",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fp.planCalls != 0 {
		t.Fatalf("expected gateway status note without planning, got plan calls %d", fp.planCalls)
	}
	if !strings.Contains(resp.Reply.Text, "命令说明：`mateway gateway status`") {
		t.Fatalf("expected command note header, got %q", resp.Reply.Text)
	}
	if strings.Contains(resp.Reply.Text, "已执行命令：") {
		t.Fatalf("expected gateway note not to claim execution, got %q", resp.Reply.Text)
	}
	if !strings.Contains(resp.Reply.Text, "不会安装开机自启动") || !strings.Contains(resp.Reply.Text, "OS service") {
		t.Fatalf("expected service boundary explanation, got %q", resp.Reply.Text)
	}
}

func TestRuntimeDirectGatewayServiceCommandDoesNotFallBackToPlanner(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "should not plan", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now"}}},
	}
	rt := Runtime{
		Model:    fp,
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{ProjectRoot: ".", Workspace: t.TempDir()},
		MaxSteps: 6,
	}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{
		Channel:    "cli",
		ThreadID:   "cli",
		UserID:     "local",
		SessionKey: "cli:gateway-start-note",
		Text:       "mateway gateway start",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fp.planCalls != 0 {
		t.Fatalf("expected gateway start note without planning, got plan calls %d", fp.planCalls)
	}
	if !strings.Contains(resp.Reply.Text, "命令说明：`mateway gateway start`") || !strings.Contains(resp.Reply.Text, "请在本机终端运行") {
		t.Fatalf("expected gateway start note, got %q", resp.Reply.Text)
	}

	resp, err = rt.Handle(context.Background(), channel.InboundMessage{
		Channel:    "cli",
		ThreadID:   "cli",
		UserID:     "local",
		SessionKey: "cli:gateway-unknown",
		Text:       "mateway gateway install",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fp.planCalls != 0 {
		t.Fatalf("expected unknown gateway command not to call planner, got %d", fp.planCalls)
	}
	if !strings.Contains(resp.Reply.Text, "命令未完成：`mateway gateway install`") || !strings.Contains(resp.Reply.Text, "unknown gateway command") {
		t.Fatalf("expected gateway unknown error, got %q", resp.Reply.Text)
	}
}

func TestRuntimeDirectlyExecutesMatewayMemoryShowCommand(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "should not plan", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now"}}},
	}
	workspace := t.TempDir()
	store := memory.NewStore(workspace)
	written, err := store.Propose(memory.ProposalInput{
		AgentID: "main",
		Scope:   "agent",
		Type:    "note",
		Title:   "Review me",
		Body:    "proposal body",
	})
	if err != nil {
		t.Fatal(err)
	}
	rt := Runtime{
		Model:    fp,
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{ProjectRoot: ".", Workspace: workspace},
		MaxSteps: 6,
		Sessions: session.NewFileStore(filepath.Join(t.TempDir(), "sessions")),
		Memory:   store,
	}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{
		Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:cli",
		Text: "mateway memory show " + written.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fp.planCalls != 0 {
		t.Fatalf("expected direct memory show without planning, got plan calls %d", fp.planCalls)
	}
	if !strings.Contains(resp.Reply.Text, "Review me") || !strings.Contains(resp.Reply.Text, "proposal body") {
		t.Fatalf("expected memory show output, got %q", resp.Reply.Text)
	}
	if !strings.Contains(resp.Reply.Text, "Memory item: "+written.ID) || !strings.Contains(resp.Reply.Text, "mateway memory commit --proposal "+written.ID) {
		t.Fatalf("expected actionable show output, got %q", resp.Reply.Text)
	}
}

func TestRuntimeMatewayMemoryCommitRequiresApprovalThenExecutes(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "should not plan", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now"}}},
	}
	workspace := t.TempDir()
	store := memory.NewStore(workspace)
	written, err := store.Propose(memory.ProposalInput{
		AgentID: "main",
		Scope:   "agent",
		Type:    "note",
		Title:   "Guard me",
		Body:    "do not auto commit",
	})
	if err != nil {
		t.Fatal(err)
	}
	rt := Runtime{
		Model:    fp,
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{ProjectRoot: ".", Workspace: workspace},
		MaxSteps: 6,
		Sessions: session.NewFileStore(filepath.Join(t.TempDir(), "sessions")),
		Memory:   store,
	}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{
		Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:cli",
		Text: "mateway memory commit --proposal " + written.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.AwaitConfirm || resp.Reply.Style != "approval_pending" {
		t.Fatalf("expected approval pending, got %#v", resp)
	}
	if fp.planCalls != 0 {
		t.Fatalf("expected guarded direct path without planning, got plan calls %d", fp.planCalls)
	}
	if !strings.Contains(resp.Reply.Text, "执行前需要你确认") {
		t.Fatalf("expected approval prompt, got %q", resp.Reply.Text)
	}
	item, err := store.Show("main", written.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(item.Text, "status: proposed") {
		t.Fatalf("expected proposal to remain proposed, got %q", item.Text)
	}
	resp, err = rt.Handle(context.Background(), channel.InboundMessage{
		Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:cli",
		Text: "确认",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.AwaitConfirm || resp.Failed {
		t.Fatalf("expected approved commit to run, got %#v", resp)
	}
	if !strings.Contains(resp.Reply.Text, "Memory committed:") {
		t.Fatalf("expected commit output, got %q", resp.Reply.Text)
	}
	item, err = store.Show("main", written.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(item.Text, "status: committed") {
		t.Fatalf("expected proposal to become committed, got %q", item.Text)
	}
	longItems, err := store.List(memory.ListOptions{AgentID: "main", Area: "long", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range longItems {
		if strings.Contains(entry.Title, "Guard me") && entry.Kind == "note" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected committed memory entry in long memory list, got %#v", longItems)
	}
}

func TestRuntimeMatewayMemoryRejectRequiresApprovalThenExecutes(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "should not plan", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now"}}},
	}
	workspace := t.TempDir()
	store := memory.NewStore(workspace)
	written, err := store.Propose(memory.ProposalInput{
		AgentID: "main",
		Scope:   "agent",
		Type:    "note",
		Title:   "Reject me",
		Body:    "reject body",
	})
	if err != nil {
		t.Fatal(err)
	}
	rt := Runtime{
		Model:    fp,
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{ProjectRoot: ".", Workspace: workspace},
		MaxSteps: 6,
		Sessions: session.NewFileStore(filepath.Join(t.TempDir(), "sessions")),
		Memory:   store,
	}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{
		Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:cli",
		Text: "mateway memory reject --proposal " + written.ID + " --reason duplicate",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.AwaitConfirm || resp.Reply.Style != "approval_pending" {
		t.Fatalf("expected approval pending, got %#v", resp)
	}
	resp, err = rt.Handle(context.Background(), channel.InboundMessage{
		Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:cli",
		Text: "同意",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.AwaitConfirm || resp.Failed {
		t.Fatalf("expected approved reject to run, got %#v", resp)
	}
	if !strings.Contains(resp.Reply.Text, "Memory proposal rejected:") {
		t.Fatalf("expected reject output, got %q", resp.Reply.Text)
	}
	item, err := store.Show("main", written.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(item.Text, "status: rejected") {
		t.Fatalf("expected proposal to become rejected, got %q", item.Text)
	}
}

func TestRuntimeDirectlyExecutesReferencedMatewayCommandFromAssistantTurn(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "should not plan", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now"}}},
	}
	workspace := t.TempDir()
	memStore := memory.NewStore(workspace)
	if _, err := memStore.Propose(memory.ProposalInput{
		AgentID: "main",
		Scope:   "agent",
		Type:    "note",
		Title:   "Inbox note",
		Body:    "remember this",
	}); err != nil {
		t.Fatal(err)
	}
	sessStore := session.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now()
	if err := sessStore.Save(session.State{
		SessionKey: "cli:cli",
		RecentTurns: []session.Turn{
			{Role: "assistant", Text: "可以用 `mateway memory list --area inbox --status proposed` 查看。", At: now.Add(-time.Minute)},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{
		Model:    fp,
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{ProjectRoot: ".", Workspace: workspace},
		MaxSteps: 6,
		Sessions: sessStore,
		Memory:   memStore,
	}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{
		Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:cli",
		Text: "执行上一条命令",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fp.planCalls != 0 {
		t.Fatalf("expected referenced command direct execution without planning, got plan calls %d", fp.planCalls)
	}
	if !strings.Contains(resp.Reply.Text, "已执行命令：`mateway memory list --area inbox --status proposed`") {
		t.Fatalf("expected referenced command header, got %q", resp.Reply.Text)
	}
	if !strings.Contains(resp.Reply.Text, "Inbox note") {
		t.Fatalf("expected referenced memory list output, got %q", resp.Reply.Text)
	}
}

func TestRuntimeDirectlyExecutesMatewayScheduleListCommand(t *testing.T) {
	fp := &fakePlanner{
		plan: model.Plan{Summary: "should not plan", Steps: []model.PlanStep{{ID: "s1", Tool: "time.now"}}},
	}
	home := t.TempDir()
	store := schedule.NewStore(home)
	if _, _, err := store.Create(schedule.CreateInput{
		ID:           "daily-sync",
		Title:        "Daily Sync",
		Prompt:       "send summary",
		AgentID:      "main",
		DailyAt:      "09:00",
		Channel:      "cli",
		ThreadID:     "cli",
		UserID:       "local",
		DeliveryMode: "artifact",
	}); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{
		Model:    fp,
		Tools:    tool.NewBuiltinRegistry(),
		ToolCtx:  tool.Context{ProjectRoot: ".", Home: home, Workspace: filepath.Join(home, "workspace")},
		MaxSteps: 6,
		Sessions: session.NewFileStore(filepath.Join(t.TempDir(), "sessions")),
		Memory:   memory.NewStore(filepath.Join(home, "workspace")),
	}
	rt.Logger.Quiet = true
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{
		Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:cli",
		Text: "mateway schedule list",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fp.planCalls != 0 {
		t.Fatalf("expected direct schedule list without planning, got plan calls %d", fp.planCalls)
	}
	if !strings.Contains(resp.Reply.Text, "daily-sync") || !strings.Contains(resp.Reply.Text, "Daily Sync") {
		t.Fatalf("expected schedule list output, got %q", resp.Reply.Text)
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
	fp := &fakePlanner{plan: model.Plan{Summary: "ask schedule fields", Steps: []model.PlanStep{{
		ID: "s1", Tool: "user.ask", Args: map[string]string{"question": "这个任务每天几点运行？"},
	}}}}
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
	if fp.planCalls != 1 {
		t.Fatalf("expected schedule request to go through planning, got %d calls", fp.planCalls)
	}
	st, err := rt.Sessions.Load("cli:schedule")
	if err != nil {
		t.Fatal(err)
	}
	task := session.ActiveTask(st)
	if task == nil || task.Status != session.TaskAwaitUserInput {
		t.Fatalf("expected awaiting schedule task, got %#v", st)
	}
	if task.PendingApproval != nil {
		t.Fatalf("expected planner-first user input, got pending approval %#v", task.PendingApproval)
	}
}

func TestRuntimeScheduleRequestUsesPlannerToolToCreateTask(t *testing.T) {
	home := t.TempDir()
	fp := &fakePlanner{plan: model.Plan{Summary: "create schedule", Steps: []model.PlanStep{{
		ID:   "s1",
		Tool: "schedule.create",
		Args: map[string]string{
			"id":       "ai-trends",
			"title":    "AI Trends",
			"prompt":   "Collect AI trend articles.",
			"daily_at": "09:00",
		},
		ExpectedEvidence: []string{"schedule task id and path"},
	}}}}
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
	if resp.AwaitUserInput || resp.AwaitConfirm || resp.Failed {
		t.Fatalf("expected schedule create through planner tool, got %#v", resp)
	}
	if fp.planCalls != 1 {
		t.Fatalf("expected schedule request to go through planning, got %d calls", fp.planCalls)
	}
	tasks, err := schedule.NewStore(home).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != "ai-trends" || tasks[0].Status != schedule.StatusActive {
		t.Fatalf("expected active schedule task, got %#v", tasks)
	}
	if len(resp.Results) != 1 || resp.Results[0].Tool != "schedule.create" || !resp.Results[0].OK {
		t.Fatalf("expected schedule.create result, got %#v", resp.Results)
	}
}

func TestRuntimeScheduleDeleteRequiresConfirmation(t *testing.T) {
	home := t.TempDir()
	store := schedule.NewStore(home)
	if _, _, err := store.Create(schedule.CreateInput{ID: "ai-trends", Title: "AI Trends", Prompt: "Collect AI trends.", DailyAt: "09:00"}); err != nil {
		t.Fatal(err)
	}
	fp := &fakePlanner{plan: model.Plan{Summary: "delete schedule", Steps: []model.PlanStep{{
		ID: "s1", Tool: "schedule.delete", Args: map[string]string{"id": "ai-trends"},
	}}}}
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
	msg := channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:schedule-delete", Text: "删除 ai-trends 定时任务"}
	resp, err := rt.Handle(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.AwaitConfirm || resp.Reply.Style != "approval_pending" {
		t.Fatalf("expected approval pending, got %#v", resp)
	}
	if fp.planCalls != 1 {
		t.Fatalf("expected delete request to go through planning, got %d calls", fp.planCalls)
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
	if resp.AwaitConfirm || resp.Failed {
		t.Fatalf("expected confirmed delete to complete, got %#v", resp)
	}
	tasks, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected task deleted, got %#v", tasks)
	}
}

func TestRuntimeSchedulePauseAndResumeUsePlannerToolsWithoutConfirmation(t *testing.T) {
	home := t.TempDir()
	store := schedule.NewStore(home)
	if _, _, err := store.Create(schedule.CreateInput{ID: "ai-trends", Title: "AI Trends", Prompt: "Collect AI trends.", DailyAt: "09:00"}); err != nil {
		t.Fatal(err)
	}
	fp := &fakePlanner{plan: model.Plan{Summary: "pause schedule", Steps: []model.PlanStep{{
		ID: "s1", Tool: "schedule.pause", Args: map[string]string{"id": "ai-trends"},
	}}}}
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
	msg := channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:schedule-pause", Text: "暂停 ai-trends 定时任务"}
	resp, err := rt.Handle(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if resp.AwaitConfirm || resp.Failed {
		t.Fatalf("expected pause without confirmation, got %#v", resp)
	}
	task, _, err := store.Show("ai-trends")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != schedule.StatusPaused {
		t.Fatalf("expected paused, got %#v", task)
	}
	fp.plan = model.Plan{Summary: "resume schedule", Steps: []model.PlanStep{{
		ID: "s1", Tool: "schedule.resume", Args: map[string]string{"id": "ai-trends"},
	}}}
	resume := msg
	resume.SessionKey = "cli:schedule-resume"
	resume.ID = "resume"
	resume.Text = "恢复 ai-trends 定时任务"
	resp, err = rt.Handle(context.Background(), resume)
	if err != nil {
		t.Fatal(err)
	}
	if resp.AwaitConfirm || resp.Failed {
		t.Fatalf("expected resume without confirmation, got %#v", resp)
	}
	task, _, err = store.Show("ai-trends")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != schedule.StatusActive {
		t.Fatalf("expected active, got %#v", task)
	}
}

func TestRuntimeScheduleUpdateUsesPlannerToolWithoutConfirmation(t *testing.T) {
	home := t.TempDir()
	store := schedule.NewStore(home)
	if _, _, err := store.Create(schedule.CreateInput{ID: "ai-trends", Title: "AI Trends", Prompt: "Collect AI trends.", DailyAt: "09:00"}); err != nil {
		t.Fatal(err)
	}
	fp := &fakePlanner{plan: model.Plan{Summary: "update schedule", Steps: []model.PlanStep{{
		ID: "s1", Tool: "schedule.update", Args: map[string]string{"id": "ai-trends", "daily_at": "10:00"},
	}}}}
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
	msg := channel.InboundMessage{Channel: "cli", ThreadID: "cli", UserID: "local", SessionKey: "cli:schedule-update", Text: "把 ai-trends 定时任务改成 10点"}
	resp, err := rt.Handle(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if resp.AwaitConfirm || resp.Failed {
		t.Fatalf("expected update without confirmation, got %#v", resp)
	}
	task, _, err := store.Show("ai-trends")
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
	if reply.Text != "继续之前需要你确认。" {
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
	if reply.Text != "完成。" {
		t.Fatalf("unexpected fallback %q", reply.Text)
	}
}

func TestDefaultSanitizerStripsToolDebugBlocks(t *testing.T) {
	s := DefaultSanitizer{}
	reply := s.Sanitize(channel.OutboundMessage{
		Style: "reply",
		Text:  "没找到结果，换个关键词再试试。\n\nTool: skill.search (step-2)\n\n---\n\n```json\n{\"query\":\"text humanizer\"}\n```\n\n---\n\n```\n[{\"step_id\":\"step-2\",\"tool\":\"skill.search\"}]\n```\n\n可以再试这些关键词：rewriting、tone、copy editing。",
	})
	if strings.Contains(reply.Text, "Tool:") || strings.Contains(reply.Text, "step-2") || strings.Contains(reply.Text, "skill.search") || strings.Contains(reply.Text, "```") {
		t.Fatalf("expected tool debug block stripped, got %q", reply.Text)
	}
	if !strings.Contains(reply.Text, "可以再试这些关键词") {
		t.Fatalf("expected user-facing guidance preserved, got %q", reply.Text)
	}
}

func TestDefaultSanitizerStripsToolCodeBlocks(t *testing.T) {
	s := DefaultSanitizer{}
	reply := s.Sanitize(channel.OutboundMessage{
		Style: "reply",
		Text:  "搜索超时失败了，文档内容还是空的占位符。让我重新搜索 AI 开源课程，并更新文档内容。\n\n1. **重新搜索最新 AI 开源课程：**\n<tool_code>\ntool: web.search\nargs: {\n  --query: \"2025 2026 best free AI machine learning courses GitHub fast.ai DeepLearning.AI popular\"\n}\n</tool_code>\n\n接下来我会继续整理一版更干净的结果。",
	})
	if strings.Contains(reply.Text, "<tool_code>") || strings.Contains(reply.Text, "tool: web.search") || strings.Contains(reply.Text, "--query") {
		t.Fatalf("expected tool_code block stripped, got %q", reply.Text)
	}
	if !strings.Contains(reply.Text, "搜索超时失败了") || !strings.Contains(reply.Text, "接下来我会继续整理一版更干净的结果。") {
		t.Fatalf("expected user-facing narrative preserved, got %q", reply.Text)
	}
}

func TestRuntimeFailureHidesModelTransportError(t *testing.T) {
	rt := Runtime{}
	resp := rt.failure(channel.InboundMessage{Channel: "cli", ID: "x"}, nil, nil, fmt.Errorf(`plan failed: Post "https://api.minimaxi.com/anthropic/v1/messages": unexpected EOF`))
	if strings.Contains(resp.Reply.Text, "api.minimaxi.com") || strings.Contains(resp.Reply.Text, "unexpected EOF") {
		t.Fatalf("expected transport detail hidden, got %q", resp.Reply.Text)
	}
	if !strings.Contains(resp.Reply.Text, "模型服务") {
		t.Fatalf("expected user-facing model service error, got %q", resp.Reply.Text)
	}
}
