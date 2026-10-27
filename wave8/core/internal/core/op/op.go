// Package op is the core's vocabulary of mutations: one struct per zfs
// command shape, built only by constructors that take world handles. Each
// op renders its own argv, so the executor is a dumb loop and cannot
// combine fields wrongly. Every op knows its replay tolerance.
package op

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/wommy/empty/internal/core/world"
)

// Kind of an op. The zero value is named, so `exhaustive` sees it.
type Kind uint8

const (
	KInvalid Kind = iota
	KCreateFS
	KCreateZvol
	KCutCloneSource
	KClone
	KSet
	KCommit
	KReleaseOrigin
	KDestroySnap
	KDestroyVol
	KDeferDestroySnap
	NKinds
)

var kindNames = [NKinds]string{"invalid", "create-fs", "create-zvol", "cut-clone-source", "clone", "set", "commit", "release-origin", "destroy-snap", "destroy-vol", "defer-destroy-snap"}

func (k Kind) String() string {
	if k < NKinds {
		return kindNames[k]
	}
	return "Kind(" + strconv.Itoa(int(k)) + ")"
}

// Tol is how a replay of this op treats finding its work done.
type Tol uint8

const (
	// TolNone: any failure is a failure. Creates never adopt: "already
	// exists" is not "done" for a dataset someone else made (F4, CX3).
	// A replay of our own create re-scans and takes the resume branch.
	TolNone Tol = iota
	// TolMissing: "does not exist" means done (destroys).
	TolMissing
)

// Op is one zfs command. Sealed: only this package implements it.
type Op interface {
	Kind() Kind
	Tol() Tol
	Target() string
	Argv() []string
	String() string
	sealed()
}

// Plan is the ordered ops of one decision.
type Plan []Op

func (p Plan) String() string {
	var b strings.Builder
	for i, o := range p {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(o.String())
	}
	return b.String()
}

// Kinds lists the plan's op kinds (for table conformance).
func (p Plan) Kinds() []Kind {
	k := make([]Kind, len(p))
	for i, o := range p {
		k[i] = o.Kind()
	}
	return k
}

// ---- properties -------------------------------------------------------------

// KV is one property assignment.
type KV struct{ K, V string }

// Props is a sorted, duplicate-free list of valid assignments.
type Props struct{ kv []KV }

var errProp = errors.New("invalid property")

// NewProps validates names (zfs property charset) and values (no newline,
// tab or NUL: zfs get -Hp prints them raw) and sorts.
func NewProps(m map[string]string) (Props, error) {
	kv := make([]KV, 0, len(m))
	for k, v := range m {
		if k == "" || strings.IndexFunc(k, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("-_.:", r))
		}) >= 0 || strings.ContainsAny(v, "\n\t\x00") || len(v) > 8191 {
			return Props{}, fmt.Errorf("%w %q", errProp, k)
		}
		kv = append(kv, KV{k, v})
	}
	slices.SortFunc(kv, func(a, b KV) int { return strings.Compare(a.K, b.K) })
	return Props{kv}, nil
}

func (p Props) with(m ...KV) Props {
	kv := slices.Clone(p.kv)
	for _, a := range m {
		i, found := slices.BinarySearchFunc(kv, a.K, func(x KV, k string) int { return strings.Compare(x.K, k) })
		if found {
			kv[i] = a
		} else {
			kv = slices.Insert(kv, i, a)
		}
	}
	return Props{kv}
}

// Len is the number of assignments.
func (p Props) Len() int { return len(p.kv) }

// All returns the assignments in order.
func (p Props) All() []KV { return p.kv }

func (p Props) opts() []string {
	a := make([]string, 0, 2*len(p.kv))
	for _, kv := range p.kv {
		a = append(a, "-o", kv.K+"="+kv.V)
	}
	return a
}

func (p Props) String() string {
	s := make([]string, len(p.kv))
	for i, kv := range p.kv {
		s[i] = kv.K + "=" + kv.V
	}
	return strings.Join(s, ",")
}

