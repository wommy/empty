package endpoint

import (
	"fmt"
	"maps"
	"reflect"
	"strings"
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
		r.Mount, r.Mounted, r.CanMount = "/tank/v/"+id, true, "on"
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
		"10.0.0.1":       "10.0.0.1/32",
		"10.0.0.1/32":    "10.0.0.1/32",
		"10.0.0.0/8":     "10.0.0.0/8",
		"192.168.0.0/24": "192.168.0.0/24",
		"128.0.0.0/1":    "128.0.0.0/1", // syntax only; Publish applies the floor
	}
	for in, want := range ok {
		c, err := ParseClient(in, false)
		if err != nil || c.Host != want {
			t.Errorf("ParseClient(%q) = %q, %v; want %q", in, c.Host, err, want)
		}
	}
	bad := []string{
		// hostnames, wildcards, netgroups: nfs-utils resolves them on every exportfs
		"node-a", "Node-A", "node-a.example", "k8s-worker-01.lan", "*", "*.example", "@netgroup",
		// separators and exportfs syntax
		"", "a,b", "a:b", "rw=x", "x(rw)", "a b", "-o", "-host", "$(reboot)", "a;b", "a\nb", "no_root_squash,rw=*",
		// IPv6 (IPv4 only for now), including the mapped and bracketed forms
		"::1", "[::1]", "fe80::/64", "::ffff:10.0.0.1", "fd00::5/128",
		// not canonical: host bits, partial or inet_aton spellings, leading zeros
		"10.1.2.3/8", "10.0.0.1/33", "10.0.0", "1.2.3.4.5", "010.0.0.1", "10.0.0.0x5", "0x7f.0x1", "0xa000005",
		"10.0.0.1/032", " 10.0.0.1", "10.0.0.1/", "/8",
		// everyone
		"0.0.0.0/0", "10.0.0.0/0",
		string(make([]byte, 300)),
	}
	for _, in := range bad {
		if _, err := ParseClient(in, false); CodeOf(err) != InvalidArgument {
			t.Errorf("ParseClient(%q): %v, want InvalidArgument", in, err)
		}
	}
}

