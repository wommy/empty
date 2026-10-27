package nfsref

import (
	"net/netip"
	"testing"
)

// TestClientGrammarBareVsCIDR pins what nfs-utils does with a bare address
// versus a /32 (checked against a real exportfs 3.1.1 build): a bare
// address is an FQDN client that is looked up, merged with any name client
// resolving to the same address (first spelling wins in etab, the second
// entry is IGNORED) and beats every subnet line at mountd regardless of
// file order; a /32 is a subnet client kept verbatim and never merged.
func TestClientGrammarBareVsCIDR(t *testing.T) {
	t.Parallel()
	res := func(name string) (string, []netip.Addr, bool) {
		if name == "node-a" {
			return "node-a", []netip.Addr{netip.MustParseAddr("10.0.0.5")}, true
		}
		return NumericResolver(name)
	}
	ip := netip.MustParseAddr("10.0.0.5")
	for _, tc := range []struct {
		exports string
		etab    []string // hosts of /a, etab order
		mode    Mode
	}{
		{"/a 10.0.0.5(rw)\n", []string{"10.0.0.5 rw"}, RW},
		{"/a 10.0.0.5/32(rw)\n", []string{"10.0.0.5/32 rw"}, RW},
		// both kept: different client classes; the bare one wins at mountd
		{"/a 10.0.0.5/32(ro) 10.0.0.5(rw)\n", []string{"10.0.0.5 rw", "10.0.0.5/32 ro"}, RW},
		// a foreign name line swallows the bare address and renames it
		{"/a node-a(ro)\n/a 10.0.0.5(rw)\n", []string{"node-a ro"}, RO},
		// ... but never the /32; the name line still wins (FQDN class first)
		{"/a node-a(ro)\n/a 10.0.0.5/32(rw)\n", []string{"node-a ro", "10.0.0.5/32 rw"}, RO},
	} {
		_, tab := ExportfsRA(ExportsFiles{Exports: []byte(tc.exports)}, res, nil)
		got := tab.HostsOf("/a")
		if len(got) != len(tc.etab) {
			t.Errorf("%q: etab %q, want %q", tc.exports, got, tc.etab)
			continue
		}
		for i := range got {
			if got[i] != tc.etab[i] {
				t.Errorf("%q: etab %q, want %q", tc.exports, got, tc.etab)
			}
		}
		if m := tab.Access("/a", ip, Env{}).Mode; m != tc.mode {
			t.Errorf("%q: 10.0.0.5 gets %v, want %v", tc.exports, m, tc.mode)
		}
	}
}
