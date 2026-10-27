package csi_test

// NFS mode (static export at create): the wave9 acceptance list N1-N15
// (scratchpad wave9 csi-diff), with wave13's decisions applied: Q1 the
// server is recorded per volume (N9's last assertion inverted, N10 and N14
// against the recorded value), Q2 nfs.clients is required in NFS mode (N15
// widened), N16, and N11 (root ownership parameters, nfs_root_test.go).
//
// Refusals are asserted twice: the code (InvalidArgument) and that the
// refusal is about the value, not about the key being unknown.

import (
	"context"
	"maps"
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
	nfsClients = zcsi.ParamNFSClients
	nfsServer  = zcsi.ParamNFSServer
	srv1       = "192.0.2.10"
	srv2       = "nfs.example.com"
)

type nfsFix struct {
	sim *zfstest.Sim
	z   *zfs.Client
	ctl *zcsi.Controller
}

// nfsFixture is the CSI front end over an endpoint that exports NFS on the
// simulator (its etab and nfsd), advertising servers (-nfs-server).
func nfsFixture(t *testing.T, servers ...string) *nfsFix {
	t.Helper()
	sim := zfstest.New("tank")
	f := &nfsFix{sim: sim, z: zfs.New(sim)}
	if err := f.z.CreateFilesystem(context.Background(), nfsParent, nil, true); err != nil {
		t.Fatal(err)
	}
	f.restart(t, servers...)
	return f
}

