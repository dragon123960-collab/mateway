package session

import (
	"fmt"
	"strings"
	"time"
)

const (
	GraphStatusPlanned       = "planned"
	GraphStatusRunning       = "running"
	GraphStatusAwaitingInput = "awaiting_input"
	GraphStatusBlocked       = "blocked"
	GraphStatusFailed        = "failed"
	GraphStatusCompleted     = "completed"
)

var validGraphStatuses = map[string]bool{
	GraphStatusPlanned:       true,
	GraphStatusRunning:       true,
	GraphStatusAwaitingInput: true,
	GraphStatusBlocked:       true,
	GraphStatusFailed:        true,
	GraphStatusCompleted:     true,
}

func IsValidGraphStatus(s string) bool {
	return validGraphStatuses[strings.TrimSpace(s)]
}

const (
	NodeStatusPending       = "pending"
	NodeStatusReady         = "ready"
	NodeStatusRunning       = "running"
	NodeStatusAwaitingInput = "awaiting_input"
	NodeStatusBlocked       = "blocked"
	NodeStatusFailed        = "failed"
	NodeStatusCompleted     = "completed"
	NodeStatusSkipped       = "skipped"
)

var validNodeStatuses = map[string]bool{
	NodeStatusPending:       true,
	NodeStatusReady:         true,
	NodeStatusRunning:       true,
	NodeStatusAwaitingInput: true,
	NodeStatusBlocked:       true,
	NodeStatusFailed:        true,
	NodeStatusCompleted:     true,
	NodeStatusSkipped:       true,
}

func IsValidNodeStatus(s string) bool {
	return validNodeStatuses[strings.TrimSpace(s)]
}

const (
	NodeTypeModel        = "model"
	NodeTypeTool         = "tool"
	NodeTypeSkill        = "skill"
	NodeTypeHumanReview  = "human_review"
	NodeTypeHumanConfirm = "human_confirm"
)

var validNodeTypes = map[string]bool{
	NodeTypeModel:        true,
	NodeTypeTool:         true,
	NodeTypeSkill:        true,
	NodeTypeHumanReview:  true,
	NodeTypeHumanConfirm: true,
}

func ValidNodeTypes() []string {
	return []string{NodeTypeModel, NodeTypeTool, NodeTypeSkill, NodeTypeHumanReview, NodeTypeHumanConfirm}
}

func IsValidNodeType(s string) bool {
	return validNodeTypes[strings.TrimSpace(s)]
}

type TaskGraph struct {
	ID        string          `json:"id"`
	TaskID    string          `json:"task_id"`
	Status    string          `json:"status"`
	Nodes     []TaskGraphNode `json:"nodes"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type TaskGraphNode struct {
	ID            string         `json:"id"`
	Type          string         `json:"type"`
	Goal          string         `json:"goal"`
	Status        string         `json:"status"`
	Depends       []string       `json:"depends,omitempty"`
	Executor      string         `json:"executor,omitempty"`
	Input         map[string]any `json:"input,omitempty"`
	Output        map[string]any `json:"output,omitempty"`
	Attempts      int            `json:"attempts,omitempty"`
	ResultSummary string         `json:"result_summary,omitempty"`
	EvidenceRefs  []EvidenceRef  `json:"evidence_refs,omitempty"`
	FailureReason string         `json:"failure_reason,omitempty"`
	Acceptance    Acceptance     `json:"acceptance"`
	VerifiedAt    time.Time      `json:"verified_at,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

type EvidenceRef struct {
	Kind      string `json:"kind,omitempty"`
	TraceID   string `json:"trace_id,omitempty"`
	TracePath string `json:"trace_path,omitempty"`
	ToolName  string `json:"tool_name,omitempty"`
	Summary   string `json:"summary,omitempty"`
}

type Acceptance struct {
	Criteria string `json:"criteria,omitempty"`
	Verified bool   `json:"verified,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type GraphValidationError struct {
	Message string `json:"message"`
	NodeID  string `json:"node_id,omitempty"`
}

func (e *GraphValidationError) Error() string {
	if e.NodeID != "" {
		return fmt.Sprintf("node %s: %s", e.NodeID, e.Message)
	}
	return e.Message
}

type GraphValidationErrors []GraphValidationError

func (e GraphValidationErrors) Error() string {
	msgs := make([]string, len(e))
	for i, err := range e {
		msgs[i] = err.Error()
	}
	return strings.Join(msgs, "; ")
}

func (e GraphValidationErrors) IsValid() bool {
	return len(e) == 0
}

func (g *TaskGraph) NodeByID(id string) *TaskGraphNode {
	for i := range g.Nodes {
		if g.Nodes[i].ID == id {
			return &g.Nodes[i]
		}
	}
	return nil
}

func (g *TaskGraph) NodeIDs() []string {
	ids := make([]string, len(g.Nodes))
	for i, n := range g.Nodes {
		ids[i] = n.ID
	}
	return ids
}
