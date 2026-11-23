package nfsref

import (
	"cmp"
	"encoding/binary"
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// ClientType is enum MCL_* (support/include/exportfs.h:18-27), in the
// order exportfs writes etab and mountd looks clients up.
type ClientType int

const (
	FQDN ClientType = iota
	Subnetwork
	Wildcard
	Netgroup
	Anonymous
	GSS
	mclMax
)

func (t ClientType) String() string {
	return [...]string{"fqdn", "subnet", "wildcard", "netgroup", "anonymous", "gss"}[t]
}

// ClientGettype is client_gettype (client.c:775-800).
func ClientGettype(ident string) ClientType {
	if ident == "" || ident == "*" {
		return Anonymous
	}
	if strings.HasPrefix(ident, "gss/") {
		return GSS
	}
	if ident[0] == '@' {
		return Netgroup
	}
	for i := 0; i < len(ident); i++ {
		switch ident[i] {
		case '*', '?', '[':
			return Wildcard
		case '/':
			return Subnetwork
		case '\\':
			if i+1 < len(ident) {
				i++
			}
		}
	}
	return FQDN
}

// Resolver is getaddrinfo(name, AI_CANONNAME) for an FQDN client
// (hostname.c host_addrinfo): its canonical name and addresses.
type Resolver func(name string) (canon string, addrs []netip.Addr, ok bool)

// NumericResolver resolves only numeric hosts, like a host with no DNS
// answers: glibc getaddrinfo takes inet_aton spellings ("10.1",
// "0x0a.0.0.1") and IPv6 literals, and returns the name itself as the
// canonical name (ambiguity: libc). Names fail ("Failed to resolve").
func NumericResolver(name string) (string, []netip.Addr, bool) {
	if a, ok := InetAton(name); ok {
		return name, []netip.Addr{a}, true
	}
	if a, err := netip.ParseAddr(name); err == nil && a.Is6() && a.Zone() == "" {
		return name, []netip.Addr{a}, true
	}
	return "", nil, false
}

// InetAton is glibc inet_aton: 1-4 parts, each decimal, 0x hex or 0 octal;
// the last part fills the remaining bytes.
func InetAton(s string) (netip.Addr, bool) {
	parts := strings.Split(s, ".")
	if len(parts) > 4 || s == "" {
		return netip.Addr{}, false
	}
	var vals []uint64
	for _, p := range parts {
		if p == "" {
			return netip.Addr{}, false
		}
		v, rest := cStrtol(p, 0)
		if rest != "" || p[0] == '+' || p[0] == '-' || isspaceC(int(p[0])) || v < 0 {
			return netip.Addr{}, false
		}
		vals = append(vals, uint64(v))
	}
	var a uint64
	for i, v := range vals[:len(vals)-1] {
		if v > 0xff {
			return netip.Addr{}, false
		}
		a |= v << (24 - 8*i)
	}
	last := vals[len(vals)-1]
	if last >= 1<<(8*(5-len(vals))) {
		return netip.Addr{}, false
	}
	a |= last
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(a))
	return netip.AddrFrom4(b), true
}

// InetPton4 is glibc inet_pton(AF_INET): exactly four decimal octets, no
// leading zeros.
func InetPton4(s string) (netip.Addr, bool) {
	a, err := netip.ParseAddr(s)
	if err != nil || !a.Is4() || a.String() != s {
		return netip.Addr{}, false
	}
	return a, true
}

// Client is struct nfs_client.
type Client struct {
	Type     ClientType
	Hostname string
	Addrs    []netip.Addr // FQDN: resolved addresses
	Net      netip.Addr   // Subnetwork: address as written (not masked)
	Mask     netip.Addr   // Subnetwork: netmask
}

const nfsclntAddrMax = 16 // NFSCLNT_ADDRMAX (exportfs.h)

// initSubnetwork is init_subnetwork (client.c:190-228) with init_netmask4
// (:62-116) and init_netmask6 (:118-176). host_pton (hostname.c:95-147)
// takes a strict inet_pton IPv4 address or a numeric IPv6 one.
func initSubnetwork(c *Client) bool {
	addr, slash, hasSlash := strings.Cut(c.Hostname, "/")
	a, ok := InetPton4(addr)
	if !ok {
		v6, err := netip.ParseAddr(addr)
		if err != nil || !v6.Is6() {
			return false // "Invalid IP address"
		}
		a = v6
	}
	c.Net = a
	bits := a.BitLen()
	if !hasSlash {
		c.Mask = maskOf(bits, bits)
		return true
	}
	if a.Is4() && strings.Contains(slash, ".") { // spelled-out netmask, any bits
		m, ok := InetPton4(slash)
		if !ok {
			return false
		}
		c.Mask = m
		return true
	}
	if a.Is6() && strings.Contains(slash, ":") {
		m, err := netip.ParseAddr(slash)
		if err != nil || !m.Is6() {
			return false
		}
		c.Mask = m
		return true
	}
	// strtoul(slash, &end, 10); trailing junk is refused unless the value
	// is ULONG_MAX or errno is ERANGE (ambiguity: a stale errno)
	v, rest := cStrtol(slash, 10)
	if v < 0 {
		v = 1 << 62 // ULONG_MAX - |v|+1: always above the limit
	} else if rest != "" {
		return false
	}
	if v > int64(bits) {
		return false
	}
	c.Mask = maskOf(int(v), bits)
	return true
}

