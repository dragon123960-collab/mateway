package secret

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
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

func TestStoreConcurrentSetKeepsAllEntries(t *testing.T) {
	home := t.TempDir()
	store := Store{Home: home}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := "secret." + strconv.Itoa(i)
			if err := store.Set(id, "value-"+strconv.Itoa(i)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	entries, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 12 {
		t.Fatalf("entries = %d want 12: %#v", len(entries), entries)
	}
}
