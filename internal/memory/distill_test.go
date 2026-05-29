package memory

import (
	"os"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/session"
)

func TestDistillSessionWritesReflectionWithoutChangingTasks(t *testing.T) {
	state := session.State{
		Key: "cli:test",
		Tasks: []session.TaskNode{
			{ID: "task-1", Goal: "完成的任务", Status: "completed"},
			{ID: "task-2", Goal: "还没完成", Status: "running"},
		},
		Pending: &session.PendingAction{Kind: "user_input", Question: "请补充主题"},
	}
	result, err := DistillSession(SessionDistillInput{Home: t.TempDir(), State: state, Reason: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"type: reflection", "session:cli:test", "task-1 completed", "task-2 running", "Pending: user_input"} {
		if !strings.Contains(text, want) {
			t.Fatalf("distill missing %q:\n%s", want, text)
		}
	}
	if state.Tasks[1].Status != "running" {
		t.Fatalf("distill mutated state: %#v", state.Tasks)
	}
}
