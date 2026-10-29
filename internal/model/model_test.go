package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mohsensc/tend-cli/internal/config"
)

func TestScript(t *testing.T) {
	boom := errors.New("overloaded")
	s := &Script{Replies: []string{"", "second", "third"}, Errs: []error{boom}}
	ctx := context.Background()

	if _, err := s.Complete(ctx, "sys", "p1"); !errors.Is(err, boom) {
		t.Fatalf("call 1 err = %v, want %v", err, boom)
	}
	for _, want := range []string{"second", "third"} {
		got, err := s.Complete(ctx, "sys", "p")
		if err != nil || got != want {
			t.Fatalf("got %q, %v; want %q", got, err, want)
		}
	}
	if _, err := s.Complete(ctx, "sys", "p4"); err == nil {
		t.Fatal("ran past the script without an error")
	}
	if len(s.Calls) != 4 || s.Calls[0] != (Call{"sys", "p1"}) || s.Calls[3].Prompt != "p4" {
		t.Fatalf("calls = %+v", s.Calls)
	}
}

func env(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func TestNewNone(t *testing.T) {
	for _, p := range []string{"", "none"} {
		m, err := New(config.Model{Provider: p}, env(nil))
		if m != nil || err != nil {
			t.Errorf("New(%q) = %v, %v; want no model", p, m, err)
		}
	}
}

func TestNewKey(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Model
		env  map[string]string
		want string // header value the request must carry, "" for an error
		fail string // substring of the error
	}{
		{"usual anthropic", config.Model{Provider: "anthropic", Model: "m"}, map[string]string{"ANTHROPIC_API_KEY": "k1"}, "k1", ""},
		{"usual openai", config.Model{Provider: "openai", Model: "m"}, map[string]string{"OPENAI_API_KEY": "k2"}, "Bearer k2", ""},
		{"tend key beats usual", config.Model{Provider: "anthropic", Model: "m"}, map[string]string{"TEND_MODEL_KEY": "k3", "ANTHROPIC_API_KEY": "k1"}, "k3", ""},
		{"key_env beats all", config.Model{Provider: "anthropic", Model: "m", KeyEnv: "MY_KEY"}, map[string]string{"MY_KEY": "k4", "TEND_MODEL_KEY": "k3", "ANTHROPIC_API_KEY": "k1"}, "k4", ""},
		{"empty key_env falls through", config.Model{Provider: "anthropic", Model: "m", KeyEnv: "MY_KEY"}, map[string]string{"ANTHROPIC_API_KEY": "k1"}, "k1", ""},
		{"key is trimmed", config.Model{Provider: "anthropic", Model: "m"}, map[string]string{"ANTHROPIC_API_KEY": " k1\n"}, "k1", ""},
		{"no key anthropic", config.Model{Provider: "anthropic", Model: "m"}, nil, "", "ANTHROPIC_API_KEY"},
		{"no key openai", config.Model{Provider: "openai", Model: "m"}, nil, "", "OPENAI_API_KEY"},
		{"no key names key_env", config.Model{Provider: "openai", Model: "m", KeyEnv: "MY_KEY"}, nil, "", "MY_KEY"},
		{"groq usual", config.Model{Provider: "groq", Model: "m"}, map[string]string{"GROQ_API_KEY": "k5"}, "Bearer k5", ""},
		{"groq tend key", config.Model{Provider: "groq", Model: "m"}, map[string]string{"TEND_MODEL_KEY": "k3"}, "Bearer k3", ""},
		{"groq tend key beats usual", config.Model{Provider: "groq", Model: "m"}, map[string]string{"TEND_MODEL_KEY": "k3", "GROQ_API_KEY": "k5"}, "Bearer k3", ""},
		{"no key groq", config.Model{Provider: "groq", Model: "m"}, nil, "", "GROQ_API_KEY or TEND_MODEL_KEY"},
		{"ollama needs no key", config.Model{Provider: "ollama", Model: "m"}, nil, "", ""},
		{"ollama tend key", config.Model{Provider: "ollama", Model: "m"}, map[string]string{"TEND_MODEL_KEY": "k3"}, "Bearer k3", ""},
		{"no key ollama names key_env", config.Model{Provider: "ollama", Model: "m", KeyEnv: "MY_KEY"}, nil, "", "MY_KEY"},
		{"no model anthropic", config.Model{Provider: "anthropic"}, map[string]string{"ANTHROPIC_API_KEY": "k"}, "", "model:"},
		{"no model openai", config.Model{Provider: "openai"}, map[string]string{"OPENAI_API_KEY": "k"}, "", "model:"},
		{"no model ollama", config.Model{Provider: "ollama"}, nil, "", "model:"},
		{"no command", config.Model{Provider: "command"}, nil, "", "command"},
		{"unknown", config.Model{Provider: "gemini"}, nil, "", "gemini"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, err := New(tc.cfg, env(tc.env))
			if tc.fail != "" {
				if err == nil || !strings.Contains(err.Error(), tc.fail) {
					t.Fatalf("err = %v, want it to mention %q", err, tc.fail)
				}
				if m != nil {
					t.Fatalf("got a model alongside the error: %v", m)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Header.Get("x-api-key") + r.Header.Get("Authorization")
				_, _ = w.Write(read(t, "anthropic_ok.json"))
			}))
			defer srv.Close()
			switch m := m.(type) {
			case *anthropic:
				m.url = srv.URL
			case *openai:
				m.url = srv.URL
			}
			_, _ = m.Complete(context.Background(), "", "hi") // only the request matters here
			if got != tc.want {
				t.Fatalf("request carried %q, want %q", got, tc.want)
			}
		})
	}
}

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// serve answers each request with the next canned reply and records what it saw.
type server struct {
	*httptest.Server
	mu      sync.Mutex
	bodies  []map[string]any
	headers []http.Header
	waits   []time.Duration
}

