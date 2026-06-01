package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/config"
)

type Client struct {
	Config config.ModelConfig
	HTTP   *http.Client
}

func NewClient(cfg config.ModelConfig) Client {
	return Client{
		Config: cfg,
		HTTP:   &http.Client{Timeout: 90 * time.Second},
	}
}

type AgentModel struct {
	Client       Client
	Fallbacks    []Client
	SystemPrompt string
}

func NewAgentModel(cfg config.ModelConfig) AgentModel {
	return AgentModel{
		Client:       NewClient(cfg),
		SystemPrompt: defaultSystemPrompt(),
	}
}

func NewFallbackAgentModel(configs []config.ModelConfig) AgentModel {
	if len(configs) == 0 {
		return NewAgentModel(config.ModelConfig{})
	}
	clients := make([]Client, 0, len(configs)-1)
	for _, cfg := range configs[1:] {
		clients = append(clients, NewClient(cfg))
	}
	return AgentModel{
		Client:       NewClient(configs[0]),
		Fallbacks:    clients,
		SystemPrompt: defaultSystemPrompt(),
	}
}

func (m AgentModel) Next(ctx context.Context, agentCtx agentcore.Context) (agentcore.Message, error) {
	messages := make([]Message, 0, len(agentCtx.Messages))
	var systemSections []string
	if base := strings.TrimSpace(agentCtx.SystemPrompt); base != "" {
		systemSections = append(systemSections, base)
	}
	for _, msg := range agentCtx.Messages {
		switch msg.Role {
		case agentcore.RoleSystem:
			if text := strings.TrimSpace(msg.Content); text != "" {
				systemSections = append(systemSections, text)
			}
		case agentcore.RoleTool:
			messages = append(messages, Message{Role: "user", Content: "Tool result (" + msg.ToolCallID + "):\n" + msg.Content})
		case agentcore.RoleAssistant:
			if strings.TrimSpace(msg.Content) != "" {
				messages = append(messages, Message{Role: "assistant", Content: msg.Content})
			}
		default:
			messages = append(messages, Message{Role: "user", Content: msg.Content})
		}
	}
	systemPrompt := strings.TrimSpace(m.SystemPrompt)
	if len(systemSections) > 0 {
		systemPrompt = strings.TrimSpace(systemPrompt + "\n\n" + strings.Join(systemSections, "\n\n"))
	}
	result, err := m.generateWithFallbacks(ctx, buildSystemPrompt(systemPrompt, agentCtx.Tools), messages)
	if err != nil {
		return agentcore.Message{}, err
	}
	text := strings.TrimSpace(result.Text)
	if call, ok := parseToolCallText(text); ok {
		return agentcore.Message{Role: agentcore.RoleAssistant, Content: text, ToolCalls: []agentcore.ToolCall{call}, Usage: usagePtr(result.Usage)}, nil
	}
	return agentcore.Message{Role: agentcore.RoleAssistant, Content: text, Usage: usagePtr(result.Usage)}, nil
}

