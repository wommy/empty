package csi_test

// CSI spec oracle (wave9).
//
// Each oracleXxx function is pure: it maps a CSI request and an abstract
// world (what the CO has been told so far) to the set of outcomes the CSI
// spec allows. It knows nothing about ZFS or the endpoint. Citations are
// "spec:N", line N of spec.md at container-storage-interface/spec v1.13.0
// (the module this repo builds against; a copy is wave9/spec-v1.13.md).
//
// The oracle is deliberately as permissive as the spec. Where the spec
// leaves a choice to the plugin, every allowed outcome is in the set and
// the choice is recorded in verdict.notes ("ambiguity"); the differential
// half of the fuzzer, not the oracle, is what makes those choices visible.

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

const (
	maxString = 128  // spec:494
	maxMap    = 4096 // spec:495
	k8sPrefix = "csi.storage.k8s.io/"
)

// ---- abstract world ----------------------------------------------------------

// absVol is a volume the CO was told exists: the request that created it
// and the volume the plugin returned for it.
type absVol struct {
	name string
	req  *csi.CreateVolumeRequest
	vol  *csi.Volume
}

func (v *absVol) id() string { return v.vol.GetVolumeId() }

// block reports whether every capability it was created with is block.
func (v *absVol) block() bool { return allAccess(v.req.GetVolumeCapabilities(), true) }

// mount reports whether every capability it was created with is mount.
func (v *absVol) mount() bool { return allAccess(v.req.GetVolumeCapabilities(), false) }

func allAccess(caps []*csi.VolumeCapability, block bool) bool {
	for _, c := range caps {
		if (c.GetBlock() != nil) != block {
			return false
		}
	}
	return len(caps) > 0
}

type absSnap struct {
	name string
	req  *csi.CreateSnapshotRequest
	snap *csi.Snapshot
}

// world is everything the oracle may use: the outcomes the plugin reported.
type world struct {
	vols  map[string]*absVol  // by CSI name
	snaps map[string]*absSnap // by CSI name
	// history, for ids that are gone
	deadVols, deadSnaps map[string]bool
	snapSource          map[string]string // snapshot id -> source volume id, ever
	tokens              map[string]bool   // next_tokens ever issued
}

func newWorld() *world {
	return &world{
		vols: map[string]*absVol{}, snaps: map[string]*absSnap{},
		deadVols: map[string]bool{}, deadSnaps: map[string]bool{},
		snapSource: map[string]string{}, tokens: map[string]bool{},
	}
}

func (w *world) volByID(id string) *absVol {
	for _, v := range w.vols {
		if v.id() == id {
			return v
		}
	}
	return nil
}

func (w *world) snapByID(id string) *absSnap {
	for _, s := range w.snaps {
		if s.snap.GetSnapshotId() == id {
			return s
		}
	}
	return nil
}

// dependents reports whether a live volume has live snapshots, or a live
// volume was cloned from it or from one of its snapshots (ever).
func (w *world) dependents(id string) bool {
	for _, s := range w.snaps {
		if s.snap.GetSourceVolumeId() == id {
			return true
		}
	}
	for _, v := range w.vols {
		src := v.req.GetVolumeContentSource()
		if src.GetVolume().GetVolumeId() == id {
			return true
		}
		if sid := src.GetSnapshot().GetSnapshotId(); sid != "" && w.snapSource[sid] == id {
			return true
		}
	}
	return false
}

func (w *world) snapHasClones(id string) bool {
	for _, v := range w.vols {
		if v.req.GetVolumeContentSource().GetSnapshot().GetSnapshotId() == id {
			return true
		}
	}
	return false
}

// ---- verdicts ----------------------------------------------------------------

// violation is a response the spec does not allow.
type violation struct {
	rule   string // stable id, used as the triage key
	cite   string
	detail string
}

func (v violation) String() string { return fmt.Sprintf("%s (%s): %s", v.rule, v.cite, v.detail) }

type verdict struct {
	allowed map[codes.Code]string // code -> why (citation)
	tags    []string              // the conditions that applied, for triage keys
	notes   []string              // spec ambiguities the request touched
	check   func(resp any) []violation
	forced  bool // a condition rules OK out
}

func newVerdict() *verdict { return &verdict{allowed: map[codes.Code]string{}} }

func (v *verdict) allow(c codes.Code, cite, tag string) {
	if _, ok := v.allowed[c]; !ok {
		v.allowed[c] = cite
	}
	if tag != "" && !slices.Contains(v.tags, tag) {
		v.tags = append(v.tags, tag)
	}
}

