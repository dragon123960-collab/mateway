package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dongping/mateway/internal/agentprofile"
	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/runtime"
	"github.com/dongping/mateway/internal/schedule"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/skill"
)

func TestTestCaseMessage(t *testing.T) {
	msg, err := testCaseMessage("read-readme")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "README.md") {
		t.Fatalf("message = %q", msg)
	}
}

func TestTestCaseWriteFileUsesConfiguredHome(t *testing.T) {
	home := t.TempDir()
	msg, err := testCaseMessage("write-file", &config.Root{App: config.AppConfig{Home: home}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(msg, "/write ") || !strings.Contains(msg, filepath.Join(home, "tmp", "mateway-test-write.txt")) {
		t.Fatalf("message = %q", msg)
	}
}

func TestTestCaseCustomRequiresMessage(t *testing.T) {
	if _, err := testCaseMessage("custom"); err == nil {
		t.Fatal("expected custom case to require --message")
	}
}

func TestWriteTestRecord(t *testing.T) {
	cwd := t.TempDir()
	old, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	path, err := writeTestRecord("read-readme", "test:one", "hello", runtime.Response{Reply: channel.OutboundMessage{Text: "ok"}, TracePath: "/tmp/trace.jsonl"}, map[string]any{"ok": true}, []testInteraction{{Message: "hello", Response: runtime.Response{Reply: channel.OutboundMessage{Text: "ok"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, "testdata/runs/") {
		t.Fatalf("path = %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"session": "test:one"`) {
		t.Fatalf("record = %s", data)
	}
	if !strings.Contains(string(data), `"trace_path": "/tmp/trace.jsonl"`) {
		t.Fatalf("record missing trace path = %s", data)
	}
	if !strings.Contains(string(data), `"interactions"`) {
		t.Fatalf("record missing interactions = %s", data)
	}
}

func TestInitSupportsHomeFlag(t *testing.T) {
	home := t.TempDir()
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		filepath.Join("config", "config.yaml"),
		filepath.Join("workspace", "skills", "software-install", "SKILL.md"),
		filepath.Join("workspace", "skills", "connector-gap", "SKILL.md"),
		filepath.Join("workspace", "skills", "skillcreate", "SKILL.md"),
	} {
		if _, err := os.Stat(filepath.Join(home, rel)); err != nil {
			t.Fatalf("expected %s under init home: %v", rel, err)
		}
	}
}

func TestInitSupportsAssetsDirFlag(t *testing.T) {
	assets := copyMainTestInitAssets(t)
	if err := os.WriteFile(filepath.Join(assets, "config", "README.md"), []byte("# CLI Custom Assets\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := run([]string{"init", "--home", home, "--assets-dir", assets}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, "config", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "# CLI Custom Assets\n" {
		t.Fatalf("expected custom asset content, got %q", string(data))
	}
}

func TestToolsDisableAcceptsAgentFlagAfterToolName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"tools", "disable", "terminal.run", "--agent", "main"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, "config", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "terminal.run") {
		t.Fatalf("expected config to include disabled tool:\n%s", data)
	}
}

func copyMainTestInitAssets(t *testing.T) string {
	t.Helper()
	source := filepath.Join("..", "..", "assets", "init")
	target := filepath.Join(t.TempDir(), "init")
	if err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		dst := filepath.Join(target, rel)
		if entry.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	}); err != nil {
		t.Fatalf("copy init assets: %v", err)
	}
	return target
}

func TestHelpIncludesSend(t *testing.T) {
	var out bytes.Buffer
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	printHelp()
	_ = w.Close()
	os.Stdout = old
	_, _ = out.ReadFrom(r)
	if !strings.Contains(out.String(), "mateway send --to <channel:target> <message>") {
		t.Fatalf("help missing send:\n%s", out.String())
	}
}

func TestMemoryLintCommandUsesHomeConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	oldStdout := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = write
	err = run([]string{"memory", "lint"})
	_ = write.Close()
	os.Stdout = oldStdout
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := out.ReadFrom(read); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "memory_root:") || !strings.Contains(text, "issues: 0") {
		t.Fatalf("unexpected lint output:\n%s", text)
	}
}

func TestMemoryIndexRebuildCommandWritesIndex(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	oldStdout := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = write
	err = run([]string{"memory", "index", "rebuild"})
	_ = write.Close()
	os.Stdout = oldStdout
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := out.ReadFrom(read); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "entries: 1") || !strings.Contains(text, "memory_index.json") {
		t.Fatalf("unexpected index output:\n%s", text)
	}
	if _, err := os.Stat(filepath.Join(home, "indexes", "memory_index.json")); err != nil {
		t.Fatalf("expected index file: %v", err)
	}
}

