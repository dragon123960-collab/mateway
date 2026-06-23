package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/skill"
)

type LearningInboxInput struct {
	Home      string
	Workspace string
	Limit     int
}

type LearningInbox struct {
	Items                 []LearningInboxItem
	MemoryProposals       int
	SkillProposals        int
	Reflections           int
	FailedTasks           int
	RepeatedToolSequences int
}

type LearningInboxItem struct {
	Kind     string
	Priority int
	ID       string
	Title    string
	Status   string
	Path     string
	Summary  string
	Action   string
	Count    int
}

func BuildLearningInbox(input LearningInboxInput) (LearningInbox, error) {
	home := defaultString(input.Home, ".mateway")
	workspace := strings.TrimSpace(input.Workspace)
	if workspace == "" {
		workspace = filepath.Join(home, "workspace")
	}
	limit := input.Limit
	if limit <= 0 {
		limit = 5
	}
	var inbox LearningInbox
	if err := addMemoryProposalInboxItems(&inbox, home, limit); err != nil {
		return inbox, err
	}
	if err := addSkillProposalInboxItems(&inbox, home, workspace, limit); err != nil {
		return inbox, err
	}
	if err := addReflectionInboxItems(&inbox, home, limit); err != nil {
		return inbox, err
	}
	if err := addFailedTaskInboxItems(&inbox, home, limit); err != nil {
		return inbox, err
	}
	if err := addRepeatedToolSequenceInboxItems(&inbox, home, limit); err != nil {
		return inbox, err
	}
	sort.SliceStable(inbox.Items, func(i, j int) bool {
		if inbox.Items[i].Priority != inbox.Items[j].Priority {
			return inbox.Items[i].Priority > inbox.Items[j].Priority
		}
		if inbox.Items[i].Count != inbox.Items[j].Count {
			return inbox.Items[i].Count > inbox.Items[j].Count
		}
		return inbox.Items[i].ID < inbox.Items[j].ID
	})
	if len(inbox.Items) > limit {
		inbox.Items = inbox.Items[:limit]
	}
	return inbox, nil
}

func addMemoryProposalInboxItems(inbox *LearningInbox, home string, limit int) error {
	proposals, err := (ProposalStore{Home: home}).List()
	if err != nil {
		return err
	}
	for _, proposal := range proposals {
		if proposal.Status != "proposed" {
			continue
		}
		inbox.MemoryProposals++
		if inbox.MemoryProposals > limit*2 {
			continue
		}
		inbox.Items = append(inbox.Items, LearningInboxItem{
			Kind:     "memory_proposal",
			Priority: 90,
			ID:       proposal.ID,
			Title:    proposal.Title,
			Status:   proposal.Status,
			Path:     proposal.Path,
			Summary:  proposalReasonSummary(proposal),
			Action:   "mateway memory proposal show " + proposal.ID,
		})
	}
	return nil
}

func addSkillProposalInboxItems(inbox *LearningInbox, home, workspace string, limit int) error {
	proposals, err := (skill.ProposalStore{Home: home, Workspace: workspace}).List()
	if err != nil {
		return err
	}
	for _, proposal := range proposals {
		if proposal.Status != "proposed" {
			continue
		}
		inbox.SkillProposals++
		if inbox.SkillProposals > limit*2 {
			continue
		}
		inbox.Items = append(inbox.Items, LearningInboxItem{
			Kind:     "skill_proposal",
			Priority: 85,
			ID:       proposal.ID,
			Title:    firstNonEmpty(proposal.SkillName, filepath.Base(filepath.Dir(proposal.TargetPath))),
			Status:   proposal.Status,
			Path:     proposal.TargetPath,
			Summary:  summarizeNudgeText(proposal.Reason, 110),
			Action:   "mateway skill proposal show " + proposal.ID,
		})
	}
	return nil
}

func addReflectionInboxItems(inbox *LearningInbox, home string, limit int) error {
	dir := filepath.Join(home, "observe", "reflections")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		doc, issues := ReadDocument(path)
		if len(issues) > 0 || doc.FrontMatter == nil {
			continue
		}
		if stringValue(doc.FrontMatter["status"]) != "proposed" {
			continue
		}
		inbox.Reflections++
		if inbox.Reflections > limit*2 {
			continue
		}
		inbox.Items = append(inbox.Items, LearningInboxItem{
			Kind:     "reflection",
			Priority: 70,
			ID:       strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())),
			Title:    firstMarkdownHeading(doc.Body, "Task reflection"),
			Status:   "proposed",
			Path:     path,
			Summary:  summarizeNudgeText(firstBodyLine(doc.Body), 110),
			Action:   "review " + path,
		})
	}
	return nil
}

func addFailedTaskInboxItems(inbox *LearningInbox, home string, limit int) error {
	store := session.NewStore(home)
	keys, err := store.List()
	if err != nil {
		return err
	}
	for _, key := range keys {
		state, err := store.Load(key)
		if err != nil {
			continue
		}
		for _, task := range state.Tasks {
			status := strings.ToLower(strings.TrimSpace(task.Status))
			if status != "failed" && status != "blocked" {
				continue
			}
			inbox.FailedTasks++
			if inbox.FailedTasks > limit*2 {
				continue
			}
			inbox.Items = append(inbox.Items, LearningInboxItem{
				Kind:     "failed_task",
				Priority: 65,
				ID:       task.ID,
				Title:    task.Goal,
				Status:   task.Status,
				Summary:  task.Summary,
				Action:   "mateway local session show " + key,
			})
		}
	}
	return nil
}

func addRepeatedToolSequenceInboxItems(inbox *LearningInbox, home string, limit int) error {
	counts := map[string]int{}
	example := map[string]string{}
	err := scanJSONL(filepath.Join(home, "observe", "learning", "events.jsonl"), func(line string) {
		var event learningSkillEvent
		if json.Unmarshal([]byte(line), &event) != nil {
			return
		}
		if len(event.ToolSequence) < 2 {
			return
		}
		key := strings.Join(event.ToolSequence, " -> ")
		counts[key]++
		if example[key] == "" {
			example[key] = strings.TrimSpace(event.Goal)
		}
	})
	if err != nil {
		return err
	}
	for key, count := range counts {
		if count < 2 {
			continue
		}
		inbox.RepeatedToolSequences++
		if inbox.RepeatedToolSequences > limit*2 {
			continue
		}
		inbox.Items = append(inbox.Items, LearningInboxItem{
			Kind:     "repeated_tool_sequence",
			Priority: 55,
			ID:       duplicateKey(key),
			Title:    key,
			Status:   "observed",
			Summary:  summarizeNudgeText(example[key], 110),
			Action:   "mateway memory heartbeat skill",
			Count:    count,
		})
	}
	return nil
}

func firstMarkdownHeading(body, fallback string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return fallback
}

func firstBodyLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "-"))
		if line != "" && !strings.HasPrefix(line, "#") {
			return line
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
