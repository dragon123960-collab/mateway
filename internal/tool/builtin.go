package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/config"
)

func NewRegistry(cfg ...*config.Root) *agentcore.ToolRegistry {
	var root *config.Root
	if len(cfg) > 0 {
		root = cfg[0]
	}
	registry := agentcore.NewToolRegistry()
	registry.Register(FileReadTool{Config: root})
	registry.Register(FileWriteTool{Config: root})
	registry.Register(FileDeleteTool{Config: root})
	registry.Register(ProjectIndexTool{Config: root})
	registry.Register(TerminalRunTool{Config: root})
	registry.Register(SecretSetTool{Config: root})
	registry.Register(ScheduleCreateTool{Config: root})
	registry.Register(ScheduleListTool{Config: root})
	registry.Register(ScheduleUpdateTool{Config: root})
	registry.Register(SchedulePauseTool{Config: root})
	registry.Register(ScheduleResumeTool{Config: root})
	registry.Register(ScheduleDeleteTool{Config: root})
	registry.Register(ScheduleRunNowTool{Config: root})
	registry.Register(TaskSearchTool{Config: root})
	registry.Register(TaskResumeTool{Config: root})
	registry.Register(ToolResultReadTool{Config: root})
	registry.Register(WebSearchTool{Config: root})
	registry.Register(WebFetchTool{Config: root})
	return registry
}

func NewRegistryForProfile(cfg *config.Root, profile config.AgentProfileConfig) *agentcore.ToolRegistry {
	registry := NewRegistry(cfg)
	filterRegistry(registry, profile.Tools)
	return registry
}

func filterRegistry(registry *agentcore.ToolRegistry, access config.AccessListConfig) {
	if registry == nil {
		return
	}
	allow := normalizedSet(access.Allow)
	deny := normalizedSet(access.Deny)
	for _, item := range registry.List() {
		name := item.Name()
		key := strings.ToLower(strings.TrimSpace(name))
		if len(allow) > 0 && !allow[key] {
			registry.Unregister(name)
			continue
		}
		if deny[key] {
			registry.Unregister(name)
		}
	}
}

func normalizedSet(values []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			out[value] = true
		}
	}
	return out
}

type EchoTool struct{}

func (EchoTool) Name() string        { return "echo" }
func (EchoTool) Description() string { return "echo text back to the model" }
func (EchoTool) Schema() agentcore.Schema {
	return agentcore.Schema{Required: []string{"text"}}
}
func (EchoTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{
		WhenToUse:            "Only in development tests that need deterministic tool echo behavior.",
		WhenNotToUse:         "Do not use for normal user tasks; answer directly instead.",
		OutputContract:       "Return exactly the provided text.",
		Evidence:             "Echoes the provided text.",
		Acceptance:           "Accepted when returned text matches the requested echo text.",
		ParallelMode:         "read_only_ok",
		ReusePolicy:          "never",
		ConfirmationBoundary: "safe read; no confirmation.",
	}
}

type FileWriteTool struct{ Config *config.Root }
type FileReadTool struct{ Config *config.Root }
type FileDeleteTool struct{ Config *config.Root }
type ProjectIndexTool struct{ Config *config.Root }
type ToolResultReadTool struct{ Config *config.Root }

type SecretSetTool struct{ Config *config.Root }
type ScheduleCreateTool struct{ Config *config.Root }
type ScheduleListTool struct{ Config *config.Root }
type ScheduleUpdateTool struct{ Config *config.Root }
type SchedulePauseTool struct{ Config *config.Root }
type ScheduleResumeTool struct{ Config *config.Root }
type ScheduleDeleteTool struct{ Config *config.Root }
type ScheduleRunNowTool struct{ Config *config.Root }
type TaskSearchTool struct{ Config *config.Root }
type TaskResumeTool struct{ Config *config.Root }

func mapListArg(value any) ([]map[string]any, error) {
	if value == nil {
		return nil, nil
	}
	switch v := value.(type) {
	case []map[string]any:
		return v, nil
	case []any:
		out := make([]map[string]any, 0, len(v))
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("env_secrets must be a list of objects")
			}
			out = append(out, m)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("env_secrets must be a list")
	}
}

func toolArgString(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	value, ok := args[key]
	if !ok || value == nil {
		return ""
	}
	text := strings.TrimSpace(fmt.Sprint(value))
	if text == "<nil>" {
		return ""
	}
	return text
}

func stringSliceArg(value any) []string {
	switch v := value.(type) {
	case []string:
		return append([]string(nil), v...)
	case []any:
		var out []string
		for _, item := range v {
			if text := strings.TrimSpace(fmt.Sprint(item)); text != "" && text != "<nil>" {
				out = append(out, text)
			}
		}
		return out
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return strings.Fields(v)
	default:
		return nil
	}
}

func intArg(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		return n
	default:
		return 0
	}
}

func boundedIntArg(value any, fallback, min, max int) int {
	n := intArg(value)
	if n == 0 {
		n = fallback
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

func (EchoTool) Risk() agentcore.Risk { return agentcore.RiskSafeRead }
func (EchoTool) Run(_ context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	return agentcore.ToolResult{ToolCallID: call.ID, Content: fmt.Sprint(call.Args["text"])}
}

func stringArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	value, ok := args[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}
