package skill

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/agentprofile"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/secret"
)

type ProposalStore struct {
	Home      string
	Workspace string
}

type Proposal struct {
	ID             string   `json:"id"`
	TargetPath     string   `json:"target_path"`
	SkillName      string   `json:"skill_name"`
	Scope          string   `json:"scope"`
	Status         string   `json:"status"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at,omitempty"`
	Reason         string   `json:"reason"`
	Sources        []string `json:"sources"`
	OldContent     string   `json:"old_content"`
	NewContent     string   `json:"new_content"`
	Diff           string   `json:"diff"`
	ModelRole      string   `json:"model_role,omitempty"`
	RejectedReason string   `json:"rejected_reason,omitempty"`
}

type CreateProposalInput struct {
	TargetPath string
	NewContent string
	Reason     string
	Sources    []string
	ModelRole  string
}

func NewProposalStore(cfg *config.Root) ProposalStore {
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
	return ProposalStore{Home: home, Workspace: workspace}
}

func (s ProposalStore) Create(input CreateProposalInput) (Proposal, error) {
	target, err := filepath.Abs(filepath.Clean(strings.TrimSpace(input.TargetPath)))
	if err != nil {
		return Proposal{}, err
	}
	skillName, scope, ok := s.skillTarget(target)
	if !ok {
		return Proposal{}, fmt.Errorf("target is not a workspace skill file: %s", target)
	}
	content := strings.TrimSpace(input.NewContent)
	if err := ValidateSkillContent(target, content); err != nil {
		return Proposal{}, err
	}
	oldData, err := os.ReadFile(target)
	if err != nil {
		return Proposal{}, err
	}
	if strings.TrimSpace(string(oldData)) == content {
		return Proposal{}, fmt.Errorf("skill proposal has no content change")
	}
	now := time.Now().Format(time.RFC3339Nano)
	id := "skillprop_" + time.Now().Format("20060102_150405_000000")
	proposal := Proposal{
		ID:         id,
		TargetPath: target,
		SkillName:  skillName,
		Scope:      scope,
		Status:     "proposed",
		CreatedAt:  now,
		UpdatedAt:  now,
		Reason:     strings.TrimSpace(input.Reason),
		Sources:    cleanStrings(input.Sources),
		OldContent: string(oldData),
		NewContent: content,
		Diff:       agentprofile.UnifiedDiff(string(oldData), content),
		ModelRole:  strings.TrimSpace(input.ModelRole),
	}
	if s.isDuplicate(proposal) {
		return Proposal{}, fmt.Errorf("duplicate skill proposal for %s", target)
	}
	if err := os.MkdirAll(filepath.Dir(s.path(id)), 0o755); err != nil {
		return Proposal{}, err
	}
	if err := s.write(proposal); err != nil {
		return Proposal{}, err
	}
	_ = s.writeAudit("skill_proposal_created", map[string]any{"proposal_id": proposal.ID, "target_path": proposal.TargetPath, "skill_name": proposal.SkillName})
	return proposal, nil
}

