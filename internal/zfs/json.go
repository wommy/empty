package zfs

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unsafe"
)

// decodeGetJSON decodes the stdout of `zfs get -j -p <props> ...` from
// OpenZFS 2.4 (output_version 0.1) into Columns, strictly:
//
//   - the document is exactly {"output_version":{...},"datasets":{...}}
//     followed by whitespace and EOF; anything else, including a truncated
//     document, is an error;
//   - every dataset carries name, type, pool, createtxg and properties, plus
//     dataset and snapshot_name exactly when it is a snapshot, and they agree
//     with the dataset's key, and no key repeats at any level;
//   - every requested property is present exactly once per dataset, nothing
//     else is, and each is {"value": string, "source": {"type", "data"}};
//   - unknown members, repeated names, invalid UTF-8 and non-string values
//     are errors.
//
// Nothing is returned unless the whole document passes. The shape comes from
// cmd/zfs/zfs_main.c (zfs_json_schema, fill_dataset_info, get_callback) and
// lib/libzfs/libzfs_util.c (zprop_nvlist_one_property) in OpenZFS 2.4.
//
// Cells use the text-mode spelling of the source column (local, default,
// inherited from X, received, temporary, -), as zprop_print_one_property
// prints it with -Hp, so Prop.Local and the Table read the same strings.
//
// The printer escapes every string, so no value can end a member, an
// object or a line: a value that would forge rows in `-Hp` text is here
// only the value it is. It is lossy in two ways no decoder can see past:
// code points above U+FFFF are dropped, and a value its locale cannot
// decode (invalid UTF-8; under LC_ALL=C any byte >= 0x80) stops the
// printer mid-document with exit 0. The second is always a syntax error
// here, because the top-level object is closed only at the very end.
func decodeGetJSON(r io.Reader, props []string) (*Columns, error) {
	s := &jsonScan{
		// Invalid UTF-8 stays an error (the default). jsontext's generic
		// duplicate-name check costs a namespace per object (13% of CPU);
		// every object below checks its own members instead.
		dec:   jsontext.NewDecoder(r, jsontext.AllowDuplicateNames(true)),
		names: map[string]struct{}{},
		props: props,
		col:   make(map[string]int, len(props)),
		c:     &Columns{Props: props},
	}
	for j, p := range props {
		if p == "" || p == "all" {
			return nil, fmt.Errorf("json scan: property %q cannot be checked strictly", p)
		}
		if _, dup := s.col[p]; dup {
			return nil, fmt.Errorf("json scan: property %q requested twice", p)
		}
		s.col[p] = j
	}
	s.seen = make([]bool, len(props))
	if err := s.document(); err != nil {
		return nil, fmt.Errorf("json scan at byte %d: %w", s.dec.InputOffset(), err)
	}
	return s.c, nil
}

type jsonScan struct {
	dec   *jsontext.Decoder
	props []string
	col   map[string]int
	seen  []bool
	c     *Columns
	names map[string]struct{} // dataset keys seen
	buf   []byte              // unquote scratch
	arena []byte              // backing store for retained strings; never rewritten
}

var errSchema = errors.New("json scan: unexpected document shape")

func (s *jsonScan) document() error {
	if err := s.delim('{'); err != nil {
		return err
	}
	var haveVersion, haveDatasets bool
	for {
		k, end, err := s.member()
		if err != nil {
			return err
		}
		if end {
			break
		}
		switch string(k) {
		case "output_version":
			if haveVersion {
				return fmt.Errorf("%w: output_version twice", errSchema)
			}
			haveVersion = true
			err = s.version()
		case "datasets":
			if haveDatasets {
				return fmt.Errorf("%w: datasets twice", errSchema)
			}
			haveDatasets = true
			err = s.datasets()
		default:
			err = fmt.Errorf("%w: unknown member %q", errSchema, k)
		}
		if err != nil {
			return err
		}
	}
	if !haveVersion || !haveDatasets {
		return fmt.Errorf("%w: output_version or datasets missing", errSchema)
	}
	// one document, then EOF: zfs writes the object and a newline
	if _, err := s.dec.ReadToken(); err != io.EOF {
		if err == nil {
			err = errors.New("trailing data after document")
		}
		return err
	}
	return nil
}

