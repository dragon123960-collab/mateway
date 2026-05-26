package skill

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureWorkspaceLayoutSeedsDefaultSkills(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := EnsureWorkspaceLayout(home, workspace); err != nil {
		t.Fatal(err)
	}
	checks := []string{
		filepath.Join(home, "config"),
		filepath.Join(home, "logs"),
		filepath.Join(home, "run"),
		filepath.Join(home, "trace"),
		filepath.Join(workspace, "skills", "chinese-summary", "SKILL.md"),
		filepath.Join(workspace, "skills", "software-install", "SKILL.md"),
		filepath.Join(workspace, "agents", "main", "skills"),
	}
	for _, path := range checks {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
	}
}

func TestLoadRegistryFindsWorkspaceSkill(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := EnsureWorkspaceLayout(home, workspace); err != nil {
		t.Fatal(err)
	}
	reg, err := LoadRegistry(workspace, "main")
	if err != nil {
		t.Fatal(err)
	}
	names := reg.Names()
	if len(names) == 0 || names[0] != "chinese-summary" {
		t.Fatalf("expected chinese-summary in registry, got %v", names)
	}
	found := false
	for _, name := range names {
		if name == "software-install" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected software-install in registry, got %v", names)
	}
}

func TestEnsureWorkspaceLayoutDoesNotOverwriteUserSkill(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := EnsureWorkspaceLayout(home, workspace); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, "skills", "chinese-summary", "SKILL.md")
	custom := "---\nname: chinese-summary\ndescription: custom\n---\n\n# Custom"
	if err := os.WriteFile(path, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureWorkspaceLayout(home, workspace); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != custom {
		t.Fatalf("expected user skill to remain untouched, got %q", string(data))
	}
}

