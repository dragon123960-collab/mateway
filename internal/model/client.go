package model

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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
	Vision       []Client
	SystemPrompt string
}

func NewAgentModel(cfg config.ModelConfig) AgentModel {
	return AgentModel{
		Client:       NewClient(cfg),
		SystemPrompt: defaultSystemPrompt(),
	}
}

func NewFallbackAgentModel(configs []config.ModelConfig) AgentModel {
	return NewRoutedAgentModel(configs, nil)
}

func NewRoutedAgentModel(configs []config.ModelConfig, visionConfigs []config.ModelConfig) AgentModel {
	if len(configs) == 0 {
		return NewAgentModel(config.ModelConfig{})
	}
	clients := make([]Client, 0, len(configs)-1)
	for _, cfg := range configs[1:] {
		clients = append(clients, NewClient(cfg))
	}
	visionClients := make([]Client, 0, len(visionConfigs))
	for _, cfg := range visionConfigs {
		visionClients = append(visionClients, NewClient(cfg))
	}
	return AgentModel{
		Client:       NewClient(configs[0]),
		Fallbacks:    clients,
		Vision:       visionClients,
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
			messages = append(messages, Message{Role: "user", Content: msg.Content, Parts: msg.Parts})
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
	if calls := parseToolCallText(text); len(calls) > 0 {
		return agentcore.Message{Role: agentcore.RoleAssistant, Content: text, ToolCalls: calls, Usage: usagePtr(result.Usage)}, nil
	}
	return agentcore.Message{Role: agentcore.RoleAssistant, Content: text, Usage: usagePtr(result.Usage)}, nil
}

func (m AgentModel) generateWithFallbacks(ctx context.Context, system string, messages []Message) (GenerateResult, error) {
	var errors []string
	if messagesRequireImage(messages) && !m.Client.Config.SupportsModality("image") {
		if result, err := m.generateViaVision(ctx, system, messages); err == nil {
			return result, nil
		} else {
			errors = append(errors, "vision: "+err.Error())
		}
	}
	result, err := m.Client.Generate(ctx, system, messages)
	if err == nil {
		return result, nil
	}
	errors = append(errors, m.Client.Config.Name+": "+err.Error())
	for _, client := range m.Fallbacks {
		if messagesRequireImage(messages) && !client.Config.SupportsModality("image") {
			continue
		}
		result, fallbackErr := client.Generate(ctx, system, messages)
		if fallbackErr == nil {
			return result, nil
		}
		errors = append(errors, client.Config.Name+": "+fallbackErr.Error())
	}
	return GenerateResult{}, fmt.Errorf("all models failed: %s", strings.Join(errors, " | "))
}

func (m AgentModel) generateViaVision(ctx context.Context, system string, messages []Message) (GenerateResult, error) {
	vision, ok := m.firstImageCapableClient()
	if !ok {
		return GenerateResult{}, fmt.Errorf("image input requires an image-capable model; configure a fallback or role model with modalities including image")
	}
	converted, err := describeImageMessages(ctx, vision, messages)
	if err != nil {
		return GenerateResult{}, err
	}
	return m.Client.Generate(ctx, system, converted)
}

func (m AgentModel) firstImageCapableClient() (Client, bool) {
	if m.Client.Config.SupportsModality("image") {
		return m.Client, true
	}
	for _, client := range m.Vision {
		if client.Config.SupportsModality("image") {
			return client, true
		}
	}
	for _, client := range m.Fallbacks {
		if client.Config.SupportsModality("image") {
			return client, true
		}
	}
	return Client{}, false
}

type Message struct {
	Role    string                  `json:"role"`
	Content string                  `json:"content"`
	Parts   []agentcore.MessagePart `json:"parts,omitempty"`
}

func messagesRequireImage(messages []Message) bool {
	for _, msg := range messages {
		for _, part := range msg.Parts {
			if part.Type == agentcore.PartImage {
				return true
			}
		}
	}
	return false
}

func describeImageMessages(ctx context.Context, vision Client, messages []Message) ([]Message, error) {
	converted := make([]Message, 0, len(messages))
	for _, msg := range messages {
		if !messageHasImage(msg) {
			converted = append(converted, msg)
			continue
		}
		prompt := strings.TrimSpace(msg.Content)
		if prompt == "" {
			prompt = "Describe the image in detail for a text-only reasoning model. Include visible text, objects, layout, and any uncertainty."
		} else {
			prompt = "User text: " + prompt + "\n\nDescribe the attached image in detail for a text-only reasoning model. Include visible text, objects, layout, and any uncertainty relevant to the user text."
		}
		visionMsg := Message{Role: "user", Content: prompt, Parts: partsWithoutText(msg.Parts)}
		result, err := vision.Generate(ctx, "You are a vision model. Return a concise but complete image description for downstream text reasoning.", []Message{visionMsg})
		if err != nil {
			return nil, err
		}
		text := strings.TrimSpace(msg.Content)
		if text != "" {
			text += "\n\n"
		}
		text += "Image description:\n" + strings.TrimSpace(result.Text)
		converted = append(converted, Message{Role: msg.Role, Content: text})
	}
	return converted, nil
}

func messageHasImage(msg Message) bool {
	for _, part := range msg.Parts {
		if part.Type == agentcore.PartImage {
			return true
		}
	}
	return false
}

func partsWithoutText(parts []agentcore.MessagePart) []agentcore.MessagePart {
	out := make([]agentcore.MessagePart, 0, len(parts))
	for _, part := range parts {
		if part.Type == agentcore.PartText {
			continue
		}
		out = append(out, part)
	}
	return out
}

type GenerateResult struct {
	Text  string
	Usage agentcore.Usage
}

func defaultSystemPrompt() string {
	return strings.TrimSpace(`You are Mateway, a concise tool-using assistant.

When you can answer directly, answer directly.
When you need tools, emit one or more tool call blocks exactly like:

[TOOL_CALL]
{"id":"call_1","name":"tool.name","args":{"key":"value"}}
[/TOOL_CALL]

Use tools sparingly. Do not expose raw tool planning unless calling tools.`)
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

func parseToolCallText(text string) []agentcore.ToolCall {
	matches := toolCallBlockPattern.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	var calls []agentcore.ToolCall
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		raw := strings.TrimSpace(match[1])
		calls = append(calls, parseToolCallPayloads(raw, len(calls)+1)...)
	}
	return calls
}

func parseToolCallPayloads(raw string, startIndex int) []agentcore.ToolCall {
	decoder := json.NewDecoder(strings.NewReader(raw))
	var calls []agentcore.ToolCall
	for {
		var payload struct {
			ID   string         `json:"id"`
			Name string         `json:"name"`
			Args map[string]any `json:"args"`
		}
		if err := decoder.Decode(&payload); err != nil {
			break
		}
		payload.ID = strings.TrimSpace(payload.ID)
		payload.Name = strings.TrimSpace(payload.Name)
		if payload.Name == "" {
			continue
		}
		if payload.ID == "" {
			payload.ID = fmt.Sprintf("call_%d", startIndex+len(calls))
		}
		if payload.Args == nil {
			payload.Args = map[string]any{}
		}
		calls = append(calls, agentcore.ToolCall{ID: payload.ID, Name: payload.Name, Args: payload.Args})
	}
	return calls
}

var toolCallBlockPattern = regexp.MustCompile(`(?is)\[\s*TOOL_CALL\s*\](.*?)\[\s*/\s*TOOL_CALL\s*\]`)

func (c Client) Generate(ctx context.Context, system string, messages []Message) (GenerateResult, error) {
	if messagesRequireImage(messages) && !c.Config.SupportsModality("image") {
		return GenerateResult{}, fmt.Errorf("model %s does not support image input", c.Config.Name)
	}
	switch strings.ToLower(strings.TrimSpace(c.Config.API)) {
	case "", "anthropic":
		return c.generateAnthropic(ctx, system, messages)
	case "openai":
		return c.generateOpenAI(ctx, system, messages)
	case "openai_chat":
		return c.generateOpenAIChat(ctx, system, messages)
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
		"max_tokens": c.Config.MaxTokensValue(),
		"messages":   anthropicMessages(messages),
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
		"max_output_tokens": c.Config.MaxTokensValue(),
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

func (c Client) generateOpenAIChat(ctx context.Context, system string, messages []Message) (GenerateResult, error) {
	endpoint, err := endpointWithSuffix(c.Config.APIBase, "/chat/completions")
	if err != nil {
		return GenerateResult{}, err
	}
	body := map[string]any{
		"model":      c.Config.Model,
		"messages":   openAIChatMessages(system, messages),
		"max_tokens": c.Config.MaxTokensValue(),
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
			"content": openAIContent(msg),
		})
	}
	return input
}

func openAIChatMessages(system string, messages []Message) []map[string]any {
	out := make([]map[string]any, 0, len(messages)+1)
	if strings.TrimSpace(system) != "" {
		out = append(out, map[string]any{"role": "system", "content": system})
	}
	for _, msg := range messages {
		role := strings.TrimSpace(msg.Role)
		if role == "" {
			role = "user"
		}
		out = append(out, map[string]any{
			"role":    role,
			"content": openAIChatContent(msg),
		})
	}
	return out
}

func openAIChatContent(msg Message) any {
	content := openAIContent(msg)
	blocks, ok := content.([]map[string]any)
	if !ok {
		return content
	}
	out := make([]map[string]any, 0, len(blocks))
	for _, block := range blocks {
		switch block["type"] {
		case "input_text":
			out = append(out, map[string]any{"type": "text", "text": block["text"]})
		case "input_image":
			out = append(out, map[string]any{
				"type":      "image_url",
				"image_url": map[string]any{"url": block["image_url"]},
			})
		}
	}
	if len(out) == 0 {
		return msg.Content
	}
	return out
}

func anthropicMessages(messages []Message) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, msg := range messages {
		role := strings.TrimSpace(msg.Role)
		if role == "" {
			role = "user"
		}
		out = append(out, map[string]any{
			"role":    role,
			"content": anthropicContent(msg),
		})
	}
	return out
}

