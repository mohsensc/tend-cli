package eco

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/ver"
)

// actionsNodeDefault is the Node major the runners run actions on. GitHub removes
// Node 20 from the runners on 2026-09-23; an action that still declares an older
// runtime stops working there.
const actionsNodeDefault = 24

const (
	actionsMajors = 12
	actionsPages  = 5
)

type actions struct{}

func (actions) Name() string { return core.Actions }

// actionsShas remembers the tag listing of each repo (tag -> commit) for Bump,
// which has no fetcher but has to write the commit of a sha-pinned ref.
var actionsShas sync.Map

func (actions) Detect(root string) ([]core.Manifest, error) {
	files, err := walk(root, ".github/workflows/*.yml", ".github/workflows/*.yaml", "action.yml", "action.yaml")
	if err != nil {
		return nil, err
	}
	var res []core.Manifest
	for _, f := range files {
		if strings.HasPrefix(path.Base(f), "action.") && !compositeAction(filepath.Join(root, filepath.FromSlash(f))) {
			continue
		}
		res = append(res, core.Manifest{Ecosystem: core.Actions, Path: f, Tool: "actions"})
	}
	return res, nil
}

func compositeAction(p string) bool {
	var doc struct {
		Runs struct{ Using string }
	}
	return readYAML(p, &doc) == nil && doc.Runs.Using == "composite"
}

type actionUse struct {
	value string
	line  int
}

func actionUses(src []byte) ([]actionUse, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	top := doc.Content[0]
	var res []actionUse
	add := func(n *yaml.Node) {
		if u := yamlGet(n, "uses"); u != nil && u.Kind == yaml.ScalarNode {
			res = append(res, actionUse{u.Value, u.Line})
		}
	}
	steps := func(n *yaml.Node) {
		if s := yamlGet(n, "steps"); s != nil {
			for _, step := range s.Content {
				add(step)
			}
		}
	}
	if jobs := yamlGet(top, "jobs"); jobs != nil {
		for i := 1; i < len(jobs.Content); i += 2 {
			add(jobs.Content[i])
			steps(jobs.Content[i])
		}
	}
	steps(yamlGet(top, "runs"))
	return res, nil
}

var (
	shaRe       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	trailingVer = regexp.MustCompile(`#\s*(v?\d[0-9A-Za-z.+-]*)`)
	usesLineRe  = regexp.MustCompile(`uses:\s*(["']?)([^\s"'#]+)`)
)

func splitUses(v string) (repo, sub, ref string, ok bool) {
	target, ref, found := strings.Cut(v, "@")
	if !found || strings.HasPrefix(v, "./") || strings.HasPrefix(v, "docker://") {
		return "", "", "", false
	}
	parts := strings.SplitN(target, "/", 3)
	if len(parts) < 2 {
		return "", "", "", false
	}
	if len(parts) == 3 {
		sub = "/" + parts[2]
	}
	return parts[0] + "/" + parts[1], sub, ref, true
}

func (actions) Deps(root string, m core.Manifest) ([]core.Dep, error) {
	p := filepath.Join(root, filepath.FromSlash(m.Path))
	src, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	uses, err := actionUses(src)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", m.Path, err)
	}
	lines := strings.Split(string(src), "\n")
	var res []core.Dep
	for _, u := range uses {
		repo, _, ref, ok := splitUses(u.value)
		if !ok {
			continue
		}
		d := core.Dep{Ecosystem: core.Actions, Name: repo, Version: ref, Req: ref, Manifest: m, Line: u.line, Direct: true}
		if shaRe.MatchString(ref) {
			d.Pin = ref
			if c := trailingVer.FindStringSubmatch(lineAfter(lines, u.line, ref)); c != nil {
				d.Version = c[1]
			}
		}
		res = append(res, d)
	}
	return res, nil
}

func lineAfter(lines []string, line int, s string) string {
	if line < 1 || line > len(lines) {
		return ""
	}
	_, after, _ := strings.Cut(lines[line-1], s)
	return after
}

type actionRepo struct {
	Archived bool `json:"archived"`
}

