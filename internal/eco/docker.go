package eco

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/ver"
)

type docker struct{}

func (docker) Name() string { return core.Docker }

// dockerTags remembers the tags the project uses per image. Meta gets only the
// image name, and which tags are comparable depends on the one in use.
var dockerTags learned[string]

// dockerTagCap bounds how many tags Meta reads from a registry.
const dockerTagCap = 500

func (docker) Detect(root string) ([]core.Manifest, error) {
	files, err := walk(root, "Dockerfile", "Dockerfile.*", "*.dockerfile",
		"compose.yml", "compose.yaml", "docker-compose*.yml", "docker-compose*.yaml")
	if err != nil {
		return nil, err
	}
	var res []core.Manifest
	for _, f := range files {
		if !strings.HasSuffix(f, ".dockerignore") {
			res = append(res, core.Manifest{Ecosystem: core.Docker, Path: f, Tool: "docker"})
		}
	}
	return res, nil
}

func dockerIsCompose(p string) bool {
	e := path.Ext(p)
	return e == ".yml" || e == ".yaml"
}

type dockerRef struct {
	line    int
	raw     string // as written, variables and all
	name    string // canonical
	rawName string
	rawTag  string
	tag     string // resolved; "" when absent or not resolvable
	digest  string
	note    string
	arg     string // ARG that holds the tag
	argLine int
}

const dockerUnknown = "\x00"

var dockerVar = regexp.MustCompile(`\$\{(\w+)(?::?([-+])([^}]*))?\}|\$(\w+)`)

// dockerExpand fills ARG references from args. Names with no default come back as the
// dockerUnknown marker and in missing.
func dockerExpand(s string, args map[string]*string) (out string, missing []string) {
	out = dockerVar.ReplaceAllStringFunc(s, func(m string) string {
		sm := dockerVar.FindStringSubmatch(m)
		name := sm[1] + sm[4]
		val := args[name]
		set := val != nil && *val != ""
		switch {
		case sm[2] == "-" && set:
			return *val
		case sm[2] == "-":
			return sm[3]
		case sm[2] == "+" && set:
			return sm[3]
		case sm[2] == "+":
			return ""
		case set:
			return *val
		}
		missing = append(missing, name)
		return dockerUnknown
	})
	return out, missing
}

// dockerSplit cuts an image reference into name, tag and digest. A ${...} is one
// piece, so its colons and dashes do not count.
func dockerSplit(s string) (name, tag, digest string) {
	at, colon, depth := -1, -1, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
		case '@':
			if depth == 0 && at < 0 {
				at = i
			}
		case '/':
			if depth == 0 && at < 0 {
				colon = -1
			}
		case ':':
			if depth == 0 && at < 0 {
				colon = i
			}
		}
	}
	if at >= 0 {
		digest, s = s[at+1:], s[:at]
	}
	if colon >= 0 {
		return s[:colon], s[colon+1:], digest
	}
	return s, "", digest
}

var dockerHubHosts = []string{"docker.io/", "index.docker.io/", "registry-1.docker.io/"}

func dockerCanon(name string) string {
	for _, h := range dockerHubHosts {
		name = strings.TrimPrefix(name, h)
	}
	if !strings.Contains(name, "/") {
		return "library/" + name
	}
	return name
}

var (
	dockerArgRef     = regexp.MustCompile(`^\$\{(\w+)\}$|^\$(\w+)$`)
	dockerArgDefault = regexp.MustCompile(`^\$\{(\w+):?-([^}]*)\}$`)
)

func dockerNewRef(line int, raw string, args map[string]*string, argLines map[string]int) (dockerRef, bool) {
	r := dockerRef{line: line, raw: raw}
	r.rawName, r.rawTag, _ = dockerSplit(raw)
	expanded, missing := dockerExpand(raw, args)
	name, tag, digest := dockerSplit(expanded)
	if name == "" || strings.Contains(name, dockerUnknown) {
		return r, false
	}
	r.name = dockerCanon(name)
	r.tag, r.digest = tag, digest
	if strings.Contains(tag+digest, dockerUnknown) {
		r.tag, r.digest = "", ""
		r.note = "unresolved ARG " + strings.Join(missing, ", ")
	}
	if m := dockerArgRef.FindStringSubmatch(r.rawTag); m != nil && args[m[1]+m[2]] != nil {
		r.arg = m[1] + m[2]
		r.argLine = argLines[r.arg]
	}
	return r, true
}

