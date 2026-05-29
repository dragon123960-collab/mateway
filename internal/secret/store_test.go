package secret

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreSetListGetDelete(t *testing.T) {
	home := t.TempDir()
	store := Store{Home: home}
	if err := store.Set("mail.smtp_pass", "supersecret123"); err != nil {
		t.Fatal(err)
	}
	entry, ok, err := store.Get("mail.smtp_pass")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || entry.Value != "supersecret123" {
		t.Fatalf("entry=%#v ok=%v", entry, ok)
	}
	entries, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Value != "" {
		t.Fatalf("list should not expose values: %#v", entries)
	}
	ok, err = store.Delete("mail.smtp_pass")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected delete")
	}
}

func TestStoreWritesPrivateFile(t *testing.T) {
	home := t.TempDir()
	if err := (Store{Home: home}).Set("token", "abcdef123456"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(home, "secrets", "secrets.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
}
