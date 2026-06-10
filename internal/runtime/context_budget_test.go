package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/session"
)

func TestContextBudgetCompactsOverSoftLimit(t *testing.T) {
	cfg := config.DefaultRoot()
	cfg.Execution.ContextBudget.RecentTurns = 2
	cfg.Execution.ContextBudget.ToolResultTargetTokens = 128
	messages := []agentcore.Message{
		{Role: agentcore.RoleUser, Content: strings.Repeat("old user ", 200)},
		{Role: agentcore.RoleAssistant, Content: strings.Repeat("old assistant ", 400)},
		{Role: agentcore.RoleTool, ToolCallID: "call_1", Content: strings.Repeat("tool output\n", 800)},
		{Role: agentcore.RoleUser, Content: strings.Repeat("older user ", 200)},
		{Role: agentcore.RoleAssistant, Content: strings.Repeat("older assistant ", 400)},
		{Role: agentcore.RoleUser, Content: strings.Repeat("recent user ", 100)},
		{Role: agentcore.RoleAssistant, Content: strings.Repeat("recent assistant ", 100)},
		{Role: agentcore.RoleUser, Content: "current request"},
	}
	result := packMessagesForContextBudget(contextBudgetInput{
		Config:       &cfg,
		ModelConfig:  config.ModelConfig{ContextWindow: 2200, MaxTokens: 200},
		SystemPrompt: "system",
		Messages:     messages,
	})
	if !result.Compacted {
		t.Fatalf("expected compaction, got %#v", result)
	}
	if result.SavedEstimatedTokens <= 0 || result.CompactedToolResults == 0 || result.DroppedMessages == 0 {
		t.Fatalf("expected savings, tool compaction, and dropped messages, got %#v", result)
	}
}

func TestContextBudgetHardLimitAfterCompaction(t *testing.T) {
	cfg := config.DefaultRoot()
	cfg.Execution.ContextBudget.RecentTurns = 1
	messages := []agentcore.Message{
		{Role: agentcore.RoleUser, Content: strings.Repeat("x", 9000)},
	}
	result := packMessagesForContextBudget(contextBudgetInput{
		Config:       &cfg,
		ModelConfig:  config.ModelConfig{ContextWindow: 1000, MaxTokens: 100},
		SystemPrompt: strings.Repeat("s", 9000),
		Messages:     messages,
	})
	if !result.HardLimitExceeded {
		t.Fatalf("expected hard limit exceeded, got %#v", result)
	}
}

func TestContextBudgetSelectsRelevantVisibleTools(t *testing.T) {
	cfg := config.DefaultRoot()
	cfg.Execution.ContextBudget.MaxVisibleTools = 3
	tools := []agentcore.Tool{
		runtimeNamedTool{name: "file.read"},
		runtimeNamedTool{name: "web.search"},
		runtimeNamedTool{name: "terminal.run"},
		runtimeNamedTool{name: "schedule.manage"},
		runtimeNamedTool{name: "toolresult.read"},
	}
	result := packMessagesForContextBudget(contextBudgetInput{
		Config:      &cfg,
		ModelConfig: config.ModelConfig{ContextWindow: 32000, MaxTokens: 1000},
		Messages: []agentcore.Message{
			{Role: agentcore.RoleUser, Content: "search latest weather and read raw_ref=tool-result:abc"},
		},
		Tools: tools,
		Contract: session.TaskContract{
			RequiresTools: true,
			RequiredTools: []string{"web.search"},
		},
	})
	names := strings.Join(result.ToolNames, ",")
	for _, want := range []string{"web.search", "toolresult.read"} {
		if !strings.Contains(names, want) {
			t.Fatalf("expected visible tool %s in %q", want, names)
		}
	}
	if result.VisibleTools > 3 || result.HiddenTools == 0 {
		t.Fatalf("unexpected visible tool stats %#v", result)
	}
}

func TestToolCompactionSpecializesFileContent(t *testing.T) {
	fileContent := strings.Repeat("package main\n", 400) + "error: important failure\n" + strings.Repeat("tail\n", 400)
	compacted, compressor := compactToolContent("file.read", fileContent, 1200)
	if compressor != "file_read" || !strings.Contains(compacted, "[model compacted file content]") || !strings.Contains(compacted, "important failure") {
		t.Fatalf("unexpected file compaction compressor=%q content=%q", compressor, compacted)
	}
}

