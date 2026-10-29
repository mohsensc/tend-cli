//go:build unix

package cli

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
)

// interrupting is a registry that is interrupted, once, while tend waits
// on it.
type interrupting struct{ once sync.Once }

func (f *interrupting) Get(ctx context.Context, url string, _ ...core.FetchOpt) (*core.Response, error) {
	f.once.Do(func() { _ = syscall.Kill(syscall.Getpid(), syscall.SIGINT) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func (f *interrupting) Post(ctx context.Context, url string, _ []byte, o ...core.FetchOpt) (*core.Response, error) {
	return f.Get(ctx, url, o...)
}

// An interrupted check or run reports nothing it found so far, saves
// nothing, and exits 130, so a CI gate on --fail-on cannot pass by being
// cancelled.
func TestInterrupted(t *testing.T) {
	for _, args := range [][]string{{"check", "--fail-on", "security"}, {"run", "--dry-run"}} {
		t.Run(args[0], func(t *testing.T) {
			dir := repo(t, map[string]string{"package.json": `{"dependencies": {"lodash": "4.17.0"}}`})
			home := t.TempDir()
			r := tend(t, dir, home, &interrupting{}, args...)
			lines := strings.Split(strings.TrimSpace(r.stderr), "\n")
			if r.code != exitInterrupted || lines[len(lines)-1] != "tend: interrupted" || strings.Count(r.stderr, "interrupt") != 1 || r.out != "" {
				t.Errorf("exit %d\nstdout: %s\nstderr: %s", r.code, r.out, r.stderr)
			}
			_ = filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
				if err == nil && d.Name() == "state.json" {
					t.Errorf("an interrupted %s saved %s", args[0], p)
				}
				return nil
			})
		})
	}
}
