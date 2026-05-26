package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dongping/mateway/internal/app"
	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
	evalpkg "github.com/dongping/mateway/internal/eval"
	"github.com/dongping/mateway/internal/gateway"
	"github.com/dongping/mateway/internal/heartbeat"
	"github.com/dongping/mateway/internal/memory"
	"github.com/dongping/mateway/internal/observer"
	runtimepkg "github.com/dongping/mateway/internal/runtime"
	"github.com/dongping/mateway/internal/schedule"
	"github.com/dongping/mateway/internal/skill"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	args := os.Args[1:]
	if len(args) == 0 {
		printHelp()
		return nil
	}
	switch args[0] {
	case "doctor":
		text, err := app.Doctor("")
		if err != nil {
			return err
		}
		fmt.Println(text)
		return nil
	case "ask":
		if len(args) < 2 {
			return fmt.Errorf("usage: mateway ask <message>")
		}
		message := strings.Join(args[1:], " ")
		a, err := app.Build("", false)
		if err != nil {
			return err
		}
		msg := channel.InboundMessage{
			ID: "cli", Channel: "cli", ThreadID: "cli", UserID: "local", Text: message,
		}
		msg.SessionKey = gateway.SessionKey(msg)
		resp, err := a.Runtime.Handle(context.Background(), msg)
		if err != nil {
			return err
		}
		fmt.Println(resp.Reply.Text)
		return nil
	case "test":
		return runTest(args[1:])
	case "eval":
		return runEval(args[1:], os.Stdout)
	case "feishu":
		return runFeishu()
	case "init":
		home, err := app.Init("")
		if err != nil {
			return err
		}
		fmt.Printf("initialized %s\n", home)
		return nil
	case "gateway":
		if len(args) < 2 {
			return fmt.Errorf("usage: mateway gateway <serve|start|restart|stop|status>")
		}
		switch args[1] {
		case "serve":
			return runGatewayServe()
		case "start":
			return gateway.NewServiceManager().Start(context.Background())
		case "restart":
			return gateway.NewServiceManager().Restart(context.Background())
		case "stop":
			return gateway.NewServiceManager().Stop(context.Background())
		case "status":
			text, err := gateway.NewServiceManager().Status(context.Background(), config.DefaultHome())
			if text != "" {
				fmt.Print(text)
			}
			return err
		default:
			return fmt.Errorf("usage: mateway gateway <serve|start|restart|stop|status>")
		}
	case "trace":
		return runTrace(args[1:], os.Stdout)
	case "memory":
		return runMemory(args[1:], os.Stdout)
	case "skill":
		return runSkill(args[1:], os.Stdout)
	case "heartbeat":
		return runHeartbeat(args[1:], os.Stdout)
	case "schedule":
		return runSchedule(args[1:], os.Stdout)
	default:
		printHelp()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runSchedule(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mateway schedule <create|propose|list|proposals|show|pause|resume|delete|due|run-due|commit-proposal|reject-proposal>")
	}
	a, err := app.Build("", true)
	if err != nil {
		return err
	}
	store := schedule.NewStore(a.Config.App.Home)
	switch args[0] {
	case "create":
		opts, err := parseScheduleCreateOptions(args[1:])
		if err != nil {
			return err
		}
		task, path, err := store.Create(schedule.CreateInput{
			ID:           opts.ID,
			Title:        opts.Title,
			Prompt:       opts.Prompt,
			AgentID:      opts.AgentID,
			RunAt:        opts.RunAt,
			DailyAt:      opts.DailyAt,
			WeeklyAt:     opts.WeeklyAt,
			Weekday:      opts.Weekday,
			Weekdays:     opts.Weekdays,
			MonthlyAt:    opts.MonthlyAt,
			MonthlyDay:   opts.MonthlyDay,
			Interval:     opts.Interval,
			Channel:      opts.Channel,
			ThreadID:     opts.ThreadID,
			UserID:       opts.UserID,
			DeliveryMode: opts.DeliveryMode,
			DeliveryPath: opts.DeliveryPath,
			AllowedTools: opts.AllowedTools,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Schedule task written: %s\n", path)
		fmt.Fprintf(out, "id=%s status=%s schedule=%s\n", task.ID, task.Status, scheduleSummary(task.Schedule))
		return nil
	case "propose":
		opts, err := parseScheduleCreateOptions(args[1:])
		if err != nil {
			return err
		}
		proposal, path, err := store.Propose(schedule.ProposalInput{CreateInput: schedule.CreateInput{
			ID:           opts.ID,
			Title:        opts.Title,
			Prompt:       opts.Prompt,
			AgentID:      opts.AgentID,
			RunAt:        opts.RunAt,
			DailyAt:      opts.DailyAt,
			WeeklyAt:     opts.WeeklyAt,
			Weekday:      opts.Weekday,
			Weekdays:     opts.Weekdays,
			MonthlyAt:    opts.MonthlyAt,
			MonthlyDay:   opts.MonthlyDay,
			Interval:     opts.Interval,
			Channel:      opts.Channel,
			ThreadID:     opts.ThreadID,
			UserID:       opts.UserID,
			DeliveryMode: opts.DeliveryMode,
			DeliveryPath: opts.DeliveryPath,
			AllowedTools: opts.AllowedTools,
		}})
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Schedule proposal written: %s\n", path)
		fmt.Fprintf(out, "id=%s status=%s schedule=%s\n", proposal.Task.ID, proposal.ProposalStatus, scheduleSummary(proposal.Task.Schedule))
		return nil
	case "list":
		tasks, err := store.List()
		if err != nil {
			return err
		}
		if len(tasks) == 0 {
			fmt.Fprintln(out, "No schedule tasks found.")
			return nil
		}
		for _, task := range tasks {
			fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\n", task.ID, task.Status, task.AgentID, scheduleSummary(task.Schedule), task.Title)
		}
		return nil
	case "proposals":
		status := ""
		if len(args) > 1 {
			var err error
			status, err = parseScheduleProposalStatus(args[1:])
			if err != nil {
				return err
			}
		}
		items, err := store.ListProposals(status)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			fmt.Fprintln(out, "No schedule proposals found.")
			return nil
		}
		for _, item := range items {
			fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", item.ID, item.Status, item.Schedule, item.Title)
		}
		return nil
	case "show":
		id, err := oneIDArg(args[1:], "usage: mateway schedule show <id>")
		if err != nil {
			return err
		}
		task, path, err := store.Show(id)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.TrimSpace(task.ID) == "" {
			return fmt.Errorf("invalid schedule task: %s", path)
		}
		fmt.Fprint(out, string(data))
		if len(data) == 0 || data[len(data)-1] != '\n' {
			fmt.Fprintln(out)
		}
		return nil
	case "pause":
		id, err := oneIDArg(args[1:], "usage: mateway schedule pause <id>")
		if err != nil {
			return err
		}
		task, _, err := store.SetStatus(id, schedule.StatusPaused)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Schedule task paused: %s\n", task.ID)
		return nil
	case "resume":
		id, err := oneIDArg(args[1:], "usage: mateway schedule resume <id>")
		if err != nil {
			return err
		}
		task, _, err := store.SetStatus(id, schedule.StatusActive)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Schedule task resumed: %s\n", task.ID)
		return nil
	case "delete":
		id, err := oneIDArg(args[1:], "usage: mateway schedule delete <id>")
		if err != nil {
			return err
		}
		path, err := store.Delete(id)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Schedule task deleted: %s\n", path)
		return nil
	case "due":
		tasks, err := store.Due(time.Now())
		if err != nil {
			return err
		}
		if len(tasks) == 0 {
			fmt.Fprintln(out, "No schedule tasks are due.")
			return nil
		}
		for _, task := range tasks {
			fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", task.ID, task.AgentID, scheduleSummary(task.Schedule), task.Title)
		}
		return nil
	case "commit-proposal":
		id, err := oneIDArg(args[1:], "usage: mateway schedule commit-proposal <id>")
		if err != nil {
			return err
		}
		task, path, err := store.CommitProposal(id)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Schedule proposal committed: %s\n", path)
		fmt.Fprintf(out, "id=%s status=%s schedule=%s\n", task.ID, task.Status, scheduleSummary(task.Schedule))
		return nil
	case "reject-proposal":
		id, reason, err := parseScheduleRejectProposalOptions(args[1:])
		if err != nil {
			return err
		}
		proposal, path, err := store.RejectProposal(id, reason)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Schedule proposal rejected: %s\n", path)
		fmt.Fprintf(out, "id=%s status=%s\n", proposal.Task.ID, proposal.ProposalStatus)
		return nil
	case "run-due":
		results, err := schedule.Runner{Store: store, Handle: scheduleHandler(a.Runtime.Handle), PolicyHandler: a.Runtime}.RunDue(context.Background(), time.Now())
		if err != nil {
			return err
		}
		if len(results) == 0 {
			fmt.Fprintln(out, "No schedule tasks are due.")
			return nil
		}
		for _, result := range results {
			status := "ok"
			if result.Failed {
				status = "failed"
			}
			fmt.Fprintf(out, "%s\t%s\truntime=%s\tdelivery=%s\ttrace=%s\toutput=%s\n",
				result.Task.ID,
				status,
				firstNonEmptyLocal(result.RuntimeAcceptStatus, "-"),
				firstNonEmptyLocal(result.DeliveryAcceptStatus, "-"),
				result.TraceID,
				result.OutputPath,
			)
			if strings.TrimSpace(result.RuntimeAcceptReason) != "" {
				fmt.Fprintf(out, "  runtime_reason=%s\n", result.RuntimeAcceptReason)
			}
			if strings.TrimSpace(result.DeliveryAcceptReason) != "" {
				fmt.Fprintf(out, "  delivery_reason=%s\n", result.DeliveryAcceptReason)
			}
			if strings.TrimSpace(result.Error) != "" {
				fmt.Fprintf(out, "  error=%s\n", result.Error)
			}
		}
		return nil
	default:
		return fmt.Errorf("usage: mateway schedule <create|propose|list|proposals|show|pause|resume|delete|due|run-due|commit-proposal|reject-proposal>")
	}
}

type scheduleCreateOptions struct {
	ID           string
	Title        string
	Prompt       string
	AgentID      string
	RunAt        string
	DailyAt      string
	WeeklyAt     string
	Weekday      string
	Weekdays     []string
	MonthlyAt    string
	MonthlyDay   int
	Interval     string
	Channel      string
	ThreadID     string
	UserID       string
	DeliveryMode string
	DeliveryPath string
	AllowedTools []string
}

func parseScheduleCreateOptions(args []string) (scheduleCreateOptions, error) {
	opts := scheduleCreateOptions{AgentID: "main", Channel: "cli", DeliveryMode: "artifact"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--id":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.ID, i = value, next
		case "--title":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.Title, i = value, next
		case "--prompt":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.Prompt, i = value, next
		case "--agent":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.AgentID, i = value, next
		case "--daily-at":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.DailyAt, i = value, next
		case "--run-at":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.RunAt, i = value, next
		case "--weekly-at":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.WeeklyAt, i = value, next
		case "--weekday":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.Weekday, i = value, next
		case "--weekdays":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.Weekdays, i = splitScheduleList(value), next
		case "--monthly-at":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.MonthlyAt, i = value, next
		case "--monthly-day":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			day, err := parsePositiveCLIInt(value)
			if err != nil {
				return opts, err
			}
			opts.MonthlyDay, i = day, next
		case "--interval":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.Interval, i = value, next
		case "--channel":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.Channel, i = value, next
		case "--thread":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.ThreadID, i = value, next
		case "--user":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.UserID, i = value, next
		case "--delivery":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.DeliveryMode, i = value, next
		case "--delivery-path":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.DeliveryPath, i = value, next
		case "--tool":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.AllowedTools, i = append(opts.AllowedTools, value), next
		default:
			return opts, fmt.Errorf("unknown schedule create option %q", args[i])
		}
	}
	if strings.TrimSpace(opts.Title) == "" || strings.TrimSpace(opts.Prompt) == "" {
		return opts, fmt.Errorf("usage: mateway schedule create --title <title> --prompt <text> (--run-at RFC3339 | --daily-at HH:MM | --weekly-at HH:MM --weekday DAY | --monthly-at HH:MM --monthly-day N | --interval 2h)")
	}
	if !opts.hasSchedule() {
		return opts, fmt.Errorf("schedule time is required: use --run-at for one-shot tasks, or an explicit recurring option such as --daily-at/--interval")
	}
	return opts, nil
}

