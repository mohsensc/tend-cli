package fetch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }
func newClock() *clock               { return &clock{t: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)} }
func (c *clock) client(dir string) *Client {
	cl := New(dir, "1.0.0")
	cl.Now = c.now
	return cl
}

func waits(cl *Client) *[]time.Duration {
	var ws []time.Duration
	cl.wait = func(ctx context.Context, d time.Duration) error {
		ws = append(ws, d)
		return nil
	}
	return &ws
}

func TestCacheETag(t *testing.T) {
	var hits, revalidated atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("If-None-Match") == `"v1"` {
			revalidated.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		fmt.Fprint(w, "hello")
	}))
	defer srv.Close()

	ck := newClock()
	cl := ck.client(t.TempDir())
	ctx := context.Background()

	res, err := cl.Get(ctx, srv.URL)
	if err != nil || res.Status != 200 || string(res.Body) != "hello" || res.Cached {
		t.Fatalf("first get = %+v, %v", res, err)
	}
	ck.add(2 * time.Hour)
	res, err = cl.Get(ctx, srv.URL)
	if err != nil || res.Status != 200 || string(res.Body) != "hello" || !res.Cached {
		t.Fatalf("revalidated get = %+v, %v", res, err)
	}
	if hits.Load() != 2 || revalidated.Load() != 1 {
		t.Fatalf("hits %d revalidated %d, want 2 and 1", hits.Load(), revalidated.Load())
	}
	ck.add(30 * time.Minute)
	if _, err := cl.Get(ctx, srv.URL); err != nil || hits.Load() != 2 {
		t.Fatalf("a 304 should restart the TTL: hits %d, %v", hits.Load(), err)
	}
}

func TestCacheTTL(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		fmt.Fprint(w, strings.Repeat("x", int(n)))
	}))
	defer srv.Close()

	ck := newClock()
	cl := ck.client(t.TempDir())
	ctx := context.Background()
	get := func() string {
		t.Helper()
		res, err := cl.Get(ctx, srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		return string(res.Body)
	}

	if got := get(); got != "x" {
		t.Fatalf("first = %q", got)
	}
	ck.add(59 * time.Minute)
	if got := get(); got != "x" {
		t.Fatalf("within TTL = %q, want cached x", got)
	}
	ck.add(2 * time.Minute)
	if got := get(); got != "xx" {
		t.Fatalf("after TTL = %q, want xx", got)
	}
	ck.add(10 * time.Minute)
	res, err := cl.Get(ctx, srv.URL, TTL(5*time.Minute))
	if err != nil || string(res.Body) != "xxx" {
		t.Fatalf("short TTL = %+v, %v", res, err)
	}
	res, err = cl.Get(ctx, srv.URL, NoCache())
	if err != nil || string(res.Body) != "xxxx" {
		t.Fatalf("no cache = %+v, %v", res, err)
	}
	if hits.Load() != 4 {
		t.Fatalf("hits = %d, want 4", hits.Load())
	}
}

func TestNoDirNoCache(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	cl := New("", "dev")
	for range 2 {
		if _, err := cl.Get(context.Background(), srv.URL); err != nil {
			t.Fatal(err)
		}
	}
	if hits.Load() != 2 {
		t.Fatalf("hits = %d, want 2", hits.Load())
	}
}