func anthropicContent(msg Message) any {
	if len(msg.Parts) == 0 {
		return msg.Content
	}
	content := make([]map[string]any, 0, len(msg.Parts)+1)
	if strings.TrimSpace(msg.Content) != "" && !partsContainText(msg.Parts) {
		content = append(content, map[string]any{"type": "text", "text": msg.Content})
	}
	for _, part := range msg.Parts {
		switch part.Type {
		case agentcore.PartText:
			if strings.TrimSpace(part.Text) != "" {
				content = append(content, map[string]any{"type": "text", "text": part.Text})
			}
		case agentcore.PartImage:
			if block, err := anthropicImageBlock(part); err == nil {
				content = append(content, block)
			}
		}
	}
	if len(content) == 0 {
		return msg.Content
	}
	return content
}

func openAIContent(msg Message) any {
	if len(msg.Parts) == 0 {
		return msg.Content
	}
	content := make([]map[string]any, 0, len(msg.Parts)+1)
	if strings.TrimSpace(msg.Content) != "" && !partsContainText(msg.Parts) {
		content = append(content, map[string]any{"type": "input_text", "text": msg.Content})
	}
	for _, part := range msg.Parts {
		switch part.Type {
		case agentcore.PartText:
			if strings.TrimSpace(part.Text) != "" {
				content = append(content, map[string]any{"type": "input_text", "text": part.Text})
			}
		case agentcore.PartImage:
			if url := imageURL(part); strings.TrimSpace(url) != "" {
				content = append(content, map[string]any{"type": "input_image", "image_url": url})
			}
		}
	}
	if len(content) == 0 {
		return msg.Content
	}
	return content
}

