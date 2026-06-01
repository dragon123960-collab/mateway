package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
)

type DistillModel interface {
	Next(context.Context, agentcore.Context) (agentcore.Message, error)
}

type DistillHeartbeatInput struct {
	Home       string
	MemoryRoot string
	StatePath  string
	Model      DistillModel
	Now        func() time.Time
	Limit      int
}

type DistillHeartbeatResult struct {
	Scanned     int
	Created     int
	Skipped     int
	Duplicates  int
	Errors      []string
	ProposalIDs []string
}

type distillState struct {
	Processed map[string]string `json:"processed"`
	LastNudge map[string]string `json:"last_nudge,omitempty"`
	UpdatedAt string            `json:"updated_at"`
}

type distillSource struct {
	Path    string
	RelPath string
	Hash    string
	Text    string
	Score   int
}

type distilledProposal struct {
	Title      string   `json:"title"`
	Type       string   `json:"type"`
	Scope      string   `json:"scope"`
	Body       string   `json:"body"`
	Sources    []string `json:"sources"`
	Confidence string   `json:"confidence"`
}

func RunDistillHeartbeat(ctx context.Context, input DistillHeartbeatInput) (DistillHeartbeatResult, error) {
	home := defaultString(input.Home, ".mateway")
	memoryRoot := strings.TrimSpace(input.MemoryRoot)
	if memoryRoot == "" {
		memoryRoot = filepath.Join(home, "workspace", "memory")
	}
	statePath := strings.TrimSpace(input.StatePath)
	if statePath == "" {
		statePath = filepath.Join(home, "indexes", "memory_distill_state.json")
	}
	now := time.Now()
	if input.Now != nil {
		now = input.Now()
	}
	result := DistillHeartbeatResult{}
	_ = writeMemoryAudit(home, "memory_distill_started", map[string]any{"time": now.Format(time.RFC3339Nano)})
	state := readDistillState(statePath)
	sources, err := collectDistillSources(home, state, input.Limit)
	if err != nil {
		return result, err
	}
	result.Scanned = len(sources)
	candidates := filterDistillCandidates(sources)
	if len(candidates) == 0 {
		result.Skipped = len(sources)
		markProcessed(state, sources)
		if err := writeDistillState(statePath, state, now); err != nil {
			return result, err
		}
		_ = writeMemoryAudit(home, "memory_distill_done", map[string]any{"scanned": result.Scanned, "created": result.Created, "skipped": result.Skipped, "duplicates": result.Duplicates})
		return result, nil
	}
	if input.Model == nil {
		result.Skipped = len(candidates)
		_ = writeMemoryAudit(home, "memory_distill_model_error", map[string]any{"error": "no memory distill model configured", "candidates": len(candidates)})
		_ = writeMemoryAudit(home, "memory_distill_done", map[string]any{"scanned": result.Scanned, "created": result.Created, "skipped": result.Skipped, "duplicates": result.Duplicates})
		return result, nil
	}
	proposal, err := runDistillModel(ctx, input.Model, candidates)
	if err != nil {
		result.Errors = append(result.Errors, err.Error())
		_ = writeMemoryAudit(home, "memory_distill_model_error", map[string]any{"error": err.Error()})
		_ = writeMemoryAudit(home, "memory_distill_done", map[string]any{"scanned": result.Scanned, "created": result.Created, "skipped": result.Skipped, "duplicates": result.Duplicates, "errors": len(result.Errors)})
		return result, nil
	}
	if isDuplicateDistillProposal(home, memoryRoot, proposal) {
		result.Duplicates++
		_ = writeMemoryAudit(home, "memory_distill_duplicate_skipped", map[string]any{"title": proposal.Title})
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
			_ = writeMemoryAudit(home, "memory_distill_model_error", map[string]any{"error": err.Error(), "stage": "proposal_create"})
		} else {
			result.Created++
			result.ProposalIDs = append(result.ProposalIDs, created.ID)
			markProcessed(state, candidates)
			_ = writeMemoryAudit(home, "memory_distill_proposal_created", map[string]any{"proposal_id": created.ID, "title": created.Title})
		}
	}
	if err := writeDistillState(statePath, state, now); err != nil {
		return result, err
	}
	_ = writeMemoryAudit(home, "memory_distill_done", map[string]any{"scanned": result.Scanned, "created": result.Created, "skipped": result.Skipped, "duplicates": result.Duplicates, "errors": len(result.Errors)})
	return result, nil
}