func (actions) Meta(ctx context.Context, f core.Fetcher, name string) (*core.Meta, error) {
	res, tags, err := githubMeta(ctx, f, name)
	if err != nil {
		return nil, fmt.Errorf("actions: %w", err)
	}
	actionsShas.Store(name, tags)
	actionRuntimes(ctx, f, name, res.Releases)
	return res, nil
}

func actionTags(ctx context.Context, f core.Fetcher, next string) (map[string]string, error) {
	var res map[string]string
	for page := 0; next != "" && page < actionsPages; page++ {
		got, err := f.Get(ctx, next)
		if err != nil {
			return nil, err
		}
		switch got.Status {
		case 404:
			return res, nil
		case 200:
		default:
			return nil, fmt.Errorf("%s: HTTP %d", next, got.Status)
		}
		var list []struct {
			Name   string `json:"name"`
			Commit struct {
				Sha string `json:"sha"`
			} `json:"commit"`
		}
		if err := json.Unmarshal(got.Body, &list); err != nil {
			return nil, fmt.Errorf("%s: %w", next, err)
		}
		if res == nil {
			res = map[string]string{}
		}
		for _, t := range list {
			res[t.Name] = t.Commit.Sha
		}
		next = nextPageLink("", http.Header(got.Header).Get("Link"))
	}
	return res, nil
}

var nodeUsing = regexp.MustCompile(`^node(\d+)$`)

// actionRuntimes marks the releases whose action.yml still names a Node runtime
// older than the runners'. A major's runtime is read once, from its newest tag:
// moving to a newer Node is itself a major release for actions.
func actionRuntimes(ctx context.Context, f core.Fetcher, name string, rs []core.Release) {
	byMajor := map[int]string{}
	var majors []int
	for _, r := range slices.Backward(rs) {
		v, _ := ver.Parse(r.Version)
		if _, ok := byMajor[v.Major()]; !ok && !r.Pre {
			byMajor[v.Major()] = r.Version
			majors = append(majors, v.Major())
		}
	}
	for _, major := range majors[:min(len(majors), actionsMajors)] {
		using := actionUsing(ctx, f, name, byMajor[major])
		m := nodeUsing.FindStringSubmatch(using)
		if m == nil {
			continue
		}
		if n, _ := strconv.Atoi(m[1]); n >= actionsNodeDefault {
			continue
		}
		for i := range rs {
			if v, _ := ver.Parse(rs[i].Version); v.Major() == major {
				rs[i].Deprecated = "runs on " + using + ", removed from GitHub runners"
			}
		}
	}
}

func actionUsing(ctx context.Context, f core.Fetcher, name, tag string) string {
	for _, file := range []string{"action.yml", "action.yaml"} {
		var doc struct {
			Content string `json:"content"`
		}
		u := "https://api.github.com/repos/" + name + "/contents/" + file + "?ref=" + tag
		ok, err := getDoc(ctx, f, u, &doc, func(o *core.FetchOpts) { o.TTL = 24 * time.Hour })
		if err != nil || !ok {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(doc.Content, "\n", ""))
		if err != nil {
			return ""
		}
		var act struct {
			Runs struct{ Using string }
		}
		if yaml.Unmarshal(raw, &act) != nil {
			return ""
		}
		return act.Runs.Using
	}
	return ""
}

