package csi_test

// NFS acceptance tests for the CSI front end (wave9).
//
// Each test encodes one requirement of docs/research/gaps/csi-nfs-design.md
// (and ADR 0004) that the CSI layer does not meet at HEAD 1603dad. They are
// expected to FAIL until the NFS CSI build lands; they are its acceptance
// list. Section references are to csi-nfs-design.md, spec references to
// container-storage-interface/spec v1.13.0 spec.md.
//
// Refusals are asserted twice: the code (InvalidArgument) and that the
// refusal is about the value, not about the key being unknown. Today every
// nfs.* key is refused as "unsupported parameter", which would otherwise
// make every negative test pass vacuously.
//
//   go test ./internal/csi -run 'TestNFSAccept' -v

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
	zcsi "github.com/wommy/empty/internal/csi"
	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/server"
	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

const (
	nfsParent  = "tank/vols"
	nfsClients = "nfs.clients" // §3
	nfsServer  = "nfs.server"  // §3
	nfsUID     = "nfs.uid"     // §13 Q1(a), ADR 0004 "root squash stays on"
	nfsGID     = "nfs.gid"
	nfsMode    = "nfs.mode"
	srv1       = "192.0.2.10"
	srv2       = "nfs.example.com"
)

type nfsFix struct {
	sim *zfstest.Sim
	ctl *zcsi.Controller
}

// nfsFixture is the CSI front end over an endpoint that exports NFS
// (FileExport on; the simulator is the etab and nfsd), advertising servers
// as its NFS server addresses (§3: the repeatable -nfs-server flag,
// GetCapabilitiesResponse.nfs_servers).
//
// TODO(nfs-csi): pass servers to the endpoint config once it has the field
// (§4 proto: GetCapabilitiesResponse.nfs_servers = 4). Until then nothing
// is advertised, and the tests that depend on it fail on their assertions.
func nfsFixture(t *testing.T, servers ...string) *nfsFix {
	t.Helper()
	ctx := context.Background()
	sim := zfstest.New("tank")
	z := zfs.New(sim)
	if err := z.CreateFilesystem(ctx, nfsParent, nil, true); err != nil {
		t.Fatal(err)
	}
	ep, err := endpoint.New(z, endpoint.Config{Parent: nfsParent, FileExport: true, Etab: sim})
	if err != nil {
		t.Fatal(err)
	}
	_ = servers // see TODO
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	pb.RegisterEndpointServiceServer(gs, &server.Server{EP: ep, Parent: nfsParent})
	go gs.Serve(lis)
	t.Cleanup(gs.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return &nfsFix{sim: sim, ctl: &zcsi.Controller{EP: pb.NewEndpointServiceClient(conn)}}
}

func mountCap(mode csi.VolumeCapability_AccessMode_Mode, fsType string) *csi.VolumeCapability {
	return &csi.VolumeCapability{
		AccessMode: &csi.VolumeCapability_AccessMode{Mode: mode},
		AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{FsType: fsType}},
	}
}

func blockCap(mode csi.VolumeCapability_AccessMode_Mode) *csi.VolumeCapability {
	return &csi.VolumeCapability{
		AccessMode: &csi.VolumeCapability_AccessMode{Mode: mode},
		AccessType: &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}},
	}
}

const (
	snw  = csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER
	snro = csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY
	mnro = csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY
	mnsw = csi.VolumeCapability_AccessMode_MULTI_NODE_SINGLE_WRITER
	mnmw = csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER
)

func nfsReq(name string, params map[string]string, caps ...*csi.VolumeCapability) *csi.CreateVolumeRequest {
	if len(caps) == 0 {
		caps = []*csi.VolumeCapability{mountCap(snw, "")}
	}
	return &csi.CreateVolumeRequest{
		Name: name, VolumeCapabilities: caps, Parameters: params,
		CapacityRange: &csi.CapacityRange{RequiredBytes: 1 << 30},
	}
}

func (f *nfsFix) create(t *testing.T, r *csi.CreateVolumeRequest) *csi.Volume {
	t.Helper()
	res, err := f.ctl.CreateVolume(context.Background(), r)
	if err != nil {
		t.Fatalf("CreateVolume(%s, %v): %v", r.GetName(), r.GetParameters(), err)
	}
	return res.GetVolume()
}

// refusedForValue asserts InvalidArgument for the value, not the key.
func refusedForValue(t *testing.T, err error, what string) {
	t.Helper()
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("%s: got %v, want InvalidArgument", what, err)
		return
	}
	if strings.Contains(status.Convert(err).Message(), "unsupported parameter") {
		t.Errorf("%s: refused as an unknown key (%q), not for its value", what, status.Convert(err).Message())
	}
}

