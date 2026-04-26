package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/config"
)

type gatewayLogTarget struct {
	Label string
	Path  string
}

func runLogsCommand(ctx context.Context, args []string, stdout io.Writer) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	targets, err := gatewayLogTargets(cfg)
	if err != nil {
		return err
	}
	mode := "show"
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		mode = strings.ToLower(strings.TrimSpace(args[0]))
	}
	switch mode {
	case "show":
		return showGatewayLogs(stdout, targets, 80)
	case "follow":
		return followGatewayLogs(ctx, stdout, targets, 40, 700*time.Millisecond)
	case "path":
		for _, target := range targets {
			_, _ = fmt.Fprintf(stdout, "%s: %s\n", target.Label, target.Path)
		}
		return nil
	default:
		return errors.New("usage: mateway logs [show|follow|path]")
	}
}

func gatewayLogTargets(cfg config.Config) ([]gatewayLogTarget, error) {
	outPath, errPath, err := gatewayLogPaths(cfg)
	if err != nil {
		return nil, err
	}
	return []gatewayLogTarget{
		{Label: "stdout", Path: outPath},
		{Label: "stderr", Path: errPath},
	}, nil
}

func showGatewayLogs(stdout io.Writer, targets []gatewayLogTarget, limit int) error {
	found := false
	for _, target := range targets {
		lines, err := readLastLines(target.Path, limit)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		if len(lines) == 0 {
			continue
		}
		found = true
		_, _ = fmt.Fprintf(stdout, "[%s] %s\n", target.Label, target.Path)
		for _, line := range lines {
			_, _ = fmt.Fprintln(stdout, line)
		}
		_, _ = fmt.Fprintln(stdout, "")
	}
	if found {
		return nil
	}
	_, _ = fmt.Fprintln(stdout, "No gateway log files found yet.")
	_, _ = fmt.Fprintln(stdout, "Hint: a managed macOS gateway writes logs after `mateway gateway restart` or launchd install.")
	return nil
}

func followGatewayLogs(ctx context.Context, stdout io.Writer, targets []gatewayLogTarget, initialLines int, interval time.Duration) error {
	type fileState struct {
		target gatewayLogTarget
		offset int64
	}
	states := make([]fileState, 0, len(targets))
	for _, target := range targets {
		if lines, err := readLastLines(target.Path, initialLines); err == nil && len(lines) > 0 {
			_, _ = fmt.Fprintf(stdout, "[%s] %s\n", target.Label, target.Path)
			for _, line := range lines {
				_, _ = fmt.Fprintf(stdout, "[%s] %s\n", target.Label, line)
			}
		}
		var size int64
		if info, err := os.Stat(target.Path); err == nil {
			size = info.Size()
		}
		states = append(states, fileState{target: target, offset: size})
	}
	if len(states) == 0 {
		return nil
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil
			}
			return ctx.Err()
		case <-ticker.C:
			for i := range states {
				nextOffset, lines, err := readNewLogLines(states[i].target.Path, states[i].offset)
				if err != nil {
					if errors.Is(err, os.ErrNotExist) {
						continue
					}
					return err
				}
				states[i].offset = nextOffset
				for _, line := range lines {
					_, _ = fmt.Fprintf(stdout, "[%s] %s\n", states[i].target.Label, line)
				}
			}
		}
	}
}

func readLastLines(path string, limit int) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		filtered = append(filtered, line)
	}
	if limit > 0 && len(filtered) > limit {
		return append([]string(nil), filtered[len(filtered)-limit:]...), nil
	}
	return filtered, nil
}

func readNewLogLines(path string, offset int64) (int64, []string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return offset, nil, err
	}
	if info.Size() < offset {
		offset = 0
	}
	f, err := os.Open(path)
	if err != nil {
		return offset, nil, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset, nil, err
	}
	scanner := bufio.NewScanner(f)
	lines := make([]string, 0, 8)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return offset, nil, err
	}
	return info.Size(), lines, nil
}

func gatewayLogPaths(cfg config.Config) (string, string, error) {
	if runtime.GOOS == "darwin" {
		home, err := gatewayUserHomeDir()
		if err != nil {
			return "", "", err
		}
		dir := filepath.Join(home, "Library", "Logs")
		return filepath.Join(dir, "mateway-gateway.out.log"), filepath.Join(dir, "mateway-gateway.err.log"), nil
	}
	dir := filepath.Join(cfg.App.Home, "logs")
	return filepath.Join(dir, "mateway-gateway.out.log"), filepath.Join(dir, "mateway-gateway.err.log"), nil
}
