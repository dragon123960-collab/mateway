package reflection

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Record struct {
	CreatedAt string         `json:"created_at"`
	SkillName string         `json:"skill_name"`
	Type      string         `json:"type"`
	Status    string         `json:"status"`
	Failure   string         `json:"failure,omitempty"`
	Duration  string         `json:"duration"`
	ExitCode  int            `json:"exit_code,omitempty"`
	RiskLevel string         `json:"risk_level,omitempty"`
	Stdout    string         `json:"stdout,omitempty"`
	Stderr    string         `json:"stderr,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type Index struct {
	TotalByType   map[string]int `json:"total_by_type,omitempty"`
	FailureByKind map[string]int `json:"failure_by_kind,omitempty"`
	StatusByType  map[string]int `json:"status_by_type,omitempty"`
	LastUpdatedAt string         `json:"last_updated_at,omitempty"`
}

func Append(workspace string, record Record) error {
	day := time.Now().Format("2006-01-02")
	dir := filepath.Join(workspace, "memory", "reflections")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create reflection dir: %w", err)
	}
	path := filepath.Join(dir, day+".jsonl")
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode reflection: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open reflection file: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("append reflection: %w", err)
	}
	return rebuildIndexes(workspace)
}

func rebuildIndexes(workspace string) error {
	dir := filepath.Join(workspace, "memory", "reflections")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read reflection dir: %w", err)
	}
	index := Index{
		TotalByType:   map[string]int{},
		FailureByKind: map[string]int{},
		StatusByType:  map[string]int{},
		LastUpdatedAt: time.Now().Format(time.RFC3339),
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		if err := foldReflectionFile(filepath.Join(dir, entry.Name()), &index); err != nil {
			return err
		}
	}
	if err := writeJSON(filepath.Join(dir, "index.json"), index); err != nil {
		return err
	}
	failures := failurePairs(index.FailureByKind)
	return writeJSON(filepath.Join(dir, "failures.json"), failures)
}

func foldReflectionFile(path string, index *Index) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record Record
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		kind := firstNonEmpty(strings.TrimSpace(record.Type), "unknown")
		index.TotalByType[kind]++
		index.StatusByType[kind+":"+firstNonEmpty(strings.TrimSpace(record.Status), "unknown")]++
		if failure := strings.TrimSpace(record.Failure); failure != "" {
			index.FailureByKind[failure]++
		}
	}
	return scanner.Err()
}

type FailurePair struct {
	Failure string `json:"failure"`
	Count   int    `json:"count"`
}

func failurePairs(values map[string]int) []FailurePair {
	out := make([]FailurePair, 0, len(values))
	for failure, count := range values {
		out = append(out, FailurePair{Failure: failure, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count == out[j].Count {
			return out[i].Failure < out[j].Failure
		}
		return out[i].Count > out[j].Count
	})
	return out
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
