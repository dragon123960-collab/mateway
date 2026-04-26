package app

import (
	"fmt"
	"io"
	"strings"
)

type helpEntry struct {
	Command string
	Summary string
}

var topLevelHelpEntries = []helpEntry{
	{Command: "init", Summary: "Initialize ~/.mateway, workspace layout, and default config"},
	{Command: "tui", Summary: "Open the local interactive session"},
	{Command: "gateway start", Summary: "Run the gateway in the foreground"},
	{Command: "gateway health", Summary: "Check the gateway health endpoint"},
	{Command: "gateway status", Summary: "Show runtime state and managed service state"},
	{Command: "gateway restart", Summary: "Restart the managed gateway on macOS"},
	{Command: "logs show", Summary: "Show recent gateway log lines"},
	{Command: "logs follow", Summary: "Follow gateway logs in real time"},
	{Command: "logs path", Summary: "Print the gateway log file paths"},
	{Command: "doctor", Summary: "Check config, models, channels, and skill catalog health"},
	{Command: "workspace create <name>", Summary: "Create a named workspace under ~/.mateway/workspaces"},
	{Command: "workspace list", Summary: "List known workspaces"},
	{Command: "skill create <cli|api> <name>", Summary: "Scaffold a runnable skill under workspace/skills"},
	{Command: "agent create <workspace-path> <name>", Summary: "Create an agent profile markdown file"},
	{Command: "agent list <workspace-path>", Summary: "List agent profiles in a workspace"},
	{Command: "model current", Summary: "Show the current default model"},
	{Command: "model list", Summary: "List enabled models"},
	{Command: "model set-default <name>", Summary: "Switch the default model"},
	{Command: "channel list", Summary: "Show channel enablement state"},
	{Command: "channel enable <name>", Summary: "Enable a channel config"},
	{Command: "channel disable <name>", Summary: "Disable a channel config"},
	{Command: "schedule create <name> <minutes> <prompt>", Summary: "Create an interval scheduler job (compat syntax)"},
	{Command: "schedule create cron <name> <expr> <tz> <prompt>", Summary: "Create a cron scheduler job"},
	{Command: "schedule create interval <name> <minutes> <prompt>", Summary: "Create an interval scheduler job"},
	{Command: "schedule list", Summary: "List scheduler jobs"},
	{Command: "schedule get <name>", Summary: "Show one scheduler job as JSON"},
	{Command: "schedule enable <name>", Summary: "Enable a scheduler job"},
	{Command: "schedule disable <name>", Summary: "Disable a scheduler job"},
	{Command: "schedule remove <name>", Summary: "Remove a scheduler job"},
	{Command: "schedule run <name>", Summary: "Run a scheduler job immediately"},
	{Command: "schedule runs <name>", Summary: "Show scheduler run history"},
	{Command: "run list [session-key]", Summary: "List recent runs"},
	{Command: "run get <run-id>", Summary: "Dump a run as JSON"},
	{Command: "version", Summary: "Show build version"},
}

func printHelp(w io.Writer) {
	_, _ = fmt.Fprintln(w, "Mateway CLI")
	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "Usage:")
	_, _ = fmt.Fprintln(w, "  mateway <command> [args]")
	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "Commands:")
	printHelpEntries(w, topLevelHelpEntries)
	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "Hint:")
	_, _ = fmt.Fprintln(w, "  Run `mateway help <command>` for focused help, for example `mateway help doctor`.")
}

func printCommandHelp(w io.Writer, topic string) bool {
	topic = strings.ToLower(strings.TrimSpace(topic))
	switch topic {
	case "", "help":
		printHelp(w)
	case "doctor":
		printFocusedHelp(w,
			"doctor",
			"Run config, model, channel, and skill catalog checks.",
			[]string{
				"mateway doctor",
			},
		)
	case "logs":
		printFocusedHelp(w,
			"logs",
			"Inspect gateway log files written by the managed service.",
			[]string{
				"mateway logs show",
				"mateway logs follow",
				"mateway logs path",
			},
		)
	case "gateway":
		printFocusedHelp(w,
			"gateway",
			"Run or inspect the WebSocket gateway service.",
			[]string{
				"mateway gateway start",
				"mateway gateway health",
				"mateway gateway status",
				"mateway gateway restart",
			},
		)
	case "schedule":
		printFocusedHelp(w,
			"schedule",
			"Manage recurring scheduler jobs with interval or cron semantics.",
			[]string{
				"mateway schedule create <name> <minutes> <prompt>",
				"mateway schedule create cron <name> <expr> <tz> <prompt>",
				"mateway schedule list",
				"mateway schedule get <name>",
				"mateway schedule enable <name>",
				"mateway schedule disable <name>",
				"mateway schedule remove <name>",
				"mateway schedule run <name>",
				"mateway schedule runs <name>",
			},
		)
	case "tui":
		printFocusedHelp(w,
			"tui",
			"Open the local terminal session with slash commands such as /skills, /tools, /trace, and /learn.",
			[]string{
				"mateway tui",
			},
		)
	default:
		return false
	}
	return true
}

func printFocusedHelp(w io.Writer, topic, summary string, usages []string) {
	_, _ = fmt.Fprintf(w, "mateway %s\n\n", topic)
	_, _ = fmt.Fprintln(w, summary)
	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "Usage:")
	for _, item := range usages {
		_, _ = fmt.Fprintf(w, "  %s\n", item)
	}
}

func printHelpEntries(w io.Writer, entries []helpEntry) {
	width := 0
	for _, entry := range entries {
		if len(entry.Command) > width {
			width = len(entry.Command)
		}
	}
	for _, entry := range entries {
		_, _ = fmt.Fprintf(w, "  %-*s  %s\n", width, entry.Command, entry.Summary)
	}
}
