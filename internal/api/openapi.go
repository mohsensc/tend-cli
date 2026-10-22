package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/mohsensc/tend-cli/internal/core"
)

type Doc struct {
	Version string             // info.version
	Base    string             // path of servers[0].url, already part of every Op.Path
	Ops     map[string]*Op     // key "METHOD /path"
	Schemas map[string]*Schema // components.schemas, by name
	At      time.Time          // when this snapshot was taken; zero for an upstream spec
	Missing []string           // $refs that point nowhere
}

type Op struct {
	Method, Path string
	Summary      string // the summary and the description, at most 600 bytes
	Deprecated   bool
	Sunset       time.Time // from x-sunset, the catalog's sunset key, or x-deprecated-at
	Params       []Param   // path, query and header
	Request      *Schema   // first JSON (else form) request body schema, resolved
	Response     *Schema   // first 2xx JSON response schema, resolved
}

type Param struct {
	Name, In             string
	Required, Deprecated bool
}

// Schema is resolved: $ref chased, allOf merged, oneOf and anyOf folded into
// Props. Recursive schemas share pointers, so walk them with a depth bound.
type Schema struct {
	Props      map[string]*Schema
	Required   []string
	Deprecated bool
	Type       string
	Items      *Schema
}

const maxText = 600

var methods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

// Load parses an OpenAPI 3.x document in JSON or YAML.
func Load(b []byte) (*Doc, error) { return load(b, "") }

type rawDoc struct {
	OpenAPI string `json:"openapi"`
	Swagger string `json:"swagger"`
	Info    struct {
		Version json.RawMessage `json:"version"`
		At      string          `json:"x-tend-snapshot"`
	} `json:"info"`
	Servers []struct {
		URL string `json:"url"`
	} `json:"servers"`
	Paths      map[string]map[string]json.RawMessage `json:"paths"`
	Components struct {
		Schemas       map[string]json.RawMessage `json:"schemas"`
		Parameters    map[string]json.RawMessage `json:"parameters"`
		RequestBodies map[string]json.RawMessage `json:"requestBodies"`
		Responses     map[string]json.RawMessage `json:"responses"`
	} `json:"components"`
}

type rawSchema struct {
	Ref        string                     `json:"$ref"`
	Type       json.RawMessage            `json:"type"`
	Properties map[string]json.RawMessage `json:"properties"`
	Required   []string                   `json:"required"`
	Deprecated bool                       `json:"deprecated"`
	Items      json.RawMessage            `json:"items"`
	AllOf      []json.RawMessage          `json:"allOf"`
	OneOf      []json.RawMessage          `json:"oneOf"`
	AnyOf      []json.RawMessage          `json:"anyOf"`
}

type rawParam struct {
	Ref        string `json:"$ref"`
	Name       string `json:"name"`
	In         string `json:"in"`
	Required   bool   `json:"required"`
	Deprecated bool   `json:"deprecated"`
}

type rawBody struct {
	Ref     string `json:"$ref"`
	Content map[string]struct {
		Schema json.RawMessage `json:"schema"`
	} `json:"content"`
}

type loader struct {
	raw     rawDoc
	sunset  []string
	schemas map[string]*Schema
	missing map[string]bool
}

// load reads a spec; sunset is the catalog's dotted key for removal dates, such
// as "x-github.removalDate".
func load(b []byte, sunset string) (*Doc, error) {
	b, err := toJSON(b)
	if err != nil {
		return nil, err
	}
	l := &loader{schemas: map[string]*Schema{}, missing: map[string]bool{}}
	if sunset != "" {
		l.sunset = strings.Split(sunset, ".")
	}
	if err := lenient(json.Unmarshal(b, &l.raw)); err != nil {
		return nil, fmt.Errorf("openapi: %w", err)
	}
	if l.raw.Swagger != "" {
		return nil, fmt.Errorf("openapi: swagger %s is not supported, only OpenAPI 3", l.raw.Swagger)
	}
	if !strings.HasPrefix(l.raw.OpenAPI, "3.") {
		return nil, errors.New("openapi: not an OpenAPI 3 document")
	}
	d := &Doc{Version: l.version(), Base: l.base(), Ops: map[string]*Op{}}
	if l.raw.Info.At != "" {
		d.At, _ = time.Parse(time.RFC3339, l.raw.Info.At)
	}
	for path, item := range l.raw.Paths {
		var shared []json.RawMessage
		decode(item["parameters"], &shared)
		for _, m := range methods {
			if raw, ok := item[m]; ok {
				op := l.op(raw, shared)
				op.Method = strings.ToUpper(m)
				op.Path = d.Base + path
				d.Ops[op.Method+" "+op.Path] = op
			}
		}
	}
	for name := range l.raw.Components.Schemas {
		l.named(name)
	}
	d.Schemas = l.schemas
	for ref := range l.missing {
		d.Missing = append(d.Missing, ref)
	}
	slices.Sort(d.Missing)
	return d, nil
}

