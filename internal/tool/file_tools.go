package tool

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/agentprofile"
	"github.com/dongping/mateway/internal/config"
)

func (FileWriteTool) Name() string        { return "file.write" }
func (FileWriteTool) Description() string { return "write a local text file" }
func (FileWriteTool) Schema() agentcore.Schema {
	return agentcore.Schema{Required: []string{"path", "content"}}
}
func (FileWriteTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{
		WhenToUse:            "Use when the task explicitly requires creating or replacing a local text file.",
		WhenNotToUse:         "Do not use to answer questions, inspect files, or modify files outside the allowed workspace.",
		OutputContract:       "Return a short write confirmation with path and byte count evidence.",
		Evidence:             "Return the written path and byte count.",
		Acceptance:           "Accepted when the file write succeeds and evidence includes path and bytes.",
		SoftFailureSignals:   []string{"permission denied", "outside allowed roots", "no such file or directory"},
		ParallelMode:         "forbid",
		ReusePolicy:          "never",
		ConfirmationBoundary: "guarded mutation; path policy and secret scanning are enforced before writing.",
	}
}
func (FileWriteTool) Risk() agentcore.Risk { return agentcore.RiskGuardedMutation }
func (t FileWriteTool) Run(_ context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	path, err := ResolveAllowedPath(fmt.Sprint(call.Args["path"]), t.Config)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true, Evidence: map[string]any{"path": fmt.Sprint(call.Args["path"])}}
	}
	content := fmt.Sprint(call.Args["content"])
	profileStore := agentprofile.NewStore(t.Config)
	if _, ok := profileStore.CoreTargetAgent(path); ok {
		proposal, err := profileStore.Create(agentprofile.CreateInput{TargetPath: path, NewContent: content})
		if err != nil {
			return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true, Evidence: map[string]any{"path": path}}
		}
		return agentcore.ToolResult{
			ToolCallID: call.ID,
			Content:    "profile proposal " + proposal.ID + " created for " + proposal.TargetPath + "; promote with mateway agent-profile proposal promote " + proposal.ID,
			Evidence: map[string]any{
				"proposal_id":     proposal.ID,
				"target_path":     proposal.TargetPath,
				"requires_review": true,
			},
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true}
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true}
	}
	return agentcore.ToolResult{ToolCallID: call.ID, Content: "wrote " + path, Evidence: map[string]any{"path": path, "bytes": len(content)}}
}

func (FileDeleteTool) Name() string { return "file.delete" }
func (FileDeleteTool) Description() string {
	return "delete a local file or directory inside allowed roots"
}
func (FileDeleteTool) Schema() agentcore.Schema {
	return agentcore.Schema{
		Required: []string{"path"},
		Properties: map[string]any{
			"path":      map[string]any{"type": "string"},
			"recursive": map[string]any{"type": "boolean", "description": "Required to delete directories."},
		},
	}
}
func (FileDeleteTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{
		WhenToUse:            "Use only when the task explicitly requires removing a local generated file or scratch directory.",
		WhenNotToUse:         "Do not use for exploratory cleanup, project source removal, config/run/secret/trace stores, or paths outside allowed roots.",
		OutputContract:       "Return a short delete confirmation with path, kind, recursive flag, and size/count evidence.",
		Evidence:             "Return resolved path, kind, bytes for files, entry count for directories, and deleted=true.",
		Acceptance:           "Accepted only when the target path is valid, inside allowed roots, not a protected root/store, and deletion succeeds.",
		SoftFailureSignals:   []string{"outside allowed roots", "refusing to delete", "recursive=true is required", "no such file or directory"},
		ParallelMode:         "forbid",
		ReusePolicy:          "never",
		ConfirmationBoundary: "dangerous mutation; guarded by strict path policy and protected path checks, not chat approval.",
	}
}
func (FileDeleteTool) Risk() agentcore.Risk { return agentcore.RiskDangerous }
func (t FileDeleteTool) Run(_ context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	rawPath := fmt.Sprint(call.Args["path"])
	path, err := ResolveAllowedPath(rawPath, t.Config)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true, Evidence: map[string]any{"path": rawPath}}
	}
	if err := ensureDeletePathInsideAllowedRoots(path, t.Config); err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true, Evidence: map[string]any{"path": rawPath}}
	}
	target, err := validateDeleteTarget(path, t.Config)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true, Evidence: map[string]any{"path": path}}
	}
	if target.Info.IsDir() && !boolArg(call.Args["recursive"]) {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: "recursive=true is required to delete a directory", IsError: true, Evidence: map[string]any{"path": path, "kind": "directory"}}
	}
	evidence := map[string]any{
		"path":      target.Path,
		"kind":      target.Kind,
		"recursive": boolArg(call.Args["recursive"]),
		"deleted":   true,
	}
	if target.Info.IsDir() {
		count, err := countDirectoryEntries(target.Path)
		if err != nil {
			return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true, Evidence: map[string]any{"path": target.Path, "kind": "directory"}}
		}
		evidence["entries"] = count
		if err := os.RemoveAll(target.Path); err != nil {
			return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true, Evidence: map[string]any{"path": target.Path, "kind": "directory"}}
		}
		return agentcore.ToolResult{ToolCallID: call.ID, Content: "deleted directory " + target.Path, Evidence: evidence}
	}
	evidence["bytes"] = target.Info.Size()
	if err := os.Remove(target.Path); err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true, Evidence: map[string]any{"path": target.Path, "kind": "file"}}
	}
	return agentcore.ToolResult{ToolCallID: call.ID, Content: "deleted file " + target.Path, Evidence: evidence}
}

