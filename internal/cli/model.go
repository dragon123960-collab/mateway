package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/dongping/mateway/internal/config"
)

func PrintModel(out io.Writer, cfg *config.Root, agentID string, verbose bool) error {
	if cfg == nil {
		return fmt.Errorf("config is required")
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		agentID = strings.TrimSpace(cfg.Agents.Default)
	}
	if agentID == "" {
		agentID = "main"
	}
	agent, ok := findAgentProfile(cfg, agentID)
	if !ok {
		return fmt.Errorf("agent profile %q not found", agentID)
	}
	selection := agent.Model
	fmt.Fprintln(out, "agent:", agent.ID)
	if strings.TrimSpace(agent.Name) != "" {
		fmt.Fprintln(out, "name:", agent.Name)
	}
	fmt.Fprintln(out, "global_default:", valueOrDash(cfg.Model.Default))
	fmt.Fprintln(out, "agent_default:", valueOrDash(selection.Default))
	fmt.Fprintln(out, "agent_fallbacks:", listOrDash(selection.Fallbacks))
	printModelRoles(out, "global_roles", cfg.Model.Roles)
	printModelRoles(out, "agent_roles", selection.Roles)
	fmt.Fprintln(out, "chain:", listOrDash(modelChain(selection, cfg.Model)))
	if verbose {
		return PrintModels(out, cfg, true)
	}
	return nil
}

func PrintModels(out io.Writer, cfg *config.Root, verbose bool) error {
	if cfg == nil {
		return fmt.Errorf("config is required")
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if verbose {
		fmt.Fprintln(tw, "NAME\tENABLED\tPROVIDER\tAPI\tMODEL\tMODALITIES\tCONTEXT\tMAX_TOKENS\tDESCRIPTION")
	} else {
		fmt.Fprintln(tw, "NAME\tENABLED\tPROVIDER\tMODEL\tMODALITIES")
	}
	models := append([]config.ModelConfig(nil), cfg.Models...)
	sort.SliceStable(models, func(i, j int) bool {
		return strings.ToLower(models[i].Name) < strings.ToLower(models[j].Name)
	})
	for _, model := range models {
		enabled := "false"
		if model.Enabled {
			enabled = "true"
		}
		modalities := listOrDash(model.Modalities)
		if verbose {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%d\t%s\n", valueOrDash(model.Name), enabled, valueOrDash(model.Provider), valueOrDash(model.API), valueOrDash(model.Model), modalities, model.ContextWindow, model.MaxTokensValue(), compactInline(model.Description, 96))
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", valueOrDash(model.Name), enabled, valueOrDash(model.Provider), valueOrDash(model.Model), modalities)
	}
	return tw.Flush()
}

func findAgentProfile(cfg *config.Root, agentID string) (config.AgentProfileConfig, bool) {
	for _, profile := range cfg.Agents.Profiles {
		if strings.EqualFold(strings.TrimSpace(profile.ID), agentID) {
			return profile, true
		}
	}
	return config.AgentProfileConfig{}, false
}

func printModelRoles(out io.Writer, label string, roles config.ModelRoles) {
	keys := make([]string, 0, len(roles))
	for key := range roles {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		fmt.Fprintln(out, label+":", "-")
		return
	}
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+listOrDash(roles.Models(key)))
	}
	fmt.Fprintln(out, label+":", strings.Join(parts, " "))
}

func modelChain(primary, fallback config.ModelSelection) []string {
	var out []string
	seen := map[string]bool{}
	add := func(values ...string) {
		for _, value := range values {
			value = strings.TrimSpace(value)
			key := strings.ToLower(value)
			if value == "" || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, value)
		}
	}
	add(primary.Default)
	add(primary.Fallbacks...)
	add(fallback.Default)
	add(fallback.Fallbacks...)
	return out
}

func listOrDash(values []string) string {
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	if len(out) == 0 {
		return "-"
	}
	return strings.Join(out, ",")
}

func valueOrDash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}
