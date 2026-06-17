package runtime

import (
	"context"
	"encoding/json"
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
	if strings.Contains(ctx.SystemPrompt, "verification judge") {
		return agentcore.Message{Role: agentcore.RoleAssistant, Content: `{"status":"passed","reason":"heuristic verifier accepted node output","missing":[],"confidence":"medium"}`}, nil
	}
	text := strings.TrimSpace(last.Content)
	if strings.Contains(ctx.SystemPrompt, "TaskGraphPlan") || strings.Contains(ctx.SystemPrompt, "task graph planner") {
		return agentcore.Message{Role: agentcore.RoleAssistant, Content: heuristicTaskGraphPlan(text)}, nil
	}
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
		return agentcore.Message{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{ID: "call_1", Name: "file.read", Args: map[string]any{"path": strings.TrimSpace(path)}}}}, nil
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
			return agentcore.Message{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{ID: "call_1", Name: "schedule.manage", Args: map[string]any{"action": "create", "run_at": parts[0], "text": parts[1], "session_key": "cli:scheduled"}}}}, nil
		}
	}
	return agentcore.Message{Role: agentcore.RoleAssistant, Content: runtimeText(nil, channel.InboundMessage{}, "runtime.heuristic.echo", textValues("text", text))}, nil
}

func heuristicTaskGraphPlan(prompt string) string {
	userText := extractHeuristicPlannerUserText(prompt)
	node := map[string]any{
		"id":         "answer",
		"type":       "subtask",
		"mode":       "direct",
		"goal":       "answer the user request",
		"acceptance": "responds to the user request",
		"outputs":    []string{"final_answer"},
	}
	tools := []string{}
	if tool := heuristicToolForSlashCommand(userText); tool != "" {
		node["id"] = "execute"
		node["goal"] = "complete the requested action"
		node["acceptance"] = "requested action completed or a concrete blocker is reported"
		// Phase 01 does not execute node-local ReAct yet. Keep heuristic slash
		// tasks direct so local CLI smoke tests do not require tool evidence that
		// the stub executor cannot produce.
		_ = tool
	}
	plan := map[string]any{
		"task": map[string]any{
			"goal":       firstNonEmpty(userText, "answer the user request"),
			"risk":       "low",
			"acceptance": node["acceptance"],
			"required_capabilities": map[string]any{
				"tools":       tools,
				"skills":      []string{},
				"human_gates": []string{},
			},
			"final_output": map[string]any{
				"text":       true,
				"structured": []string{},
			},
		},
		"nodes": []map[string]any{node},
	}
	data, err := json.Marshal(plan)
	if err != nil {
		return `{"task":{"goal":"answer the user request","risk":"low","acceptance":"responds to the user request","required_capabilities":{"tools":[],"skills":[],"human_gates":[]},"final_output":{"text":true,"structured":[]}},"nodes":[{"id":"answer","type":"subtask","mode":"direct","goal":"answer the user request","acceptance":"responds to the user request"}]}`
	}
	return string(data)
}

func extractHeuristicPlannerUserText(prompt string) string {
	text := strings.TrimSpace(prompt)
	for _, marker := range []string{"Current user message:\n", "User task:\n"} {
		idx := strings.Index(text, marker)
		if idx < 0 {
			continue
		}
		rest := strings.TrimSpace(text[idx+len(marker):])
		if cut := strings.Index(rest, "\n\n"); cut >= 0 {
			rest = strings.TrimSpace(rest[:cut])
		}
		if rest != "" {
			return rest
		}
	}
	return text
}

func heuristicToolForSlashCommand(text string) string {
	switch {
	case strings.HasPrefix(text, "/read "), strings.HasPrefix(text, "/index "):
		return "file.read"
	case strings.HasPrefix(text, "/write "):
		return "file.write"
	case strings.HasPrefix(text, "/run "):
		return "terminal.run"
	case strings.HasPrefix(text, "/search "):
		return "web.search"
	case strings.HasPrefix(text, "/schedule "):
		return "schedule.manage"
	default:
		return ""
	}
}

func lastConversationMessage(messages []agentcore.Message) agentcore.Message {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != agentcore.RoleSystem {
			return messages[i]
		}
	}
	return agentcore.Message{}
}
