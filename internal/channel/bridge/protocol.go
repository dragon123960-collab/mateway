package bridge

import (
	"strings"
	"time"

	"github.com/dongping/mateway/internal/channel"
)

type Event struct {
	ID          string            `json:"id"`
	Channel     string            `json:"channel"`
	AccountID   string            `json:"account_id"`
	PeerID      string            `json:"peer_id"`
	ThreadID    string            `json:"thread_id"`
	UserID      string            `json:"user_id"`
	ChatType    string            `json:"chat_type"`
	Text        string            `json:"text"`
	Metadata    map[string]string `json:"metadata"`
	CreatedAt   string            `json:"created_at"`
	Attachments []Attachment      `json:"attachments,omitempty"`
}

type Attachment struct {
	Type string `json:"type"`
	URL  string `json:"url,omitempty"`
	Name string `json:"name,omitempty"`
}

type Reply struct {
	ID        string            `json:"id"`
	InReplyTo string            `json:"in_reply_to"`
	Channel   string            `json:"channel"`
	PeerID    string            `json:"peer_id"`
	ThreadID  string            `json:"thread_id"`
	Text      string            `json:"text"`
	Style     string            `json:"style"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

type Ack struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

func (e Event) ToInbound() channel.InboundMessage {
	metadata := map[string]string{}
	for key, value := range e.Metadata {
		metadata[key] = value
	}
	setIfNotEmpty(metadata, "account_id", e.AccountID)
	setIfNotEmpty(metadata, "peer_id", e.PeerID)
	setIfNotEmpty(metadata, "chat_type", e.ChatType)
	setIfNotEmpty(metadata, "created_at", e.CreatedAt)
	if len(e.Attachments) > 0 {
		metadata["attachments_unsupported"] = "true"
	}
	msg := channel.InboundMessage{
		ID:       strings.TrimSpace(e.ID),
		Channel:  strings.TrimSpace(e.Channel),
		UserID:   strings.TrimSpace(e.UserID),
		ThreadID: strings.TrimSpace(e.ThreadID),
		Text:     strings.TrimSpace(e.Text),
		Metadata: metadata,
	}
	if msg.ThreadID == "" {
		msg.ThreadID = strings.TrimSpace(e.PeerID)
	}
	if msg.UserID == "" {
		msg.UserID = strings.TrimSpace(e.PeerID)
	}
	msg.SessionKey = SessionKey(e)
	return msg
}

func ReplyFromOutbound(original Event, outbound channel.OutboundMessage) Reply {
	return Reply{
		ID:        "reply-" + strings.TrimSpace(original.ID) + "-" + time.Now().UTC().Format("20060102150405.000000000"),
		InReplyTo: strings.TrimSpace(original.ID),
		Channel:   firstNonEmpty(outbound.Channel, original.Channel),
		PeerID:    strings.TrimSpace(original.PeerID),
		ThreadID:  firstNonEmpty(outbound.ThreadID, original.ThreadID, original.PeerID),
		Text:      outbound.Text,
		Style:     outbound.Style,
		Metadata: map[string]string{
			"locale": outbound.Locale,
			"title":  outbound.Title,
		},
	}
}

func SessionKey(e Event) string {
	channelName := firstNonEmpty(e.Channel, "bridge")
	accountID := strings.TrimSpace(e.AccountID)
	peerID := firstNonEmpty(e.PeerID, e.ThreadID, e.UserID, e.ID, "default")
	if accountID != "" {
		return channelName + ":" + accountID + ":" + peerID
	}
	return channelName + ":" + peerID
}

func setIfNotEmpty(values map[string]string, key, value string) {
	if strings.TrimSpace(value) != "" {
		values[key] = strings.TrimSpace(value)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
