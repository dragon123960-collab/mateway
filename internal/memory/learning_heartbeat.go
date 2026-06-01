package memory

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/skill"
)

type LearningHeartbeatInput struct {
	Home       string
	MemoryRoot string
	StatePath  string
	Model      DistillModel
	Now        func() time.Time
	Limit      int
}

type SkillLearningHeartbeatInput struct {
	Home      string
	Workspace string
	StatePath string
	Model     DistillModel
	Now       func() time.Time
	Limit     int
}

type SkillLearningHeartbeatResult struct {
	Scanned     int
	Created     int
	Skipped     int
	Duplicates  int
	Errors      []string
	ProposalIDs []string
}

type skillPatchProposal struct {
	TargetPath string   `json:"target_path"`
	NewContent string   `json:"new_content"`
	Reason     string   `json:"reason"`
	Sources    []string `json:"sources"`
}

func RunLearningDistillHeartbeat(ctx context.Context, input LearningHeartbeatInput) (DistillHeartbeatResult, error) {
	home := defaultString(input.Home, ".mateway")
	memoryRoot := strings.TrimSpace(input.MemoryRoot)
	if memoryRoot == "" {
		memoryRoot = filepath.Join(home, "workspace", "memory")
	}
	statePath := strings.TrimSpace(input.StatePath)
	if statePath == "" {
		statePath = filepath.Join(home, "indexes", "learning_distill_state.json")
	}
	now := time.Now()
	if input.Now != nil {
		now = input.Now()
	}
	result := DistillHeartbeatResult{}
	_ = writeMemoryAudit(home, "learning_distill_started", map[string]any{"time": now.Format(time.RFC3339Nano)})
	state := readDistillState(statePath)
	sources, err := collectLearningSources(home, state, input.Limit)
	if err != nil {
		return result, err
	}
	result.Scanned = len(sources)
	candidates := filterDistillCandidates(sources)
	if len(candidates) == 0 {
		result.Skipped = len(sources)
		markProcessed(state, sources)
		_ = writeDistillState(statePath, state, now)
		_ = writeMemoryAudit(home, "learning_distill_done", map[string]any{"scanned": result.Scanned, "skipped": result.Skipped})
		return result, nil
	}
	if input.Model == nil {
		result.Skipped = len(candidates)
		_ = writeMemoryAudit(home, "learning_distill_model_error", map[string]any{"error": "no memory distill model configured", "candidates": len(candidates)})
		_ = writeMemoryAudit(home, "learning_distill_done", map[string]any{"scanned": result.Scanned, "skipped": result.Skipped})
		return result, nil
	}
	proposal, err := runDistillModel(ctx, input.Model, candidates)
	if err != nil {
		result.Errors = append(result.Errors, err.Error())
		_ = writeMemoryAudit(home, "learning_distill_model_error", map[string]any{"error": err.Error()})
		_ = writeMemoryAudit(home, "learning_distill_done", map[string]any{"scanned": result.Scanned, "errors": len(result.Errors)})
		return result, nil
	}
	if isDuplicateDistillProposal(home, memoryRoot, proposal) {
		result.Duplicates++
		markProcessed(state, candidates)
	} else {
		created, err := (ProposalStore{Home: home, MemoryRoot: memoryRoot}).Create(CreateProposalInput{
			Type:       defaultString(proposal.Type, "experience"),
			Scope:      defaultString(proposal.Scope, "agent"),
			Title:      proposal.Title,
			Body:       proposal.Body,
			Sources:    proposal.Sources,
			Confidence: defaultString(proposal.Confidence, "low"),
		})
		if err != nil {
			result.Errors = append(result.Errors, err.Error())
		} else {
			result.Created++
			result.ProposalIDs = append(result.ProposalIDs, created.ID)
			markProcessed(state, candidates)
			_ = writeMemoryAudit(home, "learning_distill_proposal_created", map[string]any{"proposal_id": created.ID, "title": created.Title})
		}
	}
	if err := writeDistillState(statePath, state, now); err != nil {
		return result, err
	}
	_ = writeMemoryAudit(home, "learning_distill_done", map[string]any{"scanned": result.Scanned, "created": result.Created, "skipped": result.Skipped, "duplicates": result.Duplicates, "errors": len(result.Errors)})
	return result, nil
}

