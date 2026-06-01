package agentprofile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/secret"
)

const MaxPromptContextBytes = 2048

var coreProfileFiles = map[string]bool{
	"agent.md":  true,
	"soul.md":   true,
	"user.md":   true,
	"tools.md":  true,
	"memory.md": true,
}

func CoreProfileFileNames() []string {
	return []string{"agent.md", "soul.md", "user.md", "tools.md", "memory.md"}
}

type Store struct {
	Home      string
	Workspace string
}

type Proposal struct {
	ID          string `json:"id"`
	AgentID     string `json:"agent_id"`
	TargetPath  string `json:"target_path"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at,omitempty"`
	SourceTrace string `json:"source_trace"`
	OldContent  string `json:"old_content"`
	NewContent  string `json:"new_content"`
	Diff        string `json:"diff"`
	Reason      string `json:"reason,omitempty"`
}

type CreateInput struct {
	TargetPath  string
	NewContent  string
	SourceTrace string
}

func NewStore(cfg *config.Root) Store {
	home := config.DefaultHome()
	workspace := ""
	if cfg != nil {
		if strings.TrimSpace(cfg.App.Home) != "" {
			home = cfg.App.Home
		}
		workspace = strings.TrimSpace(cfg.App.Workspace)
	}
	if workspace == "" {
		workspace = filepath.Join(home, "workspace")
	}
	return Store{Home: home, Workspace: workspace}
}

func (s Store) Create(input CreateInput) (Proposal, error) {
	target, err := filepath.Abs(filepath.Clean(strings.TrimSpace(input.TargetPath)))
	if err != nil {
		return Proposal{}, err
	}
	agentID, ok := s.CoreTargetAgent(target)
	if !ok {
		return Proposal{}, fmt.Errorf("target is not an agent core profile file: %s", target)
	}
	content := strings.TrimSpace(input.NewContent)
	if err := ValidateCoreContent(target, content); err != nil {
		return Proposal{}, err
	}
	oldData, err := os.ReadFile(target)
	if err != nil && !os.IsNotExist(err) {
		return Proposal{}, err
	}
	now := time.Now().Format(time.RFC3339Nano)
	id := "agentprof_" + time.Now().Format("20060102_150405_000000")
	proposal := Proposal{
		ID:          id,
		AgentID:     agentID,
		TargetPath:  target,
		Status:      "proposed",
		CreatedAt:   now,
		UpdatedAt:   now,
		SourceTrace: strings.TrimSpace(input.SourceTrace),
		OldContent:  string(oldData),
		NewContent:  content,
		Diff:        UnifiedDiff(string(oldData), content),
	}
	if err := os.MkdirAll(filepath.Dir(s.path(id)), 0o755); err != nil {
		return Proposal{}, err
	}
	if err := s.write(proposal); err != nil {
		return Proposal{}, err
	}
	return proposal, nil
}

func (s Store) List() ([]Proposal, error) {
	dir := filepath.Join(s.home(), "observe", "agent_profile_proposals")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Proposal
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			continue
		}
		proposal, err := s.Read(strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())))
		if err != nil {
			continue
		}
		out = append(out, proposal)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return out, nil
}

func (s Store) Read(id string) (Proposal, error) {
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		return Proposal{}, err
	}
	var proposal Proposal
	if err := json.Unmarshal(data, &proposal); err != nil {
		return Proposal{}, err
	}
	return proposal, nil
}

func (s Store) Promote(id string) (Proposal, string, error) {
	proposal, err := s.Read(id)
	if err != nil {
		return Proposal{}, "", err
	}
	if proposal.Status != "proposed" {
		return Proposal{}, "", fmt.Errorf("proposal %s is %s", proposal.ID, proposal.Status)
	}
	target, err := filepath.Abs(filepath.Clean(proposal.TargetPath))
	if err != nil {
		return Proposal{}, "", err
	}
	agentID, ok := s.CoreTargetAgent(target)
	if !ok || agentID != proposal.AgentID {
		return Proposal{}, "", fmt.Errorf("proposal target is no longer an agent core profile file: %s", target)
	}
	if err := ValidateCoreContent(target, proposal.NewContent); err != nil {
		return Proposal{}, "", err
	}
	backupDir, err := s.backupCoreFiles(proposal.AgentID)
	if err != nil {
		return Proposal{}, "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return Proposal{}, "", err
	}
	tmp := target + ".tmp." + proposal.ID
	if err := os.WriteFile(tmp, []byte(proposal.NewContent), 0o644); err != nil {
		return Proposal{}, "", err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return Proposal{}, "", err
	}
	proposal.Status = "promoted"
	proposal.UpdatedAt = time.Now().Format(time.RFC3339Nano)
	if err := s.write(proposal); err != nil {
		return Proposal{}, "", err
	}
	return proposal, backupDir, nil
}

