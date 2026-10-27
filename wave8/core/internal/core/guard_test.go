// Package core_test holds the construction guard for the core packages.
package core_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoHandleLiterals: outside packages world and op, no code (tests
// included) writes a world.<Type>{...} or op.<Kind>{...} literal. Handles are minted by world only;
// unexported fields stop non-empty literals, this stops the empty ones.
func TestNoHandleLiterals(t *testing.T) {
	err := filepath.Walk(".", func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasPrefix(p, "world/") || strings.HasPrefix(p, "op/") {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if cl, ok := n.(*ast.CompositeLit); ok {
				if sel, ok := cl.Type.(*ast.SelectorExpr); ok {
					if id, ok := sel.X.(*ast.Ident); ok && (id.Name == "world" && sel.Sel.Name != "Local" || id.Name == "op" && sel.Sel.Name != "KV" && sel.Sel.Name != "Plan") {
						t.Errorf("%s: %s.%s{} literal outside its package", p, id.Name, sel.Sel.Name)
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestDestroySnapOnlyInDeleteVolume (SNP-06): the plain snapshot destroy is
// right only where a racing clone must fail it. DeleteSnapshot and
// Reconcile build op.NewDeferDestroySnap.
func TestDestroySnapOnlyInDeleteVolume(t *testing.T) {
	err := filepath.Walk(".", func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasPrefix(p, "op/") {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
		if err != nil {
			return err
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fd, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "NewDestroySnap" {
					if p != "decide/decide.go" || fd.Name.Name != "deleteVolume" {
						t.Errorf("%s: %s calls op.NewDestroySnap", p, fd.Name.Name)
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
