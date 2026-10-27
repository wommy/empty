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
