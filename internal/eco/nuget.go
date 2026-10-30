package eco

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/ver"
)

type nuget struct{}

func (nuget) Name() string { return core.Nuget }

// nugetIDs remembers how packages spell their ids; Dep.Name is lowercase but
// namespaces are not.
var nugetIDs learned[string]

func (nuget) Detect(root string) ([]core.Manifest, error) {
	files, err := walk(root, "*.csproj", "*.fsproj", "*.vbproj", "Directory.Packages.props", "packages.config")
	if err != nil {
		return nil, err
	}
	res := make([]core.Manifest, 0, len(files))
	for _, f := range files {
		m := core.Manifest{Ecosystem: core.Nuget, Path: f, Tool: "dotnet"}
		if lock := path.Join(path.Dir(f), "packages.lock.json"); isProject(f) && onDisk(root, lock) {
			m.Lock = lock
		}
		res = append(res, m)
	}
	return res, nil
}

func isProject(p string) bool {
	switch path.Ext(p) {
	case ".csproj", ".fsproj", ".vbproj":
		return true
	}
	return false
}

type nugetRef struct {
	id      string
	version string
	line    int
	dev     bool
	start   int // byte span of the element
	end     int
}

func nugetRefs(src []byte, elem string) ([]nugetRef, error) {
	src = bytes.TrimPrefix(src, []byte("\xef\xbb\xbf"))
	dec := xml.NewDecoder(bytes.NewReader(src))
	var res []nugetRef
	for {
		at := int(dec.InputOffset())
		tok, err := dec.Token()
		if err == io.EOF {
			return res, nil
		}
		if err != nil {
			return nil, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != elem {
			continue
		}
		r := nugetRef{line: 1 + bytes.Count(src[:at], []byte("\n")), start: at}
		for _, a := range se.Attr {
			switch strings.ToLower(a.Name.Local) {
			case "include", "id":
				r.id = a.Value
			case "version":
				r.version = a.Value
			case "privateassets":
				r.dev = strings.EqualFold(a.Value, "all")
			case "developmentdependency":
				r.dev = strings.EqualFold(a.Value, "true")
			}
		}
		for depth := 1; depth > 0; {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				var text string
				if err := dec.DecodeElement(&text, &t); err != nil {
					return nil, err
				}
				switch strings.ToLower(t.Name.Local) {
				case "version":
					r.version = strings.TrimSpace(text)
				case "privateassets":
					r.dev = strings.EqualFold(strings.TrimSpace(text), "all")
				}
			case xml.EndElement:
				depth--
			}
		}
		r.end = int(dec.InputOffset())
		res = append(res, r)
	}
}

type nugetLockFile struct {
	Dependencies map[string]map[string]struct {
		Type         string            `json:"type"`
		Resolved     string            `json:"resolved"`
		Dependencies map[string]string `json:"dependencies"`
	} `json:"dependencies"`
}

func nugetLocked(root, lock string) (map[string]string, error) {
	versions := map[string]string{}
	if lock == "" {
		return versions, nil
	}
	var lf nugetLockFile
	if err := readJSON(filepath.Join(root, filepath.FromSlash(lock)), &lf); err != nil {
		return nil, err
	}
	for _, fw := range lf.Dependencies {
		for id, e := range fw {
			if e.Type == "Project" || e.Resolved == "" {
				continue
			}
			name := strings.ToLower(id)
			nugetIDs.add(name, id)
			if cur, ok := versions[name]; !ok || ver.Less(cur, e.Resolved) {
				versions[name] = e.Resolved
			}
		}
	}
	return versions, nil
}

func centralVersions(root, project string) (map[string]string, error) {
	res := map[string]string{}
	for dir := path.Dir(project); ; dir = path.Dir(dir) {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path.Join(dir, "Directory.Packages.props"))))
		if err == nil {
			refs, err := nugetRefs(src, "PackageVersion")
			for _, r := range refs {
				res[strings.ToLower(r.id)] = r.version
			}
			return res, err
		}
		if dir == "." {
			return res, nil
		}
	}
}

var (
	plainVersion = regexp.MustCompile(`^\d[0-9A-Za-z.+-]*$`)
	forgeURL     = regexp.MustCompile(`^https://(www\.)?(github\.com|gitlab\.com|bitbucket\.org)/`)
)

