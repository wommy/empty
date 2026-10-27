package world

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unsafe"

	"github.com/wommy/empty/internal/core/name"
)

// ParseError is output OpenZFS 2.4 could not have printed. The whole scan is
// refused (Internal): a world built from it would be a guess.
type ParseError struct {
	Dataset string
	Rule    string
}

func (e *ParseError) Error() string {
	if e.Dataset == "" {
		return "zfs get -j: " + e.Rule
	}
	return fmt.Sprintf("zfs get -j: %s: %s", e.Dataset, e.Rule)
}

func perr(ds, f string, a ...any) error { return &ParseError{ds, fmt.Sprintf(f, a...)} }

// column indexes, in Schema.Props order
const (
	cCreation = iota
	cGuid
	cOrigin
	cClones
	cVolsize
	cVolbs
	cRefquota
	cRefres
	cDefer
	cUser0                        // nUser user properties
	cLocal0 = cUser0 + int(nUser) // NLocal local properties (when scoped)
	maxCols = cLocal0 + int(NLocal)
)

type srcType uint8

const (
	sNone srcType = iota + 1
	sDefault
	sLocal
	sInherited
	sReceived
	sTemporary
)

var srcNames = map[string]srcType{"NONE": sNone, "DEFAULT": sDefault, "LOCAL": sLocal, "INHERITED": sInherited, "RECEIVED": sReceived, "TEMPORARY": sTemporary}

type kindT uint8

const (
	tFS kindT = iota + 1
	tVol
	tSnap
)

// cell is one property of the dataset being decoded: its value as a span
// of the parser's scratch buffer, and its source type.
type cell struct {
	lo, hi int32
	src    srcType
}

type parser struct {
	sc      *Schema
	scope   Scope
	lease   Lease
	props   []string
	col     map[string]int
	dec     *jsontext.Decoder
	scratch []byte
	ubuf    []byte // unquote buffer for str
	kbuf    []byte // member name, copied out of the decoder's buffer
	cells   [maxCols]cell
	arena   arena

	// accumulated rows
	vols     []rawVol
	snaps    []rawSnap
	children map[string]int
	dropped  map[string]bool
}

type rawVol struct {
	Vol
	local Local
}

type rawSnap struct {
	Snap
	volID      string
	hasVolsize bool
}

// Parse decodes the documents of one scan of scope (one per Schema.Argv
// entry; a document for a volume that zfs said does not exist is simply
// left out) into a World. All or nothing.
func (sc *Schema) Parse(scope Scope, docs ...io.Reader) (*World, error) {
	return sc.ParseUnder(Lease{}, scope, docs...)
}

// ParseUnder is Parse for a scan taken after lease's locks were acquired.
// Only such a world binds requests (Bind), so only it mints handles that
// lead to ops; a world from Parse (List, single-flight) is read-only.
func (sc *Schema) ParseUnder(lease Lease, scope Scope, docs ...io.Reader) (*World, error) {
	p := &parser{sc: sc, scope: scope, lease: lease, props: sc.Props(scope), children: map[string]int{}}
	if len(p.props) > maxProps {
		panic("world: more properties than the parser's seen-set holds") // unreachable: TestWidestSchemaFits
	}
	p.col = make(map[string]int, len(p.props))
	for j, n := range p.props {
		p.col[n] = j
	}
	for _, r := range docs {
		// Invalid UTF-8 stays an error (zfs truncates on it, adv4/zfs-json.md).
		// Duplicate names are checked per object below, cheaper than jsontext's.
		p.dec = newDecoder(r)
		if err := p.document(); err != nil {
			var pe *ParseError
			if errors.As(err, &pe) {
				return nil, err
			}
			return nil, &ParseError{Rule: fmt.Sprintf("at byte %d: %v", p.dec.InputOffset(), err)}
		}
	}
	return p.build()
}

// maxProps is what the per-dataset seen-set (a uint64) can hold: at 64 the
// shift wraps to 0 and "all present" would no longer be checked (A7).
const maxProps = 64

func newDecoder(r io.Reader) *jsontext.Decoder {
	return jsontext.NewDecoder(r, jsontext.AllowDuplicateNames(true))
}