func (r dockerRef) dep(m core.Manifest) core.Dep {
	version := r.tag
	if version == "" && r.note == "" {
		version = "latest"
	}
	req := r.raw
	if r.note != "" {
		req += " (" + r.note + ")"
	}
	return core.Dep{
		Ecosystem: core.Docker, Name: r.name, Version: version, Req: req,
		Manifest: m, Line: r.line, Direct: true, Pin: r.digest,
	}
}

func dockerfileRefs(lines []string) []dockerRef {
	args := map[string]*string{}
	argLines := map[string]int{}
	stages := map[string]bool{}
	var res []dockerRef
	started := false
	for i := 0; i < len(lines); i++ {
		start := i
		text := strings.TrimRight(lines[i], "\r")
		if strings.HasPrefix(strings.TrimSpace(text), "#") {
			continue
		}
		for strings.HasSuffix(text, `\`) && i+1 < len(lines) {
			i++
			text = strings.TrimSuffix(text, `\`) + " " + strings.TrimRight(lines[i], "\r")
		}
		fields := strings.Fields(text)
		if len(fields) == 0 {
			continue
		}
		switch strings.ToUpper(fields[0]) {
		case "ARG":
			for _, f := range fields[1:] {
				if started {
					break
				}
				name, def, has := strings.Cut(f, "=")
				argLines[name] = start + 1
				if has {
					def = strings.Trim(def, `"'`)
					args[name] = &def
				} else {
					args[name] = nil
				}
			}
		case "FROM":
			started = true
			rest := slices.DeleteFunc(slices.Clone(fields[1:]), func(f string) bool { return strings.HasPrefix(f, "--") })
			if len(rest) == 0 {
				continue
			}
			skip := stages[strings.ToLower(rest[0])] || rest[0] == "scratch"
			if len(rest) >= 3 && strings.EqualFold(rest[1], "as") {
				stages[strings.ToLower(rest[2])] = true
			}
			if r, ok := dockerNewRef(start+1, rest[0], args, argLines); ok && !skip {
				res = append(res, r)
			}
		}
	}
	return res
}

func composeRefs(src []byte) ([]dockerRef, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, err
	}
	var res []dockerRef
	if len(doc.Content) == 0 {
		return nil, nil
	}
	svc := yamlGet(doc.Content[0], "services")
	if svc == nil {
		return nil, nil
	}
	for i := 1; i < len(svc.Content); i += 2 {
		img := yamlGet(svc.Content[i], "image")
		if img == nil || img.Kind != yaml.ScalarNode {
			continue
		}
		if r, ok := dockerNewRef(img.Line, img.Value, nil, nil); ok {
			res = append(res, r)
		}
	}
	return res, nil
}

func yamlGet(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func dockerRead(root string, m core.Manifest) ([]dockerRef, error) {
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(m.Path)))
	if err != nil {
		return nil, err
	}
	if dockerIsCompose(m.Path) {
		return composeRefs(src)
	}
	return dockerfileRefs(strings.Split(string(src), "\n")), nil
}

func (docker) Deps(root string, m core.Manifest) ([]core.Dep, error) {
	refs, err := dockerRead(root, m)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", m.Path, err)
	}
	res := make([]core.Dep, 0, len(refs))
	for _, r := range refs {
		if r.tag != "" {
			dockerTags.add(r.name, r.tag)
		}
		res = append(res, r.dep(m))
	}
	return res, nil
}

// dockerFamily is what makes two tags comparable: the same variant suffix, the same
// "v" prefix and the same number of version parts, so 22-alpine meets 24-alpine
// but not 22.3.0-alpine or 22-slim.
type dockerFamily struct {
	lead, suffix string
	parts        int
}

func dockerFamilyOf(tag string) (dockerFamily, bool) {
	v, ok := ver.Parse(tag)
	if !ok {
		return dockerFamily{}, false
	}
	_, suffix, _ := strings.Cut(tag, "-")
	return dockerFamily{lead: tag[:len(tag)-len(strings.TrimPrefix(tag, "v"))], suffix: suffix, parts: len(v.Nums)}, true
}

