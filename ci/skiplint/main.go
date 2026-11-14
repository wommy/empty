// Command skiplint proves, statically, that ci/expected-skips.txt lists
// every simulator-only test of the real-pool packages: the tests that
// ci/check-skips.sh would otherwise catch only on the VM leg, a CI round
// trip later. ci/check-suites.sh runs it in the static job.
//
// usage: go run ./ci/skiplint -list ci/expected-skips.txt -leg vm <package>...
//
// It parses internal/<package>/*_test.go (go/parser, no type checking) and
// finds the tests that skip on a real pool: a Test function whose body
// calls a skip helper, outside any function literal but those passed to
// t.Run, whose body is the subtest's. The skip helpers are the seeds below
// and, transitively, every package-level function or method of the test
// files whose body calls one (failOn calls h.Sim). A subtest named by a
// string literal is TestX/<name>; any other subtest is TestX/*.
//
// It fails when
//   - a skipping test (or subtest) is not covered by an entry for <leg> and
//     its package: TestX needs TestX; TestX/sub needs TestX or TestX/sub;
//     TestX/* needs TestX or some TestX/... entry;
//   - an entry for <leg> names a package that is not given, or a top-level
//     test that its package no longer has.
//
// An entry with no skip found is only reported (the test may skip by a way
// this does not model); on the VM ci/check-skips.sh still proves it skips.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// seeds are the helpers that always skip on a real pool: requireSim(t)
// when ZFS_TEST_POOL is set, (*harness).Sim(t) when there is no simulator.
// harness.NFS is not one: the VM runs an NFS server, so it does not skip.
var seeds = []string{"requireSim", "harness.Sim"}

func main() {
	list := flag.String("list", "ci/expected-skips.txt", "the skip budget")
	leg := flag.String("leg", "vm", "the leg whose entries to check")
	root := flag.String("root", "internal", "the directory holding the packages")
	flag.Parse()
	errs, notes, err := check(*list, *leg, *root, flag.Args())
	if err != nil {
		fmt.Fprintln(os.Stderr, "skiplint:", err)
		os.Exit(2)
	}
	for _, n := range notes {
		fmt.Println("skiplint: note:", n)
	}
	for _, e := range errs {
		fmt.Println("::error::skiplint:", e)
	}
	if len(errs) > 0 {
		os.Exit(1)
	}
}

type entry struct {
	pkg, test string
	line      int
}

func check(list, leg, root string, pkgs []string) (errs, notes []string, err error) {
	if len(pkgs) == 0 {
		return nil, nil, fmt.Errorf("no package given")
	}
	entries, err := readList(list, leg)
	if err != nil {
		return nil, nil, err
	}
	byPkg := map[string][]entry{}
	given := map[string]bool{}
	for _, p := range pkgs {
		given[p] = true
	}
	for _, e := range entries {
		if !given[e.pkg] {
			errs = append(errs, fmt.Sprintf("%s:%d: %s %s %s: %s is not a real-pool package (ci/real-zfs-suites.txt)", list, e.line, leg, e.pkg, e.test, e.pkg))
			continue
		}
		byPkg[e.pkg] = append(byPkg[e.pkg], e)
	}
	total := 0
	for _, p := range pkgs {
		tests, skips, err := scan(filepath.Join(root, p))
		if err != nil {
			return nil, nil, err
		}
		total += len(skips)
		listed := map[string]bool{}
		for _, e := range byPkg[p] {
			listed[e.test] = true
			top, _, _ := strings.Cut(e.test, "/")
			if !tests[top] {
				errs = append(errs, fmt.Sprintf("%s:%d: %s %s %s: internal/%s has no func %s", list, e.line, leg, p, e.test, p, top))
			}
		}
		for _, s := range skips {
			if !covered(s.name, listed) {
				errs = append(errs, fmt.Sprintf("%s skips on a real pool (%s calls %s) but %s has no `%s %s %s` entry", s.name, s.pos, s.helper, list, leg, p, entryFor(s.name)))
			}
		}
		for _, e := range byPkg[p] {
			if top, _, _ := strings.Cut(e.test, "/"); tests[top] && !hit(e.test, skips) {
				notes = append(notes, fmt.Sprintf("%s:%d: %s %s: no call to a known skip helper found (ci/check-skips.sh proves it on the VM)", list, e.line, p, e.test))
			}
		}
	}
	if len(errs) == 0 {
		notes = append(notes, fmt.Sprintf("%d simulator-only tests or subtests in %s, each listed for %s in %s", total, strings.Join(pkgs, " "), leg, list))
	}
	return errs, notes, nil
}

func entryFor(name string) string {
	if top, ok := strings.CutSuffix(name, "/*"); ok {
		return top
	}
	return name
}

func covered(name string, listed map[string]bool) bool {
	top, sub, ok := strings.Cut(name, "/")
	if listed[top] {
		return true
	}
	if !ok {
		return false
	}
	if sub != "*" {
		return listed[name]
	}
	for l := range listed {
		if strings.HasPrefix(l, top+"/") {
			return true
		}
	}
	return false
}

// hit reports whether some found skip is the entry's test or within it.
func hit(entry string, skips []skip) bool {
	for _, s := range skips {
		top, _, _ := strings.Cut(s.name, "/")
		if s.name == entry || top == entry || (strings.HasSuffix(s.name, "/*") && strings.HasPrefix(entry, top+"/")) {
			return true
		}
	}
	return false
}

func readList(path, leg string) ([]entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []entry
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line, _, _ := strings.Cut(sc.Text(), "#")
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if len(f) != 3 {
			return nil, fmt.Errorf("%s:%d: want <leg> <package> <test>, got %q", path, n, sc.Text())
		}
		if f[0] == leg {
			out = append(out, entry{pkg: f[1], test: f[2], line: n})
		}
	}
	return out, sc.Err()
}

