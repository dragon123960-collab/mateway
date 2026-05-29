package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/memory"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/tool"
)

type Runtime struct {
	Config *config.Root
	Store  session.Store
	Tools  *agentcore.ToolRegistry
	Model  agentcore.Model
	Pool   AgentPool
	Hooks  RuntimeHooks
}

type Response struct {
	Reply     channel.OutboundMessage
	TraceID   string
	TracePath string
	Failed    bool
}

func New(cfg *config.Root) Runtime {
	hooks := defaultRuntimeHooks()
	hooks.Providers = append(hooks.Providers, staticContextHookProvider{config: cfg})
	hooks.Providers = append(hooks.Providers, memorySafeReadHookProvider{config: cfg})
	return Runtime{
		Config: cfg,
		Store:  session.NewStore(cfg.App.Home),
		Tools:  tool.NewRegistry(cfg),
		Model:  HeuristicModel{},
		Pool:   NewAgentPool(cfg),
		Hooks:  hooks,
	}
}

func (rt Runtime) Handle(ctx context.Context, msg channel.InboundMessage) (Response, error) {
	start := time.Now()
	state, err := rt.Store.Load(msg.SessionKey)
	if err != nil {
		return Response{}, err
	}
	trace, err := newTraceRecorder(rt.Config)
	if err != nil {
		return Response{}, err
	}
	_ = trace.write(map[string]any{"type": "request", "session_key": msg.SessionKey, "channel": msg.Channel, "text": msg.Text})
	defer func() {
		_ = trace.write(map[string]any{"type": "runtime_done", "duration_ms": time.Since(start).Milliseconds()})
	}()
	if resp, handled, err := rt.handlePending(ctx, &state, msg, trace); handled || err != nil {
		if handled && err == nil {
			resp.TraceID = trace.id
			resp.TracePath = trace.path
			_ = trace.write(map[string]any{"type": "reply", "text": resp.Reply.Text, "style": resp.Reply.Style, "runtime_duration_ms": time.Since(start).Milliseconds()})
		}
		return resp, err
	}
	decision := resolveFollowup(state, msg.Text)
	if decision.Kind == followupClarify {
		task := state.StartTask(msg.Text)
		state.Pending = &session.PendingAction{Kind: "user_input", TaskID: task.ID, Question: decision.ClarifyPrompt, ResumeText: decision.Reason}
		state.BlockActiveTask("await_user_input")
		if err := rt.Store.Save(state); err != nil {
			return Response{}, err
		}
		resp := reply(msg, decision.ClarifyPrompt, "clarify")
		resp.TraceID = trace.id
		resp.TracePath = trace.path
		_ = trace.write(map[string]any{"type": "reply", "text": resp.Reply.Text, "style": resp.Reply.Style, "runtime_duration_ms": time.Since(start).Milliseconds()})
		return resp, nil
	}
	userText := strings.TrimSpace(msg.Text)
	if decision.Kind == followupContinuation {
		task := state.ActivateTask(decision.TaskID)
		if task == nil {
			task = state.StartTask(msg.Text)
		}
		if strings.TrimSpace(decision.ResolvedUserText) != "" {
			userText = decision.ResolvedUserText
		}
		return rt.runTask(ctx, msg, &state, task, userText, trace)
	}
	task := state.StartTask(msg.Text)
	return rt.runTask(ctx, msg, &state, task, userText, trace)
}

