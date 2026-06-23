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

const taskVerifierSystemPrompt = `You are a task-graph acceptance judge. Evaluate whether the COMBINED output of all verified nodes satisfies the OVERALL task acceptance criteria.

## Rules
- Judge only against the task acceptance criteria. Do not execute tools or modify the graph.
- Output exactly one JSON object. No text outside the JSON.
- Status values:
  * "passed" — the combined verified output closes the task acceptance.
  * "needs_repair" — the task is not blocked, but a synthesis gap remains that an extra repair/synthesis node could close.
  * "failed" — the task failed and is not salvageable by a single repair synthesis.
  * "blocked" — a concrete blocker (missing critical input, blocked dependency) prevents completion; no repair attempt.
- Provide a concise "reason".
- "verifier_feedback" must restate the concrete gap to feed the next repair round (empty when passed).
- "confidence" is "low" | "medium" | "high".
- When a previous repair round failed, use the accumulated feedback to avoid repeating the same gap.
- Do not let the absence of a synthesized summary become a false "passed"; when in doubt, be conservative.`

type taskVerifyOutput struct {
	Status           string   `json:"status"`
	Reason           string   `json:"reason"`
	Missing          []string `json:"missing,omitempty"`
	Confidence       string   `json:"confidence"`
	VerifierFeedback string   `json:"verifier_feedback,omitempty"`
}

// verifyTaskGraph runs the deterministic task-level verifier and, when the
// config policy says so, the model verifier. Returns the final
// GraphVerificationResult. The deterministic layer always runs first; the
// model layer only overrides it when the policy allows and produces a valid
// verdict.
func (rt Runtime) verifyTaskGraph(
	ctx context.Context,
	g *session.TaskGraph,
	contract *session.TaskContract,
	trace *traceRecorder,
) session.GraphVerificationResult {
	deterministic := session.VerifyTaskGraphWithContract(g, contract)

	// The graph's not settled — defer to the deterministic verdict verbatim.
	if deterministic.Status == session.GraphStatusAwaitingInput || deterministic.Status == session.GraphStatusRunning {
		return deterministic
	}

	policy := "on_failure"
	if rt.Config != nil {
		policy = rt.Config.Execution.TaskVerifierValue()
	}

	runModel := false
	switch policy {
	case "off":
		runModel = false
	case "always":
		runModel = true
	default: // on_failure
		runModel = deterministic.Status == session.GraphStatusNeedsRepair
	}

	if !runModel {
		return deterministic
	}

	modelResult := rt.verifyTaskGraphWithModel(ctx, g, contract, deterministic, trace)
	if modelResult.Status == "" {
		return deterministic
	}

	merged := deterministic
	merged.Status = modelResult.Status
	if strings.TrimSpace(modelResult.Reason) != "" {
		merged.Reason = modelResult.Reason
	}
	if len(modelResult.Missing) > 0 {
		merged.MissingNodes = modelResult.Missing
	}
	return merged
}

func (rt Runtime) verifyTaskGraphWithModel(
	ctx context.Context,
	g *session.TaskGraph,
	contract *session.TaskContract,
	deterministic session.GraphVerificationResult,
	trace *traceRecorder,
) taskGraphVerdict {
	if rt.Model == nil {
		if trace != nil {
			_ = trace.write(map[string]any{
				"type":     "task_verifier_skipped",
				"graph_id": g.ID,
				"task_id":  g.TaskID,
				"reason":   "no model configured",
			})
		}
		return taskGraphVerdict{}
	}

	prompt := renderTaskVerifierPrompt(g, contract, deterministic)
	verifyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if trace != nil {
		_ = trace.write(map[string]any{
			"type":          "model_call_start",
			"model_stage":   "task_verifier",
			"graph_id":      g.ID,
			"task_id":       g.TaskID,
			"deterministic": deterministic.Status,
			"repair_rounds": len(g.RepairAttempts),
			"prompt_chars":  len(prompt),
		})
	}

	reply, err := rt.Model.Next(verifyCtx, agentcore.Context{
		SystemPrompt: taskVerifierSystemPrompt,
		Messages:     []agentcore.Message{{Role: agentcore.RoleUser, Content: prompt}},
	})
	if err != nil {
		if trace != nil {
			_ = trace.write(map[string]any{
				"type":        "model_call_failed",
				"model_stage": "task_verifier",
				"graph_id":    g.ID,
				"task_id":     g.TaskID,
				"error":       err.Error(),
			})
		}
		return taskGraphVerdict{}
	}

	if trace != nil {
		_ = trace.write(map[string]any{
			"type":          "task_verifier_raw_output",
			"graph_id":      g.ID,
			"task_id":       g.TaskID,
			"output_length": len(reply.Content),
			"raw":           summarize(reply.Content),
		})
	}

	verdict := parseTaskVerifierOutput(reply.Content)
	if trace != nil {
		_ = trace.write(map[string]any{
			"type":              "task_verifier_decision",
			"graph_id":          g.ID,
			"task_id":           g.TaskID,
			"verify_status":     verdict.Status,
			"verify_reason":     verdict.Reason,
			"verify_confidence": verdict.Confidence,
		})
	}
	return verdict
}

