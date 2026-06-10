package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/session"
)

func TestRunTUIRequiresInteractiveTerminal(t *testing.T) {
	err := RunTUI(t.Context(), TUIOptions{
		Config: &config.Root{App: config.AppConfig{Home: t.TempDir()}},
		In:     strings.NewReader(""),
		Out:    &bytes.Buffer{},
	})
	if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("expected interactive terminal error, got %v", err)
	}
}

func TestWrapLinesSplitsLongLines(t *testing.T) {
	lines := wrapLines("abcdef", 3)
	if strings.Join(lines, ",") != "abc,def" {
		t.Fatalf("unexpected wrap: %#v", lines)
	}
}

func TestWrapLinesDoesNotLoopOnANSI(t *testing.T) {
	lines := wrapLines(colorize(strings.Repeat("x", 100), ansiDim, true), 20)
	if len(lines) != 1 {
		t.Fatalf("ANSI line should be truncated as one line, got %#v", lines)
	}
	if visibleLen(lines[0]) > 20 {
		t.Fatalf("ANSI line not truncated: %q", lines[0])
	}
}

func TestTruncateANSIDoesNotSplitUTF8(t *testing.T) {
	got := truncateANSI("中文abc", 2)
	if got != "中" {
		t.Fatalf("truncate = %q", got)
	}
}

func TestTUISlashSessionSwitch(t *testing.T) {
	app := newTUIModel(context.Background(), &config.Root{App: config.AppConfig{Home: t.TempDir()}}, "cli:default")
	done := app.handleSlash(SlashCommand{Name: "session", Args: []string{"cli:review"}})
	if done {
		t.Fatal("session command should not exit")
	}
	if app.sessionKey != "cli:review" {
		t.Fatalf("session key = %q", app.sessionKey)
	}
}

func TestTUISlashEventsRendersLatestTrace(t *testing.T) {
	home := t.TempDir()
	tracePath := filepath.Join(home, "trace.jsonl")
	trace := `{"type":"tool_execution_start","tool_call":{"Name":"terminal.run","Args":{"command":"go test ./..."}}}` + "\n" +
		`{"type":"tool_execution_end","tool_call":{"Name":"terminal.run"},"tool_result":{"Content":"ok","IsError":false},"duration_ms":42}` + "\n"
	if err := os.WriteFile(tracePath, []byte(trace), 0o600); err != nil {
		t.Fatal(err)
	}
	store := session.NewStore(home)
	state := session.State{
		Key: "cli:default",
		Tasks: []session.TaskNode{{
			ID:        "task-1",
			TracePath: tracePath,
		}},
	}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	app := newTUIModel(context.Background(), &config.Root{App: config.AppConfig{Home: home}}, "cli:default")
	done := app.handleSlash(SlashCommand{Name: "events"})
	if done {
		t.Fatal("events command should not exit")
	}
	joined := strings.Join(app.events, "\n")
	if !strings.Contains(joined, "→ Run go test ./...") || !strings.Contains(joined, "✓ Run (42ms) - ok") {
		t.Fatalf("events did not render trace lines:\n%s", joined)
	}
}

func TestTUIStartsByResumingExistingSession(t *testing.T) {
	home := t.TempDir()
	state := session.State{
		Key: "cli:default",
		Messages: []agentcore.Message{{
			Role:    agentcore.RoleUser,
			Content: "继续上一个任务",
		}},
		Tasks: []session.TaskNode{{ID: "task-1", Status: "completed", Summary: "done"}},
	}
	if err := session.NewStore(home).Save(state); err != nil {
		t.Fatal(err)
	}
	app := newTUIModel(context.Background(), &config.Root{App: config.AppConfig{Home: home}}, "cli:default")
	joined := strings.Join(app.events, "\n")
	if strings.Contains(joined, "New session") {
		t.Fatalf("banner should not claim a new session:\n%s", joined)
	}
	if !strings.Contains(joined, "Session resumed") || !strings.Contains(joined, "继续上一个任务") {
		t.Fatalf("banner should resume existing session:\n%s", joined)
	}
}

