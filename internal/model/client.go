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
	"sort"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/util"
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
	var systemSections []string
	if base := strings.TrimSpace(agentCtx.SystemPrompt); base != "" {
		systemSections = append(systemSections, base)
	}
	for _, msg := range agentCtx.Messages {
		if msg.Role == agentcore.RoleSystem {
			if text := strings.TrimSpace(msg.Content); text != "" {
				systemSections = append(systemSections, text)
			}
		}
	}
	systemPrompt := strings.TrimSpace(m.SystemPrompt)
	if len(systemSections) > 0 {
		systemPrompt = strings.TrimSpace(systemPrompt + "\n\n" + strings.Join(systemSections, "\n\n"))
	}
	result, err := m.generateWithFallbacks(ctx, systemPrompt, agentCtx.Messages, agentCtx.Tools)
	if err != nil {
		return agentcore.Message{}, err
	}
	text := strings.TrimSpace(result.Text)
	calls := result.ToolCalls
	if len(calls) == 0 {
		calls = parseToolCallText(text)
	}
	if len(calls) > 0 {
		return agentcore.Message{Role: agentcore.RoleAssistant, Content: text, ToolCalls: calls, Usage: usagePtr(result.Usage)}, nil
	}
	return agentcore.Message{Role: agentcore.RoleAssistant, Content: text, Usage: usagePtr(result.Usage)}, nil
}

func (m AgentModel) generateWithFallbacks(ctx context.Context, system string, agentMessages []agentcore.Message, tools []agentcore.Tool) (GenerateResult, error) {
	var errors []string
	nativeMessages := modelMessagesNative(agentMessages)
	textMessages := modelMessagesText(agentMessages)
	textOnlyMessages := stripImageParts(textMessages)
	if messagesRequireImage(nativeMessages) && !m.Client.Config.SupportsModality("image") {
		if result, err := m.generateViaVision(ctx, buildTextSystemPrompt(system, tools), textMessages); err == nil {
			return result, nil
		} else {
			errors = append(errors, "vision: "+err.Error())
		}
	}
	result, err := m.generateForClient(ctx, m.Client, system, nativeMessages, textOnlyMessages, tools)
	if err == nil {
		return result, nil
	}
	errors = append(errors, m.Client.Config.Name+": "+err.Error())
	for _, client := range m.Fallbacks {
		if messagesRequireImage(nativeMessages) && !client.Config.SupportsModality("image") {
			continue
		}
		result, fallbackErr := m.generateForClient(ctx, client, system, nativeMessages, textMessages, tools)
		if fallbackErr == nil {
			return result, nil
		}
		errors = append(errors, client.Config.Name+": "+fallbackErr.Error())
	}
	return GenerateResult{}, fmt.Errorf("all models failed: %s", strings.Join(errors, " | "))
}

func (m AgentModel) generateForClient(ctx context.Context, client Client, system string, nativeMessages, textMessages []Message, tools []agentcore.Tool) (GenerateResult, error) {
	if len(tools) > 0 && client.SupportsNativeTools() {
		result, err := client.GenerateWithTools(ctx, buildNativeSystemPrompt(system, tools), nativeMessages, tools)
		if err == nil {
			return result, nil
		}
		textResult, textErr := client.Generate(ctx, buildTextSystemPrompt(system, tools), textMessages)
		if textErr == nil {
			return textResult, nil
		}
		return GenerateResult{}, fmt.Errorf("native tools failed: %v; text fallback failed: %v", err, textErr)
	}
	return client.Generate(ctx, buildTextSystemPrompt(system, tools), textMessages)
}

func stripImageParts(messages []Message) []Message {
	out := make([]Message, 0, len(messages))
	for _, msg := range messages {
		if !messageHasImage(msg) {
			out = append(out, msg)
			continue
		}
		copy := msg
		copy.Parts = nil
		for _, part := range msg.Parts {
			if part.Type != agentcore.PartImage {
				copy.Parts = append(copy.Parts, part)
			}
		}
		out = append(out, copy)
	}
	return out
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
	Role       string                  `json:"role"`
	Content    string                  `json:"content"`
	Parts      []agentcore.MessagePart `json:"parts,omitempty"`
	ToolCalls  []agentcore.ToolCall    `json:"tool_calls,omitempty"`
	ToolCallID string                  `json:"tool_call_id,omitempty"`
}