func (o scheduleCreateOptions) hasSchedule() bool {
	return strings.TrimSpace(o.RunAt) != "" ||
		strings.TrimSpace(o.DailyAt) != "" ||
		strings.TrimSpace(o.WeeklyAt) != "" ||
		strings.TrimSpace(o.Weekday) != "" ||
		len(o.Weekdays) > 0 ||
		strings.TrimSpace(o.MonthlyAt) != "" ||
		o.MonthlyDay > 0 ||
		strings.TrimSpace(o.Interval) != ""
}

func parseScheduleProposalStatus(args []string) (string, error) {
	status := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--status":
			value, next, err := optionValue(args, i)
			if err != nil {
				return "", err
			}
			status, i = value, next
		default:
			return "", fmt.Errorf("unknown schedule proposals option %q", args[i])
		}
	}
	return status, nil
}

func splitScheduleList(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
	var out []string
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func parsePositiveCLIInt(value string) (int, error) {
	n := 0
	for _, ch := range strings.TrimSpace(value) {
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("expected positive integer, got %q", value)
		}
		n = n*10 + int(ch-'0')
	}
	if n <= 0 {
		return 0, fmt.Errorf("expected positive integer, got %q", value)
	}
	return n, nil
}

func parseScheduleRejectProposalOptions(args []string) (string, string, error) {
	if len(args) == 0 {
		return "", "", fmt.Errorf("usage: mateway schedule reject-proposal <id> [--reason <text>]")
	}
	id := ""
	reason := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--reason":
			value, next, err := optionValue(args, i)
			if err != nil {
				return "", "", err
			}
			reason, i = value, next
		default:
			if id == "" && !strings.HasPrefix(args[i], "-") {
				id = args[i]
				continue
			}
			return "", "", fmt.Errorf("unknown schedule reject-proposal option %q", args[i])
		}
	}
	if strings.TrimSpace(id) == "" {
		return "", "", fmt.Errorf("usage: mateway schedule reject-proposal <id> [--reason <text>]")
	}
	return id, reason, nil
}