// fail means a condition forces a non-OK outcome: OK leaves the set.
func (v *verdict) fail(c codes.Code, cite, tag string) {
	v.allow(c, cite, tag)
	v.forced = true
}

func (v *verdict) note(s string) { v.notes = append(v.notes, s) }

// finish adds OK unless a condition forced a failure.
func (v *verdict) finish(okCite string) *verdict {
	if !v.forced {
		v.allowed[codes.OK] = okCite
	}
	sort.Strings(v.tags)
	return v
}

func (v *verdict) codes() string {
	var cs []string
	for c := range v.allowed {
		cs = append(cs, c.String())
	}
	sort.Strings(cs)
	return strings.Join(cs, ",")
}

// ---- shared field rules ------------------------------------------------------

// bannedName: spec:849-851 (and spec:2178-2180 for snapshots).
func bannedName(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool {
		return r <= 0x08 || r == 0x0B || r == 0x0C || (r >= 0x0E && r <= 0x1F) || (r >= 0x7F && r <= 0x9F)
	})
}

func mapSize(m map[string]string) int {
	n := 0
	for k, v := range m {
		n += len(k) + len(v)
	}
	return n
}

// capsShape checks the parts of a capability the spec itself requires.
// It returns the forced-failure tags and the plugin-discretion tags.
func capsShape(v *verdict, caps []*csi.VolumeCapability, required bool) {
	if len(caps) == 0 {
		if required {
			// spec:876 REQUIRED, spec:500 a REQUIRED repeated field has >= 1 element
			v.fail(codes.InvalidArgument, "spec:500,525,876", "caps-missing")
		}
		return
	}
	var block, mount bool
	for _, c := range caps {
		if c.GetBlock() == nil && c.GetMount() == nil {
			v.fail(codes.InvalidArgument, "spec:1027-1028,525", "caps-no-access-type")
		}
		if c.GetAccessMode() == nil || c.GetAccessMode().GetMode() == csi.VolumeCapability_AccessMode_UNKNOWN {
			v.fail(codes.InvalidArgument, "spec:1022-1023,1034-1035,525", "caps-no-mode")
		}
		block = block || c.GetBlock() != nil
		mount = mount || c.GetMount() != nil
		switch c.GetAccessMode().GetMode() {
		case csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER, csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY:
		default:
			// which modes a plugin supports is its choice: spec:863-874
			v.allow(codes.InvalidArgument, "spec:526,873-875", "plugin-mode")
		}
		if fs := c.GetMount().GetFsType(); fs != "" {
			v.allow(codes.InvalidArgument, "spec:526", "plugin-fstype")
		}
	}
	if block && mount {
		v.allow(codes.InvalidArgument, "spec:526,863-866", "plugin-mixed-access")
	}
}

// pluginParams: parameters are opaque and validated by the plugin
// (spec:879-882), so any non-k8s key may be refused.
func pluginParams(v *verdict, p map[string]string, tag string) {
	for k := range p {
		if !strings.HasPrefix(k, k8sPrefix) {
			v.allow(codes.InvalidArgument, "spec:526,879-882", tag)
			return
		}
	}
}

// rangeShape applies spec:1039-1050 to a capacity_range.
func rangeShape(v *verdict, r *csi.CapacityRange) {
	if r == nil {
		return
	}
	req, lim := r.GetRequiredBytes(), r.GetLimitBytes()
	if req < 0 || lim < 0 {
		v.fail(codes.InvalidArgument, "spec:1044,1049,526", "range-negative")
		v.allow(codes.OutOfRange, "spec:1298", "")
		return
	}
	if req == 0 && lim == 0 {
		// "at least one of these fields MUST be specified" (spec:1040),
		// but 0 is also "unspecified" (spec:1043): a CO bug a plugin may
		// tolerate as "no range"
		v.allow(codes.InvalidArgument, "spec:1040,526", "range-empty")
		v.note("spec:1040 vs spec:1043: an all-zero capacity_range may be refused or read as unspecified")
	}
	if lim > 0 && req > lim {
		v.fail(codes.InvalidArgument, "spec:1045-1050,526", "range-inverted")
		v.allow(codes.OutOfRange, "spec:1298", "")
		v.note("required > limit: INVALID_ARGUMENT (spec:526) and OUT_OF_RANGE (spec:1298) both fit")
		return
	}
	// what sizes a plugin can make is its choice
	v.allow(codes.OutOfRange, "spec:1298", "")
}

