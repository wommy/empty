package endpoint

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// File export (NFS). The desired client list of a volume is a locally set
// user property (<prefix>:nfs_clients); sharenfs is computed from it and
// never written in any other form. Publish and Unpublish change one client
// at a time under the volume's lock, so concurrent front ends cannot drop
// each other's clients.
//
// `zfs set sharenfs=...` exits 0 even when the export fails (libzfs discards
// the exportfs status), so the kernel's export table (/var/lib/nfs/etab) is
// a planner input and is re-read after every change, and Publish also
// requires running nfsd threads.

// NFSClient is one client network allowed to mount a volume.
type NFSClient struct {
	// Host is a masked IPv4 CIDR that always carries its prefix length,
	// "/32" for a single address: "10.0.0.5/32", "10.20.0.0/24".
	//
	// Why no hostnames: nfs-utils resolves a name on every `exportfs -ra`
	// (any sharenfs change on any dataset, `zfs share -a` at boot, an
	// nfs-server reload). When DNS or NSS fails at that moment it logs
	// "Failed to resolve" and drops the client from etab, still exiting 0,
	// and nothing re-adds it: the node silently loses access. It also
	// writes a name or bare address under the canonical name of the first
	// client with the same address, so etab cannot be checked against what
	// was asked (support/export/client.c client_lookup).
	//
	// Why always "/N": nfs-utils classes a client containing '/' as a
	// subnet (MCL_SUBNETWORK). It keeps that string verbatim, never
	// resolves it and never merges it with other clients, so etab shows
	// exactly the string written to sharenfs.
	Host     string
	ReadOnly bool
	// unknownMode marks an etab line whose options start with neither ro
	// nor rw. It serves no request, so verification fails closed.
	unknownMode bool
	// broad marks an etab line with an option our sharenfs never yields
	// and that widens access (no_root_squash, insecure, crossmnt, a
	// flavor other than sys, anonuid other than 65534, ...), a token it
	// does not know, or a per-flavor mode that differs from the leading
	// one (the kernel enforces the flavor's). It serves no request.
	broad bool
	// crossmnt: the line exports the mounts below its path too
	// (nfs-utils cache.c path_matches, lookup_export).
	crossmnt bool
}

func (c NFSClient) mode() string {
	if c.ReadOnly {
		return "ro"
	}
	return "rw"
}

// prefix is the client's network, normalized (masked). Clients compare by
// it, never by spelling. An etab host that is not a subnet (a name, or a
// bare address, which nfs-utils resolves and may rename) has none.
func (c NFSClient) prefix() (netip.Prefix, bool) {
	if !strings.Contains(c.Host, "/") {
		return netip.Prefix{}, false
	}
	p, err := netip.ParsePrefix(c.Host)
	if err != nil {
		return netip.Prefix{}, false
	}
	return p.Masked(), true
}

// maxPropLen is ZFS_MAXPROPLEN (ZAP_MAXVALUELEN, 8192) minus the
// terminating NUL: libzfs refuses a value with strlen >= ZFS_MAXPROPLEN
// (libzfs_util.c zprop_parse_value), for sharenfs and user properties alike.
const maxPropLen = 8191

// DefaultNFSMinPrefix is the broadest client network Publish accepts by
// default: an IPv4 /16.
const DefaultNFSMinPrefix = 16

// ParseClient validates and normalizes a client spec: an IPv4 address
// (stored as a /32) or an IPv4 CIDR with its host bits zero. Hostnames,
// IPv6, wildcards, netgroups, inet_aton spellings (hex, octal, short forms)
// and /0 are refused. Only digits, '.' and '/' can come out, so none of
// sharenfs' separators (',' ':' '=') or exportfs' syntax ('(' '*' '@'
// space) can reach the property. The configured floor on the prefix length
// is Publish's (Config.NFSMinPrefix), not this function's.
func ParseClient(host string, readOnly bool) (NFSClient, error) {
	p, err := parseClient(host)
	if err != nil {
		return NFSClient{}, err
	}
	return NFSClient{Host: p.String(), ReadOnly: readOnly}, nil
}

