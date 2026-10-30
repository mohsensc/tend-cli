package eco

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/ver"
)

type maven struct{}

func (maven) Name() string { return core.Maven }

const (
	mavenCentral = "https://repo1.maven.org/maven2"
	googleMaven  = "https://dl.google.com/dl/android/maven2"
	mavenSearch  = "https://search.maven.org/solrsearch/select"
)

func (maven) Detect(root string) ([]core.Manifest, error) {
	files, err := walk(root, "pom.xml")
	if err != nil {
		return nil, err
	}
	var res []core.Manifest
	for _, f := range files {
		res = append(res, core.Manifest{Ecosystem: core.Maven, Path: f, Tool: "mvn"})
	}
	return res, nil
}

type pomText struct {
	val  string
	line int
}

type pomDep struct {
	group, artifact, version, scope pomText
	line                            int // the <dependency> element
}

type pom struct {
	rel                      string
	group, artifact, version pomText
	parent                   struct{ group, artifact, version, relPath pomText }
	noParentPath             bool // <relativePath/>: the parent is not looked up on disk
	props                    map[string]pomText
	deps, managed            []pomDep
	modules                  []string
}

// readPom walks the XML tokens rather than unmarshaling so that every value
// keeps its line.
func readPom(root, rel string) (*pom, error) {
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	dec := xml.NewDecoder(bytes.NewReader(src))
	dec.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	r := pomReader{pom: &pom{rel: rel, props: map[string]pomText{}}}
	var stack []string
	var text strings.Builder
	textLine := 0
	for {
		line, _ := dec.InputPos()
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return r.pom, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", rel, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			stack = append(stack, t.Name.Local)
			text.Reset()
			textLine = 0
			if pomIsDep(stack) {
				r.dep = &pomDep{line: line}
			}
		case xml.CharData:
			if s := string(t); textLine == 0 && strings.TrimSpace(s) != "" {
				textLine = line + strings.Count(s[:len(s)-len(strings.TrimLeft(s, " \t\r\n"))], "\n")
			}
			text.Write(t)
		case xml.EndElement:
			r.set(stack, pomText{strings.TrimSpace(text.String()), textLine})
			stack = stack[:len(stack)-1]
			text.Reset()
		}
	}
}

// pomReader fills a pom from the element path and text of each closing tag;
// dep is the <dependency> being read.
type pomReader struct {
	*pom
	dep *pomDep
}

func pomIsDep(stack []string) bool {
	at := strings.Join(stack, "/")
	return at == "project/dependencies/dependency" || at == "project/dependencyManagement/dependencies/dependency"
}

func (r *pomReader) set(stack []string, v pomText) {
	n := len(stack)
	if n == 3 && stack[1] == "properties" {
		r.props[stack[2]] = v
		return
	}
	switch strings.Join(stack, "/") {
	case "project/groupId":
		r.group = v
	case "project/artifactId":
		r.artifact = v
	case "project/version":
		r.version = v
	case "project/parent/groupId":
		r.parent.group = v
	case "project/parent/artifactId":
		r.parent.artifact = v
	case "project/parent/version":
		r.parent.version = v
	case "project/parent/relativePath":
		r.parent.relPath = v
		r.noParentPath = v.val == ""
	case "project/modules/module":
		r.modules = append(r.modules, v.val)
	case "project/dependencies/dependency":
		r.deps = append(r.deps, *r.dep)
		r.dep = nil
	case "project/dependencyManagement/dependencies/dependency":
		r.managed = append(r.managed, *r.dep)
		r.dep = nil
	}
	if r.dep == nil || !pomIsDep(stack[:n-1]) {
		return
	}
	switch stack[n-1] {
	case "groupId":
		r.dep.group = v
	case "artifactId":
		r.dep.artifact = v
	case "version":
		r.dep.version = v
	case "scope":
		r.dep.scope = v
	}
}

