package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/memory"
	"github.com/dongping/mateway/internal/provisioning"
	"github.com/dongping/mateway/internal/scheduler"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/skills"
)

func TestBuiltinProviderReadWriteAndProvision(t *testing.T) {
	root := t.TempDir()
	store := session.NewStore(root)
	provider := BuiltinProvider{
		Workspace:             root,
		Sessions:              store,
		Memory:                memory.Store{Workspace: root},
		Provisioner:           provisioning.Provisioner{Config: config.Config{App: config.AppConfig{Home: root}}},
		EnforceWorkspacePaths: false,
	}
	tools, err := provider.Tools(context.Background(), Scope{})
	if err != nil {
		t.Fatal(err)
	}
	index := make(map[string]Tool)
	for _, tool := range tools {
		index[tool.Spec().Name] = tool
	}

	writeArgs, _ := json.Marshal(map[string]string{"path": "notes.txt", "content": "hello"})
	if _, err := index["write_file"].Invoke(context.Background(), Call{Arguments: writeArgs}); err != nil {
		t.Fatal(err)
	}
	readArgs, _ := json.Marshal(map[string]string{"path": "notes.txt"})
	res, err := index["read_file"].Invoke(context.Background(), Call{Arguments: readArgs})
	if err != nil {
		t.Fatal(err)
	}
	var content string
	if err := json.Unmarshal(res.Output, &content); err != nil {
		t.Fatal(err)
	}
	if content != "hello" {
		t.Fatalf("content = %q", content)
	}

	createArgs, _ := json.Marshal(map[string]string{"name": "alpha"})
	if _, err := index["create_workspace"].Invoke(context.Background(), Call{Arguments: createArgs}); err != nil {
		t.Fatal(err)
	}
	if _, err := index["create_agent"].Invoke(context.Background(), Call{Arguments: mustJSON(map[string]string{
		"workspace":   filepath.Join(root, "workspaces", "alpha"),
		"name":        "writer",
		"description": "writer agent",
	})}); err != nil {
		t.Fatal(err)
	}

	if _, err := index["schedule_create"].Invoke(context.Background(), Call{Arguments: mustJSON(map[string]any{
		"name":             "daily-report",
		"kind":             "cron",
		"expr":             "0 10 * * *",
		"tz":               "Asia/Shanghai",
		"prompt":           "generate report",
		"interval_minutes": 0,
	})}); err != nil {
		t.Fatal(err)
	}
	res, err = index["schedule_list"].Invoke(context.Background(), Call{Arguments: mustJSON(map[string]any{})})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(res.Output, &content); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "daily-report") || !strings.Contains(content, `cron="0 10 * * *"`) {
		t.Fatalf("unexpected schedule list output: %q", content)
	}
	scheduleHistoryStore := scheduler.Store{Workspace: root}
	dailyReportJob, ok, err := scheduleHistoryStore.Get("daily-report")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected daily-report schedule to exist")
	}
	if err := scheduleHistoryStore.AppendRun(dailyReportJob, "completed", "run_1", "task_1", time.Second, nil); err != nil {
		t.Fatal(err)
	}
	res, err = index["schedule_runs"].Invoke(context.Background(), Call{Arguments: mustJSON(map[string]any{"name": "daily-report"})})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(res.Output, &content); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, `"job_name":"daily-report"`) {
		t.Fatalf("unexpected schedule runs output: %q", content)
	}

	if _, err := index["schedule_update"].Invoke(context.Background(), Call{Arguments: mustJSON(map[string]any{
		"name":     "daily-report",
		"new_name": "daily-plan",
		"expr":     "30 9 * * *",
		"tz":       "Asia/Shanghai",
		"prompt":   "plan today",
	})}); err != nil {
		t.Fatal(err)
	}
	scheduleStore := scheduler.Store{Workspace: root}
	job, ok, err := scheduleStore.Get("daily-plan")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected updated schedule to exist")
	}
	if job.Schedule.Expr != "30 9 * * *" || job.Prompt != "plan today" {
		t.Fatalf("unexpected updated schedule: %#v", job)
	}
	if _, ok, err := scheduleStore.Get("daily-report"); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("expected old schedule name to be replaced")
	}

	if _, err := index["schedule_remove"].Invoke(context.Background(), Call{Arguments: mustJSON(map[string]any{"name": "daily-plan"})}); err != nil {
		t.Fatal(err)
	}
	res, err = index["schedule_list"].Invoke(context.Background(), Call{Arguments: mustJSON(map[string]any{})})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(res.Output, &content); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(content, "daily-plan") {
		t.Fatalf("expected schedule to be removed, got %q", content)
	}

	if _, err := index["schedule_create"].Invoke(context.Background(), Call{SessionKey: "feishu:p2p:u1", AgentName: "writer", Arguments: mustJSON(map[string]any{
		"name":                "follow-up",
		"kind":                "interval",
		"interval_minutes":    60,
		"prompt":              "ping me",
		"target_session_mode": "current",
		"target_agent_mode":   "current",
	})}); err != nil {
		t.Fatal(err)
	}
	job, ok, err = scheduleStore.Get("follow-up")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected follow-up schedule to exist")
	}
	if job.SessionKey != "feishu:p2p:u1" || job.AgentName != "writer" {
		t.Fatalf("unexpected target resolution: %#v", job)
	}
}

