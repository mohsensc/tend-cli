package deliver

import (
	"slices"
	"strings"
	"testing"

	"github.com/mohsensc/tend-cli/internal/core"
)

func dep(eco, name, version string) core.Dep {
	return core.Dep{Ecosystem: eco, Name: name, Version: version, Direct: true}
}

func advisory(source, id, summary string) core.Evidence {
	return core.Evidence{Source: source, Title: id + " " + summary}
}

// A dep only the lockfile names that landed past the release asked for is
// titled with the release it landed on; its branch keeps the target.
func TestTitleLanded(t *testing.T) {
	ch := &core.Change{ID: "npm/tar@7.5.21", Kind: core.Security, Deps: []core.Dep{{Ecosystem: "npm", Name: "tar", Version: "7.5.20"}}, To: "7.5.21",
		Evidence: []core.Evidence{advisory("osv", "GHSA-8qq5-rm4j-mr97", "file overwrite")}}
	in := BodyInput{Change: ch, Patch: &core.Patch{Landed: map[string]string{"npm/tar": "7.5.22"}}}
	if got := Title(in, ""); got != "Bump tar to 7.5.22 (fixes GHSA-8qq5-rm4j-mr97)" {
		t.Errorf("title = %q", got)
	}
	if got := Branch(ch); got != "tend/npm-tar-7.5.21" {
		t.Errorf("branch = %q", got)
	}
	ch.Deps[0].Manifest = core.Manifest{Path: "package.json", Lock: "package-lock.json", Tool: "npm"}
	in.Patch.Files = []string{"package-lock.json"}
	body := Body(in)
	for _, want := range []string{
		"## tar 7.5.20 → 7.5.22",
		"fixed in 7.5.21",
		"tend asked for 7.5.21; `package-lock.json` names `tar` only as a dependency of other packages, and `npm` moves such a dependency to the newest release their ranges allow, 7.5.22.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
}

func TestTitleAndBranch(t *testing.T) {
	first := func(b core.Batch) core.Change {
		return core.Change{ID: core.FirstID, Kind: core.First, Deps: []core.Dep{dep("npm", "left-pad", "1.3.0")}, Batch: &b}
	}
	sec := core.Change{Kind: core.Security, Deps: []core.Dep{dep("npm", "a", "1.0.0")}}
	lib := core.Change{Kind: core.Deprecation, Deps: []core.Dep{dep("npm", "a", "1.0.0")}}
	pin := core.Change{Kind: core.Deprecation, Deps: []core.Dep{{Ecosystem: core.Toolchain, Name: "nodejs"}}}
	act := core.Change{Kind: core.Deprecation, Deps: []core.Dep{{Ecosystem: core.Actions, Name: "actions/checkout"}}}
	api := core.Change{Kind: core.Breaking, API: &core.APIChange{Provider: "stripe"}}
	two := []core.Dep{dep("npm", "a", "1.0.0"), dep("npm", "b", "1.0.0")}
	var twelve []core.Change
	for range 12 {
		twelve = append(twelve, sec)
	}
	tests := []struct {
		name   string
		ch     core.Change
		prefix string
		title  string
		branch string
	}{
		{
			name:   "first PR",
			ch:     first(core.Batch{Unused: append(append(two, two...), two...), Changes: append(slices.Clone(twelve), lib)}),
			title:  "Catch up: 12 security fixes, 1 deprecated library, 6 unused dependencies",
			branch: "tend/tidy-up",
		},
		{
			name:   "first PR that removes one",
			ch:     first(core.Batch{Unused: two[:1]}),
			prefix: "chore: ",
			title:  "chore: Catch up: 1 unused dependency",
			branch: "tend/tidy-up",
		},
		{
			name:   "first PR of every kind, in the order of its sections",
			ch:     first(core.Batch{Changes: []core.Change{act, api, sec}}),
			title:  "Catch up: 1 security fix, 1 API change, 1 deprecated Action",
			branch: "tend/tidy-up",
		},
		{
			name:   "first PR whose kinds make a long title",
			ch:     first(core.Batch{Changes: []core.Change{pin, lib, api, sec, act}, Unused: two}),
			title:  "Catch up: 1 security fix, 1 API change, 3 deprecations, 2 unused dependencies",
			branch: "tend/tidy-up",
		},
		{
			name:   "nothing to catch up on",
			ch:     first(core.Batch{}),
			title:  "Set up tend: nothing to catch up on",
			branch: "tend/tidy-up",
		},
		{
			name:   "nothing made, things to look at",
			ch:     first(core.Batch{Kept: []core.Aside{{}}}),
			title:  "Set up tend",
			branch: "tend/tidy-up",
		},
		{
			name:   "update",
			ch:     core.Change{Kind: core.Update, Deps: []core.Dep{dep("npm", "stripe", "16.0.0")}, To: "16.1.0"},
			title:  "Bump stripe to 16.1.0",
			branch: "tend/npm-stripe-16.1.0",
		},
		{
			name:   "breaking bump",
			ch:     core.Change{Kind: core.Breaking, Level: "major", Deps: []core.Dep{dep("npm", "stripe", "14.2.0")}, To: "16.0.0"},
			title:  "Bump stripe to 16.0.0",
			branch: "tend/npm-stripe-16.0.0",
		},
		{
			name: "security with one advisory",
			ch: core.Change{Kind: core.Security, Deps: []core.Dep{dep("npm", "stripe", "14.2.0")}, To: "16.0.1",
				Evidence: []core.Evidence{advisory("osv", "GHSA-xxxx-yyyy-zzzz", "bad")}},
			title:  "Bump stripe to 16.0.1 (fixes GHSA-xxxx-yyyy-zzzz)",
			branch: "tend/npm-stripe-16.0.1",
		},
		{
			name: "security with three advisories, one repeated",
			ch: core.Change{Kind: core.Security, Deps: []core.Dep{dep("npm", "stripe", "14.2.0")}, To: "16.0.1",
				Evidence: []core.Evidence{
					advisory("osv", "GHSA-1", "a"), advisory("osv", "GHSA-1", "a"),
					advisory("osv", "GHSA-2", "b"), advisory("osv", "GHSA-3", "c"),
					{Source: "releases", Title: "16.0.1"},
				}},
			title:  "Bump stripe to 16.0.1 (fixes GHSA-1 +2 more)",
			branch: "tend/npm-stripe-16.0.1",
		},
		{
			name:   "security without ids",
			ch:     core.Change{Kind: core.Security, Deps: []core.Dep{dep("go", "golang.org/x/net", "v0.1.0")}, To: "v0.2.0"},
			title:  "Bump golang.org/x/net to v0.2.0",
			branch: "tend/go-golang.org-x-net-v0.2.0",
		},
		{
			name:   "swap",
			ch:     core.Change{Kind: core.Deprecation, Deps: []core.Dep{dep("npm", "request", "2.88.2")}, To: "6.19.8", ToName: "undici"},
			title:  "Replace request with undici",
			branch: "tend/npm-request-to-undici-6.19.8",
		},
		{
			name:   "deprecation with version target",
			ch:     core.Change{Kind: core.Deprecation, Deps: []core.Dep{dep("npm", "stripe", "14.2.0")}, To: "16.0.0"},
			title:  "Migrate off deprecated stripe 14.x",
			branch: "tend/npm-stripe-16.0.0",
		},
		{
			name:   "field named in a changelog without its endpoint",
			ch:     core.Change{Kind: core.Deprecation, API: &core.APIChange{Provider: "orders", Field: "total_cents", What: "response field deprecated"}},
			title:  "Migrate off deprecated field total_cents of the orders API",
			branch: "tend/api-orders-total-cents",
		},
		{
			name:   "runtime pin",
			ch:     core.Change{Kind: core.Deprecation, Deps: []core.Dep{dep("toolchain", "nodejs", "16.20.2")}, To: "24.22.0"},
			title:  "Move nodejs to 24.22.0",
			branch: "tend/toolchain-nodejs-24.22.0",
		},
		{
			name:   "runtime minimum",
			ch:     core.Change{Kind: core.Deprecation, Deps: []core.Dep{{Ecosystem: "toolchain", Name: "go", Version: "1.24", Min: true}}, To: "1.26"},
			title:  "Require go 1.26 or later",
			branch: "tend/toolchain-go-1.26",
		},
		{
			name:   "deprecation with unparsable version",
			ch:     core.Change{Kind: core.Deprecation, Deps: []core.Dep{dep("npm", "stripe", "")}, To: "16.0.0"},
			title:  "Migrate off deprecated stripe",
			branch: "tend/npm-stripe-16.0.0",
		},
		{
			name:   "api deprecation",
			ch:     core.Change{Kind: core.Deprecation, API: &core.APIChange{Provider: "fixture", Method: "GET", Path: "/v1/orders/{id}/legacy", What: "operation deprecated"}},
			title:  "Migrate off deprecated fixture GET /v1/orders/{id}/legacy",
			branch: "tend/api-fixture-get-v1-orders-id-legacy",
		},
		{
			name:   "api deprecated field",
			ch:     core.Change{Kind: core.Deprecation, API: &core.APIChange{Provider: "fixture", Method: "get", Path: "/v1/orders", Field: "total", What: "response field deprecated"}},
			title:  "Migrate off deprecated field total on fixture GET /v1/orders",
			branch: "tend/api-fixture-get-v1-orders-total",
		},
		{
			name:   "api field removed",
			ch:     core.Change{Kind: core.Breaking, API: &core.APIChange{Provider: "fixture", Method: "GET", Path: "/v1/orders", Field: "total_cents", What: "response field removed"}},
			title:  "Handle removed field total_cents on fixture GET /v1/orders",
			branch: "tend/api-fixture-get-v1-orders-total-cents",
		},
		{
			name:   "api field type changed",
			ch:     core.Change{Kind: core.Breaking, API: &core.APIChange{Provider: "fixture", Method: "GET", Path: "/v1/orders", Field: "total_cents", What: "response field type changed"}},
			title:  "Handle changed type of field total_cents on fixture GET /v1/orders",
			branch: "tend/api-fixture-get-v1-orders-total-cents",
		},
		{
			name:   "api operation removed",
			ch:     core.Change{Kind: core.Breaking, API: &core.APIChange{Provider: "fixture", Method: "DELETE", Path: "/v1/orders/{id}", What: "operation removed"}},
			title:  "Handle removed fixture DELETE /v1/orders/{id}",
			branch: "tend/api-fixture-delete-v1-orders-id",
		},
		{
			name:   "api parameter removed",
			ch:     core.Change{Kind: core.Breaking, API: &core.APIChange{Provider: "fixture", Method: "GET", Path: "/v1/orders", Field: "expand", What: "parameter removed"}},
			title:  "Handle removed parameter expand on fixture GET /v1/orders",
			branch: "tend/api-fixture-get-v1-orders-expand",
		},
		{
			name:   "api required parameter added",
			ch:     core.Change{Kind: core.Breaking, API: &core.APIChange{Provider: "fixture", Method: "POST", Path: "/v1/orders", Field: "currency", What: "required parameter added"}},
			title:  "Handle new required parameter currency on fixture POST /v1/orders",
			branch: "tend/api-fixture-post-v1-orders-currency",
		},
		{
			name:   "docker",
			ch:     core.Change{Kind: core.Deprecation, Deps: []core.Dep{dep("docker", "library/node", "16-alpine")}, To: "22-alpine"},
			title:  "Bump node base image to 22-alpine",
			branch: "tend/docker-library-node-22-alpine",
		},
		{
			name:   "actions",
			ch:     core.Change{Kind: core.Deprecation, Deps: []core.Dep{dep("actions", "actions/checkout", "v2")}, To: "v4"},
			title:  "Bump actions/checkout to v4",
			branch: "tend/actions-actions-checkout-v4",
		},
		{
			name:   "decision",
			ch:     core.Change{Kind: core.Deprecation, Deps: []core.Dep{dep("npm", "moment", "2.30.1")}, Options: []core.Option{{Title: "dayjs"}}},
			title:  "Decide: migrate off moment",
			branch: "tend/npm-moment-decide",
		},
		{
			name: "group",
			ch: core.Change{ID: "npm/group:aws-sdk@3.700.0", Kind: core.Update,
				Deps: []core.Dep{dep("npm", "@aws-sdk/client-s3", "3.600.0"), dep("npm", "@aws-sdk/client-sqs", "3.600.0")}, To: "3.700.0"},
			title:  "Bump aws-sdk packages to 3.700.0",
			branch: "tend/npm-group-aws-sdk-3.700.0",
		},
		{
			name: "security fix with a package that moves with it",
			ch: core.Change{ID: "npm/group:vitest@3.2.5", Kind: core.Security,
				Deps: []core.Dep{dep("npm", "vitest", "3.2.4"), dep("npm", "@vitest/coverage-v8", "3.2.4")}, To: "3.2.5",
				Evidence: []core.Evidence{{Source: "osv", Title: "GHSA-aaaa-bbbb-cccc"}}},
			title:  "Bump vitest packages to 3.2.5 (fixes GHSA-aaaa-bbbb-cccc)",
			branch: "tend/npm-group-vitest-3.2.5",
		},
		{
			name:   "prefix is verbatim",
			ch:     core.Change{Kind: core.Update, Deps: []core.Dep{dep("npm", "left-pad", "1.0.0")}, To: "1.1.0"},
			prefix: "chore(deps): ",
			title:  "chore(deps): Bump left-pad to 1.1.0",
			branch: "tend/npm-left-pad-1.1.0",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Title(BodyInput{Change: &tc.ch}, tc.prefix); got != tc.title {
				t.Errorf("Title = %q, want %q", got, tc.title)
			}
			if got := Branch(&tc.ch); got != tc.branch {
				t.Errorf("Branch = %q, want %q", got, tc.branch)
			}
		})
	}
}

