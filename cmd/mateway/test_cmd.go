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
	"github.com/dongping/mateway/internal/runtime"
)

func runTest(args []string) error {
	fs := flag.NewFlagSet("mateway test", flag.ContinueOnError)
	caseName := fs.String("case", "read-readme", "test case: read-readme, project-index, web-search, write-file, or custom")
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
	interactions := []testInteraction{}
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
	interactions = append(interactions, testInteraction{Message: text, Response: resp})
	state, err := rt.Store.Load(key)
	if err != nil {
		return err
	}
	fmt.Println("case:", *caseName)
	fmt.Println("session:", key)
	fmt.Println("message:", text)
	fmt.Println()
	for i, interaction := range interactions {
		if i > 0 {
			fmt.Println()
			fmt.Println("follow-up:", interaction.Message)
			fmt.Println()
		}
		printRuntimeResponse(interaction.Response)
	}
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
		path, err := writeTestRecord(*caseName, key, text, resp, state, interactions)
		if err != nil {
			return err
		}
		fmt.Println()
		fmt.Println("record:", path)
	}
	return nil
}

type testInteraction struct {
	Message  string           `json:"message"`
	Response runtime.Response `json:"response"`
}

func writeTestRecord(caseName, sessionKey, message string, resp runtime.Response, state any, interactions ...[]testInteraction) (string, error) {
	dir := filepath.Join("testdata", "runs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := time.Now().Format("20060102-150405") + "-" + sanitizeFilePart(caseName) + ".json"
	path := filepath.Join(dir, name)
	record := map[string]any{
		"case":       caseName,
		"session":    sessionKey,
		"message":    message,
		"reply":      resp.Reply,
		"follow_ups": resp.FollowUps,
		"failed":     resp.Failed,
		"trace_id":   resp.TraceID,
		"trace_path": resp.TracePath,
		"state":      state,
		"created_at": time.Now().Format(time.RFC3339),
	}
	if len(interactions) > 0 {
		record["interactions"] = interactions[0]
	}
	data, err := json.MarshalIndent(record, "", "  ")
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
	case "write-file":
		home := config.DefaultHome()
		if len(cfg) > 0 && cfg[0] != nil && strings.TrimSpace(cfg[0].App.Home) != "" {
			home = cfg[0].App.Home
		}
		return "/write " + filepath.Join(home, "tmp", "mateway-test-write.txt") + " hello write", nil
	case "custom":
		return "", fmt.Errorf("custom case requires --message")
	default:
		return "", fmt.Errorf("unknown test case %q", name)
	}
}
