package patch

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/gitx"
)

const defaultAttempts = 2

var errNoDiff = errors.New("the reply holds no unified diff")

// migrate asks the model for a diff over the remaining sites and applies it. A reply
// that cannot be used is sent back with the reason while attempts remain. With
// o.Prove set, a diff that applies runs the project's commands too, and one that
// fails them is taken back and sent back with their output; the last attempt's
// diff stays for the caller's proof to judge.
func migrate(ctx context.Context, t *tree, in job, o Options, p *core.Patch) error {
	n := o.Attempts
	if n < 1 {
		n = defaultAttempts
	}
	snap := ""
	if o.Prove != nil && n > 1 {
		var err error
		if snap, err = stage(ctx, t.wt); err != nil {
			return err
		}
	}
	var fails []string
	var why, prev string
	ran := false
	for i := 0; i < n; i++ {
		sys, user, err := prompt(t, in, o, why, prev, ran)
		if err != nil {
			return err
		}
		reply, err := o.Model.Complete(ctx, sys, user)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fails = append(fails, fmt.Sprintf("attempt %d: model %s: %v", i+1, o.Model.Name(), err))
			continue
		}
		diff, none, err := extract(reply)
		if none {
			o.logf("the model found nothing to change")
			p.Applied = append(p.Applied, "the model found nothing to change")
			return nil
		}
		if err == nil {
			err = inScope(diff, in.allowed)
		}
		if err == nil {
			err = apply(ctx, t.wt, diff)
		}
		if err != nil {
			o.logf("model attempt %d: %v", i+1, err)
			fails = append(fails, fmt.Sprintf("attempt %d: %v", i+1, err))
			why, prev, ran = err.Error(), diff, false
			continue
		}
		if snap != "" && i < n-1 {
			failed, err := check(ctx, t.wt, o)
			if err != nil {
				return err
			}
			if failed != "" {
				o.logf("model attempt %d: the diff applies, but %s", i+1, strings.SplitN(failed, "\n", 2)[0])
				fails = append(fails, fmt.Sprintf("attempt %d: %s", i+1, strings.SplitN(failed, "\n", 2)[0]))
				if err := restore(ctx, t.wt, snap); err != nil {
					return err
				}
				why, prev, ran = failed, diff, true
				continue
			}
		}
		p.Model = true
		p.Edited = touched(diff)
		p.Applied = append(p.Applied, "the model edited "+strings.Join(p.Edited, ", "))
		return nil
	}
	return errors.New(strings.Join(fails, "; "))
}

// check stages what a diff changed and runs the project's commands on it.
// It returns the failing command and the end of its output, "" when they
// pass.
func check(ctx context.Context, wt string, o Options) (string, error) {
	if _, err := stage(ctx, wt); err != nil {
		return "", err
	}
	r, err := o.Prove(ctx, o.Commands)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", nil
	}
	if r.Pass {
		return "", nil
	}
	for _, st := range r.Steps {
		if st.Skipped == "" && st.Exit != 0 {
			return fmt.Sprintf("%s exited %d\n%s", st.Cmd, st.Exit, tail(st.Tail, feedbackMax)), nil
		}
	}
	return "the project's commands failed", nil
}

// stage stages everything in wt and returns its tree.
func stage(ctx context.Context, wt string) (string, error) {
	if _, err := gitx.Raw(ctx, wt, "add", "-A"); err != nil {
		return "", err
	}
	tree, err := gitx.Run(ctx, wt, "write-tree")
	return tree, err
}

// restore puts wt back to tree, staged, with nothing else left in it.
func restore(ctx context.Context, wt, tree string) error {
	if _, err := gitx.Raw(ctx, wt, "read-tree", "--reset", "-u", tree); err != nil {
		return err
	}
	_, err := gitx.Raw(ctx, wt, "clean", "-fdq")
	return err
}

// extract pulls the diff out of a reply: the first ```diff or bare ``` block, else the
// text from the first diff header on. none is true for a NOCHANGE reply.
func extract(reply string) (diff string, none bool, err error) {
	s := strings.ReplaceAll(reply, "\r\n", "\n")
	body, ok := fenced(s)
	if !ok {
		body = s
	}
	start := diffStart(body)
	if start < 0 {
		for _, ln := range strings.Split(s, "\n") {
			if strings.Trim(ln, " \t`*.") == "NOCHANGE" {
				return "", true, nil
			}
		}
		return "", false, errNoDiff
	}
	diff = normalize(cut(body[start:]))
	if len(touched(diff)) == 0 || !strings.Contains(diff, "\n@@") {
		return "", false, errNoDiff
	}
	return diff, false, nil
}

func fenced(s string) (string, bool) {
	lines := strings.Split(s, "\n")
	for i := 0; i < len(lines); i++ {
		ln := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(ln, "```") {
			continue
		}
		info := strings.TrimSpace(strings.TrimPrefix(ln, "```"))
		j := i + 1
		for j < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[j]), "```") {
			j++
		}
		body := strings.Join(lines[i+1:min(j, len(lines))], "\n")
		if (info == "" || info == "diff" || info == "patch") && diffStart(body) >= 0 {
			return body, true
		}
		i = j
	}
	return "", false
}

// diffStart returns the offset of the first "diff --git" or "--- " line, -1 if none.
func diffStart(s string) int {
	off := 0
	for _, ln := range strings.SplitAfter(s, "\n") {
		if strings.HasPrefix(ln, "diff --git ") || strings.HasPrefix(ln, "--- ") {
			return off
		}
		off += len(ln)
	}
	return -1
}

