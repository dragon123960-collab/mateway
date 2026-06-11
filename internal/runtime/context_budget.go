package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/session"
)

const (
	defaultContextWindowTokens = 128000
	defaultMaxOutputTokens     = 4096
	minHardInputTokens         = 2048
)

type contextBudgetInput struct {
	Config         *config.Root
	ModelConfig    config.ModelConfig
	SystemPrompt   string
	Messages       []agentcore.Message
	Tools          []agentcore.Tool
	Contract       session.TaskContract
	State          session.State
	TaskID         string
	DefaultVisible []string
}

type contextBudgetResult struct {
	Messages              []agentcore.Message
	Tools                 []agentcore.Tool
	EstimatedInputTokens  int
	BeforeInputTokens     int
	SoftLimitTokens       int
	HardLimitTokens       int
	SavedEstimatedTokens  int
	DroppedMessages       int
	CompactedMessages     int
	CompactedToolResults  int
	VisibleTools          int
	HiddenTools           int
	ToolNames             []string
	TrimmedTools          []string
	NonDefaultExposed     map[string]string
	RawRefs               []string
	MissingContractTools  []string
	ContextWindowTokens   int
	MaxOutputTokens       int
	Compacted             bool
	HardLimitExceeded     bool
	HardLimitExceededText string
}

type budgetedModel struct {
	inner          agentcore.Model
	config         *config.Root
	modelConfig    config.ModelConfig
	trace          *traceRecorder
	state          session.State
	taskID         string
	results        *[]contextBudgetResult
	defaultVisible []string
}

func (m budgetedModel) Next(ctx context.Context, agentCtx agentcore.Context) (agentcore.Message, error) {
	result := packMessagesForContextBudget(contextBudgetInput{
		Config:         m.config,
		ModelConfig:    m.modelConfig,
		SystemPrompt:   agentCtx.SystemPrompt,
		Messages:       agentCtx.Messages,
		Tools:          agentCtx.Tools,
		Contract:       taskContractFromState(m.state, m.taskID),
		State:          m.state,
		TaskID:         m.taskID,
		DefaultVisible: m.defaultVisible,
	})
	writeContextBudgetTrace(m.trace, contextBudgetConfig(m.config), result)
	if m.results != nil {
		*m.results = append(*m.results, result)
	}
	if result.HardLimitExceeded {
		return agentcore.Message{}, fmt.Errorf("%w: %s", errContextBudgetHardLimit, result.HardLimitExceededText)
	}
	agentCtx.Messages = result.Messages
	agentCtx.Tools = result.Tools
	return m.inner.Next(ctx, agentCtx)
}

var errContextBudgetHardLimit = fmt.Errorf("context budget hard limit exceeded")

func packMessagesForContextBudget(input contextBudgetInput) contextBudgetResult {
	cfg := contextBudgetConfig(input.Config)
	window := input.ModelConfig.ContextWindow
	if window <= 0 {
		window = defaultContextWindowTokens
	}
	maxOutput := input.ModelConfig.MaxTokensValue()
	if maxOutput <= 0 {
		maxOutput = defaultMaxOutputTokens
	}
	available := window - maxOutput
	if available < minHardInputTokens {
		available = minHardInputTokens
	}
	hardLimit := int(math.Floor(float64(available) * cfg.HardRatioValue()))
	if hardLimit < minHardInputTokens {
		hardLimit = minHardInputTokens
	}
	softLimit := int(math.Floor(float64(available) * cfg.SoftRatioValue()))
	if softLimit <= 0 || softLimit > hardLimit {
		softLimit = hardLimit
	}
	visibleTools, trimmedNames, nonDefaultExposed := selectVisibleTools(input.Tools, input.Messages, input.Contract, cfg, input.DefaultVisible)
	result := contextBudgetResult{
		Messages:             append([]agentcore.Message(nil), input.Messages...),
		Tools:                visibleTools,
		TrimmedTools:         trimmedNames,
		NonDefaultExposed:    nonDefaultExposed,
		SoftLimitTokens:      softLimit,
		HardLimitTokens:      hardLimit,
		ContextWindowTokens:  window,
		MaxOutputTokens:      maxOutput,
		MissingContractTools: missingContractTools(input.Tools, input.Contract),
	}
	result.VisibleTools = len(result.Tools)
	result.HiddenTools = maxInt(0, len(input.Tools)-len(result.Tools))
	result.ToolNames = toolNames(result.Tools)
	result.BeforeInputTokens = estimateModelInputTokens(input.SystemPrompt, result.Messages, result.Tools)
	result.EstimatedInputTokens = result.BeforeInputTokens
	if !cfg.EnabledValue() || result.EstimatedInputTokens <= softLimit {
		return result
	}
	result.Messages = compactMessagesByTier(result.Messages, input.State, input.TaskID, cfg, &result)
	result.EstimatedInputTokens = estimateModelInputTokens(input.SystemPrompt, result.Messages, result.Tools)
	result.SavedEstimatedTokens = maxInt(0, result.BeforeInputTokens-result.EstimatedInputTokens)
	result.Compacted = result.SavedEstimatedTokens > 0 || result.DroppedMessages > 0 || result.CompactedMessages > 0 || result.CompactedToolResults > 0
	if result.EstimatedInputTokens > hardLimit {
		result.HardLimitExceeded = true
		result.HardLimitExceededText = fmt.Sprintf("estimated input tokens %d exceed hard limit %d after compaction", result.EstimatedInputTokens, hardLimit)
	}
	return result
}

