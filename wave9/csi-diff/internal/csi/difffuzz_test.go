package csi_test

// Differential fuzzer for the CSI front end (wave9).
//
// Path A is the shipping stack: csi request -> internal/csi Controller ->
// gRPC (bufconn) -> internal/server -> internal/endpoint -> zfstest sim A.
// A unary interceptor on the server records what reached the endpoint and
// with which code, so a difference is located in the CSI layer or below.
//
// Path B is the same logical operation sent straight to a second endpoint
// on sim B, with the naive mapping a translator would use (first
// capability picks the kind, capacities converted to uint64, sparse/reserve
// parsed, everything else dropped). B runs live when A reached the
// endpoint, so both sims stay in lockstep; when the CSI layer refused the
// request before the endpoint, B runs on a throwaway replica (sim rebuilt
// from B's log) so the comparison costs no divergence.
//
// Every A response is judged by the spec oracle (oracle_test.go), and every
// (A, B) pair is diffed: codes, capacity, ids, volume_context, content
// source. Each finding has a signature; triage maps signatures to verdicts
// (csiTriage below). Untriaged findings fail the test.
//
//   go test ./internal/csi -run TestCSIDiffSeeds -v            # 2x300 sequences
//   CSI_DIFF_REPORT=/tmp/r.json go test ./internal/csi -run TestCSIDiffSeeds
//   go test ./internal/csi -run '^$' -fuzz FuzzCSIDiff

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"

	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
	zcsi "github.com/wommy/empty/internal/csi"
	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/server"
	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// ---- stack -------------------------------------------------------------------

const (
	shortParent = "tank/vols"
)

// longParent leaves less than 128 bytes for a volume id under ZFS's 255-byte
// dataset name limit (ZFS_MAX_DATASET_NAME_LEN 256 incl. NUL).
var longParent = "tank/" + strings.Repeat("p", 130)

type srvCall struct {
	method string
	code   codes.Code
}

type stack struct {
	parent string
	ctl    *zcsi.Controller
	mu     sync.Mutex
	calls  []srvCall
	stop   func()

	epB  *endpoint.Endpoint
	logB []func(context.Context, *endpoint.Endpoint)
}

func newSim() *zfstest.Sim {
	s := zfstest.New("tank")
	s.Clock = func() int64 { return 1_700_000_000 }
	return s
}

func newEP(parent string) (*endpoint.Endpoint, error) {
	z := zfs.New(newSim())
	if err := z.CreateFilesystem(context.Background(), parent, nil, true); err != nil {
		return nil, err
	}
	return endpoint.New(z, endpoint.Config{Parent: parent, LockWait: time.Second})
}

func newStack(parent string) (*stack, error) {
	epA, err := newEP(parent)
	if err != nil {
		return nil, err
	}
	epB, err := newEP(parent)
	if err != nil {
		return nil, err
	}
	s := &stack{parent: parent, epB: epB}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		resp, err := h(ctx, req)
		s.mu.Lock()
		s.calls = append(s.calls, srvCall{info.FullMethod[strings.LastIndexByte(info.FullMethod, '/')+1:], status.Code(err)})
		s.mu.Unlock()
		return resp, err
	}))
	pb.RegisterEndpointServiceServer(srv, &server.Server{EP: epA, Parent: parent})
	go srv.Serve(lis)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		srv.Stop()
		return nil, err
	}
	s.ctl = &zcsi.Controller{EP: pb.NewEndpointServiceClient(conn)}
	s.stop = func() { conn.Close(); srv.Stop() }
	return s, nil
}

func (s *stack) takeCalls() []srvCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.calls
	s.calls = nil
	return c
}

// replicaB is sim B as it stands, rebuilt from B's log.
func (s *stack) replicaB(ctx context.Context) *endpoint.Endpoint {
	ep, err := newEP(s.parent)
	if err != nil {
		panic(err)
	}
	for _, f := range s.logB {
		f(ctx, ep)
	}
	return ep
}

// ---- normalized results --------------------------------------------------------

type nvol struct {
	ID       string            `json:"id"`
	Capacity int64             `json:"capacity"`
	Context  map[string]string `json:"context,omitempty"`
	Source   string            `json:"source,omitempty"`
}

type nsnap struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Size   int64  `json:"size"`
	Ready  bool   `json:"ready"`
	Time   int64  `json:"time"`
}

type result struct {
	Code  codes.Code
	Msg   string
	Vol   *nvol
	Snap  *nsnap
	Vols  []nvol
	Snaps []nsnap
	Cap   int64
}

func csiVol(v *csi.Volume) *nvol {
	n := &nvol{ID: v.GetVolumeId(), Capacity: v.GetCapacityBytes(), Context: v.GetVolumeContext()}
	if s := v.GetContentSource(); s != nil {
		n.Source = prototext.Format(s)
	}
	return n
}

func csiSnap(s *csi.Snapshot) *nsnap {
	return &nsnap{ID: s.GetSnapshotId(), Source: s.GetSourceVolumeId(), Size: s.GetSizeBytes(), Ready: s.GetReadyToUse(), Time: s.GetCreationTime().GetSeconds()}
}

func clampI64(n uint64) int64 {
	if n > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(n)
}

// wantContext is the documented CSI context contract (controller.go
// CtxDataset/CtxKind/CtxDevicePath/CtxMountpoint) applied to an endpoint
// volume.
func wantContext(v endpoint.Volume) map[string]string {
	m := map[string]string{zcsi.CtxDataset: v.Dataset, zcsi.CtxKind: v.Kind.String()}
	if v.DevicePath != "" {
		m[zcsi.CtxDevicePath] = v.DevicePath
	}
	if v.Mountpoint != "" {
		m[zcsi.CtxMountpoint] = v.Mountpoint
	}
	return m
}

func epVol(v endpoint.Volume) *nvol {
	n := &nvol{ID: v.ID, Capacity: clampI64(v.CapacityBytes), Context: wantContext(v)}
	switch v.SourceType {
	case "snapshot":
		n.Source = prototext.Format(&csi.VolumeContentSource{Type: &csi.VolumeContentSource_Snapshot{Snapshot: &csi.VolumeContentSource_SnapshotSource{SnapshotId: v.SourceID}}})
	case "volume":
		n.Source = prototext.Format(&csi.VolumeContentSource{Type: &csi.VolumeContentSource_Volume{Volume: &csi.VolumeContentSource_VolumeSource{VolumeId: v.SourceID}}})
	}
	return n
}

