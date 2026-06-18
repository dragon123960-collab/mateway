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
	GraphID      string           `json:"graph_id,omitempty"`
	Goal         string           `json:"goal,omitempty"`
	Status       string           `json:"status,omitempty"`
	TraceID      string           `json:"trace_id,omitempty"`
	TracePath    string           `json:"trace_path,omitempty"`
	FinalSummary string           `json:"final_summary,omitempty"`
	ToolSequence []string         `json:"tool_sequence,omitempty"`
	ToolSteps    []ToolStepRecord `json:"tool_steps,omitempty"`
	NodeRecords  []NodeRecord     `json:"node_records,omitempty"`
	FailedNodes  []string         `json:"failed_nodes,omitempty"`
	RetriedNodes []string         `json:"retried_nodes,omitempty"`
	BlockedNodes []string         `json:"blocked_nodes,omitempty"`
	Sources      []string         `json:"sources,omitempty"`
}

type NodeRecord struct {
	ID                string                `json:"id"`
	Type              string                `json:"type"`
	Mode              string                `json:"mode,omitempty"`
	Goal              string                `json:"goal"`
	Status            string                `json:"status"`
	Executor          string                `json:"executor,omitempty"`
	Attempts          int                   `json:"attempts"`
	ResultSummary     string                `json:"result_summary,omitempty"`
	StructuredOutputs map[string]any        `json:"structured_outputs,omitempty"`
	FailureReason     string                `json:"failure_reason,omitempty"`
	EvidenceRefs      []session.EvidenceRef `json:"evidence_refs,omitempty"`
	VerifierStatus    string                `json:"verifier_status,omitempty"`
	VerifierReason    string                `json:"verifier_reason,omitempty"`
	SelectedSkill     string                `json:"selected_skill,omitempty"`
	VerifiedAt        string                `json:"verified_at,omitempty"`
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
	SkillNodeID  string           `json:"skill_node_id,omitempty"`
	GraphID      string           `json:"graph_id,omitempty"`
	NodeResult   string           `json:"node_result,omitempty"`
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
		GraphID:      graphID(event.GraphSummary),
		Goal:         event.Task.Goal,
		Status:       event.Task.Status,
		TraceID:      event.TraceID,
		TracePath:    event.TracePath,
		FinalSummary: strings.TrimSpace(event.FinalText),
		ToolSequence: toolSequence(event.Task),
		ToolSteps:    toolStepRecords(event.Task.Steps),
		NodeRecords:  buildNodeRecords(event.GraphSummary),
		FailedNodes:  graphNodesByClass(event.GraphSummary, "failed"),
		RetriedNodes: graphNodesByClass(event.GraphSummary, "retried"),
		BlockedNodes: graphNodesByClass(event.GraphSummary, "blocked"),
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

	if event.GraphSummary != nil {
		for _, n := range event.GraphSummary.Nodes {
			if n.Type != "skill" {
				continue
			}
			if n.Status != session.NodeStatusCompleted && strings.TrimSpace(n.ResultSummary) == "" {
				continue
			}
			status := n.Status
			if status == session.NodeStatusCompleted && n.ResultSummary != "" {
				status = "success"
			}
			skillName := strings.TrimSpace(n.SelectedSkill)
			if skillName == "" {
				skillName = strings.TrimSpace(n.Goal)
			}
			payload := SkillUsageEvidence{
				Type:       "skill_usage",
				Time:       time.Now().Format(time.RFC3339Nano),
				SessionKey: event.SessionKey,
				TaskID:     event.Task.ID,
				Goal:       event.Task.Goal,
				Status:     status,
				TraceID:    event.TraceID,
				TracePath:  event.TracePath,
				Skill: SkillEvidence{
					Name: skillName,
				},
				SkillNodeID: n.ID,
				GraphID:     event.GraphSummary.GraphID,
				NodeResult:  n.ResultSummary,
				Sources:     learningSources(event),
			}
			if err := appendJSONL(filepath.Join(home, "observe", "skill_usage", "events.jsonl"), payload); err != nil {
				return err
			}
		}
	}

	return nil
}

func buildNodeRecords(summary *session.GraphMemorySummary) []NodeRecord {
	if summary == nil {
		return nil
	}
	var records []NodeRecord
	for _, n := range summary.Nodes {
		verifiedAt := ""
		if !n.VerifiedAt.IsZero() {
			verifiedAt = n.VerifiedAt.Format(time.RFC3339)
		}
		records = append(records, NodeRecord{
			ID:                n.ID,
			Type:              n.Type,
			Mode:              n.Mode,
			Goal:              n.Goal,
			Status:            n.Status,
			Executor:          n.Executor,
			Attempts:          n.Attempts,
			ResultSummary:     n.ResultSummary,
			StructuredOutputs: n.Output,
			FailureReason:     n.FailureReason,
			EvidenceRefs:      n.EvidenceRefs,
			VerifierStatus:    n.VerifierStatus,
			VerifierReason:    n.VerifierReason,
			SelectedSkill:     n.SelectedSkill,
			VerifiedAt:        verifiedAt,
		})
	}
	return records
}

func graphID(summary *session.GraphMemorySummary) string {
	if summary == nil {
		return ""
	}
	return summary.GraphID
}

func graphNodesByClass(summary *session.GraphMemorySummary, class string) []string {
	if summary == nil {
		return nil
	}
	switch class {
	case "failed":
		return summary.FailedNodes
	case "retried":
		return summary.RetriedNodes
	case "blocked":
		return summary.BlockedNodes
	default:
		return nil
	}
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
