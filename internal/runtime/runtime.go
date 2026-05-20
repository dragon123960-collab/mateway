package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/model"
	"github.com/dongping/mateway/internal/observer"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/skill"
	"github.com/dongping/mateway/internal/tool"
)

type Runtime struct {
	Config    *config.Root
	Model     model.Planner
	Tools     *tool.Registry
	Skills    *skill.Registry
	Sanitizer ResponseSanitizer
	Logger    observer.Logger
	ToolCtx   tool.Context
	MaxSteps  int
	Observer  Observer
	Sessions  session.Store
}

type Observer interface {
	Plan(traceID string, plan model.Plan)
	ToolStart(traceID string, step model.PlanStep)
	ToolDone(traceID string, result model.ToolResult)
	Reply(traceID string, text string, failed bool)
	Control(traceID string, control string, style string)
	Failed(traceID string, reason string)
}

type Response struct {
	Reply          channel.OutboundMessage
	TraceID        string
	Plan           model.Plan
	Results        []model.ToolResult
	AwaitConfirm   bool
	AwaitUserInput bool
	Failed         bool
}

func New(cfg *config.Root, planner model.Planner, registry *tool.Registry, logger observer.Logger, projectRoot string) Runtime {
	if registry == nil {
		registry = tool.NewBuiltinRegistry()
	}
	ctx := BuildToolContext(cfg, projectRoot)
	home := firstNonEmpty(ctx.Home, config.DefaultHome())
	skills, err := skill.LoadRegistry(ctx.Workspace, "main")
	if err != nil {
		skills = skill.NewBuiltinRegistry()
	}
	return Runtime{
		Config:    cfg,
		Model:     planner,
		Tools:     registry,
		Skills:    skills,
		Sanitizer: DefaultSanitizer{},
		Logger:    logger,
		ToolCtx:   ctx,
		MaxSteps:  6,
		Sessions:  session.NewFileStore(filepath.Join(home, "run", "sessions")),
	}
}

func BuildToolContext(cfg *config.Root, projectRoot string) tool.Context {
	if projectRoot == "" {
		projectRoot, _ = filepath.Abs(".")
	}
	var home, workspace string
	var allowed []string
	var search tool.SearchConfig
	if cfg != nil {
		home = cfg.App.Home
		workspace = cfg.App.Workspace
		allowed = append(allowed, cfg.Security.AccessiblePaths...)
		search = tool.SearchConfig{
			TavilyEnabled:        cfg.Search.Providers.Tavily.Enabled,
			TavilyBaseURL:        cfg.Search.Providers.Tavily.BaseURL,
			TavilyAPIKey:         cfg.Search.Providers.Tavily.ResolvedAPIKey(),
			TavilyMaxResults:     cfg.Search.Providers.Tavily.MaxResults,
			TavilySearchDepth:    cfg.Search.Providers.Tavily.SearchDepth,
			TavilyTopic:          cfg.Search.Providers.Tavily.Topic,
			DuckDuckGoEnabled:    cfg.Search.Providers.DuckDuckGo.Enabled,
			DuckDuckGoMaxResults: cfg.Search.Providers.DuckDuckGo.MaxResults,
			DuckDuckGoRegion:     cfg.Search.Providers.DuckDuckGo.Region,
		}
	}
	return tool.Context{
		Home:          home,
		ProjectRoot:   firstNonEmpty(projectRoot, home),
		Workspace:     workspace,
		AllowedRoots:  append([]string{projectRoot}, allowed...),
		ConfigSummary: SafeConfigSummary(cfg),
		Search:        search,
	}
}

func SafeConfigSummary(cfg *config.Root) string {
	if cfg == nil {
		return "config: unavailable"
	}
	models := make([]string, 0, len(cfg.Models))
	for _, m := range cfg.Models {
		models = append(models, fmt.Sprintf("%s(provider=%s, api=%s, model=%s, enabled=%t)", m.Name, m.Provider, m.API, m.Model, m.Enabled))
	}
	return strings.Join([]string{
		"app.name=" + cfg.App.Name,
		"app.home=" + cfg.App.Home,
		"app.workspace=" + cfg.App.Workspace,
		fmt.Sprintf("feishu.enabled=%t websocket=%t", cfg.Channels.Feishu.Enabled, cfg.Channels.Feishu.WebSocket.Enabled),
		"models=" + strings.Join(models, "; "),
		fmt.Sprintf("search.tavily.enabled=%t search.duckduckgo.enabled=%t", cfg.Search.Providers.Tavily.Enabled, cfg.Search.Providers.DuckDuckGo.Enabled),
	}, "\n")
}

func (r Runtime) Handle(ctx context.Context, msg channel.InboundMessage) (Response, error) {
	loop := NewAgentLoop(r, msg)
	return loop.Run(ctx)
}

