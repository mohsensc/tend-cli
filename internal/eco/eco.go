// Package eco reads and bumps the manifests of every package ecosystem tend knows.
package eco

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/gitx"
	"github.com/mohsensc/tend-cli/internal/ver"
)

// All lists every ecosystem in the order tend reports them: the dedicated
// ones, then those that cover what the dedicated ones do not. tend.yml's deps
// entries are one more; see For.
func All() []core.Ecosystem {
	return []core.Ecosystem{
		npm{}, gomod{}, python{}, maven{}, gradle{}, cargo{},
		rubygems{}, composer{}, nuget{}, docker{}, actions{},
		toolchain{}, gitRefs{}, terraform{}, helm{},
	}
}

// Registries are the hosts the ecosystems and sources already read upstream
// facts from. Code that calls one of them is not an API tend fails to follow.
var Registries = []string{
	"registry.npmjs.org", "registry.yarnpkg.com", "proxy.golang.org", "pypi.org",
	"repo1.maven.org", "dl.google.com", "search.maven.org", "repo.packagist.org",
	"crates.io", "rubygems.org", "api.nuget.org", "hub.docker.com",
	"registry.terraform.io", "api.osv.dev", "endoflife.date",
}

// prefix makes a Page that puts the package name after p.
func prefix(p string) func(string) string {
	return func(name string) string { return p + name }
}

func ByName(name string) core.Ecosystem {
	for _, e := range All() {
		if e.Name() == name {
			return e
		}
	}
	return nil
}

// skipDirs hold other people's code, build output, or, like the go tool's
// testdata, fixtures that are not the project's own manifests.
var skipDirs = map[string]bool{
	"node_modules": true, "vendor": true, ".git": true, "target": true, "dist": true,
	"build": true, ".venv": true, "venv": true, "__pycache__": true, "bin": true, "obj": true,
	"testdata": true,
}

// walk returns the repo-relative, slash-separated paths of files under root that
// match one of the glob patterns, sorted. A pattern with a slash matches the whole
// path, one without matches the base name. Git-ignored files are left out.
func walk(root string, names ...string) ([]string, error) {
	files, err := Files(root)
	if err != nil {
		return nil, err
	}
	var res []string
	for _, f := range files {
		if skipped(f) || !matches(f, names) {
			continue
		}
		if fi, err := os.Lstat(filepath.Join(root, f)); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		res = append(res, f)
	}
	slices.Sort(res)
	return slices.Compact(res), nil
}

var listed struct {
	sync.Mutex
	m map[string][]string
}

// Files lists the files under root: what git tracks or would track, or every
// file when git is not there. Within Keep it lists each root once.
func Files(root string) ([]string, error) {
	listed.Lock()
	files, ok := listed.m[root]
	listed.Unlock()
	if ok {
		return files, nil
	}
	files, err := gitx.Files(context.Background(), root)
	if err != nil {
		return dirFiles(root)
	}
	return files, nil
}

// Keep has Files list root once until the returned func is called: one
// inventory reads one tree, and every ecosystem looks at the same list.
func Keep(root string) (done func(), err error) {
	files, err := Files(root)
	if err != nil {
		return nil, err
	}
	listed.Lock()
	defer listed.Unlock()
	if listed.m == nil {
		listed.m = map[string][]string{}
	}
	listed.m[root] = files
	return func() {
		listed.Lock()
		delete(listed.m, root)
		listed.Unlock()
	}, nil
}

func dirFiles(root string) ([]string, error) {
	var res []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		res = append(res, filepath.ToSlash(rel))
		return nil
	})
	return res, err
}

func skipped(rel string) bool {
	for _, part := range strings.Split(path.Dir(rel), "/") {
		if skipDirs[part] {
			return true
		}
	}
	return false
}

func matches(rel string, names []string) bool {
	if len(names) == 0 {
		return true
	}
	base := path.Base(rel)
	for _, n := range names {
		subject := base
		if strings.Contains(n, "/") {
			subject = rel
		}
		if ok, _ := path.Match(n, subject); ok {
			return true
		}
	}
	return false
}

// have is a variable so tests can pretend a native tool is or is not installed.
var have = func(tool string) bool {
	_, err := exec.LookPath(tool)
	return err == nil
}

var runTimeout = 10 * time.Minute

// run executes line with sh -c in root and returns its combined output. env is
// added to the process environment.
func run(ctx context.Context, root string, env []string, line string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", line)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), env...)
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return string(out), fmt.Errorf("%s: timed out after %s", line, runTimeout)
	}
	if err != nil {
		return string(out), fmt.Errorf("%s: %w", line, err)
	}
	return string(out), nil
}