// toJSON converts YAML to JSON so one decoder serves both. JSON passes through,
// which keeps the big specs (GitHub, Cloudflare) off the slower YAML path.
func toJSON(b []byte) ([]byte, error) {
	if t := bytes.TrimSpace(b); len(t) > 0 && t[0] == '{' {
		return b, nil
	}
	var v any
	if err := yaml.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("openapi: %w", err)
	}
	res, err := json.Marshal(plain(v))
	if err != nil {
		return nil, fmt.Errorf("openapi: %w", err)
	}
	return res, nil
}

// plain turns YAML's map[any]any (from keys like 200) into JSON-friendly maps.
func plain(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			t[k] = plain(x)
		}
		return t
	case map[any]any:
		m := make(map[string]any, len(t))
		for k, x := range t {
			m[fmt.Sprint(k)] = plain(x)
		}
		return m
	case []any:
		for i, x := range t {
			t[i] = plain(x)
		}
		return t
	}
	return v
}

// lenient drops type errors, so a spec with the odd field of the wrong type
// still loads: what fits is kept, the rest is left zero.
func lenient(err error) error {
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) {
		return nil
	}
	return err
}

func decode(b json.RawMessage, v any) bool {
	return len(b) > 0 && lenient(json.Unmarshal(b, v)) == nil
}

// version is info.version, which YAML specs sometimes leave unquoted.
func (l *loader) version() string {
	var s string
	if json.Unmarshal(l.raw.Info.Version, &s) == nil {
		return s
	}
	return string(l.raw.Info.Version)
}

func (l *loader) base() string {
	if len(l.raw.Servers) == 0 {
		return ""
	}
	u, err := url.Parse(l.raw.Servers[0].URL)
	if err != nil || strings.Contains(u.Path, "{") {
		return ""
	}
	return strings.TrimRight(u.Path, "/")
}