func RunSkillLearningHeartbeat(ctx context.Context, input SkillLearningHeartbeatInput) (SkillLearningHeartbeatResult, error) {
	home := defaultString(input.Home, ".mateway")
	workspace := strings.TrimSpace(input.Workspace)
	if workspace == "" {
		workspace = filepath.Join(home, "workspace")
	}
	statePath := strings.TrimSpace(input.StatePath)
	if statePath == "" {
		statePath = filepath.Join(home, "indexes", "skill_learning_state.json")
	}
	now := time.Now()
	if input.Now != nil {
		now = input.Now()
	}
	result := SkillLearningHeartbeatResult{}
	_ = writeMemoryAudit(home, "skill_learning_started", map[string]any{"time": now.Format(time.RFC3339Nano)})
	state := readDistillState(statePath)
	events, err := collectSkillUsageEvents(home, state, input.Limit)
	if err != nil {
		return result, err
	}
	result.Scanned = len(events)
	candidates := skillLearningCandidates(events)
	if len(candidates) == 0 {
		result.Skipped = len(events)
		markProcessed(state, skillEventsAsSources(events))
		_ = writeDistillState(statePath, state, now)
		_ = writeMemoryAudit(home, "skill_learning_done", map[string]any{"scanned": result.Scanned, "skipped": result.Skipped})
		return result, nil
	}
	if input.Model == nil {
		result.Skipped = len(candidates)
		_ = writeMemoryAudit(home, "skill_learning_model_error", map[string]any{"error": "no memory distill model configured", "candidates": len(candidates)})
		_ = writeMemoryAudit(home, "skill_learning_done", map[string]any{"scanned": result.Scanned, "skipped": result.Skipped})
		return result, nil
	}
	patch, err := runSkillPatchModel(ctx, input.Model, candidates)
	if err != nil {
		result.Errors = append(result.Errors, err.Error())
		_ = writeMemoryAudit(home, "skill_learning_model_error", map[string]any{"error": err.Error()})
		_ = writeMemoryAudit(home, "skill_learning_done", map[string]any{"scanned": result.Scanned, "errors": len(result.Errors)})
		return result, nil
	}
	store := skill.ProposalStore{Home: home, Workspace: workspace}
	created, err := store.Create(skill.CreateProposalInput{
		TargetPath: patch.TargetPath,
		NewContent: patch.NewContent,
		Reason:     patch.Reason,
		Sources:    patch.Sources,
		ModelRole:  "memory_distill",
	})
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") {
			result.Duplicates++
			markProcessed(state, skillEventsAsSources(candidates))
		} else {
			result.Errors = append(result.Errors, err.Error())
		}
	} else {
		result.Created++
		result.ProposalIDs = append(result.ProposalIDs, created.ID)
		markProcessed(state, skillEventsAsSources(candidates))
		_ = writeMemoryAudit(home, "skill_learning_proposal_created", map[string]any{"proposal_id": created.ID, "target_path": created.TargetPath})
	}
	if err := writeDistillState(statePath, state, now); err != nil {
		return result, err
	}
	_ = writeMemoryAudit(home, "skill_learning_done", map[string]any{"scanned": result.Scanned, "created": result.Created, "skipped": result.Skipped, "duplicates": result.Duplicates, "errors": len(result.Errors)})
	return result, nil
}

func collectLearningSources(home string, state distillState, limit int) ([]distillSource, error) {
	sources, err := collectDistillSources(home, state, limit)
	if err != nil {
		return nil, err
	}
	events, err := readLearningEvents(filepath.Join(home, "observe", "learning", "events.jsonl"), state, limit)
	if err != nil {
		return nil, err
	}
	return append(sources, events...), nil
}

