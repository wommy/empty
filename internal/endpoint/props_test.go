package endpoint

import (
	"testing"

	"github.com/wommy/empty/internal/zfs"
)

// Property passthrough is default-deny: everything outside the allowlist,
// and every allowlisted key on the wrong kind, is InvalidArgument before any
// state is looked at.
func TestPlanCreatePropertiesDenyList(t *testing.T) {
	denied := []string{
		"mountpoint", "sharenfs", "sharesmb", "canmount", "exec", "setuid", "devices",
		"keylocation", "keyformat", "encryption", "quota", "refquota", "reservation",
		"refreservation", "volsize", "readonly", "zoned",
		// user properties, including ones that would forge ownership
		"democratic-csi:managed_resource", "democratic-csi:provision_success", "user:x", "com.example:y",
		// native but not allowlisted
		"checksum", "primarycache", "special_small_blocks", "dnodesize", "Compression", "",
	}
	for _, kind := range []VolumeKind{Filesystem, Block} {
		for _, k := range denied {
			r := CreateVolumeRequest{Name: "a", Kind: kind, CapacityBytes: 1 << 20, Properties: map[string]string{k: "on"}}
			if _, err := pl.PlanCreateVolume(NewTable(nil), r); code(err) != InvalidArgument {
				t.Errorf("%s %q: %v", kind, k, err)
			}
		}
	}
	// allowlisted, but not on this kind (zfs_prop.c type masks)
	for _, c := range []struct {
		kind VolumeKind
		k, v string
	}{
		{Block, "recordsize", "16K"}, {Block, "atime", "off"}, {Block, "relatime", "off"},
		{Block, "xattr", "sa"}, {Block, "acltype", "posix"}, {Block, "snapdir", "visible"},
		{Filesystem, "volblocksize", "16K"},
		// copies count against refquota on a filesystem, so 2 or 3 would
		// silently halve or third its capacity (zfsprops.7, copies)
		{Filesystem, "copies", "2"},
	} {
		r := CreateVolumeRequest{Name: "a", Kind: c.kind, CapacityBytes: 1 << 20, Properties: map[string]string{c.k: c.v}}
		if _, err := pl.PlanCreateVolume(NewTable(nil), r); code(err) != InvalidArgument {
			t.Errorf("%s %s=%s: %v", c.kind, c.k, c.v, err)
		}
	}
}

func TestValidatePropertyValues(t *testing.T) {
	for _, c := range []struct {
		kind  VolumeKind
		k, v  string
		canon string // "" = InvalidArgument
	}{
		{Filesystem, "compression", "lz4", "lz4"},
		{Filesystem, "compression", "zstd-19", "zstd-19"},
		{Filesystem, "compression", "gzip-6", "gzip"}, // zfs prints the first table name
		{Filesystem, "compression", "zstd-fast-1", "zstd-fast"},
		{Filesystem, "compression", "LZ4", ""}, // index props are case-sensitive
		{Filesystem, "compression", "zstd-20", ""},
		{Filesystem, "compression", "lz4 ", ""},
		{Filesystem, "recordsize", "131072", "131072"},
		{Filesystem, "recordsize", "128K", "131072"},
		{Filesystem, "recordsize", "128k", "131072"},
		{Filesystem, "recordsize", "128KiB", "131072"},
		{Filesystem, "recordsize", "512", "512"},
		{Filesystem, "recordsize", "16M", "16777216"},
		{Filesystem, "recordsize", "32M", ""},
		{Filesystem, "recordsize", "256", ""},
		{Filesystem, "recordsize", "3000", ""},
		{Filesystem, "recordsize", "1G", ""},
		{Filesystem, "recordsize", "-1", ""},
		{Filesystem, "recordsize", "", ""},
		{Filesystem, "recordsize", "1 M", ""},
		{Filesystem, "recordsize", "99999999999999999999", ""},
		{Block, "volblocksize", "8k", "8192"},
		{Block, "volblocksize", "128K", "131072"},
		{Block, "volblocksize", "256K", ""}, // narrower than recordsize
		{Block, "volblocksize", "12K", ""},
		{Filesystem, "atime", "off", "off"},
		{Filesystem, "atime", "yes", ""},
		{Filesystem, "relatime", "on", "on"},
		{Block, "sync", "always", "always"},
		{Block, "sync", "never", ""},
		{Block, "logbias", "throughput", "throughput"},
		{Block, "dedup", "verify", "verify"},
		{Block, "dedup", "sha256", ""}, // off|on|verify only
		{Block, "dedup", "sha256,verify", ""},
		// xattr: on is an alias printed as sa; only sa and off are accepted
		{Filesystem, "xattr", "sa", "sa"},
		{Filesystem, "xattr", "off", "off"},
		{Filesystem, "xattr", "on", ""},
		{Filesystem, "xattr", "dir", ""},
		{Filesystem, "acltype", "posixacl", "posix"},
		{Filesystem, "acltype", "noacl", "off"},
		{Filesystem, "snapdir", "visible", "visible"},
		{Filesystem, "snapdir", "shown", ""},
		{Filesystem, "snapdir", "disabled", "disabled"},
		{Block, "copies", "3", "3"},
		{Block, "copies", "4", ""},
		{Block, "copies", "0", ""},
		{Filesystem, "compression", "gzip-9", "gzip-9"},
		{Filesystem, "recordsize", "512B", "512"},
		{Filesystem, "logbias", "throughput", "throughput"}, // logbias, dedup: both kinds
		{Filesystem, "dedup", "off", "off"},
		{Block, "atime", "on", ""}, // filesystem-only properties
		{Block, "recordsize", "128K", ""},
	} {
		got, err := ValidateProperties(c.kind, map[string]string{c.k: c.v})
		switch {
		case c.canon == "" && code(err) != InvalidArgument:
			t.Errorf("%s %s=%q: want InvalidArgument, got %v %v", c.kind, c.k, c.v, got, err)
		case c.canon != "" && (err != nil || got[c.k] != c.canon):
			t.Errorf("%s %s=%q: want %q, got %v %v", c.kind, c.k, c.v, c.canon, got, err)
		}
	}
}

