package zfs

import "testing"

// zfs output is untrusted input: user property values are arbitrary text.
func FuzzParseColumns(f *testing.F) {
	f.Add("tank/a\ttype\tvolume\t-\ntank/a\tu:p\tx\ty\tlocal\n")
	f.Add("tank/a\tvolsize\t1024\tlocal\n\n")
	f.Add("\t\t\t")
	f.Fuzz(func(t *testing.T, out string) {
		props := []string{"type", "volsize", "u:p"}
		c, err := parseColumns([]byte(out), props)
		if err != nil {
			return
		}
		if len(c.Cells) != len(c.Names)*len(props) {
			t.Fatalf("cells %d != %d x %d", len(c.Cells), len(c.Names), len(props))
		}
		for i := range c.Names {
			for j := range props {
				_ = c.Cell(i, j)
			}
		}
	})
}

func FuzzClassify(f *testing.F) {
	for _, p := range patterns {
		f.Add("cannot do 'x': " + p.text)
	}
	f.Fuzz(func(t *testing.T, s string) {
		k := classify(s)
		if k < KindUnknown || k > KindUnavailable {
			t.Fatalf("kind %d", k)
		}
	})
}
