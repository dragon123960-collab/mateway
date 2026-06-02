package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/dongping/mateway/internal/agentprofile"
	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/channel/weixin"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/gateway"
	"github.com/dongping/mateway/internal/memory"
	"github.com/dongping/mateway/internal/model"
	"github.com/dongping/mateway/internal/runtime"
	"github.com/dongping/mateway/internal/schedule"
	"github.com/dongping/mateway/internal/script"
	"github.com/dongping/mateway/internal/secret"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/skill"
	"gopkg.in/yaml.v3"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printHelp()
		return nil
	}
	switch args[0] {
	case "init":
		fs := flag.NewFlagSet("mateway init", flag.ContinueOnError)
		homeFlag := fs.String("home", "", "override MATEWAY_HOME for initialization")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		home := strings.TrimSpace(*homeFlag)
		if home == "" {
			home = config.DefaultHome()
		}
		if err := config.EnsureDefaultConfigFiles(home); err != nil {
			return err
		}
		fmt.Println("initialized", home)
		return nil
	case "ask":
		if len(args) < 2 {
			return fmt.Errorf("usage: mateway ask <message>")
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		rt := runtime.New(cfg)
		msg := channel.InboundMessage{
			ID:       "cli",
			Channel:  "cli",
			ThreadID: "cli",
			UserID:   "local",
			Text:     strings.Join(args[1:], " "),
		}
		msg.SessionKey = gateway.SessionKey(msg)
		resp, err := rt.Handle(context.Background(), msg)
		if err != nil {
			return err
		}
		fmt.Println(resp.Reply.Text)
		return nil
	case "test":
		return runTest(args[1:])
	case "doctor":
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		fmt.Println("home:", cfg.App.Home)
		fmt.Println("workspace:", cfg.App.Workspace)
		fmt.Println("model:", cfg.Model.Default)
		fmt.Println("feishu_enabled:", cfg.Channels.Feishu.Enabled)
		fmt.Println("weixin_enabled:", cfg.Channels.Weixin.Enabled)
		return nil
	case "home":
		return runHome(args[1:])
	case "workspace":
		return runWorkspace(args[1:])
	case "trace":
		if len(args) < 2 {
			return fmt.Errorf("usage: mateway trace <trace-jsonl-path>")
		}
		summary, err := runtime.SummarizeTrace(args[1])
		if err != nil {
			return err
		}
		fmt.Println("trace:", summary.Path)
		fmt.Println("events:", summary.Events)
		fmt.Println("model_ms:", summary.ModelDurationMS)
		fmt.Println("tool_ms:", summary.ToolDurationMS)
		fmt.Println("runtime_ms:", summary.RuntimeDurationMS)
		fmt.Println("reply_ms:", summary.ReplyDurationMS)
		fmt.Println("total_ms:", summary.TotalDurationMS)
		fmt.Println("model_requests:", summary.ModelRequests)
		fmt.Println("input_tokens:", summary.InputTokens)
		fmt.Println("output_tokens:", summary.OutputTokens)
		fmt.Println("total_tokens:", summary.TotalTokens)
		if len(summary.ToolCalls) > 0 {
			fmt.Println("tools:", strings.Join(summary.ToolCalls, ", "))
		}
		return nil
	case "session":
		return runSession(args[1:])
	case "memory":
		return runMemory(args[1:])
	case "agent-profile":
		return runAgentProfile(args[1:])
	case "agent":
		return runAgent(args[1:])
	case "script":
		return runScript(args[1:])
	case "sandbox":
		return runSandbox(args[1:])
	case "schedule":
		return runSchedule(args[1:])
	case "skill":
		return runSkill(args[1:])
	case "secret":
		return runSecret(args[1:])
	case "channel":
		return runChannel(args[1:])
	case "gateway":
		if len(args) < 2 {
			return fmt.Errorf("usage: mateway gateway <serve|start|restart|stop|status>")
		}
		switch args[1] {
		case "serve":
			return serveGateway()
		case "start":
			return gateway.NewServiceManager().Start(context.Background())
		case "restart":
			return gateway.NewServiceManager().Restart(context.Background())
		case "stop":
			return gateway.NewServiceManager().Stop(context.Background())
		case "status":
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			text, err := gateway.NewServiceManager().Status(context.Background(), cfg.App.Home)
			if strings.TrimSpace(text) != "" {
				fmt.Print(text)
			}
			return err
		default:
			return fmt.Errorf("usage: mateway gateway <serve|start|restart|stop|status>")
		}
	case "weixin":
		return runWeixin(args[1:])
	default:
		printHelp()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

type channelConfigInfo struct {
	ID      string
	Enabled bool
	Path    string
}

func runChannel(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mateway channel <list>")
	}
	switch args[0] {
	case "list":
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		channels, err := listChannelConfigs(filepath.Join(cfg.App.Home, "config", "channels"))
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tENABLED\tCONFIG")
		for _, ch := range channels {
			fmt.Fprintf(tw, "%s\t%t\t%s\n", ch.ID, ch.Enabled, ch.Path)
		}
		return tw.Flush()
	default:
		return fmt.Errorf("usage: mateway channel <list>")
	}
}

func listChannelConfigs(dir string) ([]channelConfigInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read channel config dir: %w", err)
	}
	var channels []channelConfigInfo
	for _, entry := range entries {
		name := entry.Name()
		if shouldSkipRuntimeConfigFile(entry, name) {
			continue
		}
		id := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".yaml")
		path := filepath.Join(dir, name)
		enabled, err := readChannelEnabled(path, id)
		if err != nil {
			return nil, err
		}
		channels = append(channels, channelConfigInfo{ID: id, Enabled: enabled, Path: path})
	}
	sort.Slice(channels, func(i, j int) bool {
		return channels[i].ID < channels[j].ID
	})
	return channels, nil
}

func shouldSkipRuntimeConfigFile(entry os.DirEntry, name string) bool {
	if entry.IsDir() {
		return true
	}
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" || strings.HasPrefix(lower, "_") || !strings.HasSuffix(lower, ".yaml") {
		return true
	}
	base := strings.TrimSuffix(lower, ".yaml")
	return strings.HasSuffix(base, ".sample") || strings.HasSuffix(base, ".example")
}

func readChannelEnabled(path, id string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	var root map[string]map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}
	values := root[id]
	if values == nil {
		return false, nil
	}
	enabled, _ := values["enabled"].(bool)
	return enabled, nil
}

func runWeixin(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mateway weixin <login|enable>")
	}
	switch args[0] {
	case "login":
		fs := flag.NewFlagSet("mateway weixin login", flag.ContinueOnError)
		timeout := fs.Duration("timeout", 2*time.Minute, "QR login timeout")
		botType := fs.String("bot-type", "", "optional iLink bot type")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		account, qrURL, err := weixin.Login(context.Background(), cfg.Channels.Weixin, cfg.App.Home, *botType, *timeout, os.Stdout)
		if err != nil {
			return err
		}
		fmt.Println("weixin_account_id:", account.AccountID)
		fmt.Println("weixin_base_url:", account.BaseURL)
		if strings.TrimSpace(qrURL) != "" {
			fmt.Println("qrcode_url:", qrURL)
		}
		fmt.Println("saved_to:", filepath.Join(cfg.App.Home, "run", "weixin", "accounts", account.AccountID+".json"))
		return nil
	case "enable":
		fs := flag.NewFlagSet("mateway weixin enable", flag.ContinueOnError)
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		accountID := ""
		if fs.NArg() > 0 {
			accountID = fs.Arg(0)
		}
		account, err := weixin.EnableSavedAccount(cfg.Channels.Weixin, cfg.App.Home, accountID)
		if err != nil {
			return err
		}
		fmt.Println("weixin_enabled:", true)
		fmt.Println("weixin_account_id:", account.AccountID)
		fmt.Println("weixin_base_url:", account.BaseURL)
		fmt.Println("config:", filepath.Join(cfg.App.Home, "config", "channels", "weixin.yaml"))
		return nil
	default:
		return fmt.Errorf("usage: mateway weixin <login|enable>")
	}
}

