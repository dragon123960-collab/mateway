package cli

import (
	"fmt"
	"io"
)

type helpSection struct {
	Title string
	Items []helpItem
}

type helpItem struct {
	Command string
	What    string
	Example string
}

func chatHelpSections() []helpSection {
	return []helpSection{
		{Title: "Conversation", Items: []helpItem{
			{Command: "/help", What: "show this grouped command guide", Example: "/help"},
			{Command: "/new", What: "start a fresh task in the current session", Example: "/new"},
			{Command: "/exit", What: "leave the interactive client", Example: "/exit"},
		}},
		{Title: "Session", Items: []helpItem{
			{Command: "/sessions", What: "open a selector for CLI, Feishu, Weixin, and scheduled sessions", Example: "/sessions"},
			{Command: "/session", What: "open the same selector and switch current session", Example: "/session"},
			{Command: "/resume", What: "open session selector before resuming from another channel", Example: "/resume"},
			{Command: "/show [key]", What: "show messages, tasks, usage, and active task for a session", Example: "/show cli:default"},
		}},
		{Title: "Observe", Items: []helpItem{
			{Command: "/trace [path|key]", What: "summarize the latest trace for a session or a trace file", Example: "/trace"},
			{Command: "/events [path|key]", What: "render process events: model, tools, approvals, final reply", Example: "/events --json"},
		}},
		{Title: "Agent / Model", Items: []helpItem{
			{Command: "/model [--agent <agent_id>] [--verbose]", What: "inspect model selection chain and loaded model endpoints", Example: "/model --verbose"},
		}},
		{Title: "Tools", Items: []helpItem{
			{Command: "/tools [--agent <agent_id>] [--verbose]", What: "list enabled tools, risk, required args, and confirmation boundary", Example: "/tools --verbose"},
			{Command: "/tools enable|disable <tool>", What: "change tool access for an agent profile", Example: "/tools disable terminal.run"},
		}},
		{Title: "Channel", Items: []helpItem{
			{Command: "/sessions", What: "continue from a locally known Feishu or Weixin session", Example: "/sessions"},
			{Command: "shell: mateway send --to <target>", What: "send a message to Feishu or Weixin", Example: "mateway send --to feishu:oc_xxx done"},
		}},
		{Title: "Memory", Items: []helpItem{
			{Command: "/memory proposals", What: "show how to review pending memory proposals", Example: "/memory proposals"},
			{Command: "shell: mateway memory search <query>", What: "search local agent memory", Example: "mateway memory search deployment"},
		}},
		{Title: "Local", Items: []helpItem{
			{Command: "/workspace", What: "show local runtime paths and current workspace", Example: "/workspace"},
			{Command: "/gateway", What: "show gateway status command", Example: "/gateway"},
		}},
	}
}

func printChatHelp(out io.Writer) {
	for i, section := range chatHelpSections() {
		if i > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintln(out, section.Title)
		for _, item := range section.Items {
			fmt.Fprintf(out, "  %-38s %s\n", item.Command, item.What)
			if item.Example != "" {
				fmt.Fprintf(out, "    e.g. %s\n", item.Example)
			}
		}
	}
}
