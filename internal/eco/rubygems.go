package eco

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
)

type rubygems struct{}

func (rubygems) Name() string { return core.Gems }

// gemLocked remembers every version Gemfile.lock pinned, so Meta can tell which
// of them the registry no longer lists.
var gemLocked learned[string]

func (rubygems) Detect(root string) ([]core.Manifest, error) {
	files, err := walk(root, "Gemfile", "*.gemspec")
	if err != nil {
		return nil, err
	}
	res := make([]core.Manifest, 0, len(files))
	for _, f := range files {
		lock := path.Join(path.Dir(f), "Gemfile.lock")
		if !onDisk(root, lock) {
			lock = ""
		}
		res = append(res, core.Manifest{Ecosystem: core.Gems, Path: f, Lock: lock, Tool: "bundle"})
	}
	return res, nil
}

type gemLit struct {
	line  int
	quote byte
	text  string
}

type gemEntry struct {
	name   string
	line   int
	lits   []gemLit // the name first, then the requirement strings
	groups []string
	skip   bool // from git, a path or another source
}

func (e gemEntry) reqs() []string {
	var res []string
	for _, l := range e.lits[1:] {
		res = append(res, l.text)
	}
	return res
}

func (e gemEntry) dev() bool {
	for _, g := range e.groups {
		if g != "test" && g != "development" {
			return false
		}
	}
	return len(e.groups) > 0
}

type gemBlock struct {
	groups []string
	skip   bool
}

var (
	gemQuoted  = regexp.MustCompile(`(['"])([^'"]*)['"]`)
	gemReqText = regexp.MustCompile(`^\s*(?:~>|>=|<=|!=|=|>|<)?\s*\d`)
	gemStart   = regexp.MustCompile(`^gem[\s(]`)
	gemOpener  = regexp.MustCompile(`^(\w+)\b.*\bdo(?:\s*\|[^|]*\|)?$`)
	gemCond    = regexp.MustCompile(`^(?:if|unless|case|begin|while|until|for)\b`)
	gemGroup   = regexp.MustCompile(`(?:\bgroups?\s*:|:groups?\s*=>)\s*(\[[^\]]*\]|:\w+|["']\w+["'])`)
	gemSource  = regexp.MustCompile(`(?:\b(?:git|github|gist|bitbucket|path|source)\s*:|:(?:git|github|gist|bitbucket|path|source)\s*=>)`)
	gemWord    = regexp.MustCompile(`\w+`)
)

// blank replaces a trailing comment with spaces so offsets in the line hold.
func blank(l string) string {
	var quote byte
	for i := 0; i < len(l); i++ {
		switch c := l[i]; {
		case quote != 0 && c == quote:
			quote = 0
		case quote != 0:
		case c == '"' || c == '\'':
			quote = c
		case c == '#':
			return l[:i] + strings.Repeat(" ", len(l)-i)
		}
	}
	return l
}

func words(s string) []string { return gemWord.FindAllString(s, -1) }

func gemfile(lines []string) []gemEntry {
	var res []gemEntry
	var stack []gemBlock
	for i := 0; i < len(lines); i++ {
		text := strings.TrimSpace(blank(lines[i]))
		switch {
		case text == "end":
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case gemStart.MatchString(text):
			end := i
			for strings.HasSuffix(strings.TrimSpace(blank(lines[end])), ",") && end+1 < len(lines) {
				end++
			}
			if e, ok := gemStatement(lines, i, end, stack); ok {
				res = append(res, e)
			}
			i = end
		case gemOpener.MatchString(text):
			stack = append(stack, gemOpen(text))
		case gemCond.MatchString(text):
			stack = append(stack, gemBlock{})
		}
	}
	return res
}

func gemOpen(text string) gemBlock {
	switch kw := gemOpener.FindStringSubmatch(text)[1]; kw {
	case "group":
		return gemBlock{groups: words(strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(text, "group")), "do"))}
	case "git", "path", "source", "github":
		return gemBlock{skip: true}
	}
	return gemBlock{}
}

