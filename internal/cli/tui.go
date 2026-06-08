package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/runtime"
	"github.com/dongping/mateway/internal/session"
	"golang.org/x/term"
)

type TUIOptions struct {
	Config     *config.Root
	SessionKey string
	In         io.Reader
	Out        io.Writer
}

func RunTUI(ctx context.Context, opts TUIOptions) error {
	if opts.Config == nil {
		return fmt.Errorf("config is required")
	}
	inFile, ok := opts.In.(*os.File)
	if opts.In == nil {
		inFile = os.Stdin
		ok = true
	}
	outFile, outOK := opts.Out.(*os.File)
	if opts.Out == nil {
		outFile = os.Stdout
		outOK = true
	}
	if !ok || !outOK || !CanRunTUI(inFile, outFile) {
		return fmt.Errorf("tui requires an interactive terminal")
	}
	oldState, err := term.MakeRaw(int(inFile.Fd()))
	if err != nil {
		return err
	}
	defer term.Restore(int(inFile.Fd()), oldState)
	app := newTUIApp(opts.Config, ResolveSessionKey(opts.SessionKey), inFile, outFile)
	return app.run(ctx)
}

func CanRunTUI(in, out *os.File) bool {
	return in != nil && out != nil && term.IsTerminal(int(in.Fd())) && term.IsTerminal(int(out.Fd()))
}

type tuiApp struct {
	cfg        *config.Root
	sessionKey string
	in         *os.File
	out        *os.File

	mu       sync.Mutex
	input    string
	events   []string
	scroll   int
	running  bool
	approval *tuiApproval
	errText  string
}

type tuiApproval struct {
	request runtime.ApprovalRequest
	reply   chan runtime.ApprovalDecision
}

func newTUIApp(cfg *config.Root, sessionKey string, in, out *os.File) *tuiApp {
	return &tuiApp{cfg: cfg, sessionKey: sessionKey, in: in, out: out}
}

func (a *tuiApp) run(ctx context.Context) error {
	a.enterScreen()
	defer a.leaveScreen()
	a.addEvent(colorize("New session - "+time.Now().Format("2006-01-02 15:04:05"), ansiDim, true))
	a.render()
	buf := make([]byte, 1)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		n, err := a.in.Read(buf)
		if err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		b := buf[0]
		switch b {
		case 3:
			return nil
		case 27:
			a.readEscape()
		case '\r', '\n':
			if a.submit(ctx) {
				return nil
			}
		case 127, 8:
			a.backspace()
		default:
			if b >= 32 && b != 127 {
				a.appendInput(string(b))
			}
		}
		a.render()
	}
}

func (a *tuiApp) submit(ctx context.Context) bool {
	a.mu.Lock()
	text := strings.TrimSpace(a.input)
	approval := a.approval
	if approval != nil {
		a.input = ""
		a.mu.Unlock()
		answer := strings.ToLower(text)
		approval.reply <- runtime.ApprovalDecision{Approved: answer == "y" || answer == "yes"}
		return false
	}
	if text == "" || a.running {
		a.mu.Unlock()
		return false
	}
	a.input = ""
	if cmd, ok := ParseSlash(text); ok {
		a.mu.Unlock()
		return a.handleSlash(ctx, cmd)
	}
	a.running = true
	a.events = append(a.events, colorize("User", ansiCyan, true), "│ "+compactBlock(text, 600), "")
	a.scroll = 0
	a.mu.Unlock()
	go a.runTask(ctx, text)
	return false
}

