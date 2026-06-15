package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/dongping/mateway/internal/agentcore"
)

const compactedContentMinBytes = 1024

var (
	htmlScriptStylePattern = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>|<style\b[^>]*>.*?</style>|<noscript\b[^>]*>.*?</noscript>`)
	htmlTagPattern         = regexp.MustCompile(`(?s)<[^>]+>`)
	htmlSpacePattern       = regexp.MustCompile(`[ \t\r\f]+`)
	logPriorityPattern     = regexp.MustCompile(`(?i)\b(error|failed|failure|fatal|panic|warn|warning|exception|traceback|timeout|denied)\b`)
	repeatedBlankPattern   = regexp.MustCompile(`\n{3,}`)
)

func compactToolResultForModel(call agentcore.ToolCall, result agentcore.ToolResult, home, traceID string) agentcore.ToolResult {
	limit := modelToolLimit(result)
	originalChars := len(result.Content)
	compacted, compressor := compactToolContent(call.Name, result.Content, limit)
	if compacted == result.Content {
		return result
	}
	rawRef, rawPath := storeRawToolResult(home, traceID, call.Name, result)
	if rawRef != "" {
		compacted = strings.TrimRight(compacted, "\n") + "\n\n[full original tool output stored as raw_ref=" + rawRef + "; call toolresult.read with raw_ref and optional query if more detail is needed]"
	}
	result.Content = compacted
	if result.Evidence == nil {
		result.Evidence = map[string]any{}
	}
	result.Evidence["model_content_truncated"] = true
	result.Evidence["model_content_limit"] = limit
	result.Evidence["model_content_original_chars"] = originalChars
	result.Evidence["model_content_compressor"] = compressor
	if rawRef != "" {
		result.Evidence["raw_ref"] = rawRef
		result.Evidence["raw_retrieval_tool"] = "toolresult.read"
	}
	if rawPath != "" {
		result.Evidence["raw_path"] = rawPath
	}
	return result
}

func modelToolLimit(result agentcore.ToolResult) int {
	limit := modelToolContentLimit
	if result.Evidence != nil {
		if _, ok := result.Evidence["status"]; ok {
			limit = 4096
		}
		if result.Evidence["output_truncated"] == true {
			limit = 4096
		}
	}
	return limit
}

func compactToolContent(toolName, content string, limit int) (string, string) {
	if len(content) <= limit || len(content) < compactedContentMinBytes {
		return content, ""
	}
	switch toolName {
	case "terminal.run":
		if compacted := compactLogContent(content, limit); len(compacted) < len(content) {
			return compacted, "log"
		}
	case "file.read":
		if compacted := compactFileReadContent(content, limit); len(compacted) < len(content) {
			return compacted, "file_read"
		}
	case "web.search", "memory.search", "task.search":
		if compacted := compactSearchContent(content, limit); len(compacted) < len(content) {
			return compacted, "search_hits"
		}
	case "web.fetch":
		if compacted := compactHTMLContent(content, limit); len(compacted) < len(content) {
			return compacted, "html_text"
		}
	}
	if looksLikeHTML(content) {
		if compacted := compactHTMLContent(content, limit); len(compacted) < len(content) {
			return compacted, "html_text"
		}
	}
	compacted, truncated := truncateMiddle(content, limit)
	if truncated {
		return compacted, "middle_truncate"
	}
	return content, ""
}

func compactFileReadContent(content string, limit int) string {
	lines := strings.Split(content, "\n")
	if len(lines) <= 80 {
		out, _ := truncateMiddle(content, limit)
		return out
	}
	headCount := minInt(40, len(lines))
	tailCount := minInt(20, len(lines)-headCount)
	priority := priorityLines(lines, 40)
	var b strings.Builder
	b.WriteString("[model compacted file content]\n")
	b.WriteString("first lines:\n")
	b.WriteString(strings.Join(lines[:headCount], "\n"))
	if len(priority) > 0 {
		b.WriteString("\n\nmatched lines:\n")
		b.WriteString(strings.Join(priority, "\n"))
	}
	if tailCount > 0 {
		b.WriteString("\n\nlast lines:\n")
		b.WriteString(strings.Join(lines[len(lines)-tailCount:], "\n"))
	}
	out := b.String()
	if len(out) > limit {
		out, _ = truncateMiddle(out, limit)
	}
	return out
}

func compactProjectIndexContent(content string, limit int) string {
	lines := strings.Split(content, "\n")
	kept := make([]string, 0, 160)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.Contains(trimmed, "[skip]") || strings.HasPrefix(trimmed, "DIR:") || strings.HasPrefix(trimmed, "FILE:") {
			kept = append(kept, line)
		}
		if len(kept) >= 160 {
			break
		}
	}
	if len(kept) == 0 {
		out, _ := truncateMiddle(content, limit)
		return out
	}
	out := "[model compacted project index]\n" + strings.Join(kept, "\n")
	if len(out) > limit {
		out, _ = truncateMiddle(out, limit)
	}
	return out
}

func compactSearchContent(content string, limit int) string {
	lines := strings.Split(content, "\n")
	kept := make([]string, 0, 80)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		if strings.Contains(lower, "http://") || strings.Contains(lower, "https://") || strings.Contains(lower, "title") || strings.Contains(lower, "path") || strings.Contains(lower, "summary") || strings.Contains(lower, "score") {
			kept = append(kept, line)
		}
		if len(kept) >= 80 {
			break
		}
	}
	if len(kept) == 0 {
		out, _ := truncateMiddle(content, limit)
		return out
	}
	out := "[model compacted search results]\n" + strings.Join(kept, "\n")
	if len(out) > limit {
		out, _ = truncateMiddle(out, limit)
	}
	return out
}

func compactLogContent(content string, limit int) string {
	lines := strings.Split(content, "\n")
	if len(lines) <= 20 {
		out, _ := truncateMiddle(content, limit)
		return out
	}
	headCount := minInt(20, len(lines))
	tailCount := minInt(40, len(lines)-headCount)
	priority := priorityLines(lines, 40)
	var b strings.Builder
	b.WriteString("[model compacted terminal output]\n")
	b.WriteString("first lines:\n")
	b.WriteString(strings.Join(lines[:headCount], "\n"))
	if len(priority) > 0 {
		b.WriteString("\n\npriority lines:\n")
		b.WriteString(strings.Join(priority, "\n"))
	}
	if tailCount > 0 {
		b.WriteString("\n\nlast lines:\n")
		b.WriteString(strings.Join(lines[len(lines)-tailCount:], "\n"))
	}
	out := b.String()
	if len(out) > limit {
		out, _ = truncateMiddle(out, limit)
	}
	return out
}

func priorityLines(lines []string, limit int) []string {
	priority := make([]string, 0, limit)
	seen := map[string]bool{}
	for idx, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || !logPriorityPattern.MatchString(trimmed) {
			continue
		}
		key := trimmed
		if len(key) > 240 {
			key = key[:240]
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		priority = append(priority, lineNumbered(idx+1, line))
		if len(priority) >= limit {
			break
		}
	}
	return priority
}

func compactHTMLContent(content string, limit int) string {
	text := htmlScriptStylePattern.ReplaceAllString(content, " ")
	text = htmlTagPattern.ReplaceAllString(text, " ")
	text = html.UnescapeString(text)
	lines := strings.Split(text, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(htmlSpacePattern.ReplaceAllString(line, " "))
		if line != "" {
			kept = append(kept, line)
		}
	}
	out := repeatedBlankPattern.ReplaceAllString(strings.Join(kept, "\n"), "\n\n")
	if len(out) > limit {
		out, _ = truncateMiddle(out, limit)
	}
	return "[model compacted html text]\n" + out
}

func looksLikeHTML(content string) bool {
	sample := strings.ToLower(content)
	if len(sample) > 2048 {
		sample = sample[:2048]
	}
	return strings.Contains(sample, "<html") || strings.Contains(sample, "<body") || strings.Contains(sample, "<!doctype html")
}

func lineNumbered(n int, line string) string {
	return fmt.Sprintf("L%d: %s", n, line)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func storeRawToolResult(home, traceID, toolName string, result agentcore.ToolResult) (string, string) {
	home = strings.TrimSpace(home)
	if home == "" || strings.TrimSpace(result.Content) == "" {
		return "", ""
	}
	sum := sha256.Sum256([]byte(traceID + "\x00" + result.ToolCallID + "\x00" + toolName + "\x00" + result.Content))
	hash := hex.EncodeToString(sum[:])[:24]
	dir := filepath.Join(home, "artifacts", "tool-results", hash[:2])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", ""
	}
	path := filepath.Join(dir, hash+".txt")
	if err := os.WriteFile(path, []byte(result.Content), 0o600); err != nil {
		return "", ""
	}
	return "tool-result:" + hash, path
}
