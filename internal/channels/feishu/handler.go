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
	"github.com/dongping/mateway/internal/memory"
	hostruntime "github.com/dongping/mateway/internal/runtime"
	"github.com/dongping/mateway/internal/scheduler"
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
	ThreadID   string
	UserID     string
	TaskID     string
	TaskKind   string
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
	if handled, reply := h.handleNaturalApproval(ctx, sessionKey, text); handled {
		return reply
	}
	switch {
	case text == "/new":
		if h.Harness == nil {
			return "当前 runtime 还没有启用 session reset。"
		}
		if h.Harness.SessionBusy(sessionKey) {
			return "当前 session 正在处理中，请稍后再试 `/new`。"
		}
		if err := h.Harness.ResetSession(ctx, sessionKey); err != nil {
			return fmt.Sprintf("重置 session 失败：%v", err)
		}
		return "已重置当前 session：对话历史、summary、agent 偏好和待审批项已清空。"
	case text == "/trace" || text == "/learn":
		if text == "/learn" {
			return h.learnReply(ctx, sessionKey, "")
		}
		return h.traceReply(ctx, sessionKey, "")
	case strings.HasPrefix(text, "/trace "):
		return h.traceReply(ctx, sessionKey, strings.TrimSpace(strings.TrimPrefix(text, "/trace ")))
	case strings.HasPrefix(text, "/learn "):
		return h.learnReply(ctx, sessionKey, strings.TrimSpace(strings.TrimPrefix(text, "/learn ")))
	case strings.HasPrefix(text, "/learn_apply"):
		if h.Harness == nil {
			return "当前 runtime 没有启用 learning loop。"
		}
		parts := strings.Fields(text)
		if len(parts) < 2 {
			return "用法: /learn_apply <run-id> [proposal-id]"
		}
		proposalID := ""
		if len(parts) > 2 {
			proposalID = parts[2]
		}
		applied, err := h.Harness.ApplyLearningProposal(ctx, parts[1], proposalID, sessionKey)
		if err != nil {
			return fmt.Sprintf("应用学习建议失败：%v", err)
		}
		lines := make([]string, 0, len(applied))
		for _, proposal := range applied {
			lines = append(lines, fmt.Sprintf("- %s [%s] -> %s", proposal.ID, proposal.Kind, firstNonEmpty(proposal.TargetPath, "-")))
		}
		return "已应用学习建议:\n" + strings.Join(lines, "\n")
	case text == "/schedule" || strings.HasPrefix(text, "/schedule "):
		return h.scheduleReply(ctx, text)
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
		runs, err := h.Harness.ListTaskRuns(ctx, sessionKey, 8)
		if err != nil {
			return fmt.Sprintf("读取 runs 失败：%v", err)
		}
		if len(runs) == 0 {
			return "当前 session 还没有 run 记录。"
		}
		lines := make([]string, 0, len(runs))
		for _, run := range runs {
			lines = append(lines, fmt.Sprintf("- %s task=%s %s", run.ID, firstNonEmpty(strings.TrimSpace(run.TaskID), "-"), agentharness.FormatTaskDigest(run)))
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
		return trimBlock(note.Content)
	case text == "/last":
		if h.Harness == nil {
			return "当前 runtime 没有启用记忆召回。"
		}
		note, ok, err := h.Harness.Memory.ReadSessionSummary(ctx, sessionKey)
		if err != nil {
			return fmt.Sprintf("读取记忆失败：%v", err)
		}
		if ok {
			if digest := metadataString(note.Metadata, "latest_task_digest"); strings.TrimSpace(digest) != "" {
				return "最近任务:\n" + trimBlock(digest)
			}
		}
		if tasks, taskErr := h.Harness.Memory.RecentTaskRecordsBySession(ctx, sessionKey, 1); taskErr == nil && len(tasks) > 0 {
			return "最近任务:\n" + trimBlock(agentharness.FormatTaskDigest(agentharness.Run{
				TaskID:   tasks[0].TaskID,
				Goal:     tasks[0].Goal,
				Status:   tasks[0].Status,
				Result:   tasks[0].Completion.Summary,
				TaskType: tasks[0].TaskType,
			}))
		}
		runs, runsErr := h.Harness.ListTaskRuns(ctx, sessionKey, 1)
		if runsErr == nil && len(runs) > 0 {
			return "最近任务:\n" + agentharness.FormatTaskDigest(runs[0])
		}
		if !ok || strings.TrimSpace(note.Content) == "" {
			return "我这里还没有这条 session 的近期任务记录。"
		}
		return "最近任务:\n" + trimBlock(note.Content)
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
			lines = append(lines, fmt.Sprintf("- %s tool=%s task=%s agent=%s", item.ID, item.ToolName, firstNonEmpty(strings.TrimSpace(item.TaskID), firstNonEmpty(strings.TrimSpace(item.RunID), "-")), item.AgentName))
			lines = append(lines, "  危险点："+agentharness.ApprovalRiskSummary(item))
			if args := agentharness.ApprovalArgumentSummary(item); strings.TrimSpace(args) != "" {
				lines = append(lines, "  参数："+args)
			}
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
			args := map[string]any{}
			if strings.TrimSpace(h.TaskID) != "" {
				args["task_id"] = strings.TrimSpace(h.TaskID)
			}
			if strings.TrimSpace(h.TaskKind) != "" {
				args["task_kind"] = strings.TrimSpace(h.TaskKind)
			}
			run, err := h.Harness.Start(ctx, agentharness.Request{
				SessionKey: sessionKey,
				ThreadID:   strings.TrimSpace(firstNonEmpty(h.ThreadID, sessionKey)),
				UserID:     strings.TrimSpace(h.UserID),
				Channel:    "feishu",
				UserText:   text,
				Mode:       "chat",
				Arguments:  args,
			}, nil)
			if err == nil {
				return run.Result
			}
			return formatRuntimeError(err)
		}
		return fmt.Sprintf("%s 已收到：%s\n\n当前基础版支持：\n- /skills 查看技能目录\n- /tools 查看当前能力\n- /run <skill-name> 执行技能", h.Config.BotName, text)
	}
}