func (a *tuiApp) runTask(ctx context.Context, text string) {
	rt := runtime.New(a.cfg)
	rt.ProgressSink = func(msg channel.OutboundMessage) {
		if len(msg.Progress) == 0 {
			return
		}
		line := renderProgressLine(msg.Progress[len(msg.Progress)-1], true)
		if strings.TrimSpace(line) == "" {
			return
		}
		a.addEvent(line)
		a.render()
	}
	rt.Hooks.ApproveToolCall = func(_ context.Context, req runtime.ApprovalRequest) (runtime.ApprovalDecision, error) {
		approval := &tuiApproval{request: req, reply: make(chan runtime.ApprovalDecision, 1)}
		a.mu.Lock()
		a.approval = approval
		a.events = append(a.events, "", colorize("! Approval required", ansiYellow, true), "  tool:   "+friendlyToolName(req.ToolCall.Name), "  detail: "+approvalDetail(req.ToolCall), "  enter y or n")
		a.mu.Unlock()
		a.render()
		decision := <-approval.reply
		a.mu.Lock()
		a.approval = nil
		a.mu.Unlock()
		a.render()
		return decision, nil
	}
	resp, err := rt.Handle(ctx, inbound(text, a.sessionKey))
	a.mu.Lock()
	a.running = false
	if err != nil {
		a.errText = err.Error()
		a.events = append(a.events, "", colorize("Error", ansiRed, true), compactBlock(err.Error(), 600))
		a.mu.Unlock()
		a.render()
		return
	}
	a.events = append(a.events, "", colorize("Assistant", ansiGreen, true))
	for _, msg := range (channel.OutboundBatch{Reply: resp.Reply, FollowUps: resp.FollowUps}).Messages() {
		a.events = append(a.events, wrapLines(msg.Text, 120)...)
		a.events = append(a.events, "")
	}
	if strings.TrimSpace(resp.TracePath) != "" {
		a.events = append(a.events, colorize("trace: "+resp.TracePath, ansiDim, true))
	}
	a.mu.Unlock()
	a.render()
}

func (a *tuiApp) appendInput(value string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.input += value
}

func (a *tuiApp) backspace() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.input) > 0 {
		a.input = a.input[:len(a.input)-1]
	}
}

func (a *tuiApp) readEscape() {
	buf := make([]byte, 2)
	n, _ := a.in.Read(buf)
	if n < 2 || buf[0] != '[' {
		return
	}
	switch buf[1] {
	case 'A':
		a.scrollBy(1)
	case 'B':
		a.scrollBy(-1)
	case '5':
		_, _ = a.in.Read(buf[:1])
		a.scrollBy(10)
	case '6':
		_, _ = a.in.Read(buf[:1])
		a.scrollBy(-10)
	}
}

func (a *tuiApp) scrollBy(delta int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.scroll += delta
	if a.scroll < 0 {
		a.scroll = 0
	}
	if a.scroll > len(a.events) {
		a.scroll = len(a.events)
	}
}

func (a *tuiApp) addEvent(line string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.events) > 0 && a.events[len(a.events)-1] == line {
		return
	}
	a.events = append(a.events, line)
	a.scroll = 0
}

