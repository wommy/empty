package world_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/wommy/empty/internal/core/coretest"
	"github.com/wommy/empty/internal/core/name"
	"github.com/wommy/empty/internal/core/world"
)

const parent = "tank/k8s"

func setup(t *testing.T) (*coretest.Model, *world.Schema) {
	p, _ := name.ParseDataset(parent)
	sc, err := world.NewSchema(p, "zfs-endpoint")
	if err != nil {
		t.Fatal(err)
	}
	m := coretest.New("tank", parent)
	run := func(a ...string) {
		if err := m.Exec(a); err != nil {
			t.Fatal(a, err)
		}
	}
	own := []string{"-o", "zfs-endpoint:managed_resource=true", "-o", "zfs-endpoint:provision_success=true"}
	run(append(append([]string{"create"}, own...), "-o", "zfs-endpoint:csi_volume_name=fs", "-o", "refquota=1073741824", parent+"/fs")...)
	run(append(append([]string{"create", "-V", "1073741824"}, own...), "-o", "zfs-endpoint:csi_volume_name=zv", parent+"/zv")...)
	run("snapshot", "-o", "zfs-endpoint:managed_resource=true", "-o", "zfs-endpoint:csi_snapshot_name=n", "-o", "zfs-endpoint:csi_snapshot_source_volume_id=zv", parent+"/zv@s")
	run("snapshot", parent+"/fs@sanoid")
	return m, sc
}

// doc is one document as a generic tree, for tampering.
type doc map[string]any

