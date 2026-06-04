package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/agentprofile"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/schedule"
	"github.com/dongping/mateway/internal/script"
	"github.com/dongping/mateway/internal/secret"
	"gopkg.in/yaml.v3"
)

func NewRegistry(cfg ...*config.Root) *agentcore.ToolRegistry {
	var root *config.Root
	if len(cfg) > 0 {
		root = cfg[0]
	}
	registry := agentcore.NewToolRegistry()
	registry.Register(FileReadTool{Config: root})
	registry.Register(FileWriteTool{Config: root})
	registry.Register(ProjectIndexTool{Config: root})
	registry.Register(TerminalRunTool{Config: root})
	registry.Register(ScriptRunTool{Config: root})
	registry.Register(SecretSetTool{Config: root})
	registry.Register(ScheduleCreateTool{Config: root})
	registry.Register(ScheduleListTool{Config: root})
	registry.Register(RemoteProfileCreateTool{Config: root})
	registry.Register(WebSearchTool{Config: root})
	registry.Register(WebFetchTool{Config: root})
	return registry
}

type EchoTool struct{}

func (EchoTool) Name() string        { return "echo" }
func (EchoTool) Description() string { return "echo text back to the model" }
func (EchoTool) Schema() agentcore.Schema {
	return agentcore.Schema{Required: []string{"text"}}
}
func (EchoTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{
		WhenToUse:            "Only in development tests that need deterministic tool echo behavior.",
		WhenNotToUse:         "Do not use for normal user tasks; answer directly instead.",
		OutputContract:       "Return exactly the provided text.",
		Evidence:             "Echoes the provided text.",
		Acceptance:           "Accepted when returned text matches the requested echo text.",
		ParallelMode:         "read_only_ok",
		ReusePolicy:          "never",
		ConfirmationBoundary: "safe read; no confirmation.",
	}
}

type FileWriteTool struct{ Config *config.Root }
type FileReadTool struct{ Config *config.Root }
type ProjectIndexTool struct{ Config *config.Root }

func (FileWriteTool) Name() string        { return "file.write" }
func (FileWriteTool) Description() string { return "write a local text file" }
func (FileWriteTool) Schema() agentcore.Schema {
	return agentcore.Schema{Required: []string{"path", "content"}}
}
func (FileWriteTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{
		WhenToUse:            "Use when the task explicitly requires creating or replacing a local text file.",
		WhenNotToUse:         "Do not use to answer questions, inspect files, or modify files outside the allowed workspace.",
		OutputContract:       "Return a short write confirmation with path and byte count evidence.",
		Evidence:             "Return the written path and byte count.",
		Acceptance:           "Accepted when the file write succeeds and evidence includes path and bytes.",
		SoftFailureSignals:   []string{"permission denied", "outside allowed roots", "no such file or directory"},
		ParallelMode:         "forbid",
		ReusePolicy:          "never",
		ConfirmationBoundary: "guarded mutation; require confirmation when security.require_approval_for_risky_tools is true.",
	}
}
func (FileWriteTool) Risk() agentcore.Risk { return agentcore.RiskGuardedMutation }
func (t FileWriteTool) Run(_ context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	path, err := ResolveAllowedPath(fmt.Sprint(call.Args["path"]), t.Config)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true, Evidence: map[string]any{"path": fmt.Sprint(call.Args["path"])}}
	}
	content := fmt.Sprint(call.Args["content"])
	profileStore := agentprofile.NewStore(t.Config)
	if _, ok := profileStore.CoreTargetAgent(path); ok {
		proposal, err := profileStore.Create(agentprofile.CreateInput{TargetPath: path, NewContent: content})
		if err != nil {
			return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true, Evidence: map[string]any{"path": path}}
		}
		return agentcore.ToolResult{
			ToolCallID: call.ID,
			Content:    "profile proposal " + proposal.ID + " created for " + proposal.TargetPath + "; promote with mateway agent-profile proposal promote " + proposal.ID,
			Evidence: map[string]any{
				"proposal_id":     proposal.ID,
				"target_path":     proposal.TargetPath,
				"requires_review": true,
			},
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true}
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true}
	}
	return agentcore.ToolResult{ToolCallID: call.ID, Content: "wrote " + path, Evidence: map[string]any{"path": path, "bytes": len(content)}}
}

func (ProjectIndexTool) Name() string        { return "project.index" }
func (ProjectIndexTool) Description() string { return "list files under a project directory" }
func (ProjectIndexTool) Schema() agentcore.Schema {
	return agentcore.Schema{Required: []string{"path"}}
}
func (ProjectIndexTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{
		WhenToUse:            "Use before reading a project when you need an overview of files and directories.",
		WhenNotToUse:         "Do not use as a replacement for reading a specific file whose path is already known.",
		OutputContract:       "Return newline-delimited relative file paths, capped to the tool limit.",
		Evidence:             "Return scanned root path and file count.",
		Acceptance:           "Accepted when the directory scan succeeds and returns file count evidence.",
		SoftFailureSignals:   []string{"path is not a directory", "permission denied", "outside allowed roots"},
		ParallelMode:         "read_only_ok",
		ReusePolicy:          "stable_read",
		ConfirmationBoundary: "safe read; no confirmation.",
	}
}
func (ProjectIndexTool) Risk() agentcore.Risk { return agentcore.RiskSafeRead }
func (t ProjectIndexTool) Run(_ context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	root, err := ResolveAllowedPath(fmt.Sprint(call.Args["path"]), t.Config)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true, Evidence: map[string]any{"path": fmt.Sprint(call.Args["path"])}}
	}
	limit := 120
	var files []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || len(files) >= limit {
			return err
		}
		name := d.Name()
		if d.IsDir() && (name == ".git" || name == "node_modules" || name == "dist" || name == "build") {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(root, path)
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true}
	}
	sort.Strings(files)
	return agentcore.ToolResult{ToolCallID: call.ID, Content: strings.Join(files, "\n"), Evidence: map[string]any{"path": root, "count": len(files)}}
}

