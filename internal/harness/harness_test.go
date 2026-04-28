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

type asyncEventRecorder struct {
	events chan AsyncResultEvent
}

func (r asyncEventRecorder) NotifyAsyncResult(_ context.Context, event AsyncResultEvent) error {
	r.events <- event
	return nil
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

func TestHarnessRecordsFailureLearning(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "ws")
	registry := tools.NewRegistry()
	h := New(workspace, session.NewStore(workspace), registry, 6)
	run := Run{
		ID:             "run_failure_learning",
		SessionKey:     "test:failure-learning",
		AgentName:      "default",
		Goal:           "研究 opencli 的用法",
		Mode:           "chat",
		Route:          "chatmodel",
		ModelName:      "aliyun-qwen",
		VisibleTools:   []string{"opencli_run", "web_search"},
		SelectedSkills: []string{"opencli"},
		Status:         "failed",
		Error:          "opencli run failed: exit status 127",
	}
	if err := h.recordFailureLearning(context.Background(), run, Request{
		SessionKey: "test:failure-learning",
		UserText:   "研究 opencli 的用法",
	}, fmt.Errorf("opencli run failed: exit status 127")); err != nil {
		t.Fatal(err)
	}
	notes, noteErr := h.Memory.Recent(context.Background(), "failures", "test:failure-learning", 5)
	if noteErr != nil {
		t.Fatal(noteErr)
	}
	if len(notes) == 0 {
		t.Fatal("expected failure note to be persisted")
	}
	if !strings.Contains(notes[0].Content, "tool_missing_binary") {
		t.Fatalf("unexpected failure note content: %s", notes[0].Content)
	}
	matches, searchErr := h.Memory.SearchWiki(context.Background(), "Failure Lesson", 5)
	if searchErr != nil {
		t.Fatal(searchErr)
	}
	if len(matches) == 0 {
		t.Fatal("expected failure lesson wiki page")
	}
	lessons, lessonErr := h.Memory.RecentLessons(context.Background(), TaskTypeLocalCLI, 5)
	if lessonErr != nil {
		t.Fatal(lessonErr)
	}
	if len(lessons) == 0 || lessons[0].FailureKind != "tool_missing_binary" {
		t.Fatalf("expected structured lesson record, got %#v", lessons)
	}
	hint := h.buildFailureAvoidanceHint("研究 opencli 的用法", []string{"opencli"}, []string{"opencli_run", "web_search"})
	if !strings.Contains(hint, "FAILURE_MEMORY") {
		t.Fatalf("expected failure hint, got %q", hint)
	}
	if !strings.Contains(hint, "tool=opencli_run") || !strings.Contains(hint, "provider=opencli") {
		t.Fatalf("expected precise tool/provider recall, got %q", hint)
	}
}

func TestRuntimeRecoveryMapping(t *testing.T) {
	h := New(t.TempDir(), nil, tools.NewRegistry(), 6)
	h.EnableEino = true
	run := Run{ID: "run_recovery", Mode: "chat", Route: "plan_execute"}
	for _, kind := range []string{"tool_missing_binary", "tool_policy_denied", "context_overflow", "timeout", "llm_throttled"} {
		if action := recoveryActionForFailure(kind, run); strings.TrimSpace(action) == "" {
			t.Fatalf("expected recovery action for %s", kind)
		}
		if !h.canRetryWithRecovery(run, kind) {
			t.Fatalf("expected retry to be allowed for %s", kind)
		}
	}
	if got := classifyTurnFailure(fmt.Errorf("context length exceeded")); got != "context_overflow" {
		t.Fatalf("unexpected context failure kind: %s", got)
	}
}

