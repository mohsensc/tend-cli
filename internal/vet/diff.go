package vet

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

type fileDiff struct {
	Old, New string // "" on the side where the file does not exist
	Hunks    []*hunk
}

// Path is the file as it is after the patch, or as it was when the patch deletes it.
func (f *fileDiff) Path() string {
	if f.New != "" {
		return f.New
	}
	return f.Old
}

type hunk struct {
	File     string
	OldStart int
	NewStart int
	Lines    []line
	Model    bool // a model wrote this hunk
}

type line struct {
	Op       byte // '+', '-' or ' '
	Text     string
	Old, New int // 1-based line on each side, 0 where the line does not exist
}

// parseDiff reads a unified git diff. Hunk bodies are consumed by their line
// counts, so a removed line that starts with "--" is never taken for a header.
func parseDiff(s string) []*fileDiff {
	var res []*fileDiff
	var curr *fileDiff
	var h *hunk
	oldLeft, newLeft, oldAt, newAt := 0, 0, 0, 0
	for _, l := range strings.Split(s, "\n") {
		if h != nil && (oldLeft > 0 || newLeft > 0) {
			if l == "" || l[0] == '\\' {
				continue
			}
			ln := line{Op: l[0], Text: l[1:]}
			switch l[0] {
			case '+':
				newAt++
				newLeft--
				ln.New = newAt
			case '-':
				oldAt++
				oldLeft--
				ln.Old = oldAt
			default:
				ln.Op = ' '
				oldAt++
				newAt++
				oldLeft--
				newLeft--
				ln.Old, ln.New = oldAt, newAt
			}
			h.Lines = append(h.Lines, ln)
			continue
		}
		switch {
		case strings.HasPrefix(l, "diff --git "):
			curr = &fileDiff{}
			if i := strings.LastIndex(l, " b/"); i > 0 {
				curr.Old = unquote(l[len("diff --git "):i], "a/")
				curr.New = unquote(l[i+1:], "b/")
			}
			res = append(res, curr)
			h = nil
		case curr == nil:
		case strings.HasPrefix(l, "new file mode"):
			curr.Old = ""
		case strings.HasPrefix(l, "deleted file mode"):
			curr.New = ""
		case strings.HasPrefix(l, "--- "):
			curr.Old = unquote(l[4:], "a/")
		case strings.HasPrefix(l, "+++ "):
			curr.New = unquote(l[4:], "b/")
		case strings.HasPrefix(l, "@@ "):
			var oldLen, newLen int
			h = &hunk{File: curr.Path()}
			h.OldStart, oldLen = span(l, '-')
			h.NewStart, newLen = span(l, '+')
			oldLeft, newLeft = oldLen, newLen
			oldAt, newAt = h.OldStart-1, h.NewStart-1
			if oldLen == 0 {
				oldAt = h.OldStart
			}
			if newLen == 0 {
				newAt = h.NewStart
			}
			curr.Hunks = append(curr.Hunks, h)
		}
	}
	return res
}

func unquote(s, prefix string) string {
	s = strings.TrimSuffix(s, "\t")
	if s == "/dev/null" {
		return ""
	}
	if strings.HasPrefix(s, `"`) {
		if u, err := strconv.Unquote(s); err == nil {
			s = u
		}
	}
	return strings.TrimPrefix(s, prefix)
}

// span reads "-12,3" or "+7" out of a hunk header.
func span(header string, sign byte) (start, n int) {
	for _, f := range strings.Fields(header) {
		if len(f) < 2 || f[0] != sign {
			continue
		}
		a, b, ok := strings.Cut(f[1:], ",")
		start, _ = strconv.Atoi(a)
		n = 1
		if ok {
			n, _ = strconv.Atoi(b)
		}
		return start, n
	}
	return 0, 0
}

// mentioned reads file names out of the parentheses of Applied lines, such as
// "bumped stripe 14.2.0 -> 16.1.0 (package.json, package-lock.json)".
func mentioned(applied []string) []string {
	var res []string
	for _, a := range applied {
		for {
			i := strings.IndexByte(a, '(')
			if i < 0 {
				break
			}
			j := strings.IndexByte(a[i:], ')')
			if j < 0 {
				break
			}
			for _, f := range strings.Split(a[i+1:i+j], ",") {
				if f = strings.TrimSpace(f); f != "" {
					res = append(res, f)
				}
			}
			a = a[i+j+1:]
		}
	}
	return res
}

// dropLines deletes the given 1-based lines from a file and leaves every other
// byte as it was.
func dropLines(dir, file string, nums []int) error {
	p := filepath.Join(dir, filepath.FromSlash(file))
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	drop := map[int]bool{}
	last := 0
	for _, n := range nums {
		drop[n] = true
		last = max(last, n)
	}
	var res []byte
	n := 0
	for len(b) > 0 {
		n++
		end := bytes.IndexByte(b, '\n') + 1
		if end == 0 {
			end = len(b)
		}
		if !drop[n] {
			res = append(res, b[:end]...)
		}
		b = b[end:]
	}
	if last > n {
		return fmt.Errorf("%s: no line %d", file, last)
	}
	fi, err := os.Stat(p)
	if err != nil {
		return err
	}
	return os.WriteFile(p, res, fi.Mode().Perm())
}

// replaceLine sets the 1-based line n of a file to text, keeping its
// indentation and line ending.
func replaceLine(dir, file string, n int, text string) error {
	p := filepath.Join(dir, filepath.FromSlash(file))
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	lines := bytes.SplitAfter(b, []byte("\n"))
	if n < 1 || n > len(lines) || len(lines[n-1]) == 0 {
		return fmt.Errorf("%s: no line %d", file, n)
	}
	l := lines[n-1]
	body := bytes.TrimRight(l, "\r\n")
	ind := body[:len(body)-len(bytes.TrimLeft(body, " \t"))]
	lines[n-1] = slices.Concat(ind, []byte(text), l[len(body):])
	fi, err := os.Stat(p)
	if err != nil {
		return err
	}
	return os.WriteFile(p, bytes.Join(lines, nil), fi.Mode().Perm())
}
