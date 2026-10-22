package vet

import (
	"context"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/mohsensc/tend-cli/internal/api"
	"github.com/mohsensc/tend-cli/internal/core"
)

// Field-shaped tokens on a line: obj.name (not a call), ["name"], .get("name"), name: / "name":.
var fieldToks = []*regexp.Regexp{
	regexp.MustCompile(`\.([A-Za-z_]\w*)\b(\s*\()?`),
	regexp.MustCompile(`\[\s*["']([A-Za-z_]\w*)["']\s*\]`),
	regexp.MustCompile(`\.get\(\s*["']([A-Za-z_]\w*)["']`),
	regexp.MustCompile(`(?:^|[{,(\s])["']?([A-Za-z_]\w*)["']?\s*:[^:]`),
}

// fields: does every field name this patch introduced exist in the spec, and
// is the removed field gone from the files it touched?
func fields(ctx context.Context, p *pass) ([]core.Finding, string) {
	ch := p.in.Change
	if ch.API == nil || ch.API.Field == "" {
		return nil, na
	}
	a := ch.API
	if p.in.Index == nil {
		return nil, "no scan index"
	}
	touched := p.changed()
	var res []core.Finding
	if strings.Contains(a.What, "removed") || strings.Contains(a.What, "deprecated") {
		for _, s := range p.in.Index.Fields(a.Field, touched) {
			res = append(res, finding("fields", s.File, s.Line, "flagged", "`%s` is still used here (%s)", a.Field, a.What))
		}
	}
	if p.in.Spec == nil {
		sortFindings(res)
		return res, ""
	}
	op := p.in.Spec.Ops[strings.ToUpper(a.Method)+" "+a.Path]
	if op == nil {
		op = p.in.Spec.Find(a.Method, a.Path)
	}
	if op == nil {
		sortFindings(res)
		return res, ""
	}
	known := schemaNames(op)
	var ok []string
	for _, c := range introduced(p, a.Field) {
		if !p.isField(c) {
			continue
		}
		if known[c.name] {
			ok = append(ok, c.name)
			continue
		}
		res = append(res, finding("fields", c.file, c.line, "flagged", "`%s` is not a field of %s %s in spec %s", c.name, op.Method, op.Path, p.in.Spec.Version))
	}
	var said []string
	if len(ok) > 0 {
		path, verb := fieldPath(op, ok), "exists"
		if strings.Contains(path, ", ") {
			verb = "exist"
		}
		in := "request"
		if props(op.Response)[ok[0]] {
			in = "response"
		}
		said = append(said, path+" "+verb+" in the "+in+" schema")
	}
	if len(res) == 0 && (strings.Contains(a.What, "removed") || strings.Contains(a.What, "deprecated")) {
		said = append(said, a.Field+" no longer referenced")
	}
	p.note = strings.Join(said, "; ")
	sortFindings(res)
	return res, ""
}

// fieldPath writes the names a patch reads as one path when each is a
// property of the one before, total.amount, else as a list.
func fieldPath(op *api.Op, list []string) string {
	list = slices.Compact(slices.Clone(list))
	if len(list) > 1 {
		for _, root := range []*api.Schema{op.Response, op.Request} {
			if at := holder(root, list[0]); at != nil && under(at, list) {
				return strings.Join(list, ".")
			}
		}
	}
	return strings.Join(list, ", ")
}

// holder finds the schema, at any depth, that has a property called name.
func holder(s *api.Schema, name string) *api.Schema {
	seen := map[*api.Schema]bool{}
	queue := []*api.Schema{s}
	for len(queue) > 0 {
		at := queue[0]
		queue = queue[1:]
		if at == nil || seen[at] {
			continue
		}
		seen[at] = true
		if at.Props[name] != nil {
			return at
		}
		queue = append(queue, at.Items)
		for _, k := range slices.Sorted(maps.Keys(at.Props)) {
			queue = append(queue, at.Props[k])
		}
	}
	return nil
}

// under reports whether path, read from at, runs through nested properties.
func under(at *api.Schema, path []string) bool {
	for _, name := range path {
		if at != nil && at.Items != nil {
			at = at.Items
		}
		if at == nil || at.Props[name] == nil {
			return false
		}
		at = at.Props[name]
	}
	return true
}

type candidate struct {
	file string
	line int
	name string
}

// introduced lists the field-shaped names on added lines of hunks that removed
// old, minus the names those removed lines already had.
func introduced(p *pass, old string) []candidate {
	var res []candidate
	for _, f := range p.files {
		for _, h := range f.Hunks {
			before := map[string]bool{}
			hit := false
			for _, l := range h.Lines {
				if l.Op == '-' {
					hit = hit || hasWord(l.Text, old)
					for _, n := range fieldNames(l.Text) {
						before[n] = true
					}
				}
			}
			if !hit {
				continue
			}
			for _, l := range h.Lines {
				if l.Op != '+' {
					continue
				}
				for _, n := range fieldNames(l.Text) {
					if !before[n] && n != old {
						res = append(res, candidate{h.File, l.New, n})
					}
				}
			}
		}
	}
	return res
}

func fieldNames(s string) []string {
	var res []string
	for i, re := range fieldToks {
		for _, m := range re.FindAllStringSubmatch(s, -1) {
			if i == 0 && m[2] != "" {
				continue
			}
			if !slices.Contains(res, m[1]) {
				res = append(res, m[1])
			}
		}
	}
	return res
}

// isField asks the index whether the name really is a field occurrence on that
// line, not something inside a comment or a string the lexer knows better about.
func (p *pass) isField(c candidate) bool {
	for _, s := range p.in.Index.Fields(c.name, []string{c.file}) {
		if s.Line == c.line {
			return true
		}
	}
	return false
}

// schemaNames collects every property name in the op's request and response
// schemas, at any depth, plus its parameter names.
func schemaNames(op *api.Op) map[string]bool {
	res := props(op.Request)
	for n := range props(op.Response) {
		res[n] = true
	}
	for _, prm := range op.Params {
		res[prm.Name] = true
	}
	return res
}

// props collects every property name in a schema, at any depth.
func props(s *api.Schema) map[string]bool {
	res := map[string]bool{}
	seen := map[*api.Schema]bool{}
	var walk func(s *api.Schema)
	walk = func(s *api.Schema) {
		if s == nil || seen[s] {
			return
		}
		seen[s] = true
		for name, sub := range s.Props {
			res[name] = true
			walk(sub)
		}
		walk(s.Items)
	}
	walk(s)
	return res
}
