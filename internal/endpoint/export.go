package endpoint

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io/fs"
	"net/netip"
	"os"
	"regexp"
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
// a planner input and is re-read after every change.

// NFSClient is one host allowed to mount a volume.
type NFSClient struct {
	// Host is a lowercase hostname, an IPv4 address or an IPv4 CIDR
	// (masked; a /32 is written as the bare address).
	Host     string
	ReadOnly bool
}

func (c NFSClient) mode() string {
	if c.ReadOnly {
		return "ro"
	}
	return "rw"
}

// maxShareLen is ZFS_MAXPROPLEN minus the terminating NUL: the longest
// sharenfs value zfs accepts.
const maxShareLen = 4095

var (
	hostChars = regexp.MustCompile(`^[A-Za-z0-9./-]+$`)
	hostLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
)

// ParseClient validates and normalizes a client spec. Only hostnames, IPv4
// addresses and IPv4 CIDRs are accepted, from an allowlist of characters:
// sharenfs separates options with ',' and hosts with ':', and exportfs reads
// '(' '*' '@' and spaces, so none of them may reach the property. Wildcards
// and netgroups are refused; "*" would export to every host.
func ParseClient(host string, readOnly bool) (NFSClient, error) {
	h, err := normalizeHost(host)
	if err != nil {
		return NFSClient{}, err
	}
	return NFSClient{Host: h, ReadOnly: readOnly}, nil
}

func normalizeHost(host string) (string, error) {
	bad := errorf(InvalidArgument, "invalid NFS client %q: want a hostname, IPv4 address or IPv4 CIDR", host)
	if host == "" || len(host) > 253 || !hostChars.MatchString(host) {
		return "", bad
	}
	if strings.Contains(host, "/") {
		p, err := netip.ParsePrefix(host)
		if err != nil || !p.Addr().Is4() {
			return "", bad
		}
		p = p.Masked()
		if p.Bits() == 32 {
			return p.Addr().String(), nil
		}
		return p.String(), nil
	}
	if a, err := netip.ParseAddr(host); err == nil {
		if !a.Is4() {
			return "", bad
		}
		return a.String(), nil
	}
	h := strings.ToLower(host)
	labels := strings.Split(h, ".")
	for _, l := range labels {
		if len(l) > 63 || !hostLabel.MatchString(l) {
			return "", bad
		}
	}
	// "10.1" or "1.2.3.4.5" would be read as (partial) addresses
	if _, err := strconv.Atoi(strings.ReplaceAll(labels[len(labels)-1], "-", "")); err == nil {
		return "", bad
	}
	return h, nil
}

// encodeClients is the stored form of a client list: "host=rw,host=ro"
// sorted by host, "none" for an empty list (a user property cannot hold "").
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
// edit is refused instead of half-read.
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
		cs = append(cs, c)
	}
	if encodeClients(cs) != s {
		return nil, errorf(FailedPrecondition, "client list %q is not canonical", s)
	}
	return cs, nil
}

func sortClients(cs []NFSClient) []NFSClient {
	cs = slices.Clone(cs)
	slices.SortFunc(cs, func(a, b NFSClient) int { return strings.Compare(a.Host, b.Host) })
	return cs
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
// per client, with special characters in the path written as \ooo.
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
		mode := opts[:strings.IndexAny(opts+",", ",)")]
		path = unescapeOctal(path)
		t[path] = append(t[path], NFSClient{Host: strings.ToLower(host), ReadOnly: mode == "ro"})
	}
	return t
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

// Lookup returns how path is exported to host. exportfs records a hostname
// by its canonical (DNS) name, so "node-a" also matches "node-a.example".
func (t Etab) Lookup(path, host string) (NFSClient, bool) {
	for _, c := range t[path] {
		if c.Host == host || (isName(host) && strings.HasPrefix(c.Host, host+".")) {
			return c, true
		}
	}
	return NFSClient{}, false
}