func runSecret(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mateway secret <set|get|list|delete>")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	store := secret.Store{Home: cfg.App.Home}
	switch args[0] {
	case "set":
		if len(args) < 2 || len(args) > 3 {
			return fmt.Errorf("usage: mateway secret set <id> [value]")
		}
		value := ""
		if len(args) == 3 {
			value = args[2]
		} else {
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				return err
			}
			value = strings.TrimRight(string(data), "\r\n")
		}
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("secret value is required; pass it as an argument or via stdin")
		}
		if err := store.Set(args[1], value); err != nil {
			return err
		}
		fmt.Println("secret:", strings.ToLower(strings.TrimSpace(args[1])))
		fmt.Println("stored: true")
		return nil
	case "get":
		if len(args) != 2 {
			return fmt.Errorf("usage: mateway secret get <id>")
		}
		entry, ok, err := store.Get(args[1])
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("secret %q not found", args[1])
		}
		fmt.Println(entry.Value)
		return nil
	case "list":
		if len(args) != 1 {
			return fmt.Errorf("usage: mateway secret list")
		}
		entries, err := store.List()
		if err != nil {
			return err
		}
		fmt.Println("secrets:", len(entries))
		for _, entry := range entries {
			fmt.Printf("- %s updated_at=%s\n", entry.ID, entry.UpdatedAt)
		}
		return nil
	case "delete":
		if len(args) != 2 {
			return fmt.Errorf("usage: mateway secret delete <id>")
		}
		deleted, err := store.Delete(args[1])
		if err != nil {
			return err
		}
		fmt.Println("secret:", strings.ToLower(strings.TrimSpace(args[1])))
		fmt.Println("deleted:", deleted)
		return nil
	default:
		return fmt.Errorf("usage: mateway secret <set|get|list|delete>")
	}
}

func runSession(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mateway session <list|show|archive>")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	store := session.NewStore(cfg.App.Home)
	switch args[0] {
	case "list":
		keys, err := store.List()
		if err != nil {
			return err
		}
		for _, key := range keys {
			state, err := store.Load(key)
			if err != nil {
				continue
			}
			fmt.Printf("%s messages=%d tasks=%d requests=%d tokens=%d updated=%s\n", key, len(state.Messages), len(state.Tasks), state.Usage.Requests, state.Usage.TotalTokens, state.UpdatedAt.Format(time.RFC3339))
		}
		return nil
	case "show":
		if len(args) != 2 {
			return fmt.Errorf("usage: mateway session show <session_key>")
		}
		state, err := store.Load(args[1])
		if err != nil {
			return err
		}
		printSessionState(state, "")
		return nil
	case "archive":
		if len(args) < 3 {
			return fmt.Errorf("usage: mateway session archive <list|show> <session_key> [archive_id]")
		}
		switch args[1] {
		case "list":
			ids, err := store.ListArchives(args[2])
			if err != nil {
				return err
			}
			for _, id := range ids {
				fmt.Println(id)
			}
			return nil
		case "show":
			if len(args) != 4 {
				return fmt.Errorf("usage: mateway session archive show <session_key> <archive_id>")
			}
			state, path, err := store.LoadArchive(args[2], args[3])
			if err != nil {
				return err
			}
			printSessionState(state, path)
			return nil
		default:
			return fmt.Errorf("usage: mateway session archive <list|show> <session_key> [archive_id]")
		}
	default:
		return fmt.Errorf("usage: mateway session <list|show|archive>")
	}
}

func printSessionState(state session.State, path string) {
	if path != "" {
		fmt.Println("path:", path)
	}
	fmt.Println("session:", state.Key)
	fmt.Println("messages:", len(state.Messages))
	fmt.Println("tasks:", len(state.Tasks))
	fmt.Println("active_task:", state.ActiveTask)
	if state.Pending != nil {
		fmt.Println("pending:", state.Pending.Kind)
	}
	fmt.Printf("usage: requests=%d input_tokens=%d output_tokens=%d total_tokens=%d\n", state.Usage.Requests, state.Usage.InputTokens, state.Usage.OutputTokens, state.Usage.TotalTokens)
	if !state.UpdatedAt.IsZero() {
		fmt.Println("updated_at:", state.UpdatedAt.Format(time.RFC3339))
	}
	for _, task := range state.Tasks {
		fmt.Printf("- %s %s %s\n", task.ID, task.Status, task.Goal)
		if task.Summary != "" {
			fmt.Println("  summary:", task.Summary)
		}
		if task.TracePath != "" {
			fmt.Println("  trace:", task.TracePath)
		}
		for _, step := range task.Steps {
			fmt.Printf("  - %s %s %s\n", step.Tool, step.Status, step.Summary)
		}
	}
}

func runHome(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mateway home <report>")
	}
	switch args[0] {
	case "report":
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		report, err := buildHomeReport(cfg.App.Home)
		if err != nil {
			return err
		}
		fmt.Println("home:", report.Home)
		fmt.Println("expected:")
		for _, item := range report.Expected {
			fmt.Printf("- %s: %s\n", item.Name, item.Kind)
		}
		fmt.Println("generated:")
		for _, item := range report.Generated {
			fmt.Printf("- %s: %s\n", item.Name, item.Kind)
		}
		fmt.Println("local:")
		for _, item := range report.Local {
			fmt.Printf("- %s: %s\n", item.Name, item.Kind)
		}
		fmt.Println("unknown:")
		for _, item := range report.Unknown {
			fmt.Printf("- %s: %s\n", item.Name, item.Kind)
		}
		return nil
	default:
		return fmt.Errorf("usage: mateway home <report>")
	}
}

func runWorkspace(args []string) error {
	if len(args) != 1 || args[0] != "report" {
		return fmt.Errorf("usage: mateway workspace report")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	memReport, _ := memory.BuildReport(memory.ReportInput{Home: cfg.App.Home, MemoryRoot: memoryRoot(cfg)})
	learningReport, _ := memory.BuildLearningReport(memory.LearningReportInput{Home: cfg.App.Home, Workspace: cfg.App.Workspace})
	skills, _ := skill.List(cfg.App.Workspace)
	scripts, _ := script.List(cfg)
	schedules, _ := schedule.Store{Home: cfg.App.Home}.List()
	traceCount := countFiles(filepath.Join(cfg.App.Home, "trace"), ".jsonl")
	fmt.Println("home:", cfg.App.Home)
	fmt.Println("workspace:", cfg.App.Workspace)
	fmt.Println("traces:", traceCount)
	fmt.Println("memory_files:", memReport.MemoryFiles)
	fmt.Println("memory_index_entries:", memReport.IndexEntries)
	fmt.Println("memory_pending_proposals:", memReport.Proposals["proposed"])
	fmt.Println("learning_tasks:", learningReport.Tasks)
	fmt.Println("learning_failures:", learningReport.Failures)
	fmt.Println("skill_usage:", learningReport.SkillUsage)
	fmt.Println("skill_pending_proposals:", learningReport.SkillProposalsPending)
	fmt.Println("skills:", len(skills))
	fmt.Println("scripts:", len(scripts))
	fmt.Println("schedules:", len(schedules))
	fmt.Println("sandbox_enabled:", cfg.Security.TerminalSandbox.Enabled)
	return nil
}

func runSkill(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mateway skill <list|search|install|catalog|proposal|usage>")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		skills, err := skill.List(cfg.App.Workspace)
		if err != nil {
			return err
		}
		fmt.Println("skills:", len(skills))
		for _, item := range skills {
			fmt.Printf("- %s scope=%s", item.Name, item.Scope)
			if item.Stage != "" {
				fmt.Printf(" stage=%s", item.Stage)
			}
			if item.Priority != "" {
				fmt.Printf(" priority=%s", item.Priority)
			}
			if item.Description != "" {
				fmt.Printf(" description=%s", item.Description)
			}
			fmt.Printf(" path=%s\n", item.Path)
		}
		return nil
	case "search":
		fs := flag.NewFlagSet("mateway skill search", flag.ContinueOnError)
		includeDisabled := fs.Bool("all", false, "include disabled catalogs")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		query := strings.TrimSpace(strings.Join(fs.Args(), " "))
		if query == "" {
			return fmt.Errorf("usage: mateway skill search [--all] <query>")
		}
		results := skill.SearchCatalogs(cfg, query)
		fmt.Println("query:", query)
		fmt.Println("catalogs:", len(results))
		for _, result := range results {
			if !result.Enabled && !*includeDisabled {
				continue
			}
			fmt.Printf("- %s enabled=%v trust=%s url=%s", result.Catalog, result.Enabled, result.TrustLevel, result.URL)
			if result.Adapter != "" {
				fmt.Printf(" adapter=%s", result.Adapter)
			}
			fmt.Printf(" can_install=%v", result.CanInstall)
			if result.InstallURL != "" {
				fmt.Printf(" install_url=%s", result.InstallURL)
			}
			fmt.Println()
		}
		return nil
	case "install":
		fs := flag.NewFlagSet("mateway skill install", flag.ContinueOnError)
		name := fs.String("name", "", "override installed skill name")
		force := fs.Bool("force", false, "overwrite existing installed skill")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return fmt.Errorf("usage: mateway skill install [--name <name>] [--force] <path-or-raw-url>")
		}
		result, err := skill.Install(skill.InstallInput{
			Workspace: cfg.App.Workspace,
			Source:    fs.Arg(0),
			Name:      *name,
			Force:     *force,
		})
		if err != nil {
			return err
		}
		fmt.Println("skill:", result.Name)
		fmt.Println("path:", result.Path)
		return nil
	case "catalog":
		return runSkillCatalog(cfg, args[1:])
	case "proposal":
		return runSkillProposal(cfg, args[1:])
	case "usage":
		return runSkillUsage(cfg, args[1:])
	default:
		return fmt.Errorf("usage: mateway skill <list|search|install|catalog|proposal|usage>")
	}
}

