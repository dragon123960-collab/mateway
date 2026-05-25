package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/memory"
	"github.com/dongping/mateway/internal/model"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/skill"
	"github.com/dongping/mateway/internal/tool"
)

type AgentLoop struct {
	runtime Runtime
	state   loopState
}

type loopState struct {
	message         channel.InboundMessage
	traceID         string
	understanding   taskUnderstanding
	plan            model.Plan
	results         []model.ToolResult
	control         string
	replyText       string
	awaitConfirm    bool
	awaitUserInput  bool
	failed          bool
	synthesisFailed bool
	startedAt       time.Time
	session         session.State
	resolvedQuery   string
	topic           string
	selectedSkills  []string
	binding         taskBindingDecision
	currentTask     *session.TaskState
	shortMemory     shortMemorySummary
	longMemory      longMemorySummary
	inboxReminder   inboxReminder
	repairReason    string
	repairAttempted bool
	finalAccept     FinalAcceptance
}

type taskUnderstanding struct {
	Goal            string
	Constraints     []string
	MissingInfo     []string
	CompletionDraft []string
	Capabilities    []string
	EvidenceHints   []string
	RiskLevel       string
	NeedsGrounding  bool
	NeedsMutation   bool
}

type toolComposition struct {
	CandidateTools  []tool.Definition
	SelectedSkills  []skill.Match
	SelectionReason string
}

func NewAgentLoop(rt Runtime, msg channel.InboundMessage) AgentLoop {
	if msg.SessionKey == "" {
		msg.SessionKey = fallbackSessionKey(msg)
	}
	return AgentLoop{
		runtime: rt,
		state: loopState{
			message:   msg,
			traceID:   traceIDForMessage(msg),
			startedAt: time.Now(),
		},
	}
}

func (l *AgentLoop) Run(ctx context.Context) (Response, error) {
	l.loadSession()
	if resp := l.resolveArtifactDirectAnswer(); resp != nil {
		return *resp, nil
	}
	binding := l.resolveTaskBinding(ctx)
	if resp := l.applyTaskBinding(binding); resp != nil {
		return *resp, nil
	}
	l.receive()
	l.loadLongMemory()
	l.state.inboxReminder = l.loadInboxReminder()
	if err := l.plan(ctx); err != nil {
		return l.fail(fmt.Errorf("plan failed: %w", err)), nil
	}
	l.verifyPlan(ctx)
	if l.state.failed {
		return l.fail(fmt.Errorf("plan contract verification failed")), nil
	}
	if l.state.control != "" {
		return l.controlReply(), nil
	}
	l.act(ctx, l.state.plan)
	if l.state.control != "" {
		return l.controlReply(), nil
	}
	if l.shouldRepairBeforeSynthesis() {
		l.repair(ctx)
		if l.state.control != "" {
			return l.controlReply(), nil
		}
	}
	if l.shouldBlockUnsupportedSynthesis() {
		return l.fail(fmt.Errorf("insufficient tool evidence for grounded answer")), nil
	}
	l.finalAccept(ctx)
	if l.state.finalAccept.Status == AcceptanceRejected {
		if !l.state.repairAttempted {
			l.state.repairReason = firstNonEmpty(l.state.finalAccept.Reason, "final acceptance rejected")
			l.repair(ctx)
			l.state.finalAccept = FinalAcceptance{}
			l.finalAccept(ctx)
		}
		if anyFailed(l.state.results) || l.state.finalAccept.Status == AcceptanceRejected {
			return l.fail(fmt.Errorf(firstNonEmpty(l.state.finalAccept.Reason, "final acceptance rejected"))), nil
		}
	}
	l.synthesize(ctx)
	return l.finalReply(), nil
}

func (l *AgentLoop) verifyPlan(ctx context.Context) {
	verification := verifyPlanContract(l.state.plan, l.runtime.Tools, l.state.resolvedRequest(), l.state.understanding)
	l.runtime.Logger.Event("runtime.plan_verified", map[string]any{
		"trace_id":            l.state.traceID,
		"warnings":            verification.Warnings,
		"repairable_warnings": verification.RepairableWarnings,
		"errors":              verification.Errors,
	})
	if guidance := verification.RepairGuidance(); guidance != "" {
		l.state.repairReason = guidance
	}
	if !verification.Blocking() {
		return
	}
	l.state.results = []model.ToolResult{{
		StepID: "plan",
		Tool:   "plan.verify",
		OK:     false,
		Error:  "plan_contract_invalid",
		Output: strings.Join(verification.Errors, "\n"),
		Evidence: map[string]any{
			"kind":                "plan_verification",
			"warnings":            verification.Warnings,
			"repairable_warnings": verification.RepairableWarnings,
			"errors":              verification.Errors,
		},
	}}
	l.state.repairReason = "plan_contract_invalid:\n" + verification.RepairGuidance()
	if !l.repairPlan(ctx) {
		l.state.failed = true
		return
	}
	second := verifyPlanContract(l.state.plan, l.runtime.Tools, l.state.resolvedRequest(), l.state.understanding)
	l.runtime.Logger.Event("runtime.plan_verified_after_repair", map[string]any{
		"trace_id":            l.state.traceID,
		"warnings":            second.Warnings,
		"repairable_warnings": second.RepairableWarnings,
		"errors":              second.Errors,
	})
	if guidance := second.RepairGuidance(); guidance != "" {
		l.state.repairReason = guidance
	}
	if second.Blocking() {
		l.state.failed = true
		l.state.results = append(l.state.results, model.ToolResult{
			StepID: "plan",
			Tool:   "plan.verify",
			OK:     false,
			Error:  "plan_contract_invalid_after_repair",
			Output: strings.Join(second.Errors, "\n"),
			Evidence: map[string]any{
				"kind":                "plan_verification",
				"warnings":            second.Warnings,
				"repairable_warnings": second.RepairableWarnings,
				"errors":              second.Errors,
			},
		})
	}
}

