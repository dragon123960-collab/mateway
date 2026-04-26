package skills

import (
	"sort"
	"strings"
)

func (r ResourceSet) AllowedDirs() []string {
	seen := map[string]bool{}
	out := make([]string, 0, 3+len(r.Extra))
	for _, item := range []string{"scripts", "references", "assets"} {
		if hasResourceEntries(item, r) {
			seen[item] = true
			out = append(out, item)
		}
	}
	if len(r.Extra) > 0 {
		keys := make([]string, 0, len(r.Extra))
		for key, values := range r.Extra {
			key = normalizeResourceDir(key)
			if key == "" || seen[key] || len(values) == 0 {
				continue
			}
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out = append(out, keys...)
	}
	return out
}

func hasResourceEntries(name string, r ResourceSet) bool {
	switch normalizeResourceDir(name) {
	case "scripts":
		return len(r.Scripts) > 0
	case "references":
		return len(r.References) > 0
	case "assets":
		return len(r.Assets) > 0
	default:
		return len(r.Extra[normalizeResourceDir(name)]) > 0
	}
}

func normalizeResourceDir(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.ReplaceAll(value, "\\", "/")
	value = strings.Trim(value, "/")
	if value == "" || value == "." || strings.Contains(value, "..") || strings.Contains(value, "/") {
		return ""
	}
	return value
}
