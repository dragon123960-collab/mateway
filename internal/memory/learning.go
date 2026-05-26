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
	Channel        string
	SessionKey     string
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

type SkillImprovementInput struct {
	AgentID             string
	SkillName           string
	ImprovementType     string
	Reason              string
	ProposedChange      string
	RepairReason        string
	PreviousPlanSummary string
	RepairedPlanSummary string
	TaskID              string
	TraceID             string
	Sources             []string
}

type Store struct {
	Root string
}

type SkillPromotionInput struct {
	AgentID   string
	Proposal  string
	SkillName string
	At        time.Time
}

type SkillPromotionResult struct {
	SourcePath string
	TargetPath string
	SkillName  string
}

func NewStore(workspace string) Store {
	return Store{Root: filepath.Join(workspace, "memory")}
}

func (s Store) ProcessTask(outcome TaskOutcome, cfg LearningConfig) (ProcessResult, error) {
	if !cfg.Enabled || !outcome.Success || outcome.Failed || outcome.AwaitConfirm || outcome.AwaitUserInput {
		return ProcessResult{}, nil
	}
	if shouldSkipLearningOutcome(outcome) {
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

func (s Store) WriteSkillImprovementProposal(in SkillImprovementInput) (string, error) {
	agentID := firstNonEmptyMemory(in.AgentID, "main")
	skillName := strings.TrimSpace(in.SkillName)
	improvementType := normalizeImprovementType(in.ImprovementType)
	reason := strings.TrimSpace(in.Reason)
	change := strings.TrimSpace(in.ProposedChange)
	if skillName == "" {
		return "", fmt.Errorf("skill name is required")
	}
	if improvementType == "" {
		return "", fmt.Errorf("improvement type is required")
	}
	if reason == "" {
		return "", fmt.Errorf("improvement reason is required")
	}
	if change == "" {
		return "", fmt.Errorf("proposed change is required")
	}
	inbox := filepath.Join(s.Root, "agents", agentID, "inbox")
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		return "", err
	}
	if existing, ok, err := s.findExistingSkillImprovementProposal(agentID, SkillImprovementInput{
		SkillName:           skillName,
		ImprovementType:     improvementType,
		RepairReason:        in.RepairReason,
		PreviousPlanSummary: in.PreviousPlanSummary,
		RepairedPlanSummary: in.RepairedPlanSummary,
	}); err != nil {
		return "", err
	} else if ok {
		return existing, nil
	}
	name := "skill-improvement-" + slugForSkillImprovement(skillName) + "-" + time.Now().Format("20060102-150405") + ".md"
	path := filepath.Join(inbox, name)
	text := renderSkillImprovementProposal(in)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func (s Store) PromoteSkillCandidate(in SkillPromotionInput) (SkillPromotionResult, error) {
	agentID := firstNonEmptyMemory(in.AgentID, "main")
	sourcePath := s.resolveProposalPath(agentID, in.Proposal)
	if strings.TrimSpace(sourcePath) == "" {
		return SkillPromotionResult{}, fmt.Errorf("skill candidate path is required")
	}
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return SkillPromotionResult{}, err
	}
	text := string(data)
	if !strings.Contains(text, "type: skill_candidate") || !strings.Contains(text, "status: proposed") {
		return SkillPromotionResult{}, fmt.Errorf("skill candidate must have frontmatter with type: skill_candidate and status: proposed")
	}
	skillName := normalizeSkillCandidateDirName(firstNonEmptyLearning(in.SkillName, titleFromMarkdown(text)))
	if skillName == "" {
		return SkillPromotionResult{}, fmt.Errorf("skill name is required")
	}
	workspace := filepath.Dir(s.Root)
	targetDir := filepath.Join(workspace, "skills", skillName)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return SkillPromotionResult{}, err
	}
	targetPath := filepath.Join(targetDir, "SKILL.md")
	now := in.At
	if now.IsZero() {
		now = time.Now()
	}
	skillText := renderPromotedSkillCandidate(text, skillName)
	if err := os.WriteFile(targetPath, []byte(skillText), 0o644); err != nil {
		return SkillPromotionResult{}, err
	}
	updated := replaceFrontmatterLine(text, "status", "committed")
	updated = replaceFrontmatterLine(updated, "updated_at", now.Format("2006-01-02"))
	updated = strings.TrimSpace(updated) + "\n\nPromoted to: [[" + filepath.ToSlash(filepath.Join("skills", skillName, "SKILL")) + "]]\n"
	if err := os.WriteFile(sourcePath, []byte(updated), 0o644); err != nil {
		return SkillPromotionResult{}, err
	}
	return SkillPromotionResult{SourcePath: sourcePath, TargetPath: targetPath, SkillName: skillName}, nil
}

func shouldSkipLearningOutcome(outcome TaskOutcome) bool {
	fields := []string{
		outcome.Channel,
		outcome.SessionKey,
		outcome.TraceID,
		outcome.TaskID,
	}
	for _, field := range fields {
		if looksLikeTestIdentifier(field) {
			return true
		}
	}
	return false
}

