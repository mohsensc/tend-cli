package eco

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/ver"
)

// toolchain reads the runtime versions a project pins for its developers and
// CI (.nvmrc, .python-version, .tool-versions, rust-toolchain, the toolchain
// line of go.mod) and the oldest ones it says it supports: engines.node,
// requires-python, the go line of go.mod, the Java release a pom compiles
// for. A dep is named for its endoflife.date product, which is where its news
// comes from; it has no registry of its own.
type toolchain struct{}

func (toolchain) Name() string { return core.Toolchain }

// pinFiles hold one version each; the value is the runtime's product.
var pinFiles = map[string]string{
	".nvmrc":             "nodejs",
	".node-version":      "nodejs",
	".python-version":    "python",
	".ruby-version":      "ruby",
	".go-version":        "go",
	".terraform-version": "terraform",
	"rust-toolchain":     "rust",
}

// asdfTools maps the tool names .tool-versions uses to products.
var asdfTools = map[string]string{
	"nodejs": "nodejs", "node": "nodejs", "python": "python", "ruby": "ruby",
	"golang": "go", "go": "go", "terraform": "terraform", "rust": "rust",
}

// minFiles are other ecosystems' manifests, read here only for the runtime
// they require.
var minFiles = []string{"package.json", "pyproject.toml", "setup.cfg", "pom.xml", "go.mod"}

var (
	toolVersion  = regexp.MustCompile(`^(v|go)?\d+(\.\d+)*$`)
	goToolchain  = regexp.MustCompile(`(?m)^toolchain\s+(go\d+(?:\.\d+)*)\s*$`)
	goLine       = regexp.MustCompile(`^go\s+(\d+(?:\.\d+)*)\s*(?://.*)?$`)
	rustChannel  = regexp.MustCompile(`(?m)^\s*channel\s*=\s*"(\d+(?:\.\d+)*)"`)
	toolSpelling = regexp.MustCompile(`^(v|go)?`)
	tomlSection  = regexp.MustCompile(`^\s*\[([^\]]+)\]`)
	pyRequires   = regexp.MustCompile(`^\s*requires-python\s*=\s*["']([^"']+)["']`)
	poetryPy     = regexp.MustCompile(`^\s*python\s*=\s*["']([^"']+)["']`)
	cfgRequires  = regexp.MustCompile(`^\s*python_requires\s*=\s*([^#;]+?)\s*$`)
	pomJava      = regexp.MustCompile(`<(java\.version|maven\.compiler\.(?:release|source|target))>\s*(\d+(?:\.\d+)*)\s*</`)
	bound        = regexp.MustCompile(`(<=|>=|<|>|\^|~=|~|===|==|!=|=)?\s*v?(\d+(?:\.\d+)*)`)
)

func (toolchain) Detect(root string) ([]core.Manifest, error) {
	names := append([]string{".tool-versions", "rust-toolchain.toml"}, minFiles...)
	for n := range pinFiles {
		names = append(names, n)
	}
	files, err := walk(root, names...)
	if err != nil {
		return nil, err
	}
	var res []core.Manifest
	for _, f := range files {
		if base := path.Base(f); slices.Contains(minFiles, base) {
			b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
			if err != nil || len(minimums(base, b)) == 0 && !goToolchain.Match(b) {
				continue
			}
		}
		m := core.Manifest{Ecosystem: core.Toolchain, Path: f, Tool: "toolchain"}
		for _, lock := range minLocks[path.Base(f)] {
			if l := path.Join(path.Dir(f), lock); onDisk(root, l) {
				m.Lock = l
				break
			}
		}
		res = append(res, m)
	}
	return res, nil
}

// minLocks are the lockfiles that record a manifest's runtime requirement.
var minLocks = map[string][]string{
	"package.json":   {"package-lock.json"},
	"pyproject.toml": {"uv.lock", "poetry.lock"},
}

