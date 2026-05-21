package memory

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProcessTaskGeneratesSkillCandidateAtThreshold(t *testing.T) {
	store := NewStore(t.TempDir())
	cfg := LearningConfig{Enabled: true, SuccessThreshold: 3, RequireUserConfirm: true}
	outcome := TaskOutcome{
		AgentID:     "main",
		TraceID:     "trace-1",
		TaskID:      "task-1",
		Intent:      "review latest release notes",
		PlanSummary: "review release notes",
		Tools:       []string{"web.search", "file.summary"},
		Success:     true,
		FinishedAt:  time.Now(),
	}

	for i := 0; i < 2; i++ {
		result, err := store.ProcessTask(outcome, cfg)
		if err != nil {
			t.Fatalf("process task: %v", err)
		}
		if result.CandidateGenerated {
			t.Fatalf("candidate generated too early at iteration %d", i+1)
		}
	}
	result, err := store.ProcessTask(outcome, cfg)
	if err != nil {
		t.Fatalf("process task: %v", err)
	}
	if !result.CandidateGenerated {
		t.Fatal("expected candidate at threshold")
	}
	if _, err := os.Stat(result.CandidatePath); err != nil {
		t.Fatalf("expected candidate file: %v", err)
	}
}

func TestLintReportsBrokenLinksAndMissingFrontmatter(t *testing.T) {
	root := t.TempDir()
	longDir := filepath.Join(root, "agents", "main", "long")
	if err := os.MkdirAll(longDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(longDir, "project.md")
	if err := os.WriteFile(path, []byte("# Project\n\nSee [[missing-page]].\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := Lint(root)
	if err != nil {
		t.Fatalf("lint: %v", err)
	}
	codes := map[string]bool{}
	for _, issue := range report.Issues {
		codes[issue.Code] = true
	}
	if !codes["missing_frontmatter"] {
		t.Fatalf("expected missing_frontmatter, got %#v", report.Issues)
	}
	if !codes["missing_sources"] {
		t.Fatalf("expected missing_sources, got %#v", report.Issues)
	}
	if !codes["broken_wikilink"] {
		t.Fatalf("expected broken_wikilink, got %#v", report.Issues)
	}
}
