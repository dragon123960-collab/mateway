package observe

import (
	"strings"
	"sync"
	"time"
)

type Event struct {
	Type       string         `json:"type"`
	Time       string         `json:"time"`
	TraceID    string         `json:"trace_id,omitempty"`
	SessionKey string         `json:"session_key,omitempty"`
	TaskID     string         `json:"task_id,omitempty"`
	Payload    map[string]any `json:"payload,omitempty"`
}

type Hub struct {
	mu          sync.Mutex
	subscribers map[int]subscription
	nextID      int
}

type subscription struct {
	sessionKey string
	ch         chan Event
}

func NewHub() *Hub {
	return &Hub{subscribers: map[int]subscription{}}
}

var DefaultHub = NewHub()

func Publish(event Event) {
	DefaultHub.Publish(event)
}

func Subscribe(sessionKey string) (int, <-chan Event) {
	return DefaultHub.Subscribe(sessionKey)
}

func Unsubscribe(id int) {
	DefaultHub.Unsubscribe(id)
}

func (h *Hub) Publish(event Event) {
	if h == nil {
		return
	}
	event.Type = strings.TrimSpace(event.Type)
	if event.Type == "" {
		return
	}
	if strings.TrimSpace(event.Time) == "" {
		event.Time = time.Now().Format(time.RFC3339Nano)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, sub := range h.subscribers {
		if sub.sessionKey != "" && event.SessionKey != sub.sessionKey {
			continue
		}
		select {
		case sub.ch <- event:
		default:
		}
	}
}

func (h *Hub) Subscribe(sessionKey string) (int, <-chan Event) {
	if h == nil {
		ch := make(chan Event)
		close(ch)
		return 0, ch
	}
	ch := make(chan Event, 128)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextID++
	id := h.nextID
	h.subscribers[id] = subscription{sessionKey: strings.TrimSpace(sessionKey), ch: ch}
	return id, ch
}

func (h *Hub) Unsubscribe(id int) {
	if h == nil || id == 0 {
		return
	}
	h.mu.Lock()
	sub, ok := h.subscribers[id]
	if ok {
		delete(h.subscribers, id)
	}
	h.mu.Unlock()
	if ok {
		close(sub.ch)
	}
}