func oneIDArg(args []string, usage string) (string, error) {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return "", fmt.Errorf("%s", usage)
	}
	return strings.TrimSpace(args[0]), nil
}

func scheduleHandler(handle func(context.Context, channel.InboundMessage) (runtimepkg.Response, error)) schedule.Handler {
	return func(ctx context.Context, msg channel.InboundMessage) (schedule.Response, error) {
		resp, err := handle(ctx, msg)
		return schedule.Response{
			Reply:             resp.Reply,
			TraceID:           resp.TraceID,
			Failed:            resp.Failed,
			AwaitConfirm:      resp.AwaitConfirm,
			AwaitUserInput:    resp.AwaitUserInput,
			FinalAcceptStatus: resp.FinalAcceptStatus,
			FinalAcceptReason: resp.FinalAcceptReason,
		}, err
	}
}

func scheduleSummary(spec schedule.ScheduleSpec) string {
	return schedule.Summary(spec)
}

func runHeartbeat(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mateway heartbeat <status|run>")
	}
	a, err := app.Build("", true)
	if err != nil {
		return err
	}
	runner := heartbeat.NewRunner(a.Config)
	switch args[0] {
	case "status":
		state, path, err := runner.Status()
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Heartbeat state: %s\n", path)
		if len(state.Jobs) == 0 {
			fmt.Fprintln(out, "No heartbeat jobs have run yet.")
			return nil
		}
		for _, job := range state.Jobs {
			fmt.Fprintf(out, "- agent=%s job=%s status=%s last_run_at=%s summary=%s\n", job.AgentID, job.Job, job.Status, job.LastRunAt.Format(time.RFC3339), job.Summary)
			if strings.TrimSpace(job.LastError) != "" {
				fmt.Fprintf(out, "  error=%s\n", job.LastError)
			}
		}
		return nil
	case "run":
		opts, err := parseHeartbeatRunOptions(args[1:])
		if err != nil {
			return err
		}
		result, err := runner.Run(heartbeat.RunOptions{AgentID: opts.AgentID, Job: opts.Job})
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Heartbeat job completed: agent=%s job=%s status=%s\n", result.State.AgentID, result.State.Job, result.State.Status)
		if result.Report != nil {
			fmt.Fprintf(out, "Memory lint checked %s\n", result.Report.Root)
			if len(result.Report.Issues) == 0 {
				fmt.Fprintln(out, "No issues found.")
			} else {
				for _, issue := range result.Report.Issues {
					fmt.Fprintf(out, "- [%s] %s: %s\n", issue.Code, issue.Path, issue.Message)
				}
			}
		}
		if result.DailyReview != nil {
			fmt.Fprintf(out, "Daily review written: %s\n", result.DailyReview.Path)
			fmt.Fprintf(out, "Tasks: %d, completed: %d, open: %d, artifacts: %d, proposed inbox items: %d\n",
				result.DailyReview.TaskCount,
				result.DailyReview.Completed,
				result.DailyReview.OpenTasks,
				result.DailyReview.Artifacts,
				result.DailyReview.InboxProposed,
			)
		}
		if result.Compact != nil {
			fmt.Fprintf(out, "Recent memory compacted: archived=%d kept=%d archive=%s\n", result.Compact.Archived, result.Compact.Kept, result.Compact.ArchivedDir)
		}
		if result.Index != nil {
			fmt.Fprintf(out, "Memory index rebuilt: %s\n", result.Index.Path)
			fmt.Fprintf(out, "Entries: %d, issues: %d\n", len(result.Index.Index.Entries), result.Index.Index.IssueCount)
		}
		return nil
	default:
		return fmt.Errorf("usage: mateway heartbeat <status|run>")
	}
}

type heartbeatRunOptions struct {
	AgentID string
	Job     string
}

func parseHeartbeatRunOptions(args []string) (heartbeatRunOptions, error) {
	opts := heartbeatRunOptions{AgentID: "main", Job: heartbeat.JobMemoryLint}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--agent":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.AgentID, i = value, next
		case "--job":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.Job, i = value, next
		default:
			return opts, fmt.Errorf("unknown heartbeat run option %q", args[i])
		}
	}
	return opts, nil
}