func load(t *testing.T, b []byte) doc {
	var d doc
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func (d doc) ds(n string) map[string]any { return d["datasets"].(map[string]any)[n].(map[string]any) }
func (d doc) prop(n, p string) map[string]any {
	return d.ds(n)["properties"].(map[string]any)[p].(map[string]any)
}
func (d doc) set(n, p, v, src string) {
	pr := d.prop(n, p)
	pr["value"] = v
	if src != "" {
		pr["source"].(map[string]any)["type"] = src
	}
}

func parse(sc *world.Schema, s world.Scope, b ...[]byte) (*world.World, error) {
	var rs []io.Reader
	for _, x := range b {
		rs = append(rs, bytes.NewReader(x))
	}
	return sc.Parse(s, rs...)
}

func TestParseValid(t *testing.T) {
	m, sc := setup(t)
	s := world.All(true)
	w, err := parse(sc, s, m.Docs(sc, s)...)
	if err != nil {
		t.Fatal(err)
	}
	fs, _ := name.ParseVolID("fs")
	zv, _ := name.ParseVolID("zv")
	if w.State(fs) != world.Committed || w.State(zv) != world.Committed {
		t.Fatalf("states %v %v", w.State(fs), w.State(zv))
	}
	v, _ := w.Vol(zv)
	if bs, ok := v.BlockSize(); !ok || bs != 16384 || v.Capacity() != 1<<30 || !v.Thick() {
		t.Fatalf("zvol row %v %v %v", bs, v.Capacity(), v.Thick())
	}
	snap, _ := v.Snap("s")
	f, _ := w.Vol(fs)
	san, _ := f.Snap("sanoid")
	if snap.Own() != world.SnapCSI || san.Own() != world.SnapForeign || snap.Volsize() != 1<<30 {
		t.Fatalf("snapshot ownership %v %v", snap.Own(), san.Own())
	}
}

// TestParseRejectsWhatZfsNeverPrints: one row per OpenZFS invariant
// (wave5/zfs-invariants.md §7). Each tamper is something zfs get -j -p
// cannot print; the whole scan must be refused.
func TestParseRejectsWhatZfsNeverPrints(t *testing.T) {
	m, sc := setup(t)
	s := world.All(true)
	base := m.Docs(sc, s)[0]
	zv, fs, snap := parent+"/zv", parent+"/fs", parent+"/zv@s"
	cases := []struct {
		name   string
		tamper func(d doc)
	}{
		{"1 type: snapshot typed filesystem", func(d doc) { d.ds(snap)["type"] = "FILESYSTEM" }},
		{"1 type: filesystem typed snapshot", func(d doc) { d.ds(fs)["type"] = "SNAPSHOT" }},
		{"1 type: bookmark not requested", func(d doc) { d.ds(fs)["type"] = "BOOKMARK" }},
		{"1 header: snapshot_name disagrees", func(d doc) { d.ds(snap)["snapshot_name"] = "t" }},
		{"1 header: name disagrees with key", func(d doc) { d.ds(fs)["name"] = parent + "/other" }},
		{"2 grammar: digit-leading pool", func(d doc) {
			x := d.ds(fs)
			x["name"], x["pool"] = "1tank/k8s/fs", "1tank"
			d["datasets"].(map[string]any)["1tank/k8s/fs"] = x
			delete(d["datasets"].(map[string]any), fs)
		}},
		{"3 type-gated dash: volblocksize on a filesystem", func(d doc) { d.set(fs, "volblocksize", "16384", "DEFAULT") }},
		{"3 type-gated dash: refquota on a zvol", func(d doc) { d.set(zv, "refquota", "0", "DEFAULT") }},
		{"3 type-gated dash: defer_destroy on a filesystem", func(d doc) { d.set(fs, "defer_destroy", "off", "NONE") }},
		{"3 type-gated dash: origin on a snapshot", func(d doc) { d.set(snap, "origin", parent+"/fs@sanoid", "NONE") }},
		{"4 readonly with a source: creation local", func(d doc) { d.set(fs, "creation", "1729000000", "LOCAL") }},
		{"4 readonly with a source: origin inherited", func(d doc) { d.set(fs, "origin", "-", "INHERITED") }},
		{"5 PROP_DEFAULT inherited: refquota", func(d doc) { d.set(fs, "refquota", "1073741824", "INHERITED") }},
		{"6 zero is default: refquota 0 local", func(d doc) { d.set(fs, "refquota", "0", "LOCAL") }},
		{"6 zero is default: refreservation 5 default", func(d doc) { d.set(fs, "refreservation", "5", "DEFAULT") }},
		{"7 geometry: volsize dash (F2 shrink)", func(d doc) { d.set(zv, "volsize", "-", "NONE") }},
		{"7 geometry: volsize 1G (F2)", func(d doc) { d.set(zv, "volsize", "1G", "") }},
		{"7 geometry: volsize not a multiple", func(d doc) { d.set(zv, "volsize", "1073741825", "") }},
		{"7 geometry: volblocksize not a power of two", func(d doc) { d.set(zv, "volblocksize", "12288", "NONE") }},
		{"7 geometry: volsize above 2^64", func(d doc) { d.set(zv, "volsize", "18446744073709551616", "") }},
		{"8 origin not a snapshot", func(d doc) { d.set(fs, "origin", "tank/other", "NONE") }},
		{"9 space bounds: refreservation above refquota", func(d doc) { d.set(fs, "refreservation", "2147483648", "LOCAL") }},
		{"10 user prop: unset with a value", func(d doc) { d.set(fs, "zfs-endpoint:csi_snapshot_name", "x", "NONE") }},
		{"10 user prop: default source", func(d doc) { d.set(fs, "zfs-endpoint:managed_resource", "true", "DEFAULT") }},
		{"cross-row: zvol snapshot without volsize", func(d doc) { d.set(snap, "volsize", "-", "NONE") }},
		{"cross-row: snapshot without its dataset (F8)", func(d doc) { delete(d["datasets"].(map[string]any), zv) }},
		{"shape: a property missing", func(d doc) { delete(d.ds(fs)["properties"].(map[string]any), "guid") }},
		{"shape: a property not requested", func(d doc) {
			d.ds(fs)["properties"].(map[string]any)["used"] = map[string]any{"value": "1", "source": map[string]any{"type": "NONE", "data": "-"}}
		}},
		{"shape: output_version 0.2", func(d doc) { d["output_version"].(map[string]any)["vers_minor"] = 2 }},
		{"shape: clones on a filesystem", func(d doc) { d.set(fs, "clones", parent+"/x", "NONE") }},
		{"outside the parent", func(d doc) {
			x := d.ds(fs)
			x["name"] = "tank/elsewhere"
			d["datasets"].(map[string]any)["tank/elsewhere"] = x
			delete(d["datasets"].(map[string]any), fs)
		}},
	}
	// control: re-encoding alone must not make the document unparsable,
	// or every row below would pass for the wrong reason
	if b, _ := json.Marshal(load(t, base)); true {
		if _, err := parse(sc, s, b); err != nil {
			t.Fatalf("control: %v", err)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := load(t, base)
			c.tamper(d)
			b, _ := json.Marshal(d)
			_, err := parse(sc, s, b)
			var pe *world.ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("accepted (err=%v)", err)
			}
			t.Log(pe)
		})
	}
	// truncated output (zfs exits 0 after invalid UTF-8: adv4/zfs-json.md §2)
	if _, err := parse(sc, s, base[:len(base)/2]); err == nil {
		t.Error("truncated document accepted")
	}
	if _, err := parse(sc, s, append(bytes.Clone(base), "{}"...)); err == nil {
		t.Error("trailing data accepted")
	}
	// the same document twice is two prints of every dataset
	if _, err := parse(sc, s, base, base); err == nil {
		t.Error("duplicate datasets accepted")
	}
}

