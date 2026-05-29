package memory

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type Issue struct {
	Path     string
	Severity string
	Code     string
	Message  string
}

type LintResult struct {
	Root   string
	Files  int
	Issues []Issue
}

func (r LintResult) HasErrors() bool {
	for _, issue := range r.Issues {
		if issue.Severity == "error" {
			return true
		}
	}
	return false
}

type Document struct {
	Path        string
	RelPath     string
	FrontMatter map[string]any
	Body        string
}

func LintRoot(root string) (LintResult, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return LintResult{}, fmt.Errorf("memory root is required")
	}
	result := LintResult{Root: root}
	err := WalkDocuments(root, func(doc Document, issues []Issue) error {
		result.Files++
		result.Issues = append(result.Issues, issues...)
		if len(issues) > 0 && doc.FrontMatter == nil {
			return nil
		}
		result.Issues = append(result.Issues, lintDocument(doc)...)
		return nil
	})
	sortIssues(result.Issues)
	return result, err
}

func WalkDocuments(root string, fn func(Document, []Issue) error) error {
	root = strings.TrimSpace(root)
	if root == "" {
		return fmt.Errorf("memory root is required")
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(path), ".md") {
			return nil
		}
		if isSupportMarkdown(root, path) {
			return nil
		}
		doc, issues := ReadDocument(path)
		if rel, err := filepath.Rel(root, path); err == nil {
			doc.RelPath = filepath.ToSlash(rel)
		}
		return fn(doc, issues)
	})
}

func ReadDocument(path string) (Document, []Issue) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Document{Path: path}, []Issue{newIssue(path, "error", "read_failed", err.Error())}
	}
	frontMatter, body, err := parseFrontMatter(data)
	if err != nil {
		return Document{Path: path, Body: strings.TrimSpace(string(data))}, []Issue{newIssue(path, "error", "invalid_frontmatter", err.Error())}
	}
	if frontMatter == nil {
		return Document{Path: path, Body: strings.TrimSpace(string(data))}, []Issue{newIssue(path, "error", "missing_frontmatter", "memory entry must start with YAML frontmatter")}
	}
	return Document{Path: path, FrontMatter: frontMatter, Body: strings.TrimSpace(body)}, nil
}

func parseFrontMatter(data []byte) (map[string]any, string, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, string(data), nil
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, string(data), fmt.Errorf("frontmatter is missing closing ---")
	}
	raw := strings.Join(lines[1:end], "\n")
	var fm map[string]any
	if err := yaml.Unmarshal([]byte(raw), &fm); err != nil {
		return nil, string(data), err
	}
	if fm == nil {
		fm = map[string]any{}
	}
	return fm, strings.Join(lines[end+1:], "\n"), nil
}

func lintDocument(doc Document) []Issue {
	var issues []Issue
	for _, field := range requiredFields {
		if strings.TrimSpace(fmt.Sprint(doc.FrontMatter[field])) == "" {
			issues = append(issues, newIssue(doc.Path, "error", "missing_required", "missing required frontmatter field: "+field))
		}
	}
	for field, allowed := range enumFields {
		value := strings.TrimSpace(fmt.Sprint(doc.FrontMatter[field]))
		if value == "" {
			continue
		}
		if !allowed[value] {
			issues = append(issues, newIssue(doc.Path, "error", "invalid_enum", fmt.Sprintf("%s has invalid value %q", field, value)))
		}
	}
	if status := strings.TrimSpace(fmt.Sprint(doc.FrontMatter["status"])); status == "active" && !hasSources(doc.FrontMatter["sources"]) {
		issues = append(issues, newIssue(doc.Path, "warning", "active_without_sources", "active memory should include source evidence"))
	}
	if looksSensitive(doc.Body) {
		issues = append(issues, newIssue(doc.Path, "error", "possible_secret", "memory body appears to contain a secret-like field"))
	}
	return issues
}

func hasSources(value any) bool {
	switch typed := value.(type) {
	case []any:
		return len(typed) > 0
	case []string:
		return len(typed) > 0
	default:
		return strings.TrimSpace(fmt.Sprint(value)) != "" && strings.TrimSpace(fmt.Sprint(value)) != "[]"
	}
}

func isSupportMarkdown(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	base := strings.ToLower(filepath.Base(rel))
	if base == "readme.md" || base == "schema.md" || base == "index.md" || base == "log.md" {
		return true
	}
	return false
}

func looksSensitive(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range []string{"api_key", "app_secret", "password", "private_key", "cookie", "authorization: bearer"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func newIssue(path, severity, code, message string) Issue {
	return Issue{Path: path, Severity: severity, Code: code, Message: message}
}

func sortIssues(issues []Issue) {
	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].Path != issues[j].Path {
			return issues[i].Path < issues[j].Path
		}
		if issues[i].Severity != issues[j].Severity {
			return issues[i].Severity < issues[j].Severity
		}
		return issues[i].Code < issues[j].Code
	})
}

var requiredFields = []string{
	"type",
	"scope",
	"visibility",
	"status",
	"sources",
	"confidence",
	"created_at",
	"updated_at",
	"schema_version",
}

var enumFields = map[string]map[string]bool{
	"type": {
		"preference": true, "decision": true, "experience": true, "skill": true,
		"pattern": true, "wiki": true, "diary": true, "reflection": true, "proposal": true,
	},
	"scope": {
		"global": true, "user": true, "org": true, "agent": true, "project": true,
	},
	"visibility": {
		"private": true, "shared-user": true, "shared-org": true,
	},
	"status": {
		"proposed": true, "active": true, "rejected": true, "deprecated": true, "archived": true,
	},
	"confidence": {
		"high": true, "medium": true, "low": true,
	},
}
