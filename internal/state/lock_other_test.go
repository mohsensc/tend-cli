//go:build !unix

package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLockStale(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	defer func() { now = time.Now }()
	p := filepath.Join(dir, "lock")

	write(t, p, fmt.Sprintf("4242 %d\n", at.Add(-StaleAfter+time.Minute).Unix()))
	now = func() time.Time { return at }
	if _, err := Lock(dir); err == nil {
		t.Fatal("took a fresh lock")
	}

	write(t, p, fmt.Sprintf("4242 %d\n", at.Add(-StaleAfter-time.Minute).Unix()))
	release, err := Lock(dir)
	if err != nil {
		t.Fatalf("stale lock not taken over: %v", err)
	}
	b, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(b), fmt.Sprint(os.Getpid())+" ") {
		t.Errorf("lock holds %q", b)
	}
	release()

	// A run that kept the lock past StaleAfter leaves the one that took it over alone.
	write(t, p, fmt.Sprintf("4242 %d\n", at.Add(-StaleAfter-time.Minute).Unix()))
	old, err := Lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	now = func() time.Time { return at.Add(StaleAfter + time.Minute) }
	release, err = Lock(dir)
	if err != nil {
		t.Fatalf("stale lock of this pid not taken over: %v", err)
	}
	old()
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("the old run's release removed the new lock: %v", err)
	}
	release()
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock still there after release: %v", err)
	}
	now = func() time.Time { return at }

	write(t, p, "")
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(dir); err == nil {
		t.Fatal("took a lock that is being written")
	}
}