func runSkillCatalog(cfg *config.Root, args []string) error {
	if len(args) != 1 || args[0] != "report" {
		return fmt.Errorf("usage: mateway skill catalog report")
	}
	reports := skill.CatalogReports(cfg)
	fmt.Println("skill_catalogs:", len(reports))
	for _, report := range reports {
		fmt.Printf("- %s enabled=%v trust=%s adapter=%s can_install=%v search_url=%s", report.Name, report.Enabled, report.TrustLevel, report.Adapter, report.CanInstall, report.SearchURL)
		if report.InstallURL != "" {
			fmt.Printf(" install_url=%s", report.InstallURL)
		}
		fmt.Println()
	}
	return nil
}

func runSkillProposal(cfg *config.Root, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mateway skill proposal <list|show|promote|reject>")
	}
	store := skill.NewProposalStore(cfg)
	switch args[0] {
	case "list":
		proposals, err := store.List()
		if err != nil {
			return err
		}
		fmt.Println("skill_proposals:", len(proposals))
		for _, proposal := range proposals {
			fmt.Printf("- %s status=%s skill=%s scope=%s target=%s\n", proposal.ID, proposal.Status, proposal.SkillName, proposal.Scope, proposal.TargetPath)
		}
		return nil
	case "show":
		if len(args) != 2 {
			return fmt.Errorf("usage: mateway skill proposal show <proposal_id>")
		}
		proposal, err := store.Read(args[1])
		if err != nil {
			return err
		}
		fmt.Println("proposal:", proposal.ID)
		fmt.Println("status:", proposal.Status)
		fmt.Println("skill:", proposal.SkillName)
		fmt.Println("scope:", proposal.Scope)
		fmt.Println("target:", proposal.TargetPath)
		fmt.Println("created_at:", proposal.CreatedAt)
		if proposal.Reason != "" {
			fmt.Println("reason:", proposal.Reason)
		}
		if len(proposal.Sources) > 0 {
			fmt.Println("sources:", strings.Join(proposal.Sources, ", "))
		}
		fmt.Println("diff:")
		fmt.Println(proposal.Diff)
		return nil
	case "promote":
		if len(args) != 2 {
			return fmt.Errorf("usage: mateway skill proposal promote <proposal_id>")
		}
		proposal, backupDir, err := store.Promote(args[1])
		if err != nil {
			return err
		}
		fmt.Println("proposal:", proposal.ID)
		fmt.Println("status:", proposal.Status)
		fmt.Println("target:", proposal.TargetPath)
		fmt.Println("backup:", backupDir)
		return nil
	case "reject":
		fs := flag.NewFlagSet("mateway skill proposal reject", flag.ContinueOnError)
		reason := fs.String("reason", "", "rejection reason")
		rejectArgs := reorderRejectReasonFlag(args[1:])
		if err := fs.Parse(rejectArgs); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return fmt.Errorf("usage: mateway skill proposal reject <proposal_id> [--reason <text>]")
		}
		proposal, err := store.Reject(fs.Arg(0), *reason)
		if err != nil {
			return err
		}
		fmt.Println("proposal:", proposal.ID)
		fmt.Println("status:", proposal.Status)
		return nil
	default:
		return fmt.Errorf("usage: mateway skill proposal <list|show|promote|reject>")
	}
}

func runSkillUsage(cfg *config.Root, args []string) error {
	if len(args) != 1 || args[0] != "report" {
		return fmt.Errorf("usage: mateway skill usage report")
	}
	report, err := memory.BuildLearningReport(memory.LearningReportInput{Home: cfg.App.Home, Workspace: cfg.App.Workspace})
	if err != nil {
		return err
	}
	printLearningReport(report)
	return nil
}

func runAgent(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mateway agent <list|report|lint|create|bind|unbind>")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	manager := agentprofile.Manager{Config: cfg}
	switch args[0] {
	case "list":
		agents := manager.List()
		fmt.Println("agents:", len(agents))
		for _, agent := range agents {
			fmt.Printf("- %s name=%s default=%v session_namespace=%s model=%s\n", agent.ID, agent.Name, agent.Default, agent.SessionNamespace, agent.Model.Default)
		}
		return nil
	case "report", "lint":
		agentID := ""
		if len(args) > 1 {
			agentID = args[1]
		}
		report, err := manager.Report(agentID)
		if err != nil {
			return err
		}
		printAgentReport(report)
		if args[0] == "lint" && hasAgentLintErrors(report.Issues) {
			return fmt.Errorf("agent lint found errors")
		}
		return nil
	case "create":
		fs := flag.NewFlagSet("mateway agent create", flag.ContinueOnError)
		name := fs.String("name", "", "agent display name")
		setDefault := fs.Bool("default", false, "set as default agent")
		if err := fs.Parse(reorderAgentCreateFlags(args[1:])); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return fmt.Errorf("usage: mateway agent create <agent_id> [--name <name>] [--default]")
		}
		created, err := manager.Create(agentprofile.CreateAgentInput{ID: fs.Arg(0), Name: *name, SetDefault: *setDefault})
		if err != nil {
			return err
		}
		fmt.Println("agent:", created.ID)
		fmt.Println("name:", created.Name)
		fmt.Println("default:", created.Default)
		return nil
	case "bind":
		fs := flag.NewFlagSet("mateway agent bind", flag.ContinueOnError)
		channelName := fs.String("channel", "", "channel name such as cli or feishu")
		accountID := fs.String("account-id", "", "optional account id")
		peerID := fs.String("peer-id", "", "optional peer/thread id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return fmt.Errorf("usage: mateway agent bind --channel <channel> [--account-id <id>] [--peer-id <id>] <agent_id>")
		}
		binding, err := manager.Bind(agentprofile.BindInput{Channel: *channelName, AccountID: *accountID, PeerID: *peerID, AgentID: fs.Arg(0)})
		if err != nil {
			return err
		}
		fmt.Printf("binding: channel=%s account_id=%s peer_id=%s agent=%s\n", binding.Channel, binding.AccountID, binding.PeerID, binding.AgentID)
		return nil
	case "unbind":
		fs := flag.NewFlagSet("mateway agent unbind", flag.ContinueOnError)
		channelName := fs.String("channel", "", "channel name such as cli or feishu")
		accountID := fs.String("account-id", "", "optional account id")
		peerID := fs.String("peer-id", "", "optional peer/thread id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		removed, err := manager.Unbind(agentprofile.BindInput{Channel: *channelName, AccountID: *accountID, PeerID: *peerID})
		if err != nil {
			return err
		}
		fmt.Println("removed:", removed)
		return nil
	default:
		return fmt.Errorf("usage: mateway agent <list|report|lint|create|bind|unbind>")
	}
}

func reorderAgentCreateFlags(args []string) []string {
	if len(args) < 3 || strings.HasPrefix(args[0], "-") {
		return args
	}
	var flags []string
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--name" || arg == "-name" {
			if i+1 < len(args) {
				flags = append(flags, arg, args[i+1])
				i++
				continue
			}
		}
		if arg == "--default" || arg == "-default" {
			flags = append(flags, arg)
			continue
		}
		positional = append(positional, arg)
	}
	return append(flags, positional...)
}

func printAgentReport(report agentprofile.AgentReport) {
	fmt.Println("agent:", report.ID)
	fmt.Println("name:", report.Name)
	fmt.Println("default:", report.Default)
	fmt.Println("session_namespace:", report.SessionNS)
	fmt.Println("agent_dir:", report.AgentDir)
	fmt.Println("memory_root:", report.MemoryRoot)
	fmt.Println("model:", report.ModelDefault)
	fmt.Println("skills:", report.Skills)
	fmt.Println("prompt_files:")
	for _, file := range report.PromptFiles {
		fmt.Printf("- %s exists=%v bytes=%d\n", file.Path, file.Exists, file.Bytes)
	}
	fmt.Println("bindings:")
	for _, binding := range report.Bindings {
		fmt.Printf("- channel=%s account_id=%s peer_id=%s agent=%s\n", binding.Channel, binding.AccountID, binding.PeerID, binding.AgentID)
	}
	fmt.Println("issues:", len(report.Issues))
	for _, issue := range report.Issues {
		fmt.Printf("- %s %s %s\n", issue.Severity, issue.Code, issue.Message)
	}
}

