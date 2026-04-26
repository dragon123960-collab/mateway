package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Message struct {
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

type Store struct {
	root string
	mu   sync.Mutex
}

func NewStore(workspace string) *Store {
	return &Store{root: filepath.Join(workspace, "memory", "sessions")}
}

func (s *Store) Append(sessionKey string, messages ...Message) error {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" || len(messages) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}
	path := filepath.Join(s.root, sanitize(sessionKey)+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open session file: %w", err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for _, msg := range messages {
		if strings.TrimSpace(msg.Role) == "" || strings.TrimSpace(msg.Content) == "" {
			continue
		}
		if msg.Timestamp.IsZero() {
			msg.Timestamp = time.Now()
		}
		if err := enc.Encode(msg); err != nil {
			return fmt.Errorf("append session message: %w", err)
		}
	}
	return nil
}

func (s *Store) LoadRecent(sessionKey string, limit int) ([]Message, error) {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" || limit <= 0 {
		return nil, nil
	}
	path := filepath.Join(s.root, sanitize(sessionKey)+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open session file: %w", err)
	}
	defer f.Close()

	var items []Message
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var msg Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		if strings.TrimSpace(msg.Role) == "" || strings.TrimSpace(msg.Content) == "" {
			continue
		}
		items = append(items, msg)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan session file: %w", err)
	}
	if len(items) <= limit {
		return items, nil
	}
	return append([]Message(nil), items[len(items)-limit:]...), nil
}

var unsafeSessionChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func sanitize(sessionKey string) string {
	cleaned := unsafeSessionChars.ReplaceAllString(sessionKey, "_")
	cleaned = strings.Trim(cleaned, "._-")
	if cleaned == "" {
		return "session"
	}
	return cleaned
}
