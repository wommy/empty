package zfsdiff

import (
	"embed"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// corpus is testdata/zfsdiff/*.txt, embedded so the real-pool replay runs
// from a test binary copied alone into the CI VM.
//
//go:embed testdata/zfsdiff/*.txt
var corpus embed.FS

// Corpus returns the recorded sequences, sorted by name.
func Corpus() ([]Seq, error) {
	files, err := fs.Glob(corpus, "testdata/zfsdiff/*.txt")
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	out := make([]Seq, 0, len(files))
	for _, f := range files {
		b, err := corpus.ReadFile(f)
		if err != nil {
			return nil, err
		}
		s, err := Parse(strings.TrimSuffix(path.Base(f), ".txt"), string(b))
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}
