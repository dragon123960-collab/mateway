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
	key := c.Config.ResolvedAPIKey()
	if key == "" {
		return "", fmt.Errorf("model api key is empty for %s", c.Config.Name)
	}
	endpoint, err := messagesEndpoint(c.Config.APIBase)
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
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("model request failed: status=%d body=%s", resp.StatusCode, truncateForError(string(data)))
	}
	text, err := parseAnthropicText(data)
	if err != nil {
		return "", err
	}
	if c.Config.StripReasoning {
		text = stripReasoning(text)
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("model returned empty text")
	}
	return strings.TrimSpace(text), nil
}

func messagesEndpoint(apiBase string) (string, error) {
	apiBase = strings.TrimRight(strings.TrimSpace(apiBase), "/")
	if apiBase == "" {
		return "", fmt.Errorf("model api_base is required")
	}
	parsed, err := url.Parse(apiBase)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid model api_base %q", apiBase)
	}
	if strings.HasSuffix(parsed.Path, "/v1/messages") {
		return apiBase, nil
	}
	return apiBase + "/v1/messages", nil
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
