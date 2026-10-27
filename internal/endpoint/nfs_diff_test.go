package endpoint

// Differential tests of the NFS export chain (wave9 nfs-diff): our encoder
// (ShareNFS), classifier (foreign, exportRow) and verifier (ParseEtab,
// serves, exposure, covers) against the reference models of OpenZFS
// libshare, nfs-utils exportfs and mountd in internal/nfsref, and the
// simulator against the same models.

import (
	"context"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/wommy/empty/internal/nfsref"
	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

const diffPath = "/tank/v/a"

// gen draws choices from fuzz input; past its end every draw is 0.
type gen struct {
	b []byte
	i int
}

func (g *gen) byte() byte {
	if g.i >= len(g.b) {
		return 0
	}
	c := g.b[g.i]
	g.i++
	return c
}

func (g *gen) intn(n int) int { return int(g.byte()) % n }

func pick[T any](g *gen, xs ...T) T { return xs[g.intn(len(xs))] }

// client is a canonical client in a small space, so networks overlap.
func (g *gen) client(floor int) NFSClient {
	bits := max(pick(g, 16, 20, 24, 28, 30, 31, 32, 32), floor)
	a := netip.AddrFrom4([4]byte{10, byte(g.intn(3)), byte(g.intn(3)), byte(g.intn(8))})
	return NFSClient{Host: netip.PrefixFrom(a, bits).Masked().String(), ReadOnly: g.intn(2) == 1}
}

// clients is a list decodeClients accepts: canonical, no overlaps.
func (g *gen) clients() []NFSClient {
	var cs []NFSClient
	for range g.intn(5) {
		c := g.client(DefaultNFSMinPrefix)
		if _, ok := overlap(cs, c); !ok {
			cs = append(cs, c)
		}
	}
	return sortClients(cs)
}

// refChain runs the reference chain: zfs.exports (and optionally
// /etc/exports and other exports.d files) through `exportfs -ra`, then
// mountd's view of the resulting etab.
type refChain struct {
	etab   string
	mountd *nfsref.Table
	diags  []nfsref.Diag
}

func runRef(etcExports string, dir map[string]string) refChain {
	files := nfsref.ExportsFiles{Dir: map[string][]byte{}}
	if etcExports != "" {
		files.Exports = []byte(etcExports)
	}
	for k, v := range dir {
		files.Dir[k] = []byte(v)
	}
	etab, t := nfsref.ExportfsRA(files, nil, nil)
	return refChain{etab: etab, mountd: nfsref.LoadEtab(etab, nil), diags: t.Diags}
}

// shareFile is zfs.exports after `zfs set sharenfs` steps (mountpoint,
// value) on mounted filesystems: "off" on a dataset without lines does
// not touch the file (changelist_postfix unshares only shared datasets).
func shareFile(t testing.TB, steps ...[2]string) string {
	f := ""
	shared := map[string]bool{}
	for _, s := range steps {
		if s[1] == "off" && !shared[s[0]] {
			continue
		}
		shared[s[0]] = s[1] != "off"
		var err error
		if f, err = nfsref.ShareFile(f, s[0], s[1]); err != nil {
			t.Fatalf("ShareFile(%q, %q): %v", s[0], s[1], err)
		}
	}
	return f
}

// wantMode is what cs grants address a (cs does not overlap).
func wantMode(cs []NFSClient, a netip.Addr) nfsref.Mode {
	for _, c := range cs {
		if p, _ := c.prefix(); p.Contains(a) {
			if c.ReadOnly {
				return nfsref.RO
			}
			return nfsref.RW
		}
	}
	return nfsref.None
}

// probes are addresses that decide what any address of the space gets:
// the ends of every requested and every exported network, their
// neighbours, and addresses outside.
func probes(m *nfsref.Table, cs []NFSClient, extra ...netip.Prefix) []netip.Addr {
	out := []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("10.0.0.0"), netip.MustParseAddr("10.3.0.1"), netip.MustParseAddr("11.0.0.0")}
	ps := slices.Clone(extra)
	for _, c := range cs {
		p, _ := c.prefix()
		ps = append(ps, p)
	}
	ps = append(ps, netip.MustParsePrefix("10.0.0.0/8"))
	for _, p := range ps {
		for _, a := range m.SampleAddrs(p) {
			out = append(out, a, a.Prev(), a.Next())
		}
	}
	return slices.DeleteFunc(out, func(a netip.Addr) bool { return !a.IsValid() })
}

