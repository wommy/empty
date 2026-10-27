package endpoint

import (
	"context"
	"sort"
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

// Table is a sorted set of rows with an index by ID.
type Table struct {
	Rows []Row
	idx  map[string]int
}

// Get returns the row with id.
func (t Table) Get(id string) (Row, bool) {
	i, ok := t.idx[id]
	if !ok {
		return Row{}, false
	}
	return t.Rows[i], true
}

// Volumes returns filesystem/volume rows (in name order).
func (t Table) Volumes() []Row {
	var out []Row
	for _, r := range t.Rows {
		if r.Type != zfs.TypeSnapshot {
			out = append(out, r)
		}
	}
	return out
}

// SnapshotsOf returns the snapshots of volume id.
func (t Table) SnapshotsOf(id string) []Row {
	var out []Row
	for _, r := range t.Rows {
		if r.Type == zfs.TypeSnapshot && strings.HasPrefix(r.ID, id+"@") {
			out = append(out, r)
		}
	}
	return out
}

// ClonesOf returns rows whose origin is the snapshot with full name snap.
func (t Table) ClonesOf(snap string) []Row {
	var out []Row
	for _, r := range t.Rows {
		if r.Origin == snap {
			out = append(out, r)
		}
	}
	return out
}

// NewTable builds a Table from rows (used by Scan and by tests).
func NewTable(rows []Row) Table {
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	t := Table{Rows: rows, idx: make(map[string]int, len(rows))}
	for i, r := range rows {
		t.idx[r.ID] = i
	}
	return t
}

// columns fetched by every scan; decode below is the only place they are read.
func (n props) columns() []string {
	return []string{
		"type", "volsize", "volblocksize", "refquota", "refreservation", "origin", "creation",
		"defer_destroy", "mountpoint",
		n.managed(), n.success(), n.volumeName(), n.sourceType(), n.sourceID(),
		n.snapshotName(), n.snapshotSource(),
	}
}

func (n props) decode(parent string, d zfs.Dataset) Row {
	u := func(p string) uint64 { v, _ := d.Props[p].Uint(); return v }
	s := func(p string) string { return userValue(d.Props[p]) }
	// ownership must be set on the dataset itself: user properties inherit,
	// so every snapshot (and any child) would otherwise look owned
	own := func(p string) string {
		if !d.Props[p].Local() {
			return ""
		}
		return userValue(d.Props[p])
	}
	r := Row{
		Name:           d.Name,
		ID:             strings.TrimPrefix(d.Name, parent+"/"),
		Type:           d.Type(),
		Volsize:        u("volsize"),
		Refquota:       u("refquota"),
		Refres:         u("refreservation"),
		BlockSz:        u("volblocksize"),
		Origin:         s("origin"),
		Deferred:       d.Props["defer_destroy"].Value == "on",
		Mount:          s("mountpoint"),
		Managed:        own(n.managed()) == "true",
		Committed:      own(n.success()) == "true",
		VolumeName:     own(n.volumeName()),
		SourceType:     own(n.sourceType()),
		SourceID:       own(n.sourceID()),
		SnapshotName:   own(n.snapshotName()),
		SnapshotSource: own(n.snapshotSource()),
	}
	if c, err := strconv.ParseInt(d.Props["creation"].Value, 10, 64); err == nil {
		r.Creation = c
	}
	return r
}

// scan reads the parent's volumes and their snapshots in one zfs call.
// With only set, it reads that one volume and its snapshots instead.
func (e *Endpoint) scan(ctx context.Context, only string) (Table, error) {
	root, depth := e.cfg.Parent, 1 // parent/vol and parent/vol@snap
	if only != "" {
		root, depth = e.dataset(only), 0 // vol and vol@snap
	}
	list, err := e.zfs.List(ctx, root, zfs.ListOptions{
		Types: []zfs.Type{zfs.TypeFilesystem, zfs.TypeVolume, zfs.TypeSnapshot},
		Depth: depth,
		Props: e.names.columns(),
	})
	if err != nil {
		if only != "" && zfs.IsNotFound(err) {
			return NewTable(nil), nil
		}
		return Table{}, zfsErr(err, "scan "+root)
	}
	if only != "" {
		// List excludes the root itself; fetch it alongside
		d, err := e.zfs.Get(ctx, root, e.names.columns()...)
		if err != nil {
			if zfs.IsNotFound(err) {
				return NewTable(nil), nil
			}
			return Table{}, zfsErr(err, "scan "+root)
		}
		list = append(list, d)
	}
	rows := make([]Row, 0, len(list))
	for _, d := range list {
		r := e.names.decode(e.cfg.Parent, d)
		// only direct children of the parent are volumes
		if vol, _, _ := strings.Cut(r.ID, "@"); strings.Contains(vol, "/") {
			continue
		}
		rows = append(rows, r)
	}
	return NewTable(rows), nil
}