func runMemory(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mateway memory <lint|index|list|show|propose|commit|reject>")
	}
	switch args[0] {
	case "lint":
		a, err := app.Build("", true)
		if err != nil {
			return err
		}
		report, err := memory.Lint(filepath.Join(a.Config.App.Workspace, "memory"))
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Memory lint checked %s\n", report.Root)
		if len(report.Issues) == 0 {
			fmt.Fprintln(out, "No issues found.")
			return nil
		}
		for _, issue := range report.Issues {
			fmt.Fprintf(out, "- [%s] %s: %s\n", issue.Code, issue.Path, issue.Message)
		}
		return nil
	case "index":
		a, err := app.Build("", true)
		if err != nil {
			return err
		}
		result, err := memory.NewStore(a.Config.App.Workspace).RebuildIndex(time.Now())
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Memory index written: %s\n", result.Path)
		fmt.Fprintf(out, "entries=%d issues=%d\n", len(result.Index.Entries), result.Index.IssueCount)
		return nil
	case "list":
		opts, err := parseMemoryListOptions(args[1:])
		if err != nil {
			return err
		}
		a, err := app.Build("", true)
		if err != nil {
			return err
		}
		items, err := memory.NewStore(a.Config.App.Workspace).List(memory.ListOptions{AgentID: opts.AgentID, Status: opts.Status, Area: opts.Area})
		if err != nil {
			return err
		}
		if len(items) == 0 {
			fmt.Fprintln(out, "No memory items found.")
			return nil
		}
		for _, item := range items {
			fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\n", item.ID, item.Status, item.Kind, item.Updated, item.Title)
		}
		return nil
	case "show":
		opts, err := parseMemoryShowOptions(args[1:])
		if err != nil {
			return err
		}
		a, err := app.Build("", true)
		if err != nil {
			return err
		}
		result, err := memory.NewStore(a.Config.App.Workspace).Show(opts.AgentID, opts.ID)
		if err != nil {
			return err
		}
		fmt.Fprint(out, result.Text)
		if !strings.HasSuffix(result.Text, "\n") {
			fmt.Fprintln(out)
		}
		return nil
	case "propose":
		opts, err := parseMemoryProposeOptions(args[1:])
		if err != nil {
			return err
		}
		a, err := app.Build("", true)
		if err != nil {
			return err
		}
		result, err := memory.NewStore(a.Config.App.Workspace).Propose(memory.ProposalInput{
			AgentID:    opts.AgentID,
			Scope:      opts.Scope,
			Type:       opts.Type,
			Title:      opts.Title,
			Body:       opts.Body,
			Sources:    opts.Sources,
			Tags:       opts.Tags,
			Confidence: opts.Confidence,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Memory proposal written: %s\n", result.Path)
		return nil
	case "commit":
		opts, err := parseMemoryCommitOptions(args[1:])
		if err != nil {
			return err
		}
		a, err := app.Build("", true)
		if err != nil {
			return err
		}
		result, err := memory.NewStore(a.Config.App.Workspace).Commit(memory.CommitInput{
			AgentID:  opts.AgentID,
			Proposal: opts.Proposal,
			Title:    opts.Title,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Memory committed: %s\n", result.TargetPath)
		return nil
	case "reject":
		opts, err := parseMemoryRejectOptions(args[1:])
		if err != nil {
			return err
		}
		a, err := app.Build("", true)
		if err != nil {
			return err
		}
		result, err := memory.NewStore(a.Config.App.Workspace).Reject(memory.RejectInput{
			AgentID:  opts.AgentID,
			Proposal: opts.Proposal,
			Reason:   opts.Reason,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Memory proposal rejected: %s\n", result.Path)
		return nil
	default:
		return fmt.Errorf("usage: mateway memory <lint|index|list|show|propose|commit|reject>")
	}
}

type memoryListOptions struct {
	AgentID string
	Area    string
	Status  string
}

type memoryShowOptions struct {
	AgentID string
	ID      string
}

type memoryProposeOptions struct {
	AgentID    string
	Scope      string
	Type       string
	Title      string
	Body       string
	Sources    []string
	Tags       []string
	Confidence string
}

type memoryCommitOptions struct {
	AgentID  string
	Proposal string
	Title    string
}

type memoryRejectOptions struct {
	AgentID  string
	Proposal string
	Reason   string
}

func parseMemoryListOptions(args []string) (memoryListOptions, error) {
	opts := memoryListOptions{AgentID: "main", Area: "inbox"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--agent":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.AgentID, i = value, next
		case "--area":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.Area, i = value, next
		case "--status":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.Status, i = value, next
		default:
			return opts, fmt.Errorf("unknown memory list option %q", args[i])
		}
	}
	return opts, nil
}

func parseMemoryShowOptions(args []string) (memoryShowOptions, error) {
	opts := memoryShowOptions{AgentID: "main"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--help", "-h":
			return opts, fmt.Errorf("usage: mateway memory show <id-or-path> [--agent <agent-id>]")
		case "--agent":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.AgentID, i = value, next
		case "--id":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.ID, i = value, next
		default:
			if strings.TrimSpace(opts.ID) == "" && !strings.HasPrefix(args[i], "-") {
				opts.ID = args[i]
				continue
			}
			return opts, fmt.Errorf("unknown memory show option %q", args[i])
		}
	}
	if strings.TrimSpace(opts.ID) == "" {
		return opts, fmt.Errorf("usage: mateway memory show <id-or-path>")
	}
	return opts, nil
}

func parseMemoryProposeOptions(args []string) (memoryProposeOptions, error) {
	opts := memoryProposeOptions{AgentID: "main", Scope: "agent", Type: "note", Confidence: "medium"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--agent":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.AgentID, i = value, next
		case "--scope":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.Scope, i = value, next
		case "--type":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.Type, i = value, next
		case "--title":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.Title, i = value, next
		case "--body":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.Body, i = value, next
		case "--source":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.Sources, i = append(opts.Sources, value), next
		case "--tag":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.Tags, i = append(opts.Tags, value), next
		case "--confidence":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.Confidence, i = value, next
		default:
			return opts, fmt.Errorf("unknown memory propose option %q", args[i])
		}
	}
	if strings.TrimSpace(opts.Title) == "" || strings.TrimSpace(opts.Body) == "" {
		return opts, fmt.Errorf("usage: mateway memory propose --title <title> --body <text> [--source <source>] [--scope agent|user|org]")
	}
	return opts, nil
}

func parseMemoryCommitOptions(args []string) (memoryCommitOptions, error) {
	opts := memoryCommitOptions{AgentID: "main"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--agent":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.AgentID, i = value, next
		case "--proposal":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.Proposal, i = value, next
		case "--title":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.Title, i = value, next
		default:
			if strings.TrimSpace(opts.Proposal) == "" && !strings.HasPrefix(args[i], "-") {
				opts.Proposal = args[i]
				continue
			}
			return opts, fmt.Errorf("unknown memory commit option %q", args[i])
		}
	}
	if strings.TrimSpace(opts.Proposal) == "" {
		return opts, fmt.Errorf("usage: mateway memory commit --proposal <proposal-id-or-path>")
	}
	return opts, nil
}

func parseMemoryRejectOptions(args []string) (memoryRejectOptions, error) {
	opts := memoryRejectOptions{AgentID: "main"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--agent":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.AgentID, i = value, next
		case "--proposal":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.Proposal, i = value, next
		case "--reason":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.Reason, i = value, next
		default:
			if strings.TrimSpace(opts.Proposal) == "" && !strings.HasPrefix(args[i], "-") {
				opts.Proposal = args[i]
				continue
			}
			return opts, fmt.Errorf("unknown memory reject option %q", args[i])
		}
	}
	if strings.TrimSpace(opts.Proposal) == "" {
		return opts, fmt.Errorf("usage: mateway memory reject --proposal <proposal-id-or-path>")
	}
	return opts, nil
}

func optionValue(args []string, i int) (string, int, error) {
	if i+1 >= len(args) {
		return "", i, fmt.Errorf("%s requires a value", args[i])
	}
	return args[i+1], i + 1, nil
}

type testCommandOptions struct {
	Title       string
	Message     string
	UseCase     string
	Channel     string
	SessionKey  string
	UserID      string
	ThreadID    string
	OutDir      string
	Home        string
	ProjectRoot string
}

type taskReport struct {
	Title             string
	Question          string
	Result            string
	ReplyText         string
	Failed            bool
	AwaitConfirm      bool
	AwaitUserInput    bool
	FinalAcceptStatus string
	FinalAcceptReason string
	TraceID           string
	TraceFile         string
	SessionKey        string
	Channel           string
	UserID            string
	ThreadID          string
	Home              string
	ProjectRoot       string
	GeneratedAt       time.Time
	QualityNotes      []string
	Skills            []skillEvent
	Events            []map[string]any
	Plan              any
	ToolResults       []any
}

