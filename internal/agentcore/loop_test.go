package agentcore

import (
	"context"
	"strings"
	"testing"
	"time"
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

func TestRunExecutesMultipleToolCallsInOneTurn(t *testing.T) {
	model := scriptedModel{messages: []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "1", Name: "test.echo", Args: map[string]any{"text": "agent.md"}},
			{ID: "2", Name: "test.echo", Args: map[string]any{"text": "user.md"}},
		}},
		{Role: RoleAssistant, Content: "done"},
	}}
	registry := NewToolRegistry()
	registry.Register(testEchoTool{})
	result, err := Run(context.Background(), Config{Model: &model, Tools: registry}, []Message{{Role: RoleUser, Content: "read files"}})
	if err != nil {
		t.Fatal(err)
	}
	if !containsToolMessage(result.Messages, "agent.md") || !containsToolMessage(result.Messages, "user.md") {
		t.Fatalf("tool results not appended: %#v", result.Messages)
	}
}

func TestRunExecutesSafeReadToolCallsInParallel(t *testing.T) {
	model := scriptedModel{messages: []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "1", Name: "test.slow_echo", Args: map[string]any{"text": "first"}},
			{ID: "2", Name: "test.slow_echo", Args: map[string]any{"text": "second"}},
		}},
		{Role: RoleAssistant, Content: "done"},
	}}
	registry := NewToolRegistry()
	registry.Register(slowEchoTool{Delay: 80 * time.Millisecond})
	start := time.Now()
	result, err := Run(context.Background(), Config{Model: &model, Tools: registry, MaxParallelTools: 4}, []Message{{Role: RoleUser, Content: "read files"}})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed >= 150*time.Millisecond {
		t.Fatalf("expected parallel execution, elapsed %s", elapsed)
	}
	if got := toolMessages(result.Messages); strings.Join(got, ",") != "first,second" {
		t.Fatalf("tool result order = %#v", got)
	}
}

func TestRunSerializesMixedParallelModes(t *testing.T) {
	model := scriptedModel{messages: []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "1", Name: "test.slow_echo", Args: map[string]any{"text": "first"}},
			{ID: "2", Name: "test.unsafe_echo", Args: map[string]any{"text": "second"}},
		}},
		{Role: RoleAssistant, Content: "done"},
	}}
	registry := NewToolRegistry()
	registry.Register(slowEchoTool{Delay: 60 * time.Millisecond})
	registry.Register(unsafeEchoTool{Delay: 60 * time.Millisecond})
	start := time.Now()
	_, err := Run(context.Background(), Config{Model: &model, Tools: registry, MaxParallelTools: 4}, []Message{{Role: RoleUser, Content: "mixed tools"}})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 110*time.Millisecond {
		t.Fatalf("expected serial execution for mixed tools, elapsed %s", elapsed)
	}
}

func TestRunMaxParallelToolsOneSerializesSafeReads(t *testing.T) {
	model := scriptedModel{messages: []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "1", Name: "test.slow_echo", Args: map[string]any{"text": "first"}},
			{ID: "2", Name: "test.slow_echo", Args: map[string]any{"text": "second"}},
		}},
		{Role: RoleAssistant, Content: "done"},
	}}
	registry := NewToolRegistry()
	registry.Register(slowEchoTool{Delay: 60 * time.Millisecond})
	start := time.Now()
	_, err := Run(context.Background(), Config{Model: &model, Tools: registry, MaxParallelTools: 1}, []Message{{Role: RoleUser, Content: "read files"}})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 110*time.Millisecond {
		t.Fatalf("expected serial execution when MaxParallelTools=1, elapsed %s", elapsed)
	}
}

