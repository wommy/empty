package endpoint_test

import (
	"context"
	"testing"
)

// zfs re-shares only through a changelist (libzfs_changelist.c
// changelist_gather: mountpoint, sharenfs, sharesmb, canmount=off): a set
// or inherit of any other property leaves a stale zfs.exports line as it
// is, so only a sharenfs set (verifyExport's reshare) heals it. The stale
// line is made with `zfs set -u sharenfs` (CL_GATHER_DONT_UNMOUNT: no
// share), and read back from the kernel's export table.
func TestFileExportStaleShareOnlySharenfsHeals(t *testing.T) {
	ctx := context.Background()
	_, h := setup(t)
	h.NFS(t)
	run := func(args ...string) {
		t.Helper()
		if _, err := h.Z.Runner.Run(ctx, "zfs", args...); err != nil {
			t.Fatalf("zfs %v: %v", args, err)
		}
	}
	ds := h.Parent + "/stale"
	run("create", "-o", "sharenfs=rw=10.0.0.1/32", ds)
	mp := h.Prop(ds, "mountpoint")
	want := func(when, served, not string) {
		t.Helper()
		if _, ok := h.served(t, mp, served); !ok {
			t.Fatalf("%s: %s not served", when, served)
		}
		if _, ok := h.served(t, mp, not); ok {
			t.Fatalf("%s: %s served", when, not)
		}
	}
	want("created", "10.0.0.1", "10.0.0.2")
	run("set", "-u", "sharenfs=rw=10.0.0.2/32", ds)
	want("set -u sharenfs", "10.0.0.1", "10.0.0.2")
	run("set", "factcheck:x=1", ds)
	run("set", "atime=off", ds)
	run("inherit", "factcheck:x", ds)
	want("set and inherit of other properties", "10.0.0.1", "10.0.0.2")
	run("set", "sharenfs=rw=10.0.0.2/32", ds)
	want("sharenfs set again", "10.0.0.2", "10.0.0.1")
}
