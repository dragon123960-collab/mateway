package agentprofile

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/agenttemplate"
	"github.com/dongping/mateway/internal/config"
	"gopkg.in/yaml.v3"
)

type Manager struct {
	Config *config.Root
}

type AgentReport struct {
	ID           string
	Name         string
	Default      bool
	SessionNS    string
	AgentDir     string
	MemoryRoot   string
	ModelDefault string
	Skills       int
	PromptFiles  []FileStatus
	Bindings     []config.AgentBindingConfig
	Issues       []Issue
}

type FileStatus struct {
	Path   string
	Exists bool
	Bytes  int64
}

type Issue struct {
	Severity string
	Code     string
	Message  string
}

type CreateAgentInput struct {
	ID         string
	Name       string
	SetDefault bool
}

type BindInput struct {
	Channel   string
	AccountID string
	PeerID    string
	AgentID   string
}

func (m Manager) List() []config.AgentProfileConfig {
	if m.Config == nil {
		return nil
	}
	m.Config.NormalizeForUse()
	out := append([]config.AgentProfileConfig(nil), m.Config.Agents.Profiles...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (m Manager) Report(agentID string) (AgentReport, error) {
	profile, ok := m.profile(agentID)
	if !ok {
		return AgentReport{}, fmt.Errorf("agent %q not found", agentID)
	}
	workspace := workspace(m.Config)
	agentDir := profile.AgentDir
	if strings.TrimSpace(agentDir) == "" {
		agentDir = filepath.Join(workspace, "agents", profile.ID)
	}
	report := AgentReport{
		ID:           profile.ID,
		Name:         profile.Name,
		Default:      profile.Default,
		SessionNS:    profile.SessionNamespace,
		AgentDir:     agentDir,
		MemoryRoot:   filepath.Join(workspace, "memory", "agents", profile.ID),
		ModelDefault: profile.Model.Default,
		Bindings:     bindingsFor(m.Config, profile.ID),
	}
	for _, name := range CoreProfileFileNames() {
		report.PromptFiles = append(report.PromptFiles, statFile(filepath.Join(agentDir, name)))
	}
	report.Skills = countSkillDirs(filepath.Join(agentDir, "skills"))
	report.Issues = lintReport(report, m.Config)
	return report, nil
}

func (m Manager) Create(input CreateAgentInput) (config.AgentProfileConfig, error) {
	if m.Config == nil {
		return config.AgentProfileConfig{}, fmt.Errorf("config is required")
	}
	m.Config.NormalizeForUse()
	id := sanitizeAgentID(input.ID)
	if id == "" {
		return config.AgentProfileConfig{}, fmt.Errorf("agent id is required")
	}
	if _, ok := m.profile(id); ok {
		return config.AgentProfileConfig{}, fmt.Errorf("agent %q already exists", id)
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = id
	}
	profile := config.AgentProfileConfig{
		ID:               id,
		Name:             name,
		Default:          input.SetDefault,
		SessionNamespace: id,
		Model:            m.Config.Model,
		Heartbeat:        defaultHeartbeat(m.Config),
	}
	if input.SetDefault {
		for i := range m.Config.Agents.Profiles {
			m.Config.Agents.Profiles[i].Default = false
		}
		m.Config.Agents.Default = id
	}
	m.Config.Agents.Profiles = append(m.Config.Agents.Profiles, profile)
	if err := ensureAgentFiles(workspace(m.Config), profile); err != nil {
		return config.AgentProfileConfig{}, err
	}
	if err := SaveConfig(m.Config); err != nil {
		return config.AgentProfileConfig{}, err
	}
	return profile, nil
}

func (m Manager) Bind(input BindInput) (config.AgentBindingConfig, error) {
	if m.Config == nil {
		return config.AgentBindingConfig{}, fmt.Errorf("config is required")
	}
	m.Config.NormalizeForUse()
	if _, ok := m.profile(input.AgentID); !ok {
		return config.AgentBindingConfig{}, fmt.Errorf("agent %q not found", input.AgentID)
	}
	binding := config.AgentBindingConfig{
		Channel:   strings.TrimSpace(input.Channel),
		AccountID: strings.TrimSpace(input.AccountID),
		PeerID:    strings.TrimSpace(input.PeerID),
		AgentID:   strings.TrimSpace(input.AgentID),
	}
	if binding.Channel == "" || binding.AgentID == "" {
		return config.AgentBindingConfig{}, fmt.Errorf("channel and agent id are required")
	}
	replaced := false
	for i, existing := range m.Config.Agents.Bindings {
		if sameBindingKey(existing, binding) {
			m.Config.Agents.Bindings[i] = binding
			replaced = true
			break
		}
	}
	if !replaced {
		m.Config.Agents.Bindings = append(m.Config.Agents.Bindings, binding)
	}
	return binding, SaveConfig(m.Config)
}

func (m Manager) Unbind(input BindInput) (bool, error) {
	if m.Config == nil {
		return false, fmt.Errorf("config is required")
	}
	var out []config.AgentBindingConfig
	removed := false
	key := config.AgentBindingConfig{Channel: strings.TrimSpace(input.Channel), AccountID: strings.TrimSpace(input.AccountID), PeerID: strings.TrimSpace(input.PeerID)}
	for _, binding := range m.Config.Agents.Bindings {
		if sameBindingKey(binding, key) {
			removed = true
			continue
		}
		out = append(out, binding)
	}
	m.Config.Agents.Bindings = out
	if removed {
		return true, SaveConfig(m.Config)
	}
	return false, nil
}

func SaveConfig(cfg *config.Root) error {
	if cfg == nil {
		return fmt.Errorf("config is required")
	}
	path := filepath.Join(cfg.App.Home, "config", "config.yaml")
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func (m Manager) profile(agentID string) (config.AgentProfileConfig, bool) {
	if m.Config == nil {
		return config.AgentProfileConfig{}, false
	}
	m.Config.NormalizeForUse()
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		agentID = m.Config.Agents.Default
	}
	for _, profile := range m.Config.Agents.Profiles {
		if strings.EqualFold(profile.ID, agentID) {
			return profile, true
		}
	}
	return config.AgentProfileConfig{}, false
}

func workspace(cfg *config.Root) string {
	if cfg != nil && strings.TrimSpace(cfg.App.Workspace) != "" {
		return strings.TrimSpace(cfg.App.Workspace)
	}
	if cfg != nil && strings.TrimSpace(cfg.App.Home) != "" {
		return filepath.Join(cfg.App.Home, "workspace")
	}
	return filepath.Join(config.DefaultHome(), "workspace")
}

func defaultHeartbeat(cfg *config.Root) config.HeartbeatConfig {
	if cfg == nil {
		return config.HeartbeatConfig{Enabled: false, Interval: "30m", Jobs: []string{"memory_lint", "memory_index_rebuild", "memory_distill", "learning_distill", "skill_learning", "memory_lifecycle"}}
	}
	for _, profile := range cfg.Agents.Profiles {
		if profile.Default || strings.EqualFold(profile.ID, cfg.Agents.Default) {
			return profile.Heartbeat
		}
	}
	return config.HeartbeatConfig{Enabled: false, Interval: "30m", Jobs: []string{"memory_lint", "memory_index_rebuild", "memory_distill", "learning_distill", "skill_learning", "memory_lifecycle"}}
}

func ensureAgentFiles(workspace string, profile config.AgentProfileConfig) error {
	agentDir := filepath.Join(workspace, "agents", profile.ID)
	files := agenttemplate.CoreFiles(agenttemplate.Profile{ID: profile.ID, Name: profile.Name})
	for name, content := range files {
		path := filepath.Join(agentDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Join(agentDir, "skills"), 0o755); err != nil {
		return err
	}
	memDir := filepath.Join(workspace, "memory", "agents", profile.ID)
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		return err
	}
	index := filepath.Join(memDir, "memory.md")
	_, err := os.Stat(index)
	if os.IsNotExist(err) {
		return os.WriteFile(index, []byte(memoryEntryTemplate(profile)), 0o644)
	}
	return err
}

func memoryEntryTemplate(profile config.AgentProfileConfig) string {
	id := strings.TrimSpace(profile.ID)
	if id == "" {
		id = "agent"
	}
	name := strings.TrimSpace(profile.Name)
	if name == "" {
		name = id
	}
	now := time.Now().UTC().Format("2006-01-02")
	return fmt.Sprintf(`---
type: wiki
scope: agent
owner_agent: %s
project_id:
visibility: private
status: proposed
tags: []
aliases: []
op_fingerprint:
sources: []
confidence: low
created_at: %s
updated_at: %s
review_after:
schema_version: 1
---

# %s Memory

Long-term memory index for this agent.
`, id, now, now, name)
}

func statFile(path string) FileStatus {
	status := FileStatus{Path: path}
	info, err := os.Stat(path)
	if err == nil && !info.IsDir() {
		status.Exists = true
		status.Bytes = info.Size()
	}
	return status
}

func countSkillDirs(path string) int {
	entries, err := os.ReadDir(path)
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			if _, err := os.Stat(filepath.Join(path, entry.Name(), "SKILL.md")); err == nil {
				count++
			}
		}
	}
	return count
}