func (l *AgentLoop) receive() {
	msg := l.state.message
	l.runtime.Logger.Event("runtime.receive", map[string]any{
		"trace_id":       l.state.traceID,
		"session_key":    msg.SessionKey,
		"channel":        msg.Channel,
		"message_id":     msg.ID,
		"thread_id":      msg.ThreadID,
		"user_id":        msg.UserID,
		"text":           msg.Text,
		"resolved_query": firstNonEmpty(l.state.resolvedQuery, msg.Text),
	})
}

func (l *AgentLoop) loadLongMemory() {
	if strings.TrimSpace(l.state.resolvedRequest()) == "" || strings.TrimSpace(l.runtime.Memory.Root) == "" {
		return
	}
	agentID := "main"
	if l.runtime.Config != nil {
		agentID = firstNonEmpty(l.runtime.Config.Agents.Default, agentID)
	}
	results, err := l.runtime.Memory.SearchLong(memory.SearchOptions{
		AgentID:      agentID,
		Query:        l.state.resolvedRequest(),
		Limit:        4,
		SnippetLimit: 500,
	})
	if err != nil {
		l.runtime.Logger.Event("runtime.long_memory_failed", map[string]any{
			"trace_id": l.state.traceID,
			"error":    err.Error(),
		})
		return
	}
	l.state.longMemory = buildLongMemorySummary(results)
	if len(results) > 0 {
		l.runtime.Logger.Event("runtime.long_memory_loaded", map[string]any{
			"trace_id": l.state.traceID,
			"count":    len(results),
			"items":    longMemoryTraceFields(results),
			"chars":    len(l.state.longMemory.Text),
		})
	}
}

func (l *AgentLoop) loadSession() {
	if l.runtime.Sessions == nil {
		return
	}
	state, err := l.runtime.Sessions.Load(l.state.message.SessionKey)
	if err != nil {
		l.runtime.Logger.Event("runtime.session_load_failed", map[string]any{
			"trace_id":    l.state.traceID,
			"session_key": l.state.message.SessionKey,
			"error":       err.Error(),
		})
		return
	}
	l.state.session = state
	l.state.shortMemory = buildShortMemorySummary(state)
	lastTaskID := ""
	lastStatus := ""
	if state.LastTask != nil {
		lastTaskID = state.LastTask.ID
		lastStatus = state.LastTask.Status
	}
	l.runtime.Logger.Event("runtime.session_loaded", map[string]any{
		"trace_id":     l.state.traceID,
		"session_key":  l.state.message.SessionKey,
		"exists":       !state.CreatedAt.IsZero() || state.TurnCount > 0 || state.LastTask != nil || len(state.RecentTurns) > 0,
		"turn_count":   state.TurnCount,
		"last_task_id": lastTaskID,
		"last_status":  lastStatus,
	})
	if l.state.shortMemory.SessionPresent && l.state.shortMemory.Text != "" {
		l.runtime.Logger.Event("runtime.short_memory_loaded", map[string]any{
			"trace_id":       l.state.traceID,
			"session_key":    l.state.message.SessionKey,
			"recent_turns":   l.state.shortMemory.RecentTurns,
			"open_tasks":     l.state.shortMemory.OpenTasks,
			"artifacts":      l.state.shortMemory.Artifacts,
			"active_task_id": l.state.shortMemory.ActiveTaskID,
			"chars":          len(l.state.shortMemory.Text),
		})
	}
}

