package schedule

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	ProposalStatusProposed  = "proposed"
	ProposalStatusCommitted = "committed"
	ProposalStatusRejected  = "rejected"
)

type Proposal struct {
	Task           Task     `yaml:",inline" json:"task"`
	ProposedBy     Endpoint `yaml:"proposed_by" json:"proposed_by"`
	ProposalStatus string   `yaml:"proposal_status" json:"proposal_status"`
	Reason         string   `yaml:"reason,omitempty" json:"reason,omitempty"`
}

type ProposalInput struct {
	CreateInput
	Reason string
}

type ProposalItem struct {
	ID        string
	Path      string
	Status    string
	Title     string
	Schedule  string
	UpdatedAt time.Time
}

func (s Store) Propose(input ProposalInput) (Proposal, string, error) {
	if check := CheckDraft(input.CreateInput); !check.Ready {
		return Proposal{}, "", fmt.Errorf("schedule proposal needs more information: %s", check.ClarifyMessage)
	}
	task, err := buildTask(input.CreateInput)
	if err != nil {
		return Proposal{}, "", err
	}
	task.Status = StatusPaused
	proposal := Proposal{
		Task:           task,
		ProposedBy:     task.Owner,
		ProposalStatus: ProposalStatusProposed,
		Reason:         strings.TrimSpace(input.Reason),
	}
	path := s.proposalPath(task.ID)
	if _, err := os.Stat(path); err == nil {
		return Proposal{}, "", fmt.Errorf("schedule proposal already exists: %s", task.ID)
	} else if !os.IsNotExist(err) {
		return Proposal{}, "", err
	}
	if err := writeProposal(path, proposal); err != nil {
		return Proposal{}, "", err
	}
	return proposal, path, nil
}

func (s Store) ListProposals(status string) ([]ProposalItem, error) {
	entries, err := os.ReadDir(s.proposalDir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var items []ProposalItem
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		path := filepath.Join(s.proposalDir(), entry.Name())
		proposal, err := readProposal(path)
		if err != nil {
			return nil, err
		}
		if status != "" && !strings.EqualFold(proposal.ProposalStatus, status) {
			continue
		}
		items = append(items, ProposalItem{
			ID:        proposal.Task.ID,
			Path:      path,
			Status:    proposal.ProposalStatus,
			Title:     proposal.Task.Title,
			Schedule:  Summary(proposal.Task.Schedule),
			UpdatedAt: proposal.Task.UpdatedAt,
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].ID < items[j].ID
	})
	return items, nil
}

func (s Store) ShowProposal(id string) (Proposal, string, error) {
	path := s.proposalPath(id)
	proposal, err := readProposal(path)
	if err != nil {
		return Proposal{}, "", err
	}
	return proposal, path, nil
}

func (s Store) CommitProposal(id string) (Task, string, error) {
	proposal, path, err := s.ShowProposal(id)
	if err != nil {
		return Task{}, "", err
	}
	if proposal.ProposalStatus != ProposalStatusProposed {
		return Task{}, "", fmt.Errorf("only proposed schedule items can be committed")
	}
	task := proposal.Task
	task.Status = StatusActive
	task.UpdatedAt = time.Now()
	if err := validateTask(task); err != nil {
		return Task{}, "", err
	}
	taskPath := s.taskPath(task.ID)
	if _, err := os.Stat(taskPath); err == nil {
		return Task{}, "", fmt.Errorf("schedule task already exists: %s", task.ID)
	} else if !os.IsNotExist(err) {
		return Task{}, "", err
	}
	if err := writeYAML(taskPath, task); err != nil {
		return Task{}, "", err
	}
	proposal.Task = task
	proposal.ProposalStatus = ProposalStatusCommitted
	if err := writeProposal(path, proposal); err != nil {
		return Task{}, "", err
	}
	return task, taskPath, nil
}

func (s Store) RejectProposal(id, reason string) (Proposal, string, error) {
	proposal, path, err := s.ShowProposal(id)
	if err != nil {
		return Proposal{}, "", err
	}
	if proposal.ProposalStatus != ProposalStatusProposed {
		return Proposal{}, "", fmt.Errorf("only proposed schedule items can be rejected")
	}
	proposal.ProposalStatus = ProposalStatusRejected
	proposal.Reason = strings.TrimSpace(reason)
	proposal.Task.UpdatedAt = time.Now()
	if err := writeProposal(path, proposal); err != nil {
		return Proposal{}, "", err
	}
	return proposal, path, nil
}

func (s Store) proposalDir() string {
	return filepath.Join(s.Home, "schedules", "proposals")
}

func (s Store) proposalPath(id string) string {
	return filepath.Join(s.proposalDir(), filepath.Base(strings.TrimSpace(id))+".yaml")
}

func readProposal(path string) (Proposal, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Proposal{}, err
	}
	var proposal Proposal
	if err := yaml.Unmarshal(data, &proposal); err != nil {
		return Proposal{}, err
	}
	if proposal.ProposalStatus == "" {
		proposal.ProposalStatus = ProposalStatusProposed
	}
	if err := validateTask(proposal.Task); err != nil {
		return Proposal{}, fmt.Errorf("%s: %w", path, err)
	}
	return proposal, nil
}

func writeProposal(path string, proposal Proposal) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(proposal)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