// Meta lists the tags of an image. name is the canonical image name, optionally
// followed by ":tag" to say which tag is in use; without it the tags Deps saw
// are used, and plain version tags when there are none.
func (docker) Meta(ctx context.Context, f core.Fetcher, name string) (*core.Meta, error) {
	image, tags := name, dockerTags.get(name)
	if i, j := strings.LastIndex(name, ":"), strings.LastIndex(name, "/"); i > j {
		image, tags = name[:i], []string{name[i+1:]}
	}
	var fams []dockerFamily
	for _, t := range tags {
		if fam, ok := dockerFamilyOf(t); ok && !slices.Contains(fams, fam) {
			fams = append(fams, fam)
		}
	}
	if len(fams) == 0 {
		fams = []dockerFamily{{}}
	}
	dockerClient.set(f)
	var all []core.Release
	var err error
	if host, repo, ok := dockerHosted(image); ok {
		all, err = registryTags(ctx, f, host, repo)
	} else {
		all, err = hubTags(ctx, f, image, fams)
	}
	if err != nil {
		return nil, fmt.Errorf("docker: %s: %w", image, err)
	}
	res := &core.Meta{Name: image}
	for _, r := range all {
		fam, ok := dockerFamilyOf(r.Version)
		if !ok {
			continue
		}
		for _, want := range fams {
			if fam.lead == want.lead && fam.suffix == want.suffix && (want.parts == 0 || fam.parts == want.parts) {
				r.Pre = isPre(r.Version)
				res.Releases = append(res.Releases, r)
				break
			}
		}
	}
	sortReleases(res.Releases)
	return res, nil
}

// hubTags reads Docker Hub's tag listing. Filtering on the variant suffix keeps
// the listing short for families like -alpine without hiding newer majors.
func hubTags(ctx context.Context, f core.Fetcher, image string, fams []dockerFamily) ([]core.Release, error) {
	next := "https://hub.docker.com/v2/repositories/" + image + "/tags?page_size=100"
	if s := fams[0].suffix; s != "" && !slices.ContainsFunc(fams, func(x dockerFamily) bool { return x.suffix != s }) {
		next += "&name=" + url.QueryEscape("-"+s)
	}
	var res []core.Release
	for next != "" && len(res) < dockerTagCap {
		var page struct {
			Next    string `json:"next"`
			Results []struct {
				Name string `json:"name"`
			} `json:"results"`
		}
		ok, err := getDoc(ctx, f, next, &page)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("not found on Docker Hub")
		}
		// Floating tags such as 16-alpine are pushed again with every rebuild,
		// so a push time says nothing about how new a version is: no Time.
		for _, t := range page.Results {
			res = append(res, core.Release{Version: t.Name})
		}
		next = page.Next
	}
	return res, nil
}

// dockerHosted splits an image on a registry host, as in ghcr.io/acme/tool.
// ok is false for a Docker Hub image.
func dockerHosted(image string) (host, repo string, ok bool) {
	first, rest, _ := strings.Cut(image, "/")
	if strings.ContainsAny(first, ".:") || first == "localhost" {
		return first, rest, true
	}
	return "", "", false
}

// dockerHub is where Docker Hub serves the registry API; hub.docker.com
// serves only its own listing.
const dockerHub = "registry-1.docker.io"

func registryBase(host string) string {
	if h, _, _ := strings.Cut(host, ":"); h == "localhost" || h == "127.0.0.1" {
		return "http://" + host
	}
	return "https://" + host
}

var dockerChallenge = regexp.MustCompile(`(\w+)="([^"]*)"`)

// registry reads one repository of a registry v2, taking an anonymous token
// the first time it is challenged for one.
type registry struct {
	f    core.Fetcher
	repo string
	auth string
}

func (r *registry) get(ctx context.Context, u string, header map[string]string, fresh bool) (*core.Response, error) {
	try := func() (*core.Response, error) {
		h := maps.Clone(header)
		if r.auth != "" {
			if h == nil {
				h = map[string]string{}
			}
			h["Authorization"] = r.auth
		}
		return r.f.Get(ctx, u, func(o *core.FetchOpts) { o.Header, o.NoCache = h, fresh })
	}
	got, err := try()
	if err != nil || got.Status != 401 || r.auth != "" {
		return got, err
	}
	tok, err := dockerToken(ctx, r.f, http.Header(got.Header).Get("WWW-Authenticate"), r.repo)
	if err != nil {
		return nil, err
	}
	r.auth = "Bearer " + tok
	return try()
}

