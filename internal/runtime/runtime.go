package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/memory"
	"github.com/dongping/mateway/internal/model"
	"github.com/dongping/mateway/internal/observer"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/skill"
	"github.com/dongping/mateway/internal/textmatch"
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
	Memory    memory.Store
	Acceptors *AcceptanceRegistry
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
	Reply             channel.OutboundMessage
	TraceID           string
	Plan              model.Plan
	Results           []model.ToolResult
	AwaitConfirm      bool
	AwaitUserInput    bool
	Failed            bool
	FinalAcceptStatus string
	FinalAcceptReason string
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
		Memory:    memory.NewStore(ctx.Workspace),
		Acceptors: NewAcceptanceRegistry(),
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
			CacheDir:                 filepath.Join(firstNonEmpty(cfg.App.Workspace, cfg.App.Home, config.DefaultHome()), "web-cache"),
			CacheEnabled:             cfg.Search.CacheEnabled,
			CacheTTLHours:            cfg.Search.CacheTTLHours,
			FreshCacheTTLHours:       cfg.Search.FreshCacheTTLHours,
			ProviderOrder:            append([]string(nil), cfg.Search.ProviderOrder...),
			TavilyEnabled:            cfg.Search.Providers.Tavily.Enabled,
			TavilyBaseURL:            cfg.Search.Providers.Tavily.BaseURL,
			TavilyAPIKey:             cfg.Search.Providers.Tavily.ResolvedAPIKey(),
			TavilyTimeoutSeconds:     cfg.Search.Providers.Tavily.TimeoutSeconds,
			TavilyMaxResults:         cfg.Search.Providers.Tavily.MaxResults,
			TavilyDailyBudget:        cfg.Search.Providers.Tavily.DailyBudget,
			TavilyMonthlyBudget:      cfg.Search.Providers.Tavily.MonthlyBudget,
			TavilySearchDepth:        cfg.Search.Providers.Tavily.SearchDepth,
			TavilyTopic:              cfg.Search.Providers.Tavily.Topic,
			DuckDuckGoEnabled:        cfg.Search.Providers.DuckDuckGo.Enabled,
			DuckDuckGoTimeoutSeconds: cfg.Search.Providers.DuckDuckGo.TimeoutSeconds,
			DuckDuckGoMaxResults:     cfg.Search.Providers.DuckDuckGo.MaxResults,
			DuckDuckGoRegion:         cfg.Search.Providers.DuckDuckGo.Region,
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

func (r Runtime) executePlan(ctx context.Context, traceID string, plan model.Plan, approvalGranted bool, approvedStepID string, previousSteps map[string]session.StepState, previousResults []model.ToolResult) ([]model.ToolResult, string) {
	var results []model.ToolResult
	approvalConsumed := false
	steps := plan.Steps
	if r.MaxSteps > 0 && len(steps) > r.MaxSteps {
		steps = steps[:r.MaxSteps]
	}
	completed := reusableResultsMap(previousSteps, previousResults)
	for i := 0; i < len(steps); {
		if failedDep := firstFailedDependency(steps[i], completed); failedDep != "" {
			result := dependencyFailedResult(steps[i], failedDep)
			results = append(results, result)
			completed[result.StepID] = result
			r.Logger.Event("runtime.step_skipped", map[string]any{"trace_id": traceID, "step_id": steps[i].ID, "tool": steps[i].Tool, "reason": result.Error, "dependency": failedDep})
			if r.Observer != nil {
				r.Observer.ToolDone(traceID, result)
			}
			i++
			continue
		}
		if reused, ok := reuseStepResult(steps[i], completed); ok {
			results = append(results, reused)
			completed[reused.StepID] = reused
			i++
			continue
		}
		batch, next := executableBatch(steps, i, completed, r.Tools)
		if len(batch) == 0 {
			batch = []model.PlanStep{steps[i]}
			next = i + 1
		}
		batchResults, control, consumed := r.executeStepBatch(ctx, traceID, batch, approvalGranted, approvedStepID, approvalConsumed)
		if consumed {
			approvalConsumed = true
		}
		results = append(results, batchResults...)
		for _, result := range batchResults {
			completed[result.StepID] = result
		}
		if control != "" {
			return results, control
		}
		i = next
	}
	return results, ""
}

