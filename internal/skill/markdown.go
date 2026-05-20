package skill

import (
	"strconv"
	"strings"
)

type Metadata struct {
	Name             string
	Description      string
	Tags             []string
	Priority         int
	Stage            string
	Scope            string
	WhenContains     []string
	WhenResultKinds  []string
	WhenUserLanguage string
}

func parseSkillMarkdown(raw string) (Metadata, string) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	if !strings.HasPrefix(raw, "---\n") {
		return Metadata{}, strings.TrimSpace(raw)
	}
	rest := strings.TrimPrefix(raw, "---\n")
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return Metadata{}, strings.TrimSpace(raw)
	}
	header := rest[:end]
	body := strings.TrimSpace(rest[end+len("\n---\n"):])
	meta := Metadata{}
	for _, line := range strings.Split(header, "\n") {
		line = strings.TrimSpace(line)
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch key {
		case "name":
			meta.Name = value
		case "description":
			meta.Description = value
		case "tags":
			meta.Tags = parseTags(value)
		case "priority":
			if n, err := strconv.Atoi(value); err == nil {
				meta.Priority = n
			}
		case "stage":
			meta.Stage = value
		case "scope":
			meta.Scope = value
		case "when_contains":
			meta.WhenContains = parseTags(value)
		case "when_result_kinds":
			meta.WhenResultKinds = parseTags(value)
		case "when_user_language":
			meta.WhenUserLanguage = value
		}
	}
	return meta, body
}

func parseTags(value string) []string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "[")
	value = strings.TrimSuffix(value, "]")
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.Trim(strings.TrimSpace(part), `"'`)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
