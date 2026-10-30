package ver

import (
	"reflect"
	"sort"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		in   string
		nums []int
		pre  string
		ok   bool
	}{
		{"1.2.3", []int{1, 2, 3}, "", true},
		{"v1.2.3", []int{1, 2, 3}, "", true},
		{"V2", []int{2}, "", true},
		{" 1.2 ", []int{1, 2}, "", true},
		{"1.2.3-rc.1", []int{1, 2, 3}, "rc.1", true},
		{"1.2.3-RC1", []int{1, 2, 3}, "rc1", true},
		{"1.0a1", []int{1, 0}, "a1", true},
		{"1.0.dev2", []int{1, 0}, "dev2", true},
		{"1.0.post1", []int{1, 0}, "", true},
		{"2024.1.2", []int{2024, 1, 2}, "", true},
		{"16-alpine", []int{16}, "", true},
		{"3.9-slim-bookworm", []int{3, 9}, "", true},
		{"20.11.1-alpine3.19", []int{20, 11, 1}, "", true},
		{"1.0.0+build.5", []int{1, 0, 0}, "", true},
		{"1.0.0-beta.2+exp.sha.5114f85", []int{1, 0, 0}, "beta.2", true},
		{"1.0.0-0.3.7", []int{1, 0, 0}, "0.3.7", true},
		{"5.0.0-SNAPSHOT", []int{5, 0, 0}, "snapshot", true},
		{"1.", []int{1}, "", true},
		{"latest", nil, "", false},
		{"", nil, "", false},
		{"v", nil, "", false},
		{"99999999999999999999999", nil, "", false},
	}
	for _, tt := range tests {
		v, ok := Parse(tt.in)
		if ok != tt.ok || !reflect.DeepEqual(v.Nums, tt.nums) || v.Pre != tt.pre || v.Raw != tt.in {
			t.Errorf("Parse(%q) = %+v, %v; want nums %v pre %q ok %v", tt.in, v, ok, tt.nums, tt.pre, tt.ok)
		}
	}
}

func TestCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.3", 0},
		{"1.2", "1.2.0", 0},
		{"v1.2.3", "1.2.3", 0},
		{"1.2.3", "1.2.4", -1},
		{"1.10.0", "1.9.0", 1},
		{"2.0.0", "10.0.0", -1},
		{"1.0.0-rc.1", "1.0.0", -1},
		{"1.0.0", "1.0.0-rc.1", 1},
		{"1.0.0-rc.2", "1.0.0-rc.10", -1},
		{"1.0.0-rc1", "1.0.0-rc2", -1},
		{"1.0.0-rc.1", "1.0.0-rc1", 0},
		{"1.0.0-alpha", "1.0.0-alpha.1", -1},
		{"1.0.0-alpha.1", "1.0.0-alpha.beta", -1},
		{"1.0.0-alpha", "1.0.0-beta", -1},
		{"1.0.0-beta.11", "1.0.0-rc.1", -1},
		{"1.0.0-0.3.7", "1.0.0-alpha", -1},
		{"1.0.dev2", "1.0a1", -1},
		{"1.0a1", "1.0b1", -1},
		{"1.0b2", "1.0rc1", -1},
		{"1.0rc1", "1.0", -1},
		{"1.0", "1.0.post1", -1},
		{"1.0.post1", "1.0.post2", -1},
		{"1.0.post1", "1.0.1", -1},
		{"0.9.9", "1.0.dev1", -1},
		{"2024.1.2", "2024.10.0", -1},
		{"2023.12.31", "2024.1.1", -1},
		{"16-alpine", "18-alpine", -1},
		{"18-alpine", "18", 0},
		{"3.9-slim", "3.10-slim", -1},
		{"1.0.0+build.1", "1.0.0+build.2", 0},
		{"1.0.0-SNAPSHOT", "1.0.0", -1},
	}
	for _, tt := range tests {
		a, okA := Parse(tt.a)
		b, okB := Parse(tt.b)
		if !okA || !okB {
			t.Fatalf("Parse(%q, %q) failed", tt.a, tt.b)
		}
		if got := Compare(a, b); got != tt.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
		if got := Compare(b, a); got != -tt.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", tt.b, tt.a, got, -tt.want)
		}
	}
}

