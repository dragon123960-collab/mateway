package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/larksuite/oapi-sdk-go/v3/ws"
)

type Receiver func(context.Context, channel.InboundMessage) error

func StartWebSocket(ctx context.Context, cfg config.FeishuConfig, receiver Receiver) error {
	cfg = cfg.ResolveSecrets()
	if !cfg.Enabled {
		return fmt.Errorf("feishu channel is disabled")
	}
	if !cfg.WebSocket.Enabled {
		return fmt.Errorf("feishu websocket is disabled")
	}
	if strings.TrimSpace(cfg.AppID) == "" || strings.TrimSpace(cfg.AppSecret) == "" {
		return fmt.Errorf("feishu app_id/app_secret are required")
	}
	d := dispatcher.NewEventDispatcher(cfg.VerificationToken, cfg.EncryptKey).
		OnP2MessageReceiveV1(func(eventCtx context.Context, event *larkim.P2MessageReceiveV1) error {
			msg := NormalizeMessageReceive(event)
			return receiver(eventCtx, msg)
		}).
		OnP2MessageReadV1(func(eventCtx context.Context, event *larkim.P2MessageReadV1) error {
			return nil
		}).
		OnP2ChatAccessEventBotP2pChatEnteredV1(func(eventCtx context.Context, event *larkim.P2ChatAccessEventBotP2pChatEnteredV1) error {
			return nil
		})
	client := ws.NewClient(cfg.AppID, cfg.AppSecret, ws.WithEventHandler(d))
	return client.Start(ctx)
}

func NormalizeMessageReceive(event *larkim.P2MessageReceiveV1) channel.InboundMessage {
	msg := channel.InboundMessage{Channel: "feishu", Metadata: map[string]string{}}
	if event == nil || event.Event == nil || event.Event.Message == nil {
		return msg
	}
	message := event.Event.Message
	msg.ID = value(message.MessageId)
	msg.ThreadID = firstNonEmpty(value(message.ThreadId), value(message.ChatId), value(message.MessageId))
	msg.Text = extractText(value(message.Content))
	msg.Metadata["chat_type"] = value(message.ChatType)
	msg.Metadata["message_type"] = value(message.MessageType)
	if event.Event.Sender != nil {
		msg.Metadata["sender_type"] = value(event.Event.Sender.SenderType)
	}
	if len(message.Mentions) > 0 {
		msg.Metadata["is_mentioned"] = "true"
	} else {
		msg.Metadata["is_mentioned"] = "false"
	}
	if event.Event.Sender != nil && event.Event.Sender.SenderId != nil {
		msg.UserID = firstNonEmpty(value(event.Event.Sender.SenderId.OpenId), value(event.Event.Sender.SenderId.UserId), value(event.Event.Sender.SenderId.UnionId))
	}
	return msg
}

func extractText(content string) string {
	content = strings.TrimSpace(content)
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(content), &payload); err == nil && payload.Text != "" {
		return strings.TrimSpace(payload.Text)
	}
	return content
}

func value(ptr *string) string {
	if ptr == nil {
		return ""
	}
	return *ptr
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