type deleteTarget struct {
	Path string
	Info os.FileInfo
	Kind string
}

func validateDeleteTarget(path string, cfg *config.Root) (deleteTarget, error) {
	clean, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return deleteTarget{}, err
	}
	info, err := os.Lstat(clean)
	if err != nil {
		return deleteTarget{}, err
	}
	if err := rejectProtectedDeletePath(clean, cfg); err != nil {
		return deleteTarget{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(clean)
		if err != nil {
			return deleteTarget{}, err
		}
		if err := ensureDeletePathInsideAllowedRoots(target, cfg); err != nil {
			return deleteTarget{}, fmt.Errorf("symlink target %s is outside allowed roots", target)
		}
		if err := rejectProtectedDeletePath(target, cfg); err != nil {
			return deleteTarget{}, err
		}
		return deleteTarget{Path: clean, Info: info, Kind: "symlink"}, nil
	}
	real := clean
	if info.IsDir() {
		real, err = filepath.EvalSymlinks(clean)
		if err != nil {
			return deleteTarget{}, err
		}
		if err := ensureDeletePathInsideAllowedRoots(real, cfg); err != nil {
			return deleteTarget{}, fmt.Errorf("directory target %s is outside allowed roots", real)
		}
		if err := rejectProtectedDeletePath(real, cfg); err != nil {
			return deleteTarget{}, err
		}
		return deleteTarget{Path: real, Info: info, Kind: "directory"}, nil
	}
	return deleteTarget{Path: clean, Info: info, Kind: "file"}, nil
}

func ensureDeletePathInsideAllowedRoots(path string, cfg *config.Root) error {
	clean, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return err
	}
	roots := allowedRoots(cfg)
	if len(roots) == 0 {
		return fmt.Errorf("file.delete requires configured allowed roots")
	}
	realPath := clean
	if evaluated, err := filepath.EvalSymlinks(clean); err == nil {
		realPath = evaluated
	}
	for _, root := range allowedRoots(cfg) {
		rootAbs, err := filepath.Abs(filepath.Clean(root))
		if err != nil || rootAbs == "" {
			continue
		}
		realRoot := rootAbs
		if evaluated, err := filepath.EvalSymlinks(rootAbs); err == nil {
			realRoot = evaluated
		}
		if realPath == realRoot || strings.HasPrefix(realPath, realRoot+string(filepath.Separator)) {
			return nil
		}
	}
	return fmt.Errorf("path %s is outside allowed roots", clean)
}

