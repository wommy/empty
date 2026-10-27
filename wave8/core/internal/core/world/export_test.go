package world

import (
	"io"
	"slices"
	"strings"
	"unsafe"
)

// Test hooks for the layout benchmark: the decoded records before build,
// and a kind-tagged alternative to the split per-kind slices.

type Raw struct{ p *parser }

func (sc *Schema) ParseRaw(scope Scope, docs ...io.Reader) (*Raw, error) {
	p := &parser{sc: sc, scope: scope, props: sc.Props(scope), children: map[string]int{}}
	p.col = map[string]int{}
	for j, n := range p.props {
		p.col[n] = j
	}
	for _, r := range docs {
		p.dec = newDecoder(r)
		if err := p.document(); err != nil {
			return nil, err
		}
	}
	return &Raw{p}, nil
}

// BuildSplit is Parse's build step (sorted per-kind slices, snapshot ranges).
func (r *Raw) BuildSplit() (*World, error) {
	q := *r.p
	q.vols, q.snaps = slices.Clone(r.p.vols), slices.Clone(r.p.snaps)
	return q.build()
}

// tagRow is the kind-tagged alternative: one fat row per dataset, every
// field present whatever the kind (HEAD's Row shape, typed).
type tagRow struct {
	name, origin, srcID, volName, csiName string
	guid, size, refres, volsize           uint64
	creation                              int64
	bs, clones                            uint32
	children                              uint16
	kind, own                             uint8
	deferred                              bool
	local                                 Local
}

type Tagged struct {
	rows    []tagRow
	snapsOf map[string][]int // volume name -> snapshot rows
}

// BuildTagged sorts fat rows by name and indexes snapshots by volume, as
// HEAD's NewTable does.
func (r *Raw) BuildTagged() *Tagged {
	t := &Tagged{rows: make([]tagRow, 0, len(r.p.vols)+len(r.p.snaps))}
	for _, v := range r.p.vols {
		t.rows = append(t.rows, tagRow{name: v.id.String(), origin: v.origin, srcID: v.srcID, volName: v.volName, guid: v.guid,
			size: v.size, refres: v.refres, creation: v.creation, bs: v.bs, children: v.children, kind: uint8(v.kind), own: uint8(v.own), local: v.local})
	}
	for _, s := range r.p.snaps {
		t.rows = append(t.rows, tagRow{name: s.volID + "@" + s.comp, csiName: s.csiName, guid: s.guid, volsize: s.volsize,
			creation: s.creation, clones: s.clones, kind: 3, own: uint8(s.own), deferred: s.deferred})
	}
	slices.SortFunc(t.rows, func(a, b tagRow) int { return strings.Compare(a.name, b.name) })
	t.snapsOf = map[string][]int{}
	for i, r := range t.rows {
		if vol, _, ok := strings.Cut(r.name, "@"); ok {
			t.snapsOf[vol] = append(t.snapsOf[vol], i)
		}
	}
	return t
}

// JudgeAll is DeleteVolume's per-volume read pattern for every volume:
// does any snapshot block it?
func (t *Tagged) JudgeAll() (blocked int) {
	for _, r := range t.rows {
		if r.kind == 3 {
			continue
		}
		for _, i := range t.snapsOf[r.name] {
			if s := &t.rows[i]; s.clones > 0 || s.own == uint8(SnapCSI) {
				blocked++
				break
			}
		}
	}
	return blocked
}

func (w *World) JudgeAll() (blocked int) {
	for i := range w.vols {
		v := &w.vols[i]
		for _, s := range w.snaps[v.snapLo:v.snapHi] {
			if s.clones > 0 || s.own == SnapCSI {
				blocked++
				break
			}
		}
	}
	return blocked
}

// ListCommitted is ListVolumes' read pattern.
func (t *Tagged) ListCommitted() (n int) {
	for i := range t.rows {
		if t.rows[i].kind != 3 && t.rows[i].own == uint8(VolCommitted) {
			n++
		}
	}
	return n
}

func (w *World) ListCommitted() (n int) {
	for i := range w.vols {
		if w.vols[i].own == VolCommitted {
			n++
		}
	}
	return n
}

// CountSnaps is ListSnapshots' read pattern.
func (t *Tagged) CountSnaps() (n int) {
	for i := range t.rows {
		if t.rows[i].kind == 3 && t.rows[i].own == uint8(SnapCSI) {
			n++
		}
	}
	return n
}

func (w *World) CountSnaps() (n int) {
	for i := range w.snaps {
		if w.snaps[i].own == SnapCSI {
			n++
		}
	}
	return n
}

func Sizes() (vol, snap, tag uintptr) {
	return unsafe.Sizeof(Vol{}), unsafe.Sizeof(Snap{}), unsafe.Sizeof(tagRow{})
}
