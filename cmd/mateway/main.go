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
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/gateway"
	"github.com/dongping/mateway/internal/runtime"
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
  mateway doctor
  mateway gateway <serve|start|restart|stop|status>`)
}
