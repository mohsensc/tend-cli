package verify

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/eco"
)

// loud are the switches that make each runtime say what is deprecated while
// the project's commands run: a stack trace with Node's, every warning once
// with Python's, Ruby's deprecation category.
var loud = []struct{ name, value string }{
	{"NODE_OPTIONS", "--trace-deprecation"},
	{"PYTHONWARNINGS", "default::DeprecationWarning,default::PendingDeprecationWarning"},
	{"RUBYOPT", "-W:deprecated"},
}

// warningsOn adds the switches to env, after whatever the variable already held.
func warningsOn(env []string) []string {
	res := slices.Clone(env)
	for _, l := range loud {
		i := slices.IndexFunc(res, func(kv string) bool { return strings.HasPrefix(kv, l.name+"=") })
		if i < 0 {
			res = append(res, l.name+"="+l.value)
			continue
		}
		sep := " "
		if l.name == "PYTHONWARNINGS" {
			sep = ","
		}
		res[i] += sep + l.value
	}
	return res
}

var (
	pyWarning   = regexp.MustCompile(`^(.+?\.py):(\d+): (?:\w*Deprecation\w*Warning): (.+)$`)
	nodeWarning = regexp.MustCompile(`^\(node:\d+\) (?:\[(\w+)\] )?DeprecationWarning: (.+)$`)
	nodeFrame   = regexp.MustCompile(`^\s+at (?:.*?\()?(.+?):(\d+):\d+\)?$`)
	rbWarning   = regexp.MustCompile(`^(.+?\.rb):(\d+): warning: (.*deprecated.*)$`)
	phpWarning  = regexp.MustCompile(`Deprecated: (.+) in (.+?\.php) on line (\d+)`)
	csWarning   = regexp.MustCompile(`^(.+?\.cs)\((\d+),\d+\): warning (?:CS0618|CS0612): (.+?)(?: \[.*\])?$`)
	javaWarning = regexp.MustCompile(`^\[WARNING\] (.+?\.java):\[(\d+),\d+\] \[deprecation\] (.+)$`)
	goWarning   = regexp.MustCompile(`^(.+?\.go):(\d+):\d+: (.+ (?:is|has been) deprecated.*?)\s*(?:\(SA1019\))?$`)
	nodeModule  = regexp.MustCompile(`node_modules/((?:@[^/]+/)?[^/]+)/`)
	nodeCore    = regexp.MustCompile(`^DEP\d{4}$`)
)

// runtime marks a warning about the runtime's own library rather than a
// package: Go's standard library, a Node core API.
const runtime = "runtime:"

// Warnings reads the deprecations out of the logs a run wrote. A warning
// counts when it points into the project's own files; one inside an
// installed package is that package's business.
func Warnings(run *core.Run, dir string) []core.Warning {
	var res []core.Warning
	for _, st := range run.Steps {
		if st.Log == "" {
			continue
		}
		f, err := os.Open(st.Log)
		if err != nil {
			continue
		}
		res = append(res, scanWarnings(bufio.NewScanner(f), dir)...)
		f.Close()
	}
	return tidy(res)
}

func tidy(ws []core.Warning) []core.Warning {
	slices.SortFunc(ws, func(a, b core.Warning) int {
		return strings.Compare(fmt.Sprintf("%s:%09d:%s", a.File, a.Line, a.Msg), fmt.Sprintf("%s:%09d:%s", b.File, b.Line, b.Msg))
	})
	return slices.Compact(ws)
}

// Listener is a command run after the project's own only for what it says
// is deprecated, from Dir, a directory of the worktree.
type Listener struct {
	Dir, Line string
}

// Listeners are staticcheck's deprecation check for each Go module, where
// staticcheck is installed: Go prints nothing about deprecations while it
// builds and tests.
func Listeners(manifests []core.Manifest) []Listener {
	if _, err := lookPath("staticcheck"); err != nil {
		return nil
	}
	var res []Listener
	for _, m := range byDepth(manifests, core.Go) {
		res = append(res, Listener{Dir: path.Dir(m.Path), Line: "staticcheck -checks SA1019 ./..."})
	}
	return res
}

// listen runs the listeners and reads their output. Their exit status means
// nothing here: a linter exits non-zero when it has something to say.
func listen(ctx context.Context, dir string, env []string, ls []Listener) []core.Warning {
	var res []core.Warning
	for _, l := range ls {
		var out bytes.Buffer
		at := filepath.Join(dir, filepath.FromSlash(l.Dir))
		execute(ctx, at, env, step{cmd: l.Line, argv: []string{"sh", "-c", l.Line}}, &capped{w: &out, n: 1 << 20})
		for _, w := range scanWarnings(bufio.NewScanner(&out), at) {
			w.File = path.Join(l.Dir, w.File)
			res = append(res, w)
		}
	}
	return res
}

// capped keeps the first n bytes written to it and drops the rest.
type capped struct {
	w io.Writer
	n int
}

