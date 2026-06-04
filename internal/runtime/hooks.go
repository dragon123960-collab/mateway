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
	"github.com/dongping/mateway/internal/script"
	"github.com/dongping/mateway/internal/secret"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/tool"
)

const (
	defaultHookTimeout             = 2 * time.Second
	defaultFollowupHookTimeout     = 8 * time.Second
	defaultCompletionReviewTimeout = 15 * time.Second
)

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
	State      session.State
	Text       string
	Model      agentcore.Model
	Locale     string
	CatalogDir string
}

type FollowupHookProvider interface {
	HookProvider
	FollowupHook(context.Context, FollowupHookInput) (followupDecision, error)
}

type PendingIntentInput struct {
	State      session.State
	Pending    session.PendingAction
	Text       string
	Model      agentcore.Model
	Locale     string
	CatalogDir string
}

type pendingIntentDecision struct {
	Kind   string
	Reason string
}

type PendingIntentHookProvider interface {
	HookProvider
	PendingIntentHook(context.Context, PendingIntentInput) (pendingIntentDecision, error)
}

type ToolPolicyHookInput struct {
	ToolCall agentcore.ToolCall
	Tool     agentcore.Tool
	Config   *config.Root
	Locale   string
}

type ToolPolicyHookResult struct {
	Block             bool
	Reason            string
	ResumeText        string
	AuthorizationOnly bool
}

type ToolPolicyHookProvider interface {
	HookProvider
	ToolPolicyHook(context.Context, ToolPolicyHookInput) (ToolPolicyHookResult, error)
}

type CompletionReviewInput struct {
	UserText           string
	Task               session.TaskNode
	FinalText          string
	TranscriptMessages []agentcore.Message
	Model              agentcore.Model
}

type CompletionReviewResult struct {
	Completed         bool
	Reason            string
	MissingItems      []string
	SuggestedFollowUp string
}

type CompletionReviewHookProvider interface {
	HookProvider
	CompletionReviewHook(context.Context, CompletionReviewInput) (CompletionReviewResult, error)
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
	Locale         string
	CatalogDir     string
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
	if decision := protocolFollowupDecision(input.State, input.Text); decision.Kind != "" {
		_ = trace.write(map[string]any{"type": "hook_event", "hook": "followup_hook", "provider": "protocol_guard", "decision": decision.Kind, "task_id": decision.TaskID, "reason": decision.Reason})
		return decision
	}
	timeout := defaultFollowupHookTimeout
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
	decision := fallbackFollowupDecision(input.State, input.Text, input.Locale, input.CatalogDir, "model followup unavailable")
	_ = trace.write(map[string]any{"type": "hook_event", "hook": "followup_hook", "provider": "safe_fallback", "decision": decision.Kind, "task_id": decision.TaskID, "reason": decision.Reason})
	return decision
}