func rejectProtectedDeletePath(path string, cfg *config.Root) error {
	clean, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return err
	}
	for _, root := range allowedRoots(cfg) {
		rootAbs, err := filepath.Abs(filepath.Clean(root))
		if err != nil || rootAbs == "" {
			continue
		}
		if clean == rootAbs {
			return fmt.Errorf("refusing to delete allowed root %s", clean)
		}
	}
	home := configHome(cfg)
	protected := []string{
		filepath.Join(home, "config"),
		filepath.Join(home, "run"),
		filepath.Join(home, "secrets"),
		filepath.Join(home, "trace"),
		filepath.Join(home, "traces"),
		filepath.Join(home, "sessions"),
		filepath.Join(home, "schedules"),
		filepath.Join(home, "indexes"),
		filepath.Join(home, "logs"),
		filepath.Join(home, "observe"),
		filepath.Join(home, "memory"),
		filepath.Join(home, ".git"),
	}
	for _, root := range protected {
		rootAbs, err := filepath.Abs(filepath.Clean(root))
		if err != nil || rootAbs == "" {
			continue
		}
		if clean == rootAbs || strings.HasPrefix(clean, rootAbs+string(filepath.Separator)) {
			return fmt.Errorf("refusing to delete protected path %s", clean)
		}
	}
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		switch part {
		case ".git", ".hg", ".svn":
			return fmt.Errorf("refusing to delete VCS path %s", clean)
		}
	}
	return nil
}

func countDirectoryEntries(root string) (int, error) {
	count := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != root {
			count++
		}
		return nil
	})
	return count, err
}

func (ProjectIndexTool) Name() string { return "project.index" }
func (ProjectIndexTool) Description() string {
	return "list files and directories under a path with bounded depth and common heavy directories skipped"
}
func (ProjectIndexTool) Schema() agentcore.Schema {
	return agentcore.Schema{
		Required: []string{"path"},
		Properties: map[string]any{
			"path":      map[string]any{"type": "string"},
			"limit":     map[string]any{"type": "integer", "description": "Maximum entries to return. Defaults to 200 and is capped at 5000."},
			"max_depth": map[string]any{"type": "integer", "description": "Maximum directory depth below path. Defaults to 4 and is capped at 8."},
			"skip_dirs": map[string]any{"type": "array", "description": "Directory names to list with [skip] and not descend into."},
		},
	}
}
func (ProjectIndexTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{
		WhenToUse:            "Use before reading a project when you need a bounded overview of the directory structure. Keep max_depth and limit small unless the user explicitly asks for a broader scan.",
		WhenNotToUse:         "Do not use as a replacement for reading a specific file whose path is already known.",
		OutputContract:       "Return relative directory entries with DIR: and FILE: prefixes. Common generated/cache/vendor directories are listed with [skip] guidance instead of hidden.",
		Evidence:             "Return scanned root path, entry count, skipped entry count, limit, max_depth, partial, and elapsed time.",
		Acceptance:           "Accepted when the directory scan succeeds and returns entry count evidence.",
		SoftFailureSignals:   []string{"path is not a directory", "permission denied", "outside allowed roots"},
		ParallelMode:         "read_only_ok",
		ReusePolicy:          "stable_read",
		ConfirmationBoundary: "safe read; no confirmation.",
	}
}
func (ProjectIndexTool) Risk() agentcore.Risk { return agentcore.RiskSafeRead }
func (t ProjectIndexTool) Run(ctx context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	start := time.Now()
	root, err := ResolveAllowedPath(fmt.Sprint(call.Args["path"]), t.Config)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true, Evidence: map[string]any{"path": fmt.Sprint(call.Args["path"])}}
	}
	info, err := os.Stat(root)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true, Evidence: map[string]any{"path": root}}
	}
	if !info.IsDir() {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: "path is not a directory", IsError: true, Evidence: map[string]any{"path": root}}
	}
	limit := boundedIntArg(call.Args["limit"], 200, 1, 5000)
	maxDepth := boundedIntArg(call.Args["max_depth"], 4, 1, 8)
	skipDirs := projectIndexSkipDirs(call.Args["skip_dirs"])
	entries, partial, skipped, err := readProjectIndexEntries(ctx, root, limit, maxDepth, skipDirs)
	evidence := map[string]any{
		"path":       root,
		"entries":    len(entries),
		"limit":      limit,
		"max_depth":  maxDepth,
		"partial":    partial,
		"skipped":    skipped,
		"elapsed_ms": time.Since(start).Milliseconds(),
	}
	if err != nil {
		evidence["partial"] = len(entries) > 0
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true, Evidence: evidence}
	}
	return agentcore.ToolResult{ToolCallID: call.ID, Content: strings.Join(projectIndexEntryLines(entries), "\n"), Evidence: evidence}
}

type projectIndexEntry struct {
	Path    string
	IsDir   bool
	Skipped bool
}