func registryTags(ctx context.Context, f core.Fetcher, host, repo string) ([]core.Release, error) {
	base := registryBase(host)
	next := base + "/v2/" + repo + "/tags/list"
	reg := &registry{f: f, repo: repo}
	var res []core.Release
	for next != "" && len(res) < dockerTagCap {
		got, err := reg.get(ctx, next, nil, false)
		if err != nil {
			return nil, err
		}
		switch got.Status {
		case 404:
			return nil, fmt.Errorf("not found on %s", host)
		case 200:
		default:
			return nil, fmt.Errorf("%s: HTTP %d", next, got.Status)
		}
		var page struct{ Tags []string }
		if err := json.Unmarshal(got.Body, &page); err != nil {
			return nil, fmt.Errorf("%s: %w", next, err)
		}
		for _, t := range page.Tags {
			res = append(res, core.Release{Version: t})
		}
		next = nextPageLink(base, http.Header(got.Header).Get("Link"))
	}
	return res, nil
}

func dockerToken(ctx context.Context, f core.Fetcher, challenge, repo string) (string, error) {
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		return "", fmt.Errorf("registry wants credentials")
	}
	attrs := map[string]string{}
	for _, m := range dockerChallenge.FindAllStringSubmatch(challenge, -1) {
		attrs[m[1]] = m[2]
	}
	if attrs["scope"] == "" {
		attrs["scope"] = "repository:" + repo + ":pull"
	}
	q := url.Values{"service": {attrs["service"]}, "scope": {attrs["scope"]}}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	ok, err := getDoc(ctx, f, attrs["realm"]+"?"+q.Encode(), &tok, func(o *core.FetchOpts) { o.NoCache = true })
	if err != nil {
		return "", fmt.Errorf("token from %s: %w", attrs["realm"], err)
	}
	if !ok {
		return "", fmt.Errorf("token from %s: not found", attrs["realm"])
	}
	return tok.Token + tok.AccessToken, nil
}

// dockerManifests are the manifest types a pin can name, the multi-platform
// index first: that is what a digest copied from docker pull or a registry
// page names.
var dockerManifests = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}, ", ")

var digestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// dockerDigest asks the registry which manifest image:tag names now. The
// header carries the digest; a registry that leaves it out gets it computed
// from the manifest as served.
func dockerDigest(ctx context.Context, f core.Fetcher, image, tag string) (string, error) {
	host, repo, ok := dockerHosted(image)
	if !ok {
		host, repo = dockerHub, image
	}
	u := registryBase(host) + "/v2/" + repo + "/manifests/" + tag
	reg := &registry{f: f, repo: repo}
	got, err := reg.get(ctx, u, map[string]string{"Accept": dockerManifests}, true)
	if err != nil {
		return "", err
	}
	if got.Status != 200 {
		return "", fmt.Errorf("%s: HTTP %d", u, got.Status)
	}
	dg := http.Header(got.Header).Get("Docker-Content-Digest")
	if dg == "" {
		dg = fmt.Sprintf("sha256:%x", sha256.Sum256(got.Body))
	}
	if !digestRe.MatchString(dg) {
		return "", fmt.Errorf("%s: digest %q is not sha256", u, dg)
	}
	return dg, nil
}

// dockerClient is the fetcher Meta last used. Bump gets none, and moving a
// digest-pinned image means asking the registry for the new tag's digest.
var dockerClient fetcherBox

type fetcherBox struct {
	mu sync.Mutex
	f  core.Fetcher
}

func (b *fetcherBox) set(f core.Fetcher) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.f = f
}

func (b *fetcherBox) get() core.Fetcher {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.f
}

var linkNextRe = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

func nextPageLink(base, header string) string {
	m := linkNextRe.FindStringSubmatch(header)
	if m == nil {
		return ""
	}
	if strings.HasPrefix(m[1], "/") {
		return base + m[1]
	}
	return m[1]
}

