package runtime

import (
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/session"
)

type FailureCategory int

const (
	FailureUnknown        FailureCategory = iota
	FailureRetryable                      // timeout, network, rate limit
	FailureFallback                       // path blocked → terminal.run, binary → sed
	FailureNeedsUserInput                 // permission, missing config
	FailureBlocked                        // destructive, tool not found
)

type FetchFailureKind string

const (
	FetchFailureTimeout            FetchFailureKind = "timeout"
	FetchFailureNetwork            FetchFailureKind = "network"
	FetchFailureRateLimit          FetchFailureKind = "rate_limit"
	FetchFailureBlockedOrBot       FetchFailureKind = "blocked_or_bot_protection"
	FetchFailureUnsupportedContent FetchFailureKind = "unsupported_content"
	FetchFailurePolicyDenied       FetchFailureKind = "policy_denied"
	FetchFailurePathDenied         FetchFailureKind = "path_denied"
	FetchFailureUnknown            FetchFailureKind = "unknown"
)

type FetchFailureEntry struct {
	URL        string
	Domain     string
	Kind       FetchFailureKind
	ToolCallID string
	Timestamp  time.Time
}

type FetchFailureBudget struct {
	mu           sync.Mutex
	entries      []FetchFailureEntry
	maxPerURL    int
	maxPerDomain int
}

func NewFetchFailureBudget(maxPerURL, maxPerDomain int) *FetchFailureBudget {
	if maxPerURL <= 0 {
		maxPerURL = 2
	}
	if maxPerDomain <= 0 {
		maxPerDomain = 3
	}
	return &FetchFailureBudget{maxPerURL: maxPerURL, maxPerDomain: maxPerDomain}
}

func (b *FetchFailureBudget) Record(entry FetchFailureEntry) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries = append(b.entries, entry)
}

func (b *FetchFailureBudget) IsExhausted(rawURL string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.entries) == 0 {
		return false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	normalized := strings.ToLower(parsed.Hostname() + parsed.Path)
	domain := strings.ToLower(parsed.Hostname())
	urlCount := 0
	domainCount := 0
	for _, e := range b.entries {
		if strings.ToLower(e.Domain) == domain {
			domainCount++
			if strings.ToLower(e.URL) == rawURL || (parsed.Path != "" && strings.HasSuffix(strings.ToLower(e.URL), normalized)) {
				urlCount++
			}
		}
	}
	return urlCount >= b.maxPerURL || domainCount >= b.maxPerDomain
}

func (b *FetchFailureBudget) Snapshot() []FetchFailureEntry {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]FetchFailureEntry, len(b.entries))
	copy(out, b.entries)
	return out
}

func resolveFetchURL(rawURL string) (*url.URL, error) {
	return url.Parse(rawURL)
}

func blockPlanItemsForFetchFailure(state *session.State, taskID string, rawURL string) {
	task := state.TaskByID(taskID)
	if task == nil || task.Execution.Contract == nil {
		return
	}
	contract := *task.Execution.Contract
	changed := false
	for i := range contract.PlanItems {
		item := &contract.PlanItems[i]
		if !strings.EqualFold(strings.TrimSpace(item.Tool), "web.fetch") {
			continue
		}
		if normalizePlanStatus(item.Status) == "completed" {
			continue
		}
		item.Status = "blocked"
		item.Evidence = fmt.Sprintf("fetch budget exhausted for %s", rawURL)
		item.UpdatedAt = time.Now()
		changed = true
	}
	if changed {
		state.SetTaskContract(taskID, contract)
	}
}

func fetchBudgetForState(state *session.State, taskID string) *FetchFailureBudget {
	task := state.TaskByID(taskID)
	if task == nil {
		return NewFetchFailureBudget(2, 3)
	}
	budget := NewFetchFailureBudget(2, 3)
	for _, ev := range task.Execution.Events {
		if ev.Type != "fetch_failure" {
			continue
		}
		entry, ok := fetchFailureEntryFromEvent(ev)
		if !ok {
			continue
		}
		budget.Record(entry)
	}
	return budget
}

func fetchFailureEntryFromEvent(ev session.ExecutionEvent) (FetchFailureEntry, bool) {
	if ev.Evidence == nil {
		return FetchFailureEntry{}, false
	}
	rawURL, _ := ev.Evidence["url"].(string)
	domain, _ := ev.Evidence["domain"].(string)
	kindText, _ := ev.Evidence["failure_kind"].(string)
	if strings.TrimSpace(rawURL) == "" || strings.TrimSpace(domain) == "" || strings.TrimSpace(kindText) == "" {
		return FetchFailureEntry{}, false
	}
	return FetchFailureEntry{
		URL:        rawURL,
		Domain:     domain,
		Kind:       FetchFailureKind(kindText),
		ToolCallID: ev.Tool,
		Timestamp:  ev.CreatedAt,
	}, true
}

