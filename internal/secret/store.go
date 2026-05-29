package secret

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Store struct {
	Home string
}

type Entry struct {
	ID        string `json:"id"`
	Value     string `json:"value"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func (s Store) Set(id, value string) error {
	id = normalizeID(id)
	if id == "" {
		return fmt.Errorf("secret id is required")
	}
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("secret value is required")
	}
	data, err := s.load()
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	entry := data[id]
	if entry.ID == "" {
		entry = Entry{ID: id, CreatedAt: now}
	}
	entry.Value = value
	entry.UpdatedAt = now
	data[id] = entry
	return s.save(data)
}

func (s Store) Get(id string) (Entry, bool, error) {
	data, err := s.load()
	if err != nil {
		return Entry{}, false, err
	}
	entry, ok := data[normalizeID(id)]
	return entry, ok, nil
}

func (s Store) List() ([]Entry, error) {
	data, err := s.load()
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(data))
	for _, entry := range data {
		entry.Value = ""
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s Store) Delete(id string) (bool, error) {
	data, err := s.load()
	if err != nil {
		return false, err
	}
	id = normalizeID(id)
	if _, ok := data[id]; !ok {
		return false, nil
	}
	delete(data, id)
	return true, s.save(data)
}

func (s Store) path() string {
	home := strings.TrimSpace(s.Home)
	if home == "" {
		home = ".mateway"
	}
	return filepath.Join(home, "secrets", "secrets.json")
}

func (s Store) load() (map[string]Entry, error) {
	path := s.path()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]Entry{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return map[string]Entry{}, nil
	}
	var out map[string]Entry
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse secret store %s: %w", path, err)
	}
	if out == nil {
		out = map[string]Entry{}
	}
	return out, nil
}

func (s Store) save(data map[string]Entry) error {
	path := s.path()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return os.WriteFile(path, out, 0o600)
}

func normalizeID(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	id = strings.ReplaceAll(id, " ", "_")
	return id
}