func parseClient(host string) (netip.Prefix, error) {
	bad := func(why string) error {
		return errorf(InvalidArgument, "invalid NFS client %q: %s", host, why)
	}
	const want = "want an IPv4 address or IPv4 CIDR (hostnames are not accepted: nfs-utils drops a name it cannot resolve on any later exportfs)"
	if len(host) > len("255.255.255.255/32") {
		return netip.Prefix{}, bad(want)
	}
	var p netip.Prefix
	if strings.Contains(host, "/") {
		var err error
		if p, err = netip.ParsePrefix(host); err != nil || !p.Addr().Is4() || p.String() != host {
			return netip.Prefix{}, bad(want)
		}
		if p.Masked() != p {
			return netip.Prefix{}, bad(fmt.Sprintf("host bits are set; the network is %s", p.Masked()))
		}
	} else {
		a, err := netip.ParseAddr(host)
		if err != nil || !a.Is4() || a.String() != host {
			return netip.Prefix{}, bad(want)
		}
		p = netip.PrefixFrom(a, 32)
	}
	if p.Bits() == 0 {
		return netip.Prefix{}, bad("/0 exports to every host")
	}
	return p, nil
}

// checkFloor refuses a client network broader than /min.
func checkFloor(c NFSClient, min int) error {
	if p, ok := c.prefix(); !ok || p.Bits() < min {
		return errorf(InvalidArgument, "NFS client %s is broader than the configured minimum prefix /%d", c.Host, min)
	}
	return nil
}

// compareClients is a total order: by network address, then prefix
// length, then mode. Hosts that are not networks (never ours) sort after,
// by spelling.
func compareClients(a, b NFSClient) int {
	pa, oka := a.prefix()
	pb, okb := b.prefix()
	switch {
	case oka && okb:
		if c := pa.Addr().Compare(pb.Addr()); c != 0 {
			return c
		}
		if c := cmp.Compare(pa.Bits(), pb.Bits()); c != 0 {
			return c
		}
	case oka != okb:
		if oka {
			return -1
		}
		return 1
	default:
		if c := strings.Compare(a.Host, b.Host); c != 0 {
			return c
		}
	}
	switch {
	case a.ReadOnly == b.ReadOnly:
		return 0
	case !a.ReadOnly:
		return -1
	}
	return 1
}

func sortClients(cs []NFSClient) []NFSClient {
	cs = slices.Clone(cs)
	slices.SortStableFunc(cs, compareClients)
	return cs
}

// overlap returns a client in cs whose network overlaps c's (contains it,
// is contained by it, or is the same), if any.
func overlap(cs []NFSClient, c NFSClient) (NFSClient, bool) {
	pc, ok := c.prefix()
	if !ok {
		return NFSClient{}, false
	}
	for _, x := range cs {
		if px, ok := x.prefix(); ok && px.Overlaps(pc) {
			return x, true
		}
	}
	return NFSClient{}, false
}

// encodeClients is the stored form of a client list: "host=rw,host=ro"
// sorted by address, "none" for an empty list (a user property cannot hold
// "").
func encodeClients(cs []NFSClient) string {
	if len(cs) == 0 {
		return "none"
	}
	parts := make([]string, len(cs))
	for i, c := range sortClients(cs) {
		parts[i] = c.Host + "=" + c.mode()
	}
	return strings.Join(parts, ",")
}

// decodeClients parses encodeClients; any deviation is an error, so a hand
// edit is refused instead of half-read. Duplicate or overlapping networks
// are refused too: nfs-utils applies the first matching line of a path, so
// the second entry's mode would not hold.
func decodeClients(s string) ([]NFSClient, error) {
	if s == "none" {
		return nil, nil
	}
	var cs []NFSClient
	for _, part := range strings.Split(s, ",") {
		host, mode, ok := strings.Cut(part, "=")
		if !ok || (mode != "rw" && mode != "ro") {
			return nil, errorf(FailedPrecondition, "invalid client entry %q", part)
		}
		c, err := ParseClient(host, mode == "ro")
		if err != nil || c.Host != host {
			return nil, errorf(FailedPrecondition, "invalid client entry %q", part)
		}
		if x, ok := overlap(cs, c); ok {
			return nil, errorf(FailedPrecondition, "client entries %s and %s overlap", x.Host, c.Host)
		}
		cs = append(cs, c)
	}
	if encodeClients(cs) != s {
		return nil, errorf(FailedPrecondition, "client list %q is not canonical", s)
	}
	return cs, nil
}

