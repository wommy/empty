package csi_test

// Reproducers for the wave9 differential-fuzz findings (csi-diff.md). Each
// FAILS at HEAD 1603dad and passes once the bug it names is fixed.
//
//   go test ./internal/csi -run 'TestWave9' -v

import (
	"context"
	"strings"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

func w9stack(t *testing.T) *stack {
	t.Helper()
	s, err := newStack(shortParent)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.stop)
	return s
}

// T1: an empty snapshot_id / volume_id is a missing REQUIRED field
// (spec:925-936, 525), not "no source".
func TestWave9_T1_EmptySourceIDRefused(t *testing.T) {
	c := w9stack(t).ctl
	for _, src := range []*csi.VolumeContentSource{
		{Type: &csi.VolumeContentSource_Snapshot{Snapshot: &csi.VolumeContentSource_SnapshotSource{}}},
		{Type: &csi.VolumeContentSource_Volume{Volume: &csi.VolumeContentSource_VolumeSource{}}},
	} {
		res, err := c.CreateVolume(context.Background(), &csi.CreateVolumeRequest{
			Name: "t1", VolumeCapabilities: []*csi.VolumeCapability{mountCap(snw, "")},
			CapacityRange: &csi.CapacityRange{RequiredBytes: 1 << 30}, VolumeContentSource: src,
		})
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("source %v: got %v (content_source %v), want InvalidArgument", src, err, res.GetVolume().GetContentSource())
		}
	}
}

// T2: ControllerExpandVolume must refuse a capability the volume does not
// support (spec:2545) and a malformed one (spec:1027-1035, 525).
func TestWave9_T2_ExpandChecksCapability(t *testing.T) {
	ctx := context.Background()
	c := w9stack(t).ctl
	v, err := c.CreateVolume(ctx, &csi.CreateVolumeRequest{Name: "t2", VolumeCapabilities: []*csi.VolumeCapability{mountCap(snw, "")},
		CapacityRange: &csi.CapacityRange{RequiredBytes: 1 << 30}})
	if err != nil {
		t.Fatal(err)
	}
	for name, vc := range map[string]*csi.VolumeCapability{
		"block on a filesystem": blockCap(snw),
		"empty capability":      {},
		"no access mode":        {AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}},
	} {
		_, err := c.ControllerExpandVolume(ctx, &csi.ControllerExpandVolumeRequest{VolumeId: v.GetVolume().GetVolumeId(),
			CapacityRange: &csi.CapacityRange{RequiredBytes: 2 << 30}, VolumeCapability: vc})
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: got %v, want InvalidArgument", name, err)
		}
	}
}

