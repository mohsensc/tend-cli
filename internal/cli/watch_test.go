package cli

import (
	"strings"
	"testing"
)

// What a later run cannot fix stops watch at once, so a broken setup does
// not look healthy for a day at a time.
func TestWatchStops(t *testing.T) {
	plain := t.TempDir()
	bad := repo(t, map[string]string{"tend.yml": "bogus: 1\n"})
	for _, c := range []struct {
		name, dir string
		args      []string
		code      int
		says      string
	}{
		{"no git", plain, []string{"watch", "--every", "1h"}, exitErr, "is not a git repository"},
		{"bad tend.yml", bad, []string{"watch"}, exitErr, "unknown key"},
		{"negative every", bad, []string{"watch", "--every=-1s"}, exitUsage, "--every wants a positive duration"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := tend(t, c.dir, t.TempDir(), offline{}, c.args...)
			if r.code != c.code || !strings.Contains(r.stderr, c.says) || strings.Contains(r.stderr, "next run") {
				t.Errorf("exit %d, stderr %q", r.code, r.stderr)
			}
		})
	}
}
