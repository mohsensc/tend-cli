package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/plan"
	"github.com/mohsensc/tend-cli/internal/state"
	"github.com/mohsensc/tend-cli/internal/verify"
)

// offline answers the URLs that end in one of its keys and 404 for the rest.
type offline map[string]string

func (f offline) Get(_ context.Context, url string, _ ...core.FetchOpt) (*core.Response, error) {
	for suffix, body := range f {
		if strings.HasSuffix(url, suffix) {
			return &core.Response{Status: 200, Body: []byte(body)}, nil
		}
	}
	return &core.Response{Status: 404}, nil
}

func (f offline) Post(context.Context, string, []byte, ...core.FetchOpt) (*core.Response, error) {
	return &core.Response{Status: 404}, nil
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Ada", "-c", "user.email=ada@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// repo is a git repo on main with files in one commit, or none when files
// is nil.
func repo(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "init", "--quiet", "--initial-branch=main")
	if files == nil {
		return dir
	}
	writeFiles(t, dir, files)
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "--quiet", "-m", "Start")
	return dir
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

type tendRun struct {
	code        int
	out, stderr string
}

func tend(t *testing.T, dir, home string, f core.Fetcher, args ...string) tendRun {
	t.Helper()
	var out, errs bytes.Buffer
	e := DefaultEnv()
	e.Dir, e.Home, e.Stdout, e.Stderr, e.Stdin = dir, home, &out, &errs, strings.NewReader("")
	e.Fetch, e.Getenv = f, func(string) string { return "" }
	code := Main(args, e)
	return tendRun{code, out.String(), errs.String()}
}

// worktrees lists the worktrees tend made under home.
func worktrees(t *testing.T, home string) []string {
	t.Helper()
	var res []string
	err := filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && d.Name() == "wt" {
			ents, _ := os.ReadDir(p)
			for _, e := range ents {
				res = append(res, filepath.Join(p, e.Name()))
			}
		}
		return err
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return res
}

// A base that names no branch stops run before it proves anything; it is
// never the checked-out branch in its place.
func TestRunBaseNotFound(t *testing.T) {
	dir := repo(t, map[string]string{"README.md": "hi\n", "tend.yml": "base: mian\n"})
	gitIn(t, dir, "checkout", "--quiet", "-b", "feature")
	home := t.TempDir()
	res := tend(t, dir, home, offline{}, "run", "--dry-run")
	if res.code != exitErr || !strings.Contains(res.stderr, `base branch "mian" not found locally`) {
		t.Errorf("run = %d, %q, %q", res.code, res.out, res.stderr)
	}
	if wts := worktrees(t, home); len(wts) > 0 {
		t.Errorf("run made worktrees %q for a base it could not find", wts)
	}

	writeFiles(t, dir, map[string]string{"tend.yml": "base: main\n"})
	res = tend(t, dir, home, offline{}, "run", "--json")
	var got struct{ Action, Summary, Worktree string }
	if err := json.Unmarshal([]byte(res.out), &got); err != nil || res.code != exitOK {
		t.Fatalf("run --json = %d, %q, %q: %v", res.code, res.out, res.stderr, err)
	}
	if got.Action != "nothing" || !strings.HasPrefix(got.Summary, "nothing to watch: no manifests") || got.Worktree != "" {
		t.Errorf("run --json = %+v", got)
	}
}

// check still lists what the checkout has when the base is nowhere, as in
// a CI checkout of a pull request, and says so.
func TestCheckBaseNotFound(t *testing.T) {
	dir := repo(t, map[string]string{"README.md": "hi\n", "tend.yml": "base: mian\n"})
	res := tend(t, dir, t.TempDir(), offline{}, "check", "--json")
	var got struct{ Notes []string }
	if err := json.Unmarshal([]byte(res.out), &got); err != nil || res.code != exitOK {
		t.Fatalf("check --json = %d, %q, %q: %v", res.code, res.out, res.stderr, err)
	}
	if !slices.ContainsFunc(got.Notes, func(n string) bool {
		return strings.HasPrefix(n, `base branch "mian" not found locally; showing the checkout at `)
	}) {
		t.Errorf("notes = %q", got.Notes)
	}
}

