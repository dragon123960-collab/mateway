package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/doctor"
	"github.com/dongping/mateway/internal/gateway"
	agentharness "github.com/dongping/mateway/internal/harness"
	"github.com/dongping/mateway/internal/memory"
	"github.com/dongping/mateway/internal/provisioning"
	hostruntime "github.com/dongping/mateway/internal/runtime"
	"github.com/dongping/mateway/internal/scheduler"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/skills"
	"github.com/dongping/mateway/internal/tools"
	"github.com/dongping/mateway/internal/version"
	"github.com/dongping/mateway/internal/workspace"
)

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printHelp(stdout)
		return nil
	}
	switch strings.ToLower(args[0]) {
	case "init":
		cfg := config.Default()
		if err := workspace.Init(cfg); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "initialized %s\n", cfg.App.Home)
		return nil
	case "doctor":
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		_, _ = io.WriteString(stdout, doctor.Format(doctor.Run(ctx, cfg)))
		return nil
	case "logs":
		return runLogsCommand(ctx, args[1:], stdout)
	case "gateway":
		return runGatewayCommand(ctx, args[1:], stdout)
	case "workspace":
		return runWorkspaceCommand(ctx, args[1:], stdout)
	case "skill":
		return runSkillCommand(ctx, args[1:], stdout)
	case "agent":
		return runAgentCommand(ctx, args[1:], stdout)
	case "model":
		return runModelCommand(ctx, args[1:], stdout)
	case "channel":
		return runChannelCommand(ctx, args[1:], stdout)
	case "schedule":
		return runScheduleCommand(ctx, args[1:], stdout)
	case "memory":
		return runMemoryCommand(ctx, args[1:], stdout)
	case "run":
		return runRunCommand(ctx, args[1:], stdout)
	case "tui":
		return runTUICommand(ctx, args[1:], stdout)
	case "version":
		_, _ = fmt.Fprintf(stdout, "mateway %s (%s) built %s\n", version.Version, version.GitCommit, version.BuildTime)
		return nil
	case "help", "--help", "-h":
		if len(args) > 1 {
			if printCommandHelp(stdout, args[1]) {
				return nil
			}
			_, _ = fmt.Fprintf(stdout, "unknown help topic: %s\n\n", args[1])
		}
		printHelp(stdout)
		return nil
	default:
		_, _ = fmt.Fprintf(stderr, "unknown command: %s\n\n", args[0])
		printHelp(stderr)
		return errors.New("unknown command")
	}
}

func runWorkspaceCommand(_ context.Context, args []string, stdout io.Writer) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	provisioner := provisioning.Provisioner{Config: cfg}
	if len(args) == 0 {
		return errors.New("workspace command requires a subcommand")
	}
	switch strings.ToLower(args[0]) {
	case "create":
		if len(args) < 2 {
			return errors.New("usage: mateway workspace create <name>")
		}
		path, err := provisioner.CreateWorkspace(args[1])
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "created workspace %s\n", path)
		return nil
	case "list":
		items, err := provisioner.ListWorkspaces()
		if err != nil {
			return err
		}
		for _, item := range items {
			_, _ = fmt.Fprintln(stdout, item)
		}
		return nil
	default:
		return fmt.Errorf("unknown workspace subcommand: %s", args[0])
	}
}

func runAgentCommand(_ context.Context, args []string, stdout io.Writer) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	provisioner := provisioning.Provisioner{Config: cfg}
	if len(args) == 0 {
		return errors.New("agent command requires a subcommand")
	}
	switch strings.ToLower(args[0]) {
	case "create":
		if len(args) < 3 {
			return errors.New("usage: mateway agent create <workspace-path> <name>")
		}
		path, err := provisioner.CreateAgent(args[1], args[2], strings.Join(args[3:], " "))
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "created agent %s\n", path)
		return nil
	case "list":
		if len(args) < 2 {
			return errors.New("usage: mateway agent list <workspace-path>")
		}
		items, err := provisioner.ListAgents(args[1])
		if err != nil {
			return err
		}
		for _, item := range items {
			_, _ = fmt.Fprintln(stdout, item)
		}
		return nil
	default:
		return fmt.Errorf("unknown agent subcommand: %s", args[0])
	}
}

