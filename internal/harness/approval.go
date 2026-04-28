package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type ApprovalPolicy struct {
	RequireRiskyTools     bool
	RequireScheduleChange bool
}

type PendingApproval struct {
	ID          string         `json:"id"`
	RunID       string         `json:"run_id,omitempty"`
	TaskID      string         `json:"task_id,omitempty"`
	InterruptID string         `json:"interrupt_id,omitempty"`
	SessionKey  string         `json:"session_key"`
	ThreadID    string         `json:"thread_id,omitempty"`
	UserID      string         `json:"user_id,omitempty"`
	Channel     string         `json:"channel,omitempty"`
	AgentName   string         `json:"agent_name"`
	ToolName    string         `json:"tool_name"`
	Arguments   map[string]any `json:"arguments,omitempty"`
	Mode        string         `json:"mode"`
	CreatedAt   time.Time      `json:"created_at"`
}

func (h *Harness) ReviewPending(ctx context.Context, sessionKey, approvalID string, approve bool, generate Generator) (string, error) {
	sessionKey = strings.TrimSpace(sessionKey)
	approvalID = strings.TrimSpace(approvalID)
	h.approvalMu.Lock()
	pending, ok := h.takePendingLocked(sessionKey, approvalID)
	h.approvalMu.Unlock()
	if !ok {
		return "", fmt.Errorf("no pending approval for this session")
	}
	if !approve {
		reply := fmt.Sprintf("已拒绝 `%s`。", pending.ToolName)
		h.markApprovalReviewed(pending.RunID, pending.ID, false, reply)
		return reply, nil
	}
	h.markApprovalReviewed(pending.RunID, pending.ID, true, "approved for execution")
	if h.EnableEino && pending.Mode == "chat" {
		run, err := h.resumeEinoRun(ctx, pending)
		if err != nil {
			return "", err
		}
		return run.Result, nil
	}
	run, err := h.Start(ctx, Request{
		SessionKey: pending.SessionKey,
		ThreadID:   pending.ThreadID,
		UserID:     pending.UserID,
		Channel:    pending.Channel,
		AgentName:  pending.AgentName,
		Mode:       pending.Mode,
		ToolName:   pending.ToolName,
		Arguments: mergeArguments(pending.Arguments, map[string]any{
			"task_id":   pending.TaskID,
			"task_kind": "approval_resume",
		}),
		SkipApproval: true,
	}, generate)
	if err != nil {
		return "", err
	}
	return run.Result, nil
}

func FormatPendingApproval(p PendingApproval) string {
	lines := []string{
		fmt.Sprintf("工具 `%s` 需要批准。", firstNonEmpty(strings.TrimSpace(p.ToolName), "unknown")),
		"危险点：" + ApprovalRiskSummary(p),
	}
	if details := ApprovalArgumentSummary(p); details != "" {
		lines = append(lines, "细节："+details)
	}
	lines = append(lines, "如果同意，直接回复“同意”“继续执行”“可以”；如果不同意，回复“不同意”“取消”“先不要”即可。也可以发送 `/approvals` 查看待批。")
	return strings.Join(lines, "\n")
}

func (h *Harness) savePendingApproval(p PendingApproval) {
	h.approvalMu.Lock()
	defer h.approvalMu.Unlock()
	if h.pendingApprovalsBySession == nil {
		h.pendingApprovalsBySession = make(map[string][]PendingApproval)
	}
	if h.pendingApprovalsByID == nil {
		h.pendingApprovalsByID = make(map[string]PendingApproval)
	}
	key := strings.TrimSpace(p.SessionKey)
	h.pendingApprovalsBySession[key] = append(h.pendingApprovalsBySession[key], p)
	h.pendingApprovalsByID[p.ID] = p
}

func (h *Harness) pendingApproval(sessionKey string) (PendingApproval, bool) {
	h.approvalMu.RLock()
	defer h.approvalMu.RUnlock()
	items := h.pendingApprovalsBySession[strings.TrimSpace(sessionKey)]
	if len(items) == 0 {
		return PendingApproval{}, false
	}
	item := items[len(items)-1]
	return item, true
}

