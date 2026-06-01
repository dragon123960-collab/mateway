package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/config"
)

func TestListFindsSharedAndAgentSkills(t *testing.T) {
	workspace := t.TempDir()
	writeSkill(t, filepath.Join(workspace, "skills", "fresh-search", "SKILL.md"), "fresh-search", "Shared search.", "planning", "80")
	writeSkill(t, filepath.Join(workspace, "agents", "main", "skills", "review", "SKILL.md"), "review", "Review code.", "synthesis", "70")

	skills, err := List(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 2 {
		t.Fatalf("skills = %#v", skills)
	}
	if skills[0].Name != "review" || skills[0].Scope != "agent" || skills[1].Name != "fresh-search" || skills[1].Scope != "shared" {
		t.Fatalf("unexpected skills: %#v", skills)
	}
}

func TestSearchCatalogsBuildsURLs(t *testing.T) {
	cfg := &config.Root{Skills: config.SkillsConfig{Catalogs: []config.SkillCatalogConfig{{
		Name: "skills.sh", Enabled: true, SearchURL: "https://skills.sh/?q={query}", TrustLevel: "high",
	}}}}
	results := SearchCatalogs(cfg, "software install")
	if len(results) != 1 || !strings.Contains(results[0].URL, "software+install") || results[0].TrustLevel != "high" {
		t.Fatalf("results = %#v", results)
	}
	if results[0].Adapter != "search_url_only" || results[0].CanInstall {
		t.Fatalf("unexpected adapter fields: %#v", results[0])
	}
}

func TestCatalogReportsShowAdapterStatus(t *testing.T) {
	cfg := &config.Root{Skills: config.SkillsConfig{Catalogs: []config.SkillCatalogConfig{{
		Name: "demo", Enabled: true, SearchURL: "https://example.test?q={query}", InstallURL: "https://example.test/install", TrustLevel: "medium",
	}}}}
	reports := CatalogReports(cfg)
	if len(reports) != 1 || reports[0].Adapter != "declared_install_url" || !reports[0].CanInstall {
		t.Fatalf("reports = %#v", reports)
	}
}

func TestInstallFromLocalSkillFile(t *testing.T) {
	workspace := t.TempDir()
	source := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(source, []byte("---\nname: Demo Skill\ndescription: Demo.\n---\n# Demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Install(InstallInput{Workspace: workspace, Source: source})
	if err != nil {
		t.Fatal(err)
	}
	if result.Name != "demo-skill" {
		t.Fatalf("name = %q", result.Name)
	}
	data, err := os.ReadFile(filepath.Join(workspace, "skills", "demo-skill", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Demo") {
		t.Fatalf("unexpected installed data: %s", data)
	}
	if _, err := Install(InstallInput{Workspace: workspace, Source: source}); err == nil {
		t.Fatal("expected duplicate install error")
	}
}

func TestInstallRejectsPlaintextSecret(t *testing.T) {
	workspace := t.TempDir()
	source := filepath.Join(t.TempDir(), "SKILL.md")
	content := "---\nname: Mail Skill\n---\n# Mail\nsmtp_pass: supersecret123\n"
	if err := os.WriteFile(source, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Install(InstallInput{Workspace: workspace, Source: source})
	if err == nil || !strings.Contains(err.Error(), "refusing to write secret-like content") {
		t.Fatalf("expected secret rejection, got %v", err)
	}
}

func writeSkill(t *testing.T, path, name, description, stage, priority string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	text := "---\nname: " + name + "\ndescription: " + description + "\nstage: " + stage + "\npriority: " + priority + "\n---\n# " + name + "\n"
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}