func (s Store) Reject(id, reason string) (Proposal, error) {
	proposal, err := s.Read(id)
	if err != nil {
		return Proposal{}, err
	}
	if proposal.Status != "proposed" {
		return Proposal{}, fmt.Errorf("proposal %s is %s", proposal.ID, proposal.Status)
	}
	proposal.Status = "rejected"
	proposal.Reason = strings.TrimSpace(reason)
	proposal.UpdatedAt = time.Now().Format(time.RFC3339Nano)
	return proposal, s.write(proposal)
}

func (s Store) CoreTargetAgent(path string) (string, bool) {
	workspace := s.workspace()
	clean, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", false
	}
	agentsRoot, err := filepath.Abs(filepath.Join(workspace, "agents"))
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(agentsRoot, clean)
	if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return "", false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 2 || !coreProfileFiles[parts[1]] || strings.TrimSpace(parts[0]) == "" {
		return "", false
	}
	return parts[0], true
}

func ValidateCoreContent(path, content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return fmt.Errorf("agent core profile content is empty")
	}
	if len([]byte(content)) > MaxPromptContextBytes {
		return fmt.Errorf("agent core profile content exceeds %d bytes", MaxPromptContextBytes)
	}
	if err := secret.RejectIfSecretLike(content, path); err != nil {
		return err
	}
	if UnsafePromptContext(content) {
		return fmt.Errorf("agent core profile content contains unsafe prompt control markers")
	}
	return nil
}

func UnsafePromptContext(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range []string{
		"[tool_call]",
		"[/tool_call]",
		"<system>",
		"</system>",
		"role: system",
		"role: assistant",
		"ignore previous instructions",
		"忽略之前",
		"无视之前",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func UnifiedDiff(oldText, newText string) string {
	oldLines := strings.Split(strings.TrimRight(oldText, "\n"), "\n")
	newLines := strings.Split(strings.TrimRight(newText, "\n"), "\n")
	var b strings.Builder
	b.WriteString("--- old\n+++ new\n")
	max := len(oldLines)
	if len(newLines) > max {
		max = len(newLines)
	}
	for i := 0; i < max; i++ {
		var oldLine, newLine string
		if i < len(oldLines) {
			oldLine = oldLines[i]
		}
		if i < len(newLines) {
			newLine = newLines[i]
		}
		switch {
		case i >= len(oldLines):
			b.WriteString("+")
			b.WriteString(newLine)
			b.WriteString("\n")
		case i >= len(newLines):
			b.WriteString("-")
			b.WriteString(oldLine)
			b.WriteString("\n")
		case oldLine == newLine:
			b.WriteString(" ")
			b.WriteString(oldLine)
			b.WriteString("\n")
		default:
			b.WriteString("-")
			b.WriteString(oldLine)
			b.WriteString("\n+")
			b.WriteString(newLine)
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (s Store) backupCoreFiles(agentID string) (string, error) {
	timestamp := time.Now().Format("20060102_150405_000000")
	backupDir := filepath.Join(s.home(), "backups", "agent_profiles", agentID, timestamp)
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return "", err
	}
	agentDir := filepath.Join(s.workspace(), "agents", agentID)
	for name := range coreProfileFiles {
		source := filepath.Join(agentDir, name)
		data, err := os.ReadFile(source)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(backupDir, name), data, 0o644); err != nil {
			return "", err
		}
	}
	return backupDir, nil
}

func (s Store) write(proposal Proposal) error {
	data, err := json.MarshalIndent(proposal, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path(proposal.ID)), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.path(proposal.ID), data, 0o644)
}

func (s Store) path(id string) string {
	return filepath.Join(s.home(), "observe", "agent_profile_proposals", strings.TrimSpace(id)+".json")
}

func (s Store) home() string {
	if strings.TrimSpace(s.Home) != "" {
		return s.Home
	}
	return config.DefaultHome()
}

func (s Store) workspace() string {
	if strings.TrimSpace(s.Workspace) != "" {
		return s.Workspace
	}
	return filepath.Join(s.home(), "workspace")
}