func epSnap(s endpoint.Snapshot) *nsnap {
	return &nsnap{ID: s.ID, Source: s.SourceVolumeID, Size: clampI64(s.SizeBytes), Ready: s.ReadyToUse, Time: s.CreationUnix}
}

func epCode(err error) codes.Code { return codes.Code(endpoint.CodeOf(err)) }

func epRes(err error) result {
	r := result{Code: epCode(err)}
	if err != nil {
		r.Msg = err.Error()
	}
	return r
}

func csiRes(err error) result {
	return result{Code: status.Code(err), Msg: status.Convert(err).Message()}
}

// ---- the naive direct mapping (path B) ------------------------------------------

func u64(n int64) uint64 { return uint64(n) } // deliberately naive: negatives wrap

func directCreate(r *csi.CreateVolumeRequest) endpoint.CreateVolumeRequest {
	out := endpoint.CreateVolumeRequest{
		Name:          r.GetName(),
		CapacityBytes: u64(r.GetCapacityRange().GetRequiredBytes()),
		LimitBytes:    u64(r.GetCapacityRange().GetLimitBytes()),
	}
	if caps := r.GetVolumeCapabilities(); len(caps) > 0 && caps[0].GetBlock() != nil {
		out.Kind = endpoint.Block
	}
	out.Sparse, _ = strconv.ParseBool(r.GetParameters()["sparse"])
	out.Reserve, _ = strconv.ParseBool(r.GetParameters()["reserve"])
	out.SourceSnapshotID = r.GetVolumeContentSource().GetSnapshot().GetSnapshotId()
	out.SourceVolumeID = r.GetVolumeContentSource().GetVolume().GetVolumeId()
	return out
}

// ---- ops -----------------------------------------------------------------------

type opKind int

const (
	opCreate opKind = iota
	opDelete
	opValidate
	opListVols
	opListSnaps
	opCapacity
	opSnap
	opDelSnap
	opExpand
	opRetry
	nOps
)

var opNames = [...]string{"CreateVolume", "DeleteVolume", "ValidateVolumeCapabilities", "ListVolumes", "ListSnapshots",
	"GetCapacity", "CreateSnapshot", "DeleteSnapshot", "ControllerExpandVolume", "Retry"}

type op struct {
	kind     opKind
	create   *csi.CreateVolumeRequest
	del      *csi.DeleteVolumeRequest
	validate *csi.ValidateVolumeCapabilitiesRequest
	listMax  int32
	listTok  string // a starting token to try (garbage) instead of a walk
	listSnap *csi.ListSnapshotsRequest
	capReq   *csi.GetCapacityRequest
	snap     *csi.CreateSnapshotRequest
	delSnap  *csi.DeleteSnapshotRequest
	expand   *csi.ControllerExpandVolumeRequest
}

func (o op) String() string {
	var m proto.Message
	switch o.kind {
	case opCreate:
		m = o.create
	case opDelete:
		m = o.del
	case opValidate:
		m = o.validate
	case opListVols:
		return fmt.Sprintf("ListVolumes walk max=%d token=%q", o.listMax, o.listTok)
	case opListSnaps:
		m = o.listSnap
	case opCapacity:
		m = o.capReq
	case opSnap:
		m = o.snap
	case opDelSnap:
		m = o.delSnap
	case opExpand:
		m = o.expand
	}
	return opNames[o.kind] + " {" + strings.TrimSpace(prototext.MarshalOptions{}.Format(m)) + "}"
}

// chooser is the fuzzer's source of choices: math/rand for seeded runs,
// the fuzz input for coverage-guided ones.
type chooser interface{ intn(n int) int }

type randChooser struct{ *rand.Rand }

func (r randChooser) intn(n int) int { return r.Intn(n) }

type byteChooser struct {
	b []byte
	i int
}

func (c *byteChooser) intn(n int) int {
	if c.i >= len(c.b) {
		return 0
	}
	v := int(c.b[c.i])
	c.i++
	return v % n
}

func pick[T any](c chooser, xs ...T) T { return xs[c.intn(len(xs))] }

var (
	volNames = []string{
		"pvc-1", "pvc-2", "pvc-3", "pvc-4", "pvc-1", "pvc-2",
		strings.Repeat("a", 128),      // spec:494 maximum
		strings.Repeat("b", 129),      // over it
		"x" + strings.Repeat("y", 99), // 100 bytes: over the 91 a snapshot id leaves
		"",
		"πβ-volume",  // legal (spec:849)
		"with space", // legal
		"a/b", "a@b", // legal by the spec, not as a zfs component
		"tab\tname", // U+0009 is not banned (spec:851)
		"ctl\x01",   // banned
		"-leading",  // legal; zfs wants a leading alnum
		"ok.name_1:2",
	}
	snapNames = []string{
		"snap-1", "snap-2", "snap-3", "snap-1",
		strings.Repeat("c", 128), strings.Repeat("d", 129),
		"", "π-snap", "s/lash", "tab\tsnap", "ctl\x02",
		"volume-source-for-volume-x", // legal CSI name, endpoint-reserved prefix
	}
	modes = []csi.VolumeCapability_AccessMode_Mode{
		csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER, csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
		csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER, csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY,
		csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY, csi.VolumeCapability_AccessMode_MULTI_NODE_SINGLE_WRITER,
		csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER, csi.VolumeCapability_AccessMode_SINGLE_NODE_SINGLE_WRITER,
		csi.VolumeCapability_AccessMode_SINGLE_NODE_MULTI_WRITER, csi.VolumeCapability_AccessMode_UNKNOWN,
	}
	ranges = []*csi.CapacityRange{
		nil, nil, nil,
		{}, {RequiredBytes: 1}, {LimitBytes: 1}, {RequiredBytes: 16383},
		{RequiredBytes: 16384, LimitBytes: 16384}, {RequiredBytes: 1 << 30}, {RequiredBytes: 1 << 30},
		{RequiredBytes: 1 << 30, LimitBytes: 1 << 30}, {RequiredBytes: 1<<30 + 1, LimitBytes: 1<<30 + 1},
		{RequiredBytes: 1<<30 + 1, LimitBytes: 1<<30 + 16384}, {RequiredBytes: 2 << 30, LimitBytes: 1 << 30},
		{LimitBytes: 2 << 30}, {RequiredBytes: -1}, {LimitBytes: -5},
		{RequiredBytes: math.MaxInt64}, {RequiredBytes: math.MaxInt64 - 100}, {LimitBytes: math.MaxInt64},
		{RequiredBytes: 3 << 30},
	}
	params = []map[string]string{
		nil, nil, nil, nil,
		{"sparse": "true"}, {"sparse": "false"}, {"reserve": "true"}, {"reserve": "false"},
		{"sparse": "yes"}, {"foo": "bar"},
		{"nfs.clients": "10.0.0.0/24"}, {"nfs.server": "192.0.2.1"},
		{"nfs.clients": "10.0.0.0/24", "nfs.server": "192.0.2.1"},
		{"csi.storage.k8s.io/pvc/name": "claim"},
		{"reserve": "true", "csi.storage.k8s.io/pv/name": "pv"},
	}
)

