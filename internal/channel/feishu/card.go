package feishu

import (
	"encoding/json"
	"strings"

	"github.com/dongping/mateway/internal/channel"
)

func renderReplyMessage(reply channel.OutboundMessage) (string, string, error) {
	text := strings.TrimSpace(reply.Text)
	if text == "" {
		text = "暂无内容。"
	}
	content, err := json.Marshal(map[string]string{"text": text})
	return "text", string(content), err
}