func TestClassifyTaskTypeFromGoal(t *testing.T) {
	cases := []struct {
		goal string
		want string
	}{
		{goal: "请调研 2026 AI 趋势并整理报告", want: TaskTypeResearch},
		{goal: "根据日志定位为什么定时任务失败", want: TaskTypeDiagnose},
		{goal: "你看看 zsh 下 lark-cli 怎么用", want: TaskTypeLocalCLI},
		{goal: "列出来现在的定时任务", want: TaskTypeSchedule},
		{goal: "帮我修改这段代码并补测试", want: TaskTypeCodeWrite},
		{goal: "解释一下这个仓库里的 harness 实现", want: TaskTypeCodeRead},
		{goal: "把这次排查沉淀进记忆", want: TaskTypeMemory},
		{goal: "你好", want: TaskTypeAnswer},
	}
	for _, tc := range cases {
		if got := classifyTaskTypeFromGoal(tc.goal); got != tc.want {
			t.Fatalf("goal %q classified as %q, want %q", tc.goal, got, tc.want)
		}
	}
}

func TestSelectEinoRouteUsesTaskType(t *testing.T) {
	h := New(t.TempDir(), nil, tools.NewRegistry(), 6)
	if route := h.selectEinoRoute(Request{UserText: "根据日志定位为什么任务失败"}); route != "plan_execute" {
		t.Fatalf("expected diagnose tasks to prefer plan_execute, got %s", route)
	}
	if route := h.selectEinoRoute(Request{UserText: "列出来现在的定时任务"}); route != "chatmodel" {
		t.Fatalf("expected schedule tasks to stay on chatmodel, got %s", route)
	}
}