func looksLikeTestIdentifier(value string) bool {
	text := strings.ToLower(strings.TrimSpace(value))
	if text == "" {
		return false
	}
	markers := []string{
		"test:",
		"test-",
		"-test",
		"cli-test",
		"feishu-test",
		"schedule:test",
		"eval",
	}
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
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
	if existing, ok, err := s.findExistingSkillCandidate(agentID, record, counter); err != nil {
		return "", err
	} else if ok {
		if err := upgradeSkillCandidate(existing, record, counter); err != nil {
			return "", err
		}
		return existing, nil
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

func (s Store) findExistingSkillCandidate(agentID string, record PatternRecord, counter Counter) (string, bool, error) {
	items, err := s.List(ListOptions{
		AgentID: agentID,
		Area:    "inbox",
		Status:  "proposed",
		Kind:    "skill_candidate",
	})
	if err != nil {
		return "", false, err
	}
	if len(items) == 0 {
		return "", false, nil
	}
	for _, item := range items {
		data, err := os.ReadFile(item.Path)
		if err != nil {
			return "", false, err
		}
		if skillCandidateLooksDuplicate(string(data), record, counter) {
			return item.Path, true, nil
		}
	}
	return "", false, nil
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

func renderPromotedSkillCandidate(text, skillName string) string {
	parsed, err := parseMarkdown(text)
	body := text
	if err == nil && parsed.Body != "" {
		body = parsed.Body
	}
	title := skillName
	description := "Promoted from a reviewed skill candidate. Refine this workflow before broader reuse."
	whenContains := skillWhenContainsFromTitle(title, skillName)
	instruction := extractSkillCandidateDraftInstructions(body)
	body = stripFirstMarkdownHeading(strings.TrimSpace(body))
	var b strings.Builder
	fmt.Fprintln(&b, "---")
	fmt.Fprintf(&b, "name: %s\n", skillName)
	fmt.Fprintf(&b, "description: %s\n", description)
	fmt.Fprintln(&b, "stage: planning")
	fmt.Fprintln(&b, "priority: 5")
	if len(whenContains) > 0 {
		fmt.Fprintf(&b, "when_contains: [%s]\n", strings.Join(whenContains, ", "))
	}
	fmt.Fprintln(&b, "---")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "# %s\n\n", title)
	fmt.Fprintln(&b, "## Workflow")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, instruction)
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Source Candidate Notes")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, strings.TrimSpace(body))
	return strings.TrimSpace(b.String()) + "\n"
}

func extractSkillCandidateDraftInstructions(body string) string {
	lines := strings.Split(strings.TrimSpace(body), "\n")
	inDraft := false
	var draft []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.EqualFold(trimmed, "## Draft Instructions") {
			inDraft = true
			continue
		}
		if strings.HasPrefix(trimmed, "## ") && inDraft {
			break
		}
		if inDraft && trimmed != "" {
			draft = append(draft, trimmed)
		}
	}
	if len(draft) == 0 {
		return "Review the source candidate notes below, turn them into concise executable instructions, and keep only the stable reusable workflow."
	}
	return strings.Join(draft, "\n")
}

func skillWhenContainsFromTitle(title, skillName string) []string {
	normalized := normalizeSkillImprovementText(title + " " + skillName)
	parts := strings.Fields(normalized)
	seen := map[string]struct{}{}
	var out []string
	for _, part := range parts {
		if len(part) < 3 || part == "proposed" || part == "skill" {
			continue
		}
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		out = append(out, part)
		if len(out) >= 6 {
			break
		}
	}
	return out
}

func stripFirstMarkdownHeading(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) == 0 {
		return text
	}
	if strings.HasPrefix(strings.TrimSpace(lines[0]), "# ") {
		lines = lines[1:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func upgradeSkillCandidate(path string, record PatternRecord, counter Counter) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(data)
	now := time.Now().Format("2006-01-02")
	text = replaceFrontmatterLine(text, "success_count", fmt.Sprint(counter.SuccessCount))
	text = replaceFrontmatterLine(text, "updated_at", now)
	text = replaceSectionBullet(text, "## Observed Pattern", "Last task", counter.LastTaskID)
	text = replaceSectionBullet(text, "## Observed Pattern", "Last trace", counter.LastTraceID)
	text = replaceSectionBullet(text, "## Evidence", "Last plan summary", record.PlanSummary)
	text = replaceSectionBullet(text, "## Evidence", "Last reply preview", record.ReplyPreview)
	return os.WriteFile(path, []byte(text), 0o644)
}

func replaceSectionBullet(text, section, label, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return text
	}
	lines := strings.Split(text, "\n")
	inSection := false
	prefix := "- " + strings.TrimSpace(label) + ":"
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			inSection = trimmed == strings.TrimSpace(section)
			continue
		}
		if !inSection {
			continue
		}
		if strings.HasPrefix(trimmed, prefix) {
			lines[i] = prefix + " " + value
			return strings.Join(lines, "\n")
		}
	}
	return text
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