func TestBranchLength(t *testing.T) {
	long := strings.Repeat("very-long-name-", 10)
	ch := core.Change{Deps: []core.Dep{dep("npm", long, "1.0.0")}, To: "1.2.3-beta.4+build.5"}
	got := Branch(&ch)
	if len(got) > 100 {
		t.Fatalf("branch is %d chars: %s", len(got), got)
	}
	if !strings.HasSuffix(got, "-1.2.3-beta.4-build.5") {
		t.Errorf("the target version was cut: %s", got)
	}

	api := core.Change{API: &core.APIChange{Provider: strings.Repeat("p", 60), Method: "GET", Path: "/v1/" + strings.Repeat("segment/", 30)}}
	got = Branch(&api)
	if len(got) > 100 || strings.HasSuffix(got, "-") || strings.HasSuffix(got, ".") {
		t.Errorf("api branch = %q (%d)", got, len(got))
	}
}

// Changes whose names share a long start get branches of their own.
func TestBranchesStayApart(t *testing.T) {
	op := func(field string) core.Change {
		return core.Change{API: &core.APIChange{Provider: "stripe", Method: "POST", Path: "/v1/test_helpers/issuing/authorizations/{authorization}/finalize_amount", Field: field}}
	}
	azure := "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/"
	pairs := [][2]core.Change{
		{op("fleet.cardholder_prompt_data"), op("fuel.unit_cost_decimal")},
		{
			{Deps: []core.Dep{dep("go", azure+"armcontainerservice/v4", "1.0.0")}, To: "1.2.0"},
			{Deps: []core.Dep{dep("go", azure+"armcontainerservicefleet", "1.0.0")}, To: "1.2.0"},
		},
	}
	for _, p := range pairs {
		a, b := Branch(&p[0]), Branch(&p[1])
		if a == b || len(a) > 100 || len(b) > 100 {
			t.Errorf("branches %q and %q", a, b)
		}
		if a != Branch(&p[0]) {
			t.Errorf("branch %q is not stable", a)
		}
	}
}

