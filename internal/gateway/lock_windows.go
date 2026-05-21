//go:build windows

package gateway

import (
	"fmt"
	"os"
)

func AcquireInstanceLock(home string) (*InstanceLock, error) {
	path, err := lockPath(home)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0o644)
	if err != nil {
		return nil, fmt.Errorf("another mateway instance is already running, lock=%s: %w", path, err)
	}
	if err := writeLockPID(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &InstanceLock{file: file, Path: path}, nil
}

func (l *InstanceLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}
