package runtime

import (
	"context"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/memory"
	"github.com/dongping/mateway/internal/session"
)

func (rt Runtime) memoryProposalNudgeOptions(msg channel.InboundMessage) memory.ProposalNudgeOptions {
	options := memory.ProposalNudgeOptions{
		Channel:      msg.Channel,
		Channels:     []string{"cli"},
		Interval:     24 * time.Hour,
		MaxProposals: 3,
	}
	if rt.Config == nil {
		return options
	}
	cfg := rt.Config.Memory.ProposalNudge
	if !cfg.EnabledValue() {
		if cfg.Enabled == nil && strings.TrimSpace(cfg.Interval) == "" && len(cfg.Channels) == 0 && cfg.MaxProposals == 0 {
			return options
		}
		options.Channels = nil
		options.Channels = []string{"__disabled__"}
		return options
	}
	if len(cfg.Channels) > 0 {
		options.Channels = cfg.Channels
	}
	if parsed, err := time.ParseDuration(strings.TrimSpace(cfg.Interval)); err == nil && parsed > 0 {
		options.Interval = parsed
	}
	if cfg.MaxProposals > 0 {
		options.MaxProposals = cfg.MaxProposals
	}
	return options
}

func proposalID(proposal *memory.Proposal) string {
	if proposal == nil {
		return ""
	}
	return proposal.ID
}

func renderMemoryProposalReview(cfg *config.Root, msg channel.InboundMessage, proposal memory.Proposal) string {
	var b strings.Builder
	b.WriteString(runtimeText(cfg, msg, "memory.proposal_review.header", nil))
	b.WriteString(proposal.ID)
	b.WriteString(" ")
	b.WriteString(strings.TrimSpace(proposal.Title))
	b.WriteString(runtimeText(cfg, msg, "memory.proposal_review.type", nil))
	b.WriteString(defaultText(proposal.Type, "experience"))
	b.WriteString(" / ")
	b.WriteString(defaultText(proposal.Scope, "agent"))
	if strings.TrimSpace(proposal.Confidence) != "" {
		b.WriteString(runtimeText(cfg, msg, "memory.proposal_review.confidence", nil))
		b.WriteString(strings.TrimSpace(proposal.Confidence))
	}
	if summary := proposalSummary(proposal); summary != "" {
		b.WriteString(runtimeText(cfg, msg, "memory.proposal_review.summary", nil))
		b.WriteString(summary)
	}
	if len(proposal.Sources) > 0 {
		b.WriteString(runtimeText(cfg, msg, "memory.proposal_review.sources", nil))
		b.WriteString(summarize(strings.Join(proposal.Sources, ", ")))
	}
	values := textValues("proposal_id", proposal.ID)
	b.WriteString(runtimeText(cfg, msg, "memory.proposal_review.show", values))
	b.WriteString(runtimeText(cfg, msg, "memory.proposal_review.commit", values))
	b.WriteString(runtimeText(cfg, msg, "memory.proposal_review.reject", values))
	b.WriteString(runtimeText(cfg, msg, "memory.proposal_review.reply", nil))
	return b.String()
}

func proposalSummary(proposal memory.Proposal) string {
	body := strings.TrimSpace(proposal.Body)
	body = strings.TrimPrefix(body, "# "+strings.TrimSpace(proposal.Title))
	body = strings.TrimSpace(body)
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "-"))
		if line != "" && !strings.HasPrefix(line, "#") {
			return summarize(line)
		}
	}
	return ""
}