func genCap(c chooser) *csi.VolumeCapability {
	vc := &csi.VolumeCapability{}
	if c.intn(20) != 0 {
		vc.AccessMode = &csi.VolumeCapability_AccessMode{Mode: pick(c, modes...)}
	}
	switch c.intn(9) {
	case 0, 1, 2:
		vc.AccessType = &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}}
	case 3, 4, 5:
		vc.AccessType = &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}
	case 6:
		vc.AccessType = &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{FsType: "zfs"}}
	case 7:
		vc.AccessType = &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{FsType: pick(c, "ext4", "nfs", "xfs")}}
	case 8:
		// no access type
	}
	return vc
}

func genCaps(c chooser) []*csi.VolumeCapability {
	switch c.intn(12) {
	case 0:
		return nil
	case 1, 2:
		return []*csi.VolumeCapability{genCap(c), genCap(c)}
	}
	return []*csi.VolumeCapability{genCap(c)}
}

type gen struct {
	c       chooser
	volIDs  []string // every volume id ever returned
	snapIDs []string
	last    *op
}

func (g *gen) volID() string {
	if len(g.volIDs) > 0 && g.c.intn(4) != 0 {
		return pick(g.c, g.volIDs...)
	}
	return pick(g.c, "", "nope", "a/b", strings.Repeat("z", 129), "pvc-1")
}

func (g *gen) snapID() string {
	if len(g.snapIDs) > 0 && g.c.intn(4) != 0 {
		return pick(g.c, g.snapIDs...)
	}
	return pick(g.c, "", "nope@x", "garbage", "pvc-1@snap-1", "pvc-1@volume-source-for-volume-pvc-2")
}

func (g *gen) source() *csi.VolumeContentSource {
	switch g.c.intn(10) {
	case 0, 1:
		return &csi.VolumeContentSource{Type: &csi.VolumeContentSource_Snapshot{Snapshot: &csi.VolumeContentSource_SnapshotSource{SnapshotId: g.snapID()}}}
	case 2, 3:
		return &csi.VolumeContentSource{Type: &csi.VolumeContentSource_Volume{Volume: &csi.VolumeContentSource_VolumeSource{VolumeId: g.volID()}}}
	case 4:
		if g.c.intn(3) == 0 {
			return &csi.VolumeContentSource{}
		}
	}
	return nil
}

func (g *gen) next() op {
	c := g.c
	k := opKind(pick(c, 0, 0, 0, 0, 0, 0, 0, 1, 1, 2, 2, 3, 4, 5, 6, 6, 6, 7, 7, 8, 8, 9, 9))
	if k == opRetry {
		if g.last != nil {
			o := *g.last
			return o
		}
		k = opCreate
	}
	o := op{kind: k}
	switch k {
	case opCreate:
		o.create = &csi.CreateVolumeRequest{
			Name: pick(c, volNames...), VolumeCapabilities: genCaps(c),
			CapacityRange: pick(c, ranges...), Parameters: pick(c, params...),
			VolumeContentSource: g.source(),
		}
		if len(g.snapIDs) > 0 && c.intn(6) == 0 {
			// a well-formed restore, so size rules on clones get exercised
			o.create.VolumeCapabilities = []*csi.VolumeCapability{{
				AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
				AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
			}}
			if c.intn(3) == 0 {
				o.create.VolumeCapabilities[0].AccessType = &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}}
			}
			o.create.Parameters = nil
			o.create.CapacityRange = pick(c, nil, &csi.CapacityRange{RequiredBytes: 1 << 30}, &csi.CapacityRange{RequiredBytes: 1}, &csi.CapacityRange{RequiredBytes: 16384})
			o.create.VolumeContentSource = &csi.VolumeContentSource{Type: &csi.VolumeContentSource_Snapshot{Snapshot: &csi.VolumeContentSource_SnapshotSource{SnapshotId: pick(c, g.snapIDs...)}}}
		}
		if c.intn(25) == 0 {
			o.create.AccessibilityRequirements = &csi.TopologyRequirement{Requisite: []*csi.Topology{{Segments: map[string]string{"zone": "a"}}}}
		}
		if c.intn(25) == 0 {
			o.create.MutableParameters = map[string]string{"sparse": "true"}
		}
	case opDelete:
		o.del = &csi.DeleteVolumeRequest{VolumeId: g.volID()}
	case opValidate:
		o.validate = &csi.ValidateVolumeCapabilitiesRequest{VolumeId: g.volID(), VolumeCapabilities: genCaps(c), Parameters: pick(c, params...)}
		if c.intn(4) == 0 {
			o.validate.VolumeContext = map[string]string{"dataset": "tank/other", "kind": "block"}
		}
	case opListVols:
		o.listMax = int32(pick(c, 0, 1, 2, 3, -1))
		if c.intn(6) == 0 {
			o.listTok = pick(c, "bogus", "zzzz")
		}
	case opListSnaps:
		o.listSnap = &csi.ListSnapshotsRequest{MaxEntries: int32(pick(c, 0, 1, 2, -1))}
		switch c.intn(4) {
		case 0:
			o.listSnap.SnapshotId = g.snapID()
		case 1:
			o.listSnap.SourceVolumeId = g.volID()
		}
		if c.intn(8) == 0 {
			o.listSnap.StartingToken = "bogus"
		}
	case opCapacity:
		o.capReq = &csi.GetCapacityRequest{}
		if c.intn(2) == 0 {
			o.capReq.VolumeCapabilities = genCaps(c)
			o.capReq.Parameters = pick(c, params...)
		}
	case opSnap:
		o.snap = &csi.CreateSnapshotRequest{Name: pick(c, snapNames...), SourceVolumeId: g.volID()}
		if c.intn(10) == 0 {
			o.snap.Parameters = map[string]string{"foo": "bar"}
		}
	case opDelSnap:
		o.delSnap = &csi.DeleteSnapshotRequest{SnapshotId: g.snapID()}
	case opExpand:
		o.expand = &csi.ControllerExpandVolumeRequest{VolumeId: g.volID(), CapacityRange: pick(c, ranges...)}
		if c.intn(2) == 0 {
			o.expand.VolumeCapability = genCap(c)
		}
	}
	g.last = &o
	return o
}