func modelMessagesNative(messages []agentcore.Message) []Message {
	out := make([]Message, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == agentcore.RoleSystem {
			continue
		}
		role := string(msg.Role)
		if role == "" {
			role = "user"
		}
		out = append(out, Message{
			Role:       role,
			Content:    msg.Content,
			Parts:      msg.Parts,
			ToolCalls:  append([]agentcore.ToolCall(nil), msg.ToolCalls...),
			ToolCallID: msg.ToolCallID,
		})
	}
	return out
}

func modelMessagesText(messages []agentcore.Message) []Message {
	out := make([]Message, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case agentcore.RoleSystem:
			continue
		case agentcore.RoleTool:
			out = append(out, Message{Role: "user", Content: "Tool result (" + msg.ToolCallID + "):\n" + msg.Content})
		case agentcore.RoleAssistant:
			if strings.TrimSpace(msg.Content) != "" {
				out = append(out, Message{Role: "assistant", Content: msg.Content})
			}
		default:
			out = append(out, Message{Role: "user", Content: msg.Content, Parts: msg.Parts})
		}
	}
	return out
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
	Text      string
	ToolCalls []agentcore.ToolCall
	Usage     agentcore.Usage
}

func defaultSystemPrompt() string {
	return strings.TrimSpace(`You are Mateway, a concise tool-using assistant.

When you can answer directly, answer directly.
Use tools sparingly. Do not expose raw tool planning unless calling tools.`)
}

func buildNativeSystemPrompt(base string, tools []agentcore.Tool) string {
	return buildSystemPrompt(base, tools, false)
}

func buildTextSystemPrompt(base string, tools []agentcore.Tool) string {
	return buildSystemPrompt(base, tools, true)
}

