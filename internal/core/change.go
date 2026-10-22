package core

import "time"

type Kind string

const (
	Security    Kind = "security"
	Breaking    Kind = "breaking"
	Deprecation Kind = "deprecation"
	Update      Kind = "update"
	// Tidy is tend's first PR on a repo, which clears out what nothing uses
	// and what is deprecated before the rest of the queue.
	Tidy Kind = "tidy"
)

type Change struct {
	ID        string // stable key, see id.go: "npm/stripe@16.1.0", "api/stripe/POST /v1/charges", "docker/library/node@22-alpine"
	Kind      Kind
	Severity  string // security only: low|moderate|high|critical
	Level     string // bumps: major|minor|patch; "" otherwise
	Deps      []Dep  // what is bumped or replaced; several only for a group
	To        string // target version/tag/ref; "" for API changes and decision-only changes
	ToName    string // replacement package when the fix swaps packages, else ""
	API       *APIChange
	Evidence  []Evidence
	Options   []Option  // viable migrations when the fix is not mechanical
	Sunset    time.Time // when the thing stops working; zero if unknown
	Published time.Time // when the upstream change appeared
	Sites     []Site    // affected call sites in this repo
	Cleanup   *Cleanup  `json:",omitempty"` // a tidy change: what it clears out
}

// Cleanup is what tend's first PR on a repo clears out: dependencies the
// project declares and nothing uses, and deprecated libraries the code calls.
type Cleanup struct {
	Unused []Dep    // nothing uses them; the PR removes them
	Moves  []Change // deprecated libraries the PR moves off, one change each
	Kept   []Aside  // they look unused, but tend cannot prove it, or removing them failed
	Left   []Aside  // deprecated libraries the PR does not move, with the ways off them
}

// Aside is a dependency a cleanup names but leaves as it is.
type Aside struct {
	Dep     Dep
	Why     string
	Options []Option // the ways off a deprecated library
}

type APIChange struct {
	Provider string
	Method   string
	Path     string // template as in the spec: "/v1/orders/{id}"
	Field    string // for field/parameter changes, "" otherwise
	// What is one of: operation removed | operation deprecated | response field removed |
	// response field deprecated | parameter removed | parameter deprecated |
	// required parameter added | request field removed
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