func ClassifyFetchFailure(result agentcore.ToolResult) (FetchFailureKind, string) {
	if !result.IsError {
		return "", ""
	}
	content := strings.ToLower(result.Content)
	if evKind, ok := result.Evidence["failure_kind"].(string); ok && evKind == "bot_protection" {
		return FetchFailureBlockedOrBot, "bot protection or JS challenge page"
	}
	if strings.Contains(content, "ssrf") || strings.Contains(content, "internal") {
		return FetchFailurePolicyDenied, "URL blocked by SSRF protection"
	}
	if strings.Contains(content, "too many requests") || strings.Contains(content, "429") {
		return FetchFailureRateLimit, "rate limited by target server"
	}
	if strings.Contains(content, "cloudflare") || strings.Contains(content, "please enable cookies") ||
		strings.Contains(content, "please enable js") || strings.Contains(content, "disable any ad blocker") ||
		strings.Contains(content, "captcha") || strings.Contains(content, "challenge") {
		return FetchFailureBlockedOrBot, "bot protection or JS challenge page"
	}
	if strings.Contains(content, "timeout") || strings.Contains(content, "timed out") ||
		strings.Contains(content, "deadline") ||
		strings.Contains(content, "client.timeout") || strings.Contains(content, "i/o timeout") {
		return FetchFailureTimeout, "fetch request timed out"
	}
	if strings.Contains(content, "connection refused") || strings.Contains(content, "connection reset") ||
		strings.Contains(content, "no such host") || strings.Contains(content, "dns") {
		return FetchFailureNetwork, "network or DNS failure"
	}
	return FetchFailureUnknown, "fetch returned an error"
}

func (c FailureCategory) String() string {
	switch c {
	case FailureRetryable:
		return "retryable"
	case FailureFallback:
		return "fallback"
	case FailureNeedsUserInput:
		return "needs_user_input"
	case FailureBlocked:
		return "blocked"
	default:
		return "unknown"
	}
}

type FailureInfo struct {
	Category FailureCategory
	Reason   string
	Guidance string
}