func (l *AgentLoop) plan(ctx context.Context) error {
	fallbackUnderstanding := l.fallbackUnderstanding()
	allTools := l.runtime.Tools.Definitions()
	recommendedTools := composeCandidateTools(allTools, fallbackUnderstanding)
	candidateTools := orderToolsForPlanning(allTools, recommendedTools)
	planMatches := skill.SelectMatches(l.skillDefinitions(), skill.StagePlanning, l.skillContext())
	planSkills := make([]skill.Definition, 0, len(planMatches))
	for _, match := range planMatches {
		planSkills = append(planSkills, match.Definition)
	}
	l.runtime.Logger.Event("runtime.skills_selected", map[string]any{
		"trace_id": l.state.traceID,
		"stage":    skill.StagePlanning,
		"skills":   selectedSkillsTraceFields(planMatches),
	})
	l.recordSelectedSkills(planSkills)
	contextPrompt := l.buildContextPrompt(skill.StagePlanning, planMatches)
	if recommendation := renderRecommendedTools(recommendedTools, len(allTools)); recommendation != "" {
		contextPrompt = strings.TrimSpace(contextPrompt + "\n\n" + recommendation)
	}
	plan, check, err := l.planJSON(ctx, strings.TrimSpace(contextPrompt+"\n\n"+skill.PromptBlock(planSkills)), candidateTools)
	if err != nil {
		return err
	}
	plan = l.normalizePlanForRuntime(plan)
	l.state.plan = plan
	l.state.understanding = mergeUnderstandingFromPlan(plan.Understanding, fallbackUnderstanding)
	l.runtime.Logger.Event("runtime.plan", map[string]any{
		"trace_id":       l.state.traceID,
		"summary":        plan.Summary,
		"steps":          len(plan.Steps),
		"tool_names":     planToolNames(plan),
		"understanding":  plan.Understanding,
		"checker_fixed":  check.Fixed,
		"checker_warns":  check.Warnings,
		"checker_raw_ok": check.Raw != "",
	})
	if l.runtime.Observer != nil {
		l.runtime.Observer.Plan(l.state.traceID, plan)
	}
	return nil
}

func (l *AgentLoop) normalizePlanForRuntime(plan model.Plan) model.Plan {
	return plan
}

func (l *AgentLoop) planJSON(ctx context.Context, skillPrompt string, candidates []tool.Definition) (model.Plan, model.PlanCheckResult, error) {
	if checker, ok := l.runtime.Model.(interface {
		PlanCheckedJSON(context.Context, string, []tool.Definition, string) (model.PlanCheckResult, error)
	}); ok {
		result, err := checker.PlanCheckedJSON(ctx, l.state.resolvedRequest(), candidates, skillPrompt)
		return result.Plan, result, err
	}
	plan, err := l.runtime.Model.PlanJSON(ctx, l.state.resolvedRequest(), candidates, skillPrompt)
	return plan, model.PlanCheckResult{Plan: plan}, err
}

func (l *AgentLoop) act(ctx context.Context, plan model.Plan) {
	var previous map[string]session.StepState
	if l.state.currentTask != nil {
		previous = l.state.currentTask.StepStates
	}
	results, control := l.runtime.executePlan(ctx, l.state.traceID, plan, l.state.binding.ApprovalGranted, l.state.binding.ApprovalStepID, previous, nil)
	l.state.plan = plan
	l.state.results = results
	l.state.control = control
}

func (l *AgentLoop) shouldRepairBeforeSynthesis() bool {
	if l.state.control != "" {
		return false
	}
	return !l.state.repairAttempted && (hasRepairableFailure(l.state.results) || needsGroundingEvidence(l.state.resolvedRequest(), l.state.results))
}

func (l *AgentLoop) shouldBlockUnsupportedSynthesis() bool {
	if l.state.control != "" {
		return false
	}
	return needsGroundingEvidence(l.state.resolvedRequest(), l.state.results)
}

func (l *AgentLoop) repair(ctx context.Context) {
	if !l.repairPlan(ctx) {
		return
	}
	var previous map[string]session.StepState
	if l.state.currentTask != nil {
		previous = l.state.currentTask.StepStates
	}
	results, control := l.runtime.executePlan(ctx, l.state.traceID, l.state.plan, l.state.binding.ApprovalGranted, l.state.binding.ApprovalStepID, previous, l.state.results)
	l.state.results = mergeToolResultsForPlan(l.state.plan, l.state.results, results)
	l.state.control = control
}

