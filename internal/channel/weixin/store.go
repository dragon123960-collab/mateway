package weixin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func defaultAccountDir(home string) string {
	return filepath.Join(home, "run", "weixin", "accounts")
}

func SaveAccount(dir string, account Account) error {
	if strings.TrimSpace(account.AccountID) == "" {
		return nil
	}
	if account.CreatedAt == "" {
		account.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, account.AccountID+".json")
	data, err := json.MarshalIndent(account, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func LoadAccount(dir, accountID string) (Account, error) {
	var account Account
	if strings.TrimSpace(accountID) == "" {
		return account, os.ErrNotExist
	}
	data, err := os.ReadFile(filepath.Join(dir, accountID+".json"))
	if err != nil {
		return account, err
	}
	err = json.Unmarshal(data, &account)
	return account, err
}

func loadSyncBuf(dir, accountID string) string {
	data, err := os.ReadFile(filepath.Join(dir, accountID+".sync.json"))
	if err != nil {
		return ""
	}
	var payload struct {
		SyncBuf string `json:"sync_buf"`
	}
	_ = json.Unmarshal(data, &payload)
	return payload.SyncBuf
}

func saveSyncBuf(dir, accountID, syncBuf string) {
	if strings.TrimSpace(accountID) == "" {
		return
	}
	_ = os.MkdirAll(dir, 0o700)
	data, _ := json.MarshalIndent(map[string]string{"sync_buf": syncBuf}, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, accountID+".sync.json"), data, 0o600)
}