func TestRetry(t *testing.T) {
	tests := []struct {
		name     string
		statuses []int
		after    string
		status   int
		err      bool
		waits    []time.Duration
	}{
		{"503 then 200", []int{503, 200}, "", 200, false, []time.Duration{time.Second}},
		{"backoff doubles", []int{502, 500, 200}, "", 200, false, []time.Duration{time.Second, 2 * time.Second}},
		{"retry after seconds", []int{429, 200}, "7", 200, false, []time.Duration{7 * time.Second}},
		{"retry after date", []int{429, 200}, "Thu, 01 Oct 2026 12:00:30 GMT", 200, false, []time.Duration{30 * time.Second}},
		{"retry after too long", []int{429, 200}, "3600", 0, true, nil},
		{"gives up", []int{503, 503, 503, 503, 200}, "", 0, true, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}},
		{"404 passthrough", []int{404}, "", 404, false, nil},
		{"400 passthrough", []int{400}, "", 400, false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var n atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				i := int(n.Add(1)) - 1
				if tt.after != "" {
					w.Header().Set("Retry-After", tt.after)
				}
				w.WriteHeader(tt.statuses[i])
				fmt.Fprint(w, "body")
			}))
			defer srv.Close()

			cl := newClock().client(t.TempDir())
			ws := waits(cl)
			res, err := cl.Get(context.Background(), srv.URL)
			if tt.err {
				if err == nil || !strings.Contains(err.Error(), srv.URL) {
					t.Fatalf("err = %v, want an error naming the URL", err)
				}
			} else if err != nil || res.Status != tt.status || string(res.Body) != "body" {
				t.Fatalf("res = %+v, %v; want status %d", res, err, tt.status)
			}
			if !reflect.DeepEqual(*ws, tt.waits) {
				t.Errorf("waits = %v, want %v", *ws, tt.waits)
			}
		})
	}
}

func TestPostRetry(t *testing.T) {
	tests := []struct {
		name     string
		opts     []core.FetchOpt
		statuses []int
		sent     int
		err      bool
	}{
		{"sent once", nil, []int{503, 200}, 1, true},
		{"429 sent once", nil, []int{429, 200}, 1, true},
		{"idempotent retried", []core.FetchOpt{Idempotent()}, []int{503, 429, 200}, 3, false},
		{"idempotent gives up", []core.FetchOpt{Idempotent()}, []int{503, 503, 503, 503, 200}, 4, true},
		{"200 once", nil, []int{200}, 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var n atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statuses[int(n.Add(1))-1])
			}))
			defer srv.Close()

			cl := newClock().client("")
			waits(cl)
			res, err := cl.Post(context.Background(), srv.URL, []byte(`{}`), tt.opts...)
			if tt.err != (err != nil) {
				t.Fatalf("res = %+v, err = %v", res, err)
			}
			if got := int(n.Load()); got != tt.sent {
				t.Errorf("sent %d times, want %d", got, tt.sent)
			}
		})
	}
}

func TestNotFoundNotCached(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	cl := newClock().client(t.TempDir())
	for range 2 {
		res, err := cl.Get(context.Background(), srv.URL)
		if err != nil || res.Status != 404 {
			t.Fatalf("res = %+v, %v", res, err)
		}
	}
	if hits.Load() != 2 {
		t.Fatalf("hits = %d, want 2", hits.Load())
	}
}

func TestRequestHeaders(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.Header.Clone() }))
	defer srv.Close()

	cl := New("", "1.2.3")
	cl.Token = func(host string) string {
		if host == "127.0.0.1" {
			return "s3cret"
		}
		return ""
	}
	if _, err := cl.Get(context.Background(), srv.URL, Header("Accept", "application/vnd.github+json")); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"User-Agent":    "tend/1.2.3 (+https://github.com/mohsensc/tend-cli)",
		"Authorization": "Bearer s3cret",
		"Accept":        "application/vnd.github+json",
	}
	for k, v := range want {
		if got.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, got.Get(k), v)
		}
	}
	if _, err := cl.Post(context.Background(), srv.URL, []byte(`{}`), Header("Authorization", "")); err != nil {
		t.Fatal(err)
	}
	if got.Get("Authorization") != "" || got.Get("Content-Type") != "application/json" {
		t.Errorf("post headers = %v", got)
	}
}

func TestRedact(t *testing.T) {
	h := http.Header{"Authorization": {"Bearer s3cret"}, "X-Api-Key": {"k"}, "Accept": {"*/*"}}
	got := redact(h)
	if strings.Contains(got, "s3cret") || strings.Contains(got, "=k") {
		t.Fatalf("redact leaked: %s", got)
	}
	if want := "Accept=*/* Authorization=REDACTED X-Api-Key=REDACTED"; got != want {
		t.Fatalf("redact = %q, want %q", got, want)
	}
}