func (actions) Bump(ctx context.Context, root string, d core.Dep, to, toName string) (*core.Bump, error) {
	p := filepath.Join(root, filepath.FromSlash(d.Manifest.Path))
	lines, err := readLines(p)
	if err != nil {
		return nil, err
	}
	if d.Line < 1 || d.Line > len(lines) {
		return nil, fmt.Errorf("actions: %s: no line %d in %s", d.Name, d.Line, d.Manifest.Path)
	}
	line := strings.TrimRight(lines[d.Line-1], "\r")
	m := usesLineRe.FindStringSubmatchIndex(line)
	if m == nil {
		return nil, fmt.Errorf("actions: %s: no uses: on %s:%d", d.Name, d.Manifest.Path, d.Line)
	}
	repo, sub, ref, ok := splitUses(line[m[4]:m[5]])
	if !ok || !strings.EqualFold(repo, d.Name) {
		return nil, fmt.Errorf("actions: %s: %s:%d does not use it", d.Name, d.Manifest.Path, d.Line)
	}
	if toName != "" {
		repo = toName
	}
	var next, comment string
	switch old, isVer := ver.Parse(ref); {
	case shaRe.MatchString(ref):
		sha, tag, err := actionPin(repo, to)
		if err != nil {
			return nil, err
		}
		next, comment = sha, tag
	case isVer:
		next = to
		if n := len(old.Nums); n < 3 {
			lead := ""
			if strings.HasPrefix(ref, "v") {
				lead = "v"
			}
			next = lead + shape(to, n)
		}
	default:
		return nil, fmt.Errorf("actions: %s: ref %q is not a version", d.Name, ref)
	}
	out := line[:m[4]] + repo + sub + "@" + next + line[m[5]:]
	if comment != "" {
		out = withTagComment(out, comment)
	}
	res := &core.Bump{Note: "no lockfile"}
	if out == line {
		return res, nil
	}
	if err := rewriteLine(p, d.Line, line, out); err != nil {
		return nil, err
	}
	res.Files = []string{d.Manifest.Path}
	return res, nil
}

func withTagComment(line, tag string) string {
	if m := trailingVer.FindStringSubmatchIndex(line); m != nil {
		return line[:m[2]] + tag + line[m[3]:]
	}
	return line + " # " + tag
}

func actionPin(repo, tag string) (sha, name string, err error) {
	v, ok := actionsShas.Load(repo)
	if !ok {
		return "", "", fmt.Errorf("actions: %s: no tag listing; run Meta first", repo)
	}
	tags := v.(map[string]string)
	sha = tags[tag]
	if sha == "" {
		return "", "", fmt.Errorf("actions: %s: tag %s not in the listing", repo, tag)
	}
	name, best := tag, 0
	for t, s := range tags {
		if pv, ok := ver.Parse(t); ok && s == sha && (len(pv.Nums) > best || (len(pv.Nums) == best && t < name)) {
			name, best = t, len(pv.Nums)
		}
	}
	return sha, name, nil
}

func (actions) Commands(root string, m core.Manifest) core.Commands { return core.Commands{} }

func (actions) Traits() core.Traits {
	return core.Traits{OSV: "GitHub Actions", Page: prefix("https://github.com/"), KeepShape: true, Tooling: true}
}

func (actions) Imports(d core.Dep) []string { return nil }

// retired are the rules that move a workflow off the set-output and
// save-state commands GitHub retired, onto its environment files: a pattern
// matched against a line and a Go regexp template for what replaces the match.
var retired = func() []retiredRule {
	var res []retiredRule
	for _, c := range []struct{ cmd, file string }{{"set-output", "GITHUB_OUTPUT"}, {"save-state", "GITHUB_STATE"}} {
		pat := `echo %s::` + c.cmd + ` name=([\w.-]+)::(.*)%s\s*$`
		res = append(res,
			retiredRule{regexp.MustCompile(fmt.Sprintf(pat, `"`, `"`)), `echo "${1}=${2}" >> "$$` + c.file + `"`},
			retiredRule{regexp.MustCompile(fmt.Sprintf(pat, `'`, `'`)), `echo '${1}=${2}' >> "$$` + c.file + `"`},
			retiredRule{regexp.MustCompile(`echo ::` + c.cmd + ` name=([\w.-]+)::(.*?)\s*$`), `echo "${1}=${2}" >> "$$` + c.file + `"`},
		)
	}
	return res
}()

type retiredRule struct {
	re   *regexp.Regexp
	tmpl string
}

// RetiredCommands is the codemod that moves the given workflow lines off the
// retired commands: one literal substitution for each line that needs it, nil
// when none does.
func RetiredCommands(lines []string) *core.Codemod {
	text := map[string]string{}
	for _, line := range lines {
		for _, r := range retired {
			if m := r.re.FindString(line); m != "" {
				text[m] = r.re.ReplaceAllString(m, r.tmpl)
				break
			}
		}
	}
	if len(text) == 0 {
		return nil
	}
	return &core.Codemod{Text: text}
}
