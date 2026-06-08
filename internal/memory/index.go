package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Index struct {
	SchemaVersion int          `json:"schema_version"`
	GeneratedAt   string       `json:"generated_at"`
	Root          string       `json:"root"`
	Entries       []IndexEntry `json:"entries"`
}

type IndexEntry struct {
	Path          string   `json:"path"`
	Type          string   `json:"type,omitempty"`
	Scope         string   `json:"scope,omitempty"`
	OwnerAgent    string   `json:"owner_agent,omitempty"`
	ProjectID     string   `json:"project_id,omitempty"`
	Visibility    string   `json:"visibility,omitempty"`
	Status        string   `json:"status,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	Aliases       []string `json:"aliases,omitempty"`
	OpFingerprint string   `json:"op_fingerprint,omitempty"`
	Sources       []string `json:"sources,omitempty"`
	Confidence    string   `json:"confidence,omitempty"`
	CreatedAt     string   `json:"created_at,omitempty"`
	UpdatedAt     string   `json:"updated_at,omitempty"`
	ReviewAfter   string   `json:"review_after,omitempty"`
	Snippet       string   `json:"snippet,omitempty"`
}

func RebuildIndex(root string) (Index, []Issue, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return Index{}, nil, fmt.Errorf("memory root is required")
	}
	index := Index{
		SchemaVersion: 1,
		GeneratedAt:   time.Now().Format(time.RFC3339),
		Root:          root,
	}
	var issues []Issue
	err := WalkDocuments(root, func(doc Document, docIssues []Issue) error {
		issues = append(issues, docIssues...)
		if len(docIssues) > 0 || doc.FrontMatter == nil {
			return nil
		}
		lintIssues := lintDocument(doc)
		issues = append(issues, lintIssues...)
		if hasError(lintIssues) {
			return nil
		}
		index.Entries = append(index.Entries, entryFromDocument(doc))
		return nil
	})
	sort.SliceStable(index.Entries, func(i, j int) bool {
		return index.Entries[i].Path < index.Entries[j].Path
	})
	sortIssues(issues)
	return index, issues, err
}

func WriteIndex(path string, index Index) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("index path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func entryFromDocument(doc Document) IndexEntry {
	return IndexEntry{
		Path:          doc.RelPath,
		Type:          stringValue(doc.FrontMatter["type"]),
		Scope:         stringValue(doc.FrontMatter["scope"]),
		OwnerAgent:    stringValue(doc.FrontMatter["owner_agent"]),
		ProjectID:     stringValue(doc.FrontMatter["project_id"]),
		Visibility:    stringValue(doc.FrontMatter["visibility"]),
		Status:        stringValue(doc.FrontMatter["status"]),
		Tags:          stringSlice(doc.FrontMatter["tags"]),
		Aliases:       stringSlice(doc.FrontMatter["aliases"]),
		OpFingerprint: stringValue(doc.FrontMatter["op_fingerprint"]),
		Sources:       stringSlice(doc.FrontMatter["sources"]),
		Confidence:    stringValue(doc.FrontMatter["confidence"]),
		CreatedAt:     stringValue(doc.FrontMatter["created_at"]),
		UpdatedAt:     stringValue(doc.FrontMatter["updated_at"]),
		ReviewAfter:   stringValue(doc.FrontMatter["review_after"]),
		Snippet:       snippet(doc.Body, 240),
	}
}

func stringValue(value any) string {
	return strings.TrimSpace(fmt.Sprint(value))
}

func stringSlice(value any) []string {
	var out []string
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if text := strings.TrimSpace(fmt.Sprint(item)); text != "" {
				out = append(out, text)
			}
		}
	case []string:
		for _, item := range typed {
			if text := strings.TrimSpace(item); text != "" {
				out = append(out, text)
			}
		}
	case nil:
	default:
		if text := strings.TrimSpace(fmt.Sprint(value)); text != "" && text != "[]" {
			out = append(out, text)
		}
	}
	return out
}

func snippet(text string, limit int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	runes := []rune(text)
	if limit <= 0 || len(runes) <= limit {
		return text
	}
	return strings.TrimSpace(string(runes[:limit]))
}

func hasError(issues []Issue) bool {
	for _, issue := range issues {
		if issue.Severity == "error" {
			return true
		}
	}
	return false
}
