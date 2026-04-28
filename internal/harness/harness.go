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
	"github.com/dongping/mateway/internal/llm"
	"github.com/dongping/mateway/internal/memory"
	"github.com/dongping/mateway/internal/observability"
	"github.com/dongping/mateway/internal/reflection"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/skills"
	"github.com/dongping/mateway/internal/textutil"
	"github.com/dongping/mateway/internal/tools"
)

var ErrSessionBusy = errors.New("session already processing a turn")

const staleSessionBusyTTL = 2 * time.Minute

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
	TaskType     string
	UserText     string
	Mode         string
	ToolName     string
	Arguments    map[string]any
	ParentRunID  string
	SkipApproval bool
	Capabilities capabilities.Effective
}

type AsyncResultEvent struct {
	Channel     string    `json:"channel,omitempty"`
	SessionKey  string    `json:"session_key,omitempty"`
	ThreadID    string    `json:"thread_id,omitempty"`
	UserID      string    `json:"user_id,omitempty"`
	RunID       string    `json:"run_id,omitempty"`
	ParentRunID string    `json:"parent_run_id,omitempty"`
	AgentName   string    `json:"agent_name,omitempty"`
	Goal        string    `json:"goal,omitempty"`
	Status      string    `json:"status,omitempty"`
	Result      string    `json:"result,omitempty"`
	Error       string    `json:"error,omitempty"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
}

type AsyncResultNotifier interface {
	NotifyAsyncResult(ctx context.Context, event AsyncResultEvent) error
}

type Run struct {
	ID                  string                 `json:"id"`
	SessionKey          string                 `json:"session_key"`
	ThreadID            string                 `json:"thread_id,omitempty"`
	TaskID              string                 `json:"task_id,omitempty"`
	UserID              string                 `json:"user_id,omitempty"`
	Channel             string                 `json:"channel,omitempty"`
	AgentName           string                 `json:"agent_name,omitempty"`
	TaskType            string                 `json:"task_type,omitempty"`
	OriginKind          string                 `json:"origin_kind,omitempty"`
	Goal                string                 `json:"goal,omitempty"`
	Mode                string                 `json:"mode,omitempty"`
	Route               string                 `json:"route,omitempty"`
	ModelName           string                 `json:"model_name,omitempty"`
	CollaborationMode   string                 `json:"collaboration_mode,omitempty"`
	ToolName            string                 `json:"tool_name,omitempty"`
	Status              string                 `json:"status"`
	Result              string                 `json:"result,omitempty"`
	Error               string                 `json:"error,omitempty"`
	ParentRunID         string                 `json:"parent_run_id,omitempty"`
	ChildRunIDs         []string               `json:"child_run_ids,omitempty"`
	LastChildRunID      string                 `json:"last_child_run_id,omitempty"`
	LastChildStatus     string                 `json:"last_child_status,omitempty"`
	LastChildResult     string                 `json:"last_child_result,omitempty"`
	LastApprovalID      string                 `json:"last_approval_id,omitempty"`
	ApprovalIDs         []string               `json:"approval_ids,omitempty"`
	Steps               []RunStep              `json:"steps,omitempty"`
	Events              []RunEvent             `json:"events,omitempty"`
	RecoveryAttempts    []RecoveryAttempt      `json:"recovery_attempts,omitempty"`
	LearningProposals   []LearningProposal     `json:"learning_proposals,omitempty"`
	CreatedAt           time.Time              `json:"created_at"`
	UpdatedAt           time.Time              `json:"updated_at"`
	Capabilities        capabilities.Effective `json:"capabilities,omitempty"`
	VisibleTools        []string               `json:"visible_tools,omitempty"`
	SelectedSkills      []string               `json:"selected_skills,omitempty"`
	SkillPickerSource   string                 `json:"skill_picker_source,omitempty"`
	ScheduleName        string                 `json:"schedule_name,omitempty"`
	ScheduleJobID       string                 `json:"schedule_job_id,omitempty"`
	ScheduleTriggeredAt time.Time              `json:"schedule_triggered_at,omitempty"`
	ModelAttempts       int                    `json:"model_attempts,omitempty"`
	Model429Count       int                    `json:"model_429_count,omitempty"`
	PromptTokens        int                    `json:"prompt_tokens,omitempty"`
	CompletionTokens    int                    `json:"completion_tokens,omitempty"`
	TotalTokens         int                    `json:"total_tokens,omitempty"`
	EstimatedCostUSD    float64                `json:"estimated_cost_usd,omitempty"`
	ModelDurationMs     int64                  `json:"model_duration_ms,omitempty"`
	ContextCompactions  int                    `json:"context_compactions,omitempty"`
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

type RunEvent struct {
	Index       int            `json:"index"`
	Kind        string         `json:"kind"`
	Status      string         `json:"status"`
	Phase       string         `json:"phase,omitempty"`
	AgentName   string         `json:"agent_name,omitempty"`
	ToolName    string         `json:"tool_name,omitempty"`
	ModelName   string         `json:"model_name,omitempty"`
	Channel     string         `json:"channel,omitempty"`
	SessionKey  string         `json:"session_key,omitempty"`
	ThreadID    string         `json:"thread_id,omitempty"`
	RunID       string         `json:"run_id,omitempty"`
	ParentRunID string         `json:"parent_run_id,omitempty"`
	ChildRunID  string         `json:"child_run_id,omitempty"`
	Message     string         `json:"message,omitempty"`
	Input       string         `json:"input,omitempty"`
	Output      string         `json:"output,omitempty"`
	Error       string         `json:"error,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	StartedAt   time.Time      `json:"started_at"`
	FinishedAt  time.Time      `json:"finished_at"`
}

type RecoveryAttempt struct {
	Index             int       `json:"index"`
	TriggerEventIndex int       `json:"trigger_event_index,omitempty"`
	FailureKind       string    `json:"failure_kind"`
	Action            string    `json:"action"`
	Status            string    `json:"status"`
	Detail            string    `json:"detail,omitempty"`
	StartedAt         time.Time `json:"started_at"`
	FinishedAt        time.Time `json:"finished_at"`
}

