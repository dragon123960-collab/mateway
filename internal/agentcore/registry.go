package agentcore

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

type ToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: map[string]Tool{}}
}

func (r *ToolRegistry) Register(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[tool.Name()] = tool
}

func (r *ToolRegistry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
}

func (r *ToolRegistry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[name]
	return tool, ok
}

func (r *ToolRegistry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]Tool, 0, len(names))
	for _, name := range names {
		out = append(out, r.tools[name])
	}
	return out
}

func (r *ToolRegistry) Execute(ctx context.Context, call ToolCall) ToolResult {
	tool, ok := r.Get(call.Name)
	if !ok {
		return ToolResult{
			ToolCallID: call.ID,
			Content:    fmt.Sprintf("tool %q not found", call.Name),
			IsError:    true,
		}
	}
	for _, required := range tool.Schema().Required {
		if _, ok := call.Args[required]; !ok {
			return ToolResult{
				ToolCallID: call.ID,
				Content:    fmt.Sprintf("tool %q missing required argument %q", call.Name, required),
				IsError:    true,
			}
		}
	}
	done := make(chan ToolResult, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- ToolResult{
					ToolCallID: call.ID,
					Content:    fmt.Sprintf("tool %q panicked: %v", call.Name, r),
					IsError:    true,
				}
			}
		}()
		done <- tool.Run(ctx, call)
	}()
	select {
	case result := <-done:
		return result
	case <-ctx.Done():
		return ToolResult{
			ToolCallID: call.ID,
			Content:    fmt.Sprintf("tool %q cancelled: %v", call.Name, ctx.Err()),
			IsError:    true,
		}
	}
}
