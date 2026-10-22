//go:build unix

package verify

import (
	"os/exec"
	"syscall"
)

// group puts the command in its own process group so a timeout kills the
// whole tree (a test runner and its workers), not just the shell.
func group(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}

// reap kills whatever the command left behind in its group after it exited.
func reap(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