func (l *AgentLoop) repairPlan(ctx context.Context) bool {
	if l.state.repairAttempted {
		return false
	}
	l.state.repairAttempted = true
	allTools := l.runtime.Tools.Definitions()
	recommendedTools := composeCandidateTools(allTools, l.state.understanding)
	candidateTools := orderToolsForPlanning(allTools, recommendedTools)
	planMatches := skill.SelectMatches(l.skillDefinitions(), skill.StagePlanning, l.skillContext())
	planSkills := make([]skill.Definition, 0, len(planMatches))
	for _, match := range planMatches {
		planSkills = append(planSkills, match.Definition)
	}
	l.runtime.Logger.Event("runtime.skills_selected", map[string]any{
		"trace_id": l.state.traceID,
		"stage":    "planning_repair",
		"skills":   selectedSkillsTraceFields(planMatches),
	})
	l.recordSelectedSkills(planSkills)
	contextPrompt := l.buildContextPrompt("planning_repair", planMatches)
	if recommendation := renderRecommendedTools(recommendedTools, len(allTools)); recommendation != "" {
		contextPrompt = strings.TrimSpace(contextPrompt + "\n\n" + recommendation)
	}
	repairGuidance := strings.TrimSpace(l.state.repairReason)
	if repairGuidance != "" {
		contextPrompt = strings.TrimSpace(contextPrompt + "\n\nRepair guidance:\n" + repairGuidance)
	}
	repaired, check, err := l.repairPlanJSON(ctx, strings.TrimSpace(contextPrompt+"\n\n"+skill.PromptBlock(planSkills)), candidateTools)
	if err != nil {
		l.runtime.Logger.Event("runtime.plan_repair_failed", map[string]any{
			"trace_id": l.state.traceID,
			"error":    err.Error(),
		})
		return false
	}
	l.runtime.Logger.Event("runtime.plan_repair", map[string]any{
		"trace_id":       l.state.traceID,
		"reason":         firstNonEmpty(l.state.repairReason, repairReasonFromResults(l.state.results)),
		"summary":        repaired.Summary,
		"steps":          len(repaired.Steps),
		"tool_names":     planToolNames(repaired),
		"checker_fixed":  check.Fixed,
		"checker_warns":  check.Warnings,
		"checker_raw_ok": check.Raw != "",
	})
	if l.runtime.Observer != nil {
		l.runtime.Observer.Plan(l.state.traceID, repaired)
	}
	l.state.plan = repaired
	return true
}

func (l *AgentLoop) repairPlanJSON(ctx context.Context, skillPrompt string, candidates []tool.Definition) (model.Plan, model.PlanCheckResult, error) {
	if checker, ok := l.runtime.Model.(interface {
		RepairPlanCheckedJSON(context.Context, string, model.Plan, []model.ToolResult, []tool.Definition, string) (model.PlanCheckResult, error)
	}); ok {
		result, err := checker.RepairPlanCheckedJSON(ctx, l.state.resolvedRequest(), l.state.plan, l.state.results, candidates, skillPrompt)
		return result.Plan, result, err
	}
	plan, err := l.runtime.Model.RepairPlanJSON(ctx, l.state.resolvedRequest(), l.state.plan, l.state.results, candidates, skillPrompt)
	return plan, model.PlanCheckResult{Plan: plan}, err
}

func (l *AgentLoop) fallbackUnderstanding() taskUnderstanding {
	req := strings.TrimSpace(l.state.resolvedRequest())
	capabilities := inferCapabilities(req)
	understanding := taskUnderstanding{
		Goal:            req,
		Constraints:     collectConstraints(req),
		CompletionDraft: inferCompletionDraft(req, capabilities),
		Capabilities:    capabilities,
		EvidenceHints:   inferEvidenceHints(capabilities, req),
		RiskLevel:       inferRiskLevel(capabilities, req),
		NeedsGrounding:  requiresGroundingEvidence(req),
		NeedsMutation:   requiresMutationCapability(capabilities),
	}
	l.runtime.Logger.Event("runtime.understand_fallback", map[string]any{
		"trace_id":        l.state.traceID,
		"goal":            understanding.Goal,
		"constraints":     understanding.Constraints,
		"capabilities":    understanding.Capabilities,
		"completion":      understanding.CompletionDraft,
		"evidence_hints":  understanding.EvidenceHints,
		"risk_level":      understanding.RiskLevel,
		"needs_grounding": understanding.NeedsGrounding,
		"needs_mutation":  understanding.NeedsMutation,
	})
	return understanding
}

func (l *AgentLoop) finalAccept(ctx context.Context) {
	if !shouldLLMAcceptFinal(l.state.plan, l.state.results, l.state.understanding, l.state.repairAttempted, l.runtime.Tools) {
		return
	}
	l.state.finalAccept = llmAcceptFinal(ctx, l.runtime.Model, l.state.resolvedRequest(), l.state.understanding, l.state.plan, l.state.results)
	l.runtime.Logger.Event("runtime.final_accept", map[string]any{
		"trace_id": l.state.traceID,
		"status":   l.state.finalAccept.Status,
		"reason":   l.state.finalAccept.Reason,
		"missing":  l.state.finalAccept.Missing,
	})
}

