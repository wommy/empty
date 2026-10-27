package endpoint

import (
	"context"
	"errors"
	"iter"
	"slices"
	"strconv"
	"strings"

	"github.com/wommy/empty/internal/zfs"
)

// Row is one dataset or snapshot under the parent, decoded once from
// `zfs get -j -p` into typed columns. Everything the planners decide on is
// here; nothing downstream reads property strings.
type Row struct {
	Name     string // full zfs name
	ID       string // name relative to the parent: "<vol>" or "<vol>@<snap>"
	Type     zfs.Type
	Volsize  uint64
	Refquota uint64
	Refres   uint64
	BlockSz  uint64 // volblocksize (zvols)
	Origin   string // full name of the clone origin snapshot, "" if none
	Creation int64
	Deferred bool // snapshot marked for deferred destroy
	// Holds is a snapshot's user hold count (userrefs): zfs refuses a
	// plain destroy of a held snapshot, and a deferred one outlives its
	// last clone until the last hold is released.
	Holds uint64
	// Clones are a snapshot's clones by full name, wherever they live
	// (the clones property): a table read for one volume sees its
	// dependents without reading the rest of the parent.
	Clones []string
	// GUID identifies the dataset across renames: a dataset made since
	// under the same name has another (OWN-15). Opaque, "" if unread.
	GUID string
	// Children are the ids of datasets below a volume. The endpoint makes
	// none, so they are never its own and never in the table; zfs refuses
	// to destroy the volume without -r while they exist.
	Children []string
	Mount    string
	// Local holds the allowlisted properties (props.go) set on the dataset
	// itself; inherited and default values read as "".
	Local LocalProps

	// NFS export state (filesystems)
	ShareNFS      string // sharenfs value, local or inherited
	ShareNFSLocal bool   // sharenfs is set on the dataset itself
	Mounted       bool
	// NFSClientsSet: a leftover <prefix>:nfs_clients is set locally. Export
	// state is sharenfs alone (ADR 0004); nothing decides on it, Reconcile
	// reports it.
	NFSClientsSet bool

	// NFSServer is the NFS server address recorded at create (local
	// <prefix>:nfs_server); NFSServerElsewhere: one is set, but inherited
	// or received, so not this volume's (Reconcile reports it).
	NFSServer          string
	NFSServerElsewhere bool

	ShareNFSRecv bool   // sharenfs came from `zfs recv` (source "received")
	CanMount     string // on, off, noauto; "-" for zvols and snapshots

	// MountLocal: mountpoint is set on the dataset itself (pinned at
	// create), so Mount does not follow the parent's (Reconcile reports
	// an owned filesystem without it).
	MountLocal bool

	// ownership, from user properties
	Managed        bool
	Committed      bool // provision_success
	VolumeName     string
	SourceType     string
	SourceID       string
	SnapshotName   string
	SnapshotSource string
	CloneSourceFor string // internal clone-source snapshots: the clone's id
	CommittedAt    int64  // committed_at, 0 if unset or unreadable
}

// Capacity is the volume's size: volsize for zvols (and their snapshots),
// refquota for filesystems (0 = unlimited).
func (r Row) Capacity() uint64 {
	if r.Type == zfs.TypeVolume || r.Volsize > 0 {
		return r.Volsize
	}
	return r.Refquota
}

// Kind of the volume a row belongs to.
func (r Row) Kind() VolumeKind {
	if r.Type == zfs.TypeVolume || (r.Type == zfs.TypeSnapshot && r.Volsize > 0) {
		return Block
	}
	return Filesystem
}

// OwnedVolume reports whether r is a volume the endpoint owns: marked
// managed and with the volume-name marker for this id, both on the dataset
// itself. A copy made with `zfs send | recv` or a renamed dataset carries
// another volume's name and is not this volume (ADR 0003). Both the
// endpoint and democratic-csi write the two markers in the same create, so
// managed without a name is a state neither makes (an `zfs inherit`, a
// hand-set marker): not ours.
func (r Row) OwnedVolume() bool {
	return r.Type != zfs.TypeSnapshot && r.Managed && r.VolumeName == r.ID
}