// narrow reports whether the export mountd answers with gives no more than
// our sharenfs line does: root squashed to 65534, secure port, no crossmnt
// or nohide, NLM auth, AUTH_SYS only, one mode across flavors.
func narrow(l *nfsref.Line) bool {
	e := l.Entry
	if e.Flags&(nfsref.ExpInsecurePort|nfsref.ExpCrossMount|nfsref.ExpNoHide|nfsref.ExpNoAuthNLM) != 0 ||
		e.Flags&nfsref.ExpRootSquash == 0 || e.AnonUID != 65534 || e.AnonGID != 65534 || e.FslocKind != "" {
		return false
	}
	for _, s := range e.Secinfo {
		if s.Num != 1 || s.Flags&nfsref.ExpRootSquash == 0 || s.Flags&nfsref.ExpInsecurePort != 0 ||
			(s.Flags^e.Flags)&nfsref.ExpReadonly != 0 {
			return false
		}
	}
	return true
}

// grantsExactly checks that mountd gives every probe address exactly what
// cs asks, through a narrow export; it returns the first violation.
func grantsExactly(m *nfsref.Table, path string, cs []NFSClient, extra ...netip.Prefix) string {
	for _, a := range probes(m, cs, extra...) {
		g := m.Access(path, a, nfsref.Env{})
		if want := wantMode(cs, a); g.Mode != want {
			return fmt.Sprintf("%s gets %v, want %v", a, g.Mode, want)
		}
		if g.Export != nil && !narrow(g.Export) {
			return fmt.Sprintf("%s answered by a broad export %s", a, nfsref.PutExportent(g.Export.Entry))
		}
	}
	if x := m.Exposure(path, prefixesOf(cs), nfsref.Env{}); len(x) > 0 {
		return fmt.Sprintf("exposed beyond the request: %s", nfsref.PutExportent(x[0].Entry))
	}
	return ""
}

// failOpen is grantsExactly restricted to grants beyond cs: an address
// that gets access cs does not give it, rw where cs says ro, or access
// through a broad export.
func failOpen(m *nfsref.Table, path string, cs []NFSClient) string {
	for _, a := range probes(m, cs) {
		g := m.Access(path, a, nfsref.Env{})
		want := wantMode(cs, a)
		if g.Mode == nfsref.None {
			continue
		}
		if want == nfsref.None || g.Mode == nfsref.Unknown || (want == nfsref.RO && g.Mode == nfsref.RW) {
			return fmt.Sprintf("%s gets %v, want %v", a, g.Mode, want)
		}
		if g.Export != nil && !narrow(g.Export) {
			return fmt.Sprintf("%s answered by a broad export %s", a, nfsref.PutExportent(g.Export.Entry))
		}
	}
	return ""
}

func prefixesOf(cs []NFSClient) []netip.Prefix {
	var out []netip.Prefix
	for _, c := range cs {
		p, _ := c.prefix()
		out = append(out, p)
	}
	return out
}

// ---- 1. round trip -----------------------------------------------------------

