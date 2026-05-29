package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/dongping/mateway/internal/config"
)

var destructiveCommandPattern = regexp.MustCompile(`(?i)(^|\s|&&|\|\||;|` + "`" + `)(rm(\s|$)|rmdir(\s|$)|shred(\s|$)|git\s+(reset|clean)(\s|$))`)

func IsDangerousCommand(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	return cmd != "" && destructiveCommandPattern.MatchString(cmd)
}

func ResolveAllowedPath(raw string, cfg *config.Root) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("path is required")
	}
	expanded := strings.TrimSpace(raw)
	if strings.HasPrefix(expanded, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			expanded = filepath.Join(home, strings.TrimPrefix(expanded, "~/"))
		}
	}
	base := defaultPathBase(cfg)
	if !filepath.IsAbs(expanded) {
		expanded = filepath.Join(base, expanded)
	}
	clean, err := filepath.Abs(filepath.Clean(expanded))
	if err != nil {
		return "", err
	}
	if cfg == nil || !cfg.Security.EnforceWorkspacePaths {
		return clean, nil
	}
	for _, root := range allowedRoots(cfg) {
		rootAbs, err := filepath.Abs(filepath.Clean(root))
		if err != nil || rootAbs == "" {
			continue
		}
		if clean == rootAbs || strings.HasPrefix(clean, rootAbs+string(filepath.Separator)) {
			return clean, nil
		}
	}
	return "", fmt.Errorf("path %s is outside allowed roots", clean)
}

func defaultPathBase(cfg *config.Root) string {
	if cfg != nil && strings.TrimSpace(cfg.App.Home) != "" {
		return cfg.App.Home
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".mateway")
	}
	return "."
}

func allowedRoots(cfg *config.Root) []string {
	if cfg == nil {
		return nil
	}
	var roots []string
	if strings.TrimSpace(cfg.App.Home) != "" {
		roots = append(roots, cfg.App.Home)
	}
	if strings.TrimSpace(cfg.App.Workspace) != "" {
		roots = append(roots, cfg.App.Workspace)
	}
	roots = append(roots, cfg.Security.AccessiblePaths...)
	return roots
}