// check reads the checkout, and says so when run would work on another
// commit: a base branch only the remote has, or a checkout behind the base.
func TestCheckSaysWhereRunWorks(t *testing.T) {
	origin := repo(t, map[string]string{"README.md": "hi\n"})
	old := strings.TrimSpace(gitIn(t, origin, "rev-parse", "HEAD"))
	writeFiles(t, origin, map[string]string{"README.md": "hello\n"})
	gitIn(t, origin, "commit", "--quiet", "-am", "next")
	dir := t.TempDir()
	gitIn(t, dir, "clone", "--quiet", origin, ".")
	gitIn(t, dir, "checkout", "--quiet", "--detach", old)
	notes := func() []string {
		res := tend(t, dir, t.TempDir(), offline{}, "check", "--json")
		var got struct{ Notes []string }
		if err := json.Unmarshal([]byte(res.out), &got); err != nil {
			t.Fatalf("check --json = %d, %q, %q: %v", res.code, res.out, res.stderr, err)
		}
		return got.Notes
	}
	has := func(notes []string, part string) bool {
		return slices.ContainsFunc(notes, func(n string) bool { return strings.Contains(n, part) })
	}
	if got := notes(); !has(got, "showing the checkout at "+old[:7]+"; run works on origin/main at ") {
		t.Errorf("behind the base: notes = %q", got)
	}
	gitIn(t, dir, "update-ref", "-d", "refs/remotes/origin/main")
	gitIn(t, dir, "branch", "--quiet", "-D", "main")
	if got := notes(); !has(got, `base branch "main" is not in this clone; showing the checkout at `+old[:7]+", but run fetches origin/main first") {
		t.Errorf("no base here: notes = %q", got)
	}
	gitIn(t, dir, "checkout", "--quiet", "-b", "main")
	gitIn(t, dir, "fetch", "--quiet", "origin")
	gitIn(t, dir, "reset", "--quiet", "--hard", "origin/main")
	writeFiles(t, dir, map[string]string{"NOTES.md": "x\n"})
	gitIn(t, dir, "add", "NOTES.md")
	gitIn(t, dir, "commit", "--quiet", "-m", "on top")
	if got := notes(); has(got, "showing the checkout") || has(got, "not in this clone") {
		t.Errorf("a branch on the base: notes = %q", got)
	}
}

// Commit mode builds on deliver.branch once it exists, and on the base
// until then.
func TestRunCommitBranchNotYet(t *testing.T) {
	dir := repo(t, map[string]string{"README.md": "hi\n", "tend.yml": "deliver: {mode: commit, branch: release}\n"})
	res := tend(t, dir, t.TempDir(), offline{}, "run")
	if res.code != exitOK || !strings.HasPrefix(res.out, "tend: nothing to watch: no manifests") {
		t.Errorf("run = %d, %q, %q", res.code, res.out, res.stderr)
	}
}

func TestRunNoCommits(t *testing.T) {
	dir := repo(t, nil)
	for _, cfg := range []string{"", "base: main\n", "base: trunk\n"} {
		writeFiles(t, dir, map[string]string{"tend.yml": cfg})
		res := tend(t, dir, t.TempDir(), offline{}, "run")
		if res.code != exitOK || res.out != "tend: nothing to watch: the repository has no commits\n" {
			t.Errorf("%q: run = %d, %q, %q", cfg, res.code, res.out, res.stderr)
		}
	}
}

// --keep says where the worktree is, which the run would otherwise remove.
func TestRunKeep(t *testing.T) {
	dir := repo(t, map[string]string{"README.md": "hi\n"})
	home := t.TempDir()
	res := tend(t, dir, home, offline{}, "run", "--keep")
	wts := worktrees(t, home)
	if res.code != exitOK || len(wts) != 1 || !strings.HasSuffix(strings.TrimSpace(res.out), "; the worktree is kept at "+wts[0]) {
		t.Errorf("run --keep = %d, %q, worktrees %q", res.code, res.out, wts)
	}
}

// A first PR that only adds tend.yml is no work upstream, so it does not
// trip --fail-on any; one that removes an unused dependency does.
func TestCheckFailOn(t *testing.T) {
	tests := []struct {
		name   string
		files  map[string]string
		failOn string
		code   int
	}{
		{"nothing but tend.yml to add", map[string]string{"go.mod": "module example.com/x\n\ngo 1.26\n"}, "any", exitOK},
		{"an unused dependency", map[string]string{
			"package.json":      `{"dependencies": {"left-pad": "^1.0.0"}}`,
			"package-lock.json": `{"lockfileVersion": 3, "packages": {"": {}, "node_modules/left-pad": {"version": "1.3.0"}}}`,
			"index.js":          "module.exports = 1\n",
		}, "any", exitFound},
		{"an unused dependency is no security fix", map[string]string{
			"package.json":      `{"dependencies": {"left-pad": "^1.0.0"}}`,
			"package-lock.json": `{"lockfileVersion": 3, "packages": {"": {}, "node_modules/left-pad": {"version": "1.3.0"}}}`,
			"index.js":          "module.exports = 1\n",
		}, "security", exitOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := repo(t, tt.files)
			if res := tend(t, dir, t.TempDir(), offline{}, "check", "--fail-on", tt.failOn); res.code != tt.code {
				t.Errorf("check --fail-on %s = %d, want %d: %q, %q", tt.failOn, res.code, tt.code, res.out, res.stderr)
			}
		})
	}
}

