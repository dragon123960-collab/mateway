package cli

import (
	"strings"
)

type SlashCommand struct {
	Name string
	Args []string
}

func ParseSlash(line string) (SlashCommand, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "/") || line == "/" {
		return SlashCommand{}, false
	}
	parts := strings.Fields(strings.TrimPrefix(line, "/"))
	if len(parts) == 0 {
		return SlashCommand{}, false
	}
	return SlashCommand{Name: strings.ToLower(parts[0]), Args: parts[1:]}, true
}
