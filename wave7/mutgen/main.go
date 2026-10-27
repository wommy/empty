// mutgen enumerates single-site mutants of Go source files as byte-range
// replacements (JSON). Usage: mutgen <root> > mutants.json
package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
)

type Mut struct {
	ID     int    `json:"id"`
	Bucket string `json:"bucket"`
	File   string `json:"file"`
	Line   int    `json:"line"`
	Op     string `json:"op"`
	Desc   string `json:"desc"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
	Repl   string `json:"repl"`
}

type target struct {
	bucket, file string
	fn           []string // if set: only these funcs (and methods by name); else line range
	from, to     int      // 1-based inclusive line range (0 = all)
}

var targets = []target{
	{"plan.go", "internal/endpoint/plan.go", nil, 0, 0},
	{"reconcile.go", "internal/endpoint/reconcile.go", nil, 1, 296},
	{"table.go", "internal/endpoint/table.go", nil, 1, 273},
	{"errors.go", "internal/endpoint/errors.go", nil, 0, 0},
	{"errors.go", "internal/endpoint/endpoint.go", []string{"zfsErr", "parentUnavailable"}, 0, 0},
	{"props.go", "internal/endpoint/props.go", nil, 0, 0},
	{"zfs/errors.go", "internal/zfs/errors.go", nil, 0, 0},
	{"zfs/zfs.go:parseColumns", "internal/zfs/zfs.go", []string{"parseColumns"}, 0, 0},
}

var codeNames = map[string]bool{"Aborted": true, "AlreadyExists": true, "DeadlineExceeded": true, "FailedPrecondition": true,
	"InvalidArgument": true, "NotFound": true, "OutOfRange": true, "ResourceExhausted": true, "Unavailable": true, "Unimplemented": true}

func main() {
	root := os.Args[1]
	var all []Mut
	for _, t := range targets {
		path := filepath.Join(root, t.file)
		src, _ := os.ReadFile(path)
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
		if err != nil {
			panic(err)
		}
		text := func(n ast.Node) string { return string(src[fset.Position(n.Pos()).Offset:fset.Position(n.End()).Offset]) }
		off := func(p token.Pos) int { return fset.Position(p).Offset }
		line := func(p token.Pos) int { return fset.Position(p).Line }
		in := func(p token.Pos) bool {
			l := line(p)
			if t.from != 0 && (l < t.from || l > t.to) {
				return false
			}
			return true
		}
		var cur []Mut
		add := func(n ast.Node, op, desc string, s, e int, repl string) {
			cur = append(cur, Mut{Bucket: t.bucket, File: t.file, Line: line(n.Pos()), Op: op, Desc: desc, Start: s, End: e, Repl: repl})
		}
		walk := func(root ast.Node) {
			// parent tracking for const decls
			ast.Inspect(root, func(n ast.Node) bool {
				if n == nil || !in(n.Pos()) {
					return true
				}
				switch x := n.(type) {
				case *ast.GenDecl:
					if x.Tok == token.CONST || x.Tok == token.IMPORT {
						return false
					}
				case *ast.BinaryExpr:
					os_, oe := off(x.OpPos), off(x.OpPos)+len(x.Op.String())
					flip := map[token.Token]string{token.EQL: "!=", token.NEQ: "==", token.LSS: "<=", token.LEQ: "<", token.GTR: ">=", token.GEQ: ">"}
					if r, ok := flip[x.Op]; ok {
						op := "EQ_FLIP"
						if x.Op != token.EQL && x.Op != token.NEQ {
							op = "REL_FLIP"
						}
						add(x, op, text(x)+"  =>  "+x.Op.String()+" becomes "+r, os_, oe, r)
					}
					if x.Op == token.LAND || x.Op == token.LOR {
						r, neutral := "||", "true"
						if x.Op == token.LOR {
							r, neutral = "&&", "false"
						}
						add(x, "ANDOR_SWAP", x.Op.String()+" becomes "+r+" in: "+text(x), os_, oe, r)
						add(x, "DROP_OPERAND", "operand `"+text(x.X)+"` replaced by "+neutral, off(x.X.Pos()), off(x.X.End()), neutral)
						add(x, "DROP_OPERAND", "operand `"+text(x.Y)+"` replaced by "+neutral, off(x.Y.Pos()), off(x.Y.End()), neutral)
					}
				case *ast.CallExpr:
					if s, ok := x.Fun.(*ast.SelectorExpr); ok && (s.Sel.Name == "OwnedVolume" || s.Sel.Name == "OwnedSnapshot") {
						add(x, "OWNED_TRUE", "ownership call `"+text(x)+"` replaced by true", off(x.Pos()), off(x.End()), "true")
					}
					if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "errorf" && len(x.Args) > 0 {
						if a, ok := x.Args[0].(*ast.Ident); ok && codeNames[a.Name] {
							add(x, "CODE_INTERNAL", "errorf code "+a.Name+" becomes Internal", off(a.Pos()), off(a.End()), "Internal")
						}
					}
				case *ast.AssignStmt:
					for _, r := range x.Rhs {
						if a, ok := r.(*ast.Ident); ok && codeNames[a.Name] {
							add(x, "CODE_INTERNAL", "assigned code "+a.Name+" becomes Internal", off(a.Pos()), off(a.End()), "Internal")
						}
						if a, ok := r.(*ast.Ident); ok && (a.Name == "true" || a.Name == "false") {
							n := map[string]string{"true": "false", "false": "true"}[a.Name]
							add(x, "BOOL_FLIP", text(x)+"  =>  "+n, off(a.Pos()), off(a.End()), n)
						}
					}
				case *ast.KeyValueExpr:
					if a, ok := x.Value.(*ast.Ident); ok {
						if codeNames[a.Name] {
							add(x, "CODE_INTERNAL", "field code "+a.Name+" becomes Internal", off(a.Pos()), off(a.End()), "Internal")
						}
						if a.Name == "true" || a.Name == "false" {
							n := map[string]string{"true": "false", "false": "true"}[a.Name]
							add(x, "BOOL_FLIP", "field `"+text(x)+"` flipped to "+n, off(a.Pos()), off(a.End()), n)
						}
					}
				case *ast.ReturnStmt:
					for _, r := range x.Results {
						if a, ok := r.(*ast.Ident); ok && (a.Name == "true" || a.Name == "false") {
							n := map[string]string{"true": "false", "false": "true"}[a.Name]
							add(x, "BOOL_FLIP", text(x)+"  =>  return "+n, off(a.Pos()), off(a.End()), n)
						}
					}
				case *ast.IfStmt:
					add(x.Cond, "IF_NEGATE", "if "+text(x.Cond)+"  =>  if !(...)", off(x.Cond.Pos()), off(x.Cond.End()), "!("+text(x.Cond)+")")
					// guard deletion: last stmt of the body is a return/continue/break
					if n := len(x.Body.List); n > 0 {
						last := x.Body.List[n-1]
						switch s := last.(type) {
						case *ast.ReturnStmt:
							add(s, "GUARD_DELETE", "guard `if "+text(x.Cond)+"`: `"+text(s)+"` deleted", off(s.Pos()), off(s.End()), "_ = 0")
						case *ast.BranchStmt:
							add(s, "GUARD_DELETE", "guard `if "+text(x.Cond)+"`: `"+text(s)+"` deleted", off(s.Pos()), off(s.End()), "_ = 0")
						}
					}
				}
				return true
			})
		}
		// patterns table in zfs/errors.go
		for _, d := range f.Decls {
			switch dd := d.(type) {
			case *ast.FuncDecl:
				if t.fn != nil {
					ok := false
					for _, n := range t.fn {
						ok = ok || n == dd.Name.Name
					}
					if !ok {
						continue
					}
				}
				walk(dd)
			case *ast.GenDecl:
				if t.fn != nil || dd.Tok != token.VAR {
					if t.fn == nil && dd.Tok == token.VAR {
					} else if t.fn != nil || dd.Tok != token.VAR {
						continue
					}
				}
				for _, sp := range dd.Specs {
					vs := sp.(*ast.ValueSpec)
					if len(vs.Names) == 1 && vs.Names[0].Name == "patterns" {
						cl := vs.Values[0].(*ast.CompositeLit)
						for _, el := range cl.Elts {
							e := el.(*ast.CompositeLit)
							k := e.Elts[0].(*ast.Ident)
							txt := text(e.Elts[1])
							add(e, "PATTERN_KIND", text(e)+": kind -> KindUnknown", off(k.Pos()), off(k.End()), "KindUnknown")
							add(e, "PATTERN_DROP", "pattern entry "+k.Name+" "+txt+" removed", off(e.Pos()), off(e.End()), "{KindUnknown, \"\\x00never\"}")
						}
					}
				}
				if t.file != "internal/zfs/errors.go" {
					walk(dd)
				}
			}
		}
		all = append(all, cur...)
	}
	sort.SliceStable(all, func(i, j int) bool { return false })
	for i := range all {
		all[i].ID = i
	}
	json.NewEncoder(os.Stdout).Encode(all)
}