func (h Handler) scheduleReply(_ context.Context, text string) string {
	if h.Harness == nil {
		return "当前 runtime 没有启用 schedule 查询。"
	}
	if strings.TrimSpace(h.Harness.Workspace) == "" {
		return "当前 runtime 没有可用的 workspace，无法读取定时任务。"
	}
	store := scheduler.Store{Workspace: h.Harness.Workspace}
	switch {
	case text == "/schedule" || text == "/schedule help":
		return "用法:\n- /schedule list\n- /schedule get <name>\n- /schedule runs <name>"
	case text == "/schedule list":
		items, err := store.List()
		if err != nil {
			return fmt.Sprintf("读取定时任务失败：%v", err)
		}
		if len(items) == 0 {
			return "当前没有定时任务。"
		}
		lines := make([]string, 0, len(items))
		for _, item := range items {
			line := fmt.Sprintf("%s enabled=%t schedule=%s next=%s status=%s",
				item.Name, item.Enabled, item.Description(), item.State.NextRunAt.Format(time.RFC3339), item.LastStatus())
			if snippet := h.scheduleTaskSnippet(item); snippet != "" {
				line += " " + snippet
			}
			lines = append(lines, line)
		}
		return "当前定时任务:\n" + strings.Join(lines, "\n")
	case strings.HasPrefix(text, "/schedule get "):
		name := strings.TrimSpace(strings.TrimPrefix(text, "/schedule get "))
		if name == "" {
			return "用法: /schedule get <name>"
		}
		job, ok, err := store.Get(name)
		if err != nil {
			return fmt.Sprintf("读取定时任务失败：%v", err)
		}
		if !ok {
			return fmt.Sprintf("没有找到定时任务 `%s`。", name)
		}
		payload := map[string]any{"job": job}
		if task := h.loadScheduleTaskRecord(job); task != nil {
			payload["last_task"] = map[string]any{
				"task_id":          task.TaskID,
				"status":           task.Status,
				"task_type":        task.TaskType,
				"summary":          task.Completion.Summary,
				"primary_artifact": taskPrimaryArtifact(task),
				"delivery_status":  firstNonEmpty(task.DeliveryStatus, task.Completion.DeliveryStatus),
			}
		}
		data, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return fmt.Sprintf("序列化定时任务失败：%v", err)
		}
		return trimBlock(string(data))
	case strings.HasPrefix(text, "/schedule runs "):
		name := strings.TrimSpace(strings.TrimPrefix(text, "/schedule runs "))
		if name == "" {
			return "用法: /schedule runs <name>"
		}
		lines, err := store.ReadRuns(name, 20)
		if err != nil {
			return fmt.Sprintf("读取定时任务运行历史失败：%v", err)
		}
		if len(lines) == 0 {
			return fmt.Sprintf("定时任务 `%s` 还没有运行历史。", name)
		}
		reply := "最近运行记录:\n" + strings.Join(lines, "\n")
		job, ok, err := store.Get(name)
		if err == nil && ok {
			if task := h.loadScheduleTaskRecord(job); task != nil {
				reply += "\n\n最近任务结果:\n"
				reply += fmt.Sprintf("- task_id: %s\n- status: %s\n- summary: %s", task.TaskID, task.Status, trimBlock(firstNonEmpty(task.Completion.Summary, "(empty)")))
				if artifact := taskPrimaryArtifact(task); artifact != "" {
					reply += "\n- primary_artifact: " + artifact
				}
			}
		}
		return reply
	default:
		return "用法:\n- /schedule list\n- /schedule get <name>\n- /schedule runs <name>"
	}
}