func buildSystemPrompt(base string, tools []agentcore.Tool, includeTextProtocol bool) string {
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
		if !includeTextProtocol {
			b.WriteString(" (native name: ")
			b.WriteString(toolAPIAlias(tool.Name()))
			b.WriteString(")")
		}
		b.WriteString(": ")
		b.WriteString(tool.Description())
		if required := tool.Schema().Required; len(required) > 0 {
			b.WriteString(" required args: ")
			b.WriteString(strings.Join(required, ", "))
		}
		if optional := optionalSchemaArgs(tool.Schema()); len(optional) > 0 {
			b.WriteString(" optional args: ")
			b.WriteString(strings.Join(optional, ", "))
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
	if includeTextProtocol {
		b.WriteString("\nIf reading a local file, use file.read exactly. Do not invent tool names such as Read.")
		b.WriteString("\n\nThis model/API may not support native tool calling. When you need tools, emit one or more tool call blocks exactly like:\n\n")
		b.WriteString("[TOOL_CALL]\n")
		b.WriteString("{\"id\":\"call_1\",\"name\":\"tool.name\",\"args\":{\"key\":\"value\"}}\n")
		b.WriteString("[/TOOL_CALL]")
	} else {
		b.WriteString("\nUse the provided native tool definitions when a tool is needed. Do not invent tool names such as Read.")
	}
	return b.String()
}

func currentDatePrompt() string {
	loc, timezone := config.TimezoneLocation("")
	now := time.Now().In(loc)
	return fmt.Sprintf("Current date: %s. Current time: %s. Timezone: %s. Treat any date before %s as historical, not current. For weather, news, prices, schedules, or other time-sensitive answers, use available tools and verify the result date matches today before presenting it as current.", now.Format("2006-01-02"), now.Format("15:04"), timezone, now.Format("2006-01-02"))
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
	var calls []agentcore.ToolCall
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		raw := strings.TrimSpace(match[1])
		calls = append(calls, parseToolCallPayloads(raw, len(calls)+1)...)
	}
	if len(calls) == 0 {
		calls = append(calls, parseNamedToolCallTags(text, len(calls)+1)...)
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
var namedToolCallTagPattern = regexp.MustCompile(`(?is)<\s*tool_call\s*>\s*([A-Za-z0-9_.-]+)\s*`)

func parseNamedToolCallTags(text string, startIndex int) []agentcore.ToolCall {
	matches := namedToolCallTagPattern.FindAllStringSubmatchIndex(text, -1)
	var calls []agentcore.ToolCall
	for _, match := range matches {
		if len(match) < 4 {
			continue
		}
		name := strings.TrimSpace(text[match[2]:match[3]])
		if name == "" {
			continue
		}
		var args map[string]any
		decoder := json.NewDecoder(strings.NewReader(text[match[1]:]))
		if err := decoder.Decode(&args); err != nil {
			continue
		}
		if args == nil {
			args = map[string]any{}
		}
		calls = append(calls, agentcore.ToolCall{
			ID:   fmt.Sprintf("call_%d", startIndex+len(calls)),
			Name: name,
			Args: args,
		})
	}
	return calls
}

func (c Client) Generate(ctx context.Context, system string, messages []Message) (GenerateResult, error) {
	if messagesRequireImage(messages) && !c.Config.SupportsModality("image") {
		return GenerateResult{}, fmt.Errorf("model %s does not support image input", c.Config.Name)
	}
	switch strings.ToLower(strings.TrimSpace(c.Config.API)) {
	case "", "anthropic":
		return c.generateAnthropic(ctx, system, messages, nil)
	case "openai":
		return c.generateOpenAI(ctx, system, messages)
	case "openai_chat":
		return c.generateOpenAIChat(ctx, system, messages, nil)
	default:
		return GenerateResult{}, fmt.Errorf("unsupported model api %q for %s", c.Config.API, c.Config.Name)
	}
}

func (c Client) GenerateWithTools(ctx context.Context, system string, messages []Message, tools []agentcore.Tool) (GenerateResult, error) {
	if messagesRequireImage(messages) && !c.Config.SupportsModality("image") {
		return GenerateResult{}, fmt.Errorf("model %s does not support image input", c.Config.Name)
	}
	if len(tools) == 0 {
		return c.Generate(ctx, system, messages)
	}
	switch strings.ToLower(strings.TrimSpace(c.Config.API)) {
	case "", "anthropic":
		return c.generateAnthropic(ctx, system, messages, tools)
	case "openai_chat":
		return c.generateOpenAIChat(ctx, system, messages, tools)
	default:
		return GenerateResult{}, fmt.Errorf("model api %q does not support native tools yet", c.Config.API)
	}
}

func (c Client) SupportsNativeTools() bool {
	switch strings.ToLower(strings.TrimSpace(c.Config.API)) {
	case "", "anthropic", "openai_chat":
		return true
	default:
		return false
	}
}

func (c Client) generateAnthropic(ctx context.Context, system string, messages []Message, tools []agentcore.Tool) (GenerateResult, error) {
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
	if len(tools) > 0 {
		body["tools"] = anthropicTools(tools)
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

func (c Client) generateOpenAIChat(ctx context.Context, system string, messages []Message, tools []agentcore.Tool) (GenerateResult, error) {
	endpoint, err := endpointWithSuffix(c.Config.APIBase, "/chat/completions")
	if err != nil {
		return GenerateResult{}, err
	}
	body := map[string]any{
		"model":      c.Config.Model,
		"messages":   openAIChatMessages(system, messages),
		"max_tokens": c.Config.MaxTokensValue(),
	}
	if len(tools) > 0 {
		body["tools"] = openAIChatTools(tools)
		body["tool_choice"] = "auto"
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
		if role == "tool" {
			out = append(out, map[string]any{
				"role":         "tool",
				"tool_call_id": msg.ToolCallID,
				"content":      msg.Content,
			})
			continue
		}
		item := map[string]any{
			"role":    role,
			"content": openAIChatContent(msg),
		}
		if role == "assistant" && len(msg.ToolCalls) > 0 {
			item["tool_calls"] = openAIChatToolCalls(msg.ToolCalls)
			if strings.TrimSpace(msg.Content) == "" {
				item["content"] = nil
			}
		}
		out = append(out, item)
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
		if role == "tool" {
			out = append(out, map[string]any{
				"role":    "user",
				"content": []map[string]any{{"type": "tool_result", "tool_use_id": msg.ToolCallID, "content": msg.Content}},
			})
			continue
		}
		out = append(out, map[string]any{
			"role":    role,
			"content": anthropicContent(msg),
		})
	}
	return out
}

func anthropicContent(msg Message) any {
	if len(msg.ToolCalls) > 0 {
		content := make([]map[string]any, 0, len(msg.ToolCalls)+1)
		if strings.TrimSpace(msg.Content) != "" {
			content = append(content, map[string]any{"type": "text", "text": msg.Content})
		}
		for _, call := range msg.ToolCalls {
			content = append(content, map[string]any{
				"type":  "tool_use",
				"id":    call.ID,
				"name":  toolAPIAlias(call.Name),
				"input": call.Args,
			})
		}
		return content
	}
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

func anthropicTools(tools []agentcore.Tool) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		out = append(out, map[string]any{
			"name":         toolAPIAlias(tool.Name()),
			"description":  toolDescriptionForAPI(tool),
			"input_schema": toolParameters(tool),
		})
	}
	return out
}

func openAIChatTools(tools []agentcore.Tool) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        toolAPIAlias(tool.Name()),
				"description": toolDescriptionForAPI(tool),
				"parameters":  toolParameters(tool),
			},
		})
	}
	return out
}