func gemStatement(lines []string, start, end int, stack []gemBlock) (gemEntry, bool) {
	var stmt strings.Builder
	offs := make([]int, 0, end-start+1)
	for i := start; i <= end; i++ {
		offs = append(offs, stmt.Len())
		stmt.WriteString(blank(lines[i]))
		stmt.WriteByte('\n')
	}
	s := stmt.String()
	at := func(off int) int {
		n := 0
		for n+1 < len(offs) && offs[n+1] <= off {
			n++
		}
		return start + n + 1
	}
	e := gemEntry{line: start + 1}
	prev := 0
	for _, m := range gemQuoted.FindAllStringSubmatchIndex(s, -1) {
		text := s[m[4]:m[5]]
		if len(e.lits) > 0 && (strings.Trim(s[prev:m[0]], " \t\r\n,") != "" || !gemReqText.MatchString(text)) {
			break
		}
		e.lits = append(e.lits, gemLit{at(m[0]), s[m[2]], text})
		prev = m[1]
	}
	if len(e.lits) == 0 {
		return e, false
	}
	e.name = e.lits[0].text
	opts := s[prev:]
	for _, b := range stack {
		e.groups = append(e.groups, b.groups...)
		e.skip = e.skip || b.skip
	}
	if m := gemGroup.FindStringSubmatch(opts); m != nil {
		e.groups = append(e.groups, words(m[1])...)
	}
	e.skip = e.skip || gemSource.MatchString(opts)
	return e, true
}

var (
	gemLockEntry = regexp.MustCompile(`^    ([^\s(]+) \(([^)]+)\)\s*$`)
	gemspecDep   = regexp.MustCompile(`\.add_(runtime_|development_)?dependency\b\s*\(?\s*(['"])([^'"]+)['"]([^\n]*)`)
)

type gemSpec struct {
	name, version string
	requires      []string
}

// gemLock reads the specs of Gemfile.lock's GEM section: "    name (version)",
// with what it requires one level deeper. A platform suffix on the version
// is dropped.
func gemLock(src []byte) []gemSpec {
	var res []gemSpec
	section, specs := "", false
	for _, l := range strings.Split(string(src), "\n") {
		l = strings.TrimRight(l, "\r")
		switch {
		case l != "" && l[0] != ' ':
			section, specs = l, false
		case strings.TrimSpace(l) == "specs:":
			specs = true
		case section != "GEM" || !specs:
		case strings.HasPrefix(l, "      ") && len(res) > 0:
			name, _, _ := strings.Cut(strings.TrimSpace(l), " ")
			res[len(res)-1].requires = append(res[len(res)-1].requires, name)
		default:
			if m := gemLockEntry.FindStringSubmatch(l); m != nil {
				v, _, _ := strings.Cut(m[2], "-")
				res = append(res, gemSpec{name: m[1], version: v})
			}
		}
	}
	return res
}

func (rubygems) Deps(root string, m core.Manifest) ([]core.Dep, error) {
	p := filepath.Join(root, filepath.FromSlash(m.Path))
	lines, err := readLines(p)
	if err != nil {
		return nil, err
	}
	ls, err := readLocked(root, m.Lock)
	if err != nil {
		return nil, err
	}
	locked := map[string]string{}
	for _, l := range ls {
		locked[l.Name] = l.Version
		gemLocked.add(l.Name, l.Version)
	}
	var res []core.Dep
	direct := map[string]bool{}
	add := func(name string, reqs []string, line int, dev bool) {
		direct[name] = true
		res = append(res, core.Dep{
			Ecosystem: core.Gems, Name: name, Version: locked[name], Req: strings.Join(reqs, ", "),
			Manifest: m, Line: line, Dev: dev, Direct: true,
		})
	}
	if strings.HasSuffix(m.Path, ".gemspec") {
		for i, l := range lines {
			if g := gemspecDep.FindStringSubmatch(blank(l)); g != nil {
				var reqs []string
				for _, q := range gemQuoted.FindAllStringSubmatch(g[4], -1) {
					reqs = append(reqs, q[2])
				}
				add(g[3], reqs, i+1, g[1] == "development_")
			}
		}
		return res, nil
	}
	for _, e := range gemfile(lines) {
		if !e.skip {
			add(e.name, e.reqs(), e.line, e.dev())
		}
	}
	var rest []core.Dep
	for _, l := range ls {
		if !direct[l.Name] {
			rest = append(rest, core.Dep{Ecosystem: core.Gems, Name: l.Name, Version: l.Version, Manifest: m})
			direct[l.Name] = true
		}
	}
	sortDeps(rest)
	return append(res, rest...), nil
}