func (r Runtime) executePlan(ctx context.Context, traceID string, plan model.Plan, approvalGranted bool) ([]model.ToolResult, string) {
	var results []model.ToolResult
	steps := plan.Steps
	if r.MaxSteps > 0 && len(steps) > r.MaxSteps {
		steps = steps[:r.MaxSteps]
	}
	for _, step := range steps {
		if strings.TrimSpace(step.Tool) == "" {
			tr := model.ToolResult{StepID: step.ID, Tool: step.Tool, OK: false, Error: "tool is required", Output: "tool is required"}
			results = append(results, tr)
			if r.Observer != nil {
				r.Observer.ToolDone(traceID, tr)
			}
			continue
		}
		r.Logger.Event("runtime.tool_start", map[string]any{"trace_id": traceID, "step_id": step.ID, "tool": step.Tool, "goal": step.Goal, "risk": step.Risk, "requires_confirm": step.RequiresConfirm})
		if r.Observer != nil {
			r.Observer.ToolStart(traceID, step)
		}
		def, ok := r.Tools.Get(step.Tool)
		if !ok {
			tr := model.ToolResult{StepID: step.ID, Tool: step.Tool, OK: false, Error: "unknown tool", Output: "unknown tool: " + step.Tool}
			results = append(results, tr)
			r.Logger.Event("runtime.tool_done", map[string]any{"trace_id": traceID, "step_id": step.ID, "tool": step.Tool, "ok": false, "error": tr.Error})
			if r.Observer != nil {
				r.Observer.ToolDone(traceID, tr)
			}
			continue
		}
		args := copyArgs(step.Args)
		delete(args, "confirmed")
		delete(args, "confirm")
		needsConfirm := tool.RequireConfirmForTool(step.Tool, args)
		if needsConfirm && !approvalGranted {
			tr := model.ToolResult{StepID: step.ID, Tool: step.Tool, OK: false, Error: "await_confirm", Output: confirmPromptForStep(step, args), Evidence: map[string]any{"kind": "step_confirm", "goal": step.Goal, "tool": step.Tool}}
			results = append(results, tr)
			r.Logger.Event("runtime.tool_done", map[string]any{"trace_id": traceID, "step_id": step.ID, "tool": step.Tool, "ok": false, "control": "await_confirm"})
			if r.Observer != nil {
				r.Observer.ToolDone(traceID, tr)
			}
			return results, "await_confirm"
		}
		call := tool.Call{Name: step.Tool, Args: args, Confirmed: approvalGranted, Context: r.ToolCtx}
		result := def.Run(ctx, call)
		tr := model.ToolResult{
			StepID:   step.ID,
			Tool:     step.Tool,
			OK:       result.OK,
			Output:   tool.Truncate(result.Output, tool.DefaultOutputLimit),
			Evidence: result.Evidence,
			Error:    result.Error,
		}
		if result.RequiresConfirm {
			tr.Error = "await_confirm"
			tr.Output = result.ConfirmMessage
			results = append(results, tr)
			r.Logger.Event("runtime.tool_done", map[string]any{"trace_id": traceID, "step_id": step.ID, "tool": step.Tool, "ok": false, "control": "await_confirm", "evidence": result.Evidence})
			if r.Observer != nil {
				r.Observer.ToolDone(traceID, tr)
			}
			return results, "await_confirm"
		}
		results = append(results, tr)
		r.Logger.Event("runtime.tool_done", map[string]any{"trace_id": traceID, "step_id": step.ID, "tool": step.Tool, "ok": tr.OK, "error": tr.Error, "output_chars": len(tr.Output), "evidence": tr.Evidence})
		if r.Observer != nil {
			r.Observer.ToolDone(traceID, tr)
		}
	}
	return results, ""
}

func (r Runtime) failure(msg channel.InboundMessage, plan *model.Plan, results []model.ToolResult, err error) Response {
	var p model.Plan
	if plan != nil {
		p = *plan
	}
	return Response{
		Reply:   r.sanitizeReply(channel.OutboundMessage{Channel: msg.Channel, ThreadID: msg.ThreadID, Text: userFacingError(err), Style: "error"}),
		TraceID: traceIDForMessage(msg),
		Plan:    p, Results: results, Failed: true,
	}
}

func userFacingError(err error) string {
	if err == nil {
		return "这次处理失败了，但我已经停在安全位置。"
	}
	text := err.Error()
	lower := strings.ToLower(text)
	if strings.Contains(lower, "insufficient tool evidence") {
		return "这次我没有拿到足够的工具证据来回答，已停止生成泛化结论。请重试；我会优先读取相关项目文档后再总结。"
	}
	if strings.Contains(lower, "unexpected eof") ||
		strings.Contains(lower, "model request") ||
		strings.Contains(lower, "api.minimaxi.com") ||
		strings.Contains(lower, "/anthropic/") ||
		strings.Contains(lower, "connection reset") ||
		strings.Contains(lower, "timeout") ||
		strings.Contains(lower, "context deadline exceeded") {
		return "这次请求模型服务时临时失败了，任务没有继续执行。请稍后重试；如果连续出现，我会建议查看 trace 日志定位。"
	}
	return "这次处理失败了，但我已经停在安全位置。请查看报告或 trace 里的错误详情。"
}

