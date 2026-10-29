package core

import (
	"strings"
	"time"
)

type Patch struct {
	Files    []string
	Diff     string   // unified diff against base
	Applied  []string // plain lines: "bumped stripe 14.2.0 -> 16.1.0 (package.json, package-lock.json)"
	Model    bool     // a model wrote part of it
	Edited   []string // files a model edited
	Decision bool     // nothing applied; the PR asks the maintainer to choose
	Note     string
	// Landed maps "<eco>/<name>" to the release a dep only the lockfile names
	// landed on, for each that went past the change's To.
	Landed map[string]string `json:",omitempty"`
}

// The phases a step belongs to.
const (
	PhaseInstall = "install"
	PhaseBuild   = "build"
	PhaseTest    = "test"
)

type Step struct {
	Cmd     string
	Phase   string `json:",omitempty"` // install|build|test; "" in runs recorded before phases were
	Exit    int
	Took    time.Duration
	Tail    string // first failure and last 40 lines of combined output
	Log     string // full log path under the state dir
	Skipped string // reason it did not run, "" if it ran
}

type Run struct {
	Commit   string
	Steps    []Step
	Pass     bool
	Took     time.Duration
	Warnings []Warning // deprecations the runtimes reported, when they were asked to
}

// Last is the last step that ran: the one that stopped the run when it
// failed. It is nil when nothing ran.
func (r *Run) Last() *Step {
	if r == nil {
		return nil
	}
	for i := len(r.Steps) - 1; i >= 0; i-- {
		if r.Steps[i].Skipped == "" {
			return &r.Steps[i]
		}
	}
	return nil
}

// Warning is one deprecation a runtime printed while the project's own
// commands ran: where the deprecated thing was used, and what was said.
type Warning struct {
	File string // repo-relative
	Line int
	Pkg  string // the package that warned, when the output names it
	Msg  string
}

type Proof struct {
	Base      *Run // nil when the baseline could not run
	Patched   *Run
	Container string
	Nothing   bool     // no build or test command exists for this project
	Runtimes  []string `json:",omitempty"` // what the commands ran on: "node v22.23.4", "go1.26.0"
}

type Finding struct {
	Check  string // symbols|lockfile|debug|comments|leftovers|dead|fields|scope|question
	File   string
	Line   int
	Msg    string
	Action string // fixed|dropped|flagged
}

type Vetting struct {
	Checks   []string // checks that ran, in order
	Findings []Finding
	Changed  bool   // vet modified the worktree; proof must be re-run
	Verdict  string // pass|draft|reject
}

// Proven says whether the patched tree did its part: it passes, or the base
// already fails and the patch stops at the very step the base stops at, after
// the base reached its tests, and with no new way of failing there. The PR
// for the second goes out as a draft. A base that dies before any test runs
// proves nothing about a change.
func (p *Proof) Proven() bool {
	if p.Patched != nil && p.Patched.Pass {
		return true
	}
	if p.Base == nil || p.Base.Pass || p.Patched == nil || !p.Base.Tested() {
		return false
	}
	fp, fb := p.Patched.Last(), p.Base.Last()
	return fp != nil && fb != nil && fp.Cmd == fb.Cmd && p.Patched.reached() == p.Base.reached() && !newFailure(fb.Tail, fp.Tail)
}

// Tested says whether a test step of r ran, passing or not. A run recorded
// before steps had phases counts as tested.
func (r *Run) Tested() bool {
	if r == nil {
		return false
	}
	phased := false
	for _, st := range r.Steps {
		phased = phased || st.Phase != ""
		if st.Phase == PhaseTest && st.Skipped == "" {
			return true
		}
	}
	return !phased && len(r.Steps) > 0
}

// reached is how many steps of a run passed before the first that failed.
func (r *Run) reached() int {
	for i, st := range r.Steps {
		if st.Skipped != "" || st.Exit != 0 {
			return i
		}
	}
	return len(r.Steps)
}

// resolveFailures are what a dependency resolver prints when the set it was
// given cannot be installed together.
var resolveFailures = []string{"ResolutionImpossible", "Cannot install", "ERESOLVE", "version solving failed", "Could not find a version that satisfies", "conflicting dependencies"}

// newFailure says whether the patched run's output shows a resolver failure
// the base's does not: the patch broke the install in a way of its own.
func newFailure(base, patched string) bool {
	for _, m := range resolveFailures {
		if strings.Contains(patched, m) && !strings.Contains(base, m) {
			return true
		}
	}
	return false
}
