package endpoint

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/wommy/empty/internal/zfs"
)

// OpKind is one zfs mutation.
type OpKind uint8

const (
	OpCreateFS OpKind = iota + 1
	OpCreateVol
	OpSnapshot
	OpClone
	OpSet
	OpDestroy
	// OpMount: `zfs mount` of a filesystem; already mounted is done
	// (OkExists).
	OpMount
	// OpSetRoot: the owner and mode of a mounted filesystem's root
	// directory (Config.Root), Props uid, gid (decimal) and mode (octal),
	// each absent when left as it is, at Path, the mountpoint the plan's
	// scan read. Not a zfs call; idempotent by itself.
	OpSetRoot
)

var opNames = map[OpKind]string{
	OpCreateFS: "create-filesystem", OpCreateVol: "create-volume", OpSnapshot: "snapshot",
	OpClone: "clone", OpSet: "set", OpDestroy: "destroy", OpMount: "mount", OpSetRoot: "set-root",
}

func (k OpKind) String() string {
	if n, ok := opNames[k]; ok {
		return n
	}
	return fmt.Sprintf("OpKind(%d)", uint8(k))
}

// Op is a planned mutation, as plain data. Each op states how a replay
// tolerates finding its work already done, so re-applying a partially
// applied plan converges instead of failing.
type Op struct {
	Kind      OpKind
	Target    string            // full zfs name
	Source    string            // clone origin snapshot
	Size      uint64            // volsize for OpCreateVol
	Sparse    bool              // OpCreateVol
	Recursive bool              // OpDestroy
	Defer     bool              // OpDestroy of a snapshot
	Props     map[string]string // create/clone/snapshot -o, or OpSet values
	OkExists  bool              // create/snapshot/clone: already exists is done
	OkMissing bool              // destroy: already gone is done
	// GUID: OpDestroy, the guid of the dataset the plan inspected under
	// Target. apply destroys Target only while it still has this guid: a
	// dataset renamed away and another made under the name since is never
	// destroyed in its place (OWN-15).
	GUID string
	Path string // OpSetRoot: the mountpoint
}

// Plan is the ordered ops of one request.
type Plan []Op

// Planner holds what planning needs besides the table: pure configuration.
type Planner struct {
	Parent       string
	Names        props
	VolBlockSize uint64
	// DestroyForeignSnapshots: Config.DestroyForeignSnapshots.
	DestroyForeignSnapshots bool
	// RequireMounted: the endpoint can mount (it runs as root), so a
	// filesystem volume zfs would mount is committed only once it is
	// mounted: the resume after the create's scan mounts an unmounted one
	// (OpMount) right before the commit. OpenZFS 2.4's `zfs create` and
	// `zfs clone` exit 0 when the mount after the create fails (zfs_main.c
	// ignores zfs_mount_and_share), and a kill after the create ioctl
	// skips the mount entirely; committing then would hand out a
	// mountpoint that is a plain directory of the parent.
	RequireMounted bool
	// NFSServers and NFSMinPrefix are Config's: what a CreateVolume NFS
	// export is checked against.
	NFSServers   []string
	NFSMinPrefix int

	// now, when set, is stamped as committed_at by a create's commit: the
	// caller's clock reading, so planning stays pure.
	now int64
	// parentMount, when an absolute path, is the parent's mountpoint as
	// the caller read it: a new filesystem's mountpoint is pinned to
	// <parentMount>/<id>, set locally in the create (csi-nfs-design §4).
	parentMount string
}

func (p Planner) full(id string) string { return p.Parent + "/" + id }

func u64(n uint64) string { return strconv.FormatUint(n, 10) }

// want is the capacity a request resolves to (rounded for zvols, to the
// requested volblocksize when props, the validated properties, carry one).
func (p Planner) want(r CreateVolumeRequest, props map[string]string) uint64 {
	if r.Kind == Block {
		bs, _ := strconv.ParseUint(props[volblocksizeProp], 10, 64)
		return p.roundTo(r.CapacityBytes, bs)
	}
	if r.CapacityBytes == 0 && r.LimitBytes > 0 {
		// "at most limit": unlimited would violate it
		return r.LimitBytes
	}
	return r.CapacityBytes
}

// MaxCapacityBytes is the largest capacity or limit a request may name:
// 8 EiB. Above it, rounding up to a block size could wrap past 2^64 to a
// small number, and OpenZFS keeps sizes in int64 arithmetic in places.
const MaxCapacityBytes = math.MaxInt64

// defaultVolBlockSize is the rounding unit when neither the volume nor the
// Planner names one (a Planner built outside New).
const defaultVolBlockSize = 16 * 1024

// roundTo rounds n up to a multiple of bs (a volume's own volblocksize,
// falling back to the configured default). Callers bound n by
// MaxCapacityBytes first, so the result cannot wrap.
func (p Planner) roundTo(n, bs uint64) uint64 {
	if bs == 0 {
		bs = p.VolBlockSize
	}
	if bs == 0 {
		bs = defaultVolBlockSize
	}
	if r := n % bs; r != 0 {
		n += bs - r
	}
	return n
}

