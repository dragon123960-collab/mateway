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
			{Command: "/sessions", What: "list local sessions from CLI, Feishu, Weixin, and schedules", Example: "/sessions"},
			{Command: "/session <key>", What: "switch the current CLI session", Example: "/session feishu:oc_xxx"},
			{Command: "/resume [--attach] <key>", What: "copy or attach another channel session into this client", Example: "/resume feishu:oc_xxx"},
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
			{Command: "mateway send --to <target>", What: "send a message from local CLI to Feishu or Weixin", Example: "mateway send --to feishu:oc_xxx done"},
			{Command: "mateway fetch-history --from <target>", What: "pull recent remote messages into a local session", Example: "mateway fetch-history --from feishu:oc_xxx --limit 20"},
		}},
		{Title: "Memory", Items: []helpItem{
			{Command: "mateway memory search <query>", What: "search local agent memory", Example: "mateway memory search deployment"},
			{Command: "mateway memory proposal list", What: "review pending memory or learning proposals", Example: "mateway memory proposal list"},
		}},
		{Title: "Local", Items: []helpItem{
			{Command: "mateway workspace report", What: "inspect workspace, trace, session, and runtime home status", Example: "mateway workspace report"},
			{Command: "mateway gateway status", What: "check the local gateway LaunchAgent state", Example: "mateway gateway status"},
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