type TerminalRunTool struct{ Config *config.Root }
type ScriptRunTool struct{ Config *config.Root }
type SecretSetTool struct{ Config *config.Root }
type ScheduleCreateTool struct{ Config *config.Root }
type ScheduleListTool struct{ Config *config.Root }
type RemoteProfileCreateTool struct{ Config *config.Root }

func (TerminalRunTool) Name() string        { return "terminal.run" }
func (TerminalRunTool) Description() string { return "run a local shell command" }
func (TerminalRunTool) Schema() agentcore.Schema {
	return agentcore.Schema{Required: []string{"command"}}
}
func (TerminalRunTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{
		WhenToUse:            "Use for local verification commands such as tests, builds, or simple shell inspection.",
		WhenNotToUse:         "Do not use for destructive commands, secret exfiltration, or long-running services unless explicitly requested.",
		OutputContract:       "Return combined stdout/stderr, trimmed, with command evidence.",
		Evidence:             "Return the command and combined output.",
		Acceptance:           "Accepted when the command exits successfully; failed when the command returns an error.",
		SoftFailureSignals:   []string{"non-zero exit", "timeout", "command not found", "permission denied"},
		ParallelMode:         "forbid",
		ReusePolicy:          "never",
		ConfirmationBoundary: "guarded mutation; require confirmation when security.require_approval_for_risky_tools is true.",
	}
}
func (TerminalRunTool) Risk() agentcore.Risk { return agentcore.RiskGuardedMutation }
func (t TerminalRunTool) Run(ctx context.Context, call agentcore.ToolCall) agentcore.ToolResult {
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
	timeout := terminalTimeout(t.Config)
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	execName, execArgs := terminalCommand(t.Config, command)
	cmd := exec.CommandContext(timeoutCtx, execName, execArgs...)
	workdir, err := terminalWorkdir(t.Config)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true, Evidence: map[string]any{"command": command}}
	}
	if workdir != "" {
		cmd.Dir = workdir
	}
	output, err := cmd.CombinedOutput()
	result := strings.TrimSpace(string(output))
	if err != nil {
		if result == "" {
			result = err.Error()
		}
		return agentcore.ToolResult{ToolCallID: call.ID, Content: result, IsError: true, Evidence: map[string]any{"command": command, "policy_classification": decision.Class, "decision": "allowed"}}
	}
	evidence := map[string]any{"command": command, "policy_classification": decision.Class, "decision": "allowed"}
	if decision.RemoteProfile != "" {
		evidence["remote_profile"] = decision.RemoteProfile
	}
	if workdir != "" {
		evidence["workdir"] = workdir
	}
	if t.Config != nil && t.Config.Security.TerminalSandbox.Enabled {
		evidence["sandbox"] = t.Config.Security.TerminalSandbox.Mode
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
			return fmt.Errorf("refusing to run terminal command containing secret value %s; use secret.set and script.run required_secret injection instead", entry.ID)
		}
	}
	return nil
}

func (SecretSetTool) Name() string { return "secret.set" }
func (SecretSetTool) Description() string {
	return "store a local secret value by id without returning the value"
}
func (SecretSetTool) Schema() agentcore.Schema {
	return agentcore.Schema{Required: []string{"id", "value"}}
}
func (SecretSetTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{
		WhenToUse:            "Use when the user has provided a concrete credential, token, password, authorization code, or API key that must be available to scripts through required_secret injection.",
		WhenNotToUse:         "Do not use with placeholders, redacted values, examples, or values the user has not actually provided.",
		OutputContract:       "Return only the normalized secret id and stored=true; never return the secret value.",
		Evidence:             "Return the secret id, stored=true, and placeholder=true on placeholder rejection.",
		Acceptance:           "Accepted when the secret store persists the value without exposing it in content or evidence.",
		SoftFailureSignals:   []string{"redacted placeholder", "secret id is required", "secret value is required"},
		ParallelMode:         "forbid",
		ReusePolicy:          "never",
		ConfirmationBoundary: "guarded local secret mutation; allowed when the user provided the value in the current task.",
	}
}
func (SecretSetTool) Risk() agentcore.Risk { return agentcore.RiskGuardedMutation }
func (t SecretSetTool) Run(_ context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	id := strings.TrimSpace(fmt.Sprint(call.Args["id"]))
	value := fmt.Sprint(call.Args["value"])
	overwrite := boolArg(call.Args["overwrite"])
	if isRedactedPlaceholder(value) {
		return agentcore.ToolResult{
			ToolCallID: call.ID,
			Content:    "secret value is a redacted placeholder; ask the user to provide the real value again",
			IsError:    true,
			Evidence:   map[string]any{"id": id, "placeholder": true},
		}
	}
	if err := (secret.Store{Home: configHome(t.Config)}).SetWithOptions(id, value, secret.SetOptions{Overwrite: overwrite}); err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true, Evidence: map[string]any{"id": id}}
	}
	return agentcore.ToolResult{
		ToolCallID: call.ID,
		Content:    "secret stored: " + strings.ToLower(strings.TrimSpace(id)),
		Evidence:   map[string]any{"id": strings.ToLower(strings.TrimSpace(id)), "stored": true},
	}
}

func boolArg(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true") || strings.EqualFold(strings.TrimSpace(v), "yes")
	default:
		return false
	}
}

