package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dongping/mateway/internal/session"
)

func TestLatestTracePathUsesNewestTaskWithTrace(t *testing.T) {
	state := session.State{Tasks: []session.TaskNode{
		{ID: "task-1", TracePath: "/tmp/one.jsonl"},
		{ID: "task-2"},
		{ID: "task-3", TracePath: "/tmp/three.jsonl"},
	}}
	if got := latestTracePath(state); got != "/tmp/three.jsonl" {
		t.Fatalf("trace path = %q", got)
	}
}

func TestPrintTraceEventsRendersProcessLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	lines := []string{
		`{"type":"model_start"}`,
		`{"type":"message_start","duration_ms":12,"message":{"ToolCalls":[{"Name":"file.read"}]}}`,
		`{"type":"tool_execution_start","tool_call":{"Name":"file.read","Args":{"path":"/tmp/project"}}}`,
		`{"type":"tool_execution_end","duration_ms":34,"tool_call":{"Name":"file.read"},"tool_result":{"Content":"found files"}}`,
		`{"type":"reply","text":"done"}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := printTraceEvents(&out, path); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"+ Thought:",
		"prepared tool call file.read",
		"→ Read /tmp/project",
		"✓ Read (34ms)",
		"Assistant\ndone",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
}

func TestPrintTraceEventsJSONRendersNormalizedEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	lines := []string{
		`{"type":"tool_execution_start","tool_call":{"Name":"terminal.run","Args":{"command":"go test ./..."}}}`,
		`{"type":"tool_execution_end","duration_ms":34,"tool_call":{"Name":"terminal.run"},"tool_result":{"Content":"ok"}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := PrintTraceEventsWithOptions(&out, path, TraceEventsOptions{JSON: true}); err != nil {
		t.Fatal(err)
	}
	rendered := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(rendered) != 2 {
		t.Fatalf("expected two events, got:\n%s", out.String())
	}
	var started ProcessEvent
	if err := json.Unmarshal([]byte(rendered[0]), &started); err != nil {
		t.Fatal(err)
	}
	if started.Type != "tool.started" || started.Tool != "terminal.run" || started.Args != "go test ./..." {
		t.Fatalf("unexpected start event: %#v", started)
	}
	var completed ProcessEvent
	if err := json.Unmarshal([]byte(rendered[1]), &completed); err != nil {
		t.Fatal(err)
	}
	if completed.Type != "tool.completed" || completed.Status != "success" || completed.DurationMS != 34 || completed.Summary != "ok" {
		t.Fatalf("unexpected completed event: %#v", completed)
	}
}

func TestPrintTraceReportShowsContractToolsAndJudgment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	lines := []string{
		`{"type":"request","trace_id":"trace-1","session_key":"cli:test","task_id":"task-1","agent_id":"main","text":"check singbox status"}`,
		`{"type":"task_contract_created","summary":"check singbox status","requires_tools":true,"required_tools":["terminal.run"],"required_evidence":[{"tool":"terminal.run","description":"systemctl status output"}],"expected_outcome":"status report"}`,
		`{"type":"model_route_selected","provider":"minimax","model":"MiniMax-M3"}`,
		`{"type":"context_budget_estimated","tools":["web.search","terminal.run"],"hidden_tools":2}`,
		`{"type":"context_budget_trimmed","trimmed_tools":["schedule.manage","task.search"],"reason":"visible_tool_budget"}`,
		`{"type":"context_budget_non_default_exposed","non_default_exposed":{"terminal.run":"contract","schedule.manage":"recent"}}`,
		`{"type":"hook_event","hook":"tool_policy_hook","tool":"terminal.run","block":false}`,
		`{"type":"tool_execution_end","duration_ms":34,"tool_call":{"Name":"terminal.run","Args":{"command":"ssh overseas 'systemctl status sing-box --no-pager'"}},"tool_result":{"Content":"active","IsError":false}}`,
		`{"type":"task_contract_satisfied","status":"completed"}`,
		`{"type":"reply","text":"sing-box is active"}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := PrintTraceReport(&out, path); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"Trace Report",
		"Request",
		"Task Contract",
		"required_tools: terminal.run",
		"Models",
		"minimax/MiniMax-M3",
		"Visible Tools",
		"web.search, terminal.run",
		"trimmed (budget): schedule.manage, task.search",
		"non-default exposed: schedule.manage:recent, terminal.run:contract",
		"Tool Process",
		"policy allowed: terminal.run",
		"ok terminal.run",
		"Result Judgment",
		"task_contract_satisfied status=completed",
		"Final Reply",
		"sing-box is active",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
}

func TestPrintTraceEventsDoesNotRenderFinalMessageStartAsThinking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"message_start","message":{"Content":"final answer"}}`+"\n"+`{"type":"reply","text":"final answer"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := printTraceEvents(&out, path); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if strings.Contains(text, "[thinking] final answer") {
		t.Fatalf("final answer leaked into thinking event:\n%s", text)
	}
	if !strings.Contains(text, "Assistant\nfinal answer") {
		t.Fatalf("missing final event:\n%s", text)
	}
}

func TestChatTraceCommandUsesCurrentSessionTrace(t *testing.T) {
	home := t.TempDir()
	tracePath := filepath.Join(home, "trace", "one.jsonl")
	if err := os.MkdirAll(filepath.Dir(tracePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tracePath, []byte(`{"type":"request","trace_id":"one","session_key":"cli:test"}`+"\n"+`{"type":"runtime_done","duration_ms":5}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := session.NewStore(home)
	state := session.State{Key: "cli:test"}
	task := state.StartTask("check trace")
	task.TracePath = tracePath
	state.UpdatedAt = time.Now()
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	chat := chatState{store: store, sessionKey: "cli:test", out: &out}
	if err := chat.printTrace(nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "trace_id: one") || !strings.Contains(out.String(), "runtime_ms: 5") {
		t.Fatalf("unexpected trace output:\n%s", out.String())
	}
}
