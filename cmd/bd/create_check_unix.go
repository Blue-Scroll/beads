//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// setCreateCheckProcessGroup puts the check in its own process group, so a
// timeout kills the check AND every child it started. Killing only `sh` would
// leave its children running, still writing to bd's stderr.
func setCreateCheckProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
