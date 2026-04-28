package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/filesystem"
	einotoolsearch "github.com/cloudwego/eino/adk/middlewares/dynamictool/toolsearch"
	einoreduction "github.com/cloudwego/eino/adk/middlewares/reduction"
	einosummarization "github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/dongping/mateway/internal/agents"
	"github.com/dongping/mateway/internal/capabilities"
	"github.com/dongping/mateway/internal/reflection"
	mwtools "github.com/dongping/mateway/internal/tools"
)

const (
	einoSummarizationContextMessages = 16
	einoSummarizationContextTokens   = 12000
	einoReductionMaxOutputLength     = 8000
	einoReductionClearTokens         = 9000
	einoReductionMinClearTokens      = 768
	einoReductionRetentionSuffix     = 6
)

var unsafeEinoSessionChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

type einoToolPlan struct {
	BaseTools    []einotool.BaseTool
	DynamicTools []einotool.BaseTool
	DynamicNames []string
}

type workspaceReductionBackend struct {
	workspace string
}

func (b workspaceReductionBackend) Write(_ context.Context, req *filesystem.WriteRequest) error {
	if req == nil {
		return fmt.Errorf("write request is required")
	}
	path := strings.TrimSpace(req.FilePath)
	if path == "" {
		return fmt.Errorf("file path is required")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(b.workspace, path)
	}
	if !isWithinHarnessWorkspace(path, b.workspace) {
		return fmt.Errorf("reduction path %q escapes workspace", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(req.Content), 0o644)
}

func (h *Harness) eligibleToolsForAgent(ctx context.Context, req Request, profile agents.Profile, effective capabilities.Effective) ([]mwtools.Tool, error) {
	if h.Tools == nil {
		return nil, nil
	}
	scope := mwtools.Scope{
		UserID:    req.UserID,
		Channel:   req.Channel,
		ThreadID:  firstNonEmpty(req.ThreadID, req.SessionKey),
		AgentName: profile.Name,
	}
	list, err := h.Tools.List(ctx, scope)
	if err != nil {
		return nil, err
	}
	out := make([]mwtools.Tool, 0, len(list))
	for _, item := range list {
		spec := item.Spec()
		if !capabilities.Allows(effective, spec.Name) {
			continue
		}
		if reporter, ok := item.(mwtools.AvailabilityReporter); ok {
			availability := reporter.Availability(ctx)
			if !availability.Available {
				continue
			}
		}
		if spec.Name == "spawn" || spec.Name == "wait_agent" {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func (h *Harness) adaptEinoTools(run Run, req Request, list []mwtools.Tool) []einotool.BaseTool {
	out := make([]einotool.BaseTool, 0, len(list))
	for _, item := range list {
		spec := item.Spec()
		out = append(out, &einoToolAdapter{
			harness: h,
			run:     run,
			req:     req,
			spec:    spec,
			tool:    item,
		})
	}
	return out
}

func (h *Harness) buildEinoToolPlan(ctx context.Context, req Request, run Run, profile agents.Profile, effective capabilities.Effective) (*einoToolPlan, error) {
	list, err := h.eligibleToolsForAgent(ctx, req, profile, effective)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return &einoToolPlan{}, nil
	}
	base, dynamic := splitDynamicToolsForGoal(firstNonEmpty(run.Goal, req.UserText), list)
	plan := &einoToolPlan{
		BaseTools: h.adaptEinoTools(run, req, base),
	}
	if len(dynamic) == 0 {
		return plan, nil
	}
	plan.DynamicTools = h.adaptEinoTools(run, req, dynamic)
	plan.DynamicNames = make([]string, 0, len(dynamic))
	for _, item := range dynamic {
		plan.DynamicNames = append(plan.DynamicNames, item.Spec().Name)
	}
	h.appendRunVisibleTool(run.ID, "tool_search")
	return plan, nil
}

func splitDynamicToolsForGoal(goal string, list []mwtools.Tool) ([]mwtools.Tool, []mwtools.Tool) {
	if len(list) == 0 {
		return nil, nil
	}
	allowed := progressiveToolDisclosure(goal, list)
	if len(allowed) == 0 {
		return append([]mwtools.Tool(nil), list...), nil
	}
	base := make([]mwtools.Tool, 0, len(list))
	dynamic := make([]mwtools.Tool, 0, len(list))
	for _, item := range list {
		if allowed[item.Spec().Name] {
			base = append(base, item)
			continue
		}
		dynamic = append(dynamic, item)
	}
	if len(base) == 0 || len(dynamic) < 2 {
		return append([]mwtools.Tool(nil), list...), nil
	}
	return base, dynamic
}

func (h *Harness) buildEinoChatMiddlewares(ctx context.Context, run Run, model model.BaseChatModel, modelOpts []model.Option, toolPlan *einoToolPlan, skillBundle *einoSkillBundle) ([]adk.ChatModelAgentMiddleware, error) {
	out := make([]adk.ChatModelAgentMiddleware, 0, 4)
	if skillBundle != nil && skillBundle.handler != nil {
		out = append(out, skillBundle.handler)
	}
	if toolPlan != nil && len(toolPlan.DynamicTools) > 0 {
		searchHandler, err := einotoolsearch.New(ctx, &einotoolsearch.Config{
			DynamicTools: toolPlan.DynamicTools,
		})
		if err != nil {
			return nil, fmt.Errorf("build tool search middleware: %w", err)
		}
		out = append(out, searchHandler)
	}
	reductionHandler, err := h.newEinoReductionMiddleware(ctx, run, toolPlanHasName(ctx, toolPlan, "read_file"))
	if err != nil {
		return nil, err
	}
	out = append(out, reductionHandler)
	summaryHandler, err := h.newEinoSummarizationMiddleware(ctx, run, model, modelOpts)
	if err != nil {
		return nil, err
	}
	out = append(out, summaryHandler)
	return out, nil
}

func (h *Harness) newEinoReductionMiddleware(ctx context.Context, run Run, allowOffload bool) (adk.ChatModelAgentMiddleware, error) {
	rootDir := filepath.Join(h.Workspace, "memory", "tool_payloads")
	cfg := &einoreduction.Config{
		ReadFileToolName:          "read_file",
		RootDir:                   rootDir,
		MaxLengthForTrunc:         einoReductionMaxOutputLength,
		MaxTokensForClear:         einoReductionClearTokens,
		ClearRetentionSuffixLimit: einoReductionRetentionSuffix,
		ClearAtLeastTokens:        einoReductionMinClearTokens,
		TokenCounter:              h.einoReductionTokenCounter,
		TruncExcludeTools:         []string{einoSkillToolName, "tool_search"},
		ClearExcludeTools:         []string{einoSkillToolName, "tool_search"},
		ClearPostProcess: func(ctx context.Context, state *adk.ChatModelAgentState) context.Context {
			h.appendRunStep(run.ID, RunStep{
				Kind:       "tool_reduction",
				Status:     "completed",
				AgentName:  currentCallbackAgentName(ctx, run.AgentName),
				Output:     trim(fmt.Sprintf("messages=%d tool context compacted", len(state.Messages)), 220),
				StartedAt:  time.Now(),
				FinishedAt: time.Now(),
			})
			return ctx
		},
	}
	if allowOffload {
		cfg.Backend = workspaceReductionBackend{workspace: h.Workspace}
	} else {
		cfg.SkipTruncation = true
	}
	return einoreduction.New(ctx, cfg)
}

func (h *Harness) newEinoSummarizationMiddleware(ctx context.Context, run Run, chatModel model.BaseChatModel, modelOpts []model.Option) (adk.ChatModelAgentMiddleware, error) {
	finalizer, err := einosummarization.NewFinalizer().
		PreserveSkills(&einosummarization.PreserveSkillsConfig{
			SkillToolName: einoSkillToolName,
		}).
		Custom(func(_ context.Context, originalMessages []adk.Message, summary adk.Message) ([]adk.Message, error) {
			systemMessages := leadingSystemMessages(originalMessages)
			out := make([]adk.Message, 0, len(systemMessages)+1)
			out = append(out, systemMessages...)
			out = append(out, summary)
			return out, nil
		}).
		Build()
	if err != nil {
		return nil, fmt.Errorf("build summarization finalizer: %w", err)
	}
	return einosummarization.New(ctx, &einosummarization.Config{
		Model:              chatModel,
		ModelOptions:       modelOpts,
		TokenCounter:       h.einoSummarizationTokenCounter,
		EmitInternalEvents: true,
		Trigger: &einosummarization.TriggerCondition{
			ContextMessages: einoSummarizationContextMessages,
			ContextTokens:   einoSummarizationContextTokens,
		},
		TranscriptFilePath: h.sessionTranscriptPath(run.SessionKey),
		Finalize:           finalizer,
		Callback: func(ctx context.Context, before, after adk.ChatModelAgentState) error {
			h.recordSummarizationArtifacts(run, currentCallbackAgentName(ctx, run.AgentName), before, after)
			return nil
		},
	})
}

func (h *Harness) einoSummarizationTokenCounter(ctx context.Context, input *einosummarization.TokenCounterInput) (int, error) {
	return int(h.estimateEinoTokenCount(ctx, input.Messages, input.Tools)), nil
}

func (h *Harness) einoReductionTokenCounter(ctx context.Context, messages []adk.Message, tools []*schema.ToolInfo) (int64, error) {
	return h.estimateEinoTokenCount(ctx, messages, tools), nil
}

func (h *Harness) estimateEinoTokenCount(_ context.Context, messages []adk.Message, tools []*schema.ToolInfo) int64 {
	var total int64
	for _, msg := range messages {
		total += int64(estimateTextTokens(einoMessageText(msg)))
	}
	for _, info := range tools {
		if info == nil {
			continue
		}
		total += int64(estimateTextTokens(strings.Join([]string{
			info.Name,
			info.Desc,
			fmt.Sprintf("%v", info.ParamsOneOf),
		}, "\n")))
	}
	return total
}

func estimateTextTokens(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	return (len(text) + 3) / 4
}

func einoMessageText(msg adk.Message) string {
	if msg == nil {
		return ""
	}
	parts := make([]string, 0, 1+len(msg.UserInputMultiContent)+len(msg.ToolCalls))
	if strings.TrimSpace(msg.Content) != "" {
		parts = append(parts, strings.TrimSpace(msg.Content))
	}
	for _, item := range msg.UserInputMultiContent {
		if strings.TrimSpace(item.Text) != "" {
			parts = append(parts, strings.TrimSpace(item.Text))
		}
	}
	for _, call := range msg.ToolCalls {
		name := strings.TrimSpace(call.Function.Name)
		args := strings.TrimSpace(call.Function.Arguments)
		if name != "" || args != "" {
			parts = append(parts, strings.TrimSpace(name+" "+args))
		}
	}
	if strings.TrimSpace(msg.ToolCallID) != "" {
		parts = append(parts, msg.ToolCallID)
	}
	if strings.TrimSpace(msg.ToolName) != "" {
		parts = append(parts, msg.ToolName)
	}
	return strings.Join(parts, "\n")
}

func leadingSystemMessages(messages []adk.Message) []adk.Message {
	out := make([]adk.Message, 0, len(messages))
	for _, msg := range messages {
		if msg == nil || msg.Role != schema.System {
			break
		}
		out = append(out, msg)
	}
	return out
}

func (h *Harness) sessionTranscriptPath(sessionKey string) string {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" || strings.TrimSpace(h.Workspace) == "" {
		return ""
	}
	cleaned := unsafeEinoSessionChars.ReplaceAllString(sessionKey, "_")
	cleaned = strings.Trim(cleaned, "._-")
	if cleaned == "" {
		cleaned = "session"
	}
	return filepath.Join(h.Workspace, "memory", "sessions", cleaned+".jsonl")
}

func (h *Harness) recordSummarizationArtifacts(run Run, agentName string, before, after adk.ChatModelAgentState) {
	artifactPath, err := h.writeSummarizationArtifact(run, agentName, before, after)
	if err != nil {
		h.appendRunStep(run.ID, RunStep{
			Kind:       "middleware_summarization",
			Status:     "failed",
			AgentName:  firstNonEmpty(agentName, run.AgentName),
			Output:     trim(err.Error(), 240),
			StartedAt:  time.Now(),
			FinishedAt: time.Now(),
		})
		return
	}
	h.appendRunStep(run.ID, RunStep{
		Kind:      "middleware_summarization",
		Status:    "completed",
		AgentName: firstNonEmpty(agentName, run.AgentName),
		Output: trim(fmt.Sprintf("before_messages=%d after_messages=%d artifact=%s",
			len(before.Messages), len(after.Messages), artifactPath), 400),
		StartedAt:  time.Now(),
		FinishedAt: time.Now(),
	})
	_ = reflection.Append(h.Workspace, reflection.Record{
		CreatedAt: time.Now().Format(time.RFC3339),
		Type:      "eino_summarization",
		Status:    "completed",
		Metadata: map[string]any{
			"run_id":          run.ID,
			"session_key":     run.SessionKey,
			"agent_name":      firstNonEmpty(agentName, run.AgentName),
			"before_messages": len(before.Messages),
			"after_messages":  len(after.Messages),
			"artifact_path":   artifactPath,
		},
	})
}

func (h *Harness) writeSummarizationArtifact(run Run, agentName string, before, after adk.ChatModelAgentState) (string, error) {
	dir := filepath.Join(h.Workspace, "memory", "summaries")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-%d.md", strings.TrimSpace(run.ID), time.Now().UnixNano())
	path := filepath.Join(dir, name)
	content := strings.Join([]string{
		"# Eino Summarization",
		"",
		fmt.Sprintf("- run: %s", run.ID),
		fmt.Sprintf("- agent: %s", firstNonEmpty(agentName, run.AgentName, "default")),
		fmt.Sprintf("- route: %s", firstNonEmpty(run.Route, "-")),
		fmt.Sprintf("- before_messages: %d", len(before.Messages)),
		fmt.Sprintf("- after_messages: %d", len(after.Messages)),
		"",
		"## Summary",
		firstNonEmpty(extractStateSummary(after), "(empty)"),
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func extractStateSummary(state adk.ChatModelAgentState) string {
	parts := make([]string, 0, len(state.Messages))
	for _, msg := range state.Messages {
		text := strings.TrimSpace(einoMessageText(msg))
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func toolPlanHasName(ctx context.Context, plan *einoToolPlan, name string) bool {
	name = strings.TrimSpace(name)
	if plan == nil || name == "" {
		return false
	}
	for _, toolName := range plan.DynamicNames {
		if toolName == name {
			return true
		}
	}
	for _, item := range plan.BaseTools {
		info, err := item.Info(ctx)
		if err == nil && info != nil && info.Name == name {
			return true
		}
	}
	return false
}

func (h *Harness) appendRunVisibleTool(runID, toolName string) {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return
	}
	h.mu.Lock()
	run, ok := h.runs[runID]
	if !ok {
		h.mu.Unlock()
		return
	}
	run.VisibleTools = appendUnique(run.VisibleTools, toolName)
	run.UpdatedAt = time.Now()
	h.runs[runID] = run
	h.mu.Unlock()
	_ = h.persistRun(run)
}

func isWithinHarnessWorkspace(path, workspace string) bool {
	candidate := filepath.Clean(path)
	root := filepath.Clean(workspace)
	if candidate == root {
		return true
	}
	return strings.HasPrefix(candidate, root+string(filepath.Separator))
}
