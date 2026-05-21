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
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/memory"
	"github.com/dongping/mateway/internal/skill"
	"github.com/dongping/mateway/internal/textmatch"
)

func RegisterBuiltins(r *Registry) {
	r.Register(TimeNow())
	r.Register(ConfigSummary())
	r.Register(MemorySearch())
	r.Register(MemoryIndex())
	r.Register(SkillSearch())
	r.Register(SkillInstall())
	r.Register(SoftwareSearch())
	r.Register(WebSearch())
	r.Register(FileRead())
	r.Register(ProjectIndex())
	r.Register(FileSummary())
	r.Register(FileWrite())
	r.Register(FilePatch())
	r.Register(ShellRun())
	r.Register(UserAsk())
}

func SkillSearch() Definition {
	return Definition{
		Name:        "skill.search",
		Description: "Search installable agent skills from configured remote catalogs, then report local Mateway workspace install state.",
		Risk:        RiskSafeRead,
		ArgsSchema: map[string]string{
			"query": "skill search query",
			"limit": "optional result limit",
		},
		Run: func(ctx context.Context, call Call) Result {
			query := strings.TrimSpace(firstNonEmpty(call.Args["query"], call.Args["q"], call.Args["name"]))
			if query == "" {
				return ErrorResult("query is required")
			}
			limit := parsePositiveArg(call.Args["limit"], 6)
			items, err := skill.SearchCatalog(ctx, call.Context.Workspace, query, skill.CatalogSearchOptions{Limit: limit})
			if err != nil {
				return ErrorResult(err.Error())
			}
			return Result{
				OK:       true,
				Output:   renderSkillSearchOutput(query, items),
				Evidence: skillSearchEvidence(query, items),
			}
		},
	}
}

func SkillInstall() Definition {
	return Definition{
		Name:        "skill.install",
		Description: "Install one agent skill into the Mateway workspace skills directory. Requires confirmation before writing files.",
		Risk:        RiskGuardedMutation,
		ArgsSchema: map[string]string{
			"name": "skill name or URL",
			"url":  "optional direct skill URL",
		},
		Run: func(ctx context.Context, call Call) Result {
			ref := strings.TrimSpace(firstNonEmpty(call.Args["url"], call.Args["name"], call.Args["query"]))
			if ref == "" {
				return ErrorResult("skill name or URL is required")
			}
			if !call.Confirmed {
				items, _ := skill.SearchCatalog(ctx, call.Context.Workspace, ref, skill.CatalogSearchOptions{Limit: 1})
				preview := renderSkillInstallPreview(ref, call.Context.Workspace, items)
				return ConfirmResult(preview, map[string]any{"kind": "skill_install_preview", "ref": ref, "workspace": call.Context.Workspace})
			}
			result, err := skill.InstallCatalogSkill(ctx, call.Context.Workspace, ref, skill.CatalogSearchOptions{Limit: 1})
			if err != nil {
				return ErrorResult(err.Error())
			}
			return Result{
				OK:       true,
				Output:   renderSkillInstallOutput(result),
				Evidence: map[string]any{"kind": "skill_install", "name": result.Item.Name, "source": result.Item.Source, "url": result.Item.URL, "install_url": result.Item.InstallURL, "target_path": result.TargetPath, "already_installed": result.AlreadyDone},
			}
		},
	}
}