// T3: what ValidateVolumeCapabilities confirms, CreateVolume must accept as
// compatible (spec:808, 1479); either answer is fine, but the same one.
func TestWave9_T3_ValidateAgreesWithCreate(t *testing.T) {
	ctx := context.Background()
	c := w9stack(t).ctl
	r := &csi.CreateVolumeRequest{Name: "t3", VolumeCapabilities: []*csi.VolumeCapability{blockCap(snw)}, CapacityRange: &csi.CapacityRange{RequiredBytes: 1 << 30}}
	v, err := c.CreateVolume(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	mount := []*csi.VolumeCapability{mountCap(snw, "")}
	val, err := c.ValidateVolumeCapabilities(ctx, &csi.ValidateVolumeCapabilitiesRequest{VolumeId: v.GetVolume().GetVolumeId(), VolumeCapabilities: mount})
	if err != nil {
		t.Fatal(err)
	}
	r.VolumeCapabilities = mount
	_, err = c.CreateVolume(ctx, r)
	if confirmed := val.GetConfirmed() != nil; confirmed != (err == nil) {
		t.Errorf("Validate(mount on zvol) confirmed=%v, CreateVolume(same name, mount) = %v", confirmed, err)
	}
}

// E6: a volume restored from a snapshot is never smaller than the snapshot
// (spec:2229-2230): grow to its size or refuse with OUT_OF_RANGE (spec:1298).
func TestWave9_E6_RestoreNotSmallerThanSnapshot(t *testing.T) {
	ctx := context.Background()
	c := w9stack(t).ctl
	mnt := []*csi.VolumeCapability{mountCap(snw, "")}
	src, err := c.CreateVolume(ctx, &csi.CreateVolumeRequest{Name: "e6", VolumeCapabilities: mnt, CapacityRange: &csi.CapacityRange{RequiredBytes: 2 << 30}})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := c.CreateSnapshot(ctx, &csi.CreateSnapshotRequest{Name: "e6s", SourceVolumeId: src.GetVolume().GetVolumeId()})
	if err != nil {
		t.Fatal(err)
	}
	for name, cs := range map[string]*csi.VolumeContentSource{
		"restore": {Type: &csi.VolumeContentSource_Snapshot{Snapshot: &csi.VolumeContentSource_SnapshotSource{SnapshotId: snap.GetSnapshot().GetSnapshotId()}}},
		"clone":   {Type: &csi.VolumeContentSource_Volume{Volume: &csi.VolumeContentSource_VolumeSource{VolumeId: src.GetVolume().GetVolumeId()}}},
	} {
		v, err := c.CreateVolume(ctx, &csi.CreateVolumeRequest{Name: "e6-" + name, VolumeCapabilities: mnt,
			CapacityRange: &csi.CapacityRange{RequiredBytes: 1 << 30}, VolumeContentSource: cs})
		if status.Code(err) == codes.OutOfRange {
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got, want := v.GetVolume().GetCapacityBytes(), snap.GetSnapshot().GetSizeBytes(); got < want {
			t.Errorf("%s: capacity %d < source size %d (refquota below the data it holds)", name, got, want)
		}
	}
}

// E2: a parent that leaves less than 128 bytes for an id must be refused at
// startup; otherwise legal 128-byte names fail later (spec:494).
func TestWave9_E2_LongParentRefused(t *testing.T) {
	z := zfs.New(zfstest.New("tank"))
	if err := z.CreateFilesystem(context.Background(), longParent, nil, true); err != nil {
		t.Fatal(err)
	}
	if _, err := endpoint.New(z, endpoint.Config{Parent: longParent}); err == nil {
		t.Errorf("endpoint.New accepted a %d-byte parent: %d + 1 + 128 > 255 (ZFS_MAX_DATASET_NAME_LEN - 1)", len(longParent), len(longParent))
	}
}

// E1/E3/E4: names the spec allows are accepted (spec:849-851, 2178-2181).
func TestWave9_E1_LegalNames(t *testing.T) {
	ctx := context.Background()
	c := w9stack(t).ctl
	mnt := []*csi.VolumeCapability{mountCap(snw, "")}
	for _, n := range []string{"πβ-volume", "with space", "ns/claim", "a@b", "tab\tname", "-leading"} {
		if _, err := c.CreateVolume(ctx, &csi.CreateVolumeRequest{Name: n, VolumeCapabilities: mnt, CapacityRange: &csi.CapacityRange{RequiredBytes: 1 << 30}}); err != nil {
			t.Errorf("CreateVolume %q: %v", n, err)
		}
	}
	v, err := c.CreateVolume(ctx, &csi.CreateVolumeRequest{Name: "e1", VolumeCapabilities: mnt, CapacityRange: &csi.CapacityRange{RequiredBytes: 1 << 30}})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"tab\tsnap", "line\nfeed", "volume-source-for-volume-x"} {
		if _, err := c.CreateSnapshot(ctx, &csi.CreateSnapshotRequest{Name: n, SourceVolumeId: v.GetVolume().GetVolumeId()}); err != nil {
			t.Errorf("CreateSnapshot %q: %v", strings.ReplaceAll(n, "\n", `\n`), err)
		}
	}
}
