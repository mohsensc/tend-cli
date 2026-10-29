package core

import (
	"os"
	"path/filepath"
)

type Site struct {
	File  string
	Line  int
	Col   int
	Chain []string // member path rooted at an import binding: ["stripe","charges","create"]
	Call  bool     // followed by "("
	Text  string   // the source line, trimmed, at most 200 chars
}

type Import struct {
	File    string
	Line    int
	Package string   // as the source names it: "stripe", "@aws-sdk/client-s3", "github.com/stripe/stripe-go/v79/charge", "com.stripe.model.Charge"
	Binding string   // local name the code uses; "" for side-effect imports
	Names   []string // named imports, local names: `import {a, b as c}` -> ["a","c"]
}

type URLUse struct {
	File   string
	Line   int
	Method string // GET|POST|... when inferable, "" otherwise
	URL    string // literal as written: full URL or path such as "/v1/orders"
	Host   string // from the literal, else a host literal elsewhere in the file; "" if unknown
	Text   string `json:",omitempty"` // the source line, trimmed
}

// ScanIndex is what internal/scan.Index implements.
type ScanIndex interface {
	// Uses returns sites whose root import matches one of the names (exact, or path prefix for
	// go/java/php/rust style namespaces), sorted by file then line.
	Uses(imports []string) []Site
	// Fields returns identifier, attribute, object-key and string-literal occurrences of name,
	// restricted to files (all files when nil).
	Fields(name string, files []string) []Site
	// Endpoints returns URL literals whose host matches one of hosts, or path-only literals in
	// files that mention one of hosts.
	Endpoints(hosts []string) []URLUse
	// URLs returns every URL literal, in file order.
	URLs() []URLUse
	Imports(pkg string) []Import
	Files() []string
}

// TestReacher is a ScanIndex that can say which files a test covers, as
// internal/scan.Index does.
type TestReacher interface {
	// Reaches reports whether the test file test reaches the file p.
	Reaches(test, p string) bool
}

// Reaches reports whether x knows the test file test to reach p, false
// when x cannot tell.
func Reaches(x ScanIndex, test, p string) bool {
	r, ok := x.(TestReacher)
	return ok && r.Reaches(test, p)
}

// NestedRepo reports whether dir is a git repository of its own, a clone, a
// submodule or a worktree, whose files are another project's, as git
// ls-files sees them.
func NestedRepo(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}
