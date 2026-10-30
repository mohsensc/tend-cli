package core

import (
	"slices"
	"strings"
	"testing"
)

func TestScrub(t *testing.T) {
	env := []string{
		"GITHUB_TOKEN=a", "GH_PAT=b", "GITHUB_PAT=c", "STRIPE_KEY=d", "VAULT_ROOT_TOKEN=e",
		"DB_PASSWORD=f", "OPENAI_API_KEY=g", "TEND_MODEL_KEY=h", "NPM_TOKEN=i",
		"PATH=/bin", "HOME=/home/a", "KEYBOARD=us", "SSH_AUTH_SOCK=/tmp/s", "PATCH_LEVEL=1",
	}
	var got []string
	for _, kv := range Scrub(env, []string{"NPM_TOKEN"}) {
		name, _, _ := strings.Cut(kv, "=")
		got = append(got, name)
	}
	want := []string{"NPM_TOKEN", "PATH", "HOME", "KEYBOARD", "SSH_AUTH_SOCK", "PATCH_LEVEL"}
	if !slices.Equal(got, want) {
		t.Errorf("Scrub kept %v, want %v", got, want)
	}
}

func TestEnviron(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "secret")
	t.Setenv("TEND_MODEL_KEY", "secret")
	t.Setenv("NPM_TOKEN", "registry")
	defer Keep(nil)
	Keep([]string{"NPM_TOKEN"})
	env := Environ("GOFLAGS=-mod=readonly")
	for _, kv := range env {
		if strings.HasPrefix(kv, "GITHUB_TOKEN=") || strings.HasPrefix(kv, "TEND_MODEL_KEY=") {
			t.Errorf("Environ passed on %s", kv)
		}
	}
	if !slices.Contains(env, "NPM_TOKEN=registry") || env[len(env)-1] != "GOFLAGS=-mod=readonly" {
		t.Errorf("Environ = %v", env)
	}
}