func markProcessed(state distillState, sources []distillSource) {
	if state.Processed == nil {
		state.Processed = map[string]string{}
	}
	for _, source := range sources {
		state.Processed[source.RelPath] = source.Hash
	}
}

func collectDistillSources(home string, state distillState, limit int) ([]distillSource, error) {
	if limit <= 0 {
		limit = 20
	}
	var out []distillSource
	for _, dir := range []string{filepath.Join(home, "observe", "diary"), filepath.Join(home, "observe", "reflections")} {
		err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.EqualFold(filepath.Ext(path), ".md") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			hash := hashText(string(data))
			rel, _ := filepath.Rel(home, path)
			if state.Processed[filepath.ToSlash(rel)] == hash {
				return nil
			}
			out = append(out, distillSource{Path: path, RelPath: filepath.ToSlash(rel), Hash: hash, Text: string(data), Score: distillScore(string(data))})
			return nil
		})
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

func filterDistillCandidates(sources []distillSource) []distillSource {
	var out []distillSource
	toolCounts := map[string]int{}
	for _, source := range sources {
		for _, toolName := range acceptedTools(source.Text) {
			toolCounts[toolName]++
		}
	}
	for _, source := range sources {
		if source.Score > 0 || hasRepeatedTool(source.Text, toolCounts) {
			out = append(out, source)
		}
	}
	return out
}

func distillScore(text string) int {
	lower := strings.ToLower(text)
	score := 0
	if HasStrongMemoryCue(lower) || containsAny(lower, []string{"纠正", "以后不要", "以后要", "修复", "失败", "failed", "error", "preference", "remember"}) {
		score += 2
	}
	if strings.Contains(lower, "status: failed") || strings.Contains(lower, " failed") || strings.Contains(lower, "Non-accepted steps:") {
		score++
	}
	if len(acceptedTools(text)) > 0 {
		score++
	}
	return score
}

var acceptedToolPattern = regexp.MustCompile(`(?m)^- ([A-Za-z0-9_.-]+) accepted`)

func acceptedTools(text string) []string {
	matches := acceptedToolPattern.FindAllStringSubmatch(text, -1)
	var out []string
	for _, match := range matches {
		if len(match) > 1 {
			out = append(out, match[1])
		}
	}
	return out
}

func hasRepeatedTool(text string, counts map[string]int) bool {
	for _, toolName := range acceptedTools(text) {
		if counts[toolName] >= 2 {
			return true
		}
	}
	return false
}

func runDistillModel(ctx context.Context, model DistillModel, candidates []distillSource) (distilledProposal, error) {
	prompt := renderDistillPrompt(candidates)
	msg, err := model.Next(ctx, agentcore.Context{
		SystemPrompt: "You distill reusable long-term memory candidates. Return one strict JSON object only.",
		Messages: []agentcore.Message{{
			Role:    agentcore.RoleUser,
			Content: prompt,
		}},
	})
	if err != nil {
		return distilledProposal{}, err
	}
	var proposal distilledProposal
	if err := json.Unmarshal([]byte(extractJSONObject(msg.Content)), &proposal); err != nil {
		return distilledProposal{}, err
	}
	proposal.Title = strings.TrimSpace(proposal.Title)
	proposal.Body = strings.TrimSpace(proposal.Body)
	proposal.Type = defaultString(proposal.Type, "experience")
	proposal.Scope = defaultString(proposal.Scope, "agent")
	proposal.Confidence = defaultString(proposal.Confidence, "low")
	proposal.Sources = cleanStrings(proposal.Sources)
	if proposal.Title == "" || proposal.Body == "" || len(proposal.Sources) == 0 {
		return distilledProposal{}, fmt.Errorf("distill model returned incomplete proposal")
	}
	return proposal, nil
}

