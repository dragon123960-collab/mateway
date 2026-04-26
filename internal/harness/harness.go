package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dongping/mateway/internal/agents"
	"github.com/dongping/mateway/internal/capabilities"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/memory"
	"github.com/dongping/mateway/internal/reflection"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/skills"
	"github.com/dongping/mateway/internal/textutil"
	"github.com/dongping/mateway/internal/tools"
)

var ErrSessionBusy = errors.New("session already processing a turn")

type HistoryMessage struct {
	Role    string
	Content string
}

type Generator func(ctx context.Context, history []HistoryMessage, userText string) (string, error)

type Request struct {
	SessionKey   string
	ThreadID     string
	UserID       string
	Channel      string
	AgentName    string
	UserText     string
	Mode         string
	ToolName     string
	Arguments    map[string]any
	ParentRunID  string
	SkipApproval bool
	Capabilities capabilities.Effective
}

type Run struct {
	ID                string                 `json:"id"`
	SessionKey        string                 `json:"session_key"`
	ThreadID          string                 `json:"thread_id,omitempty"`
	AgentName         string                 `json:"agent_name,omitempty"`
	Goal              string                 `json:"goal,omitempty"`
	Mode              string                 `json:"mode,omitempty"`
	Route             string                 `json:"route,omitempty"`
	ModelName         string                 `json:"model_name,omitempty"`
	CollaborationMode string                 `json:"collaboration_mode,omitempty"`
	ToolName          string                 `json:"tool_name,omitempty"`
	Status            string                 `json:"status"`
	Result            string                 `json:"result,omitempty"`
	Error             string                 `json:"error,omitempty"`
	ParentRunID       string                 `json:"parent_run_id,omitempty"`
	ChildRunIDs       []string               `json:"child_run_ids,omitempty"`
	LastChildRunID    string                 `json:"last_child_run_id,omitempty"`
	LastChildStatus   string                 `json:"last_child_status,omitempty"`
	LastChildResult   string                 `json:"last_child_result,omitempty"`
	LastApprovalID    string                 `json:"last_approval_id,omitempty"`
	ApprovalIDs       []string               `json:"approval_ids,omitempty"`
	Steps             []RunStep              `json:"steps,omitempty"`
	CreatedAt         time.Time              `json:"created_at"`
	UpdatedAt         time.Time              `json:"updated_at"`
	Capabilities      capabilities.Effective `json:"capabilities,omitempty"`
	VisibleTools      []string               `json:"visible_tools,omitempty"`
	SelectedSkills    []string               `json:"selected_skills,omitempty"`
	SkillPickerSource string                 `json:"skill_picker_source,omitempty"`
}

