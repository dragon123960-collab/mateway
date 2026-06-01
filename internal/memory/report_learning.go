package memory

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/dongping/mateway/internal/skill"
)

type LearningReportInput struct {
	Home      string
	Workspace string
}

type LearningReport struct {
	Tasks                  int
	Failures               int
	Reflections            int
	SkillUsage             int
	SkillIssues            int
	MemoryProposalsPending int
	SkillProposalsPending  int
	LastLearningAudit      string
}

func BuildLearningReport(input LearningReportInput) (LearningReport, error) {
	home := defaultString(input.Home, ".mateway")
	report := LearningReport{}
	learningPath := filepath.Join(home, "observe", "learning", "events.jsonl")
	if err := scanJSONL(learningPath, func(line string) {
		var event LearningEvidence
		if json.Unmarshal([]byte(line), &event) != nil {
			return
		}
		if event.Type == "task_completed" {
			report.Tasks++
			if event.Status != "" && event.Status != "completed" {
				report.Failures++
			}
			for _, step := range event.ToolSteps {
				if step.Status != "" && step.Status != "accepted" {
					report.Failures++
					break
				}
			}
		}
	}); err != nil {
		return report, err
	}
	entries, err := os.ReadDir(filepath.Join(home, "observe", "reflections"))
	if err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
				report.Reflections++
			}
		}
	} else if !os.IsNotExist(err) {
		return report, err
	}
	if err := scanJSONL(filepath.Join(home, "observe", "skill_usage", "events.jsonl"), func(line string) {
		var event SkillUsageEvidence
		if json.Unmarshal([]byte(line), &event) != nil {
			return
		}
		report.SkillUsage++
		if skillEventHasIssue(event) {
			report.SkillIssues++
		}
	}); err != nil {
		return report, err
	}
	if proposals, err := (ProposalStore{Home: home}).List(); err == nil {
		for _, proposal := range proposals {
			if proposal.Status == "proposed" {
				report.MemoryProposalsPending++
			}
		}
	} else {
		return report, err
	}
	workspace := strings.TrimSpace(input.Workspace)
	if workspace == "" {
		workspace = filepath.Join(home, "workspace")
	}
	if proposals, err := (skill.ProposalStore{Home: home, Workspace: workspace}).List(); err == nil {
		for _, proposal := range proposals {
			if proposal.Status == "proposed" {
				report.SkillProposalsPending++
			}
		}
	} else {
		return report, err
	}
	report.LastLearningAudit = lastAuditType(filepath.Join(home, "observe", "audit", "memory.jsonl"))
	return report, nil
}

func scanJSONL(path string, fn func(string)) error {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		fn(scanner.Text())
	}
	return scanner.Err()
}

func lastAuditType(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	last := ""
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var payload map[string]any
		if json.Unmarshal(scanner.Bytes(), &payload) == nil {
			if typ, ok := payload["type"].(string); ok {
				last = typ
			}
		}
	}
	return last
}
