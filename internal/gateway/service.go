package gateway

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

const (
	DefaultServiceName = "mateway.gateway"
	DarwinServiceLabel = "com.dongping.mateway.gateway"
	LinuxUserUnit      = "mateway-gateway.service"
)

type ServiceManager struct {
	GOOS string
}

func NewServiceManager() ServiceManager {
	return ServiceManager{GOOS: runtime.GOOS}
}

func (m ServiceManager) Start(ctx context.Context) error {
	switch m.GOOS {
	case "darwin":
		return runCommand(ctx, "launchctl", "kickstart", "-k", launchDomain()+"/"+DarwinServiceLabel)
	case "linux":
		return runCommand(ctx, "systemctl", "--user", "start", LinuxUserUnit)
	default:
		return unsupportedServiceOS(m.GOOS)
	}
}

func (m ServiceManager) Restart(ctx context.Context) error {
	switch m.GOOS {
	case "darwin":
		return runCommand(ctx, "launchctl", "kickstart", "-k", launchDomain()+"/"+DarwinServiceLabel)
	case "linux":
		return runCommand(ctx, "systemctl", "--user", "restart", LinuxUserUnit)
	default:
		return unsupportedServiceOS(m.GOOS)
	}
}

func (m ServiceManager) Stop(ctx context.Context) error {
	switch m.GOOS {
	case "darwin":
		return runCommand(ctx, "launchctl", "kill", "TERM", launchDomain()+"/"+DarwinServiceLabel)
	case "linux":
		return runCommand(ctx, "systemctl", "--user", "stop", LinuxUserUnit)
	default:
		return unsupportedServiceOS(m.GOOS)
	}
}

func (m ServiceManager) Status(ctx context.Context, home string) (string, error) {
	pid := RunningPIDFromLock(home)
	lockLine := "mateway serve lock: not held"
	if pid > 0 {
		lockLine = fmt.Sprintf("mateway serve lock: pid=%d", pid)
	}
	var serviceText string
	var err error
	switch m.GOOS {
	case "darwin":
		serviceText, err = commandOutput(ctx, "launchctl", "print", launchDomain()+"/"+DarwinServiceLabel)
	case "linux":
		serviceText, err = commandOutput(ctx, "systemctl", "--user", "status", LinuxUserUnit, "--no-pager")
	default:
		err = unsupportedServiceOS(m.GOOS)
	}
	if err != nil {
		if serviceText == "" {
			serviceText = err.Error()
		} else {
			serviceText = strings.TrimSpace(serviceText) + "\n" + err.Error()
		}
	}
	return lockLine + "\n\n" + strings.TrimSpace(serviceText) + "\n", err
}

func runCommand(ctx context.Context, name string, args ...string) error {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s failed: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func commandOutput(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s failed: %w", name, strings.Join(args, " "), err)
	}
	return string(out), nil
}

func unsupportedServiceOS(goos string) error {
	return fmt.Errorf("gateway service management is not implemented for %s; run 'mateway gateway serve' under your OS service manager", goos)
}

func launchDomain() string {
	return "gui/" + strconv.Itoa(currentUID())
}

func currentUID() int {
	out, err := exec.Command("id", "-u").Output()
	if err != nil {
		return 501
	}
	uid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 501
	}
	return uid
}
