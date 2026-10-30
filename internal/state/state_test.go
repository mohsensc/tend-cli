package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
)

func TestNormURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"git@github.com:Acme/Shop.git", "github.com/Acme/Shop"},
		{"https://github.com/Acme/Shop.git", "github.com/Acme/Shop"},
		{"https://github.com/Acme/Shop", "github.com/Acme/Shop"},
		{"https://GitHub.com/Acme/Shop/", "github.com/Acme/Shop"},
		{"https://token@github.com/Acme/Shop.git", "github.com/Acme/Shop"},
		{"ssh://git@github.com/Acme/Shop.git", "github.com/Acme/Shop"},
		{"ssh://git@GITHUB.COM:22/Acme/Shop.git", "github.com/Acme/Shop"},
		{"git://github.com/Acme/Shop.git", "github.com/Acme/Shop"},
		{"github.com:Acme/Shop", "github.com/Acme/Shop"},
		{"/srv/git/shop.git", "/srv/git/shop"},
		{"file:///srv/git/shop.git", "/srv/git/shop"},
	}
	for _, tt := range tests {
		if got := NormURL(tt.in); got != tt.want {
			t.Errorf("NormURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestKey(t *testing.T) {
	ssh := Key("git@github.com:acme/shop.git", "/a")
	https := Key("https://github.com/acme/shop", "/b")
	if ssh != https {
		t.Errorf("ssh and https keys differ: %s %s", ssh, https)
	}
	if len(ssh) != 12 {
		t.Errorf("key %q is not 12 hex digits", ssh)
	}
	if Key("", "/a") == Key("", "/b") {
		t.Error("paths share a key")
	}
	if Key("", "/a") == ssh {
		t.Error("path key equals origin key")
	}
}

func TestHome(t *testing.T) {
	env := map[string]string{"TEND_HOME": "/srv/tend"}
	if got := Home(func(k string) string { return env[k] }); got != "/srv/tend" {
		t.Errorf("Home = %q, want TEND_HOME", got)
	}
	if got := Home(func(string) string { return "" }); filepath.Base(got) != "tend" {
		t.Errorf("Home = %q, want a tend dir under the cache", got)
	}
}

func TestOpenSave(t *testing.T) {
	home := t.TempDir()
	s, dir, err := Open(home, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(home, "repos", "abc") {
		t.Errorf("dir = %s", dir)
	}
	if s.Version != 1 || s.Seen == nil || s.Parked == nil || s.Specs == nil || s.Open != nil {
		t.Fatalf("fresh state = %+v", s)
	}
	at := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	s.See("npm/a@2.0.0", "noise", "", at)
	s.See("npm/a@2.0.0", "queued", "", at.Add(time.Hour))
	s.Open = &OpenPR{ID: "npm/b@1.0.1", PR: 7, Branch: "tend/npm-b-1.0.1"}
	s.Baseline = &BaseRun{Sha: "abc123", Run: &core.Run{Commit: "abc123", Pass: true}}
	s.Fail("npm/c@3.0.0", "npm test failed", "abc123", at)
	s.See("npm/c@3.0.0", "unsupported", "npm test failed", at.Add(time.Hour))
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	got, _, err := Open(home, "abc")
	if err != nil {
		t.Fatal(err)
	}
	seen := got.Seen["npm/a@2.0.0"]
	if !seen.First.Equal(at) || !seen.Last.Equal(at.Add(time.Hour)) || seen.Verdict != "queued" {
		t.Errorf("seen = %+v", seen)
	}
	if c := got.Seen["npm/c@3.0.0"]; c.Base != "abc123" {
		t.Errorf("a standing verdict lost its base: %+v", c)
	}
	if got.Open == nil || got.Open.PR != 7 || got.Baseline == nil || !got.Baseline.Run.Pass {
		t.Errorf("reloaded = %+v", got)
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".state-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestSaveKeepsOldFileOnFailure(t *testing.T) {
	home := t.TempDir()
	s, dir, _ := Open(home, "k")
	s.See("x", "noise", "", time.Unix(0, 0))
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Error(err)
		}
	}()
	s.See("y", "noise", "", time.Unix(0, 0))
	if err := s.Save(); err == nil {
		t.Skip("directory is writable anyway (running as root?)")
	}
	got, _, err := Open(home, "k")
	if err != nil || len(got.Seen) != 1 {
		t.Fatalf("after a failed save: %+v, %v", got.Seen, err)
	}
}

func TestOpenCorrupt(t *testing.T) {
	tests := []struct{ name, body, why string }{
		{"truncated", `{"Version":1,"Seen":{"a":`, "unexpected end of JSON input"},
		{"zero-filled", "\x00\x00\x00\x00", "invalid character"},
		{"wrongly typed", `{"Version":1,"Seen":[1,2]}`, "cannot unmarshal array"},
		{"from a newer tend", `{"Version":7,"Seen":{"a":{"Verdict":"noise"}}}`, "newer tend"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			p := filepath.Join(Dir(home, "k"), "state.json")
			write(t, p, tc.body)
			write(t, p+".corrupt", "an older one")
			s, _, err := Open(home, "k")
			if err != nil {
				t.Fatal(err)
			}
			if s.Version != 1 || len(s.Seen) != 0 || s.Seen == nil || s.Parked == nil || s.Open != nil {
				t.Errorf("state = %+v, want a fresh one", s)
			}
			if !strings.Contains(s.Discarded, tc.why) {
				t.Errorf("Discarded = %q, want it to say %q", s.Discarded, tc.why)
			}
			aside := p + ".corrupt"
			if strings.Contains(tc.body, `"Version":7`) {
				aside = p + ".v7"
			}
			if b, err := os.ReadFile(aside); err != nil || string(b) != tc.body {
				t.Errorf("%s = %q, %v", filepath.Base(aside), b, err)
			}
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Errorf("state.json is still there: %v", err)
			}
			if err := s.Save(); err != nil {
				t.Fatal(err)
			}
			if again, _, err := Open(home, "k"); err != nil || again.Discarded != "" {
				t.Errorf("the fresh state did not load: %q, %v", again.Discarded, err)
			}
		})
	}
}