func isRedactedPlaceholder(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	return lower == "" || strings.Contains(lower, "redacted") || strings.Contains(lower, "placeholder")
}

func configHome(cfg *config.Root) string {
	if cfg != nil && strings.TrimSpace(cfg.App.Home) != "" {
		return strings.TrimSpace(cfg.App.Home)
	}
	return config.DefaultHome()
}

func (ScheduleCreateTool) Name() string        { return "schedule.create" }
func (ScheduleCreateTool) Description() string { return "create a local scheduled task" }
func (ScheduleCreateTool) Schema() agentcore.Schema {
	return agentcore.Schema{Required: []string{"text", "run_at"}}
}
func (ScheduleCreateTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{
		WhenToUse:            "Use when the user asks to run a task later. Scheduled tasks are channel-neutral and should be tested before activation unless the user explicitly waives testing.",
		WhenNotToUse:         "Do not use for immediate tasks; execute those directly.",
		OutputContract:       "Return scheduled task id, status, run time, interval when any, and the next test/activate step.",
		Evidence:             "Return id, status, run_at, interval, session_key.",
		Acceptance:           "Accepted when the task is persisted under the local schedule store.",
		SoftFailureSignals:   []string{"invalid run_at", "missing text"},
		ParallelMode:         "forbid",
		ReusePolicy:          "never",
		ConfirmationBoundary: "guarded mutation; require confirmation when security.require_approval_for_risky_tools is true.",
	}
}
func (ScheduleCreateTool) Risk() agentcore.Risk { return agentcore.RiskGuardedMutation }
func (t ScheduleCreateTool) Run(_ context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	runAt, err := time.Parse(time.RFC3339, toolArgString(call.Args, "run_at"))
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: "run_at must be RFC3339", IsError: true}
	}
	var interval time.Duration
	if raw := toolArgString(call.Args, "interval"); raw != "" {
		interval, err = time.ParseDuration(raw)
		if err != nil {
			return agentcore.ToolResult{ToolCallID: call.ID, Content: "interval must be a Go duration such as 30m or 24h", IsError: true}
		}
	}
	requireTest := true
	if raw := strings.ToLower(toolArgString(call.Args, "require_test")); raw == "false" || raw == "no" {
		requireTest = false
	}
	store := schedule.Store{Home: config.DefaultHome()}
	if t.Config != nil && strings.TrimSpace(t.Config.App.Home) != "" {
		store.Home = t.Config.App.Home
	}
	task, err := store.Create(schedule.CreateInput{
		SessionKey:  toolArgString(call.Args, "session_key"),
		Text:        toolArgString(call.Args, "text"),
		RunAt:       runAt,
		Interval:    interval,
		RequireTest: requireTest,
		Activate:    !requireTest,
	})
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true}
	}
	return agentcore.ToolResult{
		ToolCallID: call.ID,
		Content:    fmt.Sprintf("scheduled %s status=%s at %s; test with mateway schedule test %s before activation", task.ID, task.Status, task.RunAt, task.ID),
		Evidence: map[string]any{
			"id":          task.ID,
			"status":      task.Status,
			"run_at":      task.RunAt,
			"interval":    task.Interval,
			"session_key": task.SessionKey,
		},
	}
}

func (ScheduleListTool) Name() string        { return "schedule.list" }
func (ScheduleListTool) Description() string { return "list local scheduled tasks" }
func (ScheduleListTool) Schema() agentcore.Schema {
	return agentcore.Schema{}
}
func (ScheduleListTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{
		WhenToUse:            "Use when the user asks what scheduled tasks exist.",
		WhenNotToUse:         "Do not use for creating a new scheduled task.",
		OutputContract:       "Return one line per scheduled task.",
		Evidence:             "Return scheduled task count.",
		Acceptance:           "Accepted when the schedule store is read successfully.",
		ParallelMode:         "read_only_ok",
		ReusePolicy:          "stable_read",
		ConfirmationBoundary: "safe read; no confirmation.",
	}
}
func (ScheduleListTool) Risk() agentcore.Risk { return agentcore.RiskSafeRead }
func (t ScheduleListTool) Run(_ context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	store := schedule.Store{Home: config.DefaultHome()}
	if t.Config != nil && strings.TrimSpace(t.Config.App.Home) != "" {
		store.Home = t.Config.App.Home
	}
	tasks, err := store.List()
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true}
	}
	var lines []string
	for _, task := range tasks {
		lines = append(lines, fmt.Sprintf("%s status=%s run_at=%s interval=%s last=%s text=%s", task.ID, task.Status, task.RunAt, task.Interval, task.LastRunStatus, summarizeToolText(task.Text, 80)))
	}
	return agentcore.ToolResult{ToolCallID: call.ID, Content: strings.Join(lines, "\n"), Evidence: map[string]any{"count": len(tasks)}}
}