func (h *Harness) ListPending(sessionKey string) []PendingApproval {
	h.approvalMu.RLock()
	defer h.approvalMu.RUnlock()
	items := append([]PendingApproval(nil), h.pendingApprovalsBySession[strings.TrimSpace(sessionKey)]...)
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
	return items
}

func (h *Harness) takePendingLocked(sessionKey, approvalID string) (PendingApproval, bool) {
	if approvalID != "" {
		item, ok := h.pendingApprovalsByID[approvalID]
		if !ok || (sessionKey != "" && item.SessionKey != sessionKey) {
			return PendingApproval{}, false
		}
		delete(h.pendingApprovalsByID, approvalID)
		h.removePendingFromSessionLocked(item.SessionKey, approvalID)
		return item, true
	}
	items := h.pendingApprovalsBySession[sessionKey]
	if len(items) == 0 {
		return PendingApproval{}, false
	}
	item := items[len(items)-1]
	h.pendingApprovalsBySession[sessionKey] = append([]PendingApproval(nil), items[:len(items)-1]...)
	delete(h.pendingApprovalsByID, item.ID)
	return item, true
}

type approvalState struct {
	approvalMu                sync.RWMutex
	pendingApprovalsBySession map[string][]PendingApproval
	pendingApprovalsByID      map[string]PendingApproval
}

func (h *Harness) removePendingFromSessionLocked(sessionKey, approvalID string) {
	items := h.pendingApprovalsBySession[strings.TrimSpace(sessionKey)]
	if len(items) == 0 {
		return
	}
	out := items[:0]
	for _, item := range items {
		if item.ID != approvalID {
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		delete(h.pendingApprovalsBySession, strings.TrimSpace(sessionKey))
		return
	}
	h.pendingApprovalsBySession[strings.TrimSpace(sessionKey)] = append([]PendingApproval(nil), out...)
}

func (h *Harness) clearPendingApprovals(sessionKey string) int {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return 0
	}
	h.approvalMu.Lock()
	defer h.approvalMu.Unlock()
	items := h.pendingApprovalsBySession[sessionKey]
	if len(items) == 0 {
		return 0
	}
	for _, item := range items {
		delete(h.pendingApprovalsByID, item.ID)
	}
	delete(h.pendingApprovalsBySession, sessionKey)
	return len(items)
}

func ApprovalRiskSummary(p PendingApproval) string {
	switch strings.TrimSpace(p.ToolName) {
	case "schedule_create":
		return "这会创建一个后续自动执行的定时任务，可能在你不在场时持续触发。"
	case "schedule_update":
		return "这会修改一个已有的定时任务，后续自动执行的时间、内容或目标可能发生变化。"
	case "schedule_enable":
		return "这会重新启用一个定时任务，使其恢复自动执行。"
	case "schedule_disable":
		return "这会暂停一个已有的定时任务，影响后续自动执行。"
	case "schedule_remove":
		return "这会删除一个已有的定时任务，后续将不再自动执行。"
	case "spawn":
		return "这会启动子 agent，子 agent 可能继续调用其他工具并产生额外操作。"
	default:
		return "这是一个高风险操作，可能修改状态、触发自动执行，或调用更多工具。"
	}
}

func approvalContextSummary(p PendingApproval) string {
	args := ApprovalArgumentSummary(p)
	if args == "" {
		return ""
	}
	return args
}

func ApprovalArgumentSummary(p PendingApproval) string {
	return compactApprovalArguments(p.Arguments)
}

func compactApprovalArguments(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	keys := []string{"name", "new_name", "kind", "expr", "cron", "interval_minutes", "minutes", "tz", "timezone", "prompt", "user_text", "agent_name", "tool_name"}
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		if value, ok := args[key]; ok && value != nil {
			text := strings.TrimSpace(fmt.Sprint(value))
			if text != "" {
				parts = append(parts, fmt.Sprintf("%s=%s", key, trimApprovalValue(text, 120)))
			}
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "；")
	}
	data, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	return trimApprovalValue(string(data), 180)
}

func trimApprovalValue(value string, limit int) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\n", " "))
	if limit > 0 && len([]rune(value)) > limit {
		return string([]rune(value)[:limit]) + "..."
	}
	return value
}
