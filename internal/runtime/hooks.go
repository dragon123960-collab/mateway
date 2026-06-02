package runtime

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/i18n"
	"github.com/dongping/mateway/internal/memory"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/tool"
)

const defaultHookTimeout = 2 * time.Second

type ContextHookInput struct {
	Message  channel.InboundMessage
	State    session.State
	TaskID   string
	UserText string
	Profile  config.AgentProfileConfig
}

type ContextHookResult struct {
	SystemContextSections []ContextSection
	MemoryRefs            []string
	FreshnessPolicy       string
}

type ContextSection struct {
	Name    string
	Content string
	Source  string
}

type HookProvider interface {
	Name() string
}

type RuntimeHooks struct {
	Providers []HookProvider
	Timeout   time.Duration
}

type ContextHookProvider interface {
	HookProvider
	ContextHook(context.Context, ContextHookInput) (ContextHookResult, error)
}

type FollowupHookInput struct {
	State session.State
	Text  string
}

type FollowupHookProvider interface {
	HookProvider
	FollowupHook(context.Context, FollowupHookInput) (followupDecision, error)
}

type ToolPolicyHookInput struct {
	ToolCall agentcore.ToolCall
	Tool     agentcore.Tool
	Config   *config.Root
}

type ToolPolicyHookResult struct {
	Block      bool
	Reason     string
	ResumeText string
}

type ToolPolicyHookProvider interface {
	HookProvider
	ToolPolicyHook(context.Context, ToolPolicyHookInput) (ToolPolicyHookResult, error)
}

type ObserveHookInput struct {
	Kind       string
	Home       string
	SessionKey string
	State      session.State
	TaskID     string
	FinalText  string
	TraceID    string
	TracePath  string
	Skills     []memory.SkillEvidence
	UserText   string
	ToolCall   agentcore.ToolCall
	Tool       agentcore.Tool
	ToolResult agentcore.ToolResult
}

type ObserveHookResult struct {
	TaskStep       *session.TaskStep
	LearningResult *memory.LearningResult
}

type ObserveHookProvider interface {
	HookProvider
	ObserveHook(context.Context, ObserveHookInput) (ObserveHookResult, error)
}

type ResponseHookInput struct {
	RawText        string
	LearningResult *memory.LearningResult
}

type ResponseHookResult struct {
	Text string
}

type ResponseHookProvider interface {
	HookProvider
	ResponseHook(context.Context, ResponseHookInput) (ResponseHookResult, error)
}

func defaultRuntimeHooks() RuntimeHooks {
	return RuntimeHooks{
		Timeout: defaultHookTimeout,
	}
}

func (h RuntimeHooks) contextMessages(ctx context.Context, input ContextHookInput, trace *traceRecorder) []agentcore.Message {
	var messages []agentcore.Message
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = defaultHookTimeout
	}
	for _, provider := range h.Providers {
		if provider == nil {
			continue
		}
		name := strings.TrimSpace(provider.Name())
		if name == "" {
			name = "unknown"
		}
		contextProvider, ok := provider.(ContextHookProvider)
		if !ok {
			continue
		}
		result, err := runContextHookProvider(ctx, contextProvider, input, timeout)
		if err != nil {
			_ = trace.write(map[string]any{"type": "hook_warning", "hook": "context_hook", "provider": name, "error": err.Error()})
			continue
		}
		_ = trace.write(map[string]any{
			"type":             "hook_event",
			"hook":             "context_hook",
			"provider":         name,
			"sections":         sectionNames(result.SystemContextSections),
			"memory_refs":      result.MemoryRefs,
			"freshness_policy": result.FreshnessPolicy,
		})
		if text := renderContextHookResult(result); text != "" {
			messages = append(messages, agentcore.Message{Role: agentcore.RoleSystem, Content: text})
		}
	}
	return messages
}

