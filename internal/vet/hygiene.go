package vet

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/scan"
)

var debugPats = map[string][]*regexp.Regexp{
	"js":     {regexp.MustCompile(`\bconsole\.(log|debug|dir)\(`), regexp.MustCompile(`\bdebugger\s*;`)},
	"go":     {regexp.MustCompile(`\bfmt\.Print`), regexp.MustCompile(`\bprintln\(`)},
	"python": {regexp.MustCompile(`\bprint\(`)},
	"rust":   {regexp.MustCompile(`\bdbg!`)},
	"ruby":   {regexp.MustCompile(`\bbinding\.pry\b`)},
	"php":    {regexp.MustCompile(`\bvar_dump\(`)},
	"csharp": {regexp.MustCompile(`\bConsole\.WriteLine\(`)},
	"java":   {regexp.MustCompile(`\bprintln\(`)},
}

// debug: did the patch add debug output?
func debug(ctx context.Context, p *pass) ([]core.Finding, string) {
	if !p.code() {
		return nil, na
	}
	var res []core.Finding
	drops := map[string][]int{}
	for _, f := range p.files {
		file := f.Path()
		lang := langOf(file)
		pats := debugPats[lang]
		if f.New == "" || len(pats) == 0 || scan.IsTest(file) {
			continue
		}
		for _, h := range f.Hunks {
			before := 0
			for _, l := range h.Lines {
				if l.Op == '-' && matchAny(pats, codeOf(lang, l.Text)) != nil {
					before++
				}
			}
			for _, l := range h.Lines {
				code := codeOf(lang, l.Text)
				re := matchAny(pats, code)
				if l.Op != '+' || re == nil {
					continue
				}
				// A rewritten line that already printed is not new debug output.
				if before > 0 {
					before--
					continue
				}
				if !statement(code, re) {
					res = append(res, finding("debug", file, l.New, "flagged", "debug output: `%s`", strings.TrimSpace(l.Text)))
					continue
				}
				drops[file] = append(drops[file], l.New)
			}
		}
	}
	for _, file := range slices.Sorted(maps.Keys(drops)) {
		nums := drops[file]
		var keep []int
		switch langOf(file) {
		case "go":
			nums = append(nums, unusedFmt(p, file, nums)...)
		case "python":
			nums, keep = emptyBlocks(p, file, nums)
		}
		added := p.added(file)
		for _, n := range keep {
			if err := p.replace(file, n, "pass"); err != nil {
				return nil, err.Error()
			}
			res = append(res, finding("debug", file, n, "fixed", "replaced `%s` with `pass`", strings.TrimSpace(added[n])))
		}
		for _, n := range drops[file] {
			if !slices.Contains(keep, n) {
				res = append(res, finding("debug", file, n, "dropped", "removed `%s`", strings.TrimSpace(added[n])))
			}
		}
		if err := p.drop(file, nums); err != nil {
			return nil, err.Error()
		}
	}
	sortFindings(res)
	return res, ""
}

// emptyBlocks splits the Python lines to drop into those that can go and those
// that are all that is left of their block, one per block, which become pass:
// a block with no statement does not parse.
func emptyBlocks(p *pass, file string, nums []int) (drop, keep []int) {
	b, err := os.ReadFile(filepath.Join(p.in.Dir, filepath.FromSlash(file)))
	if err != nil {
		return nums, nil
	}
	lines := strings.Split(string(blank("python", b)), "\n")
	indent := func(i int) int {
		if i < 0 || i >= len(lines) || strings.TrimSpace(lines[i]) == "" {
			return -1
		}
		return len(lines[i]) - len(strings.TrimLeft(lines[i], " \t"))
	}
	done := map[int]bool{}
	for _, n := range nums {
		at := indent(n - 1)
		if at < 0 || done[n] {
			drop = append(drop, n)
			continue
		}
		// The block runs up and down from n over lines indented at least as far.
		start, end := n-1, n-1
		for i := start - 1; i >= 0; i-- {
			if d := indent(i); d >= 0 {
				if d < at {
					break
				}
				start = i
			}
		}
		for i := end + 1; i < len(lines); i++ {
			if d := indent(i); d >= 0 {
				if d < at {
					break
				}
				end = i
			}
		}
		empty := true
		var block []int
		for i := start; i <= end; i++ {
			if indent(i) != at {
				continue
			}
			if !slices.Contains(nums, i+1) {
				empty = false
				break
			}
			block = append(block, i+1)
		}
		for _, m := range block {
			done[m] = true
		}
		if empty && at > 0 {
			keep = append(keep, n)
			continue
		}
		drop = append(drop, n)
	}
	return drop, keep
}

