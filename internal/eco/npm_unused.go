package eco

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/mohsensc/tend-cli/internal/core"
)

// npmEmitted are packages compilers and bundlers import into the code they
// write, so nothing in the source names them.
var npmEmitted = map[string]string{
	"tslib":               "TypeScript's importHelpers",
	"core-js":             "Babel's useBuiltIns",
	"regenerator-runtime": "Babel's regenerator transform",
	"@swc/helpers":        "SWC's externalHelpers",
}

// npmLoadedBy are packages a framework requires by itself when they are
// installed, so nothing in the project names them.
var npmLoadedBy = map[string][]string{
	"sharp": {"next"},
}

// npmLoaders load every installed package whose name starts with a prefix,
// as gulp-load-plugins loads gulp-uglify for $.uglify().
var npmLoaders = []struct{ loader, prefix string }{
	{"gulp-load-plugins", "gulp"}, {"jit-grunt", "grunt"}, {"load-grunt-tasks", "grunt"},
}

func (npm) needs(u *usage, d core.Dep) (need, doubt string) {
	block, aliases, err := npmBlockOf(u.root, d)
	switch {
	case err != nil:
		return "tend cannot read " + d.Manifest.Path, ""
	case block != "dependencies" && block != "devDependencies":
		return "declared in " + cmp.Or(block, "no block tend edits"), ""
	}
	for _, a := range aliases {
		if u.imported(a) {
			return "imported as " + a, ""
		}
		if at := u.mention(a); at != "" {
			return "named as " + a + " in " + at, ""
		}
	}
	if why, ok := npmEmitted[d.Name]; ok {
		return "imported by the code " + why + " emits", ""
	}
	for _, by := range npmLoadedBy[d.Name] {
		if u.declared(core.Npm, by) {
			return by + " loads it when it is installed", ""
		}
	}
	pkg, typed := npmTypesFor(d.Name)
	if (typed || d.Name == "typescript") && u.typescript() {
		return "the project is written in TypeScript", ""
	}
	if typed && (u.declared(core.Npm, pkg) || u.imported(pkg)) {
		return "types for " + pkg, ""
	}
	// tsc --checkJs reads every @types package without a tsconfig.json.
	if typed && u.declared(core.Npm, "typescript") {
		return "the TypeScript compiler reads every @types package", ""
	}
	f := u.npmFacts(d.Manifest)
	if by := f.peerOf[d.Name]; by != "" {
		return by + " needs it as a peer", ""
	}
	bins := f.bins[d.Name]
	if len(bins) == 0 {
		bins = npmInstalledBins(u.root, d)
	}
	if at := u.mention(append([]string{d.Name}, bins...)...); at != "" {
		return "named in " + at, ""
	}
	if doubt := u.npmLoaded(d.Name); doubt != "" {
		return "", doubt
	}
	// A tool's config file says the tool is in use, not which of its
	// plugins; the plugin has to be named.
	if tool, short, ok := npmPlugin(d.Name); ok {
		if at := u.mention(short); short != "" && at != "" {
			return "named in " + at, ""
		}
		return "", "a plugin " + tool + " loads by a name its config gives"
	}
	if at := u.configured(npmTools(d.Name)...); at != "" {
		return "configured in " + at, ""
	}
	if doubt := u.npmOwned(d.Name); doubt != "" {
		return "", doubt
	}
	switch {
	case !f.known && d.Manifest.Lock == "":
		return "", "there is no lockfile to say which packages need it as a peer"
	case !f.known:
		return "", "the lockfile does not say which packages need it as a peer"
	case f.hasBin[d.Name] && len(bins) == 0:
		return "", "it installs a command, and the lockfile does not say what the command is called"
	}
	return "", ""
}

// npmLoaded says why code may load name without naming it: a loader that
// takes every package with its prefix, or code that builds the name from a
// prefix of it, import(`@fontsource/${font}`).
func (u *usage) npmLoaded(name string) string {
	_, base, scoped := strings.Cut(name, "/")
	if !scoped {
		base = name
	}
	for _, l := range npmLoaders {
		if u.declared(core.Npm, l.loader) && (strings.HasPrefix(base, l.prefix+"-") || strings.HasPrefix(base, l.prefix+".")) {
			return l.loader + " loads every " + l.prefix + "-* package by itself"
		}
	}
	for i := len(name) - 1; i >= 3; i-- {
		if c := name[i]; c != '/' && c != '-' && c != '.' {
			continue
		}
		if at := u.built(name[:i+1]); at != "" {
			return "the code builds a package name from " + name[:i+1] + " in " + at
		}
	}
	return ""
}