func (h RuntimeHooks) resolveFollowup(ctx context.Context, input FollowupHookInput, trace *traceRecorder) followupDecision {
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = defaultHookTimeout
	}
	for _, provider := range h.Providers {
		followupProvider, ok := provider.(FollowupHookProvider)
		if !ok {
			continue
		}
		name := strings.TrimSpace(provider.Name())
		if name == "" {
			name = "unknown"
		}
		decision, err := runFollowupHookProvider(ctx, followupProvider, input, timeout)
		if err != nil {
			_ = trace.write(map[string]any{"type": "hook_warning", "hook": "followup_hook", "provider": name, "error": err.Error()})
			continue
		}
		_ = trace.write(map[string]any{"type": "hook_event", "hook": "followup_hook", "provider": name, "decision": decision.Kind, "task_id": decision.TaskID, "reason": decision.Reason})
		if decision.Kind != "" {
			return decision
		}
	}
	decision := resolveFollowup(input.State, input.Text)
	_ = trace.write(map[string]any{"type": "hook_event", "hook": "followup_hook", "provider": "fallback_resolver", "decision": decision.Kind, "task_id": decision.TaskID, "reason": decision.Reason})
	return decision
}

func (h RuntimeHooks) toolPolicy(ctx context.Context, input ToolPolicyHookInput, trace *traceRecorder) ToolPolicyHookResult {
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = defaultHookTimeout
	}
	for _, provider := range h.Providers {
		policyProvider, ok := provider.(ToolPolicyHookProvider)
		if !ok {
			continue
		}
		name := strings.TrimSpace(provider.Name())
		if name == "" {
			name = "unknown"
		}
		result, err := runToolPolicyHookProvider(ctx, policyProvider, input, timeout)
		if err != nil {
			_ = trace.write(map[string]any{"type": "hook_warning", "hook": "tool_policy_hook", "provider": name, "error": err.Error()})
			continue
		}
		_ = trace.write(map[string]any{"type": "hook_event", "hook": "tool_policy_hook", "provider": name, "tool": input.ToolCall.Name, "block": result.Block, "reason": result.Reason})
		if result.Block {
			return result
		}
	}
	return ToolPolicyHookResult{}
}

func (h RuntimeHooks) observe(ctx context.Context, input ObserveHookInput, trace *traceRecorder) ObserveHookResult {
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = defaultHookTimeout
	}
	var combined ObserveHookResult
	for _, provider := range h.Providers {
		observeProvider, ok := provider.(ObserveHookProvider)
		if !ok {
			continue
		}
		name := strings.TrimSpace(provider.Name())
		if name == "" {
			name = "unknown"
		}
		result, err := runObserveHookProvider(ctx, observeProvider, input, timeout)
		if err != nil {
			_ = trace.write(map[string]any{"type": "hook_warning", "hook": "observe_hook", "provider": name, "kind": input.Kind, "error": err.Error()})
			continue
		}
		_ = trace.write(map[string]any{"type": "hook_event", "hook": "observe_hook", "provider": name, "kind": input.Kind})
		if result.TaskStep != nil {
			combined.TaskStep = result.TaskStep
		}
		if result.LearningResult != nil {
			combined.LearningResult = result.LearningResult
		}
	}
	return combined
}

func (h RuntimeHooks) response(ctx context.Context, input ResponseHookInput, trace *traceRecorder) string {
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = defaultHookTimeout
	}
	for _, provider := range h.Providers {
		responseProvider, ok := provider.(ResponseHookProvider)
		if !ok {
			continue
		}
		name := strings.TrimSpace(provider.Name())
		if name == "" {
			name = "unknown"
		}
		result, err := runResponseHookProvider(ctx, responseProvider, input, timeout)
		if err != nil {
			_ = trace.write(map[string]any{"type": "hook_warning", "hook": "response_hook", "provider": name, "error": err.Error()})
			continue
		}
		_ = trace.write(map[string]any{"type": "hook_event", "hook": "response_hook", "provider": name})
		if strings.TrimSpace(result.Text) != "" {
			return result.Text
		}
	}
	text := sanitizeResponse(input.RawText)
	if text == "" {
		text = fallbackFinalReply(input.RawText)
	}
	return text
}

