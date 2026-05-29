package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildReportSummarizesMemoryAndObserve(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "workspace", "memory")
	writeFile(t, filepath.Join(root, "agents", "main", "experiences", "tool.md"), `---
type: experience
scope: agent
visibility: private
status: active
sources:
  - trace:abc
confidence: high
created_at: 2026-05-29
updated_at: 2026-05-29
schema_version: 1
---
Use file.read.
`)
	store := ProposalStore{Home: home}
	if _, err := store.Create(CreateProposalInput{Title: "Candidate", Body: "Remember this.", Sources: []string{"trace:abc"}}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, "observe", "diary", "one.md"), "# diary\n")
	writeFile(t, filepath.Join(home, "observe", "audit", "memory.jsonl"), "{}\n")
	report, err := BuildReport(ReportInput{Home: home, MemoryRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if report.MemoryFiles != 1 || report.IndexEntries != 1 {
		t.Fatalf("unexpected memory summary: %#v", report)
	}
	if report.Proposals["proposed"] != 1 {
		t.Fatalf("unexpected proposal summary: %#v", report.Proposals)
	}
	if report.Observe["diary"] != 1 || report.Observe["audit"] != 1 {
		t.Fatalf("unexpected observe summary: %#v", report.Observe)
	}
	if _, err := os.Stat(filepath.Join(home, "workspace", "memory")); err != nil {
		t.Fatal(err)
	}
}