func executableBatch(steps []model.PlanStep, start int, completed map[string]model.ToolResult, registry *tool.Registry) ([]model.PlanStep, int) {
	if start >= len(steps) {
		return nil, start
	}
	first := steps[start]
	if !stepDependenciesSatisfied(first, completed) || !stepCanRunParallel(first, registry) {
		return []model.PlanStep{first}, start + 1
	}
	batch := []model.PlanStep{first}
	scopes := map[string]struct{}{parallelScope(first, registry): {}}
	for i := start + 1; i < len(steps) && len(batch) < 3; i++ {
		step := steps[i]
		if !stepDependenciesSatisfied(step, completed) || !stepCanRunParallel(step, registry) {
			break
		}
		scope := parallelScope(step, registry)
		if scope == "" {
			scope = "step:" + step.ID
		}
		if _, exists := scopes[scope]; exists {
			break
		}
		batch = append(batch, step)
		scopes[scope] = struct{}{}
	}
	if len(batch) == 1 {
		return batch, start + 1
	}
	return batch, start + len(batch)
}

func stepDependenciesSatisfied(step model.PlanStep, completed map[string]model.ToolResult) bool {
	for _, dep := range step.DependsOn {
		result, ok := completed[strings.TrimSpace(dep)]
		if !ok || !result.OK {
			return false
		}
	}
	return true
}

func firstFailedDependency(step model.PlanStep, completed map[string]model.ToolResult) string {
	for _, dep := range step.DependsOn {
		dep = strings.TrimSpace(dep)
		if dep == "" {
			continue
		}
		result, ok := completed[dep]
		if ok && !result.OK {
			return dep
		}
	}
	return ""
}

func dependencyFailedResult(step model.PlanStep, dep string) model.ToolResult {
	reason := "dependency_failed: " + dep
	return model.ToolResult{
		StepID: step.ID,
		Tool:   step.Tool,
		OK:     false,
		Error:  "dependency_failed",
		Output: reason,
		Evidence: map[string]any{
			"kind":       "dependency_failed",
			"dependency": dep,
		},
	}
}

func stepCanRunParallel(step model.PlanStep, registry *tool.Registry) bool {
	def, ok := registry.Get(step.Tool)
	if !ok {
		return false
	}
	if def.Risk != tool.RiskSafeRead {
		return false
	}
	switch def.Metadata.ParallelMode {
	case tool.ParallelReadOnlyOK, tool.ParallelIsolatedOnly:
		return true
	default:
		return false
	}
}

func parallelScope(step model.PlanStep, registry *tool.Registry) string {
	def, ok := registry.Get(step.Tool)
	if !ok {
		return ""
	}
	scope := strings.TrimSpace(def.Metadata.ResourceScope)
	if strings.Contains(scope, "filesystem") {
		if path := strings.TrimSpace(step.Args["path"]); path != "" {
			return scope + ":" + path
		}
	}
	if strings.Contains(scope, "query") {
		if q := strings.TrimSpace(firstNonEmpty(step.Args["query"], step.Args["q"])); q != "" {
			return scope + ":" + q
		}
	}
	return scope
}

func (r Runtime) executeStepBatch(ctx context.Context, traceID string, batch []model.PlanStep, approvalGranted bool, approvedStepID string, approvalConsumed bool) ([]model.ToolResult, string, bool) {
	if len(batch) == 1 {
		result, control, consumed := r.executeSingleStep(ctx, traceID, batch[0], approvalGranted, approvedStepID, approvalConsumed)
		return []model.ToolResult{result}, control, consumed
	}
	type item struct {
		index    int
		result   model.ToolResult
		control  string
		consumed bool
	}
	out := make([]item, len(batch))
	var wg sync.WaitGroup
	for i, step := range batch {
		wg.Add(1)
		go func(index int, current model.PlanStep) {
			defer wg.Done()
			result, control, consumed := r.executeSingleStep(ctx, traceID, current, approvalGranted, approvedStepID, approvalConsumed)
			out[index] = item{index: index, result: result, control: control, consumed: consumed}
		}(i, step)
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].index < out[j].index })
	results := make([]model.ToolResult, 0, len(out))
	consumed := approvalConsumed
	for _, item := range out {
		results = append(results, item.result)
		if item.consumed {
			consumed = true
		}
	}
	for _, item := range out {
		if item.control != "" {
			return results, item.control, consumed
		}
	}
	return results, "", consumed
}