func runContextHookProvider(ctx context.Context, provider ContextHookProvider, input ContextHookInput, timeout time.Duration) (result ContextHookResult, err error) {
	child, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	done := make(chan struct {
		result ContextHookResult
		err    error
	}, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				done <- struct {
					result ContextHookResult
					err    error
				}{err: fmt.Errorf("panic: %v", recovered)}
			}
		}()
		result, err := provider.ContextHook(child, input)
		done <- struct {
			result ContextHookResult
			err    error
		}{result: result, err: err}
	}()
	select {
	case output := <-done:
		return output.result, output.err
	case <-child.Done():
		if err := child.Err(); err != nil {
			return ContextHookResult{}, err
		}
		return ContextHookResult{}, context.DeadlineExceeded
	}
}

func runFollowupHookProvider(ctx context.Context, provider FollowupHookProvider, input FollowupHookInput, timeout time.Duration) (result followupDecision, err error) {
	child, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	done := make(chan struct {
		result followupDecision
		err    error
	}, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				done <- struct {
					result followupDecision
					err    error
				}{err: fmt.Errorf("panic: %v", recovered)}
			}
		}()
		result, err := provider.FollowupHook(child, input)
		done <- struct {
			result followupDecision
			err    error
		}{result: result, err: err}
	}()
	select {
	case output := <-done:
		return output.result, output.err
	case <-child.Done():
		if err := child.Err(); err != nil {
			return followupDecision{}, err
		}
		return followupDecision{}, context.DeadlineExceeded
	}
}

func runToolPolicyHookProvider(ctx context.Context, provider ToolPolicyHookProvider, input ToolPolicyHookInput, timeout time.Duration) (result ToolPolicyHookResult, err error) {
	child, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	done := make(chan struct {
		result ToolPolicyHookResult
		err    error
	}, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				done <- struct {
					result ToolPolicyHookResult
					err    error
				}{err: fmt.Errorf("panic: %v", recovered)}
			}
		}()
		result, err := provider.ToolPolicyHook(child, input)
		done <- struct {
			result ToolPolicyHookResult
			err    error
		}{result: result, err: err}
	}()
	select {
	case output := <-done:
		return output.result, output.err
	case <-child.Done():
		if err := child.Err(); err != nil {
			return ToolPolicyHookResult{}, err
		}
		return ToolPolicyHookResult{}, context.DeadlineExceeded
	}
}

func runObserveHookProvider(ctx context.Context, provider ObserveHookProvider, input ObserveHookInput, timeout time.Duration) (result ObserveHookResult, err error) {
	child, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	done := make(chan struct {
		result ObserveHookResult
		err    error
	}, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				done <- struct {
					result ObserveHookResult
					err    error
				}{err: fmt.Errorf("panic: %v", recovered)}
			}
		}()
		result, err := provider.ObserveHook(child, input)
		done <- struct {
			result ObserveHookResult
			err    error
		}{result: result, err: err}
	}()
	select {
	case output := <-done:
		return output.result, output.err
	case <-child.Done():
		if err := child.Err(); err != nil {
			return ObserveHookResult{}, err
		}
		return ObserveHookResult{}, context.DeadlineExceeded
	}
}

func runResponseHookProvider(ctx context.Context, provider ResponseHookProvider, input ResponseHookInput, timeout time.Duration) (result ResponseHookResult, err error) {
	child, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	done := make(chan struct {
		result ResponseHookResult
		err    error
	}, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				done <- struct {
					result ResponseHookResult
					err    error
				}{err: fmt.Errorf("panic: %v", recovered)}
			}
		}()
		result, err := provider.ResponseHook(child, input)
		done <- struct {
			result ResponseHookResult
			err    error
		}{result: result, err: err}
	}()
	select {
	case output := <-done:
		return output.result, output.err
	case <-child.Done():
		if err := child.Err(); err != nil {
			return ResponseHookResult{}, err
		}
		return ResponseHookResult{}, context.DeadlineExceeded
	}
}

