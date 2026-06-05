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

type ProposalNudgeOptions struct {
	Channel      string
	Channels     []string
	Interval     time.Duration
	MaxProposals int
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
	if HasStrongMemoryCue(lower) || containsAny(lower, memoryCueList("memory.distill.score_cues")) {
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

func PendingProposalNudge(home, sessionKey string, now time.Time, options ProposalNudgeOptions) (string, error) {
	home = defaultString(home, ".mateway")
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		sessionKey = "default"
	}
	if !channelAllowed(options.Channel, options.Channels) {
		return "", nil
	}
	store := ProposalStore{Home: home}
	proposals, err := store.List()
	if err != nil {
		return "", err
	}
	var pending []Proposal
	for _, proposal := range proposals {
		if proposal.Status == "proposed" {
			pending = append(pending, proposal)
		}
	}
	if len(pending) == 0 {
		return "", nil
	}
	statePath := filepath.Join(home, "indexes", "memory_distill_state.json")
	state := readDistillState(statePath)
	interval := options.Interval
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	key := strings.ReplaceAll(strings.ReplaceAll(sessionKey, "/", "_"), ":", "_")
	if last := parseNudgeTime(state.LastNudge[key]); !last.IsZero() {
		if now.Sub(last) < interval {
			return "", nil
		}
	}
	state.LastNudge[key] = now.Format(time.RFC3339)
	if err := writeDistillState(statePath, state, now); err != nil {
		return "", err
	}
	maxProposals := options.MaxProposals
	if maxProposals <= 0 {
		maxProposals = 3
	}
	return renderProposalNudge(pending, maxProposals), nil
}

func channelAllowed(channel string, allowed []string) bool {
	channel = strings.TrimSpace(strings.ToLower(channel))
	if channel == "" {
		channel = "cli"
	}
	if len(allowed) == 0 {
		allowed = []string{"cli"}
	}
	for _, value := range allowed {
		if strings.EqualFold(strings.TrimSpace(value), channel) {
			return true
		}
	}
	return false
}

func parseNudgeTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed
	}
	if parsed, err := time.Parse("2006-01-02", value); err == nil {
		return parsed
	}
	return time.Time{}
}

func renderProposalNudge(proposals []Proposal, maxProposals int) string {
	if maxProposals > len(proposals) {
		maxProposals = len(proposals)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Pending memory proposals (%d total, showing %d):", len(proposals), maxProposals)
	for i := 0; i < maxProposals; i++ {
		proposal := proposals[i]
		fmt.Fprintf(&b, "\n\n%d. %s %s\n", i+1, proposal.ID, proposal.Title)
		fmt.Fprintf(&b, "Type: %s / %s, confidence: %s", defaultString(proposal.Type, "experience"), defaultString(proposal.Scope, "agent"), defaultString(proposal.Confidence, "low"))
		if value := proposalReasonSummary(proposal); value != "" {
			fmt.Fprintf(&b, "\nValue: %s", value)
		}
		if len(proposal.Sources) > 0 {
			fmt.Fprintf(&b, "\nSources: %s", summarizeNudgeText(strings.Join(proposal.Sources, ", "), 90))
		}
		fmt.Fprintf(&b, "\nReview: mateway memory proposal show %s", proposal.ID)
	}
	if rest := len(proposals) - maxProposals; rest > 0 {
		fmt.Fprintf(&b, "\n\n... and %d more.", rest)
	}
	return b.String()
}

func proposalReasonSummary(proposal Proposal) string {
	body := strings.TrimSpace(proposal.Body)
	body = strings.TrimPrefix(body, "# "+strings.TrimSpace(proposal.Title))
	body = strings.TrimSpace(body)
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "-"))
		if line != "" && !strings.HasPrefix(line, "#") {
			return summarizeNudgeText(line, 110)
		}
	}
	return ""
}

func summarizeNudgeText(text string, limit int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if limit <= 0 || len([]rune(text)) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit]) + "..."
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
