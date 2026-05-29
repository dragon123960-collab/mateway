package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProposalStoreCreateListReject(t *testing.T) {
	home := t.TempDir()
	store := ProposalStore{Home: home}
	created, err := store.Create(CreateProposalInput{
		Type:       "experience",
		Scope:      "agent",
		Title:      "README inspection",
		Body:       "Use file.read for local README inspection.",
		Sources:    []string{"trace:abc"},
		Confidence: "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || !strings.Contains(created.Path, filepath.Join("observe", "proposals")) {
		t.Fatalf("unexpected proposal: %#v", created)
	}
	data, err := os.ReadFile(created.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "status: proposed") || !strings.Contains(string(data), "trace:abc") {
		t.Fatalf("unexpected proposal markdown:\n%s", data)
	}
	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Title != "README inspection" {
		t.Fatalf("unexpected list: %#v", list)
	}
	rejected, err := store.Reject(created.ID, "not useful")
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Status != "rejected" {
		t.Fatalf("expected rejected, got %#v", rejected)
	}
	audit, err := os.ReadFile(filepath.Join(home, "observe", "audit", "memory.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(audit), "proposal_created") || !strings.Contains(string(audit), "proposal_rejected") {
		t.Fatalf("unexpected audit:\n%s", audit)
	}
	if _, err := os.Stat(filepath.Join(home, "workspace", "memory")); !os.IsNotExist(err) {
		t.Fatalf("proposal store should not write active memory, stat err=%v", err)
	}
}

func TestProposalStoreRequiresTitleAndBody(t *testing.T) {
	store := ProposalStore{Home: t.TempDir()}
	if _, err := store.Create(CreateProposalInput{Body: "body"}); err == nil {
		t.Fatal("expected missing title error")
	}
	if _, err := store.Create(CreateProposalInput{Title: "title"}); err == nil {
		t.Fatal("expected missing body error")
	}
}