func (RemoteProfileCreateTool) Name() string { return "remote.profile.create" }
func (RemoteProfileCreateTool) Description() string {
	return "create or update a local remote server connection profile"
}
func (RemoteProfileCreateTool) Schema() agentcore.Schema {
	return agentcore.Schema{Required: []string{"alias", "host", "user"}}
}
func (RemoteProfileCreateTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{
		WhenToUse:            "Use when the user provides remote server host/IP, user, auth material or alias and wants future remote operations by profile.",
		WhenNotToUse:         "Do not use for one-off local commands or when host/user are missing.",
		OutputContract:       "Return profile alias, host, user, auth_secret_id and whether config was updated.",
		Evidence:             "Return alias, host, user, port, auth_secret_id, allowed_classes.",
		Acceptance:           "Accepted when the profile is persisted in config and auth material is stored as a secret when provided.",
		SoftFailureSignals:   []string{"missing alias", "missing host", "missing user", "secret overwrite required"},
		ParallelMode:         "forbid",
		ReusePolicy:          "never",
		ConfirmationBoundary: "guarded mutation; require confirmation before persisting remote profile or auth material.",
	}
}
func (RemoteProfileCreateTool) Risk() agentcore.Risk { return agentcore.RiskGuardedMutation }
func (t RemoteProfileCreateTool) Run(_ context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	alias := strings.ToLower(strings.TrimSpace(toolArgString(call.Args, "alias")))
	host := strings.TrimSpace(toolArgString(call.Args, "host"))
	user := strings.TrimSpace(toolArgString(call.Args, "user"))
	if alias == "" || host == "" || user == "" {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: "alias, host and user are required", IsError: true}
	}
	port := 22
	if raw := strings.TrimSpace(toolArgString(call.Args, "port")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			port = parsed
		}
	}
	authSecretID := strings.TrimSpace(toolArgString(call.Args, "auth_secret_id"))
	password := toolArgString(call.Args, "password")
	privateKey := toolArgString(call.Args, "private_key")
	if strings.TrimSpace(password) != "" || strings.TrimSpace(privateKey) != "" {
		if authSecretID == "" {
			authSecretID = "remote/" + alias + "/auth"
		}
		value := password
		if strings.TrimSpace(privateKey) != "" {
			value = privateKey
		}
		if err := (secret.Store{Home: configHome(t.Config)}).SetWithOptions(authSecretID, value, secret.SetOptions{Overwrite: boolArg(call.Args["overwrite_secret"])}); err != nil {
			return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true, Evidence: map[string]any{"alias": alias, "auth_secret_id": authSecretID}}
		}
	}
	profile := config.RemoteProfileConfig{
		Alias:          alias,
		Host:           host,
		User:           user,
		Port:           port,
		AuthSecretID:   authSecretID,
		AllowedClasses: stringSliceArg(call.Args["allowed_classes"]),
		RequireConfirm: true,
	}
	if len(profile.AllowedClasses) == 0 {
		profile.AllowedClasses = []string{"read_only"}
	}
	if err := persistRemoteProfile(t.Config, profile, boolArg(call.Args["overwrite_profile"])); err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true, Evidence: map[string]any{"alias": alias, "host": host, "user": user}}
	}
	return agentcore.ToolResult{ToolCallID: call.ID, Content: "remote profile stored: " + alias, Evidence: map[string]any{"alias": alias, "host": host, "user": user, "port": port, "auth_secret_id": authSecretID, "allowed_classes": profile.AllowedClasses}}
}

