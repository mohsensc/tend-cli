package cli

import (
	"cmp"
	"context"
	"errors"
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

	noToken  bool   // a GitHub origin, but no token to reach it with
	noRepo   bool   // GitHub does not show the origin's repo to the token
	readOnly bool   // another run holds the lock: this one saves nothing
	openWhy  string // why the open PRs are not known, "" when they are
	info     *core.RepoInfo
}

// settings finds the repo's top level and reads its tend.yml. The top level
// comes back even when the file is wrong.
func settings(ctx context.Context, e Env, o opts) (*config.Config, string, bool, error) {
	root, git := top(ctx, o.dir)
	cfg, used, err := config.Load(root, configPath(e, o, root))
	if err != nil {
		return nil, root, git, err
	}
	if err := eco.Check(cfg); err != nil {
		return nil, root, git, config.At(config.Shown(root, used), err)
	}
	return cfg, root, git, nil
}

// top is the top level of the git repo dir is in, else dir itself.
func top(ctx context.Context, dir string) (string, bool) {
	root, git := dir, false
	if t, err := gitx.Run(ctx, dir, "rev-parse", "--show-toplevel"); err == nil {
		root, git = t, true
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return root, git
}

// configPath is the config file named by --config, read from the dir tend
// runs in, or by TEND_CONFIG, read from root; "" when neither names one.
func configPath(e Env, o opts, root string) string {
	path, base := o.config, cmp.Or(o.dir, e.Dir)
	if path == "" {
		path, base = e.Getenv("TEND_CONFIG"), root
	}
	if path != "" && !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	return path
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
// With shared set, a lock another run holds leaves the session reading the
// state that run last saved, and saving nothing.
func open(ctx context.Context, e Env, o opts, shared bool) (*session, error) {
	cfg, root, git, err := settings(ctx, e, o)
	if err != nil {
		return nil, err
	}
	s := &session{e: e, o: o, root: root, git: git, cfg: cfg}
	core.Keep(cfg.Verify.Env)

	origin := ""
	if s.git {
		if r, err := deliver.DefaultRemote(ctx, s.root); err == nil {
			s.remote = r
			origin, _ = deliver.RemoteURL(ctx, s.root, r)
		}
	}
	key := state.Key(origin, s.root)
	s.dir = state.Dir(e.Home, key)
	var held *state.Locked
	if s.unlock, err = state.Lock(s.dir); errors.As(err, &held) && shared && !o.reset {
		s.readOnly = true
		s.logf("%v; this check saves nothing", err)
	} else if err != nil {
		return nil, err
	}
	if o.reset {
		if err := state.Reset(e.Home, key); err != nil {
			s.unlock()
			return nil, err
		}
	}
	if s.st, _, err = state.Open(e.Home, key); err != nil {
		s.close()
		return nil, err
	}
	if s.st.Discarded != "" {
		s.logf("state.json could not be read (%s); started fresh: open PRs and rejections are read back from GitHub", s.st.Discarded)
	}

	owner, name, ok := deliver.RepoFromRemote(origin)
	if origin == "" {
		owner, name, ok = deliver.RepoFromSlug(e.Getenv("GITHUB_REPOSITORY"))
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
			s.close()
			return nil, err
		}
	}
	s.forge = e.Forge
	switch {
	case s.forge != nil:
	case !ok && !s.git:
		s.why = "not a git repository"
	case !ok:
		s.why = "no GitHub origin remote"
	case token == "":
		s.why, s.noToken = "no GitHub token: set GITHUB_TOKEN or log in with gh", true
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

// save writes the state. A dry run leaves no trace, nor does a check that
// runs while another run holds the lock.
func (s *session) save() {
	if s.o.dryRun || s.readOnly {
		return
	}
	s.st.Updated = s.e.Now()
	if err := s.st.Save(); err != nil {
		s.logf("cannot save state: %v", err)
	}
}

// reach asks the forge about the repo, once. A repo GitHub does not show
// the token leaves the session with no forge, for that reason.
func (s *session) reach(ctx context.Context) error {
	if s.forge == nil || s.info != nil {
		return nil
	}
	info, err := s.forge.Repo(ctx)
	var missing *deliver.NoRepo
	if errors.As(err, &missing) {
		s.forge, s.why, s.noRepo = nil, err.Error(), true
		return nil
	}
	if err != nil {
		return err
	}
	s.info = &info
	return nil
}

// baseName is the branch changes go to: tend.yml's base, else the forge's
// default branch, else the remote's HEAD, else the current branch, else "".
func (s *session) baseName(ctx context.Context) (string, error) {
	if s.cfg.Base != "" {
		return s.cfg.Base, nil
	}
	if err := s.reach(ctx); err != nil {
		return "", err
	}
	if s.info != nil && s.info.Default != "" {
		return s.info.Default, nil
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
// fetched first when fetch is set, else the local branch. With no branch it
// is HEAD; a branch found nowhere is an error, never the checkout instead.
func (s *session) baseRev(ctx context.Context, branch string, fetch bool) (string, error) {
	if branch == "" {
		return "HEAD", nil
	}
	var ferr error
	if s.remote != "" {
		if fetch {
			ferr = s.push.Fetch(ctx, branch)
		}
		remote := "refs/remotes/" + s.remote + "/" + branch
		if hasCommit(ctx, s.root, remote) {
			s.fetchFailed(branch, ferr)
			return remote, nil
		}
	}
	if local := "refs/heads/" + branch; hasCommit(ctx, s.root, local) {
		s.fetchFailed(branch, ferr)
		return local, nil
	}
	where := "locally"
	if s.remote != "" {
		where = "locally or on " + s.remote
	}
	msg := fmt.Sprintf("base branch %q not found %s", branch, where)
	if ferr != nil {
		msg += fmt.Sprintf(" (fetch: %v)", ferr)
	}
	if s.remote != "" && strings.HasPrefix(branch, s.remote+"/") {
		msg += fmt.Sprintf("; base names a branch, so write %q", strings.TrimPrefix(branch, s.remote+"/"))
	}
	return "", errors.New(msg)
}

func (s *session) fetchFailed(branch string, err error) {
	if err != nil {
		s.logf("cannot fetch %s from %s: %v", branch, s.remote, err)
	}
}

func hasCommit(ctx context.Context, dir, rev string) bool {
	_, err := gitx.Run(ctx, dir, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	return err == nil
}
