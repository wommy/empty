package nfsref

import (
	"net/netip"
	"slices"
	"strings"
)

// Mode is what a client address gets for a path.
type Mode int

const (
	None    Mode = iota // no export answers it
	RO                  // read-only, AUTH_SYS
	RW                  // read-write, AUTH_SYS
	Unknown             // depends on DNS, netgroups or wildcards
)

func (m Mode) String() string { return [...]string{"none", "ro", "rw", "unknown"}[m] }

// Grant is mountd's answer for one address and path.
type Grant struct {
	Mode   Mode
	Export *Line // the export that answered, nil for None
}

// Env is what mountd checks at request time that etab does not say.
type Env struct {
	// IsMountpoint is check_is_mountpoint; nil means every path is one.
	IsMountpoint func(path string) bool
}

// isSubdirectory is is_subdirectory (cache.c:563-574) with same_path
// (:540-561) by string only (the handle/inode fallback finds bind mounts
// of one directory: ambiguity, needs a real host).
func isSubdirectory(child, parent string) bool {
	if parent == "/" && len(child) > 1 {
		return true
	}
	return len(child) > len(parent) && child[:len(parent)] == parent && child[len(parent)] == '/'
}

// pathMatches is path_matches (cache.c:576-585).
func pathMatches(e *Entry, path string) bool {
	return path == e.Path || (e.Flags&ExpCrossMount != 0 && isSubdirectory(path, e.Path))
}

// lookupExport is lookup_export (cache.c:3313-3360) for one address:
// every export whose path and client match, by client type then list
// order; a non-V4ROOT export beats a V4ROOT one; when either is crossmnt
// and the paths differ in length the longer path wins; otherwise the
// first stays. match decides unknown (wildcard, netgroup) clients.
func (t *Table) lookupExport(path string, ip netip.Addr, unknownMatches bool) *export {
	var found *export
	for i := range t.lists {
		for exp := t.lists[i].head; exp != nil; exp = exp.next {
			if !pathMatches(&exp.E, path) {
				continue
			}
			m, known := exp.Client.Match(ip)
			if !known {
				m = unknownMatches
			}
			if !m {
				continue
			}
			if found == nil {
				found = exp
				continue
			}
			if exp.E.Flags&ExpV4Root != 0 {
				continue
			}
			if found.E.Flags&ExpV4Root != 0 {
				found = exp
				continue
			}
			if (found.E.Flags&ExpCrossMount != 0 || exp.E.Flags&ExpCrossMount != 0) && len(found.E.Path) != len(exp.E.Path) {
				if len(exp.E.Path) > len(found.E.Path) {
					found = exp
				}
				continue
			}
		}
	}
	return found
}

// modeOf is what the kernel enforces for an AUTH_SYS request on the
// export mountd answered with (nl_add_export_req, cache.c:1849-1925): no
// answer when the export's mountpoint (e_mountpoint, or its own path for
// a bare "mountpoint") is not mounted; otherwise the "sys" flavor's flags
// decide ro/rw (the kernel's per-flavor flags, nfsexp_flags), and an
// export without sys or unix refuses AUTH_SYS.
func modeOf(exp *export, env Env) Mode {
	if exp == nil {
		return None
	}
	if exp.E.Mountpt != nil && env.IsMountpoint != nil {
		mp := *exp.E.Mountpt
		if mp == "" {
			mp = exp.E.Path
		}
		if !env.IsMountpoint(mp) {
			return None
		}
	}
	e := exp.E.clone()
	ensureSecinfo(&e)
	for _, s := range e.Secinfo {
		if s.Num == 1 {
			if s.Flags&ExpReadonly != 0 {
				return RO
			}
			return RW
		}
	}
	return None
}

// Access is what address ip gets for path: the mode and the export that
// gives it. When the answer depends on wildcard or netgroup clients it is
// Unknown (both resolutions are tried).
func (t *Table) Access(path string, ip netip.Addr, env Env) Grant {
	a := t.lookupExport(path, ip, false)
	b := t.lookupExport(path, ip, true)
	if a != b && modeOf(a, env) != modeOf(b, env) {
		return Grant{Mode: Unknown, Export: lineOf(b)}
	}
	return Grant{Mode: modeOf(a, env), Export: lineOf(a)}
}

