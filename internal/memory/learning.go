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
	b.WriteString("\n\nNon-accepted steps:\n")
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
	if strings.Contains(event.Task.Goal, "记住") || strings.Contains(event.Task.Goal, "偏好") || strings.Contains(event.Task.Goal, "规则") || strings.Contains(event.Task.Goal, "决定") {
		return true
	}
	for _, step := range event.Task.Steps {
		if step.Status == "accepted" {
			return true
		}
	}
	return false
}

func shouldReflect(event LearningEvent) bool {
	for _, step := range event.Task.Steps {
		if step.Status != "" && step.Status != "accepted" {
			return true
		}
	}
	return false
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