func hasAgentLintErrors(issues []agentprofile.Issue) bool {
	for _, issue := range issues {
		if issue.Severity == "error" {
			return true
		}
	}
	return false
}

func runSchedule(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mateway schedule <create|list|test|activate|pause|run-due|serve>")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	store := schedule.Store{Home: cfg.App.Home}
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("mateway schedule create", flag.ContinueOnError)
		runAtFlag := fs.String("run-at", "", "RFC3339 time to run")
		intervalFlag := fs.String("interval", "", "optional interval such as 30m or 24h")
		sessionKeyFlag := fs.String("session-key", "", "optional session key for schedule context")
		noTestFlag := fs.Bool("no-test", false, "activate without a first test run")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			return fmt.Errorf("usage: mateway schedule create --run-at <RFC3339> [--interval <duration>] [--no-test] <task>")
		}
		runAt, err := time.Parse(time.RFC3339, strings.TrimSpace(*runAtFlag))
		if err != nil {
			return fmt.Errorf("run-at must be RFC3339: %w", err)
		}
		var interval time.Duration
		if strings.TrimSpace(*intervalFlag) != "" {
			interval, err = time.ParseDuration(strings.TrimSpace(*intervalFlag))
			if err != nil {
				return err
			}
		}
		task, err := store.Create(schedule.CreateInput{
			SessionKey:  strings.TrimSpace(*sessionKeyFlag),
			Text:        strings.Join(fs.Args(), " "),
			RunAt:       runAt,
			Interval:    interval,
			RequireTest: !*noTestFlag,
			Activate:    *noTestFlag,
		})
		if err != nil {
			return err
		}
		printScheduleTask(task)
		if task.Status == "pending" {
			fmt.Println("next:", "mateway schedule test "+task.ID)
		}
		return nil
	case "list":
		tasks, err := store.List()
		if err != nil {
			return err
		}
		fmt.Println("schedules:", len(tasks))
		for _, task := range tasks {
			fmt.Printf("- %s status=%s run_at=%s interval=%s last=%s text=%s\n", task.ID, task.Status, task.RunAt, task.Interval, task.LastRunStatus, summarizeCLIText(task.Text, 80))
		}
		return nil
	case "test":
		if len(args) != 2 {
			return fmt.Errorf("usage: mateway schedule test <task_id>")
		}
		task, err := store.Read(args[1])
		if err != nil {
			return err
		}
		record, err := runScheduledTask(context.Background(), cfg, store, task, "test")
		if err != nil {
			return err
		}
		if err := store.MarkTested(task, time.Now(), record); err != nil {
			return err
		}
		fmt.Println("test:", record.Status)
		fmt.Println("run:", record.ID)
		if record.Error != "" {
			fmt.Println("error:", record.Error)
		}
		return nil
	case "activate":
		if len(args) != 2 {
			return fmt.Errorf("usage: mateway schedule activate <task_id>")
		}
		task, err := store.Activate(args[1])
		if err != nil {
			return err
		}
		printScheduleTask(task)
		return nil
	case "pause":
		if len(args) != 2 {
			return fmt.Errorf("usage: mateway schedule pause <task_id>")
		}
		task, err := store.Pause(args[1])
		if err != nil {
			return err
		}
		printScheduleTask(task)
		return nil
	case "run-due":
		return runDueSchedules(context.Background(), cfg, store)
	case "serve":
		interval := 30 * time.Second
		if parsed, err := time.ParseDuration(strings.TrimSpace(cfg.Scheduler.Interval)); err == nil && parsed > 0 {
			interval = parsed
		}
		fmt.Println("schedule:", "serve")
		fmt.Println("interval:", interval)
		for {
			if err := runDueSchedules(context.Background(), cfg, store); err != nil {
				return err
			}
			time.Sleep(interval)
		}
	default:
		return fmt.Errorf("usage: mateway schedule <create|list|test|activate|pause|run-due|serve>")
	}
}

func runScript(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mateway script <list|report|run>")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	switch args[0] {
	case "list", "report":
		scripts, err := script.List(cfg)
		if err != nil {
			return err
		}
		fmt.Println("scripts:", len(scripts))
		for _, item := range scripts {
			fmt.Printf("- %s risk=%s path=%s", item.Name, item.Risk, item.Path)
			if item.Description != "" {
				fmt.Printf(" description=%s", item.Description)
			}
			if len(item.RequiredSecrets) > 0 {
				var refs []string
				for _, ref := range item.RequiredSecrets {
					refs = append(refs, ref.ID+"->"+ref.Env)
				}
				fmt.Printf(" required_secrets=%s", strings.Join(refs, ","))
			}
			fmt.Println()
		}
		return nil
	case "run":
		fs := flag.NewFlagSet("mateway script run", flag.ContinueOnError)
		timeoutFlag := fs.Duration("timeout", 20*time.Second, "script timeout")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() < 1 {
			return fmt.Errorf("usage: mateway script run <name> [args...]")
		}
		result, err := script.Run(context.Background(), cfg, script.RunInput{
			Name:    fs.Arg(0),
			Args:    fs.Args()[1:],
			Timeout: *timeoutFlag,
		})
		fmt.Println("script:", result.Script.Name)
		fmt.Println("path:", result.Script.Path)
		fmt.Println("exit_code:", result.ExitCode)
		fmt.Println("duration_ms:", result.Duration.Milliseconds())
		if result.Output != "" {
			fmt.Println("output:")
			fmt.Println(result.Output)
		}
		return err
	default:
		return fmt.Errorf("usage: mateway script <list|report|run>")
	}
}

