package main

import (
	"flag"
	"fmt"
	"strings"

	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/memory"
	"github.com/dongping/mateway/internal/skill"
)

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
		if strings.TrimSpace(result.MetadataPath) != "" {
			fmt.Println("metadata:", result.MetadataPath)
		}
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