type skip struct {
	name   string // TestX, TestX/sub or TestX/*
	helper string
	pos    token.Position
}

// scan returns a package directory's top-level tests and the tests and
// subtests that skip on a real pool.
func scan(dir string) (map[string]bool, []skip, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		return nil, nil, err
	}
	if len(files) == 0 {
		return nil, nil, fmt.Errorf("%s: no test files", dir)
	}
	sort.Strings(files)
	fset := token.NewFileSet()
	// funcs by Go package (endpoint and endpoint_test share a binary but
	// not a namespace), then by name: f, or Recv.m for a method
	funcs := map[string]map[string]*ast.FuncDecl{}
	var all []*ast.File
	for _, fn := range files {
		f, err := parser.ParseFile(fset, fn, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, nil, err
		}
		all = append(all, f)
		m := funcs[f.Name.Name]
		if m == nil {
			m = map[string]*ast.FuncDecl{}
			funcs[f.Name.Name] = m
		}
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
				m[funcKey(fd)] = fd
			}
		}
	}
	// the skip helpers of each Go package, to a fixed point
	helpers := map[string]map[string]bool{}
	for pkg, m := range funcs {
		h := map[string]bool{}
		for _, s := range seeds {
			if _, ok := m[s]; ok {
				h[s] = true
			}
		}
		for changed := true; changed; {
			changed = false
			for k, fd := range m {
				if h[k] || strings.HasPrefix(k, "Test") {
					continue
				}
				found := false
				walk(fd.Body, "", func(string, string, token.Pos) { found = true }, h, nil)
				if found {
					h[k], changed = true, true
				}
			}
		}
		helpers[pkg] = h
	}
	tests := map[string]bool{}
	var skips []skip
	seen := map[string]bool{}
	for _, f := range all {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || fd.Body == nil || !isTest(fd) {
				continue
			}
			name := fd.Name.Name
			tests[name] = true
			walk(fd.Body, name, func(scope, helper string, pos token.Pos) {
				if !seen[scope] {
					seen[scope] = true
					skips = append(skips, skip{scope, helper, fset.Position(pos)})
				}
			}, helpers[f.Name.Name], fset)
		}
	}
	sort.Slice(skips, func(i, j int) bool { return skips[i].name < skips[j].name })
	return tests, skips, nil
}

func funcKey(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	t := fd.Recv.List[0].Type
	if s, ok := t.(*ast.StarExpr); ok {
		t = s.X
	}
	if ix, ok := t.(*ast.IndexExpr); ok {
		t = ix.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name + "." + fd.Name.Name
	}
	return fd.Name.Name
}

// isTest: func TestX(t *testing.T), X not starting with a lower-case letter
func isTest(fd *ast.FuncDecl) bool {
	n := fd.Name.Name
	if !strings.HasPrefix(n, "Test") || n == "TestMain" {
		return false
	}
	if len(n) > 4 && n[4] >= 'a' && n[4] <= 'z' {
		return false
	}
	p := fd.Type.Params.List
	return len(p) == 1 && len(p[0].Names) <= 1 && isTestingT(p[0].Type)
}

func isTestingT(e ast.Expr) bool {
	s, ok := e.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := s.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == "testing" && sel.Sel.Name == "T"
}

// walk calls found for every call to a helper in n, with the scope it
// skips: scope itself, or scope/<subtest> inside a t.Run function literal.
// Other function literals are not entered: a goroutine, a deferred call
// or a synctest bubble does not skip the test (a skip inside a synctest
// bubble reports PASS).
func walk(n ast.Node, scope string, found func(scope, helper string, pos token.Pos), helpers map[string]bool, fset *token.FileSet) {
	ast.Inspect(n, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.CallExpr:
			if sub, body, ok := subtest(n); ok && scope != "" {
				for _, a := range n.Args[:len(n.Args)-1] {
					walk(a, scope, found, helpers, fset)
				}
				walk(body, scope+"/"+sub, found, helpers, fset)
				return false
			}
			if h := callee(n, helpers); h != "" {
				found(scope, h, n.Pos())
			}
		}
		return true
	})
}

// subtest matches x.Run(name, func(t *testing.T) {...}) and returns the
// subtest's name (as go test reports it, for a string literal; else "*")
func subtest(c *ast.CallExpr) (string, *ast.BlockStmt, bool) {
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Run" || len(c.Args) != 2 {
		return "", nil, false
	}
	fl, ok := c.Args[1].(*ast.FuncLit)
	if !ok || len(fl.Type.Params.List) != 1 || !isTestingT(fl.Type.Params.List[0].Type) {
		return "", nil, false
	}
	name := "*"
	if bl, ok := c.Args[0].(*ast.BasicLit); ok && bl.Kind == token.STRING {
		if s, err := strconv.Unquote(bl.Value); err == nil {
			name = rewrite(s)
		}
	}
	return name, fl.Body, true
}

// rewrite approximates testing's subtest name rewriting: spaces become
// underscores, and unprintable runes are escaped (not modelled here)
func rewrite(s string) string {
	return strings.ReplaceAll(s, " ", "_")
}

// callee returns the helper a call names: f(...) for a helper func f, or
// x.m(...) for a helper method R.m of any receiver type R (no type
// checking: the receiver's type is not known, only the method's name).
func callee(c *ast.CallExpr, helpers map[string]bool) string {
	switch f := c.Fun.(type) {
	case *ast.Ident:
		if helpers[f.Name] {
			return f.Name
		}
	case *ast.SelectorExpr:
		for h := range helpers {
			if _, m, ok := strings.Cut(h, "."); ok && m == f.Sel.Name {
				return h
			}
		}
	}
	return ""
}
