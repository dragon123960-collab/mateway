package memory

import (
	"net/url"
	"strconv"
	"strings"
)

type SourceEvidence struct {
	Raw      string `json:"raw"`
	Kind     string `json:"kind"`
	Target   string `json:"target,omitempty"`
	Line     int    `json:"line,omitempty"`
	EndLine  int    `json:"end_line,omitempty"`
	HasLines bool   `json:"has_lines,omitempty"`
}

func ParseSources(sources []string) []SourceEvidence {
	var out []SourceEvidence
	for _, source := range cleanList(sources) {
		out = append(out, ParseSource(source))
	}
	return out
}

func ParseSource(source string) SourceEvidence {
	raw := strings.TrimSpace(source)
	lower := strings.ToLower(raw)
	switch {
	case raw == "":
		return SourceEvidence{}
	case lower == "manual":
		return SourceEvidence{Raw: raw, Kind: "manual"}
	case strings.HasPrefix(lower, "trace:"):
		return SourceEvidence{Raw: raw, Kind: "trace", Target: strings.TrimSpace(raw[len("trace:"):])}
	case strings.HasPrefix(lower, "task:"):
		return SourceEvidence{Raw: raw, Kind: "task", Target: strings.TrimSpace(raw[len("task:"):])}
	case strings.HasPrefix(lower, "file:"):
		target := strings.TrimSpace(raw[len("file:"):])
		path, line, endLine, hasLines := splitLineSuffix(target)
		return SourceEvidence{Raw: raw, Kind: "file", Target: path, Line: line, EndLine: endLine, HasLines: hasLines}
	case isURL(raw):
		return SourceEvidence{Raw: raw, Kind: "url", Target: raw}
	default:
		path, line, endLine, hasLines := splitLineSuffix(raw)
		if hasLines || strings.Contains(path, "/") || strings.HasSuffix(strings.ToLower(path), ".md") {
			return SourceEvidence{Raw: raw, Kind: "file", Target: path, Line: line, EndLine: endLine, HasLines: hasLines}
		}
		return SourceEvidence{Raw: raw, Kind: "unknown", Target: raw}
	}
}

func splitLineSuffix(value string) (string, int, int, bool) {
	value = strings.TrimSpace(value)
	idx := strings.LastIndex(value, ":")
	if idx <= 0 || idx == len(value)-1 {
		return value, 0, 0, false
	}
	suffix := strings.TrimSpace(value[idx+1:])
	start, end, ok := parseLineRange(suffix)
	if !ok {
		return value, 0, 0, false
	}
	return strings.TrimSpace(value[:idx]), start, end, true
}

func parseLineRange(value string) (int, int, bool) {
	if strings.Contains(value, "-") {
		left, right, ok := strings.Cut(value, "-")
		if !ok {
			return 0, 0, false
		}
		start, errStart := strconv.Atoi(strings.TrimSpace(left))
		end, errEnd := strconv.Atoi(strings.TrimSpace(right))
		if errStart != nil || errEnd != nil || start <= 0 || end < start {
			return 0, 0, false
		}
		return start, end, true
	}
	line, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || line <= 0 {
		return 0, 0, false
	}
	return line, line, true
}

func isURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	return parsed.Scheme == "http" || parsed.Scheme == "https"
}

func hasStrongSourceEvidence(sources []string) bool {
	for _, source := range ParseSources(sources) {
		switch source.Kind {
		case "url":
			return true
		case "file":
			if source.HasLines {
				return true
			}
		}
	}
	return false
}
