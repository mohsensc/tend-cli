package eco

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestGitDeps(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		".pre-commit-config.yaml": `repos:
  - repo: https://github.com/pre-commit/pre-commit-hooks
    rev: v4.4.0
    hooks: [{id: trailing-whitespace}]
  - repo: https://github.com/psf/black
    rev: 6fdf8a4af28071ed1d079c01122b34c5d587207a  # frozen: 23.3.0
    hooks: [{id: black}]
  - repo: local
    hooks: [{id: lint}]
  - repo: https://gitlab.com/x/y
    rev: v1.0.0
`,
		"infra/main.tf": `module "vpc" {
  source = "git::https://github.com/terraform-aws-modules/terraform-aws-vpc.git?ref=v5.1.2"
}
module "local" { source = "./modules/x" }
`,
		"package.json": `{
  "dependencies": {
    "left": "github:acme/left#v1.2.0",
    "right": "git+https://github.com/acme/right.git#semver:2.0.1",
    "plain": "^1.0.0",
    "branch": "github:acme/branch#main"
  }
}
`,
		"scripts/get-yq.sh": "curl -L https://github.com/mikefarah/yq/releases/download/v4.40.5/yq_linux_amd64 -o bin/yq\ncurl -L https://github.com/x/y/releases/download/${V}/y\n",
		"Dockerfile":        "FROM alpine:3.19\nADD https://github.com/krallin/tini/releases/download/v0.19.0/tini /tini\n",
		"README.md":         "see https://github.com/a/b/releases/download/v1/x/\n",
		"requirements.txt":  "requests==2.31.0\ngit+https://github.com/acme/pylib.git@v0.3.1#egg=pylib\ngit+https://github.com/acme/edge@main#egg=edge\n",
		"Cargo.toml":        "[dependencies]\nserde = \"1\"\nfast = { git = \"https://github.com/acme/fast\", tag = \"v0.9.0\" }\nhead = { git = \"https://github.com/acme/head\", branch = \"main\" }\n",
		"Gemfile":           "gem \"rails\", \"~> 7.1\"\ngem \"tidy\", github: \"acme/tidy\", tag: \"v1.4.0\"\ngem \"old\", :git => \"https://github.com/acme/old.git\", :tag => \"2.0.0\"\n",
	})
	e := gitRefs{}
	var got []string
	for _, m := range manifestsIn(t, e, root) {
		ds, err := e.Deps(root, m)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range ds {
			got = append(got, fmt.Sprintf("%s:%d %s %s pin=%s", d.Manifest.Path, d.Line, d.Name, d.Version, d.Pin))
		}
	}
	want := []string{
		".pre-commit-config.yaml:3 pre-commit/pre-commit-hooks v4.4.0 pin=",
		".pre-commit-config.yaml:6 psf/black 23.3.0 pin=6fdf8a4af28071ed1d079c01122b34c5d587207a",
		"Cargo.toml:3 acme/fast v0.9.0 pin=",
		"Dockerfile:2 krallin/tini v0.19.0 pin=",
		"Gemfile:2 acme/tidy v1.4.0 pin=",
		"Gemfile:3 acme/old 2.0.0 pin=",
		"infra/main.tf:2 terraform-aws-modules/terraform-aws-vpc v5.1.2 pin=",
		"package.json:3 acme/left v1.2.0 pin=",
		"package.json:4 acme/right 2.0.1 pin=",
		"requirements.txt:2 acme/pylib v0.3.1 pin=",
		"scripts/get-yq.sh:1 mikefarah/yq v4.40.5 pin=",
	}
	if !slices.Equal(got, want) {
		t.Errorf("deps =\n%q\nwant\n%q", got, want)
	}
}