func (l *loader) op(raw json.RawMessage, shared []json.RawMessage) *Op {
	var m map[string]json.RawMessage
	decode(raw, &m)
	var summary, desc string
	decode(m["summary"], &summary)
	decode(m["description"], &desc)
	// A deprecated operation's description is usually where the provider says
	// what to use instead, so it travels with the summary.
	text := strings.TrimSpace(summary)
	if desc = strings.TrimSpace(desc); desc != "" && desc != text {
		text = strings.TrimSpace(text + "\n\n" + desc)
	}
	op := &Op{Summary: core.Clip(text, maxText)}
	decode(m["deprecated"], &op.Deprecated)
	op.Sunset = date(m["x-sunset"])
	if op.Sunset.IsZero() && l.sunset != nil {
		op.Sunset = date(dig(m, l.sunset))
	}
	if op.Sunset.IsZero() {
		op.Sunset = date(m["x-deprecated-at"])
	}

	var own []json.RawMessage
	decode(m["parameters"], &own)
	seen := map[string]bool{}
	for _, raw := range append(own, shared...) {
		p, ok := l.param(raw)
		if !ok || seen[p.In+" "+p.Name] {
			continue
		}
		seen[p.In+" "+p.Name] = true
		op.Params = append(op.Params, p)
	}
	slices.SortFunc(op.Params, func(a, b Param) int { return strings.Compare(a.In+" "+a.Name, b.In+" "+b.Name) })

	op.Request = l.body(m["requestBody"], "requestBodies")
	var codes map[string]json.RawMessage
	decode(m["responses"], &codes)
	keys := make([]string, 0, len(codes))
	for k := range codes {
		if strings.HasPrefix(k, "2") {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	for _, k := range keys {
		if op.Response = l.body(codes[k], "responses"); op.Response != nil {
			break
		}
	}
	return op
}

func (l *loader) param(raw json.RawMessage) (Param, bool) {
	var p rawParam
	decode(raw, &p)
	if p.Ref != "" {
		raw = l.component(p.Ref, "parameters", l.raw.Components.Parameters)
		p = rawParam{}
		decode(raw, &p)
	}
	return Param{Name: p.Name, In: p.In, Required: p.Required, Deprecated: p.Deprecated}, p.Name != ""
}

// body picks the schema of a request body or a response: JSON first, then forms,
// because some providers (Stripe, Twilio) only take form-encoded requests.
func (l *loader) body(raw json.RawMessage, kind string) *Schema {
	var b rawBody
	decode(raw, &b)
	if ref := b.Ref; ref != "" {
		src := l.raw.Components.RequestBodies
		if kind == "responses" {
			src = l.raw.Components.Responses
		}
		b = rawBody{}
		decode(l.component(ref, kind, src), &b)
	}
	var best string
	for mt := range b.Content {
		if rank(mt) > 0 && (best == "" || rank(mt) > rank(best) || rank(mt) == rank(best) && mt < best) {
			best = mt
		}
	}
	if best == "" {
		return nil
	}
	return l.schema(b.Content[best].Schema)
}

func rank(mt string) int {
	mt, _, _ = strings.Cut(mt, ";")
	switch {
	case mt == "application/json":
		return 4
	case strings.HasSuffix(mt, "+json"):
		return 3
	case mt == "application/x-www-form-urlencoded":
		return 2
	case mt == "multipart/form-data":
		return 1
	}
	return 0
}

// component returns the raw value of a local #/components/<kind>/<name> ref.
func (l *loader) component(ref, kind string, src map[string]json.RawMessage) json.RawMessage {
	name, ok := strings.CutPrefix(ref, "#/components/"+kind+"/")
	if ok {
		if raw, ok := src[unescape(name)]; ok {
			return raw
		}
	}
	l.missing[ref] = true
	return nil
}

func unescape(s string) string {
	if u, err := url.PathUnescape(s); err == nil {
		s = u
	}
	return strings.NewReplacer("~1", "/", "~0", "~").Replace(s)
}

// named resolves a component schema once. It is registered before it is filled,
// so a schema that refers back to itself gets the same pointer and the walk ends.
func (l *loader) named(name string) *Schema {
	if s, ok := l.schemas[name]; ok {
		return s
	}
	raw, ok := l.raw.Components.Schemas[name]
	if !ok {
		l.missing["#/components/schemas/"+name] = true
		return nil
	}
	s := &Schema{}
	l.schemas[name] = s
	if res := l.schema(raw); res != nil {
		*s = *res
	}
	return s
}

func (l *loader) schema(raw json.RawMessage) *Schema {
	var r rawSchema
	if !decode(raw, &r) {
		return nil
	}
	if r.Ref != "" {
		name, ok := strings.CutPrefix(r.Ref, "#/components/schemas/")
		if !ok {
			l.missing[r.Ref] = true
			return nil
		}
		res := l.named(unescape(name))
		if res != nil && r.Deprecated && !res.Deprecated {
			cp := *res
			cp.Deprecated = true
			res = &cp
		}
		return res
	}
	s := &Schema{Type: kind(r.Type), Required: r.Required, Deprecated: r.Deprecated}
	if len(r.Items) > 0 {
		s.Items = l.schema(r.Items)
	}
	for name, p := range r.Properties {
		if ps := l.schema(p); ps != nil {
			s.prop(name, ps)
		}
	}
	for _, part := range r.AllOf {
		s.merge(l.schema(part), true)
	}
	for _, part := range append(r.OneOf, r.AnyOf...) {
		s.merge(l.schema(part), false)
	}
	return s
}

func (s *Schema) prop(name string, p *Schema) {
	if s.Props == nil {
		s.Props = map[string]*Schema{}
	}
	s.Props[name] = p
}

// merge folds part into s. allOf parts all apply, so their required fields do
// too; oneOf and anyOf only contribute the fields that may appear.
func (s *Schema) merge(part *Schema, all bool) {
	if part == nil {
		return
	}
	for name, p := range part.Props {
		if _, ok := s.Props[name]; !ok {
			s.prop(name, p)
		}
	}
	if all {
		for _, r := range part.Required {
			if !slices.Contains(s.Required, r) {
				s.Required = append(s.Required, r)
			}
		}
	}
	if s.Type == "" {
		s.Type = part.Type
	}
	if s.Items == nil {
		s.Items = part.Items
	}
}

// kind reads type as a string (3.0) or a list (3.1, where "null" means nullable).
func kind(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var list []string
	decode(raw, &list)
	for _, t := range list {
		if t != "null" {
			return t
		}
	}
	return ""
}

func dig(m map[string]json.RawMessage, keys []string) json.RawMessage {
	raw := m[keys[0]]
	for _, k := range keys[1:] {
		var next map[string]json.RawMessage
		if !decode(raw, &next) {
			return nil
		}
		raw = next[k]
	}
	return raw
}

func date(raw json.RawMessage) time.Time {
	var s string
	if !decode(raw, &s) || s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	t, _ := time.Parse(time.DateOnly, s)
	return t
}

// Find matches a concrete path against the templates: "/v1/orders/123" finds
// "/v1/orders/{id}". Full URLs and query strings are fine; the path may leave
// out the server base path. An empty method matches any, GET first. When
// several templates match, the one with the most literal text wins.
func (d *Doc) Find(method, path string) *Op { return d.find(method, path, false) }

// find with loose compares literal segments without case, "_" and "-", for
// SDK packages named like "paymentintent".
func (d *Doc) find(method, path string, loose bool) *Op {
	if d == nil {
		return nil
	}
	path = clean(path)
	res := d.match(strings.ToUpper(method), path, loose)
	if res == nil && d.Base != "" && path != d.Base && !strings.HasPrefix(path, d.Base+"/") {
		res = d.match(strings.ToUpper(method), d.Base+path, loose)
	}
	return res
}

func clean(s string) string {
	if _, rest, ok := strings.Cut(s, "://"); ok {
		s = "/"
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			s = rest[i:]
		}
	}
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	if !strings.HasPrefix(s, "/") {
		s = "/" + s
	}
	if len(s) > 1 {
		s = strings.TrimRight(s, "/")
	}
	return s
}

func (d *Doc) match(method, path string, loose bool) *Op {
	segs := strings.Split(path, "/")
	var best *Op
	top := -1
	for _, op := range d.Ops {
		if method != "" && op.Method != method {
			continue
		}
		score, ok := fit(strings.Split(op.Path, "/"), segs, loose)
		if !ok {
			continue
		}
		if score > top || score == top && better(op, best, method) {
			best, top = op, score
		}
	}
	return best
}

// better breaks ties so Find does not depend on map order.
func better(a, b *Op, method string) bool {
	if method == "" && (a.Method == "GET") != (b.Method == "GET") {
		return a.Method == "GET"
	}
	return a.Method+" "+a.Path < b.Method+" "+b.Path
}

// fit reports whether the template segments match the path segments, and how
// many literal bytes they share.
func fit(tmpl, segs []string, loose bool) (int, bool) {
	if len(tmpl) != len(segs) {
		return 0, false
	}
	score := 0
	for i, t := range tmpl {
		parts := split(t)
		s := segs[i]
		if loose {
			for j := range parts {
				parts[j].lit = fold(parts[j].lit)
			}
			s = fold(s)
		}
		if !glob(parts, s) {
			return 0, false
		}
		for _, p := range parts {
			score += len(p.lit)
		}
	}
	return score, true
}

type part struct {
	lit   string
	param bool
}

// split cuts a template segment into literals and {params}. Braces nest, so a
// param like {id:[0-9]{3}} stays one param; an unclosed one is literal text.
func split(seg string) []part {
	var res []part
	depth, start := 0, 0
	for i := 0; i < len(seg); i++ {
		switch seg[i] {
		case '{':
			if depth == 0 {
				if i > start {
					res = append(res, part{lit: seg[start:i]})
				}
				start = i
			}
			depth++
		case '}':
			if depth == 0 {
				continue
			}
			depth--
			if depth == 0 {
				res = append(res, part{param: true})
				start = i + 1
			}
		}
	}
	if start < len(seg) {
		res = append(res, part{lit: seg[start:]})
	}
	return res
}

// glob matches s against the parts; a param takes at least one byte.
func glob(parts []part, s string) bool {
	if len(parts) == 0 {
		return s == ""
	}
	p := parts[0]
	if !p.param {
		rest, ok := strings.CutPrefix(s, p.lit)
		return ok && glob(parts[1:], rest)
	}
	for i := 1; i <= len(s); i++ {
		if glob(parts[1:], s[i:]) {
			return true
		}
	}
	return false
}

func fold(s string) string {
	return strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(s))
}