func TestCurrentTUIModelUsesConfiguredModel(t *testing.T) {
	cfg := &config.Root{
		Model: config.ModelSelection{Default: "minimax"},
		Models: []config.ModelConfig{{
			Name:     "minimax",
			Provider: "minimax",
			Model:    "MiniMax-M2.7",
			Enabled:  true,
		}},
	}
	agent := config.AgentProfileConfig{ID: "main", Model: config.ModelSelection{Default: "minimax"}}
	info := currentTUIModel(cfg, agent)
	if info.Display() != "MiniMax-M2.7" {
		t.Fatalf("model display = %q", info.Display())
	}
}

func TestTUIStatusLineShowsModelAndSession(t *testing.T) {
	cfg := &config.Root{
		App: config.AppConfig{Home: t.TempDir()},
		Model: config.ModelSelection{
			Default: "minimax",
		},
		Agents: config.AgentsConfig{
			Default:  "main",
			Profiles: []config.AgentProfileConfig{{ID: "main", Name: "Main", Model: config.ModelSelection{Default: "minimax"}}},
		},
	}
	app := newTUIModel(context.Background(), cfg, "cli:default")
	line := app.statusLine(140)
	if !strings.Contains(line, "minimax") || !strings.Contains(line, "agent:main") || !strings.Contains(line, "session:default") {
		t.Fatalf("status line = %q", line)
	}
	if strings.Contains(line, "Build") || strings.Contains(line, "Todo") {
		t.Fatalf("status line should not contain copied labels: %q", line)
	}
}

func TestTUIProgressStatusUsesActionLabels(t *testing.T) {
	got := progressStatus(channel.ProgressStep{Tool: "file.read", Status: "running"})
	if got != "Acting: Read" {
		t.Fatalf("status = %q", got)
	}
	if got := progressStatus(channel.ProgressStep{Status: "thinking"}); got != "Thinking" {
		t.Fatalf("thinking status = %q", got)
	}
}

func TestTUIFooterShowsInputFrame(t *testing.T) {
	app := newTUIModel(context.Background(), &config.Root{App: config.AppConfig{Home: t.TempDir()}}, "cli:default")
	app.width = 100
	footer := app.footerView()
	if !strings.Contains(footer, "╭") || !strings.Contains(footer, "╰") {
		t.Fatalf("footer should show framed input, got %q", footer)
	}
	if strings.Count(footer, ">") > 0 {
		t.Fatalf("footer should not show repeated textarea prompts, got %q", footer)
	}
	if got := app.input.Height(); got != 3 {
		t.Fatalf("empty input height = %d", got)
	}
	lines := strings.Split(footer, "\n")
	if !strings.Contains(lines[len(lines)-1], "ctrl+c exit") {
		t.Fatalf("status line should remain at the bottom of footer, got %q", lines[len(lines)-1])
	}
}

func TestTUIViewFillsTerminalHeightWithSparseContent(t *testing.T) {
	app := newTUIModel(context.Background(), &config.Root{App: config.AppConfig{Home: t.TempDir()}}, "cli:default")
	app.width = 100
	app.height = 30
	app.events = []string{"short"}
	app.resize()
	view := app.View()
	if got := lipgloss.Height(view); got != app.height {
		t.Fatalf("view height = %d, want %d\n%s", got, app.height, view)
	}
}

func TestTUIViewDoesNotGrowWhenSidebarIsLong(t *testing.T) {
	home := t.TempDir()
	var steps []session.TaskStep
	for i := 0; i < 20; i++ {
		steps = append(steps, session.TaskStep{
			Tool:     "web.search",
			Status:   "completed",
			Summary:  fmt.Sprintf("very long sidebar step %d with enough text to wrap and grow", i),
			Accepted: true,
		})
	}
	state := session.State{
		Key: "cli:default",
		Tasks: []session.TaskNode{{
			ID:     "task-1",
			Status: "completed",
			Execution: session.ExecutionFrame{Contract: &session.TaskContract{
				Summary:          strings.Repeat("long task summary ", 20),
				RequiredTools:    []string{"web.search", "file.read", "terminal.run"},
				RequiredEvidence: []session.TaskEvidenceContract{{Tool: "web.search", Description: strings.Repeat("long evidence ", 12)}},
				ExpectedOutcome:  strings.Repeat("long expected outcome ", 12),
			}},
			Steps: steps,
		}},
	}
	if err := session.NewStore(home).Save(state); err != nil {
		t.Fatal(err)
	}
	app := newTUIModel(context.Background(), &config.Root{App: config.AppConfig{Home: home}}, "cli:default")
	app.width = 150
	app.height = 32
	app.events = []string{"short"}
	app.resize()
	view := app.View()
	if got := lipgloss.Height(view); got != app.height {
		t.Fatalf("view height = %d, want %d", got, app.height)
	}
	if !strings.Contains(view, "scroll down") {
		t.Fatalf("long sidebar should be scrollable with a hint:\n%s", view)
	}
}