func contextBudgetConfig(root *config.Root) config.ContextBudgetConfig {
	if root == nil {
		return config.DefaultRoot().Execution.ContextBudget
	}
	return root.Execution.ContextBudget
}

func selectVisibleTools(tools []agentcore.Tool, messages []agentcore.Message, contract session.TaskContract, cfg config.ContextBudgetConfig, defaultVisible []string) ([]agentcore.Tool, []string, map[string]string) {
	if len(tools) == 0 {
		return nil, nil, nil
	}
	limit := cfg.MaxVisibleToolsValue()
	if limit >= len(tools) {
		return append([]agentcore.Tool(nil), tools...), nil, nil
	}
	if len(defaultVisible) == 0 {
		defaultVisible = cfg.DefaultVisibleValue()
	}

	defaultSet := map[string]bool{}
	for _, name := range defaultVisible {
		defaultSet[name] = true
	}

	contractSet := map[string]bool{}
	for _, name := range contract.RequiredTools {
		contractSet[name] = true
	}
	for _, item := range contract.PlanItems {
		if name := strings.TrimSpace(item.Tool); name != "" {
			contractSet[name] = true
		}
	}

	out := make([]agentcore.Tool, 0, limit)
	used := map[string]bool{}
	exposed := map[string]string{}

	for _, name := range defaultVisible {
		if used[name] {
			continue
		}
		if found := findToolByName(tools, name); found != nil {
			used[name] = true
			out = append(out, found)
		}
	}

	for _, name := range contract.RequiredTools {
		if used[name] {
			continue
		}
		if found := findToolByName(tools, name); found != nil {
			used[name] = true
			out = append(out, found)
			if !defaultSet[name] {
				reason := "contract"
				if containsToolName(recentToolCallNames(messages), name) {
					reason = "contract+recent"
				}
				exposed[name] = reason
			}
		}
	}
	for _, item := range contract.PlanItems {
		name := strings.TrimSpace(item.Tool)
		if name == "" || used[name] {
			continue
		}
		if found := findToolByName(tools, name); found != nil {
			used[name] = true
			out = append(out, found)
			if !defaultSet[name] {
				reason := "contract"
				if containsToolName(recentToolCallNames(messages), name) {
					reason = "contract+recent"
				}
				exposed[name] = reason
			}
		}
	}

	scores := map[string]int{}
	text := strings.ToLower(messagesText(messages))
	for _, callName := range recentToolCallNames(messages) {
		addToolScore(scores, callName, 90)
	}
	if strings.Contains(text, "raw_ref") || strings.Contains(text, "tool-result:") {
		addToolScore(scores, "toolresult.read", 95)
	}

	budgetRemaining := limit
	var trimmed []string
	for budgetRemaining > 0 {
		bestIndex := -1
		bestScore := 0
		bestName := ""
		for i, tool := range tools {
			if tool == nil || used[tool.Name()] {
				continue
			}
			name := tool.Name()
			if defaultSet[name] || contractSet[name] {
				continue
			}
			score := scores[name]
			if score <= 0 {
				continue
			}
			if bestIndex < 0 || score > bestScore || score == bestScore && name < bestName {
				bestIndex = i
				bestScore = score
				bestName = name
			}
		}
		if bestIndex < 0 {
			break
		}
		tool := tools[bestIndex]
		used[tool.Name()] = true
		out = append(out, tool)
		exposed[tool.Name()] = "recent"
		budgetRemaining--
	}

	for _, tool := range tools {
		if tool == nil || used[tool.Name()] {
			continue
		}
		name := tool.Name()
		if defaultSet[name] || contractSet[name] {
			continue
		}
		if scores[name] > 0 {
			trimmed = append(trimmed, name)
		}
	}

	if len(out) == 0 {
		return append([]agentcore.Tool(nil), tools...), nil, nil
	}
	return out, trimmed, exposed
}

func missingContractTools(tools []agentcore.Tool, contract session.TaskContract) []string {
	var missing []string
	for _, name := range contract.RequiredTools {
		if findToolByName(tools, name) == nil {
			missing = append(missing, name)
		}
	}
	for _, item := range contract.PlanItems {
		name := strings.TrimSpace(item.Tool)
		if name != "" && findToolByName(tools, name) == nil {
			missing = append(missing, name)
		}
	}
	return missing
}