func (toolchain) Deps(root string, m core.Manifest) ([]core.Dep, error) {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(m.Path)))
	if err != nil {
		return nil, err
	}
	dep := func(product, written string, line int) core.Dep {
		return core.Dep{
			Ecosystem: core.Toolchain, Name: product, Version: toolSpelling.ReplaceAllString(written, ""),
			Req: written, Manifest: m, Line: line, Direct: true,
		}
	}
	var res []core.Dep
	base := path.Base(m.Path)
	for _, x := range minimums(base, b) {
		if low := lowest(x.req); low != "" {
			d := dep(x.product, x.req, x.line)
			d.Version, d.Min = low, true
			res = append(res, d)
		}
	}
	lines := strings.Split(string(b), "\n")
	switch base {
	case "go.mod", "rust-toolchain.toml":
		re, product := goToolchain, "go"
		if base != "go.mod" {
			re, product = rustChannel, "rust"
		}
		for i, l := range lines {
			if s := re.FindStringSubmatch(l); s != nil {
				res = append(res, dep(product, s[1], i+1))
			}
		}
	case ".tool-versions":
		for i, l := range lines {
			f := strings.Fields(strings.SplitN(l, "#", 2)[0])
			if len(f) >= 2 && asdfTools[f[0]] != "" && toolVersion.MatchString(f[1]) {
				res = append(res, dep(asdfTools[f[0]], f[1], i+1))
			}
		}
	default:
		if pinFiles[base] == "" {
			break
		}
		for i, l := range lines {
			l = strings.TrimSpace(l)
			if l == "" || strings.HasPrefix(l, "#") {
				continue
			}
			// lts/*, system and the like follow upstream already.
			if toolVersion.MatchString(l) {
				res = append(res, dep(pinFiles[base], l, i+1))
			}
			break
		}
	}
	return res, nil
}

// minimum is a runtime requirement as one manifest line writes it.
type minimum struct {
	product, req string
	line         int
}

// minimums finds the runtime requirements in a manifest of another ecosystem.
func minimums(base string, b []byte) []minimum {
	lines := strings.Split(string(b), "\n")
	var res []minimum
	switch base {
	case "package.json":
		var doc struct {
			Engines map[string]any `json:"engines"`
		}
		if json.Unmarshal(b, &doc) != nil {
			return nil
		}
		req, ok := doc.Engines["node"].(string)
		if !ok || req == "" {
			return nil
		}
		if n := enginesLine(lines, req); n > 0 {
			res = append(res, minimum{"nodejs", req, n})
		}
	case "pyproject.toml", "setup.cfg":
		section := ""
		for i, l := range lines {
			if m := tomlSection.FindStringSubmatch(l); m != nil {
				section = strings.TrimSpace(m[1])
				continue
			}
			var m []string
			switch section {
			case "project":
				m = pyRequires.FindStringSubmatch(l)
			case "tool.poetry.dependencies":
				m = poetryPy.FindStringSubmatch(l)
			case "options":
				if base == "setup.cfg" {
					m = cfgRequires.FindStringSubmatch(l)
				}
			}
			if m != nil {
				res = append(res, minimum{"python", strings.TrimSpace(m[1]), i + 1})
			}
		}
	case "pom.xml":
		for i, l := range lines {
			if m := pomJava.FindStringSubmatch(l); m != nil {
				res = append(res, minimum{"java", m[2], i + 1})
			}
		}
	case "go.mod":
		for i, l := range lines {
			if m := goLine.FindStringSubmatch(strings.TrimSpace(l)); m != nil {
				res = append(res, minimum{"go", m[1], i + 1})
				break
			}
		}
	}
	return res
}

// enginesLine is the line of package.json that sets engines.node to req.
func enginesLine(lines []string, req string) int {
	in := false
	for i, l := range lines {
		if strings.Contains(l, `"engines"`) {
			in = true
		}
		if in && strings.Contains(l, `"node"`) && strings.Contains(l, jsonString(req)) {
			return i + 1
		}
	}
	return 0
}

// jsonString quotes s the way npm writes it, with > and < left alone.
func jsonString(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSpace(b.String())
}

// lowest is the oldest version a requirement admits: its lowest lower bound,
// "16.14.0" for ">=16.14.0" and "18" for "^18 || ^20".
func lowest(req string) string {
	res := ""
	for _, m := range bound.FindAllStringSubmatch(req, -1) {
		if m[1] == "<" || m[1] == "<=" || m[1] == "!=" {
			continue
		}
		if res == "" || ver.Less(m[2], res) {
			res = m[2]
		}
	}
	return res
}