// depth bounds how far into a schema tend looks for fields, and how much of it
// snapshots keep. Each property and each array level counts as one.
const depth = 3

// walk calls fn for every property within depth, shallowest first. A name seen
// at a shallower level wins.
func walk(s *Schema, fn func(name string, p *Schema)) {
	type node struct {
		s     *Schema
		level int
	}
	seen := map[string]bool{}
	queue := []node{{s, 0}}
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		if curr.s == nil || curr.level >= depth {
			continue
		}
		if curr.s.Items != nil {
			queue = append(queue, node{curr.s.Items, curr.level + 1})
		}
		for _, name := range slices.Sorted(maps.Keys(curr.s.Props)) {
			p := curr.s.Props[name]
			if !seen[name] {
				seen[name] = true
				fn(name, p)
			}
			queue = append(queue, node{p, curr.level + 1})
		}
	}
}

// hash covers what tend models of the operations, so two specs that differ
// only in prose hash the same.
// trimSpec writes d as a small OpenAPI document holding only the ops named (all
// when ops is nil) and their schemas cut at depth. Load reads it back to the same
// model, which makes it both the snapshot format and the way recorded specs are
// kept small.
func trimSpec(d *Doc, ops []string) []byte {
	info := map[string]any{"title": "trimmed", "version": d.Version}
	if !d.At.IsZero() {
		info["x-tend-snapshot"] = d.At.UTC().Format(time.RFC3339)
	}
	doc := map[string]any{"openapi": "3.1.0", "info": info}
	if d.Base != "" {
		doc["servers"] = []any{map[string]any{"url": d.Base}}
	}
	paths := map[string]any{}
	for k, op := range d.Ops {
		if ops != nil && !slices.Contains(ops, k) {
			continue
		}
		p := strings.TrimPrefix(op.Path, d.Base)
		item, _ := paths[p].(map[string]any)
		if item == nil {
			item = map[string]any{}
			paths[p] = item
		}
		item[strings.ToLower(op.Method)] = opJSON(op)
	}
	doc["paths"] = paths
	b, _ := json.MarshalIndent(doc, "", " ")
	return append(b, '\n')
}