func (r Runtime) executeSingleStep(ctx context.Context, traceID string, step model.PlanStep, approvalGranted bool, approvedStepID string, approvalConsumed bool) (model.ToolResult, string, bool) {
	consumed := approvalConsumed
	if strings.TrimSpace(step.Tool) == "" {
		tr := model.ToolResult{StepID: step.ID, Tool: step.Tool, OK: false, Error: "tool is required", Output: "tool is required"}
		if r.Observer != nil {
			r.Observer.ToolDone(traceID, tr)
		}
		return tr, "", consumed
	}
	r.Logger.Event("runtime.tool_start", map[string]any{"trace_id": traceID, "step_id": step.ID, "tool": step.Tool, "goal": step.Goal, "risk": step.Risk, "requires_confirm": step.RequiresConfirm})
	if r.Observer != nil {
		r.Observer.ToolStart(traceID, step)
	}
	def, ok := r.Tools.Get(step.Tool)
	if !ok {
		tr := model.ToolResult{StepID: step.ID, Tool: step.Tool, OK: false, Error: "unknown tool", Output: "unknown tool: " + step.Tool}
		r.Logger.Event("runtime.tool_done", map[string]any{"trace_id": traceID, "step_id": step.ID, "tool": step.Tool, "ok": false, "error": tr.Error})
		if r.Observer != nil {
			r.Observer.ToolDone(traceID, tr)
		}
		return tr, "", consumed
	}
	args := copyArgs(step.Args)
	delete(args, "confirmed")
	delete(args, "confirm")
	needsConfirm := tool.RequireConfirmForTool(step.Tool, args)
	stepApproved := false
	if approvalGranted {
		switch {
		case approvedStepID != "" && strings.TrimSpace(step.ID) == strings.TrimSpace(approvedStepID):
			stepApproved = true
		case approvedStepID == "" && needsConfirm && !approvalConsumed:
			stepApproved = true
		}
	}
	if needsConfirm && !stepApproved {
		tr := model.ToolResult{StepID: step.ID, Tool: step.Tool, OK: false, Error: "await_confirm", Output: confirmPromptForStep(step, args), Evidence: map[string]any{"kind": "step_confirm", "goal": step.Goal, "tool": step.Tool, "step_id": step.ID}}
		r.Logger.Event("runtime.tool_done", map[string]any{"trace_id": traceID, "step_id": step.ID, "tool": step.Tool, "ok": false, "control": "await_confirm"})
		if r.Observer != nil {
			r.Observer.ToolDone(traceID, tr)
		}
		return tr, "await_confirm", consumed
	}
	call := tool.Call{Name: step.Tool, Args: args, Confirmed: stepApproved, Context: r.ToolCtx}
	if stepApproved {
		consumed = true
	}
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
		r.Logger.Event("runtime.tool_done", map[string]any{"trace_id": traceID, "step_id": step.ID, "tool": step.Tool, "ok": false, "control": "await_confirm", "evidence": result.Evidence})
		if r.Observer != nil {
			r.Observer.ToolDone(traceID, tr)
		}
		return tr, "await_confirm", consumed
	}
	accept := codeAcceptStep(step, tr, def, r.Acceptors)
	if shouldLLMAcceptStep(step, def, accept, nil) {
		accept = llmAcceptStep(ctx, r.Model, step.Goal, step, tr, def, r.Acceptors)
	}
	r.Logger.Event("runtime.step_accept", map[string]any{
		"trace_id": traceID,
		"step_id":  step.ID,
		"tool":     step.Tool,
		"status":   accept.Status,
		"reason":   accept.Reason,
		"source":   accept.Source,
	})
	if accept.Status == AcceptanceHardFail {
		tr.OK = false
		tr.Error = "step_verification_failed"
		tr.Output = strings.TrimSpace(tr.Output + "\n\nverification failed:\n" + firstNonEmpty(accept.Reason, "step rejected"))
		r.Logger.Event("runtime.execution_drift", map[string]any{"trace_id": traceID, "step_id": step.ID, "tool": step.Tool, "errors": []string{accept.Reason}})
		if r.Observer != nil {
			r.Observer.ToolDone(traceID, tr)
		}
		return tr, "", consumed
	}
	if accept.Status == AcceptanceUsable {
		tr.OK = true
		tr.Error = ""
		r.Logger.Event("runtime.execution_usable", map[string]any{"trace_id": traceID, "step_id": step.ID, "tool": step.Tool, "reason": accept.Reason})
	}
	if accept.Status == AcceptanceSuspect {
		tr.OK = false
		tr.Error = "step_acceptance_suspect"
		if accept.Reason != "" {
			tr.Output = strings.TrimSpace(tr.Output + "\n\nacceptance suspect:\n" + accept.Reason)
		}
		r.Logger.Event("runtime.execution_drift", map[string]any{"trace_id": traceID, "step_id": step.ID, "tool": step.Tool, "errors": []string{accept.Reason}})
		if r.Observer != nil {
			r.Observer.ToolDone(traceID, tr)
		}
		return tr, "", consumed
	}
	r.Logger.Event("runtime.tool_done", map[string]any{"trace_id": traceID, "step_id": step.ID, "tool": step.Tool, "ok": tr.OK, "error": tr.Error, "output_chars": len(tr.Output), "evidence": tr.Evidence})
	if r.Observer != nil {
		r.Observer.ToolDone(traceID, tr)
	}
	return tr, "", consumed
}

