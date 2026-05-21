package memory

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type LearningConfig struct {
	Enabled            bool
	SuccessThreshold   int
	RequireUserConfirm bool
}

type TaskOutcome struct {
	AgentID        string
	TraceID        string
	TaskID         string
	Intent         string
	PlanSummary    string
	Tools          []string
	SelectedSkills []string
	Success        bool
	AwaitConfirm   bool
	AwaitUserInput bool
	Failed         bool
	Artifacts      []Artifact
	ReplyPreview   string
	FinishedAt     time.Time
}

type Artifact struct {
	Kind      string `json:"kind"`
	Path      string `json:"path,omitempty"`
	Label     string `json:"label,omitempty"`
	SourceURL string `json:"source_url,omitempty"`
	Summary   string `json:"summary,omitempty"`
}

type PatternRecord struct {
	PatternKey     string     `json:"pattern_key"`
	TaskID         string     `json:"task_id"`
	TraceID        string     `json:"trace_id"`
	Intent         string     `json:"intent"`
	PlanSummary    string     `json:"plan_summary"`
	Tools          []string   `json:"tools"`
	SelectedSkills []string   `json:"selected_skills,omitempty"`
	Success        bool       `json:"success"`
	Artifacts      []Artifact `json:"artifacts,omitempty"`
	ReplyPreview   string     `json:"reply_preview,omitempty"`
	FinishedAt     time.Time  `json:"finished_at"`
}

