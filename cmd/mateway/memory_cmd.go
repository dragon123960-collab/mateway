package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/memory"
	"github.com/dongping/mateway/internal/model"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/skill"
)

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
		printSkillProposalSummaries(skill.NewProposalStore(cfg), result.ProposalIDs)
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

func printSkillProposalSummaries(store skill.ProposalStore, ids []string) {
	for _, id := range ids {
		proposal, err := store.Read(id)
		if err != nil {
			continue
		}
		fmt.Println("skill_proposal:", proposal.ID)
		fmt.Println("skill_proposal_target:", proposal.TargetPath)
		if proposal.Reason != "" {
			fmt.Println("skill_proposal_reason:", proposal.Reason)
		}
		if summary := firstDiffLine(proposal.Diff); summary != "" {
			fmt.Println("skill_proposal_summary:", summary)
		}
	}
}

func firstDiffLine(diff string) string {
	for _, line := range strings.Split(diff, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "@@") {
			continue
		}
		if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
			return line
		}
	}
	return ""
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
