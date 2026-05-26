package memory

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

type Frontmatter struct {
	Type       string   `yaml:"type"`
	Scope      string   `yaml:"scope"`
	OwnerAgent string   `yaml:"owner_agent"`
	Visibility string   `yaml:"visibility"`
	Status     string   `yaml:"status"`
	Tags       []string `yaml:"tags"`
	Aliases    []string `yaml:"aliases"`
	Sources    []string `yaml:"sources"`
	Confidence string   `yaml:"confidence"`
	CreatedAt  string   `yaml:"created_at"`
	UpdatedAt  string   `yaml:"updated_at"`
}

type ParsedMarkdown struct {
	Frontmatter Frontmatter
	Body        string
	RawYAML     string
	HasYAML     bool
}

func parseMarkdown(text string) (ParsedMarkdown, error) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "---") {
		return ParsedMarkdown{Body: text}, nil
	}
	parts := strings.SplitN(trimmed, "---", 3)
	if len(parts) < 3 {
		return ParsedMarkdown{Body: text}, fmt.Errorf("frontmatter is not closed")
	}
	raw := strings.TrimSpace(parts[1])
	body := strings.TrimSpace(parts[2])
	var fm Frontmatter
	if err := yaml.Unmarshal([]byte(raw), &fm); err != nil {
		return ParsedMarkdown{Body: body, RawYAML: raw, HasYAML: true}, err
	}
	return ParsedMarkdown{Frontmatter: fm, Body: body, RawYAML: raw, HasYAML: true}, nil
}

func ParseMarkdownForTools(text string) (ParsedMarkdown, error) {
	return parseMarkdown(text)
}

func validateMemoryFrontmatter(path string, fm Frontmatter) []LintIssue {
	var issues []LintIssue
	if strings.TrimSpace(fm.Type) == "" {
		issues = append(issues, LintIssue{Path: path, Code: "missing_type", Message: "memory frontmatter type is required"})
	}
	if !allowedValue(fm.Scope, []string{"agent", "user", "org"}) {
		issues = append(issues, LintIssue{Path: path, Code: "invalid_scope", Message: "memory frontmatter scope must be agent, user, or org"})
	}
	if !allowedValue(fm.Status, []string{"active", "proposed", "committed", "rejected", "deprecated"}) {
		issues = append(issues, LintIssue{Path: path, Code: "invalid_status", Message: "memory frontmatter status is invalid"})
	}
	if !allowedValue(fm.Confidence, []string{"high", "medium", "low"}) {
		issues = append(issues, LintIssue{Path: path, Code: "invalid_confidence", Message: "memory frontmatter confidence must be high, medium, or low"})
	}
	if len(cleanList(fm.Sources)) == 0 {
		issues = append(issues, LintIssue{Path: path, Code: "missing_sources", Message: "durable memory page should include sources"})
	}
	return issues
}

func allowedValue(value string, allowed []string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return false
	}
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}