func (rt Runtime) runTask(ctx context.Context, msg channel.InboundMessage, state *session.State, task *session.TaskNode, userText string, trace *traceRecorder) (Response, error) {
	messages := append([]agentcore.Message(nil), state.Messages...)
	messages = append(messages, agentcore.Message{Role: agentcore.RoleUser, Content: userText})

	agent := rt.Pool.AgentForSession(msg.SessionKey)
	if agent == nil {
		agent = agentcore.NewAgent(rt.Model, rt.Tools)
	}
	agent.Messages = messages
	agent.MaxIterations = 6
	profile := rt.Pool.ProfileForSession(msg.SessionKey)
	agent.Hooks = rt.hooksForState(state, task.ID, trace, rt.Hooks.contextMessages(ctx, ContextHookInput{
		Message:  msg,
		State:    *state,
		TaskID:   task.ID,
		UserText: userText,
		Profile:  profile,
	}, trace))
	result, err := agent.Continue(ctx)
	if err != nil {
		state.BlockActiveTask("failed")
		if saveErr := rt.Store.Save(*state); saveErr != nil {
			return Response{}, saveErr
		}
		text := friendlyRuntimeError(err)
		resp := Response{
			Reply: channel.OutboundMessage{
				Channel:  msg.Channel,
				ThreadID: msg.ThreadID,
				Text:     text,
				Style:    "error",
			},
			TraceID:   trace.id,
			TracePath: trace.path,
			Failed:    true,
		}
		_ = trace.write(map[string]any{"type": "model_error", "error": err.Error(), "friendly": text})
		_ = trace.write(map[string]any{"type": "reply", "text": resp.Reply.Text, "style": resp.Reply.Style})
		return resp, nil
	}

	state.Messages = result.Messages
	taskCompleted := false
	if state.Pending == nil {
		if looksLikeInputRequest(result.FinalText) {
			state.Pending = &session.PendingAction{Kind: "user_input", TaskID: task.ID, Question: result.FinalText}
			state.BlockActiveTask("await_user_input")
		} else {
			state.CompleteActiveTask()
			taskCompleted = true
		}
	}
	if err := rt.Store.Save(*state); err != nil {
		return Response{}, err
	}

	text := sanitizeResponse(result.FinalText)
	if text == "" {
		text = fallbackFinalReply(result.FinalText)
	}
	if taskCompleted {
		if learningResult, err := rt.recordTaskCompletion(*state, task.ID, text, trace); err != nil {
			_ = trace.write(map[string]any{"type": "hook_warning", "hook": "observe_hook", "provider": "self_learning", "error": err.Error()})
		} else if learningResult.Proposal != nil {
			text = appendMemoryReviewBlock(text, *learningResult.Proposal)
		}
	}
	resp := Response{
		Reply: channel.OutboundMessage{
			Channel:  msg.Channel,
			ThreadID: msg.ThreadID,
			Text:     text,
		},
		TraceID:   trace.id,
		TracePath: trace.path,
	}
	_ = trace.write(map[string]any{"type": "reply", "text": resp.Reply.Text})
	return resp, nil
}

func (rt Runtime) recordTaskCompletion(state session.State, taskID, finalText string, trace *traceRecorder) (memory.LearningResult, error) {
	task, ok := taskByID(state, taskID)
	if !ok {
		return memory.LearningResult{}, nil
	}
	home := config.DefaultHome()
	if rt.Config != nil && strings.TrimSpace(rt.Config.App.Home) != "" {
		home = rt.Config.App.Home
	}
	result, err := memory.RecordTaskCompletion(memory.LearningEvent{
		Home:       home,
		SessionKey: state.Key,
		Task:       task,
		FinalText:  finalText,
		TraceID:    trace.id,
		TracePath:  trace.path,
	})
	if err != nil {
		return memory.LearningResult{}, err
	}
	_ = trace.write(map[string]any{
		"type":            "self_learning",
		"diary_path":      result.DiaryPath,
		"reflection_path": result.ReflectionPath,
		"proposal_id":     proposalID(result.Proposal),
	})
	return result, nil
}

func taskByID(state session.State, taskID string) (session.TaskNode, bool) {
	for _, task := range state.Tasks {
		if task.ID == taskID {
			return task, true
		}
	}
	return session.TaskNode{}, false
}

func proposalID(proposal *memory.Proposal) string {
	if proposal == nil {
		return ""
	}
	return proposal.ID
}

func appendMemoryReviewBlock(text string, proposal memory.Proposal) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(text))
	b.WriteString("\n\n可沉淀记忆候选：\n")
	b.WriteString("1. ")
	b.WriteString(proposal.Type)
	b.WriteString("\n")
	b.WriteString("   建议记忆：")
	b.WriteString(proposal.Title)
	if proposal.Body != "" {
		b.WriteString("\n   内容：")
		b.WriteString(summarize(proposal.Body))
	}
	if len(proposal.Sources) > 0 {
		b.WriteString("\n   来源：")
		b.WriteString(strings.Join(proposal.Sources, ", "))
	}
	b.WriteString("\n   操作：`mateway memory proposal commit ")
	b.WriteString(proposal.ID)
	b.WriteString("` 或 `mateway memory proposal reject ")
	b.WriteString(proposal.ID)
	b.WriteString("`")
	return b.String()
}

