package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type LintIssue struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type LintReport struct {
	Root      string      `json:"root"`
	CheckedAt time.Time   `json:"checked_at"`
	Issues    []LintIssue `json:"issues"`
}

func Lint(root string) (LintReport, error) {
	report := LintReport{Root: root, CheckedAt: time.Now()}
	if strings.TrimSpace(root) == "" {
		return report, fmt.Errorf("memory root is required")
	}
	known := map[string]string{}
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		name := strings.TrimSuffix(filepath.ToSlash(rel), ".md")
		known[name] = path
		known[strings.TrimSuffix(filepath.Base(path), ".md")] = path
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			report.Issues = append(report.Issues, LintIssue{Path: path, Code: "read_failed", Message: readErr.Error()})
			return nil
		}
		text := string(data)
		parsed, parseErr := parseMarkdown(text)
		if requiresFrontmatter(path) {
			if !parsed.HasYAML {
				report.Issues = append(report.Issues, LintIssue{Path: path, Code: "missing_frontmatter", Message: "durable memory page should start with YAML frontmatter"})
			} else if parseErr != nil {
				report.Issues = append(report.Issues, LintIssue{Path: path, Code: "invalid_frontmatter", Message: parseErr.Error()})
			} else {
				report.Issues = append(report.Issues, validateMemoryFrontmatter(path, parsed.Frontmatter)...)
			}
		}
		if requiresSource(path) && hasSpecificClaim(parsed.Body) && !hasStrongSourceEvidence(parsed.Frontmatter.Sources) {
			report.Issues = append(report.Issues, LintIssue{Path: path, Code: "weak_evidence", Message: "specific claims should include source path, URL, or line evidence"})
		}
		return nil
	}); err != nil {
		return report, err
	}
	linkRe := regexp.MustCompile(`\[\[([^\]]+)\]\]`)
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		for _, match := range linkRe.FindAllStringSubmatch(stripMarkdownCode(string(data)), -1) {
			target := strings.TrimSpace(strings.Split(match[1], "|")[0])
			target = strings.TrimSuffix(filepath.ToSlash(target), ".md")
			if _, ok := known[target]; !ok {
				report.Issues = append(report.Issues, LintIssue{Path: path, Code: "broken_wikilink", Message: "missing target: " + target})
			}
		}
		return nil
	})
	return report, nil
}

func stripMarkdownCode(text string) string {
	var out []string
	inFence := false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			out = append(out, "")
			continue
		}
		if inFence {
			out = append(out, "")
			continue
		}
		out = append(out, stripInlineCode(line))
	}
	return strings.Join(out, "\n")
}

func stripInlineCode(line string) string {
	var b strings.Builder
	inCode := false
	for _, r := range line {
		if r == '`' {
			inCode = !inCode
			b.WriteRune(' ')
			continue
		}
		if inCode {
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func requiresFrontmatter(path string) bool {
	normalized := filepath.ToSlash(path)
	return strings.Contains(normalized, "/long/") || strings.Contains(normalized, "/inbox/")
}

func requiresSource(path string) bool {
	normalized := filepath.ToSlash(path)
	return strings.Contains(normalized, "/long/")
}

func hasSpecificClaim(text string) bool {
	for _, r := range text {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return strings.Contains(text, "%") || strings.Contains(text, "$")
}