func TestMemorySearchCommandPrintsResults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "workspace", "memory", "user", "long", "style.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`---
type: preference
scope: user
visibility: private
status: active
sources:
  - trace:style
confidence: high
created_at: 2026-05-29
updated_at: 2026-05-29
schema_version: 1
---
Prefer concise answers with source references.
`), 0o644); err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = write
	err = run([]string{"memory", "search", "--scope", "user", "concise"})
	_ = write.Close()
	os.Stdout = oldStdout
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := out.ReadFrom(read); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "results: 1") || !strings.Contains(text, "user/long/style.md") || !strings.Contains(text, "trace:style") {
		t.Fatalf("unexpected search output:\n%s", text)
	}
}

func TestMemoryProposalCreateListRejectCommands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"memory", "proposal", "create", "--title", "README inspection", "--body", "Use file.read for README.", "--source", "trace:abc"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(home, "observe", "proposals"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d", len(entries))
	}
	id := strings.TrimSuffix(entries[0].Name(), filepath.Ext(entries[0].Name()))
	oldStdout := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = write
	err = run([]string{"memory", "proposal", "list"})
	_ = write.Close()
	os.Stdout = oldStdout
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := out.ReadFrom(read); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "README inspection") || !strings.Contains(out.String(), "status=proposed") {
		t.Fatalf("unexpected proposal list:\n%s", out.String())
	}
	if err := run([]string{"memory", "proposal", "reject", "--reason", "skip", id}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, "observe", "proposals", id+".md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "status: rejected") {
		t.Fatalf("expected rejected proposal:\n%s", data)
	}
}

func TestMemoryProposalRejectAcceptsReasonAfterID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"memory", "proposal", "create", "--title", "README inspection", "--body", "Use file.read for README.", "--source", "trace:abc"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(home, "observe", "proposals"))
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSuffix(entries[0].Name(), filepath.Ext(entries[0].Name()))
	if err := run([]string{"memory", "proposal", "reject", id, "--reason", "skip"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, "observe", "proposals", id+".md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "status: rejected") || !strings.Contains(string(data), "Rejected reason: skip") {
		t.Fatalf("expected rejected proposal:\n%s", data)
	}
}

func TestMemoryProposalCommitCommandWritesMemory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"memory", "proposal", "create", "--title", "README Inspection", "--body", "Use file.read for README.", "--source", "trace:abc"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(home, "observe", "proposals"))
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSuffix(entries[0].Name(), filepath.Ext(entries[0].Name()))
	if err := run([]string{"memory", "proposal", "commit", id}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "workspace", "memory", "agents", "main", "experiences", "readme-inspection.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "status: active") || !strings.Contains(string(data), "trace:abc") {
		t.Fatalf("unexpected memory:\n%s", data)
	}
}

func TestMemoryProposalShowCommandPrintsDetail(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"memory", "proposal", "create", "--title", "README Inspection", "--body", "Use file.read for README.", "--source", "trace:abc", "--confidence", "medium"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(home, "observe", "proposals"))
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSuffix(entries[0].Name(), filepath.Ext(entries[0].Name()))
	out := captureStdout(t, func() error { return run([]string{"memory", "proposal", "show", id}) })
	for _, want := range []string{"proposal: " + id, "confidence: medium", "sources:", "why:", "Use file.read for README.", "actions:", "mateway memory proposal commit " + id} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in proposal detail:\n%s", want, out)
		}
	}
}

func TestAgentProfileProposalCommands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "workspace", "agents", "main", "user.md")
	store := agentprofile.NewStore(cfg)
	proposal, err := store.Create(agentprofile.CreateInput{TargetPath: target, NewContent: "# user\n\nNew preference."})
	if err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() error { return run([]string{"agent-profile", "proposal", "list"}) })
	if !strings.Contains(out, proposal.ID) || !strings.Contains(out, "status=proposed") {
		t.Fatalf("unexpected list output:\n%s", out)
	}
	out = captureStdout(t, func() error { return run([]string{"agent-profile", "proposal", "show", proposal.ID}) })
	if !strings.Contains(out, "diff:") || !strings.Contains(out, "New preference") {
		t.Fatalf("unexpected show output:\n%s", out)
	}
	if err := run([]string{"agent-profile", "proposal", "promote", proposal.ID}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "New preference") {
		t.Fatalf("target not promoted:\n%s", data)
	}
}

func TestAgentProfileProposalRejectCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "workspace", "agents", "main", "tools.md")
	store := agentprofile.NewStore(cfg)
	proposal, err := store.Create(agentprofile.CreateInput{TargetPath: target, NewContent: "# tools\n\nUpdated."})
	if err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"agent-profile", "proposal", "reject", proposal.ID, "--reason", "skip"}); err != nil {
		t.Fatal(err)
	}
	rejected, err := store.Read(proposal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Status != "rejected" || rejected.Reason != "skip" {
		t.Fatalf("unexpected rejected proposal: %#v", rejected)
	}
}

func TestMemoryDistillSessionCommandWritesReflection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	store := session.NewStore(home)
	state := session.State{
		Key: "cli:test",
		Tasks: []session.TaskNode{
			{ID: "task-1", Goal: "未完成任务", Status: "running"},
		},
	}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"memory", "distill", "session", "cli:test"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(home, "observe", "reflections"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d", len(entries))
	}
	data, err := os.ReadFile(filepath.Join(home, "observe", "reflections", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "task-1 running") {
		t.Fatalf("unexpected distill:\n%s", data)
	}
	loaded, err := store.Load("cli:test")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Tasks[0].Status != "running" {
		t.Fatalf("distill should not complete task: %#v", loaded.Tasks)
	}
}

func TestMemoryDistillProjectCloseCommandWritesReflection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "workspace", "memory", "projects", "demo", "decisions", "architecture.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`---
type: decision
scope: project
project_id: demo
visibility: private
status: active
sources:
  - trace:demo
