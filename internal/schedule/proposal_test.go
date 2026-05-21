package schedule

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreProposalCommitCreatesActiveTask(t *testing.T) {
	home := t.TempDir()
	store := NewStore(home)
	now := time.Date(2026, 5, 21, 9, 0, 0, 0, time.UTC)
	proposal, proposalPath, err := store.Propose(ProposalInput{CreateInput: CreateInput{
		ID:      "ai-trends",
		Title:   "AI Trends",
		Prompt:  "Collect AI trend articles with sources.",
		DailyAt: "09:00",
		Now:     now,
	}})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if proposal.ProposalStatus != ProposalStatusProposed || proposal.Task.Status != StatusPaused {
		t.Fatalf("unexpected proposal %#v", proposal)
	}
	if _, err := os.Stat(proposalPath); err != nil {
		t.Fatalf("expected proposal file: %v", err)
	}
	items, err := store.ListProposals(ProposalStatusProposed)
	if err != nil {
		t.Fatalf("list proposals: %v", err)
	}
	if len(items) != 1 || items[0].ID != "ai-trends" {
		t.Fatalf("unexpected proposals %#v", items)
	}
	task, taskPath, err := store.CommitProposal("ai-trends")
	if err != nil {
		t.Fatalf("commit proposal: %v", err)
	}
	if task.Status != StatusActive {
		t.Fatalf("expected active task, got %#v", task)
	}
	if _, err := os.Stat(taskPath); err != nil {
		t.Fatalf("expected task file: %v", err)
	}
	committed, _, err := store.ShowProposal("ai-trends")
	if err != nil {
		t.Fatalf("show proposal: %v", err)
	}
	if committed.ProposalStatus != ProposalStatusCommitted {
		t.Fatalf("expected committed proposal, got %#v", committed)
	}
}

func TestStoreRejectProposalDoesNotCreateTask(t *testing.T) {
	home := t.TempDir()
	store := NewStore(home)
	if _, _, err := store.Propose(ProposalInput{CreateInput: CreateInput{
		ID:      "weekly-report",
		Title:   "Weekly Report",
		Prompt:  "Summarize weekly issues.",
		DailyAt: "09:00",
	}}); err != nil {
		t.Fatalf("propose: %v", err)
	}
	rejected, _, err := store.RejectProposal("weekly-report", "not needed")
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if rejected.ProposalStatus != ProposalStatusRejected || rejected.Reason != "not needed" {
		t.Fatalf("unexpected rejected proposal %#v", rejected)
	}
	if _, err := os.Stat(filepath.Join(home, "schedules", "tasks", "weekly-report.yaml")); !os.IsNotExist(err) {
		t.Fatalf("expected no task file, got %v", err)
	}
}
