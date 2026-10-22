package patch

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/gitx"
)

const goodDiff = "diff --git a/src/billing.js b/src/billing.js\n" +
	"--- a/src/billing.js\n" +
	"+++ b/src/billing.js\n" +
	"@@ -1,3 +1,3 @@\n" +
	" const pay = require('@fixture/pay');\n" +
	"-const old = 1;\n" +
	"+const now = 1;\n" +
	" \n"

func TestExtract(t *testing.T) {
	bare := strings.TrimPrefix(goodDiff, "diff --git a/src/billing.js b/src/billing.js\n")
	tests := []struct {
		name  string
		reply string
		want  string
		none  bool
		fail  bool
	}{
		{name: "fenced diff", reply: "Here it is:\n```diff\n" + goodDiff + "```\nDone.", want: goodDiff},
		{name: "fenced plain", reply: "```\n" + goodDiff + "```", want: goodDiff},
		{name: "fenced other language first", reply: "```js\nconst a = 1;\n```\n\n```diff\n" + goodDiff + "```", want: goodDiff},
		{name: "bare", reply: goodDiff, want: goodDiff},
		{name: "no git header", reply: bare, want: goodDiff},
		{name: "chatter before and after", reply: "Sure. The fix:\n\n" + goodDiff + "\nThis renames the constant.\nLet me know.", want: goodDiff},
		{
			name:  "missing prefixes",
			reply: strings.ReplaceAll(strings.ReplaceAll(goodDiff, "--- a/", "--- "), "+++ b/", "+++ "),
			want:  goodDiff,
		},
		{
			name:  "git header without prefixes and dot slash",
			reply: strings.Replace(strings.Replace(goodDiff, "a/src/billing.js b/src/billing.js", "src/billing.js ./src/billing.js", 1), "+++ b/", "+++ ./", 1),
			want:  goodDiff,
		},
		{name: "crlf", reply: "```diff\r\n" + strings.ReplaceAll(goodDiff, "\n", "\r\n") + "```\r\n", want: goodDiff},
		{
			name:  "timestamps on headers",
			reply: strings.Replace(strings.Replace(bare, "billing.js\n", "billing.js\t2026-01-01 10:00:00\n", 1), "billing.js\n", "billing.js\t2026-01-02 10:00:00\n", 1),
			want:  goodDiff,
		},
		{
			name:  "removed line that looks like a header",
			reply: "--- a/q.sql\n+++ b/q.sql\n@@ -1,2 +1,1 @@\n--- old note\n select 1;\n",
			want:  "diff --git a/q.sql b/q.sql\n--- a/q.sql\n+++ b/q.sql\n@@ -1,2 +1,1 @@\n--- old note\n select 1;\n",
		},
		{
			name:  "new file",
			reply: "--- /dev/null\n+++ src/new.js\n@@ -0,0 +1 @@\n+x\n",
			want:  "diff --git a/src/new.js b/src/new.js\nnew file mode 100644\n--- /dev/null\n+++ b/src/new.js\n@@ -0,0 +1 @@\n+x\n",
		},
		{name: "nochange", reply: "NOCHANGE", none: true},
		{name: "nochange with chatter", reply: "I checked every site.\n\n**NOCHANGE**\n", none: true},
		{name: "nochange fenced", reply: "```\nNOCHANGE\n```", none: true},
		{name: "garbage", reply: "I could not figure this one out.", fail: true},
		{name: "headers without hunks", reply: "--- a/x\n+++ b/x\n", fail: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, none, err := extract(tt.reply)
			if tt.fail {
				if err == nil {
					t.Fatalf("got %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if none != tt.none || got != tt.want {
				t.Errorf("got none=%v %q\nwant none=%v %q", none, got, tt.none, tt.want)
			}
		})
	}
}

func TestTouched(t *testing.T) {
	diff := "--- a/b.js\n+++ b/b.js\n@@ -1 +1 @@\n-x\n+y\n--- a/a.js\n+++ /dev/null\n@@ -1 +0,0 @@\n-z\n"
	if got := touched(diff); !slices.Equal(got, []string{"a.js", "b.js"}) {
		t.Errorf("touched = %q", got)
	}
}

func TestInScope(t *testing.T) {
	allowed := []string{"package.json", "src/billing.js"}
	tests := []struct {
		diff string
		bad  string
	}{
		{goodDiff, ""},
		{"--- a/src/billing.js\n+++ b/src/billing.js\n@@ -1 +1 @@\n-a\n+b\n--- a/.env\n+++ b/.env\n@@ -1 +1 @@\n-a\n+b\n", ".env"},
		{"--- a/src/../package.json\n+++ b/src/../package.json\n@@ -1 +1 @@\n-a\n+b\n", "src/../package.json"},
		{"--- /dev/null\n+++ b/src/new.js\n@@ -0,0 +1 @@\n+x\n", "src/new.js"},
	}
	for _, tt := range tests {
		err := inScope(tt.diff, allowed)
		if tt.bad == "" {
			if err != nil {
				t.Errorf("%v", err)
			}
			continue
		}
		if err == nil || !strings.HasSuffix(err.Error(), ": "+tt.bad) {
			t.Errorf("err = %v, want it to name %s", err, tt.bad)
		}
	}
}

func TestApplyRestoresAfterConflict(t *testing.T) {
	dir := repo(t, map[string]string{"x.txt": "1\n2\n3\n4\n5\n"})
	writeFile(t, dir, "x.txt", "1\n2\nTHREE\n4\n5\n")
	diff := fmt.Sprintf("diff --git a/x.txt b/x.txt\nindex %s..%s 100644\n--- a/x.txt\n+++ b/x.txt\n@@ -2,3 +2,3 @@\n 2\n-3\n+three\n 4\n",
		blob(t, dir, "1\n2\n3\n4\n5\n"), blob(t, dir, "1\n2\nthree\n4\n5\n"))
	if err := apply(context.Background(), dir, diff); err == nil {
		t.Fatal("a conflicting diff applied")
	}
	if got := readFile(t, dir, "x.txt"); got != "1\n2\nTHREE\n4\n5\n" {
		t.Errorf("x.txt after a failed merge:\n%s", got)
	}
	if st := run(t, dir, "status", "--porcelain"); st != "M  x.txt\n" {
		t.Errorf("status:\n%s", st)
	}
}

func TestApplyCounts(t *testing.T) {
	files := map[string]string{"a.py": "def a():\n    return 1\n\n", "b.py": "def b():\n    return 2\n"}
	want := "def a():\n    return 10\n\ndef b():\n    return 20\n"
	tests := []struct {
		name, diff string
	}{
		{
			// --recount alone reads b.py's header as a removed line here.
			name: "hunk ending in context before the next file",
			diff: "--- a/a.py\n+++ b/a.py\n@@ -1,3 +1,3 @@\n def a():\n-    return 1\n+    return 10\n \n" +
				"--- a/b.py\n+++ b/b.py\n@@ -1,2 +1,2 @@\n def b():\n-    return 2\n+    return 20\n",
		},
		{
			name: "counts that are off",
			diff: "--- a/a.py\n+++ b/a.py\n@@ -1,9 +1,9 @@\n def a():\n-    return 1\n+    return 10\n \n" +
				"--- a/b.py\n+++ b/b.py\n@@ -1,7 +1,7 @@\n def b():\n-    return 2\n+    return 20\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := repo(t, files)
			if err := apply(context.Background(), dir, normalize(tt.diff)); err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, dir, "a.py") + readFile(t, dir, "b.py"); got != want {
				t.Errorf("after apply:\n%s", got)
			}
		})
	}
}

// blob stores body in the object database and returns its abbreviated id.
func blob(t *testing.T, dir, body string) string {
	t.Helper()
	out, err := (gitx.Cmd{Dir: dir, Stdin: body}).Run(context.Background(), "hash-object", "-w", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out)[:12]
}

func TestTail(t *testing.T) {
	var lines []string
	for i := 1; i <= 100; i++ {
		lines = append(lines, fmt.Sprint("line ", i))
	}
	got := tail(strings.Join(lines, "\n")+"\n", 60)
	if !strings.HasPrefix(got, "line 41\n") || !strings.HasSuffix(got, "line 100") {
		t.Errorf("tail:\n%s", got)
	}
}