func readProjectIndexEntries(ctx context.Context, root string, limit, maxDepth int, skipDirs map[string]bool) ([]projectIndexEntry, bool, int, error) {
	root = filepath.Clean(root)
	entries := make([]projectIndexEntry, 0, limit)
	skipped := 0
	partial := false
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			partial = len(entries) > 0
			return ctx.Err()
		default:
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		depth := pathDepth(rel)
		if depth > maxDepth {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		skip := d.IsDir() && skipDirs[strings.ToLower(d.Name())]
		if len(entries) >= limit {
			partial = true
			if d.IsDir() {
				return filepath.SkipDir
			}
			return filepath.SkipAll
		}
		entries = append(entries, projectIndexEntry{Path: filepath.ToSlash(rel), IsDir: d.IsDir(), Skipped: skip})
		if skip {
			skipped++
			return filepath.SkipDir
		}
		return nil
	})
	if err == filepath.SkipAll {
		err = nil
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return entries[i].Path < entries[j].Path
	})
	return entries, partial, skipped, err
}

func pathDepth(rel string) int {
	rel = filepath.Clean(rel)
	if rel == "." || rel == "" {
		return 0
	}
	return strings.Count(rel, string(filepath.Separator)) + 1
}

func projectIndexEntryLines(entries []projectIndexEntry) []string {
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir {
			suffix := "/"
			if entry.Skipped {
				suffix += " [skip]"
			}
			lines = append(lines, "DIR:  "+entry.Path+suffix)
		} else {
			lines = append(lines, "FILE: "+entry.Path)
		}
	}
	return lines
}

func projectIndexSkipDirs(value any) map[string]bool {
	out := defaultProjectIndexSkipDirs()
	for _, item := range stringSliceArg(value) {
		name := strings.ToLower(strings.TrimSpace(item))
		if name != "" {
			out[name] = true
		}
	}
	return out
}

func defaultProjectIndexSkipDirs() map[string]bool {
	out := map[string]bool{}
	for _, name := range []string{
		".git", ".hg", ".svn",
		"node_modules", "bower_components", "vendor",
		"dist", "build", "out", "target", "bin", "obj", "coverage",
		".next", ".nuxt", ".svelte-kit", ".astro", ".vite", ".parcel-cache", ".turbo", ".docusaurus",
		".cache", ".pytest_cache", ".mypy_cache", ".ruff_cache", ".tox", ".nox",
		"__pycache__", ".venv", "venv", "env", ".env", "site-packages",
		"Pods", "DerivedData", ".gradle", ".idea", ".vscode",
		"tmp", "temp", ".tmp", "logs",
	} {
		out[strings.ToLower(name)] = true
	}
	return out
}

func indexDirectoryToolResult(ctx context.Context, callID, path string, limit int) agentcore.ToolResult {
	start := time.Now()
	entries, partial, err := readDirEntriesLimited(ctx, path, limit)
	evidence := map[string]any{
		"path":       path,
		"entries":    len(entries),
		"limit":      limit,
		"partial":    partial,
		"elapsed_ms": time.Since(start).Milliseconds(),
		"directory":  true,
	}
	if err != nil {
		evidence["partial"] = len(entries) > 0
		return agentcore.ToolResult{ToolCallID: callID, Content: err.Error(), IsError: true, Evidence: evidence}
	}
	lines, skipped := directoryEntryLines(entries)
	evidence["skipped"] = skipped
	return agentcore.ToolResult{ToolCallID: callID, Content: strings.Join(lines, "\n"), Evidence: evidence}
}

func directoryEntryLines(entries []os.DirEntry) ([]string, int) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return entries[i].Name() < entries[j].Name()
	})
	lines := make([]string, 0, len(entries))
	skipped := 0
	for _, entry := range entries {
		if entry.IsDir() {
			suffix := "/"
			if shouldSkipProjectIndexDir(entry.Name()) {
				suffix += " [skip]"
				skipped++
			}
			lines = append(lines, "DIR:  "+entry.Name()+suffix)
		} else {
			lines = append(lines, "FILE: "+entry.Name())
		}
	}
	return lines, skipped
}

