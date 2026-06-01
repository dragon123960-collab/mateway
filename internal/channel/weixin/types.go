package weixin

import (
	"fmt"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/channel"
)

type GetUpdatesRequest struct {
	GetUpdatesBuf string `json:"get_updates_buf"`
}

type GetUpdatesResponse struct {
	Ret                  int       `json:"ret"`
	ErrCode              int       `json:"errcode,omitempty"`
	ErrMsg               string    `json:"errmsg,omitempty"`
	Msgs                 []Message `json:"msgs"`
	GetUpdatesBuf        string    `json:"get_updates_buf"`
	LongPollingTimeoutMS int       `json:"longpolling_timeout_ms,omitempty"`
}

type SendMessageRequest struct {
	Msg Message `json:"msg"`
}

type SendMessageResponse struct {
	Ret     int    `json:"ret"`
	ErrCode int    `json:"errcode,omitempty"`
	ErrMsg  string `json:"errmsg,omitempty"`
}

type Message struct {
	Seq          int64  `json:"seq,omitempty"`
	MessageID    int64  `json:"message_id,omitempty"`
	FromUserID   string `json:"from_user_id,omitempty"`
	ToUserID     string `json:"to_user_id,omitempty"`
	CreateTimeMS int64  `json:"create_time_ms,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	MessageType  int    `json:"message_type,omitempty"`
	MessageState int    `json:"message_state,omitempty"`
	ItemList     []Item `json:"item_list,omitempty"`
	ContextToken string `json:"context_token,omitempty"`
}

type Item struct {
	Type     int       `json:"type"`
	TextItem *TextItem `json:"text_item,omitempty"`
}

type TextItem struct {
	Text string `json:"text"`
}

type LoginStartResponse struct {
	Ret              int    `json:"ret"`
	ErrCode          int    `json:"errcode,omitempty"`
	ErrMsg           string `json:"errmsg,omitempty"`
	AccountID        string `json:"account_id,omitempty"`
	Token            string `json:"token,omitempty"`
	BaseURL          string `json:"base_url,omitempty"`
	QRCodeURL        string `json:"qrcode_url,omitempty"`
	QRCodeImgContent string `json:"qrcode_img_content,omitempty"`
	QRCode           string `json:"qrcode,omitempty"`
}

type Account struct {
	AccountID string `json:"account_id"`
	Token     string `json:"token"`
	BaseURL   string `json:"base_url"`
	CreatedAt string `json:"created_at"`
}

func (m Message) ToInbound(accountID string) (channel.InboundMessage, bool) {
	text, ok := textFromItems(m.ItemList)
	if !ok {
		return channel.InboundMessage{}, false
	}
	id := messageID(m)
	peerID := firstNonEmpty(m.FromUserID, m.SessionID, m.ToUserID, id)
	msg := channel.InboundMessage{
		ID:       id,
		Channel:  "weixin",
		ThreadID: peerID,
		UserID:   firstNonEmpty(m.FromUserID, peerID),
		Text:     text,
		Metadata: map[string]string{
			"account_id":    firstNonEmpty(accountID, m.ToUserID),
			"peer_id":       peerID,
			"session_id":    m.SessionID,
			"context_token": m.ContextToken,
			"message_type":  intString(m.MessageType),
			"message_state": intString(m.MessageState),
		},
	}
	msg.SessionKey = "weixin:" + firstNonEmpty(accountID, m.ToUserID, "default") + ":" + peerID
	return msg, true
}

func ReplyToMessage(original channel.InboundMessage, reply channel.OutboundMessage) Message {
	return Message{
		ToUserID:     firstNonEmpty(original.Metadata["peer_id"], original.UserID, original.ThreadID),
		ContextToken: original.Metadata["context_token"],
		CreateTimeMS: time.Now().UnixMilli(),
		ItemList: []Item{{
			Type:     1,
			TextItem: &TextItem{Text: reply.Text},
		}},
	}
}

func textFromItems(items []Item) (string, bool) {
	for _, item := range items {
		if item.Type == 1 && item.TextItem != nil && strings.TrimSpace(item.TextItem.Text) != "" {
			return strings.TrimSpace(item.TextItem.Text), true
		}
	}
	return "", false
}

func messageID(m Message) string {
	if m.MessageID != 0 {
		return fmt.Sprintf("%d", m.MessageID)
	}
	if m.Seq != 0 {
		return fmt.Sprintf("seq-%d", m.Seq)
	}
	return fmt.Sprintf("weixin-%d", time.Now().UnixNano())
}

func intString(value int) string {
	if value == 0 {
		return ""
	}
	return fmt.Sprintf("%d", value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
