package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/runtime"
	"github.com/dongping/mateway/internal/session"
)

type AskOptions struct {
	Config     *config.Root
	Message    string
	SessionKey string
	Quiet      bool
	JSON       bool
	Events     bool
	In         io.Reader
	Out        io.Writer
}

func RunAsk(ctx context.Context, opts AskOptions) error {
	if enabledCount(opts.Quiet, opts.JSON, opts.Events) > 1 {
		return fmt.Errorf("--quiet, --json, and --events are mutually exclusive")
	}
	cfg := opts.Config
	if cfg == nil {
		return fmt.Errorf("config is required")
	}
	text := strings.TrimSpace(opts.Message)
	if text == "" && opts.In != nil {
		data, err := io.ReadAll(opts.In)
		if err != nil {
			return err
		}
		text = strings.TrimSpace(string(data))
	}
	if text == "" {
		return fmt.Errorf("message is required")
	}
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	rt := runtime.New(cfg)
	renderer := &Renderer{Out: out, Quiet: opts.Quiet || opts.JSON || opts.Events}
	events := &NDJSONEventWriter{Out: out}
	if opts.Events {
		rt.ProgressSink = events.Progress
	} else {
		rt.ProgressSink = renderer.Progress
	}
	renderer.User(text)
	msg := inbound(text, ResolveSessionKey(opts.SessionKey))
	resp, err := rt.Handle(ctx, msg)
	if err != nil {
		return err
	}
	if opts.Events {
		return events.Final(resp, msg.SessionKey)
	}
	if opts.JSON {
		return json.NewEncoder(out).Encode(map[string]any{
			"text":        resp.Reply.Text,
			"style":       resp.Reply.Style,
			"trace_id":    resp.TraceID,
			"trace_path":  resp.TracePath,
			"session_key": msg.SessionKey,
			"failed":      resp.Failed,
		})
	}
	renderer.Reply(channel.OutboundBatch{Reply: resp.Reply, FollowUps: resp.FollowUps})
	return nil
}

func enabledCount(values ...bool) int {
	count := 0
	for _, value := range values {
		if value {
			count++
		}
	}
	return count
}

type ChatOptions struct {
	Config     *config.Root
	SessionKey string
	In         io.Reader
	Out        io.Writer
}

func RunChat(ctx context.Context, opts ChatOptions) error {
	cfg := opts.Config
	if cfg == nil {
		return fmt.Errorf("config is required")
	}
	in := opts.In
	if in == nil {
		in = os.Stdin
	}
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	state := chatState{
		cfg:        cfg,
		store:      session.NewStore(cfg.App.Home),
		sessionKey: ResolveSessionKey(opts.SessionKey),
		in:         in,
		out:        out,
		reader:     bufio.NewReader(in),
	}
	fmt.Fprintf(out, "mateway chat session=%s\n", state.sessionKey)
	fmt.Fprintln(out, "type /help for commands, /exit to quit")
	for {
		fmt.Fprintf(out, "%s> ", state.sessionKey)
		line, err := state.readLine()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if cmd, ok := ParseSlash(line); ok {
			done, err := state.handleSlash(ctx, cmd)
			if err == errUnknownSlashCommand {
				if err := state.ask(ctx, line); err != nil {
					fmt.Fprintln(out, "error:", err)
				}
				continue
			}
			if err != nil {
				fmt.Fprintln(out, "error:", err)
			}
			if done {
				break
			}
			continue
		}
		if err := state.ask(ctx, line); err != nil {
			fmt.Fprintln(out, "error:", err)
		}
	}
	return nil
}

type chatState struct {
	cfg        *config.Root
	store      session.Store
	sessionKey string
	in         io.Reader
	out        io.Writer
	reader     *bufio.Reader
}

var errUnknownSlashCommand = fmt.Errorf("unknown slash command")

func (s *chatState) readLine() (string, error) {
	if s.reader == nil {
		s.reader = bufio.NewReader(s.in)
	}
	return s.reader.ReadString('\n')
}

func (s *chatState) ask(ctx context.Context, text string) error {
	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	rt := runtime.New(s.cfg)
	rt.Hooks.ApproveToolCall = s.approveToolCall
	renderer := &Renderer{Out: s.out}
	rt.ProgressSink = renderer.Progress
	renderer.User(text)
	resp, err := rt.Handle(runCtx, inbound(text, s.sessionKey))
	if err != nil {
		return err
	}
	renderer.Reply(channel.OutboundBatch{Reply: resp.Reply, FollowUps: resp.FollowUps})
	if strings.TrimSpace(resp.TracePath) != "" {
		fmt.Fprintln(s.out, "trace:", resp.TracePath)
	}
	return nil
}