func runModelCommand(_ context.Context, args []string, stdout io.Writer) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return errors.New("usage: mateway model current | list | set-default <name>")
	}
	switch strings.ToLower(args[0]) {
	case "current":
		model := cfg.DefaultModel()
		if model == nil {
			_, _ = fmt.Fprintf(stdout, "default=%s\n", cfg.Models.Default)
			return nil
		}
		_, _ = fmt.Fprintf(stdout, "default=%s model=%s provider=%s\n", model.Name, model.Model, model.Provider)
		return nil
	case "list":
		for _, item := range cfg.ModelList {
			if !item.Enabled {
				continue
			}
			marker := " "
			if item.Name == cfg.Models.Default {
				marker = "*"
			}
			_, _ = fmt.Fprintf(stdout, "%s %s -> %s (%s)\n", marker, item.Name, item.Model, item.Provider)
		}
		return nil
	case "set-default":
		if len(args) < 2 {
			return errors.New("usage: mateway model set-default <name>")
		}
		cfg.Models.Default = strings.TrimSpace(args[1])
		if err := config.Save(config.ConfigPath(), cfg); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "default model set to %s\n", cfg.Models.Default)
		return nil
	default:
		return fmt.Errorf("unknown model subcommand: %s", args[0])
	}
}

func runChannelCommand(_ context.Context, args []string, stdout io.Writer) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	provisioner := provisioning.Provisioner{Config: cfg}
	if len(args) == 0 {
		return errors.New("usage: mateway channel create <name> <kind> | list | enable <name> | disable <name>")
	}
	switch strings.ToLower(args[0]) {
	case "create":
		if len(args) < 3 {
			return errors.New("usage: mateway channel create <name> <kind>")
		}
		path, err := provisioner.CreateChannel(args[1], args[2])
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "created channel config %s\n", path)
		return nil
	case "list":
		if cfg.Channels.Feishu.Enabled {
			_, _ = fmt.Fprintln(stdout, "feishu enabled")
		} else {
			_, _ = fmt.Fprintln(stdout, "feishu disabled")
		}
		return nil
	case "enable", "disable":
		if len(args) < 2 {
			return fmt.Errorf("usage: mateway channel %s <name>", args[0])
		}
		name := strings.ToLower(strings.TrimSpace(args[1]))
		if name != "feishu" {
			return fmt.Errorf("unsupported channel %q", args[1])
		}
		enabled := strings.ToLower(args[0]) == "enable"
		cfg.Channels.Feishu.Enabled = enabled
		if err := config.Save(config.ConfigPath(), cfg); err != nil {
			return err
		}
		state := "disabled"
		if enabled {
			state = "enabled"
		}
		_, _ = fmt.Fprintf(stdout, "channel %s %s\n", name, state)
		return nil
	default:
		return fmt.Errorf("unknown channel subcommand: %s", args[0])
	}
}

func runScheduleCommand(ctx context.Context, args []string, stdout io.Writer) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	store := scheduler.Store{Workspace: cfg.App.Workspace}
	if len(args) == 0 {
		return errors.New("schedule command requires a subcommand")
	}
	switch strings.ToLower(args[0]) {
	case "create":
		if len(args) < 2 {
			return errors.New("usage: mateway schedule create <name> <interval-minutes> <prompt> | mateway schedule create interval <name> <minutes> <prompt> | mateway schedule create cron <name> <expr> <tz> <prompt>")
		}
		var job scheduler.Job
		switch strings.ToLower(args[1]) {
		case "interval":
			if len(args) < 5 {
				return errors.New("usage: mateway schedule create interval <name> <minutes> <prompt>")
			}
			interval, convErr := strconv.Atoi(args[3])
			if convErr != nil || interval <= 0 {
				return errors.New("minutes must be a positive integer")
			}
			job, err = scheduler.NewIntervalJob(args[2], "schedule:"+args[2], strings.Join(args[4:], " "), interval)
		case "cron":
			if len(args) < 6 {
				return errors.New("usage: mateway schedule create cron <name> <expr> <tz> <prompt>")
			}
			job, err = scheduler.NewCronJob(args[2], "schedule:"+args[2], strings.Join(args[5:], " "), args[3], args[4])
		default:
			if len(args) < 4 {
				return errors.New("usage: mateway schedule create <name> <interval-minutes> <prompt>")
			}
			interval, convErr := strconv.Atoi(args[2])
			if convErr != nil || interval <= 0 {
				return errors.New("interval-minutes must be a positive integer")
			}
			job, err = scheduler.NewIntervalJob(args[1], "schedule:"+args[1], strings.Join(args[3:], " "), interval)
		}
		if err != nil {
			return err
		}
		if err := store.Save(job); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "created schedule %s\n", job.Name)
		return nil
	case "list":
		items, err := store.List()
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}
		for _, item := range items {
			_, _ = fmt.Fprintf(stdout, "%s enabled=%t schedule=%s next=%s status=%s\n",
				item.Name, item.Enabled, item.Description(), item.State.NextRunAt.Format(time.RFC3339), item.LastStatus())
		}
		return nil
	case "get":
		if len(args) < 2 {
			return errors.New("usage: mateway schedule get <name>")
		}
		job, ok, err := store.Get(args[1])
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("schedule %q not found", args[1])
		}
		data, err := json.MarshalIndent(job, "", "  ")
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(stdout, string(data))
		return nil
	case "enable", "disable":
		if len(args) < 2 {
			return fmt.Errorf("usage: mateway schedule %s <name>", args[0])
		}
		job, err := store.Enable(args[1], strings.ToLower(args[0]) == "enable")
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "schedule %s %s\n", job.Name, map[bool]string{true: "enabled", false: "disabled"}[job.Enabled])
		return nil
	case "remove", "rm", "delete":
		if len(args) < 2 {
			return fmt.Errorf("usage: mateway schedule %s <name>", args[0])
		}
		if err := store.Remove(args[1]); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "schedule %s removed\n", args[1])
		return nil
	case "run":
		if len(args) < 2 {
			return errors.New("usage: mateway schedule run <name>")
		}
		_, _, _, _, registry, runner := buildRuntime(cfg)
		svc := scheduler.Service{
			Store:  store,
			Runner: harnessScheduleRunner{runner: runner},
		}
		job, err := svc.RunNow(ctx, args[1])
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "ran schedule %s status=%s next=%s\n", job.Name, job.LastStatus(), job.State.NextRunAt.Format(time.RFC3339))
		_ = registry
		return nil
	case "runs":
		if len(args) < 2 {
			return errors.New("usage: mateway schedule runs <name>")
		}
		lines, err := store.ReadRuns(args[1], 20)
		if err != nil {
			return err
		}
		for _, line := range lines {
			_, _ = fmt.Fprintln(stdout, line)
		}
		return nil
	default:
		return fmt.Errorf("unknown schedule subcommand: %s", args[0])
	}
}

