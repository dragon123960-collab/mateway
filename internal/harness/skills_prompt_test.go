package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/agents"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/skills"
	"github.com/dongping/mateway/internal/tools"
)

func TestHarnessBuildAgentInstructionUsesVisibleSkills(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "ws")
	skillRoot := filepath.Join(root, "skills")
	if err := os.MkdirAll(filepath.Join(workspace, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(skillRoot, "frontend-design"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(skillRoot, "db-manager"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "frontend-design", "SKILL.md"), []byte(`---
name: frontend-design
description: Create distinctive landing pages.
---

# Frontend Design

Use this skill when building landing pages and UI.
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(skillRoot, "frontend-design", "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "frontend-design", "references", "patterns.md"), []byte("# patterns"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "db-manager", "SKILL.md"), []byte(`---
name: db-manager
description: Manage database schema.
---

# DB Manager

Use this skill for SQL and schema tasks.
`), 0o644); err != nil {
		t.Fatal(err)
	}

	catalog := skills.NewCatalog([]string{skillRoot})
	if err := catalog.Refresh(); err != nil {
		t.Fatal(err)
	}
	h := New(workspace, session.NewStore(workspace), tools.NewRegistry(), 6)
	h.SkillCatalog = catalog
	h.Config.Models.SystemPrompt = "Base prompt"

	effective, err := h.compileCapabilities(context.Background(), tools.Scope{AgentName: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if !testContainsString(effective.VisibleSkills, "frontend-design") || !testContainsString(effective.VisibleSkills, "db-manager") {
		t.Fatalf("expected catalog skills to be visible, got %#v", effective.VisibleSkills)
	}

	bundle, err := h.buildEinoSkillBundle(context.Background(), Request{AgentName: "default"}, Run{AgentName: "default", Capabilities: effective}, effective.VisibleSkills, []string{"frontend-design"})
	if err != nil {
		t.Fatal(err)
	}
	instruction := h.buildAgentInstruction(agents.Profile{Name: "default"}, "设计一个 landing page 首页", effective.VisibleSkills, []string{"frontend-design"}, true, bundle, false, nil)
	if !strings.Contains(instruction, "## AVAILABLE_SKILLS") || !strings.Contains(instruction, "frontend-design") {
		t.Fatalf("expected available skills in instruction: %s", instruction)
	}
	if !strings.Contains(instruction, "Use the `skill` tool to activate") {
		t.Fatalf("expected skill activation guidance to be present: %s", instruction)
	}
	if !strings.Contains(instruction, "references: references/patterns.md") {
		t.Fatalf("expected selected skill resources to be disclosed: %s", instruction)
	}
	if strings.Contains(instruction, "Use this skill when building landing pages and UI.") || strings.Contains(instruction, "Use this skill for SQL and schema tasks.") {
		t.Fatalf("expected activation mode to avoid inlining full skill bodies: %s", instruction)
	}
}

func testContainsString(list []string, target string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}