func (s *chatState) approveToolCall(_ context.Context, req runtime.ApprovalRequest) (runtime.ApprovalDecision, error) {
	color := outputColorEnabled(s.out)
	fmt.Fprintln(s.out)
	fmt.Fprintln(s.out, colorize("! Approval required", ansiYellow, color))
	fmt.Fprintln(s.out, "  reason:", req.Reason)
	fmt.Fprintln(s.out, "  tool:  ", friendlyToolName(req.ToolCall.Name))
	if detail := approvalDetail(req.ToolCall); detail != "" {
		fmt.Fprintln(s.out, "  detail:", detail)
	}
	fmt.Fprint(s.out, "allow? [y/N]: ")
	line, err := s.readLine()
	if err != nil && strings.TrimSpace(line) == "" {
		return runtime.ApprovalDecision{Approved: false, Reason: "approval input failed"}, nil
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return runtime.ApprovalDecision{Approved: answer == "y" || answer == "yes"}, nil
}

func approvalDetail(call agentcore.ToolCall) string {
	switch call.Name {
	case "terminal.run":
		return compactInline(fmt.Sprint(call.Args["command"]), 240)
	default:
		return compactInline(fmt.Sprint(call.Args), 240)
	}
}

func (s *chatState) handleSlash(ctx context.Context, cmd SlashCommand) (bool, error) {
	switch cmd.Name {
	case "help", "?":
		printChatHelp(s.out)
	case "exit", "quit", "q":
		return true, nil
	case "new":
		return false, s.ask(ctx, "/new")
	case "session":
		if len(cmd.Args) != 1 {
			return false, fmt.Errorf("usage: /session <session_key>")
		}
		s.sessionKey = ResolveSessionKey(cmd.Args[0])
		fmt.Fprintln(s.out, "session:", s.sessionKey)
	case "sessions":
		return false, s.printSessions()
	case "show":
		key := s.sessionKey
		if len(cmd.Args) > 0 {
			key = ResolveSessionKey(cmd.Args[0])
		}
		return false, s.printSession(key)
	case "trace":
		return false, s.printTrace(cmd.Args)
	case "events":
		return false, s.printEvents(cmd.Args)
	case "tools":
		return false, s.handleToolsSlash(cmd.Args)
	case "model":
		verbose, agentID, err := parseModelSlashArgs(cmd.Args)
		if err != nil {
			return false, err
		}
		return false, PrintModel(s.out, s.cfg, agentID, verbose)
	case "resume":
		return false, s.resume(cmd.Args)
	default:
		return false, errUnknownSlashCommand
	}
	return false, nil
}

func (s *chatState) handleToolsSlash(args []string) error {
	verbose := false
	agentID := ""
	var values []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--verbose":
			verbose = true
		case "--agent":
			if i+1 >= len(args) {
				return fmt.Errorf("usage: /tools [--agent <agent_id>] [--verbose]")
			}
			i++
			agentID = args[i]
		default:
			values = append(values, args[i])
		}
	}
	if len(values) == 0 {
		return PrintTools(s.out, s.cfg, agentID, verbose)
	}
	if len(values) != 2 {
		return fmt.Errorf("usage: /tools [enable|disable] <tool_name> [--agent <agent_id>]")
	}
	var (
		change ToolAccessChange
		err    error
	)
	switch values[0] {
	case "enable":
		change, err = EnableTool(s.cfg, agentID, values[1])
	case "disable":
		change, err = DisableTool(s.cfg, agentID, values[1])
	default:
		return fmt.Errorf("usage: /tools [enable|disable] <tool_name> [--agent <agent_id>]")
	}
	if err != nil {
		return err
	}
	PrintToolAccessChange(s.out, change)
	return nil
}

func (s *chatState) printSessions() error {
	keys, err := s.store.List()
	if err != nil {
		return err
	}
	sort.Strings(keys)
	for _, key := range keys {
		state, err := s.store.Load(key)
		if err != nil {
			continue
		}
		active := ""
		if key == s.sessionKey {
			active = "*"
		}
		fmt.Fprintf(s.out, "%s%s messages=%d tasks=%d updated=%s\n", active, key, len(state.Messages), len(state.Tasks), state.UpdatedAt.Format("2006-01-02 15:04:05"))
	}
	return nil
}