// restart serves a new endpoint on the same pool, as a restart with other
// -nfs-server flags does.
func (f *nfsFix) restart(t *testing.T, servers ...string) {
	t.Helper()
	ep, err := endpoint.New(f.z, endpoint.Config{Parent: nfsParent, FileExport: true, Etab: f.sim, NFSServers: servers})
	if err != nil {
		t.Fatal(err)
	}
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
	f.ctl = &zcsi.Controller{EP: pb.NewEndpointServiceClient(conn)}
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

func (f *nfsFix) list(t *testing.T) map[string]*csi.Volume {
	t.Helper()
	lv, err := f.ctl.ListVolumes(context.Background(), &csi.ListVolumesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*csi.Volume{}
	for _, e := range lv.GetEntries() {
		out[e.GetVolume().GetVolumeId()] = e.GetVolume()
	}
	return out
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
func TestNFS_N1_ParamsAccepted(t *testing.T) {
	t.Parallel()
	f := nfsFixture(t, srv1)
	f.create(t, nfsReq("n1", map[string]string{nfsClients: "10.20.0.0/24"}))
	f.create(t, nfsReq("n1b", map[string]string{nfsClients: "10.20.0.0/24", nfsServer: srv1, "csi.storage.k8s.io/pvc/name": "x"}))
}

// N2 (§2, §3): canonical IPv4 networks only, never masked.
func TestNFS_N2_ClientsValidated(t *testing.T) {
	t.Parallel()
	f := nfsFixture(t, srv1)
	for i, good := range []string{"10.20.0.0/24", "10.20.0.0/24,10.20.1.0/24", "10.20.0.5/32", "10.20.0.0/16"} {
		if _, err := f.ctl.CreateVolume(context.Background(), nfsReq("good"+string(rune('a'+i)), map[string]string{nfsClients: good})); err != nil {
			t.Errorf("nfs.clients=%q: %v", good, err)
		}
	}
	for i, bad := range []string{
		"",                            // static mode requires clients
		"10.20.0.5",                   // no /N: nfs-utils resolves a bare address (Fact C)
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
	if got := f.list(t); len(got) != 4 {
		t.Errorf("a refused create left a volume: %d listed", len(got))
	}
}

// N3 (§3, Q1): nfs.server is one of the advertised servers, the first by
// default, and is recorded with the export at create.
func TestNFS_N3_ServerSelection(t *testing.T) {
	t.Parallel()
	f := nfsFixture(t, srv1, srv2)
	v := f.create(t, nfsReq("n3a", map[string]string{nfsClients: "10.20.0.0/24"}))
	if got := v.GetVolumeContext()[zcsi.CtxServer]; got != srv1 {
		t.Errorf("default server %q, want the first advertised %q", got, srv1)
	}
	v = f.create(t, nfsReq("n3b", map[string]string{nfsClients: "10.20.0.0/24", nfsServer: srv2}))
	if got := v.GetVolumeContext()[zcsi.CtxServer]; got != srv2 {
		t.Errorf("server %q, want %q", got, srv2)
	}
	if got := f.sim.Prop(v.GetVolumeContext()[zcsi.CtxDataset], endpoint.DefaultPropertyPrefix+":nfs_server"); got != srv2 {
		t.Errorf("recorded server %q, want %q", got, srv2)
	}
	_, err := f.ctl.CreateVolume(context.Background(), nfsReq("n3c", map[string]string{nfsClients: "10.20.0.0/24", nfsServer: "198.51.100.1"}))
	refusedForValue(t, err, "nfs.server not advertised")
}

// N4 (§5, ADR 0004, S11): volume_context carries server and share (the
// mountpoint) for csi-driver-nfs, and none of the keys it would act on
// that the design forbids.
func TestNFS_N4_VolumeContext(t *testing.T) {
	t.Parallel()
	f := nfsFixture(t, srv1)
	v := f.create(t, nfsReq("n4", map[string]string{nfsClients: "10.20.0.0/24"}))
	ctx := v.GetVolumeContext()
	if ctx[zcsi.CtxServer] != srv1 || ctx[zcsi.CtxShare] == "" || ctx[zcsi.CtxShare] != ctx[zcsi.CtxMountpoint] {
		t.Fatalf("volume_context %v: csi-driver-nfs needs server and share (the mountpoint)", ctx)
	}
	for k := range ctx {
		switch strings.ToLower(k) {
		case "subdir", "mountoptions", "mountpermissions", "uid", "gid":
			t.Errorf("volume_context has %q: csi-driver-nfs would act on it (§5 forbids it)", k)
		}
	}
}

// N5 (§2, §3, §4): sharenfs is canonical (sorted, rw= unless every mode
// is *_READER_ONLY) and served before OK; share is the mountpoint pinned
// locally at create, so it survives a move of the parent's.
func TestNFS_N5_ShareNFSCanonicalAndServed(t *testing.T) {
	t.Parallel()
	f := nfsFixture(t, srv1)
	v := f.create(t, nfsReq("n5", map[string]string{nfsClients: "10.20.1.0/24,10.20.0.0/24"}))
	ds := v.GetVolumeContext()[zcsi.CtxDataset]
	if got, want := f.sim.Prop(ds, "sharenfs"), "rw=10.20.0.0/24:10.20.1.0/24"; got != want {
		t.Errorf("sharenfs %q, want %q", got, want)
	}
	b, _ := f.sim.ReadEtab(context.Background())
	etab := endpoint.ParseEtab(b)
	for _, c := range []string{"10.20.0.0/24", "10.20.1.0/24"} {
		if got, ok := etab.Lookup(v.GetVolumeContext()[zcsi.CtxShare], c); !ok || got.ReadOnly {
			t.Errorf("etab does not serve %s rw at %q", c, v.GetVolumeContext()[zcsi.CtxShare])
		}
	}
	mp, err := f.z.Get(context.Background(), ds, "mountpoint")
	if err != nil {
		t.Fatal(err)
	}
	share := v.GetVolumeContext()[zcsi.CtxShare]
	if p := mp.Props["mountpoint"]; p.Value != share || !p.Local() {
		t.Errorf("mountpoint %q (%s), share %q: want the share pinned locally", p.Value, p.Source, share)
	}
	if err := f.z.Set(context.Background(), nfsParent, map[string]string{"mountpoint": "/elsewhere"}); err != nil {
		t.Fatal(err)
	}
	if got := f.list(t)[v.GetVolumeId()].GetVolumeContext()[zcsi.CtxShare]; got != share {
		t.Errorf("share after the parent moved: %q, want %q", got, share)
	}
	ro := f.create(t, nfsReq("n5ro", map[string]string{nfsClients: "10.20.0.0/24"}, mountCap(mnro, ""), mountCap(snro, "")))
	if got, want := f.sim.Prop(ro.GetVolumeContext()[zcsi.CtxDataset], "sharenfs"), "ro=10.20.0.0/24"; got != want {
		t.Errorf("reader-only modes: sharenfs %q, want %q", got, want)
	}
}

// N6 (§4, §8 C3): with nfsd down CreateVolume is Unavailable and the
// volume is not committed; the retry after nfsd starts succeeds.
func TestNFS_N6_NFSDDownNotCommitted(t *testing.T) {
	t.Parallel()
	f := nfsFixture(t, srv1)
	f.sim.SetNFSDRunning(false)
	r := nfsReq("n6", map[string]string{nfsClients: "10.20.0.0/24"})
	_, err := f.ctl.CreateVolume(context.Background(), r)
	if status.Code(err) != codes.Unavailable {
		t.Errorf("nfsd down: got %v, want Unavailable", err)
	}
	if got := f.list(t); len(got) != 0 {
		t.Errorf("an unserved volume was committed: %v", got)
	}
	f.sim.SetNFSDRunning(true)
	f.create(t, r)
}

// N7 (§3, ADR 0004): csi-driver-nfs cannot publish a zvol.
func TestNFS_N7_BlockRefused(t *testing.T) {
	t.Parallel()
	f := nfsFixture(t, srv1)
	f.ctl.EnableBlock = true // NFS mode refuses it whatever the flag
	_, err := f.ctl.CreateVolume(context.Background(), nfsReq("n7", map[string]string{nfsClients: "10.20.0.0/24"}, blockCap(snw)))
	refusedForValue(t, err, "block access with nfs.clients")
}

// N8 (§7): a filesystem takes all five access modes over NFS.
func TestNFS_N8_MultiNodeModes(t *testing.T) {
	t.Parallel()
	f := nfsFixture(t, srv1)
	for i, m := range []csi.VolumeCapability_AccessMode_Mode{mnro, mnsw, mnmw} {
		if _, err := f.ctl.CreateVolume(context.Background(), nfsReq("n8"+string(rune('a'+i)), map[string]string{nfsClients: "10.20.0.0/24"}, mountCap(m, "nfs"))); err != nil {
			t.Errorf("%v: %v", m, err)
		}
	}
}

// N9 (§6 as amended by Q1): idempotency compares the parsed client set and
// mode; a committed volume keeps its server.
func TestNFS_N9_Idempotency(t *testing.T) {
	t.Parallel()
	f := nfsFixture(t, srv1, srv2)
	ctx := context.Background()
	r := nfsReq("n9", map[string]string{nfsClients: "10.20.0.0/24,10.20.1.0/24"})
	first := f.create(t, r)
	again := f.create(t, r)
	if again.GetVolumeId() != first.GetVolumeId() || !maps.Equal(again.GetVolumeContext(), first.GetVolumeContext()) {
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
	// inverted (Q1): the server is volume state
	_, err = f.ctl.CreateVolume(ctx, nfsReq("n9", map[string]string{nfsClients: "10.20.0.0/24,10.20.1.0/24", nfsServer: srv2}))
	if status.Code(err) != codes.AlreadyExists || !strings.Contains(err.Error(), srv1) {
		t.Errorf("another server: got %v, want AlreadyExists naming the recorded %s", err, srv1)
	}
	// omitted after a reorder: the recorded one
	f.restart(t, srv2, srv1)
	if v := f.create(t, r); v.GetVolumeContext()[zcsi.CtxServer] != srv1 {
		t.Errorf("after a reorder: context %v, want the recorded %s", v.GetVolumeContext(), srv1)
	}
}

// N9, uncommitted half: nobody holds its context yet, so it converges to
// the retry, server included.
func TestNFS_N9_UncommittedConverges(t *testing.T) {
	t.Parallel()
	f := nfsFixture(t, srv1, srv2)
	f.sim.SetNFSDRunning(false)
	if _, err := f.ctl.CreateVolume(context.Background(), nfsReq("n9u", map[string]string{nfsClients: "10.20.0.0/24"})); status.Code(err) != codes.Unavailable {
		t.Fatalf("nfsd down: %v", err)
	}
	f.sim.SetNFSDRunning(true)
	v := f.create(t, nfsReq("n9u", map[string]string{nfsClients: "10.30.0.0/24", nfsServer: srv2}))
	if v.GetVolumeContext()[zcsi.CtxServer] != srv2 || f.sim.Prop(v.GetVolumeContext()[zcsi.CtxDataset], "sharenfs") != "rw=10.30.0.0/24" {
		t.Errorf("not converged: %v", v.GetVolumeContext())
	}
}

// N10 (§7, Q1): confirmed iff an exported filesystem, mount with fs_type
// "" or "nfs", no writer on a ro-only export, parameters parse, and each
// given volume_context key is the volume's (the provisioner's own key is
// tolerated). A mismatch is a Message, not an error.
func TestNFS_N10_Validate(t *testing.T) {
	t.Parallel()
	f := nfsFixture(t, srv1, srv2)
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
	with := func(edit map[string]string) map[string]string {
		m := maps.Clone(rw.GetVolumeContext())
		maps.Copy(m, edit)
		return m
	}
	check("fs_type nfs", rw, true, nil, mountCap(snw, "nfs"))
	check("multi-node writer", rw, true, nil, mountCap(mnmw, ""))
	check("own context", rw, true, rw.GetVolumeContext(), mountCap(snw, ""))
	check("own context with the provisioner's key", rw, true, with(map[string]string{"storage.kubernetes.io/csiProvisionerIdentity": "1-2-zfs"}), mountCap(snw, ""))
	check("reader on ro export", ro, true, nil, mountCap(mnro, ""))
	check("block", rw, false, nil, blockCap(snw))
	check("fs_type ext4", rw, false, nil, mountCap(snw, "ext4"))
	check("fs_type zfs", rw, false, nil, mountCap(snw, "zfs"))
	check("writer on ro export", ro, false, nil, mountCap(snw, ""))
	check("share mismatch", rw, false, with(map[string]string{zcsi.CtxShare: "/elsewhere"}), mountCap(snw, ""))
	check("server not advertised", rw, false, with(map[string]string{zcsi.CtxServer: "198.51.100.1"}), mountCap(snw, ""))
	check("advertised, but not the recorded server", rw, false, with(map[string]string{zcsi.CtxServer: srv2}), mountCap(snw, ""))
	f.restart(t, srv2)
	check("recorded server no longer advertised", rw, false, nil, mountCap(snw, ""))
}

// N12 (§7, Fact E): DeleteVolume needs no explicit unshare.
func TestNFS_N12_DeleteUnexports(t *testing.T) {
	t.Parallel()
	f := nfsFixture(t, srv1)
	v := f.create(t, nfsReq("n12", map[string]string{nfsClients: "10.20.0.0/24"}))
	if _, err := f.ctl.DeleteVolume(context.Background(), &csi.DeleteVolumeRequest{VolumeId: v.GetVolumeId()}); err != nil {
		t.Fatal(err)
	}
	b, _ := f.sim.ReadEtab(context.Background())
	if strings.Contains(string(b), v.GetVolumeContext()[zcsi.CtxShare]+"\t") {
		t.Errorf("etab still serves %s after DeleteVolume:\n%s", v.GetVolumeContext()[zcsi.CtxShare], b)
	}
}

// N13 (§4): a restore or clone takes the request's clients and server,
// never the source's.
func TestNFS_N13_CloneTakesRequestExport(t *testing.T) {
	t.Parallel()
	f := nfsFixture(t, srv1, srv2)
	ctx := context.Background()
	src := f.create(t, nfsReq("n13src", map[string]string{nfsClients: "10.20.0.0/24"}))
	snap, err := f.ctl.CreateSnapshot(ctx, &csi.CreateSnapshotRequest{Name: "n13snap", SourceVolumeId: src.GetVolumeId()})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []*csi.CreateVolumeRequest{
		nfsReq("n13restore", map[string]string{nfsClients: "10.30.0.0/24", nfsServer: srv2}),
		nfsReq("n13clone", map[string]string{nfsClients: "10.30.0.0/24", nfsServer: srv2}),
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
		if v.GetVolumeContext()[zcsi.CtxServer] != srv2 || v.GetVolumeContext()[zcsi.CtxShare] == "" {
			t.Errorf("%s: volume_context %v, want the request's server and a share", r.Name, v.GetVolumeContext())
		}
	}
}

// N14 (spec "SHALL always report the same volume_context", Q1):
// ListVolumes reports the context CreateVolume returned, across a restart
// with the servers reordered; a pre-NFS volume reports no server.
func TestNFS_N14_ListVolumesSameContext(t *testing.T) {
	t.Parallel()
	f := nfsFixture(t)
	plain := f.create(t, nfsReq("plain", nil))
	f.restart(t, srv1, srv2)
	v := f.create(t, nfsReq("n14", map[string]string{nfsClients: "10.20.0.0/24"}))
	f.restart(t, srv2, srv1)
	got := f.list(t)
	if !maps.Equal(got[v.GetVolumeId()].GetVolumeContext(), v.GetVolumeContext()) {
		t.Errorf("ListVolumes context %v, CreateVolume context %v", got[v.GetVolumeId()].GetVolumeContext(), v.GetVolumeContext())
	}
	if !maps.Equal(got[plain.GetVolumeId()].GetVolumeContext(), plain.GetVolumeContext()) {
		t.Errorf("pre-NFS volume: listed %v, created %v", got[plain.GetVolumeId()].GetVolumeContext(), plain.GetVolumeContext())
	}
	if _, ok := plain.GetVolumeContext()[zcsi.CtxServer]; ok {
		t.Errorf("pre-NFS volume has a server: %v", plain.GetVolumeContext())
	}
}

// N15 (§3, widened by Q2): in NFS mode every mount CreateVolume needs
// nfs.clients, clones and restores included, and the refusal names it.
func TestNFS_N15_ClientsRequired(t *testing.T) {
	t.Parallel()
	f := nfsFixture(t, srv1)
	src := f.create(t, nfsReq("src", map[string]string{nfsClients: "10.20.0.0/24"}))
	clone := nfsReq("clone", nil)
	clone.VolumeContentSource = &csi.VolumeContentSource{Type: &csi.VolumeContentSource_Volume{Volume: &csi.VolumeContentSource_VolumeSource{VolumeId: src.GetVolumeId()}}}
	for _, r := range []*csi.CreateVolumeRequest{
		nfsReq("none", nil),
		nfsReq("server", map[string]string{nfsServer: srv1}),
		nfsReq("uid", map[string]string{"nfs.uid": "1000"}),
		nfsReq("sparse", map[string]string{zcsi.ParamReserve: "true"}),
		clone,
	} {
		_, err := f.ctl.CreateVolume(context.Background(), r)
		refusedForValue(t, err, r.Name)
		if err != nil && !strings.Contains(err.Error(), nfsClients+" is required") {
			t.Errorf("%s: %v does not name %s", r.Name, err, nfsClients)
		}
	}
}

// Outside NFS mode the nfs.* keys are refused for the mode, never as
// unknown, and nothing else changes: one node, fs_type zfs.
func TestNFS_OffWithoutServers(t *testing.T) {
	t.Parallel()
	f := nfsFixture(t)
	for _, p := range []map[string]string{{nfsClients: "10.20.0.0/24"}, {nfsServer: srv1}} {
		_, err := f.ctl.CreateVolume(context.Background(), nfsReq("v", p))
		refusedForValue(t, err, "NFS parameter without NFS mode")
		if err != nil && !strings.Contains(err.Error(), "-nfs-server") {
			t.Errorf("unclear: %v", err)
		}
	}
	for _, c := range []*csi.VolumeCapability{mountCap(mnmw, ""), mountCap(snw, "nfs")} {
		_, err := f.ctl.CreateVolume(context.Background(), nfsReq("v", nil, c))
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("%v: %v", c, err)
		}
	}
	v := f.create(t, nfsReq("v", nil, mountCap(snw, "zfs")))
	if _, ok := v.GetVolumeContext()[zcsi.CtxServer]; ok {
		t.Errorf("context %v", v.GetVolumeContext())
	}
}

// N16 (Q1): an nfs_server inherited from the parent is no volume's.
func TestNFS_N16_InheritedServerIgnored(t *testing.T) {
	t.Parallel()
	f := nfsFixture(t)
	if err := f.z.Set(context.Background(), nfsParent, map[string]string{endpoint.DefaultPropertyPrefix + ":nfs_server": "198.51.100.1"}); err != nil {
		t.Fatal(err)
	}
	v := f.create(t, nfsReq("plain", nil))
	if s, ok := v.GetVolumeContext()[zcsi.CtxServer]; ok {
		t.Errorf("inherited server %q in volume_context", s)
	}
}