// pomChain reads rel and the parents it inherits from that live in the repo,
// child first. A parent from a repository ends the chain.
func pomChain(root, rel string) ([]*pom, error) {
	p, err := readPom(root, rel)
	if err != nil {
		return nil, err
	}
	chain := []*pom{p}
	for len(chain) < 10 {
		cur := chain[len(chain)-1]
		if cur.parent.artifact.val == "" || cur.noParentPath {
			break
		}
		next := path.Join(path.Dir(cur.rel), cmp.Or(cur.parent.relPath.val, "../pom.xml"))
		if !strings.HasSuffix(next, ".xml") {
			next = path.Join(next, "pom.xml")
		}
		if next == ".." || strings.HasPrefix(next, "../") || !onDisk(root, next) {
			break
		}
		pp, err := readPom(root, next)
		if err != nil {
			return nil, err
		}
		if pp.artifact.val != cur.parent.artifact.val {
			break
		}
		chain = append(chain, pp)
	}
	return chain, nil
}

var pomVar = regexp.MustCompile(`\$\{([^}]+)\}`)

// pomResolve interpolates ${...} the way Maven builds the effective model:
// the child's own coordinates, then properties from the child up.
func pomResolve(chain []*pom, s string) string {
	for i := 0; i < 10 && strings.Contains(s, "${"); i++ {
		s = pomVar.ReplaceAllStringFunc(s, func(m string) string {
			if v, ok := pomLookup(chain, m[2:len(m)-1]); ok {
				return v
			}
			return m
		})
	}
	return s
}

func pomLookup(chain []*pom, name string) (string, bool) {
	p := chain[0]
	switch name {
	case "project.version", "pom.version", "version":
		return cmp.Or(p.version.val, p.parent.version.val), true
	case "project.groupId", "pom.groupId", "groupId":
		return cmp.Or(p.group.val, p.parent.group.val), true
	case "project.artifactId", "pom.artifactId", "artifactId":
		return p.artifact.val, true
	case "project.parent.version", "parent.version":
		return p.parent.version.val, true
	case "project.parent.groupId", "parent.groupId":
		return p.parent.group.val, true
	}
	for _, q := range chain {
		if v, ok := q.props[name]; ok {
			return v.val, true
		}
	}
	return "", false
}

// pomReactor reports a version tied to the project's own, the mark of a
// sibling module rather than something from a repository.
func pomReactor(expr string) bool {
	for _, s := range []string{"${project.version}", "${version}", "${pom.version}", "${project.parent.version}", "${parent.version}"} {
		if strings.Contains(expr, s) {
			return true
		}
	}
	return false
}

// pomSource is where a version is actually written: a <version> element, or
// the property it names, in whichever pom of the chain defines it.
type pomSource struct {
	pom  *pom
	line int
	text string
}

func pomSourceOf(chain []*pom, owner *pom, v pomText) (pomSource, bool) {
	src := pomSource{owner, v.line, v.val}
	for i := 0; i < 10; i++ {
		m := pomVar.FindStringSubmatch(src.text)
		if m == nil {
			return src, src.text != ""
		}
		if m[0] != src.text {
			return src, false
		}
		found := false
		for _, q := range chain {
			if pv, ok := q.props[m[1]]; ok {
				src, found = pomSource{q, pv.line, pv.val}, true
				break
			}
		}
		if !found {
			return src, false
		}
	}
	return src, false
}

func pomManaged(chain []*pom, g, a string) (*pomDep, *pom) {
	for _, q := range chain {
		for i, d := range q.managed {
			if pomResolve(chain, d.group.val) == g && pomResolve(chain, d.artifact.val) == a {
				return &q.managed[i], q
			}
		}
	}
	return nil, nil
}

// pomEntry is one dependency of a pom with the version text that decides it.
type pomEntry struct {
	name    string
	dep     *pomDep // nil for the parent
	version pomText
	owner   *pom
	line    int
	dev     bool
	direct  bool
}