// ShareNFS is the sharenfs value for a client list: "rw=a:b,ro=c" with
// hosts sorted, or "off" for none. It is the only form ever written, so
// equality with the stored value means "already applied". No other option
// is emitted: squash and security stay at the exportfs defaults
// (root_squash, sec=sys).
func ShareNFS(cs []NFSClient) string {
	var rw, ro []string
	for _, c := range sortClients(cs) {
		if c.ReadOnly {
			ro = append(ro, c.Host)
		} else {
			rw = append(rw, c.Host)
		}
	}
	var opts []string
	if len(rw) > 0 {
		opts = append(opts, "rw="+strings.Join(rw, ":"))
	}
	if len(ro) > 0 {
		opts = append(opts, "ro="+strings.Join(ro, ":"))
	}
	if len(opts) == 0 {
		return "off"
	}
	return strings.Join(opts, ",")
}

// Etab is the kernel NFS server's export table: export path to the clients
// it serves, as exportfs last applied it.
type Etab map[string][]NFSClient

// ParseEtab reads /var/lib/nfs/etab: one "path<TAB>client(ro|rw,...)" line
// per client, with special characters in the path written as \ooo. A line
// whose first option is neither ro nor rw is kept with an unknown mode: it
// is served (Unpublish must not report it gone) but in no mode Publish can
// confirm. Lines keep their file order, which is mountd's order within a
// client class (nfs-utils xtab.c xtab_write, cache.c lookup_export).
func ParseEtab(b []byte) Etab {
	t := Etab{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		line := sc.Text()
		path, rest, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		host, opts, ok := strings.Cut(rest, "(")
		if !ok {
			continue
		}
		c := NFSClient{Host: strings.ToLower(host)}
		switch opts[:strings.IndexAny(opts+",", ",)")] {
		case "ro":
			c.ReadOnly = true
		case "rw":
		default:
			c.unknownMode = true
		}
		c.broad, c.crossmnt = etabOptions(strings.TrimSuffix(opts, ")"), c.ReadOnly)
		path = unescapeOctal(path)
		t[path] = append(t[path], c)
	}
	return t
}

// etabOptions checks an etab option list against what exportfs writes for
// a line of our sharenfs (libshare "sec=sys,<mode>,no_subtree_check,
// mountpoint" with nfs-utils defaults; exports.c putexportent and
// secinfo_show). Anything else that is not known to be narrower is broad.
func etabOptions(opts string, readOnly bool) (broad, crossmnt bool) {
	mode := "rw"
	if readOnly {
		mode = "ro"
	}
	for _, o := range strings.Split(opts, ",") {
		switch {
		case o == "ro" || o == "rw":
			// the leading mode, or a flavor's: the kernel enforces the
			// flavor's flags, so every one must agree
			if o != mode {
				broad = true
			}
		case o == "crossmnt":
			crossmnt, broad = true, true
		case o == "sync", o == "async", o == "wdelay", o == "no_wdelay", o == "hide",
			o == "nocrossmnt", o == "secure", o == "root_squash", o == "all_squash",
			o == "no_all_squash", o == "subtree_check", o == "no_subtree_check",
			o == "secure_locks", o == "acl", o == "no_acl", o == "nordirplus",
			o == "security_label", o == "sign_fh", o == "pnfs", o == "no_pnfs",
			o == "mountpoint", strings.HasPrefix(o, "mountpoint="),
			o == "anonuid=65534", o == "anongid=65534",
			strings.HasPrefix(o, "fsid="), strings.HasPrefix(o, "xprtsec="):
		case strings.HasPrefix(o, "sec="):
			for _, f := range strings.Split(o[4:], ":") {
				if f != "sys" && f != "unix" {
					broad = true
				}
			}
		default:
			broad = true
		}
	}
	return broad, crossmnt
}

