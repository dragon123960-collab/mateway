package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/memory"
)

func TestProjectIndexSummarizesDirectory(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "README.md"), "# Demo\nhello\n")
	mustWriteFile(t, filepath.Join(root, "cmd", "main.go"), "package main\n\nfunc main() {}\n")
	mustWriteFile(t, filepath.Join(root, "internal", "tool", "x.go"), "package tool\n")

	result := ProjectIndex().Run(context.Background(), Call{
		Args:    map[string]string{"path": root, "max_depth": "3", "max_files": "20"},
		Context: Context{ProjectRoot: root, Workspace: root},
	})
	if !result.OK {
		t.Fatalf("expected success, got %+v", result)
	}
	if !strings.Contains(result.Output, "Project index") || !strings.Contains(result.Output, "README.md") {
		t.Fatalf("unexpected output %q", result.Output)
	}
	if kind, _ := result.Evidence["kind"].(string); kind != "project_index" {
		t.Fatalf("unexpected evidence %#v", result.Evidence)
	}
}

func TestFileSummarySummarizesTextFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "README.md")
	mustWriteFile(t, path, "# Title\n\n## Usage\nhello\nworld\n")

	result := FileSummary().Run(context.Background(), Call{
		Args:    map[string]string{"path": path, "max_lines": "20"},
		Context: Context{ProjectRoot: root, Workspace: root},
	})
	if !result.OK {
		t.Fatalf("expected success, got %+v", result)
	}
	if !strings.Contains(result.Output, "File summary") || !strings.Contains(result.Output, "## Usage") {
		t.Fatalf("unexpected output %q", result.Output)
	}
	if kind, _ := result.Evidence["kind"].(string); kind != "file_summary" {
		t.Fatalf("unexpected evidence %#v", result.Evidence)
	}
}

func TestFileSummaryRejectsDirectory(t *testing.T) {
	root := t.TempDir()
	result := FileSummary().Run(context.Background(), Call{
		Args:    map[string]string{"path": root},
		Context: Context{ProjectRoot: root, Workspace: root},
	})
	if result.OK || !strings.Contains(result.Error, "requires a file path") {
		t.Fatalf("expected directory error, got %+v", result)
	}
}

func TestSourceQualityHintForFreshSearch(t *testing.T) {
	hint := sourceQualityHint("2026 latest AI courses official")
	if !strings.Contains(hint, "official") || !strings.Contains(hint, "weak evidence") {
		t.Fatalf("expected fresh source quality hint, got %q", hint)
	}
}

func TestSoftwareSearchQueriesStayGeneric(t *testing.T) {
	got := softwareSearchQueries("飞书的cli")
	if len(got) != 1 || got[0] != "飞书的cli" {
		t.Fatalf("expected generic query without product-specific rewrite, got %#v", got)
	}
}

func TestSkillSearchNoResultSuggestsBroaderCapabilityPhrases(t *testing.T) {
	output := renderSkillSearchOutput("text humanizer rewriting assistant AI text", nil)
	if !strings.Contains(output, "Searched priority catalogs") || !strings.Contains(output, "broader capability phrases") {
		t.Fatalf("expected helpful no-result guidance, got %q", output)
	}
}

func TestSoftwareInstallPreviewAndResult(t *testing.T) {
	preview := SoftwareInstall().Run(context.Background(), Call{
		Args: map[string]string{"name": "ripgrep"},
	})
	if preview.OK || !strings.Contains(preview.Error, "install command is required") {
		t.Fatalf("expected missing command error, got %#v", preview)
	}

	preview = SoftwareInstall().Run(context.Background(), Call{
		Args: map[string]string{
			"name":           "larksuite/cli",
			"method":         "npx",
			"command":        "true",
			"executable":     "lark-cli",
			"verify_command": "true",
			"source_url":     "https://github.com/larksuite/cli",
			"source_name":    "upstream",
		},
	})
	if !preview.OK || preview.RequiresConfirm {
		t.Fatalf("expected direct install execution without confirmation, got %#v", preview)
	}

	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "lark-cli")
	mustWriteFile(t, binPath, "#!/bin/sh\nprintf 'lark version test\\n'\n")
	if err := os.Chmod(binPath, 0o755); err != nil {
		t.Fatal(err)
	}
	result := SoftwareInstall().Run(context.Background(), Call{
		Args: map[string]string{
			"name":           "lark",
			"command":        "true",
			"executable":     "lark-cli",
			"verify_command": shellQuote(binPath) + " --version",
		},
		Confirmed: true,
	})
	if !result.OK {
		t.Fatalf("expected install success, got %#v", result)
	}
	if !strings.Contains(result.Output, "安装完成") || !strings.Contains(result.Output, "lark-cli --help") {
		t.Fatalf("expected completion and next commands, got %q", result.Output)
	}
}

