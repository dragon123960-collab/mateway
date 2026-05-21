package runtime

import (
	"fmt"
	"strings"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/memory"
	"github.com/dongping/mateway/internal/session"
)

const memoryProposalReplyNoteLimit = 180

func (l *AgentLoop) proposeMemoryFromTask(resp channel.OutboundMessage, task session.TaskState) {
	if l.runtime.Config == nil || !l.runtime.Config.Memory.Enabled {
		return
	}
	if task.Status != session.TaskCompleted || task.Failed {
		return
	}
	if strings.TrimSpace(l.runtime.Memory.Root) == "" {
		return
	}
	if !taskHasMemoryEvidence(task) {
		return
	}
	body := renderTaskMemoryProposalBody(resp, task)
	if strings.TrimSpace(body) == "" {
		return
	}
	agentID := firstNonEmpty(l.runtime.Config.Agents.Default, "main")
	result, err := l.runtime.Memory.Propose(memory.ProposalInput{
		AgentID:    agentID,
		Scope:      "agent",
		Type:       "source",
		Title:      memoryProposalTitle(task),
		Body:       body,
		Sources:    memoryProposalSources(task),
		Tags:       []string{"task-summary", "auto-proposal"},
		Confidence: "low",
	})
	if err != nil {
		l.runtime.Logger.Event("runtime.memory_proposal_failed", map[string]any{
			"trace_id": l.state.traceID,
			"task_id":  task.ID,
			"error":    err.Error(),
		})
		return
	}
	l.runtime.Logger.Event("runtime.memory_proposal_created", map[string]any{
		"trace_id": l.state.traceID,
		"task_id":  task.ID,
		"path":     result.Path,
		"id":       result.ID,
	})
}

func taskHasMemoryEvidence(task session.TaskState) bool {
	if len(task.Artifacts) > 0 {
		return true
	}
	for _, toolName := range task.ToolNames {
		switch strings.TrimSpace(toolName) {
		case "file.read", "file.summary", "project.index", "web.search":
			return true
		}
	}
	return false
}

func memoryProposalTitle(task session.TaskState) string {
	base := firstNonEmpty(task.Topic, task.PlanSummary, task.ResolvedQuery, task.UserText, task.ID)
	base = compactText(base, 80)
	if base == "" {
		base = "Task summary"
	}
	return "Task memory: " + base
}

func renderTaskMemoryProposalBody(resp channel.OutboundMessage, task session.TaskState) string {
	var lines []string
	lines = append(lines, "This is an automatically proposed memory from a completed task. Review it before committing.")
	lines = append(lines, "")
	lines = append(lines, "## Task")
	lines = append(lines, "- Task ID: "+task.ID)
	lines = append(lines, "- Trace ID: "+task.TraceID)
	if query := firstNonEmpty(task.ResolvedQuery, task.UserText); query != "" {
		lines = append(lines, "- User request: "+compactText(query, 240))
	}
	if task.PlanSummary != "" {
		lines = append(lines, "- Plan summary: "+compactText(task.PlanSummary, 180))
	}
	if len(task.ToolNames) > 0 {
		lines = append(lines, "- Tools: "+strings.Join(task.ToolNames, ", "))
	}
	lines = append(lines, "")
	lines = append(lines, "## Candidate Memory")
	lines = append(lines, compactText(firstNonEmpty(resp.Text, task.LastAnswer, task.ReplyPreview), memoryProposalReplyNoteLimit))
	if len(task.Artifacts) > 0 {
		lines = append(lines, "")
		lines = append(lines, "## Evidence")
		for _, artifact := range task.Artifacts {
			lines = append(lines, "- "+formatProposalArtifact(artifact))
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func formatProposalArtifact(artifact session.Artifact) string {
	parts := []string{"kind=" + firstNonEmpty(artifact.Kind, "artifact")}
	if artifact.Path != "" {
		parts = append(parts, "path="+artifact.Path)
		if artifact.StartLine > 0 {
			line := fmt.Sprint(artifact.StartLine)
			if artifact.EndLine > artifact.StartLine {
				line += "-" + fmt.Sprint(artifact.EndLine)
			}
			parts = append(parts, "lines="+line)
		}
	}
	if artifact.SourceURL != "" {
		parts = append(parts, "url="+artifact.SourceURL)
	}
	if artifact.Label != "" {
		parts = append(parts, "label="+compactText(artifact.Label, 80))
	}
	if artifact.Summary != "" {
		parts = append(parts, "summary="+compactText(artifact.Summary, 120))
	}
	return strings.Join(parts, "; ")
}

func memoryProposalSources(task session.TaskState) []string {
	sources := []string{}
	if task.TraceID != "" {
		sources = append(sources, "trace:"+task.TraceID)
	}
	if task.ID != "" {
		sources = append(sources, "task:"+task.ID)
	}
	for _, artifact := range task.Artifacts {
		switch {
		case artifact.Path != "":
			sources = append(sources, memoryProposalFileSource(artifact))
		case artifact.SourceURL != "":
			sources = append(sources, artifact.SourceURL)
		}
	}
	if len(sources) == 0 {
		sources = append(sources, fmt.Sprintf("task:%s", firstNonEmpty(task.ID, "unknown")))
	}
	return sources
}

func memoryProposalFileSource(artifact session.Artifact) string {
	source := "file:" + artifact.Path
	if artifact.StartLine <= 0 {
		return source
	}
	source += ":" + fmt.Sprint(artifact.StartLine)
	if artifact.EndLine > artifact.StartLine {
		source += "-" + fmt.Sprint(artifact.EndLine)
	}
	return source
}