func (a *tuiApp) handleSlash(ctx context.Context, cmd SlashCommand) bool {
	switch cmd.Name {
	case "exit", "quit", "q":
		return true
	case "help", "?":
		a.addEvent(colorize("Commands", ansiGreen, true))
		a.addEvent("/help  /exit  /new  /session <key>  /sessions  /resume [--attach] <key>")
		a.addEvent("/show [key]  /trace [path|key]  /events [path|key]")
	case "new":
		a.mu.Lock()
		if a.running {
			a.mu.Unlock()
			a.addEvent(colorize("task is already running", ansiYellow, true))
			return false
		}
		a.running = true
		a.events = append(a.events, colorize("User", ansiCyan, true), "│ /new", "")
		a.scroll = 0
		a.mu.Unlock()
		go a.runTask(ctx, "/new")
	case "session":
		if len(cmd.Args) != 1 {
			a.addEvent(colorize("usage: /session <session_key>", ansiYellow, true))
			return false
		}
		next := ResolveSessionKey(cmd.Args[0])
		a.setSessionKey(next)
		a.addEvent("session: " + next)
	case "sessions":
		keys, err := session.NewStore(a.cfg.App.Home).List()
		if err != nil {
			a.addEvent(colorize("error: "+err.Error(), ansiRed, true))
			return false
		}
		sort.Strings(keys)
		a.addEvent(colorize("Sessions", ansiGreen, true))
		for _, key := range keys {
			prefix := "  "
			if key == a.currentSessionKey() {
				prefix = "* "
			}
			a.addEvent(prefix + key)
		}
	case "show":
		key := a.currentSessionKey()
		if len(cmd.Args) > 0 {
			key = ResolveSessionKey(cmd.Args[0])
		}
		state, err := session.NewStore(a.cfg.App.Home).Load(key)
		if err != nil {
			a.addEvent(colorize("error: "+err.Error(), ansiRed, true))
			return false
		}
		a.addEvent(colorize("Session", ansiGreen, true))
		a.addEvent(fmt.Sprintf("key=%s messages=%d tasks=%d active=%s", state.Key, len(state.Messages), len(state.Tasks), state.ActiveTask))
	case "trace":
		path, err := a.resolveTracePath(cmd.Args)
		if err != nil {
			a.addEvent(colorize("error: "+err.Error(), ansiRed, true))
			return false
		}
		var out bytes.Buffer
		if err := printTraceSummary(&out, path); err != nil {
			a.addEvent(colorize("error: "+err.Error(), ansiRed, true))
			return false
		}
		a.addOutput(out.String())
	case "events":
		path, err := a.resolveTracePath(cmd.Args)
		if err != nil {
			a.addEvent(colorize("error: "+err.Error(), ansiRed, true))
			return false
		}
		var out bytes.Buffer
		fmt.Fprintln(&out, "trace:", path)
		if err := PrintTraceEventsWithOptions(&out, path, TraceEventsOptions{}); err != nil {
			a.addEvent(colorize("error: "+err.Error(), ansiRed, true))
			return false
		}
		a.addOutput(out.String())
	case "resume":
		if err := a.resume(cmd.Args); err != nil {
			a.addEvent(colorize("error: "+err.Error(), ansiRed, true))
		}
	default:
		a.addEvent(colorize("unknown command: /"+cmd.Name, ansiYellow, true))
	}
	return false
}

func (a *tuiApp) currentSessionKey() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessionKey
}

func (a *tuiApp) setSessionKey(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessionKey = key
	a.scroll = 0
}

func (a *tuiApp) addOutput(text string) {
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		a.addEvent(line)
	}
}

func (a *tuiApp) resolveTracePath(args []string) (string, error) {
	if len(args) > 1 {
		return "", fmt.Errorf("usage: /trace [trace_path|session_key]")
	}
	store := session.NewStore(a.cfg.App.Home)
	if len(args) == 1 {
		value := strings.TrimSpace(args[0])
		if strings.Contains(value, "/") || strings.HasSuffix(value, ".jsonl") {
			return value, nil
		}
		key := ResolveSessionKey(value)
		state, err := store.Load(key)
		if err != nil {
			return "", err
		}
		if path := latestTracePath(state); path != "" {
			return path, nil
		}
		return "", fmt.Errorf("session %q has no trace", key)
	}
	key := a.currentSessionKey()
	state, err := store.Load(key)
	if err != nil {
		return "", err
	}
	if path := latestTracePath(state); path != "" {
		return path, nil
	}
	return "", fmt.Errorf("session %q has no trace", key)
}

func (a *tuiApp) resume(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: /resume [--attach] <session_key>")
	}
	attach := false
	var values []string
	for _, arg := range args {
		switch arg {
		case "--attach":
			attach = true
		default:
			values = append(values, arg)
		}
	}
	if len(values) != 1 {
		return fmt.Errorf("usage: /resume [--attach] <session_key>")
	}
	source := ResolveSessionKey(values[0])
	if attach {
		a.setSessionKey(source)
		a.addEvent("attached: " + source)
		return nil
	}
	target := a.currentSessionKey()
	if err := ForkSession(session.NewStore(a.cfg.App.Home), source, target); err != nil {
		return err
	}
	a.addEvent(fmt.Sprintf("resumed %s into %s", source, target))
	return nil
}

