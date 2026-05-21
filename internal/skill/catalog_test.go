package skill

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchCatalogPrefersActualSkillLinksOverNavigation(t *testing.T) {
	workspace := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/html")
		_, _ = w.Write([]byte(`
<a href="/agent/codex">Codex</a>
<a href="/agent/claude-code">Claude Code</a>
<a href="/vercel-labs/agent-browser/agent-browser">23</a>
<a href="/xixu-me/skills/use-my-browser">use-my-browser</a>
`))
	}))
	defer server.Close()

	items, err := SearchCatalog(context.Background(), workspace, "agent browser", CatalogSearchOptions{
		Sources: []CatalogSource{{Name: "skills.sh", BaseURL: server.URL, Search: server.URL + "/?q=%s"}},
		Client:  server.Client(),
		Limit:   4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one matching skill, got %#v", items)
	}
	if items[0].Name != "agent-browser" {
		t.Fatalf("expected agent-browser, got %#v", items[0])
	}
}

func TestInstallCatalogSkillWritesOnlyWorkspaceSkill(t *testing.T) {
	workspace := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			_, _ = w.Write([]byte(`<a href="/repo/demo/browser-helper">browser-helper</a>`))
		case "/repo/demo/browser-helper":
			_, _ = w.Write([]byte(`<a href="/raw/browser-helper/SKILL.md">SKILL.md</a>`))
		case "/repo/demo/raw/browser-helper/SKILL.md":
			_, _ = w.Write([]byte("---\nname: browser-helper\ndescription: test skill\n---\n\n# Browser Helper\n"))
		case "/raw/browser-helper/SKILL.md":
			_, _ = w.Write([]byte("---\nname: browser-helper\ndescription: test skill\n---\n\n# Browser Helper\n"))
		case "/browser-helper/SKILL.md":
			_, _ = w.Write([]byte("---\nname: browser-helper\ndescription: test skill\n---\n\n# Browser Helper\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := InstallCatalogSkill(context.Background(), workspace, "browser helper", CatalogSearchOptions{
		Sources: []CatalogSource{{Name: "test", BaseURL: server.URL, Search: server.URL + "/search?q=%s"}},
		Client:  server.Client(),
		Limit:   1,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(workspace, "skills", "browser-helper", "SKILL.md")
	if result.TargetPath != want {
		t.Fatalf("expected target %s, got %s", want, result.TargetPath)
	}
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "name: browser-helper") {
		t.Fatalf("unexpected skill content: %s", string(data))
	}
}

func TestInstalledInWorkspaceIgnoresNonMatewayHomes(t *testing.T) {
	workspace := t.TempDir()
	other := t.TempDir()
	otherSkill := filepath.Join(other, "skills", "browser-helper")
	if err := os.MkdirAll(otherSkill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherSkill, "SKILL.md"), []byte("# Other"), 0o644); err != nil {
		t.Fatal(err)
	}
	installed, path := InstalledInWorkspace(workspace, "browser-helper")
	if installed || path != "" {
		t.Fatalf("expected non-workspace skill to be ignored, installed=%v path=%s", installed, path)
	}
}
