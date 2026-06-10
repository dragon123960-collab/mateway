package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/dongping/mateway/internal/channel"
	"golang.org/x/term"
)

type Renderer struct {
	Out   io.Writer
	Quiet bool

	mu        sync.Mutex
	lastLine  string
	hasEvents bool
}

func (r *Renderer) User(text string) {
	if r == nil || r.Quiet || strings.TrimSpace(text) == "" {
		return
	}
	color := r.colorEnabled()
	fmt.Fprintln(r.output(), colorize("User", ansiCyan, color))
	fmt.Fprintln(r.output(), "│ "+compactBlock(text, 600))
	fmt.Fprintln(r.output())
}

func (r *Renderer) Progress(msg channel.OutboundMessage) {
	if r == nil || r.Quiet {
		return
	}
	if len(msg.Progress) == 0 {
		return
	}
	step := msg.Progress[len(msg.Progress)-1]
	line := renderProgressLine(step, r.colorEnabled())
	if strings.TrimSpace(line) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if line == r.lastLine {
		return
	}
	r.lastLine = line
	r.hasEvents = true
	fmt.Fprintln(r.output(), line)
}

func (r *Renderer) Reply(resp channel.OutboundBatch) {
	if r == nil {
		return
	}
	messages := resp.Messages()
	if len(messages) == 0 {
		return
	}
	color := r.colorEnabled()
	r.mu.Lock()
	if r.hasEvents {
		fmt.Fprintln(r.output())
		r.hasEvents = false
	}
	r.mu.Unlock()
	fmt.Fprintln(r.output(), colorize("Assistant", ansiGreen, color))
	for i, msg := range resp.Messages() {
		if i > 0 {
			fmt.Fprintln(r.output())
		}
		fmt.Fprintln(r.output(), msg.Text)
	}
}

func (r *Renderer) output() io.Writer {
	if r.Out != nil {
		return r.Out
	}
	return io.Discard
}

func (r *Renderer) colorEnabled() bool {
	return outputColorEnabled(r.output())
}

func outputColorEnabled(out io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	file, ok := out.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func renderProgressLine(step channel.ProgressStep, color bool) string {
	return renderProcessEvent(eventFromProgressStep(step), color)
}

func renderProcessEvent(event ProcessEvent, color bool) string {
	label, detail := processEventLabelAndDetail(event)
	if strings.TrimSpace(label) == "" {
		return ""
	}
	if detail == "" {
		return colorize(label, processEventColor(event), color)
	}
	return colorize(label, processEventColor(event), color) + " " + detail
}

func processEventLabelAndDetail(event ProcessEvent) (string, string) {
	summary := compactInline(event.Summary, 72)
	args := compactInline(event.Args, 96)
	tool := firstNonEmpty(event.Tool, event.Title)
	switch event.Type {
	case "model.thinking":
		thinking := firstNonEmpty(summary, event.Status)
		if strings.EqualFold(thinking, "waiting for model output") || strings.EqualFold(thinking, "thinking") {
			return "", ""
		}
		return "+ Thought:", thinking
	case "model.prepared_tools":
		return "+ Thought:", firstNonEmpty(summary, "prepared tool calls") + durationSuffix(event.DurationMS)
	case "tool.started":
		return "→", friendlyToolAction(tool, args)
	case "tool.progress":
		return "→", friendlyToolName(tool) + " running" + durationSuffix(event.DurationMS)
	case "tool.completed":
		return "✓", friendlyToolName(tool) + durationSuffix(event.DurationMS) + visibleResultSummary(tool, summary)
	case "tool.blocked":
		return "✕", friendlyToolName(tool) + suffixSummary(summary)
	case "runtime.completed":
		return "", ""
	case "gateway.completed":
		return "", ""
	default:
		return "", ""
	}
}

func visibleResultSummary(tool, summary string) string {
	if strings.TrimSpace(summary) == "" {
		return ""
	}
	switch strings.TrimSpace(tool) {
	case "terminal.run", "web.search", "web.fetch", "schedule.manage", "task.search", "task.resume":
		return suffixSummary(summary)
	default:
		return ""
	}
}

func processEventColor(event ProcessEvent) string {
	switch event.Type {
	case "tool.completed", "runtime.completed", "gateway.completed":
		return ansiGreen
	case "tool.blocked":
		return ansiRed
	case "tool.started", "tool.progress":
		return ansiBlue
	default:
		return ansiDim
	}
}

func friendlyToolAction(tool, args string) string {
	name := friendlyToolName(tool)
	if strings.TrimSpace(args) == "" {
		return name
	}
	return name + " " + args
}

func friendlyToolName(tool string) string {
	switch strings.TrimSpace(tool) {
	case "file.read":
		return "Read"
	case "file.write":
		return "Write"
	case "file.delete":
		return "Delete"
	case "terminal.run":
		return "Run"
	case "web.search":
		return "Search"
	case "web.fetch":
		return "Fetch"
	case "schedule.manage":
		return "Schedule"
	case "task.search":
		return "Search tasks"
	case "task.resume":
		return "Resume task"
	default:
		return firstNonEmpty(tool, "Tool")
	}
}

func suffixSummary(summary string) string {
	if strings.TrimSpace(summary) == "" {
		return ""
	}
	return " - " + summary
}

func compactInline(text string, limit int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}

func compactBlock(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

const (
	ansiReset  = "\x1b[0m"
	ansiDim    = "\x1b[2m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiBlue   = "\x1b[34m"
	ansiCyan   = "\x1b[36m"
)

func colorize(text, color string, enabled bool) string {
	if !enabled || color == "" {
		return text
	}
	return color + text + ansiReset
}

func padRight(text string, width int) string {
	if len(text) >= width {
		return text
	}
	return text + strings.Repeat(" ", width-len(text))
}
