package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Preferences struct {
	AgentName   string    `json:"agent_name,omitempty"`
	LastResetAt time.Time `json:"last_reset_at,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (s *Store) SavePreferences(sessionKey string, prefs Preferences) error {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := filepath.Join(filepath.Dir(s.root), "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create session prefs dir: %w", err)
	}
	if existing, err := s.loadPreferencesUnlocked(sessionKey); err == nil {
		if prefs.LastResetAt.IsZero() {
			prefs.LastResetAt = existing.LastResetAt
		}
	}
	if prefs.UpdatedAt.IsZero() {
		prefs.UpdatedAt = time.Now()
	}
	data, err := json.MarshalIndent(prefs, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session prefs: %w", err)
	}
	path := s.preferencesFilePath(sessionKey)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write session prefs: %w", err)
	}
	return nil
}

func (s *Store) LoadPreferences(sessionKey string) (Preferences, error) {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return Preferences{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadPreferencesUnlocked(sessionKey)
}

func (s *Store) loadPreferencesUnlocked(sessionKey string) (Preferences, error) {
	path := s.preferencesFilePath(sessionKey)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Preferences{}, nil
		}
		return Preferences{}, fmt.Errorf("read session prefs: %w", err)
	}
	var prefs Preferences
	if err := json.Unmarshal(data, &prefs); err != nil {
		return Preferences{}, fmt.Errorf("decode session prefs: %w", err)
	}
	return prefs, nil
}