func TestSimpleTaskSkipsContractModel(t *testing.T) {
	rt := newTestRuntime(t)
	counter := &countingModel{text: `{"summary":"should not run","requires_tools":true}`}
	rt.ContractModel = counter
	rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "hello"}, rt.Tools)
	resp, err := rt.Handle(context.Background(), inbound("cli:simple-contract", "hello?"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Text != "hello" {
		t.Fatalf("reply = %q", resp.Reply.Text)
	}
	if counter.calls != 0 {
		t.Fatalf("expected contract model to be skipped, got %d calls", counter.calls)
	}
	state := loadState(t, rt, "cli:simple-contract")
	if len(state.Tasks) != 1 || state.Tasks[0].Execution.Contract == nil || state.Tasks[0].Execution.Contract.RequiresTools {
		t.Fatalf("expected non-tool fallback contract, got %#v", state.Tasks)
	}
}

func TestTraceSummaryContextBudgetTelemetry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trace.jsonl")
	if err := AppendTraceEvent(path, map[string]any{"type": "context_budget_estimated", "estimated_input_tokens": 1000}); err != nil {
		t.Fatal(err)
	}
	if err := AppendTraceEvent(path, map[string]any{"type": "context_budget_compacted", "saved_estimated_tokens": 300, "compacted_messages": 2, "compacted_tool_results": 1}); err != nil {
		t.Fatal(err)
	}
	if err := AppendTraceEvent(path, map[string]any{"type": "model_usage", "requests": 1, "cache_hits": 1, "cache_read_tokens": 120, "cache_write_tokens": 40}); err != nil {
		t.Fatal(err)
	}
	summary, err := SummarizeTrace(path)
	if err != nil {
		t.Fatal(err)
	}
	if summary.EstimatedInputTokens != 1000 || summary.SavedEstimatedTokens != 300 || summary.CompactedMessages != 2 || summary.CompactedToolResults != 1 {
		t.Fatalf("unexpected summary %#v", summary)
	}
	if summary.CacheHits != 1 || summary.CacheReadTokens != 120 || summary.CacheWriteTokens != 40 {
		t.Fatalf("unexpected cache summary %#v", summary)
	}
}