func (nuget) Deps(root string, m core.Manifest) ([]core.Dep, error) {
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(m.Path)))
	if err != nil {
		return nil, err
	}
	elem := "PackageReference"
	switch path.Base(m.Path) {
	case "Directory.Packages.props":
		elem = "PackageVersion"
	case "packages.config":
		elem = "package"
	}
	refs, err := nugetRefs(src, elem)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", m.Path, err)
	}
	locked, err := nugetLocked(root, m.Lock)
	if err != nil {
		return nil, err
	}
	var central map[string]string
	if elem == "PackageReference" {
		if central, err = centralVersions(root, m.Path); err != nil {
			return nil, err
		}
	}
	testy := testProject(m.Path)
	var res []core.Dep
	seenID := map[string]bool{}
	for _, r := range refs {
		name := strings.ToLower(r.id)
		req := r.version
		if req == "" {
			req = central[name]
		}
		if r.id == "" || req == "" || strings.Contains(req, "$(") {
			continue
		}
		nugetIDs.add(name, r.id)
		v := locked[name]
		if v == "" && plainVersion.MatchString(req) {
			v = req
		}
		seenID[name] = true
		res = append(res, core.Dep{
			Ecosystem: core.Nuget, Name: name, Version: v, Req: req,
			Manifest: m, Line: r.line, Dev: r.dev || testy, Direct: true,
		})
	}
	var rest []core.Dep
	for name, v := range locked {
		if !seenID[name] {
			rest = append(rest, core.Dep{Ecosystem: core.Nuget, Name: name, Version: v, Manifest: m})
		}
	}
	sortDeps(rest)
	return append(res, rest...), nil
}

// testProject reports whether a project's name ends a segment in Test or
// Tests: App.Tests and AppTests are, Latest and Contest are not.
func testProject(p string) bool {
	name := strings.TrimSuffix(path.Base(p), path.Ext(p))
	for _, seg := range strings.FieldsFunc(name, func(r rune) bool { return r == '.' || r == '-' || r == '_' }) {
		low := strings.ToLower(seg)
		for _, s := range []string{"tests", "test"} {
			if at := len(seg) - len(s); strings.HasSuffix(low, s) && (at == 0 || seg[at] == 'T') {
				return true
			}
		}
	}
	return false
}

func (nuget) Meta(ctx context.Context, f core.Fetcher, name string) (*core.Meta, error) {
	id := strings.ToLower(name)
	var index struct {
		Items []nugetPage `json:"items"`
	}
	ok, err := getDoc(ctx, f, "https://api.nuget.org/v3/registration5-gz-semver2/"+id+"/index.json", &index)
	if err != nil {
		return nil, fmt.Errorf("nuget: %s: %w", name, err)
	}
	if !ok {
		return nil, fmt.Errorf("nuget: %s: not found on nuget.org", name)
	}
	res := &core.Meta{Name: id}
	entries := map[string]nugetEntry{}
	for _, pg := range index.Items {
		if len(pg.Items) == 0 && pg.ID != "" {
			ok, err := getDoc(ctx, f, pg.ID, &pg)
			if err != nil {
				return nil, fmt.Errorf("nuget: %s: %w", name, err)
			}
			if !ok {
				return nil, fmt.Errorf("nuget: %s: page %s not found", name, pg.ID)
			}
		}
		for _, leaf := range pg.Items {
			e := leaf.CatalogEntry
			t, _ := time.Parse(time.RFC3339, e.Published)
			res.Releases = append(res.Releases, core.Release{
				Version: e.Version, Time: t, Pre: semverPre(e.Version),
				Yanked:     (e.Listed != nil && !*e.Listed) || t.Year() == 1900,
				Deprecated: e.Deprecation.text(),
			})
			entries[e.Version] = e
		}
	}
	sortReleases(res.Releases)
	// the registry has no latest; the newest listed stable release speaks for the package
	var cur *core.Release
	for i := len(res.Releases) - 1; i >= 0; i-- {
		r := &res.Releases[i]
		if !r.Yanked && (cur == nil || (cur.Pre && !r.Pre)) {
			cur = r
		}
	}
	if cur == nil {
		return res, nil
	}
	e := entries[cur.Version]
	res.Deprecated = cur.Deprecated
	if cur.Deprecated != "" && e.Deprecation != nil {
		res.Replacement = strings.ToLower(e.Deprecation.Alternate.ID)
	}
	// projectUrl is often a docs site; only a forge URL is a repo
	if u := cleanRepo(e.ProjectURL); forgeURL.MatchString(u) {
		res.Repo = u
	}
	return res, nil
}

