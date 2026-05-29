package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/channel/feishu"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/gateway"
	"github.com/dongping/mateway/internal/memory"
	"github.com/dongping/mateway/internal/runtime"
	"github.com/dongping/mateway/internal/schedule"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/skill"
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
		return nil
	case "home":
		return runHome(args[1:])
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
		if len(summary.ToolCalls) > 0 {
			fmt.Println("tools:", strings.Join(summary.ToolCalls, ", "))
		}
		return nil
	case "memory":
		return runMemory(args[1:])
	case "schedule":
		return runSchedule(args[1:])
	case "skill":
		return runSkill(args[1:])
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
	default:
		printHelp()
		return fmt.Errorf("unknown command %q", args[0])
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

func runSkill(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mateway skill <list|search|install>")
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
	default:
		return fmt.Errorf("usage: mateway skill <list|search|install>")
	}
}

func runSchedule(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mateway schedule <list|run-due|serve>")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	store := schedule.Store{Home: cfg.App.Home}
	switch args[0] {
	case "list":
		tasks, err := store.List()
		if err != nil {
			return err
		}
		fmt.Println("schedules:", len(tasks))
		for _, task := range tasks {
			fmt.Printf("- %s status=%s run_at=%s interval=%s channel=%s thread=%s text=%s\n", task.ID, task.Status, task.RunAt, task.Interval, task.Channel, task.ThreadID, summarizeCLIText(task.Text, 80))
		}
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
		return fmt.Errorf("usage: mateway schedule <list|run-due|serve>")
	}
}

func runDueSchedules(ctx context.Context, cfg *config.Root, store schedule.Store) error {
	due, err := store.Due(time.Now())
	if err != nil {
		return err
	}
	fmt.Println("due:", len(due))
	rt := runtime.New(cfg)
	for _, task := range due {
		msg := channel.InboundMessage{
			ID:         task.ID,
			Channel:    firstNonEmpty(task.Channel, "schedule"),
			ThreadID:   task.ThreadID,
			UserID:     task.UserID,
			SessionKey: firstNonEmpty(task.SessionKey, task.Channel+":"+task.ThreadID),
			Text:       task.Text,
			Metadata:   map[string]string{"scheduled_task_id": task.ID},
		}
		if strings.TrimSpace(msg.SessionKey) == "" || strings.HasSuffix(msg.SessionKey, ":") {
			msg.SessionKey = gateway.SessionKey(msg)
		}
		resp, err := rt.Handle(ctx, msg)
		if err != nil {
			return err
		}
		if err := deliverScheduledReply(ctx, cfg, task, resp.Reply); err != nil {
			return err
		}
		if err := store.MarkRan(task, time.Now()); err != nil {
			return err
		}
		fmt.Println("ran:", task.ID)
	}
	return nil
}

func deliverScheduledReply(ctx context.Context, cfg *config.Root, task schedule.Task, reply channel.OutboundMessage) error {
	if task.Channel != "feishu" {
		fmt.Println("reply:", strings.TrimSpace(reply.Text))
		return nil
	}
	if cfg == nil || !cfg.Channels.Feishu.Enabled {
		return fmt.Errorf("feishu channel is disabled")
	}
	reply.Channel = "feishu"
	reply.ThreadID = task.ThreadID
	return feishu.NewSender(cfg.Channels.Feishu).Send(ctx, reply)
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
	case "report":
		return runMemoryReport(args[1:])
	default:
		return fmt.Errorf("usage: mateway memory <lint|index|search|proposal|distill|heartbeat|report>")
	}
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
		return fmt.Errorf("usage: mateway memory heartbeat <lint-index|serve>")
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
			MemoryRoot: memoryRoot(cfg),
			IndexPath:  memoryIndexPath(cfg),
			Interval:   interval,
			Jobs:       jobs,
			OnResult: func(result memory.HeartbeatResult) {
				printHeartbeatResult(result)
			},
		})
	default:
		return fmt.Errorf("usage: mateway memory heartbeat <lint-index|serve>")
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
		return fmt.Errorf("usage: mateway memory proposal <create|list|reject|commit>")
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
		return fmt.Errorf("usage: mateway memory proposal <create|list|reject|commit>")
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
		"trace":   "runtime traces",
		"observe": "learning diary, proposals, audit",
		"indexes": "derived search indexes",
		"schedules": "scheduled task state",
		"run":     "process runtime state",
		"logs":    "service logs",
		"tmp":     "temporary files",
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
  mateway memory lint [--root <path>]
  mateway memory index rebuild [--root <path>] [--out <path>]
  mateway memory search [--root <path>] [--scope <scope>] [--type <type>] <query>
  mateway memory proposal create --title <title> --body <body> [--source trace:id]
  mateway memory proposal list
  mateway memory proposal reject <proposal_id> [--reason <text>]
  mateway memory proposal commit <proposal_id>
  mateway memory distill session <session_key>
  mateway memory distill project close <project_id>
  mateway memory heartbeat lint-index
  mateway memory heartbeat serve [--once] [--interval <duration>]
  mateway memory report [--root <path>]
  mateway schedule list
  mateway schedule run-due
  mateway schedule serve
  mateway home report
  mateway skill list
  mateway skill search [--all] <query>
  mateway skill install [--name <name>] [--force] <path-or-raw-url>
  mateway doctor
  mateway gateway <serve|start|restart|stop|status>`)
}