// validateCreate checks a request on its own, before looking at state, and
// returns the volume id and the canonical properties.
func (p Planner) validateCreate(r CreateVolumeRequest) (string, map[string]string, error) {
	id, err := volumeID(r.Name)
	if err != nil {
		return "", nil, err
	}
	if r.CapacityBytes > MaxCapacityBytes || r.LimitBytes > MaxCapacityBytes {
		return "", nil, errorf(OutOfRange, "capacity %d / limit %d above %d bytes", r.CapacityBytes, r.LimitBytes, uint64(MaxCapacityBytes))
	}
	props, err := ValidateProperties(r.Kind, r.Properties)
	if err != nil {
		return "", nil, err
	}
	if r.SourceSnapshotID != "" && r.SourceVolumeID != "" {
		return "", nil, errorf(InvalidArgument, "at most one content source")
	}
	if r.Kind == Block && r.CapacityBytes == 0 && r.SourceSnapshotID == "" && r.SourceVolumeID == "" {
		return "", nil, errorf(InvalidArgument, "block volume needs a capacity")
	}
	if r.Kind == Filesystem && r.Reserve && p.want(r, props) == 0 && r.SourceSnapshotID == "" && r.SourceVolumeID == "" {
		return "", nil, errorf(InvalidArgument, "reserve needs a capacity or limit")
	}
	if r.Kind == Block && r.Reserve {
		return "", nil, errorf(InvalidArgument, "reserve applies to filesystems; use sparse for zvols")
	}
	if r.Kind == Filesystem && r.Sparse {
		return "", nil, errorf(InvalidArgument, "sparse applies to zvols; filesystems are thin unless reserved")
	}
	// a zvol clone rounds to its source's block size, which only the table
	// knows: PlanCreateVolume checks its limit
	blockClone := r.Kind == Block && (r.SourceSnapshotID != "" || r.SourceVolumeID != "")
	if r.LimitBytes > 0 && !blockClone && p.want(r, props) > r.LimitBytes {
		return "", nil, errorf(OutOfRange, "capacity %d (rounded) exceeds limit %d", p.want(r, props), r.LimitBytes)
	}
	if _, err := p.validateNFS(r); err != nil {
		return "", nil, err
	}
	return id, props, nil
}

// nfsWant is a validated CreateVolume NFS export.
type nfsWant struct {
	clients  []NFSClient // sorted, canonical
	server   string      // resolved: the request's, or the first advertised
	explicit bool        // the request named the server
	root     rootAttrs   // the volume root's owner and mode (-1: as made)
}

// validateNFS checks a request's NFS export on its own: a filesystem, an
// endpoint that advertises NFS servers, client networks in the canonical
// grammar (each with an explicit /N, host bits zero, at or above the
// floor, none overlapping), never masked or completed, and an advertised
// server. Nil without an export.
func (p Planner) validateNFS(r CreateVolumeRequest) (*nfsWant, error) {
	if r.NFS == nil {
		return nil, nil
	}
	if r.Kind == Block {
		return nil, errorf(InvalidArgument, "an NFS export needs a filesystem volume: a zvol cannot be exported over NFS")
	}
	if len(p.NFSServers) == 0 {
		return nil, errorf(FailedPrecondition, "this endpoint advertises no NFS server (-nfs-server), so it takes no NFS export")
	}
	if len(r.NFS.Clients) == 0 {
		return nil, errorf(InvalidArgument, "an NFS export needs at least one client network")
	}
	floor := cmp.Or(p.NFSMinPrefix, DefaultNFSMinPrefix)
	var cs []NFSClient
	for _, h := range r.NFS.Clients {
		if h == "" {
			return nil, errorf(InvalidArgument, "empty NFS client entry (an empty host exports to everyone)")
		}
		c, err := ParseClient(h, r.NFS.ReadOnly)
		if err != nil {
			return nil, err
		}
		if !strings.Contains(h, "/") {
			return nil, errorf(InvalidArgument, "NFS client %q: write the network with its prefix length (%s): nfs-utils resolves and renames a bare address", h, c.Host)
		}
		if err := checkFloor(c, floor); err != nil {
			return nil, err
		}
		if x, ok := overlap(cs, c); ok {
			return nil, errorf(InvalidArgument, "NFS clients %s and %s overlap: nfs-utils applies the first matching line, so both cannot hold", x.Host, c.Host)
		}
		cs = append(cs, c)
	}
	cs = sortClients(cs)
	if n := len(ShareNFS(cs)); n > maxPropLen {
		return nil, errorf(InvalidArgument, "%d NFS clients make a %d-byte sharenfs, over the %d-byte limit; use wider networks", len(cs), n, maxPropLen)
	}
	root, err := validRoot(r.NFS)
	if err != nil {
		return nil, err
	}
	server := r.NFS.Server
	if server == "" {
		server = p.NFSServers[0]
	} else if !slices.Contains(p.NFSServers, server) {
		return nil, errorf(InvalidArgument, "NFS server %q is not one this endpoint advertises (%s)", server, strings.Join(p.NFSServers, ", "))
	}
	return &nfsWant{clients: cs, server: server, explicit: r.NFS.Server != "", root: root}, nil
}

// ownsRoot reports whether the export sets the volume root's owner or
// mode. Nil-safe.
func (w *nfsWant) ownsRoot() bool { return w != nil && w.root.set() }

// nfsCompatible applies CreateVolume idempotency to a committed volume's
// export. The value is compared as parsed (canonical form is unique, so
// order never matters); an admin's edit inside the grammar is the volume's
// value, and the message names it. The server is fixed at create: another
// one named explicitly is AlreadyExists, an omitted one takes the recorded
// one, even when the advertised list changed since (csi-nfs-design §6 as
// amended by wave13 Q1).
func (p Planner) nfsCompatible(row Row, w *nfsWant) error {
	if w == nil {
		return nil
	}
	want := ShareNFS(w.clients)
	if !row.ShareNFSLocal || foreign(row) {
		src := "inherited"
		switch {
		case row.ShareNFSRecv:
			src = "received"
		case row.ShareNFSLocal:
			src = "set outside the endpoint's grammar"
		}
		return errorf(AlreadyExists, "volume %s exists with sharenfs=%q (%s), requested %s", row.ID, row.ShareNFS, src, want)
	}
	if row.ShareNFS != want {
		return errorf(AlreadyExists, "volume %s exists exported %s, requested %s", row.ID, row.ShareNFS, want)
	}
	if w.explicit && row.NFSServer != w.server {
		return errorf(AlreadyExists, "volume %s was created with NFS server %q, requested %q: a volume's server never changes (its PVs hold it)", row.ID, row.NFSServer, w.server)
	}
	return nil
}