func mergeUnderstandingFromPlan(raw model.UnderstandingJSON, fallback taskUnderstanding) taskUnderstanding {
	out := fallback
	if strings.TrimSpace(raw.Goal) != "" {
		out.Goal = strings.TrimSpace(raw.Goal)
	}
	if len(raw.Constraints) > 0 {
		out.Constraints = append([]string(nil), raw.Constraints...)
	}
	if len(raw.MissingInfo) > 0 {
		out.MissingInfo = append([]string(nil), raw.MissingInfo...)
	}
	if len(raw.CompletionCriteria) > 0 {
		out.CompletionDraft = append([]string(nil), raw.CompletionCriteria...)
	}
	if len(raw.ToolNeeds) > 0 {
		out.Capabilities = append([]string(nil), raw.ToolNeeds...)
	}
	if len(raw.EvidenceExpectations) > 0 {
		out.EvidenceHints = append([]string(nil), raw.EvidenceExpectations...)
	}
	if strings.TrimSpace(raw.RiskLevel) != "" {
		out.RiskLevel = strings.TrimSpace(raw.RiskLevel)
	}
	out.NeedsGrounding = out.NeedsGrounding || len(out.EvidenceHints) > 0
	out.NeedsMutation = out.NeedsMutation || strings.TrimSpace(out.RiskLevel) == "guarded_mutation" || strings.TrimSpace(out.RiskLevel) == "dangerous_execute"
	return out
}

func planStepOrder(plan model.Plan) []string {
	out := make([]string, 0, len(plan.Steps))
	for _, step := range plan.Steps {
		if strings.TrimSpace(step.ID) != "" {
			out = append(out, step.ID)
		}
	}
	return out
}

func buildStepStates(plan model.Plan, results []model.ToolResult, now time.Time) map[string]session.StepState {
	if len(plan.Steps) == 0 {
		return nil
	}
	resultByStep := map[string]model.ToolResult{}
	for _, result := range results {
		resultByStep[strings.TrimSpace(result.StepID)] = result
	}
	out := map[string]session.StepState{}
	for _, step := range plan.Steps {
		id := strings.TrimSpace(step.ID)
		if id == "" {
			continue
		}
		item := session.StepState{
			ID:        id,
			Goal:      step.Goal,
			Tool:      step.Tool,
			Status:    "pending",
			DependsOn: append([]string(nil), step.DependsOn...),
			UpdatedAt: now,
		}
		if result, ok := resultByStep[id]; ok {
			item.AttemptCount = 1
			item.ResultOK = result.OK
			item.ResultError = result.Error
			item.ResultSummary = shortenReply(result.Output, 240)
			item.Evidence = result.Evidence
			item.StartedAt = now
			item.FinishedAt = now
			item.UpdatedAt = now
			switch {
			case result.Error == "await_confirm":
				item.Status = "blocked"
				item.AcceptanceStatus = "await_confirm"
			case result.OK && strings.Contains(strings.ToLower(result.Output), "missing") && strings.Contains(strings.ToLower(result.Output), "evidence"):
				item.Status = "usable"
				item.AcceptanceStatus = "usable"
				item.AcceptanceReason = result.Output
			case result.Error == "step_acceptance_suspect":
				item.Status = "suspect"
				item.AcceptanceStatus = "suspect"
				item.AcceptanceReason = result.Output
			case result.OK:
				item.Status = "passed"
				item.AcceptanceStatus = "passed"
			default:
				item.Status = "failed"
				item.AcceptanceStatus = "failed"
				item.AcceptanceReason = firstNonEmpty(result.Error, result.Output)
			}
		}
		out[id] = item
	}
	return out
}

func executionStatusForResponse(resp Response) string {
	switch {
	case resp.AwaitConfirm:
		return "await_confirm"
	case resp.AwaitUserInput:
		return "await_input"
	case resp.Failed:
		return "failed"
	default:
		return "completed"
	}
}

func mergeToolResultsForPlan(plan model.Plan, existing, next []model.ToolResult) []model.ToolResult {
	if len(existing) == 0 {
		return next
	}
	allowed := map[string]bool{}
	for _, step := range plan.Steps {
		allowed[strings.TrimSpace(step.ID)] = true
	}
	merged := make([]model.ToolResult, 0, len(existing)+len(next))
	seen := map[string]bool{}
	for _, item := range next {
		seen[strings.TrimSpace(item.StepID)] = true
	}
	for _, item := range existing {
		id := strings.TrimSpace(item.StepID)
		if seen[id] || !allowed[id] {
			continue
		}
		merged = append(merged, item)
	}
	merged = append(merged, next...)
	return merged
}

func (l *AgentLoop) synthesize(ctx context.Context) {
	synthMatches := skill.SelectMatches(l.skillDefinitions(), skill.StageSynthesis, l.skillContext())
	synthSkills := make([]skill.Definition, 0, len(synthMatches))
	for _, match := range synthMatches {
		synthSkills = append(synthSkills, match.Definition)
	}
	l.runtime.Logger.Event("runtime.skills_selected", map[string]any{
		"trace_id": l.state.traceID,
		"stage":    skill.StageSynthesis,
		"skills":   selectedSkillsTraceFields(synthMatches),
	})
	l.recordSelectedSkills(synthSkills)
	contextPrompt := l.buildContextPrompt(skill.StageSynthesis, synthMatches)
	text, err := l.runtime.Model.Synthesize(ctx, l.state.resolvedRequest(), l.state.plan, l.state.results, strings.TrimSpace(contextPrompt+"\n\n"+skill.PromptBlock(synthSkills)))
	if err != nil {
		l.state.synthesisFailed = true
		l.state.replyText = fallbackSynthesis(l.state.results)
		l.runtime.Logger.Event("runtime.synthesize_failed", map[string]any{
			"trace_id": l.state.traceID,
			"error":    err.Error(),
		})
		return
	}
	l.state.replyText = text
}