func TestHarnessStartAssignsTaskType(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "ws")
	server := newOpenAICompatTestServer(t, func(messages []map[string]any) map[string]any {
		return map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": "分析完成",
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
		SessionKey: "test:task-type",
		UserText:   "请调研 AI 趋势并总结",
		Mode:       "chat",
		Arguments:  map[string]any{"runtime_route": "chatmodel"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if run.TaskType != TaskTypeResearch {
		t.Fatalf("unexpected task type: %#v", run)
	}

	summary, ok, err := h.Memory.ReadSessionSummary(context.Background(), "test:task-type")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected session summary")
	}
	if got := fmt.Sprint(summary.Metadata["task_type"]); got != TaskTypeResearch {
		t.Fatalf("unexpected summary task_type metadata: %#v", summary.Metadata)
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
	recorder := asyncEventRecorder{events: make(chan AsyncResultEvent, 1)}
	h.RegisterChannelNotifier("test-channel", recorder)

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
		ThreadID:   "thread-async",
		UserID:     "user-async",
		Channel:    "test-channel",
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
	select {
	case event := <-recorder.events:
		if event.RunID != childRunID || event.ThreadID != "thread-async" || event.Channel != "test-channel" {
			t.Fatalf("unexpected async notification event: %#v", event)
		}
		if event.Result != "child completed" || event.Status != "completed" {
			t.Fatalf("unexpected async notification result: %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async notification")
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
	if len(run.Events) == 0 {
		t.Fatalf("expected unified run events")
	}
	if len(run.LearningProposals) == 0 {
		t.Fatalf("expected learning proposals")
	}
	record, ok, err := h.Memory.GetTaskRecord(context.Background(), run.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || record.TaskID != run.TaskID {
		t.Fatalf("expected canonical task record, got %#v", record)
	}
	if record.Completion.Status == "" || record.Completion.Summary == "" {
		t.Fatalf("expected completion contract on task record, got %#v", record.Completion)
	}
	report := FormatLearnReport(run)
	for _, want := range []string{"正式复盘报告", "任务目标:", "任务分解:", "执行时间线:", "工具/模型调用:", "最终结果:", "下次策略:", "已沉淀记忆:"} {
		if !strings.Contains(report, want) {
			t.Fatalf("learn report missing %q:\n%s", want, report)
		}
	}
	path := filepath.Join(workspace, "memory", "learning", "reports", run.ID+".md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected learn proposal file: %v", err)
	}
	if !strings.Contains(string(data), "任务目标:") || !strings.Contains(string(data), "最终结果:") {
		t.Fatalf("unexpected learn proposal content: %s", string(data))
	}
	applied, err := h.ApplyLearningProposal(context.Background(), run.ID, run.LearningProposals[0].ID, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || applied[0].Status != "applied" {
		t.Fatalf("unexpected applied proposals: %#v", applied)
	}
	if _, err := os.Stat(filepath.Join(workspace, applied[0].TargetPath)); err != nil {
		t.Fatalf("expected applied learning file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "memory", "logs")); err != nil {
		t.Fatalf("expected structured logs dir: %v", err)
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
	if note.Metadata["latest_task_digest"] == "" {
		t.Fatalf("expected latest task digest metadata: %#v", note)
	}
	if note.Content == "" {
		t.Fatalf("expected non-empty summary: %#v", note)
	}
	if !strings.Contains(note.Content, "最新任务:") {
		t.Fatalf("expected task digest in summary: %s", note.Content)
	}
}

func TestHarnessWritesCompactSessionSummary(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "ws")
	server := newOpenAICompatTestServer(t, func(messages []map[string]any) map[string]any {
		return map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role": "assistant",
					"content": strings.Join([]string{
						"AI 趋势简报（实时版）",
						"",
						"| 标题 | 热度 |",
						"|---|---|",
						"| DeepSeek | 1522 万 |",
						"",
						"更多细节正文内容……",
					}, "\n"),
				},
			}},
		}
	})
	defer server.Close()
	registry := tools.NewRegistry()
	h := New(workspace, session.NewStore(workspace), registry, 6)
	h.UseEinoRuntime(testEinoConfig(server.URL))

	if _, err := h.Start(context.Background(), Request{
		SessionKey: "test:summary:compact",
		UserText:   "请给我一份 AI 趋势简报",
		Mode:       "chat",
	}, nil); err != nil {
		t.Fatal(err)
	}
	note, ok, err := h.Memory.ReadSessionSummary(context.Background(), "test:summary:compact")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected compact session summary")
	}
	if strings.Contains(note.Content, "|---|---|") {
		t.Fatalf("expected summary to avoid table body: %s", note.Content)
	}
	if len(note.Content) > 500 {
		t.Fatalf("expected compact summary, got len=%d content=%s", len(note.Content), note.Content)
	}
	if strings.Contains(note.Content, "最近结果:") {
		t.Fatalf("expected summary to use task digests instead of old result line: %s", note.Content)
	}
}

func TestFormatLearnReportAvoidsRepeatingLargeResultManyTimes(t *testing.T) {
	longResult := strings.Join([]string{
		"AI 趋势简报（实时版）",
		"",
		"第一部分：模型成本下降。",
		"第二部分：开发范式转移。",
	}, "\n")
	run := Run{
		ID:        "run_dedupe",
		Status:    "completed",
		Route:     "chatmodel",
		ModelName: "aliyun-qwen",
		Goal:      "给我发一份 ai 趋势简报",
		Result:    longResult,
		Events: []RunEvent{
			{Kind: "callback_model_end", Phase: "model", Status: "completed", Output: longResult, StartedAt: time.Now()},
			{Kind: "llm", Phase: "model", Status: "completed", Output: longResult, StartedAt: time.Now()},
			{Kind: "run_end", Phase: "runtime", Status: "completed", Output: longResult, StartedAt: time.Now()},
		},
	}
	report := FormatLearnReport(run)
	if got := strings.Count(report, "AI 趋势简报（实时版）"); got > 2 {
		t.Fatalf("expected learn report to avoid repeated large result, got count=%d\n%s", got, report)
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

func TestHarnessClearsStaleSessionBusyLock(t *testing.T) {
	workspace := t.TempDir()
	server := newOpenAICompatTestServer(t, func(messages []map[string]any) map[string]any {
		return map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": "stale lock cleared",
				},
			}},
		}
	})
	defer server.Close()
	h := New(workspace, session.NewStore(workspace), tools.NewRegistry(), 6)
	h.UseEinoRuntime(testEinoConfig(server.URL))
	h.inflight.Store("test:stale", time.Now().Add(-staleSessionBusyTTL-time.Minute))

	run, err := h.Start(context.Background(), Request{
		SessionKey: "test:stale",
		UserText:   "hello after stale lock",
		Mode:       "chat",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "completed" {
		t.Fatalf("expected completed run after stale lock cleanup, got %#v", run)
	}
}

func TestHarnessListTaskRunsGroupsByTaskID(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "ws")
	server := newOpenAICompatTestServer(t, func(messages []map[string]any) map[string]any {
		return map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": "grouped result",
				},
			}},
		}
	})
	defer server.Close()
	h := New(workspace, session.NewStore(workspace), tools.NewRegistry(), 6)
	h.UseEinoRuntime(testEinoConfig(server.URL))

	if _, err := h.Start(context.Background(), Request{
		SessionKey: "test:tasks",
		UserText:   "第一个任务",
		Mode:       "chat",
		Arguments:  map[string]any{"task_id": "task-alpha"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Start(context.Background(), Request{
		SessionKey: "test:tasks",
		UserText:   "继续第一个任务",
		Mode:       "chat",
		Arguments:  map[string]any{"task_id": "task-alpha"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Start(context.Background(), Request{
		SessionKey: "test:tasks",
		UserText:   "第二个任务",
		Mode:       "chat",
		Arguments:  map[string]any{"task_id": "task-beta"},
	}, nil); err != nil {
		t.Fatal(err)
	}

	runs, err := h.ListTaskRuns(context.Background(), "test:tasks", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected 2 grouped task runs, got %#v", runs)
	}
	ids := []string{runs[0].TaskID, runs[1].TaskID}
	if !(containsString(ids, "task-alpha") && containsString(ids, "task-beta")) {
		t.Fatalf("unexpected task ids: %#v", ids)
	}
}

func TestHarnessListTaskRunsRespectsSessionResetBoundary(t *testing.T) {
	workspace := t.TempDir()
	store := session.NewStore(workspace)
	server := newOpenAICompatTestServer(t, func(messages []map[string]any) map[string]any {
		return map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": "task result",
				},
			}},
		}
	})
	defer server.Close()
	h := New(workspace, store, tools.NewRegistry(), 6)
	h.UseEinoRuntime(testEinoConfig(server.URL))

	if _, err := h.Start(context.Background(), Request{
		SessionKey: "test:reset-boundary",
		UserText:   "旧任务",
		Mode:       "chat",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Reset("test:reset-boundary"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Start(context.Background(), Request{
		SessionKey: "test:reset-boundary",
		UserText:   "新任务",
		Mode:       "chat",
	}, nil); err != nil {
		t.Fatal(err)
	}

	runs, err := h.ListTaskRuns(context.Background(), "test:reset-boundary", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected only post-reset task runs, got %#v", runs)
	}
	if !strings.Contains(runs[0].Goal, "新任务") {
		t.Fatalf("expected post-reset run only, got %#v", runs[0])
	}
}

func TestHarnessListTaskRunsIncludesPersistedRunsAfterRestart(t *testing.T) {
	workspace := t.TempDir()
	store := session.NewStore(workspace)
	server := newOpenAICompatTestServer(t, func(messages []map[string]any) map[string]any {
		return map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": "持久化任务结果",
				},
			}},
		}
	})
	defer server.Close()

	first := New(workspace, store, tools.NewRegistry(), 6)
	first.UseEinoRuntime(testEinoConfig(server.URL))
	run, err := first.Start(context.Background(), Request{
		SessionKey: "test:persisted:tasks",
		UserText:   "第一次任务",
		Mode:       "chat",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	second := New(workspace, store, tools.NewRegistry(), 6)
	runs, err := second.ListTaskRuns(context.Background(), "test:persisted:tasks", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected persisted task run after restart, got %#v", runs)
	}
	if runs[0].ID != run.ID {
		t.Fatalf("expected persisted run %s, got %#v", run.ID, runs[0])
	}
}