// TestPhantomRowsCannotBeForged: under -Hp a user-property value with a
// newline forged whole datasets (dod3 F5). Under -j it stays a value.
func TestPhantomRowsCannotBeForged(t *testing.T) {
	m, sc := setup(t)
	evil := "x\"},\"" + parent + "/evil\":{\"name\":\"" + parent + "/evil\"\n" + parent + "/evil\tzfs-endpoint:managed_resource\ttrue\tlocal"
	m.SetLocal(parent+"/fs", "zfs-endpoint:csi_volume_content_source_id", evil)
	s := world.All(false)
	w, err := parse(sc, s, m.Docs(sc, s)...)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := w.Len(); n != 2 {
		t.Fatalf("%d volumes, want 2", n)
	}
	fs, _ := name.ParseVolID("fs")
	if w.State(fs) != world.Invalid { // half a source marker: I3
		t.Errorf("fs %v", w.State(fs))
	}
}

// TestOwnershipIsDerivedOnce: every marker combination lands in exactly
// one state, and the impossible ones are Invalid, not owned.
func TestOwnershipIsDerivedOnce(t *testing.T) {
	type mk = map[string]string
	cases := []struct {
		name string
		set  mk
		recv bool
		want world.State
	}{
		{"unmarked", mk{}, false, world.Foreign},
		{"committed", mk{":managed_resource": "true", ":csi_volume_name": "v", ":provision_success": "true"}, false, world.Committed},
		{"uncommitted", mk{":managed_resource": "true", ":csi_volume_name": "v"}, false, world.Uncommitted},
		{"received copy (D05, CX5)", mk{":managed_resource": "true", ":csi_volume_name": "v", ":provision_success": "true"}, true, world.Foreign},
		{"renamed: name marker for another id", mk{":managed_resource": "true", ":csi_volume_name": "w", ":provision_success": "true"}, false, world.Foreign},
		{"name marker literally '-' (impossible-types R8)", mk{":managed_resource": "true", ":csi_volume_name": "-", ":provision_success": "true"}, false, world.Foreign},
		{"I1 committed without managed", mk{":provision_success": "true", ":csi_volume_name": "v"}, false, world.Invalid},
		{"I2 managed without name", mk{":managed_resource": "true", ":provision_success": "true"}, false, world.Invalid},
		{"I3 half a source", mk{":managed_resource": "true", ":csi_volume_name": "v", ":csi_volume_content_source_type": "snapshot"}, false, world.Invalid},
		{"managed=TRUE", mk{":managed_resource": "TRUE", ":csi_volume_name": "v"}, false, world.Invalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, sc := setup(t)
			if err := m.Exec([]string{"create", parent + "/v"}); err != nil {
				t.Fatal(err)
			}
			for k, v := range c.set {
				if c.recv {
					m.Receive(parent+"/v", "zfs-endpoint"+k, v)
				} else {
					m.SetLocal(parent+"/v", "zfs-endpoint"+k, v)
				}
			}
			s := world.All(false)
			w, err := parse(sc, s, m.Docs(sc, s)...)
			if err != nil {
				t.Fatal(err)
			}
			id, _ := name.ParseVolID("v")
			if got := w.State(id); got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			_, owned := w.Owned(id)
			if owned != (c.want == world.Committed || c.want == world.Uncommitted) {
				t.Fatalf("Owned handle minted for %v", c.want)
			}
		})
	}
}

