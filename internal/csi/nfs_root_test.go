package csi_test

import (
	"context"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"

	zcsi "github.com/wommy/empty/internal/csi"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// N11 (wave9; csi-nfs-design §13 Q1(a)): nfs.uid, nfs.gid and nfs.mode
// reach the endpoint, which gives the volume's root that owner and mode
// before committing it; bad values are InvalidArgument for the value, and
// nothing is created.
func TestNFS_N11_RootOwnership(t *testing.T) {
	t.Parallel()
	f := nfsFixture(t, srv1)
	params := map[string]string{nfsClients: "10.20.0.0/24", zcsi.ParamNFSUID: "1000", zcsi.ParamNFSGID: "2000", zcsi.ParamNFSMode: "2775"}
	v := f.create(t, nfsReq("n11", params))
	ds := v.GetVolumeContext()[zcsi.CtxDataset]
	if got, _ := f.sim.RootOf(ds); got != (zfstest.Root{UID: 1000, GID: 2000, Mode: 0o2775}) {
		t.Fatalf("root %+v", got)
	}
	// "0775" and "775" are the same mode; each key alone works
	for name, p := range map[string]map[string]string{
		"lead": {nfsClients: "10.20.0.0/24", zcsi.ParamNFSMode: "0775"},
		"bare": {nfsClients: "10.20.0.0/24", zcsi.ParamNFSMode: "775"},
		"gid":  {nfsClients: "10.20.0.0/24", zcsi.ParamNFSGID: "0"},
		"none": {nfsClients: "10.20.0.0/24"},
	} {
		v := f.create(t, nfsReq(name, p))
		want := zfstest.Root{Mode: 0o775}
		if name == "gid" || name == "none" {
			want.Mode = 0o755 // as zfs made it
		}
		if got, _ := f.sim.RootOf(v.GetVolumeContext()[zcsi.CtxDataset]); got != want {
			t.Errorf("%s: root %+v, want %+v", name, got, want)
		}
	}
	res, err := f.ctl.ValidateVolumeCapabilities(context.Background(), &csi.ValidateVolumeCapabilitiesRequest{
		VolumeId: v.GetVolumeId(), VolumeCapabilities: []*csi.VolumeCapability{mountCap(snw, "")}, Parameters: params})
	if err != nil || res.GetConfirmed() == nil {
		t.Fatalf("Validate with the same parameters: %v, %v", res, err)
	}
	for k, vals := range map[string][]string{
		zcsi.ParamNFSUID:  {"", "-1", "+1", "1e3", "0x10", "abc", "2147483648", "4294967295"},
		zcsi.ParamNFSGID:  {"", "1000 ", "2147483648"},
		zcsi.ParamNFSMode: {"", "0o775", "8", "-1", "10000", "rwx", "07_77"},
	} {
		for _, val := range vals {
			_, err := f.ctl.CreateVolume(context.Background(), nfsReq("bad", map[string]string{nfsClients: "10.20.0.0/24", k: val}))
			refusedForValue(t, err, k+"="+val)
		}
	}
	if f.sim.Has(nfsParent + "/bad") {
		t.Fatal("a refused create made a dataset")
	}
	off := nfsFixture(t)
	for _, k := range []string{zcsi.ParamNFSUID, zcsi.ParamNFSGID, zcsi.ParamNFSMode} {
		_, err := off.ctl.CreateVolume(context.Background(), nfsReq("v", map[string]string{k: "0"}))
		refusedForValue(t, err, k+" without NFS mode")
	}
}
