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
	metadata, ok, err := ReadMetadata(filepath.Join(workspace, "skills", "demo-skill"))
	if err != nil || !ok {
		t.Fatalf("expected metadata, ok=%v err=%v", ok, err)
	}
	if metadata.ToolRuntime != "mateway" || !strings.Contains(metadata.Source, "SKILL.md") {
		t.Fatalf("metadata = %#v", metadata)
	}
	if metadata.AdapterVersion != "2" || metadata.Graph.Type != "prompt" || metadata.Graph.Granularity != "subtask" {
		t.Fatalf("metadata graph fields = %#v", metadata)
	}
	if metadata.Graph.Usage == "" || len(metadata.Graph.SuccessCriteria) == 0 {
		t.Fatalf("expected generated usage contract, got %#v", metadata.Graph)
	}
	if _, err := Install(InstallInput{Workspace: workspace, Source: source}); err == nil {
		t.Fatal("expected duplicate install error")
	}
}

func TestInstallCommandSkillWritesExecutionMetadata(t *testing.T) {
	workspace := t.TempDir()
	source := filepath.Join(t.TempDir(), "SKILL.md")
	content := "---\nname: Feishu Notify\ndescription: Create Feishu docs.\n---\n# Feishu\nUse terminal.run.\n```bash\npython3 /tmp/skill/scripts/feishu.docs.create --title X\n```\nReturn the created document URL.\n"
	if err := os.WriteFile(source, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Install(InstallInput{Workspace: workspace, Source: source})
	if err != nil {
		t.Fatal(err)
	}
	metadata, ok, err := ReadMetadata(filepath.Dir(result.Path))
	if err != nil || !ok {
		t.Fatalf("expected metadata, ok=%v err=%v", ok, err)
	}
	if metadata.Graph.Type != "react" {
		t.Fatalf("command skill should default to react, got %q", metadata.Graph.Type)
	}
	if strings.Join(metadata.Graph.AllowedTools, ",") != "terminal.run" {
		t.Fatalf("allowed tools = %v", metadata.Graph.AllowedTools)
	}
	if len(metadata.Graph.Entrypoints) == 0 || !strings.Contains(metadata.Graph.Entrypoints[0], "feishu.docs.create") {
		t.Fatalf("entrypoints = %v", metadata.Graph.Entrypoints)
	}
	if len(metadata.Graph.SuccessCriteria) == 0 || !strings.Contains(strings.Join(metadata.Graph.SuccessCriteria, " "), "URL") {
		t.Fatalf("success criteria = %v", metadata.Graph.SuccessCriteria)
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

func TestRegisterLocalSkillWritesMetadata(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "skills", "copied", "SKILL.md")
	writeRawSkill(t, path, "copied", "Copied skill.", "execution", "10")

	result, err := Register(RegisterInput{Workspace: workspace, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if result.Name != "copied" || result.MetadataPath == "" {
		t.Fatalf("register result = %#v", result)
	}
	metadata, ok, err := ReadMetadata(filepath.Dir(path))
	if err != nil || !ok {
		t.Fatalf("expected metadata after register, ok=%v err=%v", ok, err)
	}
	if metadata.Graph.Type != "prompt" || metadata.Graph.Stage != "execution" {
		t.Fatalf("metadata = %#v", metadata)
	}
	if metadata.Graph.Usage == "" || len(metadata.Graph.SuccessCriteria) == 0 {
		t.Fatalf("expected generated usage metadata, got %#v", metadata.Graph)
	}
}

func TestDoctorReportsOrphanWithoutMutating(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "skills", "orphan", "SKILL.md")
	writeRawSkill(t, path, "orphan", "Orphan skill.", "execution", "10")

	report, err := Doctor(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Orphans) != 1 || report.Orphans[0].Name != "orphan" {
		t.Fatalf("doctor report = %#v", report)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), ".mateway", "metadata.yaml")); !os.IsNotExist(err) {
		t.Fatalf("doctor should not write metadata, err=%v", err)
	}
}

func TestMetadataValidationRejectsInvalidGraphType(t *testing.T) {
	metadata := DefaultMetadata(DefaultMetadataInput{Source: "test"})
	metadata.Graph.Type = "tool"
	if err := ValidateMetadata(metadata); err == nil || !strings.Contains(err.Error(), "graph.type") {
		t.Fatalf("expected graph.type validation error, got %v", err)
	}
}

func TestDefaultMetadataFreshSearchUsesReactTools(t *testing.T) {
	metadata := DefaultMetadata(DefaultMetadataInput{
		Source: "builtin",
		Header: Skill{Name: "fresh-search", Description: "Search current sources."},
	})
	if metadata.Graph.Type != "react" {
		t.Fatalf("graph type = %q, want react", metadata.Graph.Type)
	}
	if strings.Join(metadata.Graph.AllowedTools, ",") != "web.search,web.fetch" {
		t.Fatalf("allowed tools = %v", metadata.Graph.AllowedTools)
	}
}

func writeSkill(t *testing.T, path, name, description, stage, priority string) {
	t.Helper()
	writeRawSkill(t, path, name, description, stage, priority)
	if _, err := WriteMetadata(filepath.Dir(path), DefaultMetadata(DefaultMetadataInput{
		Source: "test",
		Header: Skill{Name: name, Description: description, Stage: stage, Priority: priority},
	})); err != nil {
		t.Fatal(err)
	}
}

func writeRawSkill(t *testing.T, path, name, description, stage, priority string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	text := "---\nname: " + name + "\ndescription: " + description + "\nstage: " + stage + "\npriority: " + priority + "\n---\n# " + name + "\n"
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}