func (rt Runtime) handlePending(ctx context.Context, state *session.State, msg channel.InboundMessage, trace *traceRecorder) (Response, bool, error) {
	if state.Pending == nil {
		return Response{}, false, nil
	}
	_ = trace.write(map[string]any{
		"type":         "continuation_decision",
		"action":       ActionAnswerPending,
		"task_id":      state.Pending.TaskID,
		"reason":       "pending action intercepted before state machine",
		"pending_kind": state.Pending.Kind,
	})
	if state.Pending.Kind == session.PendingKindTaskPlanConfirm {
		return rt.handleTaskPlanConfirm(ctx, state, msg, trace)
	}
	if state.Pending.Kind == session.PendingKindHumanConfirm || state.Pending.Kind == session.PendingKindHumanReview {
		return rt.handleGraphHumanPending(state, msg, trace)
	}
	if state.Pending.Kind != session.PendingKindMemoryProposalReview {
		_ = trace.write(map[string]any{"type": "pending_discarded", "pending_kind": state.Pending.Kind, "task_id": state.Pending.TaskID})
		state.Pending = nil
		if err := rt.saveState(state, trace); err != nil {
			return Response{}, true, err
		}
		return Response{}, false, nil
	}
	action, ok := parseNumericMemoryProposalReviewAction(msg.Text)
	if !ok {
		_ = trace.write(map[string]any{"type": "pending_control_invalid_reply", "task_id": state.Pending.TaskID, "pending_kind": "memory_proposal_review"})
		resp := rt.reply(msg, "Please reply with 1 to save this memory proposal or 2 to ignore it. To start a separate task, send /new first.", channel.StyleInputRequired)
		resp.TraceID = trace.id
		resp.TracePath = trace.path
		return resp, true, nil
	}
	taskID := state.Pending.TaskID
	proposalID := state.Pending.ProposalID
	state.Pending = nil
	_ = trace.write(map[string]any{"type": "pending_control_executed", "task_id": taskID, "pending_kind": "memory_proposal_review", "command": action})
	store := memory.ProposalStore{Home: rt.home(), MemoryRoot: memoryRootForConfig(rt.Config)}
	if action == "commit" {
		proposal, target, err := store.Commit(proposalID)
		if err != nil {
			if saveErr := rt.saveState(state, trace); saveErr != nil {
				return Response{}, true, saveErr
			}
			return rt.reply(msg, runtimeText(rt.Config, msg, "memory.commit.error", textValues("error", err.Error())), channel.StyleError), true, nil
		}
		_ = trace.write(map[string]any{"type": "memory_proposal_review_committed", "proposal_id": proposal.ID, "target": target})
		if err := rt.saveState(state, trace); err != nil {
			return Response{}, true, err
		}
		return rt.reply(msg, runtimeText(rt.Config, msg, "memory.commit.done", textValues("target", target)), channel.StyleCompleted), true, nil
	}
	proposal, err := store.Reject(proposalID, "user selected numeric reject from conversation")
	if err != nil {
		if saveErr := rt.saveState(state, trace); saveErr != nil {
			return Response{}, true, saveErr
		}
		return rt.reply(msg, runtimeText(rt.Config, msg, "memory.reject.error", textValues("error", err.Error())), channel.StyleError), true, nil
	}
	_ = trace.write(map[string]any{"type": "memory_proposal_review_rejected", "proposal_id": proposal.ID})
	if err := rt.saveState(state, trace); err != nil {
		return Response{}, true, err
	}
	return rt.reply(msg, runtimeText(rt.Config, msg, "memory.reject.done", nil), channel.StyleCompleted), true, nil
}

func (rt Runtime) handleGraphHumanPending(state *session.State, msg channel.InboundMessage, trace *traceRecorder) (Response, bool, error) {
	taskID := state.Pending.TaskID
	nodeID := state.Pending.NodeID
	kind := state.Pending.Kind

	task := state.TaskByID(taskID)
	if task == nil || task.Graph == nil {
		state.Pending = nil
		return Response{}, false, nil
	}

	node := task.Graph.NodeByID(nodeID)
	if node == nil {
		state.Pending = nil
		return Response{}, false, nil
	}

	userResponse := strings.TrimSpace(msg.Text)
	action, ok := parseNumericHumanPendingAction(userResponse)
	if !ok {
		_ = trace.write(map[string]any{
			"type":          "pending_control_invalid_reply",
			"task_id":       taskID,
			"graph_id":      task.Graph.ID,
			"node_id":       nodeID,
			"pending_kind":  kind,
			"user_response": userResponse,
		})
		resp := rt.reply(msg, "Please reply with 1 to confirm and continue, or 2 to cancel and block this task.", channel.StyleInputRequired)
		resp.TraceID = traceID(trace)
		resp.TracePath = tracePath(trace)
		return resp, true, nil
	}
	isConfirm := action == "confirm"

	_ = trace.write(map[string]any{
		"type":          "graph_human_pending_resolved",
		"graph_id":      task.Graph.ID,
		"node_id":       nodeID,
		"kind":          kind,
		"command":       action,
		"confirmed":     isConfirm,
		"user_response": userResponse,
	})

	if isConfirm {
		node.Status = session.NodeStatusCompleted
		node.ResultSummary = userResponse
		node.Acceptance.Verified = true
		node.VerifiedAt = time.Now()
	} else {
		node.Status = session.NodeStatusBlocked
		node.FailureReason = userResponse
	}
	node.UpdatedAt = time.Now()

	state.Pending = nil
	if err := rt.saveState(state, trace); err != nil {
		return Response{}, true, err
	}

	return Response{}, false, nil
}

func parseNumericHumanPendingAction(text string) (string, bool) {
	switch strings.TrimSpace(text) {
	case "1":
		return "confirm", true
	case "2":
		return "cancel", true
	default:
		return "", false
	}
}

func parseNumericMemoryProposalReviewAction(text string) (string, bool) {
	switch strings.TrimSpace(text) {
	case "1":
		return "commit", true
	case "2":
		return "reject", true
	default:
		return "", false
	}
}
