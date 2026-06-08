package tool

import (
	"context"
	"fmt"
	"strings"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/secret"
)

func (SecretSetTool) Name() string { return "secret.set" }
func (SecretSetTool) Description() string {
	return "store a local secret value by id without returning the value"
}
func (SecretSetTool) Schema() agentcore.Schema {
	return agentcore.Schema{Required: []string{"id", "value"}}
}
func (SecretSetTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{
		WhenToUse:            "Use when the user has provided a concrete credential, token, password, authorization code, or API key that must be available to scripts through required_secret injection.",
		WhenNotToUse:         "Do not use with placeholders, redacted values, examples, or values the user has not actually provided.",
		OutputContract:       "Return only the normalized secret id and stored=true; never return the secret value.",
		Evidence:             "Return the secret id, stored=true, and placeholder=true on placeholder rejection.",
		Acceptance:           "Accepted when the secret store persists the value without exposing it in content or evidence.",
		SoftFailureSignals:   []string{"redacted placeholder", "secret id is required", "secret value is required"},
		ParallelMode:         "forbid",
		ReusePolicy:          "never",
		ConfirmationBoundary: "guarded local secret mutation; allowed when the user provided the value in the current task.",
	}
}
func (SecretSetTool) Risk() agentcore.Risk { return agentcore.RiskGuardedMutation }
func (t SecretSetTool) Run(_ context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	id := strings.TrimSpace(fmt.Sprint(call.Args["id"]))
	value := fmt.Sprint(call.Args["value"])
	overwrite := boolArg(call.Args["overwrite"])
	if isRedactedPlaceholder(value) {
		return agentcore.ToolResult{
			ToolCallID: call.ID,
			Content:    "secret value is a redacted placeholder; ask the user to provide the real value again",
			IsError:    true,
			Evidence:   map[string]any{"id": id, "placeholder": true},
		}
	}
	if err := (secret.Store{Home: configHome(t.Config)}).SetWithOptions(id, value, secret.SetOptions{Overwrite: overwrite}); err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true, Evidence: map[string]any{"id": id}}
	}
	return agentcore.ToolResult{
		ToolCallID: call.ID,
		Content:    "secret stored: " + strings.ToLower(strings.TrimSpace(id)),
		Evidence:   map[string]any{"id": strings.ToLower(strings.TrimSpace(id)), "stored": true},
	}
}

func isRedactedPlaceholder(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	return lower == "" || strings.Contains(lower, "redacted") || strings.Contains(lower, "placeholder")
}