func capacityOK(cap, req, lim int64, block bool) []violation {
	var out []violation
	if cap < 0 {
		out = append(out, violation{"capacity-negative", "spec:1058", fmt.Sprint(cap)})
	}
	if lim > 0 && cap > lim {
		out = append(out, violation{"capacity-over-limit", "spec:1047-1050,823", fmt.Sprintf("capacity %d > limit %d", cap, lim)})
	}
	if req > 0 && cap > 0 && cap < req {
		out = append(out, violation{"capacity-under-required", "spec:1042-1045,858", fmt.Sprintf("capacity %d < required %d", cap, req)})
	}
	if req > 0 && cap == 0 && block {
		out = append(out, violation{"capacity-unknown-block", "spec:823-824", "a block volume must match the range, reported 0 (unknown)"})
	}
	return out
}

// ---- CreateVolume ------------------------------------------------------------

func oracleCreateVolume(w *world, r *csi.CreateVolumeRequest) *verdict {
	v := newVerdict()
	name := r.GetName()
	switch {
	case name == "":
		v.fail(codes.InvalidArgument, "spec:827,525", "name-missing")
	case bannedName(name) || !utf8.ValidString(name):
		v.fail(codes.InvalidArgument, "spec:849-851,526", "name-banned")
	case len(name) > maxString:
		// the CO broke spec:494; a plugin may accept or refuse
		v.allow(codes.InvalidArgument, "spec:494,526", "name-too-long")
	default:
		// "Any Unicode string that conforms to the length limit is
		// allowed" (spec:849): no INVALID_ARGUMENT for the name itself
		v.tags = append(v.tags, "name-legal")
	}
	capsShape(v, r.GetVolumeCapabilities(), true)
	rangeShape(v, r.GetCapacityRange())
	pluginParams(v, r.GetParameters(), "plugin-param")
	if r.GetAccessibilityRequirements() != nil {
		// SHALL NOT be set without VOLUME_ACCESSIBILITY_CONSTRAINTS (spec:900-901)
		v.allow(codes.InvalidArgument, "spec:900-901,526", "co-topology")
	}
	if len(r.GetMutableParameters()) > 0 {
		// SHALL NOT be set without MODIFY_VOLUME (spec:916-917)
		v.allow(codes.InvalidArgument, "spec:916-917,526", "co-mutable")
	}
	if r.GetCapacityRange() == nil || r.GetCapacityRange().GetRequiredBytes() == 0 && r.GetCapacityRange().GetLimitBytes() == 0 {
		// "the Plugin MAY choose an implementation-defined capacity" (spec:856)
		v.allow(codes.InvalidArgument, "spec:856,526", "no-capacity")
		v.note("spec:855-857: with no capacity the plugin MAY choose one; refusing is not forbidden")
	}

	// volume_content_source (spec:921-936, 1294-1295)
	src := r.GetVolumeContentSource()
	var srcSnap *absSnap
	var srcVol *absVol
	if src != nil {
		switch {
		case src.GetSnapshot() != nil:
			id := src.GetSnapshot().GetSnapshotId()
			if id == "" {
				v.fail(codes.InvalidArgument, "spec:925-928,525", "source-snapshot-id-missing")
			} else if srcSnap = w.snapByID(id); srcSnap == nil {
				v.fail(codes.NotFound, "spec:1295", "source-missing")
			}
		case src.GetVolume() != nil:
			id := src.GetVolume().GetVolumeId()
			if id == "" {
				v.fail(codes.InvalidArgument, "spec:932-936,525", "source-volume-id-missing")
			} else if srcVol = w.volByID(id); srcVol == nil {
				v.fail(codes.NotFound, "spec:1295", "source-missing")
			}
		default:
			v.fail(codes.InvalidArgument, "spec:921-922,525", "source-empty")
		}
		// incompatible source/parameters: the plugin decides (spec:1294)
		v.allow(codes.InvalidArgument, "spec:1294", "")
		if srcSnap != nil {
			// a volume from a snapshot is never smaller than it (spec:2229-2230)
			if sz := srcSnap.snap.GetSizeBytes(); sz > 0 && r.GetCapacityRange().GetLimitBytes() > 0 && r.GetCapacityRange().GetLimitBytes() < sz {
				v.fail(codes.OutOfRange, "spec:1298,2229", "source-larger-than-limit")
			}
		}
	}

	// idempotency (spec:807-808, 1296)
	prev := w.vols[name]
	if prev != nil && name != "" && src != nil && proto.Equal(prev.req.GetVolumeContentSource(), src) && (srcSnap == nil && src.GetSnapshot().GetSnapshotId() != "" || srcVol == nil && src.GetVolume().GetVolumeId() != "") {
		// the volume exists from this very source, which is gone since:
		// spec:808 (compatible -> MUST OK) against spec:1295 (NOT_FOUND)
		v.forced = false
		v.allow(codes.NotFound, "spec:1295", "source-gone-volume-exists")
		v.note("spec:808 vs 1295: a retry for an existing volume whose source was deleted since")
	}
	if prev != nil && name != "" {
		v.tags = append(v.tags, "name-exists")
		same := proto.Equal(stripSecrets(prev.req), stripSecrets(r))
		if !same {
			inc := createIncompatible(prev, r)
			switch inc {
			case "":
				v.allow(codes.AlreadyExists, "spec:808,1296", "compat-plugin-defined")
				v.note("spec:808: compatibility of parameters/capabilities is plugin-defined")
			default:
				v.fail(codes.AlreadyExists, "spec:1296", "incompatible-"+inc)
			}
		}
	}
	// no space is always a possible failure; topology is not in play
	v.allow(codes.ResourceExhausted, "spec:1297", "")
	v.finish("spec:807-808")

	v.check = func(resp any) []violation {
		res := resp.(*csi.CreateVolumeResponse)
		vol := res.GetVolume()
		var out []violation
		if vol == nil {
			return []violation{{"volume-missing", "spec:946-948", "CreateVolumeResponse.volume is REQUIRED"}}
		}
		if vol.GetVolumeId() == "" || len(vol.GetVolumeId()) > maxString {
			out = append(out, violation{"volume-id-shape", "spec:1061-1066,494", fmt.Sprintf("%d bytes", len(vol.GetVolumeId()))})
		}
		if n := mapSize(vol.GetVolumeContext()); n > maxMap {
			out = append(out, violation{"context-too-big", "spec:495", fmt.Sprint(n)})
		}
		if len(vol.GetAccessibleTopology()) > 0 {
			out = append(out, violation{"topology-without-capability", "spec:1095-1096", "accessible_topology set"})
		}
		block := allAccess(r.GetVolumeCapabilities(), true)
		out = append(out, capacityOK(vol.GetCapacityBytes(), r.GetCapacityRange().GetRequiredBytes(), r.GetCapacityRange().GetLimitBytes(), block)...)
		if !proto.Equal(vol.GetContentSource(), r.GetVolumeContentSource()) {
			// spec:1088-1091; external-provisioner refuses a response
			// without the requested source
			out = append(out, violation{"content-source-not-echoed", "spec:1088-1091", fmt.Sprintf("got %v want %v", vol.GetContentSource(), r.GetVolumeContentSource())})
		}
		if srcSnap != nil {
			if sz := srcSnap.snap.GetSizeBytes(); sz > 0 && vol.GetCapacityBytes() > 0 && vol.GetCapacityBytes() < sz {
				out = append(out, violation{"smaller-than-snapshot", "spec:2229-2230", fmt.Sprintf("%d < %d", vol.GetCapacityBytes(), sz)})
			}
		}
		if prev != nil && name != "" {
			if vol.GetVolumeId() != prev.id() {
				out = append(out, violation{"idempotent-id", "spec:807-808,829-833", fmt.Sprintf("%q then %q", prev.id(), vol.GetVolumeId())})
			}
			if !maps(vol.GetVolumeContext(), prev.vol.GetVolumeContext()) {
				out = append(out, violation{"idempotent-context", "spec:1082-1083", fmt.Sprintf("%v then %v", prev.vol.GetVolumeContext(), vol.GetVolumeContext())})
			}
		}
		for n, o := range w.vols {
			if n != name && o.id() == vol.GetVolumeId() {
				out = append(out, violation{"id-collision", "spec:1063-1064", fmt.Sprintf("%q and %q share id %q", n, name, o.id())})
			}
		}
		if w.deadVols[vol.GetVolumeId()] && prev == nil {
			// allowed (ids may be reused) but worth seeing
			_ = 0
		}
		return out
	}
	return v
}

