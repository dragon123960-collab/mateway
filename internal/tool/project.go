package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func ProjectIndex() Definition {
	return Definition{
		Name:        "project.index",
		Description: "Index a project or directory and return a concise structural summary.",
		Metadata: Metadata{
			Purpose:        "inspect repository or directory structure",
			WhenToUse:      []string{"project overview", "repository map", "file tree", "package distribution"},
			WhenNotToUse:   []string{"reading one file", "editing files"},
			RequiredArgs:   []string{},
			OutputContract: []string{"directory path", "file count", "extension summary"},
			AcceptanceSpecRef: "project.index/default",
			AcceptanceMode: AcceptanceCodeOnly,
			ParallelMode:   ParallelReadOnlyOK,
			ResourceScope:  "project:index",
		},
		Risk:        RiskSafeRead,
		ArgsSchema: map[string]string{
			"path":      "optional project or directory path, defaults to project root",
			"max_depth": "optional max walk depth, defaults to 3",
			"max_files": "optional max listed files, defaults to 80",
		},
		Run: func(ctx context.Context, call Call) Result {
			root, err := resolveProjectTarget(call)
			if err != nil {
				return ErrorResult(err.Error())
			}
			maxDepth := intArg(call.Args["max_depth"], 3, 1, 6)
			maxFiles := intArg(call.Args["max_files"], 80, 10, 200)
			output, evidence, err := buildProjectIndex(root, maxDepth, maxFiles)
			if err != nil {
				return ErrorResult(err.Error())
			}
			return Result{OK: true, Output: Truncate(output, DefaultOutputLimit), Evidence: evidence}
		},
	}
}

func FileSummary() Definition {
	return Definition{
		Name:        "file.summary",
		Description: "Summarize one text file with metadata, headings, and a short content preview.",
		Metadata: Metadata{
			Purpose:            "summarize a single text file",
			WhenToUse:          []string{"need concise file summary", "before reading full file"},
			WhenNotToUse:       []string{"editing files", "directory overview"},
			RequiredArgs:       []string{"path"},
			OutputContract:     []string{"file path", "headings", "preview lines"},
			AcceptanceSpecRef:  "file.summary/default",
			AcceptanceMode:     AcceptanceCodeLLM,
			SoftFailureSignals: []string{"requires a file path"},
			ParallelMode:       ParallelReadOnlyOK,
			ResourceScope:      "filesystem:path",
		},
		Risk:        RiskSafeRead,
		ArgsSchema: map[string]string{
			"path":      "file path",
			"max_lines": "optional max preview lines, defaults to 40",
		},
		Run: func(ctx context.Context, call Call) Result {
			path, err := ResolveAllowedPath(call.Args["path"], call.Context)
			if err != nil {
				return ErrorResult(err.Error())
			}
			info, err := os.Stat(path)
			if err != nil {
				return ErrorResult(err.Error())
			}
			if info.IsDir() {
				return ErrorResult("file.summary requires a file path, got directory")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return ErrorResult(err.Error())
			}
			maxLines := intArg(call.Args["max_lines"], 40, 10, 120)
			output, evidence := buildFileSummary(path, data, maxLines)
			return Result{OK: true, Output: Truncate(output, DefaultOutputLimit), Evidence: evidence}
		},
	}
}

func resolveProjectTarget(call Call) (string, error) {
	raw := strings.TrimSpace(call.Args["path"])
	if raw == "" {
		if strings.TrimSpace(call.Context.ProjectRoot) != "" {
			return call.Context.ProjectRoot, nil
		}
		if strings.TrimSpace(call.Context.Workspace) != "" {
			return call.Context.Workspace, nil
		}
		return "", fmt.Errorf("path is required")
	}
	return ResolveAllowedPath(raw, call.Context)
}

