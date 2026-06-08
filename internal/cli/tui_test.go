package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/session"
)

func TestRunTUIRequiresInteractiveTerminal(t *testing.T) {
	err := RunTUI(t.Context(), TUIOptions{
		Config: &config.Root{App: config.AppConfig{Home: t.TempDir()}},
		In:     strings.NewReader(""),
		Out:    &bytes.Buffer{},
	})
	if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("expected interactive terminal error, got %v", err)
	}
}

func TestTailLinesKeepsLastEntries(t *testing.T) {
	lines := tailLines([]string{"a", "b", "c"}, 2)
	if strings.Join(lines, ",") != "b,c" {
		t.Fatalf("unexpected tail: %#v", lines)
	}
}

func TestVisibleWindowSupportsScrollback(t *testing.T) {
	lines := visibleWindow([]string{"a", "b", "c", "d"}, 2, 1)
	if strings.Join(lines, ",") != "b,c" {
		t.Fatalf("unexpected window: %#v", lines)
	}
}

func TestWrapLinesSplitsLongLines(t *testing.T) {
	lines := wrapLines("abcdef", 3)
	if strings.Join(lines, ",") != "abc,def" {
		t.Fatalf("unexpected wrap: %#v", lines)
	}
}

func TestTUISlashSessionSwitch(t *testing.T) {
	app := &tuiApp{cfg: &config.Root{App: config.AppConfig{Home: t.TempDir()}}, sessionKey: "cli:default"}
	done := app.handleSlash(context.Background(), SlashCommand{Name: "session", Args: []string{"cli:review"}})
	if done {
		t.Fatal("session command should not exit")
	}
	if app.sessionKey != "cli:review" {
		t.Fatalf("session key = %q", app.sessionKey)
	}
}

func TestTUISlashEventsRendersLatestTrace(t *testing.T) {
	home := t.TempDir()
	tracePath := filepath.Join(home, "trace.jsonl")
	trace := `{"type":"tool_execution_start","tool_call":{"Name":"terminal.run","Args":{"command":"go test ./..."}}}` + "\n" +
		`{"type":"tool_execution_end","tool_call":{"Name":"terminal.run"},"tool_result":{"Content":"ok","IsError":false},"duration_ms":42}` + "\n"
	if err := os.WriteFile(tracePath, []byte(trace), 0o600); err != nil {
		t.Fatal(err)
	}
	store := session.NewStore(home)
	state := session.State{
		Key: "cli:default",
		Tasks: []session.TaskNode{{
			ID:        "task-1",
			TracePath: tracePath,
		}},
	}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	app := &tuiApp{cfg: &config.Root{App: config.AppConfig{Home: home}}, sessionKey: "cli:default"}
	done := app.handleSlash(context.Background(), SlashCommand{Name: "events"})
	if done {
		t.Fatal("events command should not exit")
	}
	joined := strings.Join(app.events, "\n")
	if !strings.Contains(joined, "→ Run go test ./...") || !strings.Contains(joined, "✓ Run (42ms) - ok") {
		t.Fatalf("events did not render trace lines:\n%s", joined)
	}
}
