package runtime

import (
	"fmt"
	"strings"

	"github.com/dongping/mateway/internal/memory"
)

type inboxReminder struct {
	MemoryProposals int
	SkillCandidates int
}

func (l *AgentLoop) loadInboxReminder() inboxReminder {
	if strings.TrimSpace(l.runtime.Memory.Root) == "" {
		return inboxReminder{}
	}
	agentID := "main"
	if l.runtime.Config != nil {
		agentID = firstNonEmpty(l.runtime.Config.Agents.Default, agentID)
	}
	items, err := l.runtime.Memory.List(memory.ListOptions{AgentID: agentID, Area: "inbox", Status: "proposed"})
	if err != nil {
		l.runtime.Logger.Event("runtime.inbox_reminder_failed", map[string]any{
			"trace_id": l.state.traceID,
			"error":    err.Error(),
		})
		return inboxReminder{}
	}
	var reminder inboxReminder
	for _, item := range items {
		if strings.EqualFold(item.Kind, "skill_candidate") {
			reminder.SkillCandidates++
			continue
		}
		reminder.MemoryProposals++
	}
	if reminder.MemoryProposals > 0 || reminder.SkillCandidates > 0 {
		l.runtime.Logger.Event("runtime.inbox_reminder_loaded", map[string]any{
			"trace_id":         l.state.traceID,
			"memory_proposals": reminder.MemoryProposals,
			"skill_candidates": reminder.SkillCandidates,
		})
	}
	return reminder
}

func (r inboxReminder) Text() string {
	if r.MemoryProposals == 0 && r.SkillCandidates == 0 {
		return ""
	}
	parts := []string{}
	if r.MemoryProposals > 0 {
		parts = append(parts, fmt.Sprintf("%d memory proposal(s)", r.MemoryProposals))
	}
	if r.SkillCandidates > 0 {
		parts = append(parts, fmt.Sprintf("%d skill candidate(s)", r.SkillCandidates))
	}
	return "Inbox reminder: " + strings.Join(parts, " and ") + " are waiting for review. Use `mateway memory list --area inbox --status proposed` to inspect them."
}

func appendInboxReminder(text string, reminder inboxReminder) string {
	note := reminder.Text()
	if note == "" {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(text + "\n\n" + note)
}
