package endpoint

import (
	"maps"
	"reflect"
	"testing"

	"github.com/wommy/empty/internal/zfs"
)

// Export planner tests are pure like plan_test.go: rows and an export table
// in, a plan or error out.

const clientsProp = "democratic-csi:nfs_clients"

// fsv is a managed, mounted filesystem volume with sharenfs=off set locally
// at creation, as PlanCreateVolume makes it.
func fsv(id string, mut func(*Row)) Row {
	return vol(id, func(r *Row) {
		r.Mount, r.Mounted = "/tank/v/"+id, true
		r.ShareNFS, r.ShareNFSLocal = "off", true
		if mut != nil {
			mut(r)
		}
	})
}

// exported is a volume the endpoint already exports with clients enc.
func exported(id, enc, share string) Row {
	return fsv(id, func(r *Row) {
		r.NFSClients, r.NFSClientsSet = enc, true
		r.ShareNFS = share
	})
}

func etab(lines string) Etab { return ParseEtab([]byte(lines)) }

func TestParseClient(t *testing.T) {
	ok := map[string]string{
		"10.0.0.1":          "10.0.0.1",
		"10.0.0.1/32":       "10.0.0.1",
		"10.1.2.3/8":        "10.0.0.0/8",
		"192.168.0.0/24":    "192.168.0.0/24",
		"Node-A":            "node-a",
		"node-a.example":    "node-a.example",
		"k8s-worker-01.lan": "k8s-worker-01.lan",
	}
	for in, want := range ok {
		c, err := ParseClient(in, false)
		if err != nil || c.Host != want {
			t.Errorf("ParseClient(%q) = %q, %v; want %q", in, c.Host, err, want)
		}
	}
	bad := []string{
		"", "*", "*.example", "@netgroup", "a,b", "a:b", "rw=x", "x(rw)", "a b", "-o", "-host",
		"host-", "a..b", "::1", "[::1]", "fe80::/64", "10.0.0.1/33", "10.0.0", "1.2.3.4.5", "010.0.0.1",
		"$(reboot)", "a;b", "a\nb", "no_root_squash,rw=*", string(make([]byte, 300)),
	}
	for _, in := range bad {
		if _, err := ParseClient(in, false); CodeOf(err) != InvalidArgument {
			t.Errorf("ParseClient(%q): %v, want InvalidArgument", in, err)
		}
	}
}

func TestShareNFSAndClientEncoding(t *testing.T) {
	cs := []NFSClient{{Host: "b"}, {Host: "10.0.0.0/8", ReadOnly: true}, {Host: "a"}}
	if got := ShareNFS(cs); got != "rw=a:b,ro=10.0.0.0/8" {
		t.Fatalf("ShareNFS = %q", got)
	}
	if got := ShareNFS(nil); got != "off" {
		t.Fatalf("ShareNFS(nil) = %q", got)
	}
	if got := ShareNFS(cs[1:2]); got != "ro=10.0.0.0/8" {
		t.Fatalf("ro only = %q", got)
	}
	enc := encodeClients(cs)
	if enc != "10.0.0.0/8=ro,a=rw,b=rw" {
		t.Fatalf("encode = %q", enc)
	}
	back, err := decodeClients(enc)
	if err != nil || !reflect.DeepEqual(back, sortClients(cs)) {
		t.Fatalf("decode = %v %v", back, err)
	}
	if back, err := decodeClients("none"); err != nil || back != nil {
		t.Fatalf("none = %v %v", back, err)
	}
	for _, s := range []string{"", "a", "a=rx", "b=rw,a=rw", "A=rw", "*=rw", "a=rw,,b=rw", "10.0.0.1/32=rw"} {
		if _, err := decodeClients(s); err == nil {
			t.Errorf("decodeClients(%q) accepted", s)
		}
	}
}

func TestParseEtab(t *testing.T) {
	e := etab("/tank/v/a\t10.0.0.1(rw,sync,wdelay,root_squash,sec=sys,rw)\n" +
		"/tank/v/a\tNode-B.example(ro,sync)\n" +
		"/tank/with\\040space\t*(rw,sync)\n" +
		"garbage line\n")
	if c, ok := e.Lookup("/tank/v/a", "10.0.0.1"); !ok || c.ReadOnly {
		t.Fatalf("rw client: %v %v", c, ok)
	}
	// exportfs records hostnames by their canonical name
	if c, ok := e.Lookup("/tank/v/a", "node-b"); !ok || !c.ReadOnly {
		t.Fatalf("canonical host: %v %v", c, ok)
	}
	if _, ok := e.Lookup("/tank/v/a", "10.0.0"); ok {
		t.Fatal("address prefix must not match")
	}
	if _, ok := e.Lookup("/tank/with space", "*"); !ok {
		t.Fatalf("octal path: %v", e)
	}
	if len(e) != 2 {
		t.Fatalf("paths %v", e)
	}
}