func persistRemoteProfile(cfg *config.Root, profile config.RemoteProfileConfig, overwrite bool) error {
	path := filepath.Join(configHome(cfg), "config", "config.yaml")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		data = []byte("{}\n")
	} else if err != nil {
		return err
	}
	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return err
	}
	if root == nil {
		root = map[string]any{}
	}
	remote, _ := root["remote"].(map[string]any)
	if remote == nil {
		remote = map[string]any{}
	}
	var profiles []any
	if raw, ok := remote["profiles"].([]any); ok {
		profiles = raw
	}
	entry := map[string]any{
		"alias":           profile.Alias,
		"host":            profile.Host,
		"user":            profile.User,
		"port":            profile.Port,
		"auth_secret_id":  profile.AuthSecretID,
		"allowed_classes": profile.AllowedClasses,
		"require_confirm": profile.RequireConfirm,
	}
	replaced := false
	for i, raw := range profiles {
		existing, _ := raw.(map[string]any)
		if strings.EqualFold(fmt.Sprint(existing["alias"]), profile.Alias) {
			if !overwrite {
				return fmt.Errorf("remote profile %s already exists; set overwrite_profile=true to replace it", profile.Alias)
			}
			profiles[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		profiles = append(profiles, entry)
	}
	remote["profiles"] = profiles
	root["remote"] = remote
	out, err := yaml.Marshal(root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

func terminalTimeout(cfg *config.Root) time.Duration {
	seconds := 20
	if cfg != nil && cfg.Security.TerminalSandbox.TimeoutSeconds > 0 {
		seconds = cfg.Security.TerminalSandbox.TimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
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

func (ScriptRunTool) Name() string        { return "script.run" }
func (ScriptRunTool) Description() string { return "run a discovered local Mateway script" }
func (ScriptRunTool) Schema() agentcore.Schema {
	return agentcore.Schema{
		Required: []string{"name"},
		Properties: map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Discovered script name, for example email.receive.",
			},
			"args": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Command-line argv array passed to the script, for example [\"--limit\",\"10\"]. Do not JSON-encode this value.",
			},
			"timeout_seconds": map[string]any{
				"type":        "integer",
				"description": "Optional timeout in seconds for long-running scripts such as media generation. Defaults to 20.",
			},
		},
	}
}
func (ScriptRunTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{
		WhenToUse:            "Use when a reusable local script exists for a connector-like task such as mail, publishing, or server operations.",
		WhenNotToUse:         "Do not use if no matching script exists; explain the connector gap instead of fabricating execution.",
		OutputContract:       "Return script output, exit code, duration, and script path evidence. Pass script arguments with args as a string array, not as JSON text.",
		Evidence:             "Return script name, path, args, exit_code, and duration_ms.",
		Acceptance:           "Accepted when the script exits successfully and returns useful output.",
		SoftFailureSignals:   []string{"script not found", "missing required secret", "non-zero exit", "timeout"},
		ParallelMode:         "forbid",
		ReusePolicy:          "stable_if_script_unchanged",
		ConfirmationBoundary: "guarded mutation; require confirmation when security.require_approval_for_risky_tools is true.",
	}
}
func (ScriptRunTool) Risk() agentcore.Risk { return agentcore.RiskGuardedMutation }
func (t ScriptRunTool) Run(ctx context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	args := stringSliceArg(call.Args["args"])
	timeoutSeconds := intArg(call.Args["timeout_seconds"])
	var timeout time.Duration
	if timeoutSeconds > 0 {
		timeout = time.Duration(timeoutSeconds) * time.Second
	}
	result, err := script.Run(ctx, t.Config, script.RunInput{
		Name:    toolArgString(call.Args, "name"),
		Args:    args,
		Timeout: timeout,
	})
	evidence := map[string]any{}
	if result.Script.Name != "" {
		evidence["script"] = result.Script.Name
		evidence["path"] = result.Script.Path
		evidence["args"] = args
		evidence["exit_code"] = result.ExitCode
		evidence["duration_ms"] = result.Duration.Milliseconds()
	}
	if err != nil {
		content := strings.TrimSpace(result.Output)
		if content == "" {
			content = err.Error()
		}
		return agentcore.ToolResult{ToolCallID: call.ID, Content: content, IsError: true, Evidence: evidence}
	}
	return agentcore.ToolResult{ToolCallID: call.ID, Content: result.Output, Evidence: evidence}
}

type WebFetchTool struct{ Config *config.Root }

func (WebFetchTool) Name() string        { return "web.fetch" }
func (WebFetchTool) Description() string { return "fetch a URL body" }
func (WebFetchTool) Schema() agentcore.Schema {
	return agentcore.Schema{Required: []string{"url"}}
}
func (WebFetchTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{
		WhenToUse:            "Use when a specific URL must be fetched or verified.",
		WhenNotToUse:         "Do not use for broad discovery; use web.search first when no URL is known.",
		OutputContract:       "Return the response body up to the tool limit and HTTP status evidence.",
		Evidence:             "Return fetched URL and HTTP status.",
		Acceptance:           "Accepted when HTTP status is below 400 and useful body content is returned.",
		SoftFailureSignals:   []string{"HTTP status >= 400", "timeout", "DNS failure", "empty body"},
		ParallelMode:         "read_only_ok",
		ReusePolicy:          "stable_read",
		ConfirmationBoundary: "safe read; no confirmation.",
	}
}
func (WebFetchTool) Risk() agentcore.Risk { return agentcore.RiskSafeRead }
func (WebFetchTool) Run(ctx context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	rawURL := fmt.Sprint(call.Args["url"])
	if blocked, ok := IsBlockedFetchURL(rawURL); ok {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: "web.fetch blocked private or local address: " + blocked, IsError: true, Evidence: map[string]any{"url": rawURL, "blocked": true, "reason": "ssrf_blocked"}}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true}
	}
	client := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if blocked, ok := IsBlockedFetchURL(req.URL.String()); ok {
				return fmt.Errorf("web.fetch redirect blocked private or local address: %s", blocked)
			}
			if len(via) >= 5 {
				return fmt.Errorf("stopped after 5 redirects")
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		if fallback, ok := fetchFallback(ctx, call.ID, rawURL, err.Error()); ok {
			return fallback
		}
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if resp.StatusCode >= 400 {
		if fallback, ok := fetchFallback(ctx, call.ID, rawURL, fmt.Sprintf("HTTP status %d", resp.StatusCode)); ok {
			return fallback
		}
	}
	return agentcore.ToolResult{ToolCallID: call.ID, Content: string(data), IsError: resp.StatusCode >= 400, Evidence: map[string]any{"url": rawURL, "status": resp.StatusCode}}
}

func fetchFallback(ctx context.Context, callID, rawURL, reason string) (agentcore.ToolResult, bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return agentcore.ToolResult{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "news.ycombinator.com" {
		return agentcore.ToolResult{}, false
	}
	switch parsed.Path {
	case "", "/", "/news", "/newest", "/front":
		return fetchHNAlgolia(ctx, callID, rawURL, reason), true
	default:
		return agentcore.ToolResult{}, false
	}
}

func fetchHNAlgolia(ctx context.Context, callID, rawURL, reason string) agentcore.ToolResult {
	endpoint := "https://hn.algolia.com/api/v1/search?tags=front_page&hitsPerPage=30"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: callID, Content: err.Error(), IsError: true}
	}
	req.Header.Set("user-agent", "mateway/0.1")
	resp, err := searchHTTPClient(8).Do(req)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: callID, Content: reason + "; HN Algolia fallback failed: " + err.Error(), IsError: true, Evidence: map[string]any{"url": rawURL, "fallback": endpoint}}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if resp.StatusCode >= 400 {
		return agentcore.ToolResult{ToolCallID: callID, Content: reason + "; HN Algolia fallback returned " + resp.Status, IsError: true, Evidence: map[string]any{"url": rawURL, "fallback": endpoint, "status": resp.StatusCode}}
	}
	return agentcore.ToolResult{
		ToolCallID: callID,
		Content:    renderHNAlgoliaFrontPage(data, rawURL, reason),
		Evidence:   map[string]any{"url": rawURL, "fallback": endpoint, "provider": "hn_algolia", "status": resp.StatusCode},
	}
}