// PlanCreateVolume decides how to make the table satisfy r.
//
//   - no row: create (or clone) with ownership set atomically, then commit
//   - uncommitted row we own with the same source: converge it (a crash or
//     failure mid-provision), then commit
//   - committed row: must be compatible (CSI idempotency) and the plan is
//     empty, or AlreadyExists
func (p Planner) PlanCreateVolume(t Table, r CreateVolumeRequest) (Plan, error) {
	id, props, err := p.validateCreate(r)
	if err != nil {
		return nil, err
	}
	nfs, err := p.validateNFS(r)
	if err != nil {
		return nil, err
	}
	want := p.want(r, props)
	srcType, srcID := sourceOf(r)
	// absent means "create it" for id and "NotFound" for the source
	if err := t.mustCover(id, srcID); err != nil {
		return nil, err
	}
	// a clone keeps its source's volblocksize (cloneBlockSize), and zfs
	// refuses a volsize that is not a multiple of the zvol's own block
	// size: round to the source's, not to the configured default
	if bs, ok := sourceBlockSize(t, r); ok {
		if _, named := props[volblocksizeProp]; !named {
			want = p.roundTo(r.CapacityBytes, bs)
		}
		if r.LimitBytes > 0 && want > r.LimitBytes {
			return nil, errorf(OutOfRange, "capacity %d (rounded to the source's volblocksize %d) exceeds limit %d", want, bs, r.LimitBytes)
		}
	}
	commit := Op{Kind: OpSet, Target: p.full(id), Props: map[string]string{p.Names.success(): "true"}}
	if p.now > 0 {
		commit.Props[p.Names.committedAt()] = strconv.FormatInt(p.now, 10)
	}
	// size is what a provision drives the volume to. A clone starts at its
	// source's size and is never shrunk: a filesystem clone gets
	// max(requested, its source's refquota), as a zvol clone keeps its
	// source's volsize (sizeOps only grows). An unlimited source (refquota
	// 0) leaves the request as is, as a fresh create would. It is derived
	// here, before the resume branch, so a retry after a failure at any op
	// boundary reaches the end state an uninterrupted run reaches.
	size, sizeErr := want, error(nil)
	if n, ok := sourceSize(t, r); ok {
		if r.Kind == Filesystem && n > size {
			size = n
		}
		if r.LimitBytes > 0 && n > r.LimitBytes {
			sizeErr = errorf(OutOfRange, "source capacity %d exceeds limit %d: a clone is never smaller than its source", n, r.LimitBytes)
		} else if r.Kind == Filesystem && r.Reserve && size == 0 {
			sizeErr = errorf(InvalidArgument, "reserve needs a capacity or limit (the source is unlimited)")
		}
	}

	if row, ok := t.Get(id); ok {
		// the one ownership rule (ADR 0003): managed without the name
		// marker, or with another volume's, is not ours, and nothing may
		// be written on it, least of all the commit
		if !row.OwnedVolume() {
			return nil, errorf(AlreadyExists, "dataset %s exists and is not the endpoint's volume %s (managed=%v, volume name %q)",
				row.Name, id, row.Managed, row.VolumeName)
		}
		if row.Kind() != r.Kind || row.SourceType != srcType || row.SourceID != srcID {
			return nil, errorf(AlreadyExists, "volume %s exists as %s from %q %q", id, row.Kind(), row.SourceType, row.SourceID)
		}
		if !row.Committed {
			if sizeErr != nil {
				return nil, sizeErr
			}
			// volblocksize is create-only: a row without it cannot converge
			if bs, ok := props[volblocksizeProp]; ok && u64(row.BlockSz) != bs {
				return nil, errorf(AlreadyExists, "volume %s exists with volblocksize %d, requested %s", id, row.BlockSz, bs)
			}
			plan := p.sizeOps(id, row, r, size)
			set := propSet(row, props)
			if nfs != nil {
				// nobody holds an uncommitted volume's context yet: it
				// converges to the request, server included
				if share := ShareNFS(nfs.clients); !row.ShareNFSLocal || row.ShareNFS != share {
					set["sharenfs"] = share
				}
				if row.NFSServer != nfs.server {
					set[p.Names.nfsServer()] = nfs.server
				}
			}
			if len(set) > 0 {
				plan = append(plan, Op{Kind: OpSet, Target: p.full(id), Props: set})
			}
			if nfs.ownsRoot() && (!mountable(row) || !row.Mounted && !p.RequireMounted) {
				return nil, errorf(FailedPrecondition, "volume %s is not mounted (canmount=%s, mountpoint=%s): its root's owner and mode cannot be set", id, row.CanMount, row.Mount)
			}
			if p.RequireMounted && mountable(row) && !row.Mounted {
				// zfs create and clone exit 0 when their mount fails: mount
				// it now, right before the commit, so a failed mount leaves
				// the row uncommitted (a retry mounts again)
				plan = append(plan, Op{Kind: OpMount, Target: p.full(id), OkExists: true})
			}
			if nfs.ownsRoot() {
				// on every resume, not only the first: nothing records
				// that it ran, and it is idempotent, so a crash anywhere
				// before the commit is converged by applying it again
				plan = append(plan, Op{Kind: OpSetRoot, Target: p.full(id), Path: row.Mount, Props: nfs.root.props()})
			}
			return append(plan, commit), nil
		}
		if err := p.compatible(row, r, want); err != nil {
			return nil, err
		}
		if k, bad := propsMismatch(row, props); bad {
			return nil, errorf(AlreadyExists, "volume %s exists without %s=%s set locally", id, k, props[k])
		}
		return nil, p.nfsCompatible(row, nfs)
	}

	// validated properties first: none of them can name an ownership
	// marker (user properties are refused) or a share property
	own := make(map[string]string, len(props)+6)
	for k, v := range props {
		own[k] = v
	}
	own[p.Names.managed()], own[p.Names.volumeName()] = "true", r.Name
	if r.Kind == Filesystem {
		// sharenfs/sharesmb inherit: a parent shared to "*" would export
		// every new volume (restores included) to everyone. Exports are
		// opt-in, set locally at creation.
		own["sharenfs"], own["sharesmb"] = "off", "off"
		// F12: a client of an export can plant a setuid binary or a device
		// node; on the storage host the volume is mounted with neither
		// honoured, nor exec. Server-side mount options: NFS clients and
		// their own mounts are unaffected.
		own["setuid"], own["exec"], own["devices"] = "off", "off", "off"
		// pinned where zfs would put it now: a PV holds the path as
		// volume_context share (and mountpoint), which must not follow the
		// parent if its mountpoint later moves (csi-nfs-design §4). A
		// native property, not state of our own. A parent mounted nowhere
		// (none, legacy) leaves it inherited.
		if path.IsAbs(p.parentMount) {
			own["mountpoint"] = path.Join(p.parentMount, id)
		}
	}
	if nfs != nil && r.Kind == Filesystem && p.parentMount != "" && !path.IsAbs(p.parentMount) {
		// it would inherit the parent's none or legacy, never mount, and
		// never be served: refused before anything is made, rather than
		// created and then refused (or Unavailable on every retry)
		return nil, errorf(FailedPrecondition, "volume %s: the parent %s has mountpoint=%s, so an NFS volume under it would never be mounted or served; give the parent a mountpoint path", id, p.Parent, p.parentMount)
	}
	if nfs != nil {
		// set in the create (or clone) itself: no committed volume can
		// exist without its export or its server (csi-nfs-design §4); a
		// clone takes the request's export, never its source's
		own["sharenfs"], own[p.Names.nfsServer()] = ShareNFS(nfs.clients), nfs.server
	}
	var plan Plan
	switch {
	case r.SourceSnapshotID != "":
		// ids are opaque to CSI callers: one this endpoint could never have
		// issued names no snapshot, which CSI reports as NOT_FOUND
		vol, _, err := parseSnapshotID(r.SourceSnapshotID)
		if err != nil {
			return nil, errorf(NotFound, "source snapshot %s not found", r.SourceSnapshotID)
		}
		// only a CSI snapshot of a committed volume the endpoint owns: an
		// administrator's snapshot, an internal clone source or anything on
		// a foreign dataset is not ours to clone (ADR 0003)
		snap, ok := t.Get(r.SourceSnapshotID)
		src, srcOK := t.Get(vol)
		if !ok || !snap.OwnedSnapshot() || snap.Deferred || !srcOK || !src.ServedVolume() {
			return nil, errorf(NotFound, "source snapshot %s not found", r.SourceSnapshotID)
		}
		if src.Kind() != r.Kind {
			return nil, errorf(InvalidArgument, "source is %s, requested %s", src.Kind(), r.Kind)
		}
		if sizeErr != nil {
			return nil, sizeErr
		}
		if err := cloneBlockSize(own, src); err != nil {
			return nil, err
		}
		own[p.Names.sourceType()], own[p.Names.sourceID()] = srcType, srcID
		plan = Plan{{Kind: OpClone, Source: snap.Name, Target: p.full(id), Props: own}}
		// filesystem clones start without refquota or refreservation
		plan = append(plan, p.sizeOps(id, Row{Type: kindType(r.Kind), Volsize: snap.Volsize}, r, size)...)
	case r.SourceVolumeID != "":
		srcID, err := volumeID(r.SourceVolumeID)
		if err != nil {
			return nil, errorf(NotFound, "source volume %s not found", r.SourceVolumeID)
		}
		// only a committed volume the endpoint owns: cloning anything else
		// would snapshot, and pin, a dataset that is not ours
		src, ok := t.Get(srcID)
		if !ok || !src.ServedVolume() {
			return nil, errorf(NotFound, "source volume %s not found", srcID)
		}
		if src.Kind() != r.Kind {
			return nil, errorf(InvalidArgument, "source is %s, requested %s", src.Kind(), r.Kind)
		}
		if sizeErr != nil {
			return nil, sizeErr
		}
		if err := cloneBlockSize(own, src); err != nil {
			return nil, err
		}
		internal := src.Name + "@" + cloneSourceSnapshotPrefix + id
		if !zfs.ValidSnapshot(internal) {
			return nil, errorf(InvalidArgument, "names too long for clone source snapshot %q", internal)
		}
		// a snapshot already holding the reserved name is a replay of our
		// own cut only when it carries our stamp for this clone; anything
		// else is someone's snapshot, which a clone would pin (ADR 0003)
		if s, ok := t.Get(srcID + "@" + cloneSourceSnapshotPrefix + id); ok {
			if clone, ours := s.OwnedCloneSource(); !ours || clone != id {
				return nil, errorf(AlreadyExists, "snapshot %s exists and was not cut by the endpoint for %s", s.ID, id)
			}
		}
		own[p.Names.sourceType()], own[p.Names.sourceID()] = srcType, r.SourceVolumeID
		plan = Plan{
			// stamped atomically with its creation: ownership is never a
			// second step that a crash could skip
			{Kind: OpSnapshot, Target: internal, OkExists: true, Props: map[string]string{
				p.Names.managed(): "true", p.Names.cloneSourceFor(): id,
			}},
			{Kind: OpClone, Source: internal, Target: p.full(id), Props: own},
		}
		base := src
		// a clone starts with neither the source's refquota nor its
		// refreservation; size from what the clone really has
		base.Refquota, base.Refres = 0, 0
		plan = append(plan, p.sizeOps(id, base, r, size)...)
	case r.Kind == Block:
		// thick zvols: zfs create -V sets refreservation to zvol_auto_resv
		// (volsize plus metadata) and keeps it in step with volsize; setting
		// it by hand would under-reserve and stop the tracking
		plan = Plan{{Kind: OpCreateVol, Target: p.full(id), Size: want, Sparse: r.Sparse, Props: own}}
	default:
		if want > 0 {
			own["refquota"] = u64(want)
			if r.Reserve {
				own["refreservation"] = u64(want)
			}
		}
		plan = Plan{{Kind: OpCreateFS, Target: p.full(id), Props: own}}
	}
	if (p.RequireMounted || nfs.ownsRoot()) && r.Kind == Filesystem {
		// whether the create (or clone) mounted is known only from a scan
		// after it: the commit is the resume's, which checks Mounted, and
		// which sets the root's owner and mode on the mountpoint it read
		return plan, nil
	}
	return append(plan, commit), nil
}

