package cli

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var regen = flag.Bool("update", false, "rewrite golden files")

// golden compares got with testdata/name, rewriting it under -update.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *regen {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

func TestSplit(t *testing.T) {
	tests := []struct {
		args []string
		cmd  string
		rest []string
	}{
		{nil, "", nil},
		{[]string{"check"}, "check", []string{}},
		{[]string{"check", "--json"}, "check", []string{"--json"}},
		{[]string{"-C", "sub", "check", "--all"}, "check", []string{"-C", "sub", "--all"}},
		{[]string{"--config", "x.yml", "run", "--dry-run"}, "run", []string{"--config", "x.yml", "--dry-run"}},
		{[]string{"-v", "watch", "--every", "6h"}, "watch", []string{"-v", "--every", "6h"}},
		{[]string{"--home=/tmp/h", "version"}, "version", []string{"--home=/tmp/h"}},
		{[]string{"-q"}, "", []string{"-q"}},
	}
	for _, tt := range tests {
		cmd, rest := split(slices.Clone(tt.args))
		if cmd != tt.cmd || !slices.Equal(rest, tt.rest) && len(rest)+len(tt.rest) > 0 {
			t.Errorf("split(%q) = %q, %q; want %q, %q", tt.args, cmd, rest, tt.cmd, tt.rest)
		}
	}
}

func TestMainUsage(t *testing.T) {
	tests := []struct {
		args []string
		code int
		err  string
	}{
		{nil, exitUsage, "usage: tend"},
		{[]string{"frobnicate"}, exitUsage, `unknown command "frobnicate"`},
		{[]string{"check", "--nope"}, exitUsage, "flag provided but not defined: -nope"},
		{[]string{"check", "extra"}, exitUsage, `unexpected argument "extra"`},
		{[]string{"check", "--fail-on", "minor"}, exitUsage, "--fail-on wants"},
	}
	for _, tt := range tests {
		var out, errs bytes.Buffer
		e := DefaultEnv()
		e.Stdout, e.Stderr = &out, &errs
		if code := Main(tt.args, e); code != tt.code || !strings.Contains(errs.String(), tt.err) || out.Len() > 0 {
			t.Errorf("Main(%q) = %d, %q, %q; want %d and %q on stderr only", tt.args, code, out.String(), errs.String(), tt.code, tt.err)
		}
	}
}

func TestMainHelp(t *testing.T) {
	tests := []struct {
		args []string
		want []string
		not  string
	}{
		{[]string{"help"}, []string{"one proven PR at a time", "tend run --dry-run"}, "unknown command"},
		{[]string{"--help"}, []string{"usage: tend <command>"}, ""},
		{[]string{"-h"}, []string{"usage: tend <command>"}, ""},
		{[]string{"run", "-h"}, []string{"usage: tend run [flags]", "fix the top change", "-dry-run", "global flags: see tend --help"}, "-home"},
		{[]string{"version", "--help"}, []string{"print the version", "-check"}, "-config"},
	}
	for _, tt := range tests {
		var out, errs bytes.Buffer
		e := DefaultEnv()
		e.Stdout, e.Stderr = &out, &errs
		if code := Main(tt.args, e); code != exitOK || errs.Len() > 0 {
			t.Errorf("Main(%q) = %d, stderr %q; want 0 and nothing on stderr", tt.args, code, errs.String())
		}
		for _, w := range tt.want {
			if !strings.Contains(out.String(), w) {
				t.Errorf("Main(%q) printed %q, want %q in it", tt.args, out.String(), w)
			}
		}
		if tt.not != "" && strings.Contains(out.String(), tt.not) {
			t.Errorf("Main(%q) printed %q, want no %q", tt.args, out.String(), tt.not)
		}
	}
}

func TestVersion(t *testing.T) {
	tests := []struct {
		ver, commit, date string
		want              string
	}{
		{"1.2.3", "abc1234", "2026-10-01T00:00:00Z", "tend 1.2.3 (abc1234, 2026-10-01T00:00:00Z)\n"},
		{"abc1234", "abc1234", "2026-10-01T00:00:00Z", "tend abc1234 (2026-10-01T00:00:00Z)\n"},
		{"v1.2.3", "", "", "tend v1.2.3\n"},
	}
	for _, tt := range tests {
		var out bytes.Buffer
		e := DefaultEnv()
		e.Stdout, e.Version, e.Commit, e.Date = &out, tt.ver, tt.commit, tt.date
		if code := Main([]string{"version"}, e); code != 0 || out.String() != tt.want {
			t.Errorf("version %q = %d, %q; want %q", tt.ver, code, out.String(), tt.want)
		}
	}
}
