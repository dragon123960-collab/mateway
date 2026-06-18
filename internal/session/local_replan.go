package session

import (
	"fmt"
	"strings"
	"time"
)

type LocalReplanRequest struct {
	FailedNodeID     string
	ReplacementNodes []TaskGraphNode
}

func ApplyLocalReplan(g *TaskGraph, req LocalReplanRequest) GraphValidationErrors {
	if g == nil {
		return GraphValidationErrors{{Message: "graph is nil"}}
	}
	failedID := strings.TrimSpace(req.FailedNodeID)
	if failedID == "" {
		return GraphValidationErrors{{Message: "failed node ID is empty"}}
	}
	failed := g.NodeByID(failedID)
	if failed == nil {
		return GraphValidationErrors{{Message: fmt.Sprintf("failed node %q not found", failedID)}}
	}
	if failed.Status != NodeStatusFailed && failed.Status != NodeStatusBlocked && failed.Status != NodeStatusNeedsReplan {
		return GraphValidationErrors{{NodeID: failedID, Message: "local replan requires failed, blocked, or needs_replan node"}}
	}
	if len(req.ReplacementNodes) == 0 {
		return GraphValidationErrors{{Message: "local replan has no replacement nodes"}}
	}

	replaceSet := downstreamPendingSet(g, failedID)
	replaceSet[failedID] = true

	newNodes := make([]TaskGraphNode, 0, len(g.Nodes)+len(req.ReplacementNodes))
	for _, n := range g.Nodes {
		if replaceSet[n.ID] {
			continue
		}
		newNodes = append(newNodes, n)
	}
	now := time.Now()
	for _, replacement := range req.ReplacementNodes {
		if replacement.Status == "" {
			replacement.Status = NodeStatusPending
		}
		if replacement.CreatedAt.IsZero() {
			replacement.CreatedAt = now
		}
		replacement.UpdatedAt = now
		newNodes = append(newNodes, replacement)
	}

	oldNodes := g.Nodes
	oldStatus := g.Status
	g.Nodes = newNodes
	g.Status = GraphStatusRunning
	g.UpdatedAt = now
	if errs := ValidateTaskGraph(g); !errs.IsValid() {
		g.Nodes = oldNodes
		g.Status = oldStatus
		return errs
	}
	return nil
}

func downstreamPendingSet(g *TaskGraph, rootID string) map[string]bool {
	out := map[string]bool{}
	changed := true
	for changed {
		changed = false
		for _, n := range g.Nodes {
			if out[n.ID] {
				continue
			}
			if n.Status != NodeStatusPending {
				continue
			}
			for _, dep := range n.Depends {
				if dep == rootID || out[dep] {
					out[n.ID] = true
					changed = true
					break
				}
			}
		}
	}
	return out
}