func (h RuntimeHooks) pendingIntent(ctx context.Context, input PendingIntentInput, trace *traceRecorder) pendingIntentDecision {
	timeout := defaultFollowupHookTimeout
	for _, provider := range h.Providers {
		intentProvider, ok := provider.(PendingIntentHookProvider)
		if !ok {
			continue
		}
		name := strings.TrimSpace(provider.Name())
		if name == "" {
			name = "unknown"
		}
		result, err := runPendingIntentHookProvider(ctx, intentProvider, input, timeout)
		if err != nil {
			_ = trace.write(map[string]any{"type": "hook_warning", "hook": "pending_intent_hook", "provider": name, "error": err.Error()})
			continue
		}
		_ = trace.write(map[string]any{"type": "hook_event", "hook": "pending_intent_hook", "provider": name, "decision": result.Kind, "reason": result.Reason})
		if strings.TrimSpace(result.Kind) != "" {
			return result
		}
	}
	result := fallbackPendingIntentDecision(input.Pending, input.Text, "model pending intent unavailable")
	_ = trace.write(map[string]any{"type": "hook_event", "hook": "pending_intent_hook", "provider": "safe_fallback", "decision": result.Kind, "reason": result.Reason})
	return result
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

func (h RuntimeHooks) completionReview(ctx context.Context, input CompletionReviewInput, trace *traceRecorder) CompletionReviewResult {
	timeout := defaultCompletionReviewTimeout
	sawProvider := false
	for _, provider := range h.Providers {
		reviewProvider, ok := provider.(CompletionReviewHookProvider)
		if !ok {
			continue
		}
		sawProvider = true
		name := strings.TrimSpace(provider.Name())
		if name == "" {
			name = "unknown"
		}
		result, err := runCompletionReviewHookProvider(ctx, reviewProvider, input, timeout)
		if err != nil {
			_ = trace.write(map[string]any{"type": "hook_warning", "hook": "completion_review_hook", "provider": name, "error": err.Error()})
			continue
		}
		_ = trace.write(map[string]any{
			"type":               "hook_event",
			"hook":               "completion_review_hook",
			"provider":           name,
			"completed":          result.Completed,
			"reason":             result.Reason,
			"missing_items":      result.MissingItems,
			"suggested_followup": result.SuggestedFollowUp,
		})
		if result.Completed || strings.TrimSpace(result.Reason) != "" || len(result.MissingItems) > 0 || strings.TrimSpace(result.SuggestedFollowUp) != "" {
			return result
		}
	}
	result := heuristicCompletionReview(input)
	if !sawProvider {
		return result
	}
	if result.Completed {
		result.Completed = false
		result.Reason = "completion review unavailable; leaving task incomplete to avoid a false completed state"
		result.MissingItems = []string{"completion review did not return a decision"}
		result.SuggestedFollowUp = defaultCompletionFollowUp(result, input)
	}
	return result
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
		text = fallbackFinalReply(input.RawText, input.Locale, input.CatalogDir)
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

func runPendingIntentHookProvider(ctx context.Context, provider PendingIntentHookProvider, input PendingIntentInput, timeout time.Duration) (result pendingIntentDecision, err error) {
	child, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	done := make(chan struct {
		result pendingIntentDecision
		err    error
	}, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- struct {
					result pendingIntentDecision
					err    error
				}{err: fmt.Errorf("hook panic: %v", r)}
			}
		}()
		result, err := provider.PendingIntentHook(child, input)
		done <- struct {
			result pendingIntentDecision
			err    error
		}{result: result, err: err}
	}()
	select {
	case output := <-done:
		return output.result, output.err
	case <-child.Done():
		if err := child.Err(); err != nil {
			return pendingIntentDecision{}, err
		}
		return pendingIntentDecision{}, context.DeadlineExceeded
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

func runCompletionReviewHookProvider(ctx context.Context, provider CompletionReviewHookProvider, input CompletionReviewInput, timeout time.Duration) (result CompletionReviewResult, err error) {
	child, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	done := make(chan struct {
		result CompletionReviewResult
		err    error
	}, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				done <- struct {
					result CompletionReviewResult
					err    error
				}{err: fmt.Errorf("panic: %v", recovered)}
			}
		}()
		result, err := provider.CompletionReviewHook(child, input)
		done <- struct {
			result CompletionReviewResult
			err    error
		}{result: result, err: err}
	}()
	select {
	case output := <-done:
		return output.result, output.err
	case <-child.Done():
		if err := child.Err(); err != nil {
			return CompletionReviewResult{}, err
		}
		return CompletionReviewResult{}, context.DeadlineExceeded
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
		Name:    "channel_context",
		Source:  "buildRuntimeSystemContextForMessage",
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
	for _, marker := range splitCatalogCSV(i18n.New(i18n.Config{}).T(i18n.LocaleZH, "memory.safe_read.markers", nil)) {
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

type modelFollowupHookProvider struct{}

func (modelFollowupHookProvider) Name() string { return "model_followup" }

func (modelFollowupHookProvider) FollowupHook(ctx context.Context, input FollowupHookInput) (followupDecision, error) {
	if input.Model == nil {
		return followupDecision{}, nil
	}
	if strings.TrimSpace(input.Text) == "" || len(input.State.Tasks) == 0 {
		return followupDecision{}, nil
	}
	msg, err := input.Model.Next(ctx, agentcore.Context{
		SystemPrompt: "You route user messages to an existing task or a new task. Return JSON only.",
		Messages:     []agentcore.Message{{Role: agentcore.RoleUser, Content: modelFollowupPrompt(input.State, input.Text)}},
		Tools:        nil,
	})
	if err != nil {
		return followupDecision{}, err
	}
	decision, err := parseModelFollowupDecision(msg.Content, input.State, input.Text)
	if err != nil {
		return followupDecision{}, err
	}
	return decision, nil
}

type defaultToolPolicyHookProvider struct{}

func (defaultToolPolicyHookProvider) Name() string { return "default_tool_policy" }

func (defaultToolPolicyHookProvider) ToolPolicyHook(_ context.Context, input ToolPolicyHookInput) (ToolPolicyHookResult, error) {
	catalog := i18n.New(i18n.Config{})
	locale := strings.TrimSpace(input.Locale)
	if input.Config != nil {
		if locale == "" {
			locale = input.Config.App.Locale
		}
		catalog = i18n.New(i18n.Config{CatalogDir: input.Config.App.MessageCatalogDir})
	}
	if input.ToolCall.Name == "terminal.run" && tool.IsDangerousCommand(fmt.Sprint(input.ToolCall.Args["command"])) {
		return ToolPolicyHookResult{
			Block:      true,
			Reason:     catalog.T(locale, "approval.confirm.reason", nil),
			ResumeText: catalog.T(locale, "approval.confirm.resume_dangerous", nil),
		}, nil
	}
	if input.ToolCall.Name == "terminal.run" {
		decision := tool.CheckTerminalCommand(fmt.Sprint(input.ToolCall.Args["command"]), input.Config)
		if decision.Allow {
			switch decision.Class {
			case "local_read_only", "read_only_pipeline", "project_internal":
				return ToolPolicyHookResult{}, nil
			case "remote":
				if !decision.RequireConfirm {
					return ToolPolicyHookResult{}, nil
				}
			}
		}
	}
	if input.ToolCall.Name == "script.run" {
		name := strings.TrimSpace(fmt.Sprint(input.ToolCall.Args["name"]))
		scripts, err := script.List(input.Config)
		if err == nil {
			for _, candidate := range scripts {
				if candidate.Name != name {
					continue
				}
				if candidate.Source == "external_skill" {
					if !candidate.Authorized {
						return ToolPolicyHookResult{
							Block:             true,
							Reason:            catalog.T(locale, "approval.confirm.external_script", map[string]string{"script": name}),
							ResumeText:        catalog.T(locale, "approval.confirm.resume_script", nil),
							AuthorizationOnly: true,
						}, nil
					}
					return ToolPolicyHookResult{}, nil
				}
				break
			}
		}
	}
	if input.ToolCall.Name == "secret.set" {
		id := strings.ToLower(strings.TrimSpace(fmt.Sprint(input.ToolCall.Args["id"])))
		if !secret.ValidID(id) {
			return ToolPolicyHookResult{}, nil
		}
		if boolArg(input.ToolCall.Args["overwrite"]) {
			if entry, ok, err := (secret.Store{Home: configHome(input.Config)}).Get(id); err == nil && ok && entry.ID != "" {
				return ToolPolicyHookResult{
					Block:      true,
					Reason:     "Overwriting existing secret " + id + " requires confirmation.",
					ResumeText: "Continue after confirming secret overwrite",
				}, nil
			}
		}
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
		risk := ""
		acceptanceCriteria := ""
		evidenceContract := ""
		mutation := false
		if input.Tool != nil {
			risk = string(input.Tool.Risk())
			mutation = input.Tool.Risk() == agentcore.RiskGuardedMutation || input.Tool.Risk() == agentcore.RiskDangerous
			contract := agentcore.ContractFor(input.Tool)
			acceptanceCriteria = contract.Acceptance
			evidenceContract = contract.Evidence
		}
		return ObserveHookResult{TaskStep: &session.TaskStep{
			Tool:               input.ToolCall.Name,
			Status:             status,
			Summary:            redactedSummary(input.ToolResult.Content),
			Evidence:           evidence,
			Risk:               risk,
			AcceptanceCriteria: acceptanceCriteria,
			EvidenceContract:   evidenceContract,
			Accepted:           status == "accepted",
			Mutation:           mutation,
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
		text = fallbackFinalReply(input.RawText, input.Locale, input.CatalogDir)
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

func boolArg(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true") || strings.EqualFold(strings.TrimSpace(v), "yes")
	default:
		return false
	}
}

func configHome(cfg *config.Root) string {
	if cfg != nil && strings.TrimSpace(cfg.App.Home) != "" {
		return cfg.App.Home
	}
	return config.DefaultHome()
}
