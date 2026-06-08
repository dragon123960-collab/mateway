package runtime

import (
	"context"
	"strings"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/channel"
)

type HeuristicModel struct{}

func (HeuristicModel) Next(_ context.Context, ctx agentcore.Context) (agentcore.Message, error) {
	last := lastConversationMessage(ctx.Messages)
	if last.Role == agentcore.RoleTool {
		return agentcore.Message{Role: agentcore.RoleAssistant, Content: last.Content}, nil
	}
	text := strings.TrimSpace(last.Content)
	if path, ok := strings.CutPrefix(text, "/read "); ok {
		return agentcore.Message{
			Role: agentcore.RoleAssistant,
			ToolCalls: []agentcore.ToolCall{{
				ID:   "call_1",
				Name: "file.read",
				Args: map[string]any{"path": strings.TrimSpace(path)},
			}},
		}, nil
	}
	if path, ok := strings.CutPrefix(text, "/index "); ok {
		return agentcore.Message{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{ID: "call_1", Name: "project.index", Args: map[string]any{"path": strings.TrimSpace(path)}}}}, nil
	}
	if rest, ok := strings.CutPrefix(text, "/write "); ok {
		path, content, _ := strings.Cut(rest, " ")
		return agentcore.Message{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{ID: "call_1", Name: "file.write", Args: map[string]any{"path": strings.TrimSpace(path), "content": strings.TrimSpace(content)}}}}, nil
	}
	if command, ok := strings.CutPrefix(text, "/run "); ok {
		return agentcore.Message{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{ID: "call_1", Name: "terminal.run", Args: map[string]any{"command": strings.TrimSpace(command)}}}}, nil
	}
	if query, ok := strings.CutPrefix(text, "/search "); ok {
		return agentcore.Message{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{ID: "call_1", Name: "web.search", Args: map[string]any{"query": strings.TrimSpace(query)}}}}, nil
	}
	if rest, ok := strings.CutPrefix(text, "/schedule "); ok {
		parts := strings.SplitN(strings.TrimSpace(rest), " ", 2)
		if len(parts) == 2 {
			return agentcore.Message{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{ID: "call_1", Name: "schedule.create", Args: map[string]any{"run_at": parts[0], "text": parts[1], "session_key": "cli:scheduled"}}}}, nil
		}
	}
	return agentcore.Message{Role: agentcore.RoleAssistant, Content: runtimeText(nil, channel.InboundMessage{}, "runtime.heuristic.echo", textValues("text", text))}, nil
}

func lastConversationMessage(messages []agentcore.Message) agentcore.Message {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != agentcore.RoleSystem {
			return messages[i]
		}
	}
	return agentcore.Message{}
}
