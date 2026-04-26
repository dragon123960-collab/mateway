package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/config"
)

var gatewayLaunchctlRunner = func(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "launchctl", args...)
	return cmd.CombinedOutput()
}

var gatewayHealthWaiter = waitGatewayHealthy
var gatewayExecutablePath = os.Executable
var gatewayUserHomeDir = os.UserHomeDir

func runGatewayCommand(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return runGatewayForeground(ctx, stdout)
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "start":
		return runGatewayForeground(ctx, stdout)
	case "health":
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		return printGatewayHealth(ctx, cfg, stdout)
	case "status":
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		return printGatewayStatus(ctx, cfg, stdout)
	case "restart":
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		if err := restartManagedGateway(ctx, cfg); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(stdout, "gateway restarted")
		return printGatewayHealth(ctx, cfg, stdout)
	default:
		return fmt.Errorf("unknown gateway subcommand: %s", args[0])
	}
}

func runGatewayForeground(ctx context.Context, stdout io.Writer) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	catalog, watcher, invoker, _, registry, runner := buildRuntime(cfg)
	_, _ = fmt.Fprintf(stdout, "gateway listening on %s:%d\n", cfg.Gateway.Host, cfg.Gateway.Port)
	if cfg.Channels.Feishu.Enabled {
		_, _ = fmt.Fprintf(stdout, "feishu websocket channel enabled\n")
	}
	return newGatewayService(ctx, cfg, catalog, watcher, invoker, runner, registry).Run(ctx)
}

func gatewayLaunchLabel(cfg config.Config) string {
	name := strings.TrimSpace(cfg.App.Name)
	if name == "" {
		name = "mateway"
	}
	return "com.dongping." + name + ".gateway"
}

func restartManagedGateway(ctx context.Context, cfg config.Config) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("gateway restart via launchctl is only supported on macOS")
	}
	target := fmt.Sprintf("gui/%d/%s", os.Getuid(), gatewayLaunchLabel(cfg))
	if err := ensureManagedGatewayInstalled(ctx, cfg); err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			time.Sleep(1200 * time.Millisecond)
		}
		out, err := gatewayLaunchctlRunner(ctx, "kickstart", "-k", target)
		if err != nil {
			lastErr = fmt.Errorf("launchctl restart failed: %s", strings.TrimSpace(string(out)))
			if strings.Contains(strings.ToLower(lastErr.Error()), "could not find service") {
				_ = bootstrapManagedGateway(ctx, cfg)
			}
			continue
		}
		if attempt == 0 {
			_, _ = gatewayLaunchctlRunner(ctx, "kickstart", "-k", target)
		}
		if err := gatewayHealthWaiter(ctx, cfg, 12*time.Second); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("gateway restart did not become healthy")
}

func ensureManagedGatewayInstalled(ctx context.Context, cfg config.Config) error {
	target := fmt.Sprintf("gui/%d/%s", os.Getuid(), gatewayLaunchLabel(cfg))
	if _, err := gatewayLaunchctlRunner(ctx, "print", target); err == nil {
		return nil
	}
	return bootstrapManagedGateway(ctx, cfg)
}

func bootstrapManagedGateway(ctx context.Context, cfg config.Config) error {
	plistPath, err := writeLaunchdPlist(cfg)
	if err != nil {
		return err
	}
	out, err := gatewayLaunchctlRunner(ctx, "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), plistPath)
	if err != nil && !strings.Contains(strings.ToLower(string(out)), "service already loaded") {
		return fmt.Errorf("launchctl bootstrap failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func writeLaunchdPlist(cfg config.Config) (string, error) {
	home, err := gatewayUserHomeDir()
	if err != nil {
		return "", err
	}
	exePath, err := gatewayExecutablePath()
	if err != nil {
		exePath = filepath.Join(cfg.App.Home, "bin", "mateway")
	}
	exePath = filepath.Clean(exePath)
	root := filepath.Dir(exePath)
	if strings.EqualFold(filepath.Base(root), "build") {
		root = filepath.Dir(root)
	}
	if err := os.MkdirAll(filepath.Join(home, "Library", "LaunchAgents"), 0o755); err != nil {
		return "", err
	}
	outLog, errLog, err := gatewayLogPaths(cfg)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(outLog), 0o755); err != nil {
		return "", err
	}
	label := gatewayLaunchLabel(cfg)
	plistPath := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	buf.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	buf.WriteString(`<plist version="1.0"><dict>` + "\n")
	buf.WriteString("  <key>Label</key>\n  <string>" + label + "</string>\n")
	buf.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	buf.WriteString("    <string>" + exePath + "</string>\n")
	buf.WriteString("    <string>gateway</string>\n")
	buf.WriteString("  </array>\n")
	buf.WriteString("  <key>WorkingDirectory</key>\n  <string>" + root + "</string>\n")
	buf.WriteString("  <key>RunAtLoad</key>\n  <true/>\n")
	buf.WriteString("  <key>KeepAlive</key>\n  <true/>\n")
	buf.WriteString("  <key>StandardOutPath</key>\n  <string>" + outLog + "</string>\n")
	buf.WriteString("  <key>StandardErrorPath</key>\n  <string>" + errLog + "</string>\n")
	buf.WriteString("</dict></plist>\n")
	if err := os.WriteFile(plistPath, buf.Bytes(), 0o644); err != nil {
		return "", err
	}
	return plistPath, nil
}

func printGatewayStatus(ctx context.Context, cfg config.Config, stdout io.Writer) error {
	path := filepath.Join(cfg.App.Home, "gateway_state.json")
	if data, err := os.ReadFile(path); err == nil {
		_, _ = fmt.Fprintln(stdout, "runtime_state:")
		_, _ = fmt.Fprintln(stdout, string(data))
	}
	if runtime.GOOS == "darwin" {
		target := fmt.Sprintf("gui/%d/%s", os.Getuid(), gatewayLaunchLabel(cfg))
		if out, err := gatewayLaunchctlRunner(ctx, "print", target); err == nil {
			_, _ = fmt.Fprintln(stdout, "launchctl_state:")
			_, _ = fmt.Fprintln(stdout, strings.TrimSpace(string(out)))
		}
	}
	return printGatewayHealth(ctx, cfg, stdout)
}

func printGatewayHealth(ctx context.Context, cfg config.Config, stdout io.Writer) error {
	url := fmt.Sprintf("http://%s:%d/health", cfg.Gateway.Host, cfg.Gateway.Port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		_, _ = fmt.Fprintf(stdout, "health: unreachable (%v)\n", err)
		return nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	text := strings.TrimSpace(string(body))
	if text == "" {
		text = resp.Status
	}
	_, _ = fmt.Fprintf(stdout, "health: %s\n", text)
	return nil
}

func waitGatewayHealthy(ctx context.Context, cfg config.Config, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		url := fmt.Sprintf("http://%s:%d/health", cfg.Gateway.Host, cfg.Gateway.Port)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err == nil {
			resp, err := (&http.Client{Timeout: 1200 * time.Millisecond}).Do(req)
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode < 500 {
					return nil
				}
			}
		}
		time.Sleep(700 * time.Millisecond)
	}
	return fmt.Errorf("gateway health check timed out")
}
