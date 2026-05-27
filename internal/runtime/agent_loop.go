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
	message             channel.InboundMessage
	traceID             string
	understanding       taskUnderstanding
	plan                model.Plan
	previousPlanSummary string
	results             []model.ToolResult
	historicalResults   []model.ToolResult
	control             string
	replyText           string
	awaitConfirm        bool
	awaitUserInput      bool
	failed              bool
	synthesisFailed     bool
	startedAt           time.Time
	session             session.State
	resolvedQuery       string
	topic               string
	selectedSkills      []string
	binding             taskBindingDecision
	currentTask         *session.TaskState
	shortMemory         shortMemorySummary
	longMemory          longMemorySummary
	cliUsage            cliUsageContext
	inboxReminder       inboxReminder
	repairReason        string
	repairAttempted     bool
	repairCycles        int
	repairNeedsContinuation bool
	deferredResults     []model.ToolResult
	finalAccept         FinalAcceptance
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
	IsScheduledRun  bool
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
	if resp := l.resolveMatewayDirectCommand(); resp != nil {
		return *resp, nil
	}
	binding := l.resolveTaskBinding(ctx)
	if resp := l.resolveApprovedMatewayDirectCommandFromBinding(binding); resp != nil {
		return *resp, nil
	}
	if resp := l.applyTaskBinding(binding); resp != nil {
		return *resp, nil
	}
	if resp := l.resolveApprovedMatewayDirectCommand(); resp != nil {
		return *resp, nil
	}
	if resp := l.resumeApprovedTask(ctx); resp != nil {
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
			if l.state.control != "" {
				return l.controlReply(), nil
			}
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
	if l.state.understanding.IsScheduledRun && containsScheduledRunUserAskError(verification.Errors) {
		l.state.failed = true
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
		l.state.repairReason = "scheduled_run_user_ask_forbidden:\n" + verification.RepairGuidance()
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
		if diagnosticPlan, ok := safeDiagnosticPrefixForBlockedPlan(l.state.plan, l.state.resolvedRequest()); ok {
			originalStepCount := len(l.state.plan.Steps)
			l.state.plan = diagnosticPlan
			l.state.deferredResults = append(l.state.deferredResults, model.ToolResult{
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
			l.runtime.Logger.Event("runtime.plan_trimmed_to_safe_diagnostic_prefix", map[string]any{
				"trace_id":       l.state.traceID,
				"original_steps": originalStepCount,
				"prefix_steps":   len(diagnosticPlan.Steps),
			})
			return
		}
		if discoveryPlan, ok := safeDiscoveryPrefixForBlockedPlan(l.state.plan); ok {
			originalStepCount := len(l.state.plan.Steps)
			l.state.plan = discoveryPlan
			l.state.repairAttempted = false
			l.state.deferredResults = append(l.state.deferredResults, model.ToolResult{
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
			l.runtime.Logger.Event("runtime.plan_trimmed_to_safe_discovery_prefix", map[string]any{
				"trace_id":       l.state.traceID,
				"original_steps": originalStepCount,
				"prefix_steps":   len(discoveryPlan.Steps),
			})
			return
		}
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

func containsScheduledRunUserAskError(errors []string) bool {
	for _, err := range errors {
		if strings.Contains(strings.ToLower(strings.TrimSpace(err)), "scheduled runs must not require user.ask") {
			return true
		}
	}
	return false
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
	query := strings.TrimSpace(l.state.resolvedRequest())
	if cli := cliUsageCandidateFromText(query); cli.Executable != "" {
		query = strings.TrimSpace(query + "\nCLI usage: " + cli.Executable)
	}
	results, err := l.runtime.Memory.SearchLong(memory.SearchOptions{
		AgentID:      agentID,
		Query:        query,
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
	l.state.cliUsage = buildCLIUsageContext(l.state.resolvedRequest(), results)
	if len(results) > 0 {
		l.runtime.Logger.Event("runtime.long_memory_loaded", map[string]any{
			"trace_id": l.state.traceID,
			"count":    len(results),
			"items":    longMemoryTraceFields(results),
			"chars":    len(l.state.longMemory.Text),
		})
	}
	if l.state.cliUsage.Executable != "" {
		l.runtime.Logger.Event("runtime.cli_usage_memory_checked", map[string]any{
			"trace_id":     l.state.traceID,
			"executable":   l.state.cliUsage.Executable,
			"memory_found": l.state.cliUsage.MemoryFound,
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
	allTools := l.runtime.availableToolDefinitions()
	candidateTools := composeCandidateTools(allTools, fallbackUnderstanding, planningCandidateBudget)
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
	if guidance := l.cliUsagePlanningGuidance(); guidance != "" {
		contextPrompt = strings.TrimSpace(contextPrompt + "\n\n" + guidance)
	}
	if recommendation := renderRecommendedTools(candidateTools); recommendation != "" {
		contextPrompt = strings.TrimSpace(contextPrompt + "\n\n" + recommendation)
	}
	plan, check, err := l.planJSON(ctx, buildStageModelPrompt(contextPrompt, planSkills), candidateTools)
	if err != nil {
		return err
	}
	plan = l.normalizePlanForRuntime(plan)
	l.state.plan = plan
	l.state.understanding = mergeUnderstandingFromPlan(plan.Understanding, fallbackUnderstanding)
	l.state.understanding.IsScheduledRun = fallbackUnderstanding.IsScheduledRun
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
	for i := range plan.Steps {
		step := &plan.Steps[i]
		if strings.TrimSpace(step.Tool) != "web.search" {
			continue
		}
		if !understandingHasFreshFactLookup(l.state.understanding) {
			continue
		}
		if step.Args == nil {
			step.Args = map[string]string{}
		}
		if strings.TrimSpace(step.Args["freshness"]) == "" {
			step.Args["freshness"] = "current"
		}
	}
	return plan
}

func understandingHasFreshFactLookup(understanding taskUnderstanding) bool {
	for _, capability := range understanding.Capabilities {
		if strings.TrimSpace(capability) == "fresh_fact_lookup" {
			return true
		}
	}
	return false
}

func (l *AgentLoop) resumeApprovedTask(ctx context.Context) *Response {
	if l.state.binding.Kind != bindingApprovalReply || !l.state.binding.ApprovalGranted {
		return nil
	}
	plan, ok := planFromTaskState(l.state.currentTask)
	if !ok {
		return nil
	}
	l.receive()
	l.loadLongMemory()
	l.state.inboxReminder = l.loadInboxReminder()
	l.state.understanding = l.fallbackUnderstanding()
	l.state.plan = plan
	l.act(ctx, plan)
	if l.state.control != "" {
		resp := l.controlReply()
		return &resp
	}
	if l.shouldRepairBeforeSynthesis() {
		l.repair(ctx)
		if l.state.control != "" {
			resp := l.controlReply()
			return &resp
		}
	}
	if l.shouldBlockUnsupportedSynthesis() {
		resp := l.fail(fmt.Errorf("insufficient tool evidence for grounded answer"))
		return &resp
	}
	l.finalAccept(ctx)
	if l.state.finalAccept.Status == AcceptanceRejected {
		if !l.state.repairAttempted {
			l.state.repairReason = firstNonEmpty(l.state.finalAccept.Reason, "final acceptance rejected")
			l.repair(ctx)
			if l.state.control != "" {
				resp := l.controlReply()
				return &resp
			}
			l.state.finalAccept = FinalAcceptance{}
			l.finalAccept(ctx)
		}
		if anyFailed(l.state.results) || l.state.finalAccept.Status == AcceptanceRejected {
			resp := l.fail(fmt.Errorf(firstNonEmpty(l.state.finalAccept.Reason, "final acceptance rejected")))
			return &resp
		}
	}
	l.synthesize(ctx)
	resp := l.finalReply()
	return &resp
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
	var previousResults []model.ToolResult
	if l.state.currentTask != nil {
		if l.state.currentTask.Status != session.TaskFailed && !l.state.currentTask.Failed {
			previous = l.state.currentTask.StepStates
		}
	}
	results, control := l.runtime.executePlan(ctx, l.state.traceID, plan, l.state.binding.ApprovalGranted, l.state.binding.ApprovalStepID, previous, previousResults)
	l.state.plan = plan
	l.state.results = results
	if len(l.state.deferredResults) > 0 {
		l.state.results = append(l.state.results, l.state.deferredResults...)
		l.state.deferredResults = nil
	}
	l.state.control = control
}

func (l *AgentLoop) shouldRepairBeforeSynthesis() bool {
	if l.state.control != "" {
		return false
	}
	return l.remainingRepairBudget() > 0 && (hasRepairableFailure(l.state.results) || needsGroundingEvidence(l.state.resolvedRequest(), l.state.results))
}

func (l *AgentLoop) shouldBlockUnsupportedSynthesis() bool {
	if l.state.control != "" {
		return false
	}
	if onlyDependencyFailuresAfterSourceSuspect(l.state.results) {
		return false
	}
	if hasGroundingEvidence(l.state.results) {
		return false
	}
	return needsGroundingEvidence(l.state.resolvedRequest(), l.state.results)
}

func (l *AgentLoop) repair(ctx context.Context) {
	approvedCommand := l.approvedTerminalWriteCommand()
	for l.remainingRepairBudget() > 0 {
		if !l.repairPlan(ctx) {
			return
		}
		if l.applyRepairPlanVerification() {
			continue
		}
		var previous map[string]session.StepState
		var priorResults []model.ToolResult
		if l.state.currentTask != nil {
			if l.state.currentTask.Status != session.TaskFailed && !l.state.currentTask.Failed {
				previous = l.state.currentTask.StepStates
				priorResults = l.state.results
			}
		}
		approvedStepID := l.state.binding.ApprovalStepID
		if repairedStepID := repairedTerminalWriteStepCoveredByApproval(l.state.results, l.state.plan, l.state.binding.ApprovalGranted, approvedCommand); repairedStepID != "" {
			approvedStepID = repairedStepID
		}
		l.state.historicalResults = append(l.state.historicalResults, l.state.results...)
		results, control := l.runtime.executePlan(ctx, l.state.traceID, l.state.plan, l.state.binding.ApprovalGranted, approvedStepID, previous, priorResults)
		l.state.results = mergeToolResultsForPlan(l.state.plan, l.state.results, results)
		l.state.control = control
		if l.state.control != "" {
			return
		}
		if l.state.repairNeedsContinuation {
			l.state.repairNeedsContinuation = false
			l.state.repairReason = "continue from the newly collected local CLI usage evidence and build the next concrete command"
			continue
		}
		if !hasRepairableFailure(l.state.results) {
			return
		}
	}
}

func (l *AgentLoop) applyRepairPlanVerification() bool {
	verification := verifyPlanContract(l.state.plan, l.runtime.Tools, l.state.resolvedRequest(), l.state.understanding)
	verification.Errors = filterRepairVerificationErrorsSatisfiedByHistory(verification.Errors, completedStepIDs(l.state.results, l.state.historicalResults))
	if !verification.Blocking() {
		return false
	}
	l.state.repairReason = "repair_plan_invalid:\n" + verification.RepairGuidance()
	if diagnosticPlan, ok := safeDiagnosticPrefixForBlockedPlan(l.state.plan, l.state.resolvedRequest()); ok {
		l.state.plan = diagnosticPlan
		l.state.repairNeedsContinuation = true
		return false
	}
	if discoveryPlan, ok := safeDiscoveryPrefixForBlockedPlan(l.state.plan); ok {
		l.state.plan = discoveryPlan
		l.state.repairNeedsContinuation = true
		return false
	}
	return true
}

func completedStepIDs(groups ...[]model.ToolResult) map[string]bool {
	out := map[string]bool{}
	for _, results := range groups {
		for _, result := range results {
			if !result.OK {
				continue
			}
			if id := strings.TrimSpace(result.StepID); id != "" {
				out[id] = true
			}
		}
	}
	return out
}

func filterRepairVerificationErrorsSatisfiedByHistory(errors []string, completed map[string]bool) []string {
	if len(errors) == 0 || len(completed) == 0 {
		return errors
	}
	filtered := make([]string, 0, len(errors))
	for _, err := range errors {
		trimmed := strings.TrimSpace(err)
		if dep := historicalDependencyFromVerificationError(trimmed); dep != "" && completed[dep] {
			continue
		}
		filtered = append(filtered, err)
	}
	return filtered
}

func historicalDependencyFromVerificationError(text string) string {
	const marker = ": dependency "
	idx := strings.Index(text, marker)
	if idx < 0 || !strings.HasSuffix(text, " does not reference an earlier step") {
		return ""
	}
	rest := text[idx+len(marker):]
	rest = strings.TrimSuffix(rest, " does not reference an earlier step")
	return strings.TrimSpace(rest)
}

func (l *AgentLoop) approvedTerminalWriteCommand() string {
	if !l.state.binding.ApprovalGranted || l.state.currentTask == nil {
		return ""
	}
	stepID := strings.TrimSpace(l.state.binding.ApprovalStepID)
	if stepID == pendingApprovalStepID(l.state.currentTask.PendingApproval) {
		if command := commandFromPendingApproval(l.state.currentTask.PendingApproval); terminalCommandLooksExternalWriteAction(command) {
			return command
		}
	}
	if stepID != "" && l.state.currentTask.StepStates != nil {
		if step, ok := l.state.currentTask.StepStates[stepID]; ok {
			if command := terminalWriteCommandFromStepState(step); command != "" {
				return command
			}
		}
	}
	for i := len(l.state.currentTask.StepHistory) - 1; i >= 0; i-- {
		step := l.state.currentTask.StepHistory[i]
		if stepID != "" && strings.TrimSpace(step.ID) != stepID {
			continue
		}
		if command := terminalWriteCommandFromStepState(step); command != "" {
			return command
		}
	}
	return ""
}

func terminalWriteCommandFromStepState(step session.StepState) string {
	if strings.TrimSpace(step.Tool) != "terminal.run" {
		return ""
	}
	if command := strings.TrimSpace(step.Args["command"]); terminalCommandLooksExternalWriteAction(command) {
		return command
	}
	if command := strings.TrimSpace(stringValue(step.Evidence["command"])); terminalCommandLooksExternalWriteAction(command) {
		return command
	}
	return ""
}

func (l *AgentLoop) repairPlan(ctx context.Context) bool {
	if l.remainingRepairBudget() <= 0 {
		return false
	}
	l.state.repairAttempted = true
	l.state.repairCycles++
	allTools := l.runtime.availableToolDefinitions()
	candidateTools := composeCandidateTools(allTools, l.state.understanding, repairCandidateBudget, repairCandidateHints(l.state.results, l.state.repairReason)...)
	planMatches := skill.SelectMatches(l.skillDefinitions(), skill.StagePlanningRepair, l.skillContext())
	planSkills := make([]skill.Definition, 0, len(planMatches))
	for _, match := range planMatches {
		planSkills = append(planSkills, match.Definition)
	}
	l.runtime.Logger.Event("runtime.skills_selected", map[string]any{
		"trace_id": l.state.traceID,
		"stage":    skill.StagePlanningRepair,
		"skills":   selectedSkillsTraceFields(planMatches),
	})
	l.recordSelectedSkills(planSkills)
	contextPrompt := l.buildContextPrompt(skill.StagePlanningRepair, planMatches)
	if guidance := l.cliUsagePlanningGuidance(); guidance != "" {
		contextPrompt = strings.TrimSpace(contextPrompt + "\n\n" + guidance)
	}
	if recommendation := renderRecommendedTools(candidateTools); recommendation != "" {
		contextPrompt = strings.TrimSpace(contextPrompt + "\n\n" + recommendation)
	}
	repairGuidance := strings.TrimSpace(l.state.repairReason)
	if repairGuidance != "" {
		contextPrompt = strings.TrimSpace(contextPrompt + "\n\nRepair guidance:\n" + repairGuidance)
	}
	if installGuidance := softwareInstallRepairGuidance(l.state.results); installGuidance != "" {
		contextPrompt = strings.TrimSpace(contextPrompt + "\n\nSoftware install repair guidance:\n" + installGuidance)
	}
	if cliGuidance := terminalFailureRepairGuidance(l.state.results); cliGuidance != "" {
		contextPrompt = strings.TrimSpace(contextPrompt + "\n\nLocal CLI failure repair guidance:\n" + cliGuidance)
	}
	if preserve := preserveSuccessfulEvidenceGuidance(l.state.results); preserve != "" {
		contextPrompt = strings.TrimSpace(contextPrompt + "\n\nPreserve successful evidence:\n" + preserve)
	}
	l.state.previousPlanSummary = strings.TrimSpace(l.state.plan.Summary)
	repaired, check, err := l.repairPlanJSON(ctx, buildStageModelPrompt(contextPrompt, planSkills), candidateTools)
	if err != nil {
		l.runtime.Logger.Event("runtime.plan_repair_failed", map[string]any{
			"trace_id": l.state.traceID,
			"error":    err.Error(),
		})
		return false
	}
	repaired = l.normalizePlanForRuntime(repaired)
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

func (l *AgentLoop) remainingRepairBudget() int {
	maxCycles := 1
	if localCLIReactCandidate(l.state.resolvedRequest(), l.state.results) || repairContinuationCandidate(l.state.results) {
		maxCycles = 3
	}
	return maxCycles - l.state.repairCycles
}

func localCLIReactCandidate(user string, results []model.ToolResult) bool {
	if !textLooksLikeLocalCLIUseRequest(strings.ToLower(strings.TrimSpace(user))) {
		return false
	}
	if cli := cliUsageCandidateFromText(user); cli.Executable != "" {
		return true
	}
	for _, result := range results {
		if strings.TrimSpace(result.Tool) != "terminal.run" {
			continue
		}
		command := strings.TrimSpace(stringValue(result.Evidence["command"]))
		if command == "" {
			continue
		}
		root := commandRoot(command)
		if root == "" || commonCLIExecutable(root) || rootLooksLikeLocalProjectCommand(root) {
			continue
		}
		return true
	}
	return false
}

func repairContinuationCandidate(results []model.ToolResult) bool {
	for _, result := range results {
		if strings.TrimSpace(result.Tool) != "plan.verify" {
			continue
		}
		switch strings.TrimSpace(result.Error) {
		case "plan_contract_invalid", "plan_contract_invalid_after_repair":
			return true
		}
	}
	return false
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
		IsScheduledRun:  isScheduledInvocation(l.state.message),
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

func preserveSuccessfulEvidenceGuidance(results []model.ToolResult) string {
	var keep []string
	for _, result := range results {
		if !result.OK {
			continue
		}
		switch strings.TrimSpace(result.Tool) {
		case "project.index", "file.read", "file.summary", "web.fetch", "web.search", "software.search", "skill.search", "memory.search":
			keep = append(keep, strings.TrimSpace(result.StepID)+" via "+strings.TrimSpace(result.Tool))
		}
	}
	if len(keep) == 0 {
		return ""
	}
	return "Do not discard already successful read evidence unless it is clearly irrelevant. Prefer continuing from these completed steps: " + strings.Join(keep, ", ")
}

func softwareInstallRepairGuidance(results []model.ToolResult) string {
	hasSoftwareSearch := false
	hasWeakSearch := false
	hasFetchFailure := false
	for _, result := range results {
		switch strings.TrimSpace(result.Tool) {
		case "software.search":
			hasSoftwareSearch = true
			if softwareSearchResultLooksWeak(model.PlanStep{Tool: "software.search"}, result) {
				hasWeakSearch = true
			}
		case "web.fetch":
			if !result.OK {
				hasFetchFailure = true
			}
		}
	}
	if !hasSoftwareSearch && !hasFetchFailure {
		return ""
	}
	parts := []string{
		"Do not emit web.fetch until the upstream URL is explicit and credible.",
		"Do not emit software.install until install_command, verify_command, and executable_name are concrete.",
	}
	if hasWeakSearch {
		parts = append(parts, "If software.search returns weak or mismatched results, do not guess a repository, README URL, npm package, or install command. Repair source discovery first: try a narrower software.search query with the exact package/project name, npm/package-manager clues, author/owner clues from prior evidence, or an official-docs query. Only stop after these source-discovery attempts still fail.")
	}
	if hasFetchFailure {
		parts = append(parts, "If web.fetch failed because the URL was guessed or invalid, repair by narrowing the source-finding step first instead of emitting another guessed URL.")
	}
	return strings.Join(parts, " ")
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
			Args:      copyStringMap(step.Args),
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

func appendStepHistory(existing []session.StepState, plan model.Plan, results []model.ToolResult, now time.Time) []session.StepState {
	current := buildStepStatesForHistory(plan, results, now)
	if len(current) == 0 {
		return existing
	}
	out := append([]session.StepState(nil), existing...)
	for _, id := range historyStepOrder(plan, results) {
		state, ok := current[id]
		if !ok {
			continue
		}
		if state.AttemptCount == 0 && strings.TrimSpace(state.AcceptanceStatus) == "" {
			continue
		}
		out = append(out, state)
	}
	const maxStepHistory = 40
	if len(out) > maxStepHistory {
		out = out[len(out)-maxStepHistory:]
	}
	return out
}

func buildStepStatesForHistory(plan model.Plan, results []model.ToolResult, now time.Time) map[string]session.StepState {
	out := buildStepStates(plan, results, now)
	if out == nil {
		out = map[string]session.StepState{}
	}
	planByID := map[string]model.PlanStep{}
	for _, step := range plan.Steps {
		planByID[strings.TrimSpace(step.ID)] = step
	}
	for _, result := range results {
		id := strings.TrimSpace(result.StepID)
		if id == "" {
			continue
		}
		if _, ok := out[id]; ok {
			continue
		}
		step := planByID[id]
		out[id] = stepStateFromResult(step, result, now)
	}
	return out
}

func stepStateFromResult(step model.PlanStep, result model.ToolResult, now time.Time) session.StepState {
	id := firstNonEmpty(strings.TrimSpace(step.ID), strings.TrimSpace(result.StepID))
	toolName := firstNonEmpty(strings.TrimSpace(step.Tool), strings.TrimSpace(result.Tool))
	item := session.StepState{
		ID:            id,
		Goal:          step.Goal,
		Tool:          toolName,
		Args:          copyStringMap(step.Args),
		Status:        "pending",
		AttemptCount:  1,
		DependsOn:     append([]string(nil), step.DependsOn...),
		ResultOK:      result.OK,
		ResultError:   result.Error,
		ResultSummary: shortenReply(result.Output, 240),
		Evidence:      result.Evidence,
		StartedAt:     now,
		FinishedAt:    now,
		UpdatedAt:     now,
	}
	if len(item.Args) == 0 {
		if command := strings.TrimSpace(stringValue(result.Evidence["command"])); command != "" && toolName == "terminal.run" {
			item.Args = map[string]string{"command": command}
		}
	}
	switch {
	case result.Error == "await_confirm":
		item.Status = "blocked"
		item.AcceptanceStatus = "await_confirm"
	case result.OK:
		item.Status = "passed"
		item.AcceptanceStatus = "passed"
	default:
		item.Status = "failed"
		item.AcceptanceStatus = "failed"
		item.AcceptanceReason = firstNonEmpty(result.Error, result.Output)
	}
	return item
}

func historyStepOrder(plan model.Plan, results []model.ToolResult) []string {
	out := planStepOrder(plan)
	seen := map[string]bool{}
	for _, id := range out {
		seen[strings.TrimSpace(id)] = true
	}
	for _, result := range results {
		id := strings.TrimSpace(result.StepID)
		if id == "" || seen[id] {
			continue
		}
		out = append(out, id)
		seen[id] = true
	}
	return out
}

func planFromTaskState(task *session.TaskState) (model.Plan, bool) {
	if task == nil || len(task.StepOrder) == 0 || len(task.StepStates) == 0 {
		return model.Plan{}, false
	}
	steps := make([]model.PlanStep, 0, len(task.StepOrder))
	for _, id := range task.StepOrder {
		state, ok := task.StepStates[id]
		if !ok || strings.TrimSpace(state.ID) == "" || strings.TrimSpace(state.Tool) == "" {
			return model.Plan{}, false
		}
		args := copyStringMap(state.Args)
		if len(args) == 0 && strings.TrimSpace(state.Tool) == "terminal.run" && strings.TrimSpace(state.ID) == pendingApprovalStepID(task.PendingApproval) {
			if command := commandFromPendingApproval(task.PendingApproval); command != "" {
				args = map[string]string{"command": command}
			}
		}
		if len(args) == 0 && toolRequiresArgsForResume(state.Tool) {
			return model.Plan{}, false
		}
		steps = append(steps, model.PlanStep{
			ID:        state.ID,
			Goal:      state.Goal,
			Tool:      state.Tool,
			Args:      args,
			DependsOn: append([]string(nil), state.DependsOn...),
		})
	}
	if len(steps) == 0 {
		return model.Plan{}, false
	}
	return model.Plan{Summary: task.PlanSummary, Steps: steps}, true
}

func toolRequiresArgsForResume(toolName string) bool {
	switch strings.TrimSpace(toolName) {
	case "terminal.run", "shell.run", "file.write", "file.patch", "software.install", "schedule.create", "schedule.update", "schedule.delete":
		return true
	default:
		return false
	}
}

func commandFromPendingApproval(approval *session.PendingApproval) string {
	if approval == nil {
		return ""
	}
	text := strings.TrimSpace(approval.Prompt)
	if text == "" {
		return ""
	}
	markers := []string{"命令：`", "Command: `", "command: `"}
	for _, marker := range markers {
		start := strings.Index(text, marker)
		if start < 0 {
			continue
		}
		rest := text[start+len(marker):]
		end := strings.Index(rest, "`")
		if end > 0 {
			return strings.TrimSpace(rest[:end])
		}
	}
	return ""
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
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
	text, err := l.runtime.Model.Synthesize(ctx, l.state.resolvedRequest(), l.state.plan, l.state.results, buildStageModelPrompt(contextPrompt, synthSkills))
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
	reg := l.runtime.currentSkills()
	if reg == nil {
		return nil
	}
	return reg.Definitions()
}

func (l *AgentLoop) buildContextPrompt(stage string, matches []skill.Match) string {
	shortMemory := l.state.shortMemory.Text
	if l.state.shortMemory.SessionPresent {
		shortMemory = buildShortMemorySummaryForStage(l.state.session, stage).Text
	}
	return buildModelContextPrompt(l.state.resolvedRequest(), stage, matches, l.runtime.availableToolDefinitions(), l.runtime.ToolCtx, promptContextOptions{
		ShortMemory:   shortMemory,
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
	if l.state.failed {
		if text := userFacingTerminalPreconditionMessage(l.state.results); text != "" {
			l.state.replyText = text
		}
	}
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
	if resp.Reply.Style == "reply" && !resp.Failed && !resp.AwaitConfirm && !resp.AwaitUserInput && !learning.CandidateGenerated {
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
	task.StepHistory = appendStepHistory(task.StepHistory, l.state.plan, append(l.state.historicalResults, l.state.results...), finishedAt)
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
		existingApproval := task.PendingApproval
		if task.PendingApproval != nil {
			action := strings.TrimSpace(task.PendingApproval.RequestedAction)
			if strings.HasPrefix(action, "approved:") {
				approvedStepID = strings.TrimSpace(strings.TrimPrefix(action, "approved:"))
			}
		}
		if existingApproval != nil && isDirectMatewayApprovalAction(existingApproval.RequestedAction) {
			task.PendingApproval = existingApproval
			task.PendingQuestions = nil
			l.runtime.Logger.Event("runtime.task_pending_approval", map[string]any{
				"trace_id": l.state.traceID,
				"task_id":  task.ID,
			})
		} else {
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
		}
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
	learning := l.recordLearningPattern(resp, task)
	l.maybeProposeSkillImprovement(resp, task)
	l.maybeWriteCLIUsageMemory(resp, task)
	if !learning.CandidateGenerated {
		l.proposeMemoryFromTask(resp.Reply, task)
	}
	return learning
}

func (l *AgentLoop) cliUsagePlanningGuidance() string {
	ctx := l.state.cliUsage
	if ctx.Executable == "" {
		return ""
	}
	if ctx.MemoryFound {
		return "CLI usage memory:\nA long-memory playbook for `" + ctx.Executable + "` is available in Relevant long memory. Use its verified command shapes before constructing local terminal commands."
	}
	return strings.Join([]string{
		"CLI usage memory requirement:",
		"- The user request involves unknown external CLI `" + ctx.Executable + "`, and no long-memory playbook titled `CLI usage: " + ctx.Executable + "` was found.",
		"- Before executing the user's actual CLI task, first discover usage with read-only commands: verify the executable path, run version/help when available, then inspect the relevant parent and exact subcommand help.",
		"- usage discovery is only a prerequisite. After the usage is clear, continue the user's original task in the same plan.",
		"- If a parent help page only lists candidate subcommands, inspect the exact listed candidate help before building a write command.",
	}, "\n")
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

func (l *AgentLoop) maybeProposeSkillImprovement(resp Response, task session.TaskState) {
	if l.runtime.Config == nil || !l.runtime.Config.Learning.Enabled {
		return
	}
	if !l.state.repairAttempted || resp.Failed || resp.AwaitConfirm || resp.AwaitUserInput {
		return
	}
	if task.Status != session.TaskCompleted {
		return
	}
	skillName := firstSelectedSkill(task.SelectedSkills)
	if skillName == "" {
		return
	}
	agentID := firstNonEmpty(l.runtime.Config.Agents.Default, "main")
	path, err := l.runtime.Memory.WriteSkillImprovementProposal(memory.SkillImprovementInput{
		AgentID:             agentID,
		SkillName:           skillName,
		ImprovementType:     inferSkillImprovementType(l.state.repairReason),
		Reason:              buildSkillImprovementReason(skillName, l.state.repairReason),
		ProposedChange:      buildSkillImprovementChange(skillName, l.state.previousPlanSummary, l.state.plan.Summary),
		RepairReason:        strings.TrimSpace(l.state.repairReason),
		PreviousPlanSummary: strings.TrimSpace(l.state.previousPlanSummary),
		RepairedPlanSummary: strings.TrimSpace(l.state.plan.Summary),
		TaskID:              task.ID,
		TraceID:             l.state.traceID,
		Sources:             []string{"task:" + task.ID, "trace:" + l.state.traceID},
	})
	if err != nil {
		l.runtime.Logger.Event("runtime.skill_improvement_failed", map[string]any{
			"trace_id": l.state.traceID,
			"skill":    skillName,
			"error":    err.Error(),
		})
		return
	}
	l.runtime.Logger.Event("runtime.skill_improvement_proposed", map[string]any{
		"trace_id": l.state.traceID,
		"skill":    skillName,
		"path":     path,
		"type":     inferSkillImprovementType(l.state.repairReason),
	})
}

func firstSelectedSkill(skills []string) string {
	for _, skillName := range skills {
		if strings.TrimSpace(skillName) != "" {
			return strings.TrimSpace(skillName)
		}
	}
	return ""
}

func inferSkillImprovementType(repairReason string) string {
	text := strings.ToLower(strings.TrimSpace(repairReason))
	switch {
	case strings.Contains(text, "missing"), strings.Contains(text, "evidence"), strings.Contains(text, "verify"):
		return "weak_verification"
	case strings.Contains(text, "unclear"), strings.Contains(text, "ambiguous"):
		return "unclear_instruction"
	default:
		return "missing_step"
	}
}

func buildSkillImprovementReason(skillName, repairReason string) string {
	reason := strings.TrimSpace(repairReason)
	if reason == "" {
		return fmt.Sprintf("The current %s flow needed repair before the task could complete successfully.", skillName)
	}
	return fmt.Sprintf("The current %s flow needed repair before the task could complete successfully. Repair signal: %s", skillName, reason)
}

func buildSkillImprovementChange(skillName, previousPlanSummary, repairedPlanSummary string) string {
	switch {
	case strings.TrimSpace(previousPlanSummary) != "" && strings.TrimSpace(repairedPlanSummary) != "":
		return fmt.Sprintf("Update %s so the default flow moves from %q toward %q when similar tasks are detected.", skillName, strings.TrimSpace(previousPlanSummary), strings.TrimSpace(repairedPlanSummary))
	case strings.TrimSpace(repairedPlanSummary) != "":
		return fmt.Sprintf("Update %s to incorporate the repaired flow: %s", skillName, strings.TrimSpace(repairedPlanSummary))
	default:
		return fmt.Sprintf("Update %s to include the missing repair guidance that was needed in this task.", skillName)
	}
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
