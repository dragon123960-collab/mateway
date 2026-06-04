package channel

type PartType string

const (
	PartText  PartType = "text"
	PartImage PartType = "image"
	PartAudio PartType = "audio"
	PartVideo PartType = "video"
	PartFile  PartType = "file"
)

type InboundMessage struct {
	ID         string
	Channel    string
	SessionKey string
	UserID     string
	ThreadID   string
	Text       string
	Parts      []MessagePart
	Metadata   map[string]string
}

type OutboundMessage struct {
	Channel  string
	ThreadID string
	Text     string
	Title    string
	Style    string
	Locale   string
}

type OutboundBatch struct {
	Reply     OutboundMessage
	FollowUps []OutboundMessage
}

func (b OutboundBatch) Messages() []OutboundMessage {
	messages := make([]OutboundMessage, 0, 1+len(b.FollowUps))
	if b.Reply.Text != "" {
		messages = append(messages, b.Reply)
	}
	for _, msg := range b.FollowUps {
		if msg.Text == "" {
			continue
		}
		messages = append(messages, msg)
	}
	return messages
}

type MessagePart struct {
	Type     PartType          `json:"type"`
	Text     string            `json:"text,omitempty"`
	URI      string            `json:"uri,omitempty"`
	MimeType string            `json:"mime_type,omitempty"`
	Name     string            `json:"name,omitempty"`
	Size     int64             `json:"size,omitempty"`
	SHA256   string            `json:"sha256,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

func (m InboundMessage) HasContent() bool {
	if m.Text != "" {
		return true
	}
	return len(m.Parts) > 0
}
