package i18n

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveLocale(t *testing.T) {
	if got := ResolveLocale("auto", "hello"); got != LocaleEN {
		t.Fatalf("expected en-US, got %q", got)
	}
	if got := ResolveLocale("auto", "你好"); got != LocaleZH {
		t.Fatalf("expected zh-CN, got %q", got)
	}
	if got := ResolveLocale("zh", "hello"); got != LocaleZH {
		t.Fatalf("expected configured zh-CN, got %q", got)
	}
}

func TestCatalogFallbackAndExternalOverride(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "de-DE.yaml"), []byte("approval.confirm.generic: Bitte bestätigen.\naliases.confirm:\n  - bestätigen\naliases.memory_commit:\n  - speichern\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog := New(Config{CatalogDir: dir})
	if got := catalog.T("de-DE", "approval.confirm.generic", nil); got != "Bitte bestätigen." {
		t.Fatalf("expected external catalog value, got %q", got)
	}
	if got := catalog.T("de-DE", "memory.review.question", nil); got != builtinEN["memory.review.question"] {
		t.Fatalf("expected en fallback, got %q", got)
	}
	if got := catalog.T("de-DE", "missing.key", nil); got != "missing.key" {
		t.Fatalf("expected key fallback, got %q", got)
	}
	if action, ok := catalog.MatchAlias("de-DE", "bestätigen", "confirm", "cancel"); !ok || action != "confirm" {
		t.Fatalf("expected German confirm alias, got %q %v", action, ok)
	}
	if action, ok := catalog.MatchAlias("de-DE", "保存", "memory_commit", "memory_reject"); !ok || action != "memory_commit" {
		t.Fatalf("expected zh fallback alias, got %q %v", action, ok)
	}
}
