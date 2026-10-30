package vet

import (
	"bytes"
	"path"
	"strings"
)

var exts = map[string]string{
	".go": "go",
	".js": "js", ".mjs": "js", ".cjs": "js", ".jsx": "js", ".ts": "js", ".tsx": "js", ".mts": "js", ".cts": "js",
	".py":   "python",
	".rb":   "ruby",
	".php":  "php",
	".java": "java", ".kt": "java", ".kts": "java", ".scala": "java",
	".cs": "csharp",
	".rs": "rust",
}

// langOf names the language of a source file by extension, "" for anything else.
func langOf(file string) string {
	return exts[strings.ToLower(path.Ext(file))]
}

func lineComments(lang string) []string {
	switch lang {
	case "python", "ruby":
		return []string{"#"}
	case "php":
		return []string{"//", "#"}
	case "":
		return nil
	}
	return []string{"//"}
}

func blockComments(lang string) bool {
	return lang != "" && lang != "python" && lang != "ruby"
}

// blank returns src with comments and the contents of string literals replaced
// by spaces, newlines kept, so a word search over it only finds code and line
// numbers still hold. It is a lexer for this purpose only: regex literals and
// heredocs are not understood.
func blank(lang string, src []byte) []byte {
	res := bytes.Clone(src)
	starts := lineComments(lang)
	block := blockComments(lang)
	wipe := func(from, to int) {
		for k := from; k < to && k < len(res); k++ {
			if res[k] != '\n' {
				res[k] = ' '
			}
		}
	}
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case block && bytes.HasPrefix(src[i:], []byte("/*")):
			end := bytes.Index(src[i+2:], []byte("*/"))
			if end < 0 {
				end = len(src)
			} else {
				end += i + 4
			}
			wipe(i, end)
			i = end
		case hasAny(src[i:], starts):
			end := i
			for end < len(src) && src[end] != '\n' {
				end++
			}
			wipe(i, end)
			i = end
		case lang == "python" && (bytes.HasPrefix(src[i:], []byte(`"""`)) || bytes.HasPrefix(src[i:], []byte(`'''`))):
			q := src[i : i+3]
			end := bytes.Index(src[i+3:], q)
			if end < 0 {
				end = len(src)
			} else {
				end += i + 3
			}
			wipe(i+3, end)
			i = min(end+3, len(src))
		case c == '"' || c == '\'' || c == '`':
			if c == '\'' && lang == "rust" && !rustChar(src[i:]) {
				i++
				continue
			}
			if c == '`' && lang != "go" && lang != "js" {
				i++
				continue
			}
			raw := c == '`' && lang == "go"
			end := i + 1
			for end < len(src) && src[end] != c {
				if src[end] == '\n' && c != '`' {
					break
				}
				if src[end] == '\\' && !raw {
					end++
				}
				end++
			}
			wipe(i+1, end)
			i = end + 1
		default:
			i++
		}
	}
	return res
}

func hasAny(b []byte, prefixes []string) bool {
	for _, p := range prefixes {
		if bytes.HasPrefix(b, []byte(p)) {
			return true
		}
	}
	return false
}

// rustChar tells a char literal ('a', '\n') from a lifetime ('a).
func rustChar(b []byte) bool {
	return len(b) > 2 && (b[1] == '\\' || b[2] == '\'')
}

func isIdent(c byte) bool {
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// words counts identifiers in code.
func words(code []byte, count map[string]int) {
	for i := 0; i < len(code); {
		if !isIdent(code[i]) {
			i++
			continue
		}
		j := i
		for j < len(code) && isIdent(code[j]) {
			j++
		}
		count[string(code[i:j])]++
		i = j
	}
}

// hasWord reports whether name occurs in s as a whole identifier.
func hasWord(s, name string) bool {
	for i := 0; ; {
		k := strings.Index(s[i:], name)
		if k < 0 {
			return false
		}
		at := i + k
		end := at + len(name)
		if (at == 0 || !isIdent(s[at-1])) && (end == len(s) || !isIdent(s[end])) {
			return true
		}
		i = at + 1
	}
}
