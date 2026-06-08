package util

import (
	"strings"

	"github.com/dongping/mateway/internal/config"
)

func FirstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func BoolArg(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true") || strings.EqualFold(strings.TrimSpace(v), "yes")
	default:
		return false
	}
}

func ConfigHome(cfg *config.Root) string {
	if cfg != nil && strings.TrimSpace(cfg.App.Home) != "" {
		return strings.TrimSpace(cfg.App.Home)
	}
	return config.DefaultHome()
}
