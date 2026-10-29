package source

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/mohsensc/tend-cli/internal/core"
)

func TestReplacementsTable(t *testing.T) {
	var rows []replacement
	if err := yaml.Unmarshal(replacements, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) < 20 {
		t.Fatalf("%d rows, want the starter set", len(rows))
	}
	seen := map[string]bool{}
	for _, r := range rows {
		key := r.Eco + "/" + r.Name
		if r.Eco == "" || r.Name == "" {
			t.Errorf("row without eco or name: %+v", r)
		}
		if seen[key] {
			t.Errorf("%s listed twice", key)
		}
		seen[key] = true
		if r.To == "" && len(r.Options) == 0 && r.Note == "" {
			t.Errorf("%s says nothing about what to do", key)
		}
		if r.To != "" && len(r.Options) > 0 {
			t.Errorf("%s has both to and options", key)
		}
		if r.URL != "" && !strings.HasPrefix(r.URL, "https://") {
			t.Errorf("%s: url %q is not https", key, r.URL)
		}
		if r.To == "" && len(r.Options) > 0 && len(r.Options) < 2 {
			t.Errorf("%s: one option should be written as to", key)
		}
		for _, o := range r.Options {
			if o.To == "" || o.Why == "" {
				t.Errorf("%s: option %+v needs to and why: a maintainer picks between them", key, o)
			}
		}
	}
}

func TestReplacement(t *testing.T) {
	tests := []struct {
		name, eco, pkg string
		one            string
		safe           bool
		many           []string
		found          bool
	}{
		{"single", core.Npm, "node-sass", "sass", false, nil, true},
		{"single go", core.Go, "github.com/dgrijalva/jwt-go", "github.com/golang-jwt/jwt/v5", false, nil, true},
		{"drop-in", core.Pip, "pycrypto", "pycryptodome", true, nil, true},
		{"pip name as written", core.Pip, "PyCrypto", "pycryptodome", true, nil, true},
		{"pip name in capitals", core.Pip, "SKLearn", "scikit-learn", true, nil, true},
		{"composer case", core.Composer, "Fzaninotto/Faker", "fakerphp/faker", true, nil, true},
		{"new api", core.Pip, "nose", "pytest", false, nil, true},
		{"npm is case sensitive", core.Npm, "Node-Sass", "", false, nil, false},
		{"several", core.Npm, "moment", "", false, []string{"dayjs", "luxon", "date-fns"}, true},
		{"several request", core.Npm, "request", "", false, []string{"undici", "node-fetch", "axios"}, true},
		{"other ecosystem same name", core.Pip, "request", "", false, nil, false},
		{"unknown", core.Npm, "left-pad", "", false, nil, false},
		{"glob row is never matched", core.Npm, "@types/*", "", false, nil, false},
		{"glob query", core.Npm, "@types/node", "", false, nil, false},
		{"note only", core.Nuget, "microsoft.aspnet.webapi.client", "", false, nil, false},
		{"note only as nuget writes it", core.Nuget, "Microsoft.AspNet.WebApi.Client", "", false, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			one, many, ok := Replacement(tc.eco, tc.pkg)
			if ok != tc.found {
				t.Fatalf("found = %v, want %v", ok, tc.found)
			}
			switch {
			case tc.one != "":
				if one == nil || many != nil || one.ToName != tc.one || one.Safe != tc.safe {
					t.Fatalf("one = %+v, many = %+v; want a swap to %s, safe %v", one, many, tc.one, tc.safe)
				}
			case tc.many != nil:
				var got []string
				for _, o := range many {
					got = append(got, o.ToName)
					if o.Safe {
						t.Errorf("%s: option %s is marked safe", tc.pkg, o.ToName)
					}
				}
				if one != nil || !reflect.DeepEqual(got, tc.many) {
					t.Fatalf("one = %+v, options = %v; want %v", one, got, tc.many)
				}
			default:
				if one != nil || many != nil {
					t.Fatalf("one = %+v, many = %+v; want nothing", one, many)
				}
			}
		})
	}
}

func TestSafeReplacementsKeepTheirImport(t *testing.T) {
	sameImport := map[string]string{
		"pip/pycrypto":               "Crypto",
		"pip/sklearn":                "sklearn",
		"composer/fzaninotto/faker":  `Faker\`,
		"actions/actions/setup-ruby": "ruby-version input",
	}
	var rows []replacement
	if err := yaml.Unmarshal(replacements, &rows); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		key := r.Eco + "/" + r.Name
		if r.Safe && r.To == "" {
			t.Errorf("%s is safe with nothing to switch to", key)
		}
		if _, ok := sameImport[key]; r.Safe != ok {
			t.Errorf("%s: safe = %v; a swap is safe only when code keeps working unchanged", key, r.Safe)
		}
	}
}

func TestReplacementDetail(t *testing.T) {
	one, _, _ := Replacement(core.Npm, "node-sass")
	if one.Detail != "https://sass-lang.com/blog/libsass-is-deprecated/" || one.Title != "Switch to sass" {
		t.Errorf("option = %+v", one)
	}
	_, many, _ := Replacement(core.Npm, "request")
	seen := map[string]bool{}
	for _, o := range many {
		if seen[o.Detail] || !strings.HasSuffix(o.Detail, " https://github.com/request/request/issues/3142") {
			t.Errorf("detail = %q, want its own trade-off and the link", o.Detail)
		}
		seen[o.Detail] = true
	}
	if len(many) == 0 || !strings.HasPrefix(many[2].Detail, "closest to request's options-object style") {
		t.Errorf("options = %+v", many)
	}
	gone, _, _ := Replacement(core.Go, "github.com/pkg/errors")
	if gone == nil || !strings.HasPrefix(gone.Detail, "stdlib errors since Go 1.13; archived https://") {
		t.Errorf("option = %+v", gone)
	}
}

// A way off that is no package is an option tend never takes itself.
func TestReplacementInstead(t *testing.T) {
	for _, c := range []struct{ eco, name, title string }{
		{core.Actions, "actions-rs/toolchain", "Switch to dtolnay/rust-toolchain"},
		{core.Cargo, "atty", "Use std::io::IsTerminal"},
	} {
		one, opts, ok := Replacement(c.eco, c.name)
		if !ok || one != nil || len(opts) != 1 || opts[0].Title != c.title || opts[0].ToName != "" || opts[0].Safe {
			t.Errorf("Replacement(%s) = %+v, %+v, %v", c.name, one, opts, ok)
		}
	}
	if one, _, ok := Replacement(core.Cargo, "term_size"); !ok || one == nil || one.ToName != "terminal_size" {
		t.Errorf("term_size = %+v", one)
	}
}
