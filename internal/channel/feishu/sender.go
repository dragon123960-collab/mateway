package feishu

import (
	"context"
	"fmt"
	"strings"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
)

type Sender struct {
	client *lark.Client
}

func NewSender(cfg config.FeishuConfig) *Sender {
	cfg = cfg.ResolveSecrets()
	options := []lark.ClientOptionFunc{}
	if strings.TrimSpace(cfg.BaseURL) != "" {
		options = append(options, lark.WithOpenBaseUrl(cfg.BaseURL))
	}
	return &Sender{client: lark.NewClient(cfg.AppID, cfg.AppSecret, options...)}
}

func (s *Sender) Reply(ctx context.Context, original channel.InboundMessage, reply channel.OutboundMessage) error {
	_, err := s.ReplyWithID(ctx, original, reply, "")
	return err
}

func (s *Sender) Send(ctx context.Context, target channel.OutboundMessage) error {
	if strings.TrimSpace(target.ThreadID) == "" {
		return fmt.Errorf("feishu target thread id is required")
	}
	msgType, content, err := renderReplyMessage(target)
	if err != nil {
		return err
	}
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.ReceiveIdTypeChatId).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(target.ThreadID).
			MsgType(msgType).
			Content(content).
			Build()).
		Build()
	resp, err := s.client.Im.Message.Create(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("feishu send failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

func (s *Sender) ReplyWithID(ctx context.Context, original channel.InboundMessage, reply channel.OutboundMessage, uuid string) (string, error) {
	if strings.TrimSpace(original.ID) == "" {
		return "", fmt.Errorf("feishu message id is required")
	}
	msgType, content, err := renderReplyMessage(reply)
	if err != nil {
		return "", err
	}
	body := larkim.NewReplyMessageReqBodyBuilder().
		MsgType(msgType).
		Content(content).
		ReplyInThread(false)
	if strings.TrimSpace(uuid) != "" {
		body.Uuid(uuid)
	}
	req := larkim.NewReplyMessageReqBuilder().
		MessageId(original.ID).
		Body(body.Build()).
		Build()
	resp, err := s.client.Im.Message.Reply(ctx, req)
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("feishu reply failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.MessageId == nil {
		return "", nil
	}
	return *resp.Data.MessageId, nil
}

func (s *Sender) Update(ctx context.Context, messageID string, reply channel.OutboundMessage) error {
	if strings.TrimSpace(messageID) == "" {
		return fmt.Errorf("feishu message id is required")
	}
	msgType, content, err := renderReplyMessage(reply)
	if err != nil {
		return err
	}
	req := larkim.NewUpdateMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewUpdateMessageReqBodyBuilder().MsgType(msgType).Content(content).Build()).
		Build()
	resp, err := s.client.Im.Message.Update(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("feishu update failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

func (s *Sender) React(ctx context.Context, messageID string, emojiType string) error {
	if strings.TrimSpace(messageID) == "" || strings.TrimSpace(emojiType) == "" {
		return nil
	}
	req := larkim.NewCreateMessageReactionReqBuilder().
		MessageId(messageID).
		Body(larkim.NewCreateMessageReactionReqBodyBuilder().
			ReactionType(larkim.NewEmojiBuilder().EmojiType(emojiType).Build()).
			Build()).
		Build()
	resp, err := s.client.Im.MessageReaction.Create(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("feishu reaction failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}
