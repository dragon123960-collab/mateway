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
	NodeStatusVerifying     = "verifying"
	NodeStatusRetrying      = "retrying"
	NodeStatusNeedsReplan   = "needs_replan"
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
	NodeStatusVerifying:     true,
	NodeStatusRetrying:      true,
	NodeStatusNeedsReplan:   true,
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
	NodeTypeSubtask      = "subtask"
	NodeTypeHumanReview  = "human_review"
	NodeTypeHumanConfirm = "human_confirm"
)

var validNodeTypes = map[string]bool{
	NodeTypeModel:        true,
	NodeTypeTool:         true,
	NodeTypeSkill:        true,
	NodeTypeSubtask:      true,
	NodeTypeHumanReview:  true,
	NodeTypeHumanConfirm: true,
}

const (
	NodeModeDirect = "direct"
	NodeModeReact  = "react"
	NodeModeSkill  = "skill"
	NodeModeTool   = "tool"
	NodeModeScript = "script"
	NodeModeHuman  = "human"
)

var validNodeModes = map[string]bool{
	NodeModeDirect: true,
	NodeModeReact:  true,
	NodeModeSkill:  true,
	NodeModeTool:   true,
	NodeModeScript: true,
	NodeModeHuman:  true,
}

func ValidNodeTypes() []string {
	return []string{NodeTypeModel, NodeTypeTool, NodeTypeSkill, NodeTypeSubtask, NodeTypeHumanReview, NodeTypeHumanConfirm}
}

func IsValidNodeType(s string) bool {
	return validNodeTypes[strings.TrimSpace(s)]
}

func IsValidNodeMode(s string) bool {
	if s == "" {
		return true
	}
	return validNodeModes[strings.TrimSpace(s)]
}

func ValidNodeModes() []string {
	return []string{NodeModeDirect, NodeModeReact, NodeModeSkill, NodeModeTool, NodeModeScript, NodeModeHuman}
}

var validTypeModeCombos = map[string]map[string]bool{
	NodeTypeSubtask: {
		"":             true,
		NodeModeDirect: true,
		NodeModeReact:  true,
	},
	NodeTypeSkill: {
		"":            true,
		NodeModeSkill: true,
	},
	NodeTypeTool: {
		"":             true,
		NodeModeTool:   true,
		NodeModeScript: true,
	},
	NodeTypeHumanReview: {
		"":            true,
		NodeModeHuman: true,
	},
	NodeTypeHumanConfirm: {
		"":            true,
		NodeModeHuman: true,
	},
	NodeTypeModel: {
		"":             true,
		NodeModeDirect: true,
		NodeModeReact:  true,
	},
}

func IsValidTypeModeCombo(nodeType, nodeMode string) bool {
	nodeType = strings.TrimSpace(nodeType)
	nodeMode = strings.TrimSpace(nodeMode)
	if modes, ok := validTypeModeCombos[nodeType]; ok {
		_, ok := modes[nodeMode]
		return ok
	}
	return false
}

func ValidModesForType(nodeType string) []string {
	nodeType = strings.TrimSpace(nodeType)
	modes, ok := validTypeModeCombos[nodeType]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(modes))
	for m := range modes {
		if m != "" {
			out = append(out, m)
		}
	}
	return out
}

func (n *TaskGraphNode) TransitionTo(newStatus string) {
	if n == nil {
		return
	}
	now := time.Now()

	switch newStatus {
	case NodeStatusRunning:
		n.Attempts++
	}

	n.Status = newStatus
	n.UpdatedAt = now
}

func (n *TaskGraphNode) SetCompleted(verified bool, reason string) {
	if n == nil {
		return
	}
	now := time.Now()
	n.Status = NodeStatusCompleted
	n.Acceptance.Verified = verified
	n.UpdatedAt = now
	if reason != "" && n.Acceptance.Reason == "" {
		n.Acceptance.Reason = reason
	}
	if verified {
		n.VerifiedAt = now
	}
}

func (n *TaskGraphNode) SetFailed(reason string) {
	if n == nil {
		return
	}
	n.Status = NodeStatusFailed
	n.FailureReason = reason
	n.UpdatedAt = time.Now()
}

func (n *TaskGraphNode) SetBlocked(reason string) {
	if n == nil {
		return
	}
	n.Status = NodeStatusBlocked
	n.FailureReason = reason
	n.UpdatedAt = time.Now()
}

func (n *TaskGraphNode) IsTerminal() bool {
	if n == nil {
		return true
	}
	switch n.Status {
	case NodeStatusCompleted, NodeStatusFailed, NodeStatusBlocked, NodeStatusSkipped:
		return true
	}
	return false
}

func (n *TaskGraphNode) IsActive() bool {
	if n == nil {
		return false
	}
	switch n.Status {
	case NodeStatusRunning, NodeStatusVerifying, NodeStatusRetrying:
		return true
	}
	return false
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
	Mode          string         `json:"mode,omitempty"`
	Goal          string         `json:"goal"`
	Status        string         `json:"status"`
	Depends       []string       `json:"depends,omitempty"`
	Executor      string         `json:"executor,omitempty"`
	Input         map[string]any `json:"input,omitempty"`
	Output        map[string]any `json:"output,omitempty"`
	AllowedTools  []string       `json:"allowed_tools,omitempty"`
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
	IsError   bool   `json:"is_error,omitempty"`
	Blocked   bool   `json:"blocked,omitempty"`
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