func (a *tuiApp) render() {
	a.mu.Lock()
	defer a.mu.Unlock()
	width, height, err := term.GetSize(int(a.out.Fd()))
	if err != nil || width <= 0 || height <= 0 {
		width, height = 120, 40
	}
	sidebar := 28
	if width < 100 {
		sidebar = 0
	}
	mainWidth := width - sidebar - 2
	if mainWidth < 40 {
		mainWidth = width
	}
	state, _ := session.NewStore(a.cfg.App.Home).Load(a.sessionKey)
	var b strings.Builder
	b.WriteString("\x1b[H\x1b[2J")
	contentHeight := height - 4
	lines := visibleWindow(a.events, contentHeight, a.scroll)
	for row := 0; row < contentHeight; row++ {
		left := ""
		if row < len(lines) {
			left = truncateANSI(lines[row], mainWidth)
		}
		b.WriteString(padVisible(left, mainWidth))
		if sidebar > 0 {
			b.WriteString("  ")
			b.WriteString(a.sidebarLine(row, sidebar, state))
		}
		b.WriteString("\r\n")
	}
	b.WriteString(strings.Repeat("─", maxInt(0, width)) + "\r\n")
	prompt := colorize("Build", ansiBlue, true) + " · " + colorize("MiniMax-M3", ansiDim, true)
	if a.approval != nil {
		prompt = colorize("Approval", ansiYellow, true) + " · enter y or n"
	}
	if a.running {
		prompt += colorize(" · running", ansiDim, true)
	}
	b.WriteString(truncateANSI(prompt, width) + "\r\n")
	b.WriteString(truncateANSI("  "+a.input, width) + "\r\n")
	_, _ = a.out.WriteString(b.String())
}

func (a *tuiApp) sidebarLine(row, width int, state session.State) string {
	lines := []string{
		colorize("Session", ansiGreen, true),
		a.sessionKey,
		"",
		colorize("Context", ansiGreen, true),
		fmt.Sprintf("%d messages", len(state.Messages)),
		fmt.Sprintf("%d tasks", len(state.Tasks)),
		"",
		colorize("Todo", ansiGreen, true),
	}
	for _, task := range state.Tasks {
		status := "[ ]"
		if !session.IsOpenTaskStatus(task.Status) {
			status = "[x]"
		}
		lines = append(lines, status+" "+compactInline(task.Goal, width-4))
	}
	if len(state.Tasks) == 0 {
		lines = append(lines, colorize("No tasks yet", ansiDim, true))
	}
	if row >= len(lines) {
		return strings.Repeat(" ", width)
	}
	return padVisible(truncateANSI(lines[row], width), width)
}

func (a *tuiApp) enterScreen() {
	_, _ = a.out.WriteString("\x1b[?1049h\x1b[?25l")
}

func (a *tuiApp) leaveScreen() {
	_, _ = a.out.WriteString("\x1b[?25h\x1b[?1049l")
}

func tailLines(lines []string, limit int) []string {
	if limit <= 0 || len(lines) <= limit {
		return lines
	}
	return lines[len(lines)-limit:]
}

func visibleWindow(lines []string, limit, scroll int) []string {
	if limit <= 0 || len(lines) <= limit {
		return lines
	}
	end := len(lines) - scroll
	if end < limit {
		end = limit
	}
	if end > len(lines) {
		end = len(lines)
	}
	return lines[end-limit : end]
}

func wrapLines(text string, width int) []string {
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		line = strings.TrimRight(line, "\r")
		for len(line) > width && width > 0 {
			out = append(out, line[:width])
			line = line[width:]
		}
		out = append(out, line)
	}
	return out
}

func truncateANSI(text string, width int) string {
	if width <= 0 || visibleLen(text) <= width {
		return text
	}
	return text[:minInt(len(text), width)]
}

func padVisible(text string, width int) string {
	n := visibleLen(text)
	if n >= width {
		return text
	}
	return text + strings.Repeat(" ", width-n)
}

func visibleLen(text string) int {
	n := 0
	inEscape := false
	for i := 0; i < len(text); i++ {
		if text[i] == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			if text[i] == 'm' {
				inEscape = false
			}
			continue
		}
		n++
	}
	return n
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
