package endpoint

import (
	"context"
	"iter"
	"slices"
	"strconv"
	"strings"

	"github.com/wommy/empty/internal/zfs"
)

// Row is one dataset or snapshot under the parent, decoded once from
// `zfs get -Hp` into typed columns. Everything the planners decide on is
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
	Mount    string

	// NFS export state (filesystems)
	ShareNFS      string // sharenfs value, local or inherited
	ShareNFSLocal bool   // sharenfs is set on the dataset itself
	Mounted       bool
	NFSClients    string // locally set desired client list (export.go)
	NFSClientsSet bool

	// ownership, from user properties
	Managed        bool
	Committed      bool // provision_success
	VolumeName     string
	SourceType     string
	SourceID       string
	SnapshotName   string
	SnapshotSource string
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

// Table is an immutable, ID-sorted set of rows with secondary indexes built
// once: snapshots by volume and clones by origin. Planners only read it.
type Table struct {
	rows        []Row
	snapsOfVol  map[string][]int // volume id -> snapshot rows
	clonesOfSrc map[string][]int // origin snapshot full name -> clone rows
}

// NewTable sorts rows by ID and indexes them. rows is taken over.
func NewTable(rows []Row) Table {
	slices.SortFunc(rows, func(a, b Row) int { return strings.Compare(a.ID, b.ID) })
	t := Table{rows: rows, snapsOfVol: map[string][]int{}, clonesOfSrc: map[string][]int{}}
	for i, r := range rows {
		if vol, _, ok := strings.Cut(r.ID, "@"); ok {
			t.snapsOfVol[vol] = append(t.snapsOfVol[vol], i)
		}
		if r.Origin != "" {
			t.clonesOfSrc[r.Origin] = append(t.clonesOfSrc[r.Origin], i)
		}
	}
	return t
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
func (t Table) SnapshotsOf(id string) iter.Seq[Row] { return t.pick(t.snapsOfVol[id]) }

// ClonesOf yields rows whose origin is the snapshot with full name snap.
func (t Table) ClonesOf(snap string) iter.Seq[Row] { return t.pick(t.clonesOfSrc[snap]) }

// columns fetched by every scan; decode below is the only place they are read.
func (n props) columns() []string {
	return []string{
		"type", "volsize", "volblocksize", "refquota", "refreservation", "origin", "creation",
		"defer_destroy", "mountpoint", "sharenfs", "mounted",
		n.managed(), n.success(), n.volumeName(), n.sourceType(), n.sourceID(),
		n.snapshotName(), n.snapshotSource(), n.nfsClients(),
	}
}

// decodeColumns turns a columnar scan into rows: each column is located
// once, then every dataset is a straight read of its cells.
func (n props) decodeColumns(parent string, c *zfs.Columns) []Row {
	j := func(p string) int { return c.Col(p) }
	cType, cVolsize, cBlock, cRefquota, cRefres := j("type"), j("volsize"), j("volblocksize"), j("refquota"), j("refreservation")
	cOrigin, cCreation, cDefer, cMount := j("origin"), j("creation"), j("defer_destroy"), j("mountpoint")
	cManaged, cSuccess, cVolName := j(n.managed()), j(n.success()), j(n.volumeName())
	cSrcType, cSrcID, cSnapName, cSnapSrc := j(n.sourceType()), j(n.sourceID()), j(n.snapshotName()), j(n.snapshotSource())
	cShare, cMounted, cClients := j("sharenfs"), j("mounted"), j(n.nfsClients())

	rows := make([]Row, 0, len(c.Names))
	for i, name := range c.Names {
		cell := func(k int) zfs.Prop { return c.Cell(i, k) }
		u := func(k int) uint64 { v, _ := cell(k).Uint(); return v }
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
			Mount:          userValue(cell(cMount)),
			Managed:        own(cManaged) == "true",
			Committed:      own(cSuccess) == "true",
			VolumeName:     own(cVolName),
			SourceType:     own(cSrcType),
			SourceID:       own(cSrcID),
			SnapshotName:   own(cSnapName),
			SnapshotSource: own(cSnapSrc),
			ShareNFS:       userValue(cell(cShare)),
			ShareNFSLocal:  cell(cShare).Local(),
			Mounted:        cell(cMounted).Value == "yes",
			NFSClients:     own(cClients),
			NFSClientsSet:  cell(cClients).Local(),
		}
		if v, err := strconv.ParseInt(cell(cCreation).Value, 10, 64); err == nil {
			r.Creation = v
		}
		rows = append(rows, r)
	}
	return rows
}

// scan reads the parent's volumes and snapshots in one zfs call; with only
// set, that one volume and its snapshots. The parent itself and anything
// deeper than a volume are dropped.
func (e *Endpoint) scan(ctx context.Context, only string) (Table, error) {
	root, depth := e.cfg.Parent, 1 // parent/vol and parent/vol@snap
	if only != "" {
		root, depth = e.dataset(only), 0 // vol and vol@snap
	}
	cols, err := e.zfs.ListColumns(ctx, root, zfs.ListOptions{
		Types: []zfs.Type{zfs.TypeFilesystem, zfs.TypeVolume, zfs.TypeSnapshot},
		Depth: depth,
		Props: e.names.columns(),
	})
	if err != nil {
		if only != "" && zfs.IsNotFound(err) {
			// zfs says "dataset does not exist" for an exported pool too:
			// only an existing parent makes a missing volume truly absent
			ok, perr := e.zfs.Exists(ctx, e.cfg.Parent)
			if perr == nil && ok {
				return NewTable(nil), nil
			}
			return Table{}, &Error{Code: Unavailable, Msg: "parent dataset " + e.cfg.Parent + " is unavailable", Err: err}
		}
		return Table{}, zfsErr(err, "scan "+root)
	}
	rows := e.names.decodeColumns(e.cfg.Parent, cols)
	rows = slices.DeleteFunc(rows, func(r Row) bool {
		if !strings.HasPrefix(r.Name, e.cfg.Parent+"/") {
			return true // the parent and its own snapshots (pool-root parents)
		}
		vol, _, _ := strings.Cut(r.ID, "@")
		return vol == "" || strings.Contains(vol, "/")
	})
	return NewTable(rows), nil
}