// npmOwned says why a framework may load name by itself: it is in the
// scope of a declared tool, as vitest loads @vitest/coverage-v8 for
// --coverage, or a React Native app links it as a native module.
func (u *usage) npmOwned(name string) string {
	if scope, _, ok := strings.Cut(name, "/"); ok && len(scope) > 1 && scope[0] == '@' && u.declared(core.Npm, scope[1:]) {
		return scope[1:] + " loads packages of its own scope by name"
	}
	if !u.declared(core.Npm, "react-native") && !u.declared(core.Npm, "expo") {
		return ""
	}
	for _, p := range []string{"react-native-", "@react-native", "expo-", "@expo/"} {
		if strings.HasPrefix(name, p) {
			return "a React Native app links native modules and their tools without the code naming them"
		}
	}
	return ""
}

// npmBlockOf is where package.json declares d: peerDependencies or
// optionalDependencies when either does, else dependencies or
// devDependencies, else "". aliases are the keys that install d under
// another name through npm:, which the code imports it by.
func npmBlockOf(root string, d core.Dep) (block string, aliases []string, err error) {
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(d.Manifest.Path)))
	if err != nil {
		return "", nil, err
	}
	blocks, err := npmParse(src)
	if err != nil {
		return "", nil, err
	}
	for _, b := range blocks {
		for _, e := range b.entries {
			if !npmIs(e, d.Name) {
				continue
			}
			if e.key != d.Name {
				aliases = append(aliases, e.key)
			}
			switch {
			case b.name == "peerDependencies" || b.name == "optionalDependencies":
				block = b.name
			case block == "":
				block = b.name
			}
		}
	}
	return block, aliases, nil
}

// npmIs reports whether an entry installs the package name, by its key or
// through an npm: alias.
func npmIs(e npmEntry, name string) bool {
	if e.key == name {
		return true
	}
	n, ok := npmRegistryName(e.key, e.req)
	return ok && n == name
}

// npmTypesFor is the package an @types package types: @types/node is node,
// @types/babel__core is @babel/core.
func npmTypesFor(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, "@types/")
	if !ok {
		return "", false
	}
	if scope, n, ok := strings.Cut(rest, "__"); ok {
		return "@" + scope + "/" + n, true
	}
	return rest, true
}

// npmTools are the names a config file for the package may go by: the
// package's own and its scope's.
func npmTools(name string) []string {
	if scope, base, ok := strings.Cut(name, "/"); ok {
		return []string{strings.TrimPrefix(scope, "@"), base}
	}
	return []string{name}
}

// npmPlugin reports a package named the way a tool finds its plugins, which
// a config may name in a short form: eslint-plugin-react as "react",
// @babel/preset-env as "@babel/env", sass-loader as "sass".
func npmPlugin(name string) (tool, short string, ok bool) {
	scope, base, scoped := strings.Cut(name, "/")
	if !scoped {
		scope, base = "", name
	}
	in := func(s string) string {
		if scope == "" {
			return s
		}
		return scope + "/" + s
	}
	for _, p := range []struct{ tool, prefix string }{
		{"eslint", "eslint-plugin-"}, {"eslint", "eslint-config-"}, {"prettier", "prettier-plugin-"},
		{"babel", "babel-plugin-"}, {"babel", "babel-preset-"}, {"stylelint", "stylelint-"},
		{"postcss", "postcss-"}, {"remark", "remark-"}, {"rehype", "rehype-"}, {"karma", "karma-"},
		{"jest", "jest-"}, {"commitlint", "commitlint-config-"},
		{"eslint", "eslint-import-resolver-"}, {"eslint", "eslint-formatter-"},
	} {
		if rest, found := strings.CutPrefix(base, p.prefix); found {
			return p.tool, in(rest), true
		}
	}
	switch {
	case scope != "" && (base == "eslint-plugin" || base == "eslint-config"):
		return "eslint", scope, true
	case scope == "@babel":
		rest := strings.TrimPrefix(strings.TrimPrefix(base, "plugin-"), "preset-")
		return "babel", "@babel/" + rest, true
	case scope == "@typescript-eslint":
		return "eslint", scope, true
	case scope == "@commitlint":
		return "commitlint", scope, true
	case scope == "@prettier" && strings.HasPrefix(base, "plugin-"):
		// Prettier 2 loads every @prettier/plugin-* it finds installed.
		return "prettier", name, true
	case strings.HasSuffix(base, "-loader"):
		return "webpack", in(strings.TrimSuffix(base, "-loader")), true
	}
	return "", "", false
}

