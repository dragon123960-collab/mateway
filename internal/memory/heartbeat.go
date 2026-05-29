package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type HeartbeatInput struct {
	Home       string
	MemoryRoot string
	IndexPath  string
}

type HeartbeatResult struct {
	Root      string
	IndexPath string
	Files     int
	Entries   int
	Issues    []Issue
}

type HeartbeatServeInput struct {
	Home       string
	MemoryRoot string
	IndexPath  string
	Interval   time.Duration
	Jobs       []string
	Now        func() time.Time
	Sleep      func(context.Context, time.Duration) error
	OnResult   func(HeartbeatResult)
}

func RunLintIndexHeartbeat(input HeartbeatInput) (HeartbeatResult, error) {
	home := input.Home
	if home == "" {
		home = ".mateway"
	}
	root := input.MemoryRoot
	if root == "" {
		root = filepath.Join(home, "workspace", "memory")
	}
	indexPath := input.IndexPath
	if indexPath == "" {
		indexPath = filepath.Join(home, "indexes", "memory_index.json")
	}
	lint, err := LintRoot(root)
	if err != nil {
		return HeartbeatResult{}, err
	}
	result := HeartbeatResult{Root: root, IndexPath: indexPath, Files: lint.Files, Issues: lint.Issues}
	if lint.HasErrors() {
		_ = writeHeartbeatAudit(home, result)
		return result, nil
	}
	index, issues, err := RebuildIndex(root)
	if err != nil {
		return result, err
	}
	result.Issues = append(result.Issues, issues...)
	result.Entries = len(index.Entries)
	if !hasError(result.Issues) {
		if err := WriteIndex(indexPath, index); err != nil {
			return result, err
		}
	}
	if err := writeHeartbeatAudit(home, result); err != nil {
		return result, err
	}
	return result, nil
}

func ServeHeartbeat(ctx context.Context, input HeartbeatServeInput) error {
	interval := input.Interval
	if interval <= 0 {
		interval = 30 * time.Minute
	}
	jobs := NormalizeHeartbeatJobs(input.Jobs)
	for {
		for _, job := range jobs {
			switch job {
			case "lint-index":
				result, err := RunLintIndexHeartbeat(HeartbeatInput{Home: input.Home, MemoryRoot: input.MemoryRoot, IndexPath: input.IndexPath})
				if input.OnResult != nil {
					input.OnResult(result)
				}
				if err != nil {
					return err
				}
			default:
				return fmt.Errorf("unsupported heartbeat job %q", job)
			}
		}
		if input.Sleep != nil {
			if err := input.Sleep(ctx, interval); err != nil {
				return err
			}
			continue
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func NormalizeHeartbeatJobs(jobs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, job := range jobs {
		switch strings.TrimSpace(job) {
		case "lint-index", "memory_lint", "memory_index_rebuild", "memory_lint_index":
			if !seen["lint-index"] {
				out = append(out, "lint-index")
				seen["lint-index"] = true
			}
		}
	}
	if len(out) == 0 {
		out = []string{"lint-index"}
	}
	return out
}

func writeHeartbeatAudit(home string, result HeartbeatResult) error {
	path := filepath.Join(home, "observe", "audit", "memory.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{
		"type":    "memory_heartbeat",
		"files":   result.Files,
		"entries": result.Entries,
		"issues":  len(result.Issues),
		"time":    time.Now().Format(time.RFC3339Nano),
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