func TestOpenEmpty(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(Dir(home, "k"), "state.json"), "")
	s, _, err := Open(home, "k")
	if err != nil || s.Discarded != "" {
		t.Fatalf("empty state: %q, %v", s.Discarded, err)
	}
}

func TestOpenUnreadable(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(Dir(home, "k"), "state.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Open(home, "k"); err == nil || !strings.Contains(err.Error(), "--reset") {
		t.Fatalf("err = %v, want it to point at --reset", err)
	}
}

func TestOpenOldBaseline(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(Dir(home, "k"), "state.json"), `{"Version":1,"Baseline":{"0123abcd":{"Pass":true}}}`)
	s, _, err := Open(home, "k")
	if err != nil {
		t.Fatal(err)
	}
	if s.Baseline != nil {
		t.Errorf("Baseline = %+v, want nil for the old shape", s.Baseline)
	}
}

func TestReset(t *testing.T) {
	home := t.TempDir()
	if err := Reset(home, "k"); err != nil {
		t.Fatalf("reset of nothing: %v", err)
	}
	s, dir, _ := Open(home, "k")
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "wt", "abc", "f"), "x")
	release, err := Lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := Reset(home, "k"); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 || ents[0].Name() != "lock" {
		t.Fatalf("after reset: %v, want only the lock", ents)
	}
	if _, err := Lock(dir); err == nil {
		t.Fatal("reset freed the lock")
	}
}

func TestLock(t *testing.T) {
	dir := t.TempDir()
	release, err := Lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Lock(dir)
	var held *Locked
	if !errors.As(err, &held) || held.PID != os.Getpid() {
		t.Fatalf("second lock: %v", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("pid %d", os.Getpid())) {
		t.Errorf("message %q lacks the pid", err)
	}
	release()
	release2, err := Lock(dir)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	release2()
}

func write(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