// npmFacts are what a lockfile records about installed packages that the
// first PR needs: the commands each installs, and which packages need it as
// a peer. known is false for a lockfile that does not record peers.
type npmFacts struct {
	known  bool
	bins   map[string][]string
	hasBin map[string]bool   // it installs commands the lockfile does not name
	peerOf map[string]string // package -> a package that needs it as a peer
}

func (u *usage) npmFacts(m core.Manifest) *npmFacts {
	if f, ok := u.facts[m.Lock]; ok {
		return f
	}
	f := &npmFacts{bins: map[string][]string{}, hasBin: map[string]bool{}, peerOf: map[string]string{}}
	if m.Lock != "" {
		if src, err := os.ReadFile(filepath.Join(u.root, filepath.FromSlash(m.Lock))); err == nil {
			switch path.Base(m.Lock) {
			case "package-lock.json", "npm-shrinkwrap.json":
				f.packageLock(src)
			case "pnpm-lock.yaml":
				f.pnpmLock(src)
			case "yarn.lock":
				f.yarnLock(src)
			}
		}
	}
	u.facts[m.Lock] = f
	return f
}

// peer notes that by needs name as a peer, keeping the first by name so the
// reason reads the same every run.
func (f *npmFacts) peer(name, by string) {
	if old, ok := f.peerOf[name]; !ok || by < old {
		f.peerOf[name] = by
	}
}

// npmBins reads a package.json bin field: one command named for the
// package, or a map of commands.
func npmBins(name string, raw json.RawMessage) []string {
	var one string
	if json.Unmarshal(raw, &one) == nil && one != "" {
		_, base, ok := strings.Cut(name, "/")
		if !ok {
			base = name
		}
		return []string{base}
	}
	var many map[string]string
	if json.Unmarshal(raw, &many) != nil {
		return nil
	}
	return keys(many)
}

func (f *npmFacts) packageLock(src []byte) {
	var lf struct {
		Packages map[string]struct {
			Name     string          `json:"name"`
			Bin      json.RawMessage `json:"bin"`
			Peer     map[string]any  `json:"peerDependencies"`
			PeerMeta map[string]any  `json:"peerDependenciesMeta"`
		} `json:"packages"`
	}
	if json.Unmarshal(stripBOM(src), &lf) != nil || lf.Packages == nil {
		return
	}
	f.known = true
	for k, p := range lf.Packages {
		i := strings.LastIndex(k, "node_modules/")
		if i < 0 {
			continue
		}
		name := cmp.Or(p.Name, k[i+len("node_modules/"):])
		if bins := npmBins(name, p.Bin); len(bins) > 0 {
			f.bins[name] = union(f.bins[name], bins)
		}
		for _, peer := range keys(p.Peer, p.PeerMeta) {
			f.peer(peer, name)
		}
	}
}

func (f *npmFacts) pnpmLock(src []byte) {
	var lf struct {
		LockfileVersion any `yaml:"lockfileVersion"`
		Packages        map[string]struct {
			HasBin   bool           `yaml:"hasBin"`
			Peer     map[string]any `yaml:"peerDependencies"`
			PeerMeta map[string]any `yaml:"peerDependenciesMeta"`
		} `yaml:"packages"`
	}
	if yaml.Unmarshal(src, &lf) != nil {
		return
	}
	major, _ := strconv.Atoi(strings.SplitN(fmt.Sprint(lf.LockfileVersion), ".", 2)[0])
	f.known = true
	for k, p := range lf.Packages {
		name, _, ok := pnpmKey(k, major)
		if !ok {
			continue
		}
		f.hasBin[name] = f.hasBin[name] || p.HasBin
		for _, peer := range keys(p.Peer, p.PeerMeta) {
			f.peer(peer, name)
		}
	}
}