func lineOf(e *export) *Line {
	if e == nil {
		return nil
	}
	return &Line{e.E, e.Client}
}

// SampleAddrs lists IPv4 addresses that together decide what any address
// of network p gets: p's first and last address, and the first and last
// address (clipped to p) of every subnet or FQDN client in the table.
// Exact for prefix masks; a spelled-out non-contiguous mask is sampled
// only at its ends.
func (t *Table) SampleAddrs(p netip.Prefix) []netip.Addr {
	p = p.Masked()
	out := []netip.Addr{p.Addr(), lastAddr(p)}
	add := func(a netip.Addr) {
		if p.Contains(a) {
			out = append(out, a)
		}
	}
	for _, c := range t.clients[FQDN] {
		for _, a := range c.Addrs {
			add(a)
		}
	}
	for _, c := range t.clients[Subnetwork] {
		if !c.Net.Is4() {
			continue
		}
		n := 0
		for _, b := range c.Mask.AsSlice() {
			for ; b&0x80 != 0; b <<= 1 {
				n++
			}
		}
		q := netip.PrefixFrom(c.Net, n).Masked()
		if q.Overlaps(p) {
			add(q.Addr())
			add(lastAddr(q))
			if q.Bits() < p.Bits() { // q contains p: p's ends suffice
				continue
			}
		}
	}
	slices.SortFunc(out, netip.Addr.Compare)
	return slices.Compact(out)
}

func lastAddr(p netip.Prefix) netip.Addr {
	b := p.Masked().Addr().As4()
	host := 32 - p.Bits()
	for i := 0; i < host; i++ {
		b[3-i/8] |= 1 << (i % 8)
	}
	return netip.AddrFrom4(b)
}

// Exposure is every address class that gets path, beyond a set of
// networks: an Anonymous, Wildcard, Netgroup or name client on path or on
// a crossmnt ancestor, or a subnet/FQDN client not inside allowed.
func (t *Table) Exposure(path string, allowed []netip.Prefix, env Env) []Line {
	var out []Line
	for _, l := range t.Exports() {
		e := l.Entry
		if !pathMatches(&e, path) {
			continue
		}
		exp := &export{E: e, Client: l.Client}
		if modeOf(exp, env) == None {
			continue
		}
		inside := false
		switch l.Client.Type {
		case Subnetwork:
			for _, a := range allowed {
				if subnetInside(l.Client, a) {
					inside = true
				}
			}
		case FQDN:
			inside = len(l.Client.Addrs) > 0
			for _, ip := range l.Client.Addrs {
				ok := false
				for _, a := range allowed {
					ok = ok || a.Contains(ip)
				}
				inside = inside && ok
			}
		case GSS:
			inside = true
		}
		if !inside {
			out = append(out, l)
		}
	}
	return out
}

// subnetInside reports whether every address the client matches is in a.
func subnetInside(c *Client, a netip.Prefix) bool {
	if !c.Net.Is4() || !a.Addr().Is4() {
		return false
	}
	// every bit fixed by a must be fixed by c's mask and agree
	am := maskOf(a.Bits(), 32).As4()
	cm, cn, an := c.Mask.As4(), c.Net.As4(), a.Addr().As4()
	for i := 0; i < 4; i++ {
		if am[i]&^cm[i] != 0 || (cn[i]^an[i])&am[i] != 0 {
			return false
		}
	}
	return true
}

// HostsOf lists the etab clients of path as "host mode" strings, in etab
// order (for diffs and reports).
func (t *Table) HostsOf(path string) []string {
	var out []string
	for _, l := range t.Exports() {
		if l.Entry.Path != path {
			continue
		}
		m := "rw"
		if l.Entry.Flags&ExpReadonly != 0 {
			m = "ro"
		}
		out = append(out, l.Client.Hostname+" "+m)
	}
	return out
}

// EtabLinesOf returns etab's lines for path (escaped as in etab).
func EtabLinesOf(etab, path string) []string {
	var b strings.Builder
	fprintpath(&b, path)
	pfx := b.String() + "\t"
	var out []string
	for _, l := range strings.SplitAfter(etab, "\n") {
		if strings.HasPrefix(l, pfx) {
			out = append(out, l)
		}
	}
	return out
}