func (r Runtime) ExecutePlanForEval(ctx context.Context, traceID string, plan model.Plan, approvalGranted bool, approvedStepID string) ([]model.ToolResult, string) {
	return r.executePlan(ctx, traceID, plan, approvalGranted, approvedStepID, nil, nil)
}

func reusableResultsMap(previousSteps map[string]session.StepState, previousResults []model.ToolResult) map[string]model.ToolResult {
	out := map[string]model.ToolResult{}
	for id, prev := range previousSteps {
		if prev.Status != "passed" && prev.Status != "usable" {
			continue
		}
		out[id] = model.ToolResult{
			StepID:   prev.ID,
			Tool:     prev.Tool,
			OK:       prev.ResultOK,
			Output:   prev.ResultSummary,
			Evidence: prev.Evidence,
			Error:    prev.ResultError,
		}
	}
	for _, prev := range previousResults {
		if !prev.OK {
			continue
		}
		out[strings.TrimSpace(prev.StepID)] = prev
	}
	return out
}

func reuseStepResult(step model.PlanStep, reusable map[string]model.ToolResult) (model.ToolResult, bool) {
	if reusable == nil {
		return model.ToolResult{}, false
	}
	id := strings.TrimSpace(step.ID)
	if id == "" {
		return model.ToolResult{}, false
	}
	prev, ok := reusable[id]
	if !ok {
		return model.ToolResult{}, false
	}
	if strings.TrimSpace(prev.Tool) != strings.TrimSpace(step.Tool) || !prev.OK {
		return model.ToolResult{}, false
	}
	if !stepReusable(step, prev) {
		return model.ToolResult{}, false
	}
	return prev, true
}

func stepReusable(step model.PlanStep, result model.ToolResult) bool {
	switch strings.TrimSpace(step.Tool) {
	case "time.now":
		return false
	}
	return len(result.Evidence) > 0
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
		FinalAcceptStatus: string(AcceptanceRejected),
		FinalAcceptReason: err.Error(),
	}
}

func userFacingError(err error) string {
	if err == nil {
		return "任务失败了，我已经停在安全位置。"
	}
	text := err.Error()
	lower := strings.ToLower(text)
	if strings.Contains(lower, "insufficient tool evidence") {
		return "我没有拿到足够的工具证据，所以先停下，避免给出没有依据的结论。你可以稍后重试，或补充可用来源/文件路径后让我继续。"
	}
	if strings.Contains(lower, "plan contract verification") {
		return "我生成的执行计划没有通过合同校验，已经尝试自动修复一次，但仍缺少必要工具参数、依赖或证据设计，所以先停下。"
	}
	if strings.Contains(lower, "unexpected eof") ||
		strings.Contains(lower, "model request") ||
		strings.Contains(lower, "api.minimaxi.com") ||
		strings.Contains(lower, "/anthropic/") ||
		strings.Contains(lower, "connection reset") ||
		strings.Contains(lower, "timeout") ||
		strings.Contains(lower, "context deadline exceeded") {
		return "模型服务请求临时失败，所以任务没有继续。稍后可以重试；如果一直出现，再看 trace 日志。"
	}
	return "任务失败了，我已经停在安全位置。可以查看报告或 trace 了解细节。"
}