// etabClass is nfs-utils' client class of an etab host (client.c
// client_gettype); mountd tries fqdn, then subnet, then the rest.
func etabClass(h string) string {
	switch {
	case h == "" || h == "*":
		return "anonymous"
	case strings.HasPrefix(h, "gss/"):
		return "gss"
	case h[0] == '@':
		return "netgroup"
	}
	for i := 0; i < len(h); i++ {
		switch h[i] {
		case '*', '?', '[':
			return "wildcard"
		case '/':
			return "subnet"
		case '\\':
			i++
		}
	}
	return "fqdn"
}

// mayMatch reports whether etab client c can match an address of network
// p. A name, wildcard or netgroup, or a subnet spelling Go does not take
// (nfs-utils also takes "/024", "/+24" and dotted masks), may match any.
func (c NFSClient) mayMatch(p netip.Prefix) bool {
	switch etabClass(c.Host) {
	case "gss":
		return false
	case "subnet":
		if q, ok := c.prefix(); ok {
			return q.Overlaps(p)
		}
	case "fqdn":
		if a, err := netip.ParseAddr(c.Host); err == nil && a.Is4() && a.String() == c.Host {
			return p.Contains(a)
		}
	}
	return true
}

func unescapeOctal(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// Lookup returns how path is exported to client host (an address or CIDR,
// as ParseClient takes it). Entries compare by normalized network: only an
// etab subnet entry for the same network matches. A name or bare address
// in etab never matches, and neither does a different network that merely
// contains host.
func (t Etab) Lookup(path, host string) (NFSClient, bool) {
	want, err := parseClient(host)
	if err != nil {
		return NFSClient{}, false
	}
	for _, c := range t[path] {
		if p, ok := c.prefix(); ok && p == want {
			return c, true
		}
	}
	return NFSClient{}, false
}

// serves reports whether path is exported to c in c's mode, and every
// address of c gets that line: mountd answers a request with the first
// matching export by client class (an fqdn line beats every subnet line),
// then by etab order (nfs-utils cache.c lookup_export). A line with
// options our sharenfs never yields does not count.
func (t Etab) serves(path string, c NFSClient) bool {
	want, err := parseClient(c.Host)
	if err != nil {
		return false
	}
	own := false
	for _, x := range t[path] {
		cls := etabClass(x.Host)
		if p, ok := x.prefix(); ok && cls == "subnet" && p == want && !own {
			if x.unknownMode || x.broad || x.ReadOnly != c.ReadOnly {
				return false
			}
			own = true
			continue
		}
		if (cls == "fqdn" || (cls == "subnet" && !own)) && x.mayMatch(want) {
			return false
		}
	}
	return own
}

// exposure lists what serves path beyond want: an etab line of path that
// is not exactly one of want (a different network, a name, "*", a broader
// option, the other mode), and every crossmnt line of an ancestor path,
// which exports this volume's mount to its own clients (nfs-utils
// cache.c path_matches: an inherited `sharenfs=on` is "rw,crossmnt" to
// "*" on every ancestor).
func (t Etab) exposure(path string, want []NFSClient) []string {
	var out []string
	for _, x := range t.extra(path, want) {
		out = append(out, x.String())
	}
	slices.Sort(out)
	return out
}

// etabLine is one etab client of a path.
type etabLine struct {
	path string
	c    NFSClient
}

func (l etabLine) String() string {
	s := l.path + " " + l.c.Host + "(" + l.c.etabMode()
	if l.c.crossmnt {
		s += ",crossmnt"
	}
	return s + ")"
}

func (t Etab) extra(path string, want []NFSClient) []etabLine {
	var out []etabLine
	for _, x := range t[path] {
		ok := false
		if p, isNet := x.prefix(); isNet && etabClass(x.Host) == "subnet" && !x.unknownMode && !x.broad {
			for _, w := range want {
				if wp, _ := w.prefix(); wp == p && w.ReadOnly == x.ReadOnly {
					ok = true
				}
			}
		}
		if !ok {
			out = append(out, etabLine{path, x})
		}
	}
	return append(out, t.crossmntAncestors(path)...)
}

// crossmntAncestors lists the crossmnt lines of path's ancestors.
func (t Etab) crossmntAncestors(path string) []etabLine {
	var out []etabLine
	for k, cs := range t {
		if !isAncestor(k, path) {
			continue
		}
		for _, x := range cs {
			if x.crossmnt {
				out = append(out, etabLine{k, x})
			}
		}
	}
	slices.SortFunc(out, func(a, b etabLine) int { return strings.Compare(a.String(), b.String()) })
	return out
}

// covers lists the lines beyond want that may still serve host on path:
// what Unpublish must see gone (want's own lines may serve host by
// design: it was granted twice).
func (t Etab) covers(path, host string, want []NFSClient) []string {
	h, err := parseClient(host)
	if err != nil {
		return nil
	}
	var out []string
	for _, x := range t.extra(path, want) {
		if x.c.mayMatch(h) {
			out = append(out, x.String())
		}
	}
	slices.Sort(out)
	return out
}

func (c NFSClient) etabMode() string {
	if c.unknownMode {
		return "?"
	}
	return c.mode()
}

// isAncestor reports whether dir is a proper ancestor of path
// (cache.c is_subdirectory).
func isAncestor(dir, path string) bool {
	if dir == "/" {
		return len(path) > 1
	}
	return len(path) > len(dir) && strings.HasPrefix(path, dir) && path[len(dir)] == '/'
}

// at returns the table with path's entries read from real: exportfs
// stores each export under its realpath (nfs-utils nfsd_realpath), while
// the endpoint knows the mountpoint property.
func (t Etab) at(path, real string) Etab {
	if path == real {
		return t
	}
	v := make(Etab, len(t)+1)
	for k, cs := range t {
		v[k] = cs
	}
	v[path] = t[real]
	return v
}

// realPath resolves symlinks in an export path like nfsd_realpath; a path
// that cannot be resolved is used as it is.
func realPath(p string) string {
	if !filepath.IsAbs(p) {
		return p
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// EtabReader reads the kernel export table. The endpoint uses it to verify
// what `zfs set sharenfs` cannot report; tests inject a fake.
type EtabReader interface {
	ReadEtab(ctx context.Context) ([]byte, error)
}

// FileEtab reads an etab file; a missing file is an empty table (no NFS
// server has ever exported anything).
type FileEtab string

// DefaultEtab is where nfs-utils keeps the table.
const DefaultEtab FileEtab = "/var/lib/nfs/etab"

func (f FileEtab) ReadEtab(context.Context) ([]byte, error) {
	b, err := os.ReadFile(string(f))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

// NFSDReader reports how many nfsd threads run. etab alone does not show a
// running server: `exportfs -ra` (every sharenfs change) rewrites it with
// nfs-server stopped. Tests inject a fake; the simulator models one.
type NFSDReader interface {
	NFSDThreads(ctx context.Context) (int, error)
}

// FileNFSD reads a thread count file. A missing file (nfsd module not
// loaded or its filesystem not mounted) is 0 threads. Reading
// /proc/fs/nfsd/threads needs root, which file export needs anyway.
type FileNFSD string

// DefaultNFSD is the kernel's nfsd thread count.
const DefaultNFSD FileNFSD = "/proc/fs/nfsd/threads"

func (f FileNFSD) NFSDThreads(context.Context) (int, error) {
	b, err := os.ReadFile(string(f))
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

// ---- planners ---------------------------------------------------------------

// exportRow checks what both directions need: a managed filesystem whose
// stored client list is readable. A malformed id names no volume.
func (p Planner) exportRow(t Table, volID string) (Row, []NFSClient, bool, error) {
	id, err := issuedVolumeID(volID)
	if err != nil {
		return Row{}, nil, false, err
	}
	// absent is NotFound for publish and done for unpublish
	if err := t.mustCover(id); err != nil {
		return Row{}, nil, false, err
	}
	row, ok := t.Get(id)
	if !ok || !row.OwnedVolume() {
		return Row{}, nil, false, nil
	}
	if row.Kind() == Block {
		return row, nil, true, errorf(FailedPrecondition, "volume %s is a zvol; only filesystems can be NFS-exported", id)
	}
	if !row.NFSClientsSet {
		return row, nil, true, nil
	}
	cs, err := decodeClients(row.NFSClients)
	if err != nil {
		return row, nil, true, &Error{Code: FailedPrecondition, Msg: "volume " + id + ": " + p.Names.nfsClients(), Err: err}
	}
	return row, cs, true, nil
}

// foreign reports a sharenfs that exports something and that the endpoint
// did not write, so it never overwrites it:
//   - set locally by someone else (e.g. democratic-csi's driver-wide value);
//   - received (`zfs recv` of a stream with properties): the sending
//     side's export decision. A local value would silently shadow it, and
//     adopting it would mean parsing options and names the endpoint never
//     writes; it is refused instead. `zfs set sharenfs=off` (local) hands
//     the volume to the endpoint. A received "off" exports nothing and is
//     pinned locally like an inherited one.
func foreign(row Row) bool {
	if row.ShareNFS == "off" {
		return false
	}
	return row.ShareNFSRecv || (!row.NFSClientsSet && row.ShareNFSLocal)
}

func foreignErr(row Row, what string) error {
	src := "set outside the endpoint"
	if row.ShareNFSRecv {
		src = "received from a replication stream"
	}
	return errorf(FailedPrecondition, "volume %s %s sharenfs=%q, %s; set sharenfs=off on it to let the endpoint manage exports", row.ID, what, row.ShareNFS, src)
}

// exportOps sets the stored list and sharenfs where they differ from want.
// An inherited sharenfs is always pinned locally: a parent shared to "*"
// would otherwise keep exporting the volume to everyone.
func (p Planner) exportOps(row Row, want []NFSClient) (Plan, error) {
	share, enc := ShareNFS(want), encodeClients(want)
	if len(share) > maxPropLen || len(enc) > maxPropLen {
		return nil, errorf(ResourceExhausted, "volume %s: %d clients exceed the %d-byte property limit (sharenfs %d, %s %d bytes); use a CIDR",
			row.ID, len(want), maxPropLen, len(share), p.Names.nfsClients(), len(enc))
	}
	set := map[string]string{}
	if !row.NFSClientsSet || row.NFSClients != enc {
		set[p.Names.nfsClients()] = enc
	}
	if !row.ShareNFSLocal || row.ShareNFS != share {
		set["sharenfs"] = share
	}
	if len(set) == 0 {
		return nil, nil
	}
	return Plan{{Kind: OpSet, Target: row.Name, Props: set}}, nil
}

// reshare re-sets an unchanged sharenfs: libzfs rewrites the exports line
// and re-runs exportfs, which is how a previously failed export is retried.
// `zfs share` is not used: it exits 1 on an already shared dataset.
func reshare(row Row, want []NFSClient) Plan {
	return Plan{{Kind: OpSet, Target: row.Name, Props: map[string]string{"sharenfs": ShareNFS(want)}}}
}

// PlanPublishExport adds client c to volume volID. The same client in the
// same mode is an empty plan (once the kernel serves it); in the other mode
// it is AlreadyExists; a different network overlapping one already
// exported is FailedPrecondition. live is the current kernel export table,
// keyed by the mountpoint. It returns the volume's row, whose Mount is the
// export path.
func (p Planner) PlanPublishExport(t Table, live Etab, volID string, c NFSClient) (Plan, Row, error) {
	row, cs, ok, err := p.exportRow(t, volID)
	if err != nil {
		return nil, Row{}, err
	}
	if !ok {
		return nil, Row{}, errorf(NotFound, "volume %s not found", volID)
	}
	// unmounted with canmount=on: setting sharenfs mounts and shares it
	// (changelist_postfix), which is what a publish asks for
	if !row.Mounted && (row.CanMount != "on" || row.Mount == "none" || row.Mount == "legacy" || row.Mount == "") {
		return nil, Row{}, errorf(FailedPrecondition, "volume %s is not mounted on the storage host and zfs would not mount it (canmount=%s, mountpoint %q): sharenfs would be stored but nothing served",
			row.ID, row.CanMount, row.Mount)
	}
	if foreign(row) {
		return nil, Row{}, foreignErr(row, "has")
	}
	// an ancestor exported with crossmnt (an inherited sharenfs=on is
	// "rw,crossmnt" to "*" on every ancestor) serves this mount to its own
	// clients whatever this volume's sharenfs says
	if x := live.crossmntAncestors(row.Mount); len(x) > 0 {
		var s []string
		for _, l := range x {
			s = append(s, l.String())
		}
		return nil, Row{}, errorf(FailedPrecondition, "volume %s is exported through an ancestor with crossmnt: %s; set sharenfs=off (or drop crossmnt) on the ancestor first",
			row.ID, strings.Join(s, "; "))
	}
	want := cs
	if x, ok := overlap(cs, c); ok {
		switch {
		case x.Host != c.Host:
			return nil, Row{}, errorf(FailedPrecondition, "volume %s is exported to %s, which overlaps %s: nfs-utils applies the first matching line, so both cannot hold; unpublish %s first",
				row.ID, x.Host, c.Host, x.Host)
		case x.ReadOnly != c.ReadOnly:
			return nil, Row{}, errorf(AlreadyExists, "volume %s is already exported to %s %s", row.ID, c.Host, x.mode())
		}
	} else {
		want = sortClients(append(slices.Clone(cs), c))
	}
	plan, err := p.exportOps(row, want)
	if err != nil {
		return nil, Row{}, err
	}
	// unchanged but not served, or served beyond want (a stale etab after
	// a failed exportfs): re-share once; what a re-share cannot fix
	// (another exports file, an ancestor's crossmnt) the caller reports
	if len(plan) == 0 && (!live.serves(row.Mount, c) || len(live.exposure(row.Mount, want)) > 0) {
		plan = reshare(row, want)
	}
	return plan, row, nil
}

// publishWant is the client list a successful PlanPublishExport of c on
// row leads to.
func publishWant(row Row, c NFSClient) []NFSClient {
	var cs []NFSClient
	if row.NFSClientsSet {
		cs, _ = decodeClients(row.NFSClients)
	}
	if _, ok := overlap(cs, c); ok {
		return cs
	}
	return sortClients(append(cs, c))
}

// unpublishWant is the client list after removing host from row's.
func unpublishWant(row Row, host string) []NFSClient {
	var cs []NFSClient
	if row.NFSClientsSet {
		cs, _ = decodeClients(row.NFSClients)
	}
	h, err := parseClient(host)
	if err != nil {
		return cs
	}
	return slices.DeleteFunc(cs, func(x NFSClient) bool {
		px, _ := x.prefix()
		return px == h
	})
}

// PlanUnpublishExport removes host from volume volID. A missing volume, a
// malformed id or a host that is not exported is done; the last host leaves
// sharenfs=off set locally (never inherited: the parent may be shared). It
// returns the row, or a zero Row when there is nothing to verify.
func (p Planner) PlanUnpublishExport(t Table, live Etab, volID, host string) (Plan, Row, error) {
	c, err := ParseClient(host, false)
	if err != nil {
		return nil, Row{}, err
	}
	row, cs, ok, err := p.exportRow(t, volID)
	if !ok {
		if CodeOf(err) == NotFound {
			return nil, Row{}, nil // never issued, so never exported
		}
		return nil, Row{}, err
	}
	if err != nil {
		if row.Kind() == Block {
			return nil, Row{}, nil // a zvol is never NFS-exported
		}
		return nil, Row{}, err
	}
	want := slices.DeleteFunc(slices.Clone(cs), func(x NFSClient) bool {
		px, _ := x.prefix()
		pc, _ := c.prefix()
		return px == pc
	})
	// served: its own line, or (on a volume whose sharenfs is ours) any
	// line beyond want that can match it, e.g. an admin's edit of our
	// sharenfs to "rw=*" (rewritten below). A foreign sharenfs is never
	// rewritten, and a client it covers was never granted by us.
	_, isLive := live.Lookup(row.Mount, c.Host)
	isLive = isLive || (!foreign(row) && len(live.covers(row.Mount, c.Host, want)) > 0)
	if len(want) == len(cs) {
		// not one of ours; still served means drift or a foreign sharenfs
		switch {
		case !isLive:
			return nil, row, nil
		case foreign(row):
			return nil, Row{}, foreignErr(row, "is exported to "+c.Host+" by")
		}
	}
	plan, err := p.exportOps(row, want)
	if err != nil {
		return nil, Row{}, err
	}
	if len(plan) == 0 && isLive {
		plan = reshare(row, want)
	}
	return plan, row, nil
}
