package memory

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/session"
)

type SessionDistillInput struct {
	Home   string
	State  session.State
	Reason string
}

type SessionDistillResult struct {
	Path string
}

func DistillSession(input SessionDistillInput) (SessionDistillResult, error) {
	home := strings.TrimSpace(input.Home)
	if home == "" {
		home = ".mateway"
	}
	now := time.Now().Format(time.RFC3339)
	id := "session_" + time.Now().Format("20060102_150405_000000")
	path := filepath.Join(home, "observe", "reflections", id+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return SessionDistillResult{}, err
	}
	if err := os.WriteFile(path, []byte(renderSessionDistill(input, now)), 0o644); err != nil {
		return SessionDistillResult{}, err
	}
	return SessionDistillResult{Path: path}, nil
}

func renderSessionDistill(input SessionDistillInput, now string) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("type: reflection\n")
	b.WriteString("scope: agent\n")
	b.WriteString("visibility: private\n")
	b.WriteString("status: proposed\n")
	b.WriteString("sources:\n")
	if input.State.Key != "" {
		b.WriteString("  - session:")
		b.WriteString(input.State.Key)
		b.WriteString("\n")
	}
	b.WriteString("confidence: low\n")
	b.WriteString("created_at: ")
	b.WriteString(now)
	b.WriteString("\nupdated_at: ")
	b.WriteString(now)
	b.WriteString("\nschema_version: 1\n")
	b.WriteString("---\n\n")
	b.WriteString("# Session distill\n\n")
	b.WriteString("- Session: ")
	b.WriteString(input.State.Key)
	b.WriteString("\n")
	if reason := strings.TrimSpace(input.Reason); reason != "" {
		b.WriteString("- Reason: ")
		b.WriteString(reason)
		b.WriteString("\n")
	}
	if input.State.Pending != nil {
		b.WriteString("- Pending: ")
		b.WriteString(input.State.Pending.Kind)
		if input.State.Pending.Question != "" {
			b.WriteString(" - ")
			b.WriteString(input.State.Pending.Question)
		}
		b.WriteString("\n")
	}
	if len(input.State.Tasks) > 0 {
		b.WriteString("\nTasks:\n")
		for _, task := range input.State.Tasks {
			b.WriteString("- ")
			b.WriteString(task.ID)
			b.WriteString(" ")
			b.WriteString(task.Status)
			b.WriteString(": ")
			b.WriteString(task.Goal)
			b.WriteString("\n")
		}
	}
	return b.String()
}