// ---- findings ---------------------------------------------------------------------

type finding struct {
	Sig     string `json:"sig"`
	Detail  string `json:"detail"`
	Op      string `json:"op"`
	Parent  string `json:"parent"`
	Verdict string `json:"verdict,omitempty"`
}

type runner struct {
	hist  []string // ops so far, for CSI_DIFF_TRACE
	s     *stack
	w     *world
	g     *gen
	found []finding
	halt  bool // A and B diverged in state: stop the sequence
}

func (r *runner) add(sig, detail string, o op) {
	f := finding{Sig: sig, Detail: detail, Op: o.String(), Parent: r.s.parent}
	if tr := os.Getenv("CSI_DIFF_TRACE"); tr != "" && strings.Contains(sig, tr) {
		f.Detail += "\n  history:\n    " + strings.Join(r.hist, "\n    ")
	}
	r.found = append(r.found, f)
}

func (r *runner) judge(rpc string, v *verdict, err error, resp any, o op) {
	code := status.Code(err)
	for _, x := range judge(rpc, v, code, resp) {
		sig := "oracle|" + x.rule
		if code != codes.OK {
			// the tags say what the oracle saw, the message what the plugin did
			sig = fmt.Sprintf("oracle|%s:code:%s|%s", rpc, code, normMsg(status.Convert(err).Message()))
		}
		r.add(sig, x.cite+": "+x.detail+" ["+strings.Join(v.tags, "+")+"] "+status.Convert(err).Message(), o)
	}
}

var (
	reQuoted = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)
	reIDs    = regexp.MustCompile(`tank/\S+|pvc-\d|[ab]{20,}|x?y{20,}|ok\.name_1:2|πβ-volume|snap-\d|[cdz]{20,}|nope\S*|-leading|garbage`)
	reNum    = regexp.MustCompile(`\d+`)
	reMode   = regexp.MustCompile(`(SINGLE|MULTI)_NODE_[A-Z_]+|UNKNOWN`)
)

// normMsg reduces a status message to its reason, for stable signatures.
func normMsg(m string) string {
	m = reQuoted.ReplaceAllString(m, "Q")
	m = reIDs.ReplaceAllString(m, "ID")
	m = reMode.ReplaceAllString(m, "MODE")
	m = reNum.ReplaceAllString(m, "N")
	if len(m) > 90 {
		m = m[:90]
	}
	return m
}

// diff compares A with B. reached says A's request reached the endpoint;
// srv is the code the server returned for the last endpoint call.
func (r *runner) diff(rpc string, o op, a, b result, reached bool, srv codes.Code, tags []string, mutating bool) {
	where := "post"
	if !reached {
		where = "precheck"
	}
	tag := normMsg(a.Msg)
	if a.Code == codes.OK {
		tag = normMsg(b.Msg)
	}
	if a.Code != b.Code {
		class := "code"
		if reached && srv != b.Code {
			class = "server-vs-direct"
			if mutating {
				r.halt = true
			}
		} else if reached && srv != a.Code {
			class = "csi-rewrote-code"
		} else if !reached && b.Code == codes.OK {
			class = "csi-stricter"
		}
		r.add(fmt.Sprintf("diff|%s|%s|%s|A=%s|B=%s|%s", rpc, where, class, a.Code, b.Code, tag), "A: "+a.Msg+" | B: "+b.Msg+" ["+strings.Join(tags, "+")+"]", o)
		return
	}
	if a.Code != codes.OK {
		return
	}
	cmp := func(field string, x, y any) {
		xj, _ := json.Marshal(x)
		yj, _ := json.Marshal(y)
		if string(xj) != string(yj) {
			r.add(fmt.Sprintf("diff|%s|field|%s", rpc, field), fmt.Sprintf("A=%s B=%s", xj, yj), o)
			if mutating {
				r.halt = true
			}
		}
	}
	if a.Vol != nil || b.Vol != nil {
		cmp("volume", a.Vol, b.Vol)
	}
	if a.Snap != nil || b.Snap != nil {
		cmp("snapshot", a.Snap, b.Snap)
	}
	if a.Vols != nil || b.Vols != nil {
		cmp("list", a.Vols, b.Vols)
	}
	if a.Snaps != nil || b.Snaps != nil {
		cmp("snaplist", a.Snaps, b.Snaps)
	}
	if rpc == "GetCapacity" {
		cmp("available", a.Cap, b.Cap)
	}
}

// onB runs f on sim B: live when A reached the endpoint (and logs it for
// replicas), else on a replica.
func (r *runner) onB(ctx context.Context, reached, mutating bool, f func(context.Context, *endpoint.Endpoint) result) result {
	if reached || !mutating {
		res := f(ctx, r.s.epB)
		if mutating && reached {
			r.s.logB = append(r.s.logB, func(ctx context.Context, ep *endpoint.Endpoint) { f(ctx, ep) })
		}
		return res
	}
	return f(ctx, r.s.replicaB(ctx))
}