func messagesText(messages []agentcore.Message) string {
	var b strings.Builder
	for _, msg := range messages {
		b.WriteString(msg.Content)
		b.WriteByte('\n')
		for _, part := range msg.Parts {
			b.WriteString(part.Text)
			b.WriteByte('\n')
			b.WriteString(part.Name)
			b.WriteByte('\n')
			b.WriteString(part.URI)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func recentToolCallNames(messages []agentcore.Message) []string {
	var names []string
	start := maxInt(0, len(messages)-8)
	for _, msg := range messages[start:] {
		for _, call := range msg.ToolCalls {
			if strings.TrimSpace(call.Name) != "" {
				names = append(names, call.Name)
			}
		}
	}
	return names
}

func addToolScore(scores map[string]int, name string, score int) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	if score > scores[name] {
		scores[name] = score
	}
}

func containsToolName(names []string, target string) bool {
	for _, name := range names {
		if name == target {
			return true
		}
	}
	return false
}

func findToolByName(tools []agentcore.Tool, name string) agentcore.Tool {
	for _, tool := range tools {
		if tool != nil && tool.Name() == name {
			return tool
		}
	}
	return nil
}

func toolNames(tools []agentcore.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		if tool != nil {
			names = append(names, tool.Name())
		}
	}
	return names
}

func compactMessagesByTier(messages []agentcore.Message, state session.State, taskID string, cfg config.ContextBudgetConfig, result *contextBudgetResult) []agentcore.Message {
	out := append([]agentcore.Message(nil), messages...)
	targetChars := maxInt(256, cfg.ToolResultTargetTokensValue()*4)
	for i := range out {
		if out[i].Role != agentcore.RoleTool {
			continue
		}
		before := out[i].Content
		out[i].Content = compactToolMessageForBudget(before, targetChars)
		if out[i].Content != before {
			result.CompactedToolResults++
			result.CompactedMessages++
		}
		result.RawRefs = append(result.RawRefs, rawRefsFromMessage(out[i])...)
	}
	out, dropped := dropStaleAssistantMessages(out, cfg.RecentTurnsValue())
	result.DroppedMessages += dropped
	if dropped > 0 {
		result.CompactedMessages += dropped
	}
	if len(out) > cfg.RecentTurnsValue()*2+4 {
		summary := taskEvidenceSummary(state, taskID)
		keepFrom := maxInt(0, len(out)-cfg.RecentTurnsValue()*2)
		var compacted []agentcore.Message
		if summary != "" {
			compacted = append(compacted, agentcore.Message{Role: agentcore.RoleSystem, Content: summary})
		}
		compacted = append(compacted, out[keepFrom:]...)
		result.DroppedMessages += keepFrom
		if keepFrom > 0 {
			result.CompactedMessages += keepFrom
		}
		out = compacted
	}
	return out
}

func compactToolMessageForBudget(content string, targetChars int) string {
	if len(content) <= targetChars {
		return content
	}
	if strings.Contains(content, "[model compacted terminal output]") || strings.Contains(content, "[model compacted html text]") {
		out, _ := truncateMiddle(content, targetChars)
		return out
	}
	out, _ := truncateMiddle(content, targetChars)
	return out
}

func dropStaleAssistantMessages(messages []agentcore.Message, recentTurns int) ([]agentcore.Message, int) {
	if recentTurns <= 0 {
		recentTurns = 8
	}
	keepFrom := maxInt(0, len(messages)-recentTurns*2)
	out := make([]agentcore.Message, 0, len(messages))
	dropped := 0
	for i, msg := range messages {
		if i < keepFrom && msg.Role == agentcore.RoleAssistant && len(msg.ToolCalls) == 0 {
			dropped++
			continue
		}
		out = append(out, msg)
	}
	return out, dropped
}

func taskEvidenceSummary(state session.State, taskID string) string {
	task := state.TaskByID(taskID)
	sessionSummary := renderSessionSummaryContext(state.Summary)
	if task == nil && sessionSummary == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("Compacted prior task context:\n")
	if sessionSummary != "" {
		b.WriteString(sessionSummary)
		b.WriteString("\n")
	}
	if task == nil {
		return strings.TrimSpace(b.String())
	}
	if text := strings.TrimSpace(task.Summary); text != "" {
		b.WriteString("- summary: ")
		b.WriteString(text)
		b.WriteString("\n")
	}
	for _, step := range task.Steps {
		if !step.Accepted && step.Status != "success" {
			continue
		}
		if strings.TrimSpace(step.Tool) == "" && strings.TrimSpace(step.Summary) == "" {
			continue
		}
		b.WriteString("- tool evidence")
		if step.Tool != "" {
			b.WriteString(" ")
			b.WriteString(step.Tool)
		}
		if step.Summary != "" {
			b.WriteString(": ")
			b.WriteString(step.Summary)
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func rawRefsFromMessage(msg agentcore.Message) []string {
	var refs []string
	if msg.Content != "" {
		refs = append(refs, rawRefsFromText(msg.Content)...)
	}
	return refs
}

func rawRefsFromText(text string) []string {
	var refs []string
	for _, field := range strings.Fields(text) {
		field = strings.Trim(field, "[]();,.")
		if strings.HasPrefix(field, "raw_ref=tool-result:") {
			refs = append(refs, strings.TrimPrefix(field, "raw_ref="))
		} else if strings.HasPrefix(field, "tool-result:") {
			refs = append(refs, field)
		}
	}
	return refs
}

func estimateModelInputTokens(system string, messages []agentcore.Message, tools []agentcore.Tool) int {
	chars := len(system)
	for _, msg := range messages {
		chars += len(msg.Content) + 8
		for _, part := range msg.Parts {
			chars += len(part.Text) + len(part.URI) + len(part.MimeType) + len(part.Name) + 12
		}
		for _, call := range msg.ToolCalls {
			chars += len(call.Name) + len(call.ID) + estimatedJSONChars(call.Args) + 16
		}
	}
	for _, tool := range tools {
		chars += len(tool.Name()) + len(tool.Description()) + estimatedJSONChars(tool.Schema()) + 32
		contract := agentcore.ContractFor(tool)
		chars += len(contract.WhenToUse) + len(contract.WhenNotToUse) + len(contract.OutputContract) + len(contract.Evidence) + len(contract.Acceptance) + len(contract.ConfirmationBoundary)
	}
	return maxInt(1, (chars+3)/4)
}

func estimatedJSONChars(value any) int {
	data, err := json.Marshal(value)
	if err != nil {
		return len(fmt.Sprint(value))
	}
	return len(data)
}

func writeContextBudgetTrace(trace *traceRecorder, cfg config.ContextBudgetConfig, result contextBudgetResult) {
	if trace == nil || !cfg.TraceTelemetryValue() {
		return
	}
	_ = trace.write(map[string]any{
		"type":                   "context_budget_estimated",
		"estimated_input_tokens": result.BeforeInputTokens,
		"soft_limit_tokens":      result.SoftLimitTokens,
		"hard_limit_tokens":      result.HardLimitTokens,
		"context_window_tokens":  result.ContextWindowTokens,
		"max_output_tokens":      result.MaxOutputTokens,
		"visible_tools":          result.VisibleTools,
		"hidden_tools":           result.HiddenTools,
		"tools":                  result.ToolNames,
	})
	if len(result.TrimmedTools) > 0 {
		_ = trace.write(map[string]any{
			"type":          "context_budget_trimmed",
			"trimmed_tools": result.TrimmedTools,
			"reason":        "visible_tool_budget",
		})
	}
	if len(result.NonDefaultExposed) > 0 {
		_ = trace.write(map[string]any{
			"type":                "context_budget_non_default_exposed",
			"non_default_exposed": result.NonDefaultExposed,
		})
	}
	if len(result.MissingContractTools) > 0 {
		_ = trace.write(map[string]any{
			"type":                   "context_budget_missing_contract_tools",
			"missing_contract_tools": result.MissingContractTools,
		})
	}
	if result.Compacted {
		_ = trace.write(map[string]any{
			"type":                   "context_budget_compacted",
			"estimated_input_tokens": result.EstimatedInputTokens,
			"soft_limit_tokens":      result.SoftLimitTokens,
			"hard_limit_tokens":      result.HardLimitTokens,
			"dropped_messages":       result.DroppedMessages,
			"compacted_messages":     result.CompactedMessages,
			"compacted_tool_results": result.CompactedToolResults,
			"raw_refs":               uniqueStrings(result.RawRefs),
			"saved_estimated_tokens": result.SavedEstimatedTokens,
		})
	}
	if result.HardLimitExceeded {
		_ = trace.write(map[string]any{
			"type":                   "context_budget_hard_limit",
			"estimated_input_tokens": result.EstimatedInputTokens,
			"hard_limit_tokens":      result.HardLimitTokens,
			"error":                  result.HardLimitExceededText,
		})
	}
}

func usageFromBudgetResults(results []contextBudgetResult) session.Usage {
	var usage session.Usage
	for _, result := range results {
		usage.EstimatedInputTokens += result.EstimatedInputTokens
		usage.SavedEstimatedTokens += result.SavedEstimatedTokens
		usage.CompactedMessages += result.CompactedMessages
		usage.CompactedToolResults += result.CompactedToolResults
	}
	return usage
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

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