// cut keeps lines while they can belong to a diff, dropping chatter after it.
func cut(s string) string {
	var res []string
	for _, ln := range strings.Split(s, "\n") {
		if !diffLine(ln) {
			break
		}
		res = append(res, ln)
	}
	for len(res) > 0 && res[len(res)-1] == "" {
		res = res[:len(res)-1]
	}
	return strings.Join(res, "\n") + "\n"
}

func diffLine(ln string) bool {
	if ln == "" {
		return true // a blank context line whose leading space was lost
	}
	for _, p := range []string{" ", "+", "-", "@@", "\\", "diff --git ", "index ", "new file mode", "deleted file mode", "old mode", "new mode"} {
		if strings.HasPrefix(ln, p) {
			return true
		}
	}
	return false
}

// normalize gives every header the a/ and b/ prefixes git apply strips with -p1,
// and puts a "diff --git" line in front of each file that lacks one: with it,
// --recount knows where a file's last hunk ends.
func normalize(diff string) string {
	lines := strings.Split(diff, "\n")
	var res []string
	for i := 0; i < len(lines); i++ {
		ln := lines[i]
		switch {
		case strings.HasPrefix(ln, "diff --git "):
			f := strings.Fields(strings.TrimPrefix(ln, "diff --git "))
			if len(f) == 2 {
				ln = "diff --git " + prefix("a/", f[0]) + " " + prefix("b/", f[1])
			}
		case header(lines, i):
			old, now := prefix("a/", headerPath(ln)), prefix("b/", headerPath(lines[i+1]))
			if !gitHeader(res) {
				res = append(res, gitLines(old, now)...)
			}
			res = append(res, "--- "+old, "+++ "+now)
			i++
			continue
		}
		res = append(res, ln)
	}
	return strings.Join(res, "\n")
}

// gitHeader reports whether the lines so far end in a "diff --git" line and
// its extended header.
func gitHeader(lines []string) bool {
	for i := len(lines) - 1; i >= 0; i-- {
		ln := lines[i]
		if strings.HasPrefix(ln, "diff --git ") {
			return true
		}
		if !slices.ContainsFunc([]string{"index ", "new file mode", "deleted file mode", "old mode", "new mode"}, func(p string) bool { return strings.HasPrefix(ln, p) }) {
			return false
		}
	}
	return false
}

func gitLines(old, now string) []string {
	switch {
	case old == "/dev/null":
		return []string{"diff --git a/" + now[2:] + " " + now, "new file mode 100644"}
	case now == "/dev/null":
		return []string{"diff --git " + old + " b/" + old[2:], "deleted file mode 100644"}
	}
	return []string{"diff --git " + old + " " + now}
}

// header reports whether line i opens a file header; a removed line that starts
// with "-- " looks the same but is not followed by "+++ ".
func header(lines []string, i int) bool {
	return strings.HasPrefix(lines[i], "--- ") && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "+++ ")
}

func headerPath(ln string) string {
	p := ln[4:]
	if i := strings.IndexByte(p, '\t'); i >= 0 {
		p = p[:i]
	}
	return strings.TrimSpace(p)
}

func prefix(pre, p string) string {
	if p == "/dev/null" {
		return p
	}
	p = strings.TrimPrefix(strings.TrimPrefix(p, "a/"), "b/")
	return pre + strings.TrimPrefix(p, "./")
}

// touched lists the repo-relative paths a normalized diff names, sorted.
func touched(diff string) []string {
	lines := strings.Split(diff, "\n")
	var res []string
	for i := range lines {
		if !header(lines, i) {
			continue
		}
		for _, p := range []string{headerPath(lines[i]), headerPath(lines[i+1])} {
			if p != "/dev/null" {
				res = append(res, p[2:])
			}
		}
	}
	return uniq(res)
}

// inScope rejects a diff that reaches past the files the change explains.
func inScope(diff string, allowed []string) error {
	var bad []string
	for _, f := range touched(diff) {
		if path.IsAbs(f) || path.Clean(f) != f || strings.HasPrefix(f, "../") || !slices.Contains(allowed, f) {
			bad = append(bad, f)
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("the diff touches files outside this change: %s", strings.Join(bad, ", "))
	}
	return nil
}

// apply applies diff to the worktree. Hunk counts written by a model are often
// off, which --recount forgives; but --recount reads a following file's "--- a/"
// header as a removed line when a hunk ends in context, so the counts are
// trusted first. A three-way merge comes last; when it fails it leaves conflict
// markers, so the staged tree is restored. The error is the first attempt's,
// which says the most about the diff as written.
func apply(ctx context.Context, wt, diff string) error {
	if _, err := gitx.Raw(ctx, wt, "add", "-A"); err != nil {
		return err
	}
	var first error
	for _, flags := range [][]string{nil, {"--recount"}} {
		_, err := (gitx.Cmd{Dir: wt, Stdin: diff}).Run(ctx, append([]string{"apply", "--whitespace=nowarn"}, flags...)...)
		if err == nil {
			return nil
		}
		if first == nil {
			first = err
		}
	}
	snap, err := gitx.Raw(ctx, wt, "write-tree")
	if err != nil {
		return err
	}
	for _, flags := range [][]string{{"--3way"}, {"--3way", "--recount"}} {
		if _, err := (gitx.Cmd{Dir: wt, Stdin: diff}).Run(ctx, append([]string{"apply", "--whitespace=nowarn"}, flags...)...); err == nil {
			return nil
		}
		if _, err := gitx.Raw(ctx, wt, "read-tree", "--reset", "-u", strings.TrimSpace(snap)); err != nil {
			return err
		}
		if _, err := gitx.Raw(ctx, wt, "clean", "-fdq"); err != nil {
			return err
		}
	}
	return first
}
