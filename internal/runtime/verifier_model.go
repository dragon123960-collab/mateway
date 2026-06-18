package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/session"
)

const modelVerifierSystemPrompt = `You are a verification judge for a task graph runtime. Your only job is to evaluate whether a single node's output satisfies its acceptance criteria.

## Rules
- You must ONLY judge the output against the criteria. Do not execute tools, suggest actions, or modify the graph.
- You must output a valid JSON object exactly as specified. No extra text outside the JSON.
- If the output clearly satisfies the criteria, status must be "passed".
- If the output likely can be fixed by retrying the same node with clearer instructions, status must be "retry".
- If the output clearly fails and retrying the same node is unlikely to help, status must be "failed".
- If the node appears to need a different plan, skill, or decomposition, status must be "replan".
- If you cannot determine pass/fail from the available evidence, status must be "blocked".
- If the node needs human input to proceed, status must be "needs_input".
- Provide a brief "reason" explaining your decision.
- List any "missing" evidence or requirements.
- Set "confidence" to "low", "medium", or "high" based on how certain you are.
- Do not let the absence of evidence become a false "passed". When in doubt, be conservative.`

type modelVerifyOutput struct {
	Status                 string   `json:"status"`
	Reason                 string   `json:"reason"`
	Missing                []string `json:"missing,omitempty"`
	Confidence             string   `json:"confidence"`
	Retryable              bool     `json:"retryable,omitempty"`
	FeedbackForNextAttempt string   `json:"feedback_for_next_attempt,omitempty"`
}

func (rt Runtime) verifyNodeWithModel(
	ctx context.Context,
	graphID string,
	node *session.TaskGraphNode,
	trace *traceRecorder,
) session.NodeVerificationResult {
	if rt.Model == nil {
		return session.NodeVerificationResult{
			Status:  session.VerificationBlocked,
			Reason:  "model verifier unavailable: no model configured",
			Missing: []string{"semantic acceptance requires model verification"},
		}
	}

	prompt := renderModelVerifierPrompt(node)
	verifierCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	reply, err := rt.Model.Next(verifierCtx, agentcore.Context{
		SystemPrompt: modelVerifierSystemPrompt,
		Messages:     []agentcore.Message{{Role: agentcore.RoleUser, Content: prompt}},
	})
	if err != nil {
		return session.NodeVerificationResult{
			Status:  session.VerificationBlocked,
			Reason:  fmt.Sprintf("model verifier failed: %v", err),
			Missing: []string{node.Acceptance.Criteria},
		}
	}

	if trace != nil {
		_ = trace.write(map[string]any{
			"type":     "model_verifier_output",
			"graph_id": graphID,
			"node_id":  node.ID,
			"raw":      summarize(reply.Content),
		})
	}

	result := parseModelVerifierOutput(reply.Content, node)
	if trace != nil {
		_ = trace.write(map[string]any{
			"type":              "model_verifier_decision",
			"graph_id":          graphID,
			"node_id":           node.ID,
			"verify_status":     result.Status,
			"verify_reason":     result.Reason,
			"verify_confidence": result.Confidence,
		})
	}
	return result
}

func renderModelVerifierPrompt(node *session.TaskGraphNode) string {
	var sb strings.Builder
	sb.WriteString("Evaluate whether this node's output satisfies its acceptance criteria.\n\n")
	sb.WriteString(fmt.Sprintf("Node Goal: %s\n", node.Goal))
	if node.Acceptance.Criteria != "" {
		sb.WriteString(fmt.Sprintf("Acceptance Criteria: %s\n", node.Acceptance.Criteria))
	}
	if node.ResultSummary != "" {
		sb.WriteString(fmt.Sprintf("Result Summary: %s\n", node.ResultSummary))
	}
	if output := verifierNodeOutputText(node); output != "" {
		sb.WriteString(fmt.Sprintf("Node Output Text: %s\n", output))
	}
	if len(node.EvidenceRefs) > 0 {
		sb.WriteString("Evidence:\n")
		for i, ref := range node.EvidenceRefs {
			sb.WriteString(fmt.Sprintf("  [%d] %s: %s\n", i+1, ref.ToolName, ref.Summary))
		}
	}
	if node.FailureReason != "" {
		sb.WriteString(fmt.Sprintf("Failure Reason: %s\n", node.FailureReason))
	}
	sb.WriteString(fmt.Sprintf("\nNode Type: %s\n", node.Type))
	sb.WriteString(fmt.Sprintf("Attempts: %d\n", node.Attempts))
	sb.WriteString("\nIf the visible text includes a storage or trace truncation marker, evaluate the available final output and evidence. Do not fail solely because trace display was truncated; request retry only when the acceptance criteria cannot be judged from the node output/evidence.\n")
	sb.WriteString("\nOutput a JSON object with: status, reason, missing, confidence.\n")
	return sb.String()
}

