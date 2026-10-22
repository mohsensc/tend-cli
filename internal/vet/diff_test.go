package vet

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

const sample = `diff --git a/src/a.js b/src/a.js
index 1111111..2222222 100644
--- a/src/a.js
+++ b/src/a.js
@@ -1,4 +1,4 @@
 const pay = require('pay')
--- a removed line that looks like a header
+pay.paymentIntents.create({})
 keep()
 last()
@@ -10,0 +11,2 @@ function x() {
+one()
+two()
diff --git a/new.txt b/new.txt
new file mode 100644
index 0000000..3333333
--- /dev/null
+++ b/new.txt
@@ -0,0 +1 @@
+hello
\ No newline at end of file
diff --git a/old.txt b/old.txt
deleted file mode 100644
index 4444444..0000000
--- a/old.txt
+++ /dev/null
@@ -1,2 +0,0 @@
-gone
-too
diff --git "a/sp ace.txt" "b/sp ace.txt"
index 5555555..6666666 100644
--- "a/sp ace.txt"
+++ "b/sp ace.txt"
@@ -1 +1 @@
-x
+y
`

func TestParseDiff(t *testing.T) {
	files := parseDiff(sample)
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Old+"|"+f.New)
	}
	if want := []string{"src/a.js|src/a.js", "|new.txt", "old.txt|", "sp ace.txt|sp ace.txt"}; !slices.Equal(paths, want) {
		t.Fatalf("files = %q, want %q", paths, want)
	}

	a := files[0]
	if len(a.Hunks) != 2 {
		t.Fatalf("hunks = %d", len(a.Hunks))
	}
	want := []line{
		{' ', "const pay = require('pay')", 1, 1},
		{'-', "-- a removed line that looks like a header", 2, 0},
		{'+', "pay.paymentIntents.create({})", 0, 2},
		{' ', "keep()", 3, 3},
		{' ', "last()", 4, 4},
	}
	if !slices.Equal(a.Hunks[0].Lines, want) {
		t.Errorf("hunk 1 = %+v", a.Hunks[0].Lines)
	}
	if got := a.Hunks[1].Lines; len(got) != 2 || got[0].New != 11 || got[1].New != 12 {
		t.Errorf("hunk 2 = %+v", got)
	}
	if got := files[1].Hunks[0].Lines; len(got) != 1 || got[0] != (line{'+', "hello", 0, 1}) {
		t.Errorf("new file = %+v", got)
	}
	if got := files[2].Hunks[0].Lines; len(got) != 2 || got[1] != (line{'-', "too", 2, 0}) || files[2].Path() != "old.txt" {
		t.Errorf("deleted file = %+v", got)
	}
}

func TestMentioned(t *testing.T) {
	applied := []string{
		"bumped @fixture/pay 1.4.0 -> 2.0.0 (package.json, package-lock.json)",
		"the model found nothing to change",
	}
	if got := mentioned(applied); !slices.Equal(got, []string{"package.json", "package-lock.json"}) {
		t.Errorf("mentioned = %q", got)
	}
	if got := mentioned([]string{"rewrote imports (a.go) (b.go, c.go)", "unbalanced (x"}); !slices.Equal(got, []string{"a.go", "b.go", "c.go"}) {
		t.Errorf("mentioned = %q", got)
	}
}

func TestDropLines(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("one\r\ntwo\r\nthree\r\nfour"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := dropLines(dir, "f.txt", []int{2, 4}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "one\r\nthree\r\n" {
		t.Errorf("file = %q", b)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode())
	}
	if err := dropLines(dir, "f.txt", []int{9}); err == nil {
		t.Error("dropping a line past the end succeeded")
	}
}