func TestCheckFloor(t *testing.T) {
	for host, ok := range map[string]bool{"10.0.0.0/16": true, "10.0.0.1/32": true, "10.0.0.0/15": false, "128.0.0.0/1": false} {
		c, err := ParseClient(host, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := checkFloor(c, 16); (err == nil) != ok {
			t.Errorf("checkFloor(%s, 16) = %v", host, err)
		}
	}
}

func TestShareNFSAndClientEncoding(t *testing.T) {
	cs := []NFSClient{{Host: "10.10.0.0/16"}, {Host: "10.0.0.0/16", ReadOnly: true}, {Host: "10.9.0.5/32"}}
	// by address, not by spelling: 10.9 sorts before 10.10
	if got := ShareNFS(cs); got != "rw=10.9.0.5/32:10.10.0.0/16,ro=10.0.0.0/16" {
		t.Fatalf("ShareNFS = %q", got)
	}
	if got := ShareNFS(nil); got != "off" {
		t.Fatalf("ShareNFS(nil) = %q", got)
	}
	if got := ShareNFS(cs[1:2]); got != "ro=10.0.0.0/16" {
		t.Fatalf("ro only = %q", got)
	}
	enc := encodeClients(cs)
	if enc != "10.0.0.0/16=ro,10.9.0.5/32=rw,10.10.0.0/16=rw" {
		t.Fatalf("encode = %q", enc)
	}
	back, err := decodeClients(enc)
	if err != nil || !reflect.DeepEqual(back, sortClients(cs)) {
		t.Fatalf("decode = %v %v", back, err)
	}
	if back, err := decodeClients("none"); err != nil || back != nil {
		t.Fatalf("none = %v %v", back, err)
	}
	for _, s := range []string{
		"", "a", "a=rw", "10.0.0.1/32=rx", "10.0.0.2/32=rw,10.0.0.1/32=rw", "*=rw", "10.0.0.1/32=rw,,10.0.0.2/32=rw",
		"10.0.0.1=rw", // legacy bare address: not canonical
		// F14: duplicates, in either mode, and overlapping networks
		"10.0.0.1/32=rw,10.0.0.1/32=rw", "10.0.0.1/32=ro,10.0.0.1/32=rw", "10.0.0.1/32=rw,10.0.0.1/32=ro",
		"10.0.0.0/24=rw,10.0.0.5/32=ro",
	} {
		if _, err := decodeClients(s); err == nil {
			t.Errorf("decodeClients(%q) accepted", s)
		}
	}
}

// ADR 0004, csi-nfs-design §2: the canonical grammar. Exactly the values
// ShareNFS writes parse; anything else (options, names, "*", empty hosts,
// overlaps, another order) is someone else's.
func TestParseShareNFS(t *testing.T) {
	t.Parallel()
	ok := map[string][]NFSClient{
		"off":                           nil,
		"rw=10.0.0.1/32":                {{Host: "10.0.0.1/32"}},
		"ro=10.0.0.0/16":                {{Host: "10.0.0.0/16", ReadOnly: true}},
		"rw=10.9.0.5/32:10.10.0.0/16":   {{Host: "10.9.0.5/32"}, {Host: "10.10.0.0/16"}},
		"rw=10.0.0.1/32,ro=10.1.0.0/16": {{Host: "10.0.0.1/32"}, {Host: "10.1.0.0/16", ReadOnly: true}},
		"rw=10.0.0.0/8":                 {{Host: "10.0.0.0/8"}}, // below the default floor: still ours
	}
	for s, want := range ok {
		got, canonical := ParseShareNFS(s)
		if !canonical || !reflect.DeepEqual(got, want) {
			t.Errorf("ParseShareNFS(%q) = %v, %v; want %v", s, got, canonical, want)
		}
		if ShareNFS(got) != s {
			t.Errorf("ShareNFS(ParseShareNFS(%q)) = %q", s, ShareNFS(got))
		}
	}
	for _, s := range []string{
		"", "on", "rw", "ro", "rw=", "rw=*", "rw=10.0.0.0/24:", "rw=:10.0.0.0/24", "rw=10.0.0.0/24::10.1.0.0/24",
		"rw=10.0.0.1", "rw=node-a", "rw=@10.0.0.0/8", "rw=10.0.0.1/24", "rw=0.0.0.0/0", "rw=010.0.0.0/8",
		"rw=10.0.0.1/32,no_root_squash", "sec=sys,rw=10.0.0.1/32", "rw=10.0.0.1/32,sec=krb5,ro=10.1.0.0/16",
		"ro=10.1.0.0/16,rw=10.0.0.1/32", "rw=10.0.0.1/32,rw=10.0.0.2/32", "rw=10.10.0.0/16:10.9.0.5/32",
		"rw=10.0.0.0/24,ro=10.0.0.5/32", "rw=10.0.0.1/32:10.0.0.1/32", "RW=10.0.0.1/32", "rw=10.0.0.1/32 ",
		"rw=fd00::/64", "Off",
	} {
		if cs, canonical := ParseShareNFS(s); canonical {
			t.Errorf("ParseShareNFS(%q) accepted as %v", s, cs)
		}
	}
}

// F14: the order is total, so equal hosts in different modes sort the same
// whatever the input order, and the canonical check cannot flip.
func TestSortClientsIsTotal(t *testing.T) {
	a := []NFSClient{{Host: "10.0.0.1/32", ReadOnly: true}, {Host: "10.0.0.1/32"}, {Host: "10.0.0.0/24"}}
	b := []NFSClient{a[1], a[2], a[0]}
	if x, y := sortClients(a), sortClients(b); !reflect.DeepEqual(x, y) {
		t.Fatalf("%v != %v", x, y)
	}
}

func TestParseEtab(t *testing.T) {
	e := etab("/tank/v/a\t10.0.0.1/32(rw,sync,wdelay,root_squash,sec=sys,rw)\n" +
		"/tank/v/a\t10.9.0.0/16(ro,sync)\n" +
		"/tank/v/a\tnode-a.evil.example(ro,sync)\n" +
		"/tank/v/a\t10.0.0.2(rw,sync)\n" +
		"/tank/v/a\t10.0.0.3/32(\n" +
		"/tank/v/a\t10.0.0.4/32()\n" +
		"/tank/v/a\t10.0.0.6/32(sync,rw)\n" +
		"/tank/with\\040space\t10.0.0.1/32(rw,sync)\n" +
		"garbage line\n")
	// compared by network: a bare request matches its /32 subnet line
	for _, h := range []string{"10.0.0.1", "10.0.0.1/32"} {
		if c, ok := e.Lookup("/tank/v/a", h); !ok || c.ReadOnly {
			t.Fatalf("rw client %s: %v %v", h, c, ok)
		}
	}
	if c, ok := e.Lookup("/tank/v/a", "10.9.0.0/16"); !ok || !c.ReadOnly {
		t.Fatalf("ro subnet: %v %v", c, ok)
	}
	for _, h := range []string{
		"node-a",      // F3: no prefix rule
		"10.9.1.0/24", // contained in a served network, but not that network
		"10.0.0.2",    // a bare etab address is nfs-utils' FQDN class, never ours
		"10.0.0",
	} {
		if c, ok := e.Lookup("/tank/v/a", h); ok {
			t.Errorf("Lookup(%q) matched %v", h, c)
		}
	}
	// F13: no mode is served (Unpublish must see it) but never in a mode
	for _, h := range []string{"10.0.0.3", "10.0.0.4", "10.0.0.6"} {
		if _, ok := e.Lookup("/tank/v/a", h); !ok {
			t.Errorf("%s: line not seen", h)
		}
		for _, ro := range []bool{false, true} {
			if c, _ := ParseClient(h, ro); e.serves("/tank/v/a", c) {
				t.Errorf("%s with no leading mode verifies as served ro=%v", h, ro)
			}
		}
	}
	if _, ok := e.Lookup("/tank/with space", "10.0.0.1"); !ok {
		t.Fatalf("octal path: %v", e)
	}
	if len(e) != 2 {
		t.Fatalf("paths %v", e)
	}
}

// D2 (wave9 nfs-diff): serves confirms a client's mode only when mountd
// would answer with our line: nfs-utils cache.c lookup_export takes the
// first match by client class (an fqdn line beats every subnet line), then
// by etab order, and the kernel enforces every option on that line.
func TestEtabServesPrecedence(t *testing.T) {
	t.Parallel()
	const p = "/tank/v/a"
	// what exportfs writes for sharenfs=rw=10.0.0.5/32
	own := p + "\t10.0.0.5/32(rw,sync,wdelay,hide,nocrossmnt,secure,root_squash,no_all_squash,no_subtree_check," +
		"secure_locks,acl,no_pnfs,mountpoint,anonuid=65534,anongid=65534,sec=sys,rw,secure,root_squash,no_all_squash)\n"
	line := func(host, opts string) string { return p + "\t" + host + "(" + opts + ")\n" }
	cases := []struct {
		name, etab string
		ro, want   bool
	}{
		{"own line", own, false, true},
		{"own line, other mode", own, true, false},
		{"later overlapping subnet", own + line("10.0.0.0/24", "ro"), false, true},
		{"earlier overlapping subnet", line("10.0.0.0/24", "ro") + own, false, false},
		{"earlier subnet spelled /024", line("10.0.0.0/024", "ro") + own, false, false},
		{"earlier dotted mask", line("10.0.0.0/255.255.255.0", "ro") + own, false, false},
		{"earlier disjoint subnet", line("10.9.0.0/16", "ro") + own, false, true},
		{"hostname line after ours", own + line("node-a.example", "ro"), false, false},
		{"client's bare address after ours", own + line("10.0.0.5", "ro"), false, false},
		{"another bare address", own + line("10.0.0.6", "ro"), false, true},
		{"anonymous after ours", own + line("*", "ro"), false, true},
		{"netgroup after ours", own + line("@ops", "ro"), false, true},
		{"gss line", line("gss/krb5", "ro") + own, false, true},
		{"per-flavor rw under a leading ro", line("10.0.0.5/32", "ro,sec=sys,rw"), true, false},
		{"per-flavor ro under a leading rw", line("10.0.0.5/32", "rw,sec=sys,ro"), false, false},
		{"no_root_squash", line("10.0.0.5/32", "rw,no_root_squash"), false, false},
		{"insecure", line("10.0.0.5/32", "rw,insecure"), false, false},
		{"anonuid=0", line("10.0.0.5/32", "rw,anonuid=0"), false, false},
		{"crossmnt", line("10.0.0.5/32", "rw,crossmnt"), false, false},
		{"sec=krb5", line("10.0.0.5/32", "rw,sec=krb5,rw"), false, false},
		{"sec=sys:krb5", line("10.0.0.5/32", "rw,sec=sys:krb5,rw"), false, false},
		{"unknown option", line("10.0.0.5/32", "rw,bogus"), false, false},
		{"narrowing options", line("10.0.0.5/32", "rw,all_squash,subtree_check,fsid=7,sec=sys,rw"), false, true},
	}
	for _, tc := range cases {
		c, err := ParseClient("10.0.0.5/32", tc.ro)
		if err != nil {
			t.Fatal(err)
		}
		if got := etab(tc.etab).serves(p, c); got != tc.want {
			t.Errorf("%s: serves(%s ro=%v) = %v, want %v\n%s", tc.name, c.Host, tc.ro, got, tc.want, tc.etab)
		}
	}
}

func TestPlanPublishExport(t *testing.T) {
	t.Parallel()
	c1 := NFSClient{Host: "10.0.0.1/32"}
	live1 := etab("/tank/v/a\t10.0.0.1/32(rw,sync)\n")
	set := func(props map[string]string) Plan {
		return Plan{{Kind: OpSet, Target: "tank/v/a", Props: props}}
	}
	var many []NFSClient
	for i := range 500 {
		many = append(many, NFSClient{Host: fmt.Sprintf("10.%d.%d.1/32", i/250, i%250)})
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
			want: set(map[string]string{clientsProp: "10.0.0.1/32=rw", "sharenfs": "rw=10.0.0.1/32"})},
		{name: "repeat is empty", row: exported("a", "10.0.0.1/32=rw", "rw=10.0.0.1/32"), live: live1, c: c1},
		{name: "repeat, not served: reshare", row: exported("a", "10.0.0.1/32=rw", "rw=10.0.0.1/32"), c: c1,
			want: set(map[string]string{"sharenfs": "rw=10.0.0.1/32"})},
		{name: "served in the other mode: reshare", row: exported("a", "10.0.0.1/32=rw", "rw=10.0.0.1/32"),
			live: etab("/tank/v/a\t10.0.0.1/32(ro)\n"), c: c1, want: set(map[string]string{"sharenfs": "rw=10.0.0.1/32"})},
		{name: "served with no mode: reshare", row: exported("a", "10.0.0.1/32=rw", "rw=10.0.0.1/32"),
			live: etab("/tank/v/a\t10.0.0.1/32()\n"), c: c1, want: set(map[string]string{"sharenfs": "rw=10.0.0.1/32"})},
		{name: "served only under a name: reshare", row: exported("a", "10.0.0.1/32=rw", "rw=10.0.0.1/32"),
			live: etab("/tank/v/a\tnode-a.example(rw)\n"), c: c1, want: set(map[string]string{"sharenfs": "rw=10.0.0.1/32"})},
		{name: "second client", row: exported("a", "10.0.0.1/32=rw", "rw=10.0.0.1/32"), live: live1,
			c:    NFSClient{Host: "10.9.0.0/16", ReadOnly: true},
			want: set(map[string]string{clientsProp: "10.0.0.1/32=rw,10.9.0.0/16=ro", "sharenfs": "rw=10.0.0.1/32,ro=10.9.0.0/16"})},
		{name: "other mode", row: exported("a", "10.0.0.1/32=rw", "rw=10.0.0.1/32"), live: live1,
			c: NFSClient{Host: "10.0.0.1/32", ReadOnly: true}, code: AlreadyExists},
		// nfs-utils applies the first matching line: both modes cannot hold
		{name: "inside an exported network", row: exported("a", "10.0.0.0/24=rw", "rw=10.0.0.0/24"),
			c: NFSClient{Host: "10.0.0.5/32", ReadOnly: true}, code: FailedPrecondition},
		{name: "around an exported host", row: exported("a", "10.0.0.5/32=ro", "ro=10.0.0.5/32"),
			c: NFSClient{Host: "10.0.0.0/24"}, code: FailedPrecondition},
		// an edit inside the grammar is accepted as truth (ADR 0004): the
		// stored list follows it, sharenfs is not rewritten
		{name: "in-grammar edit is accepted", row: exported("a", "10.0.0.1/32=rw", "rw=10.0.0.1/32:10.6.6.6/32"), live: live1, c: c1,
			want: set(map[string]string{clientsProp: "10.0.0.1/32=rw,10.6.6.6/32=rw"})},
		{name: "inherited sharenfs is pinned", c: c1, live: etab("/tank/v/a\t*(rw)\n"),
			row:  fsv("a", func(r *Row) { r.ShareNFS, r.ShareNFSLocal = "on", false }),
			want: set(map[string]string{clientsProp: "10.0.0.1/32=rw", "sharenfs": "rw=10.0.0.1/32"})},
		// D1 (wave9 nfs-diff): an ancestor exported with crossmnt (an
		// inherited sharenfs=on is "rw,crossmnt") serves this mount to its
		// own clients, whatever the volume's sharenfs says
		{name: "crossmnt ancestor", row: fsv("a", nil), c: c1, code: FailedPrecondition,
			live: etab("/tank/v\t*(rw,sync,crossmnt,sec=sys,rw)\n")},
		{name: "crossmnt root", row: fsv("a", nil), c: c1, code: FailedPrecondition,
			live: etab("/\t10.0.0.0/8(ro,crossmnt)\n")},
		{name: "ancestor without crossmnt", row: fsv("a", nil), c: c1,
			live: etab("/tank/v\t*(rw,nocrossmnt)\n/tank/v/ab\t*(rw,crossmnt)\n/tank/v/a/b\t*(rw,crossmnt)\n"),
			want: set(map[string]string{clientsProp: "10.0.0.1/32=rw", "sharenfs": "rw=10.0.0.1/32"})},
		// D2: served, but beyond the request: re-share once (the caller
		// reports what a re-share cannot fix)
		{name: "served with no_root_squash: reshare", row: exported("a", "10.0.0.1/32=rw", "rw=10.0.0.1/32"),
			live: etab("/tank/v/a\t10.0.0.1/32(rw,no_root_squash)\n"), c: c1, want: set(map[string]string{"sharenfs": "rw=10.0.0.1/32"})},
		{name: "served to more clients: reshare", row: exported("a", "10.0.0.1/32=rw", "rw=10.0.0.1/32"),
			live: etab("/tank/v/a\t10.0.0.1/32(rw)\n/tank/v/a\t*(ro)\n"), c: c1, want: set(map[string]string{"sharenfs": "rw=10.0.0.1/32"})},
		// wave12 S7: a later version keeps export state in sharenfs alone
		// (ADR 0004). After a rollback its volumes have a canonical local
		// sharenfs and no stored list, and are still the endpoint's
		{name: "canonical sharenfs without a stored list is ours", c: NFSClient{Host: "10.0.0.2/32"}, live: live1,
			row:  fsv("a", func(r *Row) { r.ShareNFS = "rw=10.0.0.1/32" }),
			want: set(map[string]string{clientsProp: "10.0.0.1/32=rw,10.0.0.2/32=rw", "sharenfs": "rw=10.0.0.1/32:10.0.0.2/32"})},
		// ... and a stale stored list never wins over the served value: the
		// later version granted 10.0.0.2 without updating it
		{name: "stale stored list: sharenfs wins", c: c1, live: etab("/tank/v/a\t10.0.0.1/32(rw)\n/tank/v/a\t10.0.0.2/32(rw)\n"),
			row:  exported("a", "10.0.0.1/32=rw", "rw=10.0.0.1/32:10.0.0.2/32"),
			want: set(map[string]string{clientsProp: "10.0.0.1/32=rw,10.0.0.2/32=rw"})},
		{name: "foreign local sharenfs", c: c1, code: FailedPrecondition,
			row: fsv("a", func(r *Row) { r.ShareNFS = "rw=@10.0.0.0/8,no_root_squash" })},
		// a received value is the sending side's export: never shadowed
		{name: "received sharenfs", c: c1, code: FailedPrecondition,
			row: fsv("a", func(r *Row) { r.ShareNFS, r.ShareNFSLocal, r.ShareNFSRecv = "rw=10.0.0.0/8", false, true })},
		// `zfs inherit -S sharenfs` reverted to the received value
		{name: "received sharenfs over our list", c: c1, code: FailedPrecondition, live: live1,
			row: func() Row {
				r := exported("a", "10.0.0.1/32=rw", "on")
				r.ShareNFSLocal, r.ShareNFSRecv = false, true
				return r
			}()},
		{name: "received off is pinned", c: c1,
			row:  fsv("a", func(r *Row) { r.ShareNFSLocal, r.ShareNFSRecv = false, true }),
			want: set(map[string]string{clientsProp: "10.0.0.1/32=rw", "sharenfs": "rw=10.0.0.1/32"})},
		// the stored list is read only when sharenfs is not canonical
		{name: "corrupt client list", c: c1, code: FailedPrecondition, row: func() Row {
			r := exported("a", "x=yes", "off")
			r.ShareNFSLocal = false
			return r
		}()},
		{name: "duplicate client list", c: c1, code: FailedPrecondition, row: func() Row {
			r := exported("a", "10.0.0.1/32=rw,10.0.0.1/32=rw", "rw=10.0.0.1/32")
			r.ShareNFSLocal = false
			return r
		}()},
		{name: "zvol", c: c1, code: FailedPrecondition,
			row: vol("a", func(r *Row) { r.Type, r.Volsize, r.ShareNFS = zfs.TypeVolume, 1<<30, "-" })},
		// canmount=on: setting sharenfs mounts and serves it
		{name: "unmounted, canmount=on", c: c1, row: fsv("a", func(r *Row) { r.Mounted = false }),
			want: set(map[string]string{clientsProp: "10.0.0.1/32=rw", "sharenfs": "rw=10.0.0.1/32"})},
		{name: "unmounted, canmount=noauto", c: c1, code: FailedPrecondition,
			row: fsv("a", func(r *Row) { r.Mounted, r.CanMount = false, "noauto" })},
		{name: "mountpoint none", c: c1, code: FailedPrecondition,
			row: fsv("a", func(r *Row) { r.Mounted, r.Mount = false, "none" })},
		{name: "too many clients", row: exported("a", encodeClients(many), ShareNFS(many)),
			c: NFSClient{Host: "10.200.0.1/32"}, code: ResourceExhausted},
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
	// a malformed id names no volume: NotFound, like the other volume calls
	_, _, err := pl.PlanPublishExport(NewTable(nil), nil, "a/b", c1)
	if CodeOf(err) != NotFound {
		t.Fatalf("malformed id: %v", err)
	}
}

// F7: the limit is ZFS_MAXPROPLEN-1 on sharenfs and on the stored list,
// which costs more per client (",host=rw" versus ":host").
func TestExportSizeLimit(t *testing.T) {
	var cs []NFSClient
	for i := 0; len(ShareNFS(cs)) <= maxPropLen; i++ {
		cs = append(cs, NFSClient{Host: fmt.Sprintf("10.%d.%d.%d/32", i/62500, i/250%250, i%250)})
	}
	if maxPropLen != 8191 {
		t.Fatalf("maxPropLen %d", maxPropLen)
	}
	// both under the limit: planned
	n := 0
	for len(encodeClients(cs[:n+1])) <= maxPropLen {
		n++
	}
	if _, err := pl.exportOps(fsv("a", nil), cs[:n]); err != nil {
		t.Fatalf("%d clients: %v", n, err)
	}
	// sharenfs still fits, the stored list does not
	if len(ShareNFS(cs[:n+1])) > maxPropLen {
		t.Fatalf("test setup: sharenfs %d", len(ShareNFS(cs[:n+1])))
	}
	if _, err := pl.exportOps(fsv("a", nil), cs[:n+1]); CodeOf(err) != ResourceExhausted || !strings.Contains(err.Error(), "nfs_clients") {
		t.Fatalf("%d clients: %v", n+1, err)
	}
}

func TestPlanUnpublishExport(t *testing.T) {
	t.Parallel()
	two := exported("a", "10.0.0.1/32=rw,10.0.0.2/32=ro", "rw=10.0.0.1/32,ro=10.0.0.2/32")
	live := etab("/tank/v/a\t10.0.0.1/32(rw)\n/tank/v/a\t10.0.0.2/32(ro)\n")
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
			want: set(map[string]string{clientsProp: "10.0.0.1/32=rw", "sharenfs": "rw=10.0.0.1/32"})},
		{name: "spelled as /32", row: two, live: live, host: "10.0.0.2/32",
			want: set(map[string]string{clientsProp: "10.0.0.1/32=rw", "sharenfs": "rw=10.0.0.1/32"})},
		// never inherit: the parent may be shared to everyone
		{name: "last sets off locally", row: exported("a", "10.0.0.1/32=rw", "rw=10.0.0.1/32"), live: live, host: "10.0.0.1",
			want: set(map[string]string{clientsProp: "none", "sharenfs": "off"})},
		{name: "not a client", row: two, live: live, host: "10.0.0.3"},
		{name: "network around a client is not that client", row: two, live: live, host: "10.0.0.0/24"},
		{name: "already removed", row: exported("a", "none", "off"), host: "10.0.0.1"},
		{name: "removed but still served: reshare", row: exported("a", "none", "off"), live: live, host: "10.0.0.1",
			want: set(map[string]string{"sharenfs": "off"})},
		{name: "served by a foreign sharenfs", host: "10.0.0.1", live: live, code: FailedPrecondition,
			row: fsv("a", func(r *Row) { r.ShareNFS = "rw=10.0.0.1/32,no_root_squash" })},
		// wave12 S7: canonical without a stored list is ours, so a detach
		// after a rollback revokes instead of failing for good
		{name: "canonical sharenfs without a stored list is ours", host: "10.0.0.1", live: live,
			row:  fsv("a", func(r *Row) { r.ShareNFS = "rw=10.0.0.1/32,ro=10.0.0.2/32" }),
			want: set(map[string]string{clientsProp: "10.0.0.2/32=ro", "sharenfs": "ro=10.0.0.2/32"})},
		{name: "stale stored list: sharenfs wins", host: "10.0.0.1", live: live,
			row:  exported("a", "10.0.0.1/32=rw", "rw=10.0.0.1/32,ro=10.0.0.2/32"),
			want: set(map[string]string{clientsProp: "10.0.0.2/32=ro", "sharenfs": "ro=10.0.0.2/32"})},
		{name: "served by a received sharenfs", host: "10.0.0.1", live: live, code: FailedPrecondition,
			row: fsv("a", func(r *Row) { r.ShareNFS, r.ShareNFSLocal, r.ShareNFSRecv = "rw=10.0.0.1/32", false, true })},
		// D3 (wave9 nfs-diff): a line beyond the stored list that can
		// match the host still serves it; on a volume whose sharenfs is
		// ours it is rewritten, not reported gone. "Ours" is today's
		// nfs_clients model, as in "drifted sharenfs is rewritten" above;
		// when nfs_clients goes (ADR 0004) "rw=*" is foreign and this
		// case becomes a FailedPrecondition
		{name: "our sharenfs widened to *: rewritten", host: "10.0.0.1",
			row: exported("a", "10.0.0.2/32=ro", "rw=*"), live: etab("/tank/v/a\t*(rw)\n"),
			want: set(map[string]string{"sharenfs": "ro=10.0.0.2/32"})},
		{name: "inherited world export after the last client: pinned off", host: "10.0.0.1",
			row: func() Row {
				r := exported("a", "none", "rw=")
				r.ShareNFSLocal = false
				return r
			}(),
			live: etab("/tank/v/a\t*(rw)\n"), want: set(map[string]string{"sharenfs": "off"})},
		{name: "foreign sharenfs to * is not ours to revoke", host: "10.0.0.1", live: etab("/tank/v/a\t*(rw)\n"),
			row: fsv("a", func(r *Row) { r.ShareNFS = "rw=*" })},
		{name: "foreign but not served", host: "10.0.0.9", live: live,
			row: fsv("a", func(r *Row) { r.ShareNFS = "rw=10.0.0.1/32,no_root_squash" })},
		{name: "unmounted still revokes", host: "10.0.0.2",
			row:  exported("a", "10.0.0.2/32=ro", "ro=10.0.0.2/32"),
			want: set(map[string]string{clientsProp: "none", "sharenfs": "off"})},
		{name: "zvol", host: "10.0.0.1", row: vol("a", func(r *Row) { r.Type = zfs.TypeVolume })},
		{name: "missing", host: "10.0.0.1", row: fsv("b", nil)},
		{name: "corrupt", host: "10.0.0.1", row: func() Row {
			r := exported("a", "x", "off")
			r.ShareNFSLocal = false
			return r
		}(), code: FailedPrecondition},
		{name: "hostname", host: "node-a", row: two, live: live, code: InvalidArgument},
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
	// a malformed id names no volume, so nothing is exported: done
	if plan, _, err := pl.PlanUnpublishExport(NewTable(nil), nil, "a/b", "10.0.0.1"); err != nil || plan != nil {
		t.Fatalf("malformed id: %v %v", plan, err)
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
		{host: "10.0.0.1/32"}, {host: "10.2.0.0/16", ro: true}, {host: "10.1.0.0/16"},
		{host: "10.2.0.0/16", del: true}, {host: "10.0.0.1/32", del: true}, {host: "10.1.0.0/16", del: true},
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