// commits reports whether plan ends a provision (sets provision_success).
// A create plan that does not leaves the commit to a re-scan and re-plan.
func (p Planner) commits(plan Plan) bool {
	for _, op := range plan {
		if op.Kind == OpSet && op.Props[p.Names.success()] == "true" {
			return true
		}
	}
	return false
}

// mountable reports whether zfs mounts row by itself: a filesystem with
// canmount=on and a mountpoint path. canmount off/noauto and mountpoint
// none/legacy are an administrator's choice not to.
func mountable(row Row) bool {
	return row.Kind() == Filesystem && row.CanMount == "on" &&
		row.Mount != "" && row.Mount != "none" && row.Mount != "legacy"
}

// sourceBlockSize is the volblocksize of a zvol clone's source: its
// volume's, which every snapshot of it shares.
func sourceBlockSize(t Table, r CreateVolumeRequest) (uint64, bool) {
	if r.Kind != Block {
		return 0, false
	}
	vol := r.SourceVolumeID
	if r.SourceSnapshotID != "" {
		vol, _, _ = strings.Cut(r.SourceSnapshotID, "@")
	}
	if vol == "" {
		return 0, false
	}
	src, ok := t.Get(vol)
	if !ok || src.Type != zfs.TypeVolume || src.BlockSz == 0 {
		return 0, false
	}
	return src.BlockSz, true
}

