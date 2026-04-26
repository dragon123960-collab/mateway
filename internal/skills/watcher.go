package skills

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

type Watcher struct {
	catalog *Catalog
	roots   []string
	lastMu  sync.RWMutex
	lastErr string
}

func NewWatcher(catalog *Catalog, roots []string) *Watcher {
	return &Watcher{catalog: catalog, roots: roots}
}

func (w *Watcher) Start(ctx context.Context) error {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}

	for _, root := range w.roots {
		_ = os.MkdirAll(root, 0o755)
		if err := fsw.Add(root); err != nil {
			fsw.Close()
			return err
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				_ = fsw.Add(filepath.Join(root, entry.Name()))
			}
		}
	}

	go func() {
		defer fsw.Close()
		var debounce <-chan time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case err := <-fsw.Errors:
				if err != nil {
					w.setLastErr(err.Error())
				}
			case event := <-fsw.Events:
				if event.Name != "" && event.Has(fsnotify.Create) {
					if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
						_ = fsw.Add(event.Name)
					}
				}
				debounce = time.After(150 * time.Millisecond)
			case <-debounce:
				if err := w.catalog.Refresh(); err != nil {
					w.setLastErr(err.Error())
				} else {
					w.setLastErr("")
				}
				debounce = nil
			}
		}
	}()

	return nil
}

func (w *Watcher) LastErr() string {
	w.lastMu.RLock()
	defer w.lastMu.RUnlock()
	return w.lastErr
}

func (w *Watcher) setLastErr(value string) {
	w.lastMu.Lock()
	defer w.lastMu.Unlock()
	w.lastErr = value
}
