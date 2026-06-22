package memory

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type SearchOptions struct {
	Query          string
	Limit          int
	Scope          string
	Type           string
	TopicPath      string
	Subject        string
	Predicate      string
	IncludeHistory bool
	Now            func() time.Time
}

type SearchResult struct {
	Path      string   `json:"path"`
	Type      string   `json:"type,omitempty"`
	Scope     string   `json:"scope,omitempty"`
	Status    string   `json:"status,omitempty"`
	TopicPath string   `json:"topic_path,omitempty"`
	Subject   string   `json:"subject,omitempty"`
	Predicate string   `json:"predicate,omitempty"`
	Sources   []string `json:"sources,omitempty"`
	Snippet   string   `json:"snippet,omitempty"`
	Score     int      `json:"score"`
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
	now := time.Now()
	if options.Now != nil {
		now = options.Now()
	}
	index, issues, err := RebuildIndex(root)
	if err != nil {
		return nil, issues, err
	}
	var results []SearchResult
	for _, entry := range index.Entries {
		if !options.IncludeHistory && !entryActiveAt(entry, now) {
			continue
		}
		if options.Scope != "" && entry.Scope != options.Scope {
			continue
		}
		if options.Type != "" && entry.Type != options.Type {
			continue
		}
		if options.TopicPath != "" && entry.TopicPath != options.TopicPath {
			continue
		}
		if options.Subject != "" && entry.Subject != options.Subject {
			continue
		}
		if options.Predicate != "" && entry.Predicate != options.Predicate {
			continue
		}
		haystack := strings.ToLower(strings.Join([]string{
			entry.Path,
			entry.Type,
			entry.Scope,
			entry.OwnerAgent,
			entry.ProjectID,
			entry.Status,
			entry.TopicPath,
			entry.Subject,
			entry.Predicate,
			entry.Object,
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
			Path:      entry.Path,
			Type:      entry.Type,
			Scope:     entry.Scope,
			Status:    entry.Status,
			TopicPath: entry.TopicPath,
			Subject:   entry.Subject,
			Predicate: entry.Predicate,
			Sources:   entry.Sources,
			Snippet:   entry.Snippet,
			Score:     score,
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

func entryActiveAt(entry IndexEntry, now time.Time) bool {
	status := strings.TrimSpace(entry.Status)
	if status == "" {
		status = "active"
	}
	if status != "active" {
		return false
	}
	if t, ok := parseMemoryDate(entry.ValidUntil); ok && !now.Before(t) {
		return false
	}
	return true
}

func parseMemoryDate(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
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
