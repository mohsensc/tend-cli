//go:build unix

package state

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// A lock file no process holds, as a killed run or a restored cache leaves
// one, is no lock at all.
func TestLockLeftOver(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "lock"), fmt.Sprintf("999999 %d\n", time.Now().Unix()))
	release, err := Lock(dir)
	if err != nil {
		t.Fatalf("a lock nobody holds: %v", err)
	}
	release()
	if _, err := os.Stat(filepath.Join(dir, "lock")); err != nil {
		t.Errorf("release removed the lock file: %v", err)
	}
}

// TestLockChild is the child TestLockKilled starts: it takes the lock, says
// so and waits to be killed.
func TestLockChild(t *testing.T) {
	dir := os.Getenv("TEND_LOCK_CHILD")
	if dir == "" {
		t.Skip("run by TestLockKilled")
	}
	if _, err := Lock(dir); err != nil {
		t.Fatal(err)
	}
	fmt.Println("locked")
	time.Sleep(time.Minute)
}

// A run killed outright, which runs no deferred release, leaves the lock
// free for the next one.
func TestLockKilled(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockChild$", "-test.v")
	cmd.Env = append(os.Environ(), "TEND_LOCK_CHILD="+dir)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(out)
	for sc.Scan() && sc.Text() != "locked" {
	}
	_, err = Lock(dir)
	var held *Locked
	if !errors.As(err, &held) || held.PID != cmd.Process.Pid {
		t.Fatalf("lock held by the child: %v", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	release, err := Lock(dir)
	if err != nil {
		t.Fatalf("after the holder was killed: %v", err)
	}
	release()
}
