package vet

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/gitx"
)

var defPats = map[string][]*regexp.Regexp{
	"go": {
		regexp.MustCompile(`^func\s+(?:\([^)]*\)\s*)?([A-Za-z_]\w*)`),
		regexp.MustCompile(`^(?:const|var|type)\s+([A-Za-z_]\w*)`),
	},
	"js": {
		regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s*\*?\s*([A-Za-z_$][\w$]*)`),
		regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let|var|class)\s+([A-Za-z_$][\w$]*)`),
	},
	"python": {
		regexp.MustCompile(`^\s*(?:async\s+)?def\s+([A-Za-z_]\w*)`),
		regexp.MustCompile(`^\s*class\s+([A-Za-z_]\w*)`),
		regexp.MustCompile(`^([A-Za-z_]\w*)\s*=[^=]`),
	},
	"ruby": {
		regexp.MustCompile(`^\s*def\s+(?:self\.)?([A-Za-z_]\w*)`),
		regexp.MustCompile(`^\s*(?:class|module)\s+([A-Z]\w*)`),
	},
	"php": {
		regexp.MustCompile(`\bfunction\s+([A-Za-z_]\w*)`),
		regexp.MustCompile(`^\s*(?:abstract\s+|final\s+)?class\s+([A-Za-z_]\w*)`),
	},
	"java": {
		regexp.MustCompile(`^\s*(?:(?:public|private|protected|internal|static|final|abstract|open|data|sealed)\s+)*(?:class|interface|enum|fun|object)\s+([A-Za-z_]\w*)`),
		regexp.MustCompile(`^\s*(?:(?:public|private|protected|static|final|abstract|synchronized)\s+)+[\w<>\[\],.?]+\s+([A-Za-z_]\w*)\s*\(`),
	},
	"csharp": {
		regexp.MustCompile(`^\s*(?:(?:public|private|protected|internal|static|sealed|abstract|partial)\s+)*(?:class|interface|enum|struct|record)\s+([A-Za-z_]\w*)`),
		regexp.MustCompile(`^\s*(?:(?:public|private|protected|internal|static|virtual|override|async|sealed)\s+)+[\w<>\[\],.?]+\s+([A-Za-z_]\w*)\s*\(`),
	},
	"rust": {
		regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?(?:const\s+)?(?:async\s+)?(?:unsafe\s+)?fn\s+([A-Za-z_]\w*)`),
		regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?(?:struct|enum|trait|const|static|type)\s+([A-Za-z_]\w*)`),
	},
}

// Names called by a runtime or a framework rather than by the code itself.
var implicit = regexp.MustCompile(`^(main|init|constructor|__\w+__|setUp|tearDown|String|Error|toString|(Test|Benchmark|Example|Fuzz|test_)\w*)$`)

type def struct {
	file string
	line int
}

// dead: did the patch orphan anything? A definition in a touched file counts
// when the only references to it the base had were on lines the patch removed.
func dead(ctx context.Context, p *pass) ([]core.Finding, string) {
	if !p.code() {
		return nil, na
	}
	removed := map[string]int{}
	for _, f := range p.files {
		for _, h := range f.Hunks {
			lang := langOf(h.File)
			for _, l := range h.Lines {
				if l.Op != '-' {
					continue
				}
				code := blank(lang, []byte(l.Text))
				words(code, removed)
				// A rewritten definition line is not a reference to what it defines.
				if name := defined(lang, string(code)); name != "" {
					removed[name]--
				}
			}
		}
	}
	if len(removed) == 0 {
		return nil, "nothing was removed"
	}
	defs := map[string][]def{}
	for _, f := range p.files {
		file := f.Path()
		lang := langOf(file)
		if f.New == "" || defPats[lang] == nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(p.in.Dir, filepath.FromSlash(file)))
		if err != nil {
			continue
		}
		for i, l := range strings.Split(string(blank(lang, b)), "\n") {
			if name := defined(lang, l); removed[name] > 0 && len(name) > 1 && !implicit.MatchString(name) {
				defs[name] = append(defs[name], def{file, i + 1})
			}
		}
	}
	if len(defs) == 0 {
		return nil, ""
	}
	count, err := codeWords(ctx, p.in.Dir)
	if err != nil {
		return nil, err.Error()
	}
	var res []core.Finding
	for name, ds := range defs {
		if count[name] > len(ds) {
			continue
		}
		for _, d := range ds {
			res = append(res, finding("dead", d.file, d.line, "flagged", "`%s` is now unreferenced", name))
		}
	}
	sortFindings(res)
	return res, ""
}

func defined(lang, code string) string {
	for _, re := range defPats[lang] {
		if m := re.FindStringSubmatch(code); m != nil {
			return m[1]
		}
	}
	return ""
}

// codeWords counts identifiers across every source file in the worktree,
// with comments and strings blanked.
func codeWords(ctx context.Context, dir string) (map[string]int, error) {
	files, err := gitx.Files(ctx, dir)
	if err != nil {
		return nil, err
	}
	count := map[string]int{}
	for _, f := range files {
		lang := langOf(f)
		if lang == "" || strings.Contains("/"+f, "/vendor/") || strings.Contains("/"+f, "/node_modules/") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f)))
		if err != nil || len(b) > 2<<20 || bytes.IndexByte(b, 0) >= 0 {
			continue
		}
		words(blank(lang, b), count)
	}
	return count, nil
}
