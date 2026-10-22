// Package state keeps what tend remembers between runs, outside the user's tree.
package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
)

type State struct {
	Version  int               // 1
	Seen     map[string]Seen   // change id -> record
	Parked   map[string]Parked // change id -> why it waits
	Open     *OpenPR           // the change tend has a PR or commit out for
	Baseline *BaseRun          // the last base commit's run
	Specs    map[string]Snap   // provider -> snapshot
	Found    map[string]Found  // host -> the spec discovery found for it
	Release  Release           // the last self-update check
	// Onboard is where tend's first PR on the repo stands: "" until a run
	// looks, due while it is coming or open, then how that ended: merged,
	// closed, clean (nothing to catch up on), before (tend was here before), quiet,
	// off, or commit (no PRs to open).
	Onboard string `json:",omitempty"`
	// Stage is how far the catch-up after the first PR got: the stage of
	// the last catch-up PR that merged or commit that landed, 4 once the
	// catch-up is over, 0 before it starts or when tend began here before
	// it had stages.
	Stage   int `json:",omitempty"`
	Issue   Issue
	Updated time.Time

	path string
}

type Seen struct {
	First, Last time.Time
	Verdict     string // noise|ignored|cooldown|parked|queued|delivered|unsupported
	Detail      string `json:",omitempty"`
	Base        string `json:",omitempty"` // unsupported: the base commit tend failed on
}

// BaseRun is the project's own commands run on a base commit.
type BaseRun struct {
	Sha string
	Run *core.Run
}

type Parked struct {
	PR     int
	Reason string // rejected|preempted|reverted
	Detail string `json:",omitempty"` // reverted: the commit and the checks that failed
	To     string
	At     time.Time
}

type OpenPR struct {
	ID                   string
	PR                   int
	Branch, Base, Commit string
	At                   time.Time
	// Holds, for the first PR, is every queued change it was built from,
	// made or left for later, so one that appears after it opened brings
	// it up to date.
	Holds []string `json:",omitempty"`
	Stage int      `json:",omitempty"` // a catch-up PR's stage
}

// Issue is tend's tracking issue as tend last wrote it.
type Issue struct {
	Number int    `json:",omitempty"`
	Body   string `json:",omitempty"`
	Open   bool   `json:",omitempty"`
}

type Snap struct {
	At   time.Time
	File string
}

// Found is what looking for a host's API spec turned up, "" for nothing.
type Found struct {
	Spec string
	At   time.Time
}

type Release struct {
	Checked time.Time
	Tag     string // newest release seen, "" when none is newer
}

// Home is $TEND_HOME, else tend under the user cache dir.
func Home(getenv func(string) string) string {
	if h := getenv("TEND_HOME"); h != "" {
		return h
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "tend")
	}
	return filepath.Join(dir, "tend")
}