confidence: high
created_at: 2026-05-29
updated_at: 2026-05-29
schema_version: 1
---
Use hook-first runtime boundaries.
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"memory", "distill", "project", "close", "demo"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(home, "observe", "reflections"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d", len(entries))
	}
	data, err := os.ReadFile(filepath.Join(home, "observe", "reflections", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "project_id: demo") || !strings.Contains(string(data), "hook-first runtime") {
		t.Fatalf("unexpected project distill:\n%s", data)
	}
}

func TestMemoryHeartbeatLintIndexCommandWritesIndex(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"memory", "heartbeat", "lint-index"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "indexes", "memory_index.json")); err != nil {
		t.Fatalf("expected index: %v", err)
	}
	audit, err := os.ReadFile(filepath.Join(home, "observe", "audit", "memory.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(audit), "memory_heartbeat") {
		t.Fatalf("missing heartbeat audit:\n%s", audit)
	}
}

func TestMemoryHeartbeatDistillCommandNoModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	writeMainTestFile(t, filepath.Join(home, "observe", "diary", "one.md"), "# Task diary\n\n- Goal: 记住 README 检查流程\n")
	out := captureStdout(t, func() error { return run([]string{"memory", "heartbeat", "distill"}) })
	if !strings.Contains(out, "distill_scanned: 1") || !strings.Contains(out, "distill_skipped: 1") {
		t.Fatalf("unexpected distill output:\n%s", out)
	}
	audit, err := os.ReadFile(filepath.Join(home, "observe", "audit", "memory.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(audit), "memory_distill_model_error") {
		t.Fatalf("missing distill audit:\n%s", audit)
	}
}

func TestPrintSkillProposalSummariesShowsTargetAndReason(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	target := filepath.Join(workspace, "skills", "demo", "SKILL.md")
	store := skill.ProposalStore{Home: home, Workspace: workspace}
	proposal, err := store.Create(skill.CreateProposalInput{
		TargetPath: target,
		NewContent: "---\nname: demo\n---\n# Demo\n\nNew guidance.\n",
		Reason:     "Repeated workflow.",
		Sources:    []string{"observe/learning/events.jsonl:1"},
		ModelRole:  "memory_distill",
	})
	if err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() error {
		printSkillProposalSummaries(store, []string{proposal.ID})
		return nil
	})
	if !strings.Contains(out, "skill_proposal_target: "+target) || !strings.Contains(out, "skill_proposal_reason: Repeated workflow.") || !strings.Contains(out, "skill_proposal_summary:") {
		t.Fatalf("unexpected skill proposal summary:\n%s", out)
	}
}

