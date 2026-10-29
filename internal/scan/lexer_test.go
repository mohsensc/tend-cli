package scan

import (
	"fmt"
	"reflect"
	"testing"
)

func TestStrip(t *testing.T) {
	tests := []struct {
		name string
		syn  *syntax
		src  string
		code string
		lits []string // line:col:text
		bad  bool
	}{
		{
			name: "string inside a comment",
			syn:  goSyntax,
			src:  "a // \"x\"\nb",
			code: "a       \nb",
		},
		{
			name: "comment inside a string",
			syn:  goSyntax,
			src:  `s := "// not a comment" + x`,
			code: `s := "                " + x`,
			lits: []string{"1:7:// not a comment"},
		},
		{
			name: "escaped quotes",
			syn:  jsSyntax,
			src:  `x = "a\"b" + 'c\'d'`,
			code: `x = "    " + '    '`,
			lits: []string{`1:6:a\"b`, `1:15:c\'d`},
		},
		{
			name: "template literal keeps its expressions as code",
			syn:  jsSyntax,
			src:  "u = `/v1/${id}/x` + z",
			code: "u = `    ${id}  ` + z",
			lits: []string{"1:6:/v1/${id}/x"},
		},
		{
			name: "template literal nested in an expression",
			syn:  jsSyntax,
			src:  "`a${f(`b${c}`)}d`",
			code: "` ${f(` ${c}`)} `",
			lits: []string{"1:2:a${f(`b${c}`)}d", "1:8:b${c}"},
		},
		{
			name: "regex after = is not a string",
			syn:  jsSyntax,
			src:  `x = /"/.test(s) // c`,
			code: `x =    .test(s)     `,
		},
		{
			name: "division is not a regex",
			syn:  jsSyntax,
			src:  `a = b / c / "d"`,
			code: `a = b / c / " "`,
			lits: []string{"1:14:d"},
		},
		{
			name: "regex after an arrow",
			syn:  jsSyntax,
			src:  `f = u => /^https?:\/\//.test(u) && g()`,
			code: `f = u =>               .test(u) && g()`,
		},
		{
			name: "regex after return",
			syn:  jsSyntax,
			src:  "return /`/.test(c)\nb = 'x'",
			code: "return    .test(c)\nb = ' '",
			lits: []string{"2:6:x"},
		},
		{
			name: "regex after typeof",
			syn:  jsSyntax,
			src:  `if (typeof /a/ === 'x')`,
			code: `if (typeof     === ' ')`,
			lits: []string{"1:21:x"},
		},
		{
			name: "regex after a keyword on the line before",
			syn:  jsSyntax,
			src:  "yield /* c */\n  /'/g",
			code: "yield        \n      ",
		},
		{
			name: "property named like a keyword is division",
			syn:  jsSyntax,
			src:  `x.return / 2 / "d"`,
			code: `x.return / 2 / " "`,
			lits: []string{"1:17:d"},
		},
		{
			name: "division after increment",
			syn:  jsSyntax,
			src:  `a++ / b / "d"`,
			code: `a++ / b / " "`,
			lits: []string{"1:12:d"},
		},
		{
			name: "regex after a binary plus",
			syn:  jsSyntax,
			src:  `s = a + /'/.source`,
			code: `s = a +    .source`,
		},
		{
			name: "division after a closing paren",
			syn:  jsSyntax,
			src:  `m = (a + b) / 2 // "c"`,
			code: `m = (a + b) / 2       `,
		},
		{
			name: "ruby regex after if",
			syn:  rubySyntax,
			src:  "x if /'/ =~ s\ny = 'z'",
			code: "x if     =~ s\ny = ' '",
			lits: []string{"2:6:z"},
		},
		{
			name: "ruby keywords are names in js",
			syn:  jsSyntax,
			src:  `r = not / 2 / "d"`,
			code: `r = not / 2 / " "`,
			lits: []string{"1:16:d"},
		},
		{
			name: "apostrophe in jsx text",
			syn:  jsSyntax,
			src:  "<p>Don't {a.b}</p>\nc = 'x'",
			code: "<p>Don't {a.b}</p>\nc = ' '",
			lits: []string{"2:6:x"},
		},
		{
			name: "python triple quotes",
			syn:  pySyntax,
			src:  "x = \"\"\"a\n\"b\" # no\n\"\"\" + y",
			code: "x = \"\"\" \n        \n\"\"\" + y",
			lits: []string{"1:8:a\n\"b\" # no\n"},
		},
		{
			name: "python f-string and comment",
			syn:  pySyntax,
			src:  `s = f"{a.b}/x" # "c"`,
			code: `s = f"       "      `,
			lits: []string{"1:7:{a.b}/x"},
		},
		{
			name: "ruby percent literals",
			syn:  rubySyntax,
			src:  "a = %w[x y] + %q(it's (fine)) + b % c",
			code: "a = %w[   ] + %q(           ) + b % c",
			lits: []string{"1:8:x y", "1:18:it's (fine)"},
		},
		{
			name: "ruby heredoc",
			syn:  rubySyntax,
			src:  "q = <<~SQL\n  select 'x'\n  SQL\nb",
			code: "q =       \n            \n     \nb",
			lits: []string{"2:1:  select 'x'"},
		},
		{
			name: "ruby block comment and shift",
			syn:  rubySyntax,
			src:  "=begin\n'x\n=end\na << b",
			code: "      \n  \n    \na << b",
		},
		{
			name: "go raw string with //",
			syn:  goSyntax,
			src:  "s := `http://x // y`\nz",
			code: "s := `             `\nz",
			lits: []string{"1:7:http://x // y"},
		},
		{
			name: "go rune",
			syn:  goSyntax,
			src:  `r := '"'`,
			code: `r := ' '`,
			lits: []string{`1:7:"`},
		},
		{
			name: "rust nested block comment",
			syn:  rustSyntax,
			src:  "a /* b /* c */ d */ e",
			code: "a                   e",
		},
		{
			name: "rust lifetimes and chars",
			syn:  rustSyntax,
			src:  `fn f<'a>(x: &'a str) -> char { '\'' }`,
			code: `fn f<'a>(x: &'a str) -> char { '  ' }`,
			lits: []string{`1:33:\'`},
		},
		{
			name: "rust raw string",
			syn:  rustSyntax,
			src:  `s = r#"a "q" b"#;`,
			code: `s = r#"       "#;`,
			lits: []string{`1:8:a "q" b`},
		},
		{
			name: "c# verbatim string with doubled quotes",
			syn:  csSyntax,
			src:  `s = @"C:\dir\""x"""; t = $"{a}"`,
			code: `s = @"            "; t = $"   "`,
			lits: []string{`1:7:C:\dir\""x""`, "1:28:{a}"},
		},
		{
			name: "c# raw string",
			syn:  csSyntax,
			src:  `s = """a "b" c""";`,
			code: `s = """       """;`,
			lits: []string{`1:8:a "b" c`},
		},
		{
			name: "java text block",
			syn:  javaSyntax,
			src:  "s = \"\"\"\n  {\"a\": 1}\n  \"\"\";",
			code: "s = \"\"\"\n          \n  \"\"\";",
			lits: []string{"1:8:\n  {\"a\": 1}\n  "},
		},
		{
			name: "php outside tags, heredoc and nowdoc",
			syn:  phpSyntax,
			src:  "<p>it's</p>\n<?php\n$a = <<<EOT\nhi 'x'\nEOT;\n$b = <<<'N'\n$y\n  N;\n# 'c'\n#[Attr]\n?>\n<b>'</b>",
			code: "           \n     \n$a =       \n      \n   ;\n$b =       \n  \n   ;\n     \n#[Attr]\n  \n        ",
			lits: []string{"4:1:hi 'x'", "7:1:$y"},
		},
		{
			name: "php line comment ends at the close tag",
			syn:  phpSyntax,
			src:  "<?php // x ?>\n<p>it's</p>\n<?php $u = 'https://api.x.com/v1/h'; # y ?><i>'</i>",
			code: "             \n           \n      $u = '                      ';               ",
			lits: []string{"3:13:https://api.x.com/v1/h"},
		},
		{
			name: "crlf",
			syn:  pySyntax,
			src:  "a = 'x'\r\nb = 'y' # z\r\n",
			code: "a = ' '\r\nb = ' '     \n",
			lits: []string{"1:6:x", "2:6:y"},
		},
		{
			name: "bom",
			syn:  goSyntax,
			src:  "\xef\xbb\xbfpackage a // x",
			code: "   package a     ",
		},
		{
			name: "unterminated template leaves the rest as code",
			syn:  jsSyntax,
			src:  "a = 'x'; b = `y\nstripe.c()",
			code: "a = ' '; b = `y\nstripe.c()",
			lits: []string{"1:6:x"},
			bad:  true,
		},
		{
			name: "unterminated block comment",
			syn:  javaSyntax,
			src:  "a /* b",
			code: "a /* b",
			bad:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, lits, ok := strip([]byte(tt.src), tt.syn)
			if string(code) != tt.code {
				t.Errorf("code\n got %q\nwant %q", code, tt.code)
			}
			if len(code) != len(tt.src) {
				t.Errorf("code is %d bytes, source %d", len(code), len(tt.src))
			}
			var got []string
			for _, l := range lits {
				got = append(got, fmt.Sprintf("%d:%d:%s", l.Line, l.Col, l.Text))
			}
			if !reflect.DeepEqual(got, tt.lits) {
				t.Errorf("lits\n got %q\nwant %q", got, tt.lits)
			}
			if ok == tt.bad {
				t.Errorf("ok = %v", ok)
			}
		})
	}
}

func TestPos(t *testing.T) {
	nl := newlines([]byte("ab\n\tc\n\nd"))
	tests := []struct{ off, line, col int }{
		{0, 1, 1}, {2, 1, 3}, {3, 2, 1}, {4, 2, 2}, {6, 3, 1}, {7, 4, 1},
	}
	for _, tt := range tests {
		if line, col := pos(nl, tt.off); line != tt.line || col != tt.col {
			t.Errorf("pos(%d) = %d:%d, want %d:%d", tt.off, line, col, tt.line, tt.col)
		}
	}
}