func renderHNAlgoliaFrontPage(data []byte, rawURL, reason string) string {
	var parsed struct {
		Hits []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			ObjectID    string `json:"objectID"`
			Points      int    `json:"points"`
			NumComments int    `json:"num_comments"`
			CreatedAt   string `json:"created_at"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "Original fetch failed: " + reason + "\nHN Algolia fallback returned unparsable JSON."
	}
	var b strings.Builder
	b.WriteString("Original fetch failed: ")
	b.WriteString(reason)
	b.WriteString("\nFallback source: HN Algolia front_page API for ")
	b.WriteString(rawURL)
	b.WriteString("\n")
	for i, hit := range parsed.Hits {
		if i >= 20 {
			break
		}
		title := strings.TrimSpace(hit.Title)
		if title == "" {
			continue
		}
		itemURL := strings.TrimSpace(hit.URL)
		if itemURL == "" && hit.ObjectID != "" {
			itemURL = "https://news.ycombinator.com/item?id=" + hit.ObjectID
		}
		b.WriteString(fmt.Sprintf("\n%d. %s\n", i+1, title))
		if itemURL != "" {
			b.WriteString("URL: ")
			b.WriteString(itemURL)
			b.WriteString("\n")
		}
		if hit.ObjectID != "" {
			b.WriteString("HN item: https://news.ycombinator.com/item?id=")
			b.WriteString(hit.ObjectID)
			b.WriteString("\n")
		}
		b.WriteString(fmt.Sprintf("Points: %d Comments: %d\n", hit.Points, hit.NumComments))
		if hit.CreatedAt != "" {
			b.WriteString("Created at: ")
			b.WriteString(hit.CreatedAt)
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
}

type WebSearchTool struct{ Config *config.Root }

func (WebSearchTool) Name() string { return "web.search" }
func (WebSearchTool) Description() string {
	return "search the web and return structured source summaries"
}
func (WebSearchTool) Schema() agentcore.Schema {
	return agentcore.Schema{Required: []string{"query"}}
}
func (WebSearchTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{
		WhenToUse:            "Use when the task requires current or external information and no specific source URL is known.",
		WhenNotToUse:         "Do not use when the answer can be produced from local files, conversation context, or a known URL.",
		OutputContract:       "Return compact structured results with title, URL, summary, provider, date hints, and official/third-party classification.",
		Evidence:             "Return query, provider, HTTP status, and result count.",
		Acceptance:           "Accepted when the search request succeeds with HTTP status below 400.",
		SoftFailureSignals:   []string{"HTTP status >= 400", "DNS failure", "empty result page", "provider block"},
		ParallelMode:         "read_only_ok",
		ReusePolicy:          "stable_read",
		ConfirmationBoundary: "safe read; no confirmation.",
	}
}
func (WebSearchTool) Risk() agentcore.Risk { return agentcore.RiskSafeRead }
func (t WebSearchTool) Run(ctx context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	query := strings.TrimSpace(fmt.Sprint(call.Args["query"]))
	if query == "" {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: "query is required", IsError: true}
	}
	providers := searchProviderOrder(t.Config)
	var failures []string
	for _, provider := range providers {
		result := runSearchProvider(ctx, t.Config, provider, query, call.ID)
		if !result.IsError {
			return result
		}
		failures = append(failures, provider+": "+result.Content)
	}
	return agentcore.ToolResult{
		ToolCallID: call.ID,
		Content:    "all search providers failed: " + strings.Join(failures, " | "),
		IsError:    true,
		Evidence:   map[string]any{"query": query, "providers": providers},
	}
}

func searchProviderOrder(cfg *config.Root) []string {
	if cfg == nil || len(cfg.Search.ProviderOrder) == 0 {
		return []string{"tavily", "searxng", "duckduckgo"}
	}
	var out []string
	for _, provider := range cfg.Search.ProviderOrder {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider == "" || provider == "cache" {
			continue
		}
		out = append(out, provider)
	}
	if len(out) == 0 {
		return []string{"tavily", "searxng", "duckduckgo"}
	}
	return out
}

func runSearchProvider(ctx context.Context, cfg *config.Root, provider, query, callID string) agentcore.ToolResult {
	switch provider {
	case "tavily":
		return tavilySearch(ctx, cfg, query, callID)
	case "searxng", "searchxng":
		return searxngSearch(ctx, cfg, query, callID)
	case "duckduckgo":
		return duckDuckGoHTMLSearch(ctx, cfg, query, callID)
	default:
		return agentcore.ToolResult{ToolCallID: callID, Content: "unknown provider", IsError: true}
	}
}

func tavilySearch(ctx context.Context, cfg *config.Root, query, callID string) agentcore.ToolResult {
	provider := config.SearchProviderConfig{}
	if cfg != nil {
		provider = cfg.Search.Providers.Tavily
	}
	if !provider.Enabled {
		return agentcore.ToolResult{ToolCallID: callID, Content: "tavily disabled", IsError: true}
	}
	key := provider.ResolvedAPIKey()
	if key == "" {
		return agentcore.ToolResult{ToolCallID: callID, Content: "tavily api key is empty", IsError: true}
	}
	baseURL := strings.TrimSpace(provider.BaseURL)
	if baseURL == "" {
		baseURL = "https://api.tavily.com/search"
	}
	maxResults := provider.MaxResults
	if maxResults <= 0 {
		maxResults = 5
	}
	body := map[string]any{
		"api_key":     key,
		"query":       query,
		"max_results": maxResults,
	}
	if provider.SearchDepth != "" {
		body["search_depth"] = provider.SearchDepth
	}
	if provider.Topic != "" {
		body["topic"] = provider.Topic
	}
	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader(payload))
	if err != nil {
		return agentcore.ToolResult{ToolCallID: callID, Content: err.Error(), IsError: true}
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("user-agent", "mateway/0.1")
	resp, err := searchHTTPClient(provider.TimeoutSeconds).Do(req)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: callID, Content: err.Error(), IsError: true}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if resp.StatusCode >= 400 {
		return agentcore.ToolResult{ToolCallID: callID, Content: string(data), IsError: true, Evidence: map[string]any{"query": query, "status": resp.StatusCode, "provider": "tavily"}}
	}
	return agentcore.ToolResult{
		ToolCallID: callID,
		Content:    renderSearchResults(query, "tavily", tavilyResults(data), 8),
		Evidence:   map[string]any{"query": query, "status": resp.StatusCode, "provider": "tavily", "result_count": len(tavilyResults(data))},
	}
}

func searxngSearch(ctx context.Context, cfg *config.Root, query, callID string) agentcore.ToolResult {
	provider := config.SearchProviderConfig{}
	if cfg != nil {
		provider = cfg.Search.Providers.SearXNG
	}
	if !provider.Enabled {
		return agentcore.ToolResult{ToolCallID: callID, Content: "searxng disabled", IsError: true}
	}
	baseURL := strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8088"
	}
	endpoint := baseURL + "/search?q=" + url.QueryEscape(query) + "&format=json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: callID, Content: err.Error(), IsError: true}
	}
	req.Header.Set("user-agent", "mateway/0.1")
	resp, err := searchHTTPClient(provider.TimeoutSeconds).Do(req)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: callID, Content: err.Error(), IsError: true}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if resp.StatusCode >= 400 {
		return agentcore.ToolResult{ToolCallID: callID, Content: string(data), IsError: true, Evidence: map[string]any{"query": query, "status": resp.StatusCode, "provider": "searxng"}}
	}
	results := searxngResults(data)
	return agentcore.ToolResult{ToolCallID: callID, Content: renderSearchResults(query, "searxng", results, 8), Evidence: map[string]any{"query": query, "status": resp.StatusCode, "provider": "searxng", "result_count": len(results)}}
}

func duckDuckGoHTMLSearch(ctx context.Context, cfg *config.Root, query, callID string) agentcore.ToolResult {
	provider := config.SearchProviderConfig{Enabled: true, TimeoutSeconds: 4}
	if cfg != nil {
		provider = cfg.Search.Providers.DuckDuckGo
	}
	if !provider.Enabled {
		return agentcore.ToolResult{ToolCallID: callID, Content: "duckduckgo disabled", IsError: true}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://duckduckgo.com/html/?q="+url.QueryEscape(query), nil)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: callID, Content: err.Error(), IsError: true}
	}
	req.Header.Set("user-agent", "mateway/0.1")
	resp, err := searchHTTPClient(provider.TimeoutSeconds).Do(req)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: callID, Content: err.Error(), IsError: true}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	results := duckDuckGoResults(data)
	return agentcore.ToolResult{
		ToolCallID: callID,
		Content:    renderSearchResults(query, "duckduckgo_html", results, 8),
		IsError:    resp.StatusCode >= 400,
		Evidence:   map[string]any{"query": query, "status": resp.StatusCode, "provider": "duckduckgo_html", "result_count": len(results)},
	}
}

func searchHTTPClient(timeoutSeconds int) *http.Client {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 8
	}
	return &http.Client{Timeout: time.Duration(timeoutSeconds) * time.Second}
}

type searchResultItem struct {
	Title      string
	URL        string
	Summary    string
	DateHint   string
	SourceType string
	Provider   string
}

func tavilyResults(data []byte) []searchResultItem {
	var parsed struct {
		Answer  string `json:"answer"`
		Results []struct {
			Title   string  `json:"title"`
			URL     string  `json:"url"`
			Content string  `json:"content"`
			Score   float64 `json:"score"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil
	}
	var out []searchResultItem
	for i, item := range parsed.Results {
		if i >= 12 {
			break
		}
		out = append(out, normalizeSearchItem("tavily", item.Title, item.URL, item.Content))
	}
	return out
}

