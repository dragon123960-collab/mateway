package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/config"
)

func (ToolResultReadTool) Name() string { return "toolresult.read" }
func (ToolResultReadTool) Description() string {
	return "retrieve original tool output by raw_ref from a compacted tool result"
}
func (ToolResultReadTool) Schema() agentcore.Schema {
	return agentcore.Schema{
		Required: []string{"raw_ref"},
		Properties: map[string]any{
			"raw_ref": map[string]any{"type": "string", "description": "Reference from compacted tool result evidence, formatted as tool-result:<hash>."},
			"query":   map[string]any{"type": "string", "description": "Optional text to search within the original output."},
			"limit":   map[string]any{"type": "integer", "description": "Maximum characters to return. Defaults to 12000 and is capped at 50000."},
		},
	}
}
func (ToolResultReadTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{
		WhenToUse:            "Use when prior tool result evidence contains raw_ref and the compacted output is insufficient.",
		WhenNotToUse:         "Do not use for normal file paths or URLs; use file.read or web.fetch for those.",
		OutputContract:       "Return original tool output, or query-matching line snippets when query is provided.",
		Evidence:             "Return raw_ref, bytes, limit, partial, and query metadata when used.",
		Acceptance:           "Accepted when raw_ref exists in the local artifact store and readable content is returned.",
		SoftFailureSignals:   []string{"invalid raw_ref", "raw_ref not found"},
		ParallelMode:         "read_only_ok",
		ReusePolicy:          "stable_read",
		ConfirmationBoundary: "safe read; no confirmation.",
	}
}
func (ToolResultReadTool) Risk() agentcore.Risk { return agentcore.RiskSafeRead }
func (t ToolResultReadTool) Run(_ context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	rawRef := strings.TrimSpace(fmt.Sprint(call.Args["raw_ref"]))
	hash, err := parseToolResultRawRef(rawRef)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true, Evidence: map[string]any{"raw_ref": rawRef}}
	}
	path := toolResultArtifactPath(t.Config, hash)
	data, err := os.ReadFile(path)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: "raw_ref not found", IsError: true, Evidence: map[string]any{"raw_ref": rawRef}}
	}
	limit := boundedIntArg(call.Args["limit"], 12000, 1, 50000)
	query := stringArg(call.Args, "query")
	evidence := map[string]any{
		"raw_ref": rawRef,
		"bytes":   len(data),
		"limit":   limit,
	}
	content := string(data)
	if query != "" {
		var ranges []string
		content, evidence["matches"], ranges = searchToolResultContent(content, query, limit)
		evidence["query"] = query
		evidence["line_ranges"] = ranges
		evidence["partial"] = len([]rune(content)) >= limit
		return agentcore.ToolResult{ToolCallID: call.ID, Content: content, Evidence: evidence}
	}
	runes := []rune(content)
	if len(runes) > limit {
		content = string(runes[:limit]) + fmt.Sprintf("\n...[truncated %d chars; call toolresult.read with a query or higher limit for more]...", len(runes)-limit)
		evidence["partial"] = true
	} else {
		evidence["partial"] = false
	}
	return agentcore.ToolResult{ToolCallID: call.ID, Content: content, Evidence: evidence}
}

func parseToolResultRawRef(rawRef string) (string, error) {
	hash, ok := strings.CutPrefix(strings.TrimSpace(rawRef), "tool-result:")
	if !ok || len(hash) != 24 {
		return "", fmt.Errorf("invalid raw_ref")
	}
	for _, r := range hash {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return "", fmt.Errorf("invalid raw_ref")
		}
	}
	return hash, nil
}

func toolResultArtifactPath(cfg *config.Root, hash string) string {
	home := config.DefaultHome()
	if cfg != nil && strings.TrimSpace(cfg.App.Home) != "" {
		home = cfg.App.Home
	}
	return filepath.Join(home, "artifacts", "tool-results", hash[:2], hash+".txt")
}

func searchToolResultContent(content, query string, limit int) (string, int, []string) {
	terms := queryTerms(query)
	if len(terms) == 0 {
		return "no matches", 0, nil
	}
	lines := strings.Split(content, "\n")
	var out []string
	var ranges []string
	matches := 0
	for idx, line := range lines {
		if !lineMatchesTerms(line, terms) {
			continue
		}
		matches++
		start := idx - 2
		if start < 0 {
			start = 0
		}
		end := idx + 3
		if end > len(lines) {
			end = len(lines)
		}
		if len(out) > 0 {
			out = append(out, "--")
		}
		ranges = append(ranges, fmt.Sprintf("L%d-L%d", start+1, end))
		for i := start; i < end; i++ {
			out = append(out, fmt.Sprintf("L%d: %s", i+1, lines[i]))
		}
		if len([]rune(strings.Join(out, "\n"))) >= limit {
			break
		}
	}
	if matches == 0 {
		return "no matches", 0, nil
	}
	result := strings.Join(out, "\n")
	runes := []rune(result)
	if len(runes) > limit {
		result = string(runes[:limit])
	}
	return result, matches, ranges
}

func queryTerms(query string) []string {
	fields := strings.Fields(strings.ToLower(query))
	var terms []string
	seen := map[string]bool{}
	for _, field := range fields {
		field = strings.Trim(field, `"'.,:;()[]{}<>`)
		if field == "" || seen[field] {
			continue
		}
		seen[field] = true
		terms = append(terms, field)
	}
	return terms
}

func lineMatchesTerms(line string, terms []string) bool {
	lower := strings.ToLower(line)
	for _, term := range terms {
		if !strings.Contains(lower, term) {
			return false
		}
	}
	return true
}
