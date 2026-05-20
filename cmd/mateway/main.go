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
	"github.com/dongping/mateway/internal/gateway"
	"github.com/dongping/mateway/internal/observer"
	runtimepkg "github.com/dongping/mateway/internal/runtime"
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
	default:
		printHelp()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

type testCommandOptions struct {
	Title       string
	Message     string
	Channel     string
	SessionKey  string
	UserID      string
	ThreadID    string
	OutDir      string
	Home        string
	ProjectRoot string
}

type taskReport struct {
	Title          string
	Question       string
	Result         string
	ReplyText      string
	Failed         bool
	AwaitConfirm   bool
	AwaitUserInput bool
	TraceID        string
	TraceFile      string
	SessionKey     string
	Channel        string
	UserID         string
	ThreadID       string
	Home           string
	ProjectRoot    string
	GeneratedAt    time.Time
	QualityNotes   []string
	Skills         []skillEvent
	Events         []map[string]any
	Plan           any
	ToolResults    []any
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
		Title:   "默认测试任务",
		Message: "请执行一次完整的真实模型流程测试，并返回你发现的问题、结果和执行过程。",
		Channel: "cli",
		Home:    "",
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
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

func buildTaskReport(a *app.App, opts testCommandOptions, msg channel.InboundMessage, resp runtimepkg.Response) (taskReport, error) {
	traceID := firstNonEmptyLocal(resp.TraceID, traceIDForMessageLocal(msg))
	report := taskReport{
		Title:          opts.Title,
		Question:       opts.Message,
		Result:         firstNonEmptyLocal(resp.Reply.Text, ""),
		ReplyText:      resp.Reply.Text,
		Failed:         resp.Failed,
		AwaitConfirm:   resp.AwaitConfirm,
		AwaitUserInput: resp.AwaitUserInput,
		TraceID:        traceID,
		SessionKey:     msg.SessionKey,
		Channel:        msg.Channel,
		UserID:         msg.UserID,
		ThreadID:       msg.ThreadID,
		Home:           a.Config.App.Home,
		ProjectRoot:    firstNonEmptyLocal(opts.ProjectRoot, a.Runtime.ToolCtx.ProjectRoot),
		GeneratedAt:    time.Now(),
		QualityNotes:   qualityNotesForReport(opts.Message, resp),
		Skills:         collectSkillsForReport(traceID, a.Config.App.Home),
		Plan:           resp.Plan,
		ToolResults:    make([]any, 0, len(resp.Results)),
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
	fmt.Fprintln(b, "## 问题")
	fmt.Fprintln(b, report.Question)
	fmt.Fprintln(b)
	fmt.Fprintln(b, "## 结果")
	fmt.Fprintln(b, firstNonEmptyLocal(report.ReplyText, report.Result))
	fmt.Fprintln(b)
	fmt.Fprintln(b, "## 结论")
	if report.Failed {
		fmt.Fprintln(b, "任务未完全成功。")
	} else if report.AwaitConfirm {
		fmt.Fprintln(b, "任务已进入待确认状态。")
	} else if report.AwaitUserInput {
		fmt.Fprintln(b, "任务已进入待补充信息状态。")
	} else if len(report.QualityNotes) > 0 {
		fmt.Fprintln(b, "任务机制已完成，但答案质量需要人工复核。")
	} else {
		fmt.Fprintln(b, "任务已完成。")
	}
	if len(report.QualityNotes) > 0 {
		fmt.Fprintln(b)
		fmt.Fprintln(b, "## 质量提示")
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
	fmt.Fprintln(b, "## 执行过程与参数")
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
		notes = append(notes, "最终回复较短，可能只是机制跑通，未必覆盖了足够分析深度。")
	}
	if len(resp.Results) == 0 {
		notes = append(notes, "本次没有工具结果作为证据，若任务要求检索、文件分析或执行操作，需要人工确认是否足够。")
	}
	if looksAnalyticalTestQuestion(question) && len(resp.Results) <= 1 {
		notes = append(notes, "问题看起来需要分析或交叉验证，但工具证据较少，建议人工复核结论质量。")
	}
	lower := strings.ToLower(reply)
	if strings.Contains(lower, "echo") || strings.Contains(reply, "工具调用") && len([]rune(reply)) < 300 {
		notes = append(notes, "回复可能偏工具痕迹或形式化总结，建议确认是否真正回答了原问题。")
	}
	return notes
}

func looksAnalyticalTestQuestion(question string) bool {
	normalized := strings.ToLower(strings.TrimSpace(question))
	return strings.Contains(normalized, "分析") ||
		strings.Contains(normalized, "总结") ||
		strings.Contains(normalized, "趋势") ||
		strings.Contains(normalized, "评估") ||
		strings.Contains(normalized, "对比") ||
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
  ask <message>          run one CLI task
  gateway serve          run the configured gateway in foreground
  gateway start          start OS-managed gateway service
  gateway restart        restart OS-managed gateway service
  gateway stop           stop OS-managed gateway service
  gateway status         show service and instance-lock status
  trace tail             follow today's structured trace
  trace show <trace_id>  show events for one trace id

Typical binary setup:
  mateway init
  edit ~/.mateway/config/mateway.env and ~/.mateway/config/*.yaml
  mateway doctor
`)
}