// yarnLock reads what berry's lockfile records; classic's records neither
// commands nor peers.
func (f *npmFacts) yarnLock(src []byte) {
	if !strings.Contains(string(src), "\n__metadata:") {
		return
	}
	var lf map[string]struct {
		Bin      map[string]string `yaml:"bin"`
		Peer     map[string]any    `yaml:"peerDependencies"`
		PeerMeta map[string]any    `yaml:"peerDependenciesMeta"`
	}
	if yaml.Unmarshal(src, &lf) != nil {
		return
	}
	f.known = true
	for k, p := range lf {
		spec, _, _ := strings.Cut(k, ",")
		name, _, ok := yarnSpec(strings.TrimSpace(spec))
		if !ok || k == "__metadata" {
			continue
		}
		if len(p.Bin) > 0 {
			f.bins[name] = union(f.bins[name], keys(p.Bin))
		}
		for _, peer := range keys(p.Peer, p.PeerMeta) {
			f.peer(peer, name)
		}
	}
}

// npmInstalledBins reads the commands of d from an installed copy, when the
// project has its node_modules.
func npmInstalledBins(root string, d core.Dep) []string {
	p := filepath.Join(root, filepath.FromSlash(path.Dir(d.Manifest.Path)), "node_modules", filepath.FromSlash(d.Name), "package.json")
	var man struct {
		Bin json.RawMessage `json:"bin"`
	}
	if readJSON(p, &man) != nil {
		return nil
	}
	return npmBins(d.Name, man.Bin)
}

// Remove takes deps out of dependencies and devDependencies, with the comma
// that went with each, and has the package manager lock again.
func (npm) Remove(ctx context.Context, root string, deps []core.Dep) (*core.Bump, error) {
	m := deps[0].Manifest
	if err := lockUnsupported(m); err != nil {
		return nil, err
	}
	p := filepath.Join(root, filepath.FromSlash(m.Path))
	src, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, d := range deps {
		names = append(names, d.Name)
	}
	out, err := npmDrop(src, names)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", m.Path, err)
	}
	// A manifest its lockfile no longer matches fails npm ci and every
	// frozen install, so a removal waits for the tool that locks again.
	if m.Lock != "" && !have(m.Tool) {
		return nil, fmt.Errorf("%s not found to update %s", m.Tool, m.Lock)
	}
	snap := snapFiles(root, m.Path, m.Lock)
	if err := writeKeepMode(p, out); err != nil {
		return nil, err
	}
	if m.Lock == "" {
		return &core.Bump{Files: snap.changed(root), Note: "no lockfile"}, nil
	}
	line, err := npmLockLine(root, m, "", false)
	if err == nil {
		err = runIn(ctx, root, path.Dir(m.Lock), nil, line)
	}
	if err != nil {
		return nil, errors.Join(err, snap.restore(root))
	}
	return &core.Bump{Files: snap.changed(root), Locked: true}, nil
}

// npmDrop deletes every dependencies and devDependencies entry that installs
// one of names, with its comma, and leaves every other byte as it was. An
// entry alone in its block leaves {}.
func npmDrop(src []byte, names []string) ([]byte, error) {
	for {
		blocks, err := npmParse(src)
		if err != nil {
			return nil, err
		}
		b, i := npmDropAt(blocks, names)
		if b == nil {
			return src, nil
		}
		e := b.entries[i]
		switch {
		case len(b.entries) == 1:
			src = splice(src, b.open+1, b.close, "")
		case i < len(b.entries)-1:
			src = splice(src, e.keyStart-1, b.entries[i+1].keyStart-1, "")
		default:
			src = splice(src, b.entries[i-1].valEnd+1, e.valEnd+1, "")
		}
	}
}

func npmDropAt(blocks []npmBlock, names []string) (*npmBlock, int) {
	for i := range blocks {
		b := &blocks[i]
		if b.name != "dependencies" && b.name != "devDependencies" {
			continue
		}
		for j, e := range b.entries {
			for _, n := range names {
				if npmIs(e, n) {
					return b, j
				}
			}
		}
	}
	return nil, 0
}
