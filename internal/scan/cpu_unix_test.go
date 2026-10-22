//go:build unix

package scan

import (
	"syscall"
	"time"
)

// cpu is the CPU time this process has used so far.
func cpu() time.Duration {
	var u syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &u); err != nil {
		return -1
	}
	return time.Duration(u.Utime.Nano() + u.Stime.Nano())
}