// ---- JSON shape --------------------------------------------------------------

func (p *parser) open() error {
	t, err := p.dec.ReadToken()
	if err != nil {
		return err
	}
	if t.Kind() != '{' {
		return errors.New("want object")
	}
	return nil
}

// member reads the next member name of the current object into scratch
// (returned slice valid until the next scratch reset), or reports the end.
func (p *parser) member() ([]byte, bool, error) {
	if p.dec.PeekKind() == '}' {
		_, err := p.dec.ReadToken()
		return nil, true, err
	}
	v, err := p.dec.ReadValue()
	if err != nil {
		return nil, false, err
	}
	// Copied: the decoder may refill (and overwrite) its buffer on the next
	// read, and callers read the value before they are done with the name.
	// Valid until the next member call.
	if bytes.IndexByte(v, '\\') < 0 {
		p.kbuf = append(p.kbuf[:0], v[1:len(v)-1]...)
		return p.kbuf, false, nil
	}
	p.kbuf, err = jsontext.AppendUnquote(p.kbuf[:0], v)
	return p.kbuf, false, err
}

// str reads a string value. The result aliases the decoder's buffer: copy it
// before the next read.
func (p *parser) str() ([]byte, error) {
	if p.dec.PeekKind() != '"' {
		return nil, errors.New("want string")
	}
	v, err := p.dec.ReadValue()
	if err != nil {
		return nil, err
	}
	if bytes.IndexByte(v, '\\') < 0 {
		return v[1 : len(v)-1], nil
	}
	p.ubuf, err = jsontext.AppendUnquote(p.ubuf[:0], v)
	return p.ubuf, err
}

func unquote(v jsontext.Value) []byte {
	if bytes.IndexByte(v, '\\') < 0 {
		return v[1 : len(v)-1]
	}
	b, _ := jsontext.AppendUnquote(nil, v)
	return b
}

func (p *parser) document() error {
	if err := p.open(); err != nil {
		return err
	}
	var got uint8
	for {
		k, end, err := p.member()
		if err != nil {
			return err
		}
		if end {
			break
		}
		switch string(k) {
		case "output_version":
			if got&1 != 0 {
				return errors.New("output_version twice")
			}
			got |= 1
			err = p.version()
		case "datasets":
			if got&2 != 0 {
				return errors.New("datasets twice")
			}
			got |= 2
			err = p.datasets()
		default:
			err = fmt.Errorf("unknown member %q", k)
		}
		if err != nil {
			return err
		}
	}
	if got != 3 {
		return errors.New("output_version or datasets missing")
	}
	if _, err := p.dec.ReadToken(); err != io.EOF {
		return errors.New("trailing data after the document")
	}
	return nil
}

// version accepts exactly {"command":"zfs get","vers_major":0,"vers_minor":1}
// (zfs_json_schema(0, 1), unchanged 2.3.0..2.4.4).
func (p *parser) version() error {
	if err := p.open(); err != nil {
		return err
	}
	want := map[string]string{"command": `"zfs get"`, "vers_major": "0", "vers_minor": "1"}
	n := 0
	for {
		k, end, err := p.member()
		if err != nil {
			return err
		}
		if end {
			break
		}
		w, ok := want[string(k)]
		v, err := p.dec.ReadValue()
		if err != nil {
			return err
		}
		if !ok || string(v) != w {
			return fmt.Errorf("output_version %s=%s unsupported", k, v)
		}
		delete(want, string(k))
		n++
	}
	if n != 3 {
		return errors.New("output_version incomplete")
	}
	return nil
}

func (p *parser) datasets() error {
	if err := p.open(); err != nil {
		return err
	}
	for {
		k, end, err := p.member()
		if err != nil {
			return err
		}
		if end {
			return nil
		}
		key := p.arena.keep(k)
		if err := p.dataset(key); err != nil {
			var pe *ParseError
			if errors.As(err, &pe) {
				return err
			}
			return &ParseError{key, err.Error()}
		}
	}
}

const (
	hName = 1 << iota
	hType
	hPool
	hTxg
	hDataset
	hSnapName
	hProps
)