func opJSON(op *Op) map[string]any {
	m := map[string]any{"responses": map[string]any{}}
	if op.Summary != "" {
		m["summary"] = op.Summary
	}
	if op.Deprecated {
		m["deprecated"] = true
	}
	if !op.Sunset.IsZero() {
		m["x-sunset"] = op.Sunset.UTC().Format(time.RFC3339)
	}
	var params []any
	for _, p := range op.Params {
		params = append(params, map[string]any{"name": p.Name, "in": p.In, "required": p.Required, "deprecated": p.Deprecated})
	}
	if params != nil {
		m["parameters"] = params
	}
	if op.Request != nil {
		m["requestBody"] = media(op.Request)
	}
	if op.Response != nil {
		m["responses"] = map[string]any{"200": media(op.Response)}
	}
	return m
}

func media(s *Schema) map[string]any {
	return map[string]any{"content": map[string]any{"application/json": map[string]any{"schema": schemaJSON(s, 0)}}}
}

func schemaJSON(s *Schema, level int) map[string]any {
	m := map[string]any{}
	if s.Type != "" {
		m["type"] = s.Type
	}
	if s.Deprecated {
		m["deprecated"] = true
	}
	if level >= depth {
		return m
	}
	if s.Items != nil {
		m["items"] = schemaJSON(s.Items, level+1)
	}
	if len(s.Props) > 0 {
		props := map[string]any{}
		for name, p := range s.Props {
			props[name] = schemaJSON(p, level+1)
		}
		m["properties"] = props
	}
	if len(s.Required) > 0 {
		req := slices.Clone(s.Required)
		slices.Sort(req)
		m["required"] = req
	}
	return m
}
