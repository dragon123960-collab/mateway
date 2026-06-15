package agentcore

import (
	"context"
	"strings"
	"time"
)

type EventType string

const (
	EventAgentStart            EventType = "agent_start"
	EventAgentEnd              EventType = "agent_end"
	EventTurnStart             EventType = "turn_start"
	EventTurnEnd               EventType = "turn_end"
	EventModelStart            EventType = "model_start"
	EventMessageStart          EventType = "message_start"
	EventMessageEnd            EventType = "message_end"
	EventToolExecutionStart    EventType = "tool_execution_start"
	EventToolExecutionProgress EventType = "tool_execution_progress"
	EventToolExecutionEnd      EventType = "tool_execution_end"
)

type Event struct {
	Type       EventType
	Message    Message
	ToolCall   ToolCall
	ToolResult ToolResult
	Iteration  int
	Duration   time.Duration
}

type EventSink func(context.Context, Event) error

type BeforeToolCallContext struct {
	Message  Message
	ToolCall ToolCall
	Tool     Tool
}

type ToolExecutionContext struct {
	Message  Message
	ToolCall ToolCall
	Tool     Tool
}

type BeforeToolCallResult struct {
	Block     bool
	Retryable bool
	Reason    string
	Context   context.Context
}

type AfterToolCallContext struct {
	Message    Message
	ToolCall   ToolCall
	Tool       Tool
	ToolResult ToolResult
}

type AfterToolCallResult struct {
	ToolResult *ToolResult
	Terminate  bool
	StopReason string
}

type TurnContext struct {
	Message     Message
	ToolResults []ToolResult
	Messages    []Message
	Iteration   int
}

type NextTurnUpdate struct {
	Messages []Message
}

type Hooks struct {
	Emit                 EventSink
	BeforeToolCall       func(context.Context, BeforeToolCallContext) (BeforeToolCallResult, error)
	AfterToolCall        func(context.Context, AfterToolCallContext) (AfterToolCallResult, error)
	ToolTimeout          func(ToolExecutionContext) time.Duration
	ToolProgressInterval func(ToolExecutionContext) time.Duration
	ShouldStopAfterTurn  func(context.Context, TurnContext) (bool, error)
	PrepareNextTurn      func(context.Context, TurnContext) (NextTurnUpdate, error)
	GetSteeringMessages  func(context.Context) ([]Message, error)
	GetFollowUpMessages  func(context.Context) ([]Message, error)
	ToolRetryBudget      func(ToolExecutionContext) int
}

func (h Hooks) toolRetryBudget(ctx ToolExecutionContext) int {
	if h.ToolRetryBudget == nil {
		return 0
	}
	return h.ToolRetryBudget(ctx)
}

func IsRetryableToolResult(toolName string, result ToolResult) bool {
	if !result.IsError {
		return false
	}
	lower := strings.ToLower(result.Content)
	switch {
	case toolName == "web.fetch":
		return matchesRetryableFetchFailure(lower)
	case toolName == "web.search":
		if strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline") {
			return true
		}
		return false
	case toolName == "terminal.run":
		if strings.Contains(lower, "signal: killed") || strings.Contains(lower, "timed_out") ||
			strings.Contains(lower, "timed out") || strings.Contains(lower, "deadline") {
			return true
		}
		return false
	default:
		if strings.Contains(lower, "timeout") || strings.Contains(lower, "timed out") ||
			strings.Contains(lower, "deadline") || strings.Contains(lower, "i/o timeout") {
			return true
		}
		return false
	}
}

func matchesRetryableFetchFailure(lower string) bool {
	if strings.Contains(lower, "ssrf") || strings.Contains(lower, "internal") {
		return false
	}
	if strings.Contains(lower, "cloudflare") || strings.Contains(lower, "please enable cookies") ||
		strings.Contains(lower, "please enable js") || strings.Contains(lower, "disable any ad blocker") ||
		strings.Contains(lower, "captcha") || strings.Contains(lower, "challenge") {
		return false
	}
	if strings.Contains(lower, "bot") && strings.Contains(lower, "protection") {
		return false
	}
	if strings.Contains(lower, "too many requests") || strings.Contains(lower, "429") {
		return true
	}
	if strings.Contains(lower, "timeout") || strings.Contains(lower, "timed out") ||
		strings.Contains(lower, "deadline") ||
		strings.Contains(lower, "client.timeout") || strings.Contains(lower, "i/o timeout") {
		return true
	}
	if strings.Contains(lower, "connection refused") || strings.Contains(lower, "connection reset") ||
		strings.Contains(lower, "no such host") || strings.Contains(lower, "dns") {
		return true
	}
	return false
}