func TestFails(t *testing.T) {
	first := core.Change{Kind: core.First, Batch: &core.Batch{}}
	removes := core.Change{Kind: core.First, Batch: &core.Batch{Unused: []core.Dep{{Name: "left-pad"}}}}
	dep := core.Change{Kind: core.Deprecation}
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	gone := core.Change{Kind: core.Deprecation, Sunset: time.Date(2021, 2, 1, 0, 0, 0, 0, time.UTC)}
	far := core.Change{Kind: core.Deprecation, Sunset: now.AddDate(1, 0, 0)}
	for _, tt := range []struct {
		ch   core.Change
		kind string
		want bool
	}{
		{first, "any", false},
		{core.Change{Kind: core.First}, "any", false},
		{removes, "any", true},
		{removes, "security", false},
		{dep, "any", true},
		{dep, "deprecation", true},
		{dep, "security", false},
		{dep, "breaking", false},
		{gone, "breaking", true},
		{gone, "deprecation", true},
		{far, "breaking", false},
	} {
		if got := fails(tt.ch, tt.kind, now); got != tt.want {
			t.Errorf("fails(%s, %d unused, %s) = %v", tt.ch.Kind, unused(tt.ch), tt.kind, got)
		}
	}
}

func unused(ch core.Change) int {
	if ch.Batch == nil {
		return 0
	}
	return len(ch.Batch.Unused)
}

// check reads the checkout, so what it finds of the catch-up settles
// nothing: a stage it finds nothing for is not the end of it.
func TestCheckKeepsStage(t *testing.T) {
	dir := repo(t, map[string]string{"go.mod": "module example.com/x\n\ngo 1.26\n"})
	home := t.TempDir()
	key := state.Key("", dir)
	st, _, err := state.Open(home, key)
	if err != nil {
		t.Fatal(err)
	}
	st.Onboard, st.Stage = "merged", plan.Fixes
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	res := tend(t, dir, home, offline{}, "check")
	if res.code != exitOK || strings.Contains(res.stderr, "catch-up") {
		t.Errorf("check = %d, %q, %q", res.code, res.out, res.stderr)
	}
	if st, _, err = state.Open(home, key); err != nil {
		t.Fatal(err)
	}
	if st.Onboard != "merged" || st.Stage != plan.Fixes {
		t.Errorf("check left onboard %q, stage %d; want merged, %d", st.Onboard, st.Stage, plan.Fixes)
	}
}

// What run could not prove says why in --json too, and where the worktree
// is when it is kept.
func TestReportUnproven(t *testing.T) {
	var out, errs bytes.Buffer
	s := &session{e: Env{Stdout: &out, Stderr: &errs, Now: time.Now}, st: state.New(), o: opts{json: true, keep: true}}
	r := &runner{s: s, wt: &verify.Worktree{Dir: "/wt/abc", Base: "abc"}}
	detail := "npm test failed (exit 1)\nexpected 1 to equal 2"
	if code := s.report(r.unproven(&core.Change{ID: "npm/a@2.0.0", Kind: core.Update}, detail)); code != exitUnproven {
		t.Errorf("code = %d", code)
	}
	var got struct{ Action, Summary, Detail, Worktree string }
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Action != "unproven" || got.Summary != "could not prove npm/a@2.0.0: npm test failed (exit 1)" || got.Detail != detail || got.Worktree != "/wt/abc" {
		t.Errorf("json = %+v", got)
	}
	out.Reset()
	s.o.json = false
	s.report(r.unproven(&core.Change{ID: "npm/a@2.0.0", Kind: core.Update}, detail))
	if want := "tend: could not prove npm/a@2.0.0: npm test failed (exit 1); the worktree is kept at /wt/abc\n"; out.String() != want {
		t.Errorf("line = %q, want %q", out.String(), want)
	}
}

// The release notice comes with the check for a release, once a day, not
// on every run while a newer tag is known.
func TestNotice(t *testing.T) {
	now := time.Now()
	latest := offline{"/releases/latest": `{"tag_name": "v9.0.0", "html_url": "https://example.com"}`}
	var errs bytes.Buffer
	s := &session{e: Env{Stderr: &errs, Now: func() time.Time { return now }, Version: "1.0.0"}, st: state.New(), fetch: latest}
	s.notice(context.Background())
	if errs.String() != "tend: v9.0.0 is available; run tend update\n" {
		t.Errorf("first notice = %q", errs.String())
	}
	errs.Reset()
	now = now.Add(time.Hour)
	s.notice(context.Background())
	if errs.Len() > 0 {
		t.Errorf("noticed again within the day: %q", errs.String())
	}
	now = now.Add(24 * time.Hour)
	s.notice(context.Background())
	if errs.Len() == 0 {
		t.Error("no notice a day later")
	}
}

