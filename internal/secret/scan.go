package secret

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	secretKeyPattern = regexp.MustCompile(`(?i)\b(?:password|passwd|pwd|secret|token|api[_-]?key|authorization|auth[_-]?code|username|smtp[_-]?user|imap[_-]?user|pop3[_-]?user|smtp[_-]?pass|imap[_-]?pass|pop3[_-]?pass)\b\s*[:=]\s*["']?([^\s"',}#]+)`)
	bearerPattern    = regexp.MustCompile(`(?i)\bbearer\s+[a-z0-9._~+/=-]{12,}`)
)

type Finding struct {
	Kind string
	Line int
	Key  string
}

func ScanText(text string) []Finding {
	var findings []Finding
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lineNo := i + 1
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if match := secretKeyPattern.FindStringSubmatch(trimmed); len(match) > 0 && looksSensitiveValue(match[1]) {
			findings = append(findings, Finding{Kind: "secret_assignment", Line: lineNo, Key: assignmentKey(trimmed)})
			continue
		}
		if bearerPattern.MatchString(trimmed) {
			findings = append(findings, Finding{Kind: "bearer_token", Line: lineNo})
		}
	}
	return findings
}

func RejectIfSecretLike(text, target string) error {
	findings := ScanText(text)
	if len(findings) == 0 {
		return nil
	}
	first := findings[0]
	location := fmt.Sprintf("line %d", first.Line)
	if first.Key != "" {
		location += ", key " + first.Key
	}
	return fmt.Errorf("refusing to write secret-like content to %s (%s). Store credentials with `mateway secret set <id>` and reference them from the skill instead of writing plaintext", target, location)
}

func looksSensitiveValue(value string) bool {
	value = strings.Trim(strings.TrimSpace(value), `"'`)
	if value == "" {
		return false
	}
	lower := strings.ToLower(value)
	if lower == "true" || lower == "false" || lower == "none" || lower == "null" || lower == "todo" || lower == "changeme" || lower == "redacted" || strings.Contains(lower, "redacted") {
		return false
	}
	if strings.HasPrefix(value, "$") || strings.HasPrefix(value, "{{") || strings.HasPrefix(value, "<") {
		return false
	}
	return len(value) >= 4
}

func assignmentKey(line string) string {
	if key, _, ok := strings.Cut(line, ":"); ok {
		return strings.TrimSpace(key)
	}
	if key, _, ok := strings.Cut(line, "="); ok {
		return strings.TrimSpace(key)
	}
	return ""
}