func openAIChatToolCalls(calls []agentcore.ToolCall) []map[string]any {
	out := make([]map[string]any, 0, len(calls))
	for _, call := range calls {
		args := call.Args
		if args == nil {
			args = map[string]any{}
		}
		data, _ := json.Marshal(args)
		out = append(out, map[string]any{
			"id":   call.ID,
			"type": "function",
			"function": map[string]any{
				"name":      toolAPIAlias(call.Name),
				"arguments": string(data),
			},
		})
	}
	return out
}

func toolParameters(tool agentcore.Tool) map[string]any {
	schema := tool.Schema()
	required := append([]string(nil), schema.Required...)
	properties := map[string]any{}
	for name, value := range schema.Properties {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		properties[name] = value
	}
	for _, name := range required {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := properties[name]; !ok {
			properties[name] = map[string]any{"type": "string"}
		}
	}
	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": true,
	}
}

func optionalSchemaArgs(schema agentcore.Schema) []string {
	required := map[string]bool{}
	for _, name := range schema.Required {
		required[strings.TrimSpace(name)] = true
	}
	var optional []string
	for name := range schema.Properties {
		name = strings.TrimSpace(name)
		if name != "" && !required[name] {
			optional = append(optional, name)
		}
	}
	sort.Strings(optional)
	return optional
}