type reply struct {
	status int
	file   string
	retry  string
}

func serve(t *testing.T, replies ...reply) *server {
	t.Helper()
	s := &server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		s.bodies = append(s.bodies, body)
		s.headers = append(s.headers, r.Header.Clone())
		rp := replies[min(len(s.bodies), len(replies))-1]
		if rp.retry != "" {
			w.Header().Set("Retry-After", rp.retry)
		}
		w.WriteHeader(rp.status)
		_, _ = w.Write(read(t, rp.file))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) sleeper() func(context.Context, time.Duration) error {
	return func(ctx context.Context, d time.Duration) error {
		s.waits = append(s.waits, d)
		return nil
	}
}

func TestAnthropic(t *testing.T) {
	srv := serve(t, reply{200, "anthropic_ok.json", ""})
	m, err := newAnthropic(config.Model{Provider: "anthropic", Model: "model-id"}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	m.url = srv.URL + "/v1/messages"
	if m.Name() != "anthropic/model-id" {
		t.Errorf("Name = %q", m.Name())
	}
	got, err := m.Complete(context.Background(), "be terse", "fix it")
	if err != nil {
		t.Fatal(err)
	}
	if want := "Here is the patch:\n--- a/x\n+++ b/x\n"; got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}
	h := srv.headers[0]
	if h.Get("x-api-key") != "secret" || h.Get("anthropic-version") != "2023-06-01" || h.Get("Content-Type") != "application/json" {
		t.Errorf("headers = %v", h)
	}
	want := map[string]any{
		"model":      "model-id",
		"max_tokens": float64(8000),
		"system":     "be terse",
		"messages":   []any{map[string]any{"role": "user", "content": "fix it"}},
	}
	if !reflect.DeepEqual(srv.bodies[0], want) {
		t.Errorf("body = %v, want %v", srv.bodies[0], want)
	}
}

func TestAnthropicDefaults(t *testing.T) {
	srv := serve(t, reply{200, "anthropic_ok.json", ""})
	m, _ := newAnthropic(config.Model{Model: "model-id"}, "k")
	m.url = srv.URL
	if _, err := m.Complete(context.Background(), "", "p"); err != nil {
		t.Fatal(err)
	}
	b := srv.bodies[0]
	if _, ok := b["system"]; ok {
		t.Errorf("empty system was sent: %v", b)
	}
	if b["max_tokens"] != float64(8000) {
		t.Errorf("max_tokens = %v, want the 8000 default", b["max_tokens"])
	}
}