func (s *chatState) printSession(key string) error {
	state, err := s.store.Load(key)
	if err != nil {
		return err
	}
	fmt.Fprintln(s.out, "session:", state.Key)
	fmt.Fprintln(s.out, "messages:", len(state.Messages))
	fmt.Fprintln(s.out, "tasks:", len(state.Tasks))
	fmt.Fprintln(s.out, "active_task:", state.ActiveTask)
	for _, task := range state.Tasks {
		fmt.Fprintf(s.out, "- %s %s %s\n", task.ID, task.Status, task.Goal)
		if strings.TrimSpace(task.Summary) != "" {
			fmt.Fprintln(s.out, "  summary:", task.Summary)
		}
	}
	return nil
}

func (s *chatState) printTrace(args []string) error {
	path, err := s.resolveTracePath(args)
	if err != nil {
		return err
	}
	return printTraceSummary(s.out, path)
}

func (s *chatState) printEvents(args []string) error {
	opts := TraceEventsOptions{}
	var filtered []string
	for _, arg := range args {
		if arg == "--json" {
			opts.JSON = true
			continue
		}
		filtered = append(filtered, arg)
	}
	path, err := s.resolveTracePath(filtered)
	if err != nil {
		return err
	}
	fmt.Fprintln(s.out, "trace:", path)
	return PrintTraceEventsWithOptions(s.out, path, opts)
}

func (s *chatState) resolveTracePath(args []string) (string, error) {
	if len(args) > 1 {
		return "", fmt.Errorf("usage: /trace [trace_path|session_key]")
	}
	if len(args) == 1 {
		value := strings.TrimSpace(args[0])
		if strings.Contains(value, "/") || strings.HasSuffix(value, ".jsonl") {
			return value, nil
		}
		state, err := s.store.Load(ResolveSessionKey(value))
		if err != nil {
			return "", err
		}
		if path := latestTracePath(state); path != "" {
			return path, nil
		}
		return "", fmt.Errorf("session %q has no trace", ResolveSessionKey(value))
	}
	state, err := s.store.Load(s.sessionKey)
	if err != nil {
		return "", err
	}
	if path := latestTracePath(state); path != "" {
		return path, nil
	}
	return "", fmt.Errorf("session %q has no trace", s.sessionKey)
}

func (s *chatState) resume(args []string) error {
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
		s.sessionKey = source
		fmt.Fprintln(s.out, "attached:", s.sessionKey)
		return nil
	}
	if err := ForkSession(s.store, source, s.sessionKey); err != nil {
		return err
	}
	fmt.Fprintf(s.out, "resumed %s into %s\n", source, s.sessionKey)
	return nil
}

func printChatHelp(out io.Writer) {
	fmt.Fprintln(out, "slash commands:")
	fmt.Fprintln(out, "  /help")
	fmt.Fprintln(out, "  /new")
	fmt.Fprintln(out, "  /sessions")
	fmt.Fprintln(out, "  /session <session_key>")
	fmt.Fprintln(out, "  /resume [--attach] <session_key>")
	fmt.Fprintln(out, "  /show [session_key]")
	fmt.Fprintln(out, "  /trace [trace_path|session_key]")
	fmt.Fprintln(out, "  /events [trace_path|session_key]")
	fmt.Fprintln(out, "  /tools [--agent <agent_id>] [--verbose]")
	fmt.Fprintln(out, "  /tools enable|disable <tool_name> [--agent <agent_id>]")
	fmt.Fprintln(out, "  /model [--agent <agent_id>] [--verbose]")
	fmt.Fprintln(out, "  /exit")
}

func parseModelSlashArgs(args []string) (bool, string, error) {
	verbose := false
	agentID := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--verbose":
			verbose = true
		case "--agent":
			if i+1 >= len(args) {
				return false, "", fmt.Errorf("usage: /model [--agent <agent_id>] [--verbose]")
			}
			i++
			agentID = args[i]
		default:
			if agentID != "" {
				return false, "", fmt.Errorf("usage: /model [--agent <agent_id>] [--verbose]")
			}
			agentID = args[i]
		}
	}
	return verbose, agentID, nil
}

func inbound(text, sessionKey string) channel.InboundMessage {
	return channel.InboundMessage{
		ID:         "cli",
		Channel:    "cli",
		ThreadID:   strings.TrimPrefix(sessionKey, "cli:"),
		UserID:     "local",
		SessionKey: sessionKey,
		Text:       text,
	}
}
