package api

import (
	"slices"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
)

func TestDetect(t *testing.T) {
	cat := append(Catalog(), Provider{Name: "fixture", Hosts: []string{"api.fixture.dev", "localhost:8080"}})
	inv := &core.Inventory{
		Deps: []core.Dep{
			{Ecosystem: core.Npm, Name: "stripe", Version: "14.2.0"},
			{Ecosystem: core.Npm, Name: "lodash", Version: "4.17.21"},
			{Ecosystem: core.Go, Name: "github.com/stripe/stripe-go/v79", Version: "v79.1.0"},
			{Ecosystem: core.Go, Name: "github.com/stripe/stripe-gofer", Version: "v1.0.0"},
			{Ecosystem: core.Pip, Name: "pygithub", Version: "2.1.1"},
			{Ecosystem: core.Nuget, Name: "Stripe.net", Version: "45.0.0"},
			{Ecosystem: core.Pip, Name: "openai-agents", Version: "0.1.0"},
			{Ecosystem: core.Npm, Name: "twilio-helpers", Version: "1.0.0"},
		},
		Index: &index{
			uses: map[string][]core.Site{
				"stripe":                          {call("src/pay.js", 3, "stripe.charges.create")},
				"github.com/stripe/stripe-go/v79": {call("pay/charge.go", 9, "charge.New")},
				"lodash":                          {call("src/pay.js", 1, "lodash.map")},
			},
			urls: []core.URLUse{
				{File: "app/orders.py", Line: 8, URL: "https://api.fixture.dev/v1/orders"},
				{File: "app/local.py", Line: 2, URL: "http://localhost:8080/v1/orders"},
				{File: "app/orders.py", Line: 20, URL: "/v1/orders/{id}", Host: "api.fixture.dev"},
				{File: "src/gh.js", Line: 4, URL: "https://api.github.com/repos/a/b"},
				{File: "src/other.js", Line: 4, URL: "https://example.com/v1/orders"},
				{File: "test/twilio.test.js", Line: 7, URL: "https://api.twilio.com/2010-04-01/Accounts"},
				{File: "tests/test_orders.py", Line: 9, URL: "https://api.fixture.dev/v1/orders"},
			},
		},
	}
	got := Detect(inv, cat)
	var names []string
	for _, u := range got {
		names = append(names, u.Provider)
	}
	// twilio shows up only in a test's fixture, which is not a call.
	if !slices.Equal(names, []string{"stripe", "github", "fixture"}) {
		t.Fatalf("providers = %q", names)
	}

	stripe := got[0]
	var sdks []string
	for _, d := range stripe.SDK {
		sdks = append(sdks, d.Ecosystem+":"+d.Name)
	}
	if !slices.Equal(sdks, []string{"npm:stripe", "go:github.com/stripe/stripe-go/v79", "nuget:Stripe.net"}) {
		t.Errorf("stripe sdks = %q", sdks)
	}
	if len(stripe.Sites) != 2 || len(stripe.URLs) != 0 || !slices.Equal(stripe.Hosts, []string{"api.stripe.com"}) {
		t.Errorf("stripe = %+v", stripe)
	}

	github := got[1]
	if len(github.SDK) != 1 || github.SDK[0].Name != "pygithub" || len(github.URLs) != 1 {
		t.Errorf("github = %+v", github)
	}

	fixture := got[2]
	if len(fixture.SDK) != 0 || len(fixture.Sites) != 0 || len(fixture.URLs) != 3 {
		t.Errorf("fixture = %+v", fixture)
	}
}

func TestDetectNothing(t *testing.T) {
	if got := Detect(&core.Inventory{}, Catalog()); got != nil {
		t.Errorf("Detect = %+v", got)
	}
}

func TestCanon(t *testing.T) {
	tests := []struct{ eco, name, want string }{
		{core.Pip, "PyGithub", "pygithub"},
		{core.Pip, "slack_sdk", "slack-sdk"},
		{core.Pip, "Foo.Bar__baz", "foo-bar-baz"},
		{core.Nuget, "Stripe.net", "stripe.net"},
		{core.Composer, "Stripe/Stripe-PHP", "stripe/stripe-php"},
		{core.Npm, "@octokit/rest", "@octokit/rest"},
		{core.Maven, "com.stripe:stripe-java", "com.stripe:stripe-java"},
	}
	for _, tt := range tests {
		if got := canon(tt.eco, tt.name); got != tt.want {
			t.Errorf("canon(%s, %s) = %q, want %q", tt.eco, tt.name, got, tt.want)
		}
	}
}