func TestTUISidebarCanScrollLongContent(t *testing.T) {
	app := newTUIModel(context.Background(), &config.Root{App: config.AppConfig{Home: t.TempDir()}}, "cli:default")
	var lines []string
	for i := 0; i < 30; i++ {
		lines = append(lines, fmt.Sprintf("line %02d", i))
	}
	app.sidebarScroll = 6
	got := strings.Join(app.fitSidebarLines(lines, 8, 30), "\n")
	if !strings.Contains(got, "scroll up") || !strings.Contains(got, "scroll down") {
		t.Fatalf("scroll hints missing:\n%s", got)
	}
	if strings.Contains(got, "line 00") {
		t.Fatalf("sidebar did not scroll:\n%s", got)
	}
}

func TestTUIViewShowsSidebarOnWideTerminals(t *testing.T) {
	home := t.TempDir()
	tracePath := filepath.Join(home, "trace.jsonl")
	trace := `{"type":"message_start","duration_ms":12,"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}` + "\n" +
		`{"type":"tool_execution_end","tool_call":{"Name":"terminal.run"},"tool_result":{"Content":"ok"},"duration_ms":34}` + "\n" +
		`{"type":"runtime_done","duration_ms":99}` + "\n"
	if err := os.WriteFile(tracePath, []byte(trace), 0o600); err != nil {
		t.Fatal(err)
	}
	state := session.State{
		Key: "cli:default",
		Usage: session.Usage{
			Requests:     2,
			InputTokens:  20,
			OutputTokens: 8,
			TotalTokens:  28,
		},
		Tasks: []session.TaskNode{{
			ID:        "task-1",
			Status:    "completed",
			Summary:   "checked trace display",
			TracePath: tracePath,
		}},
	}
	if err := session.NewStore(home).Save(state); err != nil {
		t.Fatal(err)
	}
	app := newTUIModel(context.Background(), &config.Root{App: config.AppConfig{Home: home}}, "cli:default")
	app.width = 140
	app.height = 40
	app.resize()
	view := app.View()
	for _, want := range []string{"Mateway", "STATE", "AGENT", "SESSION", "USAGE", "TRACE", "TASK", "28 tokens", "checked trace display"} {
		if !strings.Contains(view, want) {
			t.Fatalf("wide TUI view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, ansiDim+"Usage") {
		t.Fatalf("sidebar sections should not use dim headings:\n%s", view)
	}
	if strings.Contains(view, "Commands") {
		t.Fatalf("sidebar should not show command list:\n%s", view)
	}
}

func TestTUISidebarShowsTaskContractAndSteps(t *testing.T) {
	state := session.State{
		Key:        "cli:default",
		ActiveTask: "task-1",
		Tasks: []session.TaskNode{{
			ID:     "task-1",
			Status: "running",
			Execution: session.ExecutionFrame{Contract: &session.TaskContract{
				Summary:          "inspect the knowledge base safely",
				RequiredTools:    []string{"terminal.run", "file.read"},
				RequiredEvidence: []session.TaskEvidenceContract{{Tool: "terminal.run", Description: "directory index"}},
				ExpectedOutcome:  "find timeout cause",
				CompletionPolicy: "finish after required evidence is accepted",
			}},
			Steps: []session.TaskStep{{
				Tool:     "terminal.run",
				Status:   "accepted",
				Summary:  "indexed top-level files",
				Accepted: true,
			}, {
				Tool:    "file.read",
				Status:  "running",
				Summary: "reading one file",
			}},
		}},
	}
	lines := strings.Join(taskLines(state), "\n")
	for _, want := range []string{"▾ Contract running", "[✓] inspect", "[ ] find timeout cause", "[ ] finish after required evidence is accepted", "[✓] Run", "[•] Read", "running", "[✓] directory index"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("task lines missing %q:\n%s", want, lines)
		}
	}
	if strings.Contains(lines, "Recent steps") || strings.Contains(lines, "indexed top-level files") || strings.Contains(lines, "reading one file") {
		t.Fatalf("task sidebar should show contract, not process steps:\n%s", lines)
	}
}

func TestTUISidebarSummarizesCompletedTask(t *testing.T) {
	task := session.TaskNode{
		ID:     "task-1",
		Status: "completed",
		Execution: session.ExecutionFrame{Contract: &session.TaskContract{
			Summary:          "inspect completed task",
			RequiredTools:    []string{"file.read"},
			RequiredEvidence: []session.TaskEvidenceContract{{Tool: "file.read", Description: "directory index"}},
			ExpectedOutcome:  "answer",
		}},
		Steps: []session.TaskStep{{
			Tool:     "file.read",
			Status:   "accepted",
			Summary:  "indexed files",
			Accepted: true,
		}},
	}
	lines := strings.Join(taskSidebarLines(task), "\n")
	for _, want := range []string{"▾ Contract completed", "[✓] inspect completed task", "[ ] answer", "[✓] Read", "[✓] directory index"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("completed task summary missing %q:\n%s", want, lines)
		}
	}
	if strings.Contains(lines, "indexed files") || strings.Contains(lines, "Steps:") {
		t.Fatalf("completed task should not show process step details:\n%s", lines)
	}
}