func stripSecrets(r *csi.CreateVolumeRequest) *csi.CreateVolumeRequest {
	c := proto.Clone(r).(*csi.CreateVolumeRequest)
	c.Secrets = nil
	return c
}

func maps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// createIncompatible names what makes r incompatible with prev by the spec
// alone, or "" when compatibility is the plugin's call.
func createIncompatible(prev *absVol, r *csi.CreateVolumeRequest) string {
	if !proto.Equal(prev.req.GetVolumeContentSource(), r.GetVolumeContentSource()) {
		return "source" // spec:1296 lists volume_content_source
	}
	have := prev.vol.GetCapacityBytes()
	req, lim := r.GetCapacityRange().GetRequiredBytes(), r.GetCapacityRange().GetLimitBytes()
	if req >= 0 && lim >= 0 {
		if req > 0 && have > 0 && have < req {
			return "capacity"
		}
		if lim > 0 && have > lim {
			return "capacity"
		}
	}
	// a filesystem volume created for mount cannot honour a block capability
	// by the plugin's own account (ValidateVolumeCapabilities); the reverse
	// (mount on a zvol) is the plugin's call
	if prev.mount() && slices.ContainsFunc(r.GetVolumeCapabilities(), func(c *csi.VolumeCapability) bool { return c.GetBlock() != nil }) {
		return "capabilities"
	}
	return ""
}