// pomEntries lists a pom's dependencies: an external parent first, then
// <dependencies> in order, then <dependencyManagement> entries not already
// listed, sorted by name. Sibling modules are left out.
func pomEntries(chain []*pom) []pomEntry {
	p := chain[0]
	var res []pomEntry
	if p.parent.artifact.val != "" && len(chain) == 1 {
		res = append(res, pomEntry{
			name:    p.parent.group.val + ":" + p.parent.artifact.val,
			version: p.parent.version,
			owner:   p,
			line:    p.parent.version.line,
			direct:  true,
		})
	}
	seen := map[string]bool{}
	for i := range p.deps {
		d := &p.deps[i]
		e := pomEntry{dep: d, version: d.version, owner: p, line: d.line, dev: d.scope.val == "test", direct: true}
		g, a := pomResolve(chain, d.group.val), pomResolve(chain, d.artifact.val)
		e.name = g + ":" + a
		if md, q := pomManaged(chain, g, a); md != nil {
			if e.version.val == "" {
				e.version, e.owner = md.version, q
			}
			if d.scope.val == "" {
				e.dev = md.scope.val == "test"
			}
		}
		if pomReactor(e.version.val) || seen[e.name] {
			continue
		}
		seen[e.name] = true
		res = append(res, e)
	}
	var managed []pomEntry
	for i := range p.managed {
		d := &p.managed[i]
		name := pomResolve(chain, d.group.val) + ":" + pomResolve(chain, d.artifact.val)
		if pomReactor(d.version.val) || seen[name] {
			continue
		}
		seen[name] = true
		managed = append(managed, pomEntry{name: name, dep: d, version: d.version, owner: p, line: d.line, dev: d.scope.val == "test"})
	}
	slices.SortStableFunc(managed, func(a, b pomEntry) int { return strings.Compare(a.name, b.name) })
	for i := range res {
		res[i].line = pomLine(chain, res[i])
	}
	for i := range managed {
		managed[i].line = pomLine(chain, managed[i])
	}
	return append(res, managed...)
}

// pomLine is the line of the manifest that decides the entry's version: the
// property when this pom defines it, else the <version> element, else the
// <dependency> itself.
func pomLine(chain []*pom, e pomEntry) int {
	if src, ok := pomSourceOf(chain, e.owner, e.version); ok && src.pom == chain[0] {
		return src.line
	}
	if e.owner == chain[0] && e.version.line > 0 {
		return e.version.line
	}
	return e.line
}

func (maven) Deps(root string, m core.Manifest) ([]core.Dep, error) {
	chain, err := pomChain(root, m.Path)
	if err != nil {
		return nil, err
	}
	var res []core.Dep
	for _, e := range pomEntries(chain) {
		v := pomResolve(chain, e.version.val)
		if strings.Contains(v, "${") {
			v = ""
		}
		res = append(res, core.Dep{
			Ecosystem: core.Maven,
			Name:      e.name,
			Version:   v,
			Req:       e.version.val,
			Manifest:  m,
			Line:      e.line,
			Dev:       e.dev,
			Direct:    e.direct,
		})
	}
	return res, nil
}

func (maven) Meta(ctx context.Context, f core.Fetcher, name string) (*core.Meta, error) {
	return mavenMeta(ctx, f, name, mavenCentral)
}

type mavenMetadata struct {
	Versioning struct {
		Release  string   `xml:"release"`
		Versions []string `xml:"versions>version"`
	} `xml:"versioning"`
}

type mavenPom struct {
	Parent struct {
		Group    string `xml:"groupId"`
		Artifact string `xml:"artifactId"`
		Version  string `xml:"version"`
	} `xml:"parent"`
	URL string `xml:"url"`
	SCM struct {
		URL        string `xml:"url"`
		Connection string `xml:"connection"`
	} `xml:"scm"`
	Relocation struct {
		Group    string `xml:"groupId"`
		Artifact string `xml:"artifactId"`
		Message  string `xml:"message"`
	} `xml:"distributionManagement>relocation"`
}

