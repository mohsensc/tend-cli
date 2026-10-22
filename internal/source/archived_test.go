package source

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
)

func TestArchivedReplay(t *testing.T) {
	got, err := Archived(context.Background(), replay(), archivedRepos)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("%d archived, want only jwt-go: %+v", len(got), got)
	}
	ev, ok := got["https://github.com/dgrijalva/jwt-go"]
	if !ok || ev.Source != "archived" || ev.Title != "dgrijalva/jwt-go is archived" || ev.URL != "https://github.com/dgrijalva/jwt-go" {
		t.Errorf("evidence = %+v", ev)
	}
}

func TestArchivedText(t *testing.T) {
	f := routes(map[string]*core.Response{
		"GET https://api.github.com/repos/pycrypto/pycrypto": ok(map[string]any{
			"full_name": "pycrypto/pycrypto", "html_url": "https://github.com/pycrypto/pycrypto", "archived": true, "pushed_at": "2018-06-13T01:02:03Z",
		}),
	})
	got, err := Archived(context.Background(), f, []string{"https://github.com/pycrypto/pycrypto"})
	if err != nil {
		t.Fatal(err)
	}
	if ev := got["https://github.com/pycrypto/pycrypto"]; ev.Text != "pycrypto/pycrypto is read-only. Last push: 2018-06-13." {
		t.Errorf("text = %q", ev.Text)
	}
	if call := f.calls[0]; call.Opts.TTL != 24*time.Hour || call.Opts.Header["Authorization"] != "" {
		t.Errorf("opts = %+v", call.Opts)
	}
}

// Two packages from one monorepo cost one lookup, and both are archived.
func TestArchivedOneFetchPerRepo(t *testing.T) {
	f := routes(map[string]*core.Response{
		"GET https://api.github.com/repos/o/mono": ok(map[string]any{"full_name": "o/mono", "html_url": "https://github.com/o/mono", "archived": true}),
	})
	repos := []string{"https://github.com/o/mono", "git+https://github.com/o/mono.git"}
	got, err := Archived(context.Background(), f, repos)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if len(f.calls) != 1 {
		t.Errorf("%d fetches, want 1", len(f.calls))
	}
}

func TestArchivedSkips(t *testing.T) {
	f := routes(map[string]*core.Response{
		"GET https://api.github.com/repos/o/gone":    status(404),
		"GET https://api.github.com/repos/o/limited": status(403),
		"GET https://api.github.com/repos/o/live":    ok(map[string]any{"full_name": "o/live", "archived": false}),
	})
	repos := []string{"https://github.com/o/gone", "https://github.com/o/limited", "https://github.com/o/live", "https://gitlab.com/o/r", ""}
	got, err := Archived(context.Background(), f, repos)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
	var urls []string
	for _, c := range f.calls {
		urls = append(urls, c.URL)
	}
	if want := 3; len(urls) != want || slices.ContainsFunc(urls, func(u string) bool { return u == "https://api.github.com/repos/o/r" }) {
		t.Errorf("fetched %q, want only the GitHub repos", urls)
	}
}

func TestArchivedErrors(t *testing.T) {
	repos := []string{"https://github.com/o/r"}
	if _, err := Archived(context.Background(), routes(nil), repos); err == nil {
		t.Error("a transport error was swallowed")
	}
	f := routes(map[string]*core.Response{"GET https://api.github.com/repos/o/r": text("{")})
	if _, err := Archived(context.Background(), f, repos); err == nil {
		t.Error("bad JSON was swallowed")
	}

	// A rate limit is not an answer: the repo is neither gone nor live, and
	// the others are still read.
	limited := func(n int, h map[string][]string, body string) *core.Response {
		return &core.Response{Status: n, Header: h, Body: []byte(body)}
	}
	archived := ok(map[string]any{"full_name": "o/old", "archived": true})
	for name, resp := range map[string]*core.Response{
		"429":            limited(429, nil, ""),
		"403 no quota":   limited(403, map[string][]string{"X-Ratelimit-Remaining": {"0"}}, ""),
		"403 message":    limited(403, nil, `{"message": "API rate limit exceeded"}`),
		"403 retryafter": limited(403, map[string][]string{"Retry-After": {"60"}}, ""),
	} {
		f := routes(map[string]*core.Response{"GET https://api.github.com/repos/o/r": resp, "GET https://api.github.com/repos/o/old": archived})
		got, err := Archived(context.Background(), f, []string{"https://github.com/o/r", "https://github.com/o/old"})
		if err == nil || !strings.Contains(err.Error(), "rate limit") {
			t.Errorf("%s: err = %v, want the rate limit", name, err)
		}
		if _, ok := got["https://github.com/o/old"]; !ok || len(got) != 1 {
			t.Errorf("%s: got %v, want only o/old", name, got)
		}
	}
}
