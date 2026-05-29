package tool

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/config"
)

func TestWebSearchUsesSearXNGProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" || r.URL.Query().Get("format") != "json" {
			t.Fatalf("unexpected request %s", r.URL.String())
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"Mateway","url":"https://example.test","content":"agent runtime"}]}`))
	}))
	defer server.Close()

	tool := WebSearchTool{Config: &config.Root{Search: config.SearchConfig{
		ProviderOrder: []string{"searxng"},
		Providers: config.SearchProvidersConfig{
			SearXNG: config.SearchProviderConfig{Enabled: true, BaseURL: server.URL},
		},
	}}}
	result := tool.Run(context.Background(), agentcore.ToolCall{ID: "1", Args: map[string]any{"query": "mateway"}})
	if result.IsError {
		t.Fatalf("unexpected error: %#v", result)
	}
	if result.Evidence["provider"] != "searxng" {
		t.Fatalf("provider evidence = %#v", result.Evidence)
	}
	if strings.Contains(result.Content, "<html") || !strings.Contains(result.Content, "Source:") || !strings.Contains(result.Content, "Summary:") {
		t.Fatalf("expected structured search output, got %s", result.Content)
	}
}

func TestWebSearchFallsBackFromTavilyToSearXNG(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"Fallback","url":"https://example.test","content":"ok"}]}`))
	}))
	defer server.Close()

	tool := WebSearchTool{Config: &config.Root{Search: config.SearchConfig{
		ProviderOrder: []string{"tavily", "searxng"},
		Providers: config.SearchProvidersConfig{
			Tavily:  config.SearchProviderConfig{Enabled: true},
			SearXNG: config.SearchProviderConfig{Enabled: true, BaseURL: server.URL},
		},
	}}}
	result := tool.Run(context.Background(), agentcore.ToolCall{ID: "1", Args: map[string]any{"query": "mateway"}})
	if result.IsError {
		t.Fatalf("unexpected error: %#v", result)
	}
	if result.Evidence["provider"] != "searxng" {
		t.Fatalf("provider evidence = %#v", result.Evidence)
	}
}

func TestDuckDuckGoResultsParseStructuredOutput(t *testing.T) {
	html := []byte(`<html><body>
<a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fopenai.com%2Findex%2Fdemo">OpenAI Demo</a>
<a class="result__snippet">Product May 28, 2026 official update.</a>
</body></html>`)
	results := duckDuckGoResults(html)
	if len(results) != 1 {
		t.Fatalf("expected one result, got %#v", results)
	}
	if results[0].SourceType != "official_or_primary_candidate" || results[0].DateHint == "" {
		t.Fatalf("unexpected parsed result %#v", results[0])
	}
	rendered := renderSearchResults("openai", "duckduckgo_html", results, 8)
	if strings.Contains(rendered, "<a") || !strings.Contains(rendered, "Date hint:") {
		t.Fatalf("unexpected rendered result %s", rendered)
	}
}

func TestRenderHNAlgoliaFrontPage(t *testing.T) {
	rendered := renderHNAlgoliaFrontPage([]byte(`{"hits":[{"title":"AI agents on device","url":"","objectID":"123","points":42,"num_comments":7,"created_at":"2026-05-28T01:02:03Z"}]}`), "https://news.ycombinator.com/", "timeout")
	for _, want := range []string{"Original fetch failed: timeout", "HN Algolia", "AI agents on device", "item?id=123", "Points: 42", "Created at: 2026-05-28"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered fallback missing %q:\n%s", want, rendered)
		}
	}
}