func (s ProposalStore) List() ([]Proposal, error) {
	dir := filepath.Join(s.home(), "observe", "skill_proposals")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Proposal
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		proposal, err := s.Read(strings.TrimSuffix(entry.Name(), ".json"))
		if err == nil {
			out = append(out, proposal)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return out, nil
}

func (s ProposalStore) Read(id string) (Proposal, error) {
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

func (s ProposalStore) Promote(id string) (Proposal, string, error) {
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
	if _, _, ok := s.skillTarget(target); !ok {
		return Proposal{}, "", fmt.Errorf("proposal target is no longer a workspace skill file: %s", target)
	}
	if err := ValidateSkillContent(target, proposal.NewContent); err != nil {
		return Proposal{}, "", err
	}
	backupDir, err := s.backupSkill(target)
	if err != nil {
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
	_ = s.writeAudit("skill_proposal_promoted", map[string]any{"proposal_id": proposal.ID, "target_path": target, "backup": backupDir})
	return proposal, backupDir, nil
}

func (s ProposalStore) Reject(id, reason string) (Proposal, error) {
	proposal, err := s.Read(id)
	if err != nil {
		return Proposal{}, err
	}
	if proposal.Status != "proposed" {
		return Proposal{}, fmt.Errorf("proposal %s is %s", proposal.ID, proposal.Status)
	}
	proposal.Status = "rejected"
	proposal.RejectedReason = strings.TrimSpace(reason)
	proposal.UpdatedAt = time.Now().Format(time.RFC3339Nano)
	if err := s.write(proposal); err != nil {
		return Proposal{}, err
	}
	_ = s.writeAudit("skill_proposal_rejected", map[string]any{"proposal_id": proposal.ID, "reason": proposal.RejectedReason})
	return proposal, nil
}

func ValidateSkillContent(path, content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return fmt.Errorf("skill content is empty")
	}
	if err := secret.RejectIfSecretLike(content, path); err != nil {
		return err
	}
	if agentprofile.UnsafePromptContext(content) {
		return fmt.Errorf("skill content contains unsafe prompt control markers")
	}
	return nil
}

func (s ProposalStore) skillTarget(path string) (string, string, bool) {
	workspace, err := filepath.Abs(filepath.Clean(s.workspace()))
	if err != nil {
		return "", "", false
	}
	clean, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", "", false
	}
	sharedRoot := filepath.Join(workspace, "skills")
	if rel, err := filepath.Rel(sharedRoot, clean); err == nil && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) == 2 && parts[1] == "SKILL.md" && strings.TrimSpace(parts[0]) != "" {
			return parts[0], "shared", true
		}
	}
	agentRoot := filepath.Join(workspace, "agents")
	if rel, err := filepath.Rel(agentRoot, clean); err == nil && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) == 4 && parts[1] == "skills" && parts[3] == "SKILL.md" && strings.TrimSpace(parts[2]) != "" {
			return parts[2], "agent", true
		}
	}
	return "", "", false
}

func (s ProposalStore) backupSkill(target string) (string, error) {
	data, err := os.ReadFile(target)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(s.home(), "observe", "backups", "skills", time.Now().Format("20060102_150405_000000"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	backup := filepath.Join(dir, filepath.Base(filepath.Dir(target))+"-SKILL.md")
	return dir, os.WriteFile(backup, data, 0o644)
}

func (s ProposalStore) isDuplicate(candidate Proposal) bool {
	proposals, err := s.List()
	if err != nil {
		return false
	}
	for _, existing := range proposals {
		if existing.Status != "proposed" || existing.TargetPath != candidate.TargetPath {
			continue
		}
		if sharesAny(existing.Sources, candidate.Sources) || existing.Diff == candidate.Diff {
			return true
		}
	}
	return false
}

func (s ProposalStore) write(proposal Proposal) error {
	data, err := json.MarshalIndent(proposal, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path(proposal.ID)), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.path(proposal.ID), append(data, '\n'), 0o644)
}

func (s ProposalStore) path(id string) string {
	return filepath.Join(s.home(), "observe", "skill_proposals", strings.TrimSpace(id)+".json")
}

func (s ProposalStore) home() string {
	if strings.TrimSpace(s.Home) != "" {
		return strings.TrimSpace(s.Home)
	}
	return config.DefaultHome()
}

func (s ProposalStore) workspace() string {
	if strings.TrimSpace(s.Workspace) != "" {
		return strings.TrimSpace(s.Workspace)
	}
	return filepath.Join(s.home(), "workspace")
}

func (s ProposalStore) writeAudit(event string, fields map[string]any) error {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["type"] = event
	fields["time"] = time.Now().Format(time.RFC3339Nano)
	data, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	path := filepath.Join(s.home(), "observe", "audit", "skills.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(data, '\n'))
	return err
}

func cleanStrings(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func sharesAny(left, right []string) bool {
	seen := map[string]bool{}
	for _, value := range left {
		seen[strings.TrimSpace(value)] = true
	}
	for _, value := range right {
		if seen[strings.TrimSpace(value)] {
			return true
		}
	}
	return false
}
