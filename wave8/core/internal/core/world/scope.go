package world

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/wommy/empty/internal/core/name"
)

// LocalKey indexes the allowlisted native properties a create request may
// set (props.go's allowlist). volblocksize is not here: it is Vol.bs.
type LocalKey uint8

const (
	Compression LocalKey = iota
	Recordsize
	Atime
	Relatime
	Sync
	Logbias
	Dedup
	Xattr
	ACLType
	Snapdir
	Copies
	NLocal
)

// LocalNames are the property names, by LocalKey.
var LocalNames = [NLocal]string{"compression", "recordsize", "atime", "relatime", "sync", "logbias", "dedup", "xattr", "acltype", "snapdir", "copies"}

// Local holds the allowlisted properties set locally ("" = not local).
type Local [NLocal]string

// Scope is what a scan read: the whole parent, or the subtrees of some
// volumes; and whether the cold Local columns were fetched. A world can say
// "absent" only for what its scope covers.
type Scope struct {
	all   bool
	vols  []name.VolID // sorted, unique
	local bool
}

// All scopes the whole parent.
func All(local bool) Scope { return Scope{all: true, local: local} }

// Vols scopes the given volumes' subtrees.
func Vols(local bool, ids ...name.VolID) Scope {
	ids = slices.Clone(ids)
	slices.SortFunc(ids, func(a, b name.VolID) int { return strings.Compare(a.String(), b.String()) })
	ids = slices.Compact(ids)
	return Scope{vols: ids, local: local}
}

// IsAll reports whole-parent coverage.
func (s Scope) IsAll() bool { return s.all }

// Local reports whether the Local columns are fetched.
func (s Scope) Local() bool { return s.local }

// VolIDs are the scoped volumes (nil for All).
func (s Scope) VolIDs() []name.VolID { return s.vols }

// Covers reports whether absence of id in a world of this scope is real.
func (s Scope) Covers(id name.VolID) bool {
	if s.all {
		return true
	}
	_, ok := slices.BinarySearchFunc(s.vols, id, func(a, b name.VolID) int { return strings.Compare(a.String(), b.String()) })
	return ok
}

// Includes reports whether s reads at least what need reads.
func (s Scope) Includes(need Scope) bool {
	if need.local && !s.local {
		return false
	}
	if need.all {
		return s.all
	}
	for _, id := range need.vols {
		if !s.Covers(id) {
			return false
		}
	}
	return true
}

// Schema names the properties a scan requests, under one prefix.
type Schema struct {
	parent name.Dataset
	prefix string
	user   [nUser]string
}

type userKey uint8

const (
	uManaged userKey = iota
	uSuccess
	uVolName
	uSrcType
	uSrcID
	uSnapName
	uSnapSrc
	uCloneFor
	nUser
)

var userSuffix = [nUser]string{":managed_resource", ":provision_success", ":csi_volume_name",
	":csi_volume_content_source_type", ":csi_volume_content_source_id", ":csi_snapshot_name", ":csi_snapshot_source_volume_id", ":clone_source_for"}

// native properties every scan reads (type comes from the dataset header)
var coreNative = []string{"creation", "guid", "origin", "clones", "volsize", "volblocksize", "refquota", "refreservation", "defer_destroy"}

// NewSchema validates the prefix (zfs_prop_user: a-z 0-9 - _ . :, < 256).
func NewSchema(parent name.Dataset, prefix string) (*Schema, error) {
	if prefix == "" {
		return nil, errors.New("empty prefix")
	}
	for i := 0; i < len(prefix); i++ {
		c := prefix[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte("-_.:", c) >= 0) {
			return nil, fmt.Errorf("prefix: %q is not allowed in a zfs user property", c)
		}
	}
	sc := &Schema{parent: parent, prefix: prefix}
	for k, s := range userSuffix {
		sc.user[k] = prefix + s
		if len(sc.user[k]) > 255 {
			return nil, errors.New("prefix too long")
		}
	}
	return sc, nil
}

// Parent is the schema's parent dataset.
func (sc *Schema) Parent() name.Dataset { return sc.parent }

// Prop returns the full user-property name for a marker suffix such as
// ":provision_success" (for op constructors).
func (sc *Schema) Prop(suffix string) string { return sc.prefix + suffix }

// Props is the exact, ordered property list a scan of scope s requests.
// Parse demands each of them exactly once per dataset, and nothing else.
func (sc *Schema) Props(s Scope) []string {
	p := append([]string{}, coreNative...)
	p = append(p, sc.user[:]...)
	if s.local {
		p = append(p, LocalNames[:]...)
	}
	return p
}

// Argv is the `zfs get` argument list for one scan document of scope s:
// one for All, one per volume otherwise (a missing volume must fail alone).
func (sc *Schema) Argv(s Scope) [][]string {
	props := strings.Join(sc.Props(s), ",")
	base := []string{"get", "-j", "-p", "-t", "filesystem,volume,snapshot"}
	if s.all {
		return [][]string{append(base, "-d", "2", props, sc.parent.String())}
	}
	var out [][]string
	for _, id := range s.vols {
		d, err := sc.parent.Child(id)
		if err != nil {
			continue // cannot exist: covered and absent
		}
		out = append(out, append(slices.Clone(base), "-d", "1", props, d.String()))
	}
	return out
}
