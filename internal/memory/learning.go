package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/session"
)

type LearningEvent struct {
	Home       string
	SessionKey string
	Task       session.TaskNode
	FinalText  string
	TraceID    string
	TracePath  string
	Skills     []SkillEvidence
	UserText   string
}

type LearningResult struct {
	DiaryPath      string
	ReflectionPath string
	Proposal       *Proposal
}

func RecordTaskCompletion(event LearningEvent) (LearningResult, error) {
	home := strings.TrimSpace(event.Home)
	if home == "" {
		home = ".mateway"
	}
	now := time.Now().Format(time.RFC3339)
	id := "diary_" + time.Now().Format("20060102_150405_000000")
	diaryPath := filepath.Join(home, "observe", "diary", id+".md")
	diary := renderDiary(event, now)
	if err := os.MkdirAll(filepath.Dir(diaryPath), 0o755); err != nil {
		return LearningResult{}, err
	}
	if err := os.WriteFile(diaryPath, []byte(diary), 0o644); err != nil {
		return LearningResult{}, err
	}
	result := LearningResult{DiaryPath: diaryPath}
	_ = RecordLearningEvidence(event)
	_ = RecordSkillUsage(event)
	if shouldReflect(event) {
		reflectionPath := filepath.Join(home, "observe", "reflections", "reflection_"+time.Now().Format("20060102_150405_000000")+".md")
		if err := os.MkdirAll(filepath.Dir(reflectionPath), 0o755); err != nil {
			return result, err
		}
		if err := os.WriteFile(reflectionPath, []byte(renderReflection(event, now)), 0o644); err != nil {
			return result, err
		}
		result.ReflectionPath = reflectionPath
	}
	if shouldProposeMemory(event) {
		proposal, err := ProposalStore{Home: home}.Create(CreateProposalInput{
			Type:       "experience",
			Scope:      "agent",
			Title:      proposalTitle(event),
			Body:       proposalBody(event),
			Sources:    learningSources(event),
			Confidence: "low",
		})
		if err != nil {
			return result, err
		}
		result.Proposal = &proposal
	}
	return result, nil
}

