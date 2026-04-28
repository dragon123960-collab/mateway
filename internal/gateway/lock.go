package gateway

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

var ErrGatewayAlreadyRunning = errors.New("gateway already running")

type gatewayLock struct {
	file *os.File
	path string
}

func acquireGatewayLock(path string) (*gatewayLock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, fmt.Errorf("%w: %s", ErrGatewayAlreadyRunning, path)
		}
		return nil, err
	}
	if err := file.Truncate(0); err == nil {
		if _, seekErr := file.Seek(0, 0); seekErr == nil {
			_, _ = file.WriteString(strconv.Itoa(os.Getpid()))
		}
	}
	return &gatewayLock{file: file, path: path}, nil
}

func (l *gatewayLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	err := l.file.Close()
	_ = os.Remove(l.path)
	return err
}