func ClassifyToolFailure(toolName string, result agentcore.ToolResult) FailureInfo {
	if !result.IsError {
		return FailureInfo{Category: FailureUnknown}
	}
	content := strings.ToLower(result.Content)

	switch toolName {
	case "file.edit":
		if strings.Contains(content, "found") && strings.Contains(content, "times") {
			return FailureInfo{
				Category: FailureRetryable,
				Reason:   "old_string matches multiple locations",
				Guidance: "provide more surrounding context in old_string to make it unique, or use replace_all=true",
			}
		}
		if strings.Contains(content, "old_string must not be empty") {
			return FailureInfo{
				Category: FailureNeedsUserInput,
				Reason:   "old_string is empty",
				Guidance: "old_string must be non-empty; read the file first to find the exact text to replace",
			}
		}
		if strings.Contains(content, "old_string not found") {
			return FailureInfo{
				Category: FailureRetryable,
				Reason:   "old_string not found in file",
				Guidance: "re-read the file with file.read first, then construct an exact old_string matching the current file content",
			}
		}
		if strings.Contains(content, "binary") {
			return FailureInfo{
				Category: FailureFallback,
				Reason:   "target file is binary",
				Guidance: "use terminal.run with sed, perl, or a script-based approach instead",
			}
		}
		if strings.Contains(content, "permission denied") {
			return FailureInfo{
				Category: FailureNeedsUserInput,
				Reason:   "file permission denied",
				Guidance: "check permissions with terminal.run ls -la, and ask the user if elevated permissions are needed",
			}
		}
		if strings.Contains(content, "profile proposal") {
			return FailureInfo{
				Category: FailureBlocked,
				Reason:   "profile change routed to review",
				Guidance: "promote the proposal with mateway agent-profile proposal promote PROPOSAL_ID, then retry",
			}
		}

	case "file.read":
		if strings.Contains(content, "binary") {
			return FailureInfo{
				Category: FailureFallback,
				Reason:   "target file is binary",
				Guidance: "use terminal.run file, hexdump, or xxd to inspect the file",
			}
		}
		if strings.Contains(content, "file too large") {
			return FailureInfo{
				Category: FailureFallback,
				Reason:   "file exceeds size limit",
				Guidance: "use terminal.run head, tail, or rg to inspect the file in parts",
			}
		}

	case "terminal.run":
		if strings.Contains(content, "destructive") || strings.Contains(content, "policy_classification") {
			return FailureInfo{
				Category: FailureBlocked,
				Reason:   "command classified as destructive",
				Guidance: "try a read-only alternative: ls, cat, grep, rg, find, head, tail, stat, file, wc",
			}
		}
		if strings.Contains(content, "secret value") {
			return FailureInfo{
				Category: FailureBlocked,
				Reason:   "command contains a known secret value",
				Guidance: "use mateway.required_secret or env_secrets to inject secrets safely instead of passing them in command arguments",
			}
		}
		if content == "signal: killed" || strings.Contains(result.Content, "signal: killed") {
			return FailureInfo{
				Category: FailureRetryable,
				Reason:   "command timed out",
				Guidance: "reduce scope or increase timeout_seconds",
			}
		}
		if strings.Contains(result.Content, "timed_out") || strings.Contains(content, "timed out") || strings.Contains(content, "deadline") {
			return FailureInfo{
				Category: FailureRetryable,
				Reason:   "command timed out",
				Guidance: "reduce scope or increase timeout_seconds",
			}
		}
		if strings.Contains(content, "permission denied") {
			return FailureInfo{
				Category: FailureNeedsUserInput,
				Reason:   "command permission denied",
				Guidance: "check current permissions and ask the user if elevated access is needed",
			}
		}
		if strings.Contains(content, "command not found") || strings.Contains(content, "no such file or directory") {
			return FailureInfo{
				Category: FailureFallback,
				Reason:   "command or executable not found",
				Guidance: "verify the executable exists with command -v or which, then use the correct path or install it",
			}
		}

	case "web.search":
		if strings.Contains(content, "timeout") || strings.Contains(content, "deadline") {
			return FailureInfo{
				Category: FailureRetryable,
				Reason:   "search request timed out",
				Guidance: "retry with fewer or more specific search terms",
			}
		}
		return FailureInfo{
			Category: FailureRetryable,
			Reason:   "search returned an error",
			Guidance: "retry with different search terms or use a different provider",
		}

	case "web.fetch":
		kind, reason := ClassifyFetchFailure(result)
		switch kind {
		case FetchFailureTimeout:
			return FailureInfo{
				Category: FailureRetryable,
				Reason:   reason,
				Guidance: "retry once with alternate source or API; avoid repeated timeout on same host",
			}
		case FetchFailureNetwork:
			return FailureInfo{
				Category: FailureRetryable,
				Reason:   reason,
				Guidance: "verify the URL is correct and the host is reachable; use web.search to find current URL",
			}
		case FetchFailureRateLimit:
			return FailureInfo{
				Category: FailureRetryable,
				Reason:   reason,
				Guidance: "use another source, search provider cache, official API, or wait; do not repeatedly fetch the same host",
			}
		case FetchFailureBlockedOrBot:
			return FailureInfo{
				Category: FailureBlocked,
				Reason:   reason,
				Guidance: "use web.search result summaries, official data API, or terminal.run API call; do not treat challenge page body as useful content",
			}
		case FetchFailurePolicyDenied:
			return FailureInfo{
				Category: FailureBlocked,
				Reason:   reason,
				Guidance: "use alternative inspection methods like terminal.run curl with allowed hosts",
			}
		case FetchFailurePathDenied:
			return FailureInfo{
				Category: FailureBlocked,
				Reason:   reason,
				Guidance: "path is outside allowed roots; use an allowed path or state the concrete blocker to the user",
			}
		default:
			if strings.Contains(content, "status 4") || strings.Contains(content, "status 5") {
				return FailureInfo{
					Category: FailureFallback,
					Reason:   "server returned HTTP error",
					Guidance: "the URL returned an error status; use web.search to find an alternative source or official API",
				}
			}
			return FailureInfo{
				Category: FailureFallback,
				Reason:   "fetch returned an error",
				Guidance: "verify the URL and retry, or use web.search as an alternative",
			}
		}

	case "file.write":
		if strings.Contains(content, "profile proposal") {
			return FailureInfo{
				Category: FailureBlocked,
				Reason:   "profile change routed to review",
				Guidance: "promote the proposal with mateway agent-profile proposal promote PROPOSAL_ID, then retry",
			}
		}

	case "file.delete":
		if strings.Contains(content, "outside allowed roots") {
			return FailureInfo{
				Category: FailureBlocked,
				Reason:   "path outside allowed roots",
				Guidance: "path is outside allowed workspace roots; use an allowed path or state the concrete blocker to the user",
			}
		}
		if strings.Contains(content, "protected path") {
			return FailureInfo{
				Category: FailureBlocked,
				Reason:   "path is protected from deletion",
				Guidance: "this path is protected and cannot be deleted; check if deletion is truly needed",
			}
		}

	case "schedule.manage":
		if strings.Contains(content, "not found") {
			return FailureInfo{
				Category: FailureNeedsUserInput,
				Reason:   "schedule not found",
				Guidance: "use schedule.manage action=list to find the correct schedule id",
			}
		}
	}

	if strings.Contains(content, "outside allowed roots") {
		return FailureInfo{
			Category: FailureBlocked,
			Reason:   "path outside allowed roots",
			Guidance: "use terminal.run with an allowed path instead, or check accessible_paths configuration",
		}
	}

	return FailureInfo{Category: FailureUnknown}
}

func classifyTurnFailures(results []agentcore.ToolResult, calls []agentcore.ToolCall) map[string]FailureInfo {
	if len(results) == 0 {
		return nil
	}
	callByName := map[string]string{}
	for _, call := range calls {
		if call.ID != "" && call.Name != "" {
			callByName[call.ID] = call.Name
		}
	}
	out := map[string]FailureInfo{}
	for _, result := range results {
		toolName := callByName[result.ToolCallID]
		info := ClassifyToolFailure(toolName, result)
		if info.Category == FailureUnknown {
			continue
		}
		out[toolName] = info
	}
	return out
}

func failureCategories(infos map[string]FailureInfo) []string {
	if len(infos) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, info := range infos {
		s := info.Category.String()
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