// sourceSize is the capacity a clone of r's source starts with: a zvol
// snapshot's or volume's volsize, a filesystem source volume's refquota (0
// is unlimited), a filesystem snapshot's size when it was taken
// (snapshotSize). It reads rows as they are; ownership is checked where a
// clone is planned.
func sourceSize(t Table, r CreateVolumeRequest) (uint64, bool) {
	switch {
	case r.SourceSnapshotID != "":
		if r.Kind == Block {
			s, ok := t.Get(r.SourceSnapshotID)
			return s.Volsize, ok && s.Type == zfs.TypeSnapshot
		}
		// the snapshot's size when taken, not its source's now (an
		// expand since must not grow a restore); an absent snapshot is
		// NotFound where the clone is planned
		s, ok := t.Get(r.SourceSnapshotID)
		if !ok {
			return 0, false
		}
		return snapshotSize(t, s)
	case r.SourceVolumeID != "":
		src, ok := t.Get(r.SourceVolumeID)
		return src.Capacity(), ok && src.Type != zfs.TypeSnapshot
	}
	return 0, false
}

// cloneBlockSize enforces that a clone's requested volblocksize is its
// source's, and drops it from the clone's -o: a clone takes the origin's
// block size, and libzfs refuses volblocksize there as readonly
// (zfs_valid_proplist with a non-NULL zhp).
func cloneBlockSize(own map[string]string, src Row) error {
	bs, ok := own[volblocksizeProp]
	if !ok {
		return nil
	}
	if src.Type != zfs.TypeVolume || u64(src.BlockSz) != bs {
		return errorf(InvalidArgument, "volblocksize %s: a clone keeps its source's block size (%d)", bs, src.BlockSz)
	}
	delete(own, volblocksizeProp)
	return nil
}

// sizeOps converges an owned-but-uncommitted (or freshly cloned) volume to
// the requested size and reservation. It never shrinks the size.
//
// Thick or thin converges both ways, by the rule compatible() judges a
// committed volume with (a zvol is thick when refreservation > 0, a
// filesystem when reserve was asked): a resume that committed a row of the
// other kind as-is would succeed once and then make the identical retry
// AlreadyExists.
func (p Planner) sizeOps(id string, row Row, r CreateVolumeRequest, want uint64) Plan {
	set := map[string]string{}
	if r.Kind == Block {
		if want > row.Volsize {
			set["volsize"] = u64(want)
		}
		if !r.Sparse && row.Refres == 0 {
			set["refreservation"] = "auto"
		}
		if r.Sparse && row.Refres > 0 {
			set["refreservation"] = "none"
		}
	} else {
		if want > 0 && (row.Refquota == 0 || want > row.Refquota) {
			set["refquota"] = u64(want)
		}
		if r.Reserve && row.Refres < want {
			set["refreservation"] = u64(want)
		}
		if !r.Reserve && row.Refres > 0 {
			set["refreservation"] = "none"
		}
	}
	if len(set) == 0 {
		return nil
	}
	return Plan{{Kind: OpSet, Target: p.full(id), Props: set}}
}

// compatible applies CSI's CreateVolume idempotency rule to a committed row.
func (p Planner) compatible(row Row, r CreateVolumeRequest, want uint64) error {
	id, have := row.ID, row.Capacity()
	// an unlimited filesystem (have 0) satisfies any capacity, as
	// PlanExpandVolume holds; it does violate a limit ("at most limit")
	if want > 0 && have != 0 && have < want {
		return errorf(AlreadyExists, "volume %s exists with capacity %d < %d", id, have, want)
	}
	if r.LimitBytes > 0 && (have == 0 || have > r.LimitBytes) {
		return errorf(AlreadyExists, "volume %s exists with capacity %d > limit %d", id, have, r.LimitBytes)
	}
	if row.Kind() == Block && r.Sparse == (row.Refres > 0) {
		return errorf(AlreadyExists, "volume %s exists with sparse=%v", id, row.Refres == 0)
	}
	if row.Kind() == Filesystem && r.Reserve != (row.Refres > 0) {
		return errorf(AlreadyExists, "volume %s exists with reserve=%v", id, row.Refres > 0)
	}
	return nil
}

