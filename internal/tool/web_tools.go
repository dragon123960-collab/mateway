package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/config"
)

type WebFetchTool struct{ Config *config.Root }

func (WebFetchTool) Name() string        { return "web.fetch" }
func (WebFetchTool) Description() string { return "fetch a URL body" }
func (WebFetchTool) Schema() agentcore.Schema {
	return agentcore.Schema{Required: []string{"url"}}
}
func (WebFetchTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{
		WhenToUse:            "Use when a specific URL must be fetched or verified.",
		WhenNotToUse:         "Do not use for broad discovery; use web.search first when no URL is known.",
		OutputContract:       "Return the response body up to the tool limit and HTTP status evidence.",
		Evidence:             "Return fetched URL and HTTP status.",
		Acceptance:           "Accepted when HTTP status is below 400 and useful body content is returned.",
		SoftFailureSignals:   []string{"HTTP status >= 400", "timeout", "DNS failure", "empty body"},
		ParallelMode:         "read_only_ok",
		ReusePolicy:          "stable_read",
		ConfirmationBoundary: "safe read; no confirmation.",
	}
}
func (WebFetchTool) Risk() agentcore.Risk { return agentcore.RiskSafeRead }
func (WebFetchTool) Run(ctx context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	rawURL := strings.TrimSpace(fmt.Sprint(call.Args["url"]))
	if strings.HasPrefix(rawURL, "file://") {
		return agentcore.ToolResult{
			ToolCallID: call.ID,
			Content:    "web.fetch cannot read local file:// URLs; use file.read with the absolute local path instead.",
			IsError:    true,
			Evidence:   map[string]any{"url": rawURL, "failure_kind": "local_file_url", "recommended_tool": "file.read"},
		}
	}
	if strings.HasPrefix(rawURL, "raw_ref:") || strings.HasPrefix(rawURL, "tool-result:") || strings.Contains(rawURL, "raw_ref=tool-result:") {
		return agentcore.ToolResult{
			ToolCallID: call.ID,
			Content:    "web.fetch cannot read raw_ref/tool-result references; use toolresult.read with raw_ref instead.",
			IsError:    true,
			Evidence:   map[string]any{"url": rawURL, "failure_kind": "tool_result_ref", "recommended_tool": "toolresult.read"},
		}
	}
	if blocked, ok := IsBlockedFetchURL(rawURL); ok {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: "web.fetch blocked private or local address: " + blocked, IsError: true, Evidence: map[string]any{"url": rawURL, "blocked": true, "reason": "ssrf_blocked"}}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true}
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	client := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if blocked, ok := IsBlockedFetchURL(req.URL.String()); ok {
				return fmt.Errorf("web.fetch redirect blocked private or local address: %s", blocked)
			}
			if len(via) >= 5 {
				return fmt.Errorf("stopped after 5 redirects")
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		if fallback, ok := fetchFallback(ctx, call.ID, rawURL, err.Error()); ok {
			return fallback
		}
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if resp.StatusCode >= 400 {
		if fallback, ok := fetchFallback(ctx, call.ID, rawURL, fmt.Sprintf("HTTP status %d", resp.StatusCode)); ok {
			return fallback
		}
	}
	if isBotProtectionPage(data) {
		return agentcore.ToolResult{
			ToolCallID: call.ID,
			Content:    "web.fetch received a bot-protection or JS challenge page. Use web.search result summaries or an official API instead.",
			IsError:    true,
			Evidence: map[string]any{
				"url":          rawURL,
				"status":       resp.StatusCode,
				"failure_kind": "bot_protection",
			},
		}
	}
	return agentcore.ToolResult{ToolCallID: call.ID, Content: string(data), IsError: resp.StatusCode >= 400, Evidence: map[string]any{"url": rawURL, "status": resp.StatusCode}}
}