func (c *capped) Write(b []byte) (int, error) {
	if k := min(len(b), c.n); k > 0 {
		c.n -= k
		if _, err := c.w.Write(b[:k]); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}

func scanWarnings(sc *bufio.Scanner, dir string) []core.Warning {
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var res []core.Warning
	add := func(file, line, pkg, msg string) {
		rel, ok := own(file, dir)
		n, _ := strconv.Atoi(line)
		if ok && n > 0 {
			res = append(res, core.Warning{File: rel, Line: n, Pkg: pkg, Msg: strings.TrimSpace(msg)})
		}
	}
	var node *core.Warning // a Node warning waiting for its stack
	for sc.Scan() {
		l := strings.TrimRight(sc.Text(), "\r")
		if node != nil {
			m := nodeFrame.FindStringSubmatch(l)
			if m == nil {
				node = nil
			} else if p := nodeModule.FindStringSubmatch(m[1]); p != nil {
				node.Pkg = p[1]
			} else if !strings.HasPrefix(m[1], "node:") {
				add(m[1], m[2], node.Pkg, node.Msg)
				node = nil
			}
			continue
		}
		if m := nodeWarning.FindStringSubmatch(l); m != nil {
			node = &core.Warning{Msg: m[2]}
			if nodeCore.MatchString(m[1]) {
				node.Pkg = runtime + "nodejs"
			}
			continue
		}
		for _, re := range []*regexp.Regexp{pyWarning, rbWarning, csWarning, javaWarning, goWarning} {
			if m := re.FindStringSubmatch(l); m != nil {
				pkg := ""
				if re == goWarning && strings.Contains(m[3], " since Go 1.") {
					pkg = runtime + "go"
				}
				add(m[1], m[2], pkg, m[3])
				break
			}
		}
		if m := phpWarning.FindStringSubmatch(l); m != nil {
			add(m[2], m[3], "", m[1])
		}
	}
	return res
}

// own makes file relative to the worktree, and refuses what is not the
// project's: anything outside it, installed packages, the runtime's own code.
func own(file, dir string) (string, bool) {
	file = filepath.ToSlash(file)
	for _, d := range []string{dir, realDir(dir)} {
		if rel, ok := strings.CutPrefix(file, filepath.ToSlash(d)+"/"); ok {
			file = rel
		}
	}
	if filepath.IsAbs(filepath.FromSlash(file)) || strings.HasPrefix(file, "..") || strings.HasPrefix(file, "<") {
		return "", false
	}
	for _, part := range strings.Split(file, "/") {
		switch part {
		case "node_modules", "site-packages", "dist-packages", "vendor", ".venv", "venv":
			return "", false
		}
	}
	return file, true
}

func realDir(dir string) string {
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		return r
	}
	return dir
}

// Runtime turns the warnings the base commit's run printed into changes. A
// warning belongs to the dependency the output names, else to the one whose
// call sits on that line, and each dependency with warnings gets one
// deprecation: its version stays, its calls move.
type Runtime struct {
	Warnings []core.Warning
}

var _ core.Source = (*Runtime)(nil)

func (r *Runtime) Name() string { return "runtime" }

func (r *Runtime) Changes(ctx context.Context, inv *core.Inventory) ([]core.Change, error) {
	if len(r.Warnings) == 0 || inv.Index == nil {
		return nil, nil
	}
	byDep := map[int]*core.Change{}
	var order []int
	uses := map[int][]core.Site{}
	for _, w := range r.Warnings {
		i, site := r.owner(inv, w, uses)
		if i < 0 {
			continue
		}
		ch := byDep[i]
		if ch == nil {
			d := inv.Deps[i]
			ch = &core.Change{Kind: core.Deprecation, Deps: []core.Dep{d}, To: d.Version}
			byDep[i] = ch
			order = append(order, i)
		}
		ev := core.Evidence{Source: "runtime", Title: ch.Deps[0].Name + " warns that something the code calls is deprecated", Text: w.Msg}
		if !slices.ContainsFunc(ch.Evidence, func(e core.Evidence) bool { return e.Text == w.Msg }) {
			ch.Evidence = append(ch.Evidence, ev)
		}
		if slices.ContainsFunc(ch.Sites, func(s core.Site) bool { return s.File == site.File && s.Line == site.Line }) {
			continue
		}
		if site.Text == "" {
			site.Text = lineText(inv.Root, site.File, site.Line)
		}
		ch.Sites = append(ch.Sites, site)
	}
	var res []core.Change
	for _, i := range order {
		ch := byDep[i]
		ch.ID = core.ChangeID(ch)
		res = append(res, *ch)
	}
	return res, nil
}

// lineText is what a warning's line says, for the PR to show.
func lineText(root, file string, n int) string {
	if root == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if err != nil {
		return ""
	}
	lines := strings.Split(string(b), "\n")
	if n < 1 || n > len(lines) {
		return ""
	}
	return strings.TrimSpace(lines[n-1])
}

// owner finds the direct dependency a warning is about and the call it
// points at. A warning about the runtime's own library belongs to the
// runtime the project declares. uses keeps each dependency's sites across
// warnings.
func (r *Runtime) owner(inv *core.Inventory, w core.Warning, uses map[int][]core.Site) (int, core.Site) {
	at := core.Site{File: w.File, Line: w.Line}
	if product, ok := strings.CutPrefix(w.Pkg, runtime); ok {
		for i, d := range inv.Deps {
			if d.Ecosystem == core.Toolchain && d.Name == product {
				return i, at
			}
		}
		return -1, at
	}
	for i, d := range inv.Deps {
		if !d.Direct {
			continue
		}
		e := eco.ByName(d.Ecosystem)
		if e == nil {
			continue
		}
		if _, ok := uses[i]; !ok {
			uses[i] = inv.Index.Uses(e.Imports(d))
		}
		for _, s := range uses[i] {
			if s.File == w.File && s.Line == w.Line {
				return i, s
			}
		}
		if w.Pkg != "" && w.Pkg == d.Name {
			return i, at
		}
	}
	return -1, at
}