// Key names a repo's state dir: the first 12 hex digits of the sha256 of its
// normalized origin URL, or of its absolute path when it has no origin.
func Key(origin, abs string) string {
	s := abs
	if origin != "" {
		s = NormURL(origin)
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

// NormURL reduces the ssh and https spellings of a remote to host/path, with
// the host lowercased and the user, port, trailing slash and .git dropped.
func NormURL(s string) string {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "://") {
		// scp-like: git@github.com:owner/repo.git
		if at := strings.Index(s, "@"); at >= 0 {
			s = s[at+1:]
		}
		if host, p, ok := strings.Cut(s, ":"); ok && !strings.Contains(host, "/") {
			s = "ssh://" + host + "/" + p
		} else {
			return trimRepo(s)
		}
	}
	u, err := url.Parse(s)
	switch {
	case err != nil:
		return trimRepo(s)
	case u.Host == "":
		return trimRepo(u.Path)
	}
	return strings.ToLower(u.Hostname()) + "/" + trimRepo(strings.TrimLeft(u.Path, "/"))
}

func trimRepo(p string) string {
	p = strings.TrimRight(p, "/")
	return strings.TrimSuffix(p, ".git")
}

// Dir is where the repo with this key keeps its state.
func Dir(home, key string) string { return filepath.Join(home, "repos", key) }

// Open loads the state of the repo with this key under home and returns it with
// the repo's directory. A missing or empty state is a fresh one.
func Open(home, key string) (*State, string, error) {
	dir := Dir(home, key)
	s := &State{path: filepath.Join(dir, "state.json")}
	b, err := os.ReadFile(s.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, "", err
	case len(b) > 0:
		if err := json.Unmarshal(b, s); err != nil {
			return nil, "", fmt.Errorf("state: %s: %w", s.path, err)
		}
	}
	s.init()
	return s, dir, nil
}

func (s *State) init() {
	s.Version = 1
	if s.Seen == nil {
		s.Seen = map[string]Seen{}
	}
	if s.Parked == nil {
		s.Parked = map[string]Parked{}
	}
	if s.Specs == nil {
		s.Specs = map[string]Snap{}
	}
	if s.Found == nil {
		s.Found = map[string]Found{}
	}
	// State written when the baseline was a map keyed by sha decodes to a
	// baseline with no run.
	if s.Baseline != nil && s.Baseline.Run == nil {
		s.Baseline = nil
	}
}

// New is an empty state that is never saved, for tests and dry runs.
func New() *State {
	s := &State{}
	s.init()
	return s
}

// Save writes the state to a temp file and renames it into place.
func (s *State) Save() error {
	if s.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(s.path, append(b, '\n'))
}

// See records the verdict on a change, keeping when it was first seen and,
// while the verdict stands, the base it was reached on.
func (s *State) See(id, verdict, detail string, now time.Time) {
	prev, ok := s.Seen[id]
	if !ok {
		prev.First = now
	}
	res := Seen{First: prev.First, Last: now, Verdict: verdict, Detail: detail}
	if verdict == prev.Verdict {
		res.Base = prev.Base
	}
	s.Seen[id] = res
}

// Fail records that tend could not carry a change through on base. The
// change is left alone until the base moves.
func (s *State) Fail(id, detail, base string, now time.Time) {
	s.See(id, "unsupported", detail, now)
	r := s.Seen[id]
	r.Base = base
	s.Seen[id] = r
}

// Reset forgets everything about the repo with this key. The run lock stays:
// Reset runs under it.
func Reset(home, key string) error {
	dir := Dir(home, key)
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range ents {
		if e.Name() == lockName {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func writeFile(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

// StaleAfter is how old a lock gets before another run may take it over.
const StaleAfter = 2 * time.Hour

var now = time.Now

// Locked is the error Lock returns when another run holds the lock.
type Locked struct {
	PID int
	At  time.Time
}

func (e *Locked) Error() string {
	return fmt.Sprintf("another tend run holds the lock (pid %d, since %s)", e.PID, e.At.UTC().Format(time.RFC3339))
}

const lockName = "lock"

// Lock takes the run lock in dir. A lock older than two hours is stale.
// Releasing it leaves alone a lock another run took over in the meantime.
func Lock(dir string) (release func(), err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, lockName)
	for range 2 {
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			mine := fmt.Sprintf("%d %d\n", os.Getpid(), now().Unix())
			_, err = f.WriteString(mine)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				os.Remove(p)
				return nil, err
			}
			return func() {
				if b, err := os.ReadFile(p); err == nil && string(b) == mine {
					os.Remove(p)
				}
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		held := readLock(p)
		if now().Sub(held.At) < StaleAfter {
			return nil, held
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("state: cannot take %s", p)
}

// readLock falls back to the file's mtime when the lock has no readable
// content yet: another run may be between creating and writing it.
func readLock(p string) *Locked {
	res := &Locked{}
	if fi, err := os.Stat(p); err == nil {
		res.At = fi.ModTime()
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return res
	}
	f := strings.Fields(string(b))
	if len(f) != 2 {
		return res
	}
	res.PID, _ = strconv.Atoi(f[0])
	if sec, err := strconv.ParseInt(f[1], 10, 64); err == nil {
		res.At = time.Unix(sec, 0)
	}
	return res
}