// version accepts exactly {"command":"zfs get","vers_major":0,"vers_minor":1}.
func (s *jsonScan) version() error {
	if err := s.delim('{'); err != nil {
		return err
	}
	var got uint8
	for {
		k, end, err := s.member()
		if err != nil {
			return err
		}
		if end {
			break
		}
		var key, want string
		var bit uint8
		var v []byte
		switch string(k) {
		case "command":
			key, bit, want = "command", 1, "zfs get"
			v, err = s.str()
		case "vers_major":
			key, bit, want = "vers_major", 2, "0"
			v, err = s.number()
		case "vers_minor":
			key, bit, want = "vers_minor", 4, "1"
			v, err = s.number()
		default:
			return fmt.Errorf("%w: unknown output_version member %q", errSchema, k)
		}
		if err != nil {
			return err
		}
		if string(v) != want || got&bit != 0 {
			return fmt.Errorf("output_version %s = %s, want %s once", key, v, want)
		}
		got |= bit
	}
	if got != 7 {
		return fmt.Errorf("%w: output_version incomplete", errSchema)
	}
	return nil
}

func (s *jsonScan) datasets() error {
	if err := s.delim('{'); err != nil {
		return err
	}
	for {
		k, end, err := s.member()
		if err != nil {
			return err
		}
		if end {
			return nil
		}
		if _, dup := s.names[string(k)]; dup {
			return fmt.Errorf("dataset %q twice", k)
		}
		name := s.keep(k)
		s.names[name] = struct{}{}
		if err := s.dataset(name); err != nil {
			return fmt.Errorf("dataset %q: %w", name, err)
		}
	}
}

const (
	fName = 1 << iota
	fType
	fPool
	fTxg
	fDataset
	fSnapName
	fProps
)

func (s *jsonScan) dataset(name string) error {
	if err := s.delim('{'); err != nil {
		return err
	}
	ds, snap, isSnap := cutAt(name)
	pool, _, _ := strings.Cut(ds, "/")
	var got int
	var typ string
	for {
		k, end, err := s.member()
		if err != nil {
			return err
		}
		if end {
			break
		}
		var bit int
		var v []byte
		switch string(k) {
		case "name":
			bit = fName
			if v, err = s.str(); err == nil && string(v) != name {
				err = fmt.Errorf("name %q does not match its key", v)
			}
		case "type":
			bit = fType
			if v, err = s.str(); err == nil {
				switch string(v) {
				case "FILESYSTEM":
					typ = string(TypeFilesystem)
				case "VOLUME":
					typ = string(TypeVolume)
				case "SNAPSHOT":
					typ = string(TypeSnapshot)
				default:
					err = fmt.Errorf("unexpected type %q", v)
				}
			}
		case "pool":
			bit = fPool
			if v, err = s.str(); err == nil && string(v) != pool {
				err = fmt.Errorf("pool %q does not match name", v)
			}
		case "createtxg":
			bit = fTxg
			if v, err = s.str(); err == nil && !digits(v) {
				err = fmt.Errorf("createtxg %q is not a number", v)
			}
		case "dataset":
			bit = fDataset
			if v, err = s.str(); err == nil && (!isSnap || string(v) != ds) {
				err = fmt.Errorf("dataset %q does not match name", v)
			}
		case "snapshot_name":
			bit = fSnapName
			if v, err = s.str(); err == nil && (!isSnap || string(v) != snap) {
				err = fmt.Errorf("snapshot_name %q does not match name", v)
			}
		case "properties":
			bit = fProps
			err = s.properties()
		default:
			err = fmt.Errorf("%w: unknown member %q", errSchema, k)
		}
		if err != nil {
			return err
		}
		if got&bit != 0 {
			return fmt.Errorf("%w: member repeated", errSchema)
		}
		got |= bit
	}
	want := fName | fType | fPool | fTxg | fProps
	if isSnap {
		want |= fDataset | fSnapName
	}
	if got != want {
		return fmt.Errorf("%w: members %07b, want %07b", errSchema, got, want)
	}
	if isSnap != (typ == string(TypeSnapshot)) {
		return fmt.Errorf("type %s does not match name", typ)
	}
	// the row was appended by properties(); cross-check a fetched type
	if j, ok := s.col["type"]; ok {
		if v := s.c.Cells[len(s.c.Cells)-len(s.props)+j].Value; v != typ {
			return fmt.Errorf("property type %q, dataset type %q", v, typ)
		}
	}
	s.c.Names = append(s.c.Names, name)
	return nil
}

