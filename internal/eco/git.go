package eco

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/ver"
)

// gitRefs follows the tags a project pins outside any package manager: hook
// repos in .pre-commit-config.yaml, Terraform modules taken from git, the git
// dependencies the package ecosystems leave aside (package.json, pip's
// git+https requirements, Cargo and Gemfile entries with a tag), and release
// downloads in scripts, Dockerfiles, Makefiles and workflows. Each is a
// GitHub repo at a tag, so its releases are the repo's tags, read the way the
// Actions ecosystem reads them. A dep is named owner/repo.
type gitRefs struct{}

func (gitRefs) Name() string { return core.Git }

var (
	tfGitSource   = regexp.MustCompile(`source\s*=\s*"git::(?:https://|ssh://git@)github\.com[/:]([\w.-]+)/([\w.-]+?)(?:\.git)?(?://[^"?]*)?\?ref=([\w.-]+)"`)
	npmGitSpec    = regexp.MustCompile(`"(?:github:|git\+https://github\.com/|git\+ssh://git@github\.com[:/]|https://github\.com/)([\w.-]+)/([\w.-]+?)(?:\.git)?#(?:semver:)?(v?\d[\w.-]*)"`)
	releaseDL     = regexp.MustCompile(`github\.com/([\w.-]+)/([\w.-]+)/releases/download/(v?\d[\w.-]*)/`)
	frozenComment = regexp.MustCompile(`#\s*frozen:\s*(v?\d[\w.-]*)`)
	githubRepoURL = regexp.MustCompile(`^(?:https://|git@|ssh://git@)github\.com[/:]([\w.-]+)/([\w.-]+?)(?:\.git)?/?$`)
	pipGitSpec    = regexp.MustCompile(`git\+(?:https://|ssh://git@)github\.com[/:]([\w.-]+)/([\w.-]+?)(?:\.git)?@(v?\d[\w.-]*)`)
	cargoGitSpec  = regexp.MustCompile(`\bgit\s*=\s*"(?:https://|ssh://git@)github\.com[/:]([\w.-]+)/([\w.-]+?)(?:\.git)?"[^\n]*?\btag\s*=\s*"(v?\d[\w.-]*)"`)
	gemGitSpec    = regexp.MustCompile(`(?:\bgit(?:hub)?:|:git(?:hub)?\s*=>)\s*["'](?:https://github\.com/)?([\w.-]+)/([\w.-]+?)(?:\.git)?["'][^\n]*?(?:\btag:|:tag\s*=>)\s*["'](v?\d[\w.-]*)["']`)
)

// gitLocks are the lockfiles that record which commit a git dependency is at.
var gitLocks = map[string]string{"Cargo.toml": "Cargo.lock", "Gemfile": "Gemfile.lock"}

var (
	crateKey = regexp.MustCompile(`^\s*([\w-]+)\s*=\s*\{`)
	crateAs  = regexp.MustCompile(`\bpackage\s*=\s*"([\w-]+)"`)
	gemName  = regexp.MustCompile(`^\s*gem\s+["']([\w.-]+)["']`)
)

// gitShas remembers each repo's tags, name to commit, for writing a pin that
// is a commit; Meta fills it.
var gitShas sync.Map

func (gitRefs) Detect(root string) ([]core.Manifest, error) {
	files, err := walk(root, ".pre-commit-config.yaml", "*.tf", "package.json", "Dockerfile", "Dockerfile.*", "*.dockerfile",
		"Makefile", "*.mk", "*.sh", ".github/workflows/*.yml", ".github/workflows/*.yaml",
		"requirements*.txt", "pyproject.toml", "setup.cfg", "Pipfile", "Cargo.toml", "Gemfile")
	if err != nil {
		return nil, err
	}
	var res []core.Manifest
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
		if err != nil {
			continue
		}
		if len(gitPins(f, b)) == 0 {
			continue
		}
		m := core.Manifest{Ecosystem: core.Git, Path: f, Tool: "git"}
		if lock := gitLocks[path.Base(f)]; lock != "" && onDisk(root, path.Join(path.Dir(f), lock)) {
			m.Lock = path.Join(path.Dir(f), lock)
		}
		res = append(res, m)
	}
	return res, nil
}

func (gitRefs) Deps(root string, m core.Manifest) ([]core.Dep, error) {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(m.Path)))
	if err != nil {
		return nil, err
	}
	res := gitPins(m.Path, b)
	for i := range res {
		res[i].Manifest = m
	}
	return res, nil
}

