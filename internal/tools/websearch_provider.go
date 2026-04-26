package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type WebSearchProvider struct {
	Enabled       bool
	Provider      string
	DuckDuckGoURL string
	TavilyURL     string
	TavilyAPIKey  string
	HTTPClient    *http.Client
}

func (p WebSearchProvider) Tools(_ context.Context, _ Scope) ([]Tool, error) {
	if !p.Enabled {
		return nil, nil
	}
	return []Tool{webSearchTool{
		provider:      p.Provider,
		duckDuckGoURL: p.DuckDuckGoURL,
		tavilyURL:     p.TavilyURL,
		tavilyAPIKey:  p.TavilyAPIKey,
		httpClient:    p.client(),
	}}, nil
}

func (p WebSearchProvider) client() *http.Client {
	if p.HTTPClient != nil {
		return p.HTTPClient
	}
	return &http.Client{}
}

type webSearchTool struct {
	provider      string
	duckDuckGoURL string
	tavilyURL     string
	tavilyAPIKey  string
	httpClient    *http.Client
}

func (t webSearchTool) Spec() Spec {
	return Spec{
		Name:        "web_search",
		Description: "Search the web and return a compact summary of relevant results.",
		Kind:        KindBuiltin,
		ReadOnly:    true,
		Tags:        []string{"web", "search"},
		InputSchema: schemaObject(prop("query", "string", "Search query")),
	}
}

func (t webSearchTool) Invoke(ctx context.Context, call Call) (Result, error) {
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return Result{}, fmt.Errorf("query is required")
	}
	provider := strings.ToLower(strings.TrimSpace(t.provider))
	if provider == "" {
		provider = "duckduckgo"
	}
	switch provider {
	case "duckduckgo", "ddg":
		result, err := t.searchDuckDuckGo(ctx, query)
		if err != nil && t.tavilyAPIKey != "" {
			// DuckDuckGo failed, fallback to Tavily
			result, err = t.searchTavily(ctx, query)
		}
		if err != nil {
			return Result{}, err
		}
		data, _ := json.Marshal(result)
		return Result{Output: data}, nil
	case "tavily":
		result, err := t.searchTavily(ctx, query)
		if err != nil {
			return Result{}, err
		}
		data, _ := json.Marshal(result)
		return Result{Output: data}, nil
	default:
		return Result{}, fmt.Errorf("unsupported web search provider %q", provider)
	}
}

func (t webSearchTool) searchDuckDuckGo(ctx context.Context, query string) (string, error) {
	base := strings.TrimSpace(t.duckDuckGoURL)
	if base == "" {
		base = "https://api.duckduckgo.com/"
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("q", query)
	q.Set("format", "json")
	q.Set("no_redirect", "1")
	q.Set("no_html", "1")
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("web search http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return summarizeSearchPayload(body), nil
}

func (t webSearchTool) searchTavily(ctx context.Context, query string) (string, error) {
	base := strings.TrimSpace(t.tavilyURL)
	if base == "" {
		base = "https://api.tavily.com/search"
	}
	apiKey := strings.TrimSpace(t.tavilyAPIKey)
	if apiKey == "" {
		return "", fmt.Errorf("tavily api_key is required")
	}
	payload, _ := json.Marshal(map[string]any{
		"api_key":       apiKey,
		"query":         query,
		"search_depth":  "basic",
		"max_results":   5,
		"include_answer": true,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("web search http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return summarizeTavilyPayload(body), nil
}

func summarizeSearchPayload(body []byte) string {
	var parsed struct {
		Abstract      string `json:"Abstract"`
		AbstractText  string `json:"AbstractText"`
		Heading       string `json:"Heading"`
		Answer        string `json:"Answer"`
		Definition    string `json:"Definition"`
		RelatedTopics []struct {
			Text string `json:"Text"`
		} `json:"RelatedTopics"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return strings.TrimSpace(string(body))
	}
	parts := make([]string, 0, 4)
	for _, value := range []string{parsed.Heading, parsed.Answer, parsed.AbstractText, parsed.Abstract, parsed.Definition} {
		value = strings.TrimSpace(value)
		if value != "" {
			parts = append(parts, value)
		}
		if len(parts) >= 3 {
			break
		}
	}
	for _, topic := range parsed.RelatedTopics {
		text := strings.TrimSpace(topic.Text)
		if text != "" {
			parts = append(parts, text)
		}
		if len(parts) >= 5 {
			break
		}
	}
	if len(parts) == 0 {
		return "No concise web search result was available."
	}
	return strings.Join(parts, "\n")
}

func summarizeTavilyPayload(body []byte) string {
	var parsed struct {
		Answer  string `json:"answer"`
		Query   string `json:"query"`
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return strings.TrimSpace(string(body))
	}
	parts := make([]string, 0, 6)
	if text := strings.TrimSpace(parsed.Answer); text != "" {
		parts = append(parts, text)
	}
	for _, result := range parsed.Results {
		line := strings.TrimSpace(strings.Join([]string{
			strings.TrimSpace(result.Title),
			strings.TrimSpace(result.Content),
			strings.TrimSpace(result.URL),
		}, " | "))
		line = strings.TrimSpace(strings.Trim(line, "| "))
		if line != "" {
			parts = append(parts, line)
		}
		if len(parts) >= 5 {
			break
		}
	}
	if len(parts) == 0 {
		return "No concise web search result was available."
	}
	return strings.Join(parts, "\n")
}