func fetchFallback(ctx context.Context, callID, rawURL, reason string) (agentcore.ToolResult, bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return agentcore.ToolResult{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "news.ycombinator.com" {
		return agentcore.ToolResult{}, false
	}
	switch parsed.Path {
	case "", "/", "/news", "/newest", "/front":
		return fetchHNAlgolia(ctx, callID, rawURL, reason), true
	default:
		return agentcore.ToolResult{}, false
	}
}

func fetchHNAlgolia(ctx context.Context, callID, rawURL, reason string) agentcore.ToolResult {
	endpoint := "https://hn.algolia.com/api/v1/search?tags=front_page&hitsPerPage=30"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: callID, Content: err.Error(), IsError: true}
	}
	req.Header.Set("user-agent", "mateway/0.1")
	resp, err := searchHTTPClient(8).Do(req)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: callID, Content: reason + "; HN Algolia fallback failed: " + err.Error(), IsError: true, Evidence: map[string]any{"url": rawURL, "fallback": endpoint}}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if resp.StatusCode >= 400 {
		return agentcore.ToolResult{ToolCallID: callID, Content: reason + "; HN Algolia fallback returned " + resp.Status, IsError: true, Evidence: map[string]any{"url": rawURL, "fallback": endpoint, "status": resp.StatusCode}}
	}
	return agentcore.ToolResult{
		ToolCallID: callID,
		Content:    renderHNAlgoliaFrontPage(data, rawURL, reason),
		Evidence:   map[string]any{"url": rawURL, "fallback": endpoint, "provider": "hn_algolia", "status": resp.StatusCode},
	}
}

