package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebSearchProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Heading":"OpenAI","AbstractText":"OpenAI builds AI systems.","RelatedTopics":[{"Text":"OpenAI develops large language models."}]}`))
	}))
	defer server.Close()

	provider := WebSearchProvider{Enabled: true, Provider: "duckduckgo", DuckDuckGoURL: server.URL}
	toolsList, err := provider.Tools(context.Background(), Scope{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := toolsList[0].Invoke(context.Background(), Call{Arguments: mustJSON(map[string]string{"query": "OpenAI"})})
	if err != nil {
		t.Fatal(err)
	}
	var content string
	if err := json.Unmarshal(res.Output, &content); err != nil {
		t.Fatal(err)
	}
	if content == "" {
		t.Fatal("expected summarized content")
	}
}

func TestWebSearchProviderTavily(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"answer":"Top trend is agentic workflow orchestration.","results":[{"title":"AI trends","content":"Agents, evals, memory.","url":"https://example.com/trends"}]}`))
	}))
	defer server.Close()

	provider := WebSearchProvider{Enabled: true, Provider: "tavily", TavilyURL: server.URL, TavilyAPIKey: "tvly-demo"}
	toolsList, err := provider.Tools(context.Background(), Scope{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := toolsList[0].Invoke(context.Background(), Call{Arguments: mustJSON(map[string]string{"query": "AI trends"})})
	if err != nil {
		t.Fatal(err)
	}
	var content string
	if err := json.Unmarshal(res.Output, &content); err != nil {
		t.Fatal(err)
	}
	if content == "" || content == "No concise web search result was available." {
		t.Fatalf("unexpected tavily summary: %q", content)
	}
}

func TestWebSearchProviderFallsBackToTavily(t *testing.T) {
	var ddgCalls int
	ddgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ddgCalls++
		if r.Method != http.MethodGet {
			t.Fatalf("expected ddg GET request, got %s", r.Method)
		}
		w.WriteHeader(http.StatusGatewayTimeout)
		_, _ = w.Write([]byte(`timeout`))
	}))
	defer ddgServer.Close()

	var tavilyCalls int
	tavilyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tavilyCalls++
		if r.Method != http.MethodPost {
			t.Fatalf("expected tavily POST request, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"answer":"Fallback worked.","results":[{"title":"Agent trends","content":"Fallback to Tavily.","url":"https://example.com/fallback"}]}`))
	}))
	defer tavilyServer.Close()

	provider := WebSearchProvider{
		Enabled:       true,
		Provider:      "duckduckgo",
		DuckDuckGoURL: ddgServer.URL,
		TavilyAPIKey:  "tvly-demo",
		HTTPClient: tavilyRewriteTransportClient(
			t,
			tavilyServer.URL,
		),
	}
	toolsList, err := provider.Tools(context.Background(), Scope{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := toolsList[0].Invoke(context.Background(), Call{Arguments: mustJSON(map[string]string{"query": "AI trends"})})
	if err != nil {
		t.Fatal(err)
	}
	var content string
	if err := json.Unmarshal(res.Output, &content); err != nil {
		t.Fatal(err)
	}
	if content == "" || !strings.Contains(content, "Fallback worked.") {
		t.Fatalf("unexpected fallback summary: %q", content)
	}
	if ddgCalls != 1 {
		t.Fatalf("expected 1 ddg attempt, got %d", ddgCalls)
	}
	if tavilyCalls != 1 {
		t.Fatalf("expected 1 tavily attempt, got %d", tavilyCalls)
	}
}

func tavilyRewriteTransportClient(t *testing.T, tavilyURL string) *http.Client {
	t.Helper()
	target, err := http.NewRequest(http.MethodPost, tavilyURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host == "api.tavily.com" {
				req.URL.Scheme = target.URL.Scheme
				req.URL.Host = target.URL.Host
			}
			return http.DefaultTransport.RoundTrip(req)
		}),
	}
}

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}
