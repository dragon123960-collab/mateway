package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/memory"
	"github.com/dongping/mateway/internal/schedule"
	"github.com/dongping/mateway/internal/secret"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/skill"
)

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
	fmt.Println("schedules:", len(schedules))
	fmt.Println("sandbox_enabled:", cfg.Security.TerminalSandbox.Enabled)
	return nil
}