func TestMessage(t *testing.T) {
	ch := core.Change{Deps: []core.Dep{dep("npm", "stripe", "14.2.0")}, To: "16.1.0"}
	p := &core.Patch{Applied: []string{"bumped stripe 14.2.0 -> 16.1.0 (package.json)", "renamed 2 call sites"}}
	want := "Bump stripe to 16.1.0\n\nbumped stripe 14.2.0 -> 16.1.0 (package.json)\nrenamed 2 call sites"
	if got := Message(BodyInput{Change: &ch, Patch: p}, ""); got != want {
		t.Errorf("Message = %q", got)
	}
	if got := Message(BodyInput{Change: &ch}, "x: "); got != "x: Bump stripe to 16.1.0" {
		t.Errorf("Message without a patch = %q", got)
	}
}

// A decision PR asks, and its commit is empty, so its message only asks too;
// a picked pin says it pins.
func TestTitleOfWhatThePRCarries(t *testing.T) {
	pay := dep("npm", "@fixture/pay", "1.4.0")
	ch := core.Change{
		Kind: core.Deprecation, Level: "major", Deps: []core.Dep{pay}, To: "2.0.0",
		Options: []core.Option{
			{Title: "Apply the rename", To: "2.0.0", Codemod: &core.Codemod{Renames: map[string]string{"charges.create": "paymentIntents.create"}}},
			{Title: "Pin to 1.4.0 and ignore", To: "1.4.0"},
		},
	}
	decision := &core.Patch{Decision: true, Applied: []string{"no mechanical fix; options listed"}}
	tests := []struct {
		in   BodyInput
		want string
	}{
		{BodyInput{Change: &ch}, "Migrate off deprecated @fixture/pay 1.x"},
		{BodyInput{Change: &ch, Patch: decision}, "Decide: migrate off @fixture/pay 1.x"},
		{BodyInput{Change: &ch, Patch: &core.Patch{}, Option: 1}, "Migrate off deprecated @fixture/pay 1.x"},
		{BodyInput{Change: &ch, Patch: &core.Patch{}, Option: 2}, "Pin @fixture/pay to 1.4.0"},
		{BodyInput{Change: &core.Change{Kind: core.Deprecation, API: &core.APIChange{Provider: "fixture", Method: "GET", Path: "/v1/orders/{id}/legacy", What: "operation deprecated"}}, Patch: decision},
			"Migrate off deprecated fixture GET /v1/orders/{id}/legacy"},
	}
	for _, tt := range tests {
		if got := Title(tt.in, ""); got != tt.want {
			t.Errorf("Title = %q, want %q", got, tt.want)
		}
	}
	if got := Message(BodyInput{Change: &ch, Patch: decision}, ""); got != "Decide: migrate off @fixture/pay 1.x" {
		t.Errorf("Message of a decision = %q", got)
	}
}
