package fetch

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/mohsensc/tend-cli/internal/core"
)

// kept lists the response headers worth committing; the rest is volatile or private.
var kept = []string{"Content-Type", "Etag", "Last-Modified", "Link", "Location", "Deprecation", "Sunset", "Retry-After", "Www-Authenticate", "X-Ratelimit-Remaining", "X-Ratelimit-Reset"}

type recording struct {
	URL     string              `json:"url"`
	Status  int                 `json:"status"`
	Header  map[string][]string `json:"header,omitempty"`
	Body    string              `json:"body,omitempty"`
	BodyB64 string              `json:"body_b64,omitempty"`
}

// RecordName is the file a request is recorded under: <METHOD>_<host>_<sha256(url+body)[:12]>.json.
func RecordName(method, u string, body []byte) string {
	host := "nohost"
	if pu, err := url.Parse(u); err == nil && pu.Host != "" {
		host = strings.NewReplacer(":", "_", "/", "_").Replace(pu.Host)
	}
	return method + "_" + host + "_" + key(u + string(body))[:12] + ".json"
}

func (c *Client) replayed(method, u string, body []byte) (*core.Response, error) {
	name := RecordName(method, u, body)
	b, err := os.ReadFile(filepath.Join(c.replay, name))
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("fetch: no recording for %s %s (want %s in %s; record with TEND_RECORD=1)", method, u, name, c.replay)
	}
	if err != nil {
		return nil, err
	}
	var r recording
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("fetch: %s: %w", name, err)
	}
	if r.URL != u {
		return nil, fmt.Errorf("fetch: %s records %s, not %s", name, r.URL, u)
	}
	res := &core.Response{Status: r.Status, Header: r.Header, Body: []byte(r.Body)}
	if r.BodyB64 != "" {
		res.Body, err = base64.StdEncoding.DecodeString(r.BodyB64)
		if err != nil {
			return nil, fmt.Errorf("fetch: %s: %w", name, err)
		}
	}
	c.logf("%s %s %d replayed", method, u, res.Status)
	return res, nil
}

func (c *Client) recorded(method, u string, body []byte, res *core.Response) (*core.Response, error) {
	if c.record == "" {
		return res, nil
	}
	r := recording{URL: u, Status: res.Status}
	for _, k := range kept {
		if vs := http.Header(res.Header).Values(k); len(vs) > 0 {
			if r.Header == nil {
				r.Header = map[string][]string{}
			}
			r.Header[k] = vs
		}
	}
	if utf8.Valid(res.Body) {
		r.Body = string(res.Body)
	} else {
		r.BodyB64 = base64.StdEncoding.EncodeToString(res.Body)
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writeFile(filepath.Join(c.record, RecordName(method, u, body)), append(b, '\n')); err != nil {
		return nil, fmt.Errorf("fetch: record %s: %w", u, err)
	}
	return res, nil
}
