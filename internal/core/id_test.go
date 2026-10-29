package core

import "testing"

func dep(eco, name string) Dep { return Dep{Ecosystem: eco, Name: name} }

func TestChangeID(t *testing.T) {
	tests := []struct {
		name string
		ch   Change
		want string
	}{
		{"bump", Change{Deps: []Dep{dep(Npm, "stripe")}, To: "16.1.0"}, "npm/stripe@16.1.0"},
		{"swap", Change{Deps: []Dep{dep(Npm, "request")}, ToName: "undici", To: "6.19.8"}, "npm/request->undici@6.19.8"},
		{"group from id", Change{ID: "npm/group:aws-sdk@3.600.0", Deps: []Dep{dep(Npm, "@aws-sdk/client-s3"), dep(Npm, "@aws-sdk/client-sqs")}, To: "3.700.0"}, "npm/group:aws-sdk@3.700.0"},
		{"group derived", Change{Deps: []Dep{dep(Npm, "@aws-sdk/client-s3"), dep(Npm, "@aws-sdk/lib-storage")}, To: "3.700.0"}, "npm/group:aws-sdk@3.700.0"},
		{"group no prefix", Change{Deps: []Dep{dep(Go, "golang.org/x/net"), dep(Go, "rsc.io/quote")}, To: "1.0.0"}, "go/group:golang.org/x/net@1.0.0"},
		{"api", Change{API: &APIChange{Provider: "stripe", Method: "POST", Path: "/v1/charges"}}, "api/stripe/POST /v1/charges"},
		{"api field", Change{API: &APIChange{Provider: "fixture", Method: "get", Path: "/v1/orders", Field: "total_cents"}}, "api/fixture/GET /v1/orders#total_cents"},
		{"docker", Change{Deps: []Dep{dep(Docker, "library/node")}, To: "22-alpine"}, "docker/library/node@22-alpine"},
		{"one image on two lines", Change{Deps: []Dep{{Ecosystem: Docker, Name: "library/node", Line: 1}, {Ecosystem: Docker, Name: "library/node", Line: 7}}, To: "24-alpine"}, "docker/library/node@24-alpine"},
		{"one package in two manifests", Change{Deps: []Dep{dep(Npm, "stripe"), dep(Npm, "stripe")}, To: "16.1.0"}, "npm/stripe@16.1.0"},
		{"no deps keeps id", Change{ID: "npm/moment@"}, "npm/moment@"},
		{"the first PR whatever its deps", Change{Kind: First, Deps: []Dep{dep(Npm, "left-pad")}}, "tidy-up"},
		{"a later stage", Change{ID: StageID(3), Kind: First}, "catch-up-3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ChangeID(&tt.ch); got != tt.want {
				t.Errorf("ChangeID = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSlug(t *testing.T) {
	tests := []struct{ in, want string }{
		{"stripe", "stripe"},
		{"@aws-sdk/client-s3", "aws-sdk-client-s3"},
		{"POST /v1/charges", "post-v1-charges"},
		{"github.com/Stripe/stripe-go/v79", "github.com-stripe-stripe-go-v79"},
		{"16.1.0", "16.1.0"},
		{"v1..2", "v1.2"},
		{"a___b", "a-b"},
		{"--x--", "x"},
		{".hidden", "hidden"},
		{"GET /v1/orders/{id}/legacy", "get-v1-orders-id-legacy"},
		{"", ""},
		{"日本", ""},
		{"0123456789abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz", "0123456789abcdefghijklmnopqrstuvwxyz0123456789abcde-b6034a6a"},
		{"01234567890123456789012345678901234567890123456789012345678-xyz", "012345678901234567890123456789012345678901234567890-6c86414c"},
	}
	for _, tt := range tests {
		if got := Slug(tt.in); got != tt.want {
			t.Errorf("Slug(%q) = %q, want %q", tt.in, got, tt.want)
		}
		if len(Slug(tt.in)) > 60 {
			t.Errorf("Slug(%q) longer than 60", tt.in)
		}
	}
}

func TestSubject(t *testing.T) {
	tests := []struct{ in, subject, target string }{
		{"npm/stripe@16.1.0", "npm/stripe", "16.1.0"},
		{"npm/@types/node@22.0.0", "npm/@types/node", "22.0.0"},
		{"npm/request->undici@6.19.8", "npm/request->undici", "6.19.8"},
		{"npm/group:aws-sdk@3.700.0", "npm/group:aws-sdk", "3.700.0"},
		{"api/stripe/POST /v1/charges", "api/stripe/POST /v1/charges", ""},
		{"api/gh/GET /users/{id}@x#email", "api/gh/GET /users/{id}@x#email", ""},
		{"npm/moment@", "npm/moment", ""},
		{"docker/library/node@22-alpine", "docker/library/node", "22-alpine"},
		{"no at sign", "no at sign", ""},
	}
	for _, tt := range tests {
		if got := Subject(tt.in); got != tt.subject {
			t.Errorf("Subject(%s) = %s", tt.in, got)
		}
		if got := TargetOf(tt.in); got != tt.target {
			t.Errorf("TargetOf(%s) = %q", tt.in, got)
		}
	}
}

func TestStageOf(t *testing.T) {
	for id, want := range map[string]int{"catch-up-2": 2, "catch-up-4": 4, FirstID: 0, "catch-up-": 0, "catch-up-x": 0, "npm/catch-up-2@1.0.0": 0} {
		if got := StageOf(id); got != want {
			t.Errorf("StageOf(%q) = %d, want %d", id, got, want)
		}
	}
	if !CatchUp(FirstID) || !CatchUp(StageID(2)) || CatchUp("npm/a@1.0.0") {
		t.Error("CatchUp")
	}
}
