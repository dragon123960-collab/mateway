package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/agentcore"
)

func TestRunLearningDistillHeartbeatNoModelSkipsCandidates(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "observe", "learning"), 0o755); err != nil {
		t.Fatal(err)
	}
	event := `{"type":"user_correction","task_id":"task-1","goal":"以后要先读 README","status":"completed","tool_sequence":["file.read"]}`
	if err := os.WriteFile(filepath.Join(home, "observe", "learning", "events.jsonl"), []byte(event+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := RunLearningDistillHeartbeat(context.Background(), LearningHeartbeatInput{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 1 || result.Skipped != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	audit, err := os.ReadFile(filepath.Join(home, "observe", "audit", "memory.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(audit), "learning_distill_model_error") {
		t.Fatalf("missing audit:\n%s", audit)
	}
}

func TestRunSkillLearningHeartbeatCreatesProposal(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	target := filepath.Join(workspace, "skills", "demo", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	oldContent := "---\nname: demo\n---\n# Demo\n\nOld guidance.\n"
	if err := os.WriteFile(target, []byte(oldContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "observe", "skill_usage"), 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"skill_usage","task_id":"task-1","status":"completed","skill":{"name":"demo","path":"` + filepath.ToSlash(target) + `","scope":"shared"},"related_steps":[{"tool":"file.read","status":"failed","summary":"file not found"}],"sources":["trace:one"]}`
	if err := os.WriteFile(filepath.Join(home, "observe", "skill_usage", "events.jsonl"), []byte(line+"\n"+strings.ReplaceAll(line, "task-1", "task-2")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	model := distillStaticModel{text: `{"target_path":"` + filepath.ToSlash(target) + `","new_content":"---\nname: demo\n---\n# Demo\n\n## When to use\nUse when demo file reads repeatedly fail.\n\n## Inputs\n- Target file path.\n\n## Outputs\n- Corrected read guidance.\n\n## Allowed tools\n- file.read\n\n## Safety\n- Do not read secrets.\n\n## Success criteria\n- File evidence is accepted.","reason":"Repeated file.read failures.","sources":["observe/skill_usage/events.jsonl:1","observe/skill_usage/events.jsonl:2"]}`}
	result, err := RunSkillLearningHeartbeat(context.Background(), SkillLearningHeartbeatInput{Home: home, Workspace: workspace, Model: model})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 || len(result.ProposalIDs) != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	data, err := os.ReadFile(filepath.Join(home, "observe", "skill_proposals", result.ProposalIDs[0]+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "When to use") || !strings.Contains(string(data), "Success criteria") {
		t.Fatalf("unexpected proposal:\n%s", data)
	}
}

func TestRunSkillLearningHeartbeatCreatesNewSkillProposalFromRepeatedLearning(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(filepath.Join(home, "observe", "learning"), 0o755); err != nil {
		t.Fatal(err)
	}
	event := `{"type":"task_completed","task_id":"task-1","goal":"整理会议纪要并生成周报","status":"completed","tool_sequence":["calendar.list","minutes.fetch","file.write"],"tool_steps":[{"tool":"calendar.list","status":"accepted"},{"tool":"minutes.fetch","status":"accepted"},{"tool":"file.write","status":"accepted"}],"sources":["trace:one"]}`
	second := strings.ReplaceAll(event, "task-1", "task-2")
	if err := os.WriteFile(filepath.Join(home, "observe", "learning", "events.jsonl"), []byte(event+"\n"+second+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(workspace, "skills", "meeting-weekly", "SKILL.md")
	model := distillStaticModel{text: `{"target_path":"` + filepath.ToSlash(target) + `","new_content":"---\nname: meeting-weekly\n---\n# Meeting Weekly\n\n## When to use\nUse when creating recurring meeting weekly reports.\n\n## Inputs\n- Calendar window.\n- Meeting minutes references.\n\n## Outputs\n- Weekly meeting report file.\n\n## Allowed tools\n- calendar.list\n- minutes.fetch\n- file.write\n\n## Safety\n- Do not include private minutes beyond the requested report.\n\n## Success criteria\n- Report is written and cites the relevant meetings.","reason":"Repeated successful meeting weekly workflow.","sources":["observe/learning/events.jsonl:1","observe/learning/events.jsonl:2"]}`}
	result, err := RunSkillLearningHeartbeat(context.Background(), SkillLearningHeartbeatInput{Home: home, Workspace: workspace, Model: model})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 || len(result.ProposalIDs) != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	data, err := os.ReadFile(filepath.Join(home, "observe", "skill_proposals", result.ProposalIDs[0]+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "meeting-weekly") || !strings.Contains(string(data), "Repeated successful meeting weekly workflow") {
		t.Fatalf("unexpected new skill proposal:\n%s", data)
	}
}

func TestRunSkillLearningHeartbeatRejectsIncompleteSkillProposal(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(filepath.Join(home, "observe", "learning"), 0o755); err != nil {
		t.Fatal(err)
	}
	event := `{"type":"task_completed","task_id":"task-1","goal":"整理会议纪要并生成周报","status":"completed","tool_sequence":["calendar.list","minutes.fetch"],"sources":["trace:one"]}`
	if err := os.WriteFile(filepath.Join(home, "observe", "learning", "events.jsonl"), []byte(event+"\n"+strings.ReplaceAll(event, "task-1", "task-2")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(workspace, "skills", "meeting-weekly", "SKILL.md")
	model := distillStaticModel{text: `{"target_path":"` + filepath.ToSlash(target) + `","new_content":"# Meeting Weekly\n\nDo the thing.","reason":"Repeated workflow.","sources":["observe/learning/events.jsonl:1","observe/learning/events.jsonl:2"]}`}
	result, err := RunSkillLearningHeartbeat(context.Background(), SkillLearningHeartbeatInput{Home: home, Workspace: workspace, Model: model})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 0 || len(result.Errors) != 1 || !strings.Contains(result.Errors[0], "missing required sections") {
		t.Fatalf("expected incomplete proposal error, got %#v", result)
	}
}

var _ agentcore.Model = distillStaticModel{}