func partsContainText(parts []agentcore.MessagePart) bool {
	for _, part := range parts {
		if part.Type == agentcore.PartText && strings.TrimSpace(part.Text) != "" {
			return true
		}
	}
	return false
}

func anthropicImageBlock(part agentcore.MessagePart) (map[string]any, error) {
	data, mimeType, err := imageData(part)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"type": "image",
		"source": map[string]any{
			"type":       "base64",
			"media_type": mimeType,
			"data":       data,
		},
	}, nil
}

func imageURL(part agentcore.MessagePart) string {
	uri := strings.TrimSpace(part.URI)
	if strings.HasPrefix(uri, "http://") || strings.HasPrefix(uri, "https://") || strings.HasPrefix(uri, "data:") {
		return uri
	}
	data, mimeType, err := imageData(part)
	if err != nil {
		return ""
	}
	return "data:" + mimeType + ";base64," + data
}

func imageData(part agentcore.MessagePart) (string, string, error) {
	uri := strings.TrimSpace(part.URI)
	if strings.HasPrefix(uri, "data:") {
		prefix, data, ok := strings.Cut(uri, ",")
		if !ok {
			return "", "", fmt.Errorf("invalid data uri")
		}
		mimeType := strings.TrimPrefix(strings.TrimSuffix(prefix, ";base64"), "data:")
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		return data, mimeType, nil
	}
	path := strings.TrimPrefix(uri, "file://")
	if path == "" {
		return "", "", fmt.Errorf("image uri is empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	mimeType := strings.TrimSpace(part.MimeType)
	if mimeType == "" {
		mimeType = mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	}
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	return base64.StdEncoding.EncodeToString(data), mimeType, nil
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
	case "openai", "openai_chat":
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
