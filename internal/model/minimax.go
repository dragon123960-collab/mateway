package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

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

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (c Client) Generate(ctx context.Context, system string, messages []Message) (string, error) {
	switch strings.ToLower(strings.TrimSpace(c.Config.API)) {
	case "", "anthropic":
		return c.generateAnthropic(ctx, system, messages)
	case "openai":
		return c.generateOpenAI(ctx, system, messages)
	default:
		return "", fmt.Errorf("unsupported model api %q for %s", c.Config.API, c.Config.Name)
	}
}

func (c Client) generateAnthropic(ctx context.Context, system string, messages []Message) (string, error) {
	key := c.Config.ResolvedAPIKey()
	if key == "" {
		return "", fmt.Errorf("model api key is empty for %s", c.Config.Name)
	}
	endpoint, err := endpointWithSuffix(c.Config.APIBase, "/v1/messages")
	if err != nil {
		return "", err
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
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("x-api-key", key)
	req.Header.Set("authorization", "Bearer "+key)
	return c.doGenerate(req)
}

func (c Client) generateOpenAI(ctx context.Context, system string, messages []Message) (string, error) {
	endpoint, err := endpointWithSuffix(c.Config.APIBase, "/chat/completions")
	if err != nil {
		return "", err
	}
	wireMessages := make([]Message, 0, len(messages)+1)
	if strings.TrimSpace(system) != "" {
		wireMessages = append(wireMessages, Message{Role: "system", Content: system})
	}
	wireMessages = append(wireMessages, messages...)
	body := map[string]any{
		"model":      c.Config.Model,
		"messages":   wireMessages,
		"max_tokens": 4096,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/json")
	if key := strings.TrimSpace(c.Config.ResolvedAPIKey()); key != "" {
		req.Header.Set("authorization", "Bearer "+key)
	}
	return c.doGenerate(req)
}

func (c Client) doGenerate(req *http.Request) (string, error) {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("model request failed: status=%d body=%s", resp.StatusCode, truncateForError(string(data)))
	}
	switch strings.ToLower(strings.TrimSpace(c.Config.API)) {
	case "", "anthropic":
		text, err := parseAnthropicText(data)
		return finishGenerate(c.Config, text, err)
	case "openai":
		text, err := parseOpenAIText(data)
		return finishGenerate(c.Config, text, err)
	default:
		return "", fmt.Errorf("unsupported model api %q for %s", c.Config.API, c.Config.Name)
	}
}

func finishGenerate(cfg config.ModelConfig, text string, err error) (string, error) {
	if err != nil {
		return "", err
	}
	if cfg.StripReasoning {
		text = stripReasoning(text)
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("model returned empty text")
	}
	return strings.TrimSpace(text), nil
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
	var payload struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", fmt.Errorf("parse model response: %w", err)
	}
	if payload.Error != nil {
		return "", fmt.Errorf("model error %s: %s", payload.Error.Type, payload.Error.Message)
	}
	var parts []string
	for _, item := range payload.Content {
		if item.Text != "" {
			parts = append(parts, item.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n")), nil
}

func parseOpenAIText(data []byte) (string, error) {
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
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", fmt.Errorf("parse model response: %w", err)
	}
	if payload.Error != nil {
		return "", fmt.Errorf("model error %s: %s", payload.Error.Type, payload.Error.Message)
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
	return strings.TrimSpace(strings.Join(parts, "\n")), nil
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
