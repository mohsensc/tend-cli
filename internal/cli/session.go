package cli

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/api"
	"github.com/mohsensc/tend-cli/internal/config"
	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/deliver"
	"github.com/mohsensc/tend-cli/internal/eco"
	"github.com/mohsensc/tend-cli/internal/fetch"
	"github.com/mohsensc/tend-cli/internal/gitx"
	"github.com/mohsensc/tend-cli/internal/model"
	"github.com/mohsensc/tend-cli/internal/state"
)

// session is what check and run share: the repo, its config, its state and
// the clients that talk to the outside.
type session struct {
	e      Env
	o      opts
	root   string // the repo's top level, or the dir when it is not a git repo
	git    bool
	cfg    *config.Config
	dir    string // the repo's state dir
	st     *state.State
	fetch  core.Fetcher
	forge  core.Forge      // nil without a token or a GitHub origin
	remote string          // "" without one
	push   *deliver.Remote // nil without a remote
	repo   string          // owner/name, else the root path
	why    string          // why there is no forge
	specs  *api.Specs      // the API specs the last plan read
	unlock func()
	picked *core.Model // the model, set once llm has run
}

// settings finds the repo's top level and reads its tend.yml. The top level
// comes back even when the file is wrong.
func settings(ctx context.Context, e Env, o opts) (*config.Config, string, bool, error) {
	root, git := o.dir, false
	if top, err := gitx.Run(ctx, o.dir, "rev-parse", "--show-toplevel"); err == nil {
		root, git = top, true
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	path := o.config
	if path == "" {
		path = e.Getenv("TEND_CONFIG")
	}
	if path != "" && !filepath.IsAbs(path) {
		base := e.Dir
		if o.config == "" {
			base = root
		}
		path = filepath.Join(base, path)
	}
	cfg, used, err := config.Load(root, path)
	if err != nil {
		return nil, root, git, err
	}
	if err := eco.Check(cfg); err != nil {
		return nil, root, git, fmt.Errorf("%s: %w", used, err)
	}
	return cfg, root, git, nil
}

// llm is the configured model, nil without one. A model that cannot be set
// up is said once and left out.
func (s *session) llm() core.Model {
	if s.picked != nil {
		return *s.picked
	}
	m := s.e.Model
	if m == nil {
		var err error
		if m, err = model.New(s.cfg.Model, s.e.Getenv); err != nil {
			s.logf("%v; going on without a model", err)
		}
	}
	s.picked = &m
	return m
}

func (s *session) logf(format string, args ...any) {
	if !s.o.quiet {
		fmt.Fprintf(s.e.Stderr, "tend: "+format+"\n", args...)
	}
}

// open sets up a session and takes the repo's lock. The caller runs close.
func open(ctx context.Context, e Env, o opts) (*session, error) {
	cfg, root, git, err := settings(ctx, e, o)
	if err != nil {
		return nil, err
	}
	s := &session{e: e, o: o, root: root, git: git, cfg: cfg}

	origin := ""
	if s.git {
		if r, err := deliver.DefaultRemote(ctx, s.root); err == nil {
			s.remote = r
			origin, _ = deliver.RemoteURL(ctx, s.root, r)
		}
	}
	key := state.Key(origin, s.root)
	s.dir = state.Dir(e.Home, key)
	if s.unlock, err = state.Lock(s.dir); err != nil {
		return nil, err
	}
	if o.reset {
		if err := state.Reset(e.Home, key); err != nil {
			s.unlock()
			return nil, err
		}
	}
	if s.st, _, err = state.Open(e.Home, key); err != nil {
		s.unlock()
		return nil, err
	}

	owner, name, ok := deliver.RepoFromRemote(origin)
	if !ok {
		owner, name, ok = deliver.RepoFromRemote(e.Getenv("GITHUB_REPOSITORY"))
	}
	s.repo = s.root
	if ok {
		s.repo = owner + "/" + name
	}
	token := ""
	if e.Fetch == nil || e.Forge == nil && ok {
		token = deliver.TokenFrom(e.Getenv, deliver.Exec)
	}
	s.fetch = e.Fetch
	if s.fetch == nil {
		if err := fetch.Prune(filepath.Join(e.Home, "http"), 7*24*time.Hour, e.Now()); err != nil {
			s.logf("cannot prune the HTTP cache: %v", err)
		}
		c := fetch.New(filepath.Join(e.Home, "http"), e.Version)
		if token != "" {
			c.Token = func(host string) string {
				if host == "api.github.com" {
					return token
				}
				return ""
			}
		}
		s.fetch = c
	}
	if s.remote != "" {
		if !ok {
			owner, name = "", ""
		}
		if s.push, err = deliver.NewRemote(ctx, s.root, s.remote, token, owner, name); err != nil {
			s.unlock()
			return nil, err
		}
	}
	s.forge = e.Forge
	switch {
	case s.forge != nil:
	case !ok:
		s.why = "no GitHub origin remote"
	case token == "":
		s.why = "no GitHub token (set GITHUB_TOKEN or log in with gh)"
	default:
		s.forge = deliver.NewGitHub(s.fetch, http.DefaultClient, token, owner, name)
	}
	return s, nil
}

func (s *session) close() {
	if s.unlock != nil {
		s.unlock()
	}
}

// save writes the state. A dry run leaves no trace.
func (s *session) save() {
	if s.o.dryRun {
		return
	}
	s.st.Updated = s.e.Now()
	if err := s.st.Save(); err != nil {
		s.logf("cannot save state: %v", err)
	}
}

// baseName is the branch changes go to: tend.yml's base, else the forge's
// default branch, else the remote's HEAD, else the current branch, else "".
func (s *session) baseName(ctx context.Context) (string, error) {
	if s.cfg.Base != "" {
		return s.cfg.Base, nil
	}
	if s.forge != nil {
		info, err := s.forge.Repo(ctx)
		if err != nil {
			return "", err
		}
		if info.Default != "" {
			return info.Default, nil
		}
	}
	if s.remote != "" {
		if ref, err := gitx.Run(ctx, s.root, "symbolic-ref", "--short", "refs/remotes/"+s.remote+"/HEAD"); err == nil {
			return strings.TrimPrefix(ref, s.remote+"/"), nil
		}
	}
	branch, _ := gitx.Run(ctx, s.root, "symbolic-ref", "--quiet", "--short", "HEAD")
	return branch, nil
}

// baseRev is where work on branch starts: the remote's copy when it has one,
// fetched first when fetch is set, else the local branch, else HEAD.
func (s *session) baseRev(ctx context.Context, branch string, fetch bool) string {
	if branch == "" {
		return "HEAD"
	}
	if s.remote != "" {
		if fetch {
			s.fetchBase(ctx, branch)
		}
		remote := "refs/remotes/" + s.remote + "/" + branch
		if _, err := gitx.Run(ctx, s.root, "rev-parse", "--verify", "--quiet", remote+"^{commit}"); err == nil {
			return remote
		}
	}
	if _, err := gitx.Run(ctx, s.root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch+"^{commit}"); err == nil {
		return "refs/heads/" + branch
	}
	return "HEAD"
}

func (s *session) fetchBase(ctx context.Context, branch string) {
	if err := s.push.Fetch(ctx, branch); err != nil {
		s.logf("cannot fetch %s from %s: %v", branch, s.remote, err)
	}
}