func TestPlanPublishExport(t *testing.T) {
	c1 := NFSClient{Host: "10.0.0.1"}
	live1 := etab("/tank/v/a\t10.0.0.1(rw,sync)\n")
	set := func(props map[string]string) Plan {
		return Plan{{Kind: OpSet, Target: "tank/v/a", Props: props}}
	}
	cases := []struct {
		name string
		row  Row
		live Etab
		c    NFSClient
		want Plan
		code Code
	}{
		{name: "first client", row: fsv("a", nil), c: c1,
			want: set(map[string]string{clientsProp: "10.0.0.1=rw", "sharenfs": "rw=10.0.0.1"})},
		{name: "repeat is empty", row: exported("a", "10.0.0.1=rw", "rw=10.0.0.1"), live: live1, c: c1},
		{name: "repeat, not served: reshare", row: exported("a", "10.0.0.1=rw", "rw=10.0.0.1"), c: c1,
			want: set(map[string]string{"sharenfs": "rw=10.0.0.1"})},
		{name: "served in the other mode: reshare", row: exported("a", "10.0.0.1=rw", "rw=10.0.0.1"),
			live: etab("/tank/v/a\t10.0.0.1(ro)\n"), c: c1, want: set(map[string]string{"sharenfs": "rw=10.0.0.1"})},
		{name: "second client", row: exported("a", "10.0.0.1=rw", "rw=10.0.0.1"), live: live1,
			c:    NFSClient{Host: "10.9.0.0/16", ReadOnly: true},
			want: set(map[string]string{clientsProp: "10.0.0.1=rw,10.9.0.0/16=ro", "sharenfs": "rw=10.0.0.1,ro=10.9.0.0/16"})},
		{name: "other mode", row: exported("a", "10.0.0.1=rw", "rw=10.0.0.1"), live: live1,
			c: NFSClient{Host: "10.0.0.1", ReadOnly: true}, code: AlreadyExists},
		{name: "drifted sharenfs is rewritten", row: exported("a", "10.0.0.1=rw", "rw=10.0.0.1:10.6.6.6"), live: live1, c: c1,
			want: set(map[string]string{"sharenfs": "rw=10.0.0.1"})},
		{name: "inherited sharenfs is pinned", c: c1, live: etab("/tank/v/a\t*(rw)\n"),
			row:  fsv("a", func(r *Row) { r.ShareNFS, r.ShareNFSLocal = "on", false }),
			want: set(map[string]string{clientsProp: "10.0.0.1=rw", "sharenfs": "rw=10.0.0.1"})},
		{name: "foreign local sharenfs", c: c1, code: FailedPrecondition,
			row: fsv("a", func(r *Row) { r.ShareNFS = "rw=@10.0.0.0/8,no_root_squash" })},
		{name: "corrupt client list", c: c1, code: FailedPrecondition, row: exported("a", "x=yes", "off")},
		{name: "zvol", c: c1, code: FailedPrecondition,
			row: vol("a", func(r *Row) { r.Type, r.Volsize, r.ShareNFS = zfs.TypeVolume, 1<<30, "-" })},
		{name: "unmounted", c: c1, code: FailedPrecondition,
			row: fsv("a", func(r *Row) { r.Mounted, r.Mount = false, "none" })},
		{name: "unmanaged", c: c1, code: NotFound, row: fsv("a", func(r *Row) { r.Managed = false })},
		{name: "missing", c: c1, code: NotFound, row: fsv("b", nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, row, err := pl.PlanPublishExport(NewTable([]Row{tc.row}), tc.live, "a", tc.c)
			if CodeOf(err) != tc.code {
				t.Fatalf("err %v, want code %d", err, tc.code)
			}
			if !reflect.DeepEqual(plan, tc.want) {
				t.Fatalf("plan %+v\nwant %+v", plan, tc.want)
			}
			if err == nil && row.Mount != "/tank/v/a" {
				t.Fatalf("export path %q", row.Mount)
			}
		})
	}
}

