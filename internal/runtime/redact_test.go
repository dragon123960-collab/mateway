package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
)

func TestRedactSecretString(t *testing.T) {
	input := "smtp_pass: PXUj5ftvjscRpPy7\nimap_pass=QBptnPtt6Hnp3awb\nAuthorization: Bearer abcdefghijklmnop"
	got := redactSecretString(input)
	for _, secret := range []string{"PXUj5ftvjscRpPy7", "QBptnPtt6Hnp3awb", "abcdefghijklmnop"} {
		if strings.Contains(got, secret) {
			t.Fatalf("secret leaked in %q", got)
		}
	}
	if strings.Count(got, redactedSecret) != 3 {
		t.Fatalf("expected redactions, got %q", got)
	}
}

func TestRuntimeTruncationKeepsValidUTF8(t *testing.T) {
	input := strings.Repeat("认", 100)
	middle, truncated := truncateMiddle(input, 81)
	if !truncated {
		t.Fatal("expected truncateMiddle to truncate")
	}
	if !utf8.ValidString(middle) {
		t.Fatalf("truncateMiddle returned invalid UTF-8: %q", middle)
	}
	progress := compactProgressSummary(input)
	if !utf8.ValidString(progress) {
		t.Fatalf("compactProgressSummary returned invalid UTF-8: %q", progress)
	}
	summary := summarize(input)
	if !utf8.ValidString(summary) {
		t.Fatalf("summarize returned invalid UTF-8: %q", summary)
	}
}

func TestRedactSecretStringKeepsEnvLookupCodeIntact(t *testing.T) {
	input := "pwd = _get(\"EMAIL_SMTP_PASS\")\ntoken = os.environ.get(\"API_TOKEN\")\n"
	got := redactSecretString(input)
	if got != input {
		t.Fatalf("expected env lookup code to remain intact, got %q", got)
	}
}

func TestRedactSecretStringKeepsRequiredSecretDeclarationIntact(t *testing.T) {
	input := "# mateway.required_secret: id=<secret_id> env=<ENV_NAME>\n# mateway.required_secret: id=netmail163_auth_code env=NETMAIL163_AUTH_CODE"
	got := redactSecretString(input)
	if got != input {
		t.Fatalf("expected required_secret declarations to remain intact, got %q", got)
	}
}

func TestRuntimeTraceRedactsToolResultSecrets(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Root{App: config.AppConfig{Home: home}, Agents: config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}}}
	rt := New(cfg)
	rt.Tools = agentcore.NewToolRegistry()
	rt.Tools.Register(secretTool{})
	rt.Pool.agents["main"] = agentcore.NewAgent(secretToolModel{}, rt.Tools)

	resp, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "1", Channel: "cli", SessionKey: "cli:test", Text: "inspect secret"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(resp.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, secret := range []string{"PXUj5ftvjscRpPy7", "QBptnPtt6Hnp3awb", "abcdef1234567890"} {
		if strings.Contains(text, secret) {
			t.Fatalf("trace leaked secret %q:\n%s", secret, text)
		}
	}
	if !strings.Contains(text, redactedSecret) {
		t.Fatalf("expected redacted marker in trace:\n%s", text)
	}
}

func TestRuntimeTaskStepSummaryRedactsSecrets(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Root{App: config.AppConfig{Home: home}, Agents: config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}}}
	rt := New(cfg)
	rt.Tools = agentcore.NewToolRegistry()
	rt.Tools.Register(secretTool{})
	rt.Pool.agents["main"] = agentcore.NewAgent(secretToolModel{}, rt.Tools)

	if _, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "1", Channel: "cli", SessionKey: "cli:test", Text: "inspect secret"}); err != nil {
		t.Fatal(err)
	}
	state, err := rt.Store.Load("cli:test")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Tasks) != 1 || len(state.Tasks[0].Steps) != 1 {
		t.Fatalf("expected one tool step, got %#v", state.Tasks)
	}
	if strings.Contains(state.Tasks[0].Steps[0].Summary, "PXUj5ftvjscRpPy7") {
		t.Fatalf("step summary leaked secret: %#v", state.Tasks[0].Steps[0])
	}
	if !strings.Contains(state.Tasks[0].Steps[0].Summary, redactedSecret) {
		t.Fatalf("expected redacted summary, got %#v", state.Tasks[0].Steps[0])
	}
	for _, msg := range state.Messages {
		if strings.Contains(msg.Content, "PXUj5ftvjscRpPy7") || strings.Contains(msg.Content, "QBptnPtt6Hnp3awb") {
			t.Fatalf("stored transcript leaked secret: %#v", msg)
		}
	}
}