func TestRecordReplay(t *testing.T) {
	bin := []byte{0xff, 0x00, 0xfe}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "session=private")
		w.Header().Set("Link", `<https://x/next>; rel="next"`)
		switch r.URL.Path {
		case "/bin":
			fmt.Fprintf(w, "%s", bin)
		case "/missing":
			http.NotFound(w, r)
		case "/post":
			b, _ := io.ReadAll(r.Body)
			fmt.Fprintf(w, "echo %s", b)
		default:
			fmt.Fprint(w, `{"ok":true}`)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	rec := Record(New(t.TempDir(), "dev"), dir)
	ctx := context.Background()
	want := map[string]string{}
	for _, p := range []string{"/json", "/bin", "/missing"} {
		res, err := rec.Get(ctx, srv.URL+p)
		if err != nil {
			t.Fatal(err)
		}
		want[p] = string(res.Body)
	}
	if _, err := rec.Post(ctx, srv.URL+"/post", []byte(`{"q":1}`)); err != nil {
		t.Fatal(err)
	}

	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 4 {
		t.Fatalf("recorded %d files, want 4", len(files))
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		if strings.Contains(string(b), "private") {
			t.Errorf("%s records a cookie", f)
		}
	}

	rp := Replay(dir)
	for p, body := range want {
		res, err := rp.Get(ctx, srv.URL+p)
		if err != nil {
			t.Fatal(err)
		}
		if string(res.Body) != body {
			t.Errorf("replay %s = %q, want %q", p, res.Body, body)
		}
		if p == "/missing" && res.Status != 404 {
			t.Errorf("replay %s status = %d, want 404", p, res.Status)
		}
		if p == "/json" && http.Header(res.Header).Get("Link") == "" {
			t.Errorf("replay %s lost the Link header", p)
		}
	}
	res, err := rp.Post(ctx, srv.URL+"/post", []byte(`{"q":1}`))
	if err != nil || string(res.Body) != `echo {"q":1}` {
		t.Fatalf("replay post = %+v, %v", res, err)
	}

	for _, bad := range []func() error{
		func() error { _, err := rp.Get(ctx, srv.URL+"/other"); return err },
		func() error { _, err := rp.Post(ctx, srv.URL+"/post", []byte(`{"q":2}`)); return err },
	} {
		err := bad()
		if err == nil || !strings.Contains(err.Error(), srv.URL) || !strings.Contains(err.Error(), "no recording") {
			t.Errorf("unrecorded request err = %v", err)
		}
	}
}

func TestRecordName(t *testing.T) {
	got := RecordName("GET", "https://registry.npmjs.org/stripe", nil)
	if !strings.HasPrefix(got, "GET_registry.npmjs.org_") || len(got) != len("GET_registry.npmjs.org_")+12+len(".json") {
		t.Errorf("RecordName = %q", got)
	}
	if RecordName("POST", "http://127.0.0.1:8080/x", []byte("a")) == RecordName("POST", "http://127.0.0.1:8080/x", []byte("b")) {
		t.Error("body does not change the name")
	}
	if got := RecordName("GET", "http://127.0.0.1:8080/x", nil); !strings.HasPrefix(got, "GET_127.0.0.1_8080_") {
		t.Errorf("RecordName = %q", got)
	}
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	for name, age := range map[string]time.Duration{"old.json": 8 * 24 * time.Hour, "new.json": time.Hour} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	if err := Prune(dir, 7*24*time.Hour, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "old.json")); !os.IsNotExist(err) {
		t.Error("a week-old response is still cached")
	}
	if _, err := os.Stat(filepath.Join(dir, "new.json")); err != nil {
		t.Error("a fresh response was dropped")
	}
	if err := Prune(filepath.Join(dir, "none"), time.Hour, now); err != nil {
		t.Errorf("a missing cache: %v", err)
	}
}

func hangUp(t *testing.T, w http.ResponseWriter, partial bool) {
	t.Helper()
	conn, buf, err := w.(http.Hijacker).Hijack()
	if err != nil {
		t.Error(err)
		return
	}
	if partial {
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\nhalf")
		_ = buf.Flush()
	}
	conn.Close()
}

