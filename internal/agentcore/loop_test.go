package agentcore

import (
	"context"
	"strings"
	"testing"
)

func TestRunReturnsAssistantAnswerWithoutTools(t *testing.T) {
	model := scriptedModel{messages: []Message{{Role: RoleAssistant, Content: "hello"}}}
	result, err := Run(context.Background(), Config{Model: &model}, []Message{{Role: RoleUser, Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalText != "hello" {
		t.Fatalf("FinalText = %q", result.FinalText)
	}
}

func TestRunExecutesToolCall(t *testing.T) {
	model := scriptedModel{messages: []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "1", Name: "test.echo", Args: map[string]any{"text": "ok"}}}},
		{Role: RoleAssistant, Content: "done"},
	}}
	registry := NewToolRegistry()
	registry.Register(testEchoTool{})
	result, err := Run(context.Background(), Config{Model: &model, Tools: registry}, []Message{{Role: RoleUser, Content: "use tool"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Iterations != 2 {
		t.Fatalf("Iterations = %d", result.Iterations)
	}
	if !containsToolMessage(result.Messages, "ok") {
		t.Fatalf("tool result not appended: %#v", result.Messages)
	}
}

func TestRunUnknownToolAppendsErrorResult(t *testing.T) {
	model := scriptedModel{messages: []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "1", Name: "missing"}}},
		{Role: RoleAssistant, Content: "done"},
	}}
	result, err := Run(context.Background(), Config{Model: &model, Tools: NewToolRegistry()}, []Message{{Role: RoleUser, Content: "use tool"}})
	if err != nil {
		t.Fatal(err)
	}
	if !containsToolMessage(result.Messages, `tool "missing" not found`) {
		t.Fatalf("missing tool error not appended: %#v", result.Messages)
	}
}

func TestRunMaxIterations(t *testing.T) {
	model := repeatToolModel{}
	registry := NewToolRegistry()
	registry.Register(testEchoTool{})
	result, err := Run(context.Background(), Config{Model: model, Tools: registry, MaxIterations: 2}, []Message{{Role: RoleUser, Content: "loop"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Iterations != 2 {
		t.Fatalf("Iterations = %d", result.Iterations)
	}
	if !strings.Contains(result.FinalText, "最大工具循环次数") {
		t.Fatalf("FinalText = %q", result.FinalText)
	}
}

func TestRunSynthesizesAfterToolBudget(t *testing.T) {
	model := budgetAwareModel{}
	registry := NewToolRegistry()
	registry.Register(testEchoTool{})
	result, err := Run(context.Background(), Config{Model: model, Tools: registry, MaxIterations: 2}, []Message{{Role: RoleUser, Content: "use tool"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalText != "summary from existing evidence" {
		t.Fatalf("FinalText = %q", result.FinalText)
	}
	if !containsUserMessage(result.Messages, "Tool budget reached") {
		t.Fatalf("synthesis instruction not appended: %#v", result.Messages)
	}
}

func TestRunRepairsMalformedToolCall(t *testing.T) {
	model := scriptedModel{messages: []Message{
		{Role: RoleAssistant, Content: "checking\n[TOOL_CALL]\n{\"id\":\"call_1\""},
		{Role: RoleAssistant, Content: "recovered answer"},
	}}
	result, err := Run(context.Background(), Config{Model: &model, MaxIterations: 2}, []Message{{Role: RoleUser, Content: "use tool"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalText != "recovered answer" {
		t.Fatalf("FinalText = %q", result.FinalText)
	}
	if !containsUserMessage(result.Messages, "malformed") {
		t.Fatalf("repair instruction not appended: %#v", result.Messages)
	}
}

func TestRunSynthesizesMalformedToolCallAtBudget(t *testing.T) {
	model := malformedAtBudgetModel{}
	result, err := Run(context.Background(), Config{Model: model, MaxIterations: 1}, []Message{{Role: RoleUser, Content: "use tool"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalText != "summary after malformed" {
		t.Fatalf("FinalText = %q", result.FinalText)
	}
	if !containsUserMessage(result.Messages, "malformed and cannot be executed") {
		t.Fatalf("synthesis instruction not appended: %#v", result.Messages)
	}
}

func TestRunBeforeToolCallCanBlock(t *testing.T) {
	model := scriptedModel{messages: []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "1", Name: "test.echo", Args: map[string]any{"text": "ok"}}}},
		{Role: RoleAssistant, Content: "stopped"},
	}}
	registry := NewToolRegistry()
	registry.Register(testEchoTool{})
	result, err := Run(context.Background(), Config{
		Model: &model,
		Tools: registry,
		Hooks: Hooks{
			BeforeToolCall: func(context.Context, BeforeToolCallContext) (BeforeToolCallResult, error) {
				return BeforeToolCallResult{Block: true, Reason: "needs confirmation"}, nil
			},
		},
	}, []Message{{Role: RoleUser, Content: "use tool"}})
	if err != nil {
		t.Fatal(err)
	}
	if !containsToolMessage(result.Messages, "needs confirmation") {
		t.Fatalf("blocked tool result not appended: %#v", result.Messages)
	}
}

func TestRunAfterToolCallCanRewriteResult(t *testing.T) {
	model := scriptedModel{messages: []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "1", Name: "test.echo", Args: map[string]any{"text": "ok"}}}},
		{Role: RoleAssistant, Content: "done"},
	}}
	registry := NewToolRegistry()
	registry.Register(testEchoTool{})
	result, err := Run(context.Background(), Config{
		Model: &model,
		Tools: registry,
		Hooks: Hooks{
			AfterToolCall: func(_ context.Context, ctx AfterToolCallContext) (AfterToolCallResult, error) {
				rewritten := ctx.ToolResult
				rewritten.Content = "rewritten"
				return AfterToolCallResult{ToolResult: &rewritten}, nil
			},
		},
	}, []Message{{Role: RoleUser, Content: "use tool"}})
	if err != nil {
		t.Fatal(err)
	}
	if !containsToolMessage(result.Messages, "rewritten") {
		t.Fatalf("rewritten tool result not appended: %#v", result.Messages)
	}
}

func TestRunAfterToolCallReceivesToolDefinition(t *testing.T) {
	model := scriptedModel{messages: []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "1", Name: "test.echo", Args: map[string]any{"text": "ok"}}}},
		{Role: RoleAssistant, Content: "done"},
	}}
	registry := NewToolRegistry()
	registry.Register(testEchoTool{})
	var toolName string
	_, err := Run(context.Background(), Config{
		Model: &model,
		Tools: registry,
		Hooks: Hooks{
			AfterToolCall: func(_ context.Context, ctx AfterToolCallContext) (AfterToolCallResult, error) {
				toolName = ctx.Tool.Name()
				return AfterToolCallResult{}, nil
			},
		},
	}, []Message{{Role: RoleUser, Content: "use tool"}})
	if err != nil {
		t.Fatal(err)
	}
	if toolName != "test.echo" {
		t.Fatalf("toolName = %q", toolName)
	}
}