func confirmPromptForStep(step model.PlanStep, args map[string]string) string {
	switch step.Tool {
	case "shell.run", "terminal.run":
		command := strings.TrimSpace(args["command"])
		if command != "" {
			return "这个命令可能会修改或删除本地内容，执行前需要你确认。\n\n命令：`" + command + "`\n\n回复“确认”继续执行，或回复“取消”放弃。"
		}
	case "file.write", "file.patch":
		path := strings.TrimSpace(args["path"])
		if path != "" {
			return "这个文件操作会修改本地文件，执行前需要你确认。\n\n文件：" + path + "\n\n回复“确认”继续执行，或回复“取消”放弃。"
		}
	case "skill.install":
		name := firstNonEmpty(args["name"], args["url"], args["query"])
		if name != "" {
			text := "这个操作会安装一个 agent skill，执行前需要你确认。\n\n技能：" + name
			if source := strings.TrimSpace(args["url"]); source != "" {
				text += "\n来源：" + source
			}
			return text + "\n目标：Mateway workspace 的 skills 目录\n\n回复“确认”继续执行，或回复“取消”放弃。"
		}
	case "software.install":
		command := strings.TrimSpace(args["command"])
		verify := strings.TrimSpace(args["verify_command"])
		if verify == "" {
			verify = softwareInstallVerifyCommand(args["executable"])
		}
		if command != "" {
			text := "这个安装操作会修改本地环境，执行前需要你确认。\n\n安装命令：`" + command + "`"
			if verify != "" {
				text += "\n验证命令：`" + verify + "`"
			}
			if source := strings.TrimSpace(args["source_url"]); source != "" {
				text += "\n来源：" + source
			}
			return text + "\n\n回复“确认”继续执行，或回复“取消”放弃。"
		}
	}
	goal := strings.TrimSpace(step.Goal)
	if goal == "" {
		goal = step.Tool
	}
	return "这一步需要你确认后我才能继续。\n\n操作：" + goal + "\n\n回复“确认”继续执行，或回复“取消”放弃。"
}

func softwareInstallVerifyCommand(executable string) string {
	executable = strings.TrimSpace(executable)
	if executable == "" {
		return ""
	}
	quoted := "'" + strings.ReplaceAll(executable, "'", "'\\''") + "'"
	return "command -v " + quoted + " && " + quoted + " --version"
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

func needsGroundingEvidence(user string, results []model.ToolResult) bool {
	if !requiresGroundingEvidence(user) {
		return false
	}
	return !hasGroundingEvidence(results)
}

func requiresGroundingEvidence(user string) bool {
	normalized := normalizeIntentText(user)
	if normalized == "" {
		return false
	}
	hasLocalSubject := strings.Contains(normalized, "mateway") ||
		textmatch.ContainsGroup(normalized, "project_subject")
	hasKnowledgeAction := textmatch.ContainsGroup(normalized, "project_action")
	if hasLocalSubject && hasKnowledgeAction {
		return true
	}
	if strings.Contains(normalized, "文件") || strings.Contains(normalized, "文档") || strings.Contains(normalized, "readme") {
		return strings.Contains(normalized, "总结") || strings.Contains(normalized, "读取") || strings.Contains(normalized, "内容")
	}
	if strings.Contains(normalized, "安装") || strings.Contains(normalized, "install") {
		return true
	}
	if strings.Contains(normalized, "最新") || strings.Contains(normalized, "current") || strings.Contains(normalized, "today") {
		return true
	}
	return false
}

func hasGroundingEvidence(results []model.ToolResult) bool {
	for _, result := range results {
		if !result.OK {
			continue
		}
		switch strings.TrimSpace(result.Tool) {
		case "file.read", "file.summary", "project.index", "web.search", "web.fetch", "software.search", "skill.search", "software.install", "skill.install", "terminal.run":
			return true
		}
		if kind, _ := result.Evidence["kind"].(string); groundingEvidenceKind(kind) {
			return true
		}
	}
	return false
}

func groundingEvidenceKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case "file_read", "file_summary", "project_index", "web_search", "web_fetch", "software_search", "skill_search", "software_install", "skill_install", "terminal", "shell", "memory_search", "memory_index", "schedule_create", "schedule_list", "schedule_show", "schedule_pause", "schedule_resume", "schedule_update", "schedule_delete":
		return true
	default:
		return false
	}
}

func repairReasonFromResults(results []model.ToolResult) string {
	for _, result := range results {
		if result.OK {
			continue
		}
		if result.Error != "" {
			return result.Error
		}
		if strings.TrimSpace(result.Output) != "" {
			return strings.TrimSpace(result.Output)
		}
	}
	return ""
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
					Kind:      firstNonEmpty(stringValue(evidence["kind"]), "file"),
					Path:      path,
					StartLine: intValue(evidence["start_line"]),
					EndLine:   intValue(evidence["end_line"]),
					Label:     result.Tool,
					Summary:   shortenReply(result.Output, 180),
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

func intValue(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	default:
		return 0
	}
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