func SoftwareSearch() Definition {
	return Definition{
		Name:        "software.search",
		Description: "Search public software, CLI tools, repositories, and installation clues with GitHub-first fallback behavior.",
		Risk:        RiskSafeRead,
		ArgsSchema: map[string]string{
			"query": "software or CLI query",
			"limit": "optional result limit",
		},
		Run: func(ctx context.Context, call Call) Result {
			query := strings.TrimSpace(firstNonEmpty(call.Args["query"], call.Args["q"], call.Args["name"]))
			if query == "" {
				return ErrorResult("query is required")
			}
			limit := parsePositiveArg(call.Args["limit"], 5)
			results, err := githubSoftwareSearch(ctx, query, limit)
			if err != nil {
				return ErrorResult(err.Error())
			}
			if len(results) == 0 {
				return Result{OK: true, Output: "No software results found for: " + query, Evidence: map[string]any{"kind": "software_search", "provider": "github", "query": query, "result_count": 0}}
			}
			return Result{OK: true, Output: renderSoftwareSearchOutput(query, results), Evidence: softwareSearchEvidence(query, results)}
		},
	}
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

func MemorySearch() Definition {
	return Definition{
		Name:        "memory.search",
		Description: "Search reviewed long memory and return snippets with path and line evidence.",
		Risk:        RiskSafeRead,
		ArgsSchema: map[string]string{
			"query":   "search query",
			"agent":   "optional agent id, defaults to main",
			"limit":   "optional result limit",
			"rebuild": "optional true to rebuild index before searching",
		},
		Run: func(ctx context.Context, call Call) Result {
			query := strings.TrimSpace(firstNonEmpty(call.Args["query"], call.Args["q"]))
			if query == "" {
				return ErrorResult("query is required")
			}
			store, err := memoryStoreFromToolContext(call.Context)
			if err != nil {
				return ErrorResult(err.Error())
			}
			if parseBoolArg(call.Args["rebuild"]) {
				if _, err := store.RebuildIndex(time.Now()); err != nil {
					return ErrorResult(err.Error())
				}
			}
			limit := parsePositiveArg(call.Args["limit"], 4)
			results, err := store.SearchLong(memory.SearchOptions{
				AgentID:      firstNonEmpty(call.Args["agent"], "main"),
				Query:        query,
				Limit:        limit,
				SnippetLimit: 600,
			})
			if err != nil {
				return ErrorResult(err.Error())
			}
			return Result{
				OK:       true,
				Output:   renderMemorySearchOutput(query, results),
				Evidence: memorySearchEvidence(query, results),
			}
		},
	}
}

func memorySearchEvidence(query string, results []memory.SearchResult) map[string]any {
	evidence := map[string]any{
		"kind":         "memory_search",
		"query":        query,
		"result_count": len(results),
	}
	if len(results) > 0 {
		evidence["path"] = results[0].Path
		evidence["start_line"] = results[0].StartLine
		evidence["end_line"] = results[0].EndLine
	}
	return evidence
}

func MemoryIndex() Definition {
	return Definition{
		Name:        "memory.index",
		Description: "Return a concise summary of the rebuildable memory index.",
		Risk:        RiskSafeRead,
		ArgsSchema: map[string]string{
			"rebuild": "optional true to rebuild index before reading",
		},
		Run: func(ctx context.Context, call Call) Result {
			store, err := memoryStoreFromToolContext(call.Context)
			if err != nil {
				return ErrorResult(err.Error())
			}
			var result memory.RebuildIndexResult
			if parseBoolArg(call.Args["rebuild"]) {
				result, err = store.RebuildIndex(time.Now())
				if err != nil {
					return ErrorResult(err.Error())
				}
			} else {
				result, err = store.ReadIndex()
				if err != nil {
					return ErrorResult(err.Error())
				}
			}
			return Result{
				OK:     true,
				Output: renderMemoryIndexOutput(result),
				Evidence: map[string]any{
					"kind":        "memory_index",
					"path":        result.Path,
					"entry_count": len(result.Index.Entries),
					"issue_count": result.Index.IssueCount,
				},
			}
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
				"kind":       "file_read",
				"path":       path,
				"bytes":      len(data),
				"start_line": 1,
				"end_line":   countTextLines(string(data)),
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

func renderSkillSearchOutput(query string, items []skill.CatalogItem) string {
	if len(items) == 0 {
		return "No matching skills found for: " + query
	}
	lines := []string{"Skill search results for: " + query}
	for i, item := range items {
		status := "not installed"
		if item.Installed {
			status = "installed at " + item.InstallPath
		}
		desc := strings.TrimSpace(item.Description)
		if desc != "" {
			desc = "\n   " + Truncate(desc, 260)
		}
		lines = append(lines, fmt.Sprintf("%d. %s [%s]\n   source: %s\n   url: %s%s", i+1, item.Name, status, item.Source, item.URL, desc))
	}
	return strings.Join(lines, "\n\n")
}

func skillSearchEvidence(query string, items []skill.CatalogItem) map[string]any {
	evidence := map[string]any{"kind": "skill_search", "query": query, "result_count": len(items)}
	if len(items) > 0 {
		evidence["name"] = items[0].Name
		evidence["source"] = items[0].Source
		evidence["url"] = items[0].URL
		evidence["installed"] = items[0].Installed
	}
	return evidence
}

func renderSkillInstallPreview(ref, workspace string, items []skill.CatalogItem) string {
	if len(items) == 0 {
		return fmt.Sprintf("skill.install requires confirmation before writing files.\n\nRequested skill: %s\nTarget workspace: %s\n\nI will search the priority skill catalogs and install the best matching SKILL.md into workspace/skills after confirmation.", ref, workspace)
	}
	item := items[0]
	targetName := strings.ToLower(strings.TrimSpace(item.Name))
	if targetName == "" {
		targetName = ref
	}
	return fmt.Sprintf("skill.install requires confirmation before writing files.\n\nSkill: %s\nSource: %s\nURL: %s\nTarget workspace: %s\n\nConfirm to install this skill into workspace/skills.", item.Name, item.Source, item.URL, workspace)
}

func renderSkillInstallOutput(result skill.InstallResult) string {
	if result.AlreadyDone {
		return fmt.Sprintf("Skill already installed: %s\nPath: %s", result.Item.Name, result.TargetPath)
	}
	return fmt.Sprintf("Skill installed: %s\nSource: %s\nPath: %s", result.Item.Name, result.Item.Source, result.TargetPath)
}

type softwareResult struct {
	Name        string
	FullName    string
	URL         string
	Description string
	Language    string
	Stars       int
	Forks       int
	UpdatedAt   string
	PushedAt    string
	License     string
	OwnerType   string
}

func githubSoftwareSearch(ctx context.Context, query string, limit int) ([]softwareResult, error) {
	if limit <= 0 {
		limit = 5
	}
	searches := softwareSearchQueries(query)
	seen := map[string]bool{}
	var out []softwareResult
	client := &http.Client{Timeout: 10 * time.Second}
	for _, q := range searches {
		u := "https://api.github.com/search/repositories?" + url.Values{
			"q":        {q},
			"per_page": {fmt.Sprint(limit)},
			"sort":     {"stars"},
			"order":    {"desc"},
		}.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("accept", "application/vnd.github+json")
		req.Header.Set("user-agent", "mateway-software-search/1.0")
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			continue
		}
		var parsed struct {
			Items []struct {
				Name        string `json:"name"`
				FullName    string `json:"full_name"`
				HTMLURL     string `json:"html_url"`
				Description string `json:"description"`
				Language    string `json:"language"`
				Stars       int    `json:"stargazers_count"`
				Forks       int    `json:"forks_count"`
				UpdatedAt   string `json:"updated_at"`
				PushedAt    string `json:"pushed_at"`
				Owner       struct {
					Type string `json:"type"`
				} `json:"owner"`
				License *struct {
					SPDXID string `json:"spdx_id"`
					Name   string `json:"name"`
				} `json:"license"`
			} `json:"items"`
		}
		if err := json.Unmarshal(data, &parsed); err != nil {
			continue
		}
		for _, item := range parsed.Items {
			if seen[item.FullName] {
				continue
			}
			seen[item.FullName] = true
			license := ""
			if item.License != nil {
				license = firstNonEmpty(item.License.SPDXID, item.License.Name)
			}
			out = append(out, softwareResult{
				Name:        item.Name,
				FullName:    item.FullName,
				URL:         item.HTMLURL,
				Description: item.Description,
				Language:    item.Language,
				Stars:       item.Stars,
				Forks:       item.Forks,
				UpdatedAt:   item.UpdatedAt,
				PushedAt:    item.PushedAt,
				License:     license,
				OwnerType:   item.Owner.Type,
			})
			if len(out) >= limit {
				return out, nil
			}
		}
	}
	return out, nil
}

func softwareSearchQueries(query string) []string {
	trimmed := strings.TrimSpace(query)
	queries := []string{trimmed}
	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "lark") || strings.Contains(lower, "飞书") {
		queries = append([]string{"larksuite cli", "feishu lark cli", trimmed}, queries...)
	}
	if !strings.Contains(lower, "cli") {
		queries = append(queries, trimmed+" cli")
	}
	return uniqueNonEmptyStrings(queries)
}

func renderSoftwareSearchOutput(query string, results []softwareResult) string {
	lines := []string{"Software search results for: " + query, "Source quality hint: prefer official organization repositories, recent activity, clear license, and installation docs."}
	for i, item := range results {
		desc := strings.TrimSpace(item.Description)
		if desc != "" {
			desc = "\n   " + Truncate(desc, 320)
		}
		lines = append(lines, fmt.Sprintf("%d. %s\n   url: %s\n   language: %s stars: %d forks: %d license: %s updated: %s pushed: %s owner: %s%s",
			i+1,
			firstNonEmpty(item.FullName, item.Name),
			item.URL,
			firstNonEmpty(item.Language, "unknown"),
			item.Stars,
			item.Forks,
			firstNonEmpty(item.License, "unknown"),
			item.UpdatedAt,
			item.PushedAt,
			firstNonEmpty(item.OwnerType, "unknown"),
			desc,
		))
	}
	return Truncate(strings.Join(lines, "\n\n"), DefaultOutputLimit)
}

func softwareSearchEvidence(query string, results []softwareResult) map[string]any {
	evidence := map[string]any{"kind": "software_search", "provider": "github", "query": query, "result_count": len(results)}
	if len(results) > 0 {
		evidence["name"] = results[0].FullName
		evidence["url"] = results[0].URL
		evidence["stars"] = results[0].Stars
		evidence["updated_at"] = results[0].UpdatedAt
	}
	return evidence
}

func uniqueNonEmptyStrings(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
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
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		if fallback := duckDuckGoHTMLSearch(ctx, query, maxResults); fallback.OK {
			return fallback
		}
		if github := githubSoftwareSearchFallback(ctx, query, maxResults); github.OK {
			return github
		}
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
		if fallback := duckDuckGoHTMLSearch(ctx, query, maxResults); fallback.OK {
			return fallback
		}
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

func duckDuckGoHTMLSearch(ctx context.Context, query string, maxResults int) Result {
	u := "https://duckduckgo.com/html/?" + url.Values{"q": {query}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return ErrorResult(err.Error())
	}
	req.Header.Set("user-agent", "mateway-web-search/1.0")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ErrorResult(err.Error())
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	links := extractDDGHTMLResults(string(data), maxResults)
	if len(links) == 0 {
		return ErrorResult("duckduckgo html returned no results")
	}
	lines := []string{"Search results for: " + query, sourceQualityHint(query)}
	for i, item := range links {
		lines = append(lines, fmt.Sprintf("%d. %s\n%s", i+1, item.Title, item.URL))
	}
	return Result{OK: true, Output: Truncate(strings.Join(lines, "\n\n"), DefaultOutputLimit), Evidence: map[string]any{"kind": "web_search", "provider": "duckduckgo_html", "query": query, "result_count": len(links)}}
}

type simpleSearchResult struct {
	Title string
	URL   string
}

var ddgResultPattern = regexp.MustCompile(`(?is)<a[^>]+class=["'][^"']*result__a[^"']*["'][^>]+href=["']([^"']+)["'][^>]*>(.*?)</a>`)

func extractDDGHTMLResults(raw string, maxResults int) []simpleSearchResult {
	if maxResults <= 0 {
		maxResults = 5
	}
	var out []simpleSearchResult
	for _, match := range ddgResultPattern.FindAllStringSubmatch(raw, -1) {
		if len(match) < 3 {
			continue
		}
		title := stripHTML(match[2])
		link := decodeDDGResultURL(match[1])
		if title == "" || link == "" {
			continue
		}
		out = append(out, simpleSearchResult{Title: title, URL: link})
		if len(out) >= maxResults {
			break
		}
	}
	return out
}

func decodeDDGResultURL(raw string) string {
	raw = html.UnescapeString(raw)
	u, err := url.Parse(raw)
	if err == nil {
		if target := u.Query().Get("uddg"); target != "" {
			if decoded, err := url.QueryUnescape(target); err == nil {
				return decoded
			}
			return target
		}
	}
	return raw
}

func stripHTML(raw string) string {
	text := regexp.MustCompile(`(?is)<[^>]+>`).ReplaceAllString(raw, " ")
	text = html.UnescapeString(text)
	return strings.Join(strings.Fields(text), " ")
}

func githubSoftwareSearchFallback(ctx context.Context, query string, maxResults int) Result {
	results, err := githubSoftwareSearch(ctx, query, maxResults)
	if err != nil || len(results) == 0 {
		return ErrorResult("github software fallback returned no results")
	}
	return Result{OK: true, Output: renderSoftwareSearchOutput(query, results), Evidence: softwareSearchEvidence(query, results)}
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
	}
	cues = append(cues, textmatch.Terms("fresh_search_cues")...)
	for _, cue := range cues {
		if strings.Contains(q, cue) {
			return true
		}
	}
	return false
}

func memoryStoreFromToolContext(ctx Context) (memory.Store, error) {
	workspace := strings.TrimSpace(ctx.Workspace)
	if workspace == "" {
		return memory.Store{}, fmt.Errorf("workspace is required")
	}
	return memory.NewStore(workspace), nil
}

func renderMemorySearchOutput(query string, results []memory.SearchResult) string {
	lines := []string{"Memory search results for: " + query}
	if len(results) == 0 {
		lines = append(lines, "No matching long memory found.")
		return strings.Join(lines, "\n")
	}
	for i, result := range results {
		lines = append(lines, fmt.Sprintf("%d. %s\npath: %s\nlines: %d-%d\nscore: %d\n%s", i+1, firstNonEmpty(result.Title, result.ID), result.Path, result.StartLine, result.EndLine, result.Score, result.Snippet))
	}
	return Truncate(strings.Join(lines, "\n\n"), DefaultOutputLimit)
}

func renderMemoryIndexOutput(result memory.RebuildIndexResult) string {
	counts := map[string]int{}
	sourceCount := 0
	for _, entry := range result.Index.Entries {
		counts[firstNonEmpty(entry.Area, "unknown")]++
		sourceCount += len(entry.ParsedSources)
	}
	areas := sortedIntKeys(counts)
	lines := []string{
		"Memory index: " + result.Path,
		fmt.Sprintf("entries=%d issues=%d parsed_sources=%d built_at=%s", len(result.Index.Entries), result.Index.IssueCount, sourceCount, result.Index.BuiltAt.Format(time.RFC3339)),
	}
	for _, area := range areas {
		lines = append(lines, fmt.Sprintf("- %s: %d", area, counts[area]))
	}
	return strings.Join(lines, "\n")
}

func countTextLines(text string) int {
	if text == "" {
		return 0
	}
	return len(strings.Split(text, "\n"))
}

func parseBoolArg(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func parsePositiveArg(value string, fallback int) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	n := 0
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return fallback
		}
		n = n*10 + int(ch-'0')
	}
	if n <= 0 {
		return fallback
	}
	return n
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

func sortedIntKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