func TestMemoryReportCommandPrintsReadOnlySummary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = write
	err = run([]string{"memory", "report"})
	_ = write.Close()
	os.Stdout = oldStdout
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := out.ReadFrom(read); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"memory_root:", "memory_files:", "index_entries:", "observe:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("report missing %q:\n%s", want, text)
		}
	}
}

func TestSkillListSearchInstallCommands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(source, []byte("---\nname: Demo Skill\ndescription: Demo install.\n---\n# Demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"skill", "install", source}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "workspace", "skills", "demo-skill", "SKILL.md")); err != nil {
		t.Fatalf("expected installed skill: %v", err)
	}

	out := captureStdout(t, func() error { return run([]string{"skill", "list"}) })
	if !strings.Contains(out, "demo-skill") || !strings.Contains(out, "software-install") {
		t.Fatalf("unexpected skill list:\n%s", out)
	}
	out = captureStdout(t, func() error { return run([]string{"skill", "search", "--all", "software install"}) })
	if !strings.Contains(out, "skills.sh") || !strings.Contains(out, "skillhub.cn") || !strings.Contains(out, "clawhub.ai") || !strings.Contains(out, "adapter=search_url_only") {
		t.Fatalf("unexpected skill search:\n%s", out)
	}
	out = captureStdout(t, func() error { return run([]string{"skill", "catalog", "report"}) })
	if !strings.Contains(out, "skill_catalogs:") || !strings.Contains(out, "can_install=false") {
		t.Fatalf("unexpected catalog report:\n%s", out)
	}
}

func TestSandboxAndWorkspaceReports(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() error { return run([]string{"sandbox", "report"}) })
	if !strings.Contains(out, "sandbox_enabled:") || !strings.Contains(out, "timeout_seconds:") {
		t.Fatalf("unexpected sandbox report:\n%s", out)
	}
	out = captureStdout(t, func() error { return run([]string{"workspace", "report"}) })
	if !strings.Contains(out, "workspace:") || !strings.Contains(out, "skills:") || strings.Contains(out, "scripts:") {
		t.Fatalf("unexpected workspace report:\n%s", out)
	}
}

func TestDoctorReportsConfigToolsAndSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() error { return run([]string{"doctor"}) })
	for _, want := range []string{"OK\tconfig_load", "OK\ttools", "OK\tskills", "summary\t"} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "script.run") {
		t.Fatalf("doctor should not report stale script tooling for fresh init:\n%s", out)
	}
}

func TestDoctorWarnsOnStaleSkillGuidance(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "workspace", "skills", "stale", "SKILL.md")
	writeMainTestFile(t, path, "---\nname: stale\n---\n# Stale\n\nUse script.run for this task.\n")
	out := captureStdout(t, func() error { return run([]string{"doctor"}) })
	if !strings.Contains(out, "WARN\tskill.stale_tooling") || !strings.Contains(out, path) {
		t.Fatalf("doctor did not warn about stale skill:\n%s", out)
	}
}

func TestDoctorAllowsExternalSkillWithMatewayMetadata(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "workspace", "skills", "external", "SKILL.md")
	writeMainTestFile(t, path, "---\nname: external\nallowed-tools: Bash(external:*)\n---\n# External\n")
	writeMainTestFile(t, filepath.Join(home, "workspace", "skills", "external", ".mateway", "metadata.yaml"), `adapter_version: "2"
source: "external"
installed_at: "2026-06-17T00:00:00Z"
tool_runtime: "mateway"
graph:
  mode: "adapted"
  type: "prompt"
  stage: "execution"
  granularity: "subtask"
`)
	out := captureStdout(t, func() error { return run([]string{"doctor"}) })
	if strings.Contains(out, "WARN\tskill.external_metadata_missing") {
		t.Fatalf("doctor should accept external metadata:\n%s", out)
	}
	if !strings.Contains(out, "OK\tskill.metadata") {
		t.Fatalf("doctor should report metadata:\n%s", out)
	}
}

