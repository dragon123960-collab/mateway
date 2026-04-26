package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type BrowserProvider struct {
	Enabled    bool
	HTTPClient *http.Client
}

func (p BrowserProvider) Tools(_ context.Context, _ Scope) ([]Tool, error) {
	if !p.Enabled {
		return nil, nil
	}
	return []Tool{browserFetchTool{httpClient: p.client()}}, nil
}

func (p BrowserProvider) client() *http.Client {
	if p.HTTPClient != nil {
		return p.HTTPClient
	}
	return &http.Client{}
}

type browserFetchTool struct {
	httpClient *http.Client
}

func (t browserFetchTool) Spec() Spec {
	return Spec{
		Name:        "browser_fetch",
		Description: "Fetch a web page and return a compact text-oriented summary for browser-like reading tasks.",
		Kind:        KindBuiltin,
		ReadOnly:    true,
		Tags:        []string{"browser", "web"},
		InputSchema: schemaObject(
			prop("url", "string", "HTTP or HTTPS URL"),
		),
	}
}

func (t browserFetchTool) Invoke(ctx context.Context, call Call) (Result, error) {
	var args struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return Result{}, err
	}
	rawURL := strings.TrimSpace(args.URL)
	if rawURL == "" {
		return Result{}, fmt.Errorf("url is required")
	}
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return Result{}, fmt.Errorf("url must start with http:// or https://")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Result{}, err
	}
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, err
	}
	if resp.StatusCode >= 400 {
		return Result{}, fmt.Errorf("browser fetch http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	summary := summarizeBrowserContent(string(body))
	data, _ := json.Marshal(summary)
	return Result{Output: data}, nil
}

func summarizeBrowserContent(body string) string {
	text := strings.TrimSpace(body)
	replacer := strings.NewReplacer("\r", " ", "\n", " ", "\t", " ", "<br>", " ", "<br/>", " ", "<p>", " ", "</p>", " ")
	text = replacer.Replace(text)
	for _, tag := range []string{"<html", "</html>", "<body", "</body>", "<head", "</head>", "<script", "</script>", "<style", "</style>"} {
		text = strings.ReplaceAll(text, tag, " ")
	}
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 1000 {
		text = text[:1000]
	}
	return text
}