func (p *parser) dataset(key string) error {
	if err := p.open(); err != nil {
		return err
	}
	var got int
	var typ kindT
	keyDS, keySnap, _ := strings.Cut(key, "@")
	var dsAgrees, snapAgrees bool // compared in place: no copy per snapshot
	p.scratch = p.scratch[:0]
	for {
		k, end, err := p.member()
		if err != nil {
			return err
		}
		if end {
			break
		}
		var bit int
		switch string(k) {
		case "name":
			bit = hName
			v, err := p.str()
			if err != nil {
				return err
			}
			if string(v) != key {
				return errors.New("name disagrees with its key")
			}
		case "type":
			bit = hType
			v, err := p.str()
			if err != nil {
				return err
			}
			switch string(v) {
			case "FILESYSTEM":
				typ = tFS
			case "VOLUME":
				typ = tVol
			case "SNAPSHOT":
				typ = tSnap
			default: // bookmarks and pools were not asked for
				return fmt.Errorf("type %s not requested", v)
			}
		case "pool":
			bit = hPool
			v, err := p.str()
			if err != nil {
				return err
			}
			if pool, _, _ := strings.Cut(key, "/"); string(v) != strings.SplitN(pool, "@", 2)[0] {
				return errors.New("pool disagrees with the name")
			}
		case "createtxg":
			bit = hTxg
			v, err := p.str()
			if err != nil {
				return err
			}
			if _, ok := decimal(v); !ok {
				return errors.New("createtxg not a decimal")
			}
		case "dataset":
			bit = hDataset
			v, err := p.str()
			if err != nil {
				return err
			}
			dsAgrees = string(v) == keyDS
		case "snapshot_name":
			bit = hSnapName
			v, err := p.str()
			if err != nil {
				return err
			}
			snapAgrees = string(v) == keySnap
		case "properties":
			bit = hProps
			if err := p.properties(key); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown member %q", k)
		}
		if got&bit != 0 {
			return fmt.Errorf("header member %d twice", bit)
		}
		got |= bit
	}
	base := hName | hType | hPool | hTxg | hProps
	// invariant 1: the type and the delimiters agree, and the snapshot
	// header fields exist exactly for snapshots and agree with the name
	if typ == tSnap {
		_, _, ok := strings.Cut(key, "@")
		if got != base|hDataset|hSnapName || !ok || !dsAgrees || !snapAgrees {
			return errors.New("snapshot header disagrees with its name")
		}
	} else if got != base || strings.ContainsAny(key, "@#") {
		return errors.New("dataset header disagrees with its type or name")
	}
	return p.row(key, typ)
}

// properties reads {"<prop>":{"value":"...","source":{"type":"...","data":"..."}},...}
// into p.cells: every requested property exactly once, nothing else.
func (p *parser) properties(ds string) error {
	if err := p.open(); err != nil {
		return err
	}
	var seen uint64 // len(p.props) <= maxProps, checked in ParseUnder
	for {
		k, end, err := p.member()
		if err != nil {
			return err
		}
		if end {
			break
		}
		j, ok := p.col[string(k)]
		if !ok {
			return fmt.Errorf("property %q not requested", k)
		}
		if seen&(1<<j) != 0 {
			return fmt.Errorf("property %q twice", k)
		}
		seen |= 1 << j
		if err := p.prop(&p.cells[j]); err != nil {
			return fmt.Errorf("property %q: %w", p.props[j], err) // k is overwritten by prop's members
		}
	}
	if seen != 1<<len(p.props)-1 {
		return errors.New("a requested property is missing")
	}
	return nil
}

func (p *parser) prop(c *cell) error {
	if err := p.open(); err != nil {
		return err
	}
	var got uint8
	for {
		k, end, err := p.member()
		if err != nil {
			return err
		}
		if end {
			break
		}
		switch string(k) {
		case "value":
			if got&1 != 0 {
				return errors.New("value twice")
			}
			got |= 1
			v, err := p.str() // never --json-int: values stay strings
			if err != nil {
				return err
			}
			c.lo = int32(len(p.scratch))
			p.scratch = append(p.scratch, v...)
			c.hi = int32(len(p.scratch))
		case "source":
			if got&2 != 0 {
				return errors.New("source twice")
			}
			got |= 2
			if err := p.source(c); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown member %q", k)
		}
	}
	if got != 3 {
		return errors.New("value or source missing")
	}
	return nil
}