func TestLoadRegistryAgentSkillOverridesWorkspaceSkill(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := EnsureWorkspaceLayout(home, workspace); err != nil {
		t.Fatal(err)
	}
	agentPath := filepath.Join(workspace, "agents", "main", "skills", "chinese-summary")
	if err := os.MkdirAll(agentPath, 0o755); err != nil {
		t.Fatal(err)
	}
	custom := "---\nname: chinese-summary\ndescription: agent override\npriority: 9\ntags: [agent, override]\n---\n\n# Override"
	if err := os.WriteFile(filepath.Join(agentPath, "SKILL.md"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, err := LoadRegistry(workspace, "main")
	if err != nil {
		t.Fatal(err)
	}
	def, ok := reg.Get("chinese-summary")
	if !ok {
		t.Fatal("expected chinese-summary to be loaded")
	}
	if def.Description != "agent override" {
		t.Fatalf("expected agent override description, got %q", def.Description)
	}
	if def.Priority != 9 {
		t.Fatalf("expected priority 9, got %d", def.Priority)
	}
	if len(def.Tags) != 2 || def.Tags[0] != "agent" || def.Tags[1] != "override" {
		t.Fatalf("unexpected tags: %#v", def.Tags)
	}
	if def.Dir != agentPath {
		t.Fatalf("expected dir %q, got %q", agentPath, def.Dir)
	}
}

func TestSelectMatchesPlanningAndSynthesisGenerically(t *testing.T) {
	defs := []Definition{
		{Name: "fresh-search", Stage: StagePlanning, Scope: "search-planning", WhenContains: []string{"最新"}, Instruction: "fresh"},
		{Name: "chinese-summary", Stage: StageSynthesis, Scope: "answer-language", WhenUserLanguage: "zh-CN", WhenResultKinds: []string{"web_search"}, Instruction: "summary"},
	}
	plan := Select(defs, StagePlanning, Context{UserText: "请查最新 MiniMax"})
	if len(plan) != 1 || plan[0].Name != "fresh-search" {
		t.Fatalf("unexpected planning skills: %#v", plan)
	}
	synth := Select(defs, StageSynthesis, Context{UserText: "请中文总结", Results: []ResultRef{{Kind: "web_search"}}})
	if len(synth) != 1 || synth[0].Name != "chinese-summary" {
		t.Fatalf("unexpected synthesis skills: %#v", synth)
	}
}

func TestSelectLimitsPromptBudgetByPriority(t *testing.T) {
	defs := []Definition{
		{Name: "a", Stage: StagePlanning, Priority: 1, WhenContains: []string{"最新"}},
		{Name: "b", Stage: StagePlanning, Priority: 2, WhenContains: []string{"最新"}},
		{Name: "c", Stage: StagePlanning, Priority: 3, WhenContains: []string{"最新"}},
		{Name: "d", Stage: StagePlanning, Priority: 4, WhenContains: []string{"最新"}},
	}
	got := Select(defs, StagePlanning, Context{UserText: "请查最新情况"})
	if len(got) != 2 {
		t.Fatalf("expected 2 selected skills for planning budget, got %d", len(got))
	}
	if got[0].Name != "d" || got[1].Name != "c" {
		t.Fatalf("unexpected selected order: %#v", got)
	}
}

func TestSelectAllowsPlanningSkillsDuringRepairWithWiderBudget(t *testing.T) {
	defs := []Definition{
		{Name: "a", Stage: StagePlanning, Priority: 1, WhenContains: []string{"最新"}},
		{Name: "b", Stage: StagePlanning, Priority: 2, WhenContains: []string{"最新"}},
		{Name: "c", Stage: StagePlanning, Priority: 3, WhenContains: []string{"最新"}},
		{Name: "d", Stage: StagePlanning, Priority: 4, WhenContains: []string{"最新"}},
		{Name: "e", Stage: StagePlanning, Priority: 5, WhenContains: []string{"最新"}},
	}
	got := Select(defs, StagePlanningRepair, Context{UserText: "请查最新情况"})
	if len(got) != 4 {
		t.Fatalf("expected 4 selected skills for repair budget, got %d", len(got))
	}
	if got[0].Name != "e" || got[1].Name != "d" || got[2].Name != "c" || got[3].Name != "b" {
		t.Fatalf("unexpected repair selected order: %#v", got)
	}
}

func TestSelectKeepsOnlyHighestPriorityPerScope(t *testing.T) {
	defs := []Definition{
		{Name: "fresh-search", Stage: StagePlanning, Scope: "search-planning", Priority: 8, WhenContains: []string{"最新"}},
		{Name: "old-search", Stage: StagePlanning, Scope: "search-planning", Priority: 3, WhenContains: []string{"最新"}},
		{Name: "other", Stage: StagePlanning, Scope: "other", Priority: 6, WhenContains: []string{"最新"}},
	}
	got := Select(defs, StagePlanning, Context{UserText: "请查最新情况"})
	if len(got) != 2 {
		t.Fatalf("expected two skills after scope dedupe, got %d", len(got))
	}
	if got[0].Name != "fresh-search" || got[1].Name != "other" {
		t.Fatalf("unexpected selected skills: %#v", got)
	}
}

func TestSelectDoesNotTreatCurrentProjectAsFreshSearch(t *testing.T) {
	defs := []Definition{
		{Name: "fresh-search", Stage: StagePlanning, Scope: "search-planning", Priority: 8, WhenContains: []string{"当前", "最新"}},
	}
	got := Select(defs, StagePlanning, Context{UserText: "请概览当前项目结构，并总结 README.md 的主要内容"})
	if len(got) != 0 {
		t.Fatalf("expected no fresh-search for local project context, got %#v", got)
	}
}

func TestSelectKeepsFreshSearchForFreshWebContext(t *testing.T) {
	defs := []Definition{
		{Name: "fresh-search", Stage: StagePlanning, Scope: "search-planning", Priority: 8, WhenContains: []string{"当前", "最新"}},
	}
	got := Select(defs, StagePlanning, Context{UserText: "搜索当前 AI 应用趋势"})
	if len(got) != 1 || got[0].Name != "fresh-search" {
		t.Fatalf("expected fresh-search for fresh web context, got %#v", got)
	}
}

func TestSelectMatchesSoftwareInstallSkillForCLIInstallRequests(t *testing.T) {
	defs := []Definition{
		{Name: "software-install", Stage: StagePlanning, Scope: "software-install", Priority: 9, WhenContains: []string{"install", "安装", "cli", "工具", "github"}},
	}
	got := Select(defs, StagePlanning, Context{UserText: "去看看larkcli这个cli怎么样，我想测试安装一下"})
	if len(got) != 1 || got[0].Name != "software-install" {
		t.Fatalf("expected software-install skill for cli install request, got %#v", got)
	}
}

func TestLoadRegistryParsesScope(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := EnsureWorkspaceLayout(home, workspace); err != nil {
		t.Fatal(err)
	}
	reg, err := LoadRegistry(workspace, "main")
	if err != nil {
		t.Fatal(err)
	}
	def, ok := reg.Get("fresh-search")
	if !ok {
		t.Fatal("expected fresh-search to be loaded")
	}
	if def.Scope != "search-planning" {
		t.Fatalf("expected scope search-planning, got %q", def.Scope)
	}
}