func runMemoryCommand(ctx context.Context, args []string, stdout io.Writer) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	store := memory.Store{Workspace: cfg.App.Workspace}
	if len(args) == 0 {
		return errors.New("usage: mateway memory rebuild --force --drop-all")
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "rebuild":
		hasForce := false
		hasDropAll := false
		for _, arg := range args[1:] {
			switch strings.ToLower(strings.TrimSpace(arg)) {
			case "--force":
				hasForce = true
			case "--drop-all":
				hasDropAll = true
			}
		}
		if !hasForce || !hasDropAll {
			return errors.New("usage: mateway memory rebuild --force --drop-all")
		}
		if err := store.Rebuild(ctx, true); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "rebuilt memory under %s\n", filepath.Join(cfg.App.Workspace, "memory"))
		return nil
	default:
		return fmt.Errorf("unknown memory subcommand: %s", args[0])
	}
}

func runRunCommand(ctx context.Context, args []string, stdout io.Writer) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	sessions := session.NewStore(cfg.App.Workspace)
	registry := tools.NewRegistry()
	runner := agentharness.New(cfg.App.Workspace, sessions, registry, cfg.Sessions.HistoryLimit)
	if len(args) == 0 {
		return errors.New("usage: mateway run list [session-key] | mateway run get <run-id>")
	}
	switch strings.ToLower(args[0]) {
	case "list":
		sessionKey := ""
		if len(args) > 1 {
			sessionKey = args[1]
		}
		runs, err := runner.ListRuns(ctx, sessionKey, 20)
		if err != nil {
			return err
		}
		for _, run := range runs {
			_, _ = fmt.Fprintf(stdout, "%s %s %s %s\n", run.ID, run.Status, appFirstNonEmpty(run.Mode, "-"), appFirstNonEmpty(run.ToolName, "-"))
		}
		return nil
	case "get":
		if len(args) < 2 {
			return errors.New("usage: mateway run get <run-id>")
		}
		run, ok := runner.GetRun(ctx, args[1])
		if !ok {
			return fmt.Errorf("run %q not found", args[1])
		}
		data, err := json.MarshalIndent(run, "", "  ")
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(stdout, string(data))
		return nil
	default:
		return fmt.Errorf("unknown run subcommand: %s", args[0])
	}
}

func loadConfig() (config.Config, error) {
	cfg, err := config.Load(config.ConfigPath())
	if err != nil {
		return config.Config{}, fmt.Errorf("load config: %w; run `mateway init` first", err)
	}
	return cfg, nil
}