// gitPins finds the refs one file pins.
func gitPins(file string, b []byte) []core.Dep {
	pin := func(owner, repo, ref string, line int) core.Dep {
		return core.Dep{Ecosystem: core.Git, Name: owner + "/" + repo, Version: ref, Req: ref, Line: line, Direct: true}
	}
	if path.Base(file) == ".pre-commit-config.yaml" {
		return preCommitPins(b, pin)
	}
	re := releaseDL
	switch base := path.Base(file); {
	case strings.HasSuffix(file, ".tf"):
		re = tfGitSource
	case base == "package.json":
		re = npmGitSpec
	case base == "Cargo.toml":
		re = cargoGitSpec
	case base == "Gemfile":
		re = gemGitSpec
	case base == "pyproject.toml", base == "setup.cfg", base == "Pipfile", strings.HasPrefix(base, "requirements") && strings.HasSuffix(base, ".txt"):
		re = pipGitSpec
	}
	var res []core.Dep
	for i, l := range strings.Split(string(b), "\n") {
		for _, m := range re.FindAllStringSubmatch(l, -1) {
			res = append(res, pin(m[1], m[2], m[3], i+1))
		}
	}
	return res
}

func preCommitPins(b []byte, pin func(owner, repo, ref string, line int) core.Dep) []core.Dep {
	var doc yaml.Node
	if yaml.Unmarshal(b, &doc) != nil || len(doc.Content) == 0 {
		return nil
	}
	repos := yamlGet(doc.Content[0], "repos")
	if repos == nil {
		return nil
	}
	lines := strings.Split(string(b), "\n")
	var res []core.Dep
	for _, r := range repos.Content {
		u, rev := yamlGet(r, "repo"), yamlGet(r, "rev")
		if u == nil || rev == nil {
			continue
		}
		m := githubRepoURL.FindStringSubmatch(u.Value)
		if m == nil {
			continue
		}
		d := pin(m[1], m[2], rev.Value, rev.Line)
		if shaRe.MatchString(rev.Value) {
			d.Pin = rev.Value
			c := frozenComment.FindStringSubmatch(lines[rev.Line-1])
			if c == nil {
				continue
			}
			d.Version = c[1]
		} else if _, ok := ver.Parse(rev.Value); !ok {
			continue
		}
		res = append(res, d)
	}
	return res
}

func (gitRefs) Meta(ctx context.Context, f core.Fetcher, name string) (*core.Meta, error) {
	m, tags, err := githubMeta(ctx, f, name)
	if err != nil {
		return nil, fmt.Errorf("git: %w", err)
	}
	gitShas.Store(name, tags)
	return m, nil
}

// Bump writes the new tag where the old one was. A pin that is a commit gets
// the new tag's commit and its frozen comment.
func (gitRefs) Bump(ctx context.Context, root string, d core.Dep, to, toName string) (*core.Bump, error) {
	if toName != "" {
		return nil, fmt.Errorf("git: %s: %w", d.Name, core.ErrNotSupported)
	}
	p := filepath.Join(root, filepath.FromSlash(d.Manifest.Path))
	lines, err := readLines(p)
	if err != nil {
		return nil, err
	}
	if d.Line < 1 || d.Line > len(lines) {
		return nil, fmt.Errorf("git: %s: no line %d in %s", d.Name, d.Line, d.Manifest.Path)
	}
	line := strings.TrimRight(lines[d.Line-1], "\r")
	next, ok := releaseURLs(line, d.Name, d.Req, to)
	if !ok {
		next = strings.Replace(line, d.Req, to, 1)
	}
	if d.Pin != "" {
		v, ok := gitShas.Load(d.Name)
		sha := ""
		if ok {
			sha = v.(map[string]string)[to]
		}
		if sha == "" {
			return nil, fmt.Errorf("git: %s: no commit for %s; run Meta first", d.Name, to)
		}
		next = strings.Replace(line, d.Pin, sha, 1)
		if m := frozenComment.FindStringSubmatchIndex(next); m != nil {
			next = next[:m[2]] + to + next[m[3]:]
		}
	}
	if next == line {
		return &core.Bump{Note: "no lockfile"}, nil
	}
	if err := rewriteLine(p, d.Line, line, next); err != nil {
		return nil, err
	}
	res := &core.Bump{Files: []string{d.Manifest.Path}, Note: "no lockfile"}
	if d.Manifest.Lock == "" {
		return res, nil
	}
	tool, cmd := "cargo", ""
	if m := crateKey.FindStringSubmatch(line); m != nil {
		name := m[1]
		if as := crateAs.FindStringSubmatch(line); as != nil {
			name = as[1]
		}
		cmd = "cargo update -p " + shArg(name)
	} else if m := gemName.FindStringSubmatch(line); m != nil {
		tool, cmd = "bundle", "bundle lock --update "+shArg(m[1])
	}
	switch {
	case cmd == "":
		res.Note = "cannot tell which package the lockfile records it under"
	case !have(tool):
		res.Note = core.ToolMissing{Tool: tool}.Error()
	default:
		if err := runIn(ctx, root, path.Dir(d.Manifest.Path), nil, cmd); err != nil {
			return nil, fmt.Errorf("git: %s: %w", d.Name, err)
		}
		lock, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(d.Manifest.Lock)))
		if err != nil {
			return nil, err
		}
		if !gitLockHasTag(lock, d.Name, to) {
			return nil, fmt.Errorf("git: %s: %s does not record tag %s after the update", d.Name, d.Manifest.Lock, to)
		}
		res.Files, res.Locked, res.Note = append(res.Files, d.Manifest.Lock), true, ""
	}
	return res, nil
}