type nugetPage struct {
	ID    string `json:"@id"`
	Items []struct {
		CatalogEntry nugetEntry `json:"catalogEntry"`
	} `json:"items"`
}

type nugetEntry struct {
	Version     string           `json:"version"`
	Published   string           `json:"published"`
	Listed      *bool            `json:"listed"`
	ProjectURL  string           `json:"projectUrl"`
	Deprecation *nugetDeprecated `json:"deprecation"`
}

type nugetDeprecated struct {
	Reasons   []string `json:"reasons"`
	Message   string   `json:"message"`
	Alternate struct {
		ID string `json:"id"`
	} `json:"alternatePackage"`
}

func (d *nugetDeprecated) text() string {
	switch {
	case d == nil:
		return ""
	case d.Message != "":
		return d.Message
	case len(d.Reasons) > 0:
		return strings.Join(d.Reasons, ", ")
	}
	return "deprecated"
}

func (nuget) Bump(ctx context.Context, root string, d core.Dep, to, toName string) (*core.Bump, error) {
	if !d.Direct || path.Base(d.Manifest.Path) == "packages.config" {
		return nil, fmt.Errorf("nuget: %s: nothing to edit in %s: %w", d.Name, d.Manifest.Path, core.ErrNotSupported)
	}
	file, elem, err := nugetHome(root, d)
	if err != nil {
		return nil, err
	}
	changed, err := nugetEdit(root, file, elem, d, to, toName)
	if err != nil {
		return nil, err
	}
	res := &core.Bump{}
	if changed {
		res.Files = append(res.Files, file)
	}
	locks := nugetLocks(root, d.Manifest, file)
	switch {
	case len(locks) == 0:
		res.Note = "no packages.lock.json"
		return res, nil
	case !have("dotnet"):
		res.Note = core.ToolMissing{Tool: "dotnet"}.Error()
		return res, nil
	}
	for _, l := range locks {
		line := "dotnet restore --force-evaluate"
		if isProject(d.Manifest.Path) && l == d.Manifest.Lock {
			line = "dotnet restore " + shArg(path.Base(d.Manifest.Path)) + " --force-evaluate"
		}
		if err := runIn(ctx, root, path.Dir(l), nil, line); err != nil {
			return nil, fmt.Errorf("nuget: %s: %w", d.Name, err)
		}
		res.Files = append(res.Files, l)
	}
	res.Locked = true
	return res, nil
}

func nugetLocks(root string, m core.Manifest, file string) []string {
	if isProject(file) {
		if m.Lock == "" {
			return nil
		}
		return []string{m.Lock}
	}
	all, _ := walk(root, "packages.lock.json")
	dir := path.Dir(file)
	return slices.DeleteFunc(all, func(l string) bool { return dir != "." && !strings.HasPrefix(l, dir+"/") })
}

func nugetHome(root string, d core.Dep) (file, elem string, err error) {
	file = d.Manifest.Path
	if path.Base(file) == "Directory.Packages.props" {
		return file, "PackageVersion", nil
	}
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if err != nil {
		return "", "", err
	}
	refs, err := nugetRefs(src, "PackageReference")
	if err != nil {
		return "", "", err
	}
	for _, r := range refs {
		if r.line == d.Line && strings.EqualFold(r.id, d.Name) && r.version != "" {
			return file, "PackageReference", nil
		}
	}
	for dir := path.Dir(file); ; dir = path.Dir(dir) {
		props := path.Join(dir, "Directory.Packages.props")
		if onDisk(root, props) {
			return props, "PackageVersion", nil
		}
		if dir == "." {
			return "", "", fmt.Errorf("nuget: %s: no Version in %s and no Directory.Packages.props", d.Name, file)
		}
	}
}

var (
	versionAttr = regexp.MustCompile(`(?i)(\bVersion\s*=\s*)(["'])([^"']*)(["'])`)
	versionElem = regexp.MustCompile(`(?s)(<Version>\s*)([^<]*?)(\s*</Version>)`)
	includeAttr = regexp.MustCompile(`(?i)(\bInclude\s*=\s*)(["'])([^"']*)(["'])`)
)

