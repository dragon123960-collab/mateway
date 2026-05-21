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

type IndexEntry struct {
	ID            string           `json:"id"`
	Path          string           `json:"path"`
	Area          string           `json:"area"`
	AgentID       string           `json:"agent_id,omitempty"`
	Title         string           `json:"title"`
	Type          string           `json:"type"`
	Scope         string           `json:"scope"`
	Status        string           `json:"status"`
	Tags          []string         `json:"tags,omitempty"`
	Sources       []string         `json:"sources,omitempty"`
	ParsedSources []SourceEvidence `json:"parsed_sources,omitempty"`
	Confidence    string           `json:"confidence,omitempty"`
	UpdatedAt     string           `json:"updated_at,omitempty"`
}

type Index struct {
	Root       string       `json:"root"`
	BuiltAt    time.Time    `json:"built_at"`
	Entries    []IndexEntry `json:"entries"`
	IssueCount int          `json:"issue_count"`
}

type RebuildIndexResult struct {
	Index Index
	Path  string
}

func (s Store) ReadIndex() (RebuildIndexResult, error) {
	path := filepath.Join(s.Root, "index.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return RebuildIndexResult{}, err
	}
	var index Index
	if err := json.Unmarshal(data, &index); err != nil {
		return RebuildIndexResult{}, err
	}
	return RebuildIndexResult{Index: index, Path: path}, nil
}

func (s Store) RebuildIndex(now time.Time) (RebuildIndexResult, error) {
	if strings.TrimSpace(s.Root) == "" {
		return RebuildIndexResult{}, fmt.Errorf("memory root is required")
	}
	if now.IsZero() {
		now = time.Now()
	}
	var entries []IndexEntry
	issueCount := 0
	err := filepath.WalkDir(s.Root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		area, agentID, ok := classifyMemoryIndexPath(s.Root, path)
		if !ok {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			issueCount++
			return nil
		}
		parsed, err := parseMarkdown(string(data))
		if err != nil {
			issueCount++
		}
		rel, _ := filepath.Rel(s.Root, path)
		entry := IndexEntry{
			ID:            strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
			Path:          filepath.ToSlash(rel),
			Area:          area,
			AgentID:       agentID,
			Title:         firstNonEmptyMemory(titleFromMarkdown(string(data)), strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))),
			Type:          parsed.Frontmatter.Type,
			Scope:         parsed.Frontmatter.Scope,
			Status:        parsed.Frontmatter.Status,
			Tags:          cleanList(parsed.Frontmatter.Tags),
			Sources:       cleanList(parsed.Frontmatter.Sources),
			ParsedSources: ParseSources(parsed.Frontmatter.Sources),
			Confidence:    parsed.Frontmatter.Confidence,
			UpdatedAt:     parsed.Frontmatter.UpdatedAt,
		}
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		return RebuildIndexResult{}, err
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Path < entries[j].Path
	})
	index := Index{Root: s.Root, BuiltAt: now, Entries: entries, IssueCount: issueCount}
	path := filepath.Join(s.Root, "index.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return RebuildIndexResult{}, err
	}
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return RebuildIndexResult{}, err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return RebuildIndexResult{}, err
	}
	return RebuildIndexResult{Index: index, Path: path}, nil
}

func (s Store) rebuildIndexBestEffort(now time.Time) {
	_, _ = s.RebuildIndex(now)
}

func classifyMemoryIndexPath(root, path string) (string, string, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", "", false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) >= 4 && parts[0] == "agents" {
		switch parts[2] {
		case "long", "inbox":
			return parts[2], parts[1], true
		default:
			return "", "", false
		}
	}
	if len(parts) >= 2 {
		switch parts[0] {
		case "user", "org":
			return parts[0], "", true
		}
	}
	return "", "", false
}