func TestPlanUnpublishExport(t *testing.T) {
	two := exported("a", "10.0.0.1=rw,10.0.0.2=ro", "rw=10.0.0.1,ro=10.0.0.2")
	live := etab("/tank/v/a\t10.0.0.1(rw)\n/tank/v/a\t10.0.0.2(ro)\n")
	set := func(props map[string]string) Plan {
		return Plan{{Kind: OpSet, Target: "tank/v/a", Props: props}}
	}
	cases := []struct {
		name string
		row  Row
		live Etab
		host string
		want Plan
		code Code
	}{
		{name: "one of two", row: two, live: live, host: "10.0.0.2",
			want: set(map[string]string{clientsProp: "10.0.0.1=rw", "sharenfs": "rw=10.0.0.1"})},
		// never inherit: the parent may be shared to everyone
		{name: "last sets off locally", row: exported("a", "10.0.0.1=rw", "rw=10.0.0.1"), live: live, host: "10.0.0.1",
			want: set(map[string]string{clientsProp: "none", "sharenfs": "off"})},
		{name: "not a client", row: two, live: live, host: "10.0.0.3"},
		{name: "already removed", row: exported("a", "none", "off"), host: "10.0.0.1"},
		{name: "removed but still served: reshare", row: exported("a", "none", "off"), live: live, host: "10.0.0.1",
			want: set(map[string]string{"sharenfs": "off"})},
		{name: "served by a foreign sharenfs", host: "10.0.0.1", live: live, code: FailedPrecondition,
			row: fsv("a", func(r *Row) { r.ShareNFS = "rw=10.0.0.1" })},
		{name: "foreign but not served", host: "10.0.0.9", live: live,
			row: fsv("a", func(r *Row) { r.ShareNFS = "rw=10.0.0.1" })},
		{name: "unmounted still revokes", host: "10.0.0.2",
			row:  exported("a", "10.0.0.2=ro", "ro=10.0.0.2"),
			want: set(map[string]string{clientsProp: "none", "sharenfs": "off"})},
		{name: "zvol", host: "10.0.0.1", row: vol("a", func(r *Row) { r.Type = zfs.TypeVolume })},
		{name: "missing", host: "10.0.0.1", row: fsv("b", nil)},
		{name: "corrupt", host: "10.0.0.1", row: exported("a", "x", "off"), code: FailedPrecondition},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, _, err := pl.PlanUnpublishExport(NewTable([]Row{tc.row}), tc.live, "a", tc.host)
			if CodeOf(err) != tc.code {
				t.Fatalf("err %v, want code %d", err, tc.code)
			}
			if !reflect.DeepEqual(plan, tc.want) {
				t.Fatalf("plan %+v\nwant %+v", plan, tc.want)
			}
		})
	}
}

// applyRow plays a plan's OpSet onto a row and the export table the way a
// working NFS server would: the property test below needs no zfs.
func applyRow(r Row, plan Plan) (Row, Etab) {
	for _, op := range plan {
		if v, ok := op.Props[clientsProp]; ok {
			r.NFSClients, r.NFSClientsSet = v, true
		}
		if v, ok := op.Props["sharenfs"]; ok {
			r.ShareNFS, r.ShareNFSLocal = v, true
		}
	}
	e := Etab{}
	cs, _ := decodeClients(r.NFSClients)
	if r.ShareNFS == ShareNFS(cs) && r.ShareNFS != "off" {
		e[r.Mount] = cs
	}
	return r, e
}

// Publish then publish again is an empty plan; so is unpublish twice; and
// the table reflects exactly the published clients.
func TestExportConverges(t *testing.T) {
	steps := []struct {
		host    string
		ro, del bool
	}{
		{host: "10.0.0.1"}, {host: "node-a", ro: true}, {host: "10.1.0.0/16"},
		{host: "node-a", del: true}, {host: "10.0.0.1", del: true}, {host: "10.1.0.0/16", del: true},
	}
	row, live := fsv("a", nil), Etab{}
	want := map[string]bool{}
	for _, s := range steps {
		for i := range 2 {
			var plan Plan
			var err error
			if s.del {
				plan, _, err = pl.PlanUnpublishExport(NewTable([]Row{row}), live, "a", s.host)
			} else {
				plan, _, err = pl.PlanPublishExport(NewTable([]Row{row}), live, "a", NFSClient{Host: s.host, ReadOnly: s.ro})
			}
			if err != nil {
				t.Fatalf("%+v: %v", s, err)
			}
			if i == 1 && len(plan) != 0 {
				t.Fatalf("%+v: repeat planned %+v", s, plan)
			}
			row, live = applyRow(row, plan)
		}
		if s.del {
			delete(want, s.host)
		} else {
			want[s.host] = s.ro
		}
		got := map[string]bool{}
		for _, c := range live[row.Mount] {
			got[c.Host] = c.ReadOnly
		}
		if !maps.Equal(got, want) {
			t.Fatalf("after %+v: served %v, want %v", s, got, want)
		}
	}
	if row.ShareNFS != "off" || !row.ShareNFSLocal {
		t.Fatalf("final sharenfs %q local=%v", row.ShareNFS, row.ShareNFSLocal)
	}
}