func (r *runner) step(o op) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, w := r.s, r.w
	s.takeCalls()
	last := func() (bool, codes.Code) {
		cs := s.takeCalls()
		if len(cs) == 0 {
			return false, codes.OK
		}
		return true, cs[len(cs)-1].code
	}
	switch o.kind {
	case opCreate:
		v := oracleCreateVolume(w, o.create)
		resp, err := s.ctl.CreateVolume(ctx, o.create)
		code := status.Code(err)
		r.judge("CreateVolume", v, err, resp, o)
		reached, srv := last()
		a := csiRes(err)
		if code == codes.OK {
			a.Vol = csiVol(resp.GetVolume())
		}
		b := r.onB(ctx, reached, true, func(ctx context.Context, ep *endpoint.Endpoint) result {
			vol, err := ep.CreateVolume(ctx, directCreate(o.create))
			res := epRes(err)
			if err == nil {
				res.Vol = epVol(vol)
			}
			return res
		})
		r.diff("CreateVolume", o, a, b, reached, srv, v.tags, true)
		if code == codes.OK {
			id := resp.GetVolume().GetVolumeId()
			if !slices.Contains(r.g.volIDs, id) {
				r.g.volIDs = append(r.g.volIDs, id)
			}
			if w.vols[o.create.GetName()] == nil {
				w.vols[o.create.GetName()] = &absVol{name: o.create.GetName(), req: proto.Clone(o.create).(*csi.CreateVolumeRequest), vol: resp.GetVolume()}
				delete(w.deadVols, id)
			}
		}
		r.validateOwn(ctx, o)

	case opDelete:
		v := oracleDeleteVolume(w, o.del)
		_, err := s.ctl.DeleteVolume(ctx, o.del)
		code := status.Code(err)
		r.judge("DeleteVolume", v, err, &csi.DeleteVolumeResponse{}, o)
		reached, srv := last()
		b := r.onB(ctx, reached, true, func(ctx context.Context, ep *endpoint.Endpoint) result {
			return epRes(ep.DeleteVolume(ctx, o.del.GetVolumeId()))
		})
		r.diff("DeleteVolume", o, csiRes(err), b, reached, srv, v.tags, true)
		if code == codes.OK {
			if a := w.volByID(o.del.GetVolumeId()); a != nil {
				delete(w.vols, a.name)
				w.deadVols[a.id()] = true
			}
		}

	case opValidate:
		v := oracleValidate(w, o.validate)
		resp, err := s.ctl.ValidateVolumeCapabilities(ctx, o.validate)
		r.judge("ValidateVolumeCapabilities", v, err, resp, o)
		reached, srv := last()
		b := r.onB(ctx, reached, false, func(ctx context.Context, ep *endpoint.Endpoint) result {
			_, err := ep.GetVolume(ctx, o.validate.GetVolumeId())
			return epRes(err)
		})
		r.diff("ValidateVolumeCapabilities", o, csiRes(err), b, reached, srv, v.tags, false)

	case opListVols:
		r.walkVolumes(ctx, o)

	case opListSnaps:
		r.walkSnapshots(ctx, o)

	case opCapacity:
		v := oracleGetCapacity(w, o.capReq)
		resp, err := s.ctl.GetCapacity(ctx, o.capReq)
		code := status.Code(err)
		r.judge("GetCapacity", v, err, resp, o)
		reached, srv := last()
		b := r.onB(ctx, reached, false, func(ctx context.Context, ep *endpoint.Endpoint) result {
			n, err := ep.GetCapacity(ctx)
			res := epRes(err)
			res.Cap = clampI64(n)
			return res
		})
		r.diff("GetCapacity", o, result{Code: code, Msg: status.Convert(err).Message(), Cap: resp.GetAvailableCapacity()}, b, reached, srv, v.tags, false)

	case opSnap:
		v := oracleCreateSnapshot(w, o.snap)
		resp, err := s.ctl.CreateSnapshot(ctx, o.snap)
		code := status.Code(err)
		r.judge("CreateSnapshot", v, err, resp, o)
		reached, srv := last()
		a := csiRes(err)
		if code == codes.OK {
			a.Snap = csiSnap(resp.GetSnapshot())
		}
		b := r.onB(ctx, reached, true, func(ctx context.Context, ep *endpoint.Endpoint) result {
			sn, err := ep.CreateSnapshot(ctx, o.snap.GetName(), o.snap.GetSourceVolumeId())
			res := epRes(err)
			if err == nil {
				res.Snap = epSnap(sn)
			}
			return res
		})
		r.diff("CreateSnapshot", o, a, b, reached, srv, v.tags, true)
		if code == codes.OK {
			id := resp.GetSnapshot().GetSnapshotId()
			if !slices.Contains(r.g.snapIDs, id) {
				r.g.snapIDs = append(r.g.snapIDs, id)
			}
			w.snapSource[id] = resp.GetSnapshot().GetSourceVolumeId()
			if w.snaps[o.snap.GetName()] == nil {
				w.snaps[o.snap.GetName()] = &absSnap{name: o.snap.GetName(), req: o.snap, snap: resp.GetSnapshot()}
				delete(w.deadSnaps, id)
			}
		}

	case opDelSnap:
		v := oracleDeleteSnapshot(w, o.delSnap)
		_, err := s.ctl.DeleteSnapshot(ctx, o.delSnap)
		code := status.Code(err)
		r.judge("DeleteSnapshot", v, err, &csi.DeleteSnapshotResponse{}, o)
		reached, srv := last()
		b := r.onB(ctx, reached, true, func(ctx context.Context, ep *endpoint.Endpoint) result {
			return epRes(ep.DeleteSnapshot(ctx, o.delSnap.GetSnapshotId()))
		})
		r.diff("DeleteSnapshot", o, csiRes(err), b, reached, srv, v.tags, true)
		if code == codes.OK {
			if a := w.snapByID(o.delSnap.GetSnapshotId()); a != nil {
				delete(w.snaps, a.name)
				w.deadSnaps[o.delSnap.GetSnapshotId()] = true
			}
		}

	case opExpand:
		v := oracleExpand(w, o.expand)
		resp, err := s.ctl.ControllerExpandVolume(ctx, o.expand)
		code := status.Code(err)
		r.judge("ControllerExpandVolume", v, err, resp, o)
		reached, srv := last()
		a := csiRes(err)
		if code == codes.OK {
			a.Vol = &nvol{ID: o.expand.GetVolumeId(), Capacity: resp.GetCapacityBytes()}
		}
		b := r.onB(ctx, reached, true, func(ctx context.Context, ep *endpoint.Endpoint) result {
			vol, err := ep.ExpandVolume(ctx, o.expand.GetVolumeId(), u64(o.expand.GetCapacityRange().GetRequiredBytes()), u64(o.expand.GetCapacityRange().GetLimitBytes()))
			res := epRes(err)
			if err == nil {
				res.Vol = &nvol{ID: vol.ID, Capacity: clampI64(vol.CapacityBytes)}
			}
			return res
		})
		r.diff("ControllerExpandVolume", o, a, b, reached, srv, v.tags, true)
		if code == codes.OK {
			if a := w.volByID(o.expand.GetVolumeId()); a != nil {
				nv := proto.Clone(a.vol).(*csi.Volume)
				nv.CapacityBytes = resp.GetCapacityBytes()
				a.vol = nv
			}
		}
	}
}

