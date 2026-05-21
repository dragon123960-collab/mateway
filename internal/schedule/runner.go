package schedule

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/channel"
)

type Runner struct {
	Store  Store
	Handle Handler
}

type Handler func(context.Context, channel.InboundMessage) (Response, error)

type Response struct {
	Reply   channel.OutboundMessage
	TraceID string
	Failed  bool
}

type RunResult struct {
	Task       Task
	TraceID    string
	OutputPath string
	Failed     bool
	Error      string
}

func (r Runner) RunDue(ctx context.Context, now time.Time) ([]RunResult, error) {
	tasks, err := r.Store.Due(now)
	if err != nil {
		return nil, err
	}
	var results []RunResult
	for _, task := range tasks {
		result := r.RunTask(ctx, task, now)
		results = append(results, result)
	}
	return results, nil
}

func (r Runner) RunTask(ctx context.Context, task Task, now time.Time) RunResult {
	if now.IsZero() {
		now = time.Now()
	}
	msg := scheduledMessage(task, now)
	if r.Handle == nil {
		return RunResult{Task: task, Failed: true, Error: "schedule runtime handler is required"}
	}
	resp, err := r.Handle(ctx, msg)
	result := RunResult{Task: task}
	state := RunState{TaskID: task.ID, LastRunAt: now}
	if err != nil {
		result.Failed = true
		result.Error = err.Error()
		state.Status = "failed"
		state.LastError = err.Error()
		_ = r.Store.WriteRunState(state)
		return result
	}
	result.TraceID = resp.TraceID
	result.Failed = resp.Failed
	state.TraceID = resp.TraceID
	if resp.Failed {
		state.Status = "failed"
		state.LastError = strings.TrimSpace(resp.Reply.Text)
	} else {
		state.Status = "ok"
	}
	outputPath, writeErr := r.writeOutput(task, now, resp.Reply.Text)
	if writeErr != nil {
		result.Failed = true
		result.Error = writeErr.Error()
		state.Status = "failed"
		state.LastError = writeErr.Error()
	} else {
		result.OutputPath = outputPath
		state.Output = outputPath
	}
	_ = r.Store.WriteRunState(state)
	return result
}

func scheduledMessage(task Task, now time.Time) channel.InboundMessage {
	channelName := firstNonEmpty(task.Owner.Channel, "schedule")
	threadID := firstNonEmpty(task.Owner.ThreadID, "schedule:"+task.ID)
	userID := firstNonEmpty(task.Owner.UserID, "schedule")
	return channel.InboundMessage{
		ID:         "schedule-" + task.ID + "-" + now.Format("20060102T150405"),
		Channel:    channelName,
		ThreadID:   threadID,
		UserID:     userID,
		SessionKey: "schedule:" + task.ID,
		Text:       task.Prompt,
		Metadata: map[string]string{
			"source":      "schedule",
			"schedule_id": task.ID,
		},
	}
}

func (r Runner) writeOutput(task Task, now time.Time, text string) (string, error) {
	mode := strings.TrimSpace(task.Delivery.Mode)
	if mode == "" || mode == "artifact" {
		path := strings.TrimSpace(task.Delivery.Path)
		if path == "" {
			path = filepath.Join(r.Store.Home, "workspace", "scheduled", task.ID, now.Format("2006-01-02")+".md")
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(r.Store.Home, path)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		content := fmt.Sprintf("# %s\n\n- task: %s\n- run_at: %s\n\n%s\n", task.Title, task.ID, now.Format(time.RFC3339), strings.TrimSpace(text))
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return "", err
		}
		return path, nil
	}
	return "", fmt.Errorf("unsupported schedule delivery mode: %s", mode)
}
