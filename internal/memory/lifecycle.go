package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type LifecycleHeartbeatInput struct {
	Home       string
	MemoryRoot string
	Now        func() time.Time
}

type LifecycleHeartbeatResult struct {
	Root      string
	Scanned   int
	Issues    int
	Expired   int
	ReviewDue int
	Conflicts int
	Items     []LifecycleIssue
}

type LifecycleIssue struct {
	Kind      string   `json:"kind"`
	Path      string   `json:"path,omitempty"`
	Paths     []string `json:"paths,omitempty"`
	TopicPath string   `json:"topic_path,omitempty"`
	Subject   string   `json:"subject,omitempty"`
	Predicate string   `json:"predicate,omitempty"`
	Due       string   `json:"due,omitempty"`
}

func RunLifecycleHeartbeat(input LifecycleHeartbeatInput) (LifecycleHeartbeatResult, error) {
	home := defaultString(input.Home, ".mateway")
	root := strings.TrimSpace(input.MemoryRoot)
	if root == "" {
		root = filepath.Join(home, "workspace", "memory")
	}
	now := time.Now()
	if input.Now != nil {
		now = input.Now()
	}
	result := LifecycleHeartbeatResult{Root: root}
	activeFacts := map[string][]IndexEntry{}
	index, issues, err := RebuildIndex(root)
	if err != nil {
		return result, err
	}
	if hasError(issues) {
		return result, nil
	}
	for _, entry := range index.Entries {
		result.Scanned++
		if entry.Status != "active" {
			continue
		}
		if t, ok := parseMemoryDate(entry.ValidUntil); ok && !now.Before(t) {
			result.Expired++
			result.Items = append(result.Items, LifecycleIssue{Kind: "expired", Path: entry.Path, TopicPath: entry.TopicPath, Subject: entry.Subject, Predicate: entry.Predicate, Due: entry.ValidUntil})
		}
		if t, ok := parseMemoryDate(entry.ReviewAfter); ok && !now.Before(t) {
			result.ReviewDue++
			result.Items = append(result.Items, LifecycleIssue{Kind: "review_due", Path: entry.Path, TopicPath: entry.TopicPath, Subject: entry.Subject, Predicate: entry.Predicate, Due: entry.ReviewAfter})
		}
		if entry.TopicPath != "" && entry.Subject != "" && entry.Predicate != "" {
			key := entry.TopicPath + "\x00" + entry.Subject + "\x00" + entry.Predicate
			activeFacts[key] = append(activeFacts[key], entry)
		}
	}
	for _, entries := range activeFacts {
		if len(entries) < 2 {
			continue
		}
		var paths []string
		for _, entry := range entries {
			paths = append(paths, entry.Path)
		}
		result.Conflicts++
		result.Items = append(result.Items, LifecycleIssue{Kind: "conflict", Paths: paths, TopicPath: entries[0].TopicPath, Subject: entries[0].Subject, Predicate: entries[0].Predicate})
	}
	result.Issues = len(result.Items)
	_ = writeLifecycleAudit(home, result)
	return result, nil
}

func writeLifecycleAudit(home string, result LifecycleHeartbeatResult) error {
	path := filepath.Join(home, "observe", "audit", "memory.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{
		"type":       "memory_lifecycle",
		"root":       result.Root,
		"scanned":    result.Scanned,
		"issues":     result.Issues,
		"expired":    result.Expired,
		"review_due": result.ReviewDue,
		"conflicts":  result.Conflicts,
		"time":       time.Now().Format(time.RFC3339Nano),
	})
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(payload, '\n'))
	return err
}
