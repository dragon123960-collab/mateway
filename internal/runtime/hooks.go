package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/session"
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
	ContextHook(context.Context, ContextHookInput) (ContextHookResult, error)
}

type RuntimeHooks struct {
	Providers []HookProvider
	Timeout   time.Duration
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
		result, err := runContextHookProvider(ctx, provider, input, timeout)
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

func runContextHookProvider(ctx context.Context, provider HookProvider, input ContextHookInput, timeout time.Duration) (result ContextHookResult, err error) {
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
	contextText := buildRuntimeSystemContext(p.config, input.Profile)
	if strings.TrimSpace(contextText) == "" {
		return ContextHookResult{}, nil
	}
	return ContextHookResult{SystemContextSections: []ContextSection{{
		Name:    "static_runtime_context",
		Source:  "buildRuntimeSystemContext",
		Content: contextText,
	}}}, nil
}