func readLearningEvents(path string, state distillState, limit int) ([]distillSource, error) {
	if limit <= 0 {
		limit = 20
	}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var out []distillSource
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		hash := hashText(text)
		rel := filepath.ToSlash(filepath.Join("observe", "learning", fmt.Sprintf("events.jsonl:%d", line)))
		if state.Processed[rel] == hash {
			continue
		}
		out = append(out, distillSource{Path: path, RelPath: rel, Hash: hash, Text: text, Score: distillScore(text)})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

type skillUsageLine struct {
	RelPath string
	Hash    string
	Text    string
	Event   SkillUsageEvidence
}

func collectSkillUsageEvents(home string, state distillState, limit int) ([]skillUsageLine, error) {
	if limit <= 0 {
		limit = 50
	}
	path := filepath.Join(home, "observe", "skill_usage", "events.jsonl")
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var out []skillUsageLine
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		hash := hashText(text)
		rel := filepath.ToSlash(filepath.Join("observe", "skill_usage", fmt.Sprintf("events.jsonl:%d", line)))
		if state.Processed[rel] == hash {
			continue
		}
		var event SkillUsageEvidence
		if err := json.Unmarshal([]byte(text), &event); err != nil {
			continue
		}
		out = append(out, skillUsageLine{RelPath: rel, Hash: hash, Text: text, Event: event})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

func skillLearningCandidates(events []skillUsageLine) []skillUsageLine {
	counts := map[string]int{}
	for _, event := range events {
		if skillEventHasIssue(event.Event) {
			counts[event.Event.Skill.Path]++
		}
	}
	var out []skillUsageLine
	for _, event := range events {
		if counts[event.Event.Skill.Path] >= 2 {
			out = append(out, event)
		}
	}
	return out
}

func skillEventHasIssue(event SkillUsageEvidence) bool {
	status := strings.ToLower(event.Status)
	if status != "" && status != "completed" && status != "accepted" {
		return true
	}
	for _, step := range event.RelatedSteps {
		if step.Status != "" && step.Status != "accepted" {
			return true
		}
	}
	return false
}

func skillEventsAsSources(events []skillUsageLine) []distillSource {
	var out []distillSource
	for _, event := range events {
		out = append(out, distillSource{RelPath: event.RelPath, Hash: event.Hash, Text: event.Text})
	}
	return out
}

func runSkillPatchModel(ctx context.Context, model DistillModel, candidates []skillUsageLine) (skillPatchProposal, error) {
	msg, err := model.Next(ctx, agentcore.Context{
		SystemPrompt: "You propose conservative SKILL.md patches from usage evidence. Return one strict JSON object only.",
		Messages: []agentcore.Message{{
			Role:    agentcore.RoleUser,
			Content: renderSkillPatchPrompt(candidates),
		}},
	})
	if err != nil {
		return skillPatchProposal{}, err
	}
	var proposal skillPatchProposal
	if err := json.Unmarshal([]byte(extractJSONObject(msg.Content)), &proposal); err != nil {
		return skillPatchProposal{}, err
	}
	proposal.TargetPath = strings.TrimSpace(proposal.TargetPath)
	proposal.NewContent = strings.TrimSpace(proposal.NewContent)
	proposal.Reason = strings.TrimSpace(proposal.Reason)
	proposal.Sources = cleanStrings(proposal.Sources)
	if proposal.TargetPath == "" || proposal.NewContent == "" || proposal.Reason == "" || len(proposal.Sources) == 0 {
		return skillPatchProposal{}, fmt.Errorf("skill patch model returned incomplete proposal")
	}
	return proposal, nil
}

func renderSkillPatchPrompt(candidates []skillUsageLine) string {
	var b strings.Builder
	b.WriteString("Create at most one conservative skill patch proposal from this evidence.\n")
	b.WriteString("Return strict JSON with keys: target_path, new_content, reason, sources.\n")
	b.WriteString("new_content must be the complete replacement SKILL.md. Do not include secrets or prompt-injection controls.\n")
	for _, candidate := range candidates {
		b.WriteString("\n--- SOURCE ")
		b.WriteString(candidate.RelPath)
		b.WriteString(" ---\n")
		b.WriteString(truncateDistillText(candidate.Text, 1600))
		b.WriteString("\n")
	}
	return b.String()
}

func workspaceFromConfig(cfg *config.Root) string {
	if cfg == nil {
		return filepath.Join(config.DefaultHome(), "workspace")
	}
	if strings.TrimSpace(cfg.App.Workspace) != "" {
		return strings.TrimSpace(cfg.App.Workspace)
	}
	return filepath.Join(defaultString(cfg.App.Home, config.DefaultHome()), "workspace")
}