// ownership markers every create stamps atomically with the dataset: a
// dataset of ours without them cannot be produced by this package
func owned(t world.FreeVol, p Props, src world.SrcKind, srcID string) Props {
	w := t.World()
	m := []KV{{w.Marker(":managed_resource"), "true"}, {w.Marker(":csi_volume_name"), t.ID().String()}}
	if src != world.SrcNone {
		m = append(m, KV{w.Marker(":csi_volume_content_source_type"), src.String()}, KV{w.Marker(":csi_volume_content_source_id"), srcID})
	}
	return p.with(m...)
}

// ---- ops --------------------------------------------------------------------

// CreateFS is `zfs create -o ... <fs>`: never -p (it exits 0 on an existing
// dataset), always sharenfs=off sharesmb=off (a shared parent must not export
// new volumes), ownership stamped in the same command.
type CreateFS struct {
	target string
	props  Props
}

// NewCreateFS: quota 0 is unlimited; reserve needs a quota (the request
// parser makes reserve without a size unrepresentable).
func NewCreateFS(t world.FreeVol, quota uint64, reserve bool, p Props) CreateFS {
	add := []KV{{"sharenfs", "off"}, {"sharesmb", "off"}}
	if quota > 0 {
		add = append(add, KV{"refquota", u64(quota)})
		if reserve {
			add = append(add, KV{"refreservation", u64(quota)})
		}
	}
	return CreateFS{must(t.Dataset().String()), owned(t, p.with(add...), world.SrcNone, "")}
}

// Volsize is a zvol size: > 0 and a multiple of its block size.
type Volsize struct{ n uint64 }

// NewVolsize checks zvol.c's rules.
func NewVolsize(n uint64, bs uint32) (Volsize, bool) {
	return Volsize{n}, n > 0 && bs > 0 && n%uint64(bs) == 0
}

func (v Volsize) Bytes() uint64 { return v.n }

// CreateZvol is `zfs create [-s] -V <size> -o ... <vol>`. Thick zvols get
// zfs's own refreservation (zvol_auto_resv); it is never hand-set.
type CreateZvol struct {
	target string
	size   Volsize
	sparse bool
	props  Props
}

func NewCreateZvol(t world.FreeVol, size Volsize, sparse bool, p Props) CreateZvol {
	return CreateZvol{must(t.Dataset().String()), size, sparse, owned(t, p, world.SrcNone, "")}
}

// CutCloneSource is `zfs snapshot -o <p>:managed_resource=true -o
// <p>:clone_source_for=<id> <src>@volume-source-for-volume-<id>`: the stamp
// is set by the command that creates the snapshot, so ownership is never a
// second step a crash could skip, and the name alone is never ownership
// (OWN-13). TolNone: zfs's "exists" means the scan was stale or someone
// took the name; Map turns it into Aborted (rescan), never a final
// AlreadyExists, and a deferred snapshot of that name is never minted as
// a CloneSource (A3).
type CutCloneSource struct {
	target string
	props  Props
}

// NewCutCloneSource refuses a clone source that already exists: the plan
// reuses it instead.
func NewCutCloneSource(c world.CloneSource) (CutCloneSource, bool) {
	w := c.World()
	if w == nil {
		panic("op: zero-value world.CloneSource")
	}
	p := Props{kv: []KV{{w.Marker(":clone_source_for"), c.For().String()}, {w.Marker(":managed_resource"), "true"}}}
	slices.SortFunc(p.kv, func(a, b KV) int { return strings.Compare(a.K, b.K) })
	return CutCloneSource{must(c.Snapshot().String()), p}, !c.Exists()
}

// Clone is `zfs clone -o ... <origin> <target>` with ownership and the
// content-source markers taken from the origin handle.
type Clone struct {
	origin, target string
	props          Props
}

func NewClone(o world.Origin, t world.FreeVol, p Props) Clone {
	if o.Kind() == world.FS {
		p = p.with(KV{"sharenfs", "off"}, KV{"sharesmb", "off"})
	}
	k, id := o.Source()
	return Clone{must(o.Snapshot().String()), must(t.Dataset().String()), owned(t, p, k, id)}
}

// Set is `zfs set k=v ... <target>` on a dataset being provisioned.
type Set struct {
	target string
	props  Props
}