func maskOf(n, bits int) netip.Addr {
	b := make([]byte, bits/8)
	for i := 0; i < n; i++ {
		b[i/8] |= 0x80 >> (i % 8)
	}
	a, _ := netip.AddrFromSlice(b)
	return a
}

// MatchSubnet is check_subnet_v4/v6 (client.c:528-581): ((a^b)&m) == 0.
func (c *Client) MatchSubnet(ip netip.Addr) bool {
	if ip.BitLen() != c.Net.BitLen() {
		return false
	}
	x, y, m := c.Net.AsSlice(), ip.AsSlice(), c.Mask.AsSlice()
	for i := range x {
		if (x[i]^y[i])&m[i] != 0 {
			return false
		}
	}
	return true
}

// Match is client_check (client.c:744-766) for one address. Wildcards and
// netgroups need reverse DNS and netgroup databases: they report unknown.
func (c *Client) Match(ip netip.Addr) (match, known bool) {
	switch c.Type {
	case FQDN:
		return slices.Contains(c.Addrs, ip), true
	case Subnetwork:
		return c.MatchSubnet(ip), true
	case Anonymous:
		return true, true
	case GSS:
		return false, true
	}
	return false, false
}

// export is struct nfs_export.
type export struct {
	E       Entry
	Client  *Client
	next    *export
	xtabent bool
}

const hashTableSize = 1021 // exportfs.h:107

type hashTable struct {
	head        *export
	first, last [hashTableSize]*export
}

// Table is exportlist[] and clientlist[] (export.c:30, client.c:36).
type Table struct {
	lists    [mclMax]hashTable
	clients  [mclMax][]*Client
	Resolve  Resolver
	Realpath func(string) string
	Diags    []Diag
}

// NewTable starts an empty table; nil resolve is NumericResolver.
func NewTable(resolve Resolver, realpath func(string) string) *Table {
	if resolve == nil {
		resolve = NumericResolver
	}
	return &Table{Resolve: resolve, Realpath: realpath}
}

func (t *Table) logf(format string, a ...any) {
	t.Diags = append(t.Diags, Diag{0, fmt.Sprintf(format, a...)})
}

// exportHash is export_hash/strtoint (export.c:422-444): sum of
// (signed char)c * i, unsigned, mod 1021.
func exportHash(s string) int {
	var n uint32
	for i := 0; i < len(s); i++ {
		n += uint32(int32(int8(s[i])) * int32(i))
	}
	return int(n % hashTableSize)
}

// clientLookup is client_lookup (client.c:268-314), canonical = 0.
func (t *Table) clientLookup(hname string) *Client {
	typ := ClientGettype(hname)
	var addrs []netip.Addr
	var clp *Client
	if typ == FQDN {
		canon, as, ok := t.Resolve(hname)
		if !ok {
			t.logf("Failed to resolve %s", hname)
			return nil
		}
		hname, addrs = canon, as
		for _, c := range t.clients[FQDN] {
			for _, a := range addrs {
				if slices.Contains(c.Addrs, a) {
					clp = c
					break
				}
			}
			if clp != nil {
				break
			}
		}
	} else {
		for _, c := range t.clients[typ] {
			if strings.EqualFold(hname, c.Hostname) { // strcasecmp (ASCII)
				clp = c
				break
			}
		}
	}
	if clp == nil {
		clp = &Client{Type: typ, Hostname: hname}
		if typ == Subnetwork && !initSubnetwork(clp) {
			t.logf("Invalid IP address or prefix %s", hname)
			return nil
		}
		t.clients[typ] = append(t.clients[typ], clp)
	}
	if typ == FQDN && len(clp.Addrs) == 0 {
		if len(addrs) > nfsclntAddrMax {
			addrs = addrs[:nfsclntAddrMax]
		}
		clp.Addrs = addrs
	}
	return clp
}

// exportAdd is export_add (export.c:295-321).
func (t *Table) exportAdd(exp *export) {
	tbl := &t.lists[exp.Client.Type]
	pos := exportHash(exp.E.Path)
	if tbl.first[pos] == nil {
		tbl.first[pos], tbl.last[pos] = exp, exp
		exp.next = tbl.head
		tbl.head = exp
	} else {
		exp.next = tbl.last[pos].next
		tbl.last[pos].next = exp
		tbl.last[pos] = exp
	}
}

// exportLookup is export_lookup (export.c:359-381).
func (t *Table) exportLookup(hname, path string) *export {
	clp := t.clientLookup(hname)
	if clp == nil {
		return nil
	}
	tbl := &t.lists[clp.Type]
	pos := exportHash(path)
	if tbl.first[pos] == nil {
		return nil
	}
	end := tbl.last[pos].next
	for exp := tbl.first[pos]; exp != nil && exp != end; exp = exp.next {
		if exp.Client == clp && exp.E.Path == path {
			return exp
		}
	}
	return nil
}

