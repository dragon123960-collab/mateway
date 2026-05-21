package gateway

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dongping/mateway/internal/config"
)

type InstanceLock struct {
	file *os.File
	Path string
}

func lockPath(home string) (string, error) {
	if strings.TrimSpace(home) == "" {
		home = config.DefaultHome()
	}
	runDir := filepath.Join(home, "run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(runDir, "mateway.lock"), nil
}

func writeLockPID(file *os.File) error {
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	_, err := fmt.Fprintf(file, "pid=%d\n", os.Getpid())
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
