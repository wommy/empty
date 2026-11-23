// Package zfsdiff is a differential test kit for the ZFS command layer:
//
//   - Model: an independent reference state machine of OpenZFS 2.4.4
//     `zfs` command semantics over one pool, written from the OpenZFS
//     source (tag zfs-2.4.4), not from the simulator. It implements
//     zfs.Runner, so it is driven exactly like the endpoint drives zfs.
//   - Seq / Exec / Diff: command sequences, a step executor that records
//     exit status, error class and a `zfs get -Hp` projection after every
//     command, a comparator and a minimiser.
//   - Gen: a seeded random sequence generator over a small namespace.
//   - Corpus: recorded sequences (testdata/zfsdiff/*.txt), replayed on the
//     model, the simulator and, in CI, a real pool.
//
// Citations are `path:line` in the OpenZFS source at tag zfs-2.4.4
// (scratchpad wave9/oz). They name the rule each branch implements.
package zfsdiff

import (
	"strconv"
	"strings"
)

type kind uint8

const (
	kFS kind = 1 << iota
	kVol
	kSnap
)

func (k kind) String() string {
	switch k {
	case kFS:
		return "filesystem"
	case kVol:
		return "volume"
	case kSnap:
		return "snapshot"
	}
	return "?"
}

type pattr uint8

const (
	pInherit  pattr = iota // PROP_INHERIT
	pDefault               // PROP_DEFAULT: never "inherited from"
	pReadonly              // PROP_READONLY
	pOnetime               // PROP_ONETIME: settable only at create
)

type ptype uint8

const (
	tIndex ptype = iota
	tNumber
	tString
)

type idx struct {
	name string
	val  int
}

type pdef struct {
	types kind
	attr  pattr
	typ   ptype
	def   string // printed default ("" = none)
	table []idx  // index props, in zfs_prop.c table order
}

func onoff() []idx { return []idx{{"off", 0}, {"on", 1}} }

// propDefs are the native properties the model knows, with the type mask,
// inheritance attribute, type and default registered in
// module/zcommon/zfs_prop.c:405-745 (zfs_prop_init). Others are refused
// as unmodelled (they are not generated).
var propDefs = func() map[string]pdef {
	compress := []idx{{"on", 1}, {"off", 2}, {"lzjb", 3}, {"gzip", 106}}
	for i := 1; i <= 9; i++ {
		compress = append(compress, idx{"gzip-" + strconv.Itoa(i), 100 + i})
	}
	compress = append(compress, idx{"zle", 4}, idx{"lz4", 5}, idx{"zstd", 6}, idx{"zstd-fast", 1001})
	for i := 1; i <= 19; i++ {
		compress = append(compress, idx{"zstd-" + strconv.Itoa(i), 200 + i})
	}
	for _, i := range []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 20, 30, 40, 50, 60, 70, 80, 90, 100, 500, 1000} {
		compress = append(compress, idx{"zstd-fast-" + strconv.Itoa(i), 1000 + i})
	}
	fsvol := kFS | kVol
	return map[string]pdef{
		// readonly (zfs_prop.c:541-580, 589, 627-681)
		"type":          {types: kFS | kVol | kSnap, attr: pReadonly, typ: tString},
		"origin":        {types: fsvol, attr: pReadonly, typ: tString},
		"mounted":       {types: kFS, attr: pReadonly, typ: tIndex},
		"defer_destroy": {types: kSnap, attr: pReadonly, typ: tIndex},
		"userrefs":      {types: kSnap, attr: pReadonly, typ: tNumber},
		"used":          {types: kFS | kVol | kSnap, attr: pReadonly, typ: tNumber},
		"available":     {types: fsvol, attr: pReadonly, typ: tNumber},
		// registered for snapshots, but only dsl_dir_stats reports it:
		// a snapshot prints "-" as for a type it does not apply to
		"usedbyrefreservation": {types: fsvol, attr: pReadonly, typ: tNumber},
		"creation":             {types: kFS | kVol | kSnap, attr: pReadonly, typ: tNumber},
		"name":                 {types: kFS | kVol | kSnap, attr: pReadonly, typ: tString},
		// onetime (zfs_prop.c:641-643)
		"volblocksize": {types: kVol, attr: pOnetime, typ: tNumber, def: "16384"},
		// default numbers (zfs_prop.c:691-704)
		"quota":          {types: kFS, attr: pDefault, typ: tNumber, def: "0"},
		"reservation":    {types: fsvol, attr: pDefault, typ: tNumber, def: "0"},
		"volsize":        {types: kSnap | kVol, attr: pDefault, typ: tNumber, def: "0"},
		"refquota":       {types: kFS, attr: pDefault, typ: tNumber, def: "0"},
		"refreservation": {types: fsvol, attr: pDefault, typ: tNumber, def: "0"},
		// default index (zfs_prop.c:536-539)
		"canmount": {types: kFS, attr: pDefault, typ: tIndex, def: "on", table: []idx{{"off", 0}, {"on", 1}, {"noauto", 2}}},
		// inherit strings (zfs_prop.c:583-594)
		"mountpoint": {types: kFS, attr: pInherit, typ: tString, def: "/"},
		"sharenfs":   {types: kFS, attr: pInherit, typ: tString, def: "off"},
		"sharesmb":   {types: kFS, attr: pInherit, typ: tString, def: "off"},
		// inherit numbers (zfs_prop.c:735-738)
		"recordsize": {types: kFS, attr: pInherit, typ: tNumber, def: "131072"},
		// inherit index (zfs_prop.c:417-501)
		"sync": {types: fsvol, attr: pInherit, typ: tIndex, def: "standard",
			table: []idx{{"standard", 0}, {"always", 1}, {"disabled", 2}}},
		"dedup": {types: fsvol, attr: pInherit, typ: tIndex, def: "off",
			table: []idx{{"on", 1}, {"off", 0}, {"verify", 2}, {"sha256", 3}, {"sha256,verify", 4},
				{"sha512", 5}, {"sha512,verify", 6}, {"skein", 7}, {"skein,verify", 8},
				{"edonr,verify", 9}, {"blake3", 10}, {"blake3,verify", 11}}},
		"compression": {types: fsvol, attr: pInherit, typ: tIndex, def: "on", table: compress},
		"snapdir": {types: kFS, attr: pInherit, typ: tIndex, def: "hidden",
			table: []idx{{"hidden", 0}, {"visible", 1}, {"disabled", 2}}},
		"acltype": {types: kFS | kSnap, attr: pInherit, typ: tIndex, def: "off",
			table: []idx{{"off", 0}, {"posix", 1}, {"nfsv4", 2}, {"disabled", 0}, {"noacl", 0}, {"posixacl", 1}}},
		"copies":  {types: fsvol, attr: pInherit, typ: tIndex, def: "1", table: []idx{{"1", 1}, {"2", 2}, {"3", 3}}},
		"logbias": {types: fsvol, attr: pInherit, typ: tIndex, def: "latency", table: []idx{{"latency", 0}, {"throughput", 1}}},
		// zfs_prop.c:365-370: "on" is SA in 2.4, and "sa" is listed first
		"xattr":    {types: kFS | kSnap, attr: pInherit, typ: tIndex, def: "sa", table: []idx{{"off", 0}, {"sa", 1}, {"on", 1}, {"dir", 2}}},
		"atime":    {types: kFS, attr: pInherit, typ: tIndex, def: "on", table: onoff()},
		"relatime": {types: kFS, attr: pInherit, typ: tIndex, def: "on", table: onoff()},
		"readonly": {types: fsvol, attr: pInherit, typ: tIndex, def: "off", table: onoff()},
	}
}()

