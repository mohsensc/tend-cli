package source

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/mohsensc/tend-cli/internal/core"
)

// fake is a core.Fetcher that asks a function for every answer.
type fake struct {
	h func(method, url string, body []byte) *core.Response

	mu    sync.Mutex
	calls []call
}

type call struct {
	Method, URL string
	Body        []byte
	Opts        core.FetchOpts
}

func (f *fake) Get(ctx context.Context, url string, opts ...core.FetchOpt) (*core.Response, error) {
	return f.do("GET", url, nil, opts)
}

func (f *fake) Post(ctx context.Context, url string, body []byte, opts ...core.FetchOpt) (*core.Response, error) {
	return f.do("POST", url, body, opts)
}

func (f *fake) do(method, url string, body []byte, opts []core.FetchOpt) (*core.Response, error) {
	var o core.FetchOpts
	for _, fn := range opts {
		fn(&o)
	}
	f.mu.Lock()
	f.calls = append(f.calls, call{method, url, body, o})
	f.mu.Unlock()
	if res := f.h(method, url, body); res != nil {
		return res, nil
	}
	return nil, fmt.Errorf("fake: unexpected %s %s", method, url)
}

func (f *fake) urls(method string) []string {
	var res []string
	for _, c := range f.calls {
		if c.Method == method {
			res = append(res, c.URL)
		}
	}
	return res
}

func ok(v any) *core.Response {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return &core.Response{Status: 200, Body: b}
}

func text(s string) *core.Response { return &core.Response{Status: 200, Body: []byte(s)} }

func status(n int) *core.Response { return &core.Response{Status: n} }

// routes answers GETs from a map keyed by URL and 404s nothing else.
func routes(m map[string]*core.Response) *fake {
	return &fake{h: func(method, url string, body []byte) *core.Response {
		return m[method+" "+url]
	}}
}
