package channels

import "context"

type Request struct {
	SessionKey string
	ThreadID   string
	UserID     string
	Channel    string
	Text       string
}

type Response struct {
	Text   string
	Status string
}

type Adapter interface {
	Name() string
	Start(ctx context.Context) error
}