func TestRuntimeFinalReplyRedactsSecrets(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Root{App: config.AppConfig{Home: home}, Agents: config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}}}
	rt := New(cfg)
	rt.Pool.agents["main"] = agentcore.NewAgent(secretFinalModel{}, rt.Tools)

	resp, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "1", Channel: "cli", SessionKey: "cli:test", Text: "store this secret"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(resp.Reply.Text, "QBptnPtt6Hnp3awb") {
		t.Fatalf("final reply leaked secret: %q", resp.Reply.Text)
	}
	if !strings.Contains(resp.Reply.Text, redactedSecret) {
		t.Fatalf("expected redacted marker in final reply, got %q", resp.Reply.Text)
	}
}

func TestFileReadTraceRedactsSkillSecrets(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Root{App: config.AppConfig{Home: home}, Agents: config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}}}
	rt := New(cfg)
	secretFile := filepath.Join(home, "workspace", "skills", "email", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(secretFile), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "config:\n  smtp_pass: PXUj5ftvjscRpPy7\n  imap_pass: QBptnPtt6Hnp3awb\n"
	if err := os.WriteFile(secretFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Handle(context.Background(), channel.InboundMessage{ID: "1", Channel: "cli", SessionKey: "cli:test", Text: "/read " + secretFile}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(home, "trace"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, "trace", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "PXUj5ftvjscRpPy7") || strings.Contains(string(data), "QBptnPtt6Hnp3awb") {
		t.Fatalf("file.read trace leaked secret:\n%s", data)
	}
}

type secretTool struct{}

func (secretTool) Name() string        { return "secret.read" }
func (secretTool) Description() string { return "Return a synthetic secret payload." }
func (secretTool) Schema() agentcore.Schema {
	return agentcore.Schema{}
}
func (secretTool) Risk() agentcore.Risk { return agentcore.RiskSafeRead }
func (secretTool) Run(_ context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	return agentcore.ToolResult{
		ToolCallID: call.ID,
		Content:    "smtp_pass: PXUj5ftvjscRpPy7\nimap_pass: QBptnPtt6Hnp3awb\nAuthorization: Bearer abcdef1234567890",
		Evidence:   map[string]any{"api_key": "abcdef1234567890"},
	}
}

type secretToolModel struct{}

func (secretToolModel) Next(_ context.Context, ctx agentcore.Context) (agentcore.Message, error) {
	for _, msg := range ctx.Messages {
		if msg.Role == agentcore.RoleTool {
			return agentcore.Message{Role: agentcore.RoleAssistant, Content: "checked"}, nil
		}
	}
	return agentcore.Message{
		Role: agentcore.RoleAssistant,
		ToolCalls: []agentcore.ToolCall{{
			ID:   "call_secret",
			Name: "secret.read",
			Args: map[string]any{"api_key": "abcdef1234567890"},
		}},
	}, nil
}

var _ agentcore.Tool = secretTool{}

type secretFinalModel struct{}

func (secretFinalModel) Next(_ context.Context, _ agentcore.Context) (agentcore.Message, error) {
	return agentcore.Message{Role: agentcore.RoleAssistant, Content: "Secret stored: auth_code=QBptnPtt6Hnp3awb"}, nil
}