// rewriteLine replaces the first occurrence of from on the 1-based line of the
// file at p with to. Every other byte of the file, line endings and BOM
// included, stays as it was.
func rewriteLine(p string, line int, from, to string) error {
	if line < 1 {
		return fmt.Errorf("%s: no line %d", p, line)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	start := 0
	for n := 1; n < line; n++ {
		i := bytes.IndexByte(b[start:], '\n')
		if i < 0 {
			return fmt.Errorf("%s: no line %d", p, line)
		}
		start += i + 1
	}
	end := len(b)
	if i := bytes.IndexByte(b[start:], '\n'); i >= 0 {
		end = start + i
	}
	i := bytes.Index(b[start:end], []byte(from))
	if from == "" || i < 0 {
		return fmt.Errorf("%s:%d: %q not found", p, line, from)
	}
	return writeKeepMode(p, splice(b, start+i, start+i+len(from), to))
}

func readTOML(p string, v any) error {
	if _, err := toml.DecodeFile(p, v); err != nil {
		return fmt.Errorf("%s: %w", p, err)
	}
	return nil
}

func readJSON(p string, v any) error {
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", p, err)
	}
	return nil
}

func readYAML(p string, v any) error {
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", p, err)
	}
	return nil
}

// tomlLine is a TOML line and the table it sits in. The decoder does not
// report positions, and Bump needs the line a key or a string was written on.
type tomlLine struct {
	table string
	text  string
}

func tomlScan(src []byte) []tomlLine {
	var res []tomlLine
	table := ""
	for _, ln := range strings.Split(string(src), "\n") {
		ln = strings.TrimRight(ln, "\r")
		if h, ok := tomlTable(strings.TrimSpace(ln)); ok {
			table = h
		}
		res = append(res, tomlLine{table, ln})
	}
	return res
}

// tomlTable reads [a.b] and [[a.b]] headers, quoted keys included, as a
// dotted name.
func tomlTable(t string) (string, bool) {
	if i := strings.Index(t, "#"); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	if !strings.HasPrefix(t, "[") || !strings.HasSuffix(t, "]") {
		return "", false
	}
	t = strings.Trim(t, "[]")
	if t == "" || strings.ContainsAny(t, "[],=") {
		return "", false
	}
	parts := strings.Split(t, ".")
	for i, p := range parts {
		parts[i] = strings.Trim(strings.TrimSpace(p), `"'`)
	}
	return strings.Join(parts, "."), true
}