func renderReflection(event LearningEvent, now string) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("type: reflection\n")
	b.WriteString("scope: agent\n")
	b.WriteString("visibility: private\n")
	b.WriteString("status: proposed\n")
	b.WriteString("sources:\n")
	for _, source := range learningSources(event) {
		b.WriteString("  - ")
		b.WriteString(source)
		b.WriteString("\n")
	}
	b.WriteString("confidence: low\n")
	b.WriteString("created_at: ")
	b.WriteString(now)
	b.WriteString("\nupdated_at: ")
	b.WriteString(now)
	b.WriteString("\nschema_version: 1\n")
	b.WriteString("---\n\n")
	b.WriteString("# Task reflection\n\n")
	b.WriteString("Goal: ")
	b.WriteString(event.Task.Goal)
	b.WriteString("\n\nOutcome: ")
	b.WriteString(defaultString(event.Task.Status, "unknown"))
	if text := strings.TrimSpace(event.FinalText); text != "" {
		b.WriteString("\n\nFinal summary: ")
		b.WriteString(text)
	}
	b.WriteString("\n\nFailed or suspect steps:\n")
	for _, step := range event.Task.Steps {
		if step.Status != "accepted" {
			b.WriteString("- ")
			b.WriteString(step.Tool)
			b.WriteString(" ")
			b.WriteString(step.Status)
			if step.Summary != "" {
				b.WriteString(": ")
				b.WriteString(step.Summary)
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("\nLikely cause:\n")
	b.WriteString("- ")
	b.WriteString(likelyCause(event))
	b.WriteString("\n\nAlternative strategy:\n")
	b.WriteString("- ")
	b.WriteString(alternativeStrategy(event))
	b.WriteString("\n\nMissed confirmation boundary:\n")
	b.WriteString("- ")
	b.WriteString(missedConfirmationBoundary(event))
	b.WriteString("\n\nRelated tools:\n")
	for _, toolName := range relatedTools(event) {
		b.WriteString("- ")
		b.WriteString(toolName)
		b.WriteString("\n")
	}
	b.WriteString("\nRelated skills:\n")
	for _, skill := range event.Skills {
		if strings.TrimSpace(skill.Name) == "" {
			continue
		}
		b.WriteString("- ")
		b.WriteString(skill.Name)
		if skill.Path != "" {
			b.WriteString(" (")
			b.WriteString(skill.Path)
			b.WriteString(")")
		}
		b.WriteString("\n")
	}
	b.WriteString("\nSources:\n")
	for _, source := range learningSources(event) {
		b.WriteString("- ")
		b.WriteString(source)
		b.WriteString("\n")
	}
	return b.String()
}

func renderDiary(event LearningEvent, now string) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("type: diary\n")
	b.WriteString("scope: agent\n")
	b.WriteString("visibility: private\n")
	b.WriteString("status: active\n")
	b.WriteString("sources:\n")
	for _, source := range learningSources(event) {
		b.WriteString("  - ")
		b.WriteString(source)
		b.WriteString("\n")
	}
	b.WriteString("confidence: low\n")
	b.WriteString("created_at: ")
	b.WriteString(now)
	b.WriteString("\nupdated_at: ")
	b.WriteString(now)
	b.WriteString("\nschema_version: 1\n")
	b.WriteString("---\n\n")
	b.WriteString("# Task diary\n\n")
	b.WriteString("- Session: ")
	b.WriteString(event.SessionKey)
	b.WriteString("\n- Task: ")
	b.WriteString(event.Task.ID)
	b.WriteString("\n- Goal: ")
	b.WriteString(event.Task.Goal)
	b.WriteString("\n- Status: ")
	b.WriteString(event.Task.Status)
	b.WriteString("\n")
	if len(event.Task.Steps) > 0 {
		b.WriteString("\nSteps:\n")
		for _, step := range event.Task.Steps {
			b.WriteString("- ")
			b.WriteString(step.Tool)
			b.WriteString(" ")
			b.WriteString(step.Status)
			if step.Summary != "" {
				b.WriteString(": ")
				b.WriteString(step.Summary)
			}
			b.WriteString("\n")
		}
	}
	if text := strings.TrimSpace(event.FinalText); text != "" {
		b.WriteString("\nFinal reply:\n")
		b.WriteString(text)
		b.WriteString("\n")
	}
	return b.String()
}

func shouldProposeMemory(event LearningEvent) bool {
	goal := strings.ToLower(strings.TrimSpace(event.Task.Goal))
	return HasStrongMemoryCue(goal)
}

func containsAny(text string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func HasStrongMemoryCue(text string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	return containsAny(text, []string{
		"记住", "记忆", "长期", "偏好", "规则", "决定", "经验", "流程", "以后",
		"remember", "memory", "preference", "rule", "decision", "lesson", "workflow", "next time",
	})
}

func shouldReflect(event LearningEvent) bool {
	if HasUserCorrectionCue(event.UserText) || HasUserCorrectionCue(event.FinalText) {
		return true
	}
	for _, step := range event.Task.Steps {
		if step.Status != "" && step.Status != "accepted" {
			return true
		}
	}
	return false
}

func HasUserCorrectionCue(text string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	return containsAny(text, []string{
		"不是这样", "不对", "应该", "以后别", "以后不要", "改成", "纠正", "修正",
		"wrong", "incorrect", "should", "next time don't", "do not do that", "correction",
	})
}

func likelyCause(event LearningEvent) string {
	for _, step := range event.Task.Steps {
		summary := strings.ToLower(step.Summary)
		if step.Status != "accepted" && strings.TrimSpace(step.Summary) != "" {
			if strings.Contains(summary, "not found") || strings.Contains(summary, "missing") || strings.Contains(summary, "不存在") {
				return "The selected tool or path likely lacked the expected resource."
			}
			if strings.Contains(summary, "denied") || strings.Contains(summary, "permission") || strings.Contains(summary, "权限") {
				return "The action likely crossed a permission or confirmation boundary."
			}
			return "A tool step was not accepted and needs a safer alternate path."
		}
	}
	if HasUserCorrectionCue(event.UserText) || HasUserCorrectionCue(event.FinalText) {
		return "The user corrected the task behavior or expected workflow."
	}
	return "No specific cause identified."
}

func alternativeStrategy(event LearningEvent) string {
	for _, step := range event.Task.Steps {
		if step.Status == "accepted" {
			continue
		}
		switch step.Tool {
		case "file.read", "file.write":
			return "Verify the target path and workspace boundary before retrying file operations."
		case "terminal.run":
			return "Prefer a dry-run or read-only command first, then ask for confirmation before mutation."
		case "web.search", "web.fetch":
			return "Retry with fresher or more authoritative sources and cite the retrieved evidence."
		}
	}
	if HasUserCorrectionCue(event.UserText) || HasUserCorrectionCue(event.FinalText) {
		return "Turn the correction into a future preference or workflow proposal if it is reusable."
	}
	return "Review the trace and choose the smallest safer retry."
}

func missedConfirmationBoundary(event LearningEvent) string {
	for _, step := range event.Task.Steps {
		if step.Status != "accepted" && strings.Contains(strings.ToLower(step.Summary), "confirm") {
			return "A confirmation boundary may have been reached without enough user-facing context."
		}
	}
	return "None detected."
}

func relatedTools(event LearningEvent) []string {
	seen := map[string]bool{}
	var out []string
	for _, step := range event.Task.Steps {
		name := strings.TrimSpace(step.Tool)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	if len(out) == 0 {
		out = append(out, "none")
	}
	return out
}

func proposalTitle(event LearningEvent) string {
	goal := strings.TrimSpace(event.Task.Goal)
	if goal == "" {
		return "Task experience"
	}
	if len([]rune(goal)) > 48 {
		return string([]rune(goal)[:48])
	}
	return goal
}

func proposalBody(event LearningEvent) string {
	var b strings.Builder
	b.WriteString("Potential reusable experience from completed task.\n\n")
	b.WriteString("Goal: ")
	b.WriteString(event.Task.Goal)
	b.WriteString("\n")
	for _, step := range event.Task.Steps {
		if step.Status == "accepted" {
			b.WriteString(fmt.Sprintf("- Accepted tool step: %s. %s\n", step.Tool, step.Summary))
		}
	}
	if text := strings.TrimSpace(event.FinalText); text != "" {
		b.WriteString("\nOutcome: ")
		b.WriteString(text)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func learningSources(event LearningEvent) []string {
	var sources []string
	if event.TraceID != "" {
		sources = append(sources, "trace:"+event.TraceID)
	}
	if event.Task.ID != "" {
		sources = append(sources, "task:"+event.Task.ID)
	}
	if event.TracePath != "" {
		sources = append(sources, "file:"+event.TracePath)
	}
	return sources
}
