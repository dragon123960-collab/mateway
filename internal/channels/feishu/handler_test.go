package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/dongping/mateway/internal/config"
	agentharness "github.com/dongping/mateway/internal/harness"
	hostruntime "github.com/dongping/mateway/internal/runtime"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/skills"
	"github.com/dongping/mateway/internal/tools"
)

type stubCatalog struct {
	skills []skills.Skill
}

func (s stubCatalog) Snapshot() []skills.Skill { return s.skills }

type stubInvoker struct {
	called string
}

func (s *stubInvoker) Invoke(_ context.Context, skill skills.Skill) (hostruntime.Result, error) {
	s.called = skill.Manifest.Name
	return hostruntime.Result{Stdout: "skill-ok"}, nil
}

type roundTripper func(*http.Request) (*http.Response, error)

func (r roundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return r(req) }

func TestChallengeResponse(t *testing.T) {
	handler := Handler{
		Config:  config.FeishuConfig{VerificationToken: "vt"},
		Catalog: stubCatalog{},
		Invoker: &stubInvoker{},
	}
	body := `{"type":"url_verification","challenge":"abc","token":"vt"}`
	req := httptest.NewRequest(http.MethodPost, "/feishu/events", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"challenge":"abc"`)) {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestMessageRunSkill(t *testing.T) {
	invoker := &stubInvoker{}
	handler := Handler{
		Config:  config.FeishuConfig{VerificationToken: "vt", AppID: "cli_x", AppSecret: "secret", BaseURL: "https://open.feishu.cn", BotName: "Mateway"},
		Catalog: stubCatalog{skills: []skills.Skill{{Manifest: skills.Manifest{Name: "echo", Type: skills.TypeCLI}, Executable: true}}},
		Invoker: invoker,
		HTTPClient: &http.Client{Transport: roundTripper(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/open-apis/auth/v3/tenant_access_token/internal":
				return jsonResp(http.StatusOK, `{"code":0,"tenant_access_token":"token"}`), nil
			case "/open-apis/im/v1/messages":
				data, _ := io.ReadAll(req.Body)
				if !bytes.Contains(data, []byte("skill-ok")) {
					t.Fatalf("send body missing skill output: %s", string(data))
				}
				return jsonResp(http.StatusOK, `{"code":0}`), nil
			default:
				t.Fatalf("unexpected path: %s", req.URL.Path)
				return nil, nil
			}
		})},
	}
	event := map[string]any{
		"token": "vt",
		"header": map[string]any{
			"event_type": "im.message.receive_v1",
		},
		"event": map[string]any{
			"sender": map[string]any{
				"sender_type": "user",
			},
			"message": map[string]any{
				"message_type": "text",
				"chat_id":      "oc_test",
				"content":      `{"text":"/run echo"}`,
			},
		},
	}
	raw, _ := json.Marshal(event)
	req := httptest.NewRequest(http.MethodPost, "/feishu/events", bytes.NewReader(raw))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if invoker.called != "echo" {
		t.Fatalf("skill invoked = %q", invoker.called)
	}
}

func TestMessageUsesRunnerForNormalText(t *testing.T) {
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"llm-answer"}}]}`))
	}))
	defer llmServer.Close()

	workspace := filepath.Join(t.TempDir(), "ws")
	runner := agentharness.New(workspace, session.NewStore(workspace), tools.NewRegistry(), 6)
	cfg := config.Default()
	cfg.ModelList = []config.ModelConfig{{
		Name:     "default",
		Provider: "openai_compat",
		Model:    "demo-model",
		APIBase:  llmServer.URL,
		APIKey:   "test-key",
		Enabled:  true,
	}}
	cfg.Models.Default = "default"
	runner.UseEinoRuntime(cfg)

	handler := Handler{
		Config:  config.FeishuConfig{VerificationToken: "vt", AppID: "cli_x", AppSecret: "secret", BaseURL: "https://open.feishu.cn", BotName: "Mateway"},
		Catalog: stubCatalog{},
		Invoker: &stubInvoker{},
		Harness: runner,
		HTTPClient: &http.Client{Transport: roundTripper(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/open-apis/auth/v3/tenant_access_token/internal":
				return jsonResp(http.StatusOK, `{"code":0,"tenant_access_token":"token"}`), nil
			case "/open-apis/im/v1/messages":
				data, _ := io.ReadAll(req.Body)
				if !bytes.Contains(data, []byte("llm-answer")) {
					t.Fatalf("send body missing llm answer: %s", string(data))
				}
				return jsonResp(http.StatusOK, `{"code":0}`), nil
			default:
				t.Fatalf("unexpected path: %s", req.URL.Path)
				return nil, nil
			}
		})},
	}
	event := map[string]any{
		"token":  "vt",
		"header": map[string]any{"event_type": "im.message.receive_v1"},
		"event": map[string]any{
			"sender": map[string]any{"sender_type": "user"},
			"message": map[string]any{
				"message_type": "text",
				"chat_id":      "oc_test",
				"content":      `{"text":"hello"}`,
			},
		},
	}
	raw, _ := json.Marshal(event)
	req := httptest.NewRequest(http.MethodPost, "/feishu/events", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func jsonResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

func TestServiceIgnoresSelfMessageAndDuplicate(t *testing.T) {
	svc := &Service{}
	svc.botOpenID.Store("ou_bot")

	senderType := "app"
	if !svc.isSelfMessage("ou_bot", &larkim.EventSender{SenderType: &senderType}) {
		t.Fatal("expected self message from app sender type to be ignored")
	}
	if !svc.isDuplicateMessage("msg-1") {
		if !svc.isDuplicateMessage("msg-1") {
			t.Fatal("expected second identical message id to be treated as duplicate")
		}
	}
}

func TestServiceHandleMessageReceiveSendsAckReactionAndPlaceholder(t *testing.T) {
	var callsMu syncBuffer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"token"}`))
		case r.URL.Path == "/open-apis/im/v1/messages/om_123/reactions" && r.Method == http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			callsMu.Add("reaction:create:" + string(body))
			if strings.Contains(string(body), `"emoji_type":"EYES"`) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":190001,"msg":"invalid emoji"}`))
				return
			}
			if strings.Contains(string(body), `"emoji_type":"SMILE"`) {
				_, _ = w.Write([]byte(`{"code":0,"data":{"reaction_id":"r1","reaction_type":{"emoji_type":"SMILE"}}}`))
				return
			}
			if strings.Contains(string(body), `"emoji_type":"DONE"`) {
				_, _ = w.Write([]byte(`{"code":0,"data":{"reaction_id":"r2","reaction_type":{"emoji_type":"DONE"}}}`))
				return
			}
			if strings.Contains(string(body), `"emoji_type":"OK"`) {
				_, _ = w.Write([]byte(`{"code":0,"data":{"reaction_id":"r3","reaction_type":{"emoji_type":"OK"}}}`))
				return
			}
			t.Fatalf("unexpected reaction payload: %s", string(body))
		case r.URL.Path == "/open-apis/im/v1/messages/om_123/reactions/r1" && r.Method == http.MethodDelete:
			callsMu.Add("reaction:delete:r1")
			_, _ = w.Write([]byte(`{"code":0,"data":{"reaction_id":"r1"}}`))
		case r.URL.Path == "/open-apis/im/v1/messages" && r.Method == http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			callsMu.Add("message:" + string(body))
			_, _ = w.Write([]byte(`{"code":0}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := lark.NewClient("cli_x", "secret",
		lark.WithOpenBaseUrl(server.URL),
		lark.WithHttpClient(server.Client()))
	svc := &Service{
		Config: config.FeishuConfig{
			AppID:              "cli_x",
			AppSecret:          "secret",
			BotName:            "Mateway",
			AckTextEnabled:     boolRef(true),
			AckReactionEnabled: boolRef(true),
		},
		client: client,
	}
	userType := "user"
	msgType := larkim.MsgTypeText
	chatType := "p2p"
	chatID := "oc_test"
	msgID := "om_123"
	content := `{"text":"hello"}`
	openID := "ou_user"

	err := svc.handleMessageReceive(context.Background(), &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderType: &userType,
				SenderId:   &larkim.UserId{OpenId: &openID},
			},
			Message: &larkim.EventMessage{
				MessageId:   &msgID,
				ChatId:      &chatID,
				ChatType:    &chatType,
				MessageType: &msgType,
				Content:     &content,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got := callsMu.All()
		if len(got) >= 5 {
			if !strings.Contains(got[2], "👀 已收到，处理中") {
				t.Fatalf("expected placeholder after reaction attempts, got %v", got)
			}
			if !strings.Contains(got[3], "Mateway 已收到：hello") {
				t.Fatalf("expected final reply after placeholder, got %v", got)
			}
			joined := strings.Join(got, "\n")
			if !strings.Contains(joined, `"emoji_type":"SMILE"`) {
				t.Fatalf("expected fallback smile reaction attempt, got %v", got)
			}
			if !strings.Contains(joined, `"emoji_type":"DONE"`) && !strings.Contains(joined, `"emoji_type":"OK"`) {
				t.Fatalf("expected completion reaction attempt, got %v", got)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for async calls: %v", callsMu.All())
}

type syncBuffer struct {
	mu     sync.Mutex
	values []string
}

func (b *syncBuffer) Add(value string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.values = append(b.values, value)
}

func (b *syncBuffer) All() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.values...)
}

func boolRef(v bool) *bool { return &v }

type prefTool struct{}

func (prefTool) Spec() tools.Spec { return tools.Spec{Name: "read_file", Kind: tools.KindBuiltin} }
func (prefTool) Invoke(_ context.Context, call tools.Call) (tools.Result, error) {
	return tools.Result{Output: call.Arguments}, nil
}

type prefProvider struct{}

func (prefProvider) Tools(context.Context, tools.Scope) ([]tools.Tool, error) {
	return []tools.Tool{prefTool{}}, nil
}

type approvalTool struct{ name string }

func (t approvalTool) Spec() tools.Spec {
	return tools.Spec{Name: t.name, Kind: tools.KindBuiltin, RiskLevel: "medium"}
}
func (t approvalTool) Invoke(_ context.Context, call tools.Call) (tools.Result, error) {
	return tools.Result{Output: call.Arguments}, nil
}

type approvalProvider struct{}

func (approvalProvider) Tools(context.Context, tools.Scope) ([]tools.Tool, error) {
	return []tools.Tool{approvalTool{name: "spawn"}, approvalTool{name: "wait_agent"}}, nil
}

func TestHandlerAgentCommandPersistsPreference(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "agents", "writer.md"), []byte(`---
name: writer
builtin_tools:
  - read_file
---
writer
`), 0o644); err != nil {
		t.Fatal(err)
	}
	store := session.NewStore(workspace)
	registry := tools.NewRegistry()
	registry.Register(prefProvider{})
	runner := agentharness.New(workspace, store, registry, 6)

	handler := Handler{
		Harness: runner,
		Catalog: stubCatalog{skills: []skills.Skill{{
			Manifest: skills.Manifest{Name: "demo-skill", Description: "Demo doc skill."},
		}}},
	}
	reply := handler.handleText(context.Background(), "feishu:p2p:u1", "/agent writer")
	if reply == "" {
		t.Fatal("expected reply")
	}
	prefs, err := store.LoadPreferences("feishu:p2p:u1")
	if err != nil {
		t.Fatal(err)
	}
	if prefs.AgentName != "writer" {
		t.Fatalf("unexpected prefs: %#v", prefs)
	}

	reply = handler.handleText(context.Background(), "feishu:p2p:u1", "/skills")
	if !bytes.Contains([]byte(reply), []byte("demo-skill [doc]")) {
		t.Fatalf("unexpected skills reply: %s", reply)
	}
	reply = handler.handleText(context.Background(), "feishu:p2p:u1", "/tools")
	if !bytes.Contains([]byte(reply), []byte("read_file [builtin]")) {
		t.Fatalf("unexpected tools reply: %s", reply)
	}
}

func TestHandlerApprovalCommands(t *testing.T) {
	workspace := t.TempDir()
	store := session.NewStore(workspace)
	registry := tools.NewRegistry()
	registry.Register(approvalProvider{})
	runner := agentharness.New(workspace, store, registry, 6)
	runner.ApprovalPolicy = agentharness.ApprovalPolicy{RequireRiskyTools: true}
	runner.Start(context.Background(), agentharness.Request{
		SessionKey: "feishu:p2p:u1",
		Mode:       "tool",
		ToolName:   "spawn",
		Arguments:  map[string]any{"user_text": "needs approval"},
	}, nil)

	handler := Handler{Harness: runner}
	reply := handler.handleText(context.Background(), "feishu:p2p:u1", "/approvals")
	if !bytes.Contains([]byte(reply), []byte("待批准操作")) {
		t.Fatalf("unexpected approvals reply: %s", reply)
	}
	reply = handler.handleText(context.Background(), "feishu:p2p:u1", "/deny")
	if !bytes.Contains([]byte(reply), []byte("已拒绝")) {
		t.Fatalf("unexpected deny reply: %s", reply)
	}
}

func TestHandlerRunAndSummaryCommands(t *testing.T) {
	workspace := t.TempDir()
	store := session.NewStore(workspace)
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"memory answer"}}]}`))
	}))
	defer llmServer.Close()
	registry := tools.NewRegistry()
	registry.Register(prefProvider{})
	runner := agentharness.New(workspace, store, registry, 6)
	cfg := config.Default()
	cfg.ModelList = []config.ModelConfig{{
		Name:     "default",
		Provider: "openai_compat",
		Model:    "demo-model",
		APIBase:  llmServer.URL,
		APIKey:   "test-key",
		Enabled:  true,
	}}
	cfg.Models.Default = "default"
	runner.UseEinoRuntime(cfg)
	run, err := runner.Start(context.Background(), agentharness.Request{
		SessionKey: "feishu:p2p:u1",
		ThreadID:   "thread-1",
		AgentName:  "default",
		UserText:   "hello memory",
		Mode:       "chat",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	handler := Handler{Harness: runner}
	reply := handler.handleText(context.Background(), "feishu:p2p:u1", "/runs")
	if !bytes.Contains([]byte(reply), []byte(run.ID)) {
		t.Fatalf("unexpected runs reply: %s", reply)
	}
	reply = handler.handleText(context.Background(), "feishu:p2p:u1", "/run_status "+run.ID)
	if !bytes.Contains([]byte(reply), []byte("completed")) {
		t.Fatalf("unexpected run status reply: %s", reply)
	}
	reply = handler.handleText(context.Background(), "feishu:p2p:u1", "/summary")
	if !bytes.Contains([]byte(reply), []byte("最近结果")) {
		t.Fatalf("unexpected summary reply: %s", reply)
	}
	reply = handler.handleText(context.Background(), "feishu:p2p:u1", "/last")
	if !bytes.Contains([]byte(reply), []byte("上次进度")) {
		t.Fatalf("unexpected last reply: %s", reply)
	}
	reply = handler.handleText(context.Background(), "feishu:p2p:u1", "/trace "+run.ID)
	if !bytes.Contains([]byte(reply), []byte("run `"+run.ID+"`")) {
		t.Fatalf("unexpected trace reply: %s", reply)
	}
	_ = run
}

func TestHandlerMemoryScopedCommand(t *testing.T) {
	workspace := t.TempDir()
	store := session.NewStore(workspace)
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"done alpha"}}]}`))
	}))
	defer llmServer.Close()
	registry := tools.NewRegistry()
	registry.Register(tools.BuiltinProvider{
		Workspace: workspace,
		Sessions:  store,
		Memory:    agentharness.New(workspace, store, tools.NewRegistry(), 6).Memory,
	})
	runner := agentharness.New(workspace, store, registry, 6)
	cfg := config.Default()
	cfg.ModelList = []config.ModelConfig{{
		Name:     "default",
		Provider: "openai_compat",
		Model:    "demo-model",
		APIBase:  llmServer.URL,
		APIKey:   "test-key",
		Enabled:  true,
	}}
	cfg.Models.Default = "default"
	runner.UseEinoRuntime(cfg)
	_, err := runner.Start(context.Background(), agentharness.Request{
		SessionKey: "feishu:p2p:u1",
		ThreadID:   "thread-1",
		AgentName:  "default",
		UserText:   "search project alpha",
		Mode:       "chat",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler{Harness: runner}
	reply := handler.handleText(context.Background(), "feishu:p2p:u1", "/memory thread thread-1 alpha")
	if !bytes.Contains([]byte(reply), []byte("alpha")) {
		t.Fatalf("unexpected memory reply: %s", reply)
	}
}

func TestHandlerLearnReplyShowsInitialPlan(t *testing.T) {
	workspace := t.TempDir()
	store := session.NewStore(workspace)
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"已经整理完成"}}]}`))
	}))
	defer llmServer.Close()
	registry := tools.NewRegistry()
	runner := agentharness.New(workspace, store, registry, 6)
	cfg := config.Default()
	cfg.ModelList = []config.ModelConfig{{
		Name:     "default",
		Provider: "openai_compat",
		Model:    "demo-model",
		APIBase:  llmServer.URL,
		APIKey:   "test-key",
		Enabled:  true,
	}}
	cfg.Models.Default = "default"
	runner.UseEinoRuntime(cfg)
	run, err := runner.Start(context.Background(), agentharness.Request{
		SessionKey: "feishu:p2p:u2",
		ThreadID:   "thread-2",
		AgentName:  "default",
		UserText:   "帮我调研北京 AI 活动并总结",
		Mode:       "chat",
		Arguments:  map[string]any{"runtime_route": "chatmodel"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	handler := Handler{Harness: runner}
	reply := handler.handleText(context.Background(), "feishu:p2p:u2", "/learn "+run.ID)
	if !bytes.Contains([]byte(reply), []byte("初始分解:")) {
		t.Fatalf("unexpected learn reply: %s", reply)
	}
	if !bytes.Contains([]byte(reply), []byte("执行过程:")) {
		t.Fatalf("unexpected learn reply: %s", reply)
	}
}
