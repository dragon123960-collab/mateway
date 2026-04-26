package harness

import (
	"context"
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
	InterruptID string         `json:"interrupt_id,omitempty"`
	SessionKey  string         `json:"session_key"`
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
		SessionKey:   pending.SessionKey,
		AgentName:    pending.AgentName,
		Mode:         pending.Mode,
		ToolName:     pending.ToolName,
		Arguments:    pending.Arguments,
		SkipApproval: true,
	}, generate)
	if err != nil {
		return "", err
	}
	return run.Result, nil
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