func renderHNAlgoliaFrontPage(data []byte, rawURL, reason string) string {
	var parsed struct {
		Hits []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			ObjectID    string `json:"objectID"`
			Points      int    `json:"points"`
			NumComments int    `json:"num_comments"`
			CreatedAt   string `json:"created_at"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "Original fetch failed: " + reason + "\nHN Algolia fallback returned unparsable JSON."
	}
	var b strings.Builder
	b.WriteString("Original fetch failed: ")
	b.WriteString(reason)
	b.WriteString("\nFallback source: HN Algolia front_page API for ")
	b.WriteString(rawURL)
	b.WriteString("\n")
	for i, hit := range parsed.Hits {
		if i >= 20 {
			break
		}
		title := strings.TrimSpace(hit.Title)
		if title == "" {
			continue
		}
		itemURL := strings.TrimSpace(hit.URL)
		if itemURL == "" && hit.ObjectID != "" {
			itemURL = "https://news.ycombinator.com/item?id=" + hit.ObjectID
		}
		b.WriteString(fmt.Sprintf("\n%d. %s\n", i+1, title))
		if itemURL != "" {
			b.WriteString("URL: ")
			b.WriteString(itemURL)
			b.WriteString("\n")
		}
		if hit.ObjectID != "" {
			b.WriteString("HN item: https://news.ycombinator.com/item?id=")
			b.WriteString(hit.ObjectID)
			b.WriteString("\n")
		}
		b.WriteString(fmt.Sprintf("Points: %d Comments: %d\n", hit.Points, hit.NumComments))
		if hit.CreatedAt != "" {
			b.WriteString("Created at: ")
			b.WriteString(hit.CreatedAt)
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
}

type WebSearchTool struct{ Config *config.Root }

func (WebSearchTool) Name() string { return "web.search" }
func (WebSearchTool) Description() string {
	return "search the web and return structured source summaries"
}
func (WebSearchTool) Schema() agentcore.Schema {
	return agentcore.Schema{Required: []string{"query"}}
}
func (WebSearchTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{
		WhenToUse:            "Use when the task requires current or external information and no specific source URL is known.",
		WhenNotToUse:         "Do not use when the answer can be produced from local files, conversation context, or a known URL.",
		OutputContract:       "Return compact structured results with title, URL, summary, provider, date hints, and official/third-party classification.",
		Evidence:             "Return query, provider, HTTP status, and result count.",
		Acceptance:           "Accepted when the search request succeeds with HTTP status below 400.",
		SoftFailureSignals:   []string{"HTTP status >= 400", "DNS failure", "empty result page", "provider block"},
		ParallelMode:         "read_only_ok",
		ReusePolicy:          "stable_read",
		ConfirmationBoundary: "safe read; no confirmation.",
	}
}
func (WebSearchTool) Risk() agentcore.Risk { return agentcore.RiskSafeRead }
func (t WebSearchTool) Run(ctx context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	query := stringArg(call.Args, "query")
	if query == "" {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: "query is required", IsError: true}
	}
	providers := searchProviderOrder(t.Config)
	var failures []string
	for _, provider := range providers {
		result := runSearchProvider(ctx, t.Config, provider, query, call.ID)
		if !result.IsError {
			return result
		}
		failures = append(failures, provider+": "+result.Content)
	}
	return agentcore.ToolResult{
		ToolCallID: call.ID,
		Content:    "all search providers failed: " + strings.Join(failures, " | "),
		IsError:    true,
		Evidence:   map[string]any{"query": query, "providers": providers},
	}
}

func searchProviderOrder(cfg *config.Root) []string {
	if cfg == nil || len(cfg.Search.ProviderOrder) == 0 {
		return []string{"tavily", "searxng", "duckduckgo"}
	}
	var out []string
	for _, provider := range cfg.Search.ProviderOrder {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider == "" || provider == "cache" {
			continue
		}
		out = append(out, provider)
	}
	if len(out) == 0 {
		return []string{"tavily", "searxng", "duckduckgo"}
	}
	return out
}

func runSearchProvider(ctx context.Context, cfg *config.Root, provider, query, callID string) agentcore.ToolResult {
	switch provider {
	case "tavily":
		return tavilySearch(ctx, cfg, query, callID)
	case "searxng", "searchxng":
		return searxngSearch(ctx, cfg, query, callID)
	case "duckduckgo":
		return duckDuckGoHTMLSearch(ctx, cfg, query, callID)
	default:
		return agentcore.ToolResult{ToolCallID: callID, Content: "unknown provider", IsError: true}
	}
}

func tavilySearch(ctx context.Context, cfg *config.Root, query, callID string) agentcore.ToolResult {
	provider := config.SearchProviderConfig{}
	if cfg != nil {
		provider = cfg.Search.Providers.Tavily
	}
	if !provider.Enabled {
		return agentcore.ToolResult{ToolCallID: callID, Content: "tavily disabled", IsError: true}
	}
	key := provider.ResolvedAPIKey()
	if key == "" {
		return agentcore.ToolResult{ToolCallID: callID, Content: "tavily api key is empty", IsError: true}
	}
	baseURL := strings.TrimSpace(provider.BaseURL)
	if baseURL == "" {
		baseURL = "https://api.tavily.com/search"
	}
	maxResults := provider.MaxResults
	if maxResults <= 0 {
		maxResults = 5
	}
	body := map[string]any{
		"api_key":     key,
		"query":       query,
		"max_results": maxResults,
	}
	if provider.SearchDepth != "" {
		body["search_depth"] = provider.SearchDepth
	}
	if provider.Topic != "" {
		body["topic"] = provider.Topic
	}
	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader(payload))
	if err != nil {
		return agentcore.ToolResult{ToolCallID: callID, Content: err.Error(), IsError: true}
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("user-agent", "mateway/0.1")
	resp, err := searchHTTPClient(provider.TimeoutSeconds).Do(req)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: callID, Content: err.Error(), IsError: true}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if resp.StatusCode >= 400 {
		return agentcore.ToolResult{ToolCallID: callID, Content: string(data), IsError: true, Evidence: map[string]any{"query": query, "status": resp.StatusCode, "provider": "tavily"}}
	}
	return agentcore.ToolResult{
		ToolCallID: callID,
		Content:    renderSearchResults(query, "tavily", tavilyResults(data), 8),
		Evidence:   map[string]any{"query": query, "status": resp.StatusCode, "provider": "tavily", "result_count": len(tavilyResults(data))},
	}
}

func searxngSearch(ctx context.Context, cfg *config.Root, query, callID string) agentcore.ToolResult {
	provider := config.SearchProviderConfig{}
	if cfg != nil {
		provider = cfg.Search.Providers.SearXNG
	}
	if !provider.Enabled {
		return agentcore.ToolResult{ToolCallID: callID, Content: "searxng disabled", IsError: true}
	}
	baseURL := strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8088"
	}
	endpoint := baseURL + "/search?q=" + url.QueryEscape(query) + "&format=json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: callID, Content: err.Error(), IsError: true}
	}
	req.Header.Set("user-agent", "mateway/0.1")
	resp, err := searchHTTPClient(provider.TimeoutSeconds).Do(req)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: callID, Content: err.Error(), IsError: true}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if resp.StatusCode >= 400 {
		return agentcore.ToolResult{ToolCallID: callID, Content: string(data), IsError: true, Evidence: map[string]any{"query": query, "status": resp.StatusCode, "provider": "searxng"}}
	}
	results := searxngResults(data)
	return agentcore.ToolResult{ToolCallID: callID, Content: renderSearchResults(query, "searxng", results, 8), Evidence: map[string]any{"query": query, "status": resp.StatusCode, "provider": "searxng", "result_count": len(results)}}
}

func duckDuckGoHTMLSearch(ctx context.Context, cfg *config.Root, query, callID string) agentcore.ToolResult {
	provider := config.SearchProviderConfig{Enabled: true, TimeoutSeconds: 4}
	if cfg != nil {
		provider = cfg.Search.Providers.DuckDuckGo
	}
	if !provider.Enabled {
		return agentcore.ToolResult{ToolCallID: callID, Content: "duckduckgo disabled", IsError: true}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://duckduckgo.com/html/?q="+url.QueryEscape(query), nil)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: callID, Content: err.Error(), IsError: true}
	}
	req.Header.Set("user-agent", "mateway/0.1")
	resp, err := searchHTTPClient(provider.TimeoutSeconds).Do(req)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: callID, Content: err.Error(), IsError: true}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	results := duckDuckGoResults(data)
	return agentcore.ToolResult{
		ToolCallID: callID,
		Content:    renderSearchResults(query, "duckduckgo_html", results, 8),
		IsError:    resp.StatusCode >= 400,
		Evidence:   map[string]any{"query": query, "status": resp.StatusCode, "provider": "duckduckgo_html", "result_count": len(results)},
	}
}

func searchHTTPClient(timeoutSeconds int) *http.Client {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 8
	}
	return &http.Client{Timeout: time.Duration(timeoutSeconds) * time.Second}
}

type searchResultItem struct {
	Title      string
	URL        string
	Summary    string
	DateHint   string
	SourceType string
	Provider   string
}

func tavilyResults(data []byte) []searchResultItem {
	var parsed struct {
		Answer  string `json:"answer"`
		Results []struct {
			Title   string  `json:"title"`
			URL     string  `json:"url"`
			Content string  `json:"content"`
			Score   float64 `json:"score"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil
	}
	var out []searchResultItem
	for i, item := range parsed.Results {
		if i >= 12 {
			break
		}
		out = append(out, normalizeSearchItem("tavily", item.Title, item.URL, item.Content))
	}
	return out
}

