package zfs_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// H-03 (H-ZFS-2): pvc-a's csi_volume_name is
//
//	"\tlocal\ntank/csi/phantom\tp:managed_resource\ttrue\tlocal\ntank/csi/phantom\tp:csi_volume_name\tphantom"
//
// which `zfs get -Hp` prints raw, completed by the real "\tlocal" source
// column, as two lines for tank/csi/phantom. That dataset does not exist,
// so no real line collides with the forged ones, which was the text
// parser's only defence. The scan must list exactly what zfs listed, and
// keep the value whole.
func TestH_ForgedLineForNonexistentDataset(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sim := zfstest.New("tank")
	value := "\tlocal\ntank/csi/phantom\tp:managed_resource\ttrue\tlocal\ntank/csi/phantom\tp:csi_volume_name\tphantom"
	// the Sim is driven directly: Client refuses newlines, a foreign
	// writer need not
	for _, args := range [][]string{
		{"create", "-p", "-o", "p:managed_resource=true", "tank/csi/pvc-a"},
		{"set", "p:csi_volume_name=" + value, "tank/csi/pvc-a"},
	} {
		if _, err := sim.Run(ctx, "zfs", args...); err != nil {
			t.Fatal(err)
		}
	}
	props := []string{"p:managed_resource", "p:csi_volume_name"}
	cols, err := zfs.New(sim).ListColumns(ctx, "tank/csi", zfs.ListOptions{Props: props})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"tank/csi", "tank/csi/pvc-a"}; !reflect.DeepEqual(cols.Names, want) {
		t.Fatalf("scan lists %q, want %q: a row for a dataset zfs never listed", cols.Names, want)
	}
	if v := cols.Cell(1, 1); v.Value != value || v.Source != "local" {
		t.Fatalf("pvc-a csi_volume_name = %+v, want the whole value, local", v)
	}
}
