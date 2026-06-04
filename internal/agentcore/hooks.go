package agentcore

import (
	"context"
	"time"
)

type EventType string

const (
	EventAgentStart         EventType = "agent_start"
	EventAgentEnd           EventType = "agent_end"
	EventTurnStart          EventType = "turn_start"
	EventTurnEnd            EventType = "turn_end"
	EventMessageStart       EventType = "message_start"
	EventMessageEnd         EventType = "message_end"
	EventToolExecutionStart EventType = "tool_execution_start"
	EventToolExecutionEnd   EventType = "tool_execution_end"
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

type BeforeToolCallResult struct {
	Block  bool
	Reason string
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
	Emit                EventSink
	BeforeToolCall      func(context.Context, BeforeToolCallContext) (BeforeToolCallResult, error)
	AfterToolCall       func(context.Context, AfterToolCallContext) (AfterToolCallResult, error)
	ShouldStopAfterTurn func(context.Context, TurnContext) (bool, error)
	PrepareNextTurn     func(context.Context, TurnContext) (NextTurnUpdate, error)
	GetSteeringMessages func(context.Context) ([]Message, error)
	GetFollowUpMessages func(context.Context) ([]Message, error)
}
