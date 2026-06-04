package runtime

import (
	"regexp"
	"strings"
)

func sanitizeResponse(text string) string {
	text = strings.TrimSpace(text)
	text = stripToolCallBlocks(text)
	text = stripNamedToolCallTags(text)
	text = stripDanglingToolCallBlock(text)
	return strings.TrimSpace(text)
}

func stripToolCallBlocks(text string) string {
	return toolCallSanitizerPattern.ReplaceAllString(text, "")
}

var toolCallSanitizerPattern = regexp.MustCompile(`(?is)\[\s*TOOL_CALL\s*\].*?\[\s*/\s*TOOL_CALL\s*\]`)

func stripNamedToolCallTags(text string) string {
	loc := namedToolCallPattern.FindStringIndex(text)
	if loc == nil {
		return text
	}
	return strings.TrimSpace(text[:loc[0]])
}

var namedToolCallPattern = regexp.MustCompile(`(?is)<\s*tool_call\s*>`)

func stripDanglingToolCallBlock(text string) string {
	loc := danglingToolCallPattern.FindStringIndex(text)
	if loc == nil {
		return text
	}
	return strings.TrimSpace(text[:loc[0]])
}

var danglingToolCallPattern = regexp.MustCompile(`(?is)\[\s*TOOL_CALL\s*\]`)
