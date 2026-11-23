package endpoint_test

// The reference model: a pure state machine over abstract datasets under
// one parent. It is the oracle of the stateful property test
// (modelrun_test.go): for any sequence of RPCs and out-of-band actions it
// says what each RPC returns and what the world looks like afterwards.
//
// It knows nothing of plans, ops, tables or zfs argv. It knows OpenZFS's
// observable semantics (snapshots, clones and their origins, deferred
// destroy, holds, rename) and the endpoint's contract (ADR 0003 ownership,
// CSI idempotency and codes). Where HEAD is known to diverge from the
// contract, the generator masks the input (knownOpen in modelrun_test.go);
// the model states the contract.
//
// Every branch that encodes a decision cites its spec row
// (the wave8 spec catalogue). Rows whose oracle is "openzfs" are beliefs
// about zfs, shared with the simulator: they are checked against a real
// pool by realfacts_test.go, not by this model agreeing with the Sim.

import (
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/wommy/empty/internal/endpoint"
)

const internalPrefix = "volume-source-for-volume-"

// mDS is a filesystem or zvol under the parent (or one level deeper: a
// foreign child). Markers are the endpoint's user properties as set
// locally; received or inherited values are not ownership and are not
// modelled at all.
type mDS struct {
	Kind     endpoint.VolumeKind
	Size     uint64 // fs: refquota (0 = none); zvol: volsize
	Reserved bool   // refreservation > 0
	Origin   string // relative snapshot id, "" if not a clone

	Managed, Committed      bool
	VolName, SrcType, SrcID string
}

// mSnap is a snapshot.
type mSnap struct {
	Managed           bool
	SnapName, SnapSrc string
	CloneFor          string // clone_source_for stamp (OWN-13)
	Deferred          bool
	Holds             int
}

// Model is the abstract world. Names are relative to the parent.
type Model struct {
	DS    map[string]*mDS
	Snaps map[string]*mSnap
}

func newModel() *Model { return &Model{DS: map[string]*mDS{}, Snaps: map[string]*mSnap{}} }

func (m *Model) clone() *Model {
	c := newModel()
	for k, v := range m.DS {
		d := *v
		c.DS[k] = &d
	}
	for k, v := range m.Snaps {
		s := *v
		c.Snaps[k] = &s
	}
	return c
}

// ---- ownership (ADR 0003) ---------------------------------------------------

func (m *Model) ownedVol(id string) bool {
	d, ok := m.DS[id]
	return ok && !strings.Contains(id, "/") && d.Managed && d.VolName == id // OWN-02
}

func (m *Model) ownedSnap(id string) bool {
	s, ok := m.Snaps[id]
	vol, _, _ := strings.Cut(id, "@")
	return ok && s.Managed && s.SnapName != "" && (s.SnapSrc == "" || s.SnapSrc == vol)
}

func isInternal(id string) bool {
	_, n, _ := strings.Cut(id, "@")
	return strings.HasPrefix(n, internalPrefix)
}

// ownedCloneSource: the reserved name AND the stamp cut with it; the name
// alone is someone else's snapshot (OWN-13).
func (m *Model) ownedCloneSource(id string) (string, bool) {
	s, ok := m.Snaps[id]
	_, n, _ := strings.Cut(id, "@")
	clone, named := strings.CutPrefix(n, internalPrefix)
	return clone, ok && named && clone != "" && s.Managed && s.SnapName == "" && s.CloneFor == clone
}

func (m *Model) served(id string) bool { return m.ownedVol(id) && m.DS[id].Committed } // VOL-09