func (s *jsonScan) properties() error {
	if err := s.delim('{'); err != nil {
		return err
	}
	clear(s.seen)
	base := len(s.c.Cells)
	if cap(s.c.Cells)-base < len(s.props) {
		// double: append's 1.25x growth for large slices copied the
		// pointerful Cells about 4 times over (9% of CPU at 10k)
		s.c.Cells = slices.Grow(s.c.Cells, max(base, 4*len(s.props)))
	}
	s.c.Cells = s.c.Cells[:base+len(s.props)]
	for {
		k, end, err := s.member()
		if err != nil {
			return err
		}
		if end {
			break
		}
		j, ok := s.col[string(k)]
		if !ok {
			return fmt.Errorf("property %q was not requested", k)
		}
		if s.seen[j] {
			return fmt.Errorf("property %q twice", s.props[j])
		}
		s.seen[j] = true
		p, err := s.prop()
		if err != nil {
			return fmt.Errorf("property %q: %w", s.props[j], err)
		}
		s.c.Cells[base+j] = p
	}
	for j, ok := range s.seen {
		if !ok {
			return fmt.Errorf("property %q missing", s.props[j])
		}
	}
	return nil
}

// prop reads {"value": string, "source": {"type": T, "data": D}} and spells
// the source the way zprop_print_one_property does in text mode.
func (s *jsonScan) prop() (Prop, error) {
	if err := s.delim('{'); err != nil {
		return Prop{}, err
	}
	var p Prop
	var got uint8
	for {
		k, end, err := s.member()
		if err != nil {
			return p, err
		}
		if end {
			break
		}
		switch string(k) {
		case "value":
			if got&1 != 0 {
				return p, fmt.Errorf("%w: value twice", errSchema)
			}
			got |= 1
			v, err := s.str()
			if err != nil {
				return p, err
			}
			p.Value = s.keep(v)
		case "source":
			if got&2 != 0 {
				return p, fmt.Errorf("%w: source twice", errSchema)
			}
			got |= 2
			if p.Source, err = s.source(); err != nil {
				return p, err
			}
		default:
			return p, fmt.Errorf("%w: unknown member %q", errSchema, k)
		}
	}
	if got != 3 {
		return p, fmt.Errorf("%w: value or source missing", errSchema)
	}
	return p, nil
}

