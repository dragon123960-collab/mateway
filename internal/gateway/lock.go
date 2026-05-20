package gateway

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/dongping/mateway/internal/config"
)

type InstanceLock struct {
	file *os.File
	Path string
}

func AcquireInstanceLock(home string) (*InstanceLock, error) {
	if strings.TrimSpace(home) == "" {
		home = config.DefaultHome()
	}
	runDir := filepath.Join(home, "run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(runDir, "mateway.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("another mateway instance is already running, lock=%s: %w", path, err)
	}
	if err := file.Truncate(0); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, err
	}
	if _, err := file.Seek(0, 0); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, err
	}
	_, _ = fmt.Fprintf(file, "pid=%d\n", os.Getpid())
	return &InstanceLock{file: file, Path: path}, nil
}

func (l *InstanceLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	fd := int(l.file.Fd())
	_ = syscall.Flock(fd, syscall.LOCK_UN)
	err := l.file.Close()
	l.file = nil
	return err
}

func RunningPIDFromLock(home string) int {
	if strings.TrimSpace(home) == "" {
		home = config.DefaultHome()
	}
	data, err := os.ReadFile(filepath.Join(home, "run", "mateway.lock"))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || key != "pid" {
			continue
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(value))
		return pid
	}
	return 0
}