func TestAgentCommandsCreateBindReport(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() error { return run([]string{"agent", "list"}) })
	if !strings.Contains(out, "main") {
		t.Fatalf("unexpected agent list:\n%s", out)
	}
	if err := run([]string{"agent", "create", "ops", "--name", "Ops Agent"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"agent.md", "soul.md", "user.md", "tools.md", "memory.md"} {
		if _, err := os.Stat(filepath.Join(home, "workspace", "agents", "ops", name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	if err := run([]string{"agent", "bind", "--channel", "feishu", "--peer-id", "chat-ops", "ops"}); err != nil {
		t.Fatal(err)
	}
	out = captureStdout(t, func() error { return run([]string{"agent", "report", "ops"}) })
	if !strings.Contains(out, "agent: ops") || !strings.Contains(out, "peer_id=chat-ops") || !strings.Contains(out, "issues: 0") {
		t.Fatalf("unexpected agent report:\n%s", out)
	}
	out = captureStdout(t, func() error { return run([]string{"agent", "unbind", "--channel", "feishu", "--peer-id", "chat-ops"}) })
	if !strings.Contains(out, "removed: true") {
		t.Fatalf("unexpected unbind:\n%s", out)
	}
}

func TestSecretCommandsStoreAndListWithoutValue(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"secret", "set", "mail.smtp_pass", "supersecret123"}); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() error { return run([]string{"secret", "list"}) })
	if !strings.Contains(out, "mail.smtp_pass") {
		t.Fatalf("missing secret id:\n%s", out)
	}
	if strings.Contains(out, "supersecret123") {
		t.Fatalf("secret value leaked in list:\n%s", out)
	}
	out = captureStdout(t, func() error { return run([]string{"secret", "get", "mail.smtp_pass"}) })
	if strings.TrimSpace(out) != "supersecret123" {
		t.Fatalf("unexpected get output %q", out)
	}
}

func TestChannelListReadsRuntimeChannelConfigs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	channelsDir := filepath.Join(home, "config", "channels")
	writeMainTestFile(t, filepath.Join(channelsDir, "feishu.yaml"), "feishu:\n  enabled: false\n")
	writeMainTestFile(t, filepath.Join(channelsDir, "weixin.yaml"), "weixin:\n  enabled: true\n")
	writeMainTestFile(t, filepath.Join(channelsDir, "telegram.sample.yaml"), "telegram:\n  enabled: true\n")
	out := captureStdout(t, func() error { return run([]string{"channel", "list"}) })
	if !strings.Contains(out, "feishu") || !strings.Contains(out, "false") {
		t.Fatalf("expected feishu disabled in channel list:\n%s", out)
	}
	if !strings.Contains(out, "weixin") || !strings.Contains(out, "true") {
		t.Fatalf("expected weixin enabled in channel list:\n%s", out)
	}
	if strings.Contains(out, "telegram") {
		t.Fatalf("sample channel should be skipped:\n%s", out)
	}
}

func TestHomeReportCommandClassifiesDirectories(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "docker"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "mystery"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() error { return run([]string{"home", "report"}) })
	if !strings.Contains(out, "- workspace: agent workspace, memory, skills") {
		t.Fatalf("missing expected workspace:\n%s", out)
	}
	if !strings.Contains(out, "- docker: legacy/local service data") {
		t.Fatalf("missing local docker:\n%s", out)
	}
	if !strings.Contains(out, "- mystery: not recognized by current clean layout") {
		t.Fatalf("missing unknown dir:\n%s", out)
	}
}

func TestHomeResetRuntimeDryRunPreservesFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	writeMainTestFile(t, filepath.Join(home, "sessions", "one.json"), "{}")
	writeMainTestFile(t, filepath.Join(home, "trace", "one.jsonl"), "{}")
	writeMainTestFile(t, filepath.Join(home, "secrets", "secrets.json"), "{}")
	out := captureStdout(t, func() error { return run([]string{"home", "reset-runtime"}) })
	if !strings.Contains(out, "mode: dry-run") || !strings.Contains(out, "sessions: would_remove") {
		t.Fatalf("unexpected dry-run output:\n%s", out)
	}
	for _, rel := range []string{
		filepath.Join("sessions", "one.json"),
		filepath.Join("trace", "one.jsonl"),
		filepath.Join("secrets", "secrets.json"),
	} {
		if _, err := os.Stat(filepath.Join(home, rel)); err != nil {
			t.Fatalf("dry-run should preserve %s: %v", rel, err)
		}
	}
}

