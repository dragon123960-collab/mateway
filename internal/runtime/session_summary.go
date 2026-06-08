package runtime

import (
	"fmt"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/session"
)

const (
	sessionSummaryMaxItems = 12
	sessionSummaryMaxText  = 2400
)

func updateSessionSummary(state *session.State, taskID, finalText, status string, trace *traceRecorder) {
	if state == nil {
		return
	}
	task := state.TaskByID(taskID)
	if task == nil {
		return
	}
	summary := state.Summary
	taskLine := compactSessionSummaryLine(fmt.Sprintf("%s: %s", status, firstNonEmpty(task.Summary, task.Goal, finalText)), 220)
	if taskLine != "" {
		summary.Tasks = prependUniqueLimited(summary.Tasks, taskLine, sessionSummaryMaxItems)
	}
	if strings.TrimSpace(finalText) != "" {
		summary.Text = mergeSummaryText(summary.Text, finalText)
	}
	for _, step := range task.Steps {
		if !step.Accepted || strings.TrimSpace(step.Summary) == "" {
			continue
		}
		evidence := compactSessionSummaryLine(step.Tool+": "+step.Summary, 220)
		summary.Evidence = prependUniqueLimited(summary.Evidence, evidence, sessionSummaryMaxItems)
	}
	if status == "await_user_input" {
		open := compactSessionSummaryLine(firstNonEmpty(task.Summary, finalText, task.Goal), 220)
		summary.OpenItems = prependUniqueLimited(summary.OpenItems, open, sessionSummaryMaxItems)
	} else if status == "completed" {
		summary.OpenItems = removeMatchingSummaryItem(summary.OpenItems, task.Goal)
	}
	summary.Text = compactSessionSummaryText(summary)
	summary.UpdatedAt = time.Now()
	state.Summary = summary
	_ = trace.write(map[string]any{
		"type":       "session_summary_updated",
		"task_id":    taskID,
		"status":     status,
		"tasks":      len(summary.Tasks),
		"open_items": len(summary.OpenItems),
		"evidence":   len(summary.Evidence),
		"text_chars": len(summary.Text),
		"updated_at": summary.UpdatedAt.Format(time.RFC3339),
	})
}

func renderSessionSummaryContext(summary session.SessionSummary) string {
	if strings.TrimSpace(summary.Text) == "" && len(summary.Tasks) == 0 && len(summary.OpenItems) == 0 && len(summary.Evidence) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Session summary:\n")
	if text := strings.TrimSpace(summary.Text); text != "" {
		b.WriteString(text)
		b.WriteString("\n")
	}
	if len(summary.OpenItems) > 0 {
		b.WriteString("Open items:\n")
		for _, item := range summary.OpenItems {
			b.WriteString("- ")
			b.WriteString(item)
			b.WriteString("\n")
		}
	}
	if len(summary.Tasks) > 0 {
		b.WriteString("Recent task outcomes:\n")
		for _, item := range summary.Tasks {
			b.WriteString("- ")
			b.WriteString(item)
			b.WriteString("\n")
		}
	}
	if len(summary.Evidence) > 0 {
		b.WriteString("Recent evidence:\n")
		for _, item := range summary.Evidence {
			b.WriteString("- ")
			b.WriteString(item)
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
}

func mergeSummaryText(current, finalText string) string {
	finalText = compactSessionSummaryLine(finalText, 260)
	if finalText == "" {
		return strings.TrimSpace(current)
	}
	lines := strings.Split(strings.TrimSpace(current), "\n")
	lines = prependUniqueLimited(lines, finalText, 8)
	return strings.Join(lines, "\n")
}

func compactSessionSummaryText(summary session.SessionSummary) string {
	text := strings.TrimSpace(summary.Text)
	if len([]rune(text)) <= sessionSummaryMaxText {
		return text
	}
	runes := []rune(text)
	return string(runes[:sessionSummaryMaxText])
}

func compactSessionSummaryLine(text string, limit int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

func prependUniqueLimited(values []string, value string, limit int) []string {
	value = strings.TrimSpace(value)
	if value == "" || limit <= 0 {
		return values
	}
	out := []string{value}
	seen := map[string]bool{strings.ToLower(value): true}
	for _, item := range values {
		item = strings.TrimSpace(item)
		key := strings.ToLower(item)
		if item == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, item)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func removeMatchingSummaryItem(values []string, needle string) []string {
	needle = strings.ToLower(strings.TrimSpace(needle))
	if needle == "" {
		return values
	}
	var out []string
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), needle) {
			continue
		}
		out = append(out, value)
	}
	return out
}
