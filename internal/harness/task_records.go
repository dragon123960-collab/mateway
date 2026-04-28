package harness

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/memory"
)

var absolutePathPattern = regexp.MustCompile(`/(?:[^ \n\r\t"']+)`)

func (h *Harness) shouldTrackTaskRecord(run Run) bool {
	return strings.TrimSpace(run.TaskID) != "" && strings.TrimSpace(run.ParentRunID) == ""
}

func (h *Harness) syncTaskRecordFromRun(ctx context.Context, run Run) error {
	if !h.shouldTrackTaskRecord(run) || strings.TrimSpace(h.Memory.Workspace) == "" {
		return nil
	}
	record, ok, err := h.Memory.GetTaskRecord(ctx, run.TaskID)
	if err != nil {
		return err
	}
	if !ok {
		record = memory.TaskRecord{
			TaskID:       run.TaskID,
			CreatedAt:    run.CreatedAt,
			SourceRunIDs: []string{},
		}
	}
	record.TaskID = run.TaskID
	record.SessionKey = run.SessionKey
	record.ThreadID = run.ThreadID
	record.Channel = run.Channel
	record.AgentName = run.AgentName
	record.TaskType = run.TaskType
	record.OriginKind = firstNonEmpty(run.OriginKind, record.OriginKind, "manual")
	record.Goal = firstNonEmpty(strings.TrimSpace(run.Goal), record.Goal)
	record.LatestUserText = firstNonEmpty(strings.TrimSpace(run.Goal), record.LatestUserText)
	record.Route = firstNonEmpty(strings.TrimSpace(run.Route), record.Route)
	record.SelectedSkills = append([]string(nil), run.SelectedSkills...)
	record.VisibleTools = append([]string(nil), run.VisibleTools...)
	record.InitialPlan = firstNonEmpty(taskInitialPlanFromRun(run), record.InitialPlan)
	if plan := taskStructuredPlanFromRun(run); len(plan) > 0 {
		record.StructuredPlan = plan
	}
	record.Status = firstNonEmpty(strings.TrimSpace(run.Status), record.Status, "running")
	record.DeliveryStatus = taskDeliveryStatusFromRun(run)
	record.SourceRunIDs = append(record.SourceRunIDs, run.ID)
	record.ChildRunIDs = append(record.ChildRunIDs, run.ChildRunIDs...)
	record.ScheduleName = firstNonEmpty(run.ScheduleName, record.ScheduleName)
	record.ScheduleJobID = firstNonEmpty(run.ScheduleJobID, record.ScheduleJobID)
	record.TriggeredAt = firstNonZeroTime(run.ScheduleTriggeredAt, record.TriggeredAt)
	record.LatestFailure = firstNonEmpty(strings.TrimSpace(run.Error), latestFailureFromRun(run), record.LatestFailure)
	if actions := taskRecoveryActionsFromRun(run); len(actions) > 0 {
		record.RecoveryActions = actions
	}
	if lessonIDs, wikiPages := taskKnowledgeLinksFromRun(run); len(lessonIDs) > 0 || len(wikiPages) > 0 {
		record.LessonIDs = append(record.LessonIDs, lessonIDs...)
		record.WikiPagePaths = append(record.WikiPagePaths, wikiPages...)
	}
	contract := buildCompletionContractFromRun(run)
	record.Completion = contract
	record.DeliveryStatus = firstNonEmpty(contract.DeliveryStatus, record.DeliveryStatus)
	if completedAt := taskCompletedAt(run); !completedAt.IsZero() {
		record.CompletedAt = completedAt
	}
	_, err = h.Memory.UpsertTaskRecord(ctx, record)
	return err
}