// validateOwn: after any OK CreateVolume, ValidateVolumeCapabilities with
// the very capabilities the volume was created with must confirm
// (spec:863-866, 1479), and a CreateVolume retry with the capabilities
// Validate confirms must be compatible (spec:808).
func (r *runner) validateOwn(ctx context.Context, o op) {
	a := r.w.vols[o.create.GetName()]
	if a == nil {
		return
	}
	req := &csi.ValidateVolumeCapabilitiesRequest{VolumeId: a.id(), VolumeCapabilities: a.req.GetVolumeCapabilities(), Parameters: a.req.GetParameters()}
	v := oracleValidate(r.w, req)
	resp, err := r.s.ctl.ValidateVolumeCapabilities(ctx, req)
	r.judge("ValidateVolumeCapabilities(own)", v, err, resp, op{kind: opValidate, validate: req})
	r.s.takeCalls()

	// cross-check: the other access type, if Validate confirms it, must
	// make an otherwise identical CreateVolume compatible
	other := proto.Clone(a.req).(*csi.CreateVolumeRequest)
	for _, c := range other.VolumeCapabilities {
		if c.GetBlock() != nil {
			c.AccessType = &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}
		} else {
			c.AccessType = &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}}
		}
	}
	vreq := &csi.ValidateVolumeCapabilitiesRequest{VolumeId: a.id(), VolumeCapabilities: other.VolumeCapabilities}
	vres, err := r.s.ctl.ValidateVolumeCapabilities(ctx, vreq)
	r.s.takeCalls()
	if err != nil || vres.GetConfirmed() == nil {
		return
	}
	_, err = r.s.ctl.CreateVolume(ctx, other)
	r.s.takeCalls()
	if c := status.Code(err); c == codes.AlreadyExists {
		r.add("oracle|CreateVolume:validate-confirms-but-create-refuses",
			"spec:808,1479: ValidateVolumeCapabilities confirmed "+capsString(other.VolumeCapabilities)+" for "+a.id()+" but CreateVolume with them is ALREADY_EXISTS", op{kind: opCreate, create: other})
	} else if c == codes.OK {
		// an OK here could mutate nothing (idempotent), but B must agree
		_ = 0
	}
}

func capsString(cs []*csi.VolumeCapability) string {
	var out []string
	for _, c := range cs {
		out = append(out, strings.TrimSpace(prototext.Format(c)))
	}
	return "[" + strings.Join(out, "; ") + "]"
}

func (r *runner) walkVolumes(ctx context.Context, o op) {
	s, w := r.s, r.w
	if o.listTok != "" || o.listMax < 0 {
		req := &csi.ListVolumesRequest{MaxEntries: o.listMax, StartingToken: o.listTok}
		v := oracleListPage(w, req.MaxEntries, req.StartingToken)
		resp, err := s.ctl.ListVolumes(ctx, req)
		r.judge("ListVolumes", v, err, resp, o)
		reached, srv := last(s)
		_, _, berr := s.epB.ListVolumes(ctx, int(o.listMax), o.listTok)
		r.diff("ListVolumes", o, csiRes(err), epRes(berr), reached, srv, v.tags, false)
		return
	}
	var aIDs []string
	var aVols, bVols []nvol
	tok := ""
	for i := 0; i < 50; i++ {
		req := &csi.ListVolumesRequest{MaxEntries: o.listMax, StartingToken: tok}
		v := oracleListPage(w, req.MaxEntries, req.StartingToken)
		resp, err := s.ctl.ListVolumes(ctx, req)
		r.judge("ListVolumes", v, err, resp, o)
		s.takeCalls()
		if err != nil {
			return
		}
		for _, e := range resp.GetEntries() {
			aIDs = append(aIDs, e.GetVolume().GetVolumeId())
			aVols = append(aVols, *csiVol(e.GetVolume()))
		}
		if tok = resp.GetNextToken(); tok == "" {
			break
		}
		w.tokens[tok] = true
	}
	var live []string
	for _, v := range w.vols {
		live = append(live, v.id())
	}
	for _, x := range walkInvariant(w, aIDs, live, w.deadVols, "volumes") {
		r.add("oracle|ListVolumes:"+x.rule, x.cite+": "+x.detail, o)
	}
	tok = ""
	for i := 0; i < 50; i++ {
		vols, next, err := s.epB.ListVolumes(ctx, int(o.listMax), tok)
		if err != nil {
			break
		}
		for _, v := range vols {
			bVols = append(bVols, *epVol(v))
		}
		if tok = next; tok == "" {
			break
		}
	}
	r.diff("ListVolumes", o, result{Code: codes.OK, Vols: aVols}, result{Code: codes.OK, Vols: bVols}, true, codes.OK, nil, false)
}

func last(s *stack) (bool, codes.Code) {
	cs := s.takeCalls()
	if len(cs) == 0 {
		return false, codes.OK
	}
	return true, cs[len(cs)-1].code
}

