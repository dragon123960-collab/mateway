package memory

import (
	"fmt"
	"sort"
	"strings"
)

type SearchOptions struct {
	Query string
	Limit int
	Scope string
	Type  string
}

type SearchResult struct {
	Path    string   `json:"path"`
	Type    string   `json:"type,omitempty"`
	Scope   string   `json:"scope,omitempty"`
	Status  string   `json:"status,omitempty"`
	Sources []string `json:"sources,omitempty"`
	Snippet string   `json:"snippet,omitempty"`
	Score   int      `json:"score"`
}

func SearchRoot(root string, options SearchOptions) ([]SearchResult, []Issue, error) {
	queryTerms := queryTerms(options.Query)
	if len(queryTerms) == 0 {
		return nil, nil, fmt.Errorf("memory search query is required")
	}
	limit := options.Limit
	if limit <= 0 {
		limit = 5
	}
	index, issues, err := RebuildIndex(root)
	if err != nil {
		return nil, issues, err
	}
	var results []SearchResult
	for _, entry := range index.Entries {
		if options.Scope != "" && entry.Scope != options.Scope {
			continue
		}
		if options.Type != "" && entry.Type != options.Type {
			continue
		}
		haystack := strings.ToLower(strings.Join([]string{
			entry.Path,
			entry.Type,
			entry.Scope,
			entry.OwnerAgent,
			entry.ProjectID,
			entry.Status,
			strings.Join(entry.Tags, " "),
			strings.Join(entry.Aliases, " "),
			entry.OpFingerprint,
			entry.Snippet,
		}, " "))
		score := scoreTerms(haystack, queryTerms)
		if score == 0 {
			continue
		}
		results = append(results, SearchResult{
			Path:    entry.Path,
			Type:    entry.Type,
			Scope:   entry.Scope,
			Status:  entry.Status,
			Sources: entry.Sources,
			Snippet: entry.Snippet,
			Score:   score,
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
	return results, issues, nil
}

func queryTerms(query string) []string {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
	terms := make([]string, 0, len(fields))
	seen := map[string]bool{}
	for _, field := range fields {
		field = strings.Trim(field, ".,;:!?()[]{}\"'")
		if field == "" || seen[field] {
			continue
		}
		seen[field] = true
		terms = append(terms, field)
	}
	return terms
}

func scoreTerms(haystack string, terms []string) int {
	score := 0
	for _, term := range terms {
		if strings.Contains(haystack, term) {
			score++
		}
	}
	return score
}