func buildCompletionContractFromRun(run Run) memory.CompletionContract {
	status := normalizeCompletionStatus(run)
	summary := taskSummaryFromRun(run)
	primary, secondary := taskArtifactsFromRun(run)
	evidence := taskEvidenceFromRun(run, primary, secondary)
	nextAction := taskNextActionFromRun(run)
	deliveryStatus := taskDeliveryStatusFromRun(run)

	if summary == "" {
		summary = firstNonEmpty(strings.TrimSpace(run.Result), strings.TrimSpace(run.Error))
	}
	if summary == "" {
		summary = "任务已结束，但没有生成可展示的摘要。"
	}

	if strings.EqualFold(status, "completed") {
		if len(evidence) == 0 {
			status = "partial"
		}
		switch run.TaskType {
		case TaskTypeSchedule, TaskTypeCodeWrite, TaskTypeDiagnose:
			if primary == nil || strings.TrimSpace(primary.PathOrRef) == "" {
				status = "partial"
			}
		}
	}

	return memory.CompletionContract{
		Status:             status,
		Summary:            summary,
		PrimaryArtifact:    primary,
		SecondaryArtifacts: secondary,
		Evidence:           evidence,
		NextAction:         nextAction,
		DeliveryStatus:     deliveryStatus,
	}
}

func normalizeCompletionStatus(run Run) string {
	switch strings.ToLower(strings.TrimSpace(run.Status)) {
	case "completed":
		return "completed"
	case "waiting_approval":
		return "waiting_approval"
	case "failed", "denied":
		return "failed"
	case "running":
		return "partial"
	default:
		if strings.TrimSpace(run.Error) != "" {
			return "failed"
		}
		return "partial"
	}
}

func taskSummaryFromRun(run Run) string {
	if strings.TrimSpace(run.Result) != "" {
		return summarizeForSessionMemory(run.Result, 220)
	}
	if strings.TrimSpace(run.Error) != "" {
		return summarizeForSessionMemory(run.Error, 220)
	}
	switch strings.ToLower(strings.TrimSpace(run.Status)) {
	case "running":
		return "任务执行中。"
	case "waiting_approval":
		return "任务等待批准后继续执行。"
	case "failed":
		return "任务执行失败。"
	default:
		return ""
	}
}

func taskArtifactsFromRun(run Run) (*memory.ArtifactRef, []memory.ArtifactRef) {
	primary := explicitArtifactFromRun(run)
	if primary == nil {
		primary = inferredArtifactFromRun(run)
	}
	secondary := []memory.ArtifactRef{
		{
			ArtifactType: "run_record",
			Label:        "run trace",
			PathOrRef:    filepath.ToSlash(filepath.Join("memory", "runs", run.ID+".json")),
			CreatedAt:    time.Now(),
		},
	}
	if strings.TrimSpace(run.TaskID) != "" {
		secondary = append(secondary, memory.ArtifactRef{
			ArtifactType: "task_record",
			Label:        "canonical task record",
			PathOrRef:    filepath.ToSlash(filepath.Join("memory", "tasks", sanitizeTaskRecordID(run.TaskID)+".json")),
			CreatedAt:    time.Now(),
		})
	}
	return primary, secondary
}

func explicitArtifactFromRun(run Run) *memory.ArtifactRef {
	switch {
	case strings.EqualFold(run.Mode, "tool") && strings.EqualFold(run.ToolName, "write_file"):
		if path := strings.TrimSpace(strings.Trim(run.Result, `"`)); path != "" {
			return &memory.ArtifactRef{ArtifactType: "file", Label: "written file", PathOrRef: path, CreatedAt: time.Now()}
		}
	case strings.EqualFold(run.Mode, "tool") && strings.HasPrefix(run.ToolName, "schedule_"):
		if run.ScheduleName != "" {
			return &memory.ArtifactRef{ArtifactType: "schedule", Label: "schedule", PathOrRef: run.ScheduleName, CreatedAt: time.Now()}
		}
	case run.ScheduleName != "":
		return &memory.ArtifactRef{ArtifactType: "schedule", Label: "trigger schedule", PathOrRef: run.ScheduleName, CreatedAt: time.Now()}
	}
	return nil
}

