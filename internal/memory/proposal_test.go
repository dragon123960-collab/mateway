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

func TestProposalStoreCommitWritesActiveMemory(t *testing.T) {
	home := t.TempDir()
	memoryRoot := filepath.Join(home, "workspace", "memory")
	store := ProposalStore{Home: home, MemoryRoot: memoryRoot}
	created, err := store.Create(CreateProposalInput{
		Type:       "experience",
		Scope:      "agent",
		Title:      "README Inspection",
		Body:       "Use file.read for local README inspection.",
		Sources:    []string{"trace:abc"},
		Confidence: "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	archived, target, err := store.Commit(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if archived.Status != "archived" {
		t.Fatalf("proposal status = %#v", archived)
	}
	if target != filepath.Join(memoryRoot, "agents", "main", "experiences", "readme-inspection.md") {
		t.Fatalf("target = %q", target)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "status: active") || !strings.Contains(string(data), "trace:abc") {
		t.Fatalf("unexpected active memory:\n%s", data)
	}
	proposalData, err := os.ReadFile(created.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(proposalData), "status: archived") {
		t.Fatalf("expected archived proposal:\n%s", proposalData)
	}
	audit, err := os.ReadFile(filepath.Join(home, "observe", "audit", "memory.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(audit), "proposal_committed") || !strings.Contains(string(audit), "readme-inspection.md") {
		t.Fatalf("unexpected audit:\n%s", audit)
	}
}

func TestProposalStoreCommitSupersedesMatchingActiveMemory(t *testing.T) {
	home := t.TempDir()
	memoryRoot := filepath.Join(home, "workspace", "memory")
	oldPath := filepath.Join(memoryRoot, "projects", "mateway", "environment", "old-host.md")
	writeFile(t, oldPath, `---
type: fact
scope: project
visibility: private
status: active
topic_path: projects/mateway/environment
subject: staging_server
predicate: ssh_host
object: 10.0.0.8
sources:
  - trace:old
confidence: high
created_at: 2026-05-01
updated_at: 2026-05-01
schema_version: 1
---
# Old host

Staging host was 10.0.0.8.
`)
	store := ProposalStore{Home: home, MemoryRoot: memoryRoot}
	created, err := store.Create(CreateProposalInput{
		Type:       "fact",
		Scope:      "project",
		Title:      "Staging host",
		Body:       "Staging host is now 10.0.0.9.",
		Sources:    []string{"trace:new"},
		Confidence: "high",
		TopicPath:  "projects/mateway/environment",
		Subject:    "staging_server",
		Predicate:  "ssh_host",
		Object:     "10.0.0.9",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, target, err := store.Commit(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	oldData, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(oldData), "status: superseded") || !strings.Contains(string(oldData), "superseded_by:") {
		t.Fatalf("old memory not superseded:\n%s", oldData)
	}
	newData, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(newData), "supersedes:") || !strings.Contains(string(newData), "old-host.md") {
		t.Fatalf("new memory missing supersedes:\n%s", newData)
	}
}
