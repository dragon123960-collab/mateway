package runtime

import (
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/session"
)

func TestUpdateSessionSummaryStoresTaskEvidenceAndContext(t *testing.T) {
	state := session.State{
		Tasks: []session.TaskNode{{
			ID:      "task-1",
			Goal:    "debug failing build",
			Summary: "fixed build failure",
			Status:  "completed",
			Steps: []session.TaskStep{{
				Tool:     "terminal.run",
				Status:   "accepted",
				Summary:  "go test ./... passed",
				Accepted: true,
			}},
		}},
	}
	updateSessionSummary(&state, "task-1", "The build now passes.", "completed", nil)
	if !strings.Contains(state.Summary.Text, "The build now passes.") {
		t.Fatalf("expected final text in summary: %#v", state.Summary)
	}
	if len(state.Summary.Tasks) != 1 || !strings.Contains(state.Summary.Tasks[0], "fixed build failure") {
		t.Fatalf("expected task summary, got %#v", state.Summary.Tasks)
	}
	if len(state.Summary.Evidence) != 1 || !strings.Contains(state.Summary.Evidence[0], "go test") {
		t.Fatalf("expected evidence summary, got %#v", state.Summary.Evidence)
	}
	context := renderSessionSummaryContext(state.Summary)
	if !strings.Contains(context, "Session summary:") || !strings.Contains(context, "Recent evidence:") {
		t.Fatalf("unexpected context %q", context)
	}
}

func TestAppendPreviousTaskContextIncludesSessionSummaryWithoutTasks(t *testing.T) {
	state := session.State{Summary: session.SessionSummary{Text: "User prefers concise answers."}}
	context := appendPreviousTaskContext("Base prompt", state, "task-current")
	if !strings.Contains(context, "Base prompt") || !strings.Contains(context, "Session summary:") || !strings.Contains(context, "User prefers concise answers.") {
		t.Fatalf("unexpected context %q", context)
	}
}