type skillEvent struct {
	Stage  string
	Skills []map[string]any
}

func uniqueTaskSuffix(title string) string {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return time.Now().Format("20060102-150405")
	}
	if name := slugify(trimmed); name != "" && name != "task" {
		return name
	}
	return trimmed
}

func runSkill(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mateway skill <search|install|promote|list>")
	}
	a, err := app.Build("", true)
	if err != nil {
		return err
	}
	switch args[0] {
	case "search":
		if len(args) < 2 {
			return fmt.Errorf("usage: mateway skill search <query>")
		}
		query := strings.Join(args[1:], " ")
		items, err := skill.SearchCatalog(context.Background(), a.Config.App.Workspace, query, skill.CatalogSearchOptions{Limit: 8})
		if err != nil {
			return err
		}
		if len(items) == 0 {
			fmt.Fprintf(out, "No matching skills found for: %s\n", query)
			return nil
		}
		for _, item := range items {
			status := "not-installed"
			if item.Installed {
				status = "installed"
			}
			fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", item.Name, item.Source, status, item.URL)
		}
		return nil
	case "install":
		if len(args) < 2 {
			return fmt.Errorf("usage: mateway skill install <name-or-url>")
		}
		ref := strings.Join(args[1:], " ")
		result, err := skill.InstallCatalogSkill(context.Background(), a.Config.App.Workspace, ref, skill.CatalogSearchOptions{Limit: 1})
		if err != nil {
			return err
		}
		if result.AlreadyDone {
			fmt.Fprintf(out, "Skill already installed: %s\n%s\n", result.Item.Name, result.TargetPath)
			return nil
		}
		fmt.Fprintf(out, "Skill installed: %s\n%s\n", result.Item.Name, result.TargetPath)
		return nil
	case "promote":
		opts, err := parseSkillPromoteOptions(args[1:])
		if err != nil {
			return err
		}
		result, err := memory.NewStore(a.Config.App.Workspace).PromoteSkillCandidate(memory.SkillPromotionInput{
			AgentID:   opts.AgentID,
			Proposal:  opts.Proposal,
			SkillName: opts.SkillName,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Skill promoted: %s\nThis skill will be reloadable from workspace skills on the next planning turn.\n", result.TargetPath)
		return nil
	case "list":
		defs, err := skill.ListInstalled(a.Config.App.Workspace)
		if err != nil {
			return err
		}
		if len(defs) == 0 {
			fmt.Fprintln(out, "No installed skills found.")
			return nil
		}
		for _, def := range defs {
			fmt.Fprintf(out, "%s\t%s\t%s\n", def.Name, def.Stage, def.Dir)
		}
		return nil
	default:
		return fmt.Errorf("usage: mateway skill <search|install|promote|list>")
	}
}

type skillPromoteOptions struct {
	AgentID   string
	Proposal  string
	SkillName string
}

func parseSkillPromoteOptions(args []string) (skillPromoteOptions, error) {
	opts := skillPromoteOptions{AgentID: "main"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--agent":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.AgentID, i = value, next
		case "--proposal":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.Proposal, i = value, next
		case "--name":
			value, next, err := optionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.SkillName, i = value, next
		default:
			if strings.TrimSpace(opts.Proposal) == "" && !strings.HasPrefix(args[i], "-") {
				opts.Proposal = args[i]
				continue
			}
			return opts, fmt.Errorf("unknown skill promote option %q", args[i])
		}
	}
	if strings.TrimSpace(opts.Proposal) == "" {
		return opts, fmt.Errorf("usage: mateway skill promote --proposal <proposal-id-or-path> [--name <skill-name>]")
	}
	return opts, nil
}

func runEval(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mateway eval <routing>")
	}
	switch args[0] {
	case "routing":
		outDir := ""
		focus := false
		ultraFocus := false
		for i := 1; i < len(args); i++ {
			switch args[i] {
			case "--out":
				if i+1 >= len(args) {
					return fmt.Errorf("--out requires a value")
				}
				outDir = args[i+1]
				i++
			case "--focus":
				focus = true
			case "--ultra-focus":
				ultraFocus = true
			default:
				return fmt.Errorf("usage: mateway eval routing [--out <dir>] [--focus] [--ultra-focus]")
			}
		}
		a, err := app.Build("", true)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		var cases []evalpkg.RoutingCase
		if ultraFocus {
			cases = evalpkg.FirstStageUltraFocusCases()
		} else if focus {
			cases = evalpkg.FirstStageFocusCases()
		}
		summary, err := evalpkg.RunRouting(ctx, a.Model, a.Tools, "", cases)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Routing eval: %d/%d passed\n", summary.Passed, summary.Total)
		for _, result := range summary.Results {
			status := "PASS"
			if !result.Passed {
				status = "FAIL"
			}
			fmt.Fprintf(out, "%s\t%s\ttools=%s\n", status, result.Name, strings.Join(result.Tools, ","))
			for _, errText := range result.Errors {
				fmt.Fprintf(out, "  error: %s\n", errText)
			}
			for _, warning := range result.Warnings {
				fmt.Fprintf(out, "  warning: %s\n", warning)
			}
		}
		if strings.TrimSpace(outDir) != "" {
			now := time.Now().Format("2006-01-02")
			dir := filepath.Join(outDir, now)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			name := "routing-eval.md"
			if ultraFocus {
				name = "routing-eval-first-stage-ultra.md"
			} else if focus {
				name = "routing-eval-first-stage.md"
			}
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, []byte(evalpkg.RenderRoutingMarkdown(summary)), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(out, "Routing report written: %s\n", path)
		}
		if summary.Passed != summary.Total {
			return fmt.Errorf("routing eval failed: %d/%d passed", summary.Passed, summary.Total)
		}
		return nil
	default:
		return fmt.Errorf("usage: mateway eval <routing>")
	}
}

func runTest(args []string) error {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			fmt.Println(testHelpText())
			return nil
		}
	}
	opts, err := parseTestOptions(args)
	if err != nil {
		return err
	}
	a, err := app.Build(opts.Home, true)
	if err != nil {
		return err
	}
	msg := channel.InboundMessage{
		ID:         "test-" + uniqueTaskSuffix(opts.Title),
		Channel:    firstNonEmptyLocal(opts.Channel, "cli"),
		SessionKey: firstNonEmptyLocal(opts.SessionKey, "test:"+uniqueTaskSuffix(opts.Title)),
		UserID:     firstNonEmptyLocal(opts.UserID, "local"),
		ThreadID:   firstNonEmptyLocal(opts.ThreadID, "test:"+uniqueTaskSuffix(opts.Title)),
		Text:       opts.Message,
	}
	resp, err := a.Runtime.Handle(context.Background(), msg)
	if err != nil {
		return err
	}
	report, err := buildTaskReport(a, opts, msg, resp)
	if err != nil {
		return err
	}
	path, err := writeTaskReport(report, opts.OutDir)
	if err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}

