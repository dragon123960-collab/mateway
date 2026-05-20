package gateway

import (
	"context"
	"strings"
	"testing"
)

func TestServiceManagerUnsupportedOS(t *testing.T) {
	m := ServiceManager{GOOS: "plan9"}
	if err := m.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("expected unsupported OS error, got %v", err)
	}
	text, err := m.Status(context.Background(), t.TempDir())
	if err == nil {
		t.Fatalf("expected status error for unsupported OS")
	}
	if !strings.Contains(text, "mateway serve lock") {
		t.Fatalf("expected lock status in output, got %q", text)
	}
}