func TestAnthropicBadReplies(t *testing.T) {
	tests := []struct {
		name  string
		reply reply
		want  string
	}{
		{"cut off", reply{200, "anthropic_cut.json", ""}, "cut off"},
		{"api error", reply{400, "anthropic_error.json", ""}, "max_tokens: Field required"},
		{"not json", reply{200, "openai_error.json", ""}, "no text"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := serve(t, tc.reply)
			m, _ := newAnthropic(config.Model{Model: "model-id"}, "k")
			m.url = srv.URL
			_, err := m.Complete(context.Background(), "", "p")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
			if len(srv.bodies) != 1 {
				t.Errorf("%d requests, want 1", len(srv.bodies))
			}
		})
	}
}

func TestOpenAI(t *testing.T) {
	srv := serve(t, reply{200, "openai_ok.json", ""})
	m, err := newOpenAI(config.Model{Provider: "openai", Model: "model-id"}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	m.url = srv.URL + "/v1/chat/completions"
	if m.Name() != "openai/model-id" {
		t.Errorf("Name = %q", m.Name())
	}
	got, err := m.Complete(context.Background(), "be terse", "fix it")
	if err != nil {
		t.Fatal(err)
	}
	if want := "--- a/x\n+++ b/x\n"; got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}
	if srv.headers[0].Get("Authorization") != "Bearer secret" {
		t.Errorf("headers = %v", srv.headers[0])
	}
	want := map[string]any{
		"model":                 "model-id",
		"max_completion_tokens": float64(8000),
		"messages": []any{
			map[string]any{"role": "system", "content": "be terse"},
			map[string]any{"role": "user", "content": "fix it"},
		},
	}
	if !reflect.DeepEqual(srv.bodies[0], want) {
		t.Errorf("body = %v, want %v", srv.bodies[0], want)
	}
}

func TestOpenAINoSystem(t *testing.T) {
	srv := serve(t, reply{200, "openai_ok.json", ""})
	m, _ := newOpenAI(config.Model{Model: "model-id"}, "k")
	m.url = srv.URL
	if _, err := m.Complete(context.Background(), "", "p"); err != nil {
		t.Fatal(err)
	}
	if msgs := srv.bodies[0]["messages"].([]any); len(msgs) != 1 {
		t.Errorf("messages = %v, want only the user turn", msgs)
	}
}