func inferredArtifactFromRun(run Run) *memory.ArtifactRef {
	if path := firstAbsolutePath(run.Result, run.Goal); path != "" {
		return &memory.ArtifactRef{ArtifactType: "file", Label: "referenced path", PathOrRef: path, CreatedAt: time.Now()}
	}
	switch run.TaskType {
	case TaskTypeDiagnose:
		return &memory.ArtifactRef{ArtifactType: "root_cause", Label: "diagnosis", PathOrRef: taskSummaryFromRun(run), CreatedAt: time.Now()}
	case TaskTypeSchedule:
		if run.ScheduleName != "" {
			return &memory.ArtifactRef{ArtifactType: "schedule", Label: "schedule", PathOrRef: run.ScheduleName, CreatedAt: time.Now()}
		}
		if strings.TrimSpace(run.TaskID) != "" {
			return &memory.ArtifactRef{ArtifactType: "schedule_context", Label: "task context", PathOrRef: run.TaskID, CreatedAt: time.Now()}
		}
	case TaskTypeCodeWrite:
		if strings.TrimSpace(run.Result) != "" {
			return &memory.ArtifactRef{ArtifactType: "code_change", Label: "write result", PathOrRef: taskSummaryFromRun(run), CreatedAt: time.Now()}
		}
	}
	return nil
}

func taskEvidenceFromRun(run Run, primary *memory.ArtifactRef, secondary []memory.ArtifactRef) []memory.EvidenceRef {
	evidence := []memory.EvidenceRef{
		{
			EvidenceType: "run_record",
			Label:        "run record",
			Value:        filepath.ToSlash(filepath.Join("memory", "runs", run.ID+".json")),
			CreatedAt:    time.Now(),
		},
	}
	if primary != nil && strings.TrimSpace(primary.PathOrRef) != "" {
		evidence = append(evidence, memory.EvidenceRef{
			EvidenceType: "artifact",
			Label:        firstNonEmpty(primary.Label, primary.ArtifactType, "artifact"),
			Value:        primary.PathOrRef,
			CreatedAt:    time.Now(),
		})
	}
	if strings.TrimSpace(run.Error) != "" {
		evidence = append(evidence, memory.EvidenceRef{
			EvidenceType: "error",
			Label:        "latest error",
			Value:        summarizeForSessionMemory(run.Error, 240),
			CreatedAt:    time.Now(),
		})
	}
	if strings.TrimSpace(run.ScheduleName) != "" {
		evidence = append(evidence, memory.EvidenceRef{
			EvidenceType: "schedule",
			Label:        "schedule context",
			Value:        run.ScheduleName,
			CreatedAt:    time.Now(),
		})
	}
	if len(secondary) > 0 {
		evidence = append(evidence, memory.EvidenceRef{
			EvidenceType: "task_record",
			Label:        "task record",
			Value:        filepath.ToSlash(filepath.Join("memory", "tasks", sanitizeTaskRecordID(run.TaskID)+".json")),
			CreatedAt:    time.Now(),
		})
	}
	return evidence
}

func taskNextActionFromRun(run Run) string {
	switch strings.ToLower(strings.TrimSpace(run.Status)) {
	case "waiting_approval":
		return "等待批准后继续执行。"
	case "failed":
		if strings.TrimSpace(run.Error) != "" {
			return "优先查看失败记录、相关 run 与 lesson record，再决定下一步修复路径。"
		}
		return "优先查看 task record 和 run record 中的错误信息。"
	}
	switch run.TaskType {
	case TaskTypeSchedule:
		return "如需追问成果位置，优先读取该任务主档中的 primary_artifact。"
	case TaskTypeDiagnose:
		return "根据 root cause 和 evidence 决定是修复、回退还是补充监控。"
	case TaskTypeCodeWrite:
		return "根据 primary_artifact 和 evidence 继续做验证、提交或回归测试。"
	default:
		return "后续相似任务优先复用该 task record 与相关 evidence。"
	}
}

func taskDeliveryStatusFromRun(run Run) string {
	switch strings.ToLower(strings.TrimSpace(run.Status)) {
	case "completed":
		if strings.EqualFold(strings.TrimSpace(run.Channel), "schedule") {
			return "persisted_only"
		}
		return "returned_to_caller"
	case "waiting_approval":
		return "pending_approval"
	case "failed":
		return "failed"
	default:
		return "in_progress"
	}
}

