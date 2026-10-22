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
		many           []string
		found          bool
	}{
		{"single", core.Npm, "node-sass", "sass", nil, true},
		{"single go", core.Go, "github.com/dgrijalva/jwt-go", "github.com/golang-jwt/jwt/v5", nil, true},
		{"several", core.Npm, "moment", "", []string{"dayjs", "luxon", "date-fns"}, true},
		{"several request", core.Npm, "request", "", []string{"undici", "node-fetch", "axios"}, true},
		{"other ecosystem same name", core.Pip, "request", "", nil, false},
		{"unknown", core.Npm, "left-pad", "", nil, false},
		{"glob row is never matched", core.Npm, "@types/*", "", nil, false},
		{"glob query", core.Npm, "@types/node", "", nil, false},
		{"note only", core.Nuget, "microsoft.aspnet.webapi.client", "", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			one, many, ok := Replacement(tc.eco, tc.pkg)
			if ok != tc.found {
				t.Fatalf("found = %v, want %v", ok, tc.found)
			}
			switch {
			case tc.one != "":
				if one == nil || many != nil || one.ToName != tc.one || !one.Safe {
					t.Fatalf("one = %+v, many = %+v; want a safe swap to %s", one, many, tc.one)
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
