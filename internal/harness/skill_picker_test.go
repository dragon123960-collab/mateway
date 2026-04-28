package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/skills"
	"github.com/dongping/mateway/internal/tools"
)

func TestParseSkillPickerContentFiltersUnknownNames(t *testing.T) {
	visible := []skills.Skill{
		{Manifest: skills.Manifest{Name: "alpha"}},
		{Manifest: skills.Manifest{Name: "beta"}},
	}
	names, reason, err := parseSkillPickerContent("```json\n{\"skills\":[\"beta\",\"unknown\",\"beta\"],\"reason\":\"matched\"}\n```", visible, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "beta" {
		t.Fatalf("unexpected names: %#v", names)
	}
	if reason != "matched" {
		t.Fatalf("unexpected reason: %q", reason)
	}
}

func TestHarnessPrefersHeuristicSkillPickerByDefault(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "ws")
	skillRoot := filepath.Join(root, "skills")
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

	server := newOpenAICompatTestServer(t, func(messages []map[string]any) map[string]any {
		return map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": "done",
				},
			}},
		}
	})
	defer server.Close()

	h := New(workspace, session.NewStore(workspace), tools.NewRegistry(), 6)
	h.SkillCatalog = catalog
	h.UseEinoRuntime(testEinoConfig(server.URL))

	run, err := h.Start(context.Background(), Request{
		SessionKey: "test:skill-picker",
		UserText:   "设计一个 landing page 首页",
		Mode:       "chat",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if run.SkillPickerSource != "heuristic" {
		t.Fatalf("expected heuristic skill picker source, got %#v", run)
	}
	if len(run.SelectedSkills) != 1 || run.SelectedSkills[0] != "frontend-design" {
		t.Fatalf("unexpected selected skills: %#v", run.SelectedSkills)
	}
	step := findRunStep(run.Steps, "skill_picker")
	if step == nil || !strings.Contains(step.Output, "frontend-design") {
		t.Fatalf("expected skill_picker trace step, got %#v", run.Steps)
	}
}

func TestHarnessUsesModelSkillPickerWhenUserExplicitlyAsksForSkillChoice(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "ws")
	skillRoot := filepath.Join(root, "skills")
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
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "db-manager", "SKILL.md"), []byte(`---
name: db-manager
description: Manage database schema.
---

# DB Manager
`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog := skills.NewCatalog([]string{skillRoot})
	if err := catalog.Refresh(); err != nil {
		t.Fatal(err)
	}

	server := newOpenAICompatTestServer(t, func(messages []map[string]any) map[string]any {
		if hasMessageContaining(messages, "You are the skill-picker for the Mateway agent runtime.") {
			return map[string]any{
				"choices": []map[string]any{{
					"message": map[string]any{
						"role":    "assistant",
						"content": `{"skills":["db-manager"],"reason":"user asked which skill should be used"}`,
					},
				}},
			}
		}
		return map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": "done",
				},
			}},
		}
	})
	defer server.Close()

	h := New(workspace, session.NewStore(workspace), tools.NewRegistry(), 6)
	h.SkillCatalog = catalog
	h.UseEinoRuntime(testEinoConfig(server.URL))

	run, err := h.Start(context.Background(), Request{
		SessionKey: "test:skill-picker-model",
		UserText:   "帮我判断这件事应该使用哪个 skill 来处理数据库 schema",
		Mode:       "chat",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if run.SkillPickerSource != "model" {
		t.Fatalf("expected model skill picker source, got %#v", run)
	}
	if len(run.SelectedSkills) != 1 || run.SelectedSkills[0] != "db-manager" {
		t.Fatalf("unexpected selected skills: %#v", run.SelectedSkills)
	}
}

func TestHarnessUsesEinoSkillActivationTool(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "ws")
	skillRoot := filepath.Join(root, "skills")
	if err := os.MkdirAll(filepath.Join(skillRoot, "frontend-design", "references"), 0o755); err != nil {
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
	if err := os.WriteFile(filepath.Join(skillRoot, "frontend-design", "references", "patterns.md"), []byte("# pattern"), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog := skills.NewCatalog([]string{skillRoot})
	if err := catalog.Refresh(); err != nil {
		t.Fatal(err)
	}

	server := newOpenAICompatTestServer(t, func(messages []map[string]any) map[string]any {
		if hasMessageContaining(messages, "You are the skill-picker for the Mateway agent runtime.") {
			return map[string]any{
				"choices": []map[string]any{{
					"message": map[string]any{
						"role":    "assistant",
						"content": `{"skills":["frontend-design"],"reason":"landing page task"}`,
					},
				}},
			}
		}
		if hasToolMessage(messages) {
			return map[string]any{
				"choices": []map[string]any{{
					"message": map[string]any{
						"role":    "assistant",
						"content": "done after loading skill",
					},
				}},
			}
		}
		if hasMessageContaining(messages, "Use this skill when building landing pages and UI.") {
			t.Fatalf("expected full skill body to load via skill tool, not inline prompt: %#v", messages)
		}
		return map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": "",
					"tool_calls": []map[string]any{{
						"id":   "call_skill_1",
						"type": "function",
						"function": map[string]any{
							"name":      "skill",
							"arguments": `{"skill":"frontend-design"}`,
						},
					}},
				},
			}},
		}
	})
	defer server.Close()

	h := New(workspace, session.NewStore(workspace), tools.NewRegistry(), 6)
	h.SkillCatalog = catalog
	h.UseEinoRuntime(testEinoConfig(server.URL))

	run, err := h.Start(context.Background(), Request{
		SessionKey: "test:skill-activation",
		UserText:   "设计一个 landing page 首页",
		Mode:       "chat",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if run.Result != "done after loading skill" {
		t.Fatalf("unexpected result: %#v", run)
	}
	if !containsString(run.VisibleTools, "skill") {
		t.Fatalf("expected skill tool to become visible: %#v", run.VisibleTools)
	}
}

func TestHarnessSkillForkContextRunsSubAgent(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "ws")
	skillRoot := filepath.Join(root, "skills")
	if err := os.MkdirAll(filepath.Join(skillRoot, "research-skill"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "agents", "writer.md"), []byte(`---
name: writer
description: writer agent
can_spawn: false
async_allowed: true
---

Writer agent.
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "research-skill", "SKILL.md"), []byte(`---
name: research-skill
description: Research with a forked worker
context: fork
agent: writer
---

# Research Skill

Use this skill when doing structured research.
`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog := skills.NewCatalog([]string{skillRoot})
	if err := catalog.Refresh(); err != nil {
		t.Fatal(err)
	}

	server := newOpenAICompatTestServer(t, func(messages []map[string]any) map[string]any {
		if hasMessageContaining(messages, "You are the skill-picker for the Mateway agent runtime.") {
			return map[string]any{
				"choices": []map[string]any{{
					"message": map[string]any{
						"role":    "assistant",
						"content": `{"skills":["research-skill"],"reason":"research task"}`,
					},
				}},
			}
		}
		if hasMessageContaining(messages, "Research Skill") {
			return map[string]any{
				"choices": []map[string]any{{
					"message": map[string]any{
						"role":    "assistant",
						"content": "forked worker result",
					},
				}},
			}
		}
		if hasToolMessage(messages) {
			return map[string]any{
				"choices": []map[string]any{{
					"message": map[string]any{
						"role":    "assistant",
						"content": "parent completed after fork",
					},
				}},
			}
		}
		return map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": "",
					"tool_calls": []map[string]any{{
						"id":   "call_skill_fork",
						"type": "function",
						"function": map[string]any{
							"name":      "skill",
							"arguments": `{"skill":"research-skill"}`,
						},
					}},
				},
			}},
		}
	})
	defer server.Close()

	h := New(workspace, session.NewStore(workspace), tools.NewRegistry(), 6)
	h.SkillCatalog = catalog
	h.UseEinoRuntime(testEinoConfig(server.URL))

	run, err := h.Start(context.Background(), Request{
		SessionKey: "test:skill-fork",
		AgentName:  "default",
		UserText:   "帮我调研并整理结论",
		Mode:       "chat",
		Arguments:  map[string]any{"runtime_route": "chatmodel"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if run.Result != "parent completed after fork" {
		t.Fatalf("unexpected result: %#v", run)
	}
	if !containsString(run.VisibleTools, "skill") {
		t.Fatalf("expected skill tool to become visible: %#v", run.VisibleTools)
	}
}

func hasMessageContaining(messages []map[string]any, needle string) bool {
	for _, msg := range messages {
		if strings.Contains(strings.TrimSpace(toString(msg["content"])), needle) {
			return true
		}
	}
	return false
}

func findRunStep(steps []RunStep, kind string) *RunStep {
	for i := range steps {
		if steps[i].Kind == kind {
			return &steps[i]
		}
	}
	return nil
}

func toString(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}
