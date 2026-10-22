package scan

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"strconv"
	"strings"
)

type goLang struct{}

func (goLang) Lang() string { return "go" }

func (goLang) Match(p string) bool { return strings.ToLower(path.Ext(p)) == ".go" }

// scan reads what it can. A file that does not parse still yields the
// imports and sites before the error, and the error is returned as a note.
func (goLang) scan(p string, src []byte) (*file, error) {
	f := newFile(p, "go", src, goSyntax)
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, p, src, parser.SkipObjectResolution)
	if af != nil {
		w := &goWalk{f: f, tf: fset.File(af.Pos()), done: map[*ast.SelectorExpr]bool{}}
		w.imports(af)
		w.aliases(af)
		ast.Inspect(af, w.visit)
	}
	f.finish()
	return f, err
}

type goWalk struct {
	f    *file
	tf   *token.File
	done map[*ast.SelectorExpr]bool // selectors already recorded as the callee of a call
}

func (w *goWalk) off(p token.Pos) int { return w.tf.Offset(p) }

func (w *goWalk) imports(af *ast.File) {
	f := w.f
	for _, spec := range af.Imports {
		p, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		off := w.off(spec.Path.Pos())
		name := goName(p)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		switch name {
		case "_":
			f.addImport(off, p, "", nil)
		case ".":
			f.addImport(off, p, ".", nil)
			f.open = append(f.open, p)
		default:
			f.addImport(off, p, name, nil)
			f.bind(name, 0, false, false, p)
		}
	}
	if len(f.open) == 0 {
		return
	}
	for _, d := range af.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil {
				f.decl[d.Name.Name] = true
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					f.decl[s.Name.Name] = true
				case *ast.ValueSpec:
					for _, id := range s.Names {
						f.decl[id.Name] = true
					}
				}
			}
		}
	}
}

// goName guesses the package name of an import path: the last element, minus
// a major version, a gopkg.in .vN suffix and a go- or -go affix.
func goName(p string) string {
	elems := strings.Split(p, "/")
	name := elems[len(elems)-1]
	if len(elems) > 1 && isMajor(name) {
		name = elems[len(elems)-2]
	}
	if i := strings.LastIndex(name, ".v"); i > 0 && isMajor(name[i+1:]) {
		name = name[:i]
	}
	name = strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(name, "go-"), "-go"), ".go")
	return strings.Map(func(r rune) rune {
		if r < 0x80 && !isIdent(byte(r)) || r == '$' {
			return -1
		}
		return r
	}, name)
}

func isMajor(s string) bool {
	if len(s) < 2 || s[0] != 'v' {
		return false
	}
	_, err := strconv.Atoi(s[1:])
	return err == nil
}

// aliases binds names whose type or value comes from an import: parameters
// and variables of type pkg.T, struct fields of that type (as members, so
// s.client.Do() counts), and c := pkg.NewClient(...), resp, err :=
// http.Get(u) or &pkg.T{}. A local holds until the end of its block.
func (w *goWalk) aliases(af *ast.File) {
	var stack []ast.Node
	ast.Inspect(af, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		end := scopeEnd(w.tf, stack)
		switch n := n.(type) {
		case *ast.AssignStmt:
			for k, lhs := range n.Lhs {
				var rhs ast.Expr
				switch {
				case len(n.Rhs) == len(n.Lhs):
					rhs = n.Rhs[k]
				case len(n.Rhs) == 1 && k == 0:
					rhs = n.Rhs[0]
				}
				if id, ok := lhs.(*ast.Ident); ok && rhs != nil {
					w.local(id, end, w.value(rhs))
				}
			}
		case *ast.ValueSpec:
			for k, id := range n.Names {
				quals := w.typ(n.Type)
				if quals == nil && k < len(n.Values) {
					quals = w.value(n.Values[k])
				}
				w.local(id, end, quals)
			}
		case *ast.FuncType:
			if n.Params != nil && body(stack) {
				for _, fd := range n.Params.List {
					for _, id := range fd.Names {
						w.local(id, end, w.typ(fd.Type))
					}
				}
			}
		case *ast.StructType:
			for _, fd := range n.Fields.List {
				if quals := w.typ(fd.Type); quals != nil {
					for _, id := range fd.Names {
						w.f.bind(id.Name, 0, true, true, quals...)
					}
				}
			}
		}
		stack = append(stack, n)
		return true
	})
}

func (w *goWalk) local(id *ast.Ident, end int, quals []string) {
	if quals != nil && id.Name != "_" {
		w.f.bindScoped(id.Name, w.off(id.Pos()), end, quals...)
	}
}

// body tells whether the node being visited is the signature of a function
// with a body, rather than of a func type or an interface method.
func body(stack []ast.Node) bool {
	if len(stack) == 0 {
		return false
	}
	switch stack[len(stack)-1].(type) {
	case *ast.FuncDecl, *ast.FuncLit:
		return true
	}
	return false
}

