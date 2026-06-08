package main

import (
	"flag"
	"fmt"
	"strings"

	"github.com/dongping/mateway/internal/agentprofile"
)

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

func reorderToolAccessFlags(args []string) []string {
	if len(args) < 3 || strings.HasPrefix(args[0], "-") {
		return args
	}
	var flags []string
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--agent" || arg == "-agent" {
			if i+1 < len(args) {
				flags = append(flags, arg, args[i+1])
				i++
				continue
			}
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