type LearningProposal struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	Title      string    `json:"title"`
	Rationale  string    `json:"rationale,omitempty"`
	TargetPath string    `json:"target_path,omitempty"`
	Content    string    `json:"content"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	AppliedAt  time.Time `json:"applied_at,omitempty"`
	AppliedBy  string    `json:"applied_by,omitempty"`
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
	limiterHub     *llm.LimiterHub

	mu        sync.RWMutex
	runs      map[string]Run
	inflight  sync.Map
	notifiers map[string]AsyncResultNotifier
	approvalState
}

func (h *Harness) UseEinoRuntime(cfg config.Config) {
	h.Config = cfg
	h.EnableEino = true
	h.limiterHub = llm.NewLimiterHub(cfg.Models.Limits.RequestsPerMinute, cfg.Models.Limits.CooldownOn429)
}

func New(workspace string, sessions *session.Store, registry *tools.Registry, historyLimit int) *Harness {
	return &Harness{
		Workspace:    workspace,
		Sessions:     sessions,
		Tools:        registry,
		Memory:       memory.Store{Workspace: workspace},
		runs:         make(map[string]Run),
		notifiers:    make(map[string]AsyncResultNotifier),
		HistoryLimit: historyLimit,
		approvalState: approvalState{
			pendingApprovalsBySession: make(map[string][]PendingApproval),
			pendingApprovalsByID:      make(map[string]PendingApproval),
		},
	}
}

func (h *Harness) RegisterChannelNotifier(channel string, notifier AsyncResultNotifier) {
	channel = strings.TrimSpace(strings.ToLower(channel))
	if channel == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.notifiers == nil {
		h.notifiers = make(map[string]AsyncResultNotifier)
	}
	if notifier == nil {
		delete(h.notifiers, channel)
		return
	}
	h.notifiers[channel] = notifier
}

func (h *Harness) SessionBusy(sessionKey string) bool {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return false
	}
	_, ok := h.activeSessionInflight(sessionKey)
	return ok
}

func (h *Harness) ResetSession(ctx context.Context, sessionKey string) error {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return nil
	}
	if h.SessionBusy(sessionKey) {
		return ErrSessionBusy
	}
	var errs []error
	if h.Sessions != nil {
		if err := h.Sessions.Reset(sessionKey); err != nil {
			errs = append(errs, err)
		}
	}
	if err := h.Memory.ResetSessionSummary(ctx, sessionKey); err != nil {
		errs = append(errs, err)
	}
	h.clearPendingApprovals(sessionKey)
	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
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
		TaskID:            strings.TrimSpace(argumentString(req.Arguments, "task_id")),
		UserID:            strings.TrimSpace(req.UserID),
		Channel:           strings.TrimSpace(req.Channel),
		AgentName:         firstNonEmpty(req.AgentName, "default"),
		TaskType:          resolveTaskType(req),
		OriginKind:        resolveOriginKind(req),
		Goal:              strings.TrimSpace(req.UserText),
		Mode:              firstNonEmpty(req.Mode, "chat"),
		CollaborationMode: firstNonEmpty(argumentString(req.Arguments, "collaboration_mode"), "coordinator"),
		ToolName:          strings.TrimSpace(req.ToolName),
		ScheduleName:      strings.TrimSpace(argumentString(req.Arguments, "schedule_name")),
		ScheduleJobID:     strings.TrimSpace(argumentString(req.Arguments, "schedule_job_id")),
		Status:            "running",
		ParentRunID:       strings.TrimSpace(req.ParentRunID),
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
		Capabilities:      effective,
	}
	if triggeredAt := strings.TrimSpace(argumentString(req.Arguments, "triggered_at")); triggeredAt != "" {
		if parsed, err := time.Parse(time.RFC3339, triggeredAt); err == nil {
			run.ScheduleTriggeredAt = parsed
		}
	}
	if run.TaskID == "" && strings.TrimSpace(run.ParentRunID) == "" {
		run.TaskID = run.ID
	}
	if run.Mode == "chat" && h.EnableEino {
		run.Route = h.selectEinoRoute(req)
	}
	run.VisibleTools = h.visibleToolNames(ctx, req, run)
	h.saveRun(run)
	h.appendRunEvent(run.ID, RunEvent{
		Kind:      "run_start",
		Status:    "started",
		Phase:     "runtime",
		Input:     trim(req.UserText, 400),
		StartedAt: run.CreatedAt,
	})
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
		result, err = h.runSessionTurn(ctx, run, req.UserText, func(ctx context.Context, history []HistoryMessage, userText string) (string, error) {
			return h.einoChat(ctx, req, run, history, userText)
		})
	case "tool":
		result, err = h.runTool(ctx, run, req, generate)
	default:
		err = fmt.Errorf("unsupported harness mode %q", req.Mode)
	}
	if err != nil {
		if recoveredResult, recovered, recoveryErr := h.tryRuntimeRecovery(ctx, run, req, generate, err); recovered {
			if recoveryErr == nil {
				result = recoveredResult
				err = nil
			} else {
				err = recoveryErr
			}
		}
	}
	if err != nil {
		run.Status = "failed"
		run.Error = err.Error()
		run.UpdatedAt = time.Now()
		h.saveRun(run)
		h.appendRunEvent(run.ID, RunEvent{
			Kind:       "run_end",
			Status:     "failed",
			Phase:      "runtime",
			Error:      err.Error(),
			StartedAt:  run.UpdatedAt,
			FinishedAt: run.UpdatedAt,
		})
		_ = h.recordFailureLearning(ctx, run, req, err)
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
	h.appendRunEvent(run.ID, RunEvent{
		Kind:       "run_end",
		Status:     "completed",
		Phase:      "runtime",
		Output:     trim(run.Result, 500),
		StartedAt:  run.UpdatedAt,
		FinishedAt: run.UpdatedAt,
	})
	_ = h.refreshSessionSummary(ctx, run)
	_ = h.maybeWriteLearnProposal(ctx, run, req)
	run = h.mustGetRun(run.ID)

	_ = h.Memory.Append(ctx, "runs", req.SessionKey, fmt.Sprintf("run=%s status=%s result=%s", run.ID, run.Status, trim(run.Result, 200)), map[string]any{
		"run_id":     run.ID,
		"status":     run.Status,
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
			"task_type":     run.TaskType,
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
	path, err := h.writeLearnReportFile(run, req)
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
	proposals := buildLearningProposals(run, req, path)
	if len(proposals) > 0 {
		if err := h.saveLearningProposals(ctx, run.ID, proposals); err != nil {
			return err
		}
	}
	return nil
}

func (h *Harness) writeLearnReportFile(run Run, req Request) (string, error) {
	if strings.TrimSpace(h.Workspace) == "" {
		return "", nil
	}
	dir := filepath.Join(h.Workspace, "memory", "learning", "reports")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, run.ID+".md")
	if err := os.WriteFile(path, []byte(FormatLearnReport(run)+"\n"), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func buildLearningProposals(run Run, req Request, learnPath string) []LearningProposal {
	now := time.Now()
	slug := strings.ToLower(strings.TrimSpace(run.ID))
	summary := trimInline(firstNonEmpty(run.Result, run.Error, run.Goal), 180)
	proposals := []LearningProposal{{
		ID:         "memory-" + slug,
		Kind:       "memory",
		Title:      "沉淀本次任务经验",
		Rationale:  "把本次 run 的目标、执行结果和复盘路径写入长期记忆，后续相似任务可优先召回。",
		TargetPath: filepath.Join("memory", "learning", "applied", "memory-"+slug+".md"),
		Content: strings.Join([]string{
			"# Learned Run - " + run.ID,
			"",
			"## Goal",
			firstNonEmpty(run.Goal, req.UserText, "(missing)"),
			"",
			"## Summary",
			firstNonEmpty(summary, "(empty)"),
			"",
			"## Learn Report",
			learnPath,
		}, "\n"),
		Status:    "proposed",
		CreatedAt: now,
	}}
	if strings.TrimSpace(run.Error) != "" || len(run.RecoveryAttempts) > 0 || run.Model429Count > 0 {
		proposals = append(proposals, LearningProposal{
			ID:         "prompt-" + slug,
			Kind:       "prompt",
			Title:      "补充运行时恢复提示",
			Rationale:  "本次 run 出现失败、恢复或模型受限信号，建议把恢复策略沉淀到 prompt 改进 backlog，避免后续重复犯错。",
			TargetPath: filepath.Join("memory", "learning", "prompt-improvements.md"),
			Content: strings.Join([]string{
				"## " + time.Now().Format("2006-01-02") + " " + run.ID,
				"",
				"- goal: " + firstNonEmpty(run.Goal, req.UserText, "-"),
				"- task_type: " + firstNonEmpty(run.TaskType, "-"),
				"- route: " + firstNonEmpty(run.Route, "-"),
				"- failure: " + firstNonEmpty(run.Error, "-"),
				"- suggestion: " + nextStrategyNarrative(run)[0],
			}, "\n"),
			Status:    "proposed",
			CreatedAt: now,
		})
	}
	if len(run.SelectedSkills) > 0 || failureToolName(run) != "" {
		proposals = append(proposals, LearningProposal{
			ID:         "skill-" + slug,
			Kind:       "skill",
			Title:      "补充 skill/tool 使用经验",
			Rationale:  "本次 run 涉及 skill 或工具调用，建议记录可复用的 tool 选择、降级和失败经验。",
			TargetPath: filepath.Join("memory", "learning", "skill-improvements.md"),
			Content: strings.Join([]string{
				"## " + time.Now().Format("2006-01-02") + " " + run.ID,
				"",
				"- goal: " + firstNonEmpty(run.Goal, req.UserText, "-"),
				"- task_type: " + firstNonEmpty(run.TaskType, "-"),
				"- selected_skills: " + firstNonEmpty(strings.Join(run.SelectedSkills, ", "), "-"),
				"- visible_tools: " + firstNonEmpty(strings.Join(run.VisibleTools, ", "), "-"),
				"- tool: " + firstNonEmpty(failureToolName(run), "-"),
				"- lesson: prefer verified available tools; when denied or missing, switch routes instead of retrying the same command.",
			}, "\n"),
			Status:    "proposed",
			CreatedAt: now,
		})
	}
	return proposals
}

func (h *Harness) saveLearningProposals(_ context.Context, runID string, proposals []LearningProposal) error {
	h.mu.Lock()
	run, ok := h.runs[runID]
	if !ok {
		h.mu.Unlock()
		return nil
	}
	existing := map[string]LearningProposal{}
	for _, proposal := range run.LearningProposals {
		existing[proposal.ID] = proposal
	}
	for _, proposal := range proposals {
		if prev, ok := existing[proposal.ID]; ok && prev.Status != "" {
			proposal.Status = prev.Status
			proposal.AppliedAt = prev.AppliedAt
			proposal.AppliedBy = prev.AppliedBy
		}
		existing[proposal.ID] = proposal
	}
	merged := make([]LearningProposal, 0, len(existing))
	for _, proposal := range existing {
		merged = append(merged, proposal)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].ID < merged[j].ID })
	run.LearningProposals = merged
	run.UpdatedAt = time.Now()
	h.runs[runID] = run
	h.mu.Unlock()
	if err := h.persistRun(run); err != nil {
		return err
	}
	if strings.TrimSpace(h.Workspace) == "" {
		return nil
	}
	dir := filepath.Join(h.Workspace, "memory", "learning", "proposals")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, runID+".json"), data, 0o644); err != nil {
		return err
	}
	for _, proposal := range merged {
		h.appendRunEvent(runID, RunEvent{
			Kind:      "learning_proposal",
			Status:    proposal.Status,
			Phase:     "learning",
			Message:   proposal.Title,
			Output:    proposal.Rationale,
			StartedAt: proposal.CreatedAt,
			Metadata: map[string]any{
				"proposal_id": proposal.ID,
				"kind":        proposal.Kind,
				"target_path": proposal.TargetPath,
			},
		})
	}
	return nil
}

func (h *Harness) ApplyLearningProposal(_ context.Context, runID, proposalID, appliedBy string) ([]LearningProposal, error) {
	run, ok := h.GetRun(context.Background(), runID)
	if !ok {
		return nil, fmt.Errorf("run %q not found", runID)
	}
	if len(run.LearningProposals) == 0 {
		return nil, fmt.Errorf("run %q has no learning proposals", runID)
	}
	applied := []LearningProposal{}
	for i := range run.LearningProposals {
		proposal := &run.LearningProposals[i]
		if strings.TrimSpace(proposalID) != "" && proposal.ID != strings.TrimSpace(proposalID) {
			continue
		}
		if proposal.Status == "applied" {
			applied = append(applied, *proposal)
			continue
		}
		if err := h.applyLearningProposalToDisk(*proposal); err != nil {
			return applied, err
		}
		proposal.Status = "applied"
		proposal.AppliedAt = time.Now()
		proposal.AppliedBy = strings.TrimSpace(appliedBy)
		applied = append(applied, *proposal)
	}
	if len(applied) == 0 {
		return nil, fmt.Errorf("learning proposal %q not found", proposalID)
	}
	run.UpdatedAt = time.Now()
	h.saveRun(run)
	if err := h.writeLearningProposalFile(run.ID, run.LearningProposals); err != nil {
		return applied, err
	}
	for _, proposal := range applied {
		h.appendRunEvent(run.ID, RunEvent{
			Kind:    "learning_proposal_applied",
			Status:  "completed",
			Phase:   "learning",
			Message: proposal.Title,
			Output:  proposal.TargetPath,
			Metadata: map[string]any{
				"proposal_id": proposal.ID,
				"kind":        proposal.Kind,
				"applied_by":  proposal.AppliedBy,
			},
		})
	}
	return applied, nil
}

func (h *Harness) applyLearningProposalToDisk(proposal LearningProposal) error {
	if strings.TrimSpace(h.Workspace) == "" {
		return nil
	}
	target := filepath.Join(h.Workspace, filepath.Clean(strings.TrimSpace(proposal.TargetPath)))
	if !isWithinHarnessWorkspace(target, h.Workspace) {
		return fmt.Errorf("learning proposal target escapes workspace: %s", proposal.TargetPath)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	content := strings.TrimSpace(proposal.Content)
	if content == "" {
		content = proposal.Title
	}
	if proposal.Kind == "memory" {
		return os.WriteFile(target, []byte(content+"\n"), 0o644)
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString("\n\n" + content + "\n")
	return err
}

func (h *Harness) writeLearningProposalFile(runID string, proposals []LearningProposal) error {
	if strings.TrimSpace(h.Workspace) == "" {
		return nil
	}
	dir := filepath.Join(h.Workspace, "memory", "learning", "proposals")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(proposals, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, runID+".json"), data, 0o644)
}

func FormatLearnReport(run Run) string {
	lines := []string{
		fmt.Sprintf("run `%s` 正式复盘报告", run.ID),
		fmt.Sprintf("status: %s", firstNonEmpty(run.Status, "-")),
		fmt.Sprintf("route: %s", firstNonEmpty(run.Route, "-")),
	}
	if run.TaskType != "" {
		lines = append(lines, fmt.Sprintf("task_type: %s", run.TaskType))
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

	lines = append(lines, "", "任务目标:")
	lines = append(lines, firstNonEmpty(trim(run.Goal, 1000), "(未记录目标)"))

	lines = append(lines, "", "任务分解:")
	plans := planNarrative(run)
	if len(plans) == 0 {
		lines = append(lines, "- 未记录结构化计划。")
	} else {
		lines = append(lines, plans...)
	}

	lines = append(lines, "", "执行时间线:")
	timeline := timelineNarrative(run)
	if len(timeline) == 0 {
		lines = append(lines, "- 未记录事件时间线。")
	} else {
		lines = append(lines, timeline...)
	}

	lines = append(lines, "", "工具/模型调用:")
	calls := callNarrative(run)
	if len(calls) == 0 {
		lines = append(lines, "- 未记录工具或模型调用。")
	} else {
		lines = append(lines, calls...)
	}

	lines = append(lines, "", "失败:")
	failures := failureNarrative(run)
	if len(failures) == 0 {
		lines = append(lines, "- 本次 run 没有记录失败事件。")
	} else {
		lines = append(lines, failures...)
	}

	lines = append(lines, "", "恢复动作:")
	recoveries := recoveryNarrative(run)
	if len(recoveries) == 0 {
		lines = append(lines, "- 未触发 runtime 级恢复动作。")
	} else {
		lines = append(lines, recoveries...)
	}

	lines = append(lines, "", "最终结果:")
	lines = append(lines, firstNonEmpty(trim(run.Result, 1500), trim(run.Error, 1500), "(无最终输出)"))

	lines = append(lines, "", "下次策略:")
	lines = append(lines, nextStrategyNarrative(run)...)

	lines = append(lines, "", "已沉淀记忆:")
	memories := learningMemoryNarrative(run)
	if len(memories) == 0 {
		lines = append(lines, "- 尚未沉淀新的学习记忆。")
	} else {
		lines = append(lines, memories...)
	}
	return strings.Join(lines, "\n")
}

func planNarrative(run Run) []string {
	lines := []string{}
	for _, event := range run.Events {
		if event.Phase == "planning" || event.Kind == "plan" || event.Kind == "replan" || event.Kind == "dev_plan" {
			lines = append(lines, fmt.Sprintf("- [%s] %s: %s", firstNonEmpty(event.Status, "-"), event.Kind, trim(firstNonEmpty(event.Output, event.Input), 900)))
		}
	}
	if len(lines) > 0 {
		return lines
	}
	for _, step := range run.Steps {
		if step.Kind == "dev_plan" || step.Kind == "plan" || step.Kind == "replan" {
			lines = append(lines, fmt.Sprintf("- [%s] %s: %s", firstNonEmpty(step.Status, "-"), step.Kind, trim(firstNonEmpty(step.Output, step.Input), 900)))
		}
	}
	return lines
}

func timelineNarrative(run Run) []string {
	events := run.Events
	if len(events) == 0 {
		events = eventsFromSteps(run)
	}
	lines := make([]string, 0, len(events))
	seen := map[string]bool{}
	for _, event := range events {
		if event.Kind == "" || shouldSkipTimelineEvent(event) {
			continue
		}
		line := fmt.Sprintf("- %s [%s/%s] %s%s%s",
			formatEventTime(event),
			firstNonEmpty(event.Phase, "runtime"),
			firstNonEmpty(event.Status, "-"),
			event.Kind,
			eventLabel(event),
			eventSummarySuffix(event, 180),
		)
		if seen[line] {
			continue
		}
		seen[line] = true
		lines = append(lines, line)
	}
	return lines
}

func callNarrative(run Run) []string {
	events := run.Events
	if len(events) == 0 {
		events = eventsFromSteps(run)
	}
	lines := []string{}
	seen := map[string]bool{}
	for _, event := range events {
		if (event.Phase != "model" && event.Phase != "tool") || shouldSkipCallNarrativeEvent(event) {
			continue
		}
		line := fmt.Sprintf("- [%s] %s%s%s", firstNonEmpty(event.Status, "-"), event.Kind, eventLabel(event), eventSummarySuffix(event, 180))
		if seen[line] {
			continue
		}
		seen[line] = true
		lines = append(lines, line)
	}
	return lines
}

func failureNarrative(run Run) []string {
	events := run.Events
	if len(events) == 0 {
		events = eventsFromSteps(run)
	}
	lines := []string{}
	for _, event := range events {
		if event.Status != "failed" && strings.TrimSpace(event.Error) == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s%s: %s", event.Kind, eventLabel(event), trim(firstNonEmpty(event.Error, event.Output), 500)))
	}
	if strings.TrimSpace(run.Error) != "" {
		lines = append(lines, "- run 最终错误："+trim(run.Error, 700))
	}
	return lines
}

func recoveryNarrative(run Run) []string {
	lines := []string{}
	for _, attempt := range run.RecoveryAttempts {
		lines = append(lines, fmt.Sprintf("- [%s] %s -> %s：%s", firstNonEmpty(attempt.Status, "-"), attempt.FailureKind, attempt.Action, trim(attempt.Detail, 500)))
	}
	for _, event := range run.Events {
		if event.Kind == "route_fallback" {
			lines = append(lines, fmt.Sprintf("- [%s] %s", firstNonEmpty(event.Status, "-"), trim(firstNonEmpty(event.Output, event.Message), 500)))
		}
	}
	return lines
}

func nextStrategyNarrative(run Run) []string {
	lines := []string{}
	if strings.TrimSpace(run.Error) != "" {
		lines = append(lines, "- "+failureRecommendation(classifyTurnFailure(errors.New(run.Error)), run, errors.New(run.Error)))
	}
	for _, proposal := range run.LearningProposals {
		lines = append(lines, fmt.Sprintf("- `%s` [%s/%s]: %s", proposal.ID, proposal.Kind, firstNonEmpty(proposal.Status, "proposed"), trim(firstNonEmpty(proposal.Rationale, proposal.Title), 500)))
	}
	if len(lines) == 0 {
		lines = append(lines, "- 保留本次 run 的事件时间线；后续同类任务优先复用成功路径，并在失败时查看 `/learn "+run.ID+"`。")
	}
	return lines
}

func learningMemoryNarrative(run Run) []string {
	lines := []string{}
	for _, event := range run.Events {
		if event.Kind == "learn_proposal" || event.Kind == "learning_proposal" {
			lines = append(lines, "- "+trim(firstNonEmpty(event.Output, event.Message), 700))
		}
	}
	for _, proposal := range run.LearningProposals {
		lines = append(lines, fmt.Sprintf("- `%s` [%s/%s] target=%s", proposal.ID, proposal.Kind, firstNonEmpty(proposal.Status, "proposed"), firstNonEmpty(proposal.TargetPath, "-")))
	}
	return lines
}

func eventsFromSteps(run Run) []RunEvent {
	events := make([]RunEvent, 0, len(run.Steps))
	for _, step := range run.Steps {
		event := runEventFromStep(run, step)
		event.Index = len(events) + 1
		events = append(events, event)
	}
	return events
}

func formatEventTime(event RunEvent) string {
	ts := event.StartedAt
	if ts.IsZero() {
		ts = event.FinishedAt
	}
	if ts.IsZero() {
		return "--:--:--"
	}
	return ts.Format("15:04:05")
}

func eventLabel(event RunEvent) string {
	parts := []string{}
	if event.AgentName != "" {
		parts = append(parts, "agent="+event.AgentName)
	}
	if event.ToolName != "" {
		parts = append(parts, "tool="+event.ToolName)
	}
	if event.ModelName != "" {
		parts = append(parts, "model="+event.ModelName)
	}
	if event.ChildRunID != "" {
		parts = append(parts, "child="+event.ChildRunID)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, " ") + ")"
}

func eventSummarySuffix(event RunEvent, limit int) string {
	value := summarizeEventNarrativeValue(event, limit)
	if value == "" {
		return ""
	}
	return ": " + value
}

func shouldSkipTimelineEvent(event RunEvent) bool {
	switch event.Kind {
	case "llm":
		return true
	default:
		return false
	}
}

func shouldSkipCallNarrativeEvent(event RunEvent) bool {
	switch event.Kind {
	case "run_end", "llm":
		return true
	default:
		return false
	}
}

func summarizeEventNarrativeValue(event RunEvent, limit int) string {
	switch event.Kind {
	case "run_end":
		if strings.EqualFold(event.Status, "completed") {
			return "最终结果已生成"
		}
		return trimInline(firstNonEmpty(event.Error, event.Message, event.Output), limit)
	case "callback_model_end", "llm", "respond", "agent_message":
		source := firstNonEmpty(event.Message, event.Output, event.Input)
		if strings.TrimSpace(source) == "" {
			return ""
		}
		return fmt.Sprintf("生成最终答复（chars=%d）", len([]rune(strings.TrimSpace(source))))
	case "callback_model_start", "callback_tool_start":
		return trimInline(firstNonEmpty(event.Message, event.Output, event.Input), minInt(limit, 120))
	case "callback_tool_end", "tool_result":
		return summarizeForSessionMemory(firstNonEmpty(event.Message, event.Output, event.Input), limit)
	default:
		return trim(firstNonEmpty(event.Message, event.Error, event.Output, event.Input), limit)
	}
}

func (h *Harness) recordFailureLearning(ctx context.Context, run Run, req Request, err error) error {
	failureKind := classifyTurnFailure(err)
	lesson := buildFailureLesson(run, req, failureKind, err)
	taskType := firstNonEmpty(run.TaskType, classifyTaskTypeFromGoal(firstNonEmpty(run.Goal, req.UserText)))
	lessonRecord, lessonErr := h.Memory.AppendLessonRecord(ctx, memory.LessonRecord{
		TaskID:                    run.TaskID,
		TaskType:                  taskType,
		FailureKind:               failureKind,
		ToolName:                  failureToolName(run),
		ProviderName:              failureProviderName(run),
		TriggerSignature:          trimInline(firstNonEmpty(run.Goal, req.UserText), 180),
		PreferredFallback:         failureRecommendation(failureKind, run, err),
		DoNotRepeat:               doNotRepeatGuidance(failureKind, run),
		RecommendedRetrievalOrder: recommendedRetrievalOrder(taskType),
		Summary:                   lesson.Summary,
	})
	if lessonErr == nil && strings.TrimSpace(lessonRecord.ID) != "" {
		record, ok, getErr := h.Memory.GetTaskRecord(ctx, run.TaskID)
		if getErr == nil && ok {
			record.LessonIDs = append(record.LessonIDs, lessonRecord.ID)
			record.LatestFailure = firstNonEmpty(strings.TrimSpace(run.Error), lesson.Summary, record.LatestFailure)
			_, _ = h.Memory.UpsertTaskRecord(ctx, record)
		}
	}
	_ = h.Memory.Append(ctx, "failures", firstNonEmpty(req.SessionKey, run.SessionKey), lesson.Summary, map[string]any{
		"run_id":          run.ID,
		"failure_kind":    failureKind,
		"agent_name":      run.AgentName,
		"route":           run.Route,
		"model_name":      run.ModelName,
		"tool_name":       failureToolName(run),
		"provider_name":   failureProviderName(run),
		"selected_skills": append([]string(nil), run.SelectedSkills...),
		"visible_tools":   append([]string(nil), run.VisibleTools...),
	})
	_, wikiErr := h.Memory.UpsertWikiPage(ctx, memory.WikiPage{
		Title:    "Failure Lesson - " + trimInline(firstNonEmpty(run.Goal, run.ID), 80),
		Category: "notes",
		Slug:     "failure-" + strings.ToLower(strings.TrimSpace(run.ID)),
		Summary:  lesson.Summary,
		Content:  lesson.Content,
		Sources: []string{
			"run:" + run.ID,
			"session:" + firstNonEmpty(req.SessionKey, run.SessionKey),
		},
	})
	return wikiErr
}

type failureLesson struct {
	Summary string
	Content string
}

func buildFailureLesson(run Run, req Request, failureKind string, err error) failureLesson {
	recommendation := failureRecommendation(failureKind, run, err)
	summary := trimInline(fmt.Sprintf("%s | goal=%s | advice=%s", failureKind, firstNonEmpty(run.Goal, req.UserText), recommendation), 220)
	content := strings.Join([]string{
		"## Failure Kind",
		firstNonEmpty(failureKind, "unknown"),
		"",
		"## Goal",
		firstNonEmpty(strings.TrimSpace(run.Goal), strings.TrimSpace(req.UserText), "(missing)"),
		"",
		"## Runtime Context",
		fmt.Sprintf("- route: %s", firstNonEmpty(run.Route, "-")),
		fmt.Sprintf("- model: %s", firstNonEmpty(run.ModelName, "-")),
		fmt.Sprintf("- tool: %s", firstNonEmpty(failureToolName(run), "-")),
		fmt.Sprintf("- provider: %s", firstNonEmpty(failureProviderName(run), "-")),
		fmt.Sprintf("- selected_skills: %s", firstNonEmpty(strings.Join(run.SelectedSkills, ", "), "-")),
		"",
		"## Error",
		strings.TrimSpace(err.Error()),
		"",
		"## Next-time Guidance",
		recommendation,
	}, "\n")
	return failureLesson{Summary: summary, Content: content}
}

func failureRecommendation(failureKind string, run Run, err error) string {
	switch failureKind {
	case "llm_quota_exceeded", "llm_throttled":
		return "优先减少前置模型调用，先用 heuristic skill/tool 选择；如果只是检索型任务，优先本地/记忆/网页工具而不是先做模型规划。遇到 429 时记录 candidate、阶段和 cooldown，等待冷却后再尝试。"
	case "context_overflow":
		return "下次先压缩 session summary 与历史工具结果，只保留最新目标、关键文件和最终输出；恢复时使用短上下文重试，不要原样重放完整历史。"
	case "tool_missing_binary":
		return "下次先做本地 availability check，确认命令是否存在、provider 是否可执行；如果缺失，先查文档或切换 web_search/browser_fetch，不要直接执行该 CLI。"
	case "tool_policy_denied":
		return "下次先读取 provider 允许的 root commands 或能力边界，优先选择允许的命令；如果当前 provider 不支持该动作，直接切换到其他工具链。"
	case "timeout":
		return "下次先缩小外部调用范围，优先更稳定的数据源；如果是网页/搜索超时，自动降级到备用 provider，并避免重复发起同类远程请求。"
	default:
		if strings.Contains(strings.ToLower(err.Error()), "tool_choice") {
			return "当前模型与该执行路线不兼容。下次避免使用 forced tool_choice 路由，优先切换到兼容的 chatmodel 路径。"
		}
		return "保留这次失败上下文，下次优先检查同类工具/模型是否健康，再决定是否继续使用相同路线。"
	}
}

func doNotRepeatGuidance(failureKind string, run Run) string {
	switch failureKind {
	case "tool_missing_binary":
		return "不要在未做本地 availability/path/近似命令检查前，直接回答未安装或不可用。"
	case "tool_policy_denied":
		return "不要反复重试同一条被 provider policy 拒绝的工具路径。"
	case "context_overflow":
		return "不要原样重放超长历史和大工具结果。"
	case "timeout":
		return "不要对同一慢源重复发起相同远程调用。"
	case "llm_quota_exceeded", "llm_throttled":
		return "不要在 cooldown 期间立即重复走同一路线。"
	default:
		if strings.TrimSpace(run.Route) != "" {
			return "不要在没有新证据时重复使用同一失败路线。"
		}
		return "不要重复刚刚失败的执行路径。"
	}
}

func recommendedRetrievalOrder(taskType string) []string {
	switch taskType {
	case TaskTypeSchedule:
		return []string{"task_record", "schedule_state", "schedule_runs", "artifact_index", "session_summary", "raw_history"}
	case TaskTypeLocalCLI:
		return []string{"procedure_hints", "lessons", "task_record", "cli_capability"}
	case TaskTypeDiagnose:
		return []string{"task_record", "linked_runs", "structured_logs", "lessons", "wiki"}
	case TaskTypeCodeWrite:
		return []string{"task_record", "artifact_index", "recent_task_outcomes", "wiki"}
	case TaskTypeResearch:
		return []string{"session_summary", "task_outcomes", "wiki", "external_search"}
	case TaskTypeMemory:
		return []string{"task_record", "lessons", "procedure_hints", "wiki"}
	default:
		return []string{"task_record", "session_summary", "wiki"}
	}
}

func (h *Harness) tryRuntimeRecovery(ctx context.Context, run Run, req Request, generate Generator, cause error) (string, bool, error) {
	failureKind := classifyTurnFailure(cause)
	action := recoveryActionForFailure(failureKind, run)
	if action == "" {
		return "", false, cause
	}
	startedAt := time.Now()
	if !h.canRetryWithRecovery(run, failureKind) {
		h.appendRecoveryAttempt(run.ID, RecoveryAttempt{
			FailureKind: failureKind,
			Action:      action,
			Status:      "skipped",
			Detail:      "recovery recorded but automatic retry is not safe for this mode: " + trim(cause.Error(), 220),
			StartedAt:   startedAt,
			FinishedAt:  time.Now(),
		})
		return "", false, cause
	}
	h.appendRunStep(run.ID, RunStep{
		Kind:       "route_fallback",
		Status:     "completed",
		AgentName:  run.AgentName,
		Input:      failureKind,
		Output:     fmt.Sprintf("%s；上一条错误：%s", action, trim(cause.Error(), 260)),
		StartedAt:  startedAt,
		FinishedAt: time.Now(),
	})
	retryReq := req
	retryReq.Mode = "chat"
	retryReq.UserText = buildRecoveryPrompt(req.UserText, failureKind, action, cause)
	retryReq.Arguments = mergeArguments(req.Arguments, map[string]any{"runtime_route": "chatmodel"})
	run.Route = "chatmodel"
	run.UpdatedAt = time.Now()
	h.saveRun(run)
	result, err := h.runSessionTurn(ctx, run, retryReq.UserText, func(ctx context.Context, history []HistoryMessage, userText string) (string, error) {
		return h.einoChat(ctx, retryReq, run, history, userText)
	})
	status := "completed"
	detail := "runtime recovery succeeded"
	if err != nil {
		status = "failed"
		detail = err.Error()
	}
	h.appendRecoveryAttempt(run.ID, RecoveryAttempt{
		FailureKind: failureKind,
		Action:      action,
		Status:      status,
		Detail:      detail,
		StartedAt:   startedAt,
		FinishedAt:  time.Now(),
	})
	return result, true, err
}

func recoveryActionForFailure(failureKind string, run Run) string {
	switch failureKind {
	case "tool_missing_binary":
		return "切换到不依赖缺失本地命令的工具路线，优先使用 memory/wiki/web_search/browser_fetch 或直接解释可验证结论"
	case "tool_policy_denied":
		return "避开被策略拒绝的 provider 命令，改用允许的工具或检索路线继续任务"
	case "context_overflow":
		return "压缩上下文并用 chatmodel 短上下文重试"
	case "timeout":
		return "缩小范围并降级到 chatmodel 重试一次"
	case "llm_throttled", "llm_quota_exceeded":
		if strings.EqualFold(run.Route, "plan_execute") {
			return "模型受限时跳过复杂 plan_execute，降级到 chatmodel 重试一次"
		}
		return "记录模型受限并依赖候选模型 fallback/cooldown，避免立即重复同一路线"
	default:
		return ""
	}
}

func (h *Harness) canRetryWithRecovery(run Run, failureKind string) bool {
	if !h.EnableEino || !strings.EqualFold(firstNonEmpty(run.Mode, "chat"), "chat") {
		return false
	}
	switch failureKind {
	case "tool_missing_binary", "tool_policy_denied", "context_overflow", "timeout":
		return true
	case "llm_throttled", "llm_quota_exceeded":
		return strings.EqualFold(run.Route, "plan_execute")
	default:
		return false
	}
}

func buildRecoveryPrompt(userText, failureKind, action string, cause error) string {
	base := strings.TrimSpace(userText)
	if base == "" {
		base = "继续完成当前任务。"
	}
	return strings.TrimSpace(fmt.Sprintf(`%s

