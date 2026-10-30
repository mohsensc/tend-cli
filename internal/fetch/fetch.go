// Package fetch is the one HTTP client tend uses to talk to the outside world.
package fetch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
)

const (
	DefaultTTL = time.Hour
	retries    = 3
	maxWait    = time.Minute
	maxBody    = 256 << 20
)

type Client struct {
	HTTP  *http.Client             // 30s timeout per request
	Dir   string                   // cache dir; "" disables caching
	UA    string                   // "tend/<version> (+https://github.com/mohsensc/tend-cli)"
	Token func(host string) string // bearer token per host, nil for none
	TTL   time.Duration            // default 1h
	Now   func() time.Time

	replay string
	record string
	debug  bool
	wait   func(ctx context.Context, d time.Duration) error
}

func New(dir, version string) *Client {
	return &Client{
		HTTP:  &http.Client{Timeout: 30 * time.Second},
		Dir:   dir,
		UA:    "tend/" + version + " (+https://github.com/mohsensc/tend-cli)",
		TTL:   DefaultTTL,
		Now:   time.Now,
		debug: os.Getenv("TEND_LOG") == "debug",
	}
}

// Replay serves recorded responses from dir and fails loudly on anything unrecorded.
func Replay(dir string) *Client {
	return &Client{replay: dir, Now: time.Now}
}

// Record returns a copy of c that writes every response it sees into dir in the
// format Replay reads.
func Record(c *Client, dir string) *Client {
	cp := *c
	cp.record = dir
	return &cp
}

func Header(k, v string) core.FetchOpt {
	return func(o *core.FetchOpts) {
		if o.Header == nil {
			o.Header = map[string]string{}
		}
		o.Header[k] = v
	}
}

func TTL(d time.Duration) core.FetchOpt { return func(o *core.FetchOpts) { o.TTL = d } }

func NoCache() core.FetchOpt { return func(o *core.FetchOpts) { o.NoCache = true } }

// Timeout gives one request d, its body read too, in place of the client's
// own limit: a release archive takes longer than an API answer.
func Timeout(d time.Duration) core.FetchOpt { return func(o *core.FetchOpts) { o.Timeout = d } }

// Idempotent marks a Post as safe to send again after a 429, a 5xx or a
// dropped connection. Without it a Post is sent once: the server may have
// acted on it already.
func Idempotent() core.FetchOpt { return func(o *core.FetchOpts) { o.Idempotent = true } }

func (c *Client) Get(ctx context.Context, u string, opts ...core.FetchOpt) (*core.Response, error) {
	o := options(opts)
	if c.replay != "" {
		return c.replayed("GET", u, nil)
	}
	cache := c.Dir != "" && !o.NoCache
	var ent *entry
	if cache {
		ent = c.load(u)
		if ent != nil && c.now().Sub(ent.At) < c.ttl(o) {
			c.logf("GET %s cached", u)
			return c.recorded("GET", u, nil, ent.response())
		}
	}
	h := http.Header{}
	if ent != nil && ent.ETag != "" {
		h.Set("If-None-Match", ent.ETag)
	}
	res, err := c.do(ctx, "GET", u, nil, h, o)
	if err != nil {
		return nil, err
	}
	switch {
	case res.Status == http.StatusNotModified && ent != nil:
		ent.At = c.now()
		c.store(u, ent)
		res = ent.response()
	case res.Status == http.StatusOK && cache:
		c.store(u, &entry{URL: u, ETag: first(res.Header, "Etag"), At: c.now(), Status: res.Status, Header: res.Header, Body: res.Body})
	}
	return c.recorded("GET", u, nil, res)
}

// Post is never cached, and only retried when marked Idempotent.
func (c *Client) Post(ctx context.Context, u string, body []byte, opts ...core.FetchOpt) (*core.Response, error) {
	if c.replay != "" {
		return c.replayed("POST", u, body)
	}
	res, err := c.do(ctx, "POST", u, body, http.Header{}, options(opts))
	if err != nil {
		return nil, err
	}
	return c.recorded("POST", u, body, res)
}

func options(opts []core.FetchOpt) core.FetchOpts {
	var o core.FetchOpts
	for _, f := range opts {
		f(&o)
	}
	return o
}