// N1 (§3): the static-export StorageClass parameters are understood.
func TestNFSAccept_N1_ParamsAccepted(t *testing.T) {
	f := nfsFixture(t, srv1)
	f.create(t, nfsReq("n1", map[string]string{nfsClients: "10.20.0.0/24"}))
	f.create(t, nfsReq("n1b", map[string]string{nfsClients: "10.20.0.0/24", nfsServer: srv1}))
}

// N2 (§2, §3): nfs.clients is a comma-separated list of canonical IPv4
// CIDRs: explicit /N, host bits zero, at or above the floor (default /16),
// no overlap. Non-canonical values are refused, never masked.
func TestNFSAccept_N2_ClientsValidated(t *testing.T) {
	f := nfsFixture(t, srv1)
	for i, good := range []string{"10.20.0.0/24", "10.20.0.0/24,10.20.1.0/24", "10.20.0.5/32", "10.20.0.0/16"} {
		if _, err := f.ctl.CreateVolume(context.Background(), nfsReq("good"+string(rune('a'+i)), map[string]string{nfsClients: good})); err != nil {
			t.Errorf("nfs.clients=%q: %v", good, err)
		}
	}
	for i, bad := range []string{
		"",                            // static mode requires clients
		"10.20.0.5",                   // no /N (would be FQDN-class, resolved by nfs-utils, Fact C)
		"10.20.0.5/24",                // host bits set: never silently masked
		"10.0.0.0/8",                  // below the /16 floor
		"0.0.0.0/0",                   // world
		"10.20.0.0/24,10.20.0.128/25", // overlap (Fact D)
		"host.example.com/32",         // hostname
		"*",                           // wildcard
		"2001:db8::/64",               // IPv6
		"10.20.0.0/24:10.20.1.0/24",   // sharenfs separator smuggled in
		"10.20.0.0/24,no_root_squash", // an option smuggled in
		"10.20.0.0/24,",               // empty entry (empty host = world, Fact B)
		"010.020.000.000/24",          // inet_aton spelling
	} {
		_, err := f.ctl.CreateVolume(context.Background(), nfsReq("bad"+string(rune('a'+i)), map[string]string{nfsClients: bad}))
		refusedForValue(t, err, "nfs.clients="+bad)
	}
}

// N3 (§3): nfs.server must be one of the endpoint's advertised servers;
// without it the first advertised one is used.
func TestNFSAccept_N3_ServerSelection(t *testing.T) {
	f := nfsFixture(t, srv1, srv2)
	v := f.create(t, nfsReq("n3a", map[string]string{nfsClients: "10.20.0.0/24"}))
	if got := v.GetVolumeContext()["server"]; got != srv1 {
		t.Errorf("default server %q, want the first advertised %q", got, srv1)
	}
	v = f.create(t, nfsReq("n3b", map[string]string{nfsClients: "10.20.0.0/24", nfsServer: srv2}))
	if got := v.GetVolumeContext()["server"]; got != srv2 {
		t.Errorf("server %q, want %q", got, srv2)
	}
	_, err := f.ctl.CreateVolume(context.Background(), nfsReq("n3c", map[string]string{nfsClients: "10.20.0.0/24", nfsServer: "198.51.100.1"}))
	refusedForValue(t, err, "nfs.server not advertised")
}

// N4 (§5, ADR 0004, Fact G): volume_context carries server and share (the
// mountpoint as read after create) for csi-driver-nfs, and none of the keys
// it would act on that the design forbids.
func TestNFSAccept_N4_VolumeContext(t *testing.T) {
	f := nfsFixture(t, srv1)
	v := f.create(t, nfsReq("n4", map[string]string{nfsClients: "10.20.0.0/24"}))
	ctx := v.GetVolumeContext()
	if ctx["server"] == "" || ctx["share"] == "" {
		t.Fatalf("volume_context %v: csi-driver-nfs needs server and share (nodeserver.go: both required)", ctx)
	}
	if mp := f.sim.Prop(ctx[zcsi.CtxDataset], "mountpoint"); ctx["share"] != ctx[zcsi.CtxMountpoint] || (mp != "" && mp != ctx["share"]) {
		t.Errorf("share %q, mountpoint %q (local %q): share must be the volume's mountpoint", ctx["share"], ctx[zcsi.CtxMountpoint], mp)
	}
	for k := range ctx {
		switch strings.ToLower(k) {
		case "subdir", "mountoptions", "mountpermissions", "uid", "gid":
			t.Errorf("volume_context has %q: csi-driver-nfs would act on it (§5 forbids it)", k)
		}
	}
}