// mavenMeta reads maven-metadata.xml from the first repository that has the
// artifact, then the latest release's POM for the source repo and a
// relocation. Release times come from Central's search, which is often slow
// or down; without it times stay zero.
func mavenMeta(ctx context.Context, f core.Fetcher, name string, repos ...string) (*core.Meta, error) {
	g, a, ok := strings.Cut(name, ":")
	if !ok || g == "" || a == "" {
		return nil, fmt.Errorf("maven: %q is not groupId:artifactId", name)
	}
	dir := strings.ReplaceAll(g, ".", "/") + "/" + a
	var md mavenMetadata
	repo := ""
	for _, r := range repos {
		res, err := f.Get(ctx, r+"/"+dir+"/maven-metadata.xml")
		if err != nil {
			return nil, err
		}
		if res.Status == 404 {
			continue
		}
		if res.Status != 200 {
			return nil, fmt.Errorf("GET %s/%s/maven-metadata.xml: status %d", r, dir, res.Status)
		}
		if err := xml.Unmarshal(res.Body, &md); err != nil {
			return nil, fmt.Errorf("maven %s: %w", name, err)
		}
		repo = r
		break
	}
	if repo == "" {
		return nil, fmt.Errorf("maven %s: not found in %s", name, strings.Join(repos, ", "))
	}
	meta := &core.Meta{Name: name}
	var times map[string]time.Time
	if repo == mavenCentral {
		times = mavenTimes(ctx, f, g, a)
	}
	for _, v := range md.Versioning.Versions {
		v = strings.TrimSpace(v)
		if v == "" || strings.HasSuffix(strings.ToUpper(v), "-SNAPSHOT") {
			continue
		}
		meta.Releases = append(meta.Releases, core.Release{Version: v, Time: times[v], Pre: mvnVersion(v).Pre != ""})
	}
	slices.SortFunc(meta.Releases, func(x, y core.Release) int { return mvnCompare(x.Version, y.Version) })
	meta.Releases = slices.CompactFunc(meta.Releases, func(x, y core.Release) bool { return x.Version == y.Version })
	meta.Latest = md.Versioning.Release
	if meta.Latest == "" || strings.HasSuffix(strings.ToUpper(meta.Latest), "-SNAPSHOT") {
		meta.Latest = ""
		for _, r := range meta.Releases {
			if !r.Pre {
				meta.Latest = r.Version
			}
		}
	}
	if meta.Latest == "" {
		return meta, nil
	}
	pm, err := mavenFetchPom(ctx, f, repo, g, a, meta.Latest)
	if err != nil || pm == nil {
		return meta, err
	}
	if rl := pm.Relocation; rl.Group != "" || rl.Artifact != "" {
		if to := cmp.Or(rl.Group, g) + ":" + cmp.Or(rl.Artifact, a); to != name {
			meta.Replacement = to
			meta.Deprecated = cmp.Or(strings.TrimSpace(rl.Message), "relocated to "+to)
		}
	}
	// Projects often declare <scm> once, in a parent POM.
	for i := 0; i < 3 && pm != nil; i++ {
		if meta.Repo = cmp.Or(githubRepo(pm.SCM.URL), githubRepo(pm.SCM.Connection), githubRepo(pm.URL)); meta.Repo != "" {
			break
		}
		p := pm.Parent
		if p.Group == "" || p.Artifact == "" || p.Version == "" {
			break
		}
		if pm, err = mavenFetchPom(ctx, f, repo, p.Group, p.Artifact, p.Version); err != nil {
			return nil, err
		}
	}
	return meta, nil
}

// mavenFetchPom reads one POM from repo, nil when the repo does not have it.
func mavenFetchPom(ctx context.Context, f core.Fetcher, repo, g, a, v string) (*mavenPom, error) {
	res, err := f.Get(ctx, repo+"/"+strings.ReplaceAll(g, ".", "/")+"/"+a+"/"+v+"/"+a+"-"+v+".pom")
	if err != nil {
		return nil, err
	}
	var pm mavenPom
	if res.Status != 200 || xml.Unmarshal(res.Body, &pm) != nil {
		return nil, nil
	}
	return &pm, nil
}

