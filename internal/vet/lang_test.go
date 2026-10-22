package vet

import "testing"

func TestBlank(t *testing.T) {
	tests := []struct {
		name, lang, src, want string
	}{
		{"go line comment", "go", "a := 1 // b c\nd", "a := 1       \nd"},
		{"go block comment keeps newlines", "go", "x /* y\nz */ w", "x     \n     w"},
		{"go raw string spans lines", "go", "s := `a\n\"b`+c", "s := ` \n  `+c"},
		{"string holding a comment", "js", `f("// not a comment") + g`, `f("                ") + g`},
		{"comment holding a string", "js", "f() // say \"hi\"\ng()", "f()            \ng()"},
		{"escaped quote", "js", `a("x\"y") + b`, `a("    ") + b`},
		{"template literal", "js", "a(`x\n${y}`) + b", "a(` \n    `) + b"},
		{"python triple quotes", "python", "x = \"\"\"doc\n'q'\"\"\" + y  # c", "x = \"\"\"   \n   \"\"\" + y     "},
		{"python hash", "python", "a = b # c\n", "a = b    \n"},
		{"ruby hash comment", "ruby", "foo # bar", "foo      "},
		{"php both comments", "php", "a(); # x\nb(); // y", "a();    \nb();     "},
		{"rust lifetime is not a char", "rust", "fn f<'a>(x: &'a str) -> char { 'z' }", "fn f<'a>(x: &'a str) -> char { ' ' }"},
		{"unterminated string stops at newline", "js", "a('oops\nb()", "a('    \nb()"},
		{"unknown language untouched", "", "a // b", "a // b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(blank(tt.lang, []byte(tt.src)))
			if got != tt.want {
				t.Errorf("blank =\n%q\nwant\n%q", got, tt.want)
			}
			if len(got) != len(tt.src) {
				t.Errorf("length changed: %d -> %d", len(tt.src), len(got))
			}
		})
	}
}

func TestHasWord(t *testing.T) {
	tests := []struct {
		s, name string
		want    bool
	}{
		{"order.total_cents", "total_cents", true},
		{"order.total_cents_v2", "total_cents", false},
		{"$total_cents", "total_cents", false},
		{"x total y total", "total", true},
		{"totals subtotal", "total", false},
		{"", "a", false},
	}
	for _, tt := range tests {
		if got := hasWord(tt.s, tt.name); got != tt.want {
			t.Errorf("hasWord(%q, %q) = %v", tt.s, tt.name, got)
		}
	}
}
