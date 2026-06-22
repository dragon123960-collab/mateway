package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type ProposalStore struct {
	Home       string
	MemoryRoot string
}

type Proposal struct {
	ID           string
	Path         string
	Type         string
	Scope        string
	Title        string
	Status       string
	TopicPath    string
	Subject      string
	Predicate    string
	Object       string
	ValidFrom    string
	ValidUntil   string
	ReviewAfter  string
	Supersedes   []string
	SupersededBy []string
	Sources      []string
	Confidence   string
	CreatedAt    string
	UpdatedAt    string
	Body         string
}

type CreateProposalInput struct {
	Type        string
	Scope       string
	Title       string
	Body        string
	Sources     []string
	Confidence  string
	TopicPath   string
	Subject     string
	Predicate   string
	Object      string
	ValidFrom   string
	ValidUntil  string
	ReviewAfter string
}

func (s ProposalStore) Create(input CreateProposalInput) (Proposal, error) {
	home := strings.TrimSpace(s.Home)
	if home == "" {
		home = ".mateway"
	}
	now := time.Now().Format(time.RFC3339)
	id := "prop_" + time.Now().Format("20060102_150405_000000")
	proposal := Proposal{
		ID:          id,
		Path:        filepath.Join(home, "observe", "proposals", id+".md"),
		Type:        defaultString(input.Type, "experience"),
		Scope:       defaultString(input.Scope, "agent"),
		Title:       strings.TrimSpace(input.Title),
		Status:      "proposed",
		TopicPath:   cleanTopicPath(input.TopicPath),
		Subject:     strings.TrimSpace(input.Subject),
		Predicate:   strings.TrimSpace(input.Predicate),
		Object:      strings.TrimSpace(input.Object),
		ValidFrom:   strings.TrimSpace(input.ValidFrom),
		ValidUntil:  strings.TrimSpace(input.ValidUntil),
		ReviewAfter: strings.TrimSpace(input.ReviewAfter),
		Sources:     cleanStrings(input.Sources),
		Confidence:  defaultString(input.Confidence, "low"),
		CreatedAt:   now,
		UpdatedAt:   now,
		Body:        strings.TrimSpace(input.Body),
	}
	if proposal.Title == "" {
		return Proposal{}, fmt.Errorf("proposal title is required")
	}
	if proposal.Body == "" {
		return Proposal{}, fmt.Errorf("proposal body is required")
	}
	if err := os.MkdirAll(filepath.Dir(proposal.Path), 0o755); err != nil {
		return Proposal{}, err
	}
	if err := os.WriteFile(proposal.Path, []byte(renderProposalMarkdown(proposal)), 0o644); err != nil {
		return Proposal{}, err
	}
	if err := s.writeAudit("proposal_created", proposal, ""); err != nil {
		return Proposal{}, err
	}
	return proposal, nil
}

func (s ProposalStore) List() ([]Proposal, error) {
	dir := filepath.Join(s.Home, "observe", "proposals")
	var proposals []Proposal
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(path), ".md") {
			return nil
		}
		proposal, err := readProposal(path)
		if err != nil {
			return err
		}
		proposals = append(proposals, proposal)
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sort.SliceStable(proposals, func(i, j int) bool {
		return proposals[i].CreatedAt > proposals[j].CreatedAt
	})
	return proposals, nil
}

func (s ProposalStore) Get(id string) (Proposal, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Proposal{}, fmt.Errorf("proposal id is required")
	}
	return readProposal(filepath.Join(s.Home, "observe", "proposals", id+".md"))
}

func (s ProposalStore) Reject(id, reason string) (Proposal, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Proposal{}, fmt.Errorf("proposal id is required")
	}
	path := filepath.Join(s.Home, "observe", "proposals", id+".md")
	proposal, err := readProposal(path)
	if err != nil {
		return Proposal{}, err
	}
	proposal.Status = "rejected"
	proposal.UpdatedAt = time.Now().Format(time.RFC3339)
	if reason = strings.TrimSpace(reason); reason != "" {
		proposal.Body = strings.TrimSpace(proposal.Body + "\n\nRejected reason: " + reason)
	}
	if err := os.WriteFile(path, []byte(renderProposalMarkdown(proposal)), 0o644); err != nil {
		return Proposal{}, err
	}
	if err := s.writeAudit("proposal_rejected", proposal, reason); err != nil {
		return Proposal{}, err
	}
	return proposal, nil
}

