package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func RegisterBuiltins(r *Registry) {
	r.Register(TimeNow())
	r.Register(ConfigSummary())
	r.Register(WebSearch())
	r.Register(FileRead())
	r.Register(ProjectIndex())
	r.Register(FileSummary())
	r.Register(FileWrite())
	r.Register(FilePatch())
	r.Register(ShellRun())
	r.Register(UserAsk())
}

func TimeNow() Definition {
	return Definition{
		Name:        "time.now",
		Description: "Return current local time, date, and timezone.",
		Risk:        RiskSafeRead,
		ArgsSchema:  map[string]string{"timezone": "optional IANA timezone, defaults to local"},
		Run: func(ctx context.Context, call Call) Result {
			loc := time.Local
			if tz := strings.TrimSpace(call.Args["timezone"]); tz != "" {
				if loaded, err := time.LoadLocation(tz); err == nil {
					loc = loaded
				}
			}
			now := time.Now().In(loc)
			return Result{OK: true, Output: now.Format(time.RFC3339), Evidence: map[string]any{
				"kind":     "time",
				"timezone": loc.String(),
				"unix":     now.Unix(),
			}}
		},
	}
}

func ConfigSummary() Definition {
	return Definition{
		Name:        "config.summary",
		Description: "Return safe summary of loaded Mateway configuration without secrets.",
		Risk:        RiskSafeRead,
		ArgsSchema:  map[string]string{},
		Run: func(ctx context.Context, call Call) Result {
			return Result{OK: true, Output: call.Context.ConfigSummary, Evidence: map[string]any{"kind": "config_summary"}}
		},
	}
}

func WebSearch() Definition {
	return Definition{
		Name:        "web.search",
		Description: "Search the web. Uses Tavily when configured, then DuckDuckGo fallback.",
		Risk:        RiskSafeRead,
		ArgsSchema: map[string]string{
			"query":       "search query",
			"max_results": "optional max results",
		},
		Run: func(ctx context.Context, call Call) Result {
			query := strings.TrimSpace(firstNonEmpty(call.Args["query"], call.Args["q"]))
			if query == "" {
				return ErrorResult("query is required")
			}
			if call.Context.Search.TavilyEnabled && strings.TrimSpace(call.Context.Search.TavilyAPIKey) != "" {
				if result := tavilySearch(ctx, call.Context.Search, query); result.OK {
					return result
				}
			}
			if !call.Context.Search.DuckDuckGoEnabled {
				return ErrorResult("web.search has no enabled provider")
			}
			return duckDuckGoSearch(ctx, call.Context.Search, query)
		},
	}
}

func FileRead() Definition {
	return Definition{
		Name:        "file.read",
		Description: "Read a text file under the project root or Mateway workspace.",
		Risk:        RiskSafeRead,
		ArgsSchema:  map[string]string{"path": "file path"},
		Run: func(ctx context.Context, call Call) Result {
			path, err := ResolveAllowedPath(call.Args["path"], call.Context)
			if err != nil {
				return ErrorResult(err.Error())
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return ErrorResult(err.Error())
			}
			return Result{OK: true, Output: Truncate(string(data), DefaultOutputLimit), Evidence: map[string]any{
				"kind":  "file_read",
				"path":  path,
				"bytes": len(data),
			}}
		},
	}
}

func FileWrite() Definition {
	return Definition{
		Name:        "file.write",
		Description: "Write a text file under allowed roots. Requires confirmation by default.",
		Risk:        RiskGuardedMutation,
		ArgsSchema:  map[string]string{"path": "file path", "content": "new file content"},
		Run: func(ctx context.Context, call Call) Result {
			path, err := ResolveAllowedPath(call.Args["path"], call.Context)
			if err != nil {
				return ErrorResult(err.Error())
			}
			content := call.Args["content"]
			preview := fmt.Sprintf("Write preview for %s\n\n%s", path, Truncate(content, 3000))
			if !call.Confirmed {
				return ConfirmResult("file.write requires confirmation before writing.\n\n"+preview, map[string]any{"kind": "write_preview", "path": path, "bytes": len(content)})
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return ErrorResult(err.Error())
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				return ErrorResult(err.Error())
			}
			return Result{OK: true, Output: "wrote " + path, Evidence: map[string]any{"kind": "file_write", "path": path, "bytes": len(content)}}
		},
	}
}