func (l *AgentLoop) skillContext() skill.Context {
	results := make([]skill.ResultRef, 0, len(l.state.results))
	for _, result := range l.state.results {
		kind, _ := result.Evidence["kind"].(string)
		results = append(results, skill.ResultRef{Kind: kind})
	}
	return skill.Context{
		UserText: l.state.resolvedRequest(),
		Results:  results,
	}
}

func (l *AgentLoop) skillDefinitions() []skill.Definition {
	if l.runtime.Skills == nil {
		return nil
	}
	return l.runtime.Skills.Definitions()
}

func (l *AgentLoop) buildContextPrompt(stage string, matches []skill.Match) string {
	return buildModelContextPrompt(l.state.resolvedRequest(), stage, matches, l.runtime.Tools.Definitions(), l.runtime.ToolCtx, promptContextOptions{
		ShortMemory:   l.state.shortMemory.Text,
		LongMemory:    l.state.longMemory.Text,
		Understanding: l.state.understanding,
		CurrentTask:   l.state.currentTask,
	})
}

func (l *AgentLoop) controlReply() Response {
	style := "approval_pending"
	awaitInput := false
	for _, result := range l.state.results {
		if kind, _ := result.Evidence["kind"].(string); kind == "user_input_required" {
			style = "input_required"
			awaitInput = true
			break
		}
	}
	text := controlReplyText(l.state.results, style)
	l.state.replyText = text
	l.state.awaitConfirm = l.state.control == "await_confirm" && !awaitInput
	l.state.awaitUserInput = awaitInput
	resp := Response{
		Reply: l.runtime.sanitizeReply(channel.OutboundMessage{
			Channel:  l.state.message.Channel,
			ThreadID: l.state.message.ThreadID,
			Text:     text,
			Style:    style,
			Title:    "Mateway pending confirmation",
		}),
		TraceID:           l.state.traceID,
		Plan:              l.state.plan,
		Results:           l.state.results,
		AwaitConfirm:      l.state.awaitConfirm,
		AwaitUserInput:    l.state.awaitUserInput,
		FinalAcceptStatus: string(l.state.finalAccept.Status),
		FinalAcceptReason: l.state.finalAccept.Reason,
	}
	l.runtime.Logger.Event("runtime.control", map[string]any{
		"trace_id": l.state.traceID,
		"control":  l.state.control,
		"style":    resp.Reply.Style,
	})
	if l.runtime.Observer != nil {
		l.runtime.Observer.Control(l.state.traceID, l.state.control, resp.Reply.Style)
	}
	l.saveSession(resp)
	return resp
}

func (l *AgentLoop) finalReply() Response {
	l.state.failed = anyFailed(l.state.results)
	resp := Response{
		Reply: l.runtime.sanitizeReply(channel.OutboundMessage{
			Channel:  l.state.message.Channel,
			ThreadID: l.state.message.ThreadID,
			Text:     l.state.replyText,
			Style:    styleForFailed(l.state.failed),
		}),
		TraceID:           l.state.traceID,
		Plan:              l.state.plan,
		Results:           l.state.results,
		Failed:            l.state.failed,
		FinalAcceptStatus: string(l.state.finalAccept.Status),
		FinalAcceptReason: l.state.finalAccept.Reason,
	}
	l.runtime.Logger.Event("runtime.reply", map[string]any{
		"trace_id":     l.state.traceID,
		"failed":       l.state.failed,
		"reply_chars":  len(l.state.replyText),
		"result_count": len(l.state.results),
	})
	if l.runtime.Observer != nil {
		l.runtime.Observer.Reply(l.state.traceID, l.state.replyText, l.state.failed)
	}
	learning := l.saveSession(resp)
	if learning.CandidateGenerated {
		resp.Reply.Text = strings.TrimSpace(resp.Reply.Text + "\n\n" + skillCandidatePrompt(learning))
	}
	if !resp.Failed && !resp.AwaitConfirm && !resp.AwaitUserInput && !learning.CandidateGenerated {
		resp.Reply.Text = appendInboxReminder(resp.Reply.Text, l.state.inboxReminder)
	}
	return resp
}

func (l *AgentLoop) fail(err error) Response {
	resp := l.runtime.failure(l.state.message, &l.state.plan, l.state.results, err)
	l.runtime.Logger.Event("runtime.failed", map[string]any{
		"trace_id":     l.state.traceID,
		"reason":       resp.Reply.Text,
		"error_detail": err.Error(),
	})
	if l.runtime.Observer != nil {
		l.runtime.Observer.Failed(l.state.traceID, resp.Reply.Text)
	}
	l.saveSession(resp)
	return resp
}