// ServedVolume reports whether r is a volume front ends may see and use:
// owned and committed. An uncommitted row is a provision still in flight
// (or abandoned, which Reconcile collects); only CreateVolume's resume and
// DeleteVolume act on it. Every serving path (get, list, expand, export,
// snapshot and clone sources) uses this one rule.
func (r Row) ServedVolume() bool { return r.OwnedVolume() && r.Committed }

// OwnedSnapshot reports whether r is a CSI snapshot the endpoint cut: a
// managed snapshot with a CSI name whose source marker, where set, is the
// volume it hangs off. Administrators' and internal clone-source snapshots
// carry no CSI name.
func (r Row) OwnedSnapshot() bool {
	vol, _, _ := strings.Cut(r.ID, "@")
	return r.Type == zfs.TypeSnapshot && r.Managed && r.SnapshotName != "" &&
		(r.SnapshotSource == "" || r.SnapshotSource == vol)
}

// OwnedCloneSource reports whether r is an internal clone-source snapshot
// the endpoint cut, and for which clone: named
// "<vol>@volume-source-for-volume-<clone>" and stamped at creation with
// managed_resource and clone_source_for=<clone>, both set locally. A
// snapshot that only carries the reserved name is someone else's (an
// administrator's, another tool's, or one democratic-csi cut unstamped).
func (r Row) OwnedCloneSource() (string, bool) {
	_, name, isSnap := strings.Cut(r.ID, "@")
	clone, ok := strings.CutPrefix(name, cloneSourceSnapshotPrefix)
	return clone, isSnap && ok && clone != "" && r.Type == zfs.TypeSnapshot &&
		r.Managed && r.SnapshotName == "" && r.CloneSourceFor == clone
}

// Table is an immutable, ID-sorted set of rows with secondary indexes built
// once: snapshots by volume and clones by origin. Planners only read it.
//
// A table also records what it covers: the whole parent, or only the
// volumes it was read for. A row missing from a table means "not there"
// only for what the table covers; planners that conclude from absence
// check coverage (mustCover) instead of reading "not scanned" as "gone".
type Table struct {
	rows        []Row
	snapsOfVol  map[string][2]int // volume id -> its snapshots, rows[lo:hi]
	clonesOfSrc map[string][]int  // origin snapshot full name -> clone rows
	whole       bool              // every volume under the parent was read
	covered     map[string]bool   // else: the volumes that were read
	// srcsFor: the table also covers every snapshot under the parent named
	// <vol>@volume-source-for-volume-<srcsFor> (scanDelete), which is
	// what releaseCloneSourcesFor needs instead of the whole parent.
	srcsFor string
}

// NewTable sorts rows by ID and indexes them; they are everything under
// the parent (whole coverage). rows is taken over.
func NewTable(rows []Row) Table {
	t := newPartialTable(rows)
	t.whole = true
	return t
}

// newPartialTable is a table read for vols only (with their snapshots).
func newPartialTable(rows []Row, vols ...string) Table {
	slices.SortFunc(rows, func(a, b Row) int { return strings.Compare(a.ID, b.ID) })
	t := Table{rows: rows, clonesOfSrc: map[string][]int{}, covered: map[string]bool{}}
	for _, v := range vols {
		t.covered[v] = true
	}
	// In ID order every id starting with "<vol>@" sits in one run, so a
	// volume's snapshots are a range of rows: no slice per volume.
	runs := 0
	for i := 0; i < len(rows); i = snapRun(rows, i) {
		if strings.Contains(rows[i].ID, "@") {
			runs++
		}
	}
	t.snapsOfVol = make(map[string][2]int, runs)
	for i := 0; i < len(rows); {
		j := snapRun(rows, i)
		if vol, _, ok := strings.Cut(rows[i].ID, "@"); ok {
			t.snapsOfVol[vol] = [2]int{i, j}
		}
		i = j
	}
	for i, r := range rows {
		if r.Origin != "" {
			t.clonesOfSrc[r.Origin] = append(t.clonesOfSrc[r.Origin], i)
		}
	}
	return t
}