func (docker) Bump(ctx context.Context, root string, d core.Dep, to, toName string) (*core.Bump, error) {
	refs, err := dockerRead(root, d.Manifest)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(refs, func(r dockerRef) bool { return r.line == d.Line && r.name == d.Name })
	if i < 0 {
		return nil, fmt.Errorf("docker: %s: no image at %s:%d", d.Name, d.Manifest.Path, d.Line)
	}
	r := refs[i]
	name := r.rawName
	if toName != "" {
		name = strings.TrimPrefix(toName, "library/")
	}
	p := filepath.Join(root, filepath.FromSlash(d.Manifest.Path))
	next, argEdit := name+":"+to, false
	switch m := dockerArgDefault.FindStringSubmatch(r.rawTag); {
	case r.arg != "":
		next, argEdit = name+":"+r.rawTag, true
	case m != nil:
		next = name + ":${" + m[1] + ":-" + to + "}"
	case strings.Contains(r.rawTag, "$"):
		return nil, fmt.Errorf("docker: %s: tag %q is not settable: %w", d.Name, r.rawTag, core.ErrNotSupported)
	}
	pin, err := dockerRepin(ctx, d, r, cmp.Or(dockerCanonOf(toName), r.name), to)
	if err != nil {
		return nil, err
	}
	next += pin
	res := &core.Bump{Note: "no lockfile"}
	changed := false
	if argEdit {
		if changed, err = dockerSetArg(p, r, to); err != nil {
			return nil, err
		}
	}
	if next != r.raw {
		if err := rewriteLine(p, r.line, r.raw, next); err != nil {
			return nil, fmt.Errorf("docker: %s: %w", d.Name, err)
		}
		changed = true
	}
	if changed {
		res.Files = []string{d.Manifest.Path}
	}
	return res, nil
}

// dockerRepin is the "@digest" a pinned reference carries after the bump: the
// digest of the new tag, read from the registry. A pin that cannot move with
// the tag stops the bump; dropping it would unpin the image without a word.
func dockerRepin(ctx context.Context, d core.Dep, r dockerRef, image, to string) (string, error) {
	_, _, digest := dockerSplit(r.raw)
	switch {
	case digest == "":
		return "", nil
	case strings.Contains(digest, "$"):
		return "", fmt.Errorf("docker: %s: digest %q is not settable: %w", d.Name, digest, core.ErrNotSupported)
	}
	f := dockerClient.get()
	if f == nil {
		return "", fmt.Errorf("docker: %s: pinned by digest and no registry to ask for the digest of %s: %w", d.Name, to, core.ErrNotSupported)
	}
	dg, err := dockerDigest(ctx, f, image, to)
	if err != nil {
		return "", fmt.Errorf("docker: %s: pinned by digest, and the digest of %s:%s is unknown (%v): %w", d.Name, image, to, err, core.ErrNotSupported)
	}
	return "@" + dg, nil
}

func dockerCanonOf(name string) string {
	if name == "" {
		return ""
	}
	return dockerCanon(name)
}

// dockerSetArg points the ARG behind a ${TAG} reference at a new default. It
// reports whether the file changed: another stage may have done it already.
func dockerSetArg(p string, r dockerRef, to string) (bool, error) {
	lines, err := readLines(p)
	if err != nil {
		return false, err
	}
	re := regexp.MustCompile(`(\bARG\s+` + regexp.QuoteMeta(r.arg) + `\s*=\s*)(["']?)([^"'\s]*)(["']?)`)
	m := re.FindStringSubmatch(lines[r.argLine-1])
	if m == nil {
		return false, fmt.Errorf("%s:%d: ARG %s has no default", p, r.argLine, r.arg)
	}
	if m[3] == to {
		return false, nil
	}
	return true, rewriteLine(p, r.argLine, m[0], m[1]+m[2]+to+m[4])
}

func (docker) Commands(root string, m core.Manifest) core.Commands { return core.Commands{} }

func (docker) Traits() core.Traits {
	return core.Traits{Page: dockerPage, KeepShape: true, Tooling: true}
}

func dockerPage(name string) string {
	if p, ok := strings.CutPrefix(name, "library/"); ok {
		return "https://hub.docker.com/_/" + p
	}
	return "https://hub.docker.com/r/" + name
}

func (docker) Imports(d core.Dep) []string { return nil }
