package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

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
			source:      resolution.Source,
			shellPath:   resolution.ShellPath,
		},
		externalCLIRunTool{
			name:            name,
			description:     firstNonEmpty(strings.TrimSpace(p.Description), fmt.Sprintf("Run selected commands from the %s external CLI provider.", name)),
			binaryPath:      resolution.Path,
			discoveryArgs:   listArgs,
			allowedCommands: compactArgs(p.AllowedCommands),
			blockedCommands: compactArgs(p.BlockedCommands),
			env:             cloneEnv(p.Env),
			riskLevel:       riskLevel,
			shellPath:       resolution.ShellPath,
		},
	}, nil
}

type externalCLIListTool struct {
	name        string
	description string
	binaryPath  string
	args        []string
	env         map[string]string
	source      string
	shellPath   string
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

func (t externalCLIListTool) Availability(ctx context.Context) Availability {
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := runCLI(probeCtx, t.binaryPath, t.args, t.env)
	if err != nil {
		return Availability{
			Available: false,
			Reason:    cliProviderUnavailableMessage(t.name, "list", t.args, nil, t.shellPath, err.Error()),
		}
	}
	if strings.TrimSpace(out) == "" {
		return Availability{
			Available: false,
			Reason:    fmt.Sprintf("provider %q resolved locally but its discovery command returned no output", t.name),
		}
	}
	return Availability{Available: true}
}

func (t externalCLIListTool) Invoke(ctx context.Context, _ Call) (Result, error) {
	out, err := runCLI(ctx, t.binaryPath, t.args, t.env)
	if err != nil {
		return rawResult(cliProviderUnavailableMessage(t.name, "list", t.args, nil, t.shellPath, err.Error())), nil
	}
	payload, _ := json.Marshal(map[string]any{
		"provider":            t.name,
		"binary_path":         t.binaryPath,
		"resolution_source":   t.source,
		"shell_path":          t.shellPath,
		"help_probe_strategy": defaultCLIHelpProbeOrder(),
		"output":              trimCLIOutput(out),
	})
	return Result{Output: payload}, nil
}

type externalCLIRunTool struct {
	name            string
	description     string
	binaryPath      string
	discoveryArgs   []string
	allowedCommands []string
	blockedCommands []string
	env             map[string]string
	riskLevel       string
	shellPath       string
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

func (t externalCLIRunTool) Availability(ctx context.Context) Availability {
	listTool := externalCLIListTool{
		name:       t.name,
		binaryPath: t.binaryPath,
		args:       compactArgs(t.discoveryArgs),
		env:        t.env,
		shellPath:  t.shellPath,
	}
	if len(listTool.args) == 0 {
		listTool.args = []string{"--help"}
	}
	return listTool.Availability(ctx)
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
	if preflight := t.preflight(ctx, root); preflight != "" {
		return rawResult(preflight), nil
	}
	out, err := runCLI(ctx, t.binaryPath, clean, t.env)
	if err != nil {
		if recoverable, ok := t.recoverableFailure(root, clean, err); ok {
			return rawResult(recoverable), nil
		}
		return Result{}, fmt.Errorf("%s run failed: %s", t.name, err.Error())
	}
	return rawResult(trimCLIOutput(out)), nil
}

func (t externalCLIRunTool) preflight(ctx context.Context, root string) string {
	if strings.TrimSpace(root) == "" {
		return ""
	}
	// Learn-before-run: verify the root command has a discoverable help surface first.
	for _, probe := range helpProbeCandidates(root) {
		probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		out, err := runCLI(probeCtx, t.binaryPath, probe, t.env)
		cancel()
		if err == nil && strings.TrimSpace(out) != "" {
			return ""
		}
	}
	return cliLearnBeforeRunMessage(t.name, root, t.allowedCommands, t.shellPath)
}

func (t externalCLIRunTool) recoverableFailure(root string, args []string, err error) (string, bool) {
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(msg, "executable file not found"), strings.Contains(msg, "not found"), strings.Contains(msg, "exit status 127"):
		return cliProviderUnavailableMessage(t.name, root, args, t.allowedCommands, t.shellPath, err.Error()), true
	case strings.Contains(msg, "unknown command"), strings.Contains(msg, "invalid choice"), strings.Contains(msg, "no such command"):
		return cliLearnBeforeRunMessage(t.name, root, t.allowedCommands, t.shellPath), true
	default:
		return "", false
	}
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
	parts = append(parts, "learn-before-run: call the provider list/help command first, then try another allowed command or switch to web_search/browser_fetch")
	return strings.Join(parts, "; ")
}

func cliLearnBeforeRunMessage(providerName, command string, allowed []string, shellPath string) string {
	parts := []string{
		fmt.Sprintf("learn-before-run: provider %q could not verify a help surface for root command %q", providerName, command),
		"first inspect the provider with its *_list tool or local help probes before trying to execute a task command",
		"if local inspection still fails, fall back to web_search/browser_fetch for external docs and then retry with a known supported command",
	}
	if len(allowed) > 0 {
		parts = append(parts, "allowed root commands: "+strings.Join(allowed, ", "))
	}
	if strategy := platformShellStrategy(shellPath); strategy != "" {
		parts = append(parts, strategy)
	}
	return strings.Join(parts, "; ")
}

func cliProviderUnavailableMessage(providerName, command string, args []string, allowed []string, shellPath, errText string) string {
	parts := []string{
		fmt.Sprintf("provider unavailable: %q failed while running %q with args %v", providerName, command, args),
		"do not stop here; switch to another completion route such as web_search/browser_fetch or a different local tool",
		"if this provider is important, inspect availability and docs first instead of repeating the same failing command",
		"error=" + strings.TrimSpace(errText),
	}
	if len(allowed) > 0 {
		parts = append(parts, "allowed root commands: "+strings.Join(allowed, ", "))
	}
	if strategy := platformShellStrategy(shellPath); strategy != "" {
		parts = append(parts, strategy)
	}
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

func helpProbeCandidates(root string) [][]string {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	switch runtime.GOOS {
	case "windows":
		return [][]string{
			{root, "--help"},
			{root, "-h"},
			{"help", root},
			{root, "/?"},
		}
	default:
		return [][]string{
			{root, "--help"},
			{root, "-h"},
			{"help", root},
			{root, "help"},
		}
	}
}

func defaultCLIHelpProbeOrder() []string {
	if runtime.GOOS == "windows" {
		return []string{"<root> --help", "<root> -h", "help <root>", "<root> /?"}
	}
	return []string{"<root> --help", "<root> -h", "help <root>", "<root> help"}
}

func platformShellStrategy(shellPath string) string {
	base := strings.ToLower(strings.TrimSpace(shellPath))
	switch runtime.GOOS {
	case "windows":
		return "platform-shell-strategy: prefer standalone executables first; if you must inspect shell-specific usage, try PowerShell/cmd help styles such as --help, -h, and /?"
	default:
		if strings.Contains(base, "zsh") || strings.Contains(base, "bash") || strings.Contains(base, "sh") {
			return "platform-shell-strategy: prefer standalone executables first; only use shell-specific inspection when the command is shell-bound, and try --help, -h, then help"
		}
		return "platform-shell-strategy: prefer standalone executables first and keep shell assumptions minimal"
	}
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