func buildProjectIndex(root string, maxDepth, maxFiles int) (string, map[string]any, error) {
	info, err := os.Stat(root)
	if err != nil {
		return "", nil, err
	}
	if !info.IsDir() {
		return "", nil, fmt.Errorf("project.index requires a directory path")
	}
	type item struct {
		rel   string
		dir   bool
		depth int
	}
	var (
		items        []item
		dirCount     int
		fileCount    int
		extensionMap = map[string]int{}
	)
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		if shouldSkipProjectPath(rel, d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		depth := pathDepth(rel)
		if d.IsDir() {
			dirCount++
			if depth > maxDepth {
				return filepath.SkipDir
			}
			if len(items) < maxFiles {
				items = append(items, item{rel: rel + "/", dir: true, depth: depth})
			}
			return nil
		}
		fileCount++
		ext := strings.ToLower(filepath.Ext(rel))
		if ext == "" {
			ext = "[no_ext]"
		}
		extensionMap[ext]++
		if depth <= maxDepth && len(items) < maxFiles {
			items = append(items, item{rel: rel, depth: depth})
		}
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].rel < items[j].rel })
	lines := []string{
		"Project index",
		"",
		"- root: " + root,
		fmt.Sprintf("- directories: %d", dirCount),
		fmt.Sprintf("- files: %d", fileCount),
		"- top extensions: " + topExtensions(extensionMap, 8),
		"",
		fmt.Sprintf("Sample tree (max_depth=%d, max_files=%d):", maxDepth, maxFiles),
	}
	for _, entry := range items {
		lines = append(lines, "- "+entry.rel)
	}
	evidence := map[string]any{
		"kind":            "project_index",
		"path":            root,
		"directory_count": dirCount,
		"file_count":      fileCount,
		"extensions":      extensionMap,
		"listed_count":    len(items),
	}
	return strings.Join(lines, "\n"), evidence, nil
}

func buildFileSummary(path string, data []byte, maxLines int) (string, map[string]any) {
	text := string(data)
	lines := strings.Split(text, "\n")
	headings := detectHeadings(lines, 8)
	preview := previewLines(lines, maxLines)
	evidence := map[string]any{
		"kind":       "file_summary",
		"path":       path,
		"bytes":      len(data),
		"lines":      len(lines),
		"start_line": 1,
		"end_line":   previewEndLine(lines, maxLines),
		"headings":   headings,
		"extension":  strings.ToLower(filepath.Ext(path)),
	}
	parts := []string{
		"File summary",
		"",
		"- path: " + path,
		fmt.Sprintf("- bytes: %d", len(data)),
		fmt.Sprintf("- lines: %d", len(lines)),
		"- extension: " + firstNonEmpty(strings.ToLower(filepath.Ext(path)), "[no_ext]"),
	}
	if len(headings) > 0 {
		parts = append(parts, "- headings: "+strings.Join(headings, " | "))
	}
	parts = append(parts, "", "Preview:", preview)
	return strings.Join(parts, "\n"), evidence
}

func intArg(raw string, fallback, min, max int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value == 0 {
		value = fallback
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func previewEndLine(lines []string, limit int) int {
	if len(lines) == 0 {
		return 0
	}
	if limit <= 0 || limit > len(lines) {
		return len(lines)
	}
	return limit
}

func pathDepth(rel string) int {
	clean := filepath.Clean(rel)
	if clean == "." || clean == "" {
		return 0
	}
	return len(strings.Split(clean, string(filepath.Separator)))
}

func shouldSkipProjectPath(rel string, d os.DirEntry) bool {
	base := filepath.Base(rel)
	if strings.HasPrefix(base, ".git") {
		return true
	}
	if d.IsDir() {
		switch base {
		case "node_modules", "vendor", ".idea", ".vscode", "dist", "build", "tmp":
			return true
		}
	}
	return false
}

func topExtensions(counts map[string]int, limit int) string {
	type kv struct {
		key   string
		count int
	}
	items := make([]kv, 0, len(counts))
	for key, count := range counts {
		items = append(items, kv{key: key, count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].count == items[j].count {
			return items[i].key < items[j].key
		}
		return items[i].count > items[j].count
	})
	if len(items) == 0 {
		return "none"
	}
	if len(items) > limit {
		items = items[:limit]
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, fmt.Sprintf("%s=%d", item.key, item.count))
	}
	return strings.Join(parts, ", ")
}

func detectHeadings(lines []string, limit int) []string {
	out := make([]string, 0, limit)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			out = append(out, trimmed)
		} else if strings.HasPrefix(trimmed, "package ") || strings.HasPrefix(trimmed, "func ") || strings.HasPrefix(trimmed, "type ") {
			out = append(out, trimmed)
		}
		if len(out) >= limit {
			break
		}
	}
	return out
}

func previewLines(lines []string, limit int) string {
	if limit <= 0 || len(lines) <= limit {
		return strings.TrimSpace(strings.Join(lines, "\n"))
	}
	return strings.TrimSpace(strings.Join(lines[:limit], "\n")) + "\n...[truncated]..."
}