// Request -> ShareNFS -> libshare -> exportfs -> ParseEtab/serves must give
// back the request, and mountd must grant exactly it; the simulator must
// write the same zfs.exports and etab for the same history.
func FuzzNFSRoundTrip(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{4, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	f.Add([]byte{3, 6, 0, 0, 0, 1, 6, 0, 0, 1, 0, 2, 1, 1, 3, 1})
	f.Fuzz(func(t *testing.T, data []byte) {
		g := &gen{b: data}
		cs := g.clients()
		other := g.clients()
		otherFirst := g.intn(2) == 1

		enc := encodeClients(cs)
		if back, err := decodeClients(enc); err != nil || !slices.Equal(back, cs) {
			t.Fatalf("stored list %q decodes to %v, %v", enc, back, err)
		}
		share := ShareNFS(cs)
		if err := nfsref.ValidateSharenfs(share); err != nil {
			t.Fatalf("zfs refuses our sharenfs %q: %v", share, err)
		}
		steps := [][2]string{{diffPath, share}, {"/tank/v/b", ShareNFS(other)}}
		if otherFirst {
			steps[0], steps[1] = steps[1], steps[0]
		}
		file := shareFile(t, steps...)
		ref := runRef("", map[string]string{"zfs.exports": file})

		live := ParseEtab([]byte(ref.etab))
		for _, c := range cs {
			if !live.serves(diffPath, c) {
				t.Fatalf("sharenfs %q: verifier does not see %s %s in\n%s", share, c.Host, c.mode(), ref.etab)
			}
		}
		if x := live.exposure(diffPath, cs); len(x) > 0 {
			t.Fatalf("sharenfs %q: verifier sees exposure %v", share, x)
		}
		if v := grantsExactly(ref.mountd, diffPath, cs); v != "" {
			t.Fatalf("sharenfs %q: %s\n%s", share, v, ref.etab)
		}

		// the simulator, same history
		sim := zfstest.New("tank")
		z := zfs.New(sim)
		ctx := context.Background()
		for _, ds := range []string{"tank/v", "tank/v/a", "tank/v/b"} {
			if err := z.CreateFilesystem(ctx, ds, nil, true); err != nil {
				t.Fatal(err)
			}
		}
		for _, s := range steps {
			if err := z.Set(ctx, strings.TrimPrefix(s[0], "/"), map[string]string{"sharenfs": s[1]}); err != nil {
				t.Fatal(err)
			}
		}
		if got := sim.Exports(); got != file {
			t.Fatalf("sim zfs.exports\n%s\nwant\n%s", got, file)
		}
		got, _ := sim.ReadEtab(ctx)
		if string(got) != ref.etab {
			t.Fatalf("sim etab\n%s\nwant\n%s", got, ref.etab)
		}
	})
}

// ---- 2. ownership classifier ---------------------------------------------------

// rawShare draws an admin-typed sharenfs value from sharenfs and exports
// syntax.
func (g *gen) rawShare() string {
	if g.intn(4) == 0 {
		return ShareNFS(g.clients())
	}
	toks := []string{"rw", "ro", "=", "=", ":", ":", ",", ",", "@", "*", "10.0.0.0/24", "10.0.0.5/32", "10.0.0.0/16",
		"10.1.0.0/024", "10.0.0.5", "0.0.0.0/0", "/", "[", "]", "::1", " ", "#", "sec", "sec=sys", "sec=krb5",
		"crossmnt", "no_root_squash", "insecure", "anonuid=0", "all_squash", "on", "off", "node-a", "\t", "-", "(", ")", "\"", "nohide", "sync=", "fsid=x"}
	var b strings.Builder
	for range 1 + g.intn(10) {
		b.WriteString(toks[g.intn(len(toks))])
	}
	return b.String()
}

// canonicalShareNFS is the MANAGED predicate of the design (csi-nfs-design
// §2): a value that parses strictly as "rw=<cidrs>,ro=<cidrs>" of masked
// IPv4 networks with an explicit /N at or above the floor, sorted, without
// overlaps, i.e. exactly ShareNFS of what it lists.
func canonicalShareNFS(v string, floor int) ([]NFSClient, bool) {
	if v == "off" {
		return nil, true
	}
	var cs []NFSClient
	for i, opt := range strings.Split(v, ",") {
		k, hosts, ok := strings.Cut(opt, "=")
		if !ok || (k != "rw" && k != "ro") || (i == 1 && k == "rw") || i > 1 {
			return nil, false
		}
		for _, h := range strings.Split(hosts, ":") {
			c, err := ParseClient(h, k == "ro")
			if err != nil || c.Host != h || !strings.Contains(h, "/") || checkFloor(c, floor) != nil {
				return nil, false
			}
			if _, ok := overlap(cs, c); ok {
				return nil, false
			}
			cs = append(cs, c)
		}
	}
	if ShareNFS(cs) != v {
		return nil, false
	}
	return sortClients(cs), true
}

// exportOf is the reference export of sharenfs v on diffPath, with the
// parent /tank/v exporting parentV (an inherited value is shared on the
// parent too).
func exportOf(t testing.TB, v, parentV string) refChain {
	var steps [][2]string
	if parentV != "" && parentV != "off" {
		steps = append(steps, [2]string{"/tank/v", parentV})
	}
	if v != "off" {
		steps = append(steps, [2]string{diffPath, v})
	}
	return runRef("", map[string]string{"zfs.exports": shareFile(t, steps...)})
}

func FuzzNFSClassifier(f *testing.F) {
	for _, s := range []string{"on", "rw", "rw=", "rw=10.0.0.5/32", "ro=10.0.0.0/24,rw=10.0.0.0/24", "rw=@10.0.0.0/8:", "rw=10.0.0.5/32 0.0.0.0/0", "rw=#", "rw=10.0.0.0/16,crossmnt", "sec=krb5,rw=10.0.0.0/24"} {
		f.Add([]byte{1, 1, 0, 2, 7, 3}, s)
		f.Add([]byte{0, 0, 0, 0, 1, 1}, s)
	}
	f.Fuzz(func(t *testing.T, data []byte, raw string) {
		g := &gen{b: data}
		if raw == "" {
			raw = g.rawShare()
		}
		if nfsref.ValidateSharenfs(raw) != nil {
			return // zfs never holds it
		}
		// the design's MANAGED grammar: accepted means exactly that export
		if cs, ok := canonicalShareNFS(raw, DefaultNFSMinPrefix); ok {
			ref := exportOf(t, raw, "")
			if v := grantsExactly(ref.mountd, diffPath, cs); v != "" {
				t.Fatalf("canonical %q: %s", raw, v)
			}
		}

		row := fsv("a", nil)
		switch g.intn(3) {
		case 0: // set locally
			row.ShareNFS, row.ShareNFSLocal = raw, true
		case 1: // inherited from the parent
			row.ShareNFS, row.ShareNFSLocal = raw, false
		case 2: // received
			row.ShareNFS, row.ShareNFSRecv = raw, true
		}
		var stored []NFSClient
		if g.intn(2) == 1 {
			stored = g.clients()
			row.NFSClients, row.NFSClientsSet = encodeClients(stored), true
		}
		parentV := ""
		if !row.ShareNFSLocal && !row.ShareNFSRecv {
			parentV = raw
		}
		before := exportOf(t, raw, parentV)
		live := ParseEtab([]byte(before.etab))
		tbl := NewTable([]Row{row})

		after := func(plan Plan) (string, string) {
			v, p := raw, parentV
			for _, op := range plan {
				if s, ok := op.Props["sharenfs"]; ok {
					v = s
				}
			}
			return v, p
		}

		c := g.client(DefaultNFSMinPrefix)
		plan, _, err := pl.PlanPublishExport(tbl, live, "a", c)
		if foreign(row) && err == nil {
			t.Fatalf("publish on foreign sharenfs %q (row %+v) planned %v", raw, row, plan)
		}
		if err == nil {
			v, p := after(plan)
			want := publishWant(row, c)
			ref := exportOf(t, v, p)
			// the endpoint re-reads etab after the plan and checks c and
			// the exposure: when that passes, nothing may get more than
			// want gives it
			e := ParseEtab([]byte(ref.etab))
			if e.serves(diffPath, c) && len(e.exposure(diffPath, want)) == 0 {
				if why := failOpen(ref.mountd, diffPath, want); why != "" {
					t.Fatalf("publish %s %s on sharenfs %q (local %v, recv %v, stored %q): plan %v leaves %q: %s, and the verifier passes",
						c.Host, c.mode(), raw, row.ShareNFSLocal, row.ShareNFSRecv, row.NFSClients, plan, v, why)
				}
			}
		}

		h := pick(g, append([]NFSClient{c}, stored...)...)
		plan, _, err = pl.PlanUnpublishExport(tbl, live, "a", h.Host)
		if err == nil && len(plan) == 0 && !foreign(row) {
			// done without a change and without a later check: nothing
			// beyond the remaining clients may serve h
			rest := unpublishWant(row, h.Host)
			hp, _ := h.prefix()
			for _, a := range before.mountd.SampleAddrs(hp) {
				if got := before.mountd.Access(diffPath, a, nfsref.Env{}); got.Mode != nfsref.None && got.Mode != wantMode(rest, a) {
					t.Fatalf("unpublish %s on sharenfs %q (local %v, stored %q) is a no-op, but %s still gets %v",
						h.Host, raw, row.ShareNFSLocal, row.NFSClients, a, got.Mode)
				}
			}
		}
	})
}

// ---- 3. verifier soundness -----------------------------------------------------

// foreignLine draws an exports line someone else wrote: for our path, an
// ancestor or another path, any client class, options that widen.
func (g *gen) foreignLine() string {
	path := pick(g, diffPath, diffPath, "/tank/v", "/tank", "/", "/srv/other")
	host := pick(g, "10.0.0.0/16", "10.0.0.0/24", "10.0.0.5/32", "10.0.0.5", "10.1.0.0/024", "10.1.0.0/255.255.0.0",
		"10.0.0.5/24", "*", "node-a", "*.lan", "@ng", "", "gss/krb5", "10.0.0.0/+24", g.client(16).Host)
	opts := pick(g, "rw", "ro", "rw,no_root_squash", "ro,insecure", "rw,crossmnt", "sec=sys,ro,sec=krb5,rw",
		"sec=krb5:sys,rw", "ro,sec=sys,rw", "rw,anonuid=0,all_squash", "ro,nohide", "rw,sync", "ro,fsid=0", "rw,bogus", "ro,mountpoint=/nonexistent")
	if host == "" {
		return path + " (" + opts + ")\n"
	}
	return path + " " + host + "(" + opts + ")\n"
}

func FuzzNFSVerifier(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{2, 3, 1, 0, 1, 2, 0, 0, 3, 1, 1, 0, 2})
	f.Add([]byte{1, 0, 0, 0, 1, 0, 3, 0, 4, 1, 0, 0, 0, 0, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		g := &gen{b: data}
		cs := g.clients()
		var etc, early strings.Builder
		for range g.intn(4) {
			etc.WriteString(g.foreignLine())
		}
		for range g.intn(3) {
			early.WriteString(g.foreignLine())
		}
		dir := map[string]string{"zfs.exports": shareFile(t, [2]string{diffPath, ShareNFS(cs)})}
		if early.Len() > 0 {
			dir[pick(g, "00-early.exports", "zz-late.exports")] = early.String()
		}
		ref := runRef(etc.String(), dir)
		checkVerifier(t, ref, cs, g)
	})
}

