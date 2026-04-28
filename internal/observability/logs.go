package observability

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type LogEvent struct {
	CreatedAt  string         `json:"created_at"`
	Level      string         `json:"level"`
	Component  string         `json:"component"`
	Type       string         `json:"type"`
	RunID      string         `json:"run_id,omitempty"`
	SessionKey string         `json:"session_key,omitempty"`
	ThreadID   string         `json:"thread_id,omitempty"`
	Channel    string         `json:"channel,omitempty"`
	AgentName  string         `json:"agent_name,omitempty"`
	Status     string         `json:"status,omitempty"`
	Message    string         `json:"message,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

type LogFilter struct {
	RunID      string
	SessionKey string
	Channel    string
	Type       string
	Limit      int
}

func Append(workspace string, event LogEvent) error {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return nil
	}
	if strings.TrimSpace(event.CreatedAt) == "" {
		event.CreatedAt = time.Now().Format(time.RFC3339)
	}
	if strings.TrimSpace(event.Level) == "" {
		event.Level = "info"
	}
	dir := filepath.Join(workspace, "memory", "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create structured log dir: %w", err)
	}
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode structured log: %w", err)
	}
	path := filepath.Join(dir, time.Now().Format("2006-01-02")+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open structured log: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("append structured log: %w", err)
	}
	return nil
}

func Query(_ context.Context, workspace string, filter LogFilter) ([]LogEvent, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	dir := filepath.Join(workspace, "memory", "logs")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read structured log dir: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() > entries[j].Name() })
	events := make([]LogEvent, 0, limit)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		dayEvents, err := readLogFile(filepath.Join(dir, entry.Name()), filter)
		if err != nil {
			return nil, err
		}
		for i := len(dayEvents) - 1; i >= 0; i-- {
			events = append(events, dayEvents[i])
			if len(events) >= limit {
				return events, nil
			}
		}
	}
	return events, nil
}

func StructuredLogPath(workspace string) string {
	return filepath.Join(workspace, "memory", "logs")
}

func Diagnostics(ctx context.Context, workspace string) (map[string]any, error) {
	events, err := Query(ctx, workspace, LogFilter{Limit: 500})
	if err != nil {
		return nil, err
	}
	byType := map[string]int{}
	byStatus := map[string]int{}
	for _, event := range events {
		byType[firstNonEmpty(event.Type, "unknown")]++
		byStatus[firstNonEmpty(event.Status, "unknown")]++
	}
	return map[string]any{
		"recent_events": len(events),
		"by_type":       byType,
		"by_status":     byStatus,
		"path":          StructuredLogPath(workspace),
	}, nil
}

func readLogFile(path string, filter LogFilter) ([]LogEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	events := make([]LogEvent, 0, 64)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event LogEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if !matchesFilter(event, filter) {
			continue
		}
		events = append(events, event)
	}
	return events, scanner.Err()
}

func matchesFilter(event LogEvent, filter LogFilter) bool {
	if filter.RunID != "" && strings.TrimSpace(event.RunID) != strings.TrimSpace(filter.RunID) {
		return false
	}
	if filter.SessionKey != "" && strings.TrimSpace(event.SessionKey) != strings.TrimSpace(filter.SessionKey) {
		return false
	}
	if filter.Channel != "" && !strings.EqualFold(strings.TrimSpace(event.Channel), strings.TrimSpace(filter.Channel)) {
		return false
	}
	if filter.Type != "" && !strings.EqualFold(strings.TrimSpace(event.Type), strings.TrimSpace(filter.Type)) {
		return false
	}
	return true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
