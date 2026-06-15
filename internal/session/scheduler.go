package session

func ReadyNodes(g *TaskGraph, maxParallel int) []string {
	if g == nil || maxParallel <= 0 {
		return nil
	}
	var ready []string
	for i := range g.Nodes {
		n := &g.Nodes[i]
		if n.Status != NodeStatusPending {
			continue
		}
		if !allDepsSatisfied(g, n) {
			continue
		}
		ready = append(ready, n.ID)
	}
	if maxParallel > 0 && len(ready) > maxParallel {
		ready = ready[:maxParallel]
	}
	return ready
}

func allDepsSatisfied(g *TaskGraph, n *TaskGraphNode) bool {
	if len(n.Depends) == 0 {
		return true
	}
	for _, dep := range n.Depends {
		depNode := g.NodeByID(dep)
		if depNode == nil {
			return false
		}
		if depNode.Status != NodeStatusCompleted && depNode.Status != NodeStatusSkipped {
			return false
		}
	}
	return true
}

func UpdateGraphStatus(g *TaskGraph) string {
	if g == nil {
		return ""
	}
	hasAwaitingInput := false
	hasRunningOrReady := false
	hasBlocked := false
	hasFailed := false
	allDone := true

	for i := range g.Nodes {
		s := g.Nodes[i].Status
		switch s {
		case NodeStatusAwaitingInput:
			hasAwaitingInput = true
			allDone = false
		case NodeStatusRunning, NodeStatusReady:
			hasRunningOrReady = true
			allDone = false
		case NodeStatusBlocked:
			hasBlocked = true
			allDone = false
		case NodeStatusFailed:
			hasFailed = true
			allDone = false
		case NodeStatusPending:
			allDone = false
		}
	}

	if !hasRunningOrReady && !hasAwaitingInput && !allDone {
		if len(ReadyNodes(g, 1)) > 0 {
			hasRunningOrReady = true
		}
	}

	switch {
	case hasAwaitingInput:
		g.Status = GraphStatusAwaitingInput
	case hasBlocked && !hasRunningOrReady:
		g.Status = GraphStatusBlocked
	case hasFailed && !hasRunningOrReady:
		g.Status = GraphStatusFailed
	case allDone:
		g.Status = GraphStatusCompleted
	default:
		g.Status = GraphStatusRunning
	}
	return g.Status
}