func buildRuntime(cfg config.Config) (*skills.Catalog, *skills.Watcher, hostruntime.Invoker, *session.Store, *tools.Registry, *agentharness.Harness) {
	catalog := skills.NewCatalog(cfg.Skills.Roots)
	_ = catalog.Refresh()
	var watcher *skills.Watcher
	invoker := hostruntime.Invoker{Workspace: cfg.App.Workspace}
	sessions := session.NewStore(cfg.App.Workspace)
	provisioner := provisioning.Provisioner{Config: cfg}
	registry := tools.NewRegistry()
	runner := agentharness.New(cfg.App.Workspace, sessions, registry, cfg.Sessions.HistoryLimit)
	runner.SkillCatalog = catalog
	registry.Register(tools.BuiltinProvider{
		Workspace:             cfg.App.Workspace,
		Sessions:              sessions,
		Memory:                memory.Store{Workspace: cfg.App.Workspace},
		Provisioner:           provisioner,
		EnforceWorkspacePaths: cfg.Security.EnforceWorkspacePaths,
		SkillCatalog:          catalog,
		SkillRuns:             runner,
	})
	registry.Register(tools.WebSearchProvider{
		Enabled:       cfg.Integrations.WebSearch.Enabled,
		Provider:      cfg.Integrations.WebSearch.Provider,
		DuckDuckGoURL: cfg.Integrations.WebSearch.DuckDuckGo.BaseURL,
		TavilyURL:     cfg.Integrations.WebSearch.Tavily.BaseURL,
		TavilyAPIKey:  cfg.Integrations.WebSearch.Tavily.APIKey,
	})
	registry.Register(tools.BrowserProvider{
		Enabled: cfg.Integrations.Browser.Enabled,
	})
	for _, cliProvider := range cfg.CLIProviders {
		enabled := cliProvider.Enabled == nil || *cliProvider.Enabled
		registry.Register(tools.ExternalCLIProvider{
			Name:            cliProvider.Name,
			BinaryPath:      cliProvider.Binary,
			Enabled:         enabled,
			Description:     cliProvider.Description,
			ListArgs:        cliProvider.ListArgs,
			AllowedCommands: cliProvider.AllowedCommands,
			BlockedCommands: cliProvider.BlockedCommands,
			Env:             cliProvider.Env,
			RiskLevel:       cliProvider.RiskLevel,
		})
	}
	registry.Register(tools.SkillsProvider{Catalog: catalog, Invoker: invoker})
	runner.UseEinoRuntime(cfg)
	runner.ApprovalPolicy = agentharness.ApprovalPolicy{
		RequireRiskyTools:     cfg.Security.RequireApprovalForRiskyTools,
		RequireScheduleChange: true,
	}
	return catalog, watcher, invoker, sessions, registry, runner
}

func newGatewayService(ctx context.Context, cfg config.Config, catalog *skills.Catalog, watcher *skills.Watcher, invoker hostruntime.Invoker, runner *agentharness.Harness, registry *tools.Registry) gateway.Service {
	if watcher == nil && cfg.Skills.Watch {
		watcher = skills.NewWatcher(catalog, cfg.Skills.Roots)
		_ = watcher.Start(ctx)
	}
	scheduleSvc := &scheduler.Service{
		Store:  scheduler.Store{Workspace: cfg.App.Workspace},
		Runner: harnessScheduleRunner{runner: runner},
	}
	_ = scheduleSvc.Start(ctx)
	return gateway.Service{
		Config:  cfg,
		Catalog: catalog,
		Watcher: watcher,
		Invoker: invoker,
		Runner:  runner,
		Tools:   registry,
	}
}

type harnessScheduleRunner struct {
	runner *agentharness.Harness
}

func (h harnessScheduleRunner) RunScheduledJob(ctx context.Context, job scheduler.Job) (scheduler.RunResult, error) {
	if h.runner == nil {
		return scheduler.RunResult{}, nil
	}
	args := appMergeArguments(job.Arguments, map[string]any{
		"task_kind":       "schedule",
		"task_type":       "schedule_task",
		"schedule_name":   job.Name,
		"schedule_job_id": appFirstNonEmpty(job.ID, job.Name),
		"triggered_at":    time.Now().Format(time.RFC3339),
	})
	run, err := h.runner.Start(ctx, agentharness.Request{
		SessionKey: job.SessionKey,
		AgentName:  job.AgentName,
		Channel:    "schedule",
		TaskType:   "schedule_task",
		Mode:       job.Mode,
		UserText:   job.Prompt,
		ToolName:   job.ToolName,
		Arguments:  args,
	}, nil)
	if err != nil {
		return scheduler.RunResult{
			RunID:  run.ID,
			TaskID: run.TaskID,
			Status: appFirstNonEmpty(run.Status, "error"),
			Error:  appFirstNonEmpty(run.Error, err.Error()),
		}, err
	}
	return scheduler.RunResult{
		RunID:  run.ID,
		TaskID: run.TaskID,
		Status: appFirstNonEmpty(run.Status, "completed"),
		Error:  run.Error,
	}, nil
}

func appFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func appMergeArguments(base map[string]any, extra map[string]any) map[string]any {
	if len(base) == 0 && len(extra) == 0 {
		return nil
	}
	out := make(map[string]any, len(base)+len(extra))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range extra {
		out[key] = value
	}
	return out
}