func (r *runner) walkSnapshots(ctx context.Context, o op) {
	s, w := r.s, r.w
	req := proto.Clone(o.listSnap).(*csi.ListSnapshotsRequest)
	if req.StartingToken != "" || req.MaxEntries < 0 {
		v := oracleListPage(w, req.MaxEntries, req.StartingToken)
		resp, err := s.ctl.ListSnapshots(ctx, req)
		r.judge("ListSnapshots", v, err, resp, o)
		reached, srv := last(s)
		_, _, berr := s.epB.ListSnapshots(ctx, req.SnapshotId, req.SourceVolumeId, int(req.MaxEntries), req.StartingToken)
		r.diff("ListSnapshots", o, csiRes(err), epRes(berr), reached, srv, v.tags, false)
		return
	}
	var got []*csi.Snapshot
	var aSn, bSn []nsnap
	for i := 0; i < 50; i++ {
		v := oracleListPage(w, req.MaxEntries, req.StartingToken)
		resp, err := s.ctl.ListSnapshots(ctx, req)
		r.judge("ListSnapshots", v, err, resp, o)
		s.takeCalls()
		if err != nil {
			return
		}
		for _, e := range resp.GetEntries() {
			got = append(got, e.GetSnapshot())
			aSn = append(aSn, *csiSnap(e.GetSnapshot()))
		}
		if req.StartingToken = resp.GetNextToken(); req.StartingToken == "" {
			break
		}
		w.tokens[req.StartingToken] = true
	}
	for _, x := range snapshotFilterInvariant(w, o.listSnap, got) {
		r.add("oracle|ListSnapshots:"+x.rule, x.cite+": "+x.detail, o)
	}
	if o.listSnap.GetSnapshotId() == "" && o.listSnap.GetSourceVolumeId() == "" {
		var ids, live []string
		for _, sn := range got {
			ids = append(ids, sn.GetSnapshotId())
		}
		for _, sn := range w.snaps {
			live = append(live, sn.snap.GetSnapshotId())
		}
		for _, x := range walkInvariant(w, ids, live, w.deadSnaps, "snapshots") {
			r.add("oracle|ListSnapshots:"+x.rule, x.cite+": "+x.detail, o)
		}
	}
	tok := ""
	for i := 0; i < 50; i++ {
		sn, next, err := s.epB.ListSnapshots(ctx, o.listSnap.GetSnapshotId(), o.listSnap.GetSourceVolumeId(), int(o.listSnap.GetMaxEntries()), tok)
		if err != nil {
			break
		}
		for _, x := range sn {
			bSn = append(bSn, *epSnap(x))
		}
		if tok = next; tok == "" {
			break
		}
	}
	r.diff("ListSnapshots", o, result{Code: codes.OK, Snaps: aSn}, result{Code: codes.OK, Snaps: bSn}, true, codes.OK, nil, false)
}

// run executes n ops (or until the chooser runs dry when n < 0).
func runSequence(parent string, c chooser, n int) ([]finding, error) {
	s, err := newStack(parent)
	if err != nil {
		return nil, err
	}
	defer s.stop()
	r := &runner{s: s, w: newWorld(), g: &gen{c: c}}
	for i := 0; i < n && !r.halt; i++ {
		if bc, ok := c.(*byteChooser); ok && bc.i >= len(bc.b) {
			break
		}
		o := r.g.next()
		r.hist = append(r.hist, o.String())
		r.step(o)
	}
	return r.found, nil
}

// ---- triage -------------------------------------------------------------------------

// Verdicts (wave9/csi-diff.md):
const (
	vTranslation = "csi-translation-bug"
	vEndpoint    = "endpoint-bug"
	vAmbiguity   = "oracle-ambiguity"
	vIntentional = "intentional"
)

type triageRule struct {
	re      *regexp.Regexp
	verdict string
	note    string
}

func tr(pattern, verdict, note string) triageRule {
	return triageRule{regexp.MustCompile(pattern), verdict, note}
}