func runSandbox(args []string) error {
	if len(args) != 1 || args[0] != "report" {
		return fmt.Errorf("usage: mateway sandbox report")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	sb := cfg.Security.TerminalSandbox
	fmt.Println("sandbox_enabled:", sb.Enabled)
	fmt.Println("mode:", sb.Mode)
	fmt.Println("workdir:", sb.WorkDir)
	fmt.Println("timeout_seconds:", sb.TimeoutSeconds)
	fmt.Println("command_prefix:", strings.Join(sb.CommandPrefix, " "))
	if sb.Enabled {
		fmt.Println("evidence:", "terminal.run records sandbox mode and workdir when configured")
	} else {
		fmt.Println("evidence:", "terminal sandbox disabled; terminal.run uses direct shell with configured timeout")
	}
	return nil
}

func runDueSchedules(ctx context.Context, cfg *config.Root, store schedule.Store) error {
	due, err := store.Due(time.Now())
	if err != nil {
		return err
	}
	fmt.Println("due:", len(due))
	for _, task := range due {
		record, err := runScheduledTask(ctx, cfg, store, task, "scheduled")
		if err != nil {
			return err
		}
		if err := store.MarkRan(task, time.Now(), record); err != nil {
			return err
		}
		fmt.Println("ran:", task.ID, "status="+record.Status, "run="+record.ID)
	}
	return nil
}

func runScheduledTask(ctx context.Context, cfg *config.Root, store schedule.Store, task schedule.Task, kind string) (schedule.RunRecord, error) {
	startedAt := time.Now()
	rt := runtime.New(cfg)
	msg := channel.InboundMessage{
		ID:         task.ID,
		Channel:    "schedule",
		SessionKey: firstNonEmpty(task.SessionKey, "schedule:"+task.ID),
		Text:       task.Text,
		Metadata:   map[string]string{"scheduled_task_id": task.ID, "scheduled_run_kind": kind},
	}
	resp, err := rt.Handle(ctx, msg)
	status := "success"
	errText := ""
	output := ""
	tracePath := ""
	if err != nil {
		status = "error"
		errText = err.Error()
	} else {
		output = strings.TrimSpace(resp.Reply.Text)
		tracePath = resp.TracePath
		if resp.Failed {
			status = "error"
		}
	}
	record, recordErr := store.RecordRun(schedule.RunRecord{
		TaskID:     task.ID,
		Kind:       kind,
		Status:     status,
		StartedAt:  startedAt.Format(time.RFC3339),
		FinishedAt: time.Now().Format(time.RFC3339),
		SessionKey: msg.SessionKey,
		Output:     output,
		TracePath:  tracePath,
		Error:      errText,
	})
	if recordErr != nil {
		return record, recordErr
	}
	return record, err
}

func printScheduleTask(task schedule.Task) {
	fmt.Println("schedule:", task.ID)
	fmt.Println("status:", task.Status)
	fmt.Println("run_at:", task.RunAt)
	if task.Interval != "" {
		fmt.Println("interval:", task.Interval)
	}
	fmt.Println("session:", task.SessionKey)
}

func runMemory(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mateway memory <lint|index|search|proposal>")
	}
	switch args[0] {
	case "lint":
		fs := flag.NewFlagSet("mateway memory lint", flag.ContinueOnError)
		rootFlag := fs.String("root", "", "memory root to lint")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		root := strings.TrimSpace(*rootFlag)
		if root == "" {
			root = memoryRoot(cfg)
		}
		result, err := memory.LintRoot(root)
		if err != nil {
			return err
		}
		fmt.Println("memory_root:", result.Root)
		fmt.Println("files:", result.Files)
		fmt.Println("issues:", len(result.Issues))
		for _, issue := range result.Issues {
			fmt.Printf("%s %s %s: %s\n", issue.Severity, issue.Code, issue.Path, issue.Message)
		}
		if result.HasErrors() {
			return fmt.Errorf("memory lint failed")
		}
		return nil
	case "index":
		if len(args) < 2 || args[1] != "rebuild" {
			return fmt.Errorf("usage: mateway memory index rebuild [--root <path>] [--out <path>]")
		}
		fs := flag.NewFlagSet("mateway memory index rebuild", flag.ContinueOnError)
		rootFlag := fs.String("root", "", "memory root to index")
		outFlag := fs.String("out", "", "index output path")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		root := strings.TrimSpace(*rootFlag)
		if root == "" {
			root = memoryRoot(cfg)
		}
		index, issues, err := memory.RebuildIndex(root)
		if err != nil {
			return err
		}
		for _, issue := range issues {
			fmt.Printf("%s %s %s: %s\n", issue.Severity, issue.Code, issue.Path, issue.Message)
		}
		if hasMemoryErrors(issues) {
			return fmt.Errorf("memory index rebuild failed")
		}
		out := strings.TrimSpace(*outFlag)
		if out == "" {
			out = memoryIndexPath(cfg)
		}
		if err := memory.WriteIndex(out, index); err != nil {
			return err
		}
		fmt.Println("memory_root:", index.Root)
		fmt.Println("entries:", len(index.Entries))
		fmt.Println("index:", out)
		return nil
	case "search":
		fs := flag.NewFlagSet("mateway memory search", flag.ContinueOnError)
		rootFlag := fs.String("root", "", "memory root to search")
		scopeFlag := fs.String("scope", "", "optional scope filter")
		typeFlag := fs.String("type", "", "optional type filter")
		limitFlag := fs.Int("limit", 5, "maximum results")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		query := strings.TrimSpace(strings.Join(fs.Args(), " "))
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		root := strings.TrimSpace(*rootFlag)
		if root == "" {
			root = memoryRoot(cfg)
		}
		results, issues, err := memory.SearchRoot(root, memory.SearchOptions{
			Query: query,
			Limit: *limitFlag,
			Scope: strings.TrimSpace(*scopeFlag),
			Type:  strings.TrimSpace(*typeFlag),
		})
		if err != nil {
			return err
		}
		for _, issue := range issues {
			fmt.Printf("%s %s %s: %s\n", issue.Severity, issue.Code, issue.Path, issue.Message)
		}
		if hasMemoryErrors(issues) {
			return fmt.Errorf("memory search failed")
		}
		fmt.Println("memory_root:", root)
		fmt.Println("results:", len(results))
		for i, result := range results {
			fmt.Printf("%d. %s type=%s scope=%s score=%d\n", i+1, result.Path, result.Type, result.Scope, result.Score)
			if result.Snippet != "" {
				fmt.Println("   snippet:", result.Snippet)
			}
			if len(result.Sources) > 0 {
				fmt.Println("   sources:", strings.Join(result.Sources, ", "))
			}
		}
		return nil
	case "proposal":
		return runMemoryProposal(args[1:])
	case "distill":
		return runMemoryDistill(args[1:])
	case "heartbeat":
		return runMemoryHeartbeat(args[1:])
	case "learning":
		return runMemoryLearning(args[1:])
	case "report":
		return runMemoryReport(args[1:])
	default:
		return fmt.Errorf("usage: mateway memory <lint|index|search|proposal|distill|heartbeat|learning|report>")
	}
}

func runMemoryLearning(args []string) error {
	if len(args) != 1 || args[0] != "report" {
		return fmt.Errorf("usage: mateway memory learning report")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	report, err := memory.BuildLearningReport(memory.LearningReportInput{Home: cfg.App.Home, Workspace: cfg.App.Workspace})
	if err != nil {
		return err
	}
	printLearningReport(report)
	return nil
}

func runMemoryReport(args []string) error {
	fs := flag.NewFlagSet("mateway memory report", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "memory root to report")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	root := strings.TrimSpace(*rootFlag)
	if root == "" {
		root = memoryRoot(cfg)
	}
	report, err := memory.BuildReport(memory.ReportInput{Home: cfg.App.Home, MemoryRoot: root})
	if err != nil {
		return err
	}
	fmt.Println("memory_root:", report.MemoryRoot)
	fmt.Println("memory_files:", report.MemoryFiles)
	fmt.Println("index_entries:", report.IndexEntries)
	fmt.Println("issues:", len(report.Issues))
	fmt.Println("proposals:")
	for _, status := range []string{"proposed", "active", "archived", "rejected", "unknown"} {
		if count := report.Proposals[status]; count > 0 {
			fmt.Printf("- %s: %d\n", status, count)
		}
	}
	fmt.Println("observe:")
	for _, name := range []string{"diary", "reflections", "proposals", "audit"} {
		fmt.Printf("- %s: %d\n", name, report.Observe[name])
	}
	return nil
}

func runMemoryHeartbeat(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mateway memory heartbeat <lint-index|distill|learning|skill|serve>")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	switch args[0] {
	case "lint-index":
		result, err := memory.RunLintIndexHeartbeat(memory.HeartbeatInput{
			Home:       cfg.App.Home,
			MemoryRoot: memoryRoot(cfg),
			IndexPath:  memoryIndexPath(cfg),
		})
		if err != nil {
			return err
		}
		fmt.Println("memory_root:", result.Root)
		fmt.Println("files:", result.Files)
		fmt.Println("entries:", result.Entries)
		fmt.Println("issues:", len(result.Issues))
		for _, issue := range result.Issues {
			fmt.Printf("%s %s %s: %s\n", issue.Severity, issue.Code, issue.Path, issue.Message)
		}
		if hasMemoryErrors(result.Issues) {
			return fmt.Errorf("memory heartbeat found lint errors")
		}
		fmt.Println("index:", result.IndexPath)
		return nil
	case "distill":
		result, err := memory.RunDistillHeartbeat(context.Background(), memory.DistillHeartbeatInput{
			Home:       cfg.App.Home,
			MemoryRoot: memoryRoot(cfg),
			Model:      memoryDistillModel(cfg),
		})
		printDistillResult(result)
		return err
	case "learning":
		result, err := memory.RunLearningDistillHeartbeat(context.Background(), memory.LearningHeartbeatInput{
			Home:       cfg.App.Home,
			MemoryRoot: memoryRoot(cfg),
			Model:      memoryDistillModel(cfg),
		})
		printDistillResult(result)
		return err
	case "skill":
		result, err := memory.RunSkillLearningHeartbeat(context.Background(), memory.SkillLearningHeartbeatInput{
			Home:      cfg.App.Home,
			Workspace: cfg.App.Workspace,
			Model:     memoryDistillModel(cfg),
		})
		printSkillLearningResult(result)
		return err
	case "serve":
		fs := flag.NewFlagSet("mateway memory heartbeat serve", flag.ContinueOnError)
		intervalFlag := fs.String("interval", "", "override heartbeat interval")
		onceFlag := fs.Bool("once", false, "run one heartbeat cycle and exit")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		interval := 30 * time.Minute
		jobs := []string{"lint-index"}
		if profile := defaultAgentProfile(cfg); profile != nil {
			jobs = profile.Heartbeat.Jobs
			if parsed, err := time.ParseDuration(profile.Heartbeat.Interval); err == nil && parsed > 0 {
				interval = parsed
			}
		}
		if strings.TrimSpace(*intervalFlag) != "" {
			parsed, err := time.ParseDuration(strings.TrimSpace(*intervalFlag))
			if err != nil {
				return err
			}
			interval = parsed
		}
		runOnce := func() error {
			for _, job := range memory.NormalizeHeartbeatJobs(jobs) {
				switch job {
				case "lint-index":
					result, err := memory.RunLintIndexHeartbeat(memory.HeartbeatInput{
						Home:       cfg.App.Home,
						MemoryRoot: memoryRoot(cfg),
						IndexPath:  memoryIndexPath(cfg),
					})
					printHeartbeatResult(result)
					if err != nil {
						return err
					}
					if hasMemoryErrors(result.Issues) {
						return fmt.Errorf("memory heartbeat found lint errors")
					}
				case "memory_distill":
					distill, err := memory.RunDistillHeartbeat(context.Background(), memory.DistillHeartbeatInput{
						Home:       cfg.App.Home,
						MemoryRoot: memoryRoot(cfg),
						Model:      memoryDistillModel(cfg),
					})
					printDistillResult(distill)
					if err != nil {
						return err
					}
				case "learning_distill":
					learning, err := memory.RunLearningDistillHeartbeat(context.Background(), memory.LearningHeartbeatInput{
						Home:       cfg.App.Home,
						MemoryRoot: memoryRoot(cfg),
						Model:      memoryDistillModel(cfg),
					})
					printDistillResult(learning)
					if err != nil {
						return err
					}
				case "skill_learning":
					skillResult, err := memory.RunSkillLearningHeartbeat(context.Background(), memory.SkillLearningHeartbeatInput{
						Home:      cfg.App.Home,
						Workspace: cfg.App.Workspace,
						Model:     memoryDistillModel(cfg),
					})
					printSkillLearningResult(skillResult)
					if err != nil {
						return err
					}
				}
			}
			return nil
		}
		if *onceFlag {
			return runOnce()
		}
		fmt.Println("heartbeat:", "serve")
		fmt.Println("interval:", interval)
		fmt.Println("jobs:", strings.Join(memory.NormalizeHeartbeatJobs(jobs), ", "))
		return memory.ServeHeartbeat(context.Background(), memory.HeartbeatServeInput{
			Home:       cfg.App.Home,
			Workspace:  cfg.App.Workspace,
			MemoryRoot: memoryRoot(cfg),
			IndexPath:  memoryIndexPath(cfg),
			Interval:   interval,
			Jobs:       jobs,
			Model:      memoryDistillModel(cfg),
			OnResult: func(result memory.HeartbeatResult) {
				printHeartbeatResult(result)
			},
		})
	default:
		return fmt.Errorf("usage: mateway memory heartbeat <lint-index|distill|learning|skill|serve>")
	}
}

