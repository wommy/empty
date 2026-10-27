// Package name holds the typed names of the core: what OpenZFS accepts as a
// dataset or snapshot name (the kernel grammar, zfs_namecheck.c), and the
// stricter subset the endpoint issues as volume and snapshot ids.
//
// Every type is a struct with an unexported field, so a value exists only if
// a parser in this package accepted it. Downstream code never re-validates.
package name

import (
	"errors"
	"strings"
)

// Limits from OpenZFS 2.4 (include/sys/fs/zfs.h, zfs_namecheck.c) and CSI.
const (
	MaxName  = 255 // ZFS_MAX_DATASET_NAME_LEN 256, strictly less
	MaxDepth = 49  // zfs_max_dataset_nesting 50, strictly less
	MaxPool  = 239 // pool_namecheck: under 240 bytes
	MaxID    = 128 // CSI: volume and snapshot ids
)

// Dataset is a filesystem or volume name: kernel grammar, no '@' or '#'.
type Dataset struct{ s string }

// Snapshot is "<dataset>@<component>": kernel grammar.
type Snapshot struct {
	s  string
	at int // index of '@'
}

// VolID is a volume id the endpoint issues: one dataset component directly
// under the parent, 1..128 bytes of [A-Za-z0-9_.:-], starting alphanumeric
// (never read as a flag or a hidden name), never "." or "..".
type VolID struct{ s string }

// SnapID is "<VolID>@<component>", at most 128 bytes in all.
type SnapID struct {
	vol  VolID
	snap string
}

func (d Dataset) String() string  { return d.s }
func (s Snapshot) String() string { return s.s }
func (v VolID) String() string    { return v.s }
func (s SnapID) String() string   { return s.vol.s + "@" + s.snap }

// IsZero reports whether d is the zero value (no name).
func (d Dataset) IsZero() bool { return d.s == "" }
func (v VolID) IsZero() bool   { return v.s == "" }

// IsZero reports whether s is the zero value.
func (s Snapshot) IsZero() bool { return s.s == "" }

// Dataset returns the snapshot's dataset; Component the part after '@'.
func (s Snapshot) Dataset() Dataset  { return Dataset{s.s[:s.at]} }
func (s Snapshot) Component() string { return s.s[s.at+1:] }
func (s SnapID) Vol() VolID          { return s.vol }
func (s SnapID) Component() string   { return s.snap }

var (
	ErrEmpty     = errors.New("empty name")
	ErrTooLong   = errors.New("name too long")
	ErrChar      = errors.New("invalid character")
	ErrComponent = errors.New("empty, '.' or '..' component")
	ErrDelimiter = errors.New("wrong '@'/'#' delimiters for this type")
	ErrPool      = errors.New("invalid pool name")
	ErrDepth     = errors.New("nesting too deep")
	ErrID        = errors.New("not an id this endpoint issues")
)

// kernelChar is valid_char (zfs_namecheck.c:49-56) plus '%', which
// entity_namecheck allows for internal names (zfs recv's %recv clones).
func kernelChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '-' || c == '_' || c == '.' || c == ':' || c == ' ' || c == '%'
}

func idChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '-' || c == '_' || c == '.' || c == ':'
}

func component(c string) error {
	if c == "" || c == "." || c == ".." {
		return ErrComponent
	}
	for i := 0; i < len(c); i++ {
		if !kernelChar(c[i]) {
			return ErrChar
		}
	}
	return nil
}

// ParseDataset accepts what OpenZFS accepts as a filesystem or volume name.
func ParseDataset(s string) (Dataset, error) {
	if s == "" {
		return Dataset{}, ErrEmpty
	}
	if len(s) > MaxName {
		return Dataset{}, ErrTooLong
	}
	if strings.ContainsAny(s, "@#") {
		return Dataset{}, ErrDelimiter
	}
	parts := strings.Split(s, "/")
	if len(parts)-1 > MaxDepth {
		return Dataset{}, ErrDepth
	}
	for _, p := range parts {
		if err := component(p); err != nil {
			return Dataset{}, err
		}
	}
	pool := parts[0]
	c := pool[0]
	if len(pool) > MaxPool || !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') ||
		strings.HasPrefix(pool, "mirror") || strings.HasPrefix(pool, "raidz") || strings.HasPrefix(pool, "draid") ||
		strings.ContainsRune(pool, '%') {
		return Dataset{}, ErrPool
	}
	return Dataset{s}, nil
}

// ParseSnapshot accepts what OpenZFS accepts as a snapshot name.
func ParseSnapshot(s string) (Snapshot, error) {
	at := strings.IndexByte(s, '@')
	if at < 0 || strings.Count(s, "@") != 1 || strings.ContainsRune(s, '#') {
		return Snapshot{}, ErrDelimiter
	}
	if len(s) > MaxName {
		return Snapshot{}, ErrTooLong
	}
	if _, err := ParseDataset(s[:at]); err != nil {
		return Snapshot{}, err
	}
	if c := s[at+1:]; strings.ContainsRune(c, '/') {
		return Snapshot{}, ErrDelimiter
	} else if err := component(c); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{s, at}, nil
}

// ParseVolID accepts the ids the endpoint issues. Anything else names no
// volume of ours (CSI: NOT_FOUND, or OK on delete).
func ParseVolID(s string) (VolID, error) {
	if err := idComponent(s); err != nil {
		return VolID{}, err
	}
	return VolID{s}, nil
}

func idComponent(s string) error {
	if s == "" {
		return ErrEmpty
	}
	if len(s) > MaxID {
		return ErrTooLong
	}
	if c := s[0]; !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
		return ErrID
	}
	for i := 0; i < len(s); i++ {
		if !idChar(s[i]) {
			return ErrID
		}
	}
	return nil
}

// ParseSnapID accepts "<VolID>@<component>" within 128 bytes.
func ParseSnapID(s string) (SnapID, error) {
	vol, snap, ok := strings.Cut(s, "@")
	if !ok || strings.ContainsAny(snap, "@/#") {
		return SnapID{}, ErrDelimiter
	}
	if len(s) > MaxID {
		return SnapID{}, ErrTooLong
	}
	v, err := ParseVolID(vol)
	if err != nil {
		return SnapID{}, err
	}
	if err := idComponent(snap); err != nil {
		return SnapID{}, err
	}
	return SnapID{v, snap}, nil
}

// Child is parent/id, if it fits OpenZFS's limits.
func (d Dataset) Child(id VolID) (Dataset, error) {
	s := d.s + "/" + id.s
	if len(s) > MaxName {
		return Dataset{}, ErrTooLong
	}
	if strings.Count(s, "/") > MaxDepth {
		return Dataset{}, ErrDepth
	}
	return Dataset{s}, nil
}

// Snap is d@c, if c is a valid component and the result fits.
func (d Dataset) Snap(c string) (Snapshot, error) {
	return ParseSnapshot(d.s + "@" + c)
}

// RelVol returns the VolID of a dataset directly under parent, the
// relative path for anything deeper, and whether d is under parent at all.
// rel is "" for the parent itself.
func (d Dataset) Rel(parent Dataset) (rel string, ok bool) {
	if d.s == parent.s {
		return "", true
	}
	if strings.HasPrefix(d.s, parent.s+"/") {
		return d.s[len(parent.s)+1:], true
	}
	return "", false
}