func TestLessSorts(t *testing.T) {
	vs := []string{"2.0.0", "garbage", "1.0.0", "1.0.0-rc.1", "v1.10.0", "1.2", "", "1.0.0-beta"}
	sort.Slice(vs, func(i, j int) bool { return Less(vs[i], vs[j]) })
	want := []string{"", "garbage", "1.0.0-beta", "1.0.0-rc.1", "1.0.0", "1.2", "v1.10.0", "2.0.0"}
	if !reflect.DeepEqual(vs, want) {
		t.Errorf("sorted = %q, want %q", vs, want)
	}
}

func TestParseEpoch(t *testing.T) {
	if v, ok := Parse("1!2.0"); ok {
		t.Errorf("Parse(1!2.0) = %v", v)
	}
}

func TestLevel(t *testing.T) {
	tests := []struct{ from, to, want string }{
		{"1.2.3", "2.0.0", "major"},
		{"1.2.3", "1.3.0", "minor"},
		{"1.2.3", "1.2.4", "patch"},
		{"1.2", "1.2.0", "same"},
		{"1.0.0-rc.1", "1.0.0", "patch"},
		{"1.0", "1.0.post1", "patch"},
		{"16-alpine", "22-alpine", "major"},
		{"0.1.0", "0.2.0", "major"},
		{"0.11.27", "0.12.0", "major"},
		{"0.1.0", "0.1.5", "patch"},
		{"0.0.1", "0.0.2", "major"},
		{"0.9.3", "1.0.0", "major"},
		{"1.2", "1.3", "minor"},
	}
	for _, tt := range tests {
		a, _ := Parse(tt.from)
		b, _ := Parse(tt.to)
		if got := Level(a, b); got != tt.want {
			t.Errorf("Level(%q, %q) = %q, want %q", tt.from, tt.to, got, tt.want)
		}
	}
}

func TestStep(t *testing.T) {
	tests := []struct{ from, to, want string }{
		{"1.2.3", "2.0.0", "major"},
		{"1.2", "1.2.0", ""},
		{"latest", "1.0.0", ""},
	}
	for _, tt := range tests {
		if got := Step(tt.from, tt.to); got != tt.want {
			t.Errorf("Step(%q, %q) = %q, want %q", tt.from, tt.to, got, tt.want)
		}
	}
}

func TestStepIn(t *testing.T) {
	tests := []struct{ eco, from, to, want string }{
		{"go", "v0.13.0", "v0.49.0", "minor"},
		{"go", "v0.0.0-20191026070338-33540a1f6037", "v0.49.0", "minor"},
		{"go", "v1.2.0", "v2.0.0", "major"},
		{"go", "v0.13.0", "v0.13.1", "patch"},
		{"cargo", "0.1.0", "0.2.0", "major"},
		{"npm", "0.13.0", "0.49.0", "major"},
	}
	for _, tt := range tests {
		if got := StepIn(tt.eco, tt.from, tt.to); got != tt.want {
			t.Errorf("StepIn(%s, %q, %q) = %q, want %q", tt.eco, tt.from, tt.to, got, tt.want)
		}
	}
}

func TestMajor(t *testing.T) {
	for in, want := range map[string]int{"v14.2.0": 14, "22-alpine": 22, "0.3": 0} {
		v, _ := Parse(in)
		if got := v.Major(); got != want {
			t.Errorf("Major(%q) = %d, want %d", in, got, want)
		}
	}
	if (V{}).Major() != 0 {
		t.Error("zero V has a major")
	}
}

func TestGoLess(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"v1.0.0", "v1.0.1", true},
		{"v1.0.1", "v1.0.0", false},
		{"v1.0.0-rc.1", "v1.0.0", true},
		{"v0.0.0-20240101000000-abcdefabcdef", "v0.1.0", true},
		{"v2.0.0+incompatible", "v2.0.1+incompatible", true},
		{"1.0.0", "v0.0.1", true},
	}
	for _, tt := range tests {
		if got := GoLess(tt.a, tt.b); got != tt.want {
			t.Errorf("GoLess(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}