var etabLineRE = regexp.MustCompile(`^[^\s]+\t[^\s(]*\([^\s()]*\)\n?$`)

// FuzzNFSEtabParse feeds raw etab text to ParseEtab and the mountd model.
func FuzzNFSEtabParse(f *testing.F) {
	f.Add(diffPath+"\t10.0.0.0/24(ro,sync,wdelay,hide,nocrossmnt,secure,root_squash,no_all_squash,no_subtree_check,secure_locks,acl,no_pnfs,mountpoint,anonuid=65534,anongid=65534,sec=sys,ro,secure,root_squash,no_all_squash)\n", []byte{1, 0, 0, 0, 4, 1})
	f.Add(diffPath+"\t10.0.0.5(rw)\n"+diffPath+"\t10.0.0.0/24(ro)\n", []byte{1, 0, 0, 0, 4, 1})
	f.Add(diffPath+"\t10.0.0.0/16(rw)\n"+diffPath+"\t10.0.0.0/24(ro)\n", []byte{1, 0, 0, 0, 4, 1})
	f.Add(diffPath+"\t10.0.0.0/24(ro,sec=sys,rw)\n", []byte{1, 0, 0, 0, 4, 1})
	f.Fuzz(func(t *testing.T, etab string, data []byte) {
		// etab as putexportent writes it: path (escaped), tab, a host
		// without whitespace or '(', and one option list
		for _, l := range strings.SplitAfter(etab, "\n") {
			if l != "" && !etabLineRE.MatchString(l) {
				return
			}
		}
		g := &gen{b: data}
		cs := g.clients()
		ref := refChain{etab: etab, mountd: nfsref.LoadEtab(etab, nil)}
		checkVerifier(t, ref, cs, g)
	})
}