// ---- DeleteVolume ------------------------------------------------------------

func oracleDeleteVolume(w *world, r *csi.DeleteVolumeRequest) *verdict {
	v := newVerdict()
	id := r.GetVolumeId()
	if id == "" {
		v.fail(codes.InvalidArgument, "spec:1317-1318,525", "id-missing")
		return v.finish("")
	}
	vol := w.volByID(id)
	if vol == nil {
		// spec:1307: missing (never issued, already deleted) MUST be OK
		v.tags = append(v.tags, "missing")
		return v.finish("spec:1307")
	}
	if w.dependents(id) {
		v.allow(codes.FailedPrecondition, "spec:1309-1313,1340", "has-dependents")
		v.note("spec:1309-1313: snapshots/clones may be independent (OK) or block the delete (FAILED_PRECONDITION)")
	}
	return v.finish("spec:1303-1307")
}

// ---- ValidateVolumeCapabilities ---------------------------------------------

func oracleValidate(w *world, r *csi.ValidateVolumeCapabilitiesRequest) *verdict {
	v := newVerdict()
	if r.GetVolumeId() == "" {
		v.fail(codes.InvalidArgument, "spec:1487,525", "id-missing")
	}
	if len(r.GetVolumeCapabilities()) == 0 {
		v.fail(codes.InvalidArgument, "spec:1496-1498,525", "caps-missing")
	}
	vol := w.volByID(r.GetVolumeId())
	if r.GetVolumeId() != "" && vol == nil {
		v.fail(codes.NotFound, "spec:1559", "missing")
	}
	// malformed capabilities may be refused outright or answered with a
	// message: spec:526 vs spec:1544
	for _, c := range r.GetVolumeCapabilities() {
		if (c.GetBlock() == nil && c.GetMount() == nil) || c.GetAccessMode().GetMode() == csi.VolumeCapability_AccessMode_UNKNOWN {
			v.allow(codes.InvalidArgument, "spec:526,1027-1035", "caps-malformed")
		}
	}
	v.finish("spec:1477-1480")
	v.check = func(resp any) []violation {
		res := resp.(*csi.ValidateVolumeCapabilitiesResponse)
		var out []violation
		if c := res.GetConfirmed(); c != nil {
			if res.GetMessage() != "" {
				v.note("message set with confirmed")
			}
			if len(c.GetVolumeCapabilities()) == 0 {
				out = append(out, violation{"confirmed-no-caps", "spec:1522-1523", "confirmed.volume_capabilities is REQUIRED"})
			}
			for _, cc := range r.GetVolumeCapabilities() {
				if !slices.ContainsFunc(c.GetVolumeCapabilities(), func(x *csi.VolumeCapability) bool { return proto.Equal(x, cc) }) {
					out = append(out, violation{"confirmed-subset", "spec:1479,1536-1539", "a requested capability is not in confirmed"})
				}
			}
			if r.GetVolumeContext() != nil && vol != nil && !maps(r.GetVolumeContext(), vol.vol.GetVolumeContext()) {
				// the CO broke spec:1492; confirming a context that does
				// not belong to the volume is legal but questionable
				v.note("spec:1492: confirmed a volume_context that is not the volume's")
			}
		} else if vol != nil && capsSubset(r.GetVolumeCapabilities(), vol.req.GetVolumeCapabilities()) &&
			(len(r.GetParameters()) == 0 || maps(r.GetParameters(), vol.req.GetParameters())) {
			// other parameters are the plugin's to judge (spec:879-882)
			// the volume was provisioned to satisfy ALL these (spec:863-866)
			out = append(out, violation{"unconfirmed-own-caps", "spec:863-866,1479", "refused capabilities the volume was created with: " + res.GetMessage()})
		}
		return out
	}
	return v
}