func parseTestOptions(args []string) (testCommandOptions, error) {
	opts := testCommandOptions{
		Title:   "default-test-task",
		Message: "Run one complete real-model workflow test and report the result, issues found, and execution details.",
		Channel: "cli",
		Home:    "",
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--case":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("%s requires a value", arg)
			}
			preset, ok := builtinTestCase(args[i+1])
			if !ok {
				return opts, fmt.Errorf("unknown test case %q", args[i+1])
			}
			opts.UseCase = args[i+1]
			opts.Title = preset.Title
			opts.Message = preset.Message
			i++
		case "--title":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("%s requires a value", arg)
			}
			opts.Title = args[i+1]
			i++
		case "--message", "--question":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("%s requires a value", arg)
			}
			opts.Message = args[i+1]
			i++
		case "--channel":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("%s requires a value", arg)
			}
			opts.Channel = args[i+1]
			i++
		case "--session-key":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("%s requires a value", arg)
			}
			opts.SessionKey = args[i+1]
			i++
		case "--user-id":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("%s requires a value", arg)
			}
			opts.UserID = args[i+1]
			i++
		case "--thread-id":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("%s requires a value", arg)
			}
			opts.ThreadID = args[i+1]
			i++
		case "--out":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("%s requires a value", arg)
			}
			opts.OutDir = args[i+1]
			i++
		case "--home":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("%s requires a value", arg)
			}
			opts.Home = args[i+1]
			i++
		case "--project-root":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("%s requires a value", arg)
			}
			opts.ProjectRoot = args[i+1]
			i++
		case "--help", "-h":
			return opts, fmt.Errorf(testHelpText())
		default:
			return opts, fmt.Errorf("unknown test option %q", arg)
		}
	}
	return opts, nil
}

func builtinTestCase(name string) (testCommandOptions, bool) {
	switch strings.TrimSpace(strings.ToLower(name)) {
	case "tool-boundary-project":
		return testCommandOptions{
			Title:   "tool-boundary-project",
			Message: "先概览当前仓库结构，再重点总结 README.md 和 docs/开发TODO.md，最后如果有测试命令的话跑一下最小测试确认项目状态。",
			Channel: "cli",
		}, true
	case "tool-boundary-install":
		return testCommandOptions{
			Title:   "tool-boundary-install",
			Message: "帮我安装一个叫 example-cli 的 CLI，如果安装方式不明确先查官方安装方法，装完后验证可执行文件是否能正常输出版本信息。",
			Channel: "cli",
		}, true
	case "tool-boundary-schedule":
		return testCommandOptions{
			Title:   "tool-boundary-schedule",
			Message: "如果 AI 趋势收集这个任务现在可以稳定执行，就帮我设计一个每天 9 点执行的定时任务；如果当前动作无法验证，就先说明缺什么，不要直接创建。",
			Channel: "cli",
		}, true
	case "tool-boundary-url":
		return testCommandOptions{
			Title:   "tool-boundary-url",
			Message: "请直接读取这个已知页面并总结关键信息：https://example.com",
			Channel: "cli",
		}, true
	case "tool-boundary-cli-message":
		return testCommandOptions{
			Title:   "tool-boundary-cli-message",
			Message: "用本机的 larkcli 给飞书发送一条消息",
			Channel: "cli",
		}, true
	default:
		return testCommandOptions{}, false
	}
}

func buildTaskReport(a *app.App, opts testCommandOptions, msg channel.InboundMessage, resp runtimepkg.Response) (taskReport, error) {
	traceID := firstNonEmptyLocal(resp.TraceID, traceIDForMessageLocal(msg))
	report := taskReport{
		Title:             opts.Title,
		Question:          opts.Message,
		Result:            firstNonEmptyLocal(resp.Reply.Text, ""),
		ReplyText:         resp.Reply.Text,
		Failed:            resp.Failed,
		AwaitConfirm:      resp.AwaitConfirm,
		AwaitUserInput:    resp.AwaitUserInput,
		FinalAcceptStatus: resp.FinalAcceptStatus,
		FinalAcceptReason: resp.FinalAcceptReason,
		TraceID:           traceID,
		SessionKey:        msg.SessionKey,
		Channel:           msg.Channel,
		UserID:            msg.UserID,
		ThreadID:          msg.ThreadID,
		Home:              a.Config.App.Home,
		ProjectRoot:       firstNonEmptyLocal(opts.ProjectRoot, a.Runtime.ToolCtx.ProjectRoot),
		GeneratedAt:       time.Now(),
		QualityNotes:      qualityNotesForReport(opts.Message, resp),
		Skills:            collectSkillsForReport(traceID, a.Config.App.Home),
		Plan:              resp.Plan,
		ToolResults:       make([]any, 0, len(resp.Results)),
	}
	for _, result := range resp.Results {
		report.ToolResults = append(report.ToolResults, result)
	}
	report.Events = loadTraceEvents(filepath.Join(a.Config.App.Home, "trace"), traceID)
	report.TraceFile = traceFileForTime(a.Config.App.Home, report.GeneratedAt)
	return report, nil
}