func renderDistillPrompt(candidates []distillSource) string {
	var b strings.Builder
	b.WriteString("Distill these task diary/reflection notes into at most one reusable long-term memory proposal.\n")
	b.WriteString("Return strict JSON with keys: title, type, scope, body, sources, confidence.\n")
	b.WriteString("Only preserve future reusable preferences, decisions, workflows, lessons, or tool usage patterns. Do not save one-off task outputs.\n")
	for _, source := range candidates {
		b.WriteString("\n--- SOURCE ")
		b.WriteString(source.RelPath)
		b.WriteString(" ---\n")
		b.WriteString(truncateDistillText(source.Text, 1600))
		b.WriteString("\n")
	}
	return b.String()
}

func extractJSONObject(text string) string {
	text = strings.TrimSpace(text)
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end >= start {
		return text[start : end+1]
	}
	return text
}

func isDuplicateDistillProposal(home, memoryRoot string, proposal distilledProposal) bool {
	key := duplicateKey(proposal.Title)
	if key == "" {
		return false
	}
	store := ProposalStore{Home: home, MemoryRoot: memoryRoot}
	proposals, _ := store.List()
	for _, existing := range proposals {
		if existing.Status == "proposed" && duplicateKey(existing.Title) == key {
			return true
		}
		if sharesSource(existing.Sources, proposal.Sources) {
			return true
		}
	}
	results, _, _ := SearchRoot(memoryRoot, SearchOptions{Query: proposal.Title, Limit: 5})
	for _, result := range results {
		if duplicateKey(filepath.Base(result.Path)) == key || sharesSource(result.Sources, proposal.Sources) {
			return true
		}
	}
	return false
}

func sharesSource(left, right []string) bool {
	seen := map[string]bool{}
	for _, value := range left {
		if value = strings.TrimSpace(value); value != "" {
			seen[value] = true
		}
	}
	for _, value := range right {
		if seen[strings.TrimSpace(value)] {
			return true
		}
	}
	return false
}

func duplicateKey(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	var b strings.Builder
	for _, r := range text {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || (r >= '\u4e00' && r <= '\u9fff') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func readDistillState(path string) distillState {
	state := distillState{Processed: map[string]string{}, LastNudge: map[string]string{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return state
	}
	_ = json.Unmarshal(data, &state)
	if state.Processed == nil {
		state.Processed = map[string]string{}
	}
	if state.LastNudge == nil {
		state.LastNudge = map[string]string{}
	}
	return state
}

func writeDistillState(path string, state distillState, now time.Time) error {
	state.UpdatedAt = now.Format(time.RFC3339Nano)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func PendingProposalNudge(home, sessionKey string, now time.Time) (string, error) {
	home = defaultString(home, ".mateway")
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		sessionKey = "default"
	}
	store := ProposalStore{Home: home}
	proposals, err := store.List()
	if err != nil {
		return "", err
	}
	count := 0
	for _, proposal := range proposals {
		if proposal.Status == "proposed" {
			count++
		}
	}
	if count == 0 {
		return "", nil
	}
	statePath := filepath.Join(home, "indexes", "memory_distill_state.json")
	state := readDistillState(statePath)
	day := now.Format("2006-01-02")
	key := strings.ReplaceAll(strings.ReplaceAll(sessionKey, "/", "_"), ":", "_")
	if state.LastNudge[key] == day {
		return "", nil
	}
	state.LastNudge[key] = day
	if err := writeDistillState(statePath, state, now); err != nil {
		return "", err
	}
	return fmt.Sprintf("有 %d 条长期记忆候选待审核：`mateway memory proposal list`", count), nil
}

func writeMemoryAudit(home, event string, fields map[string]any) error {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["type"] = event
	if _, ok := fields["time"]; !ok {
		fields["time"] = time.Now().Format(time.RFC3339Nano)
	}
	data, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	path := filepath.Join(home, "observe", "audit", "memory.jsonl")
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

func hashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func truncateDistillText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit] + fmt.Sprintf("\n... (%d chars)", len(text))
}