func searxngResults(data []byte) []searchResultItem {
	var parsed struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil
	}
	var out []searchResultItem
	for i, item := range parsed.Results {
		if i >= 12 {
			break
		}
		out = append(out, normalizeSearchItem("searxng", item.Title, item.URL, item.Content))
	}
	return out
}

func duckDuckGoResults(data []byte) []searchResultItem {
	text := string(data)
	re := regexp.MustCompile(`(?is)<a[^>]+class="result__a"[^>]+href="([^"]+)"[^>]*>(.*?)</a>.*?<a[^>]+class="result__snippet"[^>]*>(.*?)</a>`)
	matches := re.FindAllStringSubmatch(text, 12)
	var out []searchResultItem
	for _, match := range matches {
		if len(match) < 4 {
			continue
		}
		rawURL := html.UnescapeString(match[1])
		if parsed, err := url.Parse(rawURL); err == nil {
			if uddg := parsed.Query().Get("uddg"); uddg != "" {
				rawURL = uddg
			}
		}
		out = append(out, normalizeSearchItem("duckduckgo_html", stripHTML(match[2]), rawURL, stripHTML(match[3])))
	}
	return out
}

func renderSearchResults(query, provider string, results []searchResultItem, limit int) string {
	if limit <= 0 {
		limit = 8
	}
	var b strings.Builder
	b.WriteString("Search query: ")
	b.WriteString(query)
	b.WriteString("\nProvider: ")
	b.WriteString(provider)
	b.WriteString("\nResults are compact summaries. Use web.fetch on a source URL when exact details or quotes are required.\n")
	for i, item := range results {
		if i >= limit {
			break
		}
		b.WriteString(fmt.Sprintf("\n%d. %s\n", i+1, item.Title))
		b.WriteString("URL: ")
		b.WriteString(item.URL)
		b.WriteString("\nSource: ")
		b.WriteString(item.SourceType)
		if item.DateHint != "" {
			b.WriteString("\nDate hint: ")
			b.WriteString(item.DateHint)
		}
		b.WriteString("\nSummary: ")
		b.WriteString(item.Summary)
		b.WriteString("\n")
	}
	if len(results) == 0 {
		b.WriteString("\nNo structured results parsed. Try a different query or provider.\n")
	}
	return strings.TrimSpace(b.String())
}

