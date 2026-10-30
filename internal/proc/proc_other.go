//go:build !unix

package proc

import "os/exec"

// Group does nothing where there are no process groups to signal: a cancel
// kills the first process only, and children it started may outlive it.
func Group(cmd *exec.Cmd) {}

func Reap(cmd *exec.Cmd) {}