func TestTUISidebarContractShowsRetryCount(t *testing.T) {
	task := session.TaskNode{
		ID:     "task-1",
		Status: "completed",
		Execution: session.ExecutionFrame{Contract: &session.TaskContract{
			Summary:       "retry tool task",
			RequiredTools: []string{"web.search"},
		}},
		Steps: []session.TaskStep{{
			Tool:   "web.search",
			Status: "failed",
		}, {
			Tool:   "web.search",
			Status: "blocked",
		}, {
			Tool:     "web.search",
			Status:   "accepted",
			Accepted: true,
		}},
	}
	lines := strings.Join(taskSidebarLines(task), "\n")
	if !strings.Contains(lines, "[✓] Search") || !strings.Contains(lines, "after 2 retries") {
		t.Fatalf("retry count missing:\n%s", lines)
	}
}

func TestTUISidebarHidesGenericContractPolicy(t *testing.T) {
	state := session.State{Tasks: []session.TaskNode{{
		ID:     "task-1",
		Status: "completed",
		Execution: session.ExecutionFrame{Contract: &session.TaskContract{
			Summary:          "locate previous task",
			ExpectedOutcome:  "answer the user task directly from available context",
			CompletionPolicy: "final answer should address the user task or ask for required input",
		}},
	}}}
	lines := strings.Join(taskLines(state), "\n")
	if strings.Contains(lines, "answer the user task") || strings.Contains(lines, "final answer should") {
		t.Fatalf("generic internal policy should be hidden:\n%s", lines)
	}
	if !strings.Contains(lines, "[✓] locate previous task") {
		t.Fatalf("goal should remain visible:\n%s", lines)
	}
}

func TestTUISidebarShowsLiveTaskWhileRunning(t *testing.T) {
	state := session.State{Tasks: []session.TaskNode{{
		ID:      "old",
		Status:  "completed",
		Summary: "old completed task",
	}}}
	app := newTUIModel(context.Background(), &config.Root{App: config.AppConfig{Home: t.TempDir()}}, "cli:default")
	app.running = true
	app.status = "Thinking"
	app.currentTask = "我们来具体看看 deepseeks 的性能"
	app.recordLiveStep(tuiProgressMsg{
		tool:      "web.search",
		toolState: "running",
		summary:   "DeepSeek V4 Pro 性能 benchmark",
	})
	lines := strings.Join(app.sidebarSummary(state).TaskLines, "\n")
	if strings.Contains(lines, "old completed task") {
		t.Fatalf("live sidebar should not show old task:\n%s", lines)
	}
	for _, want := range []string{"▾ Contract pending", "[•] 我们来具体看看"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("live sidebar missing %q:\n%s", want, lines)
		}
	}
	if strings.Contains(lines, "Recent steps") || strings.Contains(lines, "DeepSeek V4 Pro") {
		t.Fatalf("pending contract sidebar should not show process steps:\n%s", lines)
	}
}

