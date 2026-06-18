package runtime

import "github.com/dongping/mateway/internal/session"

func applyLocalReplanWithTrace(g *session.TaskGraph, req session.LocalReplanRequest, trace *traceRecorder) session.GraphValidationErrors {
	if trace != nil {
		_ = trace.write(map[string]any{
			"type":           "local_replan_start",
			"graph_id":       g.ID,
			"task_id":        g.TaskID,
			"failed_node_id": req.FailedNodeID,
			"replacements":   len(req.ReplacementNodes),
		})
	}
	errs := session.ApplyLocalReplan(g, req)
	if !errs.IsValid() {
		if trace != nil {
			_ = trace.write(map[string]any{
				"type":           "local_replan_failed",
				"graph_id":       g.ID,
				"task_id":        g.TaskID,
				"failed_node_id": req.FailedNodeID,
				"error":          errs.Error(),
			})
		}
		return errs
	}
	if trace != nil {
		_ = trace.write(map[string]any{
			"type":           "local_replan_applied",
			"graph_id":       g.ID,
			"task_id":        g.TaskID,
			"failed_node_id": req.FailedNodeID,
			"nodes":          g.NodeIDs(),
		})
	}
	return nil
}
