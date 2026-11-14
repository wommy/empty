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

// Get and List read `zfs get -j -p` too. In `-Hp` text a value that holds
// "\t<source>\n<name>\t<prop>\t" prints as a second line for any property,
// and the last line for a (dataset, property) won: tank/a's p:note
//
//	"x\tlocal\ntank/a\ttype\tvolume"
//
// printed after the real type line overwrote tank/a's type with "volume"
// (waitParent would then refuse the parent as "a volume"), and a forged
// line for tank/a/ghost added a child List never had. Each value must read
// back whole, and nothing else may change.
func TestH_GetAndListForgedLinesStayValues(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sim := zfstest.New("tank")
	typeForgery := "x\tlocal\ntank/a\ttype\tvolume"
	childForgery := "x\tlocal\ntank/a/ghost\tp:mark\ttrue"
	for _, args := range [][]string{
		{"create", "-p", "-o", "p:mark=old", "tank/a/b"},
		// raw argv: Client refuses control characters, a foreign writer
		// need not
		{"set", "p:note=" + typeForgery, "tank/a"},
		{"set", "p:mark=" + childForgery, "tank/a"},
	} {
		if _, err := sim.Run(ctx, "zfs", args...); err != nil {
			t.Fatal(err)
		}
	}
	c := zfs.New(sim)
	d, err := c.Get(ctx, "tank/a", "type", "p:note")
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Props["type"]; got.Value != "filesystem" {
		t.Errorf("Get type = %+v, want filesystem: a value overwrote another property", got)
	}
	if got := d.Props["p:note"]; got.Value != typeForgery || !got.Local() {
		t.Errorf("Get p:note = %+v, want the whole value, local", got)
	}
	if len(d.Props) != 2 {
		t.Errorf("Get returned %d properties, want 2: %+v", len(d.Props), d.Props)
	}

	// List of tank's children: the forged tank/a/ghost line names a
	// dataset below the listed depth too, so depth 2 (-d 3) covers it
	list, err := c.List(ctx, "tank", zfs.ListOptions{Depth: 2, Props: []string{"p:mark"}})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range list {
		names = append(names, d.Name)
	}
	if want := []string{"tank/a", "tank/a/b"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("List = %q, want %q: a dataset zfs never listed", names, want)
	}
	if got := list[0].Props["p:mark"]; got.Value != childForgery || !got.Local() {
		t.Errorf("tank/a p:mark = %+v, want the whole value, local", got)
	}
	if got := list[1].Props["p:mark"]; got.Value != "old" || !got.Local() {
		t.Errorf("tank/a/b p:mark = %+v, want old, local", got)
	}
}
