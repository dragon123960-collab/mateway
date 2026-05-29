package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
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
