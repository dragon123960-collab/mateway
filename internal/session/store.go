package session

import (
	"bufio"
	"encoding/json"
	"errors"
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
	path := s.sessionFilePath(sessionKey)
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
	path := s.sessionFilePath(sessionKey)
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

func (s *Store) Reset(sessionKey string) error {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var errs []error
	if err := os.Remove(s.sessionFilePath(sessionKey)); err != nil && !os.IsNotExist(err) {
		errs = append(errs, err)
	}
	if err := os.MkdirAll(filepath.Dir(s.preferencesFilePath(sessionKey)), 0o755); err != nil {
		errs = append(errs, err)
	} else {
		state := Preferences{
			LastResetAt: time.Now(),
			UpdatedAt:   time.Now(),
		}
		data, err := json.MarshalIndent(state, "", "  ")
		if err != nil {
			errs = append(errs, err)
		} else if err := os.WriteFile(s.preferencesFilePath(sessionKey), data, 0o644); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}

func (s *Store) sessionFilePath(sessionKey string) string {
	return filepath.Join(s.root, sanitize(sessionKey)+".jsonl")
}

func (s *Store) preferencesFilePath(sessionKey string) string {
	return filepath.Join(filepath.Dir(s.root), "agents", sanitize(sessionKey)+".json")
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
