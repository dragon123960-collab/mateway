package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/memory"
	"github.com/dongping/mateway/internal/provisioning"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/tools"
)

type stubTool struct{}

func (stubTool) Spec() tools.Spec { return tools.Spec{Name: "echo", Kind: tools.KindBuiltin} }
func (stubTool) Invoke(_ context.Context, call tools.Call) (tools.Result, error) {
	return tools.Result{Output: call.Arguments}, nil
}

type stubProvider struct{}

func (stubProvider) Tools(context.Context, tools.Scope) ([]tools.Tool, error) {
	return []tools.Tool{stubTool{}}, nil
}

type agentProvider struct{}

func (agentProvider) Tools(context.Context, tools.Scope) ([]tools.Tool, error) {
	return []tools.Tool{spawnStubTool{name: "spawn"}, spawnStubTool{name: "wait_agent"}, spawnStubTool{name: "web_search"}}, nil
}

type spawnStubTool struct{ name string }

func (t spawnStubTool) Spec() tools.Spec {
	spec := tools.Spec{Name: t.name, Kind: tools.KindBuiltin}
	if t.name == "web_search" {
		spec.InputSchema = json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`)
	}
	return spec
}
func (t spawnStubTool) Invoke(_ context.Context, call tools.Call) (tools.Result, error) {
	return tools.Result{Output: call.Arguments}, nil
}

type schemaTool struct {
	name      string
	riskLevel string
}

func (t schemaTool) Spec() tools.Spec {
	return tools.Spec{
		Name:        t.name,
		Kind:        tools.KindBuiltin,
		RiskLevel:   t.riskLevel,
		Description: "test tool",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`),
	}
}

func (t schemaTool) Invoke(_ context.Context, call tools.Call) (tools.Result, error) {
	var args map[string]any
	_ = json.Unmarshal(call.Arguments, &args)
	data, _ := json.Marshal(map[string]any{"tool": t.name, "args": args})
	return tools.Result{Output: data}, nil
}

type schemaProvider struct {
	tools []tools.Tool
}

func (p schemaProvider) Tools(context.Context, tools.Scope) ([]tools.Tool, error) {
	return p.tools, nil
}