// releaseURLs moves every release download of repo at tag from on line to
// to. The tag is a path segment, and the asset name usually carries the
// version too, with or without the v: hugo_0.120.0_linux-amd64.tar.gz.
// Each copy in the URL moves, or the download would fetch the old asset or
// none. ok is false when the line has no release download of repo.
func releaseURLs(line, repo, from, to string) (string, bool) {
	ok := false
	var b strings.Builder
	prev := 0
	for _, m := range releaseDL.FindAllStringSubmatchIndex(line, -1) {
		if !strings.EqualFold(line[m[2]:m[3]]+"/"+line[m[4]:m[5]], repo) {
			continue
		}
		// A second pin of the repo on this line finds the URL already moved.
		ok = true
		if line[m[6]:m[7]] != from {
			continue
		}
		end := m[1] + strings.IndexAny(line[m[1]:]+" ", " \t\"'`)>")
		b.WriteString(line[prev:m[6]])
		b.WriteString(to)
		b.WriteString(line[m[7]:m[1]])
		b.WriteString(replaceVersion(line[m[1]:end], strings.TrimPrefix(from, "v"), strings.TrimPrefix(to, "v")))
		prev = end
	}
	b.WriteString(line[prev:])
	return b.String(), ok
}

// replaceVersion replaces each copy of version from in s that stands alone:
// 1.2.0 in tool_1.2.0.tar.gz or tool-v1.2.0, not in 11.2.0 or 1.2.0.1.
func replaceVersion(s, from, to string) string {
	var b strings.Builder
	prev := 0
	for i := 0; i+len(from) <= len(s); {
		j := strings.Index(s[i:], from)
		if j < 0 {
			break
		}
		at, end := i+j, i+j+len(from)
		i = at + 1
		if at > 0 && (isDigitByte(s[at-1]) || s[at-1] == '.') {
			continue
		}
		if end < len(s) && (isDigitByte(s[end]) || s[end] == '.' && end+1 < len(s) && isDigitByte(s[end+1])) {
			continue
		}
		b.WriteString(s[prev:at])
		b.WriteString(to)
		prev, i = end, end
	}
	b.WriteString(s[prev:])
	return b.String()
}

func isDigitByte(c byte) bool { return c >= '0' && c <= '9' }

// gitLockHasTag reports whether the lock records repo at tag: Cargo.lock in
// a source URL, ?tag=v1.0.0#<commit>, and Gemfile.lock as tag: v1.0.0 in the
// repo's GIT section. Both keep one blank-line separated block per source.
func gitLockHasTag(lock []byte, repo, tag string) bool {
	text := strings.ReplaceAll(string(lock), "\r\n", "\n")
	for _, block := range strings.Split(text, "\n\n") {
		if !strings.Contains(strings.ToLower(block), "github.com/"+strings.ToLower(repo)) {
			continue
		}
		if strings.Contains(block, "tag="+tag+"#") || strings.Contains(block+"\n", "  tag: "+tag+"\n") {
			return true
		}
	}
	return false
}

var submodulePath = regexp.MustCompile(`(?m)^\s*path\s*=\s*(.+?)\s*$`)

// Submodules are the paths .gitmodules declares. Each is pinned to a commit
// in the tree, with no tag to read a version from, so tend does not move them.
func Submodules(root string) []string {
	b, err := os.ReadFile(filepath.Join(root, ".gitmodules"))
	if err != nil {
		return nil
	}
	var res []string
	for _, m := range submodulePath.FindAllStringSubmatch(string(b), -1) {
		res = append(res, m[1])
	}
	return res
}

func (gitRefs) Commands(root string, m core.Manifest) core.Commands { return core.Commands{} }

func (gitRefs) Traits() core.Traits { return core.Traits{} }

func (gitRefs) Imports(d core.Dep) []string { return nil }