func searxngResults(data []byte) []searchResultItem {
	var parsed struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil
	}
	var out []searchResultItem
	for i, item := range parsed.Results {
		if i >= 12 {
			break
		}
		out = append(out, normalizeSearchItem("searxng", item.Title, item.URL, item.Content))
	}
	return out
}

func duckDuckGoResults(data []byte) []searchResultItem {
	text := string(data)
	re := regexp.MustCompile(`(?is)<a[^>]+class="result__a"[^>]+href="([^"]+)"[^>]*>(.*?)</a>.*?<a[^>]+class="result__snippet"[^>]*>(.*?)</a>`)
	matches := re.FindAllStringSubmatch(text, 12)
	var out []searchResultItem
	for _, match := range matches {
		if len(match) < 4 {
			continue
		}
		rawURL := html.UnescapeString(match[1])
		if parsed, err := url.Parse(rawURL); err == nil {
			if uddg := parsed.Query().Get("uddg"); uddg != "" {
				rawURL = uddg
			}
		}
		out = append(out, normalizeSearchItem("duckduckgo_html", stripHTML(match[2]), rawURL, stripHTML(match[3])))
	}
	return out
}

func renderSearchResults(query, provider string, results []searchResultItem, limit int) string {
	if limit <= 0 {
		limit = 8
	}
	var b strings.Builder
	b.WriteString("Search query: ")
	b.WriteString(query)
	b.WriteString("\nProvider: ")
	b.WriteString(provider)
	b.WriteString("\nResults are compact summaries. Use web.fetch on a source URL when exact details or quotes are required.\n")
	for i, item := range results {
		if i >= limit {
			break
		}
		b.WriteString(fmt.Sprintf("\n%d. %s\n", i+1, item.Title))
		b.WriteString("URL: ")
		b.WriteString(item.URL)
		b.WriteString("\nSource: ")
		b.WriteString(item.SourceType)
		if item.DateHint != "" {
			b.WriteString("\nDate hint: ")
			b.WriteString(item.DateHint)
		}
		b.WriteString("\nSummary: ")
		b.WriteString(item.Summary)
		b.WriteString("\n")
	}
	if len(results) == 0 {
		b.WriteString("\nNo structured results parsed. Try a different query or provider.\n")
	}
	return strings.TrimSpace(b.String())
}