func TestGitBump(t *testing.T) {
	gitShas.Store("psf/black", map[string]string{"24.1.0": "0123456789abcdef0123456789abcdef01234567"})
	t.Cleanup(func() { gitShas.Delete("psf/black") })
	root := t.TempDir()
	write(t, root, map[string]string{
		".pre-commit-config.yaml": "repos:\n  - repo: https://github.com/psf/black\n    rev: 6fdf8a4af28071ed1d079c01122b34c5d587207a  # frozen: 23.3.0\n",
		"scripts/get-yq.sh":       "curl -L https://github.com/mikefarah/yq/releases/download/v4.40.5/yq_linux_amd64 -o bin/yq\n",
	})
	e := gitRefs{}
	for file, to := range map[string]string{".pre-commit-config.yaml": "24.1.0", "scripts/get-yq.sh": "v4.44.3"} {
		ds := depsIn(t, e, root, file)
		if _, err := e.Bump(context.Background(), root, ds[0], to, ""); err != nil {
			t.Fatal(err)
		}
	}
	pc, _ := os.ReadFile(filepath.Join(root, ".pre-commit-config.yaml"))
	if string(pc) != "repos:\n  - repo: https://github.com/psf/black\n    rev: 0123456789abcdef0123456789abcdef01234567  # frozen: 24.1.0\n" {
		t.Errorf("pre-commit = %q", pc)
	}
	sh, _ := os.ReadFile(filepath.Join(root, "scripts", "get-yq.sh"))
	if string(sh) != "curl -L https://github.com/mikefarah/yq/releases/download/v4.44.3/yq_linux_amd64 -o bin/yq\n" {
		t.Errorf("script = %q", sh)
	}
}

