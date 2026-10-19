//go:build windows

package mediamtx

import (
	"os/exec"
	"syscall"
)

// createNoWindow keeps the child process from flashing a console window when
// the agent runs interactively.
const createNoWindow = 0x08000000

func applyProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
}
