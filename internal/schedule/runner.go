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
	Store         Store
	Handle        Handler
	PolicyHandler PolicyHandler
}

type Handler func(context.Context, channel.InboundMessage) (Response, error)

type PolicyHandler interface {
	WithSchedulePolicy(Task) Handler
}

type Response struct {
	Reply             channel.OutboundMessage
	TraceID           string
	Failed            bool
	AwaitConfirm      bool
	AwaitUserInput    bool
	FinalAcceptStatus string
	FinalAcceptReason string
}

type RunResult struct {
	Task                 Task
	TraceID              string
	OutputPath           string
	Failed               bool
	Error                string
	AwaitConfirm         bool
	AwaitUserInput       bool
	RuntimeAcceptStatus  string
	RuntimeAcceptReason  string
	DeliveryAcceptStatus string
	DeliveryAcceptReason string
}

type RunAcceptance struct {
	Status string
	Reason string
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
	handler := r.Handle
	if r.PolicyHandler != nil {
		handler = r.PolicyHandler.WithSchedulePolicy(task)
	} else if policyHandler, ok := any(r.Handle).(PolicyHandler); ok {
		handler = policyHandler.WithSchedulePolicy(task)
	}
	if limit := task.Limits.MaxRuntimeSeconds; limit > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(limit)*time.Second)
		defer cancel()
	}
	resp, err := handler(ctx, msg)
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
	result.AwaitConfirm = resp.AwaitConfirm
	result.AwaitUserInput = resp.AwaitUserInput
	result.RuntimeAcceptStatus = strings.TrimSpace(resp.FinalAcceptStatus)
	result.RuntimeAcceptReason = strings.TrimSpace(resp.FinalAcceptReason)
	state.TraceID = resp.TraceID
	if resp.Failed {
		state.Status = "failed"
		state.LastError = strings.TrimSpace(resp.Reply.Text)
	} else if resp.AwaitConfirm {
		result.Failed = true
		result.Error = "scheduled run is waiting for confirmation"
		state.Status = "blocked"
		state.LastError = strings.TrimSpace(resp.Reply.Text)
	} else if resp.AwaitUserInput {
		result.Failed = true
		result.Error = "scheduled run is waiting for user input"
		state.Status = "blocked"
		state.LastError = strings.TrimSpace(resp.Reply.Text)
	} else {
		state.Status = "ok"
	}
	if !result.Failed {
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
	}
	accept := AcceptRunResult(task, result)
	result.DeliveryAcceptStatus = accept.Status
	result.DeliveryAcceptReason = accept.Reason
	_ = r.Store.WriteRunState(state)
	return result
}

func AcceptRunResult(task Task, result RunResult) RunAcceptance {
	if result.Failed {
		return RunAcceptance{Status: "hard_fail", Reason: firstNonEmpty(result.Error, "scheduled run failed")}
	}
	mode := strings.TrimSpace(task.Delivery.Mode)
	if mode == "" || mode == "artifact" {
		path := strings.TrimSpace(result.OutputPath)
		if path == "" {
			return RunAcceptance{Status: "hard_fail", Reason: "scheduled run did not produce an output artifact path"}
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			return RunAcceptance{Status: "hard_fail", Reason: "scheduled run output artifact is missing"}
		}
		if strings.TrimSpace(result.TraceID) == "" {
			return RunAcceptance{Status: "usable", Reason: "scheduled run wrote an artifact but trace id is missing"}
		}
		return RunAcceptance{Status: "pass", Reason: "scheduled run produced an artifact and trace id"}
	}
	if strings.TrimSpace(result.TraceID) == "" {
		return RunAcceptance{Status: "usable", Reason: "scheduled run completed but trace id is missing"}
	}
	return RunAcceptance{Status: "pass", Reason: "scheduled run completed with trace id"}
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
		Text:       scheduledExecutionPrompt(task),
		Metadata: map[string]string{
			"source":      "schedule",
			"schedule_id": task.ID,
		},
	}
}

func scheduledExecutionPrompt(task Task) string {
	var b strings.Builder
	b.WriteString("这是一次已经触发的定时执行，不是在创建或修改定时任务。\n")
	b.WriteString("现在请直接完成本次任务内容，并输出本次执行结果。\n")
	b.WriteString("除非下面的任务说明明确要求，否则不要再次创建、修改、暂停、恢复或删除任何定时任务。\n\n")
	b.WriteString("任务说明：\n")
	b.WriteString(strings.TrimSpace(task.Prompt))
	return b.String()
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
		text = limitOutputChars(text, task.Limits.MaxOutputChars)
		content := fmt.Sprintf("# %s\n\n- task: %s\n- run_at: %s\n\n%s\n", task.Title, task.ID, now.Format(time.RFC3339), strings.TrimSpace(text))
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return "", err
		}
		return path, nil
	}
	return "", fmt.Errorf("unsupported schedule delivery mode: %s", mode)
}

func limitOutputChars(text string, max int) string {
	if max <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "\n\n[output truncated to " + fmt.Sprint(max) + " chars]"
}