func taskCompletedAt(run Run) time.Time {
	switch strings.ToLower(strings.TrimSpace(run.Status)) {
	case "completed", "failed", "denied":
		return run.UpdatedAt
	default:
		return time.Time{}
	}
}

func taskInitialPlanFromRun(run Run) string {
	for _, step := range run.Steps {
		if step.Kind == "dev_plan" && strings.TrimSpace(step.Output) != "" {
			return strings.TrimSpace(step.Output)
		}
	}
	return ""
}

func taskStructuredPlanFromRun(run Run) []string {
	out := make([]string, 0, 8)
	for _, step := range run.Steps {
		if (step.Kind == "plan" || step.Kind == "replan") && strings.TrimSpace(step.Output) != "" {
			for _, line := range strings.Split(step.Output, "\n") {
				line = strings.TrimSpace(line)
				if line != "" {
					out = append(out, line)
				}
			}
		}
	}
	return dedupeTaskStrings(out)
}

func latestFailureFromRun(run Run) string {
	for i := len(run.Steps) - 1; i >= 0; i-- {
		step := run.Steps[i]
		if strings.EqualFold(step.Status, "failed") && strings.TrimSpace(step.Output) != "" {
			return strings.TrimSpace(step.Output)
		}
	}
	return ""
}

func taskRecoveryActionsFromRun(run Run) []string {
	actions := make([]string, 0, len(run.RecoveryAttempts))
	for _, attempt := range run.RecoveryAttempts {
		text := strings.TrimSpace(firstNonEmpty(attempt.Action, attempt.Detail))
		if text != "" {
			actions = append(actions, text)
		}
	}
	return dedupeTaskStrings(actions)
}

func taskKnowledgeLinksFromRun(run Run) ([]string, []string) {
	lessonIDs := make([]string, 0, len(run.LearningProposals))
	wikiPages := make([]string, 0, len(run.LearningProposals))
	for _, proposal := range run.LearningProposals {
		if strings.TrimSpace(proposal.ID) != "" {
			lessonIDs = append(lessonIDs, proposal.ID)
		}
		if strings.Contains(strings.ToLower(strings.TrimSpace(proposal.TargetPath)), "memory/wiki/") {
			wikiPages = append(wikiPages, strings.TrimSpace(proposal.TargetPath))
		}
	}
	return dedupeTaskStrings(lessonIDs), dedupeTaskStrings(wikiPages)
}

func formatTaskRecordDigest(record memory.TaskRecord) string {
	goal := summarizeForSessionMemory(firstNonEmpty(record.Goal, record.LatestUserText), 90)
	if goal == "" {
		goal = "(未记录任务目标)"
	}
	outcome := summarizeForSessionMemory(firstNonEmpty(record.Completion.Summary, record.LatestFailure), 150)
	if outcome == "" {
		outcome = "暂无结果摘要"
	}
	return fmt.Sprintf("[%s] %s -> %s", firstNonEmpty(strings.TrimSpace(record.Status), "-"), goal, outcome)
}

func firstAbsolutePath(values ...string) string {
	for _, value := range values {
		matches := absolutePathPattern.FindAllString(value, -1)
		for _, match := range matches {
			if strings.TrimSpace(match) != "" {
				return match
			}
		}
	}
	return ""
}

func firstNonZeroTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value
		}
	}
	return time.Time{}
}

func artifactPathOrRef(artifact *memory.ArtifactRef) string {
	if artifact == nil {
		return ""
	}
	return strings.TrimSpace(artifact.PathOrRef)
}

func sanitizeTaskRecordID(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, ":", "_")
	value = strings.ReplaceAll(value, "/", "_")
	value = strings.ReplaceAll(value, " ", "_")
	value = strings.Trim(value, "._-")
	if value == "" {
		return "task"
	}
	return value
}

func dedupeTaskStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