func mavenTimes(ctx context.Context, f core.Fetcher, g, a string) map[string]time.Time {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	q := "g:%22" + url.QueryEscape(g) + "%22+AND+a:%22" + url.QueryEscape(a) + "%22"
	res, err := f.Get(ctx, mavenSearch+"?q="+q+"&core=gav&rows=400&wt=json")
	if err != nil || res.Status != 200 {
		return nil
	}
	var doc struct {
		Response struct {
			Docs []struct {
				V         string `json:"v"`
				Timestamp int64  `json:"timestamp"`
			} `json:"docs"`
		} `json:"response"`
	}
	if json.Unmarshal(res.Body, &doc) != nil {
		return nil
	}
	times := map[string]time.Time{}
	for _, d := range doc.Response.Docs {
		times[d.V] = time.UnixMilli(d.Timestamp).UTC()
	}
	return times
}

// mvnVersion reads a Maven version for ordering: .Final, .RELEASE and .GA
// equal the plain version, alpha, beta, RC, CR and milestones (M1) are
// pre-releases, and other qualifiers such as -jre are variants.
func mvnVersion(s string) ver.V {
	low := strings.ToLower(s)
	for _, q := range []string{"final", "release", "ga"} {
		for _, sep := range []string{".", "-"} {
			low = strings.TrimSuffix(low, sep+q)
		}
	}
	v, _ := ver.Parse(low)
	v.Raw = s
	return v
}

func mvnCompare(a, b string) int {
	return cmp.Or(ver.Compare(mvnVersion(a), mvnVersion(b)), strings.Compare(a, b))
}

func (maven) Bump(ctx context.Context, root string, d core.Dep, to, toName string) (*core.Bump, error) {
	chain, err := pomChain(root, d.Manifest.Path)
	if err != nil {
		return nil, err
	}
	var e *pomEntry
	for _, x := range pomEntries(chain) {
		if x.name == d.Name {
			e = &x
			break
		}
	}
	if e == nil {
		return nil, fmt.Errorf("%s: no dependency on %s", d.Manifest.Path, d.Name)
	}
	src, ok := pomSourceOf(chain, e.owner, e.version)
	switch {
	case !ok && e.version.val == "" && chain[len(chain)-1].parent.artifact.val != "":
		top := chain[len(chain)-1].parent
		return nil, fmt.Errorf("%s: the version of %s comes from the dependencyManagement of parent %s:%s:%s, outside this repository: %w",
			d.Manifest.Path, d.Name, top.group.val, top.artifact.val, top.version.val, core.ErrNotSupported)
	case !ok:
		return nil, fmt.Errorf("%s: version %q of %s is not a plain value: %w", d.Manifest.Path, e.version.val, d.Name, core.ErrNotSupported)
	}
	if toName != "" {
		if err := pomSwappable(chain[0], e, toName); err != nil {
			return nil, err
		}
	}
	var files []string
	if src.text != to {
		if err := pomSet(root, src.pom.rel, src.line, src.text, to); err != nil {
			return nil, err
		}
		files = append(files, src.pom.rel)
	}
	if toName != "" {
		if err := pomSwap(root, chain[0], e, toName); err != nil {
			return nil, err
		}
		files = append(files, chain[0].rel)
	}
	slices.Sort(files)
	return &core.Bump{Files: slices.Compact(files), Note: "no lockfile"}, nil
}

// pomSwappable refuses a swap before anything is written. A dependency whose
// version comes from dependencyManagement, in this pom or a parent, has none
// of its own: renaming it would leave the new artifact without a version.
func pomSwappable(p *pom, e *pomEntry, toName string) error {
	_, _, ok := strings.Cut(toName, ":")
	switch {
	case !ok || e.dep == nil || strings.Contains(e.dep.group.val+e.dep.artifact.val, "${"):
		return fmt.Errorf("%s: replacing %s with %s: %w", p.rel, e.name, toName, core.ErrNotSupported)
	case e.owner != p || e.dep.version.val == "":
		return fmt.Errorf("%s: replacing %s with %s: its version is managed in %s: %w", p.rel, e.name, toName, e.owner.rel, core.ErrNotSupported)
	}
	return nil
}