func toolDescriptionForAPI(tool agentcore.Tool) string {
	contract := agentcore.ContractFor(tool)
	parts := []string{tool.Description()}
	for _, value := range []string{contract.WhenToUse, contract.WhenNotToUse, contract.OutputContract, contract.ConfirmationBoundary} {
		if text := strings.TrimSpace(value); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func toolAPIAlias(name string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	alias := strings.Trim(b.String(), "_")
	if alias == "" {
		return "tool"
	}
	return alias
}

func toolNameFromAPI(name string) string {
	return strings.ReplaceAll(strings.TrimSpace(name), "_", ".")
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
	if strings.TrimSpace(text) == "" && len(result.ToolCalls) == 0 {
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
			Type  string         `json:"type"`
			Text  string         `json:"text"`
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return GenerateResult{}, fmt.Errorf("parse model response: %w", err)
	}
	if payload.Error != nil {
		return GenerateResult{}, fmt.Errorf("model error %s: %s", payload.Error.Type, payload.Error.Message)
	}
	var parts []string
	var calls []agentcore.ToolCall
	for _, item := range payload.Content {
		if item.Text != "" {
			parts = append(parts, item.Text)
		}
		if item.Type == "tool_use" && strings.TrimSpace(item.Name) != "" {
			args := item.Input
			if args == nil {
				args = map[string]any{}
			}
			id := strings.TrimSpace(item.ID)
			if id == "" {
				id = fmt.Sprintf("call_%d", len(calls)+1)
			}
			calls = append(calls, agentcore.ToolCall{ID: id, Name: toolNameFromAPI(item.Name), Args: args})
		}
	}
	usage := agentcore.Usage{
		InputTokens:      payload.Usage.InputTokens,
		OutputTokens:     payload.Usage.OutputTokens,
		CacheReadTokens:  payload.Usage.CacheReadInputTokens,
		CacheWriteTokens: payload.Usage.CacheCreationInputTokens,
	}
	usage.CacheInputTokens = usage.CacheReadTokens + usage.CacheWriteTokens
	usage.CacheHit = usage.CacheReadTokens > 0
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	return GenerateResult{Text: strings.TrimSpace(strings.Join(parts, "\n")), ToolCalls: calls, Usage: usage}, nil
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
				Role      string `json:"role"`
				Content   any    `json:"content"`
				Reasoning string `json:"reasoning"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			Text string `json:"text"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
		Usage struct {
			PromptTokens        int `json:"prompt_tokens"`
			CompletionTokens    int `json:"completion_tokens"`
			TotalTokens         int `json:"total_tokens"`
			PromptTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			CompletionTokensDetails struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return GenerateResult{}, fmt.Errorf("parse model response: %w", err)
	}
	if payload.Error != nil {
		return GenerateResult{}, fmt.Errorf("model error %s: %s", payload.Error.Type, payload.Error.Message)
	}
	var parts []string
	var calls []agentcore.ToolCall
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
		if strings.TrimSpace(choice.Message.Reasoning) != "" {
			parts = append(parts, choice.Message.Reasoning)
		}
		for _, rawCall := range choice.Message.ToolCalls {
			name := strings.TrimSpace(rawCall.Function.Name)
			if name == "" {
				continue
			}
			args := map[string]any{}
			if strings.TrimSpace(rawCall.Function.Arguments) != "" {
				_ = json.Unmarshal([]byte(rawCall.Function.Arguments), &args)
			}
			id := strings.TrimSpace(rawCall.ID)
			if id == "" {
				id = fmt.Sprintf("call_%d", len(calls)+1)
			}
			calls = append(calls, agentcore.ToolCall{ID: id, Name: toolNameFromAPI(name), Args: args})
		}
	}
	usage := agentcore.Usage{
		InputTokens:      payload.Usage.PromptTokens,
		OutputTokens:     payload.Usage.CompletionTokens,
		TotalTokens:      payload.Usage.TotalTokens,
		CacheReadTokens:  payload.Usage.PromptTokensDetails.CachedTokens,
		CacheInputTokens: payload.Usage.PromptTokensDetails.CachedTokens,
	}
	usage.CacheHit = usage.CacheReadTokens > 0
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}
	return GenerateResult{Text: strings.TrimSpace(strings.Join(parts, "\n")), ToolCalls: calls, Usage: usage}, nil
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
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			TotalTokens        int `json:"total_tokens"`
			InputTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return GenerateResult{}
	}
	usage := agentcore.Usage{
		InputTokens:      payload.Usage.InputTokens,
		OutputTokens:     payload.Usage.OutputTokens,
		TotalTokens:      payload.Usage.TotalTokens,
		CacheReadTokens:  payload.Usage.InputTokensDetails.CachedTokens,
		CacheInputTokens: payload.Usage.InputTokensDetails.CachedTokens,
	}
	usage.CacheHit = usage.CacheReadTokens > 0
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
	if usage.Provider == "" && usage.Model == "" && usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.TotalTokens == 0 && usage.CacheReadTokens == 0 && usage.CacheWriteTokens == 0 && usage.CacheInputTokens == 0 && usage.CacheOutputTokens == 0 {
		return nil
	}
	return &usage
}

var firstNonEmptyString = util.FirstNonEmptyString

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