func TestTUISidebarShowsStoredContractWhileRunning(t *testing.T) {
	state := session.State{
		Key:        "cli:default",
		ActiveTask: "task-1",
		Tasks: []session.TaskNode{{
			ID:     "task-1",
			Status: "running",
			Execution: session.ExecutionFrame{Contract: &session.TaskContract{
				Summary:       "search current facts",
				RequiredTools: []string{"web.search"},
			}},
			Steps: []session.TaskStep{{
				Tool:     "web.search",
				Status:   "accepted",
				Accepted: true,
			}},
		}},
	}
	home := t.TempDir()
	if err := session.NewStore(home).Save(state); err != nil {
		t.Fatal(err)
	}
	app := newTUIModel(context.Background(), &config.Root{App: config.AppConfig{Home: home}}, "cli:default")
	app.running = true
	app.currentTask = "fallback text should not be primary"
	lines := strings.Join(app.sidebarSummary(state).TaskLines, "\n")
	for _, want := range []string{"▾ Contract running", "[✓] search current facts", "[✓] Search"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("running contract missing %q:\n%s", want, lines)
		}
	}
	if strings.Contains(lines, "Recent steps") || strings.Contains(lines, "fallback text should not be primary") {
		t.Fatalf("running sidebar should prefer contract checklist:\n%s", lines)
	}
}

func TestTUIViewHidesSidebarOnNarrowTerminals(t *testing.T) {
	app := newTUIModel(context.Background(), &config.Root{App: config.AppConfig{Home: t.TempDir()}}, "cli:default")
	app.width = 100
	app.height = 30
	app.resize()
	view := app.View()
	if strings.Contains(view, "Mateway") {
		t.Fatalf("narrow TUI should hide sidebar:\n%s", view)
	}
}

func TestTUISlashModelAndTools(t *testing.T) {
	cfg := &config.Root{
		App:   config.AppConfig{Home: t.TempDir()},
		Model: config.ModelSelection{Default: "minimax"},
		Models: []config.ModelConfig{{
			Name:     "minimax",
			Provider: "minimax",
			Model:    "MiniMax-M3",
			Enabled:  true,
		}},
		Agents: config.AgentsConfig{
			Default:  "main",
			Profiles: []config.AgentProfileConfig{{ID: "main", Name: "Main", Model: config.ModelSelection{Default: "minimax"}}},
		},
	}
	app := newTUIModel(context.Background(), cfg, "cli:default")
	if done := app.handleSlash(SlashCommand{Name: "model"}); done {
		t.Fatal("model command should not exit")
	}
	if app.picker == nil || app.picker.Kind != "models" {
		t.Fatalf("/model should open model picker, got %#v", app.picker)
	}
	app.picker = nil
	if done := app.handleSlash(SlashCommand{Name: "tools"}); done {
		t.Fatal("tools command should not exit")
	}
	if app.picker == nil || app.picker.Kind != "tools" {
		t.Fatalf("/tools should open tools picker, got %#v", app.picker)
	}
}

func TestTUISubmitShowsThinkingImmediately(t *testing.T) {
	app := newTUIModel(context.Background(), &config.Root{App: config.AppConfig{Home: t.TempDir()}}, "cli:default")
	app.input.SetValue("hello")
	cmd := app.submit()
	if cmd == nil {
		t.Fatal("submit should return task command")
	}
	joined := strings.Join(app.events, "\n")
	if !strings.Contains(joined, "User") || !strings.Contains(joined, "• Thinking") {
		t.Fatalf("submit should append user and thinking events:\n%s", joined)
	}
}