func TestRunParallelBatchBlocksBeforeExecutingTools(t *testing.T) {
	model := scriptedModel{messages: []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "1", Name: "test.counting_echo", Args: map[string]any{"text": "first"}},
			{ID: "2", Name: "test.counting_echo", Args: map[string]any{"text": "second"}},
		}},
		{Role: RoleAssistant, Content: "done"},
	}}
	registry := NewToolRegistry()
	tool := &countingEchoTool{}
	registry.Register(tool)
	result, err := Run(context.Background(), Config{
		Model:            &model,
		Tools:            registry,
		MaxParallelTools: 4,
		Hooks: Hooks{
			BeforeToolCall: func(_ context.Context, ctx BeforeToolCallContext) (BeforeToolCallResult, error) {
				if ctx.ToolCall.ID == "2" {
					return BeforeToolCallResult{Block: true, Reason: "needs confirmation"}, nil
				}
				return BeforeToolCallResult{}, nil
			},
		},
	}, []Message{{Role: RoleUser, Content: "read files"}})
	if err != nil {
		t.Fatal(err)
	}
	if tool.Count != 0 {
		t.Fatalf("expected no tools to run after blocked parallel preflight, ran %d", tool.Count)
	}
	if !containsToolMessage(result.Messages, "needs confirmation") {
		t.Fatalf("blocked result not appended: %#v", result.Messages)
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
	if !strings.Contains(result.FinalText, "工具预算已到上限") || result.StopReason != "tool_budget_reached" {
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
	if result.FinalText != "工具预算已到上限，任务未完成。已有工具结果已保留在 trace 中；请补充信息后重试或继续当前任务。" {
		t.Fatalf("FinalText = %q", result.FinalText)
	}
	if result.StopReason != "tool_budget_reached" {
		t.Fatalf("StopReason = %q", result.StopReason)
	}
	if containsUserMessage(result.Messages, "Tool budget reached") {
		t.Fatalf("budget synthesis instruction should not be appended: %#v", result.Messages)
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

func TestRunCanContinueAfterRejectedFinalText(t *testing.T) {
	model := scriptedModel{messages: []Message{
		{Role: RoleAssistant, Content: "next I will write the script"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "1", Name: "test.echo", Args: map[string]any{"text": "script written"}}}},
		{Role: RoleAssistant, Content: "completed with evidence"},
	}}
	registry := NewToolRegistry()
	registry.Register(testEchoTool{})
	var followUps []Message
	result, err := Run(context.Background(), Config{
		Model: &model,
		Tools: registry,
		Hooks: Hooks{
			ShouldStopAfterTurn: func(_ context.Context, turn TurnContext) (bool, error) {
				if turn.Message.Content == "next I will write the script" {
					followUps = append(followUps, Message{Role: RoleUser, Content: "finish now"})
				}
				return false, nil
			},
			GetFollowUpMessages: func(context.Context) ([]Message, error) {
				out := followUps
				followUps = nil
				return out, nil
			},
		},
	}, []Message{{Role: RoleUser, Content: "create script"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalText != "completed with evidence" {
		t.Fatalf("FinalText = %q", result.FinalText)
	}
	if !containsUserMessage(result.Messages, "finish now") || !containsToolMessage(result.Messages, "script written") {
		t.Fatalf("expected correction and tool evidence, got %#v", result.Messages)
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

func TestRunPassesSystemPromptAndSteeringMessages(t *testing.T) {
	model := captureContextModel{}
	_, err := Run(context.Background(), Config{
		SystemPrompt: "base system",
		Model:        &model,
		Hooks: Hooks{
			GetSteeringMessages: func(context.Context) ([]Message, error) {
				return []Message{{Role: RoleSystem, Content: "hook system"}}, nil
			},
		},
	}, []Message{{Role: RoleUser, Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if model.systemPrompt != "base system" {
		t.Fatalf("system prompt = %q", model.systemPrompt)
	}
	if !containsSystemMessage(model.messages, "hook system") {
		t.Fatalf("expected steering system message, got %#v", model.messages)
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

type slowEchoTool struct {
	Delay time.Duration
}

func (slowEchoTool) Name() string        { return "test.slow_echo" }
func (slowEchoTool) Description() string { return "test slow echo" }
func (slowEchoTool) Schema() Schema      { return Schema{Required: []string{"text"}} }
func (slowEchoTool) Risk() Risk          { return RiskSafeRead }
func (slowEchoTool) ToolContract() ToolContract {
	return ToolContract{ParallelMode: "read_only_ok"}
}
func (t slowEchoTool) Run(ctx context.Context, call ToolCall) ToolResult {
	timer := time.NewTimer(t.Delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ToolResult{ToolCallID: call.ID, Content: ctx.Err().Error(), IsError: true}
	case <-timer.C:
		return ToolResult{ToolCallID: call.ID, Content: call.Args["text"].(string)}
	}
}

type unsafeEchoTool struct {
	Delay time.Duration
}

func (unsafeEchoTool) Name() string        { return "test.unsafe_echo" }
func (unsafeEchoTool) Description() string { return "test unsafe echo" }
func (unsafeEchoTool) Schema() Schema      { return Schema{Required: []string{"text"}} }
func (unsafeEchoTool) Risk() Risk          { return RiskGuardedMutation }
func (unsafeEchoTool) ToolContract() ToolContract {
	return ToolContract{ParallelMode: "forbid"}
}
func (t unsafeEchoTool) Run(ctx context.Context, call ToolCall) ToolResult {
	timer := time.NewTimer(t.Delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ToolResult{ToolCallID: call.ID, Content: ctx.Err().Error(), IsError: true}
	case <-timer.C:
		return ToolResult{ToolCallID: call.ID, Content: call.Args["text"].(string)}
	}
}

type countingEchoTool struct {
	Count int
}

func (*countingEchoTool) Name() string        { return "test.counting_echo" }
func (*countingEchoTool) Description() string { return "test counting echo" }
func (*countingEchoTool) Schema() Schema      { return Schema{Required: []string{"text"}} }
func (*countingEchoTool) Risk() Risk          { return RiskSafeRead }
func (*countingEchoTool) ToolContract() ToolContract {
	return ToolContract{ParallelMode: "read_only_ok"}
}
func (t *countingEchoTool) Run(_ context.Context, call ToolCall) ToolResult {
	t.Count++
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

func toolMessages(messages []Message) []string {
	var out []string
	for _, msg := range messages {
		if msg.Role == RoleTool {
			out = append(out, msg.Content)
		}
	}
	return out
}

func containsUserMessage(messages []Message, content string) bool {
	for _, msg := range messages {
		if msg.Role == RoleUser && strings.Contains(msg.Content, content) {
			return true
		}
	}
	return false
}

func containsSystemMessage(messages []Message, content string) bool {
	for _, msg := range messages {
		if msg.Role == RoleSystem && strings.Contains(msg.Content, content) {
			return true
		}
	}
	return false
}

type captureContextModel struct {
	systemPrompt string
	messages     []Message
}

func (m *captureContextModel) Next(_ context.Context, ctx Context) (Message, error) {
	m.systemPrompt = ctx.SystemPrompt
	m.messages = append([]Message(nil), ctx.Messages...)
	return Message{Role: RoleAssistant, Content: "ok"}, nil
}