func printHeartbeatResult(result memory.HeartbeatResult) {
	fmt.Println("memory_root:", result.Root)
	fmt.Println("files:", result.Files)
	fmt.Println("entries:", result.Entries)
	fmt.Println("issues:", len(result.Issues))
	for _, issue := range result.Issues {
		fmt.Printf("%s %s %s: %s\n", issue.Severity, issue.Code, issue.Path, issue.Message)
	}
	if result.IndexPath != "" {
		fmt.Println("index:", result.IndexPath)
	}
	if result.Distill.Scanned > 0 || result.Distill.Created > 0 || result.Distill.Skipped > 0 || result.Distill.Duplicates > 0 || len(result.Distill.Errors) > 0 {
		printDistillResult(result.Distill)
	}
	if result.Learning.Scanned > 0 || result.Learning.Created > 0 || result.Learning.Skipped > 0 || result.Learning.Duplicates > 0 || len(result.Learning.Errors) > 0 {
		printDistillResult(result.Learning)
	}
	if result.Skill.Scanned > 0 || result.Skill.Created > 0 || result.Skill.Skipped > 0 || result.Skill.Duplicates > 0 || len(result.Skill.Errors) > 0 {
		printSkillLearningResult(result.Skill)
	}
}

func printDistillResult(result memory.DistillHeartbeatResult) {
	fmt.Println("distill_scanned:", result.Scanned)
	fmt.Println("distill_created:", result.Created)
	fmt.Println("distill_skipped:", result.Skipped)
	fmt.Println("distill_duplicates:", result.Duplicates)
	if len(result.ProposalIDs) > 0 {
		fmt.Println("distill_proposals:", strings.Join(result.ProposalIDs, ", "))
	}
	for _, errText := range result.Errors {
		fmt.Println("distill_error:", errText)
	}
}

func printSkillLearningResult(result memory.SkillLearningHeartbeatResult) {
	fmt.Println("skill_learning_scanned:", result.Scanned)
	fmt.Println("skill_learning_created:", result.Created)
	fmt.Println("skill_learning_skipped:", result.Skipped)
	fmt.Println("skill_learning_duplicates:", result.Duplicates)
	if len(result.ProposalIDs) > 0 {
		fmt.Println("skill_learning_proposals:", strings.Join(result.ProposalIDs, ", "))
	}
	for _, errText := range result.Errors {
		fmt.Println("skill_learning_error:", errText)
	}
}

func printLearningReport(report memory.LearningReport) {
	fmt.Println("learning_tasks:", report.Tasks)
	fmt.Println("learning_failures:", report.Failures)
	fmt.Println("reflections:", report.Reflections)
	fmt.Println("skill_usage:", report.SkillUsage)
	fmt.Println("skill_issues:", report.SkillIssues)
	fmt.Println("memory_proposals_pending:", report.MemoryProposalsPending)
	fmt.Println("skill_proposals_pending:", report.SkillProposalsPending)
	if report.LastLearningAudit != "" {
		fmt.Println("last_learning_audit:", report.LastLearningAudit)
	}
}

func memoryDistillModel(cfg *config.Root) memory.DistillModel {
	if cfg == nil {
		return nil
	}
	names := []string{}
	names = append(names, cfg.Model.Roles.Models("memory_distill")...)
	if profile := defaultAgentProfile(cfg); profile != nil {
		names = append(names, profile.Model.Roles.Models("memory_distill")...)
		if strings.TrimSpace(profile.Model.Default) != "" {
			names = append(names, profile.Model.Default)
		}
		names = append(names, profile.Model.Fallbacks...)
	}
	if strings.TrimSpace(cfg.Model.Default) != "" {
		names = append(names, cfg.Model.Default)
	}
	names = append(names, cfg.Model.Fallbacks...)
	var configs []config.ModelConfig
	seen := map[string]bool{}
	for _, name := range names {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		for _, candidate := range cfg.Models {
			if candidate.Enabled && strings.EqualFold(candidate.Name, key) && strings.TrimSpace(candidate.ResolvedAPIKey()) != "" {
				configs = append(configs, candidate)
				break
			}
		}
	}
	if len(configs) == 0 {
		return nil
	}
	return model.NewFallbackAgentModel(configs)
}

func defaultAgentProfile(cfg *config.Root) *config.AgentProfileConfig {
	if cfg == nil {
		return nil
	}
	for i := range cfg.Agents.Profiles {
		if cfg.Agents.Profiles[i].ID == cfg.Agents.Default {
			return &cfg.Agents.Profiles[i]
		}
	}
	for i := range cfg.Agents.Profiles {
		if cfg.Agents.Profiles[i].Default {
			return &cfg.Agents.Profiles[i]
		}
	}
	if len(cfg.Agents.Profiles) > 0 {
		return &cfg.Agents.Profiles[0]
	}
	return nil
}

func runMemoryDistill(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mateway memory distill <session|project>")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	switch args[0] {
	case "session":
		if len(args) != 2 {
			return fmt.Errorf("usage: mateway memory distill session <session_key>")
		}
		store := session.NewStore(cfg.App.Home)
		state, err := store.Load(args[1])
		if err != nil {
			return err
		}
		result, err := memory.DistillSession(memory.SessionDistillInput{Home: cfg.App.Home, State: state, Reason: "manual"})
		if err != nil {
			return err
		}
		fmt.Println("session:", state.Key)
		fmt.Println("distill:", result.Path)
		return nil
	case "project":
		if len(args) != 3 || args[1] != "close" {
			return fmt.Errorf("usage: mateway memory distill project close <project_id>")
		}
		result, err := memory.DistillProject(memory.ProjectDistillInput{
			Home:       cfg.App.Home,
			MemoryRoot: memoryRoot(cfg),
			ProjectID:  args[2],
			Reason:     "project_close",
		})
		if err != nil {
			return err
		}
		fmt.Println("project:", args[2])
		fmt.Println("entries:", result.Entries)
		fmt.Println("distill:", result.Path)
		return nil
	default:
		return fmt.Errorf("usage: mateway memory distill <session|project>")
	}
}