[RUNTIME_RECOVERY]
failure_kind: %s
recovery_action: %s
previous_error: %s

请继续完成同一个用户任务。不要重复刚才失败的路线；如果某个工具不可用或被策略拒绝，改用当前可用的替代工具、历史记忆、wiki、检索或直接基于已知信息给出可验证结果。`, base, failureKind, action, trim(cause.Error(), 500)))
}

func (h *Harness) buildFailureAvoidanceHint(goal string, selectedSkillNames []string, visibleToolNames []string) string {
	if h.Memory.Workspace == "" || strings.TrimSpace(goal) == "" {
		return ""
	}
	taskType := classifyTaskTypeFromGoal(goal)
	lessonMatches := make([]string, 0, 3)
	if lessons, err := h.Memory.RecentLessons(context.Background(), taskType, 12); err == nil && len(lessons) > 0 {
		for _, lesson := range lessons {
			if len(lessonMatches) >= 3 {
				break
			}
			if lessonHintMatch(lesson, goal, selectedSkillNames, visibleToolNames) {
				lessonMatches = append(lessonMatches, renderLessonHint(lesson))
			}
		}
	}
	if len(lessonMatches) > 0 {
		lines := []string{
			"## FAILURE_MEMORY",
			"Recent similar lessons were recorded. Avoid repeating the same mistake; if a similar tool or provider fails again, switch strategy instead of returning the raw tool error.",
		}
		for _, item := range lessonMatches {
			lines = append(lines, "- "+item)
		}
		return strings.Join(lines, "\n")
	}
	notes, err := h.Memory.Recent(context.Background(), "failures", "", 12)
	if err != nil || len(notes) == 0 {
		return ""
	}
	goalText := strings.ToLower(strings.TrimSpace(goal))
	matches := make([]string, 0, 3)
	for _, note := range notes {
		if len(matches) >= 3 {
			break
		}
		if failureHintMatch(note, goalText, selectedSkillNames, visibleToolNames) {
			matches = append(matches, renderFailureHint(note))
		}
	}
	if len(matches) == 0 {
		return ""
	}
	lines := []string{
		"## FAILURE_MEMORY",
		"Recent similar failures were recorded. Avoid repeating the same mistake; if a similar tool or provider fails again, switch strategy instead of returning the raw tool error.",
	}
	for _, item := range matches {
		lines = append(lines, "- "+item)
	}
	return strings.Join(lines, "\n")
}

func lessonHintMatch(lesson memory.LessonRecord, goal string, selectedSkillNames []string, visibleToolNames []string) bool {
	goalText := strings.ToLower(strings.TrimSpace(goal))
	if lesson.TaskType != "" && strings.EqualFold(strings.TrimSpace(lesson.TaskType), classifyTaskTypeFromGoal(goal)) {
		return true
	}
	for _, value := range []string{lesson.ToolName, lesson.ProviderName, lesson.TriggerSignature, lesson.Summary, lesson.DoNotRepeat} {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" && strings.Contains(value, goalText) {
			return true
		}
	}
	if containsAny(strings.ToLower(lesson.Summary), selectedSkillNames...) || containsAny(strings.ToLower(lesson.Summary), visibleToolNames...) {
		return true
	}
	return false
}

func renderLessonHint(lesson memory.LessonRecord) string {
	parts := []string{trimInline(firstNonEmpty(lesson.Summary, lesson.DoNotRepeat), 180)}
	if lesson.FailureKind != "" {
		parts = append(parts, "kind="+lesson.FailureKind)
	}
	if lesson.ToolName != "" {
		parts = append(parts, "tool="+lesson.ToolName)
	}
	if lesson.ProviderName != "" {
		parts = append(parts, "provider="+lesson.ProviderName)
	}
	if lesson.PreferredFallback != "" {
		parts = append(parts, "fallback="+trimInline(lesson.PreferredFallback, 120))
	}
	return strings.Join(parts, " | ")
}

func failureHintMatch(note memory.Note, goalText string, selectedSkillNames []string, visibleToolNames []string) bool {
	content := strings.ToLower(strings.TrimSpace(note.Content))
	if content == "" {
		return false
	}
	if strings.Contains(content, goalText) {
		return true
	}
	if metadataMatchesAny(note.Metadata, "tool_name", visibleToolNames...) {
		return true
	}
	if metadataMatchesAny(note.Metadata, "provider_name", visibleToolNames...) {
		return true
	}
	if metadataMatchesAny(note.Metadata, "selected_skills", selectedSkillNames...) {
		return true
	}
	if containsAny(content, selectedSkillNames...) || containsAny(content, visibleToolNames...) {
		return true
	}
	return false
}

func metadataMatchesAny(metadata map[string]any, key string, values ...string) bool {
	if len(metadata) == 0 {
		return false
	}
	targets := make([]string, 0, len(values)*3)
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		targets = append(targets, value)
		targets = append(targets, strings.TrimSuffix(value, "_run"))
		targets = append(targets, strings.TrimSuffix(value, "_list"))
	}
	if len(targets) == 0 {
		return false
	}
	for _, actual := range metadataStrings(metadata[key]) {
		actual = strings.ToLower(strings.TrimSpace(actual))
		for _, target := range targets {
			if actual != "" && (actual == target || strings.Contains(actual, target) || strings.Contains(target, actual)) {
				return true
			}
		}
	}
	return false
}

func metadataStrings(value any) []string {
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return []string{v}
	case []string:
		return append([]string(nil), v...)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func renderFailureHint(note memory.Note) string {
	parts := []string{trimInline(note.Content, 180)}
	if kind := firstMetadataString(note.Metadata, "failure_kind"); kind != "" {
		parts = append(parts, "kind="+kind)
	}
	if tool := firstMetadataString(note.Metadata, "tool_name"); tool != "" {
		parts = append(parts, "tool="+tool)
	}
	if provider := firstMetadataString(note.Metadata, "provider_name"); provider != "" {
		parts = append(parts, "provider="+provider)
	}
	return strings.Join(parts, " | ")
}

func firstMetadataString(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	values := metadataStrings(metadata[key])
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

func containsString(values []string, target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	for _, value := range values {
		if strings.TrimSpace(value) == target {
			return true
		}
	}
	return false
}

func failureToolName(run Run) string {
	if strings.TrimSpace(run.ToolName) != "" {
		return strings.TrimSpace(run.ToolName)
	}
	if tool := parseToolNameFromRunError(run.Error); tool != "" {
		return tool
	}
	for i := len(run.Steps) - 1; i >= 0; i-- {
		if strings.TrimSpace(run.Steps[i].ToolName) != "" {
			return strings.TrimSpace(run.Steps[i].ToolName)
		}
	}
	for _, skillName := range run.SelectedSkills {
		candidate := strings.TrimSpace(skillName) + "_run"
		if containsString(run.VisibleTools, candidate) {
			return candidate
		}
	}
	return ""
}

func failureProviderName(run Run) string {
	tool := failureToolName(run)
	if tool == "" {
		return ""
	}
	for _, suffix := range []string{"_run", "_list"} {
		if strings.HasSuffix(tool, suffix) {
			return strings.TrimSuffix(tool, suffix)
		}
	}
	return tool
}

func parseToolNameFromRunError(value string) string {
	value = strings.TrimSpace(value)
	start := strings.Index(value, "tool[name:")
	if start < 0 {
		return ""
	}
	start += len("tool[name:")
	end := strings.Index(value[start:], " ")
	if end < 0 {
		end = strings.Index(value[start:], "]")
	}
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(value[start : start+end])
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
	if strings.TrimSpace(run.TaskType) != "" {
		lines = append(lines, "当前任务类型："+run.TaskType)
	}
	if strings.EqualFold(run.Route, "plan_execute") {
		lines = append(lines, "2. 先生成结构化计划，再按 planner -> executor -> replanner 推进。")
	} else {
		lines = append(lines, "2. 先快速判断是否需要检索、读取历史、调用工具或直接回答。")
	}
	if goalSuggestsScheduleInspection(goal) && (hasVisibleTool(run.VisibleTools, "schedule_list") || hasVisibleTool(run.VisibleTools, "schedule_get")) {
		lines = append(lines, "优先规则：如果是在查看现有定时任务、执行状态或运行成果，先用 schedule_list / schedule_get 直接核对，不要先靠记忆猜测。")
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
		lines = append(lines, "7. 若使用外接 CLI，先看 provider allowlist；若命令依赖真实本地环境、登录态、cookie 或 daemon，被拒绝后优先回退到 exec。")
	}
	if hasVisibleTool(run.VisibleTools, "exec") {
		lines = append(lines, "8. 对依赖 HOME、TMPDIR、浏览器会话、桌面应用或本地 daemon 的命令，优先用 exec。")
	}
	if hasVisibleTool(run.VisibleTools, "sandbox_exec") {
		lines = append(lines, "9. 只有在需要隔离、无状态的命令验证、脚本检查或最小实验时才用 sandbox_exec。")
	}
	if cliHint := buildCLIExplorationHint(goal, run.VisibleTools); strings.TrimSpace(cliHint) != "" {
		lines = append(lines, "10. 当前任务属于 CLI 学习/探查场景，应先本地检查命令帮助与版本，再决定是否回退到网页资料。")
	}
	if hasVisibleTool(run.VisibleTools, "spawn") {
		lines = append(lines, "11. 如果任务明显可并行或需要专门角色，再考虑 spawn 子 agent。")
	}
	if hasVisibleTool(run.VisibleTools, "schedule_create") || hasVisibleTool(run.VisibleTools, "schedule_update") || hasVisibleTool(run.VisibleTools, "schedule_list") {
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

func (h *Harness) taskResetBoundary(sessionKey string) time.Time {
	if h.Sessions == nil || strings.TrimSpace(sessionKey) == "" {
		return time.Time{}
	}
	prefs, err := h.Sessions.LoadPreferences(sessionKey)
	if err != nil {
		return time.Time{}
	}
	return prefs.LastResetAt
}

func (h *Harness) allRuns() ([]Run, error) {
	h.mu.RLock()
	merged := make(map[string]Run, len(h.runs))
	for id, run := range h.runs {
		merged[id] = run
	}
	h.mu.RUnlock()

	if strings.TrimSpace(h.Workspace) != "" {
		dir := filepath.Join(h.Workspace, "memory", "runs")
		entries, err := os.ReadDir(dir)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
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
			current, ok := merged[run.ID]
			if !ok || run.UpdatedAt.After(current.UpdatedAt) {
				merged[run.ID] = run
			}
		}
	}

	runs := make([]Run, 0, len(merged))
	for _, run := range merged {
		runs = append(runs, run)
	}
	return runs, nil
}

func (h *Harness) ListRuns(_ context.Context, sessionKey string, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = 10
	}
	runs, err := h.allRuns()
	if err != nil {
		return nil, err
	}
	filtered := make([]Run, 0, len(runs))
	for _, run := range runs {
		if strings.TrimSpace(sessionKey) != "" && strings.TrimSpace(run.SessionKey) != strings.TrimSpace(sessionKey) {
			continue
		}
		filtered = append(filtered, run)
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].CreatedAt.After(filtered[j].CreatedAt)
	})
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

func (h *Harness) ListTaskRuns(_ context.Context, sessionKey string, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = 10
	}
	resetAt := h.taskResetBoundary(sessionKey)
	allRuns, err := h.allRuns()
	if err != nil {
		return nil, err
	}
	byTask := map[string]Run{}
	for _, run := range allRuns {
		if strings.TrimSpace(sessionKey) != "" && strings.TrimSpace(run.SessionKey) != strings.TrimSpace(sessionKey) {
			continue
		}
		if !resetAt.IsZero() && run.CreatedAt.Before(resetAt) {
			continue
		}
		if !isTopLevelTaskRun(run) {
			continue
		}
		key := taskGroupingKey(run)
		prev, ok := byTask[key]
		if !ok || run.UpdatedAt.After(prev.UpdatedAt) {
			byTask[key] = run
		}
	}
	runs := make([]Run, 0, len(byTask))
	for _, run := range byTask {
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].UpdatedAt.After(runs[j].UpdatedAt)
	})
	if len(runs) > limit {
		runs = runs[:limit]
	}
	return runs, nil
}

func isTopLevelTaskRun(run Run) bool {
	return strings.EqualFold(strings.TrimSpace(firstNonEmpty(run.Mode, "chat")), "chat") && strings.TrimSpace(run.ParentRunID) == ""
}

func taskGroupingKey(run Run) string {
	if taskID := strings.TrimSpace(run.TaskID); taskID != "" {
		return taskID
	}
	if isTopLevelTaskRun(run) {
		return run.ID
	}
	return ""
}

func FormatTaskDigest(run Run) string {
	goal := summarizeForSessionMemory(firstNonEmpty(run.Goal, run.ToolName, run.Mode), 90)
	if goal == "" {
		goal = "(未记录任务目标)"
	}
	outcome := summarizeForSessionMemory(firstNonEmpty(run.Result, run.Error), 150)
	if outcome == "" {
		if strings.EqualFold(run.Status, "waiting_approval") {
			outcome = "等待批准"
		} else {
			outcome = "暂无结果摘要"
		}
	}
	return fmt.Sprintf("[%s] %s -> %s", firstNonEmpty(strings.TrimSpace(run.Status), "-"), goal, outcome)
}

func (h *Harness) ListVisibleTools(ctx context.Context, scope tools.Scope) ([]tools.Spec, error) {
	if h.Tools == nil {
		return nil, nil
	}
	items, err := h.Tools.List(ctx, scope)
	if err != nil {
		return nil, err
	}
	effective, err := h.compileCapabilities(ctx, scope)
	if err != nil {
		return nil, err
	}
	out := make([]tools.Spec, 0, len(items))
	for _, item := range items {
		spec := item.Spec()
		if capabilities.Allows(effective, spec.Name) {
			if reporter, ok := item.(tools.AvailabilityReporter); ok {
				availability := reporter.Availability(ctx)
				if !availability.Available {
					continue
				}
			}
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
			TaskID:     run.TaskID,
			SessionKey: req.SessionKey,
			ThreadID:   req.ThreadID,
			UserID:     req.UserID,
			Channel:    req.Channel,
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
		return FormatPendingApproval(pending), nil
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
			TaskID:     run.TaskID,
			SessionKey: req.SessionKey,
			ThreadID:   req.ThreadID,
			UserID:     req.UserID,
			Channel:    req.Channel,
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
		return FormatPendingApproval(pending), nil
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
	if h.runs == nil {
		h.runs = make(map[string]Run)
	}
	h.runs[run.ID] = run
	h.mu.Unlock()
	_ = h.persistRun(run)
	_ = h.syncTaskRecordFromRun(context.Background(), run)
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
	event := runEventFromStep(run, step)
	event.Index = len(run.Events) + 1
	run.Events = append(run.Events, event)
	run.UpdatedAt = time.Now()
	h.runs[runID] = run
	h.mu.Unlock()
	_ = h.persistRun(run)
	h.logRunEvent(run, event)
}

func (h *Harness) appendRunEvent(runID string, event RunEvent) {
	h.mu.Lock()
	run, ok := h.runs[runID]
	if !ok {
		h.mu.Unlock()
		return
	}
	fillRunEventDefaults(&event, run)
	event.Index = len(run.Events) + 1
	run.Events = append(run.Events, event)
	run.UpdatedAt = time.Now()
	h.runs[runID] = run
	h.mu.Unlock()
	_ = h.persistRun(run)
	h.logRunEvent(run, event)
}

func (h *Harness) appendRecoveryAttempt(runID string, attempt RecoveryAttempt) {
	h.mu.Lock()
	run, ok := h.runs[runID]
	if !ok {
		h.mu.Unlock()
		return
	}
	if attempt.StartedAt.IsZero() {
		attempt.StartedAt = time.Now()
	}
	if attempt.FinishedAt.IsZero() {
		attempt.FinishedAt = attempt.StartedAt
	}
	attempt.Index = len(run.RecoveryAttempts) + 1
	run.RecoveryAttempts = append(run.RecoveryAttempts, attempt)
	event := RunEvent{
		Kind:       "recovery_attempt",
		Status:     attempt.Status,
		Phase:      "recovery",
		Message:    attempt.Action,
		Output:     attempt.Detail,
		Error:      map[bool]string{true: attempt.Detail, false: ""}[attempt.Status == "failed"],
		StartedAt:  attempt.StartedAt,
		FinishedAt: attempt.FinishedAt,
		Metadata: map[string]any{
			"failure_kind":        attempt.FailureKind,
			"trigger_event_index": attempt.TriggerEventIndex,
			"attempt_index":       attempt.Index,
		},
	}
	fillRunEventDefaults(&event, run)
	event.Index = len(run.Events) + 1
	run.Events = append(run.Events, event)
	run.UpdatedAt = time.Now()
	h.runs[runID] = run
	h.mu.Unlock()
	_ = h.persistRun(run)
	h.logRunEvent(run, event)
}

func runEventFromStep(run Run, step RunStep) RunEvent {
	event := RunEvent{
		Kind:       step.Kind,
		Status:     firstNonEmpty(step.Status, "completed"),
		Phase:      phaseForStep(step.Kind),
		AgentName:  step.AgentName,
		ToolName:   step.ToolName,
		Input:      step.Input,
		Output:     step.Output,
		StartedAt:  step.StartedAt,
		FinishedAt: step.FinishedAt,
	}
	if event.Status == "failed" {
		event.Error = step.Output
	}
	fillRunEventDefaults(&event, run)
	return event
}

func fillRunEventDefaults(event *RunEvent, run Run) {
	if event.StartedAt.IsZero() {
		event.StartedAt = time.Now()
	}
	if event.FinishedAt.IsZero() {
		event.FinishedAt = event.StartedAt
	}
	event.RunID = firstNonEmpty(event.RunID, run.ID)
	event.SessionKey = firstNonEmpty(event.SessionKey, run.SessionKey)
	event.ThreadID = firstNonEmpty(event.ThreadID, run.ThreadID)
	event.Channel = firstNonEmpty(event.Channel, run.Channel)
	event.AgentName = firstNonEmpty(event.AgentName, run.AgentName)
	event.ModelName = firstNonEmpty(event.ModelName, run.ModelName)
	event.ParentRunID = firstNonEmpty(event.ParentRunID, run.ParentRunID)
}

func phaseForStep(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "dev_plan", "plan", "replan":
		return "planning"
	case "model_attempt", "callback_model_start", "callback_model_end", "callback_model_error", "llm", "respond", "agent_message":
		return "model"
	case "tool", "tool_choice", "tool_result", "tool_search", "tool_offload", "tool_reduction", "callback_tool_start", "callback_tool_end", "callback_tool_error":
		return "tool"
	case "route_fallback":
		return "recovery"
	case "channel_notify":
		return "channel"
	case "transfer":
		return "agent"
	case "learn_proposal", "learning_proposal":
		return "learning"
	case "context_compaction", "middleware_summarization", "middleware_summarization_attempt":
		return "memory"
	default:
		return "runtime"
	}
}

func (h *Harness) logRunEvent(run Run, event RunEvent) {
	_ = observability.Append(h.Workspace, observability.LogEvent{
		Level:      map[bool]string{true: "error", false: "info"}[strings.EqualFold(event.Status, "failed")],
		Component:  "harness",
		Type:       event.Kind,
		RunID:      run.ID,
		SessionKey: run.SessionKey,
		ThreadID:   run.ThreadID,
		Channel:    run.Channel,
		AgentName:  run.AgentName,
		Status:     event.Status,
		Message:    firstNonEmpty(event.Message, event.Output, event.Error),
		Metadata: map[string]any{
			"phase":     event.Phase,
			"tool_name": event.ToolName,
			"model":     event.ModelName,
			"index":     event.Index,
		},
	})
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

func (h *Harness) recordModelAttempt(runID, purpose, provider, modelName, status, detail, limiterState string, startedAt, finishedAt time.Time) {
	if strings.TrimSpace(runID) == "" {
		return
	}
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	if finishedAt.IsZero() {
		finishedAt = startedAt
	}
	h.mu.Lock()
	run, ok := h.runs[runID]
	if !ok {
		h.mu.Unlock()
		return
	}
	run.ModelAttempts++
	if status == "rate_limited" {
		run.Model429Count++
	}
	step := RunStep{
		Index:      len(run.Steps) + 1,
		Kind:       "model_attempt",
		Status:     status,
		AgentName:  run.AgentName,
		Input:      trim(firstNonEmpty(purpose, "runtime"), 120),
		Output:     trim(fmt.Sprintf("provider=%s candidate=%s detail=%s limiter=%s", firstNonEmpty(provider, "-"), firstNonEmpty(modelName, "-"), firstNonEmpty(detail, "-"), firstNonEmpty(limiterState, "-")), 400),
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
	}
	run.Steps = append(run.Steps, step)
	event := runEventFromStep(run, step)
	event.Index = len(run.Events) + 1
	event.Phase = "model"
	event.ModelName = firstNonEmpty(modelName, run.ModelName)
	event.Metadata = map[string]any{
		"purpose":       firstNonEmpty(purpose, "runtime"),
		"provider":      provider,
		"candidate":     modelName,
		"limiter_state": limiterState,
	}
	run.Events = append(run.Events, event)
	run.UpdatedAt = time.Now()
	h.runs[runID] = run
	h.mu.Unlock()
	_ = h.persistRun(run)
	h.logRunEvent(run, event)
}

func (h *Harness) updateRunModelUsage(runID, modelName string, promptTokens, completionTokens, totalTokens int, estimatedCostUSD float64, duration time.Duration) {
	if strings.TrimSpace(runID) == "" {
		return
	}
	h.mu.Lock()
	run, ok := h.runs[runID]
	if !ok {
		h.mu.Unlock()
		return
	}
	if strings.TrimSpace(modelName) != "" {
		run.ModelName = strings.TrimSpace(modelName)
	}
	run.PromptTokens += maxInt(promptTokens, 0)
	run.CompletionTokens += maxInt(completionTokens, 0)
	run.TotalTokens += maxInt(totalTokens, 0)
	if estimatedCostUSD > 0 {
		run.EstimatedCostUSD += estimatedCostUSD
	}
	if duration > 0 {
		run.ModelDurationMs += duration.Milliseconds()
	}
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
	event := runEventFromStep(run, step)
	event.Index = len(run.Events) + 1
	run.Events = append(run.Events, event)
	run.Status = "waiting_approval"
	run.LastApprovalID = pending.ID
	run.ApprovalIDs = appendUnique(run.ApprovalIDs, pending.ID)
	run.UpdatedAt = time.Now()
	h.runs[runID] = run
	h.mu.Unlock()
	_ = h.persistRun(run)
	h.logRunEvent(run, event)
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
	taskType := resolveTaskType(req)
	if h.EnableEino && h.shouldAvoidPlanExecuteForConfiguredModel() {
		return "chatmodel"
	}
	switch taskType {
	case TaskTypeResearch, TaskTypeDiagnose:
		return "plan_execute"
	case TaskTypeSchedule, TaskTypeTool, TaskTypeLocalCLI, TaskTypeMemory, TaskTypeDelegate:
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
			ID:                fmt.Sprintf("run_%d", time.Now().UnixNano()),
			SessionKey:        childReq.SessionKey,
			ThreadID:          strings.TrimSpace(firstNonEmpty(childReq.ThreadID, childReq.SessionKey)),
			UserID:            strings.TrimSpace(childReq.UserID),
			Channel:           strings.TrimSpace(childReq.Channel),
			AgentName:         childReq.AgentName,
			Goal:              strings.TrimSpace(childReq.UserText),
			Mode:              childReq.Mode,
			CollaborationMode: collaborationMode,
			ToolName:          childReq.ToolName,
			Status:            "queued",
			ParentRunID:       parent.ID,
			CreatedAt:         time.Now(),
			UpdatedAt:         time.Now(),
			Capabilities:      childCaps,
		}
		parent.ChildRunIDs = appendUnique(parent.ChildRunIDs, childRun.ID)
		parent.LastChildRunID = childRun.ID
		parent.LastChildStatus = "queued"
		parent.UpdatedAt = time.Now()
		h.saveRun(parent)
		h.saveRun(childRun)
		h.appendRunEvent(parent.ID, RunEvent{
			Kind:       "child_run_queued",
			Status:     "queued",
			Phase:      "agent",
			ChildRunID: childRun.ID,
			Message:    "async child run queued",
			Output:     trim(childRun.Goal, 300),
			StartedAt:  childRun.CreatedAt,
			FinishedAt: childRun.CreatedAt,
		})
		h.appendRunEvent(childRun.ID, RunEvent{
			Kind:       "run_start",
			Status:     "queued",
			Phase:      "runtime",
			Input:      trim(childRun.Goal, 400),
			StartedAt:  childRun.CreatedAt,
			FinishedAt: childRun.CreatedAt,
		})
		go h.runAsyncChild(childRun, childReq, generate)
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
	h.appendRunEvent(run.ID, RunEvent{
		Kind:      "run_start",
		Status:    "started",
		Phase:     "runtime",
		Input:     trim(req.UserText, 400),
		StartedAt: run.UpdatedAt,
	})

	var (
		result string
		err    error
	)
	switch strings.TrimSpace(req.Mode) {
	case "", "chat":
		if !h.EnableEino {
			err = fmt.Errorf("chat runtime requires Eino; call UseEinoRuntime before starting chat runs")
		} else {
			result, err = h.runSessionTurn(context.Background(), run, req.UserText, func(ctx context.Context, history []HistoryMessage, userText string) (string, error) {
				return h.einoChat(ctx, req, run, history, userText)
			})
		}
	case "tool":
		result, err = h.runTool(context.Background(), run, req, generate)
	default:
		err = fmt.Errorf("unsupported harness mode %q", req.Mode)
	}
	if err != nil {
		if recoveredResult, recovered, recoveryErr := h.tryRuntimeRecovery(context.Background(), run, req, generate, err); recovered {
			if recoveryErr == nil {
				result = recoveredResult
				err = nil
			} else {
				err = recoveryErr
			}
		}
	}
	run = h.mustGetRun(seed.ID)
	if err != nil {
		run.Status = "failed"
		run.Error = err.Error()
		_ = h.recordFailureLearning(context.Background(), run, req, err)
	} else {
		run.Status = "completed"
		run.Result = strings.TrimSpace(result)
	}
	run.UpdatedAt = time.Now()
	h.saveRun(run)
	endStatus := "completed"
	endOutput := trim(run.Result, 500)
	if run.Status == "failed" {
		endStatus = "failed"
		endOutput = ""
	}
	h.appendRunEvent(run.ID, RunEvent{
		Kind:       "run_end",
		Status:     endStatus,
		Phase:      "runtime",
		Output:     endOutput,
		Error:      run.Error,
		StartedAt:  run.UpdatedAt,
		FinishedAt: run.UpdatedAt,
	})
	if run.Status == "completed" {
		_ = h.refreshSessionSummary(context.Background(), run)
		_ = h.maybeWriteLearnProposal(context.Background(), run, req)
		run = h.mustGetRun(seed.ID)
	}
	h.notifyParent(run)
	h.notifyAsyncResult(context.Background(), run, req)
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
	h.appendRunEvent(parent.ID, RunEvent{
		Kind:       "child_run_completed",
		Status:     child.Status,
		Phase:      "agent",
		ChildRunID: child.ID,
		Output:     trim(firstNonEmpty(child.Result, child.Error), 500),
		Error:      child.Error,
		StartedAt:  child.UpdatedAt,
		FinishedAt: child.UpdatedAt,
	})
	_ = h.Memory.Append(context.Background(), "runs", parent.SessionKey, fmt.Sprintf("child_run=%s status=%s result=%s", child.ID, child.Status, trim(child.Result, 200)), map[string]any{
		"run_id":        parent.ID,
		"child_run_id":  child.ID,
		"child_status":  child.Status,
		"parent_run_id": parent.ID,
	})
}

func (h *Harness) notifyAsyncResult(ctx context.Context, run Run, req Request) {
	channel := firstNonEmpty(run.Channel, req.Channel)
	notifier := h.channelNotifier(channel)
	if notifier == nil {
		return
	}
	event := AsyncResultEvent{
		Channel:     strings.TrimSpace(channel),
		SessionKey:  firstNonEmpty(run.SessionKey, req.SessionKey),
		ThreadID:    firstNonEmpty(run.ThreadID, req.ThreadID, req.SessionKey),
		UserID:      firstNonEmpty(run.UserID, req.UserID),
		RunID:       run.ID,
		ParentRunID: firstNonEmpty(run.ParentRunID, req.ParentRunID),
		AgentName:   run.AgentName,
		Goal:        firstNonEmpty(run.Goal, req.UserText),
		Status:      run.Status,
		Result:      run.Result,
		Error:       run.Error,
		CreatedAt:   run.CreatedAt,
		UpdatedAt:   run.UpdatedAt,
	}
	startedAt := time.Now()
	status := "completed"
	output := fmt.Sprintf("channel=%s thread=%s run=%s", event.Channel, event.ThreadID, event.RunID)
	if err := notifier.NotifyAsyncResult(ctx, event); err != nil {
		status = "failed"
		output = err.Error()
	}
	h.appendRunStep(run.ID, RunStep{
		Kind:       "channel_notify",
		Status:     status,
		AgentName:  run.AgentName,
		Output:     trim(output, 500),
		StartedAt:  startedAt,
		FinishedAt: time.Now(),
	})
}

func (h *Harness) channelNotifier(channel string) AsyncResultNotifier {
	channel = strings.TrimSpace(strings.ToLower(channel))
	if channel == "" {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.notifiers[channel]
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
	case "spawn", "schedule_create", "schedule_update", "schedule_enable", "schedule_disable", "schedule_remove":
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
	case "schedule_create", "schedule_update", "schedule_enable", "schedule_disable", "schedule_remove":
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
	return h.RefreshSessionSummaryForSession(ctx, run.SessionKey)
}

func (h *Harness) RefreshSessionSummaryForSession(ctx context.Context, sessionKey string) error {
	sessionKey = strings.TrimSpace(sessionKey)
	if h.Sessions == nil || sessionKey == "" {
		return nil
	}
	recentMessages, err := h.Sessions.LoadRecent(sessionKey, 8)
	if err != nil {
		return err
	}
	recentTasks, err := h.Memory.RecentTaskRecordsBySession(ctx, sessionKey, 2)
	if err != nil {
		return err
	}
	parts := make([]string, 0, 3)
	if exchange := summarizeSessionExchange(recentMessages); exchange != "" {
		parts = append(parts, "最新对话: "+exchange)
	}
	if len(recentTasks) > 0 {
		parts = append(parts, "最新任务: "+formatTaskRecordDigest(recentTasks[0]))
	}
	if len(recentTasks) > 1 {
		parts = append(parts, "上一任务: "+formatTaskRecordDigest(recentTasks[1]))
	}
	if len(parts) == 0 {
		return nil
	}
	summary := strings.Join(parts, "\n")
	metadata := map[string]any{}
	if len(recentTasks) > 0 {
		metadata["task_id"] = recentTasks[0].TaskID
		metadata["task_type"] = recentTasks[0].TaskType
		metadata["agent_name"] = recentTasks[0].AgentName
		metadata["status"] = recentTasks[0].Status
		metadata["thread_id"] = recentTasks[0].ThreadID
		metadata["latest_task_digest"] = formatTaskRecordDigest(recentTasks[0])
		metadata["latest_task_id"] = recentTasks[0].TaskID
		metadata["primary_artifact"] = artifactPathOrRef(recentTasks[0].Completion.PrimaryArtifact)
		metadata["delivery_status"] = firstNonEmpty(recentTasks[0].DeliveryStatus, recentTasks[0].Completion.DeliveryStatus)
		if len(recentTasks[0].SourceRunIDs) > 0 {
			metadata["run_id"] = recentTasks[0].SourceRunIDs[len(recentTasks[0].SourceRunIDs)-1]
			metadata["latest_task_run_id"] = recentTasks[0].SourceRunIDs[len(recentTasks[0].SourceRunIDs)-1]
		}
	}
	if len(recentTasks) > 1 {
		metadata["previous_task_digest"] = formatTaskRecordDigest(recentTasks[1])
		metadata["previous_task_id"] = recentTasks[1].TaskID
		if len(recentTasks[1].SourceRunIDs) > 0 {
			metadata["previous_task_run_id"] = recentTasks[1].SourceRunIDs[len(recentTasks[1].SourceRunIDs)-1]
		}
	}
	return h.Memory.WriteSessionSummary(ctx, sessionKey, summary, metadata)
}

func summarizeForSessionMemory(value string, max int) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	lines := strings.Split(textutil.CleanBlock(value, 0), "\n")
	for _, line := range lines {
		line = trimInline(line, max)
		if line != "" && !strings.HasPrefix(line, "|---") {
			return line
		}
	}
	return trimInline(value, max)
}

func summarizeSessionExchange(messages []session.Message) string {
	if len(messages) == 0 {
		return ""
	}
	last := messages[len(messages)-1]
	if len(messages) >= 2 {
		prev := messages[len(messages)-2]
		if strings.EqualFold(prev.Role, "user") && strings.EqualFold(last.Role, "assistant") {
			return fmt.Sprintf("user: %s | assistant: %s",
				summarizeForSessionMemory(prev.Content, 80),
				summarizeForSessionMemory(last.Content, 120),
			)
		}
	}
	return fmt.Sprintf("%s: %s", last.Role, summarizeForSessionMemory(last.Content, 140))
}

func (h *Harness) runSessionTurn(ctx context.Context, run Run, userText string, generate Generator) (string, error) {
	sessionKey := strings.TrimSpace(run.SessionKey)
	userText = strings.TrimSpace(userText)
	if sessionKey == "" || userText == "" || generate == nil {
		return "", nil
	}
	for {
		marker := time.Now()
		if existing, loaded := h.inflight.LoadOrStore(sessionKey, marker); loaded {
			if startedAt, ok := existing.(time.Time); ok && time.Since(startedAt) > staleSessionBusyTTL {
				h.inflight.Delete(sessionKey)
				continue
			}
			return "", ErrSessionBusy
		}
		break
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
	history = h.maybeCompactSessionHistory(ctx, run, history)

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

func (h *Harness) activeSessionInflight(sessionKey string) (time.Time, bool) {
	value, ok := h.inflight.Load(sessionKey)
	if !ok {
		return time.Time{}, false
	}
	startedAt, ok := value.(time.Time)
	if !ok {
		return time.Time{}, true
	}
	if time.Since(startedAt) > staleSessionBusyTTL {
		h.inflight.Delete(sessionKey)
		return time.Time{}, false
	}
	return startedAt, true
}

func (h *Harness) maybeCompactSessionHistory(ctx context.Context, run Run, history []HistoryMessage) []HistoryMessage {
	if len(history) == 0 || h.Memory.Workspace == "" || strings.TrimSpace(run.SessionKey) == "" {
		return history
	}
	needsCompaction := len(history) >= maxInt(6, h.limit()-1)
	if !needsCompaction {
		return history
	}
	note, ok, err := h.Memory.ReadSessionSummary(ctx, run.SessionKey)
	if err != nil || !ok || strings.TrimSpace(note.Content) == "" {
		return history
	}
	retain := maxInt(4, h.limit()/2)
	if retain > len(history) {
		retain = len(history)
	}
	compacted := make([]HistoryMessage, 0, retain+1)
	compacted = append(compacted, HistoryMessage{
		Role:    "system",
		Content: "Session summary: " + strings.TrimSpace(note.Content),
	})
	compacted = append(compacted, history[len(history)-retain:]...)
	h.appendRunStep(run.ID, RunStep{
		Kind:       "context_compaction",
		Status:     "completed",
		AgentName:  run.AgentName,
		Output:     trim(fmt.Sprintf("history=%d retained=%d summary_note=%s", len(history), retain, firstNonEmpty(firstMetadataString(note.Metadata, "run_id"), "session_summary")), 240),
		StartedAt:  time.Now(),
		FinishedAt: time.Now(),
	})
	var current Run
	var exists bool
	h.mu.Lock()
	current, exists = h.runs[run.ID]
	if exists {
		current.ContextCompactions++
		current.UpdatedAt = time.Now()
		h.runs[run.ID] = current
	}
	h.mu.Unlock()
	if exists {
		_ = h.persistRun(current)
	}
	return compacted
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

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func classifyTurnFailure(err error) string {
	if errors.Is(err, ErrSessionBusy) {
		return "session_busy"
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case llm.LooksLikeQuotaExceeded(err):
		return "llm_quota_exceeded"
	case strings.Contains(msg, "429"), strings.Contains(msg, "throttling"), strings.Contains(msg, "quota exceeded"), strings.Contains(msg, "cooling down"):
		return "llm_throttled"
	case strings.Contains(msg, "executable file not found"), strings.Contains(msg, "exit status 127"):
		return "tool_missing_binary"
	case strings.Contains(msg, "provider policy deny"):
		return "tool_policy_denied"
	case strings.Contains(msg, "context length"), strings.Contains(msg, "context_length"), strings.Contains(msg, "maximum context"), strings.Contains(msg, "too many tokens"), strings.Contains(msg, "token limit"):
		return "context_overflow"
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "deadline exceeded"):
		return "timeout"
	default:
		return "llm_error"
	}
}
