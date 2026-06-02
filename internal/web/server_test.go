package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/observe"
	"github.com/dongping/mateway/internal/runtime"
	"github.com/dongping/mateway/internal/session"
	"github.com/gorilla/websocket"
)

func TestWebServesIndexAndOverview(t *testing.T) {
	home := t.TempDir()
	cfg := testWebConfig(t, home)
	router := NewServer(cfg, runtime.New(cfg)).Router()

	index := httptest.NewRecorder()
	router.ServeHTTP(index, httptest.NewRequest(http.MethodGet, "/", nil))
	if index.Code != http.StatusOK || !strings.Contains(index.Body.String(), "Mateway Console") {
		t.Fatalf("unexpected index response: %d %s", index.Code, index.Body.String())
	}

	overview := httptest.NewRecorder()
	router.ServeHTTP(overview, httptest.NewRequest(http.MethodGet, "/api/overview", nil))
	if overview.Code != http.StatusOK || !strings.Contains(overview.Body.String(), `"home"`) {
		t.Fatalf("unexpected overview response: %d %s", overview.Code, overview.Body.String())
	}
}

func TestWebChatWritesSession(t *testing.T) {
	home := t.TempDir()
	cfg := testWebConfig(t, home)
	rt := runtime.New(cfg)
	rt.Model = webStaticModel{text: "done"}
	router := NewServer(cfg, rt).Router()

	body := bytes.NewBufferString(`{"message":"hello web","session_key":"web:test"}`)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "/api/chat", body))
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "hello web") {
		t.Fatalf("unexpected chat response: %d %s", resp.Code, resp.Body.String())
	}
	state, err := session.NewStore(home).Load("web:test")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Messages) == 0 {
		t.Fatalf("expected stored session, got %#v", state)
	}
}

func TestWebConfigPatchRejectsSecretLikeContent(t *testing.T) {
	home := t.TempDir()
	cfg := testWebConfig(t, home)
	router := NewServer(cfg, runtime.New(cfg)).Router()

	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{"app":{"api_key":"sk-secret-value"}}`)))
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request, got %d %s", resp.Code, resp.Body.String())
	}
}

func TestWebScheduleCreateAndAction(t *testing.T) {
	home := t.TempDir()
	cfg := testWebConfig(t, home)
	router := NewServer(cfg, runtime.New(cfg)).Router()
	create := httptest.NewRecorder()
	router.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/api/schedules", bytes.NewBufferString(`{"text":"check","run_at":"2026-06-02T18:00:00+08:00","require_test":true}`)))
	if create.Code != http.StatusCreated {
		t.Fatalf("unexpected create response: %d %s", create.Code, create.Body.String())
	}
	var task struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	action := httptest.NewRecorder()
	router.ServeHTTP(action, httptest.NewRequest(http.MethodPatch, "/api/schedules/"+task.ID+"/activate", nil))
	if action.Code != http.StatusOK || !strings.Contains(action.Body.String(), `"active"`) {
		t.Fatalf("unexpected action response: %d %s", action.Code, action.Body.String())
	}
	testRun := httptest.NewRecorder()
	router.ServeHTTP(testRun, httptest.NewRequest(http.MethodPatch, "/api/schedules/"+task.ID+"/test", nil))
	if testRun.Code != http.StatusOK || !strings.Contains(testRun.Body.String(), `"run"`) {
		t.Fatalf("unexpected test response: %d %s", testRun.Code, testRun.Body.String())
	}
}

func TestWebChannelToggleWritesChannelConfig(t *testing.T) {
	home := t.TempDir()
	cfg := testWebConfig(t, home)
	channelDir := filepath.Join(home, "config", "channels")
	if err := os.MkdirAll(channelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(channelDir, "feishu.yaml"), []byte("enabled: false\nbot_name: test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	router := NewServer(cfg, runtime.New(cfg)).Router()
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, httptest.NewRequest(http.MethodPatch, "/api/channels/feishu/enabled", bytes.NewBufferString(`{"enabled":true}`)))
	if resp.Code != http.StatusOK {
		t.Fatalf("unexpected response: %d %s", resp.Code, resp.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(channelDir, "feishu.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "enabled: true") {
		t.Fatalf("expected enabled channel config, got %s", data)
	}
}

func TestWebSessionDetailUnescapesKey(t *testing.T) {
	home := t.TempDir()
	cfg := testWebConfig(t, home)
	store := session.NewStore(home)
	if err := store.Save(session.State{Key: "web:test", UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	router := NewServer(cfg, runtime.New(cfg)).Router()
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/api/sessions/web%3Atest", nil))
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "web:test") {
		t.Fatalf("unexpected response: %d %s", resp.Code, resp.Body.String())
	}
}

func TestWebSocketReceivesSessionEvents(t *testing.T) {
	home := t.TempDir()
	cfg := testWebConfig(t, home)
	router := NewServer(cfg, runtime.New(cfg)).Router()
	server := httptest.NewServer(router)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/events/ws?session_key=web:test"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var connected observe.Event
	if err := conn.ReadJSON(&connected); err != nil {
		t.Fatal(err)
	}
	observe.Publish(observe.Event{Type: "runtime_started", SessionKey: "web:other"})
	observe.Publish(observe.Event{Type: "runtime_started", SessionKey: "web:test", TraceID: "trace-one"})
	var event observe.Event
	if err := conn.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event.SessionKey != "web:test" || event.TraceID != "trace-one" {
		t.Fatalf("unexpected event %#v", event)
	}
}

func testWebConfig(t *testing.T, home string) *config.Root {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config", "config.yaml"), []byte("app:\n  name: mateway\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	enabled := true
	return &config.Root{
		App:    config.AppConfig{Home: home, Workspace: filepath.Join(home, "workspace")},
		Web:    config.WebConfig{Enabled: &enabled, Bind: "127.0.0.1:0", AllowConfigWrite: true, RealtimeEnabled: true, OfficeWatchEnabled: true},
		Agents: config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}},
	}
}

type webStaticModel struct {
	text string
}

func (m webStaticModel) Next(context.Context, agentcore.Context) (agentcore.Message, error) {
	return agentcore.Message{Role: agentcore.RoleAssistant, Content: m.text}, nil
}
