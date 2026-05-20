package tool

import (
	"context"
	"strings"
	"testing"
)

func TestRegistryRejectsUnknownTool(t *testing.T) {
	r := NewRegistry()
	r.Register(TimeNow())
	if _, ok := r.Get("missing.tool"); ok {
		t.Fatalf("unexpected missing tool")
	}
	if _, err := r.MustGet("missing.tool"); err == nil {
		t.Fatalf("expected error for unknown tool")
	}
}

func TestDangerousCommandGuard(t *testing.T) {
	cases := []string{
		"rm -rf tmp",
		"git reset --hard",
		"echo hi > file.txt",
		"docker compose up -d",
		"npm install",
	}
	for _, cmd := range cases {
		if !IsDangerousCommand(cmd) {
			t.Fatalf("expected dangerous: %s", cmd)
		}
	}
	for _, cmd := range []string{"pwd", "ls -la", "git status", "go test ./..."} {
		if IsDangerousCommand(cmd) {
			t.Fatalf("expected safe: %s", cmd)
		}
	}
}

func TestTruncate(t *testing.T) {
	text := "abcdefghijklmnopqrstuvwxyz"
	got := Truncate(text, 10)
	if got == text || len(got) <= 10 {
		t.Fatalf("expected truncated marker, got %q", got)
	}
}

func TestPathGuardRejectsOutsideRoot(t *testing.T) {
	_, err := ResolveAllowedPath("/etc/passwd", Context{ProjectRoot: "/tmp/project", Workspace: "/tmp/workspace"})
	if err == nil {
		t.Fatalf("expected outside root error")
	}
}

func TestPathGuardAllowsProjectRelative(t *testing.T) {
	got, err := ResolveAllowedPath("README.md", Context{ProjectRoot: "/tmp/project", Workspace: "/tmp/workspace"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/project/README.md" {
		t.Fatalf("unexpected path %q", got)
	}
}

func TestWebSearchHonorsDisabledProviders(t *testing.T) {
	result := WebSearch().Run(context.Background(), Call{
		Args:    map[string]string{"query": "Mateway"},
		Context: Context{Search: SearchConfig{}},
	})
	if result.OK || !strings.Contains(result.Error, "no enabled provider") {
		t.Fatalf("expected disabled provider error, got %#v", result)
	}
}