func FilePatch() Definition {
	return Definition{
		Name:        "file.patch",
		Description: "Patch a text file by replacing old text with new text, or appending content. Requires confirmation by default.",
		Risk:        RiskGuardedMutation,
		ArgsSchema: map[string]string{
			"path":      "file path",
			"old":       "old text to replace",
			"new":       "new text",
			"append":    "content to append when old is empty",
			"confirmed": "true to apply",
		},
		Run: func(ctx context.Context, call Call) Result {
			path, err := ResolveAllowedPath(call.Args["path"], call.Context)
			if err != nil {
				return ErrorResult(err.Error())
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return ErrorResult(err.Error())
			}
			oldText := string(data)
			old := call.Args["old"]
			newText := oldText
			if old != "" {
				count := strings.Count(oldText, old)
				if count == 0 {
					return ErrorResult("old text not found")
				}
				if count > 1 {
					return ErrorResult("old text is not unique")
				}
				newText = strings.Replace(oldText, old, call.Args["new"], 1)
			} else if appendText := call.Args["append"]; appendText != "" {
				newText = oldText
				if !strings.HasSuffix(newText, "\n") {
					newText += "\n"
				}
				newText += appendText
				if !strings.HasSuffix(newText, "\n") {
					newText += "\n"
				}
			} else {
				return ErrorResult("old or append is required")
			}
			diff := simpleDiff(oldText, newText)
			if !call.Confirmed {
				return ConfirmResult("file.patch requires confirmation before applying.\n\n"+Truncate(diff, 4000), map[string]any{"kind": "patch_preview", "path": path})
			}
			if err := os.WriteFile(path, []byte(newText), 0o644); err != nil {
				return ErrorResult(err.Error())
			}
			return Result{OK: true, Output: "patched " + path + "\n\n" + Truncate(diff, 4000), Evidence: map[string]any{"kind": "file_patch", "path": path}}
		},
	}
}

func ShellRun() Definition {
	return Definition{
		Name:        "shell.run",
		Description: "Run a non-interactive local shell command with timeout. Dangerous commands require confirmation.",
		Risk:        RiskDangerous,
		ArgsSchema: map[string]string{
			"command":   "shell command",
			"workdir":   "optional working directory",
			"confirmed": "true to run dangerous command",
		},
		Run: func(ctx context.Context, call Call) Result {
			command := strings.TrimSpace(call.Args["command"])
			if command == "" {
				return ErrorResult("command is required")
			}
			if IsDangerousCommand(command) && !call.Confirmed {
				return ConfirmResult("shell.run blocked pending confirmation for dangerous command:\n\n"+command, map[string]any{"kind": "command_confirm", "command": command})
			}
			workdir := call.Context.ProjectRoot
			if raw := strings.TrimSpace(call.Args["workdir"]); raw != "" {
				resolved, err := ResolveAllowedPath(raw, call.Context)
				if err != nil {
					return ErrorResult(err.Error())
				}
				workdir = resolved
			}
			runCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			cmd := exec.CommandContext(runCtx, "sh", "-lc", command)
			cmd.Dir = workdir
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			err := cmd.Run()
			exitCode := 0
			if err != nil {
				exitCode = 1
				if exitErr, ok := err.(*exec.ExitError); ok {
					exitCode = exitErr.ExitCode()
				}
			}
			output := strings.TrimSpace(stdout.String())
			errText := strings.TrimSpace(stderr.String())
			combined := strings.TrimSpace(output)
			if errText != "" {
				combined += "\n\nstderr:\n" + errText
			}
			if combined == "" {
				combined = fmt.Sprintf("command exited with code %d", exitCode)
			}
			return Result{OK: err == nil, Output: Truncate(combined, DefaultOutputLimit), Error: errorString(err), Evidence: map[string]any{
				"kind":      "shell",
				"command":   command,
				"workdir":   workdir,
				"exit_code": exitCode,
			}}
		},
	}
}

func UserAsk() Definition {
	return Definition{
		Name:        "user.ask",
		Description: "Ask user for missing information.",
		Risk:        RiskSafeRead,
		ArgsSchema:  map[string]string{"question": "question for user"},
		Run: func(ctx context.Context, call Call) Result {
			q := strings.TrimSpace(call.Args["question"])
			if q == "" {
				q = "I need more information to continue."
			}
			return Result{OK: false, Output: q, RequiresConfirm: true, ConfirmMessage: q, Evidence: map[string]any{"kind": "user_input_required"}}
		},
	}
}

