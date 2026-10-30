package endpoint

import (
	"testing"

	"github.com/wommy/empty/internal/zfs"
)

// A zvol size zfs could not have printed must fail the scan: read as 0 it
// would let Expand plan a volsize below the real one, a shrink.
func TestDecodeRejectsImpossibleZvolSizes(t *testing.T) {
	n := props{"zfs-endpoint"}
	cols := n.columns()
	row := func(typ, volsize, block string) *zfs.Columns {
		c := &zfs.Columns{Props: cols, Names: []string{"tank/v/a"}}
		for _, p := range cols {
			v := "-"
			switch p {
			case "type":
				v = typ
			case "volsize":
				v = volsize
			case "volblocksize":
				v = block
			}
			c.Cells = append(c.Cells, zfs.Prop{Value: v, Source: "-"})
		}
		return c
	}
	if _, err := n.decodeColumns("tank/v", row("volume", "1073741824", "16384")); err != nil {
		t.Fatalf("valid zvol: %v", err)
	}
	if _, err := n.decodeColumns("tank/v", row("filesystem", "-", "-")); err != nil {
		t.Fatalf("filesystem with n/a sizes: %v", err)
	}
	for _, tc := range []struct{ volsize, block string }{
		{"-", "16384"}, {"none", "16384"}, {"", "16384"}, {"1G", "16384"}, {"-1", "16384"},
		{"18446744073709551616", "16384"}, {"1073741824", "-"}, {"1000", "16384"},
	} {
		if _, err := n.decodeColumns("tank/v", row("volume", tc.volsize, tc.block)); CodeOf(err) != Internal {
			t.Errorf("volsize %q volblocksize %q: got %v, want the scan to fail", tc.volsize, tc.block, err)
		}
	}
}
