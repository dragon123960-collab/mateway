package runtime

import (
	"fmt"
	"strings"
)

func truncateRunes(text string, limit int) (string, bool) {
	runes := []rune(text)
	if limit <= 0 || len(runes) <= limit {
		return text, false
	}
	return string(runes[:limit]), true
}

func truncateRunesWithSuffix(text string, limit int) (string, bool) {
	head, truncated := truncateRunes(text, limit)
	if !truncated {
		return text, false
	}
	return head + fmt.Sprintf("... (%d chars)", len([]rune(text))), true
}

func trimAndTruncateRunesWithSuffix(text string, limit int) string {
	text = strings.TrimSpace(text)
	out, _ := truncateRunesWithSuffix(text, limit)
	return out
}
