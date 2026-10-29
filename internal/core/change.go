package core

import "time"

type Kind string

const (
	Security    Kind = "security"
	Breaking    Kind = "breaking"
	Deprecation Kind = "deprecation"
	Update      Kind = "update"
	// First is a catch-up PR: tend's first PR on a repo, and the stages after
	// it, which bring the repo up to date before tend opens PRs one change at
	// a time.
	First Kind = "first"
)

type Change struct {
	ID        string // stable key, see id.go: "npm/stripe@16.1.0", "api/stripe/POST /v1/charges", "docker/library/node@22-alpine"
	Kind      Kind
	Severity  string // security only: low|moderate|high|critical
	Level     string // bumps: major|minor|patch; "" otherwise
	Deps      []Dep  // what is bumped or replaced; several only for a group
	To        string // target version/tag/ref; "" for API and decision-only changes; a security fix's least fixed release, see Patch.Landed
	ToName    string // replacement package when the fix swaps packages, else ""
	API       *APIChange
	Evidence  []Evidence
	Options   []Option  // viable migrations when the fix is not mechanical
	Sunset    time.Time // when the thing stops working; zero if unknown
	Published time.Time // when the upstream change appeared
	Sites     []Site    // affected call sites in this repo
	Batch     *Batch    `json:",omitempty"` // a catch-up PR: what it carries
	// Steps are the releases a major bump can climb through when the jump
	// to To fails its proof: the newest of each major in between, ascending.
	Steps  []string `json:",omitempty"`
	Via    []string `json:",omitempty"` // the steps a run climbed through to reach To
	Choice *Choice  `json:",omitempty"` // the option a run took itself, nil when none
}

// Choice is the option tend took by its own rule, and why that one.
type Choice struct {
	N   int // 1-based, into Options
	Why string
}

// Batch is what a catch-up PR carries: the queued changes of its stage and
// the stages before it, each made as its own PR would make it, and for the
// first one the dependencies nothing uses and a test baseline; then the
// queued changes it leaves for a later stage.
type Batch struct {
	Stage    int       // the stage this PR completes, see plan.Tier
	Changes  []Change  // what the PR makes
	Unused   []Dep     // nothing uses them; the PR removes them
	Kept     []Aside   // they look unused, but tend cannot prove it, or removing them failed
	Next     []Later   // queued changes left for a later stage
	Baseline *Baseline `json:",omitempty"` // tests the PR adds first, when the project's own are weak
	Thin     string    `json:",omitempty"` // why the project's tests cannot prove the batch, when tend could add no baseline
}

// Baseline is what a catch-up PR adds so that its proof, and every later
// one, has something to go by: a smoke test of the modules that load on the
// base commit, and tests a model wrote of how the code behaves now.
type Baseline struct {
	Why     string   // why the project's own tests are weak
	Files   []string // the test files it adds
	Command string   // the line it adds to verify.test, "" when the project's tests run them
	Modules int      // modules the smoke test loads
	Checks  []string // build and typecheck lines it added to the proof
	Model   bool     // a model wrote tests
	Code    string   `json:"-"` // the files as a diff against the base
}

// Aside is a dependency a batch names but leaves as it is.
type Aside struct {
	Dep Dep
	Why string
}

// Later is a queued change a batch leaves for later, and why: for the
// stage it belongs to, or one it failed in. Stage 0 is past the catch-up.
type Later struct {
	Change Change
	Why    string
	Stage  int
}

type APIChange struct {
	Provider string
	Method   string
	Path     string // template as in the spec: "/v1/orders/{id}"
	Field    string // for field/parameter changes, "" otherwise
	// What is one of: operation removed | operation deprecated | response field removed |
	// response field deprecated | response field type changed | parameter removed |
	// parameter deprecated | required parameter added | request field removed
	What string
}

type Evidence struct {
	Source string // osv|registry|releases|changelog|openapi|headers|eol|archived|table|model
	Title  string
	Text   string // verbatim upstream excerpt, at most 1200 chars
	URL    string
}

type Option struct {
	Title   string
	Detail  string // trade-offs, one short paragraph
	Safe    bool   // mechanical: can be applied and proven without a model
	Stay    bool   `json:",omitempty"` // keeps the version in use and has tend.yml ignore the move
	Codemod *Codemod
	ToName  string
	To      string
}

type Codemod struct {
	Renames map[string]string // member chain -> member chain: "charges.create" -> "paymentIntents.create"
	Imports map[string]string // import path -> import path
	Paths   map[string]string // endpoint path -> endpoint path
	Fields  map[string]string // field -> field; "" drops the field
	Text    map[string]string // literal line substitutions (actions `::set-output` -> $GITHUB_OUTPUT)
}

// Lines counts the lines sites sit on: two chains on one line of code are
// one place to change.
func Lines(sites []Site) int {
	type at struct {
		file string
		line int
	}
	seen := map[at]bool{}
	for _, s := range sites {
		seen[at{s.File, s.Line}] = true
	}
	return len(seen)
}

// Calls are the sites of ch in code, leaving out the manifest lines that name
// what it moves.
func Calls(ch *Change) []Site {
	var res []Site
	for _, s := range ch.Sites {
		named := false
		for _, d := range ch.Deps {
			named = named || s.File == d.Manifest.Path
		}
		if !named {
			res = append(res, s)
		}
	}
	return res
}
