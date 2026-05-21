package runtime

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dongping/mateway/internal/session"
)

const (
	shortMemoryRecentTurnLimit = 6
	shortMemoryOpenTaskLimit   = 4
	shortMemoryArtifactLimit   = 6
	shortMemoryTextLimit       = 180
)

type shortMemorySummary struct {
	Text           string
	RecentTurns    int
	OpenTasks      int
	Artifacts      int
	ActiveTaskID   string
	SessionPresent bool
}

func buildShortMemorySummary(st session.State) shortMemorySummary {
	st = normalizeSessionForShortMemory(st)
	if !hasShortMemory(st) {
		return shortMemorySummary{}
	}
	var sections []string
	recentTurns := recentTurnsForShortMemory(st.RecentTurns, shortMemoryRecentTurnLimit)
	if len(recentTurns) > 0 {
		lines := []string{"Recent turns:"}
		for _, turn := range recentTurns {
			role := strings.TrimSpace(turn.Role)
			if role == "" {
				role = "unknown"
			}
			lines = append(lines, fmt.Sprintf("- %s: %s", role, compactText(turn.Text, shortMemoryTextLimit)))
		}
		sections = append(sections, strings.Join(lines, "\n"))
	}
	if active := session.ActiveTask(st); active != nil {
		sections = append(sections, "Active task:\n"+renderShortTask(*active))
	}
	openTasks := session.OpenTasks(st)
	if len(openTasks) > 0 {
		lines := []string{"Open tasks:"}
		count := 0
		for i := len(openTasks) - 1; i >= 0 && count < shortMemoryOpenTaskLimit; i-- {
			task := openTasks[i]
			if active := strings.TrimSpace(st.ActiveTaskID); active != "" && task.ID == active {
				continue
			}
			lines = append(lines, "- "+renderShortTask(task))
			count++
		}
		if count > 0 {
			sections = append(sections, strings.Join(lines, "\n"))
		}
	}
	artifactLines := shortArtifactLines(st, shortMemoryArtifactLimit)
	if len(artifactLines) > 0 {
		sections = append(sections, "Known artifacts:\n"+strings.Join(artifactLines, "\n"))
	}
	return shortMemorySummary{
		Text:           strings.TrimSpace(strings.Join(sections, "\n\n")),
		RecentTurns:    len(recentTurns),
		OpenTasks:      len(openTasks),
		Artifacts:      len(artifactLines),
		ActiveTaskID:   strings.TrimSpace(st.ActiveTaskID),
		SessionPresent: true,
	}
}

func normalizeSessionForShortMemory(st session.State) session.State {
	if st.Tasks == nil {
		st.Tasks = map[string]session.TaskState{}
	}
	return st
}

func hasShortMemory(st session.State) bool {
	return len(st.RecentTurns) > 0 || len(st.Tasks) > 0 || st.LastTask != nil
}

func recentTurnsForShortMemory(turns []session.Turn, limit int) []session.Turn {
	if limit <= 0 || len(turns) <= limit {
		return turns
	}
	out := make([]session.Turn, limit)
	copy(out, turns[len(turns)-limit:])
	return out
}

func renderShortTask(task session.TaskState) string {
	parts := []string{}
	if id := strings.TrimSpace(task.ID); id != "" {
		parts = append(parts, "id="+id)
	}
	if status := strings.TrimSpace(task.Status); status != "" {
		parts = append(parts, "status="+status)
	}
	if topic := strings.TrimSpace(task.Topic); topic != "" {
		parts = append(parts, "topic="+compactText(topic, 80))
	}
	if query := strings.TrimSpace(firstNonEmpty(task.ResolvedQuery, task.UserText)); query != "" {
		parts = append(parts, "query="+compactText(query, shortMemoryTextLimit))
	}
	if summary := strings.TrimSpace(task.PlanSummary); summary != "" {
		parts = append(parts, "plan="+compactText(summary, 120))
	}
	if pending := renderPendingTaskState(task); pending != "" {
		parts = append(parts, pending)
	}
	if len(task.Artifacts) > 0 {
		parts = append(parts, fmt.Sprintf("artifacts=%d", len(task.Artifacts)))
	}
	if len(parts) == 0 {
		return "id=" + firstNonEmpty(task.ID, "unknown")
	}
	return strings.Join(parts, "; ")
}

func renderPendingTaskState(task session.TaskState) string {
	if task.PendingApproval != nil {
		return "pending_approval=" + compactText(task.PendingApproval.RequestedAction, 120)
	}
	if len(task.PendingQuestions) > 0 {
		return "pending_question=" + compactText(task.PendingQuestions[0], 120)
	}
	if len(task.PendingFields) > 0 {
		names := make([]string, 0, len(task.PendingFields))
		for name := range task.PendingFields {
			names = append(names, name)
		}
		sort.Strings(names)
		return "pending_fields=" + strings.Join(names, ",")
	}
	return ""
}

func shortArtifactLines(st session.State, limit int) []string {
	if limit <= 0 {
		return nil
	}
	lines := []string{}
	for i := len(st.TaskOrder) - 1; i >= 0 && len(lines) < limit; i-- {
		task, ok := st.Tasks[st.TaskOrder[i]]
		if !ok {
			continue
		}
		for j := len(task.Artifacts) - 1; j >= 0 && len(lines) < limit; j-- {
			artifact := task.Artifacts[j]
			label := firstNonEmpty(artifact.Label, artifact.Summary, artifact.Path, artifact.SourceURL)
			target := firstNonEmpty(artifact.Path, artifact.SourceURL)
			if label == "" && target == "" {
				continue
			}
			line := fmt.Sprintf("- task=%s kind=%s", firstNonEmpty(task.ID, "unknown"), firstNonEmpty(artifact.Kind, "artifact"))
			if label != "" {
				line += " label=" + compactText(label, 100)
			}
			if target != "" && target != label {
				line += " target=" + compactText(target, 140)
			}
			lines = append(lines, line)
		}
	}
	return lines
}

func compactText(text string, limit int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if limit <= 0 || len(text) <= limit {
		return text
	}
	if limit <= 3 {
		return text[:limit]
	}
	return strings.TrimSpace(text[:limit-3]) + "..."
}