// raise rewrites a requirement so its oldest admitted version is to: the
// lower bound moves and everything else stays. A range of alternatives
// becomes ">=to", since raising the floor drops the older ones.
func raise(req, low, to string) (string, error) {
	if strings.Contains(req, "||") || strings.Contains(req, " - ") {
		return ">=" + to, nil
	}
	at := -1
	for _, m := range bound.FindAllStringSubmatchIndex(req, -1) {
		op, v := req[max(m[2], 0):max(m[3], 0)], req[m[4]:m[5]]
		switch {
		case op == "<" && !ver.Less(to, v), op == "<=" && ver.Less(v, to):
			return "", fmt.Errorf("%q caps the version below %s", req, to)
		case op != "<" && op != "<=" && op != "!=" && v == low && at < 0:
			at = m[4]
		}
	}
	if at < 0 {
		return "", fmt.Errorf("no lower bound %s in %q", low, req)
	}
	return req[:at] + to + req[at+len(low):], nil
}

func (toolchain) Meta(ctx context.Context, f core.Fetcher, name string) (*core.Meta, error) {
	return nil, core.ErrNotSupported
}

// Bump writes the new version where the old one was, spelled the same way:
// with its v or go in front when it had one. A minimum keeps its operator,
// and a lockfile that records it moves with it.
func (toolchain) Bump(ctx context.Context, root string, d core.Dep, to, toName string) (*core.Bump, error) {
	if toName != "" {
		return nil, fmt.Errorf("toolchain: %s: %w", d.Name, core.ErrNotSupported)
	}
	p := filepath.Join(root, filepath.FromSlash(d.Manifest.Path))
	if !d.Min {
		lead := toolSpelling.FindString(d.Req)
		if err := rewriteLine(p, d.Line, d.Req, lead+toolSpelling.ReplaceAllString(to, "")); err != nil {
			return nil, fmt.Errorf("toolchain: %w", err)
		}
		return &core.Bump{Files: []string{d.Manifest.Path}, Note: "no lockfile"}, nil
	}
	next, err := raise(d.Req, d.Version, to)
	if err != nil {
		return nil, fmt.Errorf("toolchain: %s in %s: %w", d.Name, d.Manifest.Path, err)
	}
	if err := rewriteLine(p, d.Line, d.Req, next); err != nil {
		return nil, fmt.Errorf("toolchain: %w", err)
	}
	res := &core.Bump{Files: []string{d.Manifest.Path}, Note: "no lockfile"}
	lock, dir := d.Manifest.Lock, path.Dir(d.Manifest.Path)
	switch path.Base(lock) {
	case "package-lock.json":
		if lockEngines(filepath.Join(root, filepath.FromSlash(lock)), d.Req, next) == nil {
			res.Files, res.Locked, res.Note = append(res.Files, lock), true, ""
		}
	case "uv.lock", "poetry.lock":
		tool := strings.TrimSuffix(path.Base(lock), ".lock")
		if !have(tool) {
			res.Note = core.ToolMissing{Tool: tool}.Error()
			break
		}
		line := "uv lock"
		if tool == "poetry" {
			line = poetryLock(ctx, root, dir)
		}
		if err := runIn(ctx, root, dir, nil, line); err != nil {
			return nil, fmt.Errorf("toolchain: %w", err)
		}
		res.Files, res.Locked, res.Note = append(res.Files, lock), true, ""
	}
	return res, nil
}

// lockEngines moves engines.node in package-lock.json's entry for the
// project itself, which comes before any installed package's.
func lockEngines(p, from, to string) error {
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	old, now := jsonString(from), jsonString(to)
	end := bytes.Index(b, []byte(`"node_modules/`))
	if end < 0 {
		end = len(b)
	}
	at := bytes.Index(b[:end], []byte(`"node": `+old))
	if at < 0 {
		return fmt.Errorf("%s: engines.node not found", p)
	}
	at += len(`"node": `)
	res := append(append(append([]byte{}, b[:at]...), now...), b[at+len(old):]...)
	fi, err := os.Stat(p)
	if err != nil {
		return err
	}
	return os.WriteFile(p, res, fi.Mode().Perm())
}

func (toolchain) Commands(root string, m core.Manifest) core.Commands { return core.Commands{} }

func (toolchain) Traits() core.Traits { return core.Traits{Tooling: true} }

func (toolchain) Imports(d core.Dep) []string { return nil }
