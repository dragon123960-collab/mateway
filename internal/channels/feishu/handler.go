package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/config"
	agentharness "github.com/dongping/mateway/internal/harness"
	hostruntime "github.com/dongping/mateway/internal/runtime"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/skills"
	"github.com/dongping/mateway/internal/textutil"
	"github.com/dongping/mateway/internal/tools"
)

type SkillCatalog interface {
	Snapshot() []skills.Skill
}

type SkillInvoker interface {
	Invoke(ctx context.Context, skill skills.Skill) (hostruntime.Result, error)
}

type Handler struct {
	Config     config.FeishuConfig
	Catalog    SkillCatalog
	Invoker    SkillInvoker
	Harness    *agentharness.Harness
	HTTPClient *http.Client
}

type webhookEnvelope struct {
	Type      string          `json:"type"`
	Challenge string          `json:"challenge"`
	Token     string          `json:"token"`
	Header    eventHeader     `json:"header"`
	Event     json.RawMessage `json:"event"`
}

type eventHeader struct {
	EventType string `json:"event_type"`
}

type messageEvent struct {
	Sender  messageSender `json:"sender"`
	Message messageBody   `json:"message"`
}

type messageSender struct {
	SenderType string `json:"sender_type"`
}

type messageBody struct {
	MessageType string `json:"message_type"`
	ChatID      string `json:"chat_id"`
	Content     string `json:"content"`
}

type textContent struct {
	Text string `json:"text"`
}

type tokenResponse struct {
	Code              int    `json:"code"`
	Msg               string `json:"msg"`
	TenantAccessToken string `json:"tenant_access_token"`
}

type sendMessageRequest struct {
	ReceiveID string `json:"receive_id"`
	MsgType   string `json:"msg_type"`
	Content   string `json:"content"`
}

type sendMessageResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var envelope webhookEnvelope
	if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if token := strings.TrimSpace(h.Config.VerificationToken); token != "" && strings.TrimSpace(envelope.Token) != "" && envelope.Token != token {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	if envelope.Type == "url_verification" {
		_ = json.NewEncoder(w).Encode(map[string]string{"challenge": envelope.Challenge})
		return
	}
	if envelope.Header.EventType != "im.message.receive_v1" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	var event messageEvent
	if err := json.Unmarshal(envelope.Event, &event); err != nil {
		http.Error(w, "invalid event", http.StatusBadRequest)
		return
	}
	if event.Sender.SenderType != "" && event.Sender.SenderType != "user" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if event.Message.MessageType != "text" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	text := extractText(event.Message.Content)
	reply := h.handleText(r.Context(), "feishu:direct", strings.TrimSpace(text))
	if reply == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := h.sendText(r.Context(), event.Message.ChatID, reply); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (h Handler) handleText(ctx context.Context, sessionKey, text string) string {
	text = stripMentions(text)
	if text == "" {
		return ""
	}
	switch {
	case text == "/trace" || text == "/learn":
		if text == "/learn" {
			return h.learnReply(ctx, sessionKey, "")
		}
		return h.traceReply(ctx, sessionKey, "")
	case strings.HasPrefix(text, "/trace "):
		return h.traceReply(ctx, sessionKey, strings.TrimSpace(strings.TrimPrefix(text, "/trace ")))
	case strings.HasPrefix(text, "/learn "):
		return h.learnReply(ctx, sessionKey, strings.TrimSpace(strings.TrimPrefix(text, "/learn ")))
	case strings.HasPrefix(text, "/memory "):
		if h.Harness == nil {
			return "当前 runtime 没有启用 memory 检索。"
		}
		parts := strings.Fields(strings.TrimSpace(strings.TrimPrefix(text, "/memory ")))
		if len(parts) < 2 {
			return "用法: /memory <session|thread|task|agent> <scope> [query]"
		}
		dimension := parts[0]
		scope := parts[1]
		query := ""
		if len(parts) > 2 {
			query = strings.Join(parts[2:], " ")
		}
		run, err := h.Harness.Start(ctx, agentharness.Request{
			SessionKey: sessionKey,
			Channel:    "feishu",
			Mode:       "tool",
			ToolName:   "search_scoped_memory",
			Arguments: map[string]any{
				"dimension": dimension,
				"scope":     scope,
				"query":     query,
			},
		}, nil)
		if err != nil {
			return fmt.Sprintf("memory 检索失败：%v", err)
		}
		if strings.TrimSpace(strings.Trim(run.Result, `"`)) == "" {
			return "没有找到相关记忆。"
		}
		return trimBlock(strings.Trim(run.Result, `"`))
	case text == "/runs":
		if h.Harness == nil {
			return "当前 runtime 没有启用 run 查询。"
		}
		runs, err := h.Harness.ListRuns(ctx, sessionKey, 8)
		if err != nil {
			return fmt.Sprintf("读取 runs 失败：%v", err)
		}
		if len(runs) == 0 {
			return "当前 session 还没有 run 记录。"
		}
		lines := make([]string, 0, len(runs))
		for _, run := range runs {
			lines = append(lines, fmt.Sprintf("- %s [%s] %s", run.ID, run.Status, trimBlock(firstNonEmpty(run.ToolName, run.Mode))))
		}
		return "最近 runs:\n" + strings.Join(lines, "\n")
	case strings.HasPrefix(text, "/run_status "):
		if h.Harness == nil {
			return "当前 runtime 没有启用 run 查询。"
		}
		runID := strings.TrimSpace(strings.TrimPrefix(text, "/run_status "))
		if runID == "" {
			return "用法: /run_status <run-id>"
		}
		run, ok := h.Harness.GetRun(ctx, runID)
		if !ok {
			return fmt.Sprintf("没有找到 run `%s`。", runID)
		}
		return fmt.Sprintf("run `%s`\nstatus: %s\nmode: %s\ntool: %s\nresult: %s", run.ID, run.Status, firstNonEmpty(run.Mode, "-"), firstNonEmpty(run.ToolName, "-"), trimBlock(run.Result))
	case text == "/summary":
		if h.Harness == nil {
			return "当前 runtime 没有启用 summary。"
		}
		note, ok, err := h.Harness.Memory.ReadSessionSummary(ctx, sessionKey)
		if err != nil {
			return fmt.Sprintf("读取 summary 失败：%v", err)
		}
		if !ok || strings.TrimSpace(note.Content) == "" {
			return "当前 session 还没有 summary。"
		}
		return note.Content
	case text == "/last":
		if h.Harness == nil {
			return "当前 runtime 没有启用记忆召回。"
		}
		note, ok, err := h.Harness.Memory.ReadSessionSummary(ctx, sessionKey)
		if err != nil {
			return fmt.Sprintf("读取记忆失败：%v", err)
		}
		if !ok || strings.TrimSpace(note.Content) == "" {
			return "我这里还没有这条 session 的近期记录。"
		}
		return "上次进度:\n" + note.Content
	case text == "/approvals":
		if h.Harness == nil {
			return "当前 runtime 没有启用 approval。"
		}
		items := h.Harness.ListPending(sessionKey)
		if len(items) == 0 {
			return "当前没有待批准操作。"
		}
		lines := make([]string, 0, len(items))
		for _, item := range items {
			lines = append(lines, fmt.Sprintf("- %s `%s` (%s)", item.ID, item.ToolName, item.AgentName))
		}
		return "待批准操作:\n" + strings.Join(lines, "\n")
	case text == "/approve":
		if h.Harness == nil {
			return "当前 runtime 没有启用 approval。"
		}
		reply, err := h.Harness.ReviewPending(ctx, sessionKey, "", true, nil)
		if err != nil {
			return fmt.Sprintf("approve 失败：%v", err)
		}
		return reply
	case strings.HasPrefix(text, "/approve "):
		if h.Harness == nil {
			return "当前 runtime 没有启用 approval。"
		}
		reply, err := h.Harness.ReviewPending(ctx, sessionKey, strings.TrimSpace(strings.TrimPrefix(text, "/approve ")), true, nil)
		if err != nil {
			return fmt.Sprintf("approve 失败：%v", err)
		}
		return reply
	case text == "/deny":
		if h.Harness == nil {
			return "当前 runtime 没有启用 approval。"
		}
		reply, err := h.Harness.ReviewPending(ctx, sessionKey, "", false, nil)
		if err != nil {
			return fmt.Sprintf("deny 失败：%v", err)
		}
		return reply
	case strings.HasPrefix(text, "/deny "):
		if h.Harness == nil {
			return "当前 runtime 没有启用 approval。"
		}
		reply, err := h.Harness.ReviewPending(ctx, sessionKey, strings.TrimSpace(strings.TrimPrefix(text, "/deny ")), false, nil)
		if err != nil {
			return fmt.Sprintf("deny 失败：%v", err)
		}
		return reply
	case strings.HasPrefix(text, "/agent "):
		if h.Harness == nil || h.Harness.Sessions == nil {
			return "当前 runtime 还没有启用 agent 切换。"
		}
		name := strings.TrimSpace(strings.TrimPrefix(text, "/agent "))
		if name == "" {
			return "用法: /agent <agent-name>"
		}
		if err := h.Harness.Sessions.SavePreferences(sessionKey, session.Preferences{AgentName: name}); err != nil {
			return fmt.Sprintf("切换 agent 失败：%v", err)
		}
		return fmt.Sprintf("当前 session 已切换到 agent `%s`。", name)
	case text == "/skills" || strings.EqualFold(text, "skills"):
		snapshot := h.snapshotSkills()
		if len(snapshot) == 0 {
			return "当前还没有可用 skills。"
		}
		names := make([]string, 0, len(snapshot))
		for _, skill := range snapshot {
			names = append(names, formatSkillLabel(skill))
		}
		return "可用 skills:\n- " + strings.Join(names, "\n- ")
	case text == "/tools":
		if h.Harness == nil {
			return "当前 runtime 没有启用可见 tools 查询。"
		}
		agentName := "default"
		if h.Harness.Sessions != nil {
			if prefs, err := h.Harness.Sessions.LoadPreferences(sessionKey); err == nil && strings.TrimSpace(prefs.AgentName) != "" {
				agentName = prefs.AgentName
			}
		}
		specs, err := h.Harness.ListVisibleTools(ctx, tools.Scope{Channel: "feishu", AgentName: agentName})
		if err != nil {
			return fmt.Sprintf("读取 tools 失败：%v", err)
		}
		if len(specs) == 0 {
			return "当前 session 没有可见 tools。"
		}
		names := make([]string, 0, len(specs))
		for _, spec := range specs {
			names = append(names, formatToolLabel(spec))
		}
		return "当前可见 tools:\n- " + strings.Join(names, "\n- ")
	case strings.HasPrefix(text, "/run "):
		raw := strings.TrimSpace(strings.TrimPrefix(text, "/run "))
		parts := strings.Fields(raw)
		name := ""
		if len(parts) > 0 {
			name = parts[0]
		}
		if name == "" {
			return "用法: /run <skill-name>"
		}
		args := map[string]any{}
		remainder := strings.TrimSpace(strings.TrimPrefix(raw, name))
		if strings.TrimSpace(remainder) != "" {
			if name == "web_search" {
				args["query"] = strings.TrimSpace(remainder)
			} else {
				var parsed map[string]any
				if err := json.Unmarshal([]byte(remainder), &parsed); err == nil {
					args = parsed
				}
			}
		}
		if h.Harness != nil {
			run, err := h.Harness.Start(ctx, agentharness.Request{
				SessionKey: sessionKey,
				Channel:    "feishu",
				Mode:       "tool",
				ToolName:   name,
				Arguments:  args,
			}, nil)
			if err == nil {
				return fmt.Sprintf("`%s` 运行完成。\n%s", name, trimBlock(strings.Trim(run.Result, `"`)))
			}
		}
		skill, ok := findSkill(h.Catalog.Snapshot(), name)
		if !ok {
			return fmt.Sprintf("没有找到 skill `%s`。先发 /skills 看看可用列表。", name)
		}
		if !skill.Executable {
			return fmt.Sprintf("skill `%s` 当前是说明型 `SKILL.md`，不是可直接 `/run` 的 CLI/API skill。", name)
		}
		result, err := h.Invoker.Invoke(ctx, skill)
		if err != nil {
			return fmt.Sprintf("运行 `%s` 失败。\nstdout:\n%s\nstderr:\n%s", name, trimBlock(result.Stdout), trimBlock(result.Stderr))
		}
		out := trimBlock(result.Stdout)
		if out == "" {
			out = "(no stdout)"
		}
		return fmt.Sprintf("`%s` 运行完成。\n%s", name, out)
	default:
		if h.Harness != nil {
			run, err := h.Harness.Start(ctx, agentharness.Request{
				SessionKey: sessionKey,
				Channel:    "feishu",
				UserText:   text,
				Mode:       "chat",
			}, nil)
			if err == nil {
				return run.Result
			}
			return formatRuntimeError(err)
		}
		return fmt.Sprintf("%s 已收到：%s\n\n当前基础版支持：\n- /skills 查看技能目录\n- /tools 查看当前能力\n- /run <skill-name> 执行技能", h.Config.BotName, text)
	}
}

func findSkill(snapshot []skills.Skill, name string) (skills.Skill, bool) {
	for _, skill := range snapshot {
		if skill.Manifest.Name == name {
			return skill, true
		}
	}
	return skills.Skill{}, false
}

func (h Handler) snapshotSkills() []skills.Skill {
	if h.Catalog == nil {
		return nil
	}
	return h.Catalog.Snapshot()
}

func formatSkillLabel(skill skills.Skill) string {
	kind := "doc"
	if skill.Executable && strings.TrimSpace(string(skill.Manifest.Type)) != "" {
		kind = string(skill.Manifest.Type)
	}
	description := firstNonEmpty(strings.TrimSpace(skill.Manifest.Description), "No description.")
	return fmt.Sprintf("%s [%s] %s", skill.Manifest.Name, kind, description)
}

func formatToolLabel(spec tools.Spec) string {
	description := firstNonEmpty(strings.TrimSpace(spec.Description), "No description.")
	return fmt.Sprintf("%s [%s] %s", spec.Name, spec.Kind, description)
}

func extractText(raw string) string {
	var content textContent
	if err := json.Unmarshal([]byte(raw), &content); err == nil && content.Text != "" {
		return content.Text
	}
	return raw
}

func stripMentions(text string) string {
	fields := strings.Fields(text)
	filtered := fields[:0]
	for _, field := range fields {
		if strings.HasPrefix(field, "@_user_") {
			continue
		}
		filtered = append(filtered, field)
	}
	return strings.TrimSpace(strings.Join(filtered, " "))
}

func trimBlock(value string) string {
	return textutil.CleanBlock(textutil.HumanizeRunError(value), 1000)
}

func (h Handler) traceReply(ctx context.Context, sessionKey, runID string) string {
	run, ok, errMsg := h.loadTraceRun(ctx, sessionKey, runID)
	if !ok {
		return errMsg
	}
	lines := []string{
		fmt.Sprintf("run `%s`", run.ID),
		fmt.Sprintf("status: %s", firstNonEmpty(run.Status, "-")),
		fmt.Sprintf("agent: %s", firstNonEmpty(run.AgentName, "-")),
		fmt.Sprintf("mode: %s", firstNonEmpty(run.Mode, "-")),
	}
	if run.Route != "" {
		lines = append(lines, fmt.Sprintf("route: %s", run.Route))
	}
	if run.ModelName != "" {
		lines = append(lines, fmt.Sprintf("model: %s", run.ModelName))
	}
	if run.Goal != "" {
		lines = append(lines, "goal: "+trimBlock(run.Goal))
	}
	if run.ToolName != "" {
		lines = append(lines, fmt.Sprintf("tool: %s", run.ToolName))
	}
	if len(run.VisibleTools) > 0 {
		lines = append(lines, fmt.Sprintf("visible_tools: %s", strings.Join(run.VisibleTools, ", ")))
	}
	if len(run.SelectedSkills) > 0 {
		lines = append(lines, fmt.Sprintf("selected_skills: %s", strings.Join(run.SelectedSkills, ", ")))
	}
	if run.Error != "" {
		lines = append(lines, "error: "+trimBlock(run.Error))
	}
	if len(run.ChildRunIDs) > 0 {
		lines = append(lines, fmt.Sprintf("children: %s", strings.Join(run.ChildRunIDs, ", ")))
	}
	if len(run.ApprovalIDs) > 0 {
		lines = append(lines, fmt.Sprintf("approvals: %s", strings.Join(run.ApprovalIDs, ", ")))
	}
	if len(run.Steps) == 0 {
		if run.Result != "" {
			lines = append(lines, "result: "+trimBlock(run.Result))
		}
		return strings.Join(lines, "\n")
	}
	lines = append(lines, "steps:")
	for _, step := range run.Steps {
		head := fmt.Sprintf("%d. %s %s", step.Index, step.Kind, step.Status)
		if step.AgentName != "" {
			head += " [" + step.AgentName + "]"
		}
		if step.ToolName != "" {
			head += " " + step.ToolName
		}
		lines = append(lines, head)
		if step.Input != "" {
			lines = append(lines, "in: "+trimBlock(step.Input))
		}
		if step.Output != "" {
			lines = append(lines, "out: "+trimBlock(step.Output))
		}
	}
	if run.Result != "" {
		lines = append(lines, "result: "+trimBlock(run.Result))
	}
	return strings.Join(lines, "\n")
}

func (h Handler) learnReply(ctx context.Context, sessionKey, runID string) string {
	run, ok, errMsg := h.loadTraceRun(ctx, sessionKey, runID)
	if !ok {
		return errMsg
	}
	lines := []string{
		fmt.Sprintf("run `%s` 学习摘要", run.ID),
		fmt.Sprintf("status: %s", firstNonEmpty(run.Status, "-")),
		fmt.Sprintf("route: %s", firstNonEmpty(run.Route, "-")),
	}
	if run.ModelName != "" {
		lines = append(lines, fmt.Sprintf("model: %s", run.ModelName))
	}
	if run.Goal != "" {
		lines = append(lines, "任务目标: "+trimBlock(run.Goal))
	}
	if len(run.VisibleTools) > 0 {
		lines = append(lines, "当前可见能力: "+strings.Join(run.VisibleTools, ", "))
	}
	if len(run.SelectedSkills) > 0 {
		lines = append(lines, "当前激活技能: "+strings.Join(run.SelectedSkills, ", "))
	}
	if plan := firstStepByKinds(run.Steps, "dev_plan", "plan"); plan != nil {
		lines = append(lines, "", "初始分解:")
		lines = append(lines, trimBlock(plan.Output))
	}
	execution := collectExecutionNarrative(run.Steps)
	if len(execution) > 0 {
		lines = append(lines, "", "执行过程:")
		lines = append(lines, execution...)
	}
	fallbacks := collectFallbackNarrative(run.Steps, run.Error)
	if len(fallbacks) > 0 {
		lines = append(lines, "", "失败与切换:")
		lines = append(lines, fallbacks...)
	}
	if run.Result != "" {
		lines = append(lines, "", "最终输出:")
		lines = append(lines, trimBlock(run.Result))
	}
	if run.Error != "" && run.Result == "" {
		lines = append(lines, "", "最终错误:")
		lines = append(lines, trimBlock(run.Error))
	}
	return strings.Join(lines, "\n")
}

func (h Handler) loadTraceRun(ctx context.Context, sessionKey, runID string) (agentharness.Run, bool, string) {
	if h.Harness == nil {
		return agentharness.Run{}, false, "当前 runtime 没有启用 run trace。"
	}
	var (
		run agentharness.Run
		ok  bool
		err error
	)
	if strings.TrimSpace(runID) != "" {
		run, ok = h.Harness.GetRun(ctx, runID)
		if !ok {
			return agentharness.Run{}, false, fmt.Sprintf("没有找到 run `%s`。", runID)
		}
	} else {
		var runs []agentharness.Run
		runs, err = h.Harness.ListRuns(ctx, sessionKey, 1)
		if err != nil {
			return agentharness.Run{}, false, fmt.Sprintf("读取 trace 失败：%v", err)
		}
		if len(runs) == 0 {
			return agentharness.Run{}, false, "当前 session 还没有可查看的 run。"
		}
		run = runs[0]
	}
	return run, true, ""
}

func firstStepByKinds(steps []agentharness.RunStep, kinds ...string) *agentharness.RunStep {
	for _, kind := range kinds {
		for i := range steps {
			if strings.EqualFold(steps[i].Kind, kind) {
				return &steps[i]
			}
		}
	}
	return nil
}

func collectExecutionNarrative(steps []agentharness.RunStep) []string {
	lines := make([]string, 0, len(steps))
	for _, step := range steps {
		switch step.Kind {
		case "dev_plan":
			continue
		case "plan", "replan":
			lines = append(lines, fmt.Sprintf("- %s 产出计划：%s", firstNonEmpty(step.AgentName, step.Kind), trimBlock(step.Output)))
		case "tool_choice":
			lines = append(lines, fmt.Sprintf("- 选择工具 `%s`：%s", firstNonEmpty(step.ToolName, "-"), trimBlock(step.Output)))
		case "route_fallback":
			lines = append(lines, fmt.Sprintf("- 运行时降级：%s", trimBlock(step.Output)))
		case "tool":
			lines = append(lines, fmt.Sprintf("- 调用工具 `%s`，状态 `%s`。", firstNonEmpty(step.ToolName, "-"), step.Status))
		case "callback_model_start":
			lines = append(lines, fmt.Sprintf("- 模型节点开始：%s", trimBlock(firstNonEmpty(step.Output, step.Input))))
		case "callback_model_end":
			lines = append(lines, fmt.Sprintf("- 模型节点完成：%s", trimBlock(step.Output)))
		case "callback_model_error":
			lines = append(lines, fmt.Sprintf("- 模型节点报错：%s", trimBlock(step.Output)))
		case "callback_tool_start":
			lines = append(lines, fmt.Sprintf("- 工具节点开始 `%s`：%s", firstNonEmpty(step.ToolName, "-"), trimBlock(step.Input)))
		case "callback_tool_end":
			lines = append(lines, fmt.Sprintf("- 工具节点完成 `%s`：%s", firstNonEmpty(step.ToolName, "-"), trimBlock(step.Output)))
		case "callback_tool_error":
			lines = append(lines, fmt.Sprintf("- 工具节点报错 `%s`：%s", firstNonEmpty(step.ToolName, "-"), trimBlock(step.Output)))
		case "tool_result":
			lines = append(lines, fmt.Sprintf("- 工具 `%s` 返回：%s", firstNonEmpty(step.ToolName, "-"), trimBlock(step.Output)))
		case "learn_proposal":
			lines = append(lines, fmt.Sprintf("- 已生成学习草稿：%s", trimBlock(step.Output)))
		case "transfer":
			lines = append(lines, fmt.Sprintf("- 转交给 agent：%s", trimBlock(step.Output)))
		case "llm", "agent_message", "respond":
			lines = append(lines, fmt.Sprintf("- `%s` 输出：%s", firstNonEmpty(step.Kind, "respond"), trimBlock(step.Output)))
		}
	}
	return lines
}

func collectFallbackNarrative(steps []agentharness.RunStep, runError string) []string {
	lines := make([]string, 0)
	for _, step := range steps {
		if step.Kind == "route_fallback" {
			lines = append(lines, "- 路由已自动降级："+trimBlock(step.Output))
			continue
		}
		if step.Status == "failed" {
			lines = append(lines, fmt.Sprintf("- `%s` 失败：%s", firstNonEmpty(step.ToolName, step.Kind), trimBlock(step.Output)))
			continue
		}
		lowered := strings.ToLower(step.Output)
		if strings.Contains(lowered, "provider policy deny") || strings.Contains(lowered, "try another allowed command") {
			lines = append(lines, fmt.Sprintf("- `%s` 被策略拒绝，agent 应回退到其他工具：%s", firstNonEmpty(step.ToolName, step.Kind), trimBlock(step.Output)))
		}
	}
	if strings.TrimSpace(runError) != "" {
		lines = append(lines, "- run 最终报错："+trimBlock(runError))
	}
	return lines
}

func (h Handler) sendText(ctx context.Context, chatID, text string) error {
	token, err := h.fetchTenantToken(ctx)
	if err != nil {
		return err
	}
	bodyContent, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}
	body, err := json.Marshal(sendMessageRequest{
		ReceiveID: chatID,
		MsgType:   "text",
		Content:   string(bodyContent),
	})
	if err != nil {
		return err
	}
	baseURL := strings.TrimRight(h.Config.BaseURL, "/")
	endpoint := baseURL + "/open-apis/im/v1/messages?receive_id_type=chat_id"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := h.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("feishu send http %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var parsed sendMessageResponse
	if err := json.Unmarshal(respBody, &parsed); err == nil && parsed.Code != 0 {
		return fmt.Errorf("feishu send api error code=%d msg=%s", parsed.Code, parsed.Msg)
	}
	return nil
}

func (h Handler) fetchTenantToken(ctx context.Context) (string, error) {
	body, err := json.Marshal(map[string]string{
		"app_id":     h.Config.AppID,
		"app_secret": h.Config.AppSecret,
	})
	if err != nil {
		return "", err
	}
	baseURL := strings.TrimRight(h.Config.BaseURL, "/")
	endpoint, err := url.JoinPath(baseURL, "open-apis", "auth", "v3", "tenant_access_token", "internal")
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := h.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("feishu token http %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var parsed tokenResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", err
	}
	if parsed.Code != 0 {
		return "", fmt.Errorf("feishu token api error code=%d msg=%s", parsed.Code, parsed.Msg)
	}
	if parsed.TenantAccessToken == "" {
		return "", fmt.Errorf("feishu token missing tenant_access_token")
	}
	return parsed.TenantAccessToken, nil
}

func (h Handler) client() *http.Client {
	if h.HTTPClient != nil {
		return h.HTTPClient
	}
	return &http.Client{Timeout: 10 * time.Second}
}