func writeTaskReport(report taskReport, outDir string) (string, error) {
	if strings.TrimSpace(outDir) == "" {
		outDir = firstNonEmptyLocal(report.ProjectRoot, ".")
		outDir = filepath.Join(outDir, "testdata")
	}
	dateDir := filepath.Join(outDir, time.Now().Format("2006-01-02"))
	if err := os.MkdirAll(dateDir, 0o755); err != nil {
		return "", err
	}
	name := uniqueTaskSuffix(report.Title)
	if name == "" {
		name = "task"
	}
	path := filepath.Join(dateDir, time.Now().Format("150405")+"-"+name+".md")
	var b strings.Builder
	writeTaskReportMarkdown(&b, report)
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func writeTaskReportMarkdown(b *strings.Builder, report taskReport) {
	fmt.Fprintf(b, "# %s\n\n", report.Title)
	fmt.Fprintln(b, "## Question")
	fmt.Fprintln(b, report.Question)
	fmt.Fprintln(b)
	fmt.Fprintln(b, "## Result")
	fmt.Fprintln(b, firstNonEmptyLocal(report.ReplyText, report.Result))
	fmt.Fprintln(b)
	fmt.Fprintln(b, "## Conclusion")
	if report.Failed {
		if report.FinalAcceptStatus == "partial" {
			fmt.Fprintln(b, "The task completed partially with useful output and remaining gaps.")
		} else {
			fmt.Fprintln(b, "The task did not complete successfully.")
		}
	} else if report.AwaitConfirm {
		fmt.Fprintln(b, "The task is waiting for confirmation.")
	} else if report.AwaitUserInput {
		fmt.Fprintln(b, "The task is waiting for additional user input.")
	} else if report.FinalAcceptStatus == "partial" {
		fmt.Fprintln(b, "The task completed partially with useful output and remaining gaps.")
	} else if len(report.QualityNotes) > 0 {
		fmt.Fprintln(b, "The task mechanism completed, but the answer quality needs human review.")
	} else {
		fmt.Fprintln(b, "The task completed.")
	}
	if strings.TrimSpace(report.FinalAcceptStatus) != "" || strings.TrimSpace(report.FinalAcceptReason) != "" {
		fmt.Fprintln(b)
		fmt.Fprintln(b, "## Final Acceptance")
		if strings.TrimSpace(report.FinalAcceptStatus) != "" {
			fmt.Fprintf(b, "- status: %s\n", report.FinalAcceptStatus)
		}
		if strings.TrimSpace(report.FinalAcceptReason) != "" {
			fmt.Fprintf(b, "- reason: %s\n", report.FinalAcceptReason)
		}
	}
	if len(report.QualityNotes) > 0 {
		fmt.Fprintln(b)
		fmt.Fprintln(b, "## Quality Notes")
		for _, note := range report.QualityNotes {
			fmt.Fprintf(b, "- %s\n", note)
		}
	}
	if len(report.Skills) > 0 {
		fmt.Fprintln(b)
		fmt.Fprintln(b, "## Skills")
		for _, item := range report.Skills {
			fmt.Fprintf(b, "- stage: %s\n", item.Stage)
			for _, skillItem := range item.Skills {
				name := firstNonEmptyLocal(fmt.Sprint(skillItem["name"]))
				reason := firstNonEmptyLocal(fmt.Sprint(skillItem["reason"]))
				dir := firstNonEmptyLocal(fmt.Sprint(skillItem["dir"]))
				fmt.Fprintf(b, "  - %s\n", name)
				if reason != "" {
					fmt.Fprintf(b, "    - reason: %s\n", reason)
				}
				if dir != "" {
					fmt.Fprintf(b, "    - dir: %s\n", dir)
				}
			}
		}
	}
	fmt.Fprintln(b)
	fmt.Fprintln(b, "## Execution Metadata")
	writeTaskMetaLine(b, "trace_id", report.TraceID)
	writeTaskMetaLine(b, "session_key", report.SessionKey)
	writeTaskMetaLine(b, "channel", report.Channel)
	writeTaskMetaLine(b, "user_id", report.UserID)
	writeTaskMetaLine(b, "thread_id", report.ThreadID)
	writeTaskMetaLine(b, "home", report.Home)
	writeTaskMetaLine(b, "project_root", report.ProjectRoot)
	writeTaskMetaLine(b, "generated_at", report.GeneratedAt.Format(time.RFC3339))
	if report.TraceFile != "" {
		writeTaskMetaLine(b, "trace_file", report.TraceFile)
	}
	if data, err := json.MarshalIndent(report.Plan, "", "  "); err == nil && string(data) != "null" {
		fmt.Fprintln(b)
		fmt.Fprintln(b, "### Plan")
		fmt.Fprintln(b, "```json")
		fmt.Fprintln(b, string(data))
		fmt.Fprintln(b, "```")
	}
	if len(report.ToolResults) > 0 {
		fmt.Fprintln(b)
		fmt.Fprintln(b, "### Tool Results")
		if data, err := json.MarshalIndent(report.ToolResults, "", "  "); err == nil {
			fmt.Fprintln(b, "```json")
			fmt.Fprintln(b, string(data))
			fmt.Fprintln(b, "```")
		}
	}
	if len(report.Events) > 0 {
		fmt.Fprintln(b)
		fmt.Fprintln(b, "### Trace Events")
		for _, ev := range report.Events {
			if data, err := json.Marshal(ev); err == nil {
				fmt.Fprintf(b, "- %s\n", string(data))
			}
		}
	}
}

func writeTaskMetaLine(b *strings.Builder, key, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	fmt.Fprintf(b, "- %s: %s\n", key, value)
}

func loadTraceEvents(traceDir, traceID string) []map[string]any {
	if strings.TrimSpace(traceID) == "" {
		return nil
	}
	entries, err := os.ReadDir(traceDir)
	if err != nil {
		return nil
	}
	var paths []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "events-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		paths = append(paths, filepath.Join(traceDir, name))
	}
	sort.Strings(paths)
	var out []map[string]any
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var ev map[string]any
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				continue
			}
			if fmt.Sprint(ev["trace_id"]) == traceID {
				out = append(out, ev)
			}
		}
	}
	return out
}

func collectSkillsForReport(traceID, home string) []skillEvent {
	traceFile := traceFileForTime(home, time.Now())
	data, err := os.ReadFile(traceFile)
	if err != nil {
		return nil
	}
	var out []skillEvent
	seen := map[string]struct{}{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if fmt.Sprint(ev["trace_id"]) != traceID {
			continue
		}
		if fmt.Sprint(ev["event"]) != "runtime.skills_selected" {
			continue
		}
		stage := fmt.Sprint(ev["stage"])
		key := stage + "|" + line
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		group := skillEvent{Stage: stage}
		if raw, ok := ev["skills"].([]any); ok {
			for _, item := range raw {
				if m, ok := item.(map[string]any); ok {
					group.Skills = append(group.Skills, m)
				}
			}
		}
		out = append(out, group)
	}
	return out
}

func traceFileForTime(home string, t time.Time) string {
	if strings.TrimSpace(home) == "" {
		home = config.DefaultHome()
	}
	return filepath.Join(home, "trace", "events-"+t.Format("2006-01-02")+".jsonl")
}

func qualityNotesForReport(question string, resp runtimepkg.Response) []string {
	if resp.Failed || resp.AwaitConfirm || resp.AwaitUserInput {
		return nil
	}
	var notes []string
	reply := strings.TrimSpace(resp.Reply.Text)
	if len([]rune(reply)) < 120 {
		notes = append(notes, "The final reply is short; the mechanism may have completed without enough analytical depth.")
	}
	if len(resp.Results) == 0 {
		notes = append(notes, "No tool result was captured as evidence; review manually if the task required search, file analysis, or execution.")
	}
	if looksAnalyticalTestQuestion(question) && len(resp.Results) <= 1 {
		notes = append(notes, "The question appears to require analysis or cross-checking, but tool evidence is limited.")
	}
	if strings.Contains(strings.ToLower(question), "https://") || strings.Contains(strings.ToLower(question), "http://") {
		if len(resp.Results) > 0 && strings.TrimSpace(resp.Results[0].Tool) == "web.search" {
			notes = append(notes, "This task provided a known URL, but the first tool result used web.search. Review whether web.fetch should have been preferred.")
		}
	}
	lower := strings.ToLower(reply)
	if strings.Contains(lower, "echo") || strings.Contains(lower, "tool call") && len([]rune(reply)) < 300 {
		notes = append(notes, "The reply may contain tool-call residue or an overly formal summary; confirm it answered the original question.")
	}
	return notes
}