func TestHomeResetRuntimeApplyRemovesGeneratedStateOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	writeMainTestFile(t, filepath.Join(home, "sessions", "one.json"), "{}")
	writeMainTestFile(t, filepath.Join(home, "trace", "one.jsonl"), "{}")
	writeMainTestFile(t, filepath.Join(home, "observe", "audit", "memory.jsonl"), "{}")
	writeMainTestFile(t, filepath.Join(home, "indexes", "memory_index.json"), "{}")
	writeMainTestFile(t, filepath.Join(home, "schedules", "task.json"), "{}")
	writeMainTestFile(t, filepath.Join(home, "artifacts", "tool-results", "aa", "raw.txt"), "raw")
	writeMainTestFile(t, filepath.Join(home, "logs", "service.log"), "log")
	writeMainTestFile(t, filepath.Join(home, "tmp", "scratch.txt"), "tmp")
	writeMainTestFile(t, filepath.Join(home, "run", "mateway.lock"), "lock")
	writeMainTestFile(t, filepath.Join(home, "run", "weixin", "accounts", "acct.json"), "{}")
	writeMainTestFile(t, filepath.Join(home, "secrets", "secrets.json"), "{}")
	out := captureStdout(t, func() error { return run([]string{"home", "reset-runtime", "--apply"}) })
	if !strings.Contains(out, "mode: apply") || !strings.Contains(out, "sessions: removed") {
		t.Fatalf("unexpected apply output:\n%s", out)
	}
	for _, rel := range []string{
		"sessions",
		"trace",
		"observe",
		"indexes",
		"schedules",
		filepath.Join("artifacts", "tool-results"),
		"logs",
		"tmp",
		filepath.Join("run", "mateway.lock"),
	} {
		if _, err := os.Stat(filepath.Join(home, rel)); !os.IsNotExist(err) {
			t.Fatalf("expected %s removed, err=%v", rel, err)
		}
	}
	for _, rel := range []string{
		filepath.Join("config", "config.yaml"),
		filepath.Join("workspace", "skills", "software-install", "SKILL.md"),
		filepath.Join("secrets", "secrets.json"),
		filepath.Join("run", "weixin", "accounts", "acct.json"),
	} {
		if _, err := os.Stat(filepath.Join(home, rel)); err != nil {
			t.Fatalf("expected %s preserved: %v", rel, err)
		}
	}
}

func TestScheduleRunDueCommandRunsCliTask(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	store := schedule.Store{Home: home}
	_, err := store.Create(schedule.CreateInput{
		SessionKey: "cli:test-schedule",
		Text:       "/read workspace/memory/README.md",
		RunAt:      time.Now().Add(-time.Minute),
		Activate:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() error { return run([]string{"schedule", "run-due"}) })
	if !strings.Contains(out, "due: 1") || !strings.Contains(out, "ran:") {
		t.Fatalf("unexpected schedule output:\n%s", out)
	}
	tasks, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != "done" {
		t.Fatalf("unexpected tasks: %#v", tasks)
	}
}

func TestScheduleCreateAndTestCommands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	runAt := time.Now().Add(time.Hour).Format(time.RFC3339)
	out := captureStdout(t, func() error {
		return run([]string{"schedule", "create", "--run-at", runAt, "/read workspace/memory/README.md"})
	})
	if !strings.Contains(out, "status: pending") || !strings.Contains(out, "mateway schedule test") {
		t.Fatalf("unexpected create output:\n%s", out)
	}
	tasks, err := schedule.Store{Home: home}.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != "pending" {
		t.Fatalf("unexpected tasks: %#v", tasks)
	}
	out = captureStdout(t, func() error { return run([]string{"schedule", "test", tasks[0].ID}) })
	if !strings.Contains(out, "test: success") {
		t.Fatalf("unexpected test output:\n%s", out)
	}
	tasks, err = schedule.Store{Home: home}.List()
	if err != nil {
		t.Fatal(err)
	}
	if tasks[0].Status != "active" || tasks[0].TestedAt == "" {
		t.Fatalf("unexpected tested task: %#v", tasks[0])
	}
}

func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	oldStdout := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = write
	err = fn()
	_ = write.Close()
	os.Stdout = oldStdout
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := out.ReadFrom(read); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func writeMainTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