func TestRetryDroppedConnection(t *testing.T) {
	tests := []struct {
		name    string
		post    bool
		opts    []core.FetchOpt
		partial bool
		drops   int
		sent    int
		err     bool
	}{
		{"get", false, nil, false, 1, 2, false},
		{"get body cut short", false, nil, true, 1, 2, false},
		{"get gives up", false, nil, false, 9, 4, true},
		{"post sent once", true, nil, false, 1, 1, true},
		{"idempotent post", true, []core.FetchOpt{Idempotent()}, false, 1, 2, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var n atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if int(n.Add(1)) <= tt.drops {
					hangUp(t, w, tt.partial)
					return
				}
				fmt.Fprint(w, "body")
			}))
			defer srv.Close()

			cl := newClock().client("")
			ws := waits(cl)
			var res *core.Response
			var err error
			if tt.post {
				res, err = cl.Post(context.Background(), srv.URL, []byte(`{}`), tt.opts...)
			} else {
				res, err = cl.Get(context.Background(), srv.URL, tt.opts...)
			}
			if tt.err != (err != nil) {
				t.Fatalf("res = %+v, err = %v", res, err)
			}
			if !tt.err && string(res.Body) != "body" {
				t.Errorf("body = %q", res.Body)
			}
			if got := int(n.Load()); got != tt.sent || len(*ws) != tt.sent-1 {
				t.Errorf("sent %d times with waits %v, want %d", got, *ws, tt.sent)
			}
			if tt.err && tt.sent > 1 && !strings.Contains(err.Error(), fmt.Sprintf("after %d attempts", tt.sent)) {
				t.Errorf("err = %v, want the attempts counted", err)
			}
		})
	}
}

func TestNoRetryAfterCancel(t *testing.T) {
	var n atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		cancel()
		hangUp(t, w, false)
	}))
	defer srv.Close()

	cl := newClock().client("")
	waits(cl)
	if _, err := cl.Get(ctx, srv.URL); err == nil {
		t.Fatal("no error")
	}
	if got := n.Load(); got != 1 {
		t.Errorf("sent %d times, want once after the context ended", got)
	}
}

func TestRetryTimeoutOnce(t *testing.T) {
	var n atomic.Int32
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(block)

	cl := newClock().client("")
	cl.HTTP.Timeout = 50 * time.Millisecond
	ws := waits(cl)
	_, err := cl.Get(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "after 2 attempts") {
		t.Fatalf("err = %v, want a timeout after 2 attempts", err)
	}
	if len(*ws) != 1 {
		t.Errorf("waits = %v, want one retry", *ws)
	}
}

func TestNoRetryPermanent(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "body")
	}))
	defer srv.Close()

	cl := newClock().client("")
	ws := waits(cl)
	for _, u := range []string{srv.URL, "ftp://example.invalid/x"} {
		if _, err := cl.Get(context.Background(), u); err == nil || strings.Contains(err.Error(), "attempts") {
			t.Errorf("%s: err = %v, want one failed attempt", u, err)
		}
	}
	if len(*ws) != 0 {
		t.Errorf("waits = %v, want none", *ws)
	}
}

// A body that takes longer than the client's own limit fails, unless the
// request is given longer, and the client's limit stays as it was.
func TestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		time.Sleep(300 * time.Millisecond)
		fmt.Fprint(w, "slow")
	}))
	defer srv.Close()
	c := New("", "test")
	c.HTTP = &http.Client{Timeout: 100 * time.Millisecond}
	c.wait = func(context.Context, time.Duration) error { return nil }
	if _, err := c.Get(context.Background(), srv.URL, NoCache()); err == nil {
		t.Fatal("a slow body within the client's limit")
	}
	res, err := c.Get(context.Background(), srv.URL, NoCache(), Timeout(5*time.Second))
	if err != nil || string(res.Body) != "slow" {
		t.Fatalf("with a longer timeout: %v", err)
	}
	if c.HTTP.Timeout != 100*time.Millisecond {
		t.Errorf("the client's timeout became %s", c.HTTP.Timeout)
	}
}
