package tool

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/secret"
)

type TerminalRunTool struct{ Config *config.Root }

func (TerminalRunTool) Name() string        { return "terminal.run" }
func (TerminalRunTool) Description() string { return "run a local shell command" }
func (TerminalRunTool) Schema() agentcore.Schema {
	return agentcore.Schema{
		Required: []string{"command"},
		Properties: map[string]any{
			"command":         map[string]any{"type": "string"},
			"timeout_seconds": map[string]any{"type": "integer"},
			"env_secrets": map[string]any{
				"type":        "array",
				"description": "Optional secret ids to inject as environment variables. Items use {\"id\":\"secret/id\",\"env\":\"ENV_NAME\"}.",
			},
		},
	}
}

func (TerminalRunTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{
		WhenToUse:            "Use for local verification commands such as tests, builds, or simple shell inspection. If an external skill, README, or setup guide says to run Bash, shell, CLI, command-line, or terminal commands, execute those commands through terminal.run.",
		WhenNotToUse:         "Do not invent Bash or shell tool names. Do not use for destructive commands, secret exfiltration, or long-running services unless explicitly requested.",
		OutputContract:       "Return combined stdout/stderr, trimmed, with command evidence.",
		Evidence:             "Return the command, combined output, and redacted env secret ids when injected.",
		Acceptance:           "Accepted when the command exits successfully; failed when the command returns an error.",
		SoftFailureSignals:   []string{"non-zero exit", "timeout", "command not found", "permission denied"},
		ParallelMode:         "forbid",
		ReusePolicy:          "never",
		ConfirmationBoundary: "guarded mutation; destructive commands are blocked instead of confirmed.",
	}
}

func (TerminalRunTool) Risk() agentcore.Risk { return agentcore.RiskGuardedMutation }

func (t TerminalRunTool) Run(ctx context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	start := time.Now()
	command := fmt.Sprint(call.Args["command"])
	if err := rejectCommandContainingKnownSecret(command, t.Config); err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true, Evidence: map[string]any{"blocked": true, "reason": "secret_literal"}}
	}
	decision := CheckTerminalCommand(command, t.Config)
	if !decision.Allow {
		return agentcore.ToolResult{
			ToolCallID: call.ID,
			Content:    decision.Reason,
			IsError:    true,
			Evidence:   map[string]any{"command": command, "policy_classification": decision.Class, "decision": "blocked", "reason": decision.Reason},
		}
	}
	timeout := terminalDeadline(command, call.Args["timeout_seconds"], t.Config)
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	execName, execArgs := terminalCommand(t.Config, command)
	cmd := exec.CommandContext(timeoutCtx, execName, execArgs...)
	configureTerminalProcess(cmd)
	envSecrets, err := terminalEnvSecrets(call.Args["env_secrets"], t.Config)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true, Evidence: map[string]any{"command": command}}
	}
	if len(envSecrets.Env) > 0 {
		cmd.Env = append(os.Environ(), envSecrets.Env...)
	}
	workdir, err := terminalWorkdir(t.Config)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true, Evidence: map[string]any{"command": command}}
	}
	if workdir != "" {
		cmd.Dir = workdir
	}
	output, err := cmd.CombinedOutput()
	elapsed := time.Since(start)
	timedOut := timeoutCtx.Err() == context.DeadlineExceeded
	cancelled := timeoutCtx.Err() == context.Canceled
	raw, outputTruncated := truncateToolOutput(string(output), 512*1024)
	result := strings.TrimSpace(raw)
	evidence := map[string]any{
		"command":               command,
		"policy_classification": decision.Class,
		"decision":              "allowed",
		"timed_out":             timedOut,
		"cancelled":             cancelled,
		"elapsed_ms":            elapsed.Milliseconds(),
		"deadline_ms":           timeout.Milliseconds(),
		"output_truncated":      outputTruncated,
	}
	if timedOut {
		evidence["kill_signal"] = terminalKillSignal()
	}
	if len(envSecrets.Evidence) > 0 {
		evidence["env_secrets"] = envSecrets.Evidence
	}
	if decision.RemoteProfile != "" {
		evidence["remote_profile"] = decision.RemoteProfile
	}
	if workdir != "" {
		evidence["workdir"] = workdir
	}
	if t.Config != nil && t.Config.Security.TerminalSandbox.Enabled {
		evidence["sandbox"] = t.Config.Security.TerminalSandbox.Mode
	}
	if err != nil {
		if result == "" {
			result = err.Error()
		}
		if timedOut {
			result = "command timed out after " + timeout.String()
		} else if cancelled {
			result = "command cancelled: " + timeoutCtx.Err().Error()
		}
		return agentcore.ToolResult{ToolCallID: call.ID, Content: result, IsError: true, Evidence: evidence}
	}
	return agentcore.ToolResult{ToolCallID: call.ID, Content: result, Evidence: evidence}
}