func capsSubset(a, b []*csi.VolumeCapability) bool {
	for _, x := range a {
		if !slices.ContainsFunc(b, func(y *csi.VolumeCapability) bool { return proto.Equal(x, y) }) {
			return false
		}
	}
	return len(a) > 0
}

// ---- ListVolumes / ListSnapshots (one page) ----------------------------------

func oracleListPage(w *world, maxEntries int32, token string) *verdict {
	v := newVerdict()
	if maxEntries < 0 {
		v.fail(codes.InvalidArgument, "spec:1578,526", "max-negative")
	}
	if token != "" && !w.tokens[token] {
		v.fail(codes.Aborted, "spec:1636,2418", "token-unknown")
	} else if token != "" {
		// a token that was valid may name an item since deleted
		v.allow(codes.Aborted, "spec:1566-1567,1636", "token-stale")
		v.note("spec:1566-1567 vs 1636: a stale token may restart (ABORTED) or continue")
	}
	v.finish("spec:1565")
	v.check = func(resp any) []violation {
		var n int
		var next string
		switch r := resp.(type) {
		case *csi.ListVolumesResponse:
			n, next = len(r.GetEntries()), r.GetNextToken()
			var out []violation
			for _, e := range r.GetEntries() {
				vol := e.GetVolume()
				if len(vol.GetAccessibleTopology()) > 0 {
					out = append(out, violation{"topology-without-capability", "spec:1095-1096", vol.GetVolumeId()})
				}
				if a := w.volByID(vol.GetVolumeId()); a != nil && !maps(a.vol.GetVolumeContext(), vol.GetVolumeContext()) {
					out = append(out, violation{"list-context-differs", "spec:1082-1083", fmt.Sprintf("%s: create %v, list %v", vol.GetVolumeId(), a.vol.GetVolumeContext(), vol.GetVolumeContext())})
				}
				if a := w.volByID(vol.GetVolumeId()); a != nil && !proto.Equal(a.vol.GetContentSource(), vol.GetContentSource()) {
					out = append(out, violation{"list-source-differs", "spec:1088-1091", vol.GetVolumeId()})
				}
			}
			return append(out, pageShape(maxEntries, n, next)...)
		case *csi.ListSnapshotsResponse:
			n, next = len(r.GetEntries()), r.GetNextToken()
			var out []violation
			for _, e := range r.GetEntries() {
				s := e.GetSnapshot()
				if !s.GetReadyToUse() {
					v.note("spec:2156: listed snapshot not ready_to_use")
				}
				if a := w.snapByID(s.GetSnapshotId()); a != nil && a.snap.GetSourceVolumeId() != s.GetSourceVolumeId() {
					out = append(out, violation{"list-snapshot-source-differs", "spec:2238-2240", s.GetSnapshotId()})
				}
			}
			return append(out, pageShape(maxEntries, n, next)...)
		}
		return nil
	}
	return v
}

func pageShape(maxEntries int32, n int, next string) []violation {
	var out []violation
	if maxEntries > 0 && n > int(maxEntries) {
		out = append(out, violation{"page-too-long", "spec:1571-1572", fmt.Sprintf("%d > %d", n, maxEntries)})
	}
	if maxEntries > 0 && n < int(maxEntries) && next != "" {
		// not forbidden, but a short page with a token makes the CO spin
		_ = 0
	}
	return out
}

// walkInvariant checks a complete paginated walk: every live volume the CO
// was told about is listed (spec:1564) and no deleted one is (spec:1307
// says it is gone; listing it would let a CO resurrect it).
func walkInvariant(w *world, listed []string, live []string, dead map[string]bool, what string) []violation {
	var out []violation
	set := map[string]int{}
	for _, id := range listed {
		set[id]++
	}
	for _, id := range live {
		if set[id] == 0 {
			out = append(out, violation{what + "-walk-missing", "spec:1565,2351", id})
		}
	}
	for id := range set {
		if dead[id] && !slices.Contains(live, id) {
			out = append(out, violation{what + "-walk-lists-deleted", "spec:1307,2313", id})
		}
		if set[id] > 1 {
			// allowed only under concurrent change (spec:1566); the walk is sequential
			out = append(out, violation{what + "-walk-duplicate", "spec:1566-1567", id})
		}
	}
	return out
}

// ---- GetCapacity -------------------------------------------------------------