// tomlFindKey returns the index of the line that sets key in table, comparing
// names the PEP 503 way, or -1.
func tomlFindKey(lines []tomlLine, table, key string) int {
	for i, l := range lines {
		if l.table != table {
			continue
		}
		k, _, ok := strings.Cut(strings.TrimSpace(l.text), "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" || k[0] == '[' {
			continue
		}
		if q := k[0]; q == '"' || q == '\'' {
			if len(k) < 2 || k[len(k)-1] != q {
				continue
			}
			k = k[1 : len(k)-1]
		}
		if PyNorm(k) == PyNorm(key) {
			return i
		}
	}
	return -1
}

// tomlFindString finds s written as a quoted string in table at or after line
// index from, and returns the line index and the literal with its quotes.
func tomlFindString(lines []tomlLine, table string, from int, s string) (int, string) {
	for i := from; i < len(lines); i++ {
		if lines[i].table != table {
			continue
		}
		for _, q := range []string{`"`, `'`} {
			if lit := q + s + q; strings.Contains(lines[i].text, lit) {
				return i, lit
			}
		}
	}
	return -1, ""
}

func tomlDecode(src []byte, v any) error { return toml.Unmarshal(src, v) }

// githubRepo turns the many ways registries spell a GitHub repository into
// https://github.com/owner/repo, or "" when s does not point at GitHub.
func githubRepo(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "scm:")
	s = strings.TrimPrefix(s, "git:")
	s = strings.TrimPrefix(s, "git+")
	switch {
	case strings.HasPrefix(s, "github:"):
		s = "https://github.com/" + strings.TrimPrefix(s, "github:")
	case strings.HasPrefix(s, "git@github.com:"):
		s = "https://github.com/" + strings.TrimPrefix(s, "git@github.com:")
	case !strings.Contains(s, ":") && strings.Count(s, "/") == 1:
		s = "https://github.com/" + s
	case strings.HasPrefix(s, "//"):
		s = "https:" + s
	}
	u, err := url.Parse(s)
	if err != nil || strings.TrimPrefix(u.Hostname(), "www.") != "github.com" {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[0] == "sponsors" || parts[0] == "orgs" {
		return ""
	}
	repo := strings.TrimSuffix(parts[1], ".git")
	if repo == "" {
		return ""
	}
	return "https://github.com/" + parts[0] + "/" + repo
}

// runIn keeps the end of the output in the error: that is where a native
// tool says why it failed.
func runIn(ctx context.Context, root, dir string, env []string, line string) error {
	out, err := run(ctx, filepath.Join(root, filepath.FromSlash(dir)), env, line)
	if err != nil {
		return fmt.Errorf("%w\n%s", err, lastLines(out, 20))
	}
	return nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func onDisk(root, rel string) bool {
	fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil && fi.Mode().IsRegular()
}

func lineOf(src []byte, off int) int { return 1 + bytes.Count(src[:off], []byte{'\n'}) }

func splice(src []byte, start, end int, s string) []byte {
	res := make([]byte, 0, len(src)-(end-start)+len(s))
	res = append(res, src[:start]...)
	res = append(res, s...)
	return append(res, src[end:]...)
}

func writeKeepMode(p string, b []byte) error {
	fi, err := os.Stat(p)
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, fi.Mode().Perm())
}

// fileSnap holds file contents from before a bump so the bump can report
// exactly which files it changed.
type fileSnap map[string][]byte

func snapFiles(root string, files ...string) fileSnap {
	s := fileSnap{}
	for _, f := range files {
		if f == "" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
		if err != nil {
			b = nil
		}
		s[f] = b
	}
	return s
}

// restore puts every file back as it was, removing those that were not there.
func (s fileSnap) restore(root string) error {
	var errs []error
	for f, before := range s {
		p := filepath.Join(root, filepath.FromSlash(f))
		if before == nil {
			if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
			continue
		}
		if err := writeKeepMode(p, before); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s fileSnap) changed(root string) []string {
	var res []string
	for f, before := range s {
		after, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
		if !bytes.Equal(before, after) {
			res = append(res, f)
		}
	}
	slices.Sort(res)
	return res
}

var (
	shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)
	repoSSH   = regexp.MustCompile(`^(?:ssh://)?git@([^:/]+)[:/](.+)$`)
)

func shArg(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func inDir(dir, line string) string {
	if dir == "." || dir == "" {
		return line
	}
	return "cd " + shArg(dir) + " && " + line
}

func cleanRepo(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "git+")
	if m := repoSSH.FindStringSubmatch(s); m != nil {
		s = "https://" + m[1] + "/" + m[2]
	}
	s = strings.NewReplacer("git://", "https://", "http://", "https://").Replace(s)
	s = strings.TrimSuffix(s, ".git")
	return strings.TrimRight(s, "/")
}

func isPre(v string) bool {
	p, ok := ver.Parse(v)
	return ok && p.Pre != ""
}

// semverPre is the SemVer 2.0 rule crates.io and NuGet follow: any label after
// a hyphen makes a pre-release, whatever it says (4.4.1-dependabot-02442).
func semverPre(v string) bool {
	head, _, _ := strings.Cut(v, "+")
	return strings.Contains(head, "-")
}

// sortReleases orders ascending by version. Equal versions, such as the tags
// "v4" and "v4.0.0", put the shorter spelling first.
func sortReleases(rs []core.Release) {
	slices.SortStableFunc(rs, func(a, b core.Release) int {
		switch {
		case ver.Less(a.Version, b.Version):
			return -1
		case ver.Less(b.Version, a.Version):
			return 1
		}
		return strings.Compare(a.Version, b.Version)
	})
}

// getDoc decodes the JSON at url. found is false on a 404; other failures are errors.
func getDoc(ctx context.Context, f core.Fetcher, url string, v any, opts ...core.FetchOpt) (found bool, err error) {
	res, err := f.Get(ctx, url, opts...)
	if err != nil {
		return false, err
	}
	switch {
	case res.Status == 404:
		return false, nil
	case res.Status != 200:
		return false, fmt.Errorf("%s: HTTP %d", url, res.Status)
	}
	b := res.Body
	if len(b) > 2 && b[0] == 0x1f && b[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return false, fmt.Errorf("%s: %w", url, err)
		}
		if b, err = io.ReadAll(zr); err != nil {
			return false, fmt.Errorf("%s: %w", url, err)
		}
	}
	if err := json.Unmarshal(b, v); err != nil {
		return false, fmt.Errorf("%s: %w", url, err)
	}
	return true, nil
}

// learned holds what Deps learned for the calls that get no root: Meta, Bump and Imports.
type learned[T comparable] struct {
	mu sync.Mutex
	m  map[string][]T
}

func (s *learned[T]) add(key string, vals ...T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string][]T{}
	}
	for _, v := range vals {
		if !slices.Contains(s.m[key], v) {
			s.m[key] = append(s.m[key], v)
		}
	}
}

func (s *learned[T]) get(key string) []T {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.m[key])
}
