package cli

import (
	"strings"

	"github.com/charmbracelet/glamour"
)

const maxTUIMarkdownRenderBytes = 24000

func renderMarkdownForTUI(text string, width int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if len(text) > maxTUIMarkdownRenderBytes {
		return text
	}
	if width < 40 {
		width = 40
	}
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return text
	}
	rendered, err := renderer.Render(text)
	if err != nil {
		return text
	}
	rendered = strings.TrimSpace(rendered)
	if rendered == "" {
		return text
	}
	return rendered
}