func bindingsFor(cfg *config.Root, agentID string) []config.AgentBindingConfig {
	var out []config.AgentBindingConfig
	if cfg == nil {
		return out
	}
	for _, binding := range cfg.Agents.Bindings {
		if strings.EqualFold(binding.AgentID, agentID) {
			out = append(out, binding)
		}
	}
	return out
}

func lintReport(report AgentReport, cfg *config.Root) []Issue {
	var issues []Issue
	for _, file := range report.PromptFiles {
		if !file.Exists {
			severity := "error"
			if filepath.Base(file.Path) == "soul.md" {
				severity = "warning"
			}
			issues = append(issues, Issue{Severity: severity, Code: "missing_prompt_file", Message: file.Path})
		}
	}
	if strings.TrimSpace(report.ModelDefault) == "" {
		issues = append(issues, Issue{Severity: "warning", Code: "missing_model_default", Message: "agent model default is empty"})
	}
	if _, err := os.Stat(report.MemoryRoot); err != nil {
		issues = append(issues, Issue{Severity: "warning", Code: "missing_memory_root", Message: report.MemoryRoot})
	}
	if report.Default && cfg != nil && !strings.EqualFold(cfg.Agents.Default, report.ID) {
		issues = append(issues, Issue{Severity: "warning", Code: "default_mismatch", Message: "profile default flag differs from agents.default"})
	}
	return issues
}

func sanitizeAgentID(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	var b strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "-_")
}

func sameBindingKey(left, right config.AgentBindingConfig) bool {
	return strings.EqualFold(strings.TrimSpace(left.Channel), strings.TrimSpace(right.Channel)) &&
		strings.EqualFold(strings.TrimSpace(left.AccountID), strings.TrimSpace(right.AccountID)) &&
		strings.EqualFold(strings.TrimSpace(left.PeerID), strings.TrimSpace(right.PeerID))
}