func (p *parser) source(c *cell) error {
	if err := p.open(); err != nil {
		return err
	}
	var got uint8
	for {
		k, end, err := p.member()
		if err != nil {
			return err
		}
		if end {
			break
		}
		v, err := p.str()
		if err != nil {
			return err
		}
		switch string(k) {
		case "type":
			got |= 1
			t, ok := srcNames[string(v)]
			if !ok {
				return fmt.Errorf("source type %q", v)
			}
			c.src = t
		case "data":
			got |= 2 // the ancestor for INHERITED, "-" otherwise; not used
		default:
			return fmt.Errorf("unknown member %q", k)
		}
	}
	if got != 3 {
		return errors.New("source incomplete")
	}
	return nil
}

// ---- per-row invariants and ownership ---------------------------------------

func (p *parser) v(j int) []byte { c := p.cells[j]; return p.scratch[c.lo:c.hi] }
func (p *parser) dash(j int) bool {
	return string(p.v(j)) == "-" && p.cells[j].src == sNone
}

// num reads a decimal column under -p. The source must be one of srcs.
func (p *parser) num(ds string, j int, srcs ...srcType) (uint64, error) {
	n, ok := decimal(p.v(j))
	if !ok {
		return 0, perr(ds, "%s=%q is not a decimal", p.props[j], p.v(j))
	}
	if !slices.Contains(srcs, p.cells[j].src) {
		return 0, perr(ds, "%s has an impossible source", p.props[j])
	}
	return n, nil
}

// quota-family: 0 iff source default (invariant 6); never inherited (5)
func (p *parser) quota(ds string, j int) (uint64, error) {
	n, err := p.num(ds, j, sDefault, sLocal, sReceived)
	if err != nil {
		return 0, err
	}
	if (n == 0) != (p.cells[j].src == sDefault) && p.cells[j].src != sReceived {
		return 0, perr(ds, "%s=%d with source %d: zero is default and only default", p.props[j], n, p.cells[j].src)
	}
	return n, nil
}

// local reads one of our user properties: set on this dataset, or not at
// all. A literal "-" set locally is a real value (zfs-invariants §4).
func (p *parser) user(k userKey) (string, bool, error) {
	j := cUser0 + int(k)
	c := p.cells[j]
	switch c.src {
	case sLocal:
		return p.arena.keep(p.v(j)), true, nil
	case sNone:
		if !p.dash(j) {
			return "", false, errors.New("unset user property with a value")
		}
	case sInherited, sReceived:
	default:
		return "", false, errors.New("user property with an impossible source")
	}
	return "", false, nil
}

