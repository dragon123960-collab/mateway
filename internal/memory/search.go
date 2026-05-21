package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const defaultMemorySnippetLimit = 600

type SearchOptions struct {
	AgentID      string
	Query        string
	Limit        int
	SnippetLimit int
}

type SearchResult struct {
	ID        string
	Path      string
	Title     string
	Score     int
	Snippet   string
	StartLine int
	EndLine   int
}

func (s Store) SearchLong(opts SearchOptions) ([]SearchResult, error) {
	agentID := firstNonEmptyMemory(opts.AgentID, "main")
	query := strings.TrimSpace(opts.Query)
	if strings.TrimSpace(s.Root) == "" || query == "" {
		return nil, nil
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 4
	}
	snippetLimit := opts.SnippetLimit
	if snippetLimit <= 0 {
		snippetLimit = defaultMemorySnippetLimit
	}
	candidates, err := s.longSearchCandidates(agentID)
	if err != nil {
		return nil, err
	}
	tokens := queryTokens(query)
	var results []SearchResult
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		text := string(data)
		if !isActiveMemory(text) {
			continue
		}
		score := scoreMemoryText(tokens, text, path)
		if score <= 0 {
			continue
		}
		snippet, startLine, endLine := memorySnippetForTokens(text, tokens, snippetLimit)
		results = append(results, SearchResult{
			ID:        strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
			Path:      path,
			Title:     firstNonEmptyMemory(titleFromMarkdown(text), strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))),
			Score:     score,
			Snippet:   snippet,
			StartLine: startLine,
			EndLine:   endLine,
		})
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].Path < results[j].Path
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func (s Store) longSearchCandidates(agentID string) ([]string, error) {
	if candidates, ok := s.longSearchCandidatesFromIndex(agentID); ok {
		return candidates, nil
	}
	dir := filepath.Join(s.Root, "agents", agentID, "long")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var candidates []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		candidates = append(candidates, filepath.Join(dir, entry.Name()))
	}
	sort.Strings(candidates)
	return candidates, nil
}

func (s Store) longSearchCandidatesFromIndex(agentID string) ([]string, bool) {
	path := filepath.Join(s.Root, "index.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var index Index
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, false
	}
	var candidates []string
	for _, entry := range index.Entries {
		if entry.Area != "long" || entry.AgentID != agentID {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(entry.Status))
		if status != "" && status != "active" {
			continue
		}
		if strings.TrimSpace(entry.Path) == "" {
			continue
		}
		candidates = append(candidates, filepath.Join(s.Root, filepath.FromSlash(entry.Path)))
	}
	sort.Strings(candidates)
	return candidates, true
}

func isActiveMemory(text string) bool {
	parsed, err := parseMarkdown(text)
	if err != nil {
		return false
	}
	status := strings.ToLower(strings.TrimSpace(parsed.Frontmatter.Status))
	return status == "" || status == "active"
}

func queryTokens(text string) []string {
	text = strings.ToLower(strings.TrimSpace(text))
	replacer := strings.NewReplacer("\uFF0C", " ", "\u3002", " ", "\uFF1F", " ", "\uFF01", " ", "\uFF1A", " ", "\uFF1B", " ", ",", " ", ".", " ", ":", " ", ";", " ", "\n", " ", "\t", " ")
	text = replacer.Replace(text)
	parts := strings.Fields(text)
	seen := map[string]struct{}{}
	var out []string
	for _, part := range parts {
		part = strings.Trim(part, `"'()[]{}<>`)
		if len([]rune(part)) < 2 {
			continue
		}
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		out = append(out, part)
	}
	return out
}

func scoreMemoryText(tokens []string, text, path string) int {
	if len(tokens) == 0 {
		return 0
	}
	hay := strings.ToLower(text + "\n" + filepath.Base(path))
	score := 0
	for _, token := range tokens {
		count := strings.Count(hay, token)
		switch {
		case count >= 3:
			score += 4
		case count > 0:
			score += 2
		}
		if strings.Contains(strings.ToLower(titleFromMarkdown(text)), token) {
			score += 3
		}
	}
	return score
}

func compactMemorySnippet(text string, limit int) string {
	text = stripFrontmatter(text)
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			out = append(out, line)
			continue
		}
		out = append(out, line)
	}
	joined := strings.Join(out, "\n")
	if limit > 0 && len(joined) > limit {
		return strings.TrimSpace(joined[:limit-3]) + "..."
	}
	return strings.TrimSpace(joined)
}

func memorySnippetForTokens(text string, tokens []string, limit int) (string, int, int) {
	body := stripFrontmatter(text)
	lines := strings.Split(body, "\n")
	best := firstMatchingLine(lines, tokens)
	if best < 0 {
		snippet := compactMemorySnippet(text, limit)
		end := countSnippetLines(snippet)
		if end == 0 {
			end = 1
		}
		return snippet, 1, end
	}
	start := best - 1
	if start < 0 {
		start = 0
	}
	end := best + 2
	if end > len(lines) {
		end = len(lines)
	}
	var selected []string
	for i := start; i < end; i++ {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			selected = append(selected, line)
		}
	}
	snippet := strings.TrimSpace(strings.Join(selected, "\n"))
	if limit > 0 && len(snippet) > limit {
		snippet = strings.TrimSpace(snippet[:limit-3]) + "..."
	}
	return snippet, start + 1, end
}

func firstMatchingLine(lines []string, tokens []string) int {
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		lower := strings.ToLower(line)
		for _, token := range tokens {
			if strings.Contains(lower, token) {
				return i
			}
		}
	}
	for i, line := range lines {
		lower := strings.ToLower(line)
		for _, token := range tokens {
			if strings.Contains(lower, token) {
				return i
			}
		}
	}
	return -1
}

func countSnippetLines(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	return len(strings.Split(text, "\n"))
}

func stripFrontmatter(text string) string {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "---") {
		return text
	}
	parts := strings.SplitN(text, "---", 3)
	if len(parts) < 3 {
		return text
	}
	return strings.TrimSpace(parts[2])
}
