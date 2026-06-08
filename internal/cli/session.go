package cli

import (
	"fmt"
	"hash/fnv"
	"path/filepath"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/session"
)

const DefaultSessionKey = "cli:default"

func ResolveSessionKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return DefaultSessionKey
	}
	if strings.Contains(value, ":") {
		return value
	}
	return "cli:" + value
}

func CwdSessionKey(cwd string) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return DefaultSessionKey
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(filepath.Clean(cwd)))
	return fmt.Sprintf("cli:cwd-%08x", h.Sum32())
}

func ForkSession(store session.Store, sourceKey, targetKey string) error {
	sourceKey = ResolveSessionKey(sourceKey)
	targetKey = ResolveSessionKey(targetKey)
	if sourceKey == targetKey {
		return nil
	}
	source, err := store.Load(sourceKey)
	if err != nil {
		return err
	}
	if strings.TrimSpace(source.Key) == "" || (len(source.Messages) == 0 && len(source.Tasks) == 0) {
		return fmt.Errorf("source session %q is empty", sourceKey)
	}
	target := session.State{
		Key:       targetKey,
		Messages:  append([]agentcore.Message(nil), source.Messages...),
		Tasks:     append([]session.TaskNode(nil), source.Tasks...),
		Usage:     source.Usage,
		UpdatedAt: time.Now(),
	}
	return store.Save(target)
}
