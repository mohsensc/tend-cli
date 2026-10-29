package scan

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
)

var update = flag.Bool("update", false, "rewrite golden files")

// golden is a FileScan plus what each site is attributed to.
type golden struct {
	Lang    string
	Imports []core.Import
	Sites   []goldenSite
	URLs    []core.URLUse
}

type goldenSite struct {
	Line  int
	Col   int
	Chain []string
	Call  bool
	Quals []string
	Text  string
}

func TestGolden(t *testing.T) {
	srcs, err := filepath.Glob("testdata/src/*")
	if err != nil || len(srcs) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	for _, p := range srcs {
		name := filepath.Base(p)
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			l := pick(name)
			if l == nil {
				t.Fatalf("no scanner for %s", name)
			}
			f, err := l.scan(name, src)
			if err != nil {
				t.Fatal(err)
			}
			g := golden{Lang: f.Lang, Imports: f.Imports, URLs: f.URLs}
			for _, s := range f.found {
				g.Sites = append(g.Sites, goldenSite{s.Line, s.Col, s.Chain, s.Call, s.quals, s.Text})
			}
			got, err := json.MarshalIndent(g, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			path := filepath.Join("testdata", "golden", name+".json")
			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run with -update)", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s differs from %s (run with -update and review the diff)\n%s", name, path, got)
			}
		})
	}
}

func TestScanners(t *testing.T) {
	var names []string
	for _, l := range langs {
		names = append(names, l.Lang())
	}
	want := []string{"go", "js", "python", "java", "ruby", "php", "csharp", "rust"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("langs = %v, want %v", names, want)
	}
	match := map[string]string{
		"a/b.go": "go", "x.mjs": "js", "x.TSX": "js", "x.cts": "js", "x.py": "python",
		"X.java": "java", "x.kt": "java", "build.gradle.kts": "java", "x.rb": "ruby",
		"x.rake": "ruby", "x.php": "php", "X.cs": "csharp", "x.rs": "rust",
		"x.json": "", "Makefile": "", "go.mod": "",
	}
	for p, want := range match {
		if got := Lang(p); got != want {
			t.Errorf("Lang(%q) = %q, want %q", p, got, want)
		}
	}
}

func TestIsTest(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"main_test.go", true},
		{"internal/api/catalog_test.go", true},
		{"main.go", false},
		{"src/billing.test.js", true},
		{"src/billing.spec.tsx", true},
		{"typings/index.test-d.ts", true},
		{"index.spec-d.ts", true},
		{"typings/index.d.ts", false},
		{"src/__tests__/billing.js", true},
		{"test/billing.js", true},
		{"src/billing.js", false},
		{"src/latest.js", false},
		{"tests/test_orders.py", true},
		{"app/test_orders.py", true},
		{"app/orders_test.py", true},
		{"conftest.py", true},
		{"app/orders.py", false},
		{"app/contest.py", false},
		{"spec/models/user_spec.rb", true},
		{"lib/user.rb", false},
		{"src/test/java/com/acme/BillingTest.java", true},
		{"src/main/java/com/acme/Billing.java", false},
		{"Acme.Billing.Tests/ChargeTests.cs", true},
		{"Acme.Billing/Charge.cs", false},
		{"testdata/projects/x/package.json", true},
		{"contest/entry.go", false},
		{"test.js", true},
		{"tests.js", true},
		{"packages/foo/test.mjs", true},
		{"test.py", true},
		{"app/tests.py", true},
		{"latest.js", false},
		{"contests.py", false},
	}
	for _, tt := range tests {
		if got := IsTest(tt.path); got != tt.want {
			t.Errorf("IsTest(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}
