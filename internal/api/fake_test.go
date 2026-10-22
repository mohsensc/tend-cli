package api

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/mohsensc/tend-cli/internal/core"
)

// index is a core.ScanIndex over hand-written sites.
type index struct {
	uses   map[string][]core.Site // import name -> member chains rooted at it
	urls   []core.URLUse
	fields []core.Site // Chain[0] is the field name
}

func (x *index) Uses(imports []string) []core.Site {
	var res []core.Site
	for _, name := range imports {
		res = append(res, x.uses[name]...)
	}
	return res
}

func (x *index) Fields(name string, files []string) []core.Site {
	var res []core.Site
	for _, s := range x.fields {
		if s.Chain[0] == name && (files == nil || slices.Contains(files, s.File)) {
			res = append(res, s)
		}
	}
	return res
}

func (x *index) Endpoints(hosts []string) []core.URLUse {
	var res []core.URLUse
	for _, u := range x.urls {
		host := u.Host
		if pu, err := url.Parse(u.URL); err == nil && pu.Host != "" {
			host = pu.Host
		}
		if slices.Contains(hosts, host) {
			res = append(res, u)
		}
	}
	return res
}

func (x *index) URLs() []core.URLUse              { return x.urls }
func (x *index) Imports(pkg string) []core.Import { return nil }
func (x *index) Files() []string                  { return nil }

func field(file string, line int, name string) core.Site {
	return core.Site{File: file, Line: line, Chain: []string{name}, Text: `x["` + name + `"]`}
}

func call(file string, line int, chain string) core.Site {
	return core.Site{File: file, Line: line, Chain: strings.Split(chain, "."), Call: true}
}

// fetcher serves bodies by URL; anything else is a 404.
type fetcher map[string]string

func (f fetcher) Get(ctx context.Context, u string, opts ...core.FetchOpt) (*core.Response, error) {
	var o core.FetchOpts
	for _, fn := range opts {
		fn(&o)
	}
	if o.TTL != specTTL {
		panic("specs should be fetched with the spec TTL")
	}
	if b, ok := f[u]; ok {
		return &core.Response{Status: 200, Body: []byte(b)}, nil
	}
	return &core.Response{Status: 404}, nil
}

func (f fetcher) Post(ctx context.Context, u string, body []byte, opts ...core.FetchOpt) (*core.Response, error) {
	panic("specs are never posted")
}

// pages serves bodies by URL, a 404 for anything else, and keeps what was asked.
type pages struct {
	body  map[string]string
	mu    sync.Mutex
	asked []string
}

func (p *pages) Get(ctx context.Context, u string, opts ...core.FetchOpt) (*core.Response, error) {
	p.mu.Lock()
	p.asked = append(p.asked, u)
	p.mu.Unlock()
	if b, ok := p.body[u]; ok {
		return &core.Response{Status: 200, Body: []byte(b)}, nil
	}
	return &core.Response{Status: 404}, nil
}

func (p *pages) Post(ctx context.Context, u string, body []byte, opts ...core.FetchOpt) (*core.Response, error) {
	panic("discovery never posts")
}