// isName: a hostname has a letter; addresses and CIDRs have none.
func isName(host string) bool {
	return strings.ContainsFunc(host, func(r rune) bool { return 'a' <= r && r <= 'z' })
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

// ---- planners ---------------------------------------------------------------

// exportRow checks what both directions need: a managed filesystem whose
// stored client list is readable.
func (p Planner) exportRow(t Table, volID string) (Row, []NFSClient, bool, error) {
	id, err := volumeID(volID)
	if err != nil {
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

// foreign reports a sharenfs set on the volume by something other than the
// endpoint (e.g. democratic-csi's driver-wide value): never overwritten.
func foreign(row Row) bool {
	return !row.NFSClientsSet && row.ShareNFSLocal && row.ShareNFS != "off"
}

// exportOps sets the stored list and sharenfs where they differ from want.
// An inherited sharenfs is always pinned locally: a parent shared to "*"
// would otherwise keep exporting the volume to everyone.
func (p Planner) exportOps(row Row, want []NFSClient) (Plan, error) {
	share := ShareNFS(want)
	if len(share) > maxShareLen {
		return nil, errorf(ResourceExhausted, "volume %s: %d clients exceed the sharenfs length limit; use a CIDR", row.ID, len(want))
	}
	set := map[string]string{}
	if enc := encodeClients(want); !row.NFSClientsSet || row.NFSClients != enc {
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
// it is AlreadyExists. live is the current kernel export table. It returns
// the volume's row, whose Mount is the export path.
func (p Planner) PlanPublishExport(t Table, live Etab, volID string, c NFSClient) (Plan, Row, error) {
	row, cs, ok, err := p.exportRow(t, volID)
	if err != nil {
		return nil, Row{}, err
	}
	if !ok {
		return nil, Row{}, errorf(NotFound, "volume %s not found", volID)
	}
	if !row.Mounted {
		return nil, Row{}, errorf(FailedPrecondition, "volume %s is not mounted on the storage host (mountpoint %q); zfs would store the export and serve nothing", row.ID, row.Mount)
	}
	if foreign(row) {
		return nil, Row{}, errorf(FailedPrecondition, "volume %s has sharenfs=%q set outside the endpoint; set it to off to let the endpoint manage exports", row.ID, row.ShareNFS)
	}
	want := cs
	if i := slices.IndexFunc(cs, func(x NFSClient) bool { return x.Host == c.Host }); i >= 0 {
		if cs[i].ReadOnly != c.ReadOnly {
			return nil, Row{}, errorf(AlreadyExists, "volume %s is already exported to %s %s", row.ID, c.Host, cs[i].mode())
		}
	} else {
		want = sortClients(append(slices.Clone(cs), c))
	}
	plan, err := p.exportOps(row, want)
	if err != nil {
		return nil, Row{}, err
	}
	if len(plan) == 0 {
		if got, ok := live.Lookup(row.Mount, c.Host); !ok || got.ReadOnly != c.ReadOnly {
			plan = reshare(row, want)
		}
	}
	return plan, row, nil
}

// PlanUnpublishExport removes host from volume volID. A missing volume or a
// host that is not exported is done; the last host leaves sharenfs=off set
// locally (never inherited: the parent may be shared). It returns the row,
// or a zero Row when there is nothing to verify.
func (p Planner) PlanUnpublishExport(t Table, live Etab, volID, host string) (Plan, Row, error) {
	row, cs, ok, err := p.exportRow(t, volID)
	if !ok {
		return nil, Row{}, err
	}
	if err != nil {
		if row.Kind() == Block {
			return nil, Row{}, nil // a zvol is never NFS-exported
		}
		return nil, Row{}, err
	}
	want := slices.DeleteFunc(slices.Clone(cs), func(x NFSClient) bool { return x.Host == host })
	_, isLive := live.Lookup(row.Mount, host)
	if len(want) == len(cs) {
		// not one of ours; still served means drift or a foreign sharenfs
		switch {
		case !isLive:
			return nil, row, nil
		case foreign(row):
			return nil, Row{}, errorf(FailedPrecondition, "volume %s is exported to %s by sharenfs=%q, set outside the endpoint", row.ID, host, row.ShareNFS)
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