func oracleGetCapacity(_ *world, r *csi.GetCapacityRequest) *verdict {
	v := newVerdict()
	if len(r.GetVolumeCapabilities()) > 0 || len(r.GetParameters()) > 0 {
		v.note("spec:1951-1962,1974-1977: capabilities/parameters SHALL be taken into account; a plugin that cannot serve them may report 0 or refuse")
		v.allow(codes.InvalidArgument, "spec:526", "plugin-filter")
	}
	v.finish("spec:1946-1947")
	v.check = func(resp any) []violation {
		res := resp.(*csi.GetCapacityResponse)
		if res.GetAvailableCapacity() < 0 {
			return []violation{{"capacity-negative", "spec:1979", fmt.Sprint(res.GetAvailableCapacity())}}
		}
		return nil
	}
	return v
}

// ---- CreateSnapshot ----------------------------------------------------------

func oracleCreateSnapshot(w *world, r *csi.CreateSnapshotRequest) *verdict {
	v := newVerdict()
	name := r.GetName()
	switch {
	case name == "":
		v.fail(codes.InvalidArgument, "spec:2176-2177,525", "name-missing")
	case bannedName(name) || !utf8.ValidString(name):
		v.fail(codes.InvalidArgument, "spec:2178-2180,526", "name-banned")
	case len(name) > maxString:
		v.allow(codes.InvalidArgument, "spec:494,526", "name-too-long")
	default:
		v.tags = append(v.tags, "name-legal")
	}
	if r.GetSourceVolumeId() == "" {
		v.fail(codes.InvalidArgument, "spec:2172-2173,525", "source-missing-field")
	}
	pluginParams(v, r.GetParameters(), "plugin-param")
	src := w.volByID(r.GetSourceVolumeId())
	if r.GetSourceVolumeId() != "" && src == nil {
		// no row in spec:2300-2304; any non-OK code. NOT_FOUND is the
		// one csi-test and the snapshotter expect.
		v.fail(codes.NotFound, "spec:2296-2297", "source-not-found")
		v.allow(codes.InvalidArgument, "spec:2296-2297", "")
		v.allow(codes.FailedPrecondition, "spec:2296-2297", "")
		v.note("CreateSnapshot on a missing source: the spec has no table row (spec:2300-2304)")
	}
	prev := w.snaps[name]
	if prev != nil && name != "" {
		v.tags = append(v.tags, "name-exists")
		if prev.snap.GetSourceVolumeId() != r.GetSourceVolumeId() {
			v.fail(codes.AlreadyExists, "spec:2302", "incompatible-source")
		}
	}
	v.allow(codes.ResourceExhausted, "spec:2304", "")
	v.finish("spec:2133-2134")
	v.check = func(resp any) []violation {
		s := resp.(*csi.CreateSnapshotResponse).GetSnapshot()
		var out []violation
		if s == nil {
			return []violation{{"snapshot-missing", "spec:2218-2222", ""}}
		}
		if s.GetSnapshotId() == "" || len(s.GetSnapshotId()) > maxString {
			out = append(out, violation{"snapshot-id-shape", "spec:2236-2240,494", fmt.Sprintf("%d bytes", len(s.GetSnapshotId()))})
		}
		if s.GetSourceVolumeId() != r.GetSourceVolumeId() {
			out = append(out, violation{"snapshot-source", "spec:2247-2250", fmt.Sprintf("%q != %q", s.GetSourceVolumeId(), r.GetSourceVolumeId())})
		}
		if s.GetCreationTime() == nil {
			out = append(out, violation{"snapshot-no-time", "spec:2252-2254", ""})
		}
		if s.GetSizeBytes() < 0 {
			out = append(out, violation{"snapshot-size-negative", "spec:2232", ""})
		}
		if !s.GetReadyToUse() {
			v.note("spec:2156: ready_to_use SHOULD be true for an SP without post-processing")
		}
		if prev != nil && name != "" && prev.snap.GetSnapshotId() != s.GetSnapshotId() {
			out = append(out, violation{"idempotent-snapshot-id", "spec:2133-2134", fmt.Sprintf("%q then %q", prev.snap.GetSnapshotId(), s.GetSnapshotId())})
		}
		return out
	}
	return v
}

// ---- DeleteSnapshot ----------------------------------------------------------

func oracleDeleteSnapshot(w *world, r *csi.DeleteSnapshotRequest) *verdict {
	v := newVerdict()
	id := r.GetSnapshotId()
	if id == "" {
		v.fail(codes.InvalidArgument, "spec:2322-2323,525", "id-missing")
		return v.finish("")
	}
	if w.snapByID(id) == nil {
		v.tags = append(v.tags, "missing")
		return v.finish("spec:2313")
	}
	if w.snapHasClones(id) {
		v.allow(codes.FailedPrecondition, "spec:2344", "has-clones")
	}
	return v.finish("spec:2312-2313")
}

// ---- ListSnapshots filters ---------------------------------------------------

