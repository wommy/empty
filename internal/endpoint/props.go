package endpoint

import (
	"slices"
	"strconv"
	"strings"
)

// Property passthrough (StorageClass parameters) is default-deny: a caller
// can set only the native ZFS properties below, per volume kind, with
// validated values. Everything else is InvalidArgument, including user
// properties (anything with ':'), which could otherwise forge the
// ownership markers, and every property that touches mounts, exports,
// encryption keys or the endpoint's capacity model
// (docs/research/gaps/adv-csi-frontend.md, B3).
//
// Values are canonicalised to what `zfs get -Hp` prints back, so a
// retry's compatibility check is a plain string compare against the row.

// propKey indexes the allowlisted properties that live in a Row's Local
// columns. volblocksize is not among them: it is create-only and zfs never
// reports it as locally set (dsl_prop_nvlist_add_uint64 gives source "-"),
// so it is compared against Row.BlockSz instead.
type propKey uint8

const (
	propCompression propKey = iota
	propRecordsize
	propAtime
	propRelatime
	propSync
	propLogbias
	propDedup
	propXattr
	propACLType
	propSnapdir
	propCopies
	nPropKeys
)

// LocalProps holds the allowlisted properties set locally on a dataset, by
// propKey; "" means not set locally (inherited or default).
type LocalProps [nPropKeys]string

const volblocksizeProp = "volblocksize"

var propNames = [nPropKeys]string{
	propCompression: "compression",
	propRecordsize:  "recordsize",
	propAtime:       "atime",
	propRelatime:    "relatime",
	propSync:        "sync",
	propLogbias:     "logbias",
	propDedup:       "dedup",
	propXattr:       "xattr",
	propACLType:     "acltype",
	propSnapdir:     "snapdir",
	propCopies:      "copies",
}

func propKeyOf(name string) (propKey, bool) {
	i := slices.Index(propNames[:], name)
	return propKey(i), i >= 0
}

// propRule validates one property's value and returns its canonical form.
type propRule struct {
	fs, zvol bool
	canon    func(string) (string, bool)
}

// index is an enumerated property: input -> what zfs get prints (the first
// name zfs_prop.c's table has for that value).
func index(m map[string]string) func(string) (string, bool) {
	return func(v string) (string, bool) { c, ok := m[v]; return c, ok }
}

func same(vs ...string) map[string]string {
	m := make(map[string]string, len(vs))
	for _, v := range vs {
		m[v] = v
	}
	return m
}

var (
	onOff       = same("on", "off")
	compression = func() map[string]string {
		m := same("on", "off", "lzjb", "gzip", "zle", "lz4", "zstd", "zstd-fast")
		for i := 1; i <= 9; i++ {
			m["gzip-"+strconv.Itoa(i)] = "gzip-" + strconv.Itoa(i)
		}
		m["gzip-6"] = "gzip" // zfs_prop.c: "gzip" is listed first for level 6
		for i := 1; i <= 19; i++ {
			m["zstd-"+strconv.Itoa(i)] = "zstd-" + strconv.Itoa(i)
		}
		for _, i := range []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 20, 30, 40, 50, 60, 70, 80, 90, 100, 500, 1000} {
			m["zstd-fast-"+strconv.Itoa(i)] = "zstd-fast-" + strconv.Itoa(i)
		}
		m["zstd-fast-1"] = "zstd-fast" // ZIO_ZSTD_LEVEL_FAST_DEFAULT
		return m
	}()
)

var propRules = map[string]propRule{
	"compression": {fs: true, zvol: true, canon: index(compression)},
	"recordsize":  {fs: true, canon: blockSize(512, 16<<20)},
	"atime":       {fs: true, canon: index(onOff)},
	"relatime":    {fs: true, canon: index(onOff)},
	"sync":        {fs: true, zvol: true, canon: index(same("standard", "always", "disabled"))},
	"logbias":     {fs: true, zvol: true, canon: index(same("latency", "throughput"))},
	"dedup":       {fs: true, zvol: true, canon: index(same("off", "on", "verify"))},
	// xattr: only sa and off. "on" is ZFS_XATTR_DIR in 2.2's xattr_table
	// but ZFS_XATTR_SA from 2.4, and 2.2 prints dir as "on" (the first name
	// for the index), so on and dir neither mean nor read back the same
	// across the userlands this endpoint runs on (zfs_prop.c, 2.2.9 and
	// 2.4.0). Accepting them would need a userland version probe.
	"xattr":   {fs: true, canon: index(same("sa", "off"))},
	"acltype": {fs: true, canon: index(map[string]string{"off": "off", "posix": "posix", "nfsv4": "nfsv4", "disabled": "off", "noacl": "off", "posixacl": "posix"})},
	// snapdir=disabled exists only from 2.4 (2.2's snapdir_table has hidden
	// and visible); without a version probe it is refused everywhere
	"snapdir": {fs: true, canon: index(same("hidden", "visible"))},
	// copies only on zvols: on a filesystem the extra copies count against
	// refquota (zfsprops.7, copies), so copies=2 would silently halve the
	// capacity; a zvol's refreservation=auto already scales by copies
	// (zvol_volsize_to_reservation)
	"copies":         {zvol: true, canon: index(same("1", "2", "3"))},
	volblocksizeProp: {zvol: true, canon: blockSize(512, 128<<10)},
}

