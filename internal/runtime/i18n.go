package runtime

import (
	"strings"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/i18n"
)

func runtimeLocale(cfg *config.Root, msg channel.InboundMessage) string {
	if cfg == nil {
		return i18n.ResolveLocale("", msg.Text)
	}
	return i18n.ResolveLocale(cfg.App.Locale, msg.Text)
}

func runtimeText(cfg *config.Root, msg channel.InboundMessage, key string, values map[string]string) string {
	if cfg == nil {
		return i18n.New(i18n.Config{}).T(runtimeLocale(cfg, msg), key, values)
	}
	return i18n.New(i18n.Config{CatalogDir: cfg.App.MessageCatalogDir}).T(runtimeLocale(cfg, msg), key, values)
}

func runtimeAlias(cfg *config.Root, msg channel.InboundMessage, text string, actions ...string) (string, bool) {
	if cfg == nil {
		return i18n.New(i18n.Config{}).MatchAlias(runtimeLocale(cfg, msg), text, actions...)
	}
	return i18n.New(i18n.Config{CatalogDir: cfg.App.MessageCatalogDir}).MatchAlias(runtimeLocale(cfg, msg), text, actions...)
}

func textValues(pairs ...string) map[string]string {
	out := map[string]string{}
	for i := 0; i+1 < len(pairs); i += 2 {
		out[pairs[i]] = pairs[i+1]
	}
	return out
}

func hasEnglishLocale(cfg *config.Root) bool {
	if cfg == nil {
		return false
	}
	return strings.EqualFold(i18n.ResolveLocale(cfg.App.Locale, ""), i18n.LocaleEN)
}