func TestRuntimeBudgetsEveryModelTurn(t *testing.T) {
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "web.search", content: strings.Repeat("search result\n", 1200)})
	rt.Tools = registry
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{ID: "call_1", Name: "web.search", Args: map[string]any{"query": "one"}}}},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{ID: "call_2", Name: "web.search", Args: map[string]any{"query": "two"}}}},
		{Role: agentcore.RoleAssistant, Content: "done"},
	}}, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"search twice","requires_tools":true,"required_tools":["web.search"],"required_evidence":[{"kind":"external_fact","tool":"web.search","description":"search evidence"}],"expected_outcome":"answer","completion_policy":"use evidence"}`}
	resp, err := rt.Handle(context.Background(), inbound("cli:budget-turns", "search the web twice"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Style != "input_required" {
		t.Fatalf("expected plan review, got %#v", resp)
	}
	resp, err = rt.Handle(context.Background(), inbound("cli:budget-turns", "1"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed {
		t.Fatalf("unexpected failure %#v", resp)
	}
	data, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), `"type":"context_budget_estimated"`); got < 3 {
		t.Fatalf("expected budget telemetry for each model turn, got %d:\n%s", got, string(data))
	}
}

func TestRuntimeModelSeesFilteredTools(t *testing.T) {
	rt := newTestRuntime(t)
	cfg := rt.Config
	cfg.Execution.ContextBudget.MaxVisibleTools = 2
	registry := agentcore.NewToolRegistry()
	for _, name := range []string{"file.read", "web.search", "terminal.run", "schedule.manage", "task.search"} {
		registry.Register(runtimeNamedTool{name: name, content: "ok"})
	}
	rt.Tools = registry
	capture := &captureToolsModel{text: "done"}
	rt.Pool.agents["main"] = agentcore.NewAgent(capture, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"search","requires_tools":true,"required_tools":["web.search"],"required_evidence":[{"kind":"external_fact","tool":"web.search","description":"search evidence"}],"expected_outcome":"answer","completion_policy":"use evidence"}`}
	resp, err := rt.Handle(context.Background(), inbound("cli:filtered-tools", "search latest release"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Style != "input_required" {
		t.Fatalf("expected plan review, got %#v", resp)
	}
	resp, err = rt.Handle(context.Background(), inbound("cli:filtered-tools", "1"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed {
		t.Fatalf("unexpected failure %#v", resp)
	}
	if len(capture.toolNames) == 0 || len(capture.toolNames) > 2 {
		t.Fatalf("expected filtered tools, got %#v", capture.toolNames)
	}
	if !containsString(capture.toolNames, "web.search") {
		t.Fatalf("expected web.search in filtered tools, got %#v", capture.toolNames)
	}
}

func TestRawToolResultStillStoredOutsidePrompt(t *testing.T) {
	home := t.TempDir()
	result := compactToolResultForModel(agentcore.ToolCall{ID: "call_1", Name: "web.search"}, agentcore.ToolResult{
		ToolCallID: "call_1",
		Content:    strings.Repeat("FILE: README.md\n", modelToolContentLimit),
	}, home, "trace-raw")
	rawPath, ok := result.Evidence["raw_path"].(string)
	if !ok || rawPath == "" {
		t.Fatalf("expected raw path evidence, got %#v", result.Evidence)
	}
	data, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "FILE: README.md") || !strings.Contains(result.Content, "raw_ref=tool-result:") {
		t.Fatalf("expected raw output preserved and prompt to contain raw_ref")
	}
}

type countingModel struct {
	text  string
	calls int
}

type captureToolsModel struct {
	text      string
	toolNames []string
	calls     int
	toolSent  bool
}

func (m *captureToolsModel) Next(_ context.Context, ctx agentcore.Context) (agentcore.Message, error) {
	m.calls++
	if m.toolNames == nil {
		m.toolNames = toolNames(ctx.Tools)
	}
	if ctx.Tools != nil && len(ctx.Tools) > 0 && !m.toolSent {
		m.toolSent = true
		return agentcore.Message{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{ID: "call_web", Name: "web.search", Args: map[string]any{"query": "test"}}}}, nil
	}
	return agentcore.Message{Role: agentcore.RoleAssistant, Content: m.text}, nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (m *countingModel) Next(context.Context, agentcore.Context) (agentcore.Message, error) {
	m.calls++
	return agentcore.Message{Role: agentcore.RoleAssistant, Content: m.text}, nil
}

func TestShouldSkipTaskContractModelKeepsToolTasks(t *testing.T) {
	for _, text := range []string{"read README.md", "latest weather today", "fix the code"} {
		if shouldSkipTaskContractModel(text, text) {
			t.Fatalf("expected tool-like task not to skip: %q", text)
		}
	}
}

func TestContextBudgetPreservesAllContractToolsOverLimit(t *testing.T) {
	cfg := config.DefaultRoot()
	cfg.Execution.ContextBudget.MaxVisibleTools = 2
	tools := []agentcore.Tool{
		runtimeNamedTool{name: "file.read"},
		runtimeNamedTool{name: "terminal.run"},
		runtimeNamedTool{name: "web.search"},
		runtimeNamedTool{name: "schedule.manage"},
		runtimeNamedTool{name: "toolresult.read"},
	}
	result := packMessagesForContextBudget(contextBudgetInput{
		Config:      &cfg,
		ModelConfig: config.ModelConfig{ContextWindow: 32000, MaxTokens: 1000},
		Messages:    []agentcore.Message{{Role: agentcore.RoleUser, Content: "check service and search web"}},
		Tools:       tools,
		Contract: session.TaskContract{
			RequiresTools: true,
			RequiredTools: []string{"terminal.run", "web.search", "schedule.manage"},
		},
	})
	for _, want := range []string{"terminal.run", "web.search", "schedule.manage"} {
		found := false
		for _, name := range result.ToolNames {
			if name == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("contract tool %s missing from visible tools %v", want, result.ToolNames)
		}
	}
}

func TestAcceptToolResultUsesEffectiveRiskForScheduleManage(t *testing.T) {
	scheduleTool := runtimeNamedTool{name: "schedule.manage"}

	callList := agentcore.ToolCall{Name: "schedule.manage", Args: map[string]any{"action": "list", "id": "sch_1"}}
	_, evidence := acceptToolResult(scheduleTool, callList, agentcore.ToolResult{Content: "ok", Evidence: map[string]any{"count": 0}})
	if risk, _ := evidence["risk"].(string); risk != string(agentcore.RiskSafeRead) {
		t.Fatalf("action=list expected risk=safe_read, got %q in evidence %#v", risk, evidence)
	}
	if evidence["mutation"] != false {
		t.Fatalf("action=list expected mutation=false, got evidence %#v", evidence)
	}

	callDelete := agentcore.ToolCall{Name: "schedule.manage", Args: map[string]any{"action": "delete", "id": "sch_1"}}
	_, evidence = acceptToolResult(scheduleTool, callDelete, agentcore.ToolResult{Content: "deleted", Evidence: map[string]any{"deleted": true}})
	if risk, _ := evidence["risk"].(string); risk != string(agentcore.RiskDangerous) {
		t.Fatalf("action=delete expected risk=dangerous, got %q in evidence %#v", risk, evidence)
	}
	if evidence["mutation"] != true {
		t.Fatalf("action=delete expected mutation=true, got evidence %#v", evidence)
	}

	callCreate := agentcore.ToolCall{Name: "schedule.manage", Args: map[string]any{"action": "create", "text": "x", "run_at": "2026-06-15T10:00:00Z"}}
	_, evidence = acceptToolResult(scheduleTool, callCreate, agentcore.ToolResult{Content: "scheduled", Evidence: map[string]any{"id": "sch_x", "status": "active"}})
	if risk, _ := evidence["risk"].(string); risk != string(agentcore.RiskGuardedMutation) {
		t.Fatalf("action=create expected risk=guarded_mutation, got %q in evidence %#v", risk, evidence)
	}
}
