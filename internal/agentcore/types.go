package agentcore

import "context"

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type Message struct {
	Role       Role
	Content    string
	Parts      []MessagePart `json:"parts,omitempty"`
	ToolCalls  []ToolCall
	ToolCallID string
	Usage      *Usage `json:"usage,omitempty"`
}

type PartType string

const (
	PartText  PartType = "text"
	PartImage PartType = "image"
	PartAudio PartType = "audio"
	PartVideo PartType = "video"
	PartFile  PartType = "file"
)

type MessagePart struct {
	Type     PartType          `json:"type"`
	Text     string            `json:"text,omitempty"`
	URI      string            `json:"uri,omitempty"`
	MimeType string            `json:"mime_type,omitempty"`
	Name     string            `json:"name,omitempty"`
	Size     int64             `json:"size,omitempty"`
	SHA256   string            `json:"sha256,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type Usage struct {
	Provider          string `json:"provider,omitempty"`
	Model             string `json:"model,omitempty"`
	InputTokens       int    `json:"input_tokens,omitempty"`
	OutputTokens      int    `json:"output_tokens,omitempty"`
	TotalTokens       int    `json:"total_tokens,omitempty"`
	CacheHit          bool   `json:"cache_hit,omitempty"`
	CacheReadTokens   int    `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens  int    `json:"cache_write_tokens,omitempty"`
	CacheInputTokens  int    `json:"cache_input_tokens,omitempty"`
	CacheOutputTokens int    `json:"cache_output_tokens,omitempty"`
}

type ToolCall struct {
	ID   string
	Name string
	Args map[string]any
}

type ToolResult struct {
	ToolCallID string
	Content    string
	IsError    bool
	Evidence   map[string]any
}

type Model interface {
	Next(context.Context, Context) (Message, error)
}

type Tool interface {
	Name() string
	Description() string
	Schema() Schema
	Risk() Risk
	Run(context.Context, ToolCall) ToolResult
}

type ToolContract struct {
	WhenToUse            string
	WhenNotToUse         string
	OutputContract       string
	Evidence             string
	Acceptance           string
	SoftFailureSignals   []string
	ParallelMode         string
	ReusePolicy          string
	ConfirmationBoundary string
}

type ContractedTool interface {
	ToolContract() ToolContract
}

func ContractFor(tool Tool) ToolContract {
	if tool == nil {
		return ToolContract{}
	}
	if contracted, ok := tool.(ContractedTool); ok {
		return contracted.ToolContract()
	}
	return ToolContract{
		WhenToUse:            tool.Description(),
		OutputContract:       "Return concise content suitable for the model to continue the task.",
		Evidence:             "Return useful content and structured evidence when available.",
		Acceptance:           "Result is accepted when the tool does not report an error.",
		ParallelMode:         "read_only_ok",
		ReusePolicy:          "never",
		ConfirmationBoundary: string(tool.Risk()),
	}
}

type Schema struct {
	Required   []string
	Properties map[string]any
}

type Risk string

const (
	RiskSafeRead        Risk = "safe_read"
	RiskGuardedMutation Risk = "guarded_mutation"
	RiskDangerous       Risk = "dangerous"
)

type Context struct {
	SystemPrompt string
	Messages     []Message
	Tools        []Tool
}

type Config struct {
	SystemPrompt     string
	Model            Model
	Tools            *ToolRegistry
	MaxParallelTools int
	MaxIterations    int
	Hooks            Hooks
}

type Result struct {
	Messages   []Message
	FinalText  string
	Iterations int
	StopReason string
}