// denied are refused with a specific reason; anything else not in
// propRules is refused as unknown. Kept explicit so the reasons are
// reviewable next to the allowlist.
var denied = map[string]string{
	"mountpoint": "mounts", "canmount": "mounts", "zoned": "mounts", "readonly": "mounts",
	"exec": "mount options", "setuid": "mount options", "devices": "mount options",
	"sharenfs": "exports", "sharesmb": "exports",
	"encryption": "encryption", "keylocation": "encryption", "keyformat": "encryption", "pbkdf2iters": "encryption",
	"quota": "capacity", "refquota": "capacity", "reservation": "capacity", "refreservation": "capacity", "volsize": "capacity",
}

// blockSize accepts a power of two in [lo, hi], in bytes or with a K/M
// suffix (zfs_nicestrtonum style: 128K, 128k, 128KB, 128KiB), and returns
// decimal bytes as `zfs get -p` prints them.
func blockSize(lo, hi uint64) func(string) (string, bool) {
	return func(v string) (string, bool) {
		n, ok := parseSize(v)
		if !ok || n < lo || n > hi || n&(n-1) != 0 {
			return "", false
		}
		return u64(n), true
	}
}

func parseSize(v string) (uint64, bool) {
	i := 0
	for i < len(v) && v[i] >= '0' && v[i] <= '9' {
		i++
	}
	if i == 0 || i > 9 {
		return 0, false
	}
	n, err := strconv.ParseUint(v[:i], 10, 64)
	if err != nil {
		return 0, false
	}
	suffix := strings.ToUpper(v[i:])
	var shift uint
	switch {
	case suffix == "" || suffix == "B":
	case strings.HasPrefix(suffix, "K"):
		shift = 10
	case strings.HasPrefix(suffix, "M"):
		shift = 20
	default:
		return 0, false
	}
	if shift > 0 {
		if rest := suffix[1:]; rest != "" && rest != "B" && rest != "IB" {
			return 0, false
		}
	}
	return n << shift, true
}

// ValidateProperties checks caller-supplied ZFS properties for a volume of
// kind against the allowlist and returns them canonicalised (keyed by zfs
// property name, valued as `zfs get -Hp` prints them), or InvalidArgument.
// An empty input yields nil.
func ValidateProperties(kind VolumeKind, in map[string]string) (map[string]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if strings.Contains(k, ":") {
			return nil, errorf(InvalidArgument, "property %q: user properties are not allowed", k)
		}
		if why, ok := denied[k]; ok {
			return nil, errorf(InvalidArgument, "property %q is not allowed (%s are managed by the endpoint)", k, why)
		}
		rule, ok := propRules[k]
		if !ok {
			return nil, errorf(InvalidArgument, "property %q is not allowed", k)
		}
		if (kind == Block && !rule.zvol) || (kind == Filesystem && !rule.fs) {
			return nil, errorf(InvalidArgument, "property %q does not apply to %s volumes", k, kind)
		}
		c, ok := rule.canon(v)
		if !ok {
			return nil, errorf(InvalidArgument, "property %q: invalid value %q", k, v)
		}
		out[k] = c
	}
	return out, nil
}

// propsMismatch reports the first requested property a row does not have:
// set locally to the same canonical value, or for volblocksize the zvol's
// actual block size. Unrequested properties are not compared, so volumes
// adopted with locally set properties stay compatible with plain requests.
func propsMismatch(row Row, want map[string]string) (string, bool) {
	for _, k := range sortedKeys(want) {
		if k == volblocksizeProp {
			if u64(row.BlockSz) != want[k] {
				return k, true
			}
			continue
		}
		pk, ok := propKeyOf(k)
		if !ok || row.Local[pk] != want[k] {
			return k, true
		}
	}
	return "", false
}

// propSet is what converges an uncommitted row's settable properties. It never
// unsets: only requested properties are driven.
func propSet(row Row, want map[string]string) map[string]string {
	set := map[string]string{}
	for k, v := range want {
		if k == volblocksizeProp {
			continue
		}
		if pk, ok := propKeyOf(k); ok && row.Local[pk] != v {
			set[k] = v
		}
	}
	return set
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
