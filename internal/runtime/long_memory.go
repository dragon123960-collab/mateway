package runtime

import (
	"fmt"
	"strings"

	"github.com/dongping/mateway/internal/memory"
)

const longMemoryTextLimit = 2200

type longMemorySummary struct {
	Text  string
	Items []memory.SearchResult
}

func buildLongMemorySummary(results []memory.SearchResult) longMemorySummary {
	if len(results) == 0 {
		return longMemorySummary{}
	}
	var sections []string
	used := 0
	for _, result := range results {
		item := renderLongMemoryItem(result)
		if item == "" {
			continue
		}
		if used+len(item) > longMemoryTextLimit {
			break
		}
		sections = append(sections, item)
		used += len(item)
	}
	return longMemorySummary{Text: strings.TrimSpace(strings.Join(sections, "\n\n")), Items: results}
}

func renderLongMemoryItem(result memory.SearchResult) string {
	title := strings.TrimSpace(result.Title)
	if title == "" {
		title = result.ID
	}
	snippet := strings.TrimSpace(result.Snippet)
	if snippet == "" {
		return ""
	}
	return fmt.Sprintf("- %s\n  path: %s\n  lines: %d-%d\n  score: %d\n  snippet: %s", title, result.Path, result.StartLine, result.EndLine, result.Score, indentMemorySnippet(snippet))
}

func indentMemorySnippet(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}
	return strings.Join(lines, "\n    ")
}

func longMemoryTraceFields(results []memory.SearchResult) []map[string]any {
	out := make([]map[string]any, 0, len(results))
	for _, result := range results {
		out = append(out, map[string]any{
			"id":         result.ID,
			"path":       result.Path,
			"title":      result.Title,
			"score":      result.Score,
			"start_line": result.StartLine,
			"end_line":   result.EndLine,
		})
	}
	return out
}