func runMemoryProposal(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mateway memory proposal <create|list|show|reject|commit>")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	store := memory.ProposalStore{Home: cfg.App.Home, MemoryRoot: memoryRoot(cfg)}
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("mateway memory proposal create", flag.ContinueOnError)
		title := fs.String("title", "", "proposal title")
		body := fs.String("body", "", "proposal body")
		proposalType := fs.String("type", "experience", "proposal type")
		scope := fs.String("scope", "agent", "proposal scope")
		source := fs.String("source", "", "comma-separated source refs")
		confidence := fs.String("confidence", "low", "confidence: high, medium, or low")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		proposal, err := store.Create(memory.CreateProposalInput{
			Type:       *proposalType,
			Scope:      *scope,
			Title:      *title,
			Body:       *body,
			Sources:    splitComma(*source),
			Confidence: *confidence,
		})
		if err != nil {
			return err
		}
		fmt.Println("proposal:", proposal.ID)
		fmt.Println("path:", proposal.Path)
		return nil
	case "list":
		proposals, err := store.List()
		if err != nil {
			return err
		}
		fmt.Println("proposals:", len(proposals))
		for _, proposal := range proposals {
			fmt.Printf("- %s status=%s type=%s scope=%s title=%s\n", proposal.ID, proposal.Status, proposal.Type, proposal.Scope, proposal.Title)
		}
		return nil
	case "show":
		if len(args) != 2 {
			return fmt.Errorf("usage: mateway memory proposal show <proposal_id>")
		}
		proposal, err := store.Get(args[1])
		if err != nil {
			return err
		}
		printMemoryProposalDetail(proposal)
		return nil
	case "reject":
		fs := flag.NewFlagSet("mateway memory proposal reject", flag.ContinueOnError)
		reason := fs.String("reason", "", "rejection reason")
		rejectArgs := reorderRejectReasonFlag(args[1:])
		if err := fs.Parse(rejectArgs); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return fmt.Errorf("usage: mateway memory proposal reject <proposal_id> [--reason <text>]")
		}
		proposal, err := store.Reject(fs.Arg(0), *reason)
		if err != nil {
			return err
		}
		fmt.Println("proposal:", proposal.ID)
		fmt.Println("status:", proposal.Status)
		return nil
	case "commit":
		if len(args) != 2 {
			return fmt.Errorf("usage: mateway memory proposal commit <proposal_id>")
		}
		proposal, target, err := store.Commit(args[1])
		if err != nil {
			return err
		}
		fmt.Println("proposal:", proposal.ID)
		fmt.Println("status:", proposal.Status)
		fmt.Println("memory:", target)
		return nil
	default:
		return fmt.Errorf("usage: mateway memory proposal <create|list|show|reject|commit>")
	}
}

func printMemoryProposalDetail(proposal memory.Proposal) {
	fmt.Println("proposal:", proposal.ID)
	fmt.Println("status:", proposal.Status)
	fmt.Println("type:", proposal.Type)
	fmt.Println("scope:", proposal.Scope)
	fmt.Println("title:", proposal.Title)
	fmt.Println("confidence:", proposal.Confidence)
	if proposal.CreatedAt != "" {
		fmt.Println("created_at:", proposal.CreatedAt)
	}
	if proposal.UpdatedAt != "" {
		fmt.Println("updated_at:", proposal.UpdatedAt)
	}
	if len(proposal.Sources) > 0 {
		fmt.Println("sources:")
		for _, source := range proposal.Sources {
			fmt.Println("-", source)
		}
	}
	if summary := firstContentLine(proposal.Body, proposal.Title); summary != "" {
		fmt.Println()
		fmt.Println("why:")
		fmt.Println(summary)
	}
	fmt.Println()
	fmt.Println("body:")
	fmt.Println(strings.TrimSpace(proposal.Body))
	fmt.Println()
	fmt.Println("actions:")
	fmt.Printf("commit: mateway memory proposal commit %s\n", proposal.ID)
	fmt.Printf("reject: mateway memory proposal reject %s --reason \"...\"\n", proposal.ID)
}

func firstContentLine(body, title string) string {
	body = strings.TrimSpace(body)
	body = strings.TrimPrefix(body, "# "+strings.TrimSpace(title))
	body = strings.TrimSpace(body)
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "-"))
		if line != "" && !strings.HasPrefix(line, "#") {
			return line
		}
	}
	return ""
}

func runAgentProfile(args []string) error {
	if len(args) == 0 || args[0] != "proposal" {
		return fmt.Errorf("usage: mateway agent-profile proposal <list|show|promote|reject>")
	}
	if len(args) < 2 {
		return fmt.Errorf("usage: mateway agent-profile proposal <list|show|promote|reject>")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	store := agentprofile.NewStore(cfg)
	switch args[1] {
	case "list":
		proposals, err := store.List()
		if err != nil {
			return err
		}
		fmt.Println("agent_profile_proposals:", len(proposals))
		for _, proposal := range proposals {
			fmt.Printf("- %s status=%s agent=%s target=%s\n", proposal.ID, proposal.Status, proposal.AgentID, proposal.TargetPath)
		}
		return nil
	case "show":
		if len(args) != 3 {
			return fmt.Errorf("usage: mateway agent-profile proposal show <proposal_id>")
		}
		proposal, err := store.Read(args[2])
		if err != nil {
			return err
		}
		fmt.Println("proposal:", proposal.ID)
		fmt.Println("status:", proposal.Status)
		fmt.Println("agent:", proposal.AgentID)
		fmt.Println("target:", proposal.TargetPath)
		fmt.Println("created_at:", proposal.CreatedAt)
		if proposal.Reason != "" {
			fmt.Println("reason:", proposal.Reason)
		}
		fmt.Println("diff:")
		fmt.Println(proposal.Diff)
		return nil
	case "promote":
		if len(args) != 3 {
			return fmt.Errorf("usage: mateway agent-profile proposal promote <proposal_id>")
		}
		proposal, backupDir, err := store.Promote(args[2])
		if err != nil {
			return err
		}
		fmt.Println("proposal:", proposal.ID)
		fmt.Println("status:", proposal.Status)
		fmt.Println("target:", proposal.TargetPath)
		fmt.Println("backup:", backupDir)
		return nil
	case "reject":
		fs := flag.NewFlagSet("mateway agent-profile proposal reject", flag.ContinueOnError)
		reason := fs.String("reason", "", "rejection reason")
		rejectArgs := reorderRejectReasonFlag(args[2:])
		if err := fs.Parse(rejectArgs); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return fmt.Errorf("usage: mateway agent-profile proposal reject <proposal_id> [--reason <text>]")
		}
		proposal, err := store.Reject(fs.Arg(0), *reason)
		if err != nil {
			return err
		}
		fmt.Println("proposal:", proposal.ID)
		fmt.Println("status:", proposal.Status)
		return nil
	default:
		return fmt.Errorf("usage: mateway agent-profile proposal <list|show|promote|reject>")
	}
}

func reorderRejectReasonFlag(args []string) []string {
	if len(args) != 3 || args[0] == "--reason" || args[0] == "-reason" {
		return args
	}
	if args[1] != "--reason" && args[1] != "-reason" {
		return args
	}
	return []string{args[1], args[2], args[0]}
}

func memoryRoot(cfg *config.Root) string {
	if cfg == nil {
		return filepath.Join(config.DefaultHome(), "workspace", "memory")
	}
	if root := strings.TrimSpace(cfg.Memory.Root); root != "" {
		return root
	}
	workspace := strings.TrimSpace(cfg.App.Workspace)
	if workspace == "" {
		workspace = filepath.Join(cfg.App.Home, "workspace")
	}
	return filepath.Join(workspace, "memory")
}

func memoryIndexPath(cfg *config.Root) string {
	home := config.DefaultHome()
	if cfg != nil && strings.TrimSpace(cfg.App.Home) != "" {
		home = cfg.App.Home
	}
	return filepath.Join(home, "indexes", "memory_index.json")
}

func hasMemoryErrors(issues []memory.Issue) bool {
	for _, issue := range issues {
		if issue.Severity == "error" {
			return true
		}
	}
	return false
}

func splitComma(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func countFiles(dir, ext string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && (ext == "" || strings.EqualFold(filepath.Ext(entry.Name()), ext)) {
			count++
		}
	}
	return count
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func summarizeCLIText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit] + fmt.Sprintf("... (%d chars)", len(text))
}

type homeReport struct {
	Home      string
	Expected  []homeReportItem
	Generated []homeReportItem
	Local     []homeReportItem
	Unknown   []homeReportItem
}

type homeReportItem struct {
	Name string
	Kind string
}

