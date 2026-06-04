package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/i18n"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
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
		OnP2CardActionTrigger(func(eventCtx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
			msg := NormalizeCardAction(event)
			if err := receiver(eventCtx, msg); err != nil {
				return nil, err
			}
			return &callback.CardActionTriggerResponse{
				Toast: &callback.Toast{
					Type:    "info",
					Content: i18n.New(i18n.Config{}).T(i18n.LocaleZH, "gateway.processing_ack", nil),
				},
			}, nil
		}).
		OnP2MessageReadV1(func(eventCtx context.Context, event *larkim.P2MessageReadV1) error {
			return nil
		}).
		OnP2MessageReactionCreatedV1(func(eventCtx context.Context, event *larkim.P2MessageReactionCreatedV1) error {
			return nil
		}).
		OnP2MessageReactionDeletedV1(func(eventCtx context.Context, event *larkim.P2MessageReactionDeletedV1) error {
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
	if imageKey := extractImageKey(value(message.Content)); imageKey != "" {
		msg.Metadata["image_key"] = imageKey
		msg.Parts = append(msg.Parts, channel.MessagePart{
			Type: channel.PartImage,
			Name: imageKey,
			Metadata: map[string]string{
				"channel":   "feishu",
				"image_key": imageKey,
			},
		})
	}
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

func NormalizeCardAction(event *callback.CardActionTriggerEvent) channel.InboundMessage {
	msg := channel.InboundMessage{Channel: "feishu", Metadata: map[string]string{}}
	if event == nil || event.Event == nil {
		return msg
	}
	if event.Event.Context != nil {
		msg.ID = strings.TrimSpace(event.Event.Context.OpenMessageID)
		msg.ThreadID = firstNonEmpty(event.Event.Context.OpenChatID, event.Event.Context.OpenMessageID)
	}
	msg.Text = extractCardActionText(event.Event.Action)
	msg.Metadata["message_type"] = "interactive"
	msg.Metadata["sender_type"] = "user"
	if decision := extractCardActionDecision(event.Event.Action); decision != "" {
		msg.Metadata["card_action"] = decision
	}
	if sessionKey := extractCardActionString(event.Event.Action, "mateway_session_key"); sessionKey != "" {
		msg.SessionKey = sessionKey
	}
	if threadID := extractCardActionString(event.Event.Action, "mateway_thread_id"); threadID != "" {
		msg.ThreadID = threadID
	}
	if event.Event.Operator != nil {
		msg.UserID = firstNonEmpty(event.Event.Operator.OpenID, value(event.Event.Operator.UserID))
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

func extractImageKey(content string) string {
	content = strings.TrimSpace(content)
	var payload struct {
		ImageKey string `json:"image_key"`
	}
	if err := json.Unmarshal([]byte(content), &payload); err == nil && payload.ImageKey != "" {
		return strings.TrimSpace(payload.ImageKey)
	}
	return ""
}

func value(ptr *string) string {
	if ptr == nil {
		return ""
	}
	return *ptr
}

func extractCardActionText(action *callback.CallBackAction) string {
	if action == nil {
		return ""
	}
	if text, _ := action.Value["mateway_text"].(string); strings.TrimSpace(text) != "" {
		return strings.TrimSpace(text)
	}
	switch extractCardActionDecision(action) {
	case "confirm":
		return i18n.New(i18n.Config{}).T(i18n.LocaleZH, "aliases.confirm.primary", nil)
	case "cancel":
		return i18n.New(i18n.Config{}).T(i18n.LocaleZH, "aliases.cancel.primary", nil)
	}
	return strings.TrimSpace(action.InputValue)
}

func extractCardActionDecision(action *callback.CallBackAction) string {
	if action == nil {
		return ""
	}
	return extractCardActionString(action, "decision")
}

func extractCardActionString(action *callback.CallBackAction, key string) string {
	if action == nil {
		return ""
	}
	if value, _ := action.Value[key].(string); strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
