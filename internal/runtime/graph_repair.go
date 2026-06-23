package runtime

import (
	"fmt"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/session"
)

// maxNodeReplanDepth resolves the node-level model replan depth cap from config
// (default 2, range 1-3).
func maxNodeReplanDepth(cfg *config.Root) int {
	if cfg == nil {
		return 2
	}
	return cfg.Execution.MaxNodeReplanDepthValue()
}

// repairAttemptStatus translates a follow-up task-level verify status into the
// status recorded on the RepairAttempt that just completed.
func repairAttemptStatus(verifyStatus string) string {
	switch verifyStatus {
	case session.GraphStatusCompleted:
		return session.RepairStatusPassed
	case session.GraphStatusBlocked:
		return session.RepairStatusBlocked
	default:
		return session.RepairStatusFailed
	}
}

// buildRepairNode constructs the task-level repair/synthesis node appended when
// the task verifier returns needs_repair. Deliberately model/node typed (no new
// enum): a single direct-mode model node that synthesises the missing
// acceptance from the verifier feedback.
func buildRepairNode(g *session.TaskGraph, task *session.TaskNode, feedback string, round int) session.TaskGraphNode {
	now := time.Now()
	depends := make([]string, 0, len(g.Nodes))
	verifiedOutputs := make(map[string]any, len(g.Nodes))
	for i := range g.Nodes {
		n := &g.Nodes[i]
		if n.Status != session.NodeStatusCompleted && n.Status != session.NodeStatusSkipped {
			continue
		}
		if n.Acceptance.Criteria != "" && !n.Acceptance.Verified {
			continue
		}
		depends = append(depends, n.ID)
		for k, v := range n.Output {
			if _, exists := verifiedOutputs[k]; !exists {
				verifiedOutputs[k] = v
			}
		}
	}

	acceptance := "The user-visible task goal"
	if task != nil && strings.TrimSpace(task.Goal) != "" {
		acceptance = strings.TrimSpace(task.Goal)
	}

	feedback = strings.TrimSpace(feedback)
	if feedback == "" {
		feedback = "task acceptance not yet satisfied"
	}

	input := map[string]any{
		"task_acceptance":  acceptance,
		"repair_target":    feedback,
		"repair_round":     round,
		"verified_outputs": verifiedOutputs,
	}
	if len(g.RepairAttempts) > 0 {
		prior := make([]map[string]any, 0, len(g.RepairAttempts))
		for _, a := range g.RepairAttempts {
			prior = append(prior, map[string]any{
				"round":             a.Round,
				"verifier_feedback": a.VerifierFeedback,
				"status":            a.Status,
			})
		}
		input["prior_repair_attempts"] = prior
	}

	return session.TaskGraphNode{
		ID:      fmt.Sprintf("repair-%d-%s", round, g.ID),
		Type:    session.NodeTypeModel,
		Mode:    session.NodeModeDirect,
		Goal:    fmt.Sprintf("Synthesise the missing task acceptance: %s", feedback),
		Status:  session.NodeStatusPending,
		Depends: depends,
		Input:   input,
		Acceptance: session.Acceptance{
			Criteria: "Produces a complete result that closes the verifier's listed gaps.",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func unrecordedTaskRepairNode(g *session.TaskGraph) (string, string) {
	if g == nil {
		return "", ""
	}
	recorded := make(map[string]bool, len(g.RepairAttempts))
	for _, attempt := range g.RepairAttempts {
		recorded[attempt.RepairNodeID] = true
	}
	for i := len(g.Nodes) - 1; i >= 0; i-- {
		n := &g.Nodes[i]
		if recorded[n.ID] || n.Type != session.NodeTypeModel || n.Mode != session.NodeModeDirect {
			continue
		}
		if n.Status != session.NodeStatusCompleted && n.Status != session.NodeStatusBlocked && n.Status != session.NodeStatusFailed {
			continue
		}
		if _, ok := n.Input["repair_round"]; !ok {
			continue
		}
		feedback, _ := n.Input["repair_target"].(string)
		return n.ID, strings.TrimSpace(feedback)
	}
	return "", ""
}
