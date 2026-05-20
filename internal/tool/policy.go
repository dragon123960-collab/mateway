package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var destructiveCommandPattern = regexp.MustCompile(`(?i)(^|\s|&&|\|\||;|` + "`" + `)(rm(\s|$)|rmdir(\s|$)|mv(\s|$)|sed\s+-i|truncate(\s|$)|dd(\s|$)|shred(\s|$)|git\s+(reset|clean|checkout)(\s|$)|git\s+push(\s|$)|docker\s+compose\s+(up|down)(\s|$)|brew\s+install(\s|$)|npm\s+(install|i)(\s|$)|pnpm\s+(install|add)(\s|$)|pip\s+install(\s|$)|go\s+install(\s|$))`)
var overwriteRedirectPattern = regexp.MustCompile(`(^|[^>])>($|[^>])`)

func IsDangerousCommand(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return false
	}
	if destructiveCommandPattern.MatchString(cmd) {
		return true
	}
	return overwriteRedirectPattern.MatchString(cmd)
}

func RequireConfirmForTool(name string, args map[string]string) bool {
	switch name {
	case "file.write", "file.patch":
		return true
	case "shell.run":
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
