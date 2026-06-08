package cli

import (
	"path/filepath"
	"testing"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/session"
)

func TestResolveSessionKey(t *testing.T) {
	if got := ResolveSessionKey(""); got != "cli:default" {
		t.Fatalf("empty session = %q", got)
	}
	if got := ResolveSessionKey("debug"); got != "cli:debug" {
		t.Fatalf("short session = %q", got)
	}
	if got := ResolveSessionKey("feishu:thread"); got != "feishu:thread" {
		t.Fatalf("channel session = %q", got)
	}
}

func TestCwdSessionKeyIsStable(t *testing.T) {
	cwd := filepath.Join("Users", "dongping", "project", "mateway")
	first := CwdSessionKey(cwd)
	second := CwdSessionKey(cwd)
	if first == "" || first != second || first == DefaultSessionKey {
		t.Fatalf("unexpected cwd session keys: %q %q", first, second)
	}
}

func TestForkSessionCopiesMessagesAndTasks(t *testing.T) {
	home := t.TempDir()
	store := session.NewStore(home)
	source := session.State{
		Key:      "feishu:thread",
		Messages: []agentcore.Message{{Role: agentcore.RoleUser, Content: "hello"}},
	}
	source.StartTask("continue work")
	if err := store.Save(source); err != nil {
		t.Fatal(err)
	}
	if err := ForkSession(store, "feishu:thread", "cli:default"); err != nil {
		t.Fatal(err)
	}
	target, err := store.Load("cli:default")
	if err != nil {
		t.Fatal(err)
	}
	if target.Key != "cli:default" || len(target.Messages) != 1 || len(target.Tasks) != 1 {
		t.Fatalf("unexpected target: %#v", target)
	}
}
