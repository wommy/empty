package deploy

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// installSection is the README's "## Install" section, up to the next
// second-level heading.
func installSection(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, s, ok := strings.Cut(string(b), "\n## Install\n")
	if !ok {
		t.Fatal(`README has no "## Install" section`)
	}
	if i := strings.Index(s, "\n## "); i >= 0 {
		s = s[:i]
	}
	return s
}

// installPath is a path an operator installs, or a unit refers to as
// installed: under /usr/local (the binary) or /etc (units, drop-ins).
// /run paths are left out: the unit and the daemon create them at start.
var installPath = regexp.MustCompile("/(?:usr/local|etc)/[^\\s'\"`(),;]+")

// unitRef is an ordering or dependency on another unit.
var unitRef = regexp.MustCompile(`(?m)^(?:After|Before|Wants|Requires|BindsTo|PartOf|Requisite)=(.+)$`)

// An operator following the README's Install section ends with the files
// the units need where they need them (deploy seam review B2): the section
// names every file shipped in deploy/systemd by its repository path, every
// /usr/local or /etc path those files mention (the ExecStart binary, the
// drop-ins' installed names), and every unit they are ordered after or
// want that is not shipped here, so the package that provides it is
// installed first.
func TestReadmeInstallNamesWhatTheUnitsNeed(t *testing.T) {
	t.Parallel()
	install := installSection(t)
	shipped := map[string]bool{}
	var files []string
	err := filepath.WalkDir("systemd", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		files = append(files, p)
		shipped[filepath.Base(p)] = true
		return nil
	})
	if err != nil || len(files) == 0 {
		t.Fatalf("deploy/systemd: %v, %d files", err, len(files))
	}
	for _, f := range files {
		repoPath := filepath.ToSlash(filepath.Join("deploy", f))
		if !strings.Contains(install, repoPath) {
			t.Errorf("README Install does not name %s", repoPath)
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range installPath.FindAllString(string(b), -1) {
			if !strings.Contains(install, p) {
				t.Errorf("%s mentions %s; README Install does not", f, p)
			}
		}
		for _, m := range unitRef.FindAllStringSubmatch(string(b), -1) {
			for _, u := range strings.Fields(m[1]) {
				// an instance of a shipped template: zfs-endpoint@%i.service
				if at, rest, ok := strings.Cut(u, "@"); ok {
					if _, suffix, ok := strings.Cut(rest, "."); ok && shipped[at+"@."+suffix] {
						continue
					}
				}
				if !shipped[u] && !strings.Contains(install, u) {
					t.Errorf("%s orders after or wants %s; README Install does not name it (or the package that provides it)", f, u)
				}
			}
		}
	}
}