func (p *parser) row(key string, typ kindT) error {
	ds := key
	d, comp := key, ""
	if typ == tSnap {
		sn, err := name.ParseSnapshot(key)
		if err != nil {
			return perr(ds, "%v", err)
		}
		d, comp = sn.Dataset().String(), sn.Component()
	} else if _, err := name.ParseDataset(key); err != nil {
		return perr(ds, "%v", err)
	}
	dd, _ := name.ParseDataset(d)
	rel, under := dd.Rel(p.sc.parent)
	if !under {
		return perr(ds, "outside the parent")
	}
	// invariant 4: readonly values carry no source
	for _, j := range []int{cCreation, cGuid, cOrigin, cClones, cDefer} {
		if p.cells[j].src != sNone {
			return perr(ds, "readonly %s has a source", p.props[j])
		}
	}
	creation, err := p.num(ds, cCreation, sNone)
	if err != nil {
		return err
	}
	guid, err := p.num(ds, cGuid, sNone)
	if err != nil {
		return err
	}
	if typ == tSnap {
		return p.snapRow(ds, rel, comp, creation, guid)
	}
	// invariant 3: type gates "-"
	if !p.dash(cDefer) || !p.dash(cClones) {
		return perr(ds, "snapshot-only property on a %v", typ)
	}
	if rel == "" {
		return nil // the parent itself: validated, not a volume
	}
	vol, _, deeper := strings.Cut(rel, "/")
	if deeper {
		p.children[vol]++
		return nil
	}
	id, err := name.ParseVolID(vol)
	if err != nil {
		if p.dropped == nil {
			p.dropped = map[string]bool{}
		}
		p.dropped[vol] = true // no request can name it; its clones still count on their origins
		return nil
	}
	// id is a substring of key, which the arena already holds
	rv := rawVol{Vol: Vol{id: id, creation: int64(creation), guid: guid}}
	// origin (invariant 8)
	if !p.dash(cOrigin) {
		o := p.v(cOrigin)
		if _, err := name.ParseSnapshot(string(o)); err != nil {
			return perr(ds, "origin %q is not a snapshot", o)
		}
		rv.origin = p.arena.keep(o)
	}
	refres, err := p.quota(ds, cRefres)
	if err != nil {
		return err
	}
	rv.refres = refres
	switch typ {
	case tVol:
		rv.kind = Block
		if !p.dash(cRefquota) {
			return perr(ds, "refquota on a volume")
		}
		size, err := p.num(ds, cVolsize, sLocal, sNone, sReceived)
		if err != nil {
			return err
		}
		bs, err := p.num(ds, cVolbs, sDefault, sNone)
		if err != nil {
			return err
		}
		// invariant 7: volume geometry
		if bs < 512 || bs > 16<<20 || bs&(bs-1) != 0 || size == 0 || size%bs != 0 {
			return perr(ds, "volsize %d / volblocksize %d break zvol.c's rules", size, bs)
		}
		rv.size, rv.bs = size, uint32(bs)
	case tFS:
		rv.kind = FS
		if !p.dash(cVolsize) || !p.dash(cVolbs) {
			return perr(ds, "volume property on a filesystem")
		}
		q, err := p.quota(ds, cRefquota)
		if err != nil {
			return err
		}
		if q > 0 && refres > q { // invariant 9
			return perr(ds, "refreservation %d above refquota %d", refres, q)
		}
		rv.size = q
	}
	if p.scope.local {
		for k := range NLocal {
			j := cLocal0 + int(k)
			applies := typ == tFS || k == Compression || k == Sync || k == Logbias || k == Dedup || k == Copies
			if !applies {
				if !p.dash(j) {
					return perr(ds, "%s on a volume", p.props[j])
				}
				continue
			}
			if p.cells[j].src == sLocal {
				rv.local[k] = p.arena.keep(p.v(j))
			}
		}
	}
	if err := p.classifyVol(ds, &rv); err != nil {
		return err
	}
	p.vols = append(p.vols, rv)
	return nil
}

// classifyVol is the only place a volume's ownership is decided.
func (p *parser) classifyVol(ds string, rv *rawVol) error {
	var val [nUser]string
	var ok [nUser]bool
	for k := range nUser {
		var err error
		if val[k], ok[k], err = p.user(k); err != nil {
			return perr(ds, "%s: %v", p.props[cUser0+int(k)], err)
		}
	}
	m, mOK, c, cOK, vn, vnOK := val[uManaged], ok[uManaged], val[uSuccess], ok[uSuccess], val[uVolName], ok[uVolName]
	st, stOK, sid, sidOK := val[uSrcType], ok[uSrcType], val[uSrcID], ok[uSrcID]
	rv.volName = vn
	switch {
	case !mOK && (cOK || vnOK || stOK || sidOK):
		rv.own = VolInvalid // I1: markers without managed (e.g. `zfs inherit managed_resource`)
		return nil
	case !mOK:
		rv.own = VolForeign
		return nil
	case m != "true":
		rv.own = VolInvalid
		return nil
	case !vnOK:
		rv.own = VolInvalid // I2: nobody writes managed without the name marker
		return nil
	case vn != rv.id.String():
		rv.own = VolForeign // renamed or copied: it is another volume's dataset
		return nil
	}
	switch {
	case !stOK && !sidOK:
		rv.src = SrcNone
	case stOK && sidOK && st == "snapshot" && sid != "":
		rv.src, rv.srcID = SrcSnapshot, sid
	case stOK && sidOK && st == "volume" && sid != "":
		rv.src, rv.srcID = SrcVolume, sid
	default:
		rv.own = VolInvalid // I3: half a source
		return nil
	}
	switch {
	case !cOK:
		rv.own = VolUncommitted
	case c == "true":
		rv.own = VolCommitted
	default:
		rv.own = VolInvalid
	}
	return nil
}