// snapRun returns the end of the run of snapshots of one volume starting
// at rows[i], or i+1 if rows[i] is not a snapshot.
func snapRun(rows []Row, i int) int {
	vol, _, ok := strings.Cut(rows[i].ID, "@")
	j := i + 1
	if !ok {
		return j
	}
	for j < len(rows) {
		id := rows[j].ID
		if len(id) <= len(vol) || id[len(vol)] != '@' || id[:len(vol)] != vol {
			break
		}
		j++
	}
	return j
}

// Whole reports whether the table covers every volume under the parent.
func (t Table) Whole() bool { return t.whole }

// Covers reports whether the table can say volume vol (or a snapshot of
// it) is absent. An id that cannot name a volume has no rows anywhere, so
// any table covers it.
func (t Table) Covers(vol string) bool {
	vol, _, _ = strings.Cut(vol, "@")
	if t.whole || t.covered[vol] {
		return true
	}
	_, err := volumeID(vol)
	return err != nil
}

// mustCover refuses a table that does not cover ids (volume or snapshot
// ids; empty ones are skipped): a planner bug, never "missing".
func (t Table) mustCover(ids ...string) error {
	for _, id := range ids {
		if id != "" && !t.Covers(id) {
			return errorf(Internal, "table does not cover %s", id)
		}
	}
	return nil
}

// mustBeWhole refuses a table that does not cover the whole parent.
func (t Table) mustBeWhole(what string) error {
	if !t.whole {
		return errorf(Internal, "%s needs a scan of the whole parent", what)
	}
	return nil
}

// Len is the number of rows.
func (t Table) Len() int { return len(t.rows) }

// Get returns the row with id.
func (t Table) Get(id string) (Row, bool) {
	i, ok := slices.BinarySearchFunc(t.rows, id, func(r Row, id string) int { return strings.Compare(r.ID, id) })
	if !ok {
		return Row{}, false
	}
	return t.rows[i], true
}

// All yields every row in ID order.
func (t Table) All() iter.Seq[Row] { return slices.Values(t.rows) }

// Volumes yields filesystem/volume rows in ID order.
func (t Table) Volumes() iter.Seq[Row] {
	return func(yield func(Row) bool) {
		for _, r := range t.rows {
			if r.Type != zfs.TypeSnapshot && !yield(r) {
				return
			}
		}
	}
}

func (t Table) pick(idx []int) iter.Seq[Row] {
	return func(yield func(Row) bool) {
		for _, i := range idx {
			if !yield(t.rows[i]) {
				return
			}
		}
	}
}

// SnapshotsOf yields the snapshots of volume id.
func (t Table) SnapshotsOf(id string) iter.Seq[Row] {
	r := t.snapsOfVol[id] // absent: {0, 0}, no rows
	return slices.Values(t.rows[r[0]:r[1]])
}

// ClonesOf yields rows whose origin is the snapshot with full name snap.
func (t Table) ClonesOf(snap string) iter.Seq[Row] { return t.pick(t.clonesOfSrc[snap]) }

// dependent names a clone of snapshot s, if it has any: a row of the table
// first (by id, in id order), else one its clones property lists, by id
// when under the parent, by full name elsewhere. A table read for one
// volume holds no clone rows; the property still names them.
func (t Table) dependent(s Row, parent string) (string, bool) {
	for c := range t.ClonesOf(s.Name) {
		return c.ID, true
	}
	if len(s.Clones) == 0 {
		return "", false
	}
	c := slices.Min(s.Clones)
	if rel, ok := strings.CutPrefix(c, parent+"/"); ok {
		return rel, true
	}
	return c, true
}

// mergeTables is one partial table of the rows of parts, which cover
// disjoint volumes, covering what each covers.
func mergeTables(parts ...Table) Table {
	var rows []Row
	var vols []string
	for _, p := range parts {
		rows = append(rows, p.rows...)
		for v := range p.covered {
			vols = append(vols, v)
		}
	}
	return newPartialTable(rows, vols...)
}

// columns fetched by every scan; decode below is the only place they are read.
func (n props) columns() []string {
	return append([]string{
		"type", "volsize", "volblocksize", "refquota", "refreservation", "origin", "creation",
		"defer_destroy", "userrefs", "clones", "guid", "mountpoint", "sharenfs", "mounted", "canmount",
		n.managed(), n.success(), n.volumeName(), n.sourceType(), n.sourceID(),
		n.snapshotName(), n.snapshotSource(), n.nfsClients(), n.cloneSourceFor(), n.committedAt(), n.nfsServer(),
	}, propNames[:]...)
}