func buildHomeReport(home string) (homeReport, error) {
	report := homeReport{Home: home}
	entries, err := os.ReadDir(home)
	if err != nil {
		return report, err
	}
	expected := map[string]string{
		"config":    "configuration",
		"workspace": "agent workspace, memory, skills",
		"sessions":  "runtime session state",
	}
	generated := map[string]string{
		"trace":     "runtime traces",
		"observe":   "learning diary, proposals, audit",
		"indexes":   "derived search indexes",
		"schedules": "scheduled task state",
		"run":       "process runtime state",
		"logs":      "service logs",
		"tmp":       "temporary files",
	}
	local := map[string]string{
		"scripts":        "local user scripts",
		"docker":         "legacy/local service data",
		"docker-compose": "legacy/local service data",
	}
	for _, entry := range entries {
		item := homeReportItem{Name: entry.Name()}
		switch {
		case expected[entry.Name()] != "":
			item.Kind = expected[entry.Name()]
			report.Expected = append(report.Expected, item)
		case generated[entry.Name()] != "":
			item.Kind = generated[entry.Name()]
			report.Generated = append(report.Generated, item)
		case local[entry.Name()] != "":
			item.Kind = local[entry.Name()]
			report.Local = append(report.Local, item)
		default:
			item.Kind = "not recognized by current clean layout"
			report.Unknown = append(report.Unknown, item)
		}
	}
	return report, nil
}

func runTest(args []string) error {
	fs := flag.NewFlagSet("mateway test", flag.ContinueOnError)
	caseName := fs.String("case", "read-readme", "test case: read-readme, project-index, web-search, or custom")
	message := fs.String("message", "", "custom task message")
	sessionKey := fs.String("session-key", "", "session key to reuse")
	home := fs.String("home", "", "override MATEWAY_HOME for this run")
	record := fs.Bool("record", true, "write test result JSON under testdata/runs")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := loadConfigFromHome(*home)
	if err != nil {
		return err
	}
	addCurrentWorkingDirectoryForTest(cfg)
	text := strings.TrimSpace(*message)
	if text == "" {
		text, err = testCaseMessage(*caseName, cfg)
		if err != nil {
			return err
		}
	}
	key := strings.TrimSpace(*sessionKey)
	if key == "" {
		key = "test:" + strings.ReplaceAll(strings.ToLower(strings.TrimSpace(*caseName)), " ", "-") + "-" + time.Now().Format("20060102150405")
	}
	rt := runtime.New(cfg)
	resp, err := rt.Handle(context.Background(), channel.InboundMessage{
		ID:         "test",
		Channel:    "test",
		ThreadID:   key,
		UserID:     "local",
		SessionKey: key,
		Text:       text,
	})
	if err != nil {
		return err
	}
	state, err := rt.Store.Load(key)
	if err != nil {
		return err
	}
	fmt.Println("case:", *caseName)
	fmt.Println("session:", key)
	fmt.Println("message:", text)
	fmt.Println()
	fmt.Println(resp.Reply.Text)
	if resp.TracePath != "" {
		fmt.Println()
		fmt.Println("trace:", resp.TracePath)
	}
	if len(state.Tasks) > 0 {
		task := state.Tasks[len(state.Tasks)-1]
		fmt.Println()
		fmt.Println("task:", task.ID, task.Status)
		for _, step := range task.Steps {
			fmt.Printf("- %s %s", step.Tool, step.Status)
			if acceptance, ok := step.Evidence["acceptance"]; ok {
				fmt.Printf(" acceptance=%v", acceptance)
			}
			fmt.Println()
		}
	}
	if *record {
		path, err := writeTestRecord(*caseName, key, text, resp, state)
		if err != nil {
			return err
		}
		fmt.Println()
		fmt.Println("record:", path)
	}
	return nil
}

func writeTestRecord(caseName, sessionKey, message string, resp runtime.Response, state any) (string, error) {
	dir := filepath.Join("testdata", "runs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := time.Now().Format("20060102-150405") + "-" + sanitizeFilePart(caseName) + ".json"
	path := filepath.Join(dir, name)
	data, err := json.MarshalIndent(map[string]any{
		"case":       caseName,
		"session":    sessionKey,
		"message":    message,
		"reply":      resp.Reply,
		"failed":     resp.Failed,
		"trace_id":   resp.TraceID,
		"trace_path": resp.TracePath,
		"state":      state,
		"created_at": time.Now().Format(time.RFC3339),
	}, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, append(data, '\n'), 0o644)
}

func sanitizeFilePart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		value = "custom"
	}
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('-')
	}
	return strings.Trim(b.String(), "-")
}

func addCurrentWorkingDirectoryForTest(cfg *config.Root) {
	if cfg == nil {
		return
	}
	cwd, err := os.Getwd()
	if err != nil || strings.TrimSpace(cwd) == "" {
		return
	}
	for _, existing := range cfg.Security.AccessiblePaths {
		if existing == cwd {
			return
		}
	}
	cfg.Security.AccessiblePaths = append(cfg.Security.AccessiblePaths, cwd)
}

func testCaseMessage(name string, cfg ...*config.Root) (string, error) {
	cwd, _ := os.Getwd()
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "read-readme":
		return "请读取 " + filepath.Join(cwd, "README.md") + "，然后用三句话总结这个项目当前形态。", nil
	case "project-index":
		return "请查看 " + cwd + " 的项目结构，并说明最重要的目录各自负责什么。", nil
	case "web-search":
		return "请搜索今天 OpenAI API 的最新公开信息，并用两句话总结来源。", nil
	case "custom":
		return "", fmt.Errorf("custom case requires --message")
	default:
		return "", fmt.Errorf("unknown test case %q", name)
	}
}

func loadConfig() (*config.Root, error) {
	return loadConfigFromHome("")
}

func loadConfigFromHome(home string) (*config.Root, error) {
	if strings.TrimSpace(home) == "" {
		home = config.DefaultHome()
	}
	if err := config.EnsureDefaultConfigFiles(home); err != nil {
		return nil, err
	}
	return config.NewLoader(home).Load()
}

func serveGateway() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	rt := runtime.New(cfg)
	return gateway.Serve(context.Background(), gateway.Config{
		Config:  cfg,
		Runtime: rt,
	})
}

func printHelp() {
	fmt.Println(`mateway

Usage:
  mateway init
  mateway ask <message>
  mateway test [--case read-readme|project-index|web-search] [--message <task>] [--record=false]
  mateway trace <trace-jsonl-path>
  mateway workspace report
  mateway session list
  mateway session show <session_key>
  mateway session archive list <session_key>
  mateway session archive show <session_key> <archive_id>
  mateway memory lint [--root <path>]
  mateway memory index rebuild [--root <path>] [--out <path>]
  mateway memory search [--root <path>] [--scope <scope>] [--type <type>] <query>
  mateway memory proposal create --title <title> --body <body> [--source trace:id]
  mateway memory proposal list
  mateway memory proposal show <proposal_id>
  mateway memory proposal reject <proposal_id> [--reason <text>]
  mateway memory proposal commit <proposal_id>
  mateway agent list
  mateway agent report [agent_id]
  mateway agent lint [agent_id]
  mateway agent create <agent_id> [--name <name>] [--default]
  mateway agent bind --channel <channel> [--account-id <id>] [--peer-id <id>] <agent_id>
  mateway agent unbind --channel <channel> [--account-id <id>] [--peer-id <id>]
  mateway agent-profile proposal list
  mateway agent-profile proposal show <proposal_id>
  mateway agent-profile proposal promote <proposal_id>
  mateway agent-profile proposal reject <proposal_id> [--reason <text>]
  mateway memory distill session <session_key>
  mateway memory distill project close <project_id>
  mateway memory heartbeat lint-index
  mateway memory heartbeat distill
  mateway memory heartbeat learning
  mateway memory heartbeat skill
  mateway memory heartbeat serve [--once] [--interval <duration>]
  mateway memory learning report
  mateway memory report [--root <path>]
  mateway schedule list
  mateway schedule run-due
  mateway schedule serve
  mateway sandbox report
  mateway script list
  mateway script report
  mateway script run <name> [args...]
  mateway home report
  mateway skill list
  mateway skill catalog report
  mateway skill search [--all] <query>
  mateway skill install [--name <name>] [--force] <path-or-raw-url>
  mateway skill proposal list
  mateway skill proposal show <proposal_id>
  mateway skill proposal promote <proposal_id>
  mateway skill proposal reject <proposal_id> [--reason <text>]
  mateway skill usage report
  mateway secret set <id> [value]
  mateway secret get <id>
  mateway secret list
  mateway secret delete <id>
  mateway channel list
  mateway weixin login [--timeout <duration>]
  mateway weixin enable [account_id]
  mateway doctor
  mateway gateway <serve|start|restart|stop|status>`)
}
