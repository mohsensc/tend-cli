package core

import "time"

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

type Step struct {
	Cmd     string
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
	Nothing   bool // no build or test command exists for this project
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
