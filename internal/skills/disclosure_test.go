package skills

import "testing"

func TestProgressiveDisclosureSelectsRelevantSkillBodies(t *testing.T) {
	list := []Skill{
		{
			Manifest: Manifest{
				Name:        "frontend-design",
				Description: "Create distinctive landing pages and frontend interfaces.",
				Tags:        []string{"frontend", "design", "landing"},
			},
			Body: "# Frontend Design\nUse this skill when building landing pages, dashboards, or React UI.",
		},
		{
			Manifest: Manifest{
				Name:        "db-manager",
				Description: "Manage database schema and SQL workflows.",
				Tags:        []string{"database", "sql"},
			},
			Body: "# DB Manager\nUse this for schema updates and database queries.",
		},
	}

	selected := ProgressiveDisclosure("设计一个 landing page 首页", list, 2)
	if len(selected) == 0 || selected[0].Manifest.Name != "frontend-design" {
		t.Fatalf("expected frontend-design to be selected first, got %#v", selected)
	}
}

func TestFilterVisibleKeepsNamedSkills(t *testing.T) {
	list := []Skill{
		{Manifest: Manifest{Name: "alpha"}},
		{Manifest: Manifest{Name: "beta"}},
	}

	filtered := FilterVisible(list, []string{"beta"})
	if len(filtered) != 1 || filtered[0].Manifest.Name != "beta" {
		t.Fatalf("unexpected filtered skills: %#v", filtered)
	}
}
