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
	"github.com/dongping/mateway/internal/schedule"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/skill"
	"github.com/dongping/mateway/internal/textmatch"
	"github.com/dongping/mateway/internal/tool"
)

type Runtime struct {
	Config       *config.Root
	Model        model.Planner
	Tools        *tool.Registry
	Skills       *skill.Registry
	Sanitizer    ResponseSanitizer
	Logger       observer.Logger
	ToolCtx      tool.Context
	MaxSteps     int
	Observer     Observer
	Sessions     session.Store
	Memory       memory.Store
	Acceptors    *AcceptanceRegistry
	AllowedTools map[string]bool
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

func (r Runtime) WithAllowedTools(names []string) Runtime {
	allowed := map[string]bool{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" {
			allowed[name] = true
		}
	}
	if len(allowed) == 0 {
		r.AllowedTools = nil
		return r
	}
	r.AllowedTools = allowed
	return r
}

func (r Runtime) WithSchedulePolicy(task schedule.Task) schedule.Handler {
	return func(ctx context.Context, msg channel.InboundMessage) (schedule.Response, error) {
		resp, err := r.WithAllowedTools(task.AllowedTools).Handle(ctx, msg)
		return schedule.Response{
			Reply:             resp.Reply,
			TraceID:           resp.TraceID,
			Failed:            resp.Failed,
			AwaitConfirm:      resp.AwaitConfirm,
			AwaitUserInput:    resp.AwaitUserInput,
			FinalAcceptStatus: resp.FinalAcceptStatus,
			FinalAcceptReason: resp.FinalAcceptReason,
		}, err
	}
}

func (r Runtime) currentSkills() *skill.Registry {
	if strings.TrimSpace(r.ToolCtx.Workspace) != "" {
		if reg, err := skill.LoadRegistry(r.ToolCtx.Workspace, "main"); err == nil && reg != nil {
			return reg
		}
	}
	return r.Skills
}

func (r Runtime) availableToolDefinitions() []tool.Definition {
	defs := r.Tools.Definitions()
	if len(r.AllowedTools) == 0 {
		return defs
	}
	out := make([]tool.Definition, 0, len(defs))
	for _, def := range defs {
		if r.AllowedTools[def.Name] {
			out = append(out, def)
		}
	}
	return out
}

func (r Runtime) toolAllowed(name string) bool {
	if len(r.AllowedTools) == 0 {
		return true
	}
	return r.AllowedTools[strings.TrimSpace(name)]
}

