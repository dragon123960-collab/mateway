package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/skills"
)

func TestBuildIncludesWorkspaceMarkdown(t *testing.T) {
	root := t.TempDir()
	for _, file := range []struct {
		name string
		body string
	}{
		{"SOUL.md", "Be warm."},
		{"AGENT.md", "Act carefully."},
		{"USER.md", "User likes concise replies."},
	} {
		if err := os.WriteFile(filepath.Join(root, file.name), []byte(file.body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := Assembler{Workspace: root, SystemPrompt: "Base prompt"}.Build()
	for _, want := range []string{"Base prompt", "Be warm.", "Act carefully.", "User likes concise replies."} {
		if !strings.Contains(out, want) {
			t.Fatalf("assembled prompt missing %q: %s", want, out)
		}
	}
}

func TestBuildIncludesSkillProgressiveDisclosure(t *testing.T) {
	root := t.TempDir()
	out := Assembler{
		Workspace:    root,
		SystemPrompt: "Base prompt",
		Goal:         "设计一个 landing page 首页",
		Skills: []skills.Skill{
			{
				Manifest: skills.Manifest{
					Name:        "frontend-design",
					Description: "Create distinctive landing pages and frontend UI.",
					Tags:        []string{"frontend", "landing"},
				},
				SkillPath: filepath.Join(root, "skills", "frontend-design", "SKILL.md"),
				Body:      "# Frontend Design\nUse this when building landing pages.",
				Resources: skills.ResourceSet{
					References: []string{"references/patterns.md"},
					Scripts:    []string{"scripts/generate.sh"},
				},
			},
			{
				Manifest: skills.Manifest{
					Name:        "db-manager",
					Description: "Manage SQL schema migrations.",
				},
				SkillPath: filepath.Join(root, "skills", "db-manager", "SKILL.md"),
				Body:      "# DB Manager\nUse this for schema tasks.",
			},
		},
	}.Build()

	for _, want := range []string{"## AVAILABLE_SKILLS", "frontend-design", "## SELECTED_SKILLS", "Use this when building landing pages.", "references: references/patterns.md", "Use `read_skill_resource` to inspect these files on demand when needed."} {
		if !strings.Contains(out, want) {
			t.Fatalf("assembled prompt missing %q: %s", want, out)
		}
	}
	if strings.Contains(out, "Use this for schema tasks.") {
		t.Fatalf("expected unrelated skill body to stay undisclosed: %s", out)
	}
}

func TestBuildUsesSkillActivationWithoutInliningBody(t *testing.T) {
	root := t.TempDir()
	out := Assembler{
		Workspace:          root,
		SystemPrompt:       "Base prompt",
		Goal:               "设计一个 landing page 首页",
		UseSelectedSkills:  true,
		UseSkillActivation: true,
		SkillToolName:      "skill",
		Skills: []skills.Skill{
			{
				Manifest: skills.Manifest{
					Name:        "frontend-design",
					Description: "Create distinctive landing pages and frontend UI.",
				},
				Body: "# Frontend Design\nUse this when building landing pages.",
				Resources: skills.ResourceSet{
					References: []string{"references/patterns.md"},
				},
			},
		},
		SelectedSkills: []skills.Skill{
			{
				Manifest: skills.Manifest{
					Name:        "frontend-design",
					Description: "Create distinctive landing pages and frontend UI.",
				},
				Body: "# Frontend Design\nUse this when building landing pages.",
				Resources: skills.ResourceSet{
					References: []string{"references/patterns.md"},
				},
			},
		},
	}.Build()

	for _, want := range []string{"## SELECTED_SKILLS", "Use the `skill` tool to activate", "Activate this skill through the skill tool", "references: references/patterns.md"} {
		if !strings.Contains(out, want) {
			t.Fatalf("assembled prompt missing %q: %s", want, out)
		}
	}
	if strings.Contains(out, "Use this when building landing pages.") {
		t.Fatalf("expected activation mode to avoid inlining full body: %s", out)
	}
}

func TestBuildHonorsExplicitEmptySelectedSkills(t *testing.T) {
	root := t.TempDir()
	out := Assembler{
		Workspace:         root,
		SystemPrompt:      "Base prompt",
		Goal:              "设计一个 landing page 首页",
		UseSelectedSkills: true,
		Skills: []skills.Skill{
			{
				Manifest: skills.Manifest{
					Name:        "frontend-design",
					Description: "Create distinctive landing pages and frontend UI.",
				},
				Body: "# Frontend Design\nUse this when building landing pages.",
			},
		},
	}.Build()

	if !strings.Contains(out, "## AVAILABLE_SKILLS") {
		t.Fatalf("expected available skills block: %s", out)
	}
	if strings.Contains(out, "## SELECTED_SKILLS") {
		t.Fatalf("did not expect fallback selection when explicit selection is empty: %s", out)
	}
}
