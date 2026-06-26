package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/runtime"
	"github.com/dongping/mateway/internal/schedule"
)

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
		if record.RunbookID != "" {
			fmt.Println("runbook:", record.RunbookID)
		}
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
	sessionKey := firstNonEmpty(task.SessionKey, "schedule:"+task.ID)
	taskText := task.Text
	runbookID := task.RunbookID
	if kind == "scheduled" && strings.TrimSpace(runbookID) != "" {
		runbook, runbookErr := store.ReadRunbook(runbookID)
		if runbookErr != nil {
			record, recordErr := store.RecordRun(schedule.RunRecord{
				TaskID:     task.ID,
				Kind:       kind,
				Status:     "error",
				RunbookID:  runbookID,
				StartedAt:  startedAt.Format(time.RFC3339),
				FinishedAt: time.Now().Format(time.RFC3339),
				SessionKey: sessionKey,
				Error:      "runbook: " + runbookErr.Error(),
			})
			if recordErr != nil {
				return record, recordErr
			}
			return record, nil
		}
		if strings.TrimSpace(runbook.Text) != "" {
			taskText = runbook.Text
		}
	}
	msg := channel.InboundMessage{
		ID:         task.ID,
		Channel:    "schedule",
		SessionKey: sessionKey,
		Text:       taskText,
		Metadata:   map[string]string{"scheduled_task_id": task.ID, "scheduled_run_kind": kind, "scheduled_runbook_id": runbookID},
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
	if kind == "test" && status == "success" {
		if runbook, runbookErr := buildScheduleRunbook(store, rt, task, sessionKey, tracePath); runbookErr == nil {
			runbookID = runbook.ID
		} else {
			status = "error"
			errText = "runbook: " + runbookErr.Error()
		}
	}
	record, recordErr := store.RecordRun(schedule.RunRecord{
		TaskID:     task.ID,
		Kind:       kind,
		Status:     status,
		RunbookID:  runbookID,
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

func buildScheduleRunbook(store schedule.Store, rt runtime.Runtime, task schedule.Task, sessionKey, tracePath string) (schedule.Runbook, error) {
	state, err := rt.Store.Load(sessionKey)
	if err != nil {
		return schedule.Runbook{}, err
	}
	runbook := schedule.Runbook{
		TaskID:    task.ID,
		Text:      task.Text,
		TracePath: tracePath,
	}
	for i := len(state.Tasks) - 1; i >= 0; i-- {
		t := state.Tasks[i]
		if strings.TrimSpace(t.Goal) != strings.TrimSpace(task.Text) && strings.TrimSpace(t.Execution.OriginalTask) != strings.TrimSpace(task.Text) {
			continue
		}
		if t.Graph != nil {
			runbook.Lane = strings.TrimSpace(t.Graph.Lane)
			for _, node := range t.Graph.Nodes {
				if goal := strings.TrimSpace(node.Goal); goal != "" {
					runbook.Steps = append(runbook.Steps, goal)
				}
				if root := stringInputValue(node.Input, "artifact_root"); root != "" && runbook.OutputRoot == "" {
					runbook.OutputRoot = root
				}
			}
		}
		if t.Execution.Contract != nil {
			for _, skill := range t.Execution.Contract.RequiredSkills {
				if path := strings.TrimSpace(skill.Path); path != "" {
					runbook.SkillPaths = append(runbook.SkillPaths, path)
				}
			}
		}
		break
	}
	if runbook.Lane == "" {
		runbook.Lane = "direct"
	}
	return store.RecordRunbook(runbook)
}

func stringInputValue(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(values[key]))
}

func printScheduleTask(task schedule.Task) {
	fmt.Println("schedule:", task.ID)
	fmt.Println("status:", task.Status)
	fmt.Println("run_at:", task.RunAt)
	if task.Interval != "" {
		fmt.Println("interval:", task.Interval)
	}
	if task.RunbookID != "" {
		fmt.Println("runbook:", task.RunbookID)
	}
	fmt.Println("session:", task.SessionKey)
}