func TestSnapshotOwnership(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want world.SnapOwn
	}{
		{"csi", []string{"-o", "zfs-endpoint:managed_resource=true", "-o", "zfs-endpoint:csi_snapshot_name=n", "-o", "zfs-endpoint:csi_snapshot_source_volume_id=zv"}, world.SnapCSI},
		{"sanoid", nil, world.SnapForeign},
		{"I8 csi name without managed (F11)", []string{"-o", "zfs-endpoint:csi_snapshot_name=n"}, world.SnapInvalid},
		{"I9 managed without csi name", []string{"-o", "zfs-endpoint:managed_resource=true"}, world.SnapInvalid},
		{"copied: source marker names another volume", []string{"-o", "zfs-endpoint:managed_resource=true", "-o", "zfs-endpoint:csi_snapshot_name=n", "-o", "zfs-endpoint:csi_snapshot_source_volume_id=other"}, world.SnapForeign},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, sc := setup(t)
			if err := m.Exec(append(append([]string{"snapshot"}, c.argv...), parent+"/zv@t")); err != nil {
				t.Fatal(err)
			}
			s := world.All(false)
			w, err := parse(sc, s, m.Docs(sc, s)...)
			if err != nil {
				t.Fatal(err)
			}
			zv, _ := name.ParseVolID("zv")
			v, _ := w.Vol(zv)
			sn, ok := v.Snap("t")
			if !ok || sn.Own() != c.want {
				t.Fatalf("got %v", sn.Own())
			}
		})
	}
	// a clone source is ours by its stamp, never by its name (OWN-13):
	// x stamped for x is ours; y managed but unstamped, z stamped for
	// another clone are half-ours (Invalid); u bare name is someone's
	m, sc := setup(t)
	_ = m.Exec([]string{"snapshot", "-o", "zfs-endpoint:managed_resource=true", "-o", "zfs-endpoint:clone_source_for=x", parent + "/zv@volume-source-for-volume-x"})
	_ = m.Exec([]string{"snapshot", "-o", "zfs-endpoint:managed_resource=true", parent + "/zv@volume-source-for-volume-y"})
	_ = m.Exec([]string{"snapshot", "-o", "zfs-endpoint:managed_resource=true", "-o", "zfs-endpoint:clone_source_for=q", parent + "/zv@volume-source-for-volume-z"})
	_ = m.Exec([]string{"snapshot", parent + "/zv@volume-source-for-volume-u"})
	s := world.All(false)
	w, _ := parse(sc, s, m.Docs(sc, s)...)
	zv, _ := name.ParseVolID("zv")
	v, _ := w.Vol(zv)
	x, _ := v.Snap("volume-source-for-volume-x")
	y, _ := v.Snap("volume-source-for-volume-y")
	z, _ := v.Snap("volume-source-for-volume-z")
	u, _ := v.Snap("volume-source-for-volume-u")
	if x.Own() != world.SnapCloneSource || y.Own() != world.SnapInvalid || z.Own() != world.SnapInvalid || u.Own() != world.SnapForeign {
		t.Fatalf("clone sources: %v %v %v %v", x.Own(), y.Own(), z.Own(), u.Own())
	}
}

func TestUnaddressableNamesAreDropped(t *testing.T) {
	m, sc := setup(t)
	_ = m.Exec([]string{"create", parent + "/has space"})
	_ = m.Exec([]string{"snapshot", parent + "/has space@s"})
	s := world.All(false)
	w, err := parse(sc, s, m.Docs(sc, s)...)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := w.Len(); n != 2 || strings.Contains(w.Parent().String(), " ") {
		t.Fatalf("%d volumes", n)
	}
}

// A7: the parser tracks seen properties in a uint64; the widest scan must
// fit (ParseUnder panics otherwise).
func TestWidestSchemaFits(t *testing.T) {
	_, sc := setup(t)
	if n := len(sc.Props(world.All(true))); n > 64 {
		t.Fatalf("%d properties", n)
	}
}
