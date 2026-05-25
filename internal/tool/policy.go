package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var destructiveCommandPattern = regexp.MustCompile(`(?i)(^|\s|&&|\|\||;|` + "`" + `)(rm(\s|$)|rmdir(\s|$)|shred(\s|$)|git\s+(reset|clean)(\s|$))`)

func IsDangerousCommand(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return false
	}
	return destructiveCommandPattern.MatchString(cmd)
}

func RequireConfirmForTool(name string, args map[string]string) bool {
	switch name {
	case "schedule.delete":
		return true
	case "shell.run", "terminal.run":
		return IsDangerousCommand(args["command"])
	default:
		return false
	}
}

func ResolveAllowedPath(raw string, ctx Context) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("path is required")
	}
	expanded := strings.TrimSpace(raw)
	if strings.HasPrefix(expanded, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			expanded = filepath.Join(home, strings.TrimPrefix(expanded, "~/"))
		}
	}
	if !filepath.IsAbs(expanded) {
		expanded = filepath.Join(ctx.ProjectRoot, expanded)
	}
	clean, err := filepath.Abs(filepath.Clean(expanded))
	if err != nil {
		return "", err
	}
	roots := allowedRoots(ctx)
	for _, root := range roots {
		rootAbs, err := filepath.Abs(filepath.Clean(root))
		if err != nil {
			continue
		}
		if clean == rootAbs || strings.HasPrefix(clean, rootAbs+string(filepath.Separator)) {
			return clean, nil
		}
	}
	if filepath.IsAbs(strings.TrimSpace(raw)) {
		if remapped, ok := remapAbsolutePathToAllowedRoot(clean, roots); ok {
			return remapped, nil
		}
	}
	return "", fmt.Errorf("path %s is outside allowed roots", clean)
}

func allowedRoots(ctx Context) []string {
	var roots []string
	if ctx.ProjectRoot != "" {
		roots = append(roots, ctx.ProjectRoot)
	}
	if ctx.Workspace != "" {
		roots = append(roots, ctx.Workspace)
	}
	roots = append(roots, ctx.AllowedRoots...)
	return roots
}

func remapAbsolutePathToAllowedRoot(clean string, roots []string) (string, bool) {
	for _, root := range roots {
		rootAbs, err := filepath.Abs(filepath.Clean(root))
		if err != nil || rootAbs == "" {
			continue
		}
		if candidate, ok := remapByKnownProjectSuffix(clean, rootAbs); ok {
			return candidate, true
		}
		if candidate, ok := remapByExistingTail(clean, rootAbs); ok {
			return candidate, true
		}
	}
	return "", false
}

func remapByKnownProjectSuffix(clean, rootAbs string) (string, bool) {
	rootName := filepath.Base(rootAbs)
	if rootName == "" || rootName == "." || rootName == string(filepath.Separator) {
		return "", false
	}
	parts := splitCleanPath(clean)
	for i, part := range parts {
		if part != rootName || i == len(parts)-1 {
			continue
		}
		rel := filepath.Join(parts[i+1:]...)
		if rel == "" {
			continue
		}
		candidate := filepath.Join(rootAbs, rel)
		if fileExists(candidate) {
			return candidate, true
		}
	}
	return "", false
}

func remapByExistingTail(clean, rootAbs string) (string, bool) {
	parts := splitCleanPath(clean)
	for i := 0; i < len(parts)-1; i++ {
		rel := filepath.Join(parts[i:]...)
		if rel == "" {
			continue
		}
		candidate := filepath.Join(rootAbs, rel)
		if fileExists(candidate) {
			return candidate, true
		}
	}
	return "", false
}

func splitCleanPath(path string) []string {
	trimmed := strings.Trim(path, string(filepath.Separator))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, string(filepath.Separator))
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
