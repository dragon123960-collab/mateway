package textutil

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

func CleanBlock(value string, limit int) string {
	value = ansiPattern.ReplaceAllString(strings.ToValidUTF8(value, ""), "")
	value = strings.TrimSpace(cleanControls(value, true))
	if limit > 0 {
		value = safeRunePrefix(value, limit)
	}
	return strings.TrimRightFunc(value, func(r rune) bool {
		return r == unicode.ReplacementChar
	})
}

func CleanInline(value string, limit int) string {
	value = CleanBlock(value, 0)
	value = strings.TrimSpace(strings.ReplaceAll(value, "\n", " "))
	if limit > 0 {
		value = safeRunePrefix(value, limit)
	}
	return strings.TrimRightFunc(value, func(r rune) bool {
		return r == unicode.ReplacementChar
	})
}

func HumanizeRunError(value string) string {
	value = CleanBlock(value, 0)
	value = stripNodePath(value)
	const nodePrefix = "[NodeRunError] failed to invoke tool["
	if strings.HasPrefix(value, nodePrefix) {
		if idx := strings.Index(value, "]: "); idx >= 0 {
			value = value[idx+3:]
		}
	}
	if command, ok := extractMissingExecutable(value); ok {
		return fmt.Sprintf("工具运行失败：找不到可执行文件 `%s`。当前运行环境的 PATH 里没有它；可改用绝对路径、先检查 PATH，或把这个 CLI 正式接成 provider/skill。", command)
	}
	return value
}

func stripNodePath(value string) string {
	if idx := strings.Index(value, "\n------------------------"); idx >= 0 {
		return strings.TrimSpace(value[:idx])
	}
	return value
}

func extractMissingExecutable(value string) (string, bool) {
	const marker = `exec: "`
	idx := strings.Index(value, marker)
	if idx < 0 {
		return "", false
	}
	rest := value[idx+len(marker):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return "", false
	}
	command := strings.TrimSpace(rest[:end])
	if command == "" || !strings.Contains(value, "executable file not found in $PATH") {
		return "", false
	}
	return command, true
}

func cleanControls(value string, keepLineBreaks bool) string {
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		switch {
		case keepLineBreaks && (r == '\n' || r == '\t'):
			b.WriteRune(r)
		case unicode.IsControl(r):
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func safeRunePrefix(value string, limit int) string {
	if limit <= 0 {
		return value
	}
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}