func (l *AgentLoop) saveSession(resp Response) memory.ProcessResult {
	if l.runtime.Sessions == nil {
		return memory.ProcessResult{}
	}
	finishedAt := time.Now()
	task := l.baseTaskForSave()
	task.TraceID = l.state.traceID
	task.UserText = l.state.message.Text
	task.ResolvedQuery = l.state.resolvedRequest()
	task.Topic = firstNonEmpty(l.state.topic, task.Topic, l.state.plan.Summary)
	task.PlanSummary = l.state.plan.Summary
	task.ToolNames = planToolNames(l.state.plan)
	task.SelectedSkills = append([]string(nil), l.state.selectedSkills...)
	task.ExecutionStatus = executionStatusForResponse(resp)
	task.StepOrder = planStepOrder(l.state.plan)
	task.StepStates = buildStepStates(l.state.plan, l.state.results, finishedAt)
	if task.Status != session.TaskAbandoned {
		task.Status = taskStatusForResponse(resp)
	}
	task.Failed = resp.Failed
	task.ResultCount = len(resp.Results)
	task.ReplyPreview = shortenReply(resp.Reply.Text, 240)
	task.LastAnswer = strings.TrimSpace(resp.Reply.Text)
	task.Artifacts = collectArtifacts(resp.Results)
	task.UpdatedAt = finishedAt
	task.FinishedAt = finishedAt
	if task.StartedAt.IsZero() {
		task.StartedAt = l.state.startedAt
	}
	if resp.AwaitConfirm {
		approvedStepID := ""
		if task.PendingApproval != nil {
			action := strings.TrimSpace(task.PendingApproval.RequestedAction)
			if strings.HasPrefix(action, "approved:") {
				approvedStepID = strings.TrimSpace(strings.TrimPrefix(action, "approved:"))
			}
		}
		pendingAction := firstNonEmpty(task.PlanSummary, task.ResolvedQuery)
		for i := len(resp.Results) - 1; i >= 0; i-- {
			result := resp.Results[i]
			if result.Error != "await_confirm" {
				continue
			}
			if stepID := strings.TrimSpace(result.StepID); stepID != "" {
				pendingAction = "step:" + stepID
			}
			break
		}
		task.PendingApproval = &session.PendingApproval{
			ApprovalType:    "boolean_confirm",
			Prompt:          strings.TrimSpace(resp.Reply.Text),
			RequestedAction: pendingAction,
		}
		if approvedStepID != "" && pendingAction == "step:"+approvedStepID {
			task.PendingApproval = nil
		}
		task.PendingQuestions = nil
		l.runtime.Logger.Event("runtime.task_pending_approval", map[string]any{
			"trace_id": l.state.traceID,
			"task_id":  task.ID,
		})
	} else if resp.AwaitUserInput {
		task.PendingQuestions = []string{strings.TrimSpace(resp.Reply.Text)}
		if len(task.PendingFields) == 0 {
			task.PendingFields = nil
		}
		task.PendingApproval = nil
		l.runtime.Logger.Event("runtime.task_pending_input", map[string]any{
			"trace_id": l.state.traceID,
			"task_id":  task.ID,
			"fields":   pendingFieldNames(task.PendingFields),
		})
	} else {
		task.PendingApproval = nil
		task.PendingQuestions = nil
		task.PendingFields = nil
	}
	next := session.ApplyTask(l.state.session, session.StateMeta{
		SessionKey: l.state.message.SessionKey,
		Channel:    l.state.message.Channel,
		UserID:     l.state.message.UserID,
		ThreadID:   l.state.message.ThreadID,
	}, session.AppendTaskInput{
		Task:           task,
		AssistantReply: resp.Reply.Text,
		At:             finishedAt,
		Activate:       true,
	})
	if err := l.runtime.Sessions.Save(next); err != nil {
		l.runtime.Logger.Event("runtime.session_save_failed", map[string]any{
			"trace_id":    l.state.traceID,
			"session_key": l.state.message.SessionKey,
			"error":       err.Error(),
		})
		return memory.ProcessResult{}
	}
	l.state.session = next
	l.state.currentTask = session.ActiveTask(next)
	l.runtime.Logger.Event("runtime.session_saved", map[string]any{
		"trace_id":     l.state.traceID,
		"session_key":  next.SessionKey,
		"turn_count":   next.TurnCount,
		"task_id":      task.ID,
		"task_status":  task.Status,
		"result_count": task.ResultCount,
	})
	l.proposeMemoryFromTask(resp.Reply, task)
	return l.recordLearningPattern(resp, task)
}

