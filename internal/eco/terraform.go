package eco

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
)

// terraform reads the providers a configuration requires and the registry
// modules it calls, with the provider versions .terraform.lock.hcl settled
// on. A provider is named namespace/type, a module namespace/name/provider.
type terraform struct{}

func (terraform) Name() string { return core.Terraform }

const tfRegistry = "https://registry.terraform.io/v1/"

var (
	tfBlock    = regexp.MustCompile(`^\s*(required_providers|module\s+"[^"]+")\s*\{`)
	tfEntry    = regexp.MustCompile(`^\s*([\w-]+)\s*=\s*\{`)
	tfAttr     = regexp.MustCompile(`^\s*(source|version)\s*=\s*"([^"]*)"`)
	tfLockHead = regexp.MustCompile(`^provider\s+"(?:registry\.terraform\.io/)?([^"]+)"\s*\{`)
	tfLockVer  = regexp.MustCompile(`^\s*version\s*=\s*"([^"]+)"`)
	tfModule   = regexp.MustCompile(`^[\w-]+/[\w-]+/[\w-]+$`)
)

func (terraform) Detect(root string) ([]core.Manifest, error) {
	files, err := walk(root, "*.tf")
	if err != nil {
		return nil, err
	}
	var res []core.Manifest
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
		if err != nil || len(tfPins(b)) == 0 {
			continue
		}
		m := core.Manifest{Ecosystem: core.Terraform, Path: f, Tool: "terraform"}
		if lock := path.Join(path.Dir(f), ".terraform.lock.hcl"); onDisk(root, lock) {
			m.Lock = lock
		}
		res = append(res, m)
	}
	return res, nil
}

type tfPin struct {
	name, constraint string
	line             int
	module           bool
}

// tfPins reads the required_providers entries and registry module calls of
// one file. A line scan with brace depth covers how Terraform is written in
// practice without an HCL parser.
func tfPins(b []byte) []tfPin {
	var res []tfPin
	var cur *tfPin
	in, depth := "", 0
	for i, l := range strings.Split(string(b), "\n") {
		if in == "" {
			if m := tfBlock.FindStringSubmatch(l); m != nil {
				in, depth = m[1], 1
				if in != "required_providers" {
					cur = &tfPin{module: true}
				}
			}
			continue
		}
		if in == "required_providers" && depth == 1 {
			if m := tfEntry.FindStringSubmatch(l); m != nil {
				cur = &tfPin{name: "hashicorp/" + m[1]}
			}
		}
		if m := tfAttr.FindStringSubmatch(l); m != nil && cur != nil {
			switch m[1] {
			case "source":
				cur.name = m[2]
				if !cur.module && !strings.Contains(m[2], "/") {
					cur.name = "hashicorp/" + m[2]
				}
			case "version":
				cur.constraint, cur.line = m[2], i+1
			}
		}
		depth += strings.Count(l, "{") - strings.Count(l, "}")
		if cur != nil && (depth == 1 && in == "required_providers" || depth == 0) {
			if cur.line > 0 && (!cur.module || tfModule.MatchString(cur.name)) {
				res = append(res, *cur)
			}
			cur = nil
		}
		if depth <= 0 {
			in = ""
		}
	}
	return res
}

func (terraform) Deps(root string, m core.Manifest) ([]core.Dep, error) {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(m.Path)))
	if err != nil {
		return nil, err
	}
	locked := map[string]string{}
	if m.Lock != "" {
		lb, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(m.Lock)))
		if err == nil {
			name := ""
			for _, l := range strings.Split(string(lb), "\n") {
				if h := tfLockHead.FindStringSubmatch(l); h != nil {
					name = strings.ToLower(h[1])
				} else if v := tfLockVer.FindStringSubmatch(l); v != nil && name != "" {
					locked[name], name = v[1], ""
				}
			}
		}
	}
	var res []core.Dep
	for _, p := range tfPins(b) {
		d := core.Dep{Ecosystem: core.Terraform, Name: strings.ToLower(p.name), Req: p.constraint, Manifest: m, Line: p.line, Direct: true}
		d.Version = locked[d.Name]
		if d.Version == "" {
			d.Version = strings.TrimSpace(strings.TrimLeft(p.constraint, "~>=<! "))
		}
		res = append(res, d)
	}
	return res, nil
}

func (terraform) Meta(ctx context.Context, f core.Fetcher, name string) (*core.Meta, error) {
	var vs []string
	if strings.Count(name, "/") == 2 {
		var doc struct {
			Modules []struct {
				Versions []struct {
					Version string `json:"version"`
				} `json:"versions"`
			} `json:"modules"`
		}
		ok, err := getDoc(ctx, f, tfRegistry+"modules/"+name+"/versions", &doc)
		if err != nil || !ok {
			return nil, tfNotFound(name, err)
		}
		for _, m := range doc.Modules {
			for _, v := range m.Versions {
				vs = append(vs, v.Version)
			}
		}
	} else {
		var doc struct {
			Versions []struct {
				Version string `json:"version"`
			} `json:"versions"`
		}
		ok, err := getDoc(ctx, f, tfRegistry+"providers/"+name+"/versions", &doc)
		if err != nil || !ok {
			return nil, tfNotFound(name, err)
		}
		for _, v := range doc.Versions {
			vs = append(vs, v.Version)
		}
	}
	res := &core.Meta{Name: name}
	for _, v := range vs {
		res.Releases = append(res.Releases, core.Release{Version: v, Pre: semverPre(v)})
	}
	sortReleases(res.Releases)
	return res, nil
}