func rejectCommandContainingKnownSecret(command string, cfg *config.Root) error {
	store := secret.Store{Home: configHome(cfg)}
	entries, err := store.List()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		full, ok, err := store.Get(entry.ID)
		if err != nil {
			return err
		}
		if !ok || strings.TrimSpace(full.Value) == "" {
			continue
		}
		if len(full.Value) >= 8 && strings.Contains(command, full.Value) {
			return fmt.Errorf("refusing to run terminal command containing secret value %s; use secret.set and terminal.run env_secrets instead", entry.ID)
		}
	}
	return nil
}

type terminalEnvSecretResult struct {
	Env      []string
	Evidence []map[string]string
}

func terminalEnvSecrets(value any, cfg *config.Root) (terminalEnvSecretResult, error) {
	items, err := mapListArg(value)
	if err != nil {
		return terminalEnvSecretResult{}, err
	}
	if len(items) == 0 {
		return terminalEnvSecretResult{}, nil
	}
	store := secret.Store{Home: configHome(cfg)}
	var result terminalEnvSecretResult
	for _, item := range items {
		id := strings.TrimSpace(fmt.Sprint(item["id"]))
		envName := strings.TrimSpace(fmt.Sprint(item["env"]))
		if id == "" || envName == "" {
			return terminalEnvSecretResult{}, fmt.Errorf("env_secrets items require id and env")
		}
		entry, ok, err := store.Get(id)
		if err != nil {
			return terminalEnvSecretResult{}, err
		}
		if !ok {
			return terminalEnvSecretResult{}, fmt.Errorf("missing required secret %s", id)
		}
		result.Env = append(result.Env, envName+"="+entry.Value)
		result.Evidence = append(result.Evidence, map[string]string{"id": strings.ToLower(strings.TrimSpace(id)), "env": envName})
	}
	return result, nil
}

func terminalTimeout(cfg *config.Root) time.Duration {
	seconds := 20
	if cfg != nil && cfg.Security.TerminalSandbox.TimeoutSeconds > 0 {
		seconds = cfg.Security.TerminalSandbox.TimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}

func terminalDeadline(command string, requested any, cfg *config.Root) time.Duration {
	timeout := terminalTimeout(cfg)
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	if isInspectCommand(command) && timeout < 60*time.Second {
		timeout = 60 * time.Second
	}
	if isBuildOrTestCommand(command) && timeout < 300*time.Second {
		timeout = 300 * time.Second
	}
	max := 600 * time.Second
	if isBuildOrTestCommand(command) {
		max = 1800 * time.Second
	}
	if isDestructiveCommand(command) {
		max = 60 * time.Second
		if timeout > 20*time.Second {
			timeout = 20 * time.Second
		}
	}
	if seconds := intArg(requested); seconds > 0 {
		timeout = time.Duration(seconds) * time.Second
	}
	if timeout > max {
		return max
	}
	return timeout
}

func isInspectCommand(command string) bool {
	lower := strings.ToLower(strings.TrimSpace(command))
	for _, marker := range []string{"ls ", "ls\t", "find ", "du ", "grep ", "rg ", "cat ", "sed ", "head ", "tail ", "pwd", "stat ", "wc "} {
		if strings.HasPrefix(lower, marker) || strings.Contains(lower, "&& "+marker) || strings.Contains(lower, "; "+marker) || strings.Contains(lower, "| "+marker) {
			return true
		}
	}
	return false
}

func isBuildOrTestCommand(command string) bool {
	lower := strings.ToLower(command)
	for _, marker := range []string{"go test", "go build", "npm test", "npm run", "pnpm test", "pnpm run", "yarn test", "pytest", "cargo test", "mvn test"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func isDestructiveCommand(command string) bool {
	lower := strings.ToLower(command)
	for _, marker := range []string{"rm ", "rm\t", "mv ", "mv\t", "sudo ", "chmod ", "chown ", "dd "} {
		if strings.HasPrefix(lower, marker) || strings.Contains(lower, "&& "+marker) || strings.Contains(lower, "; "+marker) {
			return true
		}
	}
	return false
}

func truncateToolOutput(raw string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(raw) <= maxBytes {
		return raw, false
	}
	out := raw[:maxBytes]
	for !utf8.ValidString(out) && len(out) > 0 {
		out = out[:len(out)-1]
	}
	return out + "\n... (output truncated at 512KB)", true
}

func terminalCommand(cfg *config.Root, command string) (string, []string) {
	if cfg != nil && cfg.Security.TerminalSandbox.Enabled && len(cfg.Security.TerminalSandbox.CommandPrefix) > 0 {
		prefix := cfg.Security.TerminalSandbox.CommandPrefix
		args := append([]string{}, prefix[1:]...)
		args = append(args, command)
		return prefix[0], args
	}
	return "/bin/sh", []string{"-lc", command}
}

func terminalWorkdir(cfg *config.Root) (string, error) {
	if cfg == nil || !cfg.Security.TerminalSandbox.Enabled {
		return "", nil
	}
	raw := strings.TrimSpace(cfg.Security.TerminalSandbox.WorkDir)
	if raw == "" {
		raw = strings.TrimSpace(cfg.App.Workspace)
	}
	if raw == "" {
		raw = strings.TrimSpace(cfg.App.Home)
	}
	if raw == "" {
		return "", nil
	}
	return ResolveAllowedPath(raw, cfg)
}
