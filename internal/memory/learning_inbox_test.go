package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/skill"
)

func TestBuildLearningInboxSummarizesPendingWork(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if _, err := (ProposalStore{Home: home}).Create(CreateProposalInput{
		Title:      "Remember README workflow",
		Type:       "experience",
		Scope:      "agent",
		Body:       "Use file.read before changing README files.",
		Sources:    []string{"trace:one"},
		Confidence: "medium",
	}); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(workspace, "skills", "readme-workflow", "SKILL.md")
	if _, err := (skill.ProposalStore{Home: home, Workspace: workspace}).Create(skill.CreateProposalInput{
		TargetPath: target,
		NewContent: "---\nname: readme-workflow\n---\n# README Workflow\n\nGuidance.\n",
		Reason:     "Repeated README edits.",
		Sources:    []string{"observe/learning/events.jsonl:1"},
	}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, "observe", "reflections", "reflection_one.md"), `---
type: reflection
status: proposed
---
# Task reflection

Failed file read should be reviewed.
`)
	if err := os.MkdirAll(filepath.Join(home, "observe", "learning"), 0o755); err != nil {
		t.Fatal(err)
	}
	event := `{"type":"task_completed","goal":"inspect README","status":"completed","tool_sequence":["file.read","file.write"]}`
	if err := os.WriteFile(filepath.Join(home, "observe", "learning", "events.jsonl"), []byte(event+"\n"+event+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	inbox, err := BuildLearningInbox(LearningInboxInput{Home: home, Workspace: workspace, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if inbox.MemoryProposals != 1 || inbox.SkillProposals != 1 || inbox.Reflections != 1 || inbox.RepeatedToolSequences != 1 {
		t.Fatalf("unexpected inbox counts: %#v", inbox)
	}
	joined := inboxKinds(inbox)
	for _, want := range []string{"memory_proposal", "skill_proposal", "reflection", "repeated_tool_sequence"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in inbox: %#v", want, inbox.Items)
		}
	}
}

func inboxKinds(inbox LearningInbox) string {
	var values []string
	for _, item := range inbox.Items {
		values = append(values, item.Kind)
	}
	return strings.Join(values, ",")
}