// -C names a directory that must exist; tend never makes one for it.
func TestMainDir(t *testing.T) {
	dir := t.TempDir()
	res := tend(t, dir, t.TempDir(), offline{}, "-C", "missing", "init")
	if res.code != exitUsage || !strings.Contains(res.stderr, "-C missing: no such directory") {
		t.Errorf("init in a missing dir = %d, %q", res.code, res.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "missing")); !os.IsNotExist(err) {
		t.Errorf("init made the missing dir: %v", err)
	}
	writeFiles(t, dir, map[string]string{"file": "x"})
	if res := tend(t, dir, t.TempDir(), offline{}, "-C", "file", "check"); res.code != exitUsage || !strings.Contains(res.stderr, "-C file: not a directory") {
		t.Errorf("-C on a file = %d, %q", res.code, res.stderr)
	}
	writeFiles(t, dir, map[string]string{"sub/README.md": "hi\n"})
	if res := tend(t, dir, t.TempDir(), offline{}, "--C", "sub", "check"); res.code != exitOK || !strings.Contains(res.out, "nothing to watch") {
		t.Errorf("--C sub check = %d, %q, %q", res.code, res.out, res.stderr)
	}
}

// A relative home, from --home or TEND_HOME, is one place under the dir tend
// runs as if in, for the state and for the worktrees git makes alike.
func TestRelativeHome(t *testing.T) {
	for _, flag := range []bool{true, false} {
		dir := repo(t, map[string]string{"sub/README.md": "hi\n"})
		args := []string{"-C", "sub", "run", "--keep"}
		home := "rel"
		if flag {
			args, home = append([]string{"--home", "rel"}, args...), filepath.Join(t.TempDir(), "unused")
		}
		res := tend(t, dir, home, offline{}, args...)
		if res.code != exitOK {
			t.Fatalf("exit %d: %s", res.code, res.stderr)
		}
		want := filepath.Join(dir, "sub", "rel")
		if _, err := os.Stat(filepath.Join(want, "repos")); err != nil {
			t.Errorf("no state under %s: %v", want, err)
		}
		list := gitIn(t, dir, "worktree", "list", "--porcelain")
		for _, line := range strings.Split(list, "\n") {
			if p, ok := strings.CutPrefix(line, "worktree "); ok && p != dir && !strings.HasPrefix(p, want+string(filepath.Separator)) {
				t.Errorf("a worktree outside %s: %s", want, p)
			}
		}
		if !strings.Contains(list, want) {
			t.Errorf("no worktree under %s:\n%s", want, list)
		}
	}
}

// --config is read as if tend started in -C's dir, as git -C does.
func TestConfigUnderDir(t *testing.T) {
	dir := t.TempDir()
	r2 := repo(t, map[string]string{"tend.yml": "bogus: 1\n"})
	if err := os.Rename(r2, filepath.Join(dir, "r2")); err != nil {
		t.Fatal(err)
	}
	res := tend(t, dir, t.TempDir(), offline{}, "-C", "r2", "--config", "tend.yml", "check")
	if res.code != exitErr || !strings.Contains(res.stderr, "unknown key") {
		t.Errorf("exit %d: %s", res.code, res.stderr)
	}
}

// A repo with nothing to watch owes no first PR, in check's text, its JSON
// and its exit code alike.
func TestCheckNothing(t *testing.T) {
	for _, files := range []map[string]string{nil, {"README.md": "hi\n"}} {
		dir := repo(t, files)
		if r := tend(t, dir, t.TempDir(), offline{}, "check", "--fail-on", "any"); r.code != exitOK || !strings.Contains(r.out, "nothing to watch") {
			t.Errorf("check --fail-on any = %d, %q, %q", r.code, r.out, r.stderr)
		}
		r := tend(t, dir, t.TempDir(), offline{}, "check", "--json")
		var got struct{ Queue []any }
		if err := json.Unmarshal([]byte(r.out), &got); err != nil || len(got.Queue) != 0 {
			t.Errorf("check --json = %v, %s", err, r.out)
		}
	}
	dir := repo(t, map[string]string{"package.json": `{"dependencies": {"lodash": "4.17.0"}}`, "tend.yml": "ecosystems: [docker]\n"})
	if r := tend(t, dir, t.TempDir(), offline{}, "check"); !strings.Contains(r.out, "tend.yml's ecosystems lists only docker") {
		t.Errorf("check with ecosystems: [docker] = %q, %q", r.out, r.stderr)
	}
}