func (h Handler) loadScheduleTaskRecord(job scheduler.Job) *memory.TaskRecord {
	if h.Harness == nil || strings.TrimSpace(job.State.LastTaskID) == "" {
		return nil
	}
	record, ok, err := h.Harness.Memory.GetTaskRecord(context.Background(), job.State.LastTaskID)
	if err != nil || !ok {
		return nil
	}
	return &record
}

func (h Handler) scheduleTaskSnippet(job scheduler.Job) string {
	task := h.loadScheduleTaskRecord(job)
	if task == nil {
		return ""
	}
	parts := []string{"task_id=" + task.TaskID}
	if artifact := taskPrimaryArtifact(task); artifact != "" {
		parts = append(parts, "artifact="+artifact)
	}
	return strings.Join(parts, " ")
}

func taskPrimaryArtifact(record *memory.TaskRecord) string {
	if record == nil || record.Completion.PrimaryArtifact == nil {
		return ""
	}
	return strings.TrimSpace(record.Completion.PrimaryArtifact.PathOrRef)
}

func (h Handler) handleNaturalApproval(ctx context.Context, sessionKey, text string) (bool, string) {
	if h.Harness == nil || isSlashCommand(text) {
		return false, ""
	}
	items := h.Harness.ListPending(sessionKey)
	if len(items) == 0 {
		return false, ""
	}
	decision, ok := detectApprovalIntent(text)
	if !ok {
		return false, ""
	}
	reply, err := h.Harness.ReviewPending(ctx, sessionKey, "", decision, nil)
	if err != nil {
		if decision {
			return true, fmt.Sprintf("批准失败：%v", err)
		}
		return true, fmt.Sprintf("拒绝失败：%v", err)
	}
	if h.Harness.Sessions != nil {
		_ = h.Harness.Sessions.Append(sessionKey,
			session.Message{Role: "user", Content: text},
			session.Message{Role: "assistant", Content: reply},
		)
		_ = h.Harness.RefreshSessionSummaryForSession(ctx, sessionKey)
	}
	return true, reply
}