func confirmPromptForStep(step model.PlanStep, args map[string]string) string {
	switch step.Tool {
	case "shell.run":
		command := strings.TrimSpace(args["command"])
		if command != "" {
			return "这个命令可能会修改或删除本地内容，需要你确认后我才会执行。\n\n命令：`" + command + "`\n\n如果确认执行，请回复“同意”或“确认”；如果不执行，请回复“取消”。"
		}
	case "file.write", "file.patch":
		path := strings.TrimSpace(args["path"])
		if path != "" {
			return "这个文件操作会修改本地文件，需要你确认后我才会执行。\n\n文件：" + path + "\n\n如果确认执行，请回复“同意”或“确认”；如果不执行，请回复“取消”。"
		}
	}
	goal := strings.TrimSpace(step.Goal)
	if goal == "" {
		goal = step.Tool
	}
	return "这一步需要你确认后才能继续。\n\n操作：" + goal + "\n\n如果确认执行，请回复“同意”或“确认”；如果不执行，请回复“取消”。"
}

func (r Runtime) sanitizeReply(reply channel.OutboundMessage) channel.OutboundMessage {
	if r.Sanitizer == nil {
		return DefaultSanitizer{}.Sanitize(reply)
	}
	return r.Sanitizer.Sanitize(reply)
}

func hasRepairableFailure(results []model.ToolResult) bool {
	for _, result := range results {
		if !result.OK && result.Error != "await_confirm" {
			return true
		}
	}
	return false
}

func needsGroundedProjectEvidence(user string, results []model.ToolResult) bool {
	if !requiresProjectEvidence(user) {
		return false
	}
	return !hasGroundingEvidence(results)
}

func requiresProjectEvidence(user string) bool {
	normalized := normalizeIntentText(user)
	if normalized == "" {
		return false
	}
	hasLocalSubject := strings.Contains(normalized, "当前") ||
		strings.Contains(normalized, "mateway") ||
		strings.Contains(normalized, "项目") ||
		strings.Contains(normalized, "仓库") ||
		strings.Contains(normalized, "测试")
	hasKnowledgeAction := strings.Contains(normalized, "总结") ||
		strings.Contains(normalized, "概览") ||
		strings.Contains(normalized, "梳理") ||
		strings.Contains(normalized, "说明") ||
		strings.Contains(normalized, "分析") ||
		strings.Contains(normalized, "列出") ||
		strings.Contains(normalized, "检查项") ||
		strings.Contains(normalized, "目标")
	return hasLocalSubject && hasKnowledgeAction
}

func hasGroundingEvidence(results []model.ToolResult) bool {
	for _, result := range results {
		if !result.OK {
			continue
		}
		switch strings.TrimSpace(result.Tool) {
		case "file.read", "file.summary", "project.index":
			return true
		}
		if kind, _ := result.Evidence["kind"].(string); kind == "file_read" || kind == "file_summary" || kind == "project_index" {
			return true
		}
	}
	return false
}

func normalizeIntentText(text string) string {
	replacer := strings.NewReplacer("，", "", "。", "", "？", "", "！", "", "：", "", "\n", "")
	return strings.ToLower(strings.TrimSpace(replacer.Replace(text)))
}

func anyFailed(results []model.ToolResult) bool {
	for _, result := range results {
		if !result.OK {
			return true
		}
	}
	return false
}

func fallbackSynthesis(results []model.ToolResult) string {
	var b strings.Builder
	for _, result := range results {
		status := "OK"
		if !result.OK {
			status = "WAIT/FAILED"
		}
		fmt.Fprintf(&b, "- %s %s via %s\n%s\n", result.StepID, status, result.Tool, strings.TrimSpace(result.Output))
		if result.Error != "" && result.Error != "await_confirm" {
			fmt.Fprintf(&b, "error: %s\n", result.Error)
		}
	}
	return strings.TrimSpace(b.String())
}

func styleForFailed(failed bool) string {
	if failed {
		return "error"
	}
	return "reply"
}

func copyArgs(in map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range in {
		out[key] = value
	}
	return out
}