// csiTriage maps finding signatures to verdicts. Filled from the wave9 runs;
// see csi-diff.md for the evidence behind each.
var csiTriage = []triageRule{
	// ---- CSI translation bugs (internal/csi) ----
	tr(`^oracle\|CreateVolume:code:OK\[.*source-(snapshot|volume)-id-missing`, vTranslation,
		"T1: a content source with an empty snapshot_id/volume_id becomes no source; an empty volume is created and reported without content_source (spec:925-936 REQUIRED, 525)"),
	tr(`^oracle\|ControllerExpandVolume:code:OK\[.*exceeds-capabilities`, vTranslation,
		"T2: ControllerExpandVolume ignores volume_capability; block access on a filesystem volume is OK, though Validate refuses it (spec:2545 MUST INVALID_ARGUMENT)"),
	tr(`^oracle\|ControllerExpandVolume:code:OK\[.*caps-no-(access-type|mode)`, vTranslation,
		"T2: ControllerExpandVolume does not validate volume_capability (no access type / no mode accepted; spec:1027-1028, 1034-1035, 525)"),
	tr(`^oracle\|CreateVolume:validate-confirms-but-create-refuses`, vTranslation,
		"T3: ValidateVolumeCapabilities confirms mount on a zvol (capSupported), CreateVolume with the same capability is ALREADY_EXISTS (kindOf maps mount to FILESYSTEM): the plugin contradicts itself (spec:808, 1479)"),

	// ---- endpoint bugs (internal/endpoint) ----
	tr(`^oracle\|CreateVolume:code:InvalidArgument\|invalid volume name`, vEndpoint,
		"E1: volume names the spec allows (unicode, space, '/', '@', tab, leading '-') are refused: volumeID uses the CSI name as the zfs component; snapshot names are already hashed (snapComponent) (spec:849-851)"),
	tr(`^oracle\|CreateVolume:code:InvalidArgument\|scan ID zfs list ID invalid property: invalid name`, vEndpoint,
		"E2: a parent over 126 bytes is accepted, then a legal 128-byte name overflows zfs's 255-byte dataset name and is refused as an invalid property (spec:494; csi-test maxlen)"),
	tr(`^oracle\|CreateSnapshot:code:InvalidArgument\|invalid snapshot name`, vEndpoint,
		"E3: unicode.IsControl refuses TAB/LF/CR, which the spec allows in names (spec:2178-2181 bans only U+0000-0008, 000B, 000C, 000E-001F, 007F-009F)"),
	tr(`^oracle\|CreateSnapshot:code:InvalidArgument\|snapshot name prefix Q is reserved`, vEndpoint,
		"E4: the internal clone-source prefix volume-source-for-volume- is refused as a CSI snapshot name; any CO name is legal (spec:2178); the reservation belongs in a property, not the name space"),
	tr(`^oracle\|CreateSnapshot:code:InvalidArgument\|volume id Q too long to address snapshots`, vEndpoint,
		"E5: volumes with ids over 91 bytes cannot be snapshotted (ADR 0002, proposed)"),
	tr(`^oracle\|CreateVolume:smaller-than-snapshot`, vEndpoint,
		"E6: a filesystem restore/clone with required_bytes below the source's size gets refquota=required, smaller than the snapshot (spec:2229-2230; plan.go PlanCreateVolume keeps size=want)"),

	tr(`^oracle\|CreateVolume:code:InvalidArgument\|block volume needs a capacity`, vEndpoint,
		"E7: a block volume with only limit_bytes is refused as having no capacity, though the range is valid and filesystems take limit as the size (spec:855-858, 1039-1050; plan.go validateCreate)"),

	// ---- intentional (cited) ----
	tr(`^oracle\|ControllerExpandVolume:expand-shrank`, vIntentional,
		"endpoint.go ExpandVolume: an unlimited filesystem stays unlimited and reports the requested size, never 0 (spec:2531-2532 REQUIRED); consecutive expands therefore report whatever was last asked, also smaller"),
	tr(`^diff\|CreateVolume\|precheck\|csi-stricter\|A=InvalidArgument\|B=OK\|unsupported volume_content_source`, vIntentional,
		"controller.go CreateVolume: an empty VolumeContentSource is refused (spec:921-922 one type MUST be set)"),
	tr(`^diff\|CreateVolume\|precheck\|csi-stricter\|A=InvalidArgument\|B=OK\|(access mode MODE is not supported|block and mount access types in one request)`, vIntentional,
		"controller.go modeSupported: no export or attach yet, single-node modes only; kindOf refuses mixed access types"),
	tr(`^diff\|[^|]+\|precheck\|csi-stricter\|A=InvalidArgument\|B=OK\|(unsupported parameter Q|parameter sparse=Q: not a boolean)`, vIntentional,
		"controller.go parseParams/noParams: unknown parameters are refused rather than silently dropped"),
	tr(`^diff\|CreateVolume\|precheck\|csi-stricter\|A=InvalidArgument\|B=OK\|fs_type Q: mount volumes are zfs filesystems`, vIntentional,
		"controller.go kindOf: mount volumes are zfs filesystems"),
	tr(`^diff\|[^|]+\|precheck\|csi-stricter\|A=InvalidArgument\|B=OK\|(volume_capabilities are required|volume capability needs an access type|capacity_range is required|capacity_range must not be negative|capacity_range needs required_bytes or limit_bytes|snapshot_id is required|name is required)`, vIntentional,
		"identity.go package doc: required CSI fields are checked in CSI terms (spec:525, 1040, 1044, 1049); the naive endpoint mapping shows why (negatives wrap to huge uint64 and succeed)"),
	tr(`^diff\|(ListVolumes|ListSnapshots)\|post\|server-vs-direct\|A=InvalidArgument\|B=(OK|Aborted)\|max_entries must be >= N`, vIntentional,
		"server.go maxEntries guards spec:1578; the endpoint's Go API reads a negative max as unlimited (unreachable over gRPC)"),

	// ---- oracle ambiguities ----
	tr(`^diff\|[^|]+\|precheck\|code\|A=InvalidArgument\|B=OutOfRange\|required_bytes N exceeds limit_bytes N`, vAmbiguity,
		"inverted capacity_range: CSI layer says INVALID_ARGUMENT (spec:526), endpoint OUT_OF_RANGE (spec:1298); both fit"),
	tr(`^diff\|[^|]+\|precheck\|code\|A=InvalidArgument\|B=(NotFound|AlreadyExists|OutOfRange)\|`, vAmbiguity,
		"precedence: the CSI layer refuses a malformed request before the endpoint looks at state; the spec defines no order among applicable codes"),
}

func triage(f *finding) bool {
	for _, t := range csiTriage {
		if t.re.MatchString(f.Sig) {
			f.Verdict = t.verdict + ": " + t.note
			return true
		}
	}
	return false
}

type agg struct {
	Sig     string  `json:"sig"`
	Count   int     `json:"count"`
	Verdict string  `json:"verdict"`
	Example finding `json:"example"`
}

func aggregate(fs []finding) []agg {
	m := map[string]*agg{}
	for _, f := range fs {
		a := m[f.Sig]
		if a == nil {
			a = &agg{Sig: f.Sig, Verdict: f.Verdict, Example: f}
			m[f.Sig] = a
		}
		a.Count++
	}
	var out []agg
	for _, a := range m {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sig < out[j].Sig })
	return out
}

// TestCSIDiffSeeds runs seeded sequences on a normal and a long parent and
// fails on any untriaged finding.
func TestCSIDiffSeeds(t *testing.T) {
	seeds, ops := 300, 40
	if testing.Short() {
		seeds = 40
	}
	if v, err := strconv.Atoi(os.Getenv("CSI_DIFF_SEEDS")); err == nil {
		seeds = v
	}
	var all []finding
	for _, parent := range []string{shortParent, longParent} {
		for seed := 0; seed < seeds; seed++ {
			fs, err := runSequence(parent, randChooser{rand.New(rand.NewSource(int64(seed)))}, ops)
			if err != nil {
				t.Fatal(err)
			}
			all = append(all, fs...)
		}
	}
	untriaged := 0
	for i := range all {
		if !triage(&all[i]) {
			untriaged++
		}
	}
	aggs := aggregate(all)
	if p := os.Getenv("CSI_DIFF_REPORT"); p != "" {
		b, _ := json.MarshalIndent(aggs, "", "  ")
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range aggs {
		if a.Verdict == "" {
			t.Errorf("untriaged x%d %s\n    %s\n    op: %s", a.Count, a.Sig, a.Example.Detail, a.Example.Op)
		} else {
			t.Logf("x%-4d %s\n    => %s", a.Count, a.Sig, a.Verdict)
		}
	}
	t.Logf("%d findings, %d signatures, %d untriaged", len(all), len(aggs), untriaged)
}

// FuzzCSIDiff is the coverage-guided form: the input bytes are the choices.
func FuzzCSIDiff(f *testing.F) {
	for i := 0; i < 8; i++ {
		r := rand.New(rand.NewSource(int64(i)))
		b := make([]byte, 96)
		r.Read(b)
		f.Add(false, b)
	}
	f.Fuzz(func(t *testing.T, long bool, data []byte) {
		parent := shortParent
		if long {
			parent = longParent
		}
		fs, err := runSequence(parent, &byteChooser{b: data}, 64)
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range fs {
			if !triage(&x) {
				t.Fatalf("untriaged %s: %s\n op: %s", x.Sig, x.Detail, x.Op)
			}
		}
	})
}