func tfNotFound(name string, err error) error {
	if err != nil {
		return fmt.Errorf("terraform: %s: %w", name, err)
	}
	return fmt.Errorf("terraform: %s: not found in the registry", name)
}

// Bump rewrites the version constraint just enough to admit the target and
// moves the lock to it. Terraform takes no version on the command line and
// -upgrade moves every provider, so the constraint asks for "= to" for one
// pass, the other providers get their lock entries back, and a plain init
// settles the lock on the constraint as written.
func (terraform) Bump(ctx context.Context, root string, d core.Dep, to, toName string) (*core.Bump, error) {
	if toName != "" {
		return nil, fmt.Errorf("terraform: %s: %w", d.Name, core.ErrNotSupported)
	}
	next, err := fixReq(d.Req, to, false)
	if err != nil {
		return nil, fmt.Errorf("terraform: %s: %w", d.Name, err)
	}
	p := filepath.Join(root, filepath.FromSlash(d.Manifest.Path))
	res := &core.Bump{}
	if next != d.Req {
		if err := rewriteLine(p, d.Line, `"`+d.Req+`"`, `"`+next+`"`); err != nil {
			return nil, fmt.Errorf("terraform: %w", err)
		}
		res.Files = append(res.Files, d.Manifest.Path)
	}
	if d.Manifest.Lock == "" || strings.Count(d.Name, "/") == 2 {
		res.Note = "no lockfile"
		return res, nil
	}
	if !have("terraform") {
		res.Note = core.ToolMissing{Tool: "terraform"}.Error()
		return res, nil
	}
	if err := tfLockAt(ctx, root, d, next, to); err != nil {
		return nil, fmt.Errorf("terraform: %s: %w", d.Name, err)
	}
	res.Locked = true
	res.Files = append(res.Files, d.Manifest.Lock)
	return res, nil
}

const tfInit = "terraform init -backend=false -input=false"

func tfLockAt(ctx context.Context, root string, d core.Dep, req, to string) error {
	p := filepath.Join(root, filepath.FromSlash(d.Manifest.Path))
	lp := filepath.Join(root, filepath.FromSlash(d.Manifest.Lock))
	src, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	lock, err := os.ReadFile(lp)
	if err != nil {
		return err
	}
	if err := rewriteLine(p, d.Line, `"`+req+`"`, `"= `+to+`"`); err != nil {
		return err
	}
	dir := path.Dir(d.Manifest.Path)
	err = runIn(ctx, root, dir, nil, tfInit+" -upgrade")
	if werr := writeKeepMode(p, src); err == nil {
		err = werr
	}
	if err != nil {
		return err
	}
	moved, err := os.ReadFile(lp)
	if err != nil {
		return err
	}
	if err := writeKeepMode(lp, tfKeepOthers(lock, moved, d.Name)); err != nil {
		return err
	}
	if err := runIn(ctx, root, dir, nil, tfInit); err != nil {
		return err
	}
	return tfLanded(lp, d.Name, to)
}

// tfLockBlocks finds each provider block of a lock file: name to the byte
// range from its header through its closing brace.
func tfLockBlocks(src []byte) map[string][2]int {
	res := map[string][2]int{}
	name, start := "", 0
	off := 0
	for _, l := range strings.SplitAfter(string(src), "\n") {
		switch h := tfLockHead.FindStringSubmatch(l); {
		case h != nil:
			name, start = strings.ToLower(h[1]), off
		case name != "" && strings.HasPrefix(l, "}"):
			res[name] = [2]int{start, off + len(strings.TrimRight(l, "\r\n"))}
			name = ""
		}
		off += len(l)
	}
	return res
}

// tfKeepOthers is the moved lock with every provider but name put back as
// it was before.
func tfKeepOthers(before, moved []byte, name string) []byte {
	old := tfLockBlocks(before)
	cur := tfLockBlocks(moved)
	type span struct {
		at  [2]int
		was []byte
	}
	var todo []span
	for n, at := range cur {
		if was, ok := old[n]; ok && n != name {
			todo = append(todo, span{at, before[was[0]:was[1]]})
		}
	}
	slices.SortFunc(todo, func(a, b span) int { return b.at[0] - a.at[0] })
	res := moved
	for _, s := range todo {
		res = splice(res, s.at[0], s.at[1], string(s.was))
	}
	return res
}

func tfLanded(lock, name, to string) error {
	b, err := os.ReadFile(lock)
	if err != nil {
		return err
	}
	at, ok := tfLockBlocks(b)[name]
	if !ok {
		return fmt.Errorf("%s is not in the lock after the update to %s", name, to)
	}
	for _, l := range strings.Split(string(b[at[0]:at[1]]), "\n") {
		m := tfLockVer.FindStringSubmatch(l)
		switch {
		case m == nil:
		case sameVersion(m[1], to):
			return nil
		default:
			return fmt.Errorf("%s locked at %s, not %s", name, m[1], to)
		}
	}
	return fmt.Errorf("%s has no version in the lock", name)
}

// Commands validate the configuration where terraform is installed. Many
// repos carry a little Terraform next to the code their CI builds without it,
// and a proof that fails for want of the tool proves nothing.
func (terraform) Commands(root string, m core.Manifest) core.Commands {
	if !have("terraform") {
		return core.Commands{}
	}
	dir := path.Dir(m.Path)
	return core.Commands{
		Install: []string{inDir(dir, "terraform init -backend=false -input=false")},
		Test:    []string{inDir(dir, "terraform validate")},
	}
}

func (terraform) Traits() core.Traits { return core.Traits{} }

func (terraform) Imports(d core.Dep) []string { return nil }