// N5 (§2, §3, §4): sharenfs is written at create in canonical form (sorted,
// ':'-joined, rw= unless every mode is *_READER_ONLY), the mountpoint is
// pinned locally, and the export is served before OK.
func TestNFSAccept_N5_ShareNFSCanonicalAndServed(t *testing.T) {
	f := nfsFixture(t, srv1)
	v := f.create(t, nfsReq("n5", map[string]string{nfsClients: "10.20.1.0/24,10.20.0.0/24"}))
	ds := v.GetVolumeContext()[zcsi.CtxDataset]
	if got, want := f.sim.Prop(ds, "sharenfs"), "rw=10.20.0.0/24:10.20.1.0/24"; got != want {
		t.Errorf("sharenfs %q, want %q", got, want)
	}
	if f.sim.Prop(ds, "mountpoint") == "" {
		t.Errorf("mountpoint is not set locally on %s (§4 step 1: pinned so share stays valid)", ds)
	}
	b, _ := f.sim.ReadEtab(context.Background())
	etab := endpoint.ParseEtab(b)
	for _, c := range []string{"10.20.0.0/24", "10.20.1.0/24"} {
		if got, ok := etab.Lookup(v.GetVolumeContext()["share"], c); !ok || got.ReadOnly {
			t.Errorf("etab does not serve %s rw at %q", c, v.GetVolumeContext()["share"])
		}
	}
	ro := f.create(t, nfsReq("n5ro", map[string]string{nfsClients: "10.20.0.0/24"}, mountCap(mnro, ""), mountCap(snro, "")))
	if got, want := f.sim.Prop(ro.GetVolumeContext()[zcsi.CtxDataset], "sharenfs"), "ro=10.20.0.0/24"; got != want {
		t.Errorf("reader-only modes: sharenfs %q, want %q", got, want)
	}
}