// checkVerifier: whatever serves/exposure/covers claim about diffPath must
// hold in mountd's model of the same etab.
func checkVerifier(t *testing.T, ref refChain, cs []NFSClient, g *gen) {
	live := ParseEtab([]byte(ref.etab))
	m := ref.mountd
	cands := append(slices.Clone(cs), g.client(16), g.client(16))
	for _, c := range cands {
		for _, ro := range []bool{false, true} {
			c.ReadOnly = ro
			if !live.serves(diffPath, c) {
				continue
			}
			p, _ := c.prefix()
			for _, a := range m.SampleAddrs(p) {
				gr := m.Access(diffPath, a, nfsref.Env{})
				want := nfsref.RW
				if ro {
					want = nfsref.RO
				}
				if gr.Mode != want || (gr.Export != nil && !narrow(gr.Export)) {
					var how string
					if gr.Export != nil {
						how = nfsref.PutExportent(gr.Export.Entry)
					}
					t.Fatalf("verifier: %s served %s, but mountd gives %s %v via %s\netab:\n%s", c.Host, c.mode(), a, gr.Mode, how, ref.etab)
				}
			}
		}
	}
	if len(live.exposure(diffPath, cs)) == 0 && len(cs) > 0 {
		allServed := true
		for _, c := range cs {
			allServed = allServed && live.serves(diffPath, c)
		}
		if allServed {
			if v := grantsExactly(m, diffPath, cs); v != "" {
				t.Fatalf("verifier: no exposure and every client served, but %s\netab:\n%s", v, ref.etab)
			}
		}
	}
	// unpublish: h gone (no line of its own, nothing beyond the rest)
	h := cands[g.intn(len(cands))]
	rest := slices.DeleteFunc(slices.Clone(cs), func(x NFSClient) bool { return x.Host == h.Host })
	if _, ok := live.Lookup(diffPath, h.Host); !ok && len(live.covers(diffPath, h.Host, rest)) == 0 {
		hp, _ := h.prefix()
		for _, a := range m.SampleAddrs(hp) {
			if gr := m.Access(diffPath, a, nfsref.Env{}); gr.Mode != nfsref.None && gr.Mode != wantMode(rest, a) {
				t.Fatalf("verifier: %s reported gone, but mountd gives %s %v\netab:\n%s", h.Host, a, gr.Mode, ref.etab)
			}
		}
	}
}