func TestTUISubmitWhileRunningKeepsDraft(t *testing.T) {
	app := newTUIModel(context.Background(), &config.Root{App: config.AppConfig{Home: t.TempDir()}}, "cli:default")
	app.running = true
	app.input.SetValue("不要吞掉这句")
	cmd := app.submit()
	if cmd != nil {
		t.Fatal("submit while running should not start another task")
	}
	if app.input.Value() != "不要吞掉这句" {
		t.Fatalf("draft should remain, got %q", app.input.Value())
	}
}

func TestTUIStatusLineUsesProgressSummary(t *testing.T) {
	app := newTUIModel(context.Background(), &config.Root{App: config.AppConfig{Home: t.TempDir()}}, "cli:default")
	app.running = true
	app.status = "Thinking"
	app.progress = 9
	app.toolEvents = 3
	line := app.statusLine(120)
	if strings.Contains(line, "9 events") || !strings.Contains(line, "3 tool updates") {
		t.Fatalf("status line = %q", line)
	}
}

func TestTUIEventsAreTrimmed(t *testing.T) {
	app := newTUIModel(context.Background(), &config.Root{App: config.AppConfig{Home: t.TempDir()}}, "cli:default")
	for i := 0; i < maxTUIEventLines+20; i++ {
		app.addEvent("line " + fmt.Sprint(i))
	}
	if len(app.events) > maxTUIEventLines {
		t.Fatalf("events not trimmed: %d", len(app.events))
	}
	if !strings.Contains(app.events[0], "trimmed") {
		t.Fatalf("missing trim notice: %q", app.events[0])
	}
}

func TestTUICommandPanelRunsDirectCommand(t *testing.T) {
	app := newTUIModel(context.Background(), &config.Root{App: config.AppConfig{Home: t.TempDir()}}, "cli:default")
	app.input.SetValue("help")
	app.commandPanel = true
	items := app.filteredCommandItems()
	for i, item := range items {
		if item.Command == "/help" {
			app.commandIndex = i
			break
		}
	}
	model, _ := app.updateCommandPanel(tea.KeyMsg{Type: tea.KeyEnter})
	next := model.(*tuiModel)
	if next.commandPanel {
		t.Fatal("command panel should close after enter")
	}
	if next.running || next.input.Value() != "" {
		t.Fatalf("direct command should run synchronously and clear input, running=%v input=%q", next.running, next.input.Value())
	}
	if !strings.Contains(strings.Join(next.events, "\n"), "Conversation") {
		t.Fatalf("help command should render help text")
	}
}

func TestTUICommandPanelOpensSessionPicker(t *testing.T) {
	home := t.TempDir()
	if err := session.NewStore(home).Save(session.State{Key: "cli:other"}); err != nil {
		t.Fatal(err)
	}
	app := newTUIModel(context.Background(), &config.Root{App: config.AppConfig{Home: home}}, "cli:default")
	app.input.SetValue("switch session")
	app.commandPanel = true
	items := app.filteredCommandItems()
	for i, item := range items {
		if item.Label == "Switch session" {
			app.commandIndex = i
			break
		}
	}
	model, _ := app.updateCommandPanel(tea.KeyMsg{Type: tea.KeyEnter})
	next := model.(*tuiModel)
	if next.commandPanel {
		t.Fatal("command panel should close after enter")
	}
	if next.input.Value() != "" {
		t.Fatalf("session action should clear input, got %q", next.input.Value())
	}
	if next.picker == nil || next.picker.Kind != "sessions" {
		t.Fatalf("session action should open sessions picker, got %#v", next.picker)
	}
}

