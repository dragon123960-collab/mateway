package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/session"
)

type SkillEvidence struct {
	Name        string `json:"name"`
	Path        string `json:"path,omitempty"`
	Scope       string `json:"scope,omitempty"`
	Description string `json:"description,omitempty"`
}

type LearningEvidence struct {
	Type         string           `json:"type"`
	Time         string           `json:"time"`
	SessionKey   string           `json:"session_key,omitempty"`
	TaskID       string           `json:"task_id,omitempty"`
	Goal         string           `json:"goal,omitempty"`
	Status       string           `json:"status,omitempty"`
	TraceID      string           `json:"trace_id,omitempty"`
	TracePath    string           `json:"trace_path,omitempty"`
	FinalSummary string           `json:"final_summary,omitempty"`
	ToolSequence []string         `json:"tool_sequence,omitempty"`
	ToolSteps    []ToolStepRecord `json:"tool_steps,omitempty"`
	Sources      []string         `json:"sources,omitempty"`
}

type ToolStepRecord struct {
	Tool     string         `json:"tool"`
	Status   string         `json:"status,omitempty"`
	Summary  string         `json:"summary,omitempty"`
	Evidence map[string]any `json:"evidence,omitempty"`
}

type SkillUsageEvidence struct {
	Type         string           `json:"type"`
	Time         string           `json:"time"`
	SessionKey   string           `json:"session_key,omitempty"`
	TaskID       string           `json:"task_id,omitempty"`
	Goal         string           `json:"goal,omitempty"`
	Status       string           `json:"status,omitempty"`
	TraceID      string           `json:"trace_id,omitempty"`
	TracePath    string           `json:"trace_path,omitempty"`
	Skill        SkillEvidence    `json:"skill"`
	ToolSequence []string         `json:"tool_sequence,omitempty"`
	RelatedSteps []ToolStepRecord `json:"related_steps,omitempty"`
	Sources      []string         `json:"sources,omitempty"`
}

func RecordLearningEvidence(event LearningEvent) error {
	home := defaultString(event.Home, ".mateway")
	payload := LearningEvidence{
		Type:         "task_completed",
		Time:         time.Now().Format(time.RFC3339Nano),
		SessionKey:   event.SessionKey,
		TaskID:       event.Task.ID,
		Goal:         event.Task.Goal,
		Status:       event.Task.Status,
		TraceID:      event.TraceID,
		TracePath:    event.TracePath,
		FinalSummary: strings.TrimSpace(event.FinalText),
		ToolSequence: toolSequence(event.Task),
		ToolSteps:    toolStepRecords(event.Task.Steps),
		Sources:      learningSources(event),
	}
	if err := appendJSONL(filepath.Join(home, "observe", "learning", "events.jsonl"), payload); err != nil {
		return err
	}
	if HasUserCorrectionCue(event.UserText) {
		correction := payload
		correction.Type = "user_correction"
		correction.FinalSummary = strings.TrimSpace(event.UserText)
		return appendJSONL(filepath.Join(home, "observe", "learning", "events.jsonl"), correction)
	}
	return nil
}

func RecordSkillUsage(event LearningEvent) error {
	home := defaultString(event.Home, ".mateway")
	steps := toolStepRecords(event.Task.Steps)
	for _, skill := range event.Skills {
		if strings.TrimSpace(skill.Name) == "" {
			continue
		}
		payload := SkillUsageEvidence{
			Type:         "skill_usage",
			Time:         time.Now().Format(time.RFC3339Nano),
			SessionKey:   event.SessionKey,
			TaskID:       event.Task.ID,
			Goal:         event.Task.Goal,
			Status:       event.Task.Status,
			TraceID:      event.TraceID,
			TracePath:    event.TracePath,
			Skill:        skill,
			ToolSequence: toolSequence(event.Task),
			RelatedSteps: steps,
			Sources:      learningSources(event),
		}
		if err := appendJSONL(filepath.Join(home, "observe", "skill_usage", "events.jsonl"), payload); err != nil {
			return err
		}
	}
	return nil
}

func toolSequence(task session.TaskNode) []string {
	var out []string
	for _, step := range task.Steps {
		if strings.TrimSpace(step.Tool) != "" {
			out = append(out, step.Tool)
		}
	}
	return out
}

func toolStepRecords(steps []session.TaskStep) []ToolStepRecord {
	var out []ToolStepRecord
	for _, step := range steps {
		out = append(out, ToolStepRecord{
			Tool:     step.Tool,
			Status:   step.Status,
			Summary:  step.Summary,
			Evidence: step.Evidence,
		})
	}
	return out
}

func appendJSONL(path string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(data, '\n'))
	return err
}
