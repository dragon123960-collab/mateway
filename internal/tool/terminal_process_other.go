//go:build !unix

package tool

import (
	"os/exec"
	"time"
)

func configureTerminalProcess(cmd *exec.Cmd) {
	cmd.WaitDelay = 2 * time.Second
}

func terminalKillSignal() string {
	return "SIGKILL"
}