func collectArtifacts(results []model.ToolResult) []session.Artifact {
	var artifacts []session.Artifact
	seen := map[string]struct{}{}
	for _, result := range results {
		evidence := result.Evidence
		if evidence == nil {
			continue
		}
		if path, _ := evidence["path"].(string); strings.TrimSpace(path) != "" {
			key := "path:" + path
			if _, ok := seen[key]; !ok {
				seen[key] = struct{}{}
				artifacts = append(artifacts, session.Artifact{
					Kind:    firstNonEmpty(stringValue(evidence["kind"]), "file"),
					Path:    path,
					Label:   result.Tool,
					Summary: shortenReply(result.Output, 180),
				})
			}
		}
		if url, _ := evidence["url"].(string); strings.TrimSpace(url) != "" {
			key := "url:" + url
			if _, ok := seen[key]; !ok {
				seen[key] = struct{}{}
				artifacts = append(artifacts, session.Artifact{
					Kind:      firstNonEmpty(stringValue(evidence["kind"]), "link"),
					SourceURL: url,
					Label:     result.Tool,
					Summary:   shortenReply(result.Output, 180),
				})
			}
		}
		if urls, ok := evidence["urls"].([]string); ok {
			for _, item := range urls {
				key := "url:" + item
				if _, exists := seen[key]; exists || strings.TrimSpace(item) == "" {
					continue
				}
				seen[key] = struct{}{}
				artifacts = append(artifacts, session.Artifact{Kind: firstNonEmpty(stringValue(evidence["kind"]), "link"), SourceURL: item, Label: result.Tool})
			}
		}
		if more := artifactsFromOutput(result); len(more) > 0 {
			for _, artifact := range more {
				key := artifact.Kind + ":" + firstNonEmpty(artifact.Path, artifact.SourceURL, artifact.Label)
				if _, ok := seen[key]; ok || strings.TrimSpace(key) == ":" {
					continue
				}
				seen[key] = struct{}{}
				artifacts = append(artifacts, artifact)
			}
		}
	}
	return artifacts
}

var urlPattern = regexp.MustCompile(`https?://[^\s]+`)

func artifactsFromOutput(result model.ToolResult) []session.Artifact {
	text := strings.TrimSpace(result.Output)
	if text == "" {
		return nil
	}
	var out []session.Artifact
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		urls := urlPattern.FindAllString(trimmed, -1)
		for _, item := range urls {
			label := ""
			if i > 0 {
				prev := strings.TrimSpace(lines[i-1])
				if prev != "" && !strings.HasPrefix(prev, "http") {
					label = prev
				}
			}
			out = append(out, session.Artifact{
				Kind:      "link",
				SourceURL: strings.TrimRight(item, ".,);"),
				Label:     firstNonEmpty(label, result.Tool),
				Summary:   shortenReply(trimmed, 120),
			})
		}
		if looksFilesystemPath(trimmed) {
			out = append(out, session.Artifact{
				Kind:    "file",
				Path:    strings.Trim(trimmed, "` "),
				Label:   result.Tool,
				Summary: shortenReply(trimmed, 120),
			})
		}
	}
	if strings.Contains(text, "Search results for:") {
		query := ""
		if lines := strings.SplitN(text, "\n", 2); len(lines) > 0 {
			query = strings.TrimPrefix(strings.TrimSpace(lines[0]), "Search results for:")
		}
		if strings.TrimSpace(query) != "" {
			out = append(out, session.Artifact{
				Kind:    "search_query",
				Label:   result.Tool,
				Summary: strings.TrimSpace(query),
			})
		}
	}
	return out
}

func looksFilesystemPath(text string) bool {
	text = strings.TrimSpace(strings.Trim(text, "`"))
	if text == "" {
		return false
	}
	if strings.HasPrefix(text, "/") || strings.HasPrefix(text, "~/") {
		return true
	}
	if strings.Contains(text, string(filepath.Separator)) && strings.Contains(text, ".") {
		return true
	}
	return false
}

func stringValue(v any) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func parseBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y", "confirmed":
		return true
	default:
		return false
	}
}

func DebugJSON(v any) string {
	data, _ := json.MarshalIndent(v, "", "  ")
	return string(data)
}

func traceIDForMessage(msg channel.InboundMessage) string {
	if strings.TrimSpace(msg.ID) != "" {
		return msg.Channel + "-" + msg.ID
	}
	if strings.TrimSpace(msg.SessionKey) != "" {
		return msg.SessionKey + "-" + time.Now().Format("20060102T150405.000000000")
	}
	return msg.Channel + "-" + time.Now().Format("20060102T150405.000000000")
}

func planToolNames(plan model.Plan) []string {
	out := make([]string, 0, len(plan.Steps))
	for _, step := range plan.Steps {
		out = append(out, step.Tool)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func fallbackSessionKey(msg channel.InboundMessage) string {
	channelName := firstNonEmpty(msg.Channel, "unknown")
	threadID := firstNonEmpty(msg.ThreadID, msg.UserID, msg.ID, "default")
	return channelName + ":" + threadID
}