type RunStep struct {
	Index      int       `json:"index"`
	Kind       string    `json:"kind"`
	Status     string    `json:"status"`
	AgentName  string    `json:"agent_name,omitempty"`
	ToolName   string    `json:"tool_name,omitempty"`
	Input      string    `json:"input,omitempty"`
	Output     string    `json:"output,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

type Harness struct {
	Workspace      string
	Sessions       *session.Store
	Tools          *tools.Registry
	Memory         memory.Store
	HistoryLimit   int
	ApprovalPolicy ApprovalPolicy
	Config         config.Config
	EnableEino     bool
	SkillCatalog   *skills.Catalog

	mu       sync.RWMutex
	runs     map[string]Run
	inflight sync.Map
	approvalState
}

func (h *Harness) UseEinoRuntime(cfg config.Config) {
	h.Config = cfg
	h.EnableEino = true
}

func New(workspace string, sessions *session.Store, registry *tools.Registry, historyLimit int) *Harness {
	return &Harness{
		Workspace:    workspace,
		Sessions:     sessions,
		Tools:        registry,
		Memory:       memory.Store{Workspace: workspace},
		runs:         make(map[string]Run),
		HistoryLimit: historyLimit,
		approvalState: approvalState{
			pendingApprovalsBySession: make(map[string][]PendingApproval),
			pendingApprovalsByID:      make(map[string]PendingApproval),
		},
	}
}

func (h *Harness) Start(ctx context.Context, req Request, generate Generator) (Run, error) {
	if strings.TrimSpace(req.AgentName) == "" && h.Sessions != nil {
		if prefs, err := h.Sessions.LoadPreferences(req.SessionKey); err == nil && strings.TrimSpace(prefs.AgentName) != "" {
			req.AgentName = prefs.AgentName
		}
	}
	effective := req.Capabilities
	if !hasCapabilities(effective) {
		var err error
		effective, err = h.compileCapabilities(ctx, tools.Scope{
			UserID:    req.UserID,
			Channel:   req.Channel,
			ThreadID:  firstNonEmpty(req.ThreadID, req.SessionKey),
			AgentName: firstNonEmpty(req.AgentName, "default"),
		})
		if err != nil {
			return Run{}, err
		}
	}
	run := Run{
		ID:                fmt.Sprintf("run_%d", time.Now().UnixNano()),
		SessionKey:        strings.TrimSpace(req.SessionKey),
		ThreadID:          strings.TrimSpace(firstNonEmpty(req.ThreadID, req.SessionKey)),
		AgentName:         firstNonEmpty(req.AgentName, "default"),
		Goal:              strings.TrimSpace(req.UserText),
		Mode:              firstNonEmpty(req.Mode, "chat"),
		CollaborationMode: firstNonEmpty(argumentString(req.Arguments, "collaboration_mode"), "coordinator"),
		ToolName:          strings.TrimSpace(req.ToolName),
		Status:            "running",
		ParentRunID:       strings.TrimSpace(req.ParentRunID),
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
		Capabilities:      effective,
	}
	if run.Mode == "chat" && h.EnableEino {
		run.Route = h.selectEinoRoute(req)
	}
	run.VisibleTools = h.visibleToolNames(ctx, req, run)
	h.saveRun(run)
	if run.Mode == "chat" {
		run = h.applySkillSelection(ctx, run)
		if step, ok := buildInitialPlanStep(req, run); ok {
			h.appendRunStep(run.ID, step)
		}
	}

	var (
		result string
		err    error
	)
	switch strings.TrimSpace(req.Mode) {
	case "", "chat":
		if !h.EnableEino {
			return Run{}, fmt.Errorf("chat runtime requires Eino; call UseEinoRuntime before starting chat runs")
		}
		result, err = h.runSessionTurn(ctx, req.SessionKey, req.UserText, func(ctx context.Context, history []HistoryMessage, userText string) (string, error) {
			return h.einoChat(ctx, req, run, history, userText)
		})
	case "tool":
		result, err = h.runTool(ctx, run, req, generate)
	default:
		err = fmt.Errorf("unsupported harness mode %q", req.Mode)
	}
	if err != nil {
		run.Status = "failed"
		run.Error = err.Error()
		run.UpdatedAt = time.Now()
		h.saveRun(run)
		return run, err
	}
	run = h.mustGetRun(run.ID)
	if run.Status == "waiting_approval" {
		run.Result = strings.TrimSpace(result)
		run.UpdatedAt = time.Now()
		h.saveRun(run)
		return run, nil
	}
	run.Status = "completed"
	run.Result = strings.TrimSpace(result)
	run.UpdatedAt = time.Now()
	h.saveRun(run)
	_ = h.refreshSessionSummary(ctx, run)
	_ = h.maybeWriteLearnProposal(ctx, run, req)
	run = h.mustGetRun(run.ID)

	_ = h.Memory.Append(ctx, "runs", req.SessionKey, fmt.Sprintf("run=%s status=%s result=%s", run.ID, run.Status, trim(run.Result, 200)), map[string]any{
		"run_id":     run.ID,
		"agent_name": run.AgentName,
		"channel":    req.Channel,
		"thread_id":  run.ThreadID,
		"task":       trim(req.UserText, 160),
	})
	_ = reflection.Append(h.Workspace, reflection.Record{
		CreatedAt: time.Now().Format(time.RFC3339),
		Type:      "harness_run",
		Status:    run.Status,
		Failure:   run.Error,
		Metadata: map[string]any{
			"run_id":        run.ID,
			"session_key":   req.SessionKey,
			"thread_id":     run.ThreadID,
			"agent_name":    run.AgentName,
			"mode":          firstNonEmpty(req.Mode, "chat"),
			"tool_name":     run.ToolName,
			"parent_run_id": run.ParentRunID,
			"route":         run.Route,
			"model_name":    run.ModelName,
			"visible_tools": len(run.VisibleTools),
			"skills":        len(run.SelectedSkills),
			"trace_stats":   buildRunTraceStats(run.Steps),
		},
	})
	return run, nil
}

func (h *Harness) maybeWriteLearnProposal(ctx context.Context, run Run, req Request) error {
	if !shouldWriteLearnProposal(run) {
		return nil
	}
	slug := "learn-proposal-" + strings.ToLower(strings.TrimSpace(run.ID))
	title := "Learn Proposal - " + trimInline(firstNonEmpty(run.Goal, run.ID), 80)
	summary := trimInline(firstNonEmpty(run.Result, run.Error, run.Goal), 180)
	contentParts := []string{
		"## Goal",
		firstNonEmpty(strings.TrimSpace(run.Goal), "(missing)"),
		"",
		"## Route",
		fmt.Sprintf("- route: %s", firstNonEmpty(run.Route, "-")),
		fmt.Sprintf("- model: %s", firstNonEmpty(run.ModelName, "-")),
	}
	if len(run.VisibleTools) > 0 {
		contentParts = append(contentParts, "", "## Visible Tools", strings.Join(run.VisibleTools, ", "))
	}
	if len(run.SelectedSkills) > 0 {
		contentParts = append(contentParts, "", "## Selected Skills", strings.Join(run.SelectedSkills, ", "))
	}
	if plan := firstRunStepByKinds(run.Steps, "dev_plan", "plan"); plan != nil {
		contentParts = append(contentParts, "", "## Initial Plan", strings.TrimSpace(plan.Output))
	}
	execution := summarizeRunExecution(run.Steps)
	if len(execution) > 0 {
		contentParts = append(contentParts, "", "## Execution Notes")
		contentParts = append(contentParts, execution...)
	}
	stats := buildRunTraceStats(run.Steps)
	if stats != (runTraceStats{}) {
		contentParts = append(contentParts, "", "## Runtime Signals",
			fmt.Sprintf("- model_calls: %d", stats.ModelCalls),
			fmt.Sprintf("- tool_calls: %d", stats.ToolCalls),
			fmt.Sprintf("- tool_searches: %d", stats.ToolSearches),
			fmt.Sprintf("- summarizations: %d", stats.Summarizations),
			fmt.Sprintf("- reductions: %d", stats.ReductionPasses),
			fmt.Sprintf("- offloaded_results: %d", stats.OffloadedResults),
			fmt.Sprintf("- transfers: %d", stats.Transfers),
		)
	}
	if strings.TrimSpace(run.Result) != "" {
		contentParts = append(contentParts, "", "## Final Output", strings.TrimSpace(run.Result))
	}
	if strings.TrimSpace(run.Error) != "" {
		contentParts = append(contentParts, "", "## Error", strings.TrimSpace(run.Error))
	}
	path, err := h.Memory.UpsertWikiPage(ctx, memory.WikiPage{
		Title:    title,
		Category: "notes",
		Slug:     slug,
		Summary:  summary,
		Content:  strings.Join(contentParts, "\n"),
		Sources: []string{
			"run:" + run.ID,
			"session:" + req.SessionKey,
		},
	})
	if err != nil {
		return err
	}
	h.appendRunStep(run.ID, RunStep{
		Kind:       "learn_proposal",
		Status:     "completed",
		AgentName:  run.AgentName,
		Output:     trim(path, 400),
		StartedAt:  time.Now(),
		FinishedAt: time.Now(),
	})
	return nil
}

func shouldWriteLearnProposal(run Run) bool {
	if run.Status != "completed" || strings.TrimSpace(run.Result) == "" {
		return false
	}
	if run.Mode != "chat" {
		return false
	}
	if strings.EqualFold(run.Route, "plan_execute") {
		return true
	}
	if shouldUsePlanExecute(run.Goal) {
		return true
	}
	if len([]rune(strings.TrimSpace(run.Goal))) >= 30 {
		return true
	}
	for _, step := range run.Steps {
		switch step.Kind {
		case "tool_choice", "tool", "tool_result", "route_fallback", "transfer":
			return true
		}
	}
	return false
}

func firstRunStepByKinds(steps []RunStep, kinds ...string) *RunStep {
	for _, kind := range kinds {
		for i := range steps {
			if strings.EqualFold(steps[i].Kind, kind) {
				return &steps[i]
			}
		}
	}
	return nil
}

func summarizeRunExecution(steps []RunStep) []string {
	lines := make([]string, 0, len(steps))
	for _, step := range steps {
		switch step.Kind {
		case "tool_choice":
			lines = append(lines, fmt.Sprintf("- choose `%s`: %s", firstNonEmpty(step.ToolName, "-"), trim(step.Output, 220)))
		case "tool_result":
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(step.ToolName)), "schedule_") {
				lines = append(lines, fmt.Sprintf("- schedule `%s`: %s", firstNonEmpty(step.ToolName, "-"), trim(step.Output, 220)))
			} else {
				lines = append(lines, fmt.Sprintf("- result `%s`: %s", firstNonEmpty(step.ToolName, "-"), trim(step.Output, 220)))
			}
		case "tool_search":
			lines = append(lines, fmt.Sprintf("- search tools: %s", trim(step.Output, 220)))
		case "tool_offload":
			lines = append(lines, fmt.Sprintf("- offload `%s`: %s", firstNonEmpty(step.ToolName, "-"), trim(step.Output, 220)))
		case "tool_reduction":
			lines = append(lines, fmt.Sprintf("- reduce context: %s", trim(step.Output, 220)))
		case "middleware_summarization":
			lines = append(lines, fmt.Sprintf("- summarize context: %s", trim(step.Output, 220)))
		case "middleware_summarization_attempt":
			lines = append(lines, fmt.Sprintf("- summarization attempt: %s", trim(step.Output, 220)))
		case "route_fallback":
			lines = append(lines, fmt.Sprintf("- fallback: %s", trim(step.Output, 220)))
		case "respond":
			lines = append(lines, fmt.Sprintf("- respond: %s", trim(step.Output, 220)))
		}
	}
	return lines
}

func trimInline(value string, max int) string {
	return textutil.CleanInline(value, max)
}

func buildInitialPlanStep(req Request, run Run) (RunStep, bool) {
	goal := strings.TrimSpace(firstNonEmpty(run.Goal, req.UserText))
	if goal == "" {
		return RunStep{}, false
	}
	lines := make([]string, 0, 8)
	lines = append(lines, "1. 明确目标与输出要求："+trim(goal, 180))
	if strings.EqualFold(run.Route, "plan_execute") {
		lines = append(lines, "2. 先生成结构化计划，再按 planner -> executor -> replanner 推进。")
	} else {
		lines = append(lines, "2. 先快速判断是否需要检索、读取历史、调用工具或直接回答。")
	}
	if hasVisibleTool(run.VisibleTools, "search_scoped_memory") || hasVisibleTool(run.VisibleTools, "wiki_query") || hasVisibleTool(run.VisibleTools, "search_history") {
		lines = append(lines, "3. 优先查历史记忆与 wiki，避免重复工作。")
	}
	if hasVisibleTool(run.VisibleTools, "web_search") {
		lines = append(lines, "4. 需要外部信息时先用 web_search；必要时再结合 browser_fetch 打开正文。")
	}
	if len(run.SelectedSkills) > 0 && hasVisibleTool(run.VisibleTools, einoSkillToolName) {
		lines = append(lines, "5. 对当前已激活的候选 skill，先用 skill 工具加载完整 SKILL.md，再按需要执行后续步骤。")
	}
	if len(run.SelectedSkills) > 0 && hasVisibleTool(run.VisibleTools, "read_skill_resource") {
		lines = append(lines, "6. 如果当前已激活 skill 且需要它的脚本/参考资料/资源文件，用 read_skill_resource 按需读取。")
	}
	if hasVisibleTool(run.VisibleTools, "opencli_run") {
		lines = append(lines, "7. 若使用外接 CLI，只能调用 provider allowlist 允许的根命令；被拒绝时回退到 web_search 或 browser_fetch。")
	}
	if hasVisibleTool(run.VisibleTools, "sandbox_exec") {
		lines = append(lines, "8. 只有在需要验证命令、脚本或最小实验时才用 sandbox_exec。")
	}
	if cliHint := buildCLIExplorationHint(goal, run.VisibleTools); strings.TrimSpace(cliHint) != "" {
		lines = append(lines, "9. 当前任务属于 CLI 学习/探查场景，应先本地检查命令帮助与版本，再决定是否回退到网页资料。")
	}
	if hasVisibleTool(run.VisibleTools, "spawn") {
		lines = append(lines, "10. 如果任务明显可并行或需要专门角色，再考虑 spawn 子 agent。")
	}
	if hasVisibleTool(run.VisibleTools, "schedule_create") || hasVisibleTool(run.VisibleTools, "schedule_list") {
		lines = append(lines, "11. 如果任务需要后续自动执行，可先查看现有 schedule，再创建或调整定时任务。")
	}
	if hasVisibleTool(run.VisibleTools, "write_file") || hasVisibleTool(run.VisibleTools, "wiki_ingest") || hasVisibleTool(run.VisibleTools, "write_memory_note") {
		lines = append(lines, "12. 完成后把结果沉淀到文件、记忆或 wiki。")
	}
	return RunStep{
		Kind:       "dev_plan",
		Status:     "completed",
		AgentName:  run.AgentName,
		Input:      trim(goal, 220),
		Output:     trim(strings.Join(lines, "\n"), 700),
		StartedAt:  time.Now(),
		FinishedAt: time.Now(),
	}, true
}

func hasVisibleTool(list []string, name string) bool {
	for _, item := range list {
		if strings.EqualFold(strings.TrimSpace(item), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

func (h *Harness) GetRun(_ context.Context, runID string) (Run, bool) {
	h.mu.RLock()
	run, ok := h.runs[runID]
	h.mu.RUnlock()
	if ok {
		return run, true
	}
	if strings.TrimSpace(h.Workspace) == "" || strings.TrimSpace(runID) == "" {
		return Run{}, false
	}
	data, err := os.ReadFile(filepath.Join(h.Workspace, "memory", "runs", runID+".json"))
	if err != nil {
		return Run{}, false
	}
	var persisted Run
	if err := json.Unmarshal(data, &persisted); err != nil {
		return Run{}, false
	}
	return persisted, true
}

func (h *Harness) Resume(ctx context.Context, runID string, req Request, generate Generator) (Run, error) {
	if run, ok := h.GetRun(ctx, runID); ok && run.Status == "completed" {
		return run, nil
	}
	return h.Start(ctx, req, generate)
}

func (h *Harness) ListRuns(_ context.Context, sessionKey string, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = 10
	}
	dir := filepath.Join(h.Workspace, "memory", "runs")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	runs := make([]Run, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var run Run
		if err := json.Unmarshal(data, &run); err != nil {
			continue
		}
		if strings.TrimSpace(sessionKey) != "" && strings.TrimSpace(run.SessionKey) != strings.TrimSpace(sessionKey) {
			continue
		}
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].CreatedAt.After(runs[j].CreatedAt)
	})
	if len(runs) > limit {
		runs = runs[:limit]
	}
	return runs, nil
}

func (h *Harness) ListVisibleTools(ctx context.Context, scope tools.Scope) ([]tools.Spec, error) {
	if h.Tools == nil {
		return nil, nil
	}
	specs, err := h.Tools.Specs(ctx, scope)
	if err != nil {
		return nil, err
	}
	effective, err := h.compileCapabilities(ctx, scope)
	if err != nil {
		return nil, err
	}
	out := make([]tools.Spec, 0, len(specs))
	for _, spec := range specs {
		if capabilities.Allows(effective, spec.Name) {
			out = append(out, spec)
		}
	}
	return out, nil
}

func (h *Harness) runTool(ctx context.Context, run Run, req Request, generate Generator) (string, error) {
	if h.Tools == nil {
		return "", fmt.Errorf("tool registry is not configured")
	}
	if !capabilities.Allows(run.Capabilities, req.ToolName) {
		return "", fmt.Errorf("tool %q is not allowed for agent %q", req.ToolName, run.AgentName)
	}
	args, _ := json.Marshal(req.Arguments)
	startedAt := time.Now()
	if !req.SkipApproval && reservedToolRequiresApproval(req.ToolName) &&
		(h.ApprovalPolicy.RequireRiskyTools || h.ApprovalPolicy.RequireScheduleChange) {
		pending := PendingApproval{
			ID:         fmt.Sprintf("approval_%d", time.Now().UnixNano()),
			RunID:      run.ID,
			SessionKey: req.SessionKey,
			AgentName:  run.AgentName,
			ToolName:   req.ToolName,
			Arguments:  req.Arguments,
			Mode:       req.Mode,
			CreatedAt:  time.Now(),
		}
		h.savePendingApproval(pending)
		h.markRunWaitingApproval(run.ID, pending, RunStep{
			Kind:       "tool",
			Status:     "waiting_approval",
			AgentName:  run.AgentName,
			ToolName:   req.ToolName,
			Input:      string(args),
			StartedAt:  startedAt,
			FinishedAt: time.Now(),
		})
		return fmt.Sprintf("工具 `%s` 需要批准，approval id: `%s`。发送 `/approve %s` 执行，`/deny %s` 拒绝，或 `/approvals` 查看全部待批。", req.ToolName, pending.ID, pending.ID, pending.ID), nil
	}
	switch strings.TrimSpace(req.ToolName) {
	case "spawn":
		return h.spawnAgent(ctx, run, req, generate)
	case "wait_agent":
		return h.waitAgent(ctx, run, req)
	}
	scope := tools.Scope{
		UserID:    req.UserID,
		Channel:   req.Channel,
		ThreadID:  firstNonEmpty(req.ThreadID, req.SessionKey),
		AgentName: run.AgentName,
	}
	tool, ok, err := h.Tools.Find(ctx, scope, req.ToolName)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("tool %q not available", req.ToolName)
	}
	spec := tool.Spec()
	if !req.SkipApproval && h.requiresApprovalForTool(spec) {
		pending := PendingApproval{
			ID:         fmt.Sprintf("approval_%d", time.Now().UnixNano()),
			RunID:      run.ID,
			SessionKey: req.SessionKey,
			AgentName:  run.AgentName,
			ToolName:   req.ToolName,
			Arguments:  req.Arguments,
			Mode:       req.Mode,
			CreatedAt:  time.Now(),
		}
		h.savePendingApproval(pending)
		h.markRunWaitingApproval(run.ID, pending, RunStep{
			Kind:       "tool",
			Status:     "waiting_approval",
			AgentName:  run.AgentName,
			ToolName:   req.ToolName,
			Input:      string(args),
			StartedAt:  startedAt,
			FinishedAt: time.Now(),
		})
		return fmt.Sprintf("工具 `%s` 需要批准，approval id: `%s`。发送 `/approve %s` 执行，`/deny %s` 拒绝，或 `/approvals` 查看全部待批。", req.ToolName, pending.ID, pending.ID, pending.ID), nil
	}
	res, err := tool.Invoke(ctx, tools.Call{
		RunID:      run.ID,
		StepID:     "step_1",
		SessionKey: req.SessionKey,
		ThreadID:   req.ThreadID,
		AgentName:  run.AgentName,
		ToolName:   req.ToolName,
		Arguments:  args,
	})
	if err != nil {
		h.appendRunStep(run.ID, RunStep{
			Kind:       "tool",
			Status:     "failed",
			AgentName:  run.AgentName,
			ToolName:   req.ToolName,
			Input:      string(args),
			Output:     err.Error(),
			StartedAt:  startedAt,
			FinishedAt: time.Now(),
		})
		return "", err
	}
	h.appendRunStep(run.ID, RunStep{
		Kind:       "tool",
		Status:     "completed",
		AgentName:  run.AgentName,
		ToolName:   req.ToolName,
		Input:      string(args),
		Output:     string(res.Output),
		StartedAt:  startedAt,
		FinishedAt: time.Now(),
	})
	return string(res.Output), nil
}

func (h *Harness) saveRun(run Run) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.runs == nil {
		h.runs = make(map[string]Run)
	}
	h.runs[run.ID] = run
	_ = h.persistRun(run)
}

func (h *Harness) appendRunStep(runID string, step RunStep) {
	h.mu.Lock()
	run, ok := h.runs[runID]
	if !ok {
		h.mu.Unlock()
		return
	}
	step.Index = len(run.Steps) + 1
	run.Steps = append(run.Steps, step)
	run.UpdatedAt = time.Now()
	h.runs[runID] = run
	h.mu.Unlock()
	_ = h.persistRun(run)
}

func (h *Harness) updateRunModel(runID, modelName string) {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(modelName) == "" {
		return
	}
	h.mu.Lock()
	run, ok := h.runs[runID]
	if !ok {
		h.mu.Unlock()
		return
	}
	run.ModelName = strings.TrimSpace(modelName)
	run.UpdatedAt = time.Now()
	h.runs[runID] = run
	h.mu.Unlock()
	_ = h.persistRun(run)
}

func (h *Harness) markRunWaitingApproval(runID string, pending PendingApproval, step RunStep) {
	h.mu.Lock()
	run, ok := h.runs[runID]
	if !ok {
		h.mu.Unlock()
		return
	}
	step.Index = len(run.Steps) + 1
	run.Steps = append(run.Steps, step)
	run.Status = "waiting_approval"
	run.LastApprovalID = pending.ID
	run.ApprovalIDs = appendUnique(run.ApprovalIDs, pending.ID)
	run.UpdatedAt = time.Now()
	h.runs[runID] = run
	h.mu.Unlock()
	_ = h.persistRun(run)
}

func (h *Harness) markApprovalReviewed(runID, approvalID string, approved bool, note string) {
	if strings.TrimSpace(runID) == "" {
		return
	}
	h.mu.Lock()
	run, ok := h.runs[runID]
	if !ok {
		h.mu.Unlock()
		return
	}
	status := "denied"
	if approved {
		status = "approved"
	}
	step := RunStep{
		Index:      len(run.Steps) + 1,
		Kind:       "approval",
		Status:     status,
		AgentName:  run.AgentName,
		Input:      approvalID,
		Output:     note,
		StartedAt:  time.Now(),
		FinishedAt: time.Now(),
	}
	run.Steps = append(run.Steps, step)
	if approved {
		run.Status = "approved"
	} else {
		run.Status = "denied"
		run.Result = note
	}
	run.UpdatedAt = time.Now()
	h.runs[runID] = run
	h.mu.Unlock()
	_ = h.persistRun(run)
}

func (h *Harness) visibleToolNames(ctx context.Context, req Request, run Run) []string {
	if h.Tools == nil {
		return nil
	}
	scope := tools.Scope{
		UserID:    req.UserID,
		Channel:   req.Channel,
		ThreadID:  firstNonEmpty(req.ThreadID, req.SessionKey),
		AgentName: run.AgentName,
	}
	specs, err := h.ListVisibleTools(ctx, scope)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		out = append(out, spec.Name)
	}
	sort.Strings(out)
	return out
}

func (h *Harness) selectEinoRoute(req Request) string {
	if mode := strings.TrimSpace(argumentString(req.Arguments, "runtime_route")); mode != "" {
		return mode
	}
	if h.EnableEino && h.shouldAvoidPlanExecuteForConfiguredModel() {
		return "chatmodel"
	}
	if shouldUsePlanExecute(firstNonEmpty(req.UserText, argumentString(req.Arguments, "user_text"))) {
		return "plan_execute"
	}
	return "chatmodel"
}

func shouldUsePlanExecute(text string) bool {
	text = strings.TrimSpace(strings.ToLower(text))
	if text == "" {
		return false
	}
	if len([]rune(text)) >= 80 {
		return true
	}
	keywords := []string{
		"调研", "研究", "分析", "方案", "报告", "收集", "整理", "规划", "拆解",
		"research", "analyze", "analysis", "report", "plan", "investigate", "compare",
	}
	for _, keyword := range keywords {
		if strings.Contains(text, keyword) {
			return true
		}
	}
	if strings.Contains(text, "先") && (strings.Contains(text, "再") || strings.Contains(text, "然后")) {
		return true
	}
	if strings.Contains(text, "并且") || strings.Contains(text, "同时") {
		return true
	}
	return false
}

func (h *Harness) spawnAgent(ctx context.Context, parent Run, req Request, generate Generator) (string, error) {
	var args struct {
		AgentName         string         `json:"agent_name"`
		Mode              string         `json:"mode"`
		UserText          string         `json:"user_text"`
		ToolName          string         `json:"tool_name"`
		Arguments         map[string]any `json:"arguments"`
		Async             bool           `json:"async"`
		SessionKey        string         `json:"session_key"`
		ThreadID          string         `json:"thread_id"`
		CollaborationMode string         `json:"collaboration_mode"`
	}
	data, _ := json.Marshal(req.Arguments)
	if err := json.Unmarshal(data, &args); err != nil {
		return "", err
	}
	childProfile, err := h.loadAgentProfile(firstNonEmpty(args.AgentName, parent.AgentName))
	if err != nil {
		return "", err
	}
	collaborationMode := resolveCollaborationMode(args.CollaborationMode, childProfile)
	childSessionKey := firstNonEmpty(args.SessionKey, req.SessionKey+":subagent:"+firstNonEmpty(args.AgentName, "default"))
	childThreadID := firstNonEmpty(args.ThreadID, req.ThreadID)
	if collaborationMode == "shared" {
		childSessionKey = req.SessionKey
		childThreadID = firstNonEmpty(req.ThreadID, req.SessionKey)
	}
	childReq := Request{
		SessionKey:  childSessionKey,
		ThreadID:    childThreadID,
		UserID:      req.UserID,
		Channel:     req.Channel,
		AgentName:   firstNonEmpty(args.AgentName, parent.AgentName),
		UserText:    args.UserText,
		Mode:        firstNonEmpty(args.Mode, "chat"),
		ToolName:    args.ToolName,
		Arguments:   mergeArguments(args.Arguments, map[string]any{"collaboration_mode": collaborationMode}),
		ParentRunID: parent.ID,
	}
	childCaps, err := h.compileCapabilities(ctx, tools.Scope{
		UserID:    childReq.UserID,
		Channel:   childReq.Channel,
		ThreadID:  firstNonEmpty(childReq.ThreadID, childReq.SessionKey),
		AgentName: childReq.AgentName,
	})
	if err != nil {
		return "", err
	}
	childCaps = capabilities.Narrow(parent.Capabilities, childCaps)
	childReq.Capabilities = childCaps

	if args.Async {
		childReq.Mode = firstNonEmpty(args.Mode, "chat")
		childRun := Run{
			ID:           fmt.Sprintf("run_%d", time.Now().UnixNano()),
			SessionKey:   childReq.SessionKey,
			ThreadID:     strings.TrimSpace(firstNonEmpty(childReq.ThreadID, childReq.SessionKey)),
			AgentName:    childReq.AgentName,
			Mode:         childReq.Mode,
			ToolName:     childReq.ToolName,
			Status:       "queued",
			ParentRunID:  parent.ID,
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
			Capabilities: childCaps,
		}
		parent.ChildRunIDs = appendUnique(parent.ChildRunIDs, childRun.ID)
		parent.LastChildRunID = childRun.ID
		parent.LastChildStatus = "queued"
		parent.UpdatedAt = time.Now()
		h.saveRun(parent)
		h.saveRun(childRun)
		go h.runAsyncChild(childRun, childReq, generate)
		childRun.CollaborationMode = collaborationMode
		h.saveRun(childRun)
		return fmt.Sprintf(`{"run_id":"%s","status":"queued","async":true,"collaboration_mode":%q}`, childRun.ID, collaborationMode), nil
	}

	parent.Status = "waiting_subagent"
	parent.UpdatedAt = time.Now()
	h.saveRun(parent)
	childRun, err := h.Start(ctx, childReq, generate)
	parent = h.mustGetRun(parent.ID)
	parent.ChildRunIDs = appendUnique(parent.ChildRunIDs, childRun.ID)
	parent.LastChildRunID = childRun.ID
	parent.LastChildStatus = childRun.Status
	parent.LastChildResult = childRun.Result
	parent.Status = "running"
	parent.UpdatedAt = time.Now()
	h.saveRun(parent)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`{"run_id":"%s","status":"%s","result":%q,"async":false,"collaboration_mode":%q}`, childRun.ID, childRun.Status, childRun.Result, collaborationMode), nil
}

func (h *Harness) waitAgent(_ context.Context, parent Run, req Request) (string, error) {
	runID := strings.TrimSpace(argumentString(req.Arguments, "run_id"))
	if runID == "" {
		runID = parent.LastChildRunID
	}
	if runID == "" {
		return "", fmt.Errorf("run_id is required")
	}
	child, ok := h.GetRun(context.Background(), runID)
	if !ok {
		return "", fmt.Errorf("run %q not found", runID)
	}
	parent.LastChildRunID = child.ID
	parent.LastChildStatus = child.Status
	parent.LastChildResult = child.Result
	parent.UpdatedAt = time.Now()
	h.saveRun(parent)
	return fmt.Sprintf(`{"run_id":"%s","status":"%s","result":%q}`, child.ID, child.Status, child.Result), nil
}

func (h *Harness) runAsyncChild(seed Run, req Request, generate Generator) {
	h.mu.Lock()
	run := h.runs[seed.ID]
	run.Status = "running"
	run.UpdatedAt = time.Now()
	h.runs[seed.ID] = run
	h.mu.Unlock()
	_ = h.persistRun(run)

	var (
		result string
		err    error
	)
	switch strings.TrimSpace(req.Mode) {
	case "", "chat":
		if !h.EnableEino {
			err = fmt.Errorf("chat runtime requires Eino; call UseEinoRuntime before starting chat runs")
		} else {
			result, err = h.runSessionTurn(context.Background(), req.SessionKey, req.UserText, func(ctx context.Context, history []HistoryMessage, userText string) (string, error) {
				return h.einoChat(ctx, req, run, history, userText)
			})
		}
	case "tool":
		result, err = h.runTool(context.Background(), run, req, generate)
	default:
		err = fmt.Errorf("unsupported harness mode %q", req.Mode)
	}
	run = h.mustGetRun(seed.ID)
	if err != nil {
		run.Status = "failed"
		run.Error = err.Error()
	} else {
		run.Status = "completed"
		run.Result = strings.TrimSpace(result)
	}
	run.UpdatedAt = time.Now()
	h.saveRun(run)
	h.notifyParent(run)
}

func (h *Harness) notifyParent(child Run) {
	if strings.TrimSpace(child.ParentRunID) == "" {
		return
	}
	parent, ok := h.GetRun(context.Background(), child.ParentRunID)
	if !ok {
		return
	}
	parent.ChildRunIDs = appendUnique(parent.ChildRunIDs, child.ID)
	parent.LastChildRunID = child.ID
	parent.LastChildStatus = child.Status
	parent.LastChildResult = child.Result
	if parent.Status == "waiting_subagent" {
		parent.Status = "running"
	}
	parent.UpdatedAt = time.Now()
	h.saveRun(parent)
	_ = h.Memory.Append(context.Background(), "runs", parent.SessionKey, fmt.Sprintf("child_run=%s status=%s result=%s", child.ID, child.Status, trim(child.Result, 200)), map[string]any{
		"run_id":        parent.ID,
		"child_run_id":  child.ID,
		"child_status":  child.Status,
		"parent_run_id": parent.ID,
	})
}

func (h *Harness) persistRun(run Run) error {
	if strings.TrimSpace(h.Workspace) == "" {
		return nil
	}
	dir := filepath.Join(h.Workspace, "memory", "runs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, run.ID+".json"), data, 0o644)
}

func (h *Harness) mustGetRun(runID string) Run {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.runs[runID]
}

func (h *Harness) compileCapabilities(ctx context.Context, scope tools.Scope) (capabilities.Effective, error) {
	var specs []tools.Spec
	if h.Tools != nil {
		var err error
		specs, err = h.Tools.Specs(ctx, scope)
		if err != nil {
			return capabilities.Effective{}, err
		}
	}
	profile, err := h.loadAgentProfile(scope.AgentName)
	if err != nil {
		return capabilities.Effective{}, err
	}
	effective := capabilities.ApplyScopePolicy(capabilities.Compile(h.Workspace, scope, profile, specs), scope)
	effective.VisibleSkills = h.visibleSkillNames(profile, effective.VisibleSkills)
	return effective, nil
}

func (h *Harness) loadAgentProfile(name string) (agents.Profile, error) {
	name = firstNonEmpty(name, "default")
	profile, err := agents.Resolve(filepath.Join(h.Workspace, "agents"), name)
	if err == nil {
		return profile, nil
	}
	if name == "default" {
		return agents.Profile{
			Name:         "default",
			CanSpawn:     true,
			AsyncAllowed: true,
		}, nil
	}
	return agents.Profile{}, err
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func trim(value string, limit int) string {
	return textutil.CleanBlock(value, limit)
}

func appendUnique(values []string, value string) []string {
	for _, item := range values {
		if item == value {
			return values
		}
	}
	return append(values, value)
}

func (h *Harness) visibleSkillNames(profile agents.Profile, existing []string) []string {
	set := make(map[string]bool, len(existing))
	allowed := stringSet(profile.AllowedSkills)
	for _, name := range existing {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if len(allowed) > 0 && !allowed[name] {
			continue
		}
		set[name] = true
	}
	if h.SkillCatalog != nil {
		for _, skill := range h.SkillCatalog.Snapshot() {
			name := strings.TrimSpace(skill.Manifest.Name)
			if name == "" {
				continue
			}
			if len(allowed) > 0 && !allowed[name] {
				continue
			}
			set[name] = true
		}
	}
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func stringSet(values []string) map[string]bool {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out[value] = true
		}
	}
	return out
}

func (h *Harness) visibleSkillCatalog(visibleNames []string) []skills.Skill {
	if h.SkillCatalog == nil || len(visibleNames) == 0 {
		return nil
	}
	return skills.FilterVisible(h.SkillCatalog.Snapshot(), visibleNames)
}

func (h *Harness) SkillAccessForRun(ctx context.Context, runID string) ([]string, []string, bool) {
	run, ok := h.GetRun(ctx, runID)
	if !ok {
		return nil, nil, false
	}
	return append([]string(nil), run.SelectedSkills...), append([]string(nil), run.Capabilities.VisibleSkills...), true
}

func (h *Harness) applySkillSelection(ctx context.Context, run Run) Run {
	selected, source, note := h.selectSkillsForRun(ctx, run)
	run.SelectedSkills = append([]string(nil), selected...)
	run.SkillPickerSource = strings.TrimSpace(source)
	if bundle, err := h.buildEinoSkillBundle(ctx, Request{UserID: "", Channel: "", ThreadID: run.ThreadID, AgentName: run.AgentName}, run, run.Capabilities.VisibleSkills, run.SelectedSkills); err == nil && bundle != nil {
		run.VisibleTools = appendUnique(run.VisibleTools, einoSkillToolName)
	}
	run.UpdatedAt = time.Now()
	h.saveRun(run)
	if strings.TrimSpace(note) != "" {
		h.appendRunStep(run.ID, RunStep{
			Kind:       "skill_picker",
			Status:     "completed",
			AgentName:  run.AgentName,
			Input:      trim(run.Goal, 220),
			Output:     trim(note, 500),
			StartedAt:  time.Now(),
			FinishedAt: time.Now(),
		})
	}
	return h.mustGetRun(run.ID)
}

func mergeArguments(base map[string]any, extra map[string]any) map[string]any {
	if len(base) == 0 && len(extra) == 0 {
		return nil
	}
	out := make(map[string]any, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func argumentString(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	raw, ok := values[key]
	if !ok || raw == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(raw))
}

func hasCapabilities(e capabilities.Effective) bool {
	return e.AgentName != "" || len(e.CallableTools) > 0 || len(e.VisibleTools) > 0
}

func reservedToolRequiresApproval(name string) bool {
	switch strings.TrimSpace(name) {
	case "spawn", "schedule_create", "schedule_enable", "schedule_disable", "schedule_remove":
		return true
	default:
		return false
	}
}

func (h *Harness) requiresApprovalForTool(spec tools.Spec) bool {
	name := strings.TrimSpace(spec.Name)
	if h.ApprovalPolicy.RequireScheduleChange && isScheduleMutationTool(name) {
		return true
	}
	if h.ApprovalPolicy.RequireRiskyTools && spec.RiskLevel != "" && spec.RiskLevel != "low" {
		return true
	}
	return false
}

func isScheduleMutationTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "schedule_create", "schedule_enable", "schedule_disable", "schedule_remove":
		return true
	default:
		return false
	}
}

func resolveCollaborationMode(raw string, profile agents.Profile) string {
	mode := strings.ToLower(strings.TrimSpace(raw))
	if mode == "" {
		mode = strings.ToLower(strings.TrimSpace(profile.CollaborationMode))
	}
	if mode == "" {
		if strings.EqualFold(strings.TrimSpace(profile.ChannelVisibility), "shared") {
			mode = "shared"
		} else {
			mode = "coordinator"
		}
	}
	switch mode {
	case "shared":
		return "shared"
	default:
		return "coordinator"
	}
}

func (h *Harness) refreshSessionSummary(ctx context.Context, run Run) error {
	if h.Sessions == nil || strings.TrimSpace(run.SessionKey) == "" {
		return nil
	}
	recentMessages, err := h.Sessions.LoadRecent(run.SessionKey, 8)
	if err != nil {
		return err
	}
	recentRuns, err := h.Memory.Recent(ctx, "runs", run.SessionKey, 4)
	if err != nil {
		return err
	}
	parts := make([]string, 0, 4)
	if len(recentMessages) > 0 {
		last := recentMessages[len(recentMessages)-1]
		parts = append(parts, fmt.Sprintf("最新对话: %s: %s", last.Role, trim(last.Content, 180)))
	}
	if strings.TrimSpace(run.Result) != "" {
		parts = append(parts, fmt.Sprintf("最近结果: %s", trim(run.Result, 220)))
	}
	if len(recentRuns) > 0 {
		fragments := make([]string, 0, len(recentRuns))
		for _, note := range recentRuns {
			fragments = append(fragments, trim(note.Content, 120))
			if len(fragments) >= 2 {
				break
			}
		}
		if len(fragments) > 0 {
			parts = append(parts, "近期执行: "+strings.Join(fragments, " | "))
		}
	}
	if len(parts) == 0 {
		return nil
	}
	summary := strings.Join(parts, "\n")
	return h.Memory.WriteSessionSummary(ctx, run.SessionKey, summary, map[string]any{
		"run_id":     run.ID,
		"agent_name": run.AgentName,
		"status":     run.Status,
		"thread_id":  run.ThreadID,
	})
}

func (h *Harness) runSessionTurn(ctx context.Context, sessionKey, userText string, generate Generator) (string, error) {
	sessionKey = strings.TrimSpace(sessionKey)
	userText = strings.TrimSpace(userText)
	if sessionKey == "" || userText == "" || generate == nil {
		return "", nil
	}
	if _, loaded := h.inflight.LoadOrStore(sessionKey, time.Now()); loaded {
		return "", ErrSessionBusy
	}
	defer h.inflight.Delete(sessionKey)

	var history []HistoryMessage
	if h.Sessions != nil {
		items, err := h.Sessions.LoadRecent(sessionKey, h.limit())
		if err == nil {
			history = make([]HistoryMessage, 0, len(items))
			for _, item := range items {
				history = append(history, HistoryMessage{
					Role:    item.Role,
					Content: item.Content,
				})
			}
		}
	}

	reply, err := generate(ctx, history, userText)
	if err != nil {
		h.reflectTurn(sessionKey, "failed", classifyTurnFailure(err), len(history))
		return "", err
	}
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return "", nil
	}
	if h.Sessions != nil {
		_ = h.Sessions.Append(sessionKey,
			session.Message{Role: "user", Content: userText},
			session.Message{Role: "assistant", Content: reply},
		)
	}
	h.reflectTurn(sessionKey, "success", "", len(history))
	return reply, nil
}

func (h *Harness) limit() int {
	if h.HistoryLimit > 0 {
		return h.HistoryLimit
	}
	return 12
}

func (h *Harness) reflectTurn(sessionKey, status, failure string, historyCount int) {
	if h.Sessions == nil || strings.TrimSpace(h.Workspace) == "" {
		return
	}
	_ = reflection.Append(h.Workspace, reflection.Record{
		CreatedAt: time.Now().Format(time.RFC3339),
		Type:      "llm_turn",
		Status:    status,
		Failure:   failure,
		Metadata: map[string]any{
			"session_key":    sessionKey,
			"history_count":  historyCount,
			"turn_harness":   "embedded",
			"runtime_family": "mateway",
		},
	})
}

func classifyTurnFailure(err error) string {
	if errors.Is(err, ErrSessionBusy) {
		return "session_busy"
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(msg, "429"), strings.Contains(msg, "throttling"), strings.Contains(msg, "quota exceeded"), strings.Contains(msg, "cooling down"):
		return "llm_throttled"
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "deadline exceeded"):
		return "timeout"
	default:
		return "llm_error"
	}
}
