// Package model talks to the optional model provider used for code migrations.
package model

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/config"
	"github.com/mohsensc/tend-cli/internal/core"
)

const (
	timeout  = 10 * time.Minute
	retries  = 3
	maxWait  = time.Minute
	maxBody  = 32 << 20
	replyMax = 8000 // tokens in one reply

	noReply = "(no reply)"
	noText  = "(empty)"
)

// presets are providers that speak the chat completions API at their own
// address, so tend.yml can name them in one line. Each takes its key from
// TEND_MODEL_KEY as well as its own variable; one with no variable of its own
// needs no key, but sends TEND_MODEL_KEY when it is set.
var presets = map[string]struct{ url, key string }{
	"groq":   {"https://api.groq.com/openai/v1", "GROQ_API_KEY"},
	"ollama": {"http://localhost:11434/v1", ""},
}

// New returns the configured model, or nil when the provider is "none".
// getenv is os.Getenv outside tests.
func New(cfg config.Model, getenv func(string) string) (core.Model, error) {
	if p, ok := presets[cfg.Provider]; ok {
		cfg.BaseURL = cmp.Or(cfg.BaseURL, p.url)
		key, err := apiKey(cfg, p.key, getenv)
		if err != nil && (p.key != "" || cfg.KeyEnv != "") {
			return nil, err
		}
		m, err := newOpenAI(cfg, key)
		if err != nil {
			return nil, err
		}
		return m, nil
	}
	switch cfg.Provider {
	case "", "none":
		return nil, nil
	case "anthropic":
		key, err := apiKey(cfg, "ANTHROPIC_API_KEY", getenv)
		if err != nil {
			return nil, err
		}
		m, err := newAnthropic(cfg, key)
		if err != nil {
			return nil, err
		}
		return m, nil
	case "openai":
		key, err := apiKey(cfg, "OPENAI_API_KEY", getenv)
		if err != nil {
			return nil, err
		}
		m, err := newOpenAI(cfg, key)
		if err != nil {
			return nil, err
		}
		return m, nil
	case "command":
		m, err := newCommand(cfg)
		if err != nil {
			return nil, err
		}
		return m, nil
	}
	return nil, fmt.Errorf("model: unknown provider %q (want none, anthropic, openai, groq, ollama or command)", cfg.Provider)
}

// apiKey looks in the variable the config names, then TEND_MODEL_KEY, then the
// provider's usual one.
func apiKey(cfg config.Model, usual string, getenv func(string) string) (string, error) {
	names := []string{cfg.KeyEnv, "TEND_MODEL_KEY", usual}
	for _, name := range names {
		if name == "" {
			continue
		}
		if key := strings.TrimSpace(getenv(name)); key != "" {
			return key, nil
		}
	}
	if cfg.KeyEnv != "" {
		return "", fmt.Errorf("model: no API key: %s is empty (also tried TEND_MODEL_KEY and %s)", cfg.KeyEnv, usual)
	}
	return "", fmt.Errorf("model: no API key: set %s or TEND_MODEL_KEY", usual)
}

// poster is one JSON POST with retries on 429, 5xx, a dropped connection, and
// a tool call the model sent malformed.
type poster struct {
	url    string
	header map[string]string
	http   *http.Client
	wait   func(context.Context, time.Duration) error
}

func newPoster(url string, header map[string]string) poster {
	return poster{url: url, header: header, http: &http.Client{Timeout: timeout}}
}

func (p *poster) post(ctx context.Context, body any) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range p.header {
			req.Header.Set(k, v)
		}
		resp, err := p.http.Do(req)
		var data []byte
		if err == nil {
			data, err = io.ReadAll(io.LimitReader(resp.Body, maxBody))
			resp.Body.Close()
		}
		if err != nil {
			if ctx.Err() != nil || timedOut(err) || attempt == retries {
				return nil, err
			}
			if err := p.sleep(ctx, delay(attempt, "", time.Now())); err != nil {
				return nil, err
			}
			continue
		}
		if resp.StatusCode/100 == 2 {
			return data, nil
		}
		retry := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 || badCall(resp.StatusCode, data)
		d := delay(attempt, cmp.Or(resp.Header.Get("Retry-After"), tryAgain(data)), time.Now())
		if !retry || attempt == retries || d > maxWait {
			return nil, httpError(resp.StatusCode, data)
		}
		if err := p.sleep(ctx, d); err != nil {
			return nil, err
		}
	}
}

func timedOut(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func (p *poster) sleep(ctx context.Context, d time.Duration) error {
	if p.wait != nil {
		return p.wait(ctx, d)
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

var tryAgainIn = regexp.MustCompile(`(?i)try again in ((?:\d+(?:\.\d+)?(?:h|ms|m|s))+)`)

// tryAgain reads the wait a rate limit names in its message, "Please try
// again in 3.77s", in whole seconds, rounded up.
func tryAgain(data []byte) string {
	m := tryAgainIn.FindSubmatch(data)
	if m == nil {
		return ""
	}
	d, err := time.ParseDuration(string(m[1]))
	if err != nil {
		return ""
	}
	return strconv.Itoa(int((d + time.Second - 1) / time.Second))
}

// delay honors Retry-After, in seconds or as a date, else doubles from one second.
func delay(attempt int, retryAfter string, now time.Time) time.Duration {
	retryAfter = strings.TrimSpace(retryAfter)
	if n, err := strconv.Atoi(retryAfter); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(retryAfter); err == nil {
		return max(t.Sub(now), 0)
	}
	return time.Second << attempt
}

// badCall reports a 400 for output the model got wrong, such as a call to a
// tool that does not exist. Groq sends it with the failed generation, and the
// same request usually passes on a second try.
func badCall(status int, data []byte) bool {
	var e struct {
		Error struct {
			Code   string          `json:"code"`
			Failed json.RawMessage `json:"failed_generation"`
		} `json:"error"`
	}
	if status != http.StatusBadRequest || json.Unmarshal(data, &e) != nil {
		return false
	}
	return e.Error.Code == "tool_use_failed" || e.Error.Code == "output_parse_failed" || len(e.Error.Failed) > 0
}

// httpError reads the {"error": {"message": ...}} body both APIs send.
func httpError(status int, data []byte) error {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	msg := strings.TrimSpace(string(data))
	if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
		msg = e.Error.Message
	}
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return fmt.Errorf("status %d: %s", status, msg)
}
