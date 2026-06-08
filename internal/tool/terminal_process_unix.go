//go:build unix

package tool

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

func configureTerminalProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = 2 * time.Second
}

func terminalKillSignal() string {
	return "SIGKILL_PROCESS_GROUP"
}
