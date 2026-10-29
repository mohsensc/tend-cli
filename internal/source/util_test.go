package source

import (
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
)

func TestParseRepo(t *testing.T) {
	tests := []struct {
		in, owner, name string
		ok              bool
	}{
		{"https://github.com/o/r", "o", "r", true},
		{"https://github.com/o/r.git", "o", "r", true},
		{"https://github.com/o/r/", "o", "r", true},
		{"https://github.com/o/r/tree/main/pkg", "o", "r", true},
		{"https://github.com/o/r#readme", "o", "r", true},
		{"git+https://github.com/o/r.git", "o", "r", true},
		{"git@github.com:o/r.git", "o", "r", true},
		{"ssh://git@github.com/o/r.git", "o", "r", true},
		{"http://github.com/o/my.repo", "o", "my.repo", true},
		{" https://github.com/o/r \n", "o", "r", true},
		{"https://GitHub.com/O/r", "O", "r", true},
		{"https://gitlab.com/o/r", "", "", false},
		{"https://github.com/o", "", "", false},
		{"https://example.com/github.com/o/r", "", "", false},
		{"", "", "", false},
	}
	for _, tc := range tests {
		owner, name, ok := ParseRepo(tc.in)
		if owner != tc.owner || name != tc.name || ok != tc.ok {
			t.Errorf("ParseRepo(%q) = %q, %q, %v; want %q, %q, %v", tc.in, owner, name, ok, tc.owner, tc.name, tc.ok)
		}
	}
}

func TestPlainVersion(t *testing.T) {
	tests := []struct {
		eco, in, want string
	}{
		{core.Npm, "1.2.3", "1.2.3"},
		{core.Npm, "  1.2.3 ", "1.2.3"},
		{core.Npm, "1.2.3-rc.1", "1.2.3-rc.1"},
		{core.Go, "v1.2.3", "1.2.3"},
		{core.Go, "v2.0.0+incompatible", "2.0.0"},
		{core.Go, "v0.0.0-20200101000000-abcdef123456", "0.0.0-20200101000000-abcdef123456"},
		{core.Pip, "2.28", "2.28"},
		{core.Actions, "v4.2.2", "4.2.2"},
		{core.Actions, "4.2.2", "4.2.2"},
		{core.Actions, "v4", ""},
		{core.Actions, "v4.2", ""},
		{core.Actions, "main", ""},
		{core.Actions, "11bd71901bbe5b1630ceea73d27597364c9af683", ""},
		{core.Npm, "", ""},
		{core.Npm, "latest", ""},
		{core.Npm, "workspace:*", ""},
	}
	for _, tc := range tests {
		got, ok := plainVersion(core.Dep{Ecosystem: tc.eco, Version: tc.in})
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("plainVersion(%s %q) = %q, %v; want %q", tc.eco, tc.in, got, ok, tc.want)
		}
	}
}

func TestAsWritten(t *testing.T) {
	tests := []struct{ cur, to, want string }{
		{"v1.2.3", "1.2.4", "v1.2.4"},
		{"1.2.3", "1.2.4", "1.2.4"},
		{"v1.2.3", "v1.2.4", "v1.2.4"},
		{"v1.2.3", "", ""},
		{"", "1.0.0", "1.0.0"},
	}
	for _, tc := range tests {
		if got := asWritten(tc.cur, tc.to); got != tc.want {
			t.Errorf("asWritten(%q, %q) = %q, want %q", tc.cur, tc.to, got, tc.want)
		}
	}
}

