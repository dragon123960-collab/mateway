package session

import (
	"time"
)

type GraphMemorySummary struct {
	GraphID string
	TaskID  string
	Goal    string
	Status  string
	Nodes   []NodeMemorySummary
}

type NodeMemorySummary struct {
	ID            string
	Type          string
	Goal          string
	Status        string
	Attempts      int
	ResultSummary string
	FailureReason string
	EvidenceRefs  []EvidenceRef
	VerifiedAt    time.Time
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
		summary.Nodes = append(summary.Nodes, NodeMemorySummary{
			ID:            n.ID,
			Type:          n.Type,
			Goal:          n.Goal,
			Status:        n.Status,
			Attempts:      n.Attempts,
			ResultSummary: n.ResultSummary,
			FailureReason: n.FailureReason,
			EvidenceRefs:  n.EvidenceRefs,
			VerifiedAt:    n.VerifiedAt,
		})
	}
	return summary
}
