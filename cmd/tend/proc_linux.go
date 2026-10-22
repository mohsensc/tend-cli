package main

import "syscall"

// harden makes tend's own /proc entries readable by root only. The project's
// commands run as the same user, and /proc/<pid>/environ would otherwise
// hand them the tokens tend started with. Children are dumpable again after
// exec, so nothing they run notices.
func harden() {
	_, _, _ = syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_SET_DUMPABLE, 0, 0)
}