func (r Runtime) executePlan(ctx context.Context, traceID string, plan model.Plan, approvalGranted bool, approvedStepID string, previousSteps map[string]session.StepState, previousResults []model.ToolResult) ([]model.ToolResult, string) {
	var results []model.ToolResult
	approvalConsumed := false
	steps := plan.Steps
	if r.MaxSteps > 0 && len(steps) > r.MaxSteps {
		steps = steps[:r.MaxSteps]
	}
	reuseAllowed := true
	for _, prev := range previousSteps {
		if strings.TrimSpace(prev.Status) == "failed" {
			reuseAllowed = false
			break
		}
	}
	completed := reusableResultsMap(previousSteps, previousResults, reuseAllowed)
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
		if reused, ok := reuseStepResult(steps[i], completed, r.Tools); ok {
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
		if !ok || !dependencyResultSatisfied(result) {
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
		if ok && !dependencyResultSatisfied(result) {
			return dep
		}
	}
	return ""
}

func dependencyResultSatisfied(result model.ToolResult) bool {
	if result.OK {
		return true
	}
	return strings.TrimSpace(result.Error) == "" && len(result.Evidence) > 0
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
	if !r.toolAllowed(step.Tool) {
		tr := model.ToolResult{
			StepID: step.ID,
			Tool:   step.Tool,
			OK:     false,
			Error:  "tool_not_allowed",
			Output: "tool is not allowed for this scheduled task: " + step.Tool,
			Evidence: map[string]any{
				"kind": "tool_policy",
				"tool": step.Tool,
			},
		}
		r.Logger.Event("runtime.tool_done", map[string]any{"trace_id": traceID, "step_id": step.ID, "tool": step.Tool, "ok": false, "error": tr.Error})
		if r.Observer != nil {
			r.Observer.ToolDone(traceID, tr)
		}
		return tr, "", consumed
	}
	args := copyArgs(step.Args)
	delete(args, "confirmed")
	delete(args, "confirm")
	needsConfirm := tool.RequireConfirmForTool(step.Tool, args) || terminalStepRequiresExternalWriteConfirm(step.Tool, args)
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

func reusableResultsMap(previousSteps map[string]session.StepState, previousResults []model.ToolResult, allowReuse bool) map[string]model.ToolResult {
	out := map[string]model.ToolResult{}
	if !allowReuse {
		return out
	}
	allowConfirmedMutationReuse := hasPendingConfirmationStep(previousSteps)
	for id, prev := range previousSteps {
		if prev.Status != "passed" && prev.Status != "usable" {
			continue
		}
		if confirmedMutationReusable(prev.Tool) && !allowConfirmedMutationReuse {
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

func hasPendingConfirmationStep(steps map[string]session.StepState) bool {
	for _, step := range steps {
		if strings.TrimSpace(step.Status) == "blocked" && strings.TrimSpace(step.ResultError) == "await_confirm" {
			return true
		}
		if strings.TrimSpace(step.AcceptanceStatus) == "await_confirm" {
			return true
		}
	}
	return false
}

func reuseStepResult(step model.PlanStep, reusable map[string]model.ToolResult, registry *tool.Registry) (model.ToolResult, bool) {
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
	if !stepReusable(step, prev, registry) {
		return model.ToolResult{}, false
	}
	return prev, true
}

func stepReusable(step model.PlanStep, result model.ToolResult, registry *tool.Registry) bool {
	if confirmedMutationReusable(result.Tool) {
		return result.OK && len(result.Evidence) > 0
	}
	if registry != nil {
		if def, ok := registry.Get(strings.TrimSpace(step.Tool)); ok {
			if def.Metadata.ReusePolicy == tool.ReuseStableRead {
				return len(result.Evidence) > 0
			}
			if def.Metadata.ReusePolicy == "" || def.Metadata.ReusePolicy == tool.ReuseNever {
				return stableReadEvidence(result.Evidence)
			}
			return false
		}
	}
	return stableReadEvidence(result.Evidence)
}

func confirmedMutationReusable(toolName string) bool {
	toolName = strings.TrimSpace(toolName)
	return toolName == "file.write" || strings.HasPrefix(toolName, "schedule.")
}

func stableReadEvidence(evidence map[string]any) bool {
	if len(evidence) == 0 {
		return false
	}
	kind, _ := evidence["kind"].(string)
	switch strings.TrimSpace(kind) {
	case "file_read", "file_summary", "project_index", "web_search", "web_fetch", "memory_search", "memory_index":
		return true
	default:
		return false
	}
}

func (r Runtime) failure(msg channel.InboundMessage, plan *model.Plan, results []model.ToolResult, err error) Response {
	var p model.Plan
	if plan != nil {
		p = *plan
	}
	text := userFacingError(err)
	if preflight := userFacingTerminalPreconditionMessage(results); preflight != "" {
		text = preflight
	} else if planVerification := userFacingPlanVerificationMessage(results); planVerification != "" {
		text = planVerification
	}
	return Response{
		Reply:   r.sanitizeReply(channel.OutboundMessage{Channel: msg.Channel, ThreadID: msg.ThreadID, Text: text, Style: "error"}),
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

func userFacingPlanVerificationMessage(results []model.ToolResult) string {
	var errors []string
	for _, result := range results {
		if strings.TrimSpace(result.Tool) != "plan.verify" {
			continue
		}
		errors = append(errors, stringListValue(result.Evidence["errors"])...)
		if strings.TrimSpace(result.Output) != "" && len(errors) == 0 {
			errors = append(errors, strings.Split(strings.TrimSpace(result.Output), "\n")...)
		}
	}
	if len(errors) == 0 {
		return ""
	}
	diagnostics := planVerificationDiagnostics(results)
	if len(diagnostics) == 0 && hasToolResult(results, "terminal.run") {
		diagnostics = append(diagnostics, "已有本地命令步骤尝试过，但没有得到足够明确的命令诊断输出。")
	}
	if len(diagnostics) == 0 {
		diagnostics = append(diagnostics, "还没执行到本地命令，所以目前不能判断是命令不存在、参数错误，还是认证未配置。")
	}
	reasons := planVerificationReasons(errors)
	if len(reasons) == 0 && len(diagnostics) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("这次还没有执行发送命令，失败点在执行前的计划检查。")
	if len(diagnostics) > 0 {
		b.WriteString("\n\n已确认的情况：")
		for _, item := range diagnostics {
			b.WriteString("\n- " + item)
		}
	}
	if len(reasons) > 0 {
		b.WriteString("\n\n还缺：")
	}
	for _, reason := range reasons {
		b.WriteString("\n- " + reason)
	}
	b.WriteString("\n\n所以我停下了，避免用没确认过的命令或缺参数的命令去真实发送。")
	return b.String()
}

func hasToolResult(results []model.ToolResult, toolName string) bool {
	for _, result := range results {
		if strings.TrimSpace(result.Tool) == toolName {
			return true
		}
	}
	return false
}

func planVerificationDiagnostics(results []model.ToolResult) []string {
	var diagnostics []string
	add := func(text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		for _, existing := range diagnostics {
			if existing == text {
				return
			}
		}
		diagnostics = append(diagnostics, text)
	}
	for _, result := range results {
		if strings.TrimSpace(result.Tool) != "terminal.run" {
			continue
		}
		command := strings.TrimSpace(stringValue(result.Evidence["command"]))
		output := strings.TrimSpace(result.Output)
		lowerOutput := strings.ToLower(output)
		executable := commandVDiagnosticExecutable(command)
		displayName := "`" + firstNonEmpty(executable, commandRoot(command), "CLI") + "`"
		switch {
		case executable != "" && (strings.Contains(lowerOutput, "not_found") || strings.Contains(lowerOutput, "not found") || commandVDiagnosticLooksMissing(command, result)):
			add("本机没有找到用户写的 " + displayName + " 命令。")
		case executable != "" && output != "":
			add("已检查用户写的 " + displayName + " 命令，路径/版本输出：" + compactText(output, 120))
		case command != "" && strings.Contains(strings.ToLower(result.Error+"\n"+result.Output), "not configured"):
			add(displayName + " 返回未配置/未认证。")
		case command != "" && (strings.Contains(strings.ToLower(result.Error), "exit status 127") || strings.Contains(lowerOutput, "command not found")):
			add("命令执行返回 127，通常表示命令不存在或 PATH 找不到。")
		}
	}
	if len(diagnostics) > 3 {
		return diagnostics[:3]
	}
	return diagnostics
}

func planVerificationReasons(errors []string) []string {
	var reasons []string
	add := func(text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		for _, existing := range reasons {
			if existing == text {
				return
			}
		}
		reasons = append(reasons, text)
	}
	for _, item := range errors {
		lower := strings.ToLower(strings.TrimSpace(item))
		switch {
		case strings.Contains(lower, "first inspect the exact help or usage"):
			add("还没有拿到“发送消息”子命令的精确 help / usage，所以不知道正确子命令和参数名。")
		case strings.Contains(lower, "without an earlier read-only preflight"):
			add("真实发送前还缺只读预检，例如 auth/profile/status/whoami 或 dry-run。")
		case strings.Contains(lower, "rewritten executable name before evidence") || strings.Contains(lower, "different executable name than the user provided"):
			add("计划里曾尝试把用户写的命令名改成另一个命令名，但还没有足够证据允许这样切换。")
		case strings.Contains(lower, "first check the exact user-provided executable"):
			add("需要先检查用户写的原始命令名是否存在；如果不存在，要明确告诉用户。")
		case strings.Contains(lower, "missing chat/user id"):
			add("还缺接收人或 chat/user id。")
		case strings.Contains(lower, "explicit message content"):
			add("还缺要发送的消息内容。")
		case strings.Contains(lower, "auth/config preflight"):
			add("还缺认证/配置预检。建议先执行 `<cli> auth list`、`<cli> profile list`、`<cli> status`、`<cli> whoami` 中该工具支持的一条；不确定哪条可用时，先执行 `<cli> --help` 或 `<cli> auth --help`。")
		case strings.Contains(lower, "unresolved placeholder"):
			add("计划里还有占位参数，必须替换成真实命令、真实参数或先向用户确认。")
		}
	}
	if len(reasons) > 4 {
		return reasons[:4]
	}
	return reasons
}

func stringListValue(v any) []string {
	switch values := v.(type) {
	case []string:
		return values
	case []any:
		out := make([]string, 0, len(values))
		for _, item := range values {
			if text := strings.TrimSpace(fmt.Sprint(item)); text != "" {
				out = append(out, text)
			}
		}
		return out
	case string:
		if strings.TrimSpace(values) == "" {
			return nil
		}
		return strings.Split(strings.TrimSpace(values), "\n")
	default:
		return nil
	}
}

func confirmPromptForStep(step model.PlanStep, args map[string]string) string {
	switch step.Tool {
	case "shell.run", "terminal.run":
		command := strings.TrimSpace(args["command"])
		if command != "" {
			if terminalStepRequiresExternalWriteConfirm(step.Tool, args) && !tool.IsDangerousCommand(command) {
				return "这个命令可能会修改外部系统或发送消息，执行前需要你确认。\n\n命令：`" + command + "`\n\n回复“确认”继续执行，或回复“取消”放弃。"
			}
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
	case "memory.commit":
		proposal := firstNonEmpty(args["proposal"], args["id"], args["path"])
		kind := strings.TrimSpace(args["type"])
		target := strings.TrimSpace(args["target_hint"])
		if proposal != "" {
			text := "这条长期记忆提交可能会影响后续默认记忆注入，执行前需要你确认。\n\nProposal：" + proposal
			if kind != "" {
				text += "\n类型：" + kind
			}
			if target != "" {
				text += "\n推荐落点：" + target
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

func userFacingTerminalPreconditionMessage(results []model.ToolResult) string {
	for _, result := range results {
		if strings.TrimSpace(result.Tool) != "terminal.run" {
			continue
		}
		command := strings.TrimSpace(stringValue(result.Evidence["command"]))
		executable := commandRoot(command)
		if executable == "" {
			executable = "CLI"
		}
		displayName := "`" + executable + "`"
		text := strings.ToLower(strings.TrimSpace(result.Output + "\n" + result.Error))
		if strings.Contains(text, "command not found") || strings.Contains(text, "executable file not found") || strings.Contains(text, "no such file or directory") || strings.Contains(text, "exit status 127") || terminalOutputLooksNotFound(result.Output) || commandVDiagnosticLooksMissing(command, result) {
			return "我先做了命令前置检查，结果发现本机找不到 " + displayName + " 这个命令。\n\n下一步应该主动查官方/可信来源，确认正确的 canonical executable；确认后再检查本机是否安装。"
		}
		if strings.Contains(text, "not configured") {
			message := "我先做了命令前置检查，结果发现当前 " + displayName + " 还没有完成配置/认证，所以现在不适合继续执行真实操作。"
			if hint := terminalPreconditionHint(result.Output); hint != "" {
				return message + "\n\n工具输出提示：" + hint + "\n\n请先完成认证/配置，完成后我再继续测试。"
			}
			return message + "\n\n可以先执行这些只读命令确认该工具支持哪种认证方式：\n- `" + executable + " --help`\n- `" + executable + " auth --help`\n- `" + executable + " auth list`\n- `" + executable + " profile list`\n\n如果你愿意，我也可以先帮你执行 help/auth 预检，再根据输出继续。"
		}
		if strings.Contains(text, "unauthorized") || strings.Contains(text, "authentication") || strings.Contains(text, "permission denied") {
			return "我先做了命令前置检查，结果显示当前 " + displayName + " 认证或权限还没准备好，所以现在不适合继续执行真实操作。\n\n可以先执行：\n- `" + executable + " --help`\n- `" + executable + " auth --help`\n- `" + executable + " auth list`\n\n确认登录/授权命令后，我可以继续帮你跑只读预检。"
		}
	}
	return ""
}

func terminalFailureRepairGuidance(results []model.ToolResult) string {
	var parts []string
	for _, result := range results {
		if strings.TrimSpace(result.Tool) != "terminal.run" || result.OK {
			continue
		}
		command := strings.TrimSpace(stringValue(result.Evidence["command"]))
		executable := firstNonEmpty(commandRoot(command), "the CLI")
		text := strings.ToLower(strings.TrimSpace(result.Output + "\n" + result.Error))
		switch {
		case strings.Contains(text, "command not found") ||
			strings.Contains(text, "executable file not found") ||
			strings.Contains(text, "no such file or directory") ||
			strings.Contains(text, "exit status 127") ||
			terminalOutputLooksNotFound(result.Output) ||
			commandVDiagnosticLooksMissing(command, result):
			parts = append(parts, "The local executable `"+executable+"` appears missing. Do not stop at asking the user to spell it; use software.search or official web evidence to find the canonical executable name, then check command -v for the confirmed name. Installing still requires confirmation.")
		case strings.Contains(text, "unknown command") ||
			strings.Contains(text, "unknown subcommand") ||
			strings.Contains(text, "unknown flag") ||
			strings.Contains(text, "invalid option") ||
			strings.Contains(text, "unrecognized option"):
			parts = append(parts, "The CLI syntax appears wrong. First use read-only parent help such as `"+executable+" --help` or the nearest subcommand help to correct the command/flag. If local help conflicts with the task or is insufficient, fetch official docs/README before trying another write command.")
		case strings.Contains(text, "missing required") ||
			strings.Contains(text, "required flag") ||
			strings.Contains(text, "requires an argument") ||
			strings.Contains(text, "missing argument"):
			parts = append(parts, "The CLI is missing required arguments. Use local help to identify the exact required target/content flags, then use user.ask for facts only the user can provide. Do not invent IDs, tokens, recipients, or message content.")
		case strings.Contains(text, "unauthorized") ||
			strings.Contains(text, "authentication") ||
			strings.Contains(text, "not logged in") ||
			strings.Contains(text, "token expired") ||
			strings.Contains(text, "not configured"):
			parts = append(parts, "The command failed on authentication/configuration. Prefer local read-only diagnostics first, such as `"+executable+" auth --help`, `"+executable+" status`, or `"+executable+" whoami` if supported. If the login/config flow is unclear, use official docs/README. Do not run login, write config, or create tokens without user confirmation.")
		case strings.Contains(text, "permission denied") ||
			strings.Contains(text, "forbidden") ||
			strings.Contains(text, "insufficient scope") ||
			strings.Contains(text, "missing scope"):
			parts = append(parts, "The command failed on permissions. Explain that the app/bot/user may lack scope or target access. If the required scope is unclear, consult official permission docs. Do not attempt admin, permission, or token changes without user confirmation.")
		case strings.Contains(text, "version") ||
			strings.Contains(text, "deprecated") ||
			strings.Contains(text, "unsupported"):
			parts = append(parts, "The failure may be version-related. Run read-only `"+executable+" --version` and compare with official README/release notes if needed. Upgrades or installs require confirmation.")
		case strings.Contains(text, "timeout") ||
			strings.Contains(text, "timed out") ||
			strings.Contains(text, "network") ||
			strings.Contains(text, "connection refused") ||
			strings.Contains(text, "connection reset"):
			parts = append(parts, "The failure looks network or service related. Give the user the exact error summary, then use only read-only retry/status diagnostics unless a write retry is explicitly confirmed.")
		default:
			parts = append(parts, "The terminal failure is not locally classifiable from stdout/stderr. Use software.search/web.fetch against official docs, README, issues, or release notes to identify the error before proposing another write command.")
		}
	}
	return strings.Join(dedupeStrings(parts), "\n")
}

func terminalStepRequiresExternalWriteConfirm(toolName string, args map[string]string) bool {
	if strings.TrimSpace(toolName) != "terminal.run" && strings.TrimSpace(toolName) != "shell.run" {
		return false
	}
	command := strings.TrimSpace(args["command"])
	if terminalCommandLooksCLIReadinessPreflight(command) {
		return false
	}
	return terminalCommandLooksExternalWriteAction(command)
}

func repairedTerminalWriteStepCoveredByApproval(results []model.ToolResult, plan model.Plan, approvalGranted bool) string {
	if !approvalGranted {
		return ""
	}
	var approvedFailedCommands []string
	for _, result := range results {
		if result.OK || strings.TrimSpace(result.Tool) != "terminal.run" {
			continue
		}
		command := strings.TrimSpace(stringValue(result.Evidence["command"]))
		if terminalCommandLooksExternalWriteAction(command) {
			approvedFailedCommands = append(approvedFailedCommands, command)
		}
	}
	if len(approvedFailedCommands) == 0 {
		return ""
	}
	for _, step := range plan.Steps {
		if strings.TrimSpace(step.Tool) != "terminal.run" {
			continue
		}
		next := strings.TrimSpace(step.Args["command"])
		if !terminalCommandLooksExternalWriteAction(next) {
			continue
		}
		for _, prev := range approvedFailedCommands {
			if terminalWriteCommandsShareApprovalBoundary(prev, next) {
				return strings.TrimSpace(step.ID)
			}
		}
	}
	return ""
}

func terminalWriteCommandsShareApprovalBoundary(prev, next string) bool {
	prev = strings.TrimSpace(prev)
	next = strings.TrimSpace(next)
	if prev == "" || next == "" || commandRoot(prev) != commandRoot(next) {
		return false
	}
	if terminalWriteActionKind(prev) == "" || terminalWriteActionKind(prev) != terminalWriteActionKind(next) {
		return false
	}
	for _, flag := range []string{"chat-id", "chat_id", "receive-id", "receive_id", "user-id", "user_id", "open-id", "open_id"} {
		prevValue := commandFlagValue(prev, flag)
		nextValue := commandFlagValue(next, flag)
		if prevValue == "" || nextValue == "" {
			continue
		}
		return prevValue == nextValue
	}
	return false
}

func terminalWriteActionKind(command string) string {
	lower := strings.ToLower(strings.TrimSpace(command))
	for _, kind := range []string{"send", "reply", "create", "update", "delete", "remove", "install", "publish", "deploy", "upload", "apply", "write", "patch", "commit"} {
		if strings.Contains(lower, " "+kind) || strings.Contains(lower, "-"+kind) || strings.Contains(lower, "+"+kind) {
			return kind
		}
	}
	return ""
}

func commandFlagValue(command, flag string) string {
	pattern := regexp.MustCompile(`(?:^|\s)--` + regexp.QuoteMeta(flag) + `(?:=|\s+)(?:"([^"]+)"|'([^']+)'|([^\s]+))`)
	match := pattern.FindStringSubmatch(command)
	if len(match) == 0 {
		return ""
	}
	for _, value := range match[1:] {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func commandVDiagnosticExecutable(command string) string {
	if !strings.HasPrefix(strings.TrimSpace(command), "command -v ") {
		return ""
	}
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) < 3 {
		return ""
	}
	return strings.Trim(strings.TrimSpace(fields[2]), `'"`)
}

func terminalOutputLooksNotFound(output string) bool {
	normalized := strings.ToLower(strings.TrimSpace(output))
	return normalized == "not_found" ||
		normalized == "not found" ||
		normalized == "not-found" ||
		normalized == "notfound"
}

func commandVDiagnosticLooksMissing(command string, result model.ToolResult) bool {
	if !strings.HasPrefix(strings.TrimSpace(command), "command -v ") {
		return false
	}
	stdout := strings.TrimSpace(stringValue(result.Evidence["stdout"]))
	if stdout != "" {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(result.Output + "\n" + result.Error))
	if strings.Contains(text, "exit status") {
		return true
	}
	exitCode := strings.TrimSpace(fmt.Sprint(result.Evidence["exit_code"]))
	return exitCode != "" && exitCode != "0" && exitCode != "<nil>"
}

func terminalPreconditionHint(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if strings.Contains(lower, "hint:") || strings.Contains(lower, "run `") || strings.Contains(lower, "please run") {
			return line
		}
	}
	return ""
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