func pomSwap(root string, p *pom, e *pomEntry, toName string) error {
	if err := pomSwappable(p, e, toName); err != nil {
		return err
	}
	g, a, _ := strings.Cut(toName, ":")
	if err := pomSet(root, p.rel, e.dep.group.line, e.dep.group.val, g); err != nil {
		return err
	}
	return pomSet(root, p.rel, e.dep.artifact.line, e.dep.artifact.val, a)
}

// pomSet replaces the text of an element on its line, matching the element
// boundaries when they sit on the same line.
func pomSet(root, rel string, line int, from, to string) error {
	if from == to {
		return nil
	}
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := rewriteLine(p, line, ">"+from+"<", ">"+to+"<"); err == nil {
		return nil
	}
	return rewriteLine(p, line, from, to)
}

func (maven) Commands(root string, m core.Manifest) core.Commands {
	dir := mavenReactor(root, path.Dir(m.Path))
	mvn := "mvn"
	if onDisk(root, path.Join(dir, "mvnw")) {
		mvn = "./mvnw"
	}
	return core.Commands{
		Build: []string{inDir(dir, mvn+" -q -B -DskipTests package")},
		Test:  []string{inDir(dir, mvn+" -q -B verify")},
	}
}

// mavenReactor climbs to the top pom whose <modules> include dir, since a
// module only builds against its siblings from there.
func mavenReactor(root, dir string) string {
	for dir != "." {
		up := path.Dir(dir)
		p, err := readPom(root, path.Join(up, "pom.xml"))
		if err != nil || !slices.ContainsFunc(p.modules, func(m string) bool { return path.Join(up, m) == dir }) {
			break
		}
		dir = up
	}
	return dir
}

// Traits says Maven cannot move a transitive dependency by itself: it has
// no lockfile, and only a direct declaration overrides what a parent brings.
func (maven) Traits() core.Traits {
	return core.Traits{OSV: "Maven", Page: centralPage, KeepSuffix: true}
}

func centralPage(name string) string {
	return "https://central.sonatype.com/artifact/" + strings.Replace(name, ":", "/", 1)
}

func (maven) Imports(d core.Dep) []string { return mavenImports(d.Name) }

// mavenPackages are artifacts whose Java package is not their groupId.
var mavenPackages = map[string]string{
	"com.fasterxml.jackson.core:jackson-annotations": "com.fasterxml.jackson.annotation",
	"com.fasterxml.jackson.core:jackson-core":        "com.fasterxml.jackson.core",
	"com.fasterxml.jackson.core:jackson-databind":    "com.fasterxml.jackson.databind",
	"com.google.code.gson:gson":                      "com.google.gson",
	"com.google.guava:guava":                         "com.google.common",
	"com.squareup.okhttp3:okhttp":                    "okhttp3",
	"commons-codec:commons-codec":                    "org.apache.commons.codec",
	"commons-io:commons-io":                          "org.apache.commons.io",
	"junit:junit":                                    "org.junit",
	"org.apache.commons:commons-collections4":        "org.apache.commons.collections4",
	"org.apache.commons:commons-lang3":               "org.apache.commons.lang3",
	"org.apache.commons:commons-text":                "org.apache.commons.text",
	"org.jetbrains.kotlinx:kotlinx-coroutines-core":  "kotlinx.coroutines",
	"org.projectlombok:lombok":                       "lombok",
}

// mavenImports is the package prefix code imports an artifact by: the
// groupId (org.springframework.boot, io.github.x), unless the table knows
// better.
func mavenImports(name string) []string {
	if p, ok := mavenPackages[name]; ok {
		return []string{p}
	}
	g, _, _ := strings.Cut(name, ":")
	return []string{g}
}