// NewSet refuses an empty assignment list.
func NewSet(t world.Target, p Props) (Set, bool) {
	return Set{must(t.Dataset().String()), p}, p.Len() > 0
}

// Commit is `zfs set <prefix>:provision_success=true`: the last op of every
// provision; before it the volume is Uncommitted and the CO has nothing.
type Commit struct{ target, key string }

func NewCommit(w *world.World, t world.Target) Commit {
	return Commit{must(t.Dataset().String()), w.Marker(":provision_success")}
}

// ReleaseOrigin is `zfs destroy -d` of a volume's clone source: zfs keeps
// it while the clone lives and reaps it with the clone (lifecycle-fsm R1).
type ReleaseOrigin struct{ target string }

func NewReleaseOrigin(o world.ReleasableOrigin) ReleaseOrigin {
	return ReleaseOrigin{must(o.Snapshot().String())}
}

// DestroySnap is `zfs destroy <snap>` of a judged snapshot.
//
// Only DeleteVolume builds one (TestDestroySnapOnlyInDeleteVolume): it is
// right there because a racing clone must make it fail, not hide it.
// DeleteSnapshot and Reconcile use DeferDestroySnap (SNP-06).
type DestroySnap struct {
	target string
	guid   uint64
}

func NewDestroySnap(d world.DoomedSnap) DestroySnap {
	return DestroySnap{must(d.Snapshot().String()), d.Guid()}
}

// DeferDestroySnap is `zfs destroy -d <snap>`: gone now if nothing clones
// it, else hidden (defer_destroy=on) until its last clone is destroyed.
type DeferDestroySnap struct{ target string }

func NewDeferDestroySnap(d world.DeferrableSnap) DeferDestroySnap {
	return DeferDestroySnap{must(d.Snapshot().String())}
}

// DestroyVol is `zfs destroy <vol>`, never -r: anything the scan did not
// judge (a racing sanoid snapshot, a child) makes zfs refuse instead of
// taking it along (CX2, F1).
//
// guid is the scanned dataset's: Apply checks it immediately before the
// destroy, so an admin's rename-and-recreate between scan and destroy
// makes the RPC Aborted instead of destroying the new dataset (OWN-15).
// A window of one zfs exec remains; `zfs program` closes it (core.md).
type DestroyVol struct {
	target string
	guid   uint64
}

func NewDestroyVol(o world.OwnedVol) DestroyVol {
	return DestroyVol{must(o.Dataset().String()), o.Guid()}
}

// Guarded ops name the dataset and the guid it must still have.
type Guarded interface {
	Op
	Guard() (target string, guid uint64)
}

func (o DestroySnap) Guard() (string, uint64) { return o.target, o.guid }
func (o DestroyVol) Guard() (string, uint64)  { return o.target, o.guid }

func (CreateFS) Kind() Kind         { return KCreateFS }
func (CreateZvol) Kind() Kind       { return KCreateZvol }
func (CutCloneSource) Kind() Kind   { return KCutCloneSource }
func (Clone) Kind() Kind            { return KClone }
func (Set) Kind() Kind              { return KSet }
func (Commit) Kind() Kind           { return KCommit }
func (ReleaseOrigin) Kind() Kind    { return KReleaseOrigin }
func (DestroySnap) Kind() Kind      { return KDestroySnap }
func (DestroyVol) Kind() Kind       { return KDestroyVol }
func (DeferDestroySnap) Kind() Kind { return KDeferDestroySnap }

func (CreateFS) Tol() Tol         { return TolNone }
func (CreateZvol) Tol() Tol       { return TolNone }
func (CutCloneSource) Tol() Tol   { return TolNone }
func (Clone) Tol() Tol            { return TolNone }
func (Set) Tol() Tol              { return TolNone }
func (Commit) Tol() Tol           { return TolNone }
func (ReleaseOrigin) Tol() Tol    { return TolMissing }
func (DestroySnap) Tol() Tol      { return TolMissing }
func (DestroyVol) Tol() Tol       { return TolMissing }
func (DeferDestroySnap) Tol() Tol { return TolMissing }