func verifierNodeOutputText(node *session.TaskGraphNode) string {
	if node == nil || len(node.Output) == 0 {
		return ""
	}
	for _, key := range []string{"text", "summary", "final_answer", "report", "repair_result"} {
		if value, ok := node.Output[key]; ok {
			text := strings.TrimSpace(fmt.Sprint(value))
			if text != "" && text != "true" {
				return trimAndTruncateRunesWithSuffix(text, 12000)
			}
		}
	}
	return ""
}

func parseModelVerifierOutput(raw string, node *session.TaskGraphNode) session.NodeVerificationResult {
	jsonText := extractJSONBlock(raw)
	if jsonText == "" {
		return conservativeBlocked(node, "model verifier produced no valid JSON")
	}

	var rawFields map[string]any
	if err := json.Unmarshal([]byte(jsonText), &rawFields); err != nil {
		return conservativeBlocked(node, fmt.Sprintf("model verifier output not valid JSON: %v", err))
	}
	out := modelVerifyOutput{
		Status:     asString(rawFields["status"]),
		Reason:     asString(rawFields["reason"]),
		Confidence: asString(rawFields["confidence"]),
		Retryable:  asBool(rawFields["retryable"]),
	}
	out.FeedbackForNextAttempt = asString(rawFields["feedback_for_next_attempt"])
	if raw, ok := rawFields["missing"]; ok {
		switch v := raw.(type) {
		case string:
			if v != "" {
				out.Missing = []string{v}
			}
		case []any:
			for _, item := range v {
				if s, ok := item.(string); ok && s != "" {
					out.Missing = append(out.Missing, s)
				}
			}
		}
	}

	status := normalizeVerifierStatus(out.Status)
	if status == "" {
		return conservativeBlocked(node, fmt.Sprintf("model verifier output has invalid status %q", out.Status))
	}

	confidence := normalizeConfidence(out.Confidence)

	return session.NodeVerificationResult{
		Status:                 status,
		Reason:                 strings.TrimSpace(out.Reason),
		Missing:                out.Missing,
		Confidence:             confidence,
		EvidenceRefs:           node.EvidenceRefs,
		Retryable:              out.Retryable || status == session.VerificationRetry,
		FeedbackForNextAttempt: firstNonEmpty(out.FeedbackForNextAttempt, strings.TrimSpace(out.Reason)),
	}
}

func conservativeBlocked(node *session.TaskGraphNode, reason string) session.NodeVerificationResult {
	missing := []string{reason}
	if node.Acceptance.Criteria != "" {
		missing = append(missing, node.Acceptance.Criteria)
	}
	return session.NodeVerificationResult{
		Status:     session.VerificationBlocked,
		Reason:     reason,
		Missing:    missing,
		Confidence: "low",
	}
}

func asString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func asBool(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	if s, ok := v.(string); ok {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true", "yes", "1":
			return true
		}
	}
	return false
}

func normalizeVerifierStatus(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "passed":
		return session.VerificationPassed
	case "retry":
		return session.VerificationRetry
	case "failed":
		return session.VerificationFailed
	case "blocked":
		return session.VerificationBlocked
	case "needs_input":
		return session.VerificationNeedsInput
	case "replan":
		return session.VerificationReplan
	default:
		return ""
	}
}

func normalizeConfidence(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "low", "medium", "high":
		return s
	default:
		return "low"
	}
}