// PlanDeleteVolume: missing is done; unmanaged, snapshotted (like the
// baseline: CSI snapshots must outlive the call or it fails unchanged) or
// cloned volumes are refused; an internal clone-source origin is released.
// A volume with snapshots the endpoint did not cut is refused too, unless
// DestroyForeignSnapshots (ADR 0003).
//
// Never `destroy -r`: the volume's own snapshots are destroyed one by one,
// then the volume plainly, so zfs itself refuses ("has children") on
// anything the scan cannot see, such as a dataset someone created below a
// filesystem volume, rather than taking it along.
//
// An absent volume is done, after releasing any clone-source snapshot the
// endpoint cut for it that no clone uses: a CreateVolume that died between
// cutting the snapshot and cloning, then given up on, would otherwise leak
// it until Reconcile's orphan rule collects it, MinAge later. Finding those
// needs the whole parent: they hang off whichever volume was the source.
//
// A present volume releases them too, first: CreateVolume re-issued
// without the source after such a crash makes a plain volume, and a
// delete that left the snapshot to a retry (which finds the volume absent)
// would not be idempotent: DV;DV != DV.
func (p Planner) PlanDeleteVolume(t Table, volID string) (Plan, error) {
	d, err := p.planDelete(t, volID, p.DestroyForeignSnapshots)
	if err != nil {
		return nil, err
	}
	rel, err := p.releaseCloneSourcesFor(t, volID)
	if err != nil || d.vol == nil {
		return rel, err
	}
	// the volume's own origin is in use (by the volume), so never in rel;
	// d.plan releases it
	return append(rel, d.plan()...), nil
}

// releaseCloneSourcesFor plans the release (destroy -d, as a clone delete
// releases its origin) of every unused clone-source snapshot stamped for
// clone id on a volume the endpoint owns. Only the stamp makes one ours;
// the reserved name alone is anyone's (ADR 0003).
func (p Planner) releaseCloneSourcesFor(t Table, id string) (Plan, error) {
	if t.srcsFor != id {
		if err := t.mustBeWhole("finding the clone sources cut for " + id); err != nil {
			return nil, err
		}
	}
	var plan Plan
	for src := range t.Volumes() {
		if op, ok := p.releaseCloneSource(t, src, id); ok {
			plan = append(plan, op)
		}
	}
	return plan, nil
}

// releaseCloneSource is the release of the clone-source snapshot cut on
// src for clone id, when it is the endpoint's (stamped for id, on a volume
// it owns), unused and not yet released.
func (p Planner) releaseCloneSource(t Table, src Row, id string) (Op, bool) {
	s, ok := t.Get(src.ID + "@" + cloneSourceSnapshotPrefix + id)
	if !ok || !src.OwnedVolume() || s.Deferred || !zfs.ValidSnapshot(s.Name) {
		return Op{}, false
	}
	if _, used := t.dependent(s, p.Parent); used {
		return Op{}, false
	}
	if clone, ours := s.OwnedCloneSource(); !ours || clone != id {
		return Op{}, false
	}
	return Op{Kind: OpDestroy, Target: s.Name, Defer: true, OkMissing: true, GUID: s.GUID}, true
}

// PlanRollbackCreate undoes what a fresh CreateVolume of r made before it
// failed (ADR 0010), from a table read after the failure that covers the
// volume and its clone source. It touches only what passes Resume's own
// guards: a row the endpoint owns under r's name (OwnedVolume), of r's
// kind and source, not committed. Anything else is not the call's to undo
// (it changed owner, or was committed by an answer that was lost) and the
// plan is empty.
//
// A row that passes is deleted as DeleteVolume deletes it (planDelete):
// its released origin first, then the dataset, each destroy tied to the
// guid read, plain (never -r), and refused (FailedPrecondition) when
// something hangs off it that the call did not make: children, a
// snapshot, a hold, a clone. With no row and a source volume, the clone
// failed after its snapshot was cut: the plan releases that one snapshot,
// when it is stamped for this clone and unused.
func (p Planner) PlanRollbackCreate(t Table, r CreateVolumeRequest) (Plan, error) {
	id, err := volumeID(r.Name)
	if err != nil {
		return nil, err
	}
	srcType, srcID := sourceOf(r)
	cover := []string{id}
	if r.SourceVolumeID != "" {
		cover = append(cover, r.SourceVolumeID)
	}
	if err := t.mustCover(cover...); err != nil {
		return nil, err
	}
	row, ok := t.Get(id)
	if !ok {
		src, ok := t.Get(r.SourceVolumeID)
		if r.SourceVolumeID == "" || !ok {
			return nil, nil
		}
		if op, ok := p.releaseCloneSource(t, src, id); ok {
			return Plan{op}, nil
		}
		return nil, nil
	}
	if !row.OwnedVolume() || row.Committed || row.Kind() != r.Kind || row.SourceType != srcType || row.SourceID != srcID {
		return nil, nil
	}
	d, err := p.planDelete(t, id, false)
	if err != nil {
		return nil, err
	}
	return d.plan(), nil
}

// volumesOf is the volume ids plan's ops act on (a snapshot's counts as its
// volume's), in order of first appearance: what a caller must hold locks
// for. Targets outside the parent name no volume and are left out.
func (p Planner) volumesOf(plan Plan) []string {
	var vols []string
	for _, op := range plan {
		rel, ok := strings.CutPrefix(op.Target, p.Parent+"/")
		if !ok {
			continue
		}
		vol, _, _ := strings.Cut(rel, "@")
		if vol != "" && !slices.Contains(vols, vol) {
			vols = append(vols, vol)
		}
	}
	return vols
}

// deletion is a volume delete as parts: the clone-source origin it
// releases, the volume's snapshots, and the volume (plan gives the order).
type deletion struct {
	snaps  Plan
	vol    *Op
	origin *Op
	src    string // the volume the origin hangs off
}

func (d deletion) plan() Plan {
	if d.vol == nil {
		return nil
	}
	// 1. the released origin first, deferred: zfs keeps it while the clone
	//    lives and reaps it with the clone, so no crash point leaves an
	//    orphan that a retry (volume row gone) could no longer see
	// 2. exactly the snapshots this plan judged, then the dataset without
	//    -r: a snapshot or child made since the scan (sanoid, an admin)
	//    fails the last op ("has children") instead of going with it
	var plan Plan
	if d.origin != nil {
		plan = append(plan, *d.origin)
	}
	plan = append(plan, d.snaps...)
	return append(plan, *d.vol)
}

