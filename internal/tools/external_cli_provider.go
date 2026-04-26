package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/dongping/mateway/internal/cmdresolve"
)

type ExternalCLIProvider struct {
	Name            string
	BinaryPath      string
	Enabled         bool
	Description     string
	ListArgs        []string
	AllowedCommands []string
	BlockedCommands []string
	Env             map[string]string
	RiskLevel       string
}

func (p ExternalCLIProvider) Tools(_ context.Context, _ Scope) ([]Tool, error) {
	if !p.Enabled {
		return nil, nil
	}
	name := sanitizeCLIProviderName(p.Name)
	if name == "" {
		return nil, nil
	}
	bin := strings.TrimSpace(p.BinaryPath)
	if bin == "" {
		return nil, nil
	}
	resolution, err := cmdresolve.Default().Resolve(bin)
	if err != nil {
		return nil, nil
	}
	riskLevel := strings.TrimSpace(p.RiskLevel)
	if riskLevel == "" {
		riskLevel = "medium"
	}
	listArgs := compactArgs(p.ListArgs)
	if len(listArgs) == 0 {
		listArgs = []string{"--help"}
	}
	return []Tool{
		externalCLIListTool{
			name:        name,
			description: firstNonEmpty(strings.TrimSpace(p.Description), fmt.Sprintf("List available commands for the %s external CLI provider.", name)),
			binaryPath:  resolution.Path,
			args:        listArgs,
			env:         cloneEnv(p.Env),
		},
		externalCLIRunTool{
			name:            name,
			description:     firstNonEmpty(strings.TrimSpace(p.Description), fmt.Sprintf("Run selected commands from the %s external CLI provider.", name)),
			binaryPath:      resolution.Path,
			allowedCommands: compactArgs(p.AllowedCommands),
			blockedCommands: compactArgs(p.BlockedCommands),
			env:             cloneEnv(p.Env),
			riskLevel:       riskLevel,
		},
	}, nil
}

type externalCLIListTool struct {
	name        string
	description string
	binaryPath  string
	args        []string
	env         map[string]string
}

func (t externalCLIListTool) Spec() Spec {
	return Spec{
		Name:        t.name + "_list",
		Description: t.description,
		Kind:        KindCLI,
		ReadOnly:    true,
		Tags:        []string{"cli", "discovery", t.name},
		InputSchema: schemaObject(),
	}
}

func (t externalCLIListTool) Invoke(ctx context.Context, _ Call) (Result, error) {
	out, err := runCLI(ctx, t.binaryPath, t.args, t.env)
	if err != nil {
		return Result{}, fmt.Errorf("%s list failed: %s", t.name, err.Error())
	}
	return rawResult(trimCLIOutput(out)), nil
}

type externalCLIRunTool struct {
	name            string
	description     string
	binaryPath      string
	allowedCommands []string
	blockedCommands []string
	env             map[string]string
	riskLevel       string
}

func (t externalCLIRunTool) Spec() Spec {
	description := strings.TrimSpace(t.description)
	if description == "" {
		description = fmt.Sprintf("Run selected commands from the %s external CLI provider.", t.name)
	}
	if len(t.allowedCommands) > 0 {
		description = fmt.Sprintf("%s Allowed root commands: %s.", strings.TrimRight(description, "."), strings.Join(t.allowedCommands, ", "))
	}
	return Spec{
		Name:        t.name + "_run",
		Description: description,
		Kind:        KindCLI,
		ReadOnly:    false,
		RiskLevel:   t.riskLevel,
		Tags:        append([]string{"cli", t.name}, compactArgs(append([]string(nil), t.allowedCommands...))...),
		InputSchema: schemaObject(
			prop("args", "array", "Argument array such as [\"search\", \"OpenAI\"] or [\"reddit\", \"search\", \"OpenAI\"]"),
		),
	}
}

func (t externalCLIRunTool) Invoke(ctx context.Context, call Call) (Result, error) {
	var args struct {
		Args []string `json:"args"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	clean := compactArgs(args.Args)
	if len(clean) == 0 {
		return Result{}, fmt.Errorf("args is required")
	}
	root := clean[0]
	if commandBlocked(root, t.blockedCommands) {
		return rawResult(cliPolicyDeniedMessage(t.name, root, "blocked", t.allowedCommands, t.blockedCommands)), nil
	}
	if len(t.allowedCommands) > 0 && !commandAllowed(root, t.allowedCommands) {
		return rawResult(cliPolicyDeniedMessage(t.name, root, "not_allowed", t.allowedCommands, t.blockedCommands)), nil
	}
	out, err := runCLI(ctx, t.binaryPath, clean, t.env)
	if err != nil {
		return Result{}, fmt.Errorf("%s run failed: %s", t.name, err.Error())
	}
	return rawResult(trimCLIOutput(out)), nil
}

func cliPolicyDeniedMessage(providerName, command, reason string, allowed, blocked []string) string {
	parts := []string{
		fmt.Sprintf("provider policy deny: external cli command %q %s for provider %q", command, strings.ReplaceAll(reason, "_", " "), providerName),
	}
	if len(allowed) > 0 {
		parts = append(parts, "allowed root commands: "+strings.Join(allowed, ", "))
	}
	if len(blocked) > 0 {
		parts = append(parts, "blocked root commands: "+strings.Join(blocked, ", "))
	}
	parts = append(parts, "try another allowed command or switch to web_search/browser_fetch instead")
	return strings.Join(parts, "; ")
}

func runCLI(ctx context.Context, binary string, args []string, env map[string]string) (string, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	if len(env) > 0 {
		pairs := make([]string, 0, len(env))
		for key, value := range env {
			pairs = append(pairs, fmt.Sprintf("%s=%s", key, value))
		}
		sort.Strings(pairs)
		cmd.Env = append(cmd.Environ(), pairs...)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)), err
	}
	return string(out), nil
}

func compactArgs(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func cloneEnv(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func sanitizeCLIProviderName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return ""
	}
	var b strings.Builder
	lastUnderscore := false
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}

func commandAllowed(root string, allowlist []string) bool {
	root = strings.ToLower(strings.TrimSpace(root))
	for _, allowed := range allowlist {
		if strings.ToLower(strings.TrimSpace(allowed)) == root {
			return true
		}
	}
	return false
}

func commandBlocked(root string, denylist []string) bool {
	root = strings.ToLower(strings.TrimSpace(root))
	for _, blocked := range denylist {
		if strings.ToLower(strings.TrimSpace(blocked)) == root {
			return true
		}
	}
	return false
}

func trimCLIOutput(out string) string {
	out = strings.TrimSpace(out)
	if len(out) > 4000 {
		return out[:4000]
	}
	return out
}
