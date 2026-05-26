package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dongping/mateway/internal/memory"
)

func TestRunSkillPromote(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	workspace := filepath.Join(home, "workspace")
	t.Setenv("MATEWAY_HOME", home)
	store := memory.NewStore(workspace)
	result, err := store.ProcessTask(memory.TaskOutcome{
		AgentID:     "main",
		TraceID:     "trace-1",
		TaskID:      "task-1",
		Intent:      "review release notes",
		PlanSummary: "review release notes",
		Tools:       []string{"web.search", "file.summary"},
		Success:     true,
		FinishedAt:  time.Now(),
	}, memory.LearningConfig{Enabled: true, SuccessThreshold: 1, RequireUserConfirm: true})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runSkill([]string{"promote", "--proposal", result.CandidatePath, "--name", "release-review"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Skill promoted:") {
		t.Fatalf("expected promote output, got %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(workspace, "skills", "release-review", "SKILL.md")); err != nil {
		t.Fatalf("expected promoted skill file: %v", err)
	}
}