func plural(n uint64, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// planDelete is PlanDeleteVolume with the foreign-snapshot opt-in explicit:
// Reconcile never passes it (no per-volume request asked for anything).
func (p Planner) planDelete(t Table, volID string, destroyForeign bool) (deletion, error) {
	id, err := issuedVolumeID(volID)
	if err != nil {
		return deletion{}, err
	}
	if err := t.mustCover(id); err != nil {
		return deletion{}, err
	}
	row, ok := t.Get(id)
	if !ok {
		return deletion{}, nil
	}
	if !row.OwnedVolume() {
		return deletion{}, errorf(FailedPrecondition, "dataset %s is not managed", row.Name)
	}
	// zfs refuses the plain destroy below on every attempt while these
	// stand, so a plan through them would fail on every run (OWN-12)
	if len(row.Children) > 0 {
		return deletion{}, errorf(FailedPrecondition, "volume %s has datasets below it the endpoint did not make: %s", id, strings.Join(row.Children, ", "))
	}
	var d deletion
	var foreign []string
	for s := range t.SnapshotsOf(id) {
		if s.SnapshotName != "" && !s.Deferred {
			return deletion{}, errorf(FailedPrecondition, "volume %s has snapshot %s", id, s.ID)
		}
		if c, ok := t.dependent(s, p.Parent); ok {
			return deletion{}, errorf(FailedPrecondition, "volume %s has dependent clone %s", id, c)
		}
		// a user hold is someone's claim on the snapshot (zfs send, a
		// backup tool); only its holder releases it (HOLD-REPLAN)
		if s.Holds > 0 {
			return deletion{}, errorf(FailedPrecondition, "volume %s has held snapshot %s (%d user %s): zfs release it first",
				id, s.ID, s.Holds, plural(s.Holds, "hold", "holds"))
		}
		// the endpoint's own: an internal clone-source snapshot, or one
		// already pending deferred destruction (a deleted CSI snapshot)
		if _, ours := s.OwnedCloneSource(); !ours && !s.Deferred {
			foreign = append(foreign, s.ID)
		}
		// plain, never -d: a snapshot still in use must make zfs refuse
		d.snaps = append(d.snaps, Op{Kind: OpDestroy, Target: s.Name, OkMissing: true, GUID: s.GUID})
	}
	if len(foreign) > 0 && !destroyForeign {
		return deletion{}, errorf(FailedPrecondition, "volume %s has snapshots the endpoint did not make: %s (delete them, or run with -destroy-foreign-snapshots)",
			id, strings.Join(foreign, ", "))
	}
	d.vol = &Op{Kind: OpDestroy, Target: row.Name, OkMissing: true, GUID: row.GUID}
	// an origin already marked (an earlier, interrupted delete) is released:
	// zfs reaps it with the clone, and re-marking it would be a no-op op
	// that a delete zfs keeps refusing repeats on every reconcile
	if origin, src, ok := p.releasedOrigin(t, row); ok && !origin.Deferred {
		d.origin = &Op{Kind: OpDestroy, Target: origin.Name, Defer: true, OkMissing: true, GUID: origin.GUID}
		d.src = src
	}
	return d, nil
}

// releasedOrigin is the internal clone-source snapshot that deleting row
// releases, and the volume it hangs off. It is the endpoint's only when
// all of these hold:
//   - the origin is exactly <parent>/<src>@volume-source-for-volume-<row id>:
//     cut for this clone, under the parent, from an addressable volume id
//   - src is a volume the endpoint owns
//   - the snapshot is stamped for this clone or, legacy (democratic-csi
//     cuts them bare), carries no ownership marker at all: the clone's
//     origin, recorded by zfs, is then the proof it was cut for this clone.
//     A snapshot stamped for another clone, or a CSI snapshot, is not.
func (p Planner) releasedOrigin(t Table, row Row) (Row, string, bool) {
	rel, ok := strings.CutPrefix(row.Origin, p.Parent+"/")
	if !ok {
		return Row{}, "", false
	}
	src, name, ok := strings.Cut(rel, "@")
	if !ok || name != cloneSourceSnapshotPrefix+row.ID || !zfs.ValidSnapshot(row.Origin) {
		return Row{}, "", false
	}
	if _, err := volumeID(src); err != nil || !t.Covers(src) {
		return Row{}, "", false
	}
	if s, ok := t.Get(src); !ok || !s.OwnedVolume() {
		return Row{}, "", false
	}
	snap, ok := t.Get(rel)
	if !ok || snap.Type != zfs.TypeSnapshot {
		return Row{}, "", false
	}
	clone, stamped := snap.OwnedCloneSource()
	legacy := !snap.Managed && snap.SnapshotName == "" && snap.CloneSourceFor == ""
	if !(stamped && clone == row.ID) && !legacy {
		return Row{}, "", false
	}
	return snap, src, true
}

// PlanExpandVolume grows to at least capacity; shrinking is a no-op.
func (p Planner) PlanExpandVolume(t Table, volID string, capacity, limit uint64) (Plan, Row, error) {
	id, err := issuedVolumeID(volID)
	if err != nil {
		return nil, Row{}, err
	}
	if err := t.mustCover(id); err != nil {
		return nil, Row{}, err
	}
	row, ok := t.Get(id)
	if !ok || !row.ServedVolume() {
		return nil, Row{}, errorf(NotFound, "volume %s not found", id)
	}
	if capacity > MaxCapacityBytes || limit > MaxCapacityBytes {
		return nil, Row{}, errorf(OutOfRange, "capacity %d / limit %d above %d bytes", capacity, limit, uint64(MaxCapacityBytes))
	}
	want := capacity
	if row.Kind() == Block {
		want = p.roundTo(capacity, row.BlockSz)
	}
	if limit > 0 && want > limit {
		return nil, Row{}, errorf(OutOfRange, "capacity %d exceeds limit %d", want, limit)
	}
	if row.Kind() == Filesystem && row.Refquota == 0 {
		// unlimited already satisfies any capacity; a quota would shrink it
		return nil, row, nil
	}
	if row.Kind() == Filesystem && row.Refres > 0 && row.Refres < max(want, row.Refquota) {
		// thick and under-reserved: an earlier Expand's set applied
		// refquota, then refreservation failed ENOSPC (zfs applies space
		// properties one by one), and its rollback (PlanExpandRollback)
		// never ran: killed in between, or the rollback failed too.
		// Reserve the capacity, never report it done.
		set := map[string]string{"refreservation": u64(max(want, row.Refquota))}
		if want > row.Refquota {
			set["refquota"] = u64(want)
		}
		return Plan{{Kind: OpSet, Target: row.Name, Props: set}}, row, nil
	}
	if want <= row.Capacity() {
		return nil, row, nil
	}
	set := map[string]string{}
	if row.Kind() == Block {
		// refreservation follows volsize by itself when it was auto
		set["volsize"] = u64(want)
	} else {
		// refquota must land first: zfs refuses a refreservation above
		// the refquota (dsl_dataset_set_refreservation_check), so a grow
		// cannot reserve before it raises the quota. A failed reservation
		// is rolled back instead (PlanExpandRollback).
		set["refquota"] = u64(want)
		if row.Refres > 0 {
			set["refreservation"] = u64(want)
		}
	}
	return Plan{{Kind: OpSet, Target: row.Name, Props: set}}, row, nil
}

// PlanExpandRollback undoes the half of a failed thick-filesystem grow that
// landed. zfs applies refquota, then refreservation; when the reservation
// fails (ENOSPC: the pool cannot back the new size) the raised refquota
// stays, and the volume would report a capacity it cannot reserve, which
// every later Expand must then reserve (ResourceExhausted until the pool
// grows). before is the row the failed plan was made from, t a scan after
// the failure. The plan puts refquota back where it was, and only when
// this grow raised it and the reservation stayed behind; otherwise nil.
func (p Planner) PlanExpandRollback(t Table, before Row) Plan {
	row, ok := t.Get(before.ID)
	if !ok || row.Name != before.Name || row.Kind() != Filesystem || before.Kind() != Filesystem {
		return nil
	}
	if row.Refres == 0 || row.Refres >= row.Refquota || row.Refquota <= before.Refquota {
		return nil
	}
	return Plan{{Kind: OpSet, Target: row.Name, Props: map[string]string{"refquota": u64(max(before.Refquota, row.Refres))}}}
}

// PlanCreateSnapshot: CSI snapshot names are global; the same name on the
// same volume is done, on another volume AlreadyExists.
func (p Planner) PlanCreateSnapshot(t Table, name, volID string) (Plan, string, error) {
	vol, err := issuedVolumeID(volID)
	if err != nil {
		return nil, "", err
	}
	if name == "" || len(name) > 128 || strings.ContainsFunc(name, unicode.IsControl) {
		return nil, "", errorf(InvalidArgument, "invalid snapshot name %q", name)
	}
	if strings.HasPrefix(name, cloneSourceSnapshotPrefix) {
		return nil, "", errorf(InvalidArgument, "snapshot name prefix %q is reserved", cloneSourceSnapshotPrefix)
	}
	id := vol + "@" + snapComponent(vol, name)
	if len(id) > 128 {
		// ids are "<volume>@<component>"; a volume id over 91 bytes cannot
		// carry even a hashed component within CSI's limit
		return nil, "", errorf(InvalidArgument, "volume id %q too long to address snapshots within 128 bytes", vol)
	}
	// snapshot names are global: only the whole parent says one is free
	if err := t.mustBeWhole("CreateSnapshot"); err != nil {
		return nil, "", err
	}
	for r := range t.All() {
		if r.OwnedSnapshot() && r.SnapshotName == name && !r.Deferred {
			if r.ID == id {
				return nil, id, nil
			}
			return nil, "", errorf(AlreadyExists, "snapshot %q exists for volume %s", name, r.SnapshotSource)
		}
	}
	src, ok := t.Get(vol)
	if !ok || !src.ServedVolume() {
		return nil, "", errorf(NotFound, "source volume %s not found", vol)
	}
	if r, ok := t.Get(id); ok {
		// a snapshot of that name we do not own, or one pending deletion
		return nil, "", errorf(AlreadyExists, "snapshot %s exists (managed=%v deferred=%v)", id, r.Managed, r.Deferred)
	}
	props := map[string]string{
		p.Names.managed():        "true",
		p.Names.snapshotName():   name,
		p.Names.snapshotSource(): vol,
	}
	if src.Type != zfs.TypeVolume {
		// refquota is not a snapshot property and the source's grows with
		// an expand: record the size now (a zvol snapshot keeps volsize)
		props[p.Names.snapshotSize()] = u64(src.Refquota)
	}
	return Plan{{Kind: OpSnapshot, Target: p.full(id), Props: props}}, id, nil
}

// snapComponent is the zfs snapshot name for CSI snapshot name: the name
// itself when it is a valid zfs component and "<vol>@<name>" fits CSI's
// 128-byte id limit, else a stable hash of it. The CSI name always lives in
// the csi_snapshot_name property, which is what lookups match on.
func snapComponent(vol, name string) string {
	if zfs.ValidSnapshot("x@"+name) && len(vol)+1+len(name) <= 128 {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	return "csi-" + hex.EncodeToString(sum[:16])
}

// PlanDeleteSnapshot: missing or never-issued ids are done; snapshots with
// clones are destroyed deferred (lzc_destroy_snaps defer) instead of leaking.
func (p Planner) PlanDeleteSnapshot(t Table, snapID string) (Plan, error) {
	if _, _, err := parseSnapshotID(snapID); err != nil {
		return nil, nil
	}
	if err := t.mustCover(snapID); err != nil {
		return nil, err
	}
	row, ok := t.Get(snapID)
	if !ok || row.Deferred {
		return nil, nil
	}
	if !row.OwnedSnapshot() {
		return nil, errorf(FailedPrecondition, "snapshot %s is not managed", snapID)
	}
	return Plan{{Kind: OpDestroy, Target: row.Name, Defer: true, OkMissing: true, GUID: row.GUID}}, nil
}

func sourceOf(r CreateVolumeRequest) (string, string) {
	switch {
	case r.SourceSnapshotID != "":
		return "snapshot", r.SourceSnapshotID
	case r.SourceVolumeID != "":
		return "volume", r.SourceVolumeID
	}
	return "", ""
}

func kindType(k VolumeKind) zfs.Type {
	if k == Block {
		return zfs.TypeVolume
	}
	return zfs.TypeFilesystem
}