type taskGraphVerdict struct {
	Status     string
	Reason     string
	Missing    []string
	Confidence string
}

func renderTaskVerifierPrompt(g *session.TaskGraph, contract *session.TaskContract, deterministic session.GraphVerificationResult) string {
	var sb strings.Builder
	sb.WriteString("Evaluate whether the combined verified node output satisfies the overall task acceptance.\n\n")

	acceptance := ""
	if contract != nil {
		acceptance = strings.TrimSpace(contract.TaskAcceptance)
		if acceptance == "" {
			acceptance = strings.TrimSpace(contract.ExpectedOutcome)
		}
	}
	if acceptance == "" {
		acceptance = strings.TrimSpace(taskGoalFromGraph(g))
	}
	sb.WriteString(fmt.Sprintf("Task Acceptance: %s\n", acceptance))

	if contract != nil && len(contract.FinalOutput) > 0 {
		sb.WriteString(fmt.Sprintf("Required Final Outputs: %s\n", strings.Join(contract.FinalOutput, ", ")))
	}

	sb.WriteString("\nDeterministic Verdict: ")
	sb.WriteString(deterministic.Status)
	if strings.TrimSpace(deterministic.Reason) != "" {
		sb.WriteString(" (")
		sb.WriteString(deterministic.Reason)
		sb.WriteString(")")
	}
	sb.WriteString("\n\nVerified Node Outputs (compact):\n")
	for i := range g.Nodes {
		n := &g.Nodes[i]
		if n.Status != session.NodeStatusCompleted && n.Status != session.NodeStatusSkipped {
			continue
		}
		if n.Acceptance.Criteria != "" && !n.Acceptance.Verified {
			continue
		}
		sb.WriteString(fmt.Sprintf("- %s [%s/%s]: goal=%q", n.ID, n.Type, n.Mode, n.Goal))
		if strings.TrimSpace(n.ResultSummary) != "" {
			sb.WriteString(fmt.Sprintf(" summary=%q", n.ResultSummary))
		}
		if text := verifierNodeOutputText(n); text != "" {
			sb.WriteString(fmt.Sprintf(" text=%q", truncateForVerifier(text)))
		}
		if len(n.Output) > 0 {
			outKeys := make([]string, 0, len(n.Output))
			for k := range n.Output {
				outKeys = append(outKeys, k)
			}
			sb.WriteString(fmt.Sprintf(" outputs=%v", outKeys))
		}
		sb.WriteString("\n")
	}

	if len(g.RepairAttempts) > 0 {
		sb.WriteString("\nAccumulated Repair Feedback (most recent last):\n")
		for _, attempt := range g.RepairAttempts {
			sb.WriteString(fmt.Sprintf("- round %d (%s): %s\n", attempt.Round, attempt.Status, attempt.VerifierFeedback))
		}
	}

	sb.WriteString("\nOutput a JSON object with: status, reason, missing, confidence, verifier_feedback.\n")
	return sb.String()
}

func truncateForVerifier(text string) string {
	const max = 8000
	if len([]rune(text)) <= max {
		return text
	}
	runes := []rune(text)
	return string(runes[:max]) + "...[truncated]"
}

func parseTaskVerifierOutput(raw string) taskGraphVerdict {
	jsonText := extractJSONBlock(raw)
	if jsonText == "" {
		return taskGraphVerdict{Status: "", Reason: "task verifier produced no valid JSON"}
	}

	var fields map[string]any
	if err := json.Unmarshal([]byte(jsonText), &fields); err != nil {
		return taskGraphVerdict{Status: "", Reason: fmt.Sprintf("task verifier output not valid JSON: %v", err)}
	}

	out := taskVerifyOutput{
		Status:           asString(fields["status"]),
		Reason:           asString(fields["reason"]),
		Confidence:       asString(fields["confidence"]),
		VerifierFeedback: asString(fields["verifier_feedback"]),
	}
	if raw, ok := fields["missing"]; ok {
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

	status := normalizeTaskVerifierStatus(out.Status)
	verdict := taskGraphVerdict{
		Status:     status,
		Reason:     strings.TrimSpace(out.Reason),
		Missing:    out.Missing,
		Confidence: normalizeConfidence(out.Confidence),
	}
	if feedback := strings.TrimSpace(out.VerifierFeedback); feedback != "" {
		if verdict.Reason == "" {
			verdict.Reason = feedback
		}
	}
	return verdict
}

func normalizeTaskVerifierStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "passed":
		return session.GraphStatusCompleted
	case "needs_repair":
		return session.GraphStatusNeedsRepair
	case "failed":
		return session.GraphStatusFailed
	case "blocked":
		return session.GraphStatusBlocked
	default:
		return ""
	}
}