// canonIndex: the printed name is the first table entry with the value's
// index (zprop_index_to_string, module/zcommon/zprop_common.c).
func canonIndex(d pdef, v string) (string, bool) {
	for _, e := range d.table {
		if e.name == v {
			for _, f := range d.table {
				if f.val == e.val {
					return f.name, true
				}
			}
		}
	}
	return "", false
}

// isUserProp is zfs_prop_user (module/zcommon/zfs_prop.c:833-852): every
// byte in [a-z0-9-_.:] (zprop_common.c:420-425) and at least one ':'.
func isUserProp(name string) bool {
	if !strings.Contains(name, ":") {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == ':') {
			return false
		}
	}
	return true
}

// niceStrToNum is zfs_nicestrtonum (lib/libzfs/libzfs_util.c:1676-1740),
// integer part only: digits, then an optional suffix from "BKMGTPEZ",
// optionally followed by "B" or "iB" (str2shift, :1620-1665).
func niceStrToNum(v string) (uint64, string, bool) {
	if v == "" || (v[0] < '0' || v[0] > '9') {
		return 0, "bad numeric value '" + v + "'", false
	}
	i := 0
	for i < len(v) && v[i] >= '0' && v[i] <= '9' {
		i++
	}
	n, err := strconv.ParseUint(v[:i], 10, 64)
	if err != nil {
		return 0, "numeric value is too large", false
	}
	suf := strings.ToUpper(v[i:])
	if suf == "" {
		return n, "", true
	}
	shift := strings.IndexByte("BKMGTPEZ", suf[0])
	if shift < 0 || !(len(suf) == 1 || suf[0] != 'B' && (suf[1:] == "B" || suf[1:] == "IB")) {
		return 0, "invalid numeric suffix '" + v[i:] + "'", false
	}
	shift *= 10
	if shift >= 64 || (n<<shift)>>shift != n {
		return 0, "numeric value is too large", false
	}
	return n << shift, "", true
}

// niceBytes is zfs_nicebytes for powers of two (used in error texts).
func niceBytes(n uint64) string {
	units := "BKMGTPE"
	u := 0
	for n >= 1024 && n%1024 == 0 && u < len(units)-1 {
		n /= 1024
		u++
	}
	if u == 0 {
		return strconv.FormatUint(n, 10) + "B"
	}
	return strconv.FormatUint(n, 10) + string(units[u])
}

// zvolResv is zvol_volsize_to_reservation (lib/libzfs/libzfs_dataset.c:
// 5587-5626) for a pool without raidz/draid top-level vdevs, where
// volsize_from_vdevs (:5483-5578) returns nblocks*blksize. ncopies is
// always 1 in the callers the model covers: zfs create passes real_props,
// where copies is already a uint64 so the string lookup (:5598) misses,
// and the set/auto paths pass only volblocksize (:1625-1627, :1688-1689).
func zvolResv(volsize, volblocksize uint64) uint64 {
	const dnodesPerLevel = 1 << (17 - 7) // DN_MAX_INDBLKSHIFT - SPA_BLKPTRSHIFT
	nblocks := volsize / volblocksize
	size := nblocks * volblocksize
	numdb := uint64(7)
	for nblocks > 1 {
		nblocks += dnodesPerLevel - 1
		nblocks /= dnodesPerLevel
		numdb += nblocks
	}
	numdb *= 2 // MIN(SPA_DVAS_PER_BP, ncopies + 1)
	numdb *= 1 << 17
	return size + numdb
}