func (rubygems) Meta(ctx context.Context, f core.Fetcher, name string) (*core.Meta, error) {
	var list []struct {
		Number     string    `json:"number"`
		CreatedAt  time.Time `json:"created_at"`
		Prerelease bool      `json:"prerelease"`
		Platform   string    `json:"platform"`
	}
	ok, err := getDoc(ctx, f, "https://rubygems.org/api/v1/versions/"+name+".json", &list)
	if err != nil {
		return nil, fmt.Errorf("rubygems: %s: %w", name, err)
	}
	if !ok {
		return nil, fmt.Errorf("rubygems: %s: not found on rubygems.org", name)
	}
	res := &core.Meta{Name: name}
	listed := map[string]bool{}
	for _, v := range list {
		listed[v.Number] = true
		if v.Platform == "ruby" || v.Platform == "" {
			res.Releases = append(res.Releases, core.Release{Version: v.Number, Time: v.CreatedAt, Pre: v.Prerelease})
		}
	}
	// the versions list only has what is installable; a pinned version missing from it was yanked
	for _, v := range gemLocked.get(name) {
		if !listed[v] {
			res.Releases = append(res.Releases, core.Release{Version: v, Yanked: true})
		}
	}
	sortReleases(res.Releases)
	var info struct {
		Source   string `json:"source_code_uri"`
		Homepage string `json:"homepage_uri"`
	}
	if ok, err := getDoc(ctx, f, "https://rubygems.org/api/v1/gems/"+name+".json", &info); err == nil && ok {
		res.Repo = cleanRepo(info.Source)
		if res.Repo == "" {
			res.Repo = cleanRepo(info.Homepage)
		}
	}
	return res, nil
}

func (rubygems) Bump(ctx context.Context, root string, d core.Dep, to, toName string) (*core.Bump, error) {
	if strings.HasSuffix(d.Manifest.Path, ".gemspec") {
		return nil, fmt.Errorf("rubygems: %s: gemspec is read-only: %w", d.Name, core.ErrNotSupported)
	}
	res := &core.Bump{}
	if d.Direct {
		changed, err := gemEdit(root, d, to, toName)
		if err != nil {
			return nil, err
		}
		if changed {
			res.Files = append(res.Files, d.Manifest.Path)
		}
	} else if toName != "" {
		return nil, fmt.Errorf("rubygems: %s: cannot swap a transitive gem: %w", d.Name, core.ErrNotSupported)
	}
	switch {
	case d.Manifest.Lock == "":
		res.Note = "no Gemfile.lock"
		return res, nil
	case !have("bundle"):
		res.Note = core.ToolMissing{Tool: "bundle"}.Error()
		return res, nil
	}
	line := "bundle lock --update " + shArg(d.Name) + " --conservative"
	if toName != "" {
		line = "bundle lock"
	}
	name := cmp.Or(toName, d.Name)
	if err := gemLockAt(ctx, root, d, name, to, line); err != nil {
		return nil, fmt.Errorf("rubygems: %s: %w", d.Name, err)
	}
	if err := lockLanded(root, d.Manifest.Lock, core.Gems, name, to); err != nil {
		return nil, fmt.Errorf("rubygems: %w", err)
	}
	res.Locked = true
	res.Files = append(res.Files, d.Manifest.Lock)
	return res, nil
}

// gemLockAt locks name at exactly to. Bundler takes the newest release the
// Gemfile allows, which may be past the one tend chose, and takes no version
// on the command line: so the Gemfile asks for "= to" for one pass, then goes
// back to what it said, which the locked to satisfies and the next pass keeps.
func gemLockAt(ctx context.Context, root string, d core.Dep, name, to, line string) error {
	p := filepath.Join(root, filepath.FromSlash(d.Manifest.Path))
	src, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	pinned, err := gemPin(src, d, name, to)
	if err != nil {
		return err
	}
	if err := writeKeepMode(p, pinned); err != nil {
		return err
	}
	dir := path.Dir(d.Manifest.Path)
	err = runIn(ctx, root, dir, nil, line)
	if werr := writeKeepMode(p, src); err == nil {
		err = werr
	}
	if err != nil {
		return err
	}
	return runIn(ctx, root, dir, nil, "bundle lock")
}

// gemPin is the Gemfile src asking for name at exactly to: the gem statement
// at d.Line gets the requirement, and a gem the Gemfile does not name gets a
// statement of its own.
func gemPin(src []byte, d core.Dep, name, to string) ([]byte, error) {
	exact := "= " + to
	if !d.Direct {
		if len(src) > 0 && src[len(src)-1] != '\n' {
			src = append(src, '\n')
		}
		return append(src, fmt.Sprintf("gem %q, %q\n", name, exact)...), nil
	}
	lines := strings.Split(string(src), "\n")
	gems := gemfile(lines)
	i := slices.IndexFunc(gems, func(e gemEntry) bool { return e.line == d.Line && e.name == name })
	if i < 0 {
		return nil, fmt.Errorf("no gem statement at %s:%d", d.Manifest.Path, d.Line)
	}
	l := gems[i].lits[0]
	q := string(l.quote)
	from, next := q+l.text+q, q+l.text+q+", "+q+exact+q
	if len(gems[i].lits) > 1 {
		// The other requirements already admit to.
		l = gems[i].lits[1]
		q = string(l.quote)
		from, next = q+l.text+q, q+exact+q
	}
	text := lines[l.line-1]
	at := strings.Index(text, from)
	if at < 0 {
		return nil, fmt.Errorf("%s:%d: %q not found", d.Manifest.Path, l.line, from)
	}
	lines[l.line-1] = text[:at] + next + text[at+len(from):]
	return []byte(strings.Join(lines, "\n")), nil
}