func normalizeSearchItem(provider, title, rawURL, summary string) searchResultItem {
	title = compactWhitespace(html.UnescapeString(stripHTML(title)))
	summary = compactWhitespace(html.UnescapeString(stripHTML(summary)))
	rawURL = strings.TrimSpace(html.UnescapeString(rawURL))
	if title == "" {
		title = "(untitled)"
	}
	if len([]rune(summary)) > 520 {
		rs := []rune(summary)
		summary = string(rs[:520]) + "..."
	}
	return searchResultItem{
		Title:      title,
		URL:        rawURL,
		Summary:    summary,
		DateHint:   firstDateHint(title + " " + summary),
		SourceType: classifySource(rawURL),
		Provider:   provider,
	}
}

func classifySource(rawURL string) string {
	host := ""
	if parsed, err := url.Parse(rawURL); err == nil {
		host = strings.ToLower(parsed.Hostname())
	}
	switch {
	case host == "":
		return "unknown"
	case strings.Contains(host, "github.com") || strings.Contains(host, "openai.com") || strings.Contains(host, "microsoft.com") || strings.Contains(host, "docs.") || strings.Contains(host, "developer.") || strings.Contains(host, "developers."):
		return "official_or_primary_candidate"
	case strings.Contains(host, "youtube.com") || strings.Contains(host, "reddit.com") || strings.Contains(host, "medium.com") || strings.Contains(host, "blog."):
		return "community_or_secondary"
	default:
		return "third_party"
	}
}

func firstDateHint(text string) string {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`\b20[0-9]{2}[-/年.][0-9]{1,2}[-/月.][0-9]{1,2}`),
		regexp.MustCompile(`\b(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)[a-z]*\s+[0-9]{1,2},?\s+20[0-9]{2}`),
		regexp.MustCompile(`20[0-9]{2}年[0-9]{1,2}月[0-9]{1,2}日`),
	}
	for _, pattern := range patterns {
		if match := pattern.FindString(text); match != "" {
			return match
		}
	}
	return ""
}

func stripHTML(text string) string {
	re := regexp.MustCompile(`(?s)<[^>]+>`)
	return re.ReplaceAllString(text, " ")
}

func compactWhitespace(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func summarizeToolText(text string, limit int) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if limit <= 0 || len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + fmt.Sprintf("... (%d chars)", len(runes))
}

func isBotProtectionPage(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	lower := strings.ToLower(string(data))
	indicators := []string{
		"cf-browser-verification",
		"cf-challenge",
		"cloudflare",
		"please enable cookies",
		"please enable javascript",
		"enable js",
		"captcha",
		"recaptcha",
		"hcaptcha",
		"challenge-platform",
		"are you a human",
		"verify you are human",
		"verify you are a human",
		"disable any ad blocker",
		"turn off your ad blocker",
	}
	count := 0
	for _, indicator := range indicators {
		if strings.Contains(lower, indicator) {
			count++
			if count >= 2 {
				return true
			}
		}
	}
	if len(data) < 2048 {
		if strings.Contains(lower, "<title>") && strings.Contains(lower, "robot") {
			return true
		}
	}
	return false
}
