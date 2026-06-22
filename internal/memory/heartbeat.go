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
	Distill   DistillHeartbeatResult
	Learning  DistillHeartbeatResult
	Skill     SkillLearningHeartbeatResult
	Lifecycle LifecycleHeartbeatResult
}

type HeartbeatServeInput struct {
	Home       string
	Workspace  string
	MemoryRoot string
	IndexPath  string
	Interval   time.Duration
	Jobs       []string
	Model      DistillModel
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
			case "memory_distill":
				distill, err := RunDistillHeartbeat(ctx, DistillHeartbeatInput{Home: input.Home, MemoryRoot: input.MemoryRoot, Model: input.Model, Now: input.Now})
				if input.OnResult != nil {
					input.OnResult(HeartbeatResult{Root: input.MemoryRoot, IndexPath: input.IndexPath, Distill: distill})
				}
				if err != nil {
					return err
				}
			case "learning_distill":
				learning, err := RunLearningDistillHeartbeat(ctx, LearningHeartbeatInput{Home: input.Home, MemoryRoot: input.MemoryRoot, Model: input.Model, Now: input.Now})
				if input.OnResult != nil {
					input.OnResult(HeartbeatResult{Root: input.MemoryRoot, IndexPath: input.IndexPath, Learning: learning})
				}
				if err != nil {
					return err
				}
			case "skill_learning":
				workspace := strings.TrimSpace(input.Workspace)
				if workspace == "" {
					workspace = filepath.Join(input.Home, "workspace")
				}
				skill, err := RunSkillLearningHeartbeat(ctx, SkillLearningHeartbeatInput{Home: input.Home, Workspace: workspace, Model: input.Model, Now: input.Now})
				if input.OnResult != nil {
					input.OnResult(HeartbeatResult{Root: input.MemoryRoot, IndexPath: input.IndexPath, Skill: skill})
				}
				if err != nil {
					return err
				}
			case "lifecycle":
				lifecycle, err := RunLifecycleHeartbeat(LifecycleHeartbeatInput{Home: input.Home, MemoryRoot: input.MemoryRoot, Now: input.Now})
				if input.OnResult != nil {
					input.OnResult(HeartbeatResult{Root: input.MemoryRoot, IndexPath: input.IndexPath, Lifecycle: lifecycle})
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
		case "memory_distill", "distill":
			if !seen["memory_distill"] {
				out = append(out, "memory_distill")
				seen["memory_distill"] = true
			}
		case "learning_distill", "learning":
			if !seen["learning_distill"] {
				out = append(out, "learning_distill")
				seen["learning_distill"] = true
			}
		case "skill_learning", "skill":
			if !seen["skill_learning"] {
				out = append(out, "skill_learning")
				seen["skill_learning"] = true
			}
		case "lifecycle", "memory_lifecycle":
			if !seen["lifecycle"] {
				out = append(out, "lifecycle")
				seen["lifecycle"] = true
			}
		}
	}
	if len(out) == 0 {
		out = []string{"lint-index", "memory_distill", "learning_distill", "skill_learning"}
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
