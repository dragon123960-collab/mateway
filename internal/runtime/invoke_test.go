package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dongping/mateway/internal/skills"
)

func TestInvokeCLIShellPythonNodeAndAPI(t *testing.T) {
	workspace := t.TempDir()
	invoker := Invoker{Workspace: workspace}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var cliTests []skills.Skill

	shellEntry := "run.sh"
	shellBody := "#!/bin/sh\necho shell-ok\n"
	if runtime.GOOS == "windows" {
		shellEntry = "run.cmd"
		shellBody = "@echo off\necho shell-ok\n"
	}
	cliTests = append(cliTests, writeSkill(t, workspace, "shell", shellEntry, shellBody))

	if _, err := exec.LookPath("python3"); err == nil {
		cliTests = append(cliTests, writeSkill(t, workspace, "python", "run.py", "print('python-ok')\n"))
	}
	if _, err := exec.LookPath("node"); err == nil {
		cliTests = append(cliTests, writeSkill(t, workspace, "node", "run.js", "console.log('node-ok')\n"))
	}

	for _, skill := range cliTests {
		result, err := invoker.Invoke(ctx, skill)
		if err != nil {
			t.Fatalf("invoke %s: %v", skill.Manifest.Name, err)
		}
		if !strings.Contains(result.Stdout, "-ok") {
			t.Fatalf("unexpected stdout for %s: %q", skill.Manifest.Name, result.Stdout)
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("api-ok"))
	}))
	defer server.Close()
	apiSkill := skills.Skill{
		Manifest: skills.Manifest{
			Name: "api",
			Type: skills.TypeAPI,
			URL:  server.URL,
		},
		Directory:  workspace,
		Executable: true,
	}
	result, err := invoker.Invoke(ctx, apiSkill)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Stdout, "api-ok") {
		t.Fatalf("unexpected api stdout: %q", result.Stdout)
	}
}

func writeSkill(t *testing.T, root, name, entry, body string) skills.Skill {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	entryPath := filepath.Join(dir, entry)
	mode := os.FileMode(0o755)
	if filepath.Ext(entry) == ".py" || filepath.Ext(entry) == ".js" {
		mode = 0o644
	}
	if err := os.WriteFile(entryPath, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	return skills.Skill{
		Manifest: skills.Manifest{
			Name:  name,
			Type:  skills.TypeCLI,
			Entry: entry,
		},
		Directory:  dir,
		Executable: true,
	}
}
