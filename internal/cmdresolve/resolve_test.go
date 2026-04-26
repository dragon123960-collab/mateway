package cmdresolve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolverFindsCommandInCurrentPath(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	commandPath := filepath.Join(binDir, "demo")
	if err := os.WriteFile(commandPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	resolver := NewResolver("", binDir)
	resolution, err := resolver.Resolve("demo")
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Path != commandPath {
		t.Fatalf("unexpected path: %#v", resolution)
	}
}

func TestResolverFallsBackToShellCommandV(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	commandPath := filepath.Join(binDir, "from-shell")
	if err := os.WriteFile(commandPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	shellPath := filepath.Join(root, "fake-shell")
	script := `#!/bin/sh
last=""
for arg in "$@"; do
  last="$arg"
done
case "$last" in
  *"__MATEWAY_PATH__"*)
    echo "__MATEWAY_PATH__` + binDir + `"
    ;;
  *"__MATEWAY_COMMAND__"*)
    echo "__MATEWAY_COMMAND__` + commandPath + `"
    ;;
esac
`
	if err := os.WriteFile(shellPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	resolver := NewResolver(shellPath, "")
	resolution, err := resolver.Resolve("from-shell")
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Source != "login_shell_command_v" && !strings.Contains(resolution.Source, "path") {
		t.Fatalf("unexpected resolution source: %#v", resolution)
	}
}