func fallbackFinalReply(raw string) string {
	if strings.Contains(strings.ToUpper(raw), "[TOOL_CALL]") {
		return "模型生成了无效的工具调用格式，已停止执行，避免误操作。请重试或把任务说得更具体。"
	}
	return "我还没有生成可用回复。"
}

func (rt Runtime) hooksForState(state *session.State, taskID string, trace *traceRecorder, steering []agentcore.Message) agentcore.Hooks {
	steeringSent := false
	return agentcore.Hooks{
		Emit: trace.emit,
		GetSteeringMessages: func(context.Context) ([]agentcore.Message, error) {
			if steeringSent {
				return nil, nil
			}
			steeringSent = true
			return append([]agentcore.Message(nil), steering...), nil
		},
		BeforeToolCall: func(_ context.Context, input agentcore.BeforeToolCallContext) (agentcore.BeforeToolCallResult, error) {
			if input.ToolCall.Name == "terminal.run" && tool.IsDangerousCommand(fmt.Sprint(input.ToolCall.Args["command"])) {
				state.Pending = &session.PendingAction{
					Kind:       "confirm_tool",
					TaskID:     taskID,
					ToolCall:   input.ToolCall,
					ResumeText: "确认后继续执行危险命令",
				}
				state.BlockActiveTask("await_confirm")
				return agentcore.BeforeToolCallResult{Block: true, Reason: "这个命令可能有破坏性。回复“确认”继续，或回复“取消”放弃。"}, nil
			}
			if input.Tool.Risk() == agentcore.RiskGuardedMutation || input.Tool.Risk() == agentcore.RiskDangerous {
				if rt.Config != nil && !rt.Config.Security.RequireApprovalForRiskyTool {
					return agentcore.BeforeToolCallResult{}, nil
				}
				state.Pending = &session.PendingAction{
					Kind:       "confirm_tool",
					TaskID:     taskID,
					ToolCall:   input.ToolCall,
					ResumeText: "确认后继续执行 " + input.Tool.Name(),
				}
				state.BlockActiveTask("await_confirm")
				return agentcore.BeforeToolCallResult{Block: true, Reason: "继续之前需要确认。回复“确认”继续，或回复“取消”放弃。"}, nil
			}
			return agentcore.BeforeToolCallResult{}, nil
		},
		AfterToolCall: func(_ context.Context, input agentcore.AfterToolCallContext) (agentcore.AfterToolCallResult, error) {
			status, evidence := acceptToolResult(input.Tool, input.ToolResult)
			state.AddStep(taskID, session.TaskStep{
				Tool:     input.ToolCall.Name,
				Status:   status,
				Summary:  summarize(input.ToolResult.Content),
				Evidence: evidence,
			})
			return agentcore.AfterToolCallResult{}, nil
		},
	}
}