func tavilySearch(ctx context.Context, cfg SearchConfig, query string) Result {
	maxResults := cfg.TavilyMaxResults
	if maxResults <= 0 {
		maxResults = 5
	}
	baseURL := strings.TrimRight(firstNonEmpty(cfg.TavilyBaseURL, "https://api.tavily.com"), "/")
	body := map[string]any{
		"query":          query,
		"max_results":    maxResults,
		"search_depth":   firstNonEmpty(cfg.TavilySearchDepth, "advanced"),
		"topic":          firstNonEmpty(cfg.TavilyTopic, "general"),
		"include_answer": false,
	}
	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/search", bytes.NewReader(payload))
	if err != nil {
		return ErrorResult(err.Error())
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+cfg.TavilyAPIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ErrorResult(err.Error())
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ErrorResult(fmt.Sprintf("tavily search failed status=%d body=%s", resp.StatusCode, Truncate(string(data), 1000)))
	}
	var parsed struct {
		Results []struct {
			Title   string  `json:"title"`
			URL     string  `json:"url"`
			Content string  `json:"content"`
			Score   float64 `json:"score"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return ErrorResult(err.Error())
	}
	lines := []string{"Search results for: " + query, sourceQualityHint(query)}
	for i, item := range parsed.Results {
		lines = append(lines, fmt.Sprintf("%d. %s\n%s\n%s", i+1, item.Title, item.URL, item.Content))
	}
	return Result{OK: true, Output: Truncate(strings.Join(lines, "\n\n"), DefaultOutputLimit), Evidence: map[string]any{"kind": "web_search", "provider": "tavily", "query": query, "result_count": len(parsed.Results)}}
}

func duckDuckGoSearch(ctx context.Context, cfg SearchConfig, query string) Result {
	maxResults := cfg.DuckDuckGoMaxResults
	if maxResults <= 0 {
		maxResults = 5
	}
	u := "https://api.duckduckgo.com/?" + url.Values{
		"q":             {query},
		"format":        {"json"},
		"no_html":       {"1"},
		"skip_disambig": {"1"},
	}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return ErrorResult(err.Error())
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ErrorResult(err.Error())
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	var parsed struct {
		AbstractText  string `json:"AbstractText"`
		AbstractURL   string `json:"AbstractURL"`
		RelatedTopics []struct {
			Text     string `json:"Text"`
			FirstURL string `json:"FirstURL"`
		} `json:"RelatedTopics"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return ErrorResult(err.Error())
	}
	lines := []string{"Search results for: " + query, sourceQualityHint(query)}
	if parsed.AbstractText != "" {
		lines = append(lines, parsed.AbstractText+"\n"+parsed.AbstractURL)
	}
	for _, item := range parsed.RelatedTopics {
		if len(lines) > maxResults {
			break
		}
		if item.Text != "" {
			lines = append(lines, item.Text+"\n"+item.FirstURL)
		}
	}
	return Result{OK: true, Output: Truncate(strings.Join(lines, "\n\n"), DefaultOutputLimit), Evidence: map[string]any{"kind": "web_search", "provider": "duckduckgo", "query": query, "result_count": len(lines) - 2}}
}

func sourceQualityHint(query string) string {
	if !looksTimeSensitiveSearch(query) {
		return "Source quality hint: classify sources as official/primary, authoritative media, secondary roundup, or unclear-date before using them."
	}
	return strings.Join([]string{
		"Source quality hint for fresh/current query:",
		"- Prefer official docs/blogs, official GitHub, release notes, changelogs, academic or standards sources.",
		"- Treat secondary roundups, SEO listicles, reposts, and unclear-date pages as weak evidence.",
		"- Do not present a claim as latest/current unless the source date or release context supports it.",
	}, "\n")
}

func looksTimeSensitiveSearch(query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	cues := []string{
		"latest", "current", "recent", "official", "release", "changelog", "2026", "trend", "trends", "course", "courses",
		"最新", "当前", "最近", "官方", "发布", "版本", "趋势", "走向", "课程", "最热", "权威",
	}
	for _, cue := range cues {
		if strings.Contains(q, cue) {
			return true
		}
	}
	return false
}

func simpleDiff(oldText, newText string) string {
	oldLines := strings.Split(oldText, "\n")
	newLines := strings.Split(newText, "\n")
	return fmt.Sprintf("--- before\n+++ after\n-old lines: %d\n+new lines: %d\n\n--- before preview\n%s\n\n+++ after preview\n%s", len(oldLines), len(newLines), Truncate(oldText, 1800), Truncate(newText, 1800))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