func TestHarnessChatAndToolModes(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "ws")
	server := newOpenAICompatTestServer(t, func(messages []map[string]any) map[string]any {
		return map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": "world",
				},
			}},
		}
	})
	defer server.Close()
	registry := tools.NewRegistry()
	registry.Register(stubProvider{})
	registry.Register(agentProvider{})
	h := New(workspace, session.NewStore(workspace), registry, 6)
	h.UseEinoRuntime(testEinoConfig(server.URL))

	run, err := h.Start(context.Background(), Request{
		SessionKey: "test:1",
		UserText:   "hello",
		Mode:       "chat",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if run.Result != "world" {
		t.Fatalf("unexpected run result: %#v", run)
	}
	if len(run.Steps) == 0 || run.Steps[0].Kind != "dev_plan" {
		t.Fatalf("expected initial dev_plan step, got %#v", run.Steps)
	}

	run, err = h.Start(context.Background(), Request{
		SessionKey: "test:1",
		Mode:       "tool",
		ToolName:   "echo",
		Arguments:  map[string]any{"ok": true},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(run.Result), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["ok"] != true {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	if _, err := os.Stat(filepath.Join(workspace, "memory", "runs", run.ID+".json")); err != nil {
		t.Fatalf("expected run file to exist: %v", err)
	}
}

func TestHarnessSpawnSyncAndAsync(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "ws")
	server := newOpenAICompatTestServer(t, func(messages []map[string]any) map[string]any {
		return map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": "child completed",
				},
			}},
		}
	})
	defer server.Close()
	if err := os.MkdirAll(filepath.Join(workspace, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "agents", "worker.md"), []byte(`---
name: worker
can_spawn: false
async_allowed: true
---

Worker agent.
`), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	registry.Register(agentProvider{})
	h := New(workspace, session.NewStore(workspace), registry, 6)
	h.UseEinoRuntime(testEinoConfig(server.URL))

	replyFn := func(ctx context.Context, history []HistoryMessage, userText string) (string, error) {
		return "child:" + userText, nil
	}

	syncRun, err := h.Start(context.Background(), Request{
		SessionKey: "test:spawn",
		Mode:       "tool",
		ToolName:   "spawn",
		Arguments: map[string]any{
			"agent_name": "worker",
			"user_text":  "draft summary",
		},
	}, replyFn)
	if err != nil {
		t.Fatal(err)
	}
	var syncPayload map[string]any
	if err := json.Unmarshal([]byte(syncRun.Result), &syncPayload); err != nil {
		t.Fatal(err)
	}
	if syncPayload["status"] != "completed" {
		t.Fatalf("unexpected sync payload: %#v", syncPayload)
	}

	asyncRun, err := h.Start(context.Background(), Request{
		SessionKey: "test:spawn",
		Mode:       "tool",
		ToolName:   "spawn",
		Arguments: map[string]any{
			"agent_name": "worker",
			"user_text":  "background task",
			"async":      true,
		},
	}, replyFn)
	if err != nil {
		t.Fatal(err)
	}
	var asyncPayload map[string]any
	if err := json.Unmarshal([]byte(asyncRun.Result), &asyncPayload); err != nil {
		t.Fatal(err)
	}
	childRunID, _ := asyncPayload["run_id"].(string)
	if childRunID == "" {
		t.Fatalf("unexpected async payload: %#v", asyncPayload)
	}

	var child Run
	for i := 0; i < 20; i++ {
		var ok bool
		child, ok = h.GetRun(context.Background(), childRunID)
		if ok && child.Status == "completed" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if child.Status != "completed" {
		t.Fatalf("expected async child to complete, got %#v", child)
	}

	waitRun, err := h.Start(context.Background(), Request{
		SessionKey: "test:spawn",
		Mode:       "tool",
		ToolName:   "wait_agent",
		Arguments: map[string]any{
			"run_id": childRunID,
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var waitPayload map[string]any
	if err := json.Unmarshal([]byte(waitRun.Result), &waitPayload); err != nil {
		t.Fatal(err)
	}
	if waitPayload["status"] != "completed" {
		t.Fatalf("unexpected wait payload: %#v", waitPayload)
	}
}

func TestHarnessAppliesAgentCapabilities(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "ws")
	if err := os.MkdirAll(filepath.Join(workspace, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := os.WriteFile(filepath.Join(workspace, "agents", "limited.md"), []byte(`---
name: limited
builtin_tools:
  - read_file
allowed_skills:
  - echo
can_spawn: false
async_allowed: false
---

Limited agent.
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	registry.Register(stubProvider{})
	h := New(workspace, session.NewStore(workspace), registry, 6)

	specs, err := h.ListVisibleTools(context.Background(), tools.Scope{AgentName: "limited"})
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 0 {
		t.Fatalf("expected no visible tools from stub provider for limited agent, got %#v", specs)
	}

	_, err = h.Start(context.Background(), Request{
		SessionKey: "test:cap",
		AgentName:  "limited",
		Mode:       "tool",
		ToolName:   "spawn",
		Arguments: map[string]any{
			"user_text": "blocked",
		},
	}, nil)
	if err == nil {
		t.Fatal("expected spawn to be denied for limited agent")
	}
}

func TestHarnessEinoChatToolLoop(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "ws")
	server := newOpenAICompatTestServer(t, func(messages []map[string]any) map[string]any {
		if hasToolMessage(messages) {
			return map[string]any{
				"choices": []map[string]any{{
					"message": map[string]any{
						"role":    "assistant",
						"content": "done after tool",
					},
				}},
			}
		}
		return map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": "",
					"tool_calls": []map[string]any{{
						"id":   "call_1",
						"type": "function",
						"function": map[string]any{
							"name":      "echo",
							"arguments": `{"text":"hello from tool"}`,
						},
					}},
				},
			}},
		}
	})
	defer server.Close()

	registry := tools.NewRegistry()
	registry.Register(schemaProvider{tools: []tools.Tool{schemaTool{name: "echo"}}})
	h := New(workspace, session.NewStore(workspace), registry, 6)
	h.UseEinoRuntime(testEinoConfig(server.URL))

	run, err := h.Start(context.Background(), Request{
		SessionKey: "test:eino:tool",
		UserText:   "please use a tool",
		Mode:       "chat",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if run.Result != "done after tool" {
		t.Fatalf("unexpected eino result: %#v", run)
	}
	if len(run.Steps) == 0 {
		t.Fatalf("expected eino steps to be recorded: %#v", run)
	}
	if !hasRunStep(run.Steps, "callback_model_start") || !hasRunStep(run.Steps, "callback_tool_start") || !hasRunStep(run.Steps, "tool_choice") {
		t.Fatalf("expected callback steps to be recorded: %#v", run.Steps)
	}
}

func TestHarnessWritesLearnProposalForHighValueChat(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "ws")
	server := newOpenAICompatTestServer(t, func(messages []map[string]any) map[string]any {
		return map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": "已经整理出活动时间、地点和报名建议。",
				},
			}},
		}
	})
	defer server.Close()
	registry := tools.NewRegistry()
	h := New(workspace, session.NewStore(workspace), registry, 6)
	h.UseEinoRuntime(testEinoConfig(server.URL))

	run, err := h.Start(context.Background(), Request{
		SessionKey: "test:learn:proposal",
		ThreadID:   "thread:proposal",
		AgentName:  "default",
		UserText:   "请调研北京 AI 活动并整理结论",
		Mode:       "chat",
		Arguments:  map[string]any{"runtime_route": "chatmodel"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hasRunStep(run.Steps, "learn_proposal") {
		t.Fatalf("expected learn_proposal step, got %#v", run.Steps)
	}
	path := filepath.Join(workspace, "memory", "wiki", "notes", "learn-proposal-"+run.ID+".md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected learn proposal file: %v", err)
	}
	if !strings.Contains(string(data), "## Goal") || !strings.Contains(string(data), "最终") && !strings.Contains(string(data), "Final Output") {
		t.Fatalf("unexpected learn proposal content: %s", string(data))
	}
}

func TestHarnessEinoApprovalResume(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "ws")
	server := newOpenAICompatTestServer(t, func(messages []map[string]any) map[string]any {
		if hasToolMessage(messages) {
			return map[string]any{
				"choices": []map[string]any{{
					"message": map[string]any{
						"role":    "assistant",
						"content": "approved and completed",
					},
				}},
			}
		}
		return map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": "",
					"tool_calls": []map[string]any{{
						"id":   "call_approval",
						"type": "function",
						"function": map[string]any{
							"name":      "needs_approval",
							"arguments": `{"text":"secret"}`,
						},
					}},
				},
			}},
		}
	})
	defer server.Close()

	registry := tools.NewRegistry()
	registry.Register(schemaProvider{tools: []tools.Tool{schemaTool{name: "needs_approval", riskLevel: "medium"}}})
	h := New(workspace, session.NewStore(workspace), registry, 6)
	h.UseEinoRuntime(testEinoConfig(server.URL))
	h.ApprovalPolicy = ApprovalPolicy{RequireRiskyTools: true}

	run, err := h.Start(context.Background(), Request{
		SessionKey: "test:eino:approval",
		UserText:   "do the risky thing",
		Mode:       "chat",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "waiting_approval" {
		t.Fatalf("expected waiting approval, got %#v", run)
	}
	pending := h.ListPending("test:eino:approval")
	if len(pending) != 1 || pending[0].InterruptID == "" {
		t.Fatalf("expected interrupt-backed pending approval, got %#v", pending)
	}
	reply, err := h.ReviewPending(context.Background(), "test:eino:approval", pending[0].ID, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "approved and completed") {
		t.Fatalf("unexpected resume reply: %s", reply)
	}
}

func TestSelectEinoRoutePrefersPlanExecuteForResearchTasks(t *testing.T) {
	h := New(t.TempDir(), session.NewStore(t.TempDir()), tools.NewRegistry(), 6)
	h.EnableEino = true

	route := h.selectEinoRoute(Request{UserText: "请调研 2026 AI 趋势并整理结论"})
	if route != "plan_execute" {
		t.Fatalf("expected plan_execute route, got %s", route)
	}

	route = h.selectEinoRoute(Request{UserText: "你好，帮我简单打个招呼"})
	if route != "chatmodel" {
		t.Fatalf("expected chatmodel route, got %s", route)
	}
}

func TestSelectEinoRouteAvoidsPlanExecuteForQwen3(t *testing.T) {
	h := New(t.TempDir(), session.NewStore(t.TempDir()), tools.NewRegistry(), 6)
	h.EnableEino = true
	h.Config = config.Default()
	h.Config.ModelList = []config.ModelConfig{{
		Name:     "aliyun-qwen",
		Provider: "openai_compat",
		Model:    "qwen3.6-plus",
		APIBase:  "http://example.com",
		APIKey:   "test-key",
		Enabled:  true,
	}}
	h.Config.Models.Default = "aliyun-qwen"

	route := h.selectEinoRoute(Request{UserText: "请调研 2026 AI 趋势并整理结论"})
	if route != "chatmodel" {
		t.Fatalf("expected chatmodel route for qwen3 compatibility fallback, got %s", route)
	}
}

func TestIsPlanExecuteToolChoiceIncompatible(t *testing.T) {
	err := fmt.Errorf("<400> InternalError.Algo.InvalidParameter: The tool_choice parameter does not support being set to required or object in thinking mode")
	if !isPlanExecuteToolChoiceIncompatible(err) {
		t.Fatal("expected tool_choice incompatibility to be detected")
	}
	if isPlanExecuteToolChoiceIncompatible(fmt.Errorf("ordinary timeout")) {
		t.Fatal("did not expect unrelated errors to match tool_choice incompatibility")
	}
}

func testEinoConfig(baseURL string) config.Config {
	cfg := config.Default()
	cfg.ModelList = []config.ModelConfig{{
		Name:     "default",
		Provider: "openai_compat",
		Model:    "fake-model",
		APIBase:  baseURL,
		APIKey:   "test-key",
		Enabled:  true,
	}}
	cfg.Models.Default = "default"
	cfg.Models.RequestTimeout = 15
	return cfg
}

func newOpenAICompatTestServer(t *testing.T, fn func(messages []map[string]any) map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode request: %v\n%s", err, string(body))
		}
		rawMessages, _ := payload["messages"].([]any)
		messages := make([]map[string]any, 0, len(rawMessages))
		for _, item := range rawMessages {
			msg, _ := item.(map[string]any)
			messages = append(messages, msg)
		}
		resp := fn(messages)
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatal(err)
		}
	}))
}

func hasToolMessage(messages []map[string]any) bool {
	for _, msg := range messages {
		if fmt.Sprint(msg["role"]) == "tool" {
			return true
		}
	}
	return false
}

func hasRunStep(steps []RunStep, kind string) bool {
	for _, step := range steps {
		if step.Kind == kind {
			return true
		}
	}
	return false
}

func TestHarnessApprovalPolicy(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "ws")
	registry := tools.NewRegistry()
	registry.Register(agentProvider{})
	h := New(workspace, session.NewStore(workspace), registry, 6)
	h.ApprovalPolicy = ApprovalPolicy{RequireRiskyTools: true}

	run, err := h.Start(context.Background(), Request{
		SessionKey: "test:approve",
		Mode:       "tool",
		ToolName:   "spawn",
		Arguments:  map[string]any{"user_text": "need review"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "waiting_approval" || run.Result == "" {
		t.Fatalf("unexpected approval prompt run: %#v", run)
	}
	first, ok := h.pendingApproval("test:approve")
	if !ok {
		t.Fatal("expected pending approval to be recorded")
	}
	_, err = h.Start(context.Background(), Request{
		SessionKey: "test:approve",
		Mode:       "tool",
		ToolName:   "spawn",
		Arguments:  map[string]any{"user_text": "need review 2"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.ListPending("test:approve")) != 2 {
		t.Fatalf("expected two pending approvals, got %#v", h.ListPending("test:approve"))
	}
	reply, err := h.ReviewPending(context.Background(), "test:approve", first.ID, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reply == "" {
		t.Fatal("expected deny reply")
	}
	deniedRun, ok := h.GetRun(context.Background(), first.RunID)
	if !ok {
		t.Fatal("expected original run to remain queryable")
	}
	if deniedRun.Status != "denied" {
		t.Fatalf("expected original run to become denied, got %#v", deniedRun)
	}
	if len(deniedRun.ApprovalIDs) == 0 || deniedRun.LastApprovalID == "" {
		t.Fatalf("expected approval ids to be recorded, got %#v", deniedRun)
	}
}

func TestHarnessScheduleMutationRequiresApprovalByDefault(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "ws")
	registry := tools.NewRegistry()
	registry.Register(tools.BuiltinProvider{
		Workspace:             workspace,
		Sessions:              session.NewStore(workspace),
		Memory:                memory.Store{Workspace: workspace},
		Provisioner:           provisioning.Provisioner{Config: config.Config{App: config.AppConfig{Home: root}}},
		EnforceWorkspacePaths: false,
	})
	h := New(workspace, session.NewStore(workspace), registry, 6)
	h.ApprovalPolicy = ApprovalPolicy{RequireScheduleChange: true}

	run, err := h.Start(context.Background(), Request{
		SessionKey: "test:schedule:approval",
		Mode:       "tool",
		ToolName:   "schedule_create",
		Arguments: map[string]any{
			"name":                "follow-up",
			"kind":                "interval",
			"interval_minutes":    60,
			"prompt":              "ping me",
			"target_session_mode": "current",
			"target_agent_mode":   "current",
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "waiting_approval" {
		t.Fatalf("expected waiting_approval, got %#v", run)
	}
	if _, ok := h.pendingApproval("test:schedule:approval"); !ok {
		t.Fatal("expected pending schedule approval")
	}
}

func TestHarnessWritesSessionSummary(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "ws")
	server := newOpenAICompatTestServer(t, func(messages []map[string]any) map[string]any {
		return map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": "summary answer",
				},
			}},
		}
	})
	defer server.Close()
	registry := tools.NewRegistry()
	h := New(workspace, session.NewStore(workspace), registry, 6)
	h.UseEinoRuntime(testEinoConfig(server.URL))

	run, err := h.Start(context.Background(), Request{
		SessionKey: "test:summary",
		UserText:   "hello summary",
		Mode:       "chat",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	note, ok, err := h.Memory.ReadSessionSummary(context.Background(), "test:summary")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected session summary to exist")
	}
	if note.Metadata["run_id"] != run.ID {
		t.Fatalf("unexpected summary metadata: %#v", note)
	}
	if note.Content == "" {
		t.Fatalf("expected non-empty summary: %#v", note)
	}
}

func TestHarnessSpawnSharedCollaborationMode(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "ws")
	server := newOpenAICompatTestServer(t, func(messages []map[string]any) map[string]any {
		return map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": "shared child",
				},
			}},
		}
	})
	defer server.Close()
	if err := os.MkdirAll(filepath.Join(workspace, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "agents", "shared-worker.md"), []byte(`---
name: shared-worker
can_spawn: false
async_allowed: true
channel_visibility: shared
collaboration_mode: shared
---

Shared worker.
`), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	registry.Register(agentProvider{})
	h := New(workspace, session.NewStore(workspace), registry, 6)
	h.UseEinoRuntime(testEinoConfig(server.URL))

	run, err := h.Start(context.Background(), Request{
		SessionKey: "test:shared",
		ThreadID:   "thread-1",
		Mode:       "tool",
		ToolName:   "spawn",
		Arguments: map[string]any{
			"agent_name": "shared-worker",
			"user_text":  "join same thread",
		},
	}, func(ctx context.Context, history []HistoryMessage, userText string) (string, error) {
		return "shared child", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(run.Result), &payload); err != nil {
		t.Fatal(err)
	}
	childRunID, _ := payload["run_id"].(string)
	child, ok := h.GetRun(context.Background(), childRunID)
	if !ok {
		t.Fatal("expected child run")
	}
	if child.CollaborationMode != "shared" {
		t.Fatalf("unexpected collaboration mode: %#v", child)
	}
	if child.SessionKey != "test:shared" {
		t.Fatalf("expected shared session key, got %#v", child)
	}
}