// parseSize reads at most 9 digits (so no overflow before the shift).
func TestParseSize(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   string
		want uint64
		ok   bool
	}{
		{"999999999", 999999999, true},
		{"1000000000", 0, false},
		{"512B", 512, true},
		{"512b", 512, true},
		{"1K", 1 << 10, true},
		{"1M", 1 << 20, true},
		{"K", 0, false},
	} {
		if got, ok := parseSize(tc.in); ok != tc.ok || ok && got != tc.want {
			t.Errorf("parseSize(%q) = %d %v, want %d %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestPlanCreateAppliesProperties(t *testing.T) {
	props := map[string]string{"compression": "zstd", "recordsize": "16K", "atime": "off"}
	plan, err := pl.PlanCreateVolume(NewTable(nil), CreateVolumeRequest{Name: "fs", CapacityBytes: 1 << 20, Properties: props})
	if err != nil || plan[0].Kind != OpCreateFS || plan[0].Props["compression"] != "zstd" ||
		plan[0].Props["recordsize"] != "16384" || plan[0].Props["atime"] != "off" ||
		plan[0].Props["democratic-csi:managed_resource"] != "true" || plan[0].Props["sharenfs"] != "off" {
		t.Fatalf("fs: %+v %v", plan, err)
	}

	// the requested volblocksize, not the configured default, rounds volsize
	plan, err = pl.PlanCreateVolume(NewTable(nil), CreateVolumeRequest{Name: "z", Kind: Block, CapacityBytes: 1<<20 + 1,
		Properties: map[string]string{"volblocksize": "4K", "sync": "always"}})
	if err != nil || plan[0].Kind != OpCreateVol || plan[0].Size != 1<<20+4096 ||
		plan[0].Props["volblocksize"] != "4096" || plan[0].Props["sync"] != "always" {
		t.Fatalf("zvol: %+v %v", plan, err)
	}
	if _, err := pl.PlanCreateVolume(NewTable(nil), CreateVolumeRequest{Name: "z", Kind: Block, CapacityBytes: 1<<20 + 1,
		LimitBytes: 1<<20 + 4096, Properties: map[string]string{"volblocksize": "4K"}}); err != nil {
		t.Fatalf("limit is checked against the requested block size: %v", err)
	}

	// clones take properties as clone -o, but never volblocksize: a clone
	// keeps its origin's block size and libzfs refuses it as readonly
	tab := NewTable([]Row{
		vol("src", func(r *Row) { r.Type, r.Volsize, r.BlockSz = zfs.TypeVolume, 1<<20, 8192 }),
		snap("src@s", func(r *Row) { r.Volsize = 1 << 20 }),
	})
	for _, r := range []CreateVolumeRequest{
		{Name: "c1", Kind: Block, SourceSnapshotID: "src@s", Properties: map[string]string{"volblocksize": "8K", "compression": "lz4"}},
		{Name: "c2", Kind: Block, SourceVolumeID: "src", Properties: map[string]string{"volblocksize": "8192", "compression": "lz4"}},
	} {
		plan, err := pl.PlanCreateVolume(tab, r)
		if err != nil {
			t.Fatalf("%s: %v", r.Name, err)
		}
		var clone Op
		for _, op := range plan {
			if op.Kind == OpClone {
				clone = op
			}
		}
		if _, set := clone.Props["volblocksize"]; set || clone.Props["compression"] != "lz4" {
			t.Fatalf("%s: clone op %+v", r.Name, clone)
		}
		r.Properties = map[string]string{"volblocksize": "16K"}
		if _, err := pl.PlanCreateVolume(tab, r); code(err) != InvalidArgument {
			t.Fatalf("%s: mismatched clone volblocksize: %v", r.Name, err)
		}
	}
}

// CSI idempotency with properties: a committed volume is compatible only
// when each requested property is set locally with the same canonical value.
func TestPlanCreatePropertiesRetry(t *testing.T) {
	tab := NewTable([]Row{
		vol("fs", func(r *Row) {
			r.Refquota = 1 << 20
			r.Local[propCompression], r.Local[propRecordsize], r.Local[propXattr] = "gzip", "16384", "sa"
		}),
		vol("plain", func(r *Row) { r.Refquota = 1 << 20 }),
		vol("z", func(r *Row) { r.Type, r.Volsize, r.Refres, r.BlockSz = zfs.TypeVolume, 1<<20, 2<<20, 8192 }),
	})
	for _, c := range []struct {
		name  string
		kind  VolumeKind
		props map[string]string
		want  Code
	}{
		{"fs", Filesystem, map[string]string{"compression": "gzip-6", "recordsize": "16K", "xattr": "sa"}, OK},
		{"fs", Filesystem, map[string]string{"compression": "gzip"}, OK},
		{"fs", Filesystem, nil, OK}, // unrequested local properties are not compared
		{"fs", Filesystem, map[string]string{"compression": "lz4"}, AlreadyExists},
		{"fs", Filesystem, map[string]string{"recordsize": "32K"}, AlreadyExists},
		{"fs", Filesystem, map[string]string{"atime": "off"}, AlreadyExists}, // not set locally
		{"fs", Filesystem, map[string]string{"mountpoint": "/"}, InvalidArgument},
		{"plain", Filesystem, map[string]string{"compression": "on"}, AlreadyExists}, // default/inherited is not local
		{"z", Block, map[string]string{"volblocksize": "8K"}, OK},                    // effective value: zfs never reports it local
		{"z", Block, map[string]string{"volblocksize": "16K"}, AlreadyExists},
	} {
		plan, err := pl.PlanCreateVolume(tab, CreateVolumeRequest{Name: c.name, Kind: c.kind, CapacityBytes: 1 << 20, Properties: c.props})
		if code(err) != c.want || plan != nil {
			t.Errorf("%s %v: plan %+v err %v, want code %d", c.name, c.props, plan, err, c.want)
		}
	}
}

// An uncommitted row is driven toward the request: missing or different
// properties are set, then committed. volblocksize cannot be driven.
func TestPlanCreatePropertiesConvergeUncommitted(t *testing.T) {
	tab := NewTable([]Row{
		vol("fs", func(r *Row) {
			r.Committed, r.Refquota = false, 1<<20
			r.Local[propCompression], r.Local[propAtime] = "lz4", "off"
		}),
		vol("z", func(r *Row) {
			r.Committed, r.Type, r.Volsize, r.Refres, r.BlockSz = false, zfs.TypeVolume, 1<<20, 2<<20, 16384
		}),
	})
	plan, err := pl.PlanCreateVolume(tab, CreateVolumeRequest{Name: "fs", CapacityBytes: 1 << 20,
		Properties: map[string]string{"compression": "zstd", "atime": "off", "sync": "always"}})
	if err != nil || len(plan) != 2 || plan[0].Kind != OpSet || len(plan[0].Props) != 2 ||
		plan[0].Props["compression"] != "zstd" || plan[0].Props["sync"] != "always" ||
		plan[1].Props["democratic-csi:provision_success"] != "true" {
		t.Fatalf("converge: %+v %v", plan, err)
	}
	if _, err := pl.PlanCreateVolume(tab, CreateVolumeRequest{Name: "z", Kind: Block, CapacityBytes: 1 << 20,
		Properties: map[string]string{"volblocksize": "8K"}}); code(err) != AlreadyExists {
		t.Fatalf("volblocksize cannot converge: %v", err)
	}
	plan, err = pl.PlanCreateVolume(tab, CreateVolumeRequest{Name: "z", Kind: Block, CapacityBytes: 1 << 20,
		Properties: map[string]string{"volblocksize": "16K"}})
	if err != nil || len(plan) != 1 {
		t.Fatalf("matching volblocksize: %+v %v", plan, err)
	}
}