// scopeEnd returns where the innermost scope on the stack ends, 0 at file level.
func scopeEnd(tf *token.File, stack []ast.Node) int {
	for k := len(stack) - 1; k >= 0; k-- {
		switch stack[k].(type) {
		case *ast.BlockStmt, *ast.CaseClause, *ast.CommClause, *ast.IfStmt, *ast.ForStmt,
			*ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.FuncDecl, *ast.FuncLit:
			return tf.Offset(stack[k].End())
		}
	}
	return 0
}

// typ returns the import a type expression such as *pkg.T or pkg.T[X] names.
func (w *goWalk) typ(e ast.Expr) []string {
	for {
		switch x := e.(type) {
		case *ast.StarExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.IndexListExpr:
			e = x.X
		case *ast.SelectorExpr:
			return w.pkg(x.X)
		default:
			return nil
		}
	}
}

// value returns the import a call or composite literal comes from: what
// pkg.F(...) returns is a value of pkg, and reading its members is using pkg.
func (w *goWalk) value(e ast.Expr) []string {
	for {
		switch x := e.(type) {
		case *ast.UnaryExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		case *ast.CompositeLit:
			return w.typ(x.Type)
		case *ast.CallExpr:
			if sel, ok := callee(x.Fun).(*ast.SelectorExpr); ok {
				return w.pkg(sel.X)
			}
			return nil
		default:
			return nil
		}
	}
}

// pkg returns the import an identifier names, if it is an import binding.
func (w *goWalk) pkg(e ast.Expr) []string {
	id, ok := e.(*ast.Ident)
	if !ok {
		return nil
	}
	if r := w.f.lookup(id.Name, w.off(id.Pos()), false); r != nil && !r.alias {
		return r.quals
	}
	return nil
}

func (w *goWalk) visit(n ast.Node) bool {
	switch n := n.(type) {
	case *ast.CallExpr:
		fun := callee(n.Fun)
		if w.site(fun, true) {
			if sel, ok := fun.(*ast.SelectorExpr); ok {
				w.done[sel] = true
			}
		}
	case *ast.SelectorExpr:
		if !w.done[n] {
			w.site(n, false)
		}
		ast.Inspect(base(n), w.visit)
		return false
	}
	return true
}

// site records the chain e if it starts at an import binding, a local alias,
// a struct field alias somewhere along it, or a dot-imported name.
func (w *goWalk) site(e ast.Expr, call bool) bool {
	var ids []*ast.Ident
	for {
		if sel, ok := e.(*ast.SelectorExpr); ok {
			ids = append(ids, sel.Sel)
			e = ast.Unparen(sel.X)
			continue
		}
		if id, ok := e.(*ast.Ident); ok {
			ids = append(ids, id)
		}
		break
	}
	for a, b := 0, len(ids)-1; a < b; a, b = a+1, b-1 {
		ids[a], ids[b] = ids[b], ids[a]
	}
	if len(ids) == 0 {
		return false
	}
	start, quals := w.start(ids, e)
	if quals == nil {
		return false
	}
	var chain []string
	for _, id := range ids[start:] {
		chain = append(chain, id.Name)
	}
	if len(chain) < 2 && !call {
		return false
	}
	w.f.addSite(w.off(ids[start].Pos()), chain, call, quals)
	return true
}

// start finds where in ids a chain begins and what it is rooted at. e is
// what ids hang off: the root identifier itself, or some other expression.
func (w *goWalk) start(ids []*ast.Ident, e ast.Expr) (int, []string) {
	f := w.f
	if root, ok := e.(*ast.Ident); ok {
		if r := f.lookup(root.Name, w.off(root.Pos()), false); r != nil {
			return 0, r.quals
		}
		if len(f.open) > 0 && ast.IsExported(root.Name) && !f.decl[root.Name] {
			return 0, f.open
		}
	}
	for k, id := range ids {
		if k == 0 && e == ast.Expr(id) {
			continue
		}
		if r := f.lookup(id.Name, w.off(id.Pos()), true); r != nil {
			return k, r.quals
		}
	}
	return 0, nil
}

func callee(e ast.Expr) ast.Expr {
	e = ast.Unparen(e)
	switch x := e.(type) {
	case *ast.IndexExpr:
		return ast.Unparen(x.X)
	case *ast.IndexListExpr:
		return ast.Unparen(x.X)
	}
	return e
}

// base returns the expression a selector chain hangs off.
func base(e ast.Expr) ast.Expr {
	for {
		sel, ok := ast.Unparen(e).(*ast.SelectorExpr)
		if !ok {
			return e
		}
		e = sel.X
	}
}
