package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
