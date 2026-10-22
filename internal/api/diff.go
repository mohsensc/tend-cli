package api

import (
	"cmp"
	"slices"
	"time"
)

type Line struct {
	Op     string // "METHOD /path"
	What   string
	Field  string
	Detail string // "integer -> object", "2027-03-01"
}

func (l Line) String() string {
	s := l.Op + ": " + l.What
	if l.Field != "" {
		s += " " + l.Field
	}
	if l.Detail != "" {
		s += " (" + l.Detail + ")"
	}
	return s
}

// Diff compares two versions of a spec over the operations in used only. A
// changed response field type is reported as such; the caller treats it as the
// old field being gone.
func Diff(old, cur *Doc, used []Usage) []Line {
	var res []Line
	for _, u := range used {
		key := u.Method + " " + u.Path
		o, n := op(old, key), op(cur, key)
		switch {
		case o == nil:
		case n == nil:
			res = append(res, Line{Op: key, What: "operation removed"})
		default:
			res = append(res, diffOp(key, o, n)...)
		}
	}
	slices.SortFunc(res, func(a, b Line) int {
		return cmp.Or(cmp.Compare(a.Op, b.Op), cmp.Compare(a.What, b.What), cmp.Compare(a.Field, b.Field))
	})
	return slices.Compact(res)
}

func op(d *Doc, key string) *Op {
	if d == nil {
		return nil
	}
	return d.Ops[key]
}

func diffOp(key string, o, n *Op) []Line {
	var res []Line
	add := func(what, field, detail string) {
		res = append(res, Line{Op: key, What: what, Field: field, Detail: detail})
	}
	if n.Deprecated && !o.Deprecated {
		add("operation deprecated", "", "")
	}
	switch {
	case n.Sunset.IsZero():
	case o.Sunset.IsZero():
		add("sunset added", "", day(n.Sunset))
	case !n.Sunset.Equal(o.Sunset):
		add("sunset changed", "", day(o.Sunset)+" -> "+day(n.Sunset))
	}

	before := map[string]Param{}
	for _, p := range o.Params {
		before[p.In+" "+p.Name] = p
	}
	after := map[string]Param{}
	for _, p := range n.Params {
		after[p.In+" "+p.Name] = p
		b, ok := before[p.In+" "+p.Name]
		switch {
		case p.In == "path":
		case p.Required && (!ok || !b.Required):
			add("required parameter added", p.Name, p.In)
		case p.Deprecated && ok && !b.Deprecated:
			add("parameter deprecated", p.Name, p.In)
		}
	}
	for k, p := range before {
		if _, ok := after[k]; !ok && p.In != "path" {
			add("parameter removed", p.Name, p.In)
		}
	}

	oreq, nreq := fields(o.Request), fields(n.Request)
	for name := range oreq {
		if _, ok := nreq[name]; !ok {
			add("request field removed", name, "")
		}
	}
	if n.Request != nil {
		for _, name := range n.Request.Required {
			if o.Request == nil || !slices.Contains(o.Request.Required, name) {
				add("required parameter added", name, "body")
			}
		}
	}

	ores, nres := fields(o.Response), fields(n.Response)
	for name, was := range ores {
		now, ok := nres[name]
		switch {
		case !ok:
			add("response field removed", name, "")
		case was.Type != "" && now.Type != "" && was.Type != now.Type:
			add("response field type changed", name, was.Type+" -> "+now.Type)
		case now.Deprecated && !was.Deprecated:
			add("response field deprecated", name, "")
		}
	}
	return res
}

func fields(s *Schema) map[string]*Schema {
	res := map[string]*Schema{}
	walk(s, func(name string, p *Schema) { res[name] = p })
	return res
}

func day(t time.Time) string {
	if t.Equal(t.Truncate(24 * time.Hour)) {
		return t.Format(time.DateOnly)
	}
	return t.Format(time.RFC3339)
}