// N6 (§4 step 2-3, §8 C3, F4): with nfsd down CreateVolume is Unavailable
// and the volume is not committed; the retry after nfsd starts succeeds.
func TestNFSAccept_N6_NFSDDownNotCommitted(t *testing.T) {
	f := nfsFixture(t, srv1)
	f.sim.SetNFSDRunning(false)
	r := nfsReq("n6", map[string]string{nfsClients: "10.20.0.0/24"})
	_, err := f.ctl.CreateVolume(context.Background(), r)
	if status.Code(err) != codes.Unavailable {
		t.Errorf("nfsd down: got %v, want Unavailable", err)
	}
	lv, err := f.ctl.ListVolumes(context.Background(), &csi.ListVolumesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(lv.GetEntries()) != 0 {
		t.Errorf("an unserved volume was committed: %v", lv.GetEntries())
	}
	f.sim.SetNFSDRunning(true)
	f.create(t, r)
}

// N7 (§3 last bullet, ADR 0004 consequences): csi-driver-nfs cannot publish
// a zvol, so block access is refused for NFS volumes.
func TestNFSAccept_N7_BlockRefused(t *testing.T) {
	f := nfsFixture(t, srv1)
	_, err := f.ctl.CreateVolume(context.Background(), nfsReq("n7", map[string]string{nfsClients: "10.20.0.0/24"}, blockCap(snw)))
	refusedForValue(t, err, "block access with nfs.clients")
}

// N8 (§7): in static mode a filesystem volume supports all five access
// modes; MULTI_NODE_* works because the export is a CIDR.
func TestNFSAccept_N8_MultiNodeModes(t *testing.T) {
	f := nfsFixture(t, srv1)
	for i, m := range []csi.VolumeCapability_AccessMode_Mode{mnro, mnsw, mnmw} {
		if _, err := f.ctl.CreateVolume(context.Background(), nfsReq("n8"+string(rune('a'+i)), map[string]string{nfsClients: "10.20.0.0/24"}, mountCap(m, ""))); err != nil {
			t.Errorf("%v: %v", m, err)
		}
	}
}

// N9 (§6): idempotency compares the parsed client set and mode.
func TestNFSAccept_N9_Idempotency(t *testing.T) {
	f := nfsFixture(t, srv1, srv2)
	ctx := context.Background()
	r := nfsReq("n9", map[string]string{nfsClients: "10.20.0.0/24,10.20.1.0/24"})
	first := f.create(t, r)
	again := f.create(t, r)
	if again.GetVolumeId() != first.GetVolumeId() || !maps(again.GetVolumeContext(), first.GetVolumeContext()) {
		t.Errorf("retry: %v then %v", first, again)
	}
	f.create(t, nfsReq("n9", map[string]string{nfsClients: "10.20.1.0/24,10.20.0.0/24"})) // order is irrelevant
	_, err := f.ctl.CreateVolume(ctx, nfsReq("n9", map[string]string{nfsClients: "10.20.2.0/24"}))
	if status.Code(err) != codes.AlreadyExists {
		t.Errorf("other clients: got %v, want AlreadyExists", err)
	}
	_, err = f.ctl.CreateVolume(ctx, nfsReq("n9", map[string]string{nfsClients: "10.20.0.0/24,10.20.1.0/24"}, mountCap(mnro, "")))
	if status.Code(err) != codes.AlreadyExists {
		t.Errorf("same clients read-only: got %v, want AlreadyExists (mode is part of the value)", err)
	}
	// §6: nfs.server is not volume state; the response carries the new value
	v := f.create(t, nfsReq("n9", map[string]string{nfsClients: "10.20.0.0/24,10.20.1.0/24", nfsServer: srv2}))
	if v.GetVolumeContext()["server"] != srv2 {
		t.Errorf("nfs.server changed on retry: context %v", v.GetVolumeContext())
	}
}

// N10 (§7 ValidateVolumeCapabilities): confirmed iff filesystem, mount with
// fs_type "" or "nfs", no writer mode on a ro-only value, parameters parse,
// and a given volume_context matches (share = mountpoint, server
// advertised). A mismatch is a Message, not an error.
func TestNFSAccept_N10_Validate(t *testing.T) {
	f := nfsFixture(t, srv1)
	ctx := context.Background()
	rw := f.create(t, nfsReq("n10rw", map[string]string{nfsClients: "10.20.0.0/24"}, mountCap(mnmw, "")))
	ro := f.create(t, nfsReq("n10ro", map[string]string{nfsClients: "10.20.0.0/24"}, mountCap(mnro, "")))
	check := func(name string, vol *csi.Volume, want bool, vctx map[string]string, caps ...*csi.VolumeCapability) {
		t.Helper()
		res, err := f.ctl.ValidateVolumeCapabilities(ctx, &csi.ValidateVolumeCapabilitiesRequest{
			VolumeId: vol.GetVolumeId(), VolumeCapabilities: caps, VolumeContext: vctx,
			Parameters: map[string]string{nfsClients: "10.20.0.0/24"},
		})
		if err != nil {
			t.Errorf("%s: %v", name, err)
			return
		}
		if got := res.GetConfirmed() != nil; got != want {
			t.Errorf("%s: confirmed=%v (%q), want %v", name, got, res.GetMessage(), want)
		}
	}
	check("fs_type nfs", rw, true, nil, mountCap(snw, "nfs"))
	check("multi-node writer", rw, true, nil, mountCap(mnmw, ""))
	check("own context", rw, true, rw.GetVolumeContext(), mountCap(snw, ""))
	check("block", rw, false, nil, blockCap(snw))
	check("fs_type ext4", rw, false, nil, mountCap(snw, "ext4"))
	check("writer on ro export", ro, false, nil, mountCap(snw, ""))
	bad := map[string]string{}
	for k, v := range rw.GetVolumeContext() {
		bad[k] = v
	}
	bad["share"] = "/elsewhere"
	check("share mismatch", rw, false, bad, mountCap(snw, ""))
	bad["share"], bad["server"] = rw.GetVolumeContext()["share"], "198.51.100.1"
	check("server not advertised", rw, false, bad, mountCap(snw, ""))
}

// N11 (ADR 0004 "root squash stays on", §13 Q1(a)): the dataset root's
// owner and mode come from StorageClass parameters, validated.
func TestNFSAccept_N11_RootOwnershipParams(t *testing.T) {
	f := nfsFixture(t, srv1)
	f.create(t, nfsReq("n11", map[string]string{nfsClients: "10.20.0.0/24", nfsUID: "1000", nfsGID: "1000", nfsMode: "0775"}))
	for i, bad := range []map[string]string{
		{nfsUID: "-1"}, {nfsUID: "alice"}, {nfsGID: "4294967296"}, {nfsMode: "0999"}, {nfsMode: "17777"}, {nfsMode: "rwx"},
	} {
		bad[nfsClients] = "10.20.0.0/24"
		_, err := f.ctl.CreateVolume(context.Background(), nfsReq("n11bad"+string(rune('a'+i)), bad))
		refusedForValue(t, err, "root ownership "+strings.TrimSpace(strings.ReplaceAll(strings.Trim(strings.Join([]string{bad[nfsUID], bad[nfsGID], bad[nfsMode]}, " "), " "), "  ", " ")))
	}
}

// N12 (§7 delete ordering, Fact E): DeleteVolume needs no explicit unshare;
// afterwards nothing serves the path.
func TestNFSAccept_N12_DeleteUnexports(t *testing.T) {
	f := nfsFixture(t, srv1)
	v := f.create(t, nfsReq("n12", map[string]string{nfsClients: "10.20.0.0/24"}))
	if _, err := f.ctl.DeleteVolume(context.Background(), &csi.DeleteVolumeRequest{VolumeId: v.GetVolumeId()}); err != nil {
		t.Fatal(err)
	}
	b, _ := f.sim.ReadEtab(context.Background())
	if strings.Contains(string(b), v.GetVolumeContext()["share"]+"\t") {
		t.Errorf("etab still serves %s after DeleteVolume:\n%s", v.GetVolumeContext()["share"], b)
	}
}

// N13 (§4: "A clone uses zfs clone -o … the same way"): a restore or clone
// takes the request's clients, never the source's.
func TestNFSAccept_N13_CloneTakesRequestClients(t *testing.T) {
	f := nfsFixture(t, srv1)
	ctx := context.Background()
	src := f.create(t, nfsReq("n13src", map[string]string{nfsClients: "10.20.0.0/24"}))
	snap, err := f.ctl.CreateSnapshot(ctx, &csi.CreateSnapshotRequest{Name: "n13snap", SourceVolumeId: src.GetVolumeId()})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []*csi.CreateVolumeRequest{
		nfsReq("n13restore", map[string]string{nfsClients: "10.30.0.0/24"}),
		nfsReq("n13clone", map[string]string{nfsClients: "10.30.0.0/24"}),
	} {
		if strings.HasSuffix(r.Name, "restore") {
			r.VolumeContentSource = &csi.VolumeContentSource{Type: &csi.VolumeContentSource_Snapshot{Snapshot: &csi.VolumeContentSource_SnapshotSource{SnapshotId: snap.GetSnapshot().GetSnapshotId()}}}
		} else {
			r.VolumeContentSource = &csi.VolumeContentSource{Type: &csi.VolumeContentSource_Volume{Volume: &csi.VolumeContentSource_VolumeSource{VolumeId: src.GetVolumeId()}}}
		}
		v := f.create(t, r)
		if got := f.sim.Prop(v.GetVolumeContext()[zcsi.CtxDataset], "sharenfs"); got != "rw=10.30.0.0/24" {
			t.Errorf("%s: sharenfs %q, want the request's rw=10.30.0.0/24", r.Name, got)
		}
		if v.GetVolumeContext()["server"] == "" || v.GetVolumeContext()["share"] == "" {
			t.Errorf("%s: volume_context %v lacks server/share", r.Name, v.GetVolumeContext())
		}
	}
}

// N14 (spec:1082-1083 "A volume uniquely identified by volume_id SHALL
// always report the same volume_context"): ListVolumes reports the context
// CreateVolume returned, server included. nfs.server is not volume state
// (§6), so the design must decide where ListVolumes gets it from.
func TestNFSAccept_N14_ListVolumesSameContext(t *testing.T) {
	f := nfsFixture(t, srv1, srv2)
	v := f.create(t, nfsReq("n14", map[string]string{nfsClients: "10.20.0.0/24", nfsServer: srv2}))
	lv, err := f.ctl.ListVolumes(context.Background(), &csi.ListVolumesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range lv.GetEntries() {
		if e.GetVolume().GetVolumeId() == v.GetVolumeId() {
			if !maps(e.GetVolume().GetVolumeContext(), v.GetVolumeContext()) {
				t.Errorf("ListVolumes context %v, CreateVolume context %v", e.GetVolume().GetVolumeContext(), v.GetVolumeContext())
			}
			return
		}
	}
	t.Errorf("%s not listed", v.GetVolumeId())
}

// N15 (§3 table: nfs.clients is "required" in static mode): nfs.server or
// root-ownership parameters without nfs.clients are refused.
func TestNFSAccept_N15_ClientsRequired(t *testing.T) {
	f := nfsFixture(t, srv1)
	for _, p := range []map[string]string{{nfsServer: srv1}, {nfsUID: "1000"}} {
		_, err := f.ctl.CreateVolume(context.Background(), nfsReq("n15", p))
		refusedForValue(t, err, "NFS parameters without nfs.clients")
	}
}