// exportCreate is export_create (export.c:227-242).
func (t *Table) exportCreate(e *Entry) *export {
	clp := t.clientLookup(e.Hostname)
	if clp == nil {
		return nil
	}
	exp := &export{E: e.clone(), Client: clp}
	t.exportAdd(exp)
	return exp
}

// ReadExports is export_read (export.c:112-153): entries until the end of
// the file or the first entry getexportent fails on; a duplicate
// (client, path) keeps the first entry (warn_duplicated_exports :86-100).
// It reports whether reading stopped on an error.
func (t *Table) ReadExports(fname string, b []byte) (stopped bool) {
	p := newParser(fname, b, t.Realpath)
	for {
		e := p.next()
		if e == nil {
			break
		}
		if exp := t.exportLookup(e.Hostname, e.Path); exp == nil {
			t.exportCreate(e)
		} else {
			t.logf("duplicated export entries: %s:%s (flags %#x vs %#x)", e.Hostname, e.Path, e.Flags, exp.E.Flags)
		}
	}
	for _, d := range p.diags {
		t.Diags = append(t.Diags, Diag{d.Line, fname + ": " + d.Msg})
	}
	return p.stoppedOnError
}

// ExportsFiles is the input of `exportfs -ra`: /etc/exports and the files
// of /etc/exports.d.
type ExportsFiles struct {
	Exports []byte            // /etc/exports; nil when missing
	Dir     map[string][]byte // /etc/exports.d: file name -> content
}

// ExportfsRA is `exportfs -ra` (exportfs.c:135-276): read /etc/exports,
// then each /etc/exports.d file named *.exports (not starting with '.') in
// versionsort order (export.c:164-217), mark every export (export_all
// :282-301; validate_export only logs), and write etab (xtab_write,
// xtab.c:91-122): by client type, then list order, each under its
// client's canonical name. With -r the old etab is not merged
// (exportfs.c:264-265).
func ExportfsRA(files ExportsFiles, resolve Resolver, realpath func(string) string) (etab string, t *Table) {
	t = NewTable(resolve, realpath)
	if files.Exports != nil {
		t.ReadExports("/etc/exports", files.Exports)
	}
	names := make([]string, 0, len(files.Dir))
	for n := range files.Dir {
		if n == "" || n[0] == '.' || len(n) < len(".exports")+1 || !strings.HasSuffix(n, ".exports") {
			continue
		}
		names = append(names, n)
	}
	slices.SortFunc(names, strverscmp)
	for _, n := range names {
		t.ReadExports("/etc/exports.d/"+n, files.Dir[n])
	}
	var b strings.Builder
	for i := range t.lists {
		for exp := t.lists[i].head; exp != nil; exp = exp.next {
			exp.xtabent = true
			e := exp.E.clone()
			e.Hostname = exp.Client.Hostname
			b.WriteString(PutExportent(e))
		}
	}
	return b.String(), t
}

// strverscmp orders digit runs by value (glibc versionsort, simplified:
// leading-zero fractional rules are not modelled).
func strverscmp(a, b string) int {
	for a != "" && b != "" {
		if isDigit(a[0]) && isDigit(b[0]) {
			i, j := 0, 0
			for i < len(a) && isDigit(a[i]) {
				i++
			}
			for j < len(b) && isDigit(b[j]) {
				j++
			}
			x, y := strings.TrimLeft(a[:i], "0"), strings.TrimLeft(b[:j], "0")
			if c := cmp.Compare(len(x), len(y)); c != 0 {
				return c
			}
			if c := strings.Compare(x, y); c != 0 {
				return c
			}
			a, b = a[i:], b[j:]
			continue
		}
		if a[0] != b[0] {
			return cmp.Compare(a[0], b[0])
		}
		a, b = a[1:], b[1:]
	}
	return cmp.Compare(len(a), len(b))
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// LoadEtab is mountd's view of etab (xtab_read, xtab.c:35-77): every entry
// getexportent returns, until the first failure; one whose client cannot
// be looked up is skipped.
func LoadEtab(etab string, resolve Resolver) *Table {
	t := NewTable(resolve, nil)
	p := newParser("/var/lib/nfs/etab", []byte(etab), nil)
	for {
		e := p.next()
		if e == nil {
			break
		}
		exp := t.exportLookup(e.Hostname, e.Path)
		if exp == nil {
			exp = t.exportCreate(e)
		}
		if exp != nil {
			exp.xtabent = true
		}
	}
	return t
}

// Line is one export as exportfs or mountd holds it.
type Line struct {
	Entry  Entry
	Client *Client
}

// Exports lists the table in etab order.
func (t *Table) Exports() []Line {
	var out []Line
	for i := range t.lists {
		for exp := t.lists[i].head; exp != nil; exp = exp.next {
			out = append(out, Line{exp.E, exp.Client})
		}
	}
	return out
}