type scriptedModel struct {
	messages []Message
	index    int
}

func (m *scriptedModel) Next(context.Context, Context) (Message, error) {
	msg := m.messages[m.index]
	m.index++
	return msg, nil
}

type repeatToolModel struct{}

func (repeatToolModel) Next(context.Context, Context) (Message, error) {
	return Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "1", Name: "test.echo", Args: map[string]any{"text": "again"}}}}, nil
}

type budgetAwareModel struct{}

func (budgetAwareModel) Next(_ context.Context, ctx Context) (Message, error) {
	for _, msg := range ctx.Messages {
		if msg.Role == RoleUser && strings.Contains(msg.Content, "Tool budget reached") {
			return Message{Role: RoleAssistant, Content: "summary from existing evidence"}, nil
		}
	}
	return Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "1", Name: "test.echo", Args: map[string]any{"text": "again"}}}}, nil
}

type malformedAtBudgetModel struct{}

func (malformedAtBudgetModel) Next(_ context.Context, ctx Context) (Message, error) {
	for _, msg := range ctx.Messages {
		if msg.Role == RoleUser && strings.Contains(msg.Content, "malformed and cannot be executed") {
			return Message{Role: RoleAssistant, Content: "summary after malformed"}, nil
		}
	}
	return Message{Role: RoleAssistant, Content: "[TOOL_CALL]\n{\"id\":\"call_1\""}, nil
}

type testEchoTool struct{}

func (testEchoTool) Name() string        { return "test.echo" }
func (testEchoTool) Description() string { return "test echo" }
func (testEchoTool) Schema() Schema      { return Schema{Required: []string{"text"}} }
func (testEchoTool) Risk() Risk          { return RiskSafeRead }
func (testEchoTool) Run(_ context.Context, call ToolCall) ToolResult {
	return ToolResult{ToolCallID: call.ID, Content: call.Args["text"].(string)}
}

func containsToolMessage(messages []Message, content string) bool {
	for _, msg := range messages {
		if msg.Role == RoleTool && strings.Contains(msg.Content, content) {
			return true
		}
	}
	return false
}

func containsUserMessage(messages []Message, content string) bool {
	for _, msg := range messages {
		if msg.Role == RoleUser && strings.Contains(msg.Content, content) {
			return true
		}
	}
	return false
}