func renderContextHookResult(result ContextHookResult) string {
	var b strings.Builder
	for _, section := range result.SystemContextSections {
		content := strings.TrimSpace(section.Content)
		if content == "" {
			continue
		}
		name := strings.TrimSpace(section.Name)
		if name == "" {
			name = "context"
		}
		b.WriteString("[")
		b.WriteString(name)
		b.WriteString("]\n")
		if source := strings.TrimSpace(section.Source); source != "" {
			b.WriteString("Source: ")
			b.WriteString(source)
			b.WriteString("\n")
		}
		b.WriteString(content)
		b.WriteString("\n\n")
	}
	if len(result.MemoryRefs) > 0 {
		b.WriteString("[memory_refs]\n")
		for _, ref := range result.MemoryRefs {
			ref = strings.TrimSpace(ref)
			if ref == "" {
				continue
			}
			b.WriteString("- ")
			b.WriteString(ref)
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
}

func sectionNames(sections []ContextSection) []string {
	names := make([]string, 0, len(sections))
	for _, section := range sections {
		if name := strings.TrimSpace(section.Name); name != "" {
			names = append(names, name)
		}
	}
	return names
}

type staticContextHookProvider struct {
	config *config.Root
}

func (staticContextHookProvider) Name() string { return "static_context" }

func (p staticContextHookProvider) ContextHook(_ context.Context, input ContextHookInput) (ContextHookResult, error) {
	contextText := buildRuntimeSystemContextForMessage(p.config, input.Profile, input.Message)
	if strings.TrimSpace(contextText) == "" {
		return ContextHookResult{}, nil
	}
	return ContextHookResult{SystemContextSections: []ContextSection{{
		Name:    "static_runtime_context",
		Source:  "buildRuntimeSystemContext",
		Content: contextText,
	}}}, nil
}

type memorySafeReadHookProvider struct {
	config *config.Root
}

func (memorySafeReadHookProvider) Name() string { return "memory_safe_read" }

func (p memorySafeReadHookProvider) ContextHook(_ context.Context, input ContextHookInput) (ContextHookResult, error) {
	if !shouldSearchMemory(input.UserText) {
		return ContextHookResult{}, nil
	}
	root := memoryRootForConfig(p.config)
	results, issues, err := memory.SearchRoot(root, memory.SearchOptions{Query: input.UserText, Limit: 3})
	if err != nil {
		return ContextHookResult{}, err
	}
	if hasMemoryLintErrors(issues) || len(results) == 0 {
		return ContextHookResult{}, nil
	}
	var refs []string
	var lines []string
	for _, result := range results {
		refs = append(refs, result.Path)
		fullPath := filepath.Join(root, filepath.FromSlash(result.Path))
		line := "- " + result.Path
		if fullPath != result.Path {
			line += " [path: " + fullPath + "]"
		}
		if result.Type != "" || result.Scope != "" {
			line += " (" + strings.Trim(strings.TrimSpace(result.Type+" / "+result.Scope), "/ ") + ")"
		}
		if result.Snippet != "" {
			line += ": " + result.Snippet
		}
		if len(result.Sources) > 0 {
			line += " [sources: " + strings.Join(result.Sources, ", ") + "]"
		}
		lines = append(lines, line)
	}
	return ContextHookResult{
		SystemContextSections: []ContextSection{{
			Name:    "memory_safe_read",
			Source:  root,
			Content: "Relevant memory snippets:\n" + strings.Join(lines, "\n"),
		}},
		MemoryRefs: refs,
	}, nil
}

func shouldSearchMemory(text string) bool {
	text = strings.TrimSpace(text)
	if len([]rune(text)) >= 12 {
		return true
	}
	lower := strings.ToLower(text)
	for _, marker := range []string{"memory", "remember", "preference", "project", "readme", "tool", "记忆", "偏好", "项目", "工具"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func memoryRootForConfig(cfg *config.Root) string {
	if cfg == nil {
		return filepath.Join(config.DefaultHome(), "workspace", "memory")
	}
	if root := strings.TrimSpace(cfg.Memory.Root); root != "" {
		return root
	}
	workspace := strings.TrimSpace(cfg.App.Workspace)
	if workspace == "" {
		workspace = filepath.Join(cfg.App.Home, "workspace")
	}
	return filepath.Join(workspace, "memory")
}

func hasMemoryLintErrors(issues []memory.Issue) bool {
	for _, issue := range issues {
		if issue.Severity == "error" {
			return true
		}
	}
	return false
}

type ruleFollowupHookProvider struct{}

func (ruleFollowupHookProvider) Name() string { return "rule_followup" }

func (ruleFollowupHookProvider) FollowupHook(_ context.Context, input FollowupHookInput) (followupDecision, error) {
	return resolveFollowup(input.State, input.Text), nil
}

type defaultToolPolicyHookProvider struct{}

func (defaultToolPolicyHookProvider) Name() string { return "default_tool_policy" }

func (defaultToolPolicyHookProvider) ToolPolicyHook(_ context.Context, input ToolPolicyHookInput) (ToolPolicyHookResult, error) {
	catalog := i18n.New(i18n.Config{})
	locale := ""
	if input.Config != nil {
		locale = input.Config.App.Locale
		catalog = i18n.New(i18n.Config{CatalogDir: input.Config.App.MessageCatalogDir})
	}
	if input.ToolCall.Name == "terminal.run" && tool.IsDangerousCommand(fmt.Sprint(input.ToolCall.Args["command"])) {
		return ToolPolicyHookResult{
			Block:      true,
			Reason:     catalog.T(locale, "approval.confirm.reason", nil),
			ResumeText: catalog.T(locale, "approval.confirm.resume_dangerous", nil),
		}, nil
	}
	if input.Tool == nil {
		return ToolPolicyHookResult{}, nil
	}
	if input.Tool.Risk() == agentcore.RiskGuardedMutation || input.Tool.Risk() == agentcore.RiskDangerous {
		if input.Config != nil && !input.Config.Security.RequireApprovalForRiskyTool {
			return ToolPolicyHookResult{}, nil
		}
		return ToolPolicyHookResult{
			Block:      true,
			Reason:     catalog.T(locale, "approval.confirm.generic", nil),
			ResumeText: catalog.T(locale, "approval.confirm.resume_tool", map[string]string{"tool": input.Tool.Name()}),
		}, nil
	}
	return ToolPolicyHookResult{}, nil
}

type defaultObserveHookProvider struct{}

func (defaultObserveHookProvider) Name() string { return "default_observe" }

func (defaultObserveHookProvider) ObserveHook(_ context.Context, input ObserveHookInput) (ObserveHookResult, error) {
	switch input.Kind {
	case "tool_result":
		status, evidence := acceptToolResult(input.Tool, input.ToolResult)
		return ObserveHookResult{TaskStep: &session.TaskStep{
			Tool:     input.ToolCall.Name,
			Status:   status,
			Summary:  redactedSummary(input.ToolResult.Content),
			Evidence: evidence,
		}}, nil
	case "task_completed":
		result, err := memory.RecordTaskCompletion(memory.LearningEvent{
			Home:       input.Home,
			SessionKey: input.SessionKey,
			Task:       taskFromState(input.State, input.TaskID),
			FinalText:  input.FinalText,
			TraceID:    input.TraceID,
			TracePath:  input.TracePath,
			Skills:     input.Skills,
			UserText:   input.UserText,
		})
		if err != nil {
			return ObserveHookResult{}, err
		}
		return ObserveHookResult{LearningResult: &result}, nil
	default:
		return ObserveHookResult{}, nil
	}
}

type defaultResponseHookProvider struct{}

func (defaultResponseHookProvider) Name() string { return "default_response" }

func (defaultResponseHookProvider) ResponseHook(_ context.Context, input ResponseHookInput) (ResponseHookResult, error) {
	text := sanitizeResponse(input.RawText)
	if text == "" {
		text = fallbackFinalReply(input.RawText)
	}
	return ResponseHookResult{Text: text}, nil
}

func taskFromState(state session.State, taskID string) session.TaskNode {
	for _, task := range state.Tasks {
		if task.ID == taskID {
			return task
		}
	}
	return session.TaskNode{ID: taskID}
}
