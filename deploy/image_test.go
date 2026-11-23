package deploy

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// fromLine is a Dockerfile FROM: the image reference, and its stage name.
var fromLine = regexp.MustCompile(`(?m)^FROM\s+(\S+)(?:\s+AS\s+(\S+))?\s*$`)

// The zfs-csi image (Dockerfile at the repository root; built nightly,
// never in the PR budget, ADR 0005) is reproducible from the repository:
// every base pinned by digest, the binary static and stamped through the
// -X path of the real version variable, and run as uid 0 (ADR 0006, ADR
// 0008). A rename of internal/version or of its variable fails here, not
// as an image that silently reports "dev".
func TestDockerfile(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile(filepath.Join("..", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	df := string(b)

	froms := fromLine.FindAllStringSubmatch(df, -1)
	if len(froms) < 2 {
		t.Fatalf("Dockerfile has %d FROM lines, want a build stage and a runtime stage", len(froms))
	}
	digest := regexp.MustCompile(`^[^@\s]+:[^@\s]+@sha256:[0-9a-f]{64}$`)
	for _, m := range froms {
		if !digest.MatchString(m[1]) {
			t.Errorf("FROM %s: want <image>:<tag>@sha256:<digest> (a tag can be re-pushed)", m[1])
		}
	}

	// the golang base is go.mod's toolchain, so the image builds with the
	// compiler CI tests with
	gomod, err := os.ReadFile(filepath.Join("..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	module := regexp.MustCompile(`(?m)^module\s+(\S+)$`).FindStringSubmatch(string(gomod))
	toolchain := regexp.MustCompile(`(?m)^toolchain\s+go(\S+)$`).FindStringSubmatch(string(gomod))
	if module == nil || toolchain == nil {
		t.Fatal("go.mod: no module or toolchain line")
	}
	if build := froms[0][1]; !strings.Contains(build, "golang:"+toolchain[1]+"-") && !strings.Contains(build, "golang:"+toolchain[1]+"@") {
		t.Errorf("build stage FROM %s: want golang:%s (go.mod toolchain)", build, toolchain[1])
	}

	// -X names the version variable that exists: a string var Version in
	// internal/version, under this module's path
	x := regexp.MustCompile(`-X\s+([^\s"=]+)\.([A-Za-z_]\w*)=\$\{?VERSION\}?`).FindStringSubmatch(df)
	if x == nil {
		t.Fatal("Dockerfile: no -X <package>.<var>=${VERSION} in the build's -ldflags")
	}
	if want := module[1] + "/internal/version"; x[1] != want {
		t.Errorf("-X package %s, want %s", x[1], want)
	}
	if !stringVar(t, filepath.Join("..", "internal", "version", "version.go"), x[2]) {
		t.Errorf("-X %s.%s: internal/version has no package-level string var %s", x[1], x[2], x[2])
	}
	if !regexp.MustCompile(`(?m)^ARG VERSION\b`).MatchString(df) {
		t.Error("Dockerfile has no ARG VERSION: the version cannot be set with --build-arg")
	}

	for _, want := range []string{"CGO_ENABLED=0", "-trimpath", "./cmd/zfs-csi"} {
		if !strings.Contains(df, want) {
			t.Errorf("Dockerfile does not build with %s", want)
		}
	}

	// the runtime stage: the binary the build wrote, run as uid 0
	runtime := df[strings.LastIndex(df, froms[len(froms)-1][0]):]
	cp := regexp.MustCompile(`(?m)^COPY\s+--from=\S+\s+/out/zfs-csi\s+(\S+)$`).FindStringSubmatch(runtime)
	if cp == nil {
		t.Fatal("runtime stage does not copy /out/zfs-csi from the build stage")
	}
	if !strings.Contains(runtime, `ENTRYPOINT ["`+cp[1]+`"]`) {
		t.Errorf("runtime ENTRYPOINT is not %s, the binary it copies", cp[1])
	}
	if !regexp.MustCompile(`(?m)^USER 0(:0)?$`).MatchString(runtime) {
		t.Error("runtime stage does not run as USER 0: zfs-csi dials the endpoint's 0600 root socket (ADR 0008)")
	}

	ignore, err := os.ReadFile(filepath.Join("..", ".dockerignore"))
	if err != nil {
		t.Fatalf("no .dockerignore: the whole checkout, .git included, would be the build context: %v", err)
	}
	if !strings.Contains(string(ignore), "\n*\n") {
		t.Error(".dockerignore does not start from excluding everything")
	}
}

// stringVar reports whether file declares a package-level var name of type
// string, or initialised from a string literal.
func stringVar(t *testing.T, file, name string) bool {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.VAR {
			continue
		}
		for _, s := range g.Specs {
			vs := s.(*ast.ValueSpec)
			for i, n := range vs.Names {
				if n.Name != name {
					continue
				}
				if id, ok := vs.Type.(*ast.Ident); ok && id.Name == "string" {
					return true
				}
				if vs.Type == nil && i < len(vs.Values) {
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						return true
					}
				}
			}
		}
	}
	return false
}
