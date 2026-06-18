package session

import (
	"fmt"
	"sort"
	"strings"
)

func ValidateTaskGraph(g *TaskGraph) GraphValidationErrors {
	var errs GraphValidationErrors
	if g == nil {
		return append(errs, GraphValidationError{Message: "graph is nil"})
	}

	errs = append(errs, validateGraphHeader(g)...)
	if len(errs) > 0 {
		return errs
	}

	errs = append(errs, validateNodeIDs(g)...)
	errs = append(errs, validateNodeFields(g)...)
	errs = append(errs, validateDependencies(g)...)

	if len(errs) > 0 {
		return errs
	}

	errs = append(errs, validateCycles(g)...)
	return errs
}

func validateGraphHeader(g *TaskGraph) GraphValidationErrors {
	var errs GraphValidationErrors
	if strings.TrimSpace(g.ID) == "" {
		errs = append(errs, GraphValidationError{Message: "graph ID is empty"})
	}
	if strings.TrimSpace(g.TaskID) == "" {
		errs = append(errs, GraphValidationError{Message: "graph TaskID is empty"})
	}
	if !IsValidGraphStatus(g.Status) {
		errs = append(errs, GraphValidationError{
			Message: fmt.Sprintf("invalid graph status %q", g.Status),
		})
	}
	return errs
}

func validateNodeIDs(g *TaskGraph) GraphValidationErrors {
	var errs GraphValidationErrors
	if len(g.Nodes) == 0 {
		return append(errs, GraphValidationError{Message: "graph has no nodes"})
	}

	seen := make(map[string]bool, len(g.Nodes))
	for i := range g.Nodes {
		id := strings.TrimSpace(g.Nodes[i].ID)
		if id == "" {
			errs = append(errs, GraphValidationError{
				Message: fmt.Sprintf("node at index %d has empty ID", i),
			})
			continue
		}
		if seen[id] {
			errs = append(errs, GraphValidationError{
				Message: "duplicate node ID",
				NodeID:  id,
			})
		}
		seen[id] = true
	}
	return errs
}

func validateNodeFields(g *TaskGraph) GraphValidationErrors {
	var errs GraphValidationErrors
	for i := range g.Nodes {
		n := &g.Nodes[i]
		id := strings.TrimSpace(n.ID)
		if id == "" {
			continue
		}

		t := strings.TrimSpace(n.Type)
		mode := strings.TrimSpace(n.Mode)

		if strings.TrimSpace(n.Goal) == "" {
			if t != NodeTypeHumanReview && t != NodeTypeHumanConfirm {
				errs = append(errs, GraphValidationError{
					Message: "node goal is empty",
					NodeID:  id,
				})
			}
		}
		if t == "" {
			errs = append(errs, GraphValidationError{
				Message: "node type is empty",
				NodeID:  id,
			})
			continue
		}
		if !IsValidNodeType(t) {
			errs = append(errs, GraphValidationError{
				Message: fmt.Sprintf("invalid node type %q", t),
				NodeID:  id,
			})
		}

		if mode != "" && !IsValidNodeMode(mode) {
			errs = append(errs, GraphValidationError{
				Message: fmt.Sprintf("invalid node mode %q", mode),
				NodeID:  id,
			})
		}

		if mode != "" && !IsValidTypeModeCombo(t, mode) {
			valid := ValidModesForType(t)
			msg := fmt.Sprintf("invalid mode %q for type %q", mode, t)
			if len(valid) > 0 {
				msg += fmt.Sprintf(" (valid modes for %s: %s)", t, strings.Join(valid, ", "))
			}
			errs = append(errs, GraphValidationError{
				Message: msg,
				NodeID:  id,
			})
		}

		if !IsValidNodeStatus(n.Status) {
			errs = append(errs, GraphValidationError{
				Message: fmt.Sprintf("invalid node status %q", n.Status),
				NodeID:  id,
			})
		}

		switch t {
		case NodeTypeTool:
			tool := strings.TrimSpace(n.Executor)
			if tool == "" {
				if v, ok := n.Input["tool"]; ok {
					if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
						tool = s
					}
				}
			}
			if tool == "" {
				errs = append(errs, GraphValidationError{
					Message: "tool node missing executor or input.tool",
					NodeID:  id,
				})
			}

		case NodeTypeSkill:
			skill := strings.TrimSpace(n.Executor)
			if skill == "" {
				if v, ok := n.Input["skill"]; ok {
					if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
						skill = s
					}
				}
			}
			if skill == "" {
				errs = append(errs, GraphValidationError{
					Message: "skill node missing executor or input.skill",
					NodeID:  id,
				})
			}

		case NodeTypeSubtask:
			if mode == "" {
				errs = append(errs, GraphValidationError{
					Message: "subtask node missing mode",
					NodeID:  id,
				})
			}

		case NodeTypeHumanReview, NodeTypeHumanConfirm:
			if strings.TrimSpace(n.Goal) == "" && strings.TrimSpace(n.Acceptance.Criteria) == "" {
				errs = append(errs, GraphValidationError{
					Message: "human node missing goal or acceptance criteria",
					NodeID:  id,
				})
			}
		}
	}
	return errs
}

func validateDependencies(g *TaskGraph) GraphValidationErrors {
	var errs GraphValidationErrors
	nodeIDs := make(map[string]bool, len(g.Nodes))
	for _, n := range g.Nodes {
		id := strings.TrimSpace(n.ID)
		if id != "" {
			nodeIDs[id] = true
		}
	}

	for i := range g.Nodes {
		for _, dep := range g.Nodes[i].Depends {
			dep = strings.TrimSpace(dep)
			if dep == "" {
				errs = append(errs, GraphValidationError{
					Message: "empty dependency reference",
					NodeID:  g.Nodes[i].ID,
				})
				continue
			}
			if dep == g.Nodes[i].ID {
				errs = append(errs, GraphValidationError{
					Message: "node cannot depend on itself",
					NodeID:  g.Nodes[i].ID,
				})
				continue
			}
			if !nodeIDs[dep] {
				errs = append(errs, GraphValidationError{
					Message: fmt.Sprintf("depends on unknown node %q", dep),
					NodeID:  g.Nodes[i].ID,
				})
			}
		}
	}
	return errs
}

func validateCycles(g *TaskGraph) GraphValidationErrors {
	nodeIDs := make(map[string]bool, len(g.Nodes))
	for _, n := range g.Nodes {
		id := strings.TrimSpace(n.ID)
		if id != "" {
			nodeIDs[id] = true
		}
	}

	adj := make(map[string][]string, len(g.Nodes))
	for _, n := range g.Nodes {
		id := strings.TrimSpace(n.ID)
		if id == "" {
			continue
		}
		for _, dep := range n.Depends {
			dep = strings.TrimSpace(dep)
			if dep != "" && nodeIDs[dep] {
				adj[dep] = append(adj[dep], id)
			}
		}
	}

	state := make(map[string]int, len(nodeIDs))
	for id := range nodeIDs {
		state[id] = 0
	}

	var dfs func(id string) bool
	dfs = func(id string) bool {
		state[id] = 1
		for _, next := range adj[id] {
			if state[next] == 1 {
				return false
			}
			if state[next] == 0 {
				if !dfs(next) {
					return false
				}
			}
		}
		state[id] = 2
		return true
	}

	ids := make([]string, 0, len(nodeIDs))
	for id := range nodeIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		if state[id] == 0 {
			if !dfs(id) {
				return GraphValidationErrors{{Message: "graph contains a cycle"}}
			}
		}
	}
	return nil
}