func (rt Runtime) handlePending(ctx context.Context, state *session.State, msg channel.InboundMessage, trace *traceRecorder) (Response, bool, error) {
	if state.Pending == nil {
		return Response{}, false, nil
	}
	text := strings.TrimSpace(msg.Text)
	switch state.Pending.Kind {
	case "confirm_tool":
		if isCancel(text) {
			state.Pending = nil
			state.BlockActiveTask("cancelled")
			if err := rt.Store.Save(*state); err != nil {
				return Response{}, true, err
			}
			return reply(msg, "已取消。", "cancelled"), true, nil
		}
		if !isConfirm(text) {
			return reply(msg, "这个操作还在等待确认。回复“确认”继续，或回复“取消”放弃。", "approval_pending"), true, nil
		}
		call := state.Pending.ToolCall
		state.Pending = nil
		_ = trace.write(map[string]any{"type": "pending_confirmed", "tool_call": call})
		result := rt.Tools.Execute(ctx, call)
		toolDef, _ := rt.Tools.Get(call.Name)
		status, evidence := acceptToolResult(toolDef, result)
		state.AddStep(state.ActiveTask, session.TaskStep{Tool: call.Name, Status: status, Summary: summarize(result.Content), Evidence: evidence})
		_ = trace.write(map[string]any{"type": "tool_execution_end", "tool_call": call, "tool_result": result, "acceptance": status, "evidence": evidence})
		state.Messages = append(state.Messages,
			agentcore.Message{Role: agentcore.RoleUser, Content: text},
			agentcore.Message{Role: agentcore.RoleTool, ToolCallID: call.ID, Content: result.Content},
		)
		if !result.IsError {
			state.CompleteActiveTask()
		}
		if err := rt.Store.Save(*state); err != nil {
			return Response{}, true, err
		}
		if result.IsError {
			return reply(msg, result.Content, "error"), true, nil
		}
		return reply(msg, result.Content, "completed"), true, nil
	case "user_input":
		taskID := state.Pending.TaskID
		state.Pending = nil
		state.Messages = append(state.Messages,
			agentcore.Message{Role: agentcore.RoleUser, Content: text},
		)
		_ = trace.write(map[string]any{"type": "pending_user_input", "task_id": taskID, "text": text})
		if err := rt.Store.Save(*state); err != nil {
			return Response{}, true, err
		}
		return Response{}, false, nil
	default:
		state.Pending = nil
		return Response{}, false, nil
	}
}

func acceptToolResult(tool agentcore.Tool, result agentcore.ToolResult) (string, map[string]any) {
	evidence := map[string]any{}
	for key, value := range result.Evidence {
		evidence[key] = value
	}
	if tool != nil {
		contract := agentcore.ContractFor(tool)
		if contract.Acceptance != "" {
			evidence["acceptance_criteria"] = contract.Acceptance
		}
	}
	if result.IsError {
		evidence["acceptance"] = "failed"
		return "failed", evidence
	}
	if len(result.Evidence) == 0 && strings.TrimSpace(result.Content) == "" {
		evidence["acceptance"] = "suspect"
		return "suspect", evidence
	}
	evidence["acceptance"] = "accepted"
	return "accepted", evidence
}

func reply(msg channel.InboundMessage, text, style string) Response {
	return Response{Reply: channel.OutboundMessage{Channel: msg.Channel, ThreadID: msg.ThreadID, Text: text, Style: style}}
}

func isConfirm(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "确认", "同意", "继续", "yes", "y", "ok":
		return true
	default:
		return false
	}
}

func isCancel(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "取消", "不要", "放弃", "no", "n", "cancel":
		return true
	default:
		return false
	}
}

func summarize(text string) string {
	text = strings.TrimSpace(text)
	if len(text) <= 160 {
		return text
	}
	return text[:160] + fmt.Sprintf("... (%d chars)", len(text))
}

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
	return agentcore.Message{Role: agentcore.RoleAssistant, Content: "收到：" + text}, nil
}

func lastConversationMessage(messages []agentcore.Message) agentcore.Message {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != agentcore.RoleSystem {
			return messages[i]
		}
	}
	return agentcore.Message{}
}

func looksLikeInputRequest(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	if strings.Contains(trimmed, "需要你") || strings.Contains(trimmed, "请提供") || strings.Contains(trimmed, "请补充") {
		return true
	}
	return strings.HasSuffix(trimmed, "？") && (strings.Contains(trimmed, "哪个") || strings.Contains(trimmed, "什么") || strings.Contains(trimmed, "是否"))
}

func friendlyRuntimeError(err error) string {
	raw := strings.TrimSpace(fmt.Sprint(err))
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "context deadline exceeded") || strings.Contains(lower, "client.timeout"):
		return "模型服务这次响应超时了，任务已经停在安全位置。你可以直接回复“重试”或把问题再发一遍，我会接着当前上下文继续。"
	case strings.Contains(lower, "model api key is empty"):
		return "当前模型配置缺少 API Key，任务没有继续执行。请检查模型配置后重试。"
	case strings.Contains(lower, "all models failed"):
		return "当前可用模型都调用失败了，任务已经停在安全位置。你可以稍后回复“重试”，或切换/检查 fallback 模型配置。"
	default:
		if raw == "" {
			return "任务执行失败了，已经停在安全位置。你可以补充信息后重试。"
		}
		return "任务执行失败了，已经停在安全位置。你可以补充信息后重试。"
	}
}