func (m AgentModel) generateWithFallbacks(ctx context.Context, system string, messages []Message) (GenerateResult, error) {
	result, err := m.Client.Generate(ctx, system, messages)
	if err == nil {
		return result, nil
	}
	errors := []string{m.Client.Config.Name + ": " + err.Error()}
	for _, client := range m.Fallbacks {
		result, fallbackErr := client.Generate(ctx, system, messages)
		if fallbackErr == nil {
			return result, nil
		}
		errors = append(errors, client.Config.Name+": "+fallbackErr.Error())
	}
	return GenerateResult{}, fmt.Errorf("all models failed: %s", strings.Join(errors, " | "))
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type GenerateResult struct {
	Text  string
	Usage agentcore.Usage
}

func defaultSystemPrompt() string {
	return strings.TrimSpace(`You are Mateway, a concise tool-using assistant.

When you can answer directly, answer directly.
When you need a tool, emit a single tool call block exactly like:

[TOOL_CALL]
{"id":"call_1","name":"tool.name","args":{"key":"value"}}
[/TOOL_CALL]

Use tools sparingly. Do not expose raw tool planning unless calling a tool.`)
}

func buildSystemPrompt(base string, tools []agentcore.Tool) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(base))
	b.WriteString("\n\n")
	b.WriteString(currentDatePrompt())
	if len(tools) == 0 {
		return b.String()
	}
	b.WriteString("\n\nAvailable tools:\n")
	for _, tool := range tools {
		contract := agentcore.ContractFor(tool)
		b.WriteString("- ")
		b.WriteString(tool.Name())
		b.WriteString(": ")
		b.WriteString(tool.Description())
		if required := tool.Schema().Required; len(required) > 0 {
			b.WriteString(" required args: ")
			b.WriteString(strings.Join(required, ", "))
		}
		b.WriteString("\n")
		writeContractLine(&b, "  Use when: ", contract.WhenToUse)
		writeContractLine(&b, "  Do not use when: ", contract.WhenNotToUse)
		writeContractLine(&b, "  Output contract: ", contract.OutputContract)
		writeContractLine(&b, "  Evidence: ", contract.Evidence)
		writeContractLine(&b, "  Acceptance: ", contract.Acceptance)
		writeContractLine(&b, "  Soft failure signals: ", strings.Join(contract.SoftFailureSignals, "; "))
		writeContractLine(&b, "  Parallel mode: ", contract.ParallelMode)
		writeContractLine(&b, "  Reuse policy: ", contract.ReusePolicy)
		writeContractLine(&b, "  Confirmation boundary: ", contract.ConfirmationBoundary)
	}
	b.WriteString("\nIf reading a local file, use file.read exactly. Do not invent tool names such as Read.")
	return b.String()
}

func currentDatePrompt() string {
	now := time.Now().In(time.FixedZone("Asia/Shanghai", 8*60*60))
	return fmt.Sprintf("Current date: %s. Current time: %s. Timezone: Asia/Shanghai. Treat any date before %s as historical, not current. For weather, news, prices, schedules, or other time-sensitive answers, use available tools and verify the result date matches today before presenting it as current.", now.Format("2006-01-02"), now.Format("15:04"), now.Format("2006-01-02"))
}

func writeContractLine(b *strings.Builder, label, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	b.WriteString(label)
	b.WriteString(value)
	b.WriteString("\n")
}

