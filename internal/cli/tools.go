package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/agentprofile"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/tool"
)

func PrintTools(out io.Writer, cfg *config.Root, agentID string, verbose bool) error {
	if cfg == nil {
		return fmt.Errorf("config is required")
	}
	registry := tool.NewRegistry(cfg)
	profile, hasProfile := findAgentProfile(cfg, firstNonEmpty(agentID, cfg.Agents.Default))
	status := map[string]string{}
	for _, item := range registry.List() {
		status[item.Name()] = "enabled"
	}
	if hasProfile {
		filtered := tool.NewRegistryForProfile(cfg, profile)
		filteredSet := map[string]bool{}
		for _, item := range filtered.List() {
			filteredSet[item.Name()] = true
		}
		for name := range status {
			if !filteredSet[name] {
				status[name] = "disabled"
			}
		}
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if verbose {
		fmt.Fprintln(tw, "NAME\tSTATUS\tRISK\tREQUIRED\tPARALLEL\tCONFIRMATION\tDESCRIPTION")
	} else {
		fmt.Fprintln(tw, "NAME\tSTATUS\tRISK\tREQUIRED\tCONFIRMATION")
	}
	for _, tool := range registry.List() {
		contract := agentcore.ContractFor(tool)
		required := strings.Join(tool.Schema().Required, ",")
		if required == "" {
			required = "-"
		}
		confirmation := compactInline(contract.ConfirmationBoundary, 72)
		if confirmation == "" {
			confirmation = "-"
		}
		if verbose {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", tool.Name(), status[tool.Name()], tool.Risk(), required, firstNonEmpty(contract.ParallelMode, "-"), confirmation, compactInline(tool.Description(), 96))
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", tool.Name(), status[tool.Name()], tool.Risk(), required, confirmation)
	}
	return tw.Flush()
}

type ToolAccessChange struct {
	AgentID string
	Tool    string
	Action  string
	Allow   []string
	Deny    []string
}

func EnableTool(cfg *config.Root, agentID, toolName string) (ToolAccessChange, error) {
	return updateToolAccess(cfg, agentID, toolName, "enable")
}

func DisableTool(cfg *config.Root, agentID, toolName string) (ToolAccessChange, error) {
	return updateToolAccess(cfg, agentID, toolName, "disable")
}

func updateToolAccess(cfg *config.Root, agentID, toolName, action string) (ToolAccessChange, error) {
	if cfg == nil {
		return ToolAccessChange{}, fmt.Errorf("config is required")
	}
	cfg.NormalizeForUse()
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return ToolAccessChange{}, fmt.Errorf("tool name is required")
	}
	if _, ok := tool.NewRegistry(cfg).Get(toolName); !ok {
		return ToolAccessChange{}, fmt.Errorf("tool %q not found", toolName)
	}
	index, profile, ok := agentProfileIndex(cfg, agentID)
	if !ok {
		return ToolAccessChange{}, fmt.Errorf("agent profile %q not found", firstNonEmpty(agentID, cfg.Agents.Default))
	}
	switch action {
	case "enable":
		profile.Tools.Deny = removeAccessValue(profile.Tools.Deny, toolName)
		if len(profile.Tools.Allow) > 0 {
			profile.Tools.Allow = addAccessValue(profile.Tools.Allow, toolName)
		}
	case "disable":
		profile.Tools.Deny = addAccessValue(profile.Tools.Deny, toolName)
	case "allow-only":
		profile.Tools.Allow = addAccessValue(profile.Tools.Allow, toolName)
		profile.Tools.Deny = removeAccessValue(profile.Tools.Deny, toolName)
	default:
		return ToolAccessChange{}, fmt.Errorf("unknown tool access action %q", action)
	}
	sort.Strings(profile.Tools.Allow)
	sort.Strings(profile.Tools.Deny)
	cfg.Agents.Profiles[index] = profile
	if err := agentprofile.SaveConfig(cfg); err != nil {
		return ToolAccessChange{}, err
	}
	return ToolAccessChange{AgentID: profile.ID, Tool: toolName, Action: action, Allow: profile.Tools.Allow, Deny: profile.Tools.Deny}, nil
}

func agentProfileIndex(cfg *config.Root, agentID string) (int, config.AgentProfileConfig, bool) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		agentID = strings.TrimSpace(cfg.Agents.Default)
	}
	for i, profile := range cfg.Agents.Profiles {
		if strings.EqualFold(strings.TrimSpace(profile.ID), agentID) {
			return i, profile, true
		}
	}
	return -1, config.AgentProfileConfig{}, false
}

func addAccessValue(values []string, value string) []string {
	key := strings.ToLower(strings.TrimSpace(value))
	seen := map[string]bool{}
	var out []string
	for _, item := range values {
		item = strings.TrimSpace(item)
		itemKey := strings.ToLower(item)
		if item == "" || seen[itemKey] {
			continue
		}
		seen[itemKey] = true
		out = append(out, item)
	}
	if key != "" && !seen[key] {
		out = append(out, value)
	}
	return out
}

func removeAccessValue(values []string, value string) []string {
	key := strings.ToLower(strings.TrimSpace(value))
	var out []string
	seen := map[string]bool{}
	for _, item := range values {
		item = strings.TrimSpace(item)
		itemKey := strings.ToLower(item)
		if item == "" || itemKey == key || seen[itemKey] {
			continue
		}
		seen[itemKey] = true
		out = append(out, item)
	}
	return out
}

func PrintToolAccessChange(out io.Writer, change ToolAccessChange) {
	fmt.Fprintln(out, "agent:", change.AgentID)
	fmt.Fprintln(out, "tool:", change.Tool)
	fmt.Fprintln(out, "action:", change.Action)
	fmt.Fprintln(out, "allow:", listOrDash(change.Allow))
	fmt.Fprintln(out, "deny:", listOrDash(change.Deny))
}