func (s ProposalStore) Commit(id string) (Proposal, string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Proposal{}, "", fmt.Errorf("proposal id is required")
	}
	path := filepath.Join(s.Home, "observe", "proposals", id+".md")
	proposal, err := readProposal(path)
	if err != nil {
		return Proposal{}, "", err
	}
	if proposal.Status == "rejected" {
		return Proposal{}, "", fmt.Errorf("rejected proposal cannot be committed")
	}
	memoryRoot := strings.TrimSpace(s.MemoryRoot)
	if memoryRoot == "" {
		memoryRoot = filepath.Join(s.Home, "workspace", "memory")
	}
	target := proposalTargetPath(memoryRoot, proposal)
	active := proposal
	active.Status = "active"
	active.UpdatedAt = time.Now().Format(time.RFC3339)
	superseded, err := s.supersedeMatchingActive(memoryRoot, target, active)
	if err != nil {
		return Proposal{}, "", err
	}
	active.Supersedes = append(active.Supersedes, superseded...)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return Proposal{}, "", err
	}
	if err := os.WriteFile(target, []byte(renderCommittedMemory(active)), 0o644); err != nil {
		return Proposal{}, "", err
	}
	proposal.Status = "archived"
	proposal.UpdatedAt = active.UpdatedAt
	if err := os.WriteFile(path, []byte(renderProposalMarkdown(proposal)), 0o644); err != nil {
		return Proposal{}, "", err
	}
	if err := s.writeAudit("proposal_committed", proposal, target); err != nil {
		return Proposal{}, "", err
	}
	return proposal, target, nil
}

func (s ProposalStore) supersedeMatchingActive(memoryRoot, target string, active Proposal) ([]string, error) {
	if active.TopicPath == "" || active.Subject == "" || active.Predicate == "" {
		return nil, nil
	}
	var superseded []string
	now := time.Now().Format(time.RFC3339)
	err := WalkDocuments(memoryRoot, func(doc Document, issues []Issue) error {
		if len(issues) > 0 || doc.FrontMatter == nil {
			return nil
		}
		if doc.Path == target {
			return nil
		}
		if stringValue(doc.FrontMatter["status"]) != "active" ||
			stringValue(doc.FrontMatter["topic_path"]) != active.TopicPath ||
			stringValue(doc.FrontMatter["subject"]) != active.Subject ||
			stringValue(doc.FrontMatter["predicate"]) != active.Predicate {
			return nil
		}
		doc.FrontMatter["status"] = "superseded"
		doc.FrontMatter["updated_at"] = now
		relTarget, _ := filepath.Rel(memoryRoot, target)
		by := append(stringSlice(doc.FrontMatter["superseded_by"]), filepath.ToSlash(relTarget))
		doc.FrontMatter["superseded_by"] = uniqueStrings(by)
		if err := writeDocument(doc.Path, doc.FrontMatter, doc.Body); err != nil {
			return err
		}
		if rel, err := filepath.Rel(memoryRoot, doc.Path); err == nil {
			superseded = append(superseded, filepath.ToSlash(rel))
		}
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil
	}
	return superseded, err
}

func writeDocument(path string, frontMatter map[string]any, body string) error {
	data, err := yaml.Marshal(frontMatter)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte("---\n"+string(data)+"---\n\n"+strings.TrimSpace(body)+"\n"), 0o644)
}