func normalizeSearchItem(provider, title, rawURL, summary string) searchResultItem {
	title = compactWhitespace(html.UnescapeString(stripHTML(title)))
	summary = compactWhitespace(html.UnescapeString(stripHTML(summary)))
	rawURL = strings.TrimSpace(html.UnescapeString(rawURL))
	if title == "" {
		title = "(untitled)"
	}
	if len([]rune(summary)) > 520 {
		rs := []rune(summary)
		summary = string(rs[:520]) + "..."
	}
	return searchResultItem{
		Title:      title,
		URL:        rawURL,
		Summary:    summary,
		DateHint:   firstDateHint(title + " " + summary),
		SourceType: classifySource(rawURL),
		Provider:   provider,
	}
}

func classifySource(rawURL string) string {
	host := ""
	if parsed, err := url.Parse(rawURL); err == nil {
		host = strings.ToLower(parsed.Hostname())
	}
	switch {
	case host == "":
		return "unknown"
	case strings.Contains(host, "github.com") || strings.Contains(host, "openai.com") || strings.Contains(host, "microsoft.com") || strings.Contains(host, "docs.") || strings.Contains(host, "developer.") || strings.Contains(host, "developers."):
		return "official_or_primary_candidate"
	case strings.Contains(host, "youtube.com") || strings.Contains(host, "reddit.com") || strings.Contains(host, "medium.com") || strings.Contains(host, "blog."):
		return "community_or_secondary"
	default:
		return "third_party"
	}
}

func firstDateHint(text string) string {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`\b20[0-9]{2}[-/年.][0-9]{1,2}[-/月.][0-9]{1,2}`),
		regexp.MustCompile(`\b(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)[a-z]*\s+[0-9]{1,2},?\s+20[0-9]{2}`),
		regexp.MustCompile(`20[0-9]{2}年[0-9]{1,2}月[0-9]{1,2}日`),
	}
	for _, pattern := range patterns {
		if match := pattern.FindString(text); match != "" {
			return match
		}
	}
	return ""
}

func stripHTML(text string) string {
	re := regexp.MustCompile(`(?s)<[^>]+>`)
	return re.ReplaceAllString(text, " ")
}

func compactWhitespace(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func summarizeToolText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit] + fmt.Sprintf("... (%d chars)", len(text))
}

func toolArgString(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	value, ok := args[key]
	if !ok || value == nil {
		return ""
	}
	text := strings.TrimSpace(fmt.Sprint(value))
	if text == "<nil>" {
		return ""
	}
	return text
}

func stringSliceArg(value any) []string {
	switch v := value.(type) {
	case []string:
		return append([]string(nil), v...)
	case []any:
		var out []string
		for _, item := range v {
			if text := strings.TrimSpace(fmt.Sprint(item)); text != "" && text != "<nil>" {
				out = append(out, text)
			}
		}
		return out
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return strings.Fields(v)
	default:
		return nil
	}
}

func intArg(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		return n
	default:
		return 0
	}
}

func (EchoTool) Risk() agentcore.Risk { return agentcore.RiskSafeRead }
func (EchoTool) Run(_ context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	return agentcore.ToolResult{ToolCallID: call.ID, Content: fmt.Sprint(call.Args["text"])}
}

func (FileReadTool) Name() string        { return "file.read" }
func (FileReadTool) Description() string { return "read a local text file" }
func (FileReadTool) Schema() agentcore.Schema {
	return agentcore.Schema{Required: []string{"path"}}
}
func (FileReadTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{
		WhenToUse:            "Use when the task requires reading a known local text file.",
		WhenNotToUse:         "Do not use when the file path is unknown; inspect the project first with project.index.",
		OutputContract:       "Return the file text content and path/byte evidence.",
		Evidence:             "Return read path and byte count.",
		Acceptance:           "Accepted when the file exists, is readable, and evidence includes path and bytes.",
		SoftFailureSignals:   []string{"no such file or directory", "permission denied", "is a directory", "outside allowed roots"},
		ParallelMode:         "read_only_ok",
		ReusePolicy:          "stable_read",
		ConfirmationBoundary: "safe read; no confirmation.",
	}
}
func (FileReadTool) Risk() agentcore.Risk { return agentcore.RiskSafeRead }
func (t FileReadTool) Run(_ context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	path, err := ResolveAllowedPath(fmt.Sprint(call.Args["path"]), t.Config)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true, Evidence: map[string]any{"path": fmt.Sprint(call.Args["path"])}}
	}
	info, err := os.Stat(path)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true}
	}
	if info.IsDir() {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: "path is a directory", IsError: true, Evidence: map[string]any{"path": path}}
	}
	if info.Size() > 512*1024 {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: fmt.Sprintf("file too large: %d bytes", info.Size()), IsError: true, Evidence: map[string]any{"path": path, "bytes": info.Size()}}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true}
	}
	if isLikelyBinary(data) {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: "file appears to be binary", IsError: true, Evidence: map[string]any{"path": path, "bytes": len(data)}}
	}
	return agentcore.ToolResult{
		ToolCallID: call.ID,
		Content:    string(data),
		Evidence:   map[string]any{"path": path, "bytes": len(data)},
	}
}

func isLikelyBinary(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	sample := data
	if len(sample) > 4096 {
		sample = sample[:4096]
	}
	for _, b := range sample {
		if b == 0 {
			return true
		}
	}
	return !utf8.Valid(sample)
}