func (o CreateFS) Target() string         { return o.target }
func (o CreateZvol) Target() string       { return o.target }
func (o CutCloneSource) Target() string   { return o.target }
func (o Clone) Target() string            { return o.target }
func (o Set) Target() string              { return o.target }
func (o Commit) Target() string           { return o.target }
func (o ReleaseOrigin) Target() string    { return o.target }
func (o DestroySnap) Target() string      { return o.target }
func (o DestroyVol) Target() string       { return o.target }
func (o DeferDestroySnap) Target() string { return o.target }

func (o CreateFS) Argv() []string {
	return append(append([]string{"create"}, o.props.opts()...), o.target)
}
func (o CreateZvol) Argv() []string {
	a := []string{"create"}
	if o.sparse {
		a = append(a, "-s")
	}
	a = append(a, "-V", u64(o.size.n))
	return append(append(a, o.props.opts()...), o.target)
}
func (o CutCloneSource) Argv() []string {
	return append(append([]string{"snapshot"}, o.props.opts()...), o.target)
}
func (o Clone) Argv() []string {
	return append(append([]string{"clone"}, o.props.opts()...), o.origin, o.target)
}
func (o Set) Argv() []string {
	a := []string{"set"}
	for _, kv := range o.props.kv {
		a = append(a, kv.K+"="+kv.V)
	}
	return append(a, o.target)
}
func (o Commit) Argv() []string           { return []string{"set", o.key + "=true", o.target} }
func (o ReleaseOrigin) Argv() []string    { return []string{"destroy", "-d", o.target} }
func (o DestroySnap) Argv() []string      { return []string{"destroy", o.target} }
func (o DestroyVol) Argv() []string       { return []string{"destroy", o.target} }
func (o DeferDestroySnap) Argv() []string { return []string{"destroy", "-d", o.target} }

func (o CreateFS) String() string { return fmt.Sprintf("create-fs %s [%s]", o.target, o.props) }
func (o CreateZvol) String() string {
	return fmt.Sprintf("create-zvol %s size=%d sparse=%v [%s]", o.target, o.size.n, o.sparse, o.props)
}
func (o CutCloneSource) String() string {
	return fmt.Sprintf("cut-clone-source %s [%s]", o.target, o.props)
}
func (o Clone) String() string {
	return fmt.Sprintf("clone %s -> %s [%s]", o.origin, o.target, o.props)
}
func (o Set) String() string              { return fmt.Sprintf("set %s [%s]", o.target, o.props) }
func (o Commit) String() string           { return "commit " + o.target }
func (o ReleaseOrigin) String() string    { return "release-origin " + o.target }
func (o DestroySnap) String() string      { return "destroy-snap " + o.target }
func (o DestroyVol) String() string       { return "destroy-vol " + o.target }
func (o DeferDestroySnap) String() string { return "defer-destroy-snap " + o.target }

func (CreateFS) sealed()         {}
func (CreateZvol) sealed()       {}
func (CutCloneSource) sealed()   {}
func (Clone) sealed()            {}
func (Set) sealed()              {}
func (Commit) sealed()           {}
func (ReleaseOrigin) sealed()    {}
func (DestroySnap) sealed()      {}
func (DestroyVol) sealed()       {}
func (DeferDestroySnap) sealed() {}

func u64(n uint64) string { return strconv.FormatUint(n, 10) }

// zero-value handles are the one way around the handle discipline Go
// cannot forbid (world.CloneSource{} compiles anywhere). Constructors fail
// closed on them, before any op exists; TestNoHandleLiterals bans writing
// them outside package world.
func must(s string) string {
	if s == "" || strings.HasSuffix(s, "@") || strings.HasPrefix(s, "@") {
		panic("op: zero-value world handle")
	}
	return s
}

// Size props for a provision set (filesystem quota/reservation, zvol size
// and auto reservation). Exposed so decide never spells property names.
func FSSize(quota uint64, reserve bool) []KV {
	var kv []KV
	if quota > 0 {
		kv = append(kv, KV{"refquota", u64(quota)})
		if reserve {
			kv = append(kv, KV{"refreservation", u64(quota)})
		}
	}
	return kv
}

// With returns p with kv assigned.
func (p Props) With(kv ...KV) Props { return p.with(kv...) }