func TestCommandNameVariantsSuggestHyphenatedCLI(t *testing.T) {
	variants := commandNameVariants("larkcli")
	joined := strings.Join(variants, ",")
	if !strings.Contains(joined, "lark-cli") {
		t.Fatalf("expected lark-cli variant, got %q", joined)
	}
}

func TestBuiltinProviderAllowsExternalPathsWhenEnforcementDisabled(t *testing.T) {
	root := t.TempDir()
	external := filepath.Join(t.TempDir(), "external.txt")
	if err := os.WriteFile(external, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	provider := BuiltinProvider{
		Workspace:             root,
		Memory:                memory.Store{Workspace: root},
		Provisioner:           provisioning.Provisioner{Config: config.Config{App: config.AppConfig{Home: root}}},
		EnforceWorkspacePaths: false,
	}
	toolsList, err := provider.Tools(context.Background(), Scope{})
	if err != nil {
		t.Fatal(err)
	}
	index := make(map[string]Tool)
	for _, tool := range toolsList {
		index[tool.Spec().Name] = tool
	}
	res, err := index["read_file"].Invoke(context.Background(), Call{Arguments: mustJSON(map[string]string{"path": external})})
	if err != nil {
		t.Fatal(err)
	}
	var content string
	if err := json.Unmarshal(res.Output, &content); err != nil {
		t.Fatal(err)
	}
	if content != "outside" {
		t.Fatalf("unexpected content: %q", content)
	}
}

func TestBuiltinProviderEnforcesWorkspaceAndBlocksDangerousExec(t *testing.T) {
	root := t.TempDir()
	external := filepath.Join(t.TempDir(), "external.txt")
	if err := os.WriteFile(external, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	provider := BuiltinProvider{
		Workspace:             root,
		Memory:                memory.Store{Workspace: root},
		Provisioner:           provisioning.Provisioner{Config: config.Config{App: config.AppConfig{Home: root}}},
		EnforceWorkspacePaths: true,
	}
	toolsList, err := provider.Tools(context.Background(), Scope{})
	if err != nil {
		t.Fatal(err)
	}
	index := make(map[string]Tool)
	for _, tool := range toolsList {
		index[tool.Spec().Name] = tool
	}
	if _, err := index["read_file"].Invoke(context.Background(), Call{Arguments: mustJSON(map[string]string{"path": external})}); err == nil {
		t.Fatal("expected external read to be blocked when enforcement is enabled")
	}
	if _, err := index["exec"].Invoke(context.Background(), Call{Arguments: mustJSON(map[string]string{"command": "rm -rf /tmp/test"})}); err == nil {
		t.Fatal("expected dangerous exec to be blocked")
	}
}

func TestBuiltinProviderSandboxExecCreatesIsolatedDirAndBlocksShellString(t *testing.T) {
	root := t.TempDir()
	provider := BuiltinProvider{
		Workspace:             root,
		Memory:                memory.Store{Workspace: root},
		Provisioner:           provisioning.Provisioner{Config: config.Config{App: config.AppConfig{Home: root}}},
		EnforceWorkspacePaths: false,
	}
	toolsList, err := provider.Tools(context.Background(), Scope{})
	if err != nil {
		t.Fatal(err)
	}
	index := make(map[string]Tool)
	for _, tool := range toolsList {
		index[tool.Spec().Name] = tool
	}

	res, err := index["sandbox_exec"].Invoke(context.Background(), Call{Arguments: mustJSON(map[string]any{
		"command": "pwd",
	})})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(res.Output, &payload); err != nil {
		t.Fatal(err)
	}
	sandboxDir, _ := payload["sandbox_dir"].(string)
	output, _ := payload["output"].(string)
	if sandboxDir == "" || output == "" {
		t.Fatalf("unexpected sandbox payload: %#v", payload)
	}
	resolvedOutput, _ := filepath.EvalSymlinks(filepath.Clean(output))
	resolvedSandbox, _ := filepath.EvalSymlinks(filepath.Clean(sandboxDir))
	if resolvedOutput != resolvedSandbox {
		t.Fatalf("expected pwd output to equal sandbox dir, got output=%q sandbox=%q", output, sandboxDir)
	}
	if _, err := index["sandbox_exec"].Invoke(context.Background(), Call{Arguments: mustJSON(map[string]any{
		"command": "bash",
		"args":    []string{"-lc", "echo hi"},
	})}); err == nil {
		t.Fatal("expected shell string execution to be blocked")
	}

	res, err = index["sandbox_exec"].Invoke(context.Background(), Call{Arguments: mustJSON(map[string]any{
		"command": "definitely-missing-cli-command",
	})})
	if err != nil {
		t.Fatal(err)
	}
	payload = map[string]any{}
	if err := json.Unmarshal(res.Output, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["status"] != "command_not_found" {
		t.Fatalf("expected command_not_found payload, got %#v", payload)
	}
}

type stubSkillRuns struct {
	selected []string
	visible  []string
}

func (s stubSkillRuns) SkillAccessForRun(context.Context, string) ([]string, []string, bool) {
	return append([]string(nil), s.selected...), append([]string(nil), s.visible...), true
}

func TestBuiltinProviderReadSkillResource(t *testing.T) {
	root := t.TempDir()
	skillRoot := filepath.Join(root, "skills")
	dir := filepath.Join(skillRoot, "designer")
	if err := os.MkdirAll(filepath.Join(dir, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(`---
name: designer
description: Design skill
---

# Designer
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "references", "guide.md"), []byte("design guide"), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog := skills.NewCatalog([]string{skillRoot})
	if err := catalog.Refresh(); err != nil {
		t.Fatal(err)
	}
	provider := BuiltinProvider{
		Workspace:             root,
		Memory:                memory.Store{Workspace: root},
		Provisioner:           provisioning.Provisioner{Config: config.Config{App: config.AppConfig{Home: root}}},
		EnforceWorkspacePaths: true,
		SkillCatalog:          catalog,
		SkillRuns:             stubSkillRuns{selected: []string{"designer"}, visible: []string{"designer"}},
	}
	toolsList, err := provider.Tools(context.Background(), Scope{})
	if err != nil {
		t.Fatal(err)
	}
	index := make(map[string]Tool)
	for _, tool := range toolsList {
		index[tool.Spec().Name] = tool
	}
	res, err := index["read_skill_resource"].Invoke(context.Background(), Call{
		RunID:     "run_1",
		Arguments: mustJSON(map[string]string{"skill_name": "designer", "path": "references/guide.md"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	var content string
	if err := json.Unmarshal(res.Output, &content); err != nil {
		t.Fatal(err)
	}
	if content != "design guide" {
		t.Fatalf("unexpected content: %q", content)
	}
}

func TestBuiltinProviderReadSkillResourceBlocksUnselectedSkill(t *testing.T) {
	root := t.TempDir()
	skillRoot := filepath.Join(root, "skills")
	dir := filepath.Join(skillRoot, "designer")
	if err := os.MkdirAll(filepath.Join(dir, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(`---
name: designer
description: Design skill
---

# Designer
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "references", "guide.md"), []byte("design guide"), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog := skills.NewCatalog([]string{skillRoot})
	if err := catalog.Refresh(); err != nil {
		t.Fatal(err)
	}
	provider := BuiltinProvider{
		Workspace:             root,
		Memory:                memory.Store{Workspace: root},
		Provisioner:           provisioning.Provisioner{Config: config.Config{App: config.AppConfig{Home: root}}},
		EnforceWorkspacePaths: true,
		SkillCatalog:          catalog,
		SkillRuns:             stubSkillRuns{selected: []string{"other"}, visible: []string{"designer", "other"}},
	}
	toolsList, err := provider.Tools(context.Background(), Scope{})
	if err != nil {
		t.Fatal(err)
	}
	index := make(map[string]Tool)
	for _, tool := range toolsList {
		index[tool.Spec().Name] = tool
	}
	if _, err := index["read_skill_resource"].Invoke(context.Background(), Call{
		RunID:     "run_1",
		Arguments: mustJSON(map[string]string{"skill_name": "designer", "path": "references/guide.md"}),
	}); err == nil {
		t.Fatal("expected unselected skill resource read to be blocked")
	}
}

func TestBuiltinProviderReadSkillResourceAllowsDeclaredExtraDir(t *testing.T) {
	root := t.TempDir()
	skillRoot := filepath.Join(root, "skills")
	dir := filepath.Join(skillRoot, "designer")
	if err := os.MkdirAll(filepath.Join(dir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(`---
name: designer
description: Design skill
resource_dirs:
  - templates
---

# Designer
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "templates", "landing.md"), []byte("template"), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog := skills.NewCatalog([]string{skillRoot})
	if err := catalog.Refresh(); err != nil {
		t.Fatal(err)
	}
	provider := BuiltinProvider{
		Workspace:             root,
		Memory:                memory.Store{Workspace: root},
		Provisioner:           provisioning.Provisioner{Config: config.Config{App: config.AppConfig{Home: root}}},
		EnforceWorkspacePaths: true,
		SkillCatalog:          catalog,
		SkillRuns:             stubSkillRuns{selected: []string{"designer"}, visible: []string{"designer"}},
	}
	toolsList, err := provider.Tools(context.Background(), Scope{})
	if err != nil {
		t.Fatal(err)
	}
	index := make(map[string]Tool)
	for _, tool := range toolsList {
		index[tool.Spec().Name] = tool
	}
	res, err := index["read_skill_resource"].Invoke(context.Background(), Call{
		RunID:     "run_1",
		Arguments: mustJSON(map[string]string{"skill_name": "designer", "path": "templates/landing.md"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	var content string
	if err := json.Unmarshal(res.Output, &content); err != nil {
		t.Fatal(err)
	}
	if content != "template" {
		t.Fatalf("unexpected content: %q", content)
	}
}

func mustJSON(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}
