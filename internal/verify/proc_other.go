//go:build !unix

package verify

import "os/exec"

// On Windows there are no process groups to signal: a timeout kills the shell
// only, and children it started may outlive the step. Use verify.container
// there when that matters.
func group(cmd *exec.Cmd) {}

func reap(cmd *exec.Cmd) {}