type Counter struct {
	PatternKey         string    `json:"pattern_key"`
	IntentFamily       string    `json:"intent_family"`
	Tools              []string  `json:"tools"`
	SuccessCount       int       `json:"success_count"`
	FailureCount       int       `json:"failure_count"`
	CandidateGenerated bool      `json:"candidate_generated"`
	LastTaskID         string    `json:"last_task_id,omitempty"`
	LastTraceID        string    `json:"last_trace_id,omitempty"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type ProcessResult struct {
	PatternKey         string
	SuccessCount       int
	CandidateGenerated bool
	CandidatePath      string
}

type Store struct {
	Root string
}

func NewStore(workspace string) Store {
	return Store{Root: filepath.Join(workspace, "memory")}
}

func (s Store) ProcessTask(outcome TaskOutcome, cfg LearningConfig) (ProcessResult, error) {
	if !cfg.Enabled || !outcome.Success || outcome.Failed || outcome.AwaitConfirm || outcome.AwaitUserInput {
		return ProcessResult{}, nil
	}
	if strings.TrimSpace(outcome.AgentID) == "" {
		outcome.AgentID = "main"
	}
	if outcome.FinishedAt.IsZero() {
		outcome.FinishedAt = time.Now()
	}
	threshold := cfg.SuccessThreshold
	if threshold <= 0 {
		threshold = 3
	}
	key := PatternKey(outcome)
	record := PatternRecord{
		PatternKey:     key,
		TaskID:         outcome.TaskID,
		TraceID:        outcome.TraceID,
		Intent:         strings.TrimSpace(outcome.Intent),
		PlanSummary:    strings.TrimSpace(outcome.PlanSummary),
		Tools:          stableStrings(outcome.Tools),
		SelectedSkills: stableStrings(outcome.SelectedSkills),
		Success:        true,
		Artifacts:      outcome.Artifacts,
		ReplyPreview:   strings.TrimSpace(outcome.ReplyPreview),
		FinishedAt:     outcome.FinishedAt,
	}
	root := s.agentLearningRoot(outcome.AgentID)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return ProcessResult{}, err
	}
	if err := appendJSONL(filepath.Join(root, "patterns.jsonl"), record); err != nil {
		return ProcessResult{}, err
	}
	counters, err := readCounters(filepath.Join(root, "counters.json"))
	if err != nil {
		return ProcessResult{}, err
	}
	counter := counters[key]
	counter.PatternKey = key
	counter.IntentFamily = intentFamily(outcome.Intent, outcome.PlanSummary)
	counter.Tools = stableStrings(outcome.Tools)
	counter.SuccessCount++
	counter.LastTaskID = outcome.TaskID
	counter.LastTraceID = outcome.TraceID
	counter.UpdatedAt = outcome.FinishedAt
	counters[key] = counter
	if err := writeCounters(filepath.Join(root, "counters.json"), counters); err != nil {
		return ProcessResult{}, err
	}
	result := ProcessResult{PatternKey: key, SuccessCount: counter.SuccessCount}
	if counter.SuccessCount >= threshold && !counter.CandidateGenerated {
		path, err := s.writeSkillCandidate(outcome.AgentID, record, counter, cfg)
		if err != nil {
			return result, err
		}
		counter.CandidateGenerated = true
		counters[key] = counter
		if err := writeCounters(filepath.Join(root, "counters.json"), counters); err != nil {
			return result, err
		}
		result.CandidateGenerated = true
		result.CandidatePath = path
	}
	return result, nil
}

func PatternKey(outcome TaskOutcome) string {
	family := intentFamily(outcome.Intent, outcome.PlanSummary)
	tools := strings.Join(stableStrings(outcome.Tools), ">")
	raw := family + "|" + tools + "|risk:derived"
	sum := sha1.Sum([]byte(raw))
	return family + "-" + hex.EncodeToString(sum[:])[:10]
}

func intentFamily(values ...string) string {
	text := strings.ToLower(strings.TrimSpace(strings.Join(values, " ")))
	if text == "" {
		return "general-task"
	}
	re := regexp.MustCompile(`[^a-z0-9]+`)
	slug := strings.Trim(re.ReplaceAllString(text, "-"), "-")
	if slug == "" {
		return "general-task"
	}
	parts := strings.Split(slug, "-")
	if len(parts) > 8 {
		parts = parts[:8]
	}
	return strings.Join(parts, "-")
}

func (s Store) writeSkillCandidate(agentID string, record PatternRecord, counter Counter, cfg LearningConfig) (string, error) {
	inbox := filepath.Join(s.Root, "agents", agentID, "inbox")
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		return "", err
	}
	name := "skill-candidate-" + counter.PatternKey + ".md"
	path := filepath.Join(inbox, name)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	text := renderSkillCandidate(record, counter, cfg)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func renderSkillCandidate(record PatternRecord, counter Counter, cfg LearningConfig) string {
	now := time.Now().Format("2006-01-02")
	confirm := "true"
	if !cfg.RequireUserConfirm {
		confirm = "false"
	}
	return fmt.Sprintf(`---
type: skill_candidate
scope: agent
status: proposed
tags: [skill-candidate, learning]
sources:
  - task:%s
  - trace:%s
confidence: medium
success_count: %d
requires_user_confirm: %s
created_at: %s
updated_at: %s
---

# Proposed Skill: %s

## Why This Was Proposed

This workflow pattern has completed successfully %d times.

## Observed Pattern

- Intent family: %s
- Tool sequence: %s
- Last task: %s
- Last trace: %s

## Draft Instructions

Use this candidate as a starting point. Review the source tasks, remove accidental details, and promote it to `+"`workspace/skills/<skill-name>/SKILL.md`"+` only after user approval.

## Evidence

- Last plan summary: %s
- Last reply preview: %s
`, record.TaskID, record.TraceID, counter.SuccessCount, confirm, now, now, titleFromFamily(counter.IntentFamily), counter.SuccessCount, counter.IntentFamily, strings.Join(counter.Tools, " -> "), counter.LastTaskID, counter.LastTraceID, record.PlanSummary, record.ReplyPreview)
}

func titleFromFamily(family string) string {
	parts := strings.Split(strings.ReplaceAll(family, "-", " "), " ")
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}

func appendJSONL(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

func readCounters(path string) (map[string]Counter, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]Counter{}, nil
	}
	if err != nil {
		return nil, err
	}
	var counters map[string]Counter
	if err := json.Unmarshal(data, &counters); err != nil {
		return nil, err
	}
	if counters == nil {
		counters = map[string]Counter{}
	}
	return counters, nil
}

func writeCounters(path string, counters map[string]Counter) error {
	data, err := json.MarshalIndent(counters, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func stableStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	sort.Strings(out)
	return out
}

func (s Store) agentLearningRoot(agentID string) string {
	return filepath.Join(s.Root, "agents", agentID, "learning")
}
