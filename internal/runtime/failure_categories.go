package runtime

import (
	"strings"

	"github.com/dongping/mateway/internal/agentcore"
)

type FailureCategory int

const (
	FailureUnknown        FailureCategory = iota
	FailureRetryable                      // timeout, network, rate limit
	FailureFallback                       // path blocked → terminal.run, binary → sed
	FailureNeedsUserInput                 // permission, missing config
	FailureBlocked                        // destructive, tool not found
)

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
		if strings.Contains(content, "ssrf") || strings.Contains(content, "internal") {
			return FailureInfo{
				Category: FailureBlocked,
				Reason:   "URL blocked by SSRF protection",
				Guidance: "use alternative inspection methods like terminal.run curl with allowed hosts",
			}
		}
		if strings.Contains(content, "too many requests") || strings.Contains(content, "429") {
			return FailureInfo{
				Category: FailureRetryable,
				Reason:   "rate limited by target server",
				Guidance: "use another source, search provider cache, official API, or wait; do not repeatedly fetch the same host",
			}
		}
		if strings.Contains(content, "cloudflare") || strings.Contains(content, "please enable cookies") ||
			strings.Contains(content, "please enable js") || strings.Contains(content, "disable any ad blocker") ||
			strings.Contains(content, "captcha") || strings.Contains(content, "challenge") {
			return FailureInfo{
				Category: FailureBlocked,
				Reason:   "bot protection or JS challenge page",
				Guidance: "use web.search result summaries, official data API, or terminal.run API call; do not treat challenge page body as useful content",
			}
		}
		if strings.Contains(content, "timeout") || strings.Contains(content, "timed out") ||
			strings.Contains(content, "deadline") ||
			strings.Contains(content, "client.timeout") || strings.Contains(content, "i/o timeout") {
			return FailureInfo{
				Category: FailureRetryable,
				Reason:   "fetch request timed out",
				Guidance: "retry once with alternate source or API; avoid repeated timeout on same host",
			}
		}
		if strings.Contains(content, "connection refused") || strings.Contains(content, "connection reset") ||
			strings.Contains(content, "no such host") || strings.Contains(content, "dns") {
			return FailureInfo{
				Category: FailureRetryable,
				Reason:   "network or DNS failure",
				Guidance: "verify the URL is correct and the host is reachable; use web.search to find current URL",
			}
		}
		if strings.Contains(content, "status 4") || strings.Contains(content, "status 5") {
			return FailureInfo{
				Category: FailureFallback,
				Reason:   "server returned HTTP error",
				Guidance: "the URL returned an error status; use web.search to find an alternative source or official API",
			}
		}
		return FailureInfo{
			Category: FailureRetryable,
			Reason:   "fetch returned an error",
			Guidance: "verify the URL and retry, or use web.search as an alternative",
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