func (l *AgentLoop) recordLearningPattern(resp Response, task session.TaskState) memory.ProcessResult {
	if l.runtime.Config == nil || !l.runtime.Config.Learning.Enabled || !l.runtime.Config.Learning.SkillCrystallization.Enabled {
		return memory.ProcessResult{}
	}
	if task.Status != session.TaskCompleted || resp.Failed || resp.AwaitConfirm || resp.AwaitUserInput {
		return memory.ProcessResult{}
	}
	agentID := firstNonEmpty(l.runtime.Config.Agents.Default, "main")
	artifacts := make([]memory.Artifact, 0, len(task.Artifacts))
	for _, artifact := range task.Artifacts {
		artifacts = append(artifacts, memory.Artifact{
			Kind:      artifact.Kind,
			Path:      artifact.Path,
			Label:     artifact.Label,
			SourceURL: artifact.SourceURL,
			Summary:   artifact.Summary,
		})
	}
	result, err := l.runtime.Memory.ProcessTask(memory.TaskOutcome{
		AgentID:        agentID,
		Channel:        l.state.message.Channel,
		SessionKey:     l.state.message.SessionKey,
		TraceID:        l.state.traceID,
		TaskID:         task.ID,
		Intent:         task.ResolvedQuery,
		PlanSummary:    task.PlanSummary,
		Tools:          task.ToolNames,
		SelectedSkills: task.SelectedSkills,
		Success:        true,
		Failed:         resp.Failed,
		AwaitConfirm:   resp.AwaitConfirm,
		AwaitUserInput: resp.AwaitUserInput,
		Artifacts:      artifacts,
		ReplyPreview:   task.ReplyPreview,
		FinishedAt:     task.FinishedAt,
	}, memory.LearningConfig{
		Enabled:            l.runtime.Config.Learning.Enabled && l.runtime.Config.Learning.SkillCrystallization.Enabled,
		SuccessThreshold:   l.runtime.Config.Learning.SkillCrystallization.SuccessThreshold,
		RequireUserConfirm: l.runtime.Config.Learning.SkillCrystallization.RequireUserConfirm,
	})
	if err != nil {
		l.runtime.Logger.Event("runtime.learning_failed", map[string]any{"trace_id": l.state.traceID, "error": err.Error()})
		return memory.ProcessResult{}
	}
	if result.PatternKey != "" {
		l.runtime.Logger.Event("runtime.learning_pattern_recorded", map[string]any{
			"trace_id":            l.state.traceID,
			"pattern_key":         result.PatternKey,
			"success_count":       result.SuccessCount,
			"candidate_generated": result.CandidateGenerated,
			"candidate_path":      result.CandidatePath,
		})
	}
	return result
}

func (l *AgentLoop) saveConversationOnly(resp Response) {
	if l.runtime.Sessions == nil {
		return
	}
	next := session.AppendConversation(l.state.session, session.StateMeta{
		SessionKey: l.state.message.SessionKey,
		Channel:    l.state.message.Channel,
		UserID:     l.state.message.UserID,
		ThreadID:   l.state.message.ThreadID,
	}, l.state.message.Text, resp.Reply.Text, time.Now())
	if err := l.runtime.Sessions.Save(next); err != nil {
		l.runtime.Logger.Event("runtime.session_save_failed", map[string]any{
			"trace_id":    l.state.traceID,
			"session_key": l.state.message.SessionKey,
			"error":       err.Error(),
		})
		return
	}
	l.state.session = next
	l.runtime.Logger.Event("runtime.session_saved", map[string]any{
		"trace_id":    l.state.traceID,
		"session_key": next.SessionKey,
		"turn_count":  next.TurnCount,
	})
}

func skillCandidatePrompt(result memory.ProcessResult) string {
	return fmt.Sprintf("Learning note: this workflow pattern has succeeded %d times, so I created a proposed skill candidate for review: %s\nIt has not been enabled automatically.", result.SuccessCount, result.CandidatePath)
}

func (l *AgentLoop) baseTaskForSave() session.TaskState {
	if l.state.currentTask != nil {
		return *l.state.currentTask
	}
	return session.TaskState{
		ID:        firstNonEmpty(l.state.binding.TargetTaskID, l.state.traceID),
		StartedAt: l.state.startedAt,
		Status:    session.TaskOpen,
	}
}

func (s *loopState) resolvedRequest() string {
	return firstNonEmpty(s.resolvedQuery, s.message.Text)
}

func (l *AgentLoop) recordSelectedSkills(defs []skill.Definition) {
	for _, def := range defs {
		if strings.TrimSpace(def.Name) == "" {
			continue
		}
		seen := false
		for _, existing := range l.state.selectedSkills {
			if existing == def.Name {
				seen = true
				break
			}
		}
		if !seen {
			l.state.selectedSkills = append(l.state.selectedSkills, def.Name)
		}
	}
}

func taskStatusForResponse(resp Response) string {
	switch {
	case resp.AwaitUserInput:
		return "await_user_input"
	case resp.AwaitConfirm:
		return "await_confirm"
	case resp.Failed:
		return "failed"
	default:
		return "completed"
	}
}

func shortenReply(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	if limit <= 3 {
		return text[:limit]
	}
	return text[:limit-3] + "..."
}