// decodeColumns turns a columnar scan into rows: each column is located
// once, then every dataset is a straight read of its cells. It fails the
// whole scan on a number zfs could not have printed, or a zvol whose size
// breaks OpenZFS's own rules (volsize > 0, a multiple of volblocksize):
// read as 0, such a size would let Expand plan a shrink.
func (n props) decodeColumns(parent string, c *zfs.Columns) ([]Row, error) {
	j := func(p string) int { return c.Col(p) }
	cType, cVolsize, cBlock, cRefquota, cRefres := j("type"), j("volsize"), j("volblocksize"), j("refquota"), j("refreservation")
	cOrigin, cCreation, cDefer, cMount := j("origin"), j("creation"), j("defer_destroy"), j("mountpoint")
	cHolds, cClones, cGUID := j("userrefs"), j("clones"), j("guid")
	cManaged, cSuccess, cVolName := j(n.managed()), j(n.success()), j(n.volumeName())
	cSrcType, cSrcID, cSnapName, cSnapSrc := j(n.sourceType()), j(n.sourceID()), j(n.snapshotName()), j(n.snapshotSource())
	var cLocal [nPropKeys]int
	for k, name := range propNames {
		cLocal[k] = j(name)
	}
	cShare, cMounted, cCanMount, cClients := j("sharenfs"), j("mounted"), j("canmount"), j(n.nfsClients())
	cCloneFor, cCommittedAt, cNFSServer := j(n.cloneSourceFor()), j(n.committedAt()), j(n.nfsServer())

	rows := make([]Row, 0, len(c.Names))
	var bad error
	for i, name := range c.Names {
		cell := func(k int) zfs.Prop { return c.Cell(i, k) }
		u := func(k int) uint64 {
			v, err := cell(k).Uint()
			if err != nil && bad == nil {
				bad = errorf(Internal, "scan: %s: unparsable number %q: %v", name, cell(k).Value, err)
			}
			return v
		}
		// ownership must be set on the dataset itself: user properties
		// inherit, so every snapshot (and child) would otherwise look owned
		own := func(k int) string {
			if p := cell(k); p.Local() {
				return userValue(p)
			}
			return ""
		}
		r := Row{
			Name:           name,
			ID:             strings.TrimPrefix(name, parent+"/"),
			Type:           zfs.Type(cell(cType).Value),
			Volsize:        u(cVolsize),
			Refquota:       u(cRefquota),
			Refres:         u(cRefres),
			BlockSz:        u(cBlock),
			Origin:         userValue(cell(cOrigin)),
			Deferred:       cell(cDefer).Value == "on",
			Holds:          u(cHolds),
			GUID:           userValue(cell(cGUID)),
			Mount:          userValue(cell(cMount)),
			Managed:        own(cManaged) == "true",
			Committed:      own(cSuccess) == "true",
			VolumeName:     own(cVolName),
			SourceType:     own(cSrcType),
			SourceID:       own(cSrcID),
			SnapshotName:   own(cSnapName),
			SnapshotSource: own(cSnapSrc),
			CloneSourceFor: own(cCloneFor),
			ShareNFS:       userValue(cell(cShare)),
			ShareNFSLocal:  cell(cShare).Local(),
			Mounted:        cell(cMounted).Value == "yes",
			NFSClientsSet:  cell(cClients).Local(),
			ShareNFSRecv:   cell(cShare).Source == "received",
			CanMount:       cell(cCanMount).Value,
		}
		r.NFSServer = own(cNFSServer)
		r.NFSServerElsewhere = !cell(cNFSServer).Local() && userValue(cell(cNFSServer)) != ""
		r.MountLocal = cell(cMount).Local() // the mountpoint pin (no new column)
		// "-" (or "") when none; most snapshots have none
		if v := userValue(cell(cClones)); v != "" && r.Type == zfs.TypeSnapshot {
			r.Clones = strings.Split(v, ",")
		}
		for k, col := range cLocal {
			// only local values: an inherited compression is the parent's
			// choice, not this volume's, and must not satisfy a request
			r.Local[k] = own(col)
		}
		if v, err := strconv.ParseInt(cell(cCreation).Value, 10, 64); err == nil {
			r.Creation = v
		}
		if r.Type == zfs.TypeVolume && (r.Volsize == 0 || r.BlockSz == 0 || r.Volsize%r.BlockSz != 0) && bad == nil {
			bad = errorf(Internal, "scan: %s: zvol volsize %d / volblocksize %d breaks OpenZFS's own invariants", name, r.Volsize, r.BlockSz)
		}
		// most rows have none: skip ParseInt's error allocation for ""
		if at := own(cCommittedAt); at != "" {
			if v, err := strconv.ParseInt(at, 10, 64); err == nil && v > 0 {
				r.CommittedAt = v
			}
		}
		rows = append(rows, r)
	}
	if bad != nil {
		return nil, bad
	}
	return rows, nil
}

