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

func TestSoftwareSearchQueriesPreferOfficialLarkCLI(t *testing.T) {
	got := softwareSearchQueries("飞书的cli")
	if len(got) == 0 || got[0] != "larksuite cli" {
		t.Fatalf("expected larksuite cli first, got %#v", got)
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

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