func parseToolCallText(text string) (agentcore.ToolCall, bool) {
	parts := toolCallBlockPattern.FindStringSubmatchIndex(text)
	if len(parts) < 4 {
		return agentcore.ToolCall{}, false
	}
	raw := strings.TrimSpace(text[parts[2]:parts[3]])
	var payload struct {
		ID   string         `json:"id"`
		Name string         `json:"name"`
		Args map[string]any `json:"args"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return agentcore.ToolCall{}, false
	}
	payload.ID = strings.TrimSpace(payload.ID)
	payload.Name = strings.TrimSpace(payload.Name)
	if payload.Name == "" {
		return agentcore.ToolCall{}, false
	}
	if payload.ID == "" {
		payload.ID = "call_1"
	}
	if payload.Args == nil {
		payload.Args = map[string]any{}
	}
	return agentcore.ToolCall{ID: payload.ID, Name: payload.Name, Args: payload.Args}, true
}

var toolCallBlockPattern = regexp.MustCompile(`(?is)\[\s*TOOL_CALL\s*\](.*?)\[\s*/\s*TOOL_CALL\s*\]`)

func (c Client) Generate(ctx context.Context, system string, messages []Message) (GenerateResult, error) {
	switch strings.ToLower(strings.TrimSpace(c.Config.API)) {
	case "", "anthropic":
		return c.generateAnthropic(ctx, system, messages)
	case "openai":
		return c.generateOpenAI(ctx, system, messages)
	default:
		return GenerateResult{}, fmt.Errorf("unsupported model api %q for %s", c.Config.API, c.Config.Name)
	}
}

func (c Client) generateAnthropic(ctx context.Context, system string, messages []Message) (GenerateResult, error) {
	key := c.Config.ResolvedAPIKey()
	if key == "" {
		return GenerateResult{}, fmt.Errorf("model api key is empty for %s", c.Config.Name)
	}
	endpoint, err := endpointWithSuffix(c.Config.APIBase, "/v1/messages")
	if err != nil {
		return GenerateResult{}, err
	}
	body := map[string]any{
		"model":      c.Config.Model,
		"max_tokens": 4096,
		"messages":   messages,
	}
	if strings.TrimSpace(system) != "" {
		body["system"] = system
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return GenerateResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return GenerateResult{}, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("x-api-key", key)
	req.Header.Set("authorization", "Bearer "+key)
	return c.doGenerate(req)
}

func (c Client) generateOpenAI(ctx context.Context, system string, messages []Message) (GenerateResult, error) {
	endpoint, err := endpointWithSuffix(c.Config.APIBase, "/responses")
	if err != nil {
		return GenerateResult{}, err
	}
	body := map[string]any{
		"model":             c.Config.Model,
		"input":             openAIResponsesInput(system, messages),
		"max_output_tokens": 4096,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return GenerateResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return GenerateResult{}, err
	}
	req.Header.Set("content-type", "application/json")
	if key := strings.TrimSpace(c.Config.ResolvedAPIKey()); key != "" {
		req.Header.Set("authorization", "Bearer "+key)
	}
	return c.doGenerate(req)
}

func openAIResponsesInput(system string, messages []Message) []map[string]any {
	input := make([]map[string]any, 0, len(messages)+1)
	if strings.TrimSpace(system) != "" {
		input = append(input, map[string]any{
			"role":    "system",
			"content": system,
		})
	}
	for _, msg := range messages {
		role := strings.TrimSpace(msg.Role)
		if role == "" {
			role = "user"
		}
		input = append(input, map[string]any{
			"role":    role,
			"content": msg.Content,
		})
	}
	return input
}

func (c Client) doGenerate(req *http.Request) (GenerateResult, error) {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return GenerateResult{}, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return GenerateResult{}, fmt.Errorf("model request failed: status=%d body=%s", resp.StatusCode, truncateForError(string(data)))
	}
	switch strings.ToLower(strings.TrimSpace(c.Config.API)) {
	case "", "anthropic":
		result, err := parseAnthropicResult(data)
		return finishGenerate(c.Config, result, err)
	case "openai":
		result, err := parseOpenAIResult(data)
		return finishGenerate(c.Config, result, err)
	default:
		return GenerateResult{}, fmt.Errorf("unsupported model api %q for %s", c.Config.API, c.Config.Name)
	}
}

func finishGenerate(cfg config.ModelConfig, result GenerateResult, err error) (GenerateResult, error) {
	if err != nil {
		return GenerateResult{}, err
	}
	text := result.Text
	if cfg.StripReasoning {
		text = stripReasoning(text)
	}
	if strings.TrimSpace(text) == "" {
		return GenerateResult{}, fmt.Errorf("model returned empty text")
	}
	result.Text = strings.TrimSpace(text)
	result.Usage.Provider = strings.TrimSpace(cfg.API)
	if result.Usage.Provider == "" {
		result.Usage.Provider = "anthropic"
	}
	result.Usage.Model = firstNonEmptyString(cfg.Model, cfg.Name)
	return result, nil
}

func endpointWithSuffix(apiBase, suffix string) (string, error) {
	apiBase = strings.TrimRight(strings.TrimSpace(apiBase), "/")
	if apiBase == "" {
		return "", fmt.Errorf("model api_base is required")
	}
	parsed, err := url.Parse(apiBase)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid model api_base %q", apiBase)
	}
	if strings.HasSuffix(parsed.Path, suffix) {
		return apiBase, nil
	}
	return apiBase + suffix, nil
}

func parseAnthropicText(data []byte) (string, error) {
	result, err := parseAnthropicResult(data)
	return result.Text, err
}

func parseAnthropicResult(data []byte) (GenerateResult, error) {
	var payload struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return GenerateResult{}, fmt.Errorf("parse model response: %w", err)
	}
	if payload.Error != nil {
		return GenerateResult{}, fmt.Errorf("model error %s: %s", payload.Error.Type, payload.Error.Message)
	}
	var parts []string
	for _, item := range payload.Content {
		if item.Text != "" {
			parts = append(parts, item.Text)
		}
	}
	usage := agentcore.Usage{InputTokens: payload.Usage.InputTokens, OutputTokens: payload.Usage.OutputTokens}
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	return GenerateResult{Text: strings.TrimSpace(strings.Join(parts, "\n")), Usage: usage}, nil
}

func parseOpenAIText(data []byte) (string, error) {
	result, err := parseOpenAIResult(data)
	return result.Text, err
}

func parseOpenAIResult(data []byte) (GenerateResult, error) {
	if result := parseOpenAIResponsesResult(data); strings.TrimSpace(result.Text) != "" {
		return result, nil
	}
	var payload struct {
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"message"`
			Text string `json:"text"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return GenerateResult{}, fmt.Errorf("parse model response: %w", err)
	}
	if payload.Error != nil {
		return GenerateResult{}, fmt.Errorf("model error %s: %s", payload.Error.Type, payload.Error.Message)
	}
	var parts []string
	for _, choice := range payload.Choices {
		if choice.Text != "" {
			parts = append(parts, choice.Text)
			continue
		}
		switch content := choice.Message.Content.(type) {
		case string:
			if strings.TrimSpace(content) != "" {
				parts = append(parts, content)
			}
		case []any:
			for _, item := range content {
				if m, ok := item.(map[string]any); ok {
					if text, ok := m["text"].(string); ok && strings.TrimSpace(text) != "" {
						parts = append(parts, text)
					}
				}
			}
		}
	}
	usage := agentcore.Usage{InputTokens: payload.Usage.PromptTokens, OutputTokens: payload.Usage.CompletionTokens, TotalTokens: payload.Usage.TotalTokens}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}
	return GenerateResult{Text: strings.TrimSpace(strings.Join(parts, "\n")), Usage: usage}, nil
}

func parseOpenAIResponsesText(data []byte) string {
	return parseOpenAIResponsesResult(data).Text
}

func parseOpenAIResponsesResult(data []byte) GenerateResult {
	var payload struct {
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		OutputText string `json:"output_text"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return GenerateResult{}
	}
	usage := agentcore.Usage{InputTokens: payload.Usage.InputTokens, OutputTokens: payload.Usage.OutputTokens, TotalTokens: payload.Usage.TotalTokens}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}
	if strings.TrimSpace(payload.OutputText) != "" {
		return GenerateResult{Text: strings.TrimSpace(payload.OutputText), Usage: usage}
	}
	var parts []string
	for _, item := range payload.Output {
		for _, content := range item.Content {
			if strings.TrimSpace(content.Text) != "" {
				parts = append(parts, content.Text)
			}
		}
	}
	return GenerateResult{Text: strings.TrimSpace(strings.Join(parts, "\n")), Usage: usage}
}

func usagePtr(usage agentcore.Usage) *agentcore.Usage {
	if usage.Provider == "" && usage.Model == "" && usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.TotalTokens == 0 {
		return nil
	}
	return &usage
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func stripReasoning(text string) string {
	for {
		start := strings.Index(text, "<think>")
		end := strings.Index(text, "</think>")
		if start < 0 || end < 0 || end < start {
			return strings.TrimSpace(text)
		}
		text = text[:start] + text[end+len("</think>"):]
	}
}

func truncateForError(text string) string {
	text = strings.TrimSpace(text)
	if len(text) <= 600 {
		return text
	}
	return text[:300] + "\n...[truncated]...\n" + text[len(text)-300:]
}