func (c *Client) do(ctx context.Context, method, u string, body []byte, h http.Header, o core.FetchOpts) (*core.Response, error) {
	pu, err := url.Parse(u)
	if err != nil {
		return nil, err
	}
	h.Set("User-Agent", c.UA)
	if c.Token != nil {
		if tok := c.Token(pu.Hostname()); tok != "" {
			h.Set("Authorization", "Bearer "+tok)
		}
	}
	if body != nil {
		h.Set("Content-Type", "application/json")
	}
	for k, v := range o.Header {
		if v == "" {
			h.Del(k)
			continue
		}
		h.Set(k, v)
	}
	limit := retries
	if method != http.MethodGet && !o.Idempotent {
		limit = 0
	}
	timeouts := 0
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header = h.Clone()
		start := c.now()
		res, b, err := c.send(req, o.Timeout)
		if err != nil {
			if timedOut(err) {
				timeouts++
			}
			if ctx.Err() != nil || !transient(err) || attempt == limit || timeouts > 1 {
				return nil, attemptsError(method, u, err, attempt)
			}
			c.logf("%s %s: %v", method, u, err)
			if err := c.sleep(ctx, backoff(attempt, "", c.now())); err != nil {
				return nil, fmt.Errorf("%s %s: %w", method, u, err)
			}
			continue
		}
		if len(b) > maxBody {
			return nil, fmt.Errorf("%s %s: body larger than %d bytes", method, u, maxBody)
		}
		c.logf("%s %s %d %s %s", method, u, res.StatusCode, c.now().Sub(start).Round(time.Millisecond), redact(req.Header))
		if res.StatusCode != http.StatusTooManyRequests && res.StatusCode < 500 {
			return &core.Response{Status: res.StatusCode, Header: res.Header, Body: b}, nil
		}
		d := backoff(attempt, res.Header.Get("Retry-After"), c.now())
		if attempt == limit || d > maxWait {
			return nil, fmt.Errorf("%s %s: %s after %d attempts", method, u, res.Status, attempt+1)
		}
		if err := c.sleep(ctx, d); err != nil {
			return nil, fmt.Errorf("%s %s: %w", method, u, err)
		}
	}
}

func (c *Client) send(req *http.Request, timeout time.Duration) (*http.Response, []byte, error) {
	hc := c.client()
	if timeout > 0 {
		cp := *hc
		cp.Timeout = timeout
		hc = &cp
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, maxBody+1))
	if err != nil {
		return nil, nil, err
	}
	return res, b, nil
}

func attemptsError(method, u string, err error, attempt int) error {
	if attempt == 0 {
		return fmt.Errorf("%s %s: %w", method, u, err)
	}
	return fmt.Errorf("%s %s: %w after %d attempts", method, u, err, attempt+1)
}

func transient(err error) bool {
	var cert *tls.CertificateVerificationError
	var record tls.RecordHeaderError
	if errors.As(err, &cert) || errors.As(err, &record) {
		return false
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return dns.IsTimeout || dns.IsTemporary
	}
	var op *net.OpError
	return timedOut(err) || errors.As(err, &op) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EPIPE)
}

func timedOut(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// backoff doubles from one second unless the server says how long to wait.
func backoff(attempt int, retryAfter string, now time.Time) time.Duration {
	if retryAfter != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
		if t, err := http.ParseTime(retryAfter); err == nil {
			return max(t.Sub(now), 0)
		}
	}
	return time.Second << attempt
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if c.wait != nil {
		return c.wait(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) ttl(o core.FetchOpts) time.Duration {
	if o.TTL > 0 {
		return o.TTL
	}
	if c.TTL > 0 {
		return c.TTL
	}
	return DefaultTTL
}

func (c *Client) logf(format string, args ...any) {
	if c.debug {
		log.Printf("tend: http: "+format, args...)
	}
}

func redact(h http.Header) string {
	var parts []string
	for k, vs := range h {
		v := strings.Join(vs, ",")
		lk := strings.ToLower(k)
		if lk == "authorization" || strings.Contains(lk, "key") || strings.Contains(lk, "token") {
			v = "REDACTED"
		}
		parts = append(parts, k+"="+v)
	}
	slices.Sort(parts)
	return strings.Join(parts, " ")
}

type entry struct {
	URL    string              `json:"url"`
	ETag   string              `json:"etag,omitempty"`
	At     time.Time           `json:"at"`
	Status int                 `json:"status"`
	Header map[string][]string `json:"header,omitempty"`
	Body   []byte              `json:"body"`
}

func (e *entry) response() *core.Response {
	return &core.Response{Status: e.Status, Header: e.Header, Body: e.Body, Cached: true}
}

func key(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (c *Client) load(u string) *entry {
	b, err := os.ReadFile(filepath.Join(c.Dir, key(u)+".json"))
	if err != nil {
		return nil
	}
	var e entry
	if json.Unmarshal(b, &e) != nil || e.URL != u {
		return nil
	}
	return &e
}

// store is best effort: a cache that cannot be written only costs a refetch.
func (c *Client) store(u string, e *entry) {
	b, err := json.Marshal(e)
	if err == nil {
		err = writeFile(filepath.Join(c.Dir, key(u)+".json"), b)
	}
	if err != nil {
		c.logf("cache %s: %v", u, err)
	}
}

// Prune drops cached responses written longer ago than age. The cache holds
// one file per URL ever fetched, whole registry documents among them.
func Prune(dir string, age time.Duration, now time.Time) error {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range ents {
		info, err := e.Info()
		if err != nil || e.IsDir() || now.Sub(info.ModTime()) <= age {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func writeFile(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

func first(h map[string][]string, k string) string {
	if vs := http.Header(h).Values(k); len(vs) > 0 {
		return vs[0]
	}
	return ""
}
