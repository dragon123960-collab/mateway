package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/session"
)

type SessionDistillInput struct {
	Home   string
	State  session.State
	Reason string
}

type SessionDistillResult struct {
	Path string
}

type ProjectDistillInput struct {
	Home       string
	MemoryRoot string
	ProjectID  string
	Reason     string
}

type ProjectDistillResult struct {
	Path    string
	Entries int
}

func DistillSession(input SessionDistillInput) (SessionDistillResult, error) {
	home := strings.TrimSpace(input.Home)
	if home == "" {
		home = ".mateway"
	}
	now := time.Now().Format(time.RFC3339)
	id := "session_" + time.Now().Format("20060102_150405_000000")
	path := filepath.Join(home, "observe", "reflections", id+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return SessionDistillResult{}, err
	}
	if err := os.WriteFile(path, []byte(renderSessionDistill(input, now)), 0o644); err != nil {
		return SessionDistillResult{}, err
	}
	return SessionDistillResult{Path: path}, nil
}

func renderSessionDistill(input SessionDistillInput, now string) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("type: reflection\n")
	b.WriteString("scope: agent\n")
	b.WriteString("visibility: private\n")
	b.WriteString("status: proposed\n")
	b.WriteString("sources:\n")
	if input.State.Key != "" {
		b.WriteString("  - session:")
		b.WriteString(input.State.Key)
		b.WriteString("\n")
	}
	b.WriteString("confidence: low\n")
	b.WriteString("created_at: ")
	b.WriteString(now)
	b.WriteString("\nupdated_at: ")
	b.WriteString(now)
	b.WriteString("\nschema_version: 1\n")
	b.WriteString("---\n\n")
	b.WriteString("# Session distill\n\n")
	b.WriteString("- Session: ")
	b.WriteString(input.State.Key)
	b.WriteString("\n")
	if reason := strings.TrimSpace(input.Reason); reason != "" {
		b.WriteString("- Reason: ")
		b.WriteString(reason)
		b.WriteString("\n")
	}
	if input.State.Pending != nil {
		b.WriteString("- Pending: ")
		b.WriteString(input.State.Pending.Kind)
		if input.State.Pending.Question != "" {
			b.WriteString(" - ")
			b.WriteString(input.State.Pending.Question)
		}
		b.WriteString("\n")
	}
	if len(input.State.Tasks) > 0 {
		b.WriteString("\nTasks:\n")
		for _, task := range input.State.Tasks {
			b.WriteString("- ")
			b.WriteString(task.ID)
			b.WriteString(" ")
			b.WriteString(task.Status)
			b.WriteString(": ")
			b.WriteString(task.Goal)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func DistillProject(input ProjectDistillInput) (ProjectDistillResult, error) {
	home := strings.TrimSpace(input.Home)
	if home == "" {
		home = ".mateway"
	}
	projectID := strings.TrimSpace(input.ProjectID)
	if projectID == "" {
		return ProjectDistillResult{}, fmt.Errorf("project id is required")
	}
	memoryRoot := strings.TrimSpace(input.MemoryRoot)
	if memoryRoot == "" {
		memoryRoot = filepath.Join(home, "workspace", "memory")
	}
	index, issues, err := RebuildIndex(memoryRoot)
	if err != nil {
		return ProjectDistillResult{}, err
	}
	if hasError(issues) {
		return ProjectDistillResult{}, fmt.Errorf("project memory has lint errors")
	}
	var entries []IndexEntry
	projectPrefix := filepath.ToSlash(filepath.Join("projects", projectID)) + "/"
	for _, entry := range index.Entries {
		if entry.ProjectID == projectID || strings.HasPrefix(entry.Path, projectPrefix) {
			entries = append(entries, entry)
		}
	}
	now := time.Now().Format(time.RFC3339)
	id := "project_" + sanitizeProposalFileName(projectID) + "_" + time.Now().Format("20060102_150405_000000")
	path := filepath.Join(home, "observe", "reflections", id+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return ProjectDistillResult{}, err
	}
	if err := os.WriteFile(path, []byte(renderProjectDistill(input, entries, now)), 0o644); err != nil {
		return ProjectDistillResult{}, err
	}
	if err := writeProjectAudit(home, projectID, path, len(entries)); err != nil {
		return ProjectDistillResult{}, err
	}
	return ProjectDistillResult{Path: path, Entries: len(entries)}, nil
}

func renderProjectDistill(input ProjectDistillInput, entries []IndexEntry, now string) string {
	projectID := strings.TrimSpace(input.ProjectID)
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("type: reflection\n")
	b.WriteString("scope: project\n")
	b.WriteString("project_id: ")
	b.WriteString(projectID)
	b.WriteString("\nvisibility: private\n")
	b.WriteString("status: proposed\n")
	b.WriteString("sources:\n")
	b.WriteString("  - project:")
	b.WriteString(projectID)
	b.WriteString("\nconfidence: low\n")
	b.WriteString("created_at: ")
	b.WriteString(now)
	b.WriteString("\nupdated_at: ")
	b.WriteString(now)
	b.WriteString("\nschema_version: 1\n")
	b.WriteString("---\n\n")
	b.WriteString("# Project distill\n\n")
	b.WriteString("- Project: ")
	b.WriteString(projectID)
	b.WriteString("\n")
	if reason := strings.TrimSpace(input.Reason); reason != "" {
		b.WriteString("- Reason: ")
		b.WriteString(reason)
		b.WriteString("\n")
	}
	b.WriteString("- Entries: ")
	b.WriteString(fmtInt(len(entries)))
	b.WriteString("\n")
	if len(entries) > 0 {
		b.WriteString("\nMemory entries:\n")
		for _, entry := range entries {
			b.WriteString("- ")
			b.WriteString(entry.Path)
			if entry.Type != "" || entry.Status != "" {
				b.WriteString(" (")
				b.WriteString(strings.Trim(strings.TrimSpace(entry.Type+" / "+entry.Status), "/ "))
				b.WriteString(")")
			}
			if entry.Snippet != "" {
				b.WriteString(": ")
				b.WriteString(entry.Snippet)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

func writeProjectAudit(home, projectID, path string, entries int) error {
	store := ProposalStore{Home: home}
	return store.writeAudit("project_distilled", Proposal{ID: projectID, Path: path, Status: "proposed"}, fmtInt(entries))
}

func fmtInt(value int) string {
	return strconv.Itoa(value)
}