func skillCandidateLooksDuplicate(text string, record PatternRecord, counter Counter) bool {
	normalizedText := normalizeSkillImprovementText(text)
	intent := normalizeSkillImprovementText(counter.IntentFamily)
	tools := normalizeSkillImprovementText(strings.Join(counter.Tools, " -> "))
	plan := normalizeSkillImprovementText(record.PlanSummary)
	if intent == "" || tools == "" {
		return false
	}
	if !containsNormalizedSegment(normalizedText, intent) {
		return false
	}
	if !containsNormalizedSegment(normalizedText, tools) {
		return false
	}
	if plan != "" && !containsNormalizedSegment(normalizedText, plan) {
		return false
	}
	return true
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

func (s Store) findExistingSkillImprovementProposal(agentID string, in SkillImprovementInput) (string, bool, error) {
	items, err := s.List(ListOptions{
		AgentID: agentID,
		Area:    "inbox",
		Status:  "proposed",
		Kind:    "skill_improvement",
	})
	if err != nil {
		return "", false, err
	}
	if len(items) == 0 {
		return "", false, nil
	}
	for _, item := range items {
		data, err := os.ReadFile(item.Path)
		if err != nil {
			return "", false, err
		}
		text := string(data)
		if skillImprovementLooksDuplicate(text, in) {
			return item.Path, true, nil
		}
	}
	return "", false, nil
}

func skillImprovementLooksDuplicate(text string, in SkillImprovementInput) bool {
	normalizedSkill := strings.ToLower(strings.TrimSpace(in.SkillName))
	if normalizedSkill == "" {
		return false
	}
	normalizedText := normalizeSkillImprovementText(text)
	if !strings.Contains(normalizedText, "# proposed skill improvement: "+normalizedSkill) {
		return false
	}
	if !strings.Contains(normalizedText, "improvement_type: "+strings.ToLower(normalizeImprovementType(in.ImprovementType))) {
		return false
	}
	matched := 0
	for _, marker := range []string{
		"repair reason: " + normalizeSkillImprovementText(in.RepairReason),
		"previous plan summary: " + normalizeSkillImprovementText(in.PreviousPlanSummary),
		"repaired plan summary: " + normalizeSkillImprovementText(in.RepairedPlanSummary),
	} {
		value := strings.TrimSpace(strings.SplitN(marker, ":", 2)[1])
		if value == "" {
			continue
		}
		if !containsNormalizedSegment(normalizedText, value) {
			continue
		}
		matched++
	}
	return matched >= 2
}

func normalizeSkillImprovementText(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	replacer := strings.NewReplacer(
		"\n", " ", "\t", " ",
		",", " ", ".", " ", ":", ": ", ";", " ",
		"(", " ", ")", " ", "[", " ", "]", " ",
		"{", " ", "}", " ", "-", " ", "_", " ",
	)
	text = replacer.Replace(text)
	return strings.Join(strings.Fields(text), " ")
}

func containsNormalizedSegment(text, segment string) bool {
	text = normalizeSkillImprovementText(text)
	segment = normalizeSkillImprovementText(segment)
	if segment == "" {
		return true
	}
	return strings.Contains(text, segment) || strings.Contains(segment, text)
}

func renderSkillImprovementProposal(in SkillImprovementInput) string {
	now := time.Now().Format("2006-01-02")
	sources := cleanList(in.Sources)
	if len(sources) == 0 {
		sources = []string{"manual"}
	}
	return fmt.Sprintf(`---
type: skill_improvement
scope: agent
status: proposed
tags: [skill-improvement, learning]
improvement_type: %s
sources:
%s
confidence: medium
created_at: %s
updated_at: %s
---

# Proposed Skill Improvement: %s

## Why This Was Proposed

%s

## Improvement Type

- %s

## Proposed Change

%s

## Repair Context

- Repair reason: %s
- Previous plan summary: %s
- Repaired plan summary: %s

## Evidence

- Task: %s
- Trace: %s
`, normalizeImprovementType(in.ImprovementType), renderImprovementSources(sources), now, now, in.SkillName, in.Reason, normalizeImprovementType(in.ImprovementType), in.ProposedChange, firstNonEmptyLearning(in.RepairReason, "unknown"), firstNonEmptyLearning(in.PreviousPlanSummary, "unknown"), firstNonEmptyLearning(in.RepairedPlanSummary, "unknown"), firstNonEmptyLearning(in.TaskID, "unknown"), firstNonEmptyLearning(in.TraceID, "unknown"))
}

func renderImprovementSources(sources []string) string {
	lines := make([]string, 0, len(sources))
	for _, source := range sources {
		lines = append(lines, "  - "+source)
	}
	return strings.Join(lines, "\n")
}

func normalizeSkillCandidateDirName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == ' ', r == '-', r == '_', r == '.', r == '/', r == ':':
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "skill"
	}
	return out
}

func slugForSkillImprovement(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	var b strings.Builder
	lastDash := false
	for _, r := range text {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == ' ', r == '-', r == '_', r == '.', r == '/', r == ':':
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "skill"
	}
	return out
}

func normalizeImprovementType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "missing_step", "unclear_instruction", "weak_verification":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func firstNonEmptyLearning(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func (s Store) agentLearningRoot(agentID string) string {
	return filepath.Join(s.Root, "agents", agentID, "learning")
}