func TestLinks(t *testing.T) {
	tests := []struct {
		name string
		h    []string
		want []link
	}{
		{"one", []string{`<https://a.example/x>; rel="next"`}, []link{{"https://a.example/x", "next"}}},
		{"two in one value", []string{`<https://a/1>; rel="next", <https://a/9>; rel="last"`}, []link{{"https://a/1", "next"}, {"https://a/9", "last"}}},
		{"two values", []string{`<https://a/1>; rel="deprecation"`, `<https://a/2>; rel=sunset`}, []link{{"https://a/1", "deprecation"}, {"https://a/2", "sunset"}}},
		{"rel with several words", []string{`<https://a/1>; rel="deprecation sunset"`}, []link{{"https://a/1", "deprecation"}, {"https://a/1", "sunset"}}},
		{"other params first", []string{`<https://a/1>; type="text/html"; rel="Deprecation"`}, []link{{"https://a/1", "deprecation"}}},
		{"no rel", []string{`<https://a/1>; title="x"`}, nil},
		{"none", nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := links(map[string][]string{"Link": tc.h})
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("links = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestSameName(t *testing.T) {
	tests := []struct {
		eco, a, b string
		want      bool
	}{
		{core.Npm, "Lodash", "lodash", true},
		{core.Npm, "lodash", "lodash-es", false},
		{core.Pip, "Some_Pkg.name", "some-pkg-name", true},
		{core.Pip, "some--pkg", "some-pkg", true},
		{core.Npm, "a_b", "a-b", false},
		{core.Maven, "org.x:Y", "org.x:y", true},
	}
	for _, tc := range tests {
		if got := sameName(tc.eco, tc.a, tc.b); got != tc.want {
			t.Errorf("sameName(%s, %q, %q) = %v, want %v", tc.eco, tc.a, tc.b, got, tc.want)
		}
	}
}

func TestParallel(t *testing.T) {
	var live, peak, sum atomic.Int64
	slots := make([]int, 100)
	Parallel(len(slots), func(i int) {
		n := live.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		slots[i] = i * 2
		sum.Add(1)
		live.Add(-1)
	})
	if sum.Load() != 100 {
		t.Errorf("ran %d times, want 100", sum.Load())
	}
	if peak.Load() > workers {
		t.Errorf("%d at once, want at most %d", peak.Load(), workers)
	}
	for i, v := range slots {
		if v != i*2 {
			t.Fatalf("slot %d = %d", i, v)
		}
	}
	Parallel(0, func(int) { t.Error("ran with nothing to do") })
}

func TestNormSeverity(t *testing.T) {
	for in, want := range map[string]string{"LOW": "low", "Moderate": "moderate", "medium": "moderate", " HIGH ": "high", "critical": "critical", "unknown": "", "": "", "none": ""} {
		if got := normSeverity(in); got != want {
			t.Errorf("normSeverity(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBand(t *testing.T) {
	for score, want := range map[float64]string{10: "critical", 9: "critical", 8.9: "high", 7: "high", 6.9: "moderate", 4: "moderate", 3.9: "low", 0.1: "low", 0: ""} {
		if got := band(score); got != want {
			t.Errorf("band(%v) = %q, want %q", score, got, want)
		}
	}
}

func TestSecurityChangesOrder(t *testing.T) {
	d := dep(core.Go, "example.com/m", "v1.0.0")
	advs := []advisory{
		{fixed: "", severity: "low", ev: core.Evidence{Title: "none"}},
		{fixed: "1.10.0", ev: core.Evidence{Title: "ten"}},
		{fixed: "1.9.0", severity: "high", ev: core.Evidence{Title: "nine"}},
		{fixed: "2.0.0", ev: core.Evidence{Title: "major"}},
		{fixed: "1.9.0", severity: "critical", ev: core.Evidence{Title: "nine again"}},
	}
	got := securityChanges(d, advs)
	var have []string
	for _, c := range got {
		have = append(have, c.To+"/"+c.Severity+"/"+c.Level+"/"+strings.Join(tags(c.Evidence), ","))
	}
	want := []string{
		"v1.9.0/critical/minor/nine again,nine",
		"v1.10.0//minor/ten",
		"v2.0.0//major/major",
		"/low//none",
	}
	if !reflect.DeepEqual(have, want) {
		t.Errorf("changes:\n%v\nwant:\n%v", have, want)
	}
}