// TestGitBumpReleaseURL moves a release download: the tag in the path and
// the version in the asset name, with or without the v, together.
func TestGitBumpReleaseURL(t *testing.T) {
	tests := []struct {
		name, line, dep, from, to, want string
	}{
		{
			"bare version in the asset",
			"curl -sL https://github.com/gohugoio/hugo/releases/download/v0.120.0/hugo_extended_0.120.0_linux-amd64.tar.gz | tar xz",
			"gohugoio/hugo", "v0.120.0", "v0.121.1",
			"curl -sL https://github.com/gohugoio/hugo/releases/download/v0.121.1/hugo_extended_0.121.1_linux-amd64.tar.gz | tar xz",
		},
		{
			"v in the asset",
			`ADD https://github.com/acme/tool/releases/download/v1.2.0/tool-v1.2.0-linux.tar.gz /tmp/`,
			"acme/tool", "v1.2.0", "v1.10.0",
			`ADD https://github.com/acme/tool/releases/download/v1.10.0/tool-v1.10.0-linux.tar.gz /tmp/`,
		},
		{
			"tag without v",
			`url: "https://github.com/acme/tool/releases/download/1.2.0/tool_1.2.0_checksums.txt"`,
			"acme/tool", "1.2.0", "1.3.0",
			`url: "https://github.com/acme/tool/releases/download/1.3.0/tool_1.3.0_checksums.txt"`,
		},
		{
			"look-alikes stay",
			"wget https://github.com/acme/tool/releases/download/v1.2.0/tool-1.2.0.1-11.2.0.zip",
			"acme/tool", "v1.2.0", "v1.3.0",
			"wget https://github.com/acme/tool/releases/download/v1.3.0/tool-1.2.0.1-11.2.0.zip",
		},
		{
			"two URLs, one other repo",
			"get https://github.com/acme/tool/releases/download/v1.2.0/tool_1.2.0.sha256 https://github.com/acme/other/releases/download/v1.2.0/other_1.2.0.zip https://github.com/acme/tool/releases/download/v1.2.0/tool_1.2.0.zip -o tool_1.2.0",
			"acme/tool", "v1.2.0", "v1.3.0",
			"get https://github.com/acme/tool/releases/download/v1.3.0/tool_1.3.0.sha256 https://github.com/acme/other/releases/download/v1.2.0/other_1.2.0.zip https://github.com/acme/tool/releases/download/v1.3.0/tool_1.3.0.zip -o tool_1.2.0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, map[string]string{"get.sh": tt.line + "\n"})
			// Every pin of the repo is bumped, as a change carries them all.
			for _, d := range depsIn(t, gitRefs{}, root, "get.sh") {
				if d.Name != tt.dep {
					continue
				}
				if d.Version != tt.from {
					t.Fatalf("dep = %+v", d)
				}
				if _, err := (gitRefs{}).Bump(context.Background(), root, d, tt.to, ""); err != nil {
					t.Fatal(err)
				}
			}
			if got := strings.TrimSuffix(treeSnapshot(t, root)["get.sh"], "\n"); got != tt.want {
				t.Errorf("line =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

// A tag in Cargo.toml moves with Cargo.lock, which records the commit; with
// cargo missing, the manifest moves and the PR says why the lock did not.
func TestGitBumpLocked(t *testing.T) {
	old := have
	have = func(string) bool { return false }
	t.Cleanup(func() { have = old })
	root := t.TempDir()
	write(t, root, map[string]string{
		"Cargo.toml": "[dependencies]\nfast = { git = \"https://github.com/acme/fast\", tag = \"v0.9.0\" }\n",
		"Cargo.lock": "# locked\n",
	})
	ds := depsIn(t, gitRefs{}, root, "Cargo.toml")
	if ds[0].Manifest.Lock != "Cargo.lock" {
		t.Fatalf("manifest = %+v", ds[0].Manifest)
	}
	b, err := gitRefs{}.Bump(context.Background(), root, ds[0], "v0.10.0", "")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "Cargo.toml"))
	if string(got) != "[dependencies]\nfast = { git = \"https://github.com/acme/fast\", tag = \"v0.10.0\" }\n" || b.Locked || b.Note != "cargo not found in PATH" {
		t.Errorf("Cargo.toml = %q, bump %+v", got, b)
	}
}

// TestGitBumpLockTag checks the lock took the new tag after the native tool ran.
func TestGitBumpLockTag(t *testing.T) {
	const cargoLock = "[[package]]\nname = \"fast\"\nversion = \"0.9.0\"\nsource = \"git+https://github.com/acme/fast?tag=v0.9.0#aaaa\"\n"
	const gemLock = "GIT\n  remote: https://github.com/acme/tidy.git\n  revision: aaaa\n  tag: v1.4.0\n  specs:\n    tidy (1.4.0)\n\nGEM\n  specs:\n"
	tests := []struct {
		name, tool, manifest, src, lock, to string
		moved                               func(string) string
		ok                                  bool
	}{
		{"cargo", "cargo", "Cargo.toml", "[dependencies]\nfast = { git = \"https://github.com/acme/fast\", tag = \"v0.9.0\" }\n",
			cargoLock, "v0.10.0", strings.NewReplacer("tag=v0.9.0#aaaa", "tag=v0.10.0#bbbb").Replace, true},
		{"cargo unmoved", "cargo", "Cargo.toml", "[dependencies]\nfast = { git = \"https://github.com/acme/fast\", tag = \"v0.9.0\" }\n",
			cargoLock, "v0.10.0", nil, false},
		{"bundler", "bundle", "Gemfile", "gem \"tidy\", github: \"acme/tidy\", tag: \"v1.4.0\"\n",
			gemLock, "v1.5.0", strings.NewReplacer("tag: v1.4.0", "tag: v1.5.0").Replace, true},
		{"bundler unmoved", "bundle", "Gemfile", "gem \"tidy\", github: \"acme/tidy\", tag: \"v1.4.0\"\n",
			gemLock, "v1.5.0", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			lock := gitLocks[tt.manifest]
			write(t, root, map[string]string{tt.manifest: tt.src, lock: tt.lock})
			moved := tt.moved
			if moved == nil {
				moved = func(s string) string { return s }
			}
			lockingTool(t, tt.tool, root, lock, moved)
			d := depsIn(t, gitRefs{}, root, tt.manifest)[0]
			b, err := gitRefs{}.Bump(context.Background(), root, d, tt.to, "")
			if tt.ok != (err == nil) {
				t.Fatalf("bump = %+v, %v", b, err)
			}
			if tt.ok && (!b.Locked || !slices.Equal(b.Files, []string{tt.manifest, lock})) {
				t.Errorf("bump = %+v", b)
			}
		})
	}
}

func TestGitMeta(t *testing.T) {
	f := replayCanned(t,
		canned{url: "https://api.github.com/repos/mikefarah/yq/tags?per_page=100", body: `[{"name": "v4.44.3", "commit": {"sha": "b"}}, {"name": "v4.40.5", "commit": {"sha": "a"}}]`},
		canned{url: "https://api.github.com/repos/mikefarah/yq/releases?per_page=100", body: `[]`},
		canned{url: "https://api.github.com/repos/mikefarah/yq", body: `{"archived": true}`},
	)
	m, err := (gitRefs{}).Meta(context.Background(), f, "mikefarah/yq")
	if err != nil {
		t.Fatal(err)
	}
	if m.Latest != "v4.44.3" || len(m.Releases) != 2 || m.Deprecated != "repository archived" {
		t.Errorf("meta = %+v", m)
	}
}
