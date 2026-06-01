package channel

type InboundMessage struct {
	ID         string
	Channel    string
	SessionKey string
	UserID     string
	ThreadID   string
	Text       string
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
