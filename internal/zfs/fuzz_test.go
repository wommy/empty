package zfs

import (
	"strings"
	"testing"
)

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
		if k < KindUnknown || k > kindLast {
			t.Fatalf("kind %d", k)
		}
	})
}

// A user property value with a newline is printed raw by zfs get -Hp, so it
// can forge lines for another dataset. Here pvc-a's csi_volume_name forges
// ownership of victim; victim's own lines still follow, and the repeat must
// fail the whole scan rather than let either line win.
func TestParseColumnsRejectsForgedLines(t *testing.T) {
	props := []string{"p:managed_resource", "p:csi_volume_name"}
	out := []byte("tank/csi/pvc-a\tp:managed_resource\ttrue\tlocal\n" +
		"tank/csi/pvc-a\tp:csi_volume_name\tx\tlocal\n" +
		"tank/csi/victim\tp:managed_resource\ttrue\tlocal\n" + // forged
		"tank/csi/victim\tp:managed_resource\t-\t-\n" +
		"tank/csi/victim\tp:csi_volume_name\t-\t-\n")
	if _, err := parseColumns(out, props); err == nil {
		t.Fatal("forged duplicate line accepted")
	}
}

// parseColumns on untrusted output: a line without four fields is an error
// (never a slice-bounds panic or a misread), blank lines are skipped, and a
// property we did not ask for is ignored, never written into a column.
func TestParseColumns(t *testing.T) {
	t.Parallel()
	props := []string{"type", "volsize"}
	for _, tc := range []struct {
		name string
		out  string
		want string // "name:type=value/source,volsize=value/source;..." or "error"
	}{
		{"three fields", "tank/a\ttype\tvolume\n", "error"},
		{"two fields", "tank/a\ttype\n", "error"},
		{"one field", "tank/a\n", "error"},
		{"three fields, no property match", "a\tb\tc\n", "error"},
		{"well formed", "tank/a\ttype\tvolume\t-\ntank/a\tvolsize\t1024\tlocal\n", "tank/a:type=volume/-,volsize=1024/local"},
		{"value with tabs", "tank/a\ttype\tx\ty\tlocal\n", "tank/a:type=x\ty/local,volsize=-/-"},
		{"blank lines skipped", "tank/a\ttype\tvolume\t-\n\ntank/b\ttype\tfilesystem\t-\n", "tank/a:type=volume/-,volsize=-/-;tank/b:type=filesystem/-,volsize=-/-"},
		{"unrequested property ignored", "tank/a\ttype\tvolume\t-\ntank/a\tbogus\tx\tlocal\n", "tank/a:type=volume/-,volsize=-/-"},
	} {
		c, err := parseColumns([]byte(tc.out), props)
		got := "error"
		if err == nil {
			var ds []string
			for i, n := range c.Names {
				var cells []string
				for j, p := range props {
					cells = append(cells, p+"="+c.Cell(i, j).Value+"/"+c.Cell(i, j).Source)
				}
				ds = append(ds, n+":"+strings.Join(cells, ","))
			}
			got = strings.Join(ds, ";")
		}
		if got != tc.want {
			t.Errorf("%s: %q, want %q (err %v)", tc.name, got, tc.want, err)
		}
	}
}