func gemEdit(root string, d core.Dep, to, toName string) (bool, error) {
	p := filepath.Join(root, filepath.FromSlash(d.Manifest.Path))
	lines, err := readLines(p)
	if err != nil {
		return false, err
	}
	var e *gemEntry
	for _, c := range gemfile(lines) {
		if c.line == d.Line && c.name == d.Name {
			e = &c
			break
		}
	}
	if e == nil {
		return false, fmt.Errorf("rubygems: %s: no gem statement at %s:%d", d.Name, d.Manifest.Path, d.Line)
	}
	if toName != "" {
		return true, gemSwap(p, *e, to, toName)
	}
	if len(e.lits) == 1 {
		return false, nil
	}
	old := strings.Join(e.reqs(), ", ")
	next, err := fixReq(old, to, false)
	if err != nil {
		return false, fmt.Errorf("rubygems: %s: %w", d.Name, err)
	}
	if next == old {
		return false, nil
	}
	for i, r := range strings.Split(next, ", ") {
		l := e.lits[i+1]
		if r == l.text {
			continue
		}
		q := string(l.quote)
		if err := rewriteLine(p, l.line, q+l.text+q, q+r+q); err != nil {
			return false, err
		}
	}
	return true, nil
}

func gemSwap(p string, e gemEntry, to, toName string) error {
	if len(e.lits) > 2 {
		return fmt.Errorf("rubygems: %s: cannot swap several requirements: %w", e.name, core.ErrNotSupported)
	}
	for i, l := range e.lits {
		q := string(l.quote)
		next := toName
		if i == 1 {
			next = "~> " + shape(to, 2)
		}
		if err := rewriteLine(p, l.line, q+l.text+q, q+next+q); err != nil {
			return err
		}
	}
	return nil
}

func (rubygems) Commands(root string, m core.Manifest) core.Commands {
	dir := path.Dir(m.Path)
	res := core.Commands{Install: []string{inDir(dir, "bundle install")}}
	switch spec, err := os.Stat(filepath.Join(root, filepath.FromSlash(path.Join(dir, "spec")))); {
	case err == nil && spec.IsDir():
		res.Test = []string{inDir(dir, "bundle exec rspec")}
	case onDisk(root, path.Join(dir, "Rakefile")):
		res.Test = []string{inDir(dir, "bundle exec rake test")}
	}
	return res
}

var gemConst = map[string]string{
	"rails":         "Rails",
	"railties":      "Rails",
	"activerecord":  "ActiveRecord",
	"activesupport": "ActiveSupport",
	"activemodel":   "ActiveModel",
	"activejob":     "ActiveJob",
	"actionpack":    "ActionPack",
	"actionmailer":  "ActionMailer",
	"octokit":       "Octokit",
	"pg":            "PG",
	"httparty":      "HTTParty",
	"rspec":         "RSpec",
	"rspec-rails":   "RSpec::Rails",
	"rest-client":   "RestClient",
	"json":          "JSON",
	"webmock":       "WebMock",
	"vcr":           "VCR",
	"twilio-ruby":   "Twilio",
	"sendgrid-ruby": "SendGrid",
	"sentry-ruby":   "Sentry",
	"jwt":           "JWT",
	"aws-sdk":       "Aws",
}

func (rubygems) Traits() core.Traits {
	return core.Traits{OSV: "RubyGems", Page: prefix("https://rubygems.org/gems/"), Transitive: true}
}

func (rubygems) Imports(d core.Dep) []string {
	if c, ok := gemConst[d.Name]; ok {
		return []string{c}
	}
	segs := strings.Split(d.Name, "-")
	if len(segs) > 2 && segs[0] == "aws" && segs[1] == "sdk" {
		segs = append([]string{"aws"}, segs[2:]...)
	}
	for i, s := range segs {
		segs[i] = gemCamel(s)
	}
	return []string{strings.Join(segs, "::")}
}

func gemCamel(s string) string {
	var b strings.Builder
	for _, p := range strings.Split(s, "_") {
		if p != "" {
			b.WriteString(strings.ToUpper(p[:1]) + p[1:])
		}
	}
	return b.String()
}