func uniqueStrings(values []string) []string {
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

func readProposal(path string) (Proposal, error) {
	doc, issues := ReadDocument(path)
	if len(issues) > 0 {
		return Proposal{}, fmt.Errorf("%s: %s", issues[0].Code, issues[0].Message)
	}
	return Proposal{
		ID:           strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
		Path:         path,
		Type:         stringValue(doc.FrontMatter["type"]),
		Scope:        stringValue(doc.FrontMatter["scope"]),
		Title:        stringValue(doc.FrontMatter["title"]),
		Status:       stringValue(doc.FrontMatter["status"]),
		TopicPath:    stringValue(doc.FrontMatter["topic_path"]),
		Subject:      stringValue(doc.FrontMatter["subject"]),
		Predicate:    stringValue(doc.FrontMatter["predicate"]),
		Object:       stringValue(doc.FrontMatter["object"]),
		ValidFrom:    stringValue(doc.FrontMatter["valid_from"]),
		ValidUntil:   stringValue(doc.FrontMatter["valid_until"]),
		ReviewAfter:  stringValue(doc.FrontMatter["review_after"]),
		Supersedes:   stringSlice(doc.FrontMatter["supersedes"]),
		SupersededBy: stringSlice(doc.FrontMatter["superseded_by"]),
		Sources:      stringSlice(doc.FrontMatter["sources"]),
		Confidence:   stringValue(doc.FrontMatter["confidence"]),
		CreatedAt:    stringValue(doc.FrontMatter["created_at"]),
		UpdatedAt:    stringValue(doc.FrontMatter["updated_at"]),
		Body:         doc.Body,
	}, nil
}

func renderProposalMarkdown(proposal Proposal) string {
	fm := map[string]any{
		"type":           proposal.Type,
		"scope":          proposal.Scope,
		"title":          proposal.Title,
		"visibility":     "private",
		"status":         proposal.Status,
		"sources":        proposal.Sources,
		"confidence":     proposal.Confidence,
		"created_at":     proposal.CreatedAt,
		"updated_at":     proposal.UpdatedAt,
		"schema_version": 1,
	}
	addTreeLifecycleFrontMatter(fm, proposal)
	data, _ := yaml.Marshal(fm)
	return "---\n" + string(data) + "---\n\n# " + proposal.Title + "\n\n" + strings.TrimSpace(proposal.Body) + "\n"
}

func renderCommittedMemory(proposal Proposal) string {
	fm := map[string]any{
		"type":           proposal.Type,
		"scope":          proposal.Scope,
		"visibility":     "private",
		"status":         "active",
		"sources":        proposal.Sources,
		"confidence":     proposal.Confidence,
		"created_at":     proposal.CreatedAt,
		"updated_at":     proposal.UpdatedAt,
		"schema_version": 1,
	}
	addTreeLifecycleFrontMatter(fm, proposal)
	data, _ := yaml.Marshal(fm)
	return "---\n" + string(data) + "---\n\n# " + proposal.Title + "\n\n" + strings.TrimSpace(proposal.Body) + "\n"
}

func proposalTargetPath(memoryRoot string, proposal Proposal) string {
	scope := defaultString(proposal.Scope, "agent")
	typ := defaultString(proposal.Type, "experience")
	name := sanitizeProposalFileName(proposal.Title)
	if topic := cleanTopicPath(proposal.TopicPath); topic != "" {
		return filepath.Join(memoryRoot, filepath.FromSlash(topic), name+".md")
	}
	switch scope {
	case "user":
		return filepath.Join(memoryRoot, "user", "long", name+".md")
	case "global":
		return filepath.Join(memoryRoot, "global", typ+"s", name+".md")
	case "project":
		return filepath.Join(memoryRoot, "projects", "general", typ+"s", name+".md")
	case "org":
		return filepath.Join(memoryRoot, "org", "long", name+".md")
	default:
		return filepath.Join(memoryRoot, "agents", "main", typ+"s", name+".md")
	}
}

func addTreeLifecycleFrontMatter(fm map[string]any, proposal Proposal) {
	if proposal.TopicPath != "" {
		fm["topic_path"] = proposal.TopicPath
	}
	if proposal.Subject != "" {
		fm["subject"] = proposal.Subject
	}
	if proposal.Predicate != "" {
		fm["predicate"] = proposal.Predicate
	}
	if proposal.Object != "" {
		fm["object"] = proposal.Object
	}
	if proposal.ValidFrom != "" {
		fm["valid_from"] = proposal.ValidFrom
	}
	if proposal.ValidUntil != "" {
		fm["valid_until"] = proposal.ValidUntil
	}
	if proposal.ReviewAfter != "" {
		fm["review_after"] = proposal.ReviewAfter
	}
	if len(proposal.Supersedes) > 0 {
		fm["supersedes"] = proposal.Supersedes
	}
	if len(proposal.SupersededBy) > 0 {
		fm["superseded_by"] = proposal.SupersededBy
	}
}

func cleanTopicPath(value string) string {
	value = filepath.ToSlash(strings.TrimSpace(value))
	value = strings.Trim(value, "/")
	if value == "." || strings.Contains(value, "..") {
		return ""
	}
	return value
}

func (s ProposalStore) writeAudit(event string, proposal Proposal, reason string) error {
	path := filepath.Join(s.Home, "observe", "audit", "memory.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	payload := map[string]any{
		"type":        event,
		"proposal_id": proposal.ID,
		"path":        proposal.Path,
		"status":      proposal.Status,
		"time":        time.Now().Format(time.RFC3339Nano),
	}
	if reason != "" {
		payload["reason"] = reason
	}
	data, err := json.Marshal(payload)
	if err != nil {
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

func defaultString(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}

func cleanStrings(values []string) []string {
	var out []string
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func sanitizeProposalFileName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "proposal"
	}
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('-')
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		return "proposal"
	}
	return name
}
