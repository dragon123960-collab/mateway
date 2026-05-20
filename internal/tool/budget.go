package tool

import "strings"

const DefaultOutputLimit = 6000

func Truncate(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 {
		limit = DefaultOutputLimit
	}
	if len(text) <= limit {
		return text
	}
	edge := limit / 2
	if limit >= 1200 && edge < 500 {
		edge = 500
	}
	if edge < 1 {
		edge = 1
	}
	if edge*2 > len(text) {
		edge = len(text) / 3
		if edge < 1 {
			edge = 1
		}
	}
	return text[:edge] + "\n...[truncated]...\n" + text[len(text)-edge:]
}
