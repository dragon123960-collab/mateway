package textmatch

import (
	_ "embed"
	"strings"
	"sync"
)

//go:embed zh_lexicon.txt
var lexiconData string

var (
	lexiconOnce sync.Once
	lexiconMap  map[string][]string
)

func Terms(group string) []string {
	lexiconOnce.Do(loadLexicon)
	items := lexiconMap[strings.TrimSpace(group)]
	out := make([]string, len(items))
	copy(out, items)
	return out
}

func ContainsAny(text string, terms ...string) bool {
	lower := strings.ToLower(text)
	for _, term := range terms {
		if strings.Contains(lower, strings.ToLower(term)) {
			return true
		}
	}
	return false
}

func ContainsGroup(text, group string) bool {
	return ContainsAny(text, Terms(group)...)
}

func ExactGroup(text, group string) bool {
	text = strings.TrimSpace(text)
	for _, term := range Terms(group) {
		if text == term {
			return true
		}
	}
	return false
}

func StopSet(groups ...string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, group := range groups {
		for _, term := range Terms(group) {
			out[strings.ToLower(strings.TrimSpace(term))] = struct{}{}
		}
	}
	return out
}

func loadLexicon() {
	lexiconMap = map[string][]string{}
	for _, line := range strings.Split(lexiconData, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, raw, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		for _, item := range strings.Split(raw, "|") {
			item = strings.TrimSpace(item)
			if item != "" {
				lexiconMap[key] = append(lexiconMap[key], item)
			}
		}
	}
}