func TestTerminalRunReturnsCommandEvidence(t *testing.T) {
	root := t.TempDir()
	result := TerminalRun().Run(context.Background(), Call{
		Args:    map[string]string{"command": "printf hello", "timeout": "5", "purpose": "diagnose local cli"},
		Context: Context{ProjectRoot: root, Workspace: root},
	})
	if !result.OK {
		t.Fatalf("expected terminal success, got %#v", result)
	}
	if !strings.Contains(result.Output, "hello") {
		t.Fatalf("expected stdout in output, got %q", result.Output)
	}
	if result.Evidence["kind"] != "terminal" || result.Evidence["command"] != "printf hello" || result.Evidence["exit_code"] != 0 {
		t.Fatalf("unexpected terminal evidence %#v", result.Evidence)
	}
	if result.Evidence["stdout"] != "hello" || result.Evidence["purpose"] != "diagnose local cli" {
		t.Fatalf("expected stdout and purpose evidence, got %#v", result.Evidence)
	}
}

func TestMemoryToolsSearchAndIndex(t *testing.T) {
	workspace := t.TempDir()
	store := memory.NewStore(workspace)
	proposal, err := store.Propose(memory.ProposalInput{
		AgentID: "main",
		Title:   "Tool Memory",
		Body:    "Runtime tools can search reviewed Markdown memory.",
		Sources: []string{"manual"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Commit(memory.CommitInput{AgentID: "main", Proposal: proposal.ID}); err != nil {
		t.Fatal(err)
	}
	search := MemorySearch().Run(context.Background(), Call{
		Args:    map[string]string{"query": "reviewed Markdown memory"},
		Context: Context{Workspace: workspace},
	})
	if !search.OK {
		t.Fatalf("expected search success, got %#v", search)
	}
	if !strings.Contains(search.Output, "Tool Memory") || !strings.Contains(search.Output, "lines:") {
		t.Fatalf("unexpected search output %q", search.Output)
	}
	index := MemoryIndex().Run(context.Background(), Call{
		Args:    map[string]string{},
		Context: Context{Workspace: workspace},
	})
	if !index.OK {
		t.Fatalf("expected index success, got %#v", index)
	}
	if !strings.Contains(index.Output, "entries=") || !strings.Contains(index.Output, "parsed_sources=") || !strings.Contains(index.Output, "long:") {
		t.Fatalf("unexpected index output %q", index.Output)
	}
}

func TestScheduleToolsConfirmationBoundary(t *testing.T) {
	home := t.TempDir()
	ctx := Context{Home: home, ProjectRoot: home, Workspace: home}
	create := ScheduleCreate().Run(context.Background(), Call{
		Args: map[string]string{
			"id":       "ai-trends",
			"title":    "AI Trends",
			"prompt":   "Collect AI trends.",
			"daily_at": "09:00",
		},
		Context: ctx,
	})
	if !create.OK || create.RequiresConfirm {
		t.Fatalf("expected schedule create without confirmation, got %#v", create)
	}
	update := ScheduleUpdate().Run(context.Background(), Call{
		Args:    map[string]string{"id": "ai-trends", "daily_at": "10:00"},
		Context: ctx,
	})
	if !update.OK || update.RequiresConfirm {
		t.Fatalf("expected schedule update without confirmation, got %#v", update)
	}
	if !RequireConfirmForTool("schedule.delete", map[string]string{"id": "ai-trends"}) {
		t.Fatalf("expected schedule.delete to require confirmation")
	}
	if RequireConfirmForTool("schedule.update", map[string]string{"id": "ai-trends"}) {
		t.Fatalf("expected schedule.update not to require confirmation")
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
