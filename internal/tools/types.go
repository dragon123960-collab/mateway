package tools

import (
	"context"
	"encoding/json"
)

type Kind string

const (
	KindBuiltin Kind = "builtin"
	KindSkill   Kind = "skill"
	KindMCP     Kind = "mcp"
	KindCLI     Kind = "cli"
)

type Scope struct {
	UserID     string
	Channel    string
	ThreadID   string
	AgentName  string
	Visibility string
}

type Spec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Kind        Kind            `json:"kind"`
	ReadOnly    bool            `json:"read_only"`
	RiskLevel   string          `json:"risk_level,omitempty"`
	TimeoutSec  int             `json:"timeout_seconds,omitempty"`
	Tags        []string        `json:"tags,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

type Call struct {
	RunID          string          `json:"run_id"`
	StepID         string          `json:"step_id"`
	SessionKey     string          `json:"session_key"`
	ThreadID       string          `json:"thread_id,omitempty"`
	AgentName      string          `json:"agent_name,omitempty"`
	ToolName       string          `json:"tool_name"`
	Arguments      json.RawMessage `json:"arguments,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
}

type Result struct {
	Output json.RawMessage `json:"output,omitempty"`
	Meta   map[string]any  `json:"meta,omitempty"`
}

type Tool interface {
	Spec() Spec
	Invoke(ctx context.Context, call Call) (Result, error)
}

type Provider interface {
	Tools(ctx context.Context, scope Scope) ([]Tool, error)
}

type MCPProvider interface {
	Provider
	Name() string
}
