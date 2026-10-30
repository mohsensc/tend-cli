//go:build unix

package proc

import (
	"os/exec"
	"syscall"
)

// Group puts cmd in its own process group, so an interrupt at the terminal
// reaches tend alone, and a cancel or a timeout kills the whole tree it
// started, not just its first process.
func Group(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}

// Reap kills whatever cmd left behind in its group after it exited.
func Reap(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
