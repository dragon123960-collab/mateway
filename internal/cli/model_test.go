package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/config"
)

func TestPrintModelShowsAgentChain(t *testing.T) {
	cfg := modelTestConfig()
	var out bytes.Buffer
	if err := PrintModel(&out, cfg, "main", false); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"agent: main",
		"global_default: global",
		"agent_default: agent",
		"agent_fallbacks: agent-fallback",
		"chain: agent,agent-fallback,global,global-fallback",
		"agent_roles: review=agent-review",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
}

func TestPrintModelVerboseIncludesLoadedModels(t *testing.T) {
	cfg := modelTestConfig()
	var out bytes.Buffer
	if err := PrintModel(&out, cfg, "main", true); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"NAME", "ENABLED", "PROVIDER", "global", "agent"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
}

func TestPrintModelsListsEndpoints(t *testing.T) {
	cfg := modelTestConfig()
	var out bytes.Buffer
	if err := PrintModels(&out, cfg, false); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"NAME", "ENABLED", "agent", "openai", "agent-model"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
}

func TestParseModelSlashArgs(t *testing.T) {
	verbose, agentID, err := parseModelSlashArgs([]string{"--agent", "main", "--verbose"})
	if err != nil {
		t.Fatal(err)
	}
	if !verbose || agentID != "main" {
		t.Fatalf("unexpected args: verbose=%t agent=%q", verbose, agentID)
	}
	_, _, err = parseModelSlashArgs([]string{"--agent"})
	if err == nil {
		t.Fatal("expected usage error")
	}
}

func modelTestConfig() *config.Root {
	return &config.Root{
		Model: config.ModelSelection{
			Default:   "global",
			Fallbacks: []string{"global-fallback"},
			Roles: config.ModelRoles{
				"review": []string{"global-review"},
			},
		},
		Agents: config.AgentsConfig{
			Default: "main",
			Profiles: []config.AgentProfileConfig{{
				ID:   "main",
				Name: "Main",
				Model: config.ModelSelection{
					Default:   "agent",
					Fallbacks: []string{"agent-fallback"},
					Roles: config.ModelRoles{
						"review": []string{"agent-review"},
					},
				},
			}},
		},
		Models: []config.ModelConfig{
			{Name: "global", Provider: "openai", Model: "global-model", Enabled: true, Modalities: []string{"text"}},
			{Name: "agent", Provider: "openai", Model: "agent-model", Enabled: true, Modalities: []string{"text"}},
		},
	}
}