func nugetEdit(root, file, elem string, d core.Dep, to, toName string) (bool, error) {
	p := filepath.Join(root, filepath.FromSlash(file))
	src, err := os.ReadFile(p)
	if err != nil {
		return false, err
	}
	refs, err := nugetRefs(src, elem)
	if err != nil {
		return false, err
	}
	bom := 0
	if bytes.HasPrefix(src, []byte("\xef\xbb\xbf")) {
		bom = 3
	}
	for _, r := range refs {
		if !strings.EqualFold(r.id, d.Name) || (file == d.Manifest.Path && r.line != d.Line) {
			continue
		}
		start, end := r.start+bom, r.end+bom
		el := string(src[start:end])
		if toName != "" && elem == "PackageVersion" {
			return false, fmt.Errorf("nuget: %s: cannot swap a centrally versioned package: %w", d.Name, core.ErrNotSupported)
		}
		next, ok := nugetVersion(r.version, to)
		if toName != "" {
			next, ok = to, true
		}
		if !ok {
			return false, fmt.Errorf("nuget: %s: cannot rewrite version %q", d.Name, r.version)
		}
		out := el
		if toName != "" {
			out = setGroup(includeAttr, out, 3, toName)
		}
		if next != r.version {
			if versionAttr.MatchString(out[:openTag(out)]) {
				out = setGroup(versionAttr, out, 3, next)
			} else {
				out = setGroup(versionElem, out, 2, next)
			}
		}
		if out == el {
			return false, nil
		}
		res := string(src[:start]) + out + string(src[end:])
		fi, err := os.Stat(p)
		if err != nil {
			return false, err
		}
		return true, os.WriteFile(p, []byte(res), fi.Mode().Perm())
	}
	return false, fmt.Errorf("nuget: %s: not found in %s", d.Name, file)
}

func openTag(el string) int {
	var quote byte
	for i := 0; i < len(el); i++ {
		switch c := el[i]; {
		case quote != 0 && c == quote:
			quote = 0
		case quote != 0:
		case c == '"' || c == '\'':
			quote = c
		case c == '>':
			return i + 1
		}
	}
	return len(el)
}

func setGroup(re *regexp.Regexp, s string, g int, val string) string {
	m := re.FindStringSubmatchIndex(s)
	if m == nil {
		return s
	}
	return s[:m[2*g]] + val + s[m[2*g+1]:]
}

var nugetRange = regexp.MustCompile(`^([\[(])\s*([^,\])]*?)\s*(?:(,)\s*([^\])]*?)\s*)?([\])])$`)

// nugetVersion computes the text that makes a PackageReference reach to. A
// plain version is a minimum and moves to the target; a range moves only when
// it excludes it.
func nugetVersion(req, to string) (string, bool) {
	want, ok := ver.Parse(to)
	if !ok {
		return "", false
	}
	switch {
	case plainVersion.MatchString(req):
		return to, true
	case strings.HasSuffix(req, ".*") && plainVersion.MatchString(strings.TrimSuffix(req, ".*")):
		t := term{num: req}
		if t.holds(want, anyDialect) {
			return req, true
		}
		return t.move(to, anyDialect), true
	}
	m := nugetRange.FindStringSubmatchIndex(req)
	if m == nil {
		return "", false
	}
	sub := func(i int) string {
		if m[2*i] < 0 {
			return ""
		}
		return req[m[2*i]:m[2*i+1]]
	}
	low, comma, high := sub(2), sub(3), sub(4)
	if comma == "" {
		return "[" + to + "]", true
	}
	lo, _ := ver.Parse(low)
	hi, hasHi := ver.Parse(high)
	inside := (low == "" || ver.Compare(want, lo) > 0 || (ver.Compare(want, lo) == 0 && sub(1) == "[")) &&
		(!hasHi || ver.Compare(want, hi) < 0 || (ver.Compare(want, hi) == 0 && sub(5) == "]"))
	if inside {
		return req, true
	}
	res := req
	if hasHi {
		res = res[:m[8]] + nextMajor(to, len(hi.Nums)) + res[m[9]:]
	}
	if low != "" {
		res = res[:m[2]] + "[" + res[m[3]:m[4]] + to + res[m[5]:]
	}
	return res, true
}

func (nuget) Commands(root string, m core.Manifest) core.Commands {
	dir := path.Dir(m.Path)
	return core.Commands{
		Build: []string{inDir(dir, "dotnet build --nologo")},
		Test:  []string{inDir(dir, "dotnet test --nologo")},
	}
}

func (nuget) Traits() core.Traits {
	return core.Traits{OSV: "NuGet", Page: prefix("https://www.nuget.org/packages/")}
}

func (nuget) Imports(d core.Dep) []string {
	if ids := nugetIDs.get(d.Name); len(ids) > 0 {
		sort.Strings(ids)
		return ids[:1]
	}
	return []string{d.Name}
}