// scan reads the parent's volumes and snapshots in one zfs call; with only
// set, that one volume and its snapshots. The parent itself and anything
// deeper than a volume are dropped.
// maxWholeScans bounds the whole-parent scans in flight. Each holds zfs's
// output and its parsed copy, both O(volumes) (about 3.6 KB per dataset),
// and locks are per volume while zfs.Limit bounds processes, not buffers:
// without it, a namespace deletion of N volumes held N copies at once.
// Waiting callers queue in arrival order and give up with their context.
const maxWholeScans = 2

func (e *Endpoint) scan(ctx context.Context, only string) (Table, error) {
	root, depth := e.cfg.Parent, 1 // parent/vol and parent/vol@snap
	if only == "" {
		select {
		case e.scans <- struct{}{}:
			defer func() { <-e.scans }()
		case <-ctx.Done():
			code := Aborted
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				code = DeadlineExceeded
			}
			return Table{}, &Error{Code: code, Msg: "waiting for a whole-parent scan slot", Err: ctx.Err()}
		}
	}
	if only != "" {
		root, depth = e.dataset(only), 0 // vol and vol@snap
	}
	cols, err := e.zfs.ListColumns(ctx, root, zfs.ListOptions{
		Types: []zfs.Type{zfs.TypeFilesystem, zfs.TypeVolume, zfs.TypeSnapshot},
		Depth: depth,
		Props: e.names.columns(),
	})
	if err != nil {
		if only == "" && zfs.IsNotFound(err) {
			// the root is the parent itself: missing or its pool exported
			return Table{}, parentUnavailable(e.cfg.Parent, err)
		}
		if only != "" && zfs.IsNotFound(err) {
			// zfs says "dataset does not exist" for an exported pool too:
			// only an existing parent makes a missing volume truly absent
			ok, perr := e.zfs.Exists(ctx, e.cfg.Parent)
			if perr == nil && ok {
				return newPartialTable(nil, only), nil
			}
			return Table{}, parentUnavailable(e.cfg.Parent, err)
		}
		return Table{}, zfsErr(err, "scan "+root)
	}
	rows, err := e.names.decodeColumns(e.cfg.Parent, cols)
	if err != nil {
		return Table{}, err
	}
	// a dataset below a volume is dropped, but its volume records it: the
	// volume cannot be destroyed without -r while it stands (OWN-12)
	below := map[string][]string{}
	rows = slices.DeleteFunc(rows, func(r Row) bool {
		if !strings.HasPrefix(r.Name, e.cfg.Parent+"/") {
			return true // the parent and its own snapshots (pool-root parents)
		}
		vol, _, _ := strings.Cut(r.ID, "@")
		if top, _, deeper := strings.Cut(vol, "/"); deeper {
			if r.Type != zfs.TypeSnapshot && top != "" {
				below[top] = append(below[top], r.ID)
			}
			return true
		}
		return vol == ""
	})
	for i := range rows {
		if kids, ok := below[rows[i].ID]; ok {
			rows[i].Children = kids
		}
	}
	if only != "" {
		return newPartialTable(rows, only), nil
	}
	return NewTable(rows), nil
}