func (p *parser) snapRow(ds, rel, comp string, creation, guid uint64) error {
	if !p.dash(cOrigin) || !p.dash(cVolbs) || !p.dash(cRefquota) || !p.dash(cRefres) {
		return perr(ds, "dataset-only property on a snapshot")
	}
	if rel == "" {
		return nil // a snapshot of the parent itself
	}
	vol := rel
	if strings.Contains(vol, "/") {
		return nil // a snapshot below a volume (beyond the scan depth; ignore)
	}
	rs := rawSnap{Snap: Snap{creation: int64(creation), guid: guid}}
	switch string(p.v(cDefer)) {
	case "on":
		rs.deferred = true
	case "off":
	default:
		return perr(ds, "defer_destroy=%q", p.v(cDefer))
	}
	if !p.dash(cVolsize) {
		n, err := p.num(ds, cVolsize, sNone, sLocal)
		if err != nil {
			return err
		}
		if n == 0 {
			return perr(ds, "zero volsize")
		}
		rs.volsize, rs.hasVolsize = n, true
	}
	if !p.dash(cClones) {
		for c := range strings.SplitSeq(string(p.v(cClones)), ",") {
			if _, err := name.ParseDataset(c); err != nil {
				return perr(ds, "clone %q is not a dataset", c)
			}
			rs.clones++
		}
	}
	// snapshot markers are read now, judged in build once the volume's own
	// ownership is known
	var val [nUser]string
	var ok [nUser]bool
	for k := range nUser {
		var err error
		if val[k], ok[k], err = p.user(k); err != nil {
			return perr(ds, "%s: %v", p.props[cUser0+int(k)], err)
		}
	}
	rs.comp, rs.volID = comp, vol // substrings of the arena-held key
	rs.own = classifySnapMarkers(vol, comp, rs.deferred, val, ok)
	sn := val[uSnapName]
	if rs.own == SnapCSI || rs.own == SnapCSIDeferred {
		rs.csiName = sn
	}
	p.snaps = append(p.snaps, rs)
	return nil
}

// classifySnapMarkers is the snapshot half of ownership; build downgrades
// every snapshot of a volume that is not ours to SnapForeign.
//
// A clone source is ours only by its stamp (OWN-13): managed_resource=true
// and clone_source_for=<the VolID its name ends in>, both set locally by
// the `zfs snapshot -o` that cut it. The reserved name alone is anyone's
// (an administrator, sanoid with that prefix, democratic-csi's unstamped
// cuts): SnapForeign, never destroyed or released by us.
func classifySnapMarkers(vol, comp string, deferred bool, val [nUser]string, ok [nUser]bool) SnapOwn {
	m, mOK := val[uManaged], ok[uManaged]
	sn, snOK := val[uSnapName], ok[uSnapName]
	ss, ssOK := val[uSnapSrc], ok[uSnapSrc]
	cf, cfOK := val[uCloneFor], ok[uCloneFor]
	suffix, reserved := strings.CutPrefix(comp, CloneSourcePrefix)
	switch {
	case !mOK && (snOK || ssOK || cfOK):
		return SnapInvalid // I8: a marker without managed_resource
	case !mOK:
		return SnapForeign // including an unstamped reserved name
	case m != "true":
		return SnapInvalid
	case reserved:
		if snOK || ssOK || !cfOK || cf != suffix {
			return SnapInvalid // half-stamped, or stamped for another clone
		}
		if _, err := name.ParseVolID(suffix); err != nil {
			return SnapInvalid
		}
		return SnapCloneSource
	case cfOK, !snOK || sn == "":
		return SnapInvalid // I9, I10
	case ssOK && ss != vol:
		return SnapForeign // a renamed or copied snapshot: another volume's
	case deferred:
		return SnapCSIDeferred
	}
	return SnapCSI
}

