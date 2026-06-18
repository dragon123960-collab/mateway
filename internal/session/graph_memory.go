package session

import (
	"strings"
	"time"
)

type GraphMemorySummary struct {
	GraphID      string
	TaskID       string
	Goal         string
	Status       string
	Nodes        []NodeMemorySummary
	FailedNodes  []string
	RetriedNodes []string
	BlockedNodes []string
	Skills       []SkillMemorySummary
}

type NodeMemorySummary struct {
	ID             string
	Type           string
	Mode           string
	Goal           string
	Status         string
	Executor       string
	Attempts       int
	ResultSummary  string
	Output         map[string]any
	FailureReason  string
	EvidenceRefs   []EvidenceRef
	VerifierStatus string
	VerifierReason string
	SelectedSkill  string
	VerifiedAt     time.Time
}

type SkillMemorySummary struct {
	Name          string
	NodeID        string
	Status        string
	ResultSummary string
	FailureReason string
}

func BuildGraphMemorySummary(g *TaskGraph, taskGoal string) *GraphMemorySummary {
	if g == nil {
		return nil
	}
	summary := &GraphMemorySummary{
		GraphID: g.ID,
		TaskID:  g.TaskID,
		Goal:    taskGoal,
		Status:  g.Status,
	}
	for _, n := range g.Nodes {
		node := NodeMemorySummary{
			ID:             n.ID,
			Type:           n.Type,
			Mode:           n.Mode,
			Goal:           n.Goal,
			Executor:       n.Executor,
			Status:         n.Status,
			Attempts:       n.Attempts,
			ResultSummary:  n.ResultSummary,
			Output:         cloneMemoryOutput(n.Output),
			FailureReason:  n.FailureReason,
			EvidenceRefs:   n.EvidenceRefs,
			VerifierStatus: verifierStatusForNode(n),
			VerifierReason: n.Acceptance.Reason,
			SelectedSkill:  selectedSkillForNode(n),
			VerifiedAt:     n.VerifiedAt,
		}
		summary.Nodes = append(summary.Nodes, node)
		if n.Status == NodeStatusFailed {
			summary.FailedNodes = append(summary.FailedNodes, n.ID)
		}
		if n.Attempts > 1 {
			summary.RetriedNodes = append(summary.RetriedNodes, n.ID)
		}
		if n.Status == NodeStatusBlocked || n.Status == NodeStatusAwaitingInput {
			summary.BlockedNodes = append(summary.BlockedNodes, n.ID)
		}
		if n.Type == NodeTypeSkill {
			name := selectedSkillForNode(n)
			if name == "" {
				name = n.Goal
			}
			summary.Skills = append(summary.Skills, SkillMemorySummary{
				Name:          name,
				NodeID:        n.ID,
				Status:        n.Status,
				ResultSummary: n.ResultSummary,
				FailureReason: n.FailureReason,
			})
		}
	}
	return summary
}

func cloneMemoryOutput(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]any, len(input))
	for k, v := range input {
		out[k] = v
	}
	return out
}

func verifierStatusForNode(n TaskGraphNode) string {
	if n.Acceptance.Verified {
		return VerificationPassed
	}
	switch n.Status {
	case NodeStatusFailed:
		return VerificationFailed
	case NodeStatusBlocked:
		return VerificationBlocked
	case NodeStatusAwaitingInput:
		return VerificationNeedsInput
	case NodeStatusRetrying:
		return VerificationRetry
	default:
		return ""
	}
}

func selectedSkillForNode(n TaskGraphNode) string {
	if n.Type != NodeTypeSkill {
		return ""
	}
	if strings.TrimSpace(n.Executor) != "" {
		return strings.TrimSpace(n.Executor)
	}
	if n.Input != nil {
		if skill, ok := n.Input["skill"].(string); ok {
			return strings.TrimSpace(skill)
		}
	}
	return ""
}