func codeOf(lang, s string) string { return string(blank(lang, []byte(s))) }

func matchAny(pats []*regexp.Regexp, s string) *regexp.Regexp {
	for _, re := range pats {
		if re.MatchString(s) {
			return re
		}
	}
	return nil
}

// statement reports whether the debug call is the whole line, so removing the
// line removes exactly it.
func statement(code string, re *regexp.Regexp) bool {
	code = strings.TrimSpace(code)
	loc := re.FindStringIndex(code)
	if loc == nil || loc[0] != 0 {
		return false
	}
	if strings.Count(code, "(") != strings.Count(code, ")") {
		return false
	}
	return loc[1] == len(code) || strings.HasSuffix(code, ")") || strings.HasSuffix(code, ";")
}

// unusedFmt returns the line of an import of "fmt" the patch added, when the
// debug lines being dropped were the file's only use of it.
func unusedFmt(p *pass, file string, drops []int) []int {
	b, err := os.ReadFile(filepath.Join(p.in.Dir, filepath.FromSlash(file)))
	if err != nil {
		return nil
	}
	lines := strings.Split(string(blank("go", b)), "\n")
	for i, l := range lines {
		if !slices.Contains(drops, i+1) && strings.Contains(l, "fmt.") {
			return nil
		}
	}
	for n, text := range p.added(file) {
		t := strings.TrimSpace(text)
		if t == `"fmt"` || t == `import "fmt"` {
			return []int{n}
		}
	}
	return nil
}

var pragma = regexp.MustCompile(`^(//\s*(go:|\+build|nolint|eslint|@ts-|prettier-|istanbul|biome-|deno-|#region|#endregion)|#\s*(type:|noqa|pylint|pyright|mypy|frozen_string_literal|rubocop|-\*-)|#!|///\s*<reference)`)

// comments: did the model add comments? Deterministic edits never do.
func comments(ctx context.Context, p *pass) ([]core.Finding, string) {
	if !p.in.Patch.Model {
		return nil, na
	}
	var res []core.Finding
	drops := map[string][]int{}
	for _, f := range p.files {
		for _, h := range f.Hunks {
			lang := langOf(h.File)
			if !h.Model || lang == "" || f.New == "" {
				continue
			}
			before := map[string]bool{}
			for _, l := range h.Lines {
				if l.Op == '-' {
					before[strings.TrimSpace(l.Text)] = true
				}
			}
			for _, n := range commentLines(lang, h.Lines, before) {
				drops[h.File] = append(drops[h.File], n.New)
				res = append(res, finding("comments", h.File, n.New, "dropped", "removed comment `%s`", strings.TrimSpace(n.Text)))
			}
		}
	}
	for _, file := range slices.Sorted(maps.Keys(drops)) {
		if err := p.drop(file, drops[file]); err != nil {
			return nil, err.Error()
		}
	}
	sortFindings(res)
	return res, ""
}

// commentLines picks the added lines that are only a comment and were not
// already there before the hunk: whole line comments, and block comments
// whose every line is added.
func commentLines(lang string, lines []line, before map[string]bool) []line {
	var res []line
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		t := strings.TrimSpace(l.Text)
		if l.Op != '+' || before[t] || pragma.MatchString(t) {
			continue
		}
		if blockComments(lang) && strings.HasPrefix(t, "/*") {
			end := i
			for end < len(lines) && lines[end].Op == '+' && !strings.Contains(lines[end].Text, "*/") {
				end++
			}
			if end == len(lines) || lines[end].Op != '+' || !strings.HasSuffix(strings.TrimSpace(lines[end].Text), "*/") {
				continue
			}
			res = append(res, lines[i:end+1]...)
			i = end
			continue
		}
		if hasAny([]byte(t), lineComments(lang)) {
			res = append(res, l)
		}
	}
	return res
}