func looksAnalyticalTestQuestion(question string) bool {
	normalized := strings.ToLower(strings.TrimSpace(question))
	return strings.Contains(normalized, "analysis") ||
		strings.Contains(normalized, "analyze") ||
		strings.Contains(normalized, "summary") ||
		strings.Contains(normalized, "summarize") ||
		strings.Contains(normalized, "trend") ||
		strings.Contains(normalized, "evaluate") ||
		strings.Contains(normalized, "compare") ||
		strings.Contains(normalized, "review") ||
		strings.Contains(normalized, "research")
}

func slugify(text string) string {
	text = strings.TrimSpace(strings.ToLower(text))
	if text == "" {
		return ""
	}
	var b strings.Builder
	lastDash := false
	for _, r := range text {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == ' ', r == '-', r == '_', r == '.', r == '/', r == ':':
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "task"
	}
	return out
}

func testHelpText() string {
	return `usage: mateway test [options]

Run one real-model end-to-end test and write a markdown report under testdata/YYYY-MM-DD/.

Options:
  --case <name>         run a built-in real-model regression case
  --title <name>        task title used as the markdown heading and file name
  --question <text>     task question / problem statement
  --message <text>      alias of --question
  --channel <name>      message channel, default cli
  --session-key <key>   session key, default test:<title>
  --user-id <id>        user id, default local
  --thread-id <id>      thread id, default test:<title>
  --out <dir>           output root, default ./testdata
  --home <dir>          mateway home, default ~/.mateway
  --project-root <dir>  project root used by runtime

Built-in cases:
  tool-boundary-project   project.index vs file.summary vs terminal.run
  tool-boundary-install   software.search/install and verification path
  tool-boundary-schedule  verify-before-schedule-create boundary
  tool-boundary-url       known URL should bias toward web.fetch
  tool-boundary-cli-message  local CLI send flow: help -> ask missing args -> auth preflight -> send
`
}

func firstNonEmptyLocal(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func traceIDForMessageLocal(msg channel.InboundMessage) string {
	if strings.TrimSpace(msg.ID) != "" {
		return msg.Channel + "-" + msg.ID
	}
	if strings.TrimSpace(msg.SessionKey) != "" {
		return msg.SessionKey + "-" + time.Now().Format("20060102T150405.000000000")
	}
	return msg.Channel + "-" + time.Now().Format("20060102T150405.000000000")
}

func runFeishu() error {
	fmt.Fprintln(os.Stderr, "warning: 'feishu' is a compatibility shortcut; use 'mateway gateway serve'")
	return runGatewayServe()
}

func runGatewayServe() error {
	a, err := app.Build("", false)
	if err != nil {
		return err
	}
	lock, err := gateway.AcquireInstanceLock(a.Config.App.Home)
	if err != nil {
		return err
	}
	defer lock.Close()
	fmt.Fprintln(os.Stderr, "mateway instance lock:", lock.Path)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return gateway.New(a).Serve(ctx)
}

func runTrace(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mateway trace <tail|show>")
	}
	traceDir := filepath.Join(config.DefaultHome(), "trace")
	switch args[0] {
	case "tail":
		opts, err := parseTraceTailOptions(args[1:])
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return observer.TailTrace(ctx, traceDir, opts, out)
	case "show":
		opts, traceID, err := parseTraceShowOptions(args[1:])
		if err != nil {
			return err
		}
		return observer.ShowTrace(traceDir, traceID, opts, out)
	default:
		return fmt.Errorf("usage: mateway trace <tail|show>")
	}
}

func parseTraceTailOptions(args []string) (observer.TraceTailOptions, error) {
	opts := observer.TraceTailOptions{Lines: 80, Follow: true}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-n", "--lines":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("%s requires a number", args[i])
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 0 {
				return opts, fmt.Errorf("invalid line count %q", args[i+1])
			}
			opts.Lines = n
			i++
		case "--no-follow":
			opts.Follow = false
		case "--raw":
			opts.Raw = true
		default:
			return opts, fmt.Errorf("unknown trace tail option %q", args[i])
		}
	}
	return opts, nil
}

func parseTraceShowOptions(args []string) (observer.TraceShowOptions, string, error) {
	var opts observer.TraceShowOptions
	var traceID string
	for _, arg := range args {
		switch arg {
		case "--raw":
			opts.Raw = true
		default:
			if strings.HasPrefix(arg, "-") {
				return opts, "", fmt.Errorf("unknown trace show option %q", arg)
			}
			if traceID != "" {
				return opts, "", fmt.Errorf("usage: mateway trace show <trace_id>")
			}
			traceID = arg
		}
	}
	if strings.TrimSpace(traceID) == "" {
		return opts, "", fmt.Errorf("usage: mateway trace show <trace_id>")
	}
	return opts, traceID, nil
}

func printHelp() {
	fmt.Print(`mateway

Commands:
  init                   initialize ~/.mateway config, samples, docs, and default skills
  doctor                 validate config and list tools
  eval routing           run real-model planner/tool routing evaluation
  ask <message>          run one CLI task
  gateway serve          run the configured gateway in foreground; does not install autostart
  gateway start          start an already-registered OS-managed gateway service
  gateway restart        restart an already-registered OS-managed gateway service
  gateway stop           stop an already-registered OS-managed gateway service
  gateway status         show service and instance-lock status
  heartbeat status       show heartbeat job state
  heartbeat run          run one heartbeat job manually
  schedule create        create one user scheduled task
  schedule propose       write a pending user scheduled task proposal
  schedule list          list user scheduled tasks
  schedule proposals     list pending user scheduled task proposals
  schedule show <id>     print one user scheduled task
  schedule due           list user scheduled tasks due now
  schedule run-due       run due user scheduled tasks through runtime
  skill search <query>   search installable skills from priority catalogs
  skill install <ref>    install a skill into ~/.mateway/workspace/skills
  skill promote          promote a reviewed skill candidate into ~/.mateway/workspace/skills
  skill list             list installed Mateway workspace skills
  memory lint            check Markdown memory wiki health without modifying files
  memory index           rebuild JSON memory index from Markdown
  memory list            first-class direct command to list inbox or long memory items
  memory show            first-class direct command to print one memory item
  memory review          first-class direct command to inspect or write a long-memory review proposal
  memory propose         write a reviewed memory proposal into inbox
  memory commit          direct command with confirmation; commit an inbox proposal into long memory
  memory reject          direct command with confirmation; mark an inbox proposal as rejected
  trace tail             follow today's structured trace
  trace show <trace_id>  show events for one trace id

Typical binary setup:
  mateway init
  edit ~/.mateway/config/mateway.env and ~/.mateway/config/*.yaml
  mateway doctor
`)
}