func TestTUICommandPaletteViewShowsGroupedOverlay(t *testing.T) {
	app := newTUIModel(context.Background(), &config.Root{App: config.AppConfig{Home: t.TempDir()}}, "cli:default")
	app.width = 120
	app.height = 36
	app.commandPanel = true
	app.input.SetValue("/trace")
	app.resize()
	view := app.View()
	for _, want := range []string{"Commands", "Search", "trace", "Suggested", "Show trace", "Observe", "Trace summary", "Esc close"} {
		if !strings.Contains(view, want) {
			t.Fatalf("palette missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "mateway gateway status") || strings.Contains(view, "mateway memory proposal list") {
		t.Fatalf("palette should not show raw external commands:\n%s", view)
	}
}

func TestTUISessionsPickerSwitchesSession(t *testing.T) {
	home := t.TempDir()
	store := session.NewStore(home)
	if err := store.Save(session.State{Key: "cli:first"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(session.State{Key: "cli:second"}); err != nil {
		t.Fatal(err)
	}
	app := newTUIModel(context.Background(), &config.Root{App: config.AppConfig{Home: home}}, "cli:first")
	if done := app.handleSlash(SlashCommand{Name: "sessions"}); done {
		t.Fatal("sessions should not exit")
	}
	if app.picker == nil || app.picker.Kind != "sessions" {
		t.Fatalf("expected sessions picker, got %#v", app.picker)
	}
	for i, item := range app.filteredPickerItems() {
		if item.Value == "cli:second" {
			app.picker.Index = i
			break
		}
	}
	app.acceptPicker()
	if app.sessionKey != "cli:second" {
		t.Fatalf("session key = %q", app.sessionKey)
	}
}

func TestTUIModelsPickerSwitchesCurrentModel(t *testing.T) {
	cfg := &config.Root{
		App:   config.AppConfig{Home: t.TempDir()},
		Model: config.ModelSelection{Default: "a"},
		Models: []config.ModelConfig{
			{Name: "a", Provider: "test", Model: "model-a", Enabled: true},
			{Name: "b", Provider: "test", Model: "model-b", Enabled: true},
		},
		Agents: config.AgentsConfig{
			Default:  "main",
			Profiles: []config.AgentProfileConfig{{ID: "main", Model: config.ModelSelection{Default: "a"}}},
		},
	}
	app := newTUIModel(context.Background(), cfg, "cli:default")
	app.openModelsPicker()
	for i, item := range app.filteredPickerItems() {
		if item.Value == "b" {
			app.picker.Index = i
			break
		}
	}
	app.acceptPicker()
	if app.model.Display() != "model-b" {
		t.Fatalf("model = %q", app.model.Display())
	}
}

func TestTUIToolsPickerTogglesToolAccess(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Root{
		App: config.AppConfig{Home: home},
		Agents: config.AgentsConfig{
			Default:  "main",
			Profiles: []config.AgentProfileConfig{{ID: "main"}},
		},
	}
	app := newTUIModel(context.Background(), cfg, "cli:default")
	app.openToolsPicker()
	for i, item := range app.filteredPickerItems() {
		if item.Value == "terminal.run" {
			app.picker.Index = i
			break
		}
	}
	app.acceptPicker()
	if !containsAccessValue(app.cfg.Agents.Profiles[0].Tools.Deny, "terminal.run") {
		t.Fatalf("terminal.run should be denied: %#v", app.cfg.Agents.Profiles[0].Tools)
	}
}

func TestTUIHistorySupportsUTF8(t *testing.T) {
	app := newTUIModel(context.Background(), &config.Root{App: config.AppConfig{Home: t.TempDir()}}, "cli:default")
	app.recordHistory("第一条")
	app.recordHistory("第二条")
	app.historyUp()
	if app.input.Value() != "第二条" {
		t.Fatalf("history up = %q", app.input.Value())
	}
	app.historyUp()
	if app.input.Value() != "第一条" {
		t.Fatalf("second history up = %q", app.input.Value())
	}
	app.historyDown()
	if app.input.Value() != "第二条" {
		t.Fatalf("history down = %q", app.input.Value())
	}
	app.historyDown()
	if app.input.Value() != "" {
		t.Fatalf("history down should clear at end, got %q", app.input.Value())
	}
}

func TestTUIScrollStopsAutoFollowAndReportsNewEvents(t *testing.T) {
	app := newTUIModel(context.Background(), &config.Root{App: config.AppConfig{Home: t.TempDir()}}, "cli:default")
	app.width = 100
	app.height = 8
	app.resize()
	for i := 0; i < 20; i++ {
		app.addEvent("line")
	}
	app.viewport.ViewUp()
	app.autoFollow = false
	app.addEvent("fresh event")
	if app.newEvents == 0 {
		t.Fatal("new events should increment while not following")
	}
	line := app.statusLine(100)
	if !strings.Contains(line, "new events") {
		t.Fatalf("status line should mention new events: %q", line)
	}
}

func TestRenderTUIProgressBlockShowsToolFields(t *testing.T) {
	block := renderTUIProgressBlock(channel.ProgressStep{
		Tool:       "terminal.run",
		Status:     "completed",
		Summary:    "ok",
		DurationMS: 42,
	})
	for _, want := range []string{"Tool Run", "summary: ok", "duration: 42ms", "completed"} {
		if !strings.Contains(block, want) {
			t.Fatalf("progress block missing %q:\n%s", want, block)
		}
	}
}

func TestTUITextareaHandlesUTF8(t *testing.T) {
	app := newTUIModel(context.Background(), &config.Root{App: config.AppConfig{Home: t.TempDir()}}, "cli:default")
	app.input.SetValue("中文")
	if app.input.Value() != "中文" {
		t.Fatalf("textarea value = %q", app.input.Value())
	}
}

func TestTUIResultAppendsAssistantAndTrace(t *testing.T) {
	app := newTUIModel(context.Background(), &config.Root{App: config.AppConfig{Home: t.TempDir()}}, "cli:default")
	app.width = 120
	app.height = 30
	app.resize()
	app.Update(tuiResultMsg{
		reply:     channel.OutboundMessage{Text: "done"},
		tracePath: "/tmp/trace.jsonl",
	})
	content := strings.Join(app.events, "\n")
	if !strings.Contains(content, "Assistant") || !strings.Contains(content, "done") || !strings.Contains(content, "trace: /tmp/trace.jsonl") {
		t.Fatalf("content = %q", content)
	}
}

func TestRenderMarkdownForTUIAddsANSI(t *testing.T) {
	rendered := renderMarkdownForTUI("## Title\n\n**bold**\n\n| A | B |\n|---|---|\n| 1 | 2 |", 80)
	if !strings.Contains(rendered, "\x1b[") {
		t.Fatalf("expected ANSI markdown rendering, got %q", rendered)
	}
	if !strings.Contains(rendered, "Title") || !strings.Contains(rendered, "bold") {
		t.Fatalf("rendered markdown missing content: %q", rendered)
	}
}

func TestRenderMarkdownForTUILargeInputFallsBackToPlainText(t *testing.T) {
	text := strings.Repeat("large markdown\n", maxTUIMarkdownRenderBytes)
	rendered := renderMarkdownForTUI(text, 80)
	if strings.Contains(rendered, "\x1b[") {
		t.Fatalf("large markdown should not be ANSI rendered")
	}
	if rendered != strings.TrimSpace(text) {
		t.Fatalf("large markdown should be returned as plain text")
	}
}

func TestPrintChatHelpIsGroupedAndUseful(t *testing.T) {
	var out bytes.Buffer
	printChatHelp(&out)
	text := out.String()
	for _, want := range []string{"Conversation", "Session", "Observe", "Agent / Model", "Tools", "Channel", "Memory", "Local", "e.g. /trace"} {
		if !strings.Contains(text, want) {
			t.Fatalf("help missing %q:\n%s", want, text)
		}
	}
}

func TestSidebarTraceFallbackUsage(t *testing.T) {
	home := t.TempDir()
	tracePath := filepath.Join(home, "trace.jsonl")
	trace := `{"type":"model_usage","requests":1,"input_tokens":11,"output_tokens":7,"total_tokens":18}` + "\n"
	if err := os.WriteFile(tracePath, []byte(trace), 0o600); err != nil {
		t.Fatal(err)
	}
	state := session.State{
		Key: "cli:default",
		Tasks: []session.TaskNode{{
			ID:        "task-1",
			Status:    "completed",
			TracePath: tracePath,
		}},
	}
	if err := session.NewStore(home).Save(state); err != nil {
		t.Fatal(err)
	}
	app := newTUIModel(context.Background(), &config.Root{App: config.AppConfig{Home: home}}, "cli:default")
	summary := app.sidebarSummary(state)
	joined := strings.Join(append(summary.UsageLines, summary.TraceLines...), "\n")
	if !strings.Contains(joined, "18 tokens") || !strings.Contains(joined, "1 requests") {
		t.Fatalf("sidebar should derive usage from trace, got %#v", summary)
	}
}