func TestOpenAIBadReplies(t *testing.T) {
	tests := []struct {
		name  string
		reply reply
		want  string
	}{
		{"cut off", reply{200, "openai_cut.json", ""}, "cut off"},
		{"refusal", reply{200, "openai_refusal.json", ""}, "I can't help with that."},
		{"api error", reply{401, "openai_error.json", ""}, "Incorrect API key provided."},
		{"not a completion", reply{200, "anthropic_ok.json", ""}, "no choices"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := serve(t, tc.reply)
			m, _ := newOpenAI(config.Model{Model: "model-id"}, "k")
			m.url = srv.URL
			_, err := m.Complete(context.Background(), "", "p")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestRetry(t *testing.T) {
	srv := serve(t,
		reply{429, "anthropic_error.json", "3"},
		reply{503, "anthropic_error.json", ""},
		reply{200, "anthropic_ok.json", ""},
	)
	m, _ := newAnthropic(config.Model{Model: "model-id"}, "k")
	m.url, m.wait = srv.URL, srv.sleeper()
	if _, err := m.Complete(context.Background(), "", "p"); err != nil {
		t.Fatal(err)
	}
	if len(srv.bodies) != 3 {
		t.Errorf("%d requests, want 3", len(srv.bodies))
	}
	if want := []time.Duration{3 * time.Second, 2 * time.Second}; !reflect.DeepEqual(srv.waits, want) {
		t.Errorf("waits = %v, want %v", srv.waits, want)
	}
}

func TestRetryBadToolCall(t *testing.T) {
	srv := serve(t,
		reply{400, "openai_bad_call.json", ""},
		reply{400, "openai_bad_parse.json", ""},
		reply{200, "openai_tools.json", ""},
	)
	m, _ := newOpenAI(config.Model{Model: "model-id"}, "k")
	m.url, m.wait = srv.URL, srv.sleeper()
	if _, err := m.Chat(context.Background(), "", []Turn{{Role: "user", Text: "p"}}, nil); err != nil {
		t.Fatal(err)
	}
	if len(srv.bodies) != 3 {
		t.Errorf("%d requests, want 3", len(srv.bodies))
	}

	srv = serve(t, reply{400, "openai_error.json", ""})
	m.url = srv.URL
	if _, err := m.Complete(context.Background(), "", "p"); err == nil || len(srv.bodies) != 1 {
		t.Errorf("err = %v after %d requests; a plain 400 is not retried", err, len(srv.bodies))
	}
}

func TestRetryWaitInMessage(t *testing.T) {
	srv := serve(t,
		reply{429, "openai_rate.json", ""},
		reply{200, "openai_ok.json", ""},
	)
	m, _ := newOpenAI(config.Model{Model: "model-id"}, "k")
	m.url, m.wait = srv.URL, srv.sleeper()
	if _, err := m.Complete(context.Background(), "", "p"); err != nil {
		t.Fatal(err)
	}
	if want := []time.Duration{4 * time.Second}; !reflect.DeepEqual(srv.waits, want) {
		t.Errorf("waits = %v, want %v", srv.waits, want)
	}
	for in, want := range map[string]string{"try again in 3.7725s": "4", "Try again in 1m2s.": "62", "try again in 250ms": "1", "try again later": ""} {
		if got := tryAgain([]byte(in)); got != want {
			t.Errorf("tryAgain(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRetryGivesUp(t *testing.T) {
	srv := serve(t, reply{500, "anthropic_error.json", ""})
	m, _ := newAnthropic(config.Model{Model: "model-id"}, "k")
	m.url, m.wait = srv.URL, srv.sleeper()
	_, err := m.Complete(context.Background(), "", "p")
	if err == nil || !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("err = %v", err)
	}
	if len(srv.bodies) != 1+retries {
		t.Errorf("%d requests, want %d", len(srv.bodies), 1+retries)
	}
}

func TestRetryAfterTooLong(t *testing.T) {
	srv := serve(t, reply{429, "anthropic_error.json", "3600"})
	m, _ := newOpenAI(config.Model{Model: "model-id"}, "k")
	m.url, m.wait = srv.URL, srv.sleeper()
	if _, err := m.Complete(context.Background(), "", "p"); err == nil {
		t.Fatal("waited an hour instead of failing")
	}
	if len(srv.bodies) != 1 || len(srv.waits) != 0 {
		t.Errorf("%d requests, waits %v; want one request and no wait", len(srv.bodies), srv.waits)
	}
}

func TestDelay(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		attempt int
		header  string
		want    time.Duration
	}{
		{0, "", time.Second},
		{1, "", 2 * time.Second},
		{0, "7", 7 * time.Second},
		{1, " 0 ", 0},
		{0, now.Add(20 * time.Second).Format(http.TimeFormat), 20 * time.Second},
		{0, now.Add(-time.Hour).Format(http.TimeFormat), 0},
		{2, "soon", 4 * time.Second},
	}
	for _, tc := range tests {
		if got := delay(tc.attempt, tc.header, now); got != tc.want {
			t.Errorf("delay(%d, %q) = %v, want %v", tc.attempt, tc.header, got, tc.want)
		}
	}
}

func script(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "model.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCommand(t *testing.T) {
	m, err := New(config.Model{Provider: "command", Command: script(t, "cat")}, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if m.Name() != "command" {
		t.Errorf("Name = %q", m.Name())
	}
	got, err := m.Complete(context.Background(), "sys", "prompt")
	if err != nil || got != "sys\n\nprompt" {
		t.Fatalf("got %q, %v", got, err)
	}
	got, err = m.Complete(context.Background(), "", "prompt")
	if err != nil || got != "prompt" {
		t.Fatalf("no system: got %q, %v", got, err)
	}
}

// The command never sees the repo token, only the key tend.yml names.
func TestCommandEnv(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghs_secret")
	t.Setenv("LLM_KEY", "sk-local")
	m, err := New(config.Model{Provider: "command", Command: "env", KeyEnv: "LLM_KEY"}, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.Complete(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "ghs_secret") || !strings.Contains(got, "LLM_KEY=sk-local") {
		t.Errorf("env:\n%s", got)
	}
}

func TestCommandName(t *testing.T) {
	m, _ := New(config.Model{Provider: "command", Model: "local-7b", Command: "true"}, env(nil))
	if m.Name() != "command/local-7b" {
		t.Errorf("Name = %q", m.Name())
	}
}

func TestCommandFails(t *testing.T) {
	m, _ := New(config.Model{Provider: "command", Command: script(t, "echo one >&2; echo out; echo out of memory >&2; exit 3")}, env(nil))
	got, err := m.Complete(context.Background(), "", "p")
	if err == nil {
		t.Fatalf("got %q with no error", got)
	}
	for _, want := range []string{"exit status 3", "out of memory"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to mention %q", err, want)
		}
	}
	if got != "" {
		t.Errorf("reply = %q, want none on failure", got)
	}
}

func TestCommandStopsWithContext(t *testing.T) {
	m, _ := New(config.Model{Provider: "command", Command: script(t, "sleep 30")}, env(nil))
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := m.Complete(ctx, "", "p")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a deadline", err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("took %v to stop", took)
	}
}

func TestTail(t *testing.T) {
	if got := tail("a\nb\nc\nd\ne\nf\ng\n"); got != "c\nd\ne\nf\ng" {
		t.Errorf("tail = %q", got)
	}
	long := strings.Repeat("x", 900)
	if got := tail(long); len(got) != 500 {
		t.Errorf("len = %d, want 500", len(got))
	}
}

// capture writes every response body that passes through into dir.
type capture struct {
	dir, name string
	next      http.RoundTripper
}

func (c capture) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := c.next.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	var pretty json.RawMessage = b
	if out, err := json.MarshalIndent(pretty, "", "  "); err == nil {
		b = append(out, '\n')
	}
	if err := os.WriteFile(filepath.Join(c.dir, c.name), b, 0o644); err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(strings.NewReader(string(b)))
	return resp, nil
}

func recording(t *testing.T, keyEnv, modelEnv string) (key, model string) {
	t.Helper()
	if os.Getenv("TEND_RECORD") != "1" {
		t.Skip("set TEND_RECORD=1 to record")
	}
	key, model = os.Getenv(keyEnv), os.Getenv(modelEnv)
	if key == "" || model == "" {
		t.Skipf("set %s and %s to record", keyEnv, modelEnv)
	}
	return key, model
}

func TestRecordAnthropic(t *testing.T) {
	key, id := recording(t, "ANTHROPIC_API_KEY", "TEND_RECORD_ANTHROPIC_MODEL")
	m, err := newAnthropic(config.Model{Model: id}, key)
	if err != nil {
		t.Fatal(err)
	}
	m.http.Transport = capture{"testdata", "anthropic_recorded.json", http.DefaultTransport}
	got, err := m.Complete(context.Background(), "", "Reply with the single word: ok")
	if err != nil || got == "" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestRecordOpenAI(t *testing.T) {
	key, id := recording(t, "OPENAI_API_KEY", "TEND_RECORD_OPENAI_MODEL")
	m, err := newOpenAI(config.Model{Model: id}, key)
	if err != nil {
		t.Fatal(err)
	}
	m.http.Transport = capture{"testdata", "openai_recorded.json", http.DefaultTransport}
	got, err := m.Complete(context.Background(), "", "Reply with the single word: ok")
	if err != nil || got == "" {
		t.Fatalf("got %q, %v", got, err)
	}
}

var conversation = []Turn{
	{Role: "user", Text: "move off charges"},
	{Role: "assistant", Text: "Looking.", Calls: []ToolCall{
		{ID: "c1", Name: "grep", Args: json.RawMessage(`{"pattern":"charges"}`)},
		{ID: "c2", Name: "run", Args: json.RawMessage(`{"name":"test"}`)},
	}},
	{Role: "tool", CallID: "c1", Text: "src/billing.js:12: pay.charges.create("},
	{Role: "tool", CallID: "c2", Text: "exit 0"},
}

var tools = []Tool{{Name: "read", Description: "Read lines of a file.", Params: map[string]any{"type": "object"}}}

func TestAnthropicChat(t *testing.T) {
	srv := serve(t, reply{200, "anthropic_tools.json", ""})
	m, _ := newAnthropic(config.Model{Provider: "anthropic", Model: "model-id"}, "k")
	m.url = srv.URL
	got, err := m.Chat(context.Background(), "be careful", conversation, tools)
	if err != nil {
		t.Fatal(err)
	}
	want := &Reply{Text: "Reading the file first.", Tokens: 873, Calls: []ToolCall{
		{ID: "toolu_02", Name: "read", Args: json.RawMessage(`{"path": "src/billing.js", "from": 1, "to": 40}`)},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("reply = %+v, want %+v", got, want)
	}
	body := srv.bodies[0]
	sent, _ := json.Marshal(body["messages"])
	wantSent := `[{"content":[{"text":"move off charges","type":"text"}],"role":"user"},` +
		`{"content":[{"text":"Looking.","type":"text"},{"id":"c1","input":{"pattern":"charges"},"name":"grep","type":"tool_use"},{"id":"c2","input":{"name":"test"},"name":"run","type":"tool_use"}],"role":"assistant"},` +
		`{"content":[{"content":"src/billing.js:12: pay.charges.create(","tool_use_id":"c1","type":"tool_result"},{"content":"exit 0","tool_use_id":"c2","type":"tool_result"}],"role":"user"}]`
	if string(sent) != wantSent {
		t.Errorf("messages =\n%s\nwant\n%s", sent, wantSent)
	}
	defs, _ := json.Marshal(body["tools"])
	if string(defs) != `[{"description":"Read lines of a file.","input_schema":{"type":"object"},"name":"read"}]` || body["system"] != "be careful" {
		t.Errorf("tools = %s, system = %v", defs, body["system"])
	}
}

func TestOpenAIChat(t *testing.T) {
	srv := serve(t, reply{200, "openai_tools.json", ""})
	m, _ := newOpenAI(config.Model{Provider: "openai", Model: "model-id"}, "k")
	m.url = srv.URL
	got, err := m.Chat(context.Background(), "be careful", conversation, tools)
	if err != nil {
		t.Fatal(err)
	}
	want := &Reply{Tokens: 790, Calls: []ToolCall{
		{ID: "call_2", Name: "edit", Args: json.RawMessage(`{"path":"src/billing.js","old":"charges","new":"paymentIntents"}`)},
		{ID: "call_3", Name: "done", Args: json.RawMessage(`{}`)},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("reply = %+v, want %+v", got, want)
	}
	sent, _ := json.Marshal(srv.bodies[0]["messages"])
	wantSent := `[{"content":"be careful","role":"system"},{"content":"move off charges","role":"user"},` +
		`{"content":"Looking.","role":"assistant","tool_calls":[{"function":{"arguments":"{\"pattern\":\"charges\"}","name":"grep"},"id":"c1","type":"function"},{"function":{"arguments":"{\"name\":\"test\"}","name":"run"},"id":"c2","type":"function"}]},` +
		`{"content":"src/billing.js:12: pay.charges.create(","role":"tool","tool_call_id":"c1"},{"content":"exit 0","role":"tool","tool_call_id":"c2"}]`
	if string(sent) != wantSent {
		t.Errorf("messages =\n%s\nwant\n%s", sent, wantSent)
	}
	defs, _ := json.Marshal(srv.bodies[0]["tools"])
	if string(defs) != `[{"function":{"description":"Read lines of a file.","name":"read","parameters":{"type":"object"}},"type":"function"}]` {
		t.Errorf("tools = %s", defs)
	}
}

func TestPresets(t *testing.T) {
	tests := []struct {
		cfg  config.Model
		env  map[string]string
		url  string
		auth string
		name string
	}{
		{config.Model{Provider: "groq", Model: "openai/gpt-oss-120b"}, map[string]string{"GROQ_API_KEY": "g"}, "https://api.groq.com/openai/v1/chat/completions", "Bearer g", "groq/openai/gpt-oss-120b"},
		{config.Model{Provider: "groq", Model: "m"}, map[string]string{"TEND_MODEL_KEY": "t", "GROQ_API_KEY": "g"}, "https://api.groq.com/openai/v1/chat/completions", "Bearer t", "groq/m"},
		{config.Model{Provider: "ollama", Model: "qwen3"}, nil, "http://localhost:11434/v1/chat/completions", "", "ollama/qwen3"},
		{config.Model{Provider: "ollama", Model: "qwen3", BaseURL: "http://gpu:11434/v1/"}, nil, "http://gpu:11434/v1/chat/completions", "", "ollama/qwen3"},
		{config.Model{Provider: "openai", Model: "m", BaseURL: "http://localhost:8080/v1"}, map[string]string{"OPENAI_API_KEY": "o"}, "http://localhost:8080/v1/chat/completions", "Bearer o", "openai/m"},
	}
	for _, tt := range tests {
		m, err := New(tt.cfg, env(tt.env))
		if err != nil {
			t.Errorf("%+v: %v", tt.cfg, err)
			continue
		}
		o := m.(*openai)
		if o.url != tt.url || o.header["Authorization"] != tt.auth || o.Name() != tt.name {
			t.Errorf("%+v: url %s, auth %q, name %s", tt.cfg, o.url, o.header["Authorization"], o.Name())
		}
		if _, ok := m.(Agent); !ok {
			t.Errorf("%s calls no tools", o.Name())
		}
	}
	if _, err := New(config.Model{Provider: "groq", Model: "m"}, env(nil)); err == nil || !strings.Contains(err.Error(), "GROQ_API_KEY") {
		t.Errorf("groq without a key: %v", err)
	}
}

func TestSteps(t *testing.T) {
	s := &Steps{Replies: []Reply{{Text: "one"}}}
	got, err := s.Chat(context.Background(), "", conversation[:1], nil)
	if err != nil || got.Text != "one" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := s.Chat(context.Background(), "", conversation, nil); err == nil {
		t.Error("ran past the steps without an error")
	}
	if len(s.Chats) != 2 || len(s.Chats[1]) != 4 {
		t.Errorf("chats = %+v", s.Chats)
	}
}

func TestChatEmptyTurns(t *testing.T) {
	for _, empty := range []string{"", "\n", "  "} {
		turns := []Turn{{Role: "user", Text: "hi"}, {Role: "assistant", Text: empty}, {Role: "user", Text: empty}}
		t.Run("anthropic "+strconv.Quote(empty), func(t *testing.T) {
			srv := serve(t, reply{200, "anthropic_tools.json", ""})
			m, _ := newAnthropic(config.Model{Provider: "anthropic", Model: "model-id"}, "k")
			m.url = srv.URL
			if _, err := m.Chat(context.Background(), "", turns, tools); err != nil {
				t.Fatal(err)
			}
			msgs := srv.bodies[0]["messages"].([]any)
			if len(msgs) != 3 {
				t.Fatalf("%d messages, want 3 so the roles still alternate", len(msgs))
			}
			for _, m := range msgs {
				content, ok := m.(map[string]any)["content"].([]any)
				if !ok || len(content) == 0 {
					t.Fatalf("message %v has no content blocks", m)
				}
				for _, b := range content {
					b := b.(map[string]any)
					if text, ok := b["text"].(string); b["type"] == "text" && (!ok || strings.TrimSpace(text) == "") {
						t.Errorf("message %v has a blank text block", m)
					}
				}
			}
		})
		t.Run("openai "+strconv.Quote(empty), func(t *testing.T) {
			srv := serve(t, reply{200, "openai_tools.json", ""})
			m, _ := newOpenAI(config.Model{Provider: "openai", Model: "model-id"}, "k")
			m.url = srv.URL
			if _, err := m.Chat(context.Background(), "", turns, tools); err != nil {
				t.Fatal(err)
			}
			msgs := srv.bodies[0]["messages"].([]any)
			if len(msgs) != 3 {
				t.Fatalf("%d messages, want 3", len(msgs))
			}
			for _, m := range msgs {
				m := m.(map[string]any)
				if _, ok := m["content"].(string); !ok && m["tool_calls"] == nil {
					t.Errorf("message %v has no content and no tool calls", m)
				}
			}
		})
	}
}

func TestOpenAIChatCallsOnly(t *testing.T) {
	srv := serve(t, reply{200, "openai_tools.json", ""})
	m, _ := newOpenAI(config.Model{Provider: "openai", Model: "model-id"}, "k")
	m.url = srv.URL
	turns := []Turn{{Role: "user", Text: "hi"}, {Role: "assistant", Calls: []ToolCall{{ID: "c1", Name: "read"}}}, {Role: "tool", CallID: "c1", Text: "x"}}
	if _, err := m.Chat(context.Background(), "", turns, tools); err != nil {
		t.Fatal(err)
	}
	sent, _ := json.Marshal(srv.bodies[0]["messages"].([]any)[1])
	if want := `{"content":null,"role":"assistant","tool_calls":[{"function":{"arguments":"{}","name":"read"},"id":"c1","type":"function"}]}`; string(sent) != want {
		t.Errorf("assistant = %s, want %s", sent, want)
	}
}

func hangUpFirst(t *testing.T, ok string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	calls := new(atomic.Int32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
			return
		}
		_, _ = w.Write(read(t, ok))
	}))
	t.Cleanup(srv.Close)
	return srv, calls
}

func TestRetryDroppedConnection(t *testing.T) {
	srv, calls := hangUpFirst(t, "anthropic_ok.json")
	m, _ := newAnthropic(config.Model{Provider: "anthropic", Model: "m"}, "k")
	var waits []time.Duration
	m.url, m.wait = srv.URL, func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	if _, err := m.Complete(context.Background(), "", "p"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || !reflect.DeepEqual(waits, []time.Duration{time.Second}) {
		t.Errorf("calls = %d, waits = %v; want a second try after 1s", calls.Load(), waits)
	}
}

func TestNoRetryAfterCancel(t *testing.T) {
	srv, calls := hangUpFirst(t, "anthropic_ok.json")
	m, _ := newAnthropic(config.Model{Provider: "anthropic", Model: "m"}, "k")
	ctx, cancel := context.WithCancel(context.Background())
	m.url, m.wait = srv.URL, func(context.Context, time.Duration) error { return nil }
	m.http = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		cancel()
		return http.DefaultTransport.RoundTrip(r)
	})}
	if _, err := m.Complete(ctx, "", "p"); err == nil {
		t.Fatal("no error after cancel")
	}
	if calls.Load() > 1 {
		t.Errorf("calls = %d, want no retry once the context is done", calls.Load())
	}
}

func TestNoRetryOnTimeout(t *testing.T) {
	block := make(chan struct{})
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		<-block
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(block) })
	m, _ := newAnthropic(config.Model{Provider: "anthropic", Model: "m"}, "k")
	m.url, m.wait = srv.URL, func(context.Context, time.Duration) error { return nil }
	m.http.Timeout = 50 * time.Millisecond
	if _, err := m.Complete(context.Background(), "", "p"); err == nil {
		t.Fatal("no error after timeout")
	}
	if calls.Load() > 1 {
		t.Errorf("calls = %d, want one: a timed out reply is not sent again", calls.Load())
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