func shouldSkipProjectIndexDir(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if normalized == "" {
		return false
	}
	switch normalized {
	case ".git", ".hg", ".svn",
		"node_modules", "bower_components", "vendor",
		"dist", "build", "out", "target", "bin", "obj", "coverage",
		".next", ".nuxt", ".svelte-kit", ".astro", ".vite", ".parcel-cache", ".turbo",
		".cache", ".pytest_cache", ".mypy_cache", ".ruff_cache", ".tox", ".nox",
		"__pycache__", ".venv", "venv", "env", ".env", "site-packages",
		"Pods", "DerivedData", ".gradle", ".idea", ".vscode",
		"tmp", "temp", ".tmp", "logs":
		return true
	default:
		return false
	}
}

func readDirEntriesLimited(ctx context.Context, root string, limit int) ([]os.DirEntry, bool, error) {
	dir, err := os.Open(root)
	if err != nil {
		return nil, false, err
	}
	defer dir.Close()
	entries := make([]os.DirEntry, 0, limit)
	for len(entries) < limit {
		select {
		case <-ctx.Done():
			return entries, len(entries) > 0, ctx.Err()
		default:
		}
		batchSize := limit - len(entries)
		if batchSize > 128 {
			batchSize = 128
		}
		batch, err := dir.ReadDir(batchSize)
		if len(batch) > 0 {
			entries = append(entries, batch...)
		}
		if err == io.EOF {
			return entries, false, nil
		}
		if err != nil {
			return entries, len(entries) > 0, err
		}
	}
	if _, err := dir.ReadDir(1); err == io.EOF {
		return entries, false, nil
	}
	return entries, true, nil
}

func (FileReadTool) Name() string        { return "file.read" }
func (FileReadTool) Description() string { return "read a local text file" }
func (FileReadTool) Schema() agentcore.Schema {
	return agentcore.Schema{Required: []string{"path"}}
}
func (FileReadTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{
		WhenToUse:            "Use when the task requires reading a known local text file.",
		WhenNotToUse:         "Do not use when the file path is unknown; inspect the project first with project.index.",
		OutputContract:       "Return file text content for files. For directories, return the same non-recursive index format as project.index.",
		Evidence:             "Return read path and byte count for files; return path, entries, limit, partial, and directory=true for directories.",
		Acceptance:           "Accepted when the file or directory exists, is readable, and evidence describes what was read.",
		SoftFailureSignals:   []string{"no such file or directory", "permission denied", "outside allowed roots"},
		ParallelMode:         "read_only_ok",
		ReusePolicy:          "stable_read",
		ConfirmationBoundary: "safe read; no confirmation.",
	}
}
func (FileReadTool) Risk() agentcore.Risk { return agentcore.RiskSafeRead }
func (t FileReadTool) Run(ctx context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	path, err := ResolveAllowedPath(fmt.Sprint(call.Args["path"]), t.Config)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true, Evidence: map[string]any{"path": fmt.Sprint(call.Args["path"])}}
	}
	if err := rejectProtectedReadPath(path, t.Config); err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true, Evidence: map[string]any{"path": path}}
	}
	info, err := os.Stat(path)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true}
	}
	if info.IsDir() {
		return indexDirectoryToolResult(ctx, call.ID, path, boundedIntArg(call.Args["limit"], 120, 1, 1000))
	}
	if info.Size() > 512*1024 {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: fmt.Sprintf("file too large: %d bytes", info.Size()), IsError: true, Evidence: map[string]any{"path": path, "bytes": info.Size()}}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true}
	}
	if isLikelyBinary(data) {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: "file appears to be binary", IsError: true, Evidence: map[string]any{"path": path, "bytes": len(data)}}
	}
	return agentcore.ToolResult{
		ToolCallID: call.ID,
		Content:    string(data),
		Evidence:   map[string]any{"path": path, "bytes": len(data)},
	}
}

func isLikelyBinary(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	sample := data
	if len(sample) > 4096 {
		sample = sample[:4096]
	}
	for _, b := range sample {
		if b == 0 {
			return true
		}
	}
	return !utf8.Valid(data)
}

func rejectProtectedReadPath(path string, cfg *config.Root) error {
	clean, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return err
	}
	home := configHome(cfg)
	protected := []string{
		filepath.Join(home, "secrets"),
	}
	for _, root := range protected {
		rootAbs, err := filepath.Abs(filepath.Clean(root))
		if err != nil || rootAbs == "" {
			continue
		}
		if clean == rootAbs || strings.HasPrefix(clean, rootAbs+string(filepath.Separator)) {
			return fmt.Errorf("refusing to read protected path %s", clean)
		}
	}
	return nil
}