// ---- build ---------------------------------------------------------------

func (p *parser) build() (*World, error) {
	w := &World{parent: p.sc.parent, prefix: p.sc.prefix, scope: p.scope, lease: p.lease, dropped: len(p.dropped)}
	cmpVol := func(a, b rawVol) int { return strings.Compare(a.id.String(), b.id.String()) }
	if !slices.IsSortedFunc(p.vols, cmpVol) {
		slices.SortFunc(p.vols, cmpVol)
	}
	w.vols = make([]Vol, len(p.vols))
	if p.scope.local {
		w.local = make([]Local, len(p.vols))
	}
	for i := range p.vols {
		if i > 0 && p.vols[i].id == p.vols[i-1].id {
			return nil, perr(p.vols[i].id.String(), "printed twice")
		}
		w.vols[i] = p.vols[i].Vol
		if w.local != nil {
			w.local[i] = p.vols[i].local
		}
	}
	for v, n := range p.children {
		if i, ok := w.findStr(v); ok {
			w.vols[i].children = uint16(min(n, 1<<16-1))
		}
	}
	// resolve each snapshot's volume (zfs prints a dataset right before its
	// snapshots, so the last hit almost always matches)
	last := -1
	keep := p.snaps[:0]
	for _, s := range p.snaps {
		if last < 0 || w.vols[last].id.String() != s.volID {
			i, ok := w.findStr(s.volID)
			if !ok {
				if p.dropped[s.volID] {
					continue
				}
				return nil, perr(s.volID+"@"+s.comp, "snapshot without its dataset")
			}
			last = i
		}
		v := &w.vols[last]
		// cross-row invariant: snapshot volsize is "-" iff its head is a filesystem
		if s.hasVolsize != (v.kind == Block) {
			return nil, perr(s.volID+"@"+s.comp, "volsize disagrees with its head's type")
		}
		if v.own != VolUncommitted && v.own != VolCommitted {
			s.own, s.csiName = SnapForeign, ""
		}
		s.vol = uint32(last)
		keep = append(keep, s)
	}
	cmpSnap := func(a, b rawSnap) int {
		if a.vol != b.vol {
			return int(a.vol) - int(b.vol)
		}
		return strings.Compare(a.comp, b.comp)
	}
	if !slices.IsSortedFunc(keep, cmpSnap) {
		slices.SortFunc(keep, cmpSnap)
	}
	w.snaps = make([]Snap, len(keep))
	for i, s := range keep {
		if i > 0 && cmpSnap(keep[i-1], s) == 0 {
			return nil, perr(s.volID+"@"+s.comp, "printed twice")
		}
		w.snaps[i] = s.Snap
		v := &w.vols[s.vol]
		if v.snapHi == 0 {
			v.snapLo = uint32(i)
		}
		v.snapHi = uint32(i + 1)
	}
	return w, nil
}

func (w *World) findStr(id string) (int, bool) {
	return slices.BinarySearchFunc(w.vols, id, func(v Vol, s string) int { return strings.Compare(v.id.String(), s) })
}

// decimal parses a -p number strictly: digits only, no leading zero, no sign.
func decimal(b []byte) (uint64, bool) {
	if len(b) == 0 || len(b) > 20 || (b[0] == '0' && len(b) > 1) {
		return 0, false
	}
	var n uint64
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, false
		}
		d := uint64(c - '0')
		if n > (1<<64-1-d)/10 {
			return 0, false
		}
		n = n*10 + d
	}
	return n, true
}

// arena holds every retained string of one world in a few large blocks.
type arena struct{ cur []byte }

func (a *arena) keep(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if cap(a.cur)-len(a.cur) < len(b) {
		a.cur = make([]byte, 0, max(256<<10, len(b)))
	}
	n := len(a.cur)
	a.cur = append(a.cur, b...)
	s := a.cur[n:]
	return unsafe.String(&s[0], len(s))
}