func (m *Model) snapsOf(vol string) []string {
	var out []string
	for k := range m.Snaps {
		if strings.HasPrefix(k, vol+"@") {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func (m *Model) hasClones(snap string) bool {
	for _, d := range m.DS {
		if d.Origin == snap {
			return true
		}
	}
	return false
}

func (m *Model) children(vol string) []string {
	var out []string
	for k := range m.DS {
		if strings.HasPrefix(k, vol+"/") {
			out = append(out, k)
		}
	}
	return out
}

// ---- zfs semantics ----------------------------------------------------------

// reap destroys a snapshot whose deferred destroy is due.
func (m *Model) reap(snap string) {
	if s, ok := m.Snaps[snap]; ok && s.Deferred && s.Holds == 0 && !m.hasClones(snap) {
		delete(m.Snaps, snap)
	}
}

// destroyDeferred is `zfs destroy -d snap`.
func (m *Model) destroyDeferred(snap string) {
	s, ok := m.Snaps[snap]
	if !ok {
		return
	}
	s.Deferred = true
	m.reap(snap)
}

// destroyTree is `zfs destroy -r vol` once it is known to succeed.
func (m *Model) destroyTree(vol string) {
	var origins []string
	for _, k := range append(m.children(vol), vol) {
		origins = append(origins, m.DS[k].Origin)
		delete(m.DS, k)
		for _, s := range m.snapsOf(k) {
			delete(m.Snaps, s)
		}
	}
	for _, o := range origins {
		if o != "" {
			m.reap(o)
		}
	}
}

func roundUp(n, bs uint64) uint64 {
	if r := n % bs; r != 0 {
		n += bs - r
	}
	return n
}

// ---- RPCs -------------------------------------------------------------------

type mResult struct {
	Code endpoint.Code
	Vol  endpoint.Volume   // CreateVolume: ID Kind CapacityBytes SourceType SourceID
	Snap endpoint.Snapshot // CreateSnapshot: ID SourceVolumeID SizeBytes ReadyToUse
	Acts []string          // Reconcile: sorted "target|reason"
	// Reconcile: every skip target the dry run and the run reported.
	// Not compared with the model (its reasons are the endpoint's
	// diagnostics); checkSkips holds them to what the endpoint owns.
	Skips []string
}

func (m *Model) volView(id string) endpoint.Volume {
	d := m.DS[id]
	return endpoint.Volume{ID: id, Kind: d.Kind, CapacityBytes: d.Size, SourceType: d.SrcType, SourceID: d.SrcID}
}

func validID(id string) bool {
	return id != "" && !strings.ContainsAny(id, "/@ ") && len(id) <= 128
}

func (m *Model) CreateVolume(r endpoint.CreateVolumeRequest) mResult {
	fail := func(c endpoint.Code) mResult { return mResult{Code: c} }
	id := r.Name
	if !validID(id) || r.SourceSnapshotID != "" && r.SourceVolumeID != "" {
		return fail(endpoint.InvalidArgument)
	}
	switch {
	case r.Kind == endpoint.Block && r.CapacityBytes == 0 && r.SourceSnapshotID == "" && r.SourceVolumeID == "",
		r.Kind == endpoint.Filesystem && r.Reserve && r.CapacityBytes == 0 && r.SourceSnapshotID == "" && r.SourceVolumeID == "",
		r.Kind == endpoint.Block && r.Reserve, r.Kind == endpoint.Filesystem && r.Sparse:
		return fail(endpoint.InvalidArgument)
	}
	want := r.CapacityBytes
	if r.Kind == endpoint.Block {
		want = roundUp(want, 16384)
	}
	srcType, srcID := "", ""
	if r.SourceSnapshotID != "" {
		srcType, srcID = "snapshot", r.SourceSnapshotID
	} else if r.SourceVolumeID != "" {
		srcType, srcID = "volume", r.SourceVolumeID
	}
	reserved := r.Reserve || r.Kind == endpoint.Block && !r.Sparse

	if d, ok := m.DS[id]; ok {
		unlimited := d.Kind == endpoint.Filesystem && d.Size == 0
		switch {
		case !d.Managed, d.VolName != "" && d.VolName != id, // OWN-05, OWN-02
			d.Kind != r.Kind || d.SrcType != srcType || d.SrcID != srcID, // VOL-05
			want > 0 && !unlimited && d.Size < want,                      // VOL-06 / D07: unlimited satisfies required
			want > 0 && d.Kind == endpoint.Block && d.Size == 0,
			d.Reserved != reserved: // VOL-05, VOL-12
			return fail(endpoint.AlreadyExists)
		}
		return mResult{Vol: m.volView(id)}
	}

	nd := &mDS{Kind: r.Kind, Size: want, Reserved: reserved, Managed: true, Committed: true,
		VolName: id, SrcType: srcType, SrcID: srcID}
	var internal string
	if srcID != "" {
		var srcVol string
		var srcSize uint64
		if srcType == "snapshot" {
			vol, name, ok := strings.Cut(srcID, "@")
			if !ok || !validID(vol) || name == "" || strings.Contains(name, "@") {
				return fail(endpoint.NotFound)
			}
			s, ok := m.Snaps[srcID]
			if !ok || !m.ownedSnap(srcID) || s.Deferred || !m.served(vol) { // OWN-07, CLN-07
				return fail(endpoint.NotFound)
			}
			srcVol, nd.Origin = vol, srcID
		} else {
			if !validID(srcID) || !m.served(srcID) { // OWN-07, CLN-08
				return fail(endpoint.NotFound)
			}
			srcVol = srcID
			internal = srcID + "@" + internalPrefix + id
			nd.Origin = internal
		}
		src := m.DS[srcVol]
		if src.Kind != r.Kind {
			return fail(endpoint.InvalidArgument)
		}
		srcSize = src.Size
		// a clone is never smaller than its source (E6); an unlimited
		// filesystem source (size 0) leaves the request as is
		nd.Size = max(nd.Size, srcSize)
		if r.Kind == endpoint.Filesystem && r.Reserve && nd.Size == 0 {
			return fail(endpoint.InvalidArgument)
		}
	}
	if internal != "" {
		if _, ok := m.Snaps[internal]; ok {
			// a snapshot holding the reserved name is a replay of our own
			// cut only with our stamp for this clone (OWN-13, A1)
			if c, ours := m.ownedCloneSource(internal); !ours || c != id {
				return fail(endpoint.AlreadyExists)
			}
		} else {
			m.Snaps[internal] = &mSnap{Managed: true, CloneFor: id} // stamped atomically (CR-04)
		}
	}
	m.DS[id] = nd
	return mResult{Vol: m.volView(id)}
}

// refuses is what DeleteVolume judges from the scan, before any op: the
// refusals that leave every dataset unchanged. A dataset below the volume
// (OWN-12) and a user hold (REC-11, HOLD-REPLAN) are refused here, not left
// to zfs, so a reconcile never re-plans what zfs refuses every time.
func (m *Model) refuses(id string, destroyForeign bool) endpoint.Code {
	if len(m.children(id)) > 0 {
		return endpoint.FailedPrecondition
	}
	for _, s := range m.snapsOf(id) {
		sn := m.Snaps[s]
		_, ours := m.ownedCloneSource(s)
		switch {
		case sn.SnapName != "" && !sn.Deferred: // DEL-02: a CSI snapshot outlives the call
			return endpoint.FailedPrecondition
		case m.hasClones(s): // DEL-03
			return endpoint.FailedPrecondition
		case sn.Holds > 0: // REC-11
			return endpoint.FailedPrecondition
		case !ours && !sn.Deferred && !destroyForeign: // OWN-10, OWN-11, OWN-13
			return endpoint.FailedPrecondition
		}
	}
	return endpoint.OK
}

// releasable is the origin a delete of id releases (OWN-14): exactly
// <src>@volume-source-for-volume-<id> on an owned src, stamped for id or a
// bare legacy one, not yet marked.
func (m *Model) releasable(id string) string {
	o := m.DS[id].Origin
	src, name, ok := strings.Cut(o, "@")
	sn, exists := m.Snaps[o]
	if !ok || name != internalPrefix+id || !m.ownedVol(src) || !exists || sn.Deferred {
		return ""
	}
	legacy := !sn.Managed && sn.SnapName == "" && sn.CloneFor == ""
	if c, ours := m.ownedCloneSource(o); !(ours && c == id) && !legacy {
		return ""
	}
	return o
}

// deletePlan is the order the endpoint plans a judged delete in (DEL-06):
// release the origin (-d), destroy each snapshot plainly, then the volume
// plainly. It is part of the contract because it decides what a crash or
// an out-of-band writer can observe half-way.
func (m *Model) deletePlan(id string) []string {
	var plan []string
	if o := m.releasable(id); o != "" {
		plan = append(plan, o)
	}
	plan = append(plan, m.snapsOf(id)...)
	return append(plan, id)
}

// deleteOps runs a delete plan with zfs semantics; race, when set, runs
// just before op k (an out-of-band writer in the scan-to-op window). zfs
// stops it at a held or cloned snapshot (busy, dependent clones) or a
// volume that still has snapshots or children ("has children", OWN-12,
// OWN-15); what ran before stays, and the code is FailedPrecondition
// (DEL-07). done lists what was destroyed or marked, as reconcile reports.
func (m *Model) deleteOps(id string, k int, race func(*Model)) (done []string, code endpoint.Code) {
	plan := m.deletePlan(id)
	origin := m.DS[id].Origin
	for i, t := range plan {
		if i == k && race != nil {
			race(m)
		}
		switch {
		case i == 0 && t == origin && t != id: // the release, -d, OkMissing
			if _, ok := m.Snaps[t]; ok {
				m.destroyDeferred(t)
			}
			done = append(done, t+"|"+endpoint.ReasonReleasedWithVolume)
		case strings.Contains(t, "@"): // plain, OkMissing
			sn, ok := m.Snaps[t]
			if !ok {
				continue
			}
			if sn.Holds > 0 || m.hasClones(t) {
				return done, endpoint.FailedPrecondition
			}
			delete(m.Snaps, t)
			done = append(done, t+"|"+endpoint.ReasonWithVolume)
		default: // the volume, plain, OkMissing
			if _, ok := m.DS[t]; !ok {
				continue
			}
			if len(m.snapsOf(t)) > 0 || len(m.children(t)) > 0 {
				return done, endpoint.FailedPrecondition
			}
			delete(m.DS, t)
			if origin != "" {
				m.reap(origin) // DEL-05
			}
			done = append(done, t+"|"+endpoint.ReasonUndesired)
		}
	}
	return done, endpoint.OK
}

func (m *Model) DeleteVolume(id string) mResult {
	if id == "" {
		return mResult{Code: endpoint.InvalidArgument}
	}
	if !validID(id) {
		return mResult{} // ID-01: never issued, so CSI says OK
	}
	if _, ok := m.DS[id]; !ok {
		m.releaseCloneSourcesFor(id)
		return mResult{} // DEL-01
	}
	if !m.ownedVol(id) {
		return mResult{Code: endpoint.FailedPrecondition} // OWN-05
	}
	if c := m.refuses(id, false); c != endpoint.OK {
		return mResult{Code: c}
	}
	m.releaseCloneSourcesFor(id)
	_, code := m.deleteOps(id, -1, nil)
	return mResult{Code: code}
}

// releaseCloneSourcesFor is the first part of every judged delete of id,
// present or absent (PlanDeleteVolume): destroy -d of each unused
// clone-source snapshot stamped for id on a volume the endpoint owns. A
// create that died after the cut, or whose clone was refused, leaves one.
func (m *Model) releaseCloneSourcesFor(id string) {
	var rel []string
	for k, sn := range m.Snaps {
		src, _, _ := strings.Cut(k, "@")
		if c, ours := m.ownedCloneSource(k); ours && c == id && m.ownedVol(src) && !sn.Deferred && !m.hasClones(k) {
			rel = append(rel, k)
		}
	}
	sort.Strings(rel)
	for _, k := range rel {
		m.destroyDeferred(k)
	}
}

// DeleteVolumeRaced is DeleteVolume with an out-of-band action landing just
// before op k: the scan (and so every judgement) precedes it.
func (m *Model) DeleteVolumeRaced(id string, k int, race func(*Model)) mResult {
	if !validID(id) || m.DS[id] == nil || !m.ownedVol(id) || m.refuses(id, false) != endpoint.OK || k >= len(m.deletePlan(id)) {
		return mResult{Code: -1} // not a raced delete: the plain linearizations cover it
	}
	_, code := m.deleteOps(id, k, race)
	return mResult{Code: code}
}

func (m *Model) CreateSnapshot(name, vol string) mResult {
	if !validID(vol) {
		return mResult{Code: endpoint.NotFound}
	}
	if name == "" || strings.HasPrefix(name, internalPrefix) {
		return mResult{Code: endpoint.InvalidArgument}
	}
	id := vol + "@" + name
	view := func(id string) mResult {
		v, _, _ := strings.Cut(id, "@")
		s := endpoint.Snapshot{ID: id, Name: m.Snaps[id].SnapName, SourceVolumeID: v, ReadyToUse: true}
		if d, ok := m.DS[v]; ok {
			s.SizeBytes = d.Size
		}
		return mResult{Snap: s}
	}
	for _, k := range slices.Sorted(maps.Keys(m.Snaps)) {
		if s := m.Snaps[k]; m.ownedSnap(k) && s.SnapName == name && !s.Deferred {
			if k == id {
				return view(k)
			}
			return mResult{Code: endpoint.AlreadyExists}
		}
	}
	if !m.served(vol) { // SNP-12, SNP-14
		return mResult{Code: endpoint.NotFound}
	}
	if _, ok := m.Snaps[id]; ok {
		return mResult{Code: endpoint.AlreadyExists}
	}
	m.Snaps[id] = &mSnap{Managed: true, SnapName: name, SnapSrc: vol}
	return view(id)
}

func (m *Model) DeleteSnapshot(id string) mResult {
	vol, name, ok := strings.Cut(id, "@")
	if !ok || !validID(vol) || name == "" || strings.Contains(name, "@") {
		return mResult{}
	}
	s, ok := m.Snaps[id]
	if !ok || s.Deferred {
		return mResult{}
	}
	if !m.ownedSnap(id) {
		return mResult{Code: endpoint.FailedPrecondition}
	}
	m.destroyDeferred(id)
	return mResult{}
}

// Reconcile collects, to a fixpoint, owned volumes outside a desired set
// that names at least one owned volume (REC-03, REC-04) and that the
// delete contract lets go, applying group by group: a failing group is
// reported and the run goes on (REC-12). Then stamped orphan clone sources
// on owned volumes (REC-08). Everything is old enough: the harness clock
// runs two days ahead, so MinAge (REC-05, REC-06) is not modelled.
func (m *Model) Reconcile(desired []string) mResult {
	for _, id := range desired {
		if !validID(id) {
			return mResult{Code: endpoint.InvalidArgument} // REC-13
		}
	}
	want := map[string]bool{}
	stated := false
	for _, id := range desired {
		want[id] = true
		stated = stated || m.ownedVol(id)
	}
	acts := []string{}
	var pending []string
	for _, id := range slices.Sorted(maps.Keys(m.DS)) {
		if m.ownedVol(id) && !want[id] && (stated || !m.DS[id].Committed) {
			pending = append(pending, id)
		}
	}
	if n := len(pending); n > endpoint.DefaultMaxDeletions {
		return mResult{Code: endpoint.FailedPrecondition} // REC-04 cap (never reached: 4 names)
	}
	for progress := true; progress; {
		progress = false
		var blocked []string
		for _, id := range pending {
			if m.refuses(id, false) != endpoint.OK {
				blocked = append(blocked, id)
				continue
			}
			done, code := m.deleteOps(id, -1, nil)
			acts = append(acts, done...)
			progress = progress || code == endpoint.OK
		}
		pending = blocked
	}
	for _, k := range slices.Sorted(maps.Keys(m.Snaps)) {
		s, ok := m.Snaps[k]
		vol, _, _ := strings.Cut(k, "@")
		if _, ours := m.ownedCloneSource(k); !ok || !ours || s.Deferred || m.hasClones(k) || !m.ownedVol(vol) {
			continue
		}
		acts = append(acts, k+"|"+endpoint.ReasonOrphanCloneSource)
		m.destroyDeferred(k)
	}
	sort.Strings(acts)
	return mResult{Acts: acts}
}

// ---- out-of-band actions (an administrator, a backup tool) -----------------
// Each returns whether zfs would accept it.

func (m *Model) AdminSnapshot(vol, name string) bool {
	id := vol + "@" + name
	if _, ok := m.DS[vol]; !ok {
		return false
	}
	if _, ok := m.Snaps[id]; ok {
		return false
	}
	m.Snaps[id] = &mSnap{}
	return true
}

func (m *Model) AdminCreate(name string) bool {
	if _, ok := m.DS[name]; ok {
		return false
	}
	if p, _, nested := strings.Cut(name, "/"); nested {
		if d, ok := m.DS[p]; !ok || d.Kind != endpoint.Filesystem {
			return false
		}
	}
	m.DS[name] = &mDS{Kind: endpoint.Filesystem}
	return true
}

func (m *Model) AdminClone(snap, name string) bool {
	if _, ok := m.Snaps[snap]; !ok {
		return false
	}
	if _, ok := m.DS[name]; ok {
		return false
	}
	vol, _, _ := strings.Cut(snap, "@")
	src := m.DS[vol]
	d := &mDS{Kind: src.Kind, Origin: snap}
	if src.Kind == endpoint.Block {
		d.Size = src.Size
	}
	m.DS[name] = d
	return true
}

// AdminRecv is `zfs snapshot vol@tag; zfs send -R vol@tag | zfs recv to`:
// a full copy whose properties all arrive "received", so it owns nothing.
func (m *Model) AdminRecv(vol, tag, to string) bool {
	// the harness refuses a source with children: -R would need the tag
	// on every descendant
	if _, ok := m.DS[to]; ok || strings.Contains(vol, "/") || len(m.children(vol)) > 0 {
		return false
	}
	if !m.AdminSnapshot(vol, tag) {
		return false
	}
	src := m.DS[vol]
	m.DS[to] = &mDS{Kind: src.Kind, Size: src.Size, Reserved: src.Reserved}
	for _, s := range m.snapsOf(vol) {
		_, n, _ := strings.Cut(s, "@")
		m.Snaps[to+"@"+n] = &mSnap{}
	}
	return true
}

func (m *Model) AdminRename(from, to string) bool {
	if _, ok := m.DS[from]; !ok || strings.Contains(from, "/") {
		return false
	}
	if _, ok := m.DS[to]; ok {
		return false
	}
	moved := map[string]string{}
	for _, k := range append(m.children(from), from) {
		nk := to + strings.TrimPrefix(k, from)
		m.DS[nk] = m.DS[k]
		delete(m.DS, k)
		for _, s := range m.snapsOf(k) {
			ns := nk + strings.TrimPrefix(s, k)
			m.Snaps[ns] = m.Snaps[s]
			delete(m.Snaps, s)
			moved[s] = ns
		}
	}
	for _, d := range m.DS {
		if n, ok := moved[d.Origin]; ok {
			d.Origin = n
		}
	}
	return true
}

func (m *Model) AdminHold(snap string) bool {
	s, ok := m.Snaps[snap]
	if !ok || s.Holds > 0 {
		return false
	}
	s.Holds++
	return true
}

func (m *Model) AdminRelease(snap string) bool {
	s, ok := m.Snaps[snap]
	if !ok || s.Holds == 0 {
		return false
	}
	s.Holds--
	m.reap(snap)
	return true
}

// ---- observation ------------------------------------------------------------

// Project renders the world as the harness reads it back from zfs:
// relative name -> canonical row. Both sides go through this format.
func (m *Model) Project() map[string]string {
	out := map[string]string{}
	for k, d := range m.DS {
		out[k] = projDS(d.Kind, d.Size, d.Reserved, d.Origin, d.Managed, d.Committed, d.VolName, d.SrcType, d.SrcID)
	}
	for k, s := range m.Snaps {
		out[k] = projSnap(s.Deferred, s.Holds, s.Managed, s.SnapName, s.SnapSrc, s.CloneFor)
	}
	return out
}
