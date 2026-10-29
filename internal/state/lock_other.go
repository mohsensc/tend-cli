//go:build !unix

package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// StaleAfter is how old a lock gets before another run may take it over.
const StaleAfter = 2 * time.Hour

// Lock takes the run lock in dir. Where the system has no lock a process
// holds until it dies, a lock older than two hours is stale. Releasing it
// leaves alone a lock another run took over in the meantime.
func Lock(dir string) (release func(), err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, lockName)
	for range 2 {
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			mine := fmt.Sprintf("%d %d\n", os.Getpid(), now().Unix())
			_, err = f.WriteString(mine)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				os.Remove(p)
				return nil, err
			}
			return func() {
				if b, err := os.ReadFile(p); err == nil && string(b) == mine {
					os.Remove(p)
				}
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		held := readLock(p)
		if now().Sub(held.At) < StaleAfter {
			return nil, held
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("state: cannot take %s", p)
}