func snapshotFilterInvariant(w *world, r *csi.ListSnapshotsRequest, got []*csi.Snapshot) []violation {
	var out []violation
	for _, s := range got {
		if r.GetSnapshotId() != "" && s.GetSnapshotId() != r.GetSnapshotId() {
			out = append(out, violation{"snapshot-filter-id", "spec:2380-2385", s.GetSnapshotId()})
		}
		if r.GetSourceVolumeId() != "" && s.GetSourceVolumeId() != r.GetSourceVolumeId() {
			out = append(out, violation{"snapshot-filter-source", "spec:2376-2378", s.GetSnapshotId()})
		}
	}
	if id := r.GetSnapshotId(); id != "" && w.snapByID(id) != nil && r.GetStartingToken() == "" && !slices.ContainsFunc(got, func(s *csi.Snapshot) bool { return s.GetSnapshotId() == id }) {
		out = append(out, violation{"snapshot-filter-id-missing", "spec:2351,2378-2382", id})
	}
	return out
}

// ---- ControllerExpandVolume --------------------------------------------------

func oracleExpand(w *world, r *csi.ControllerExpandVolumeRequest) *verdict {
	v := newVerdict()
	if r.GetVolumeId() == "" {
		v.fail(codes.InvalidArgument, "spec:2509,525", "id-missing")
	}
	if r.GetCapacityRange() == nil {
		v.fail(codes.InvalidArgument, "spec:2512-2513,525", "range-missing")
	}
	rangeShape(v, r.GetCapacityRange())
	vol := w.volByID(r.GetVolumeId())
	if r.GetVolumeId() != "" && vol == nil {
		v.fail(codes.NotFound, "spec:2546", "missing")
	}
	if c := r.GetVolumeCapability(); c != nil {
		capsShape(v, []*csi.VolumeCapability{c}, false)
		if vol != nil && vol.mount() && c.GetBlock() != nil {
			// the plugin's own ValidateVolumeCapabilities refuses block
			// access to a volume created for mount
			v.fail(codes.InvalidArgument, "spec:2545", "exceeds-capabilities")
		}
	}
	if vol != nil {
		have := vol.vol.GetCapacityBytes()
		if lim := r.GetCapacityRange().GetLimitBytes(); lim > 0 && have > lim {
			v.allow(codes.OutOfRange, "spec:2548", "already-over-limit")
			v.note("spec:2472 vs 2548: already larger than a limit")
		}
	}
	v.finish("spec:2471-2472")
	v.check = func(resp any) []violation {
		res := resp.(*csi.ControllerExpandVolumeResponse)
		var out []violation
		req, lim := r.GetCapacityRange().GetRequiredBytes(), r.GetCapacityRange().GetLimitBytes()
		c := res.GetCapacityBytes()
		if c <= 0 {
			out = append(out, violation{"expand-capacity-unset", "spec:2531-2532", fmt.Sprint(c)})
		}
		if req > 0 && c > 0 && c < req {
			out = append(out, violation{"expand-under-required", "spec:2471-2472,2532", fmt.Sprintf("%d < %d", c, req)})
		}
		if lim > 0 && c > lim && !(vol != nil && vol.vol.GetCapacityBytes() > lim) {
			out = append(out, violation{"expand-over-limit", "spec:1047-1050", fmt.Sprintf("%d > %d", c, lim)})
		}
		if vol != nil && vol.vol.GetCapacityBytes() > 0 && c < vol.vol.GetCapacityBytes() {
			out = append(out, violation{"expand-shrank", "spec:2471-2472", fmt.Sprintf("%d < %d", c, vol.vol.GetCapacityBytes())})
		}
		return out
	}
	return v
}

// ---- verdict application ------------------------------------------------------

// judge checks one outcome against a verdict.
func judge(rpc string, v *verdict, code codes.Code, resp any) []violation {
	if _, ok := v.allowed[code]; !ok {
		return []violation{{
			rule:   fmt.Sprintf("%s:code:%s[%s]", rpc, code, strings.Join(v.tags, "+")),
			cite:   firstCite(v),
			detail: "allowed " + v.codes(),
		}}
	}
	if code == codes.OK && v.check != nil {
		out := v.check(resp)
		for i := range out {
			out[i].rule = rpc + ":" + out[i].rule
		}
		return out
	}
	return nil
}

func firstCite(v *verdict) string {
	var cs []string
	for _, c := range v.allowed {
		if c != "" {
			cs = append(cs, c)
		}
	}
	sort.Strings(cs)
	return strings.Join(cs, "; ")
}
