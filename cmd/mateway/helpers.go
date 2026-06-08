package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/memory"
)

func reorderRejectReasonFlag(args []string) []string {
	if len(args) != 3 || args[0] == "--reason" || args[0] == "-reason" {
		return args
	}
	if args[1] != "--reason" && args[1] != "-reason" {
		return args
	}
	return []string{args[1], args[2], args[0]}
}

func memoryRoot(cfg *config.Root) string {
	if cfg == nil {
		return filepath.Join(config.DefaultHome(), "workspace", "memory")
	}
	if root := strings.TrimSpace(cfg.Memory.Root); root != "" {
		return root
	}
	workspace := strings.TrimSpace(cfg.App.Workspace)
	if workspace == "" {
		workspace = filepath.Join(cfg.App.Home, "workspace")
	}
	return filepath.Join(workspace, "memory")
}

func memoryIndexPath(cfg *config.Root) string {
	home := config.DefaultHome()
	if cfg != nil && strings.TrimSpace(cfg.App.Home) != "" {
		home = cfg.App.Home
	}
	return filepath.Join(home, "indexes", "memory_index.json")
}

func hasMemoryErrors(issues []memory.Issue) bool {
	for _, issue := range issues {
		if issue.Severity == "error" {
			return true
		}
	}
	return false
}

func splitComma(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func countFiles(dir, ext string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && (ext == "" || strings.EqualFold(filepath.Ext(entry.Name()), ext)) {
			count++
		}
	}
	return count
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func summarizeCLIText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit] + fmt.Sprintf("... (%d chars)", len(text))
}

type homeReport struct {
	Home      string
	Expected  []homeReportItem
	Generated []homeReportItem
	Local     []homeReportItem
	Unknown   []homeReportItem
}

type homeReportItem struct {
	Name string
	Kind string
}

func buildHomeReport(home string) (homeReport, error) {
	report := homeReport{Home: home}
	entries, err := os.ReadDir(home)
	if err != nil {
		return report, err
	}
	expected := map[string]string{
		"config":    "configuration",
		"workspace": "agent workspace, memory, skills",
		"sessions":  "runtime session state",
	}
	generated := map[string]string{
		"trace":     "runtime traces",
		"observe":   "learning diary, proposals, audit",
		"indexes":   "derived search indexes",
		"schedules": "scheduled task state",
		"run":       "process runtime state",
		"logs":      "service logs",
		"tmp":       "temporary files",
	}
	local := map[string]string{
		"scripts":        "local user scripts",
		"docker":         "legacy/local service data",
		"docker-compose": "legacy/local service data",
	}
	for _, entry := range entries {
		item := homeReportItem{Name: entry.Name()}
		switch {
		case expected[entry.Name()] != "":
			item.Kind = expected[entry.Name()]
			report.Expected = append(report.Expected, item)
		case generated[entry.Name()] != "":
			item.Kind = generated[entry.Name()]
			report.Generated = append(report.Generated, item)
		case local[entry.Name()] != "":
			item.Kind = local[entry.Name()]
			report.Local = append(report.Local, item)
		default:
			item.Kind = "not recognized by current clean layout"
			report.Unknown = append(report.Unknown, item)
		}
	}
	return report, nil
}
