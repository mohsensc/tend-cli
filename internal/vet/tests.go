package vet

import (
	"context"
	"regexp"
	"slices"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/scan"
)

var (
	assertPat  = regexp.MustCompile(`\b(assert\w*|expect\w*|should|require\.\w+|t\.(Error|Errorf|Fatal|Fatalf|Fail|FailNow))\b`)
	trivialPat = regexp.MustCompile(`\b(assert\w*|expect\w*)(\.\w+)*\s*(\(\s*(true|True|1|!0)\s*\)|\s+(true|True|1)\s*$)`)
	casePat    = regexp.MustCompile(`(^|[^.\w])(it|test|describe|context)\s*(\.\w+\s*)?\(|\bit\s+["']|^\s*func\s+Test\w*\s*\(|^\s*(async\s+)?def\s+test\w*\s*\(|@Test\b|#\[test\]|\bt\.Run\s*\(`)
	skipPat    = regexp.MustCompile(`(^|[^\w])(xit|xdescribe|xtest)\s*\(|\.(skip|todo)\s*\(|\bt\.Skip\w*\s*\(|@(pytest\.mark\.skip\w*|unittest\.skip\w*|Disabled|Ignore)\b|#\[ignore\]|\bskip\s*\(`)
)

// tests: did a model weaken a test, or edit one that reaches none of the
// files this change explains? A test edit is never reverted, since the
// proof ran with it; the flag makes the PR a draft.
func tests(ctx context.Context, p *pass) ([]core.Finding, string) {
	if !p.in.Patch.Model {
		return nil, na
	}
	ok := p.explained()
	var res []core.Finding
	for _, f := range p.files {
		file := f.Path()
		if !scan.IsTest(file) || !slices.ContainsFunc(f.Hunks, func(h *hunk) bool { return h.Model }) {
			continue
		}
		if !ok[file] && !p.reachesSite(file) {
			res = append(res, finding("tests", file, 0, "flagged", "a model edited this test, which reaches none of the files this change explains"))
		}
		if f.New == "" {
			res = append(res, finding("tests", file, 0, "flagged", "a model deleted this test"))
			continue
		}
		lang := langOf(file)
		asserts, cases, skips := 0, 0, 0
		for _, h := range f.Hunks {
			if !h.Model {
				continue
			}
			for _, l := range h.Lines {
				code := l.Text
				if lang != "" {
					code = codeOf(lang, l.Text)
				}
				n := 0
				switch l.Op {
				case '-':
					n = 1
				case '+':
					n = -1
					if skipPat.MatchString(code) {
						skips++
					}
				default:
					continue
				}
				if assertPat.MatchString(code) && (n > 0 || !trivialPat.MatchString(code)) {
					asserts += n
				}
				if casePat.MatchString(code) {
					cases += n
				}
			}
		}
		switch {
		case cases > 0:
			res = append(res, finding("tests", file, 0, "flagged", "a model removed %s from this test", count(cases, "test case")))
		case asserts > 0:
			res = append(res, finding("tests", file, 0, "flagged", "a model removed %s from this test without adding as many back", count(asserts, "assertion")))
		}
		if skips > 0 {
			res = append(res, finding("tests", file, 0, "flagged", "a model skipped %s in this test", count(skips, "test")))
		}
	}
	sortFindings(res)
	return res, ""
}

// explained are the files this change explains: the manifests and lockfiles
// of the deps, files with affected call sites, files the patch's own steps
// name, and the code files a model was given to edit. A test the model
// edited is not explained by that alone.
func (p *pass) explained() map[string]bool {
	ok := p.depFiles()
	for _, s := range p.in.Change.Sites {
		ok[s.File] = true
	}
	for _, f := range mentioned(p.in.Patch.Applied) {
		ok[f] = true
	}
	for f := range p.model {
		if !scan.IsTest(f) {
			ok[f] = true
		}
	}
	return ok
}

// reachesSite reports whether the test file test reaches a file with one of
// the change's call sites.
func (p *pass) reachesSite(test string) bool {
	return slices.ContainsFunc(p.in.Change.Sites, func(s core.Site) bool { return core.Reaches(p.in.Index, test, s.File) })
}