func detectApprovalIntent(text string) (bool, bool) {
	normalized := strings.ToLower(strings.TrimSpace(text))
	if normalized == "" {
		return false, false
	}
	for _, token := range []string{"不同意", "不可以", "不批准", "不执行", "先不要", "先别", "取消", "拒绝", "deny", "no", "stop"} {
		if strings.Contains(normalized, token) {
			return false, true
		}
	}
	for _, token := range []string{"同意", "可以", "批准", "执行吧", "继续执行", "继续", "确认", "好的执行", "go ahead", "approve", "yes", "ok"} {
		if strings.Contains(normalized, token) {
			return true, true
		}
	}
	return false, false
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
	if run.TaskType != "" {
		lines = append(lines, fmt.Sprintf("task_type: %s", run.TaskType))
	}
	if run.Route != "" {
		lines = append(lines, fmt.Sprintf("route: %s", run.Route))
	}
	if strings.TrimSpace(run.TaskID) != "" && h.Harness != nil {
		if record, ok, err := h.Harness.Memory.GetTaskRecord(ctx, run.TaskID); err == nil && ok {
			lines = append(lines, fmt.Sprintf("task_record_status: %s", firstNonEmpty(record.Status, "-")))
			if artifact := taskPrimaryArtifact(&record); artifact != "" {
				lines = append(lines, fmt.Sprintf("primary_artifact: %s", artifact))
			}
			if summary := strings.TrimSpace(record.Completion.Summary); summary != "" {
				lines = append(lines, "task_summary: "+trimBlock(summary))
			}
		}
	}
	if run.ModelName != "" {
		lines = append(lines, fmt.Sprintf("model: %s", run.ModelName))
	}
	if run.ModelAttempts > 0 || run.Model429Count > 0 {
		lines = append(lines, fmt.Sprintf("model_attempts: %d", run.ModelAttempts))
		lines = append(lines, fmt.Sprintf("model_429_count: %d", run.Model429Count))
	}
	if run.PromptTokens > 0 || run.CompletionTokens > 0 || run.TotalTokens > 0 {
		lines = append(lines, fmt.Sprintf("tokens: prompt=%d completion=%d total=%d", run.PromptTokens, run.CompletionTokens, run.TotalTokens))
	}
	if run.ModelDurationMs > 0 {
		lines = append(lines, fmt.Sprintf("model_duration_ms: %d", run.ModelDurationMs))
	}
	if run.EstimatedCostUSD > 0 {
		lines = append(lines, fmt.Sprintf("estimated_cost_usd: %.6f", run.EstimatedCostUSD))
	}
	if run.ContextCompactions > 0 {
		lines = append(lines, fmt.Sprintf("context_compactions: %d", run.ContextCompactions))
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
		if shouldSkipTraceStep(step) {
			continue
		}
		head := fmt.Sprintf("%d. %s %s", step.Index, step.Kind, step.Status)
		if step.AgentName != "" {
			head += " [" + step.AgentName + "]"
		}
		if step.ToolName != "" {
			head += " " + step.ToolName
		}
		lines = append(lines, head)
		if step.Input != "" {
			lines = append(lines, "in: "+trimBlock(summarizeTraceStepValue(step.Kind, step.Input, 180)))
		}
		if step.Output != "" {
			lines = append(lines, "out: "+trimBlock(summarizeTraceStepValue(step.Kind, step.Output, 220)))
		}
	}
	if run.Result != "" {
		lines = append(lines, "result: "+trimBlock(run.Result))
	}
	return strings.Join(lines, "\n")
}

func shouldSkipTraceStep(step agentharness.RunStep) bool {
	switch step.Kind {
	case "llm":
		return true
	default:
		return false
	}
}

func summarizeTraceStepValue(kind, value string, limit int) string {
	switch strings.TrimSpace(kind) {
	case "callback_model_end", "respond", "agent_message":
		clean := strings.TrimSpace(textutil.CleanBlock(value, 0))
		if clean == "" {
			return ""
		}
		return fmt.Sprintf("生成最终答复（chars=%d）", len([]rune(clean)))
	case "tool_result":
		return summarizeTraceBlock(value, limit)
	case "callback_model_start", "callback_tool_start":
		return textutil.CleanInline(value, min(120, limit))
	default:
		return value
	}
}

func summarizeTraceBlock(value string, limit int) string {
	clean := textutil.CleanBlock(value, 0)
	for _, line := range strings.Split(clean, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "|---") {
			return textutil.CleanInline(line, limit)
		}
	}
	return textutil.CleanInline(clean, limit)
}

func (h Handler) learnReply(ctx context.Context, sessionKey, runID string) string {
	run, ok, errMsg := h.loadTraceRun(ctx, sessionKey, runID)
	if !ok {
		return errMsg
	}
	return agentharness.FormatLearnReport(run)
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
		runs, err = h.Harness.ListTaskRuns(ctx, sessionKey, 1)
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

func metadataString(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(metadata[key]))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