func (s *jsonScan) source() (string, error) {
	if err := s.delim('{'); err != nil {
		return "", err
	}
	var typ, data string
	var got uint8
	for {
		k, end, err := s.member()
		if err != nil {
			return "", err
		}
		if end {
			break
		}
		// k is only valid until the next read: classify it first
		isType := string(k) == "type"
		if !isType && string(k) != "data" {
			return "", fmt.Errorf("%w: unknown member %q", errSchema, k)
		}
		v, err := s.str()
		if err != nil {
			return "", err
		}
		bit := uint8(2)
		if isType {
			bit = 1
		}
		if got&bit != 0 {
			return "", fmt.Errorf("%w: source member repeated", errSchema)
		}
		switch {
		case isType:
			got |= 1
			switch string(v) {
			case "NONE":
				typ = "-"
			case "DEFAULT":
				typ = "default"
			case "LOCAL":
				typ = "local"
			case "TEMPORARY":
				typ = "temporary"
			case "RECEIVED":
				typ = "received"
			case "INHERITED":
				typ = "inherited"
			default:
				return "", fmt.Errorf("unknown source type %q", v)
			}
		default:
			got |= 2
			data = "-"
			if string(v) != "-" {
				data = s.keepPrefixed("inherited from ", v)
			}
		}
	}
	if got != 3 {
		return "", fmt.Errorf("%w: source type or data missing", errSchema)
	}
	// data is "-" for everything but INHERITED (RECEIVED carries the
	// received value only when -o received is asked for, which we never do)
	if typ == "inherited" {
		if data == "-" || data == "inherited from " {
			return "", errors.New("inherited source without a dataset")
		}
		return data, nil
	}
	if data != "-" {
		return "", fmt.Errorf("source %s carries data %q", typ, data)
	}
	return typ, nil
}

// member reads the next object member name, or the closing '}' (end=true).
// The returned bytes are valid until the next read.
func (s *jsonScan) member() (k []byte, end bool, err error) {
	if s.dec.PeekKind() == '}' {
		_, err := s.dec.ReadToken()
		return nil, true, err
	}
	k, err = s.str()
	return k, false, err
}

func (s *jsonScan) delim(k jsontext.Kind) error {
	t, err := s.dec.ReadToken()
	if err != nil {
		return err
	}
	if t.Kind() != k {
		return fmt.Errorf("%w: got %v, want %v", errSchema, t.Kind(), k)
	}
	return nil
}

// str reads a string value and returns it unescaped, valid until the next read.
func (s *jsonScan) str() ([]byte, error) {
	v, err := s.dec.ReadValue()
	if err != nil {
		return nil, err
	}
	if v.Kind() != '"' {
		return nil, fmt.Errorf("%w: got %v, want string", errSchema, v.Kind())
	}
	if bytes.IndexByte(v, '\\') < 0 {
		return v[1 : len(v)-1], nil
	}
	s.buf, err = jsontext.AppendUnquote(s.buf[:0], v)
	return s.buf, err
}

func (s *jsonScan) number() ([]byte, error) {
	v, err := s.dec.ReadValue()
	if err != nil {
		return nil, err
	}
	if v.Kind() != '0' {
		return nil, fmt.Errorf("%w: got %v, want number", errSchema, v.Kind())
	}
	return v, nil
}

// keep copies b into the arena. Strings handed out are never overwritten,
// so the unsafe.String is immutable for its whole life.
func (s *jsonScan) keep(b []byte) string {
	switch string(b) {
	case "":
		return ""
	case "-":
		return "-"
	}
	return s.keepPrefixed("", b)
}

func (s *jsonScan) keepPrefixed(prefix string, b []byte) string {
	n := len(prefix) + len(b)
	if n == 0 {
		return ""
	}
	if cap(s.arena)-len(s.arena) < n {
		// chunks double from 4 KiB to 256 KiB: a one-volume scan stays
		// small, a whole-parent scan makes a few dozen allocations
		s.arena = make([]byte, 0, max(n, min(256<<10, 2*cap(s.arena)), 4<<10))
	}
	start := len(s.arena)
	s.arena = append(append(s.arena, prefix...), b...)
	return unsafe.String(&s.arena[start], n)
}

func cutAt(name string) (ds, snap string, ok bool) {
	for i := 0; i < len(name); i++ {
		if name[i] == '@' {
			return name[:i], name[i+1:], true
		}
	}
	return name, "", false
}

func digits(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for _, c := range b {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
