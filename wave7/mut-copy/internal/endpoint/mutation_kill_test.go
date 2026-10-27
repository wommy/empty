package endpoint

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wommy/empty/internal/zfs"
)

// Tests written from the mutation run (wave7/mutation.md). Each names the
// invariant a surviving mutant showed to be unasserted.

// ---- ownership -------------------------------------------------------------

// M1: cloning from a CSI snapshot needs the source VOLUME to be owned too.
func TestMutCloneFromSnapshotNeedsOwnedSourceVolume(t *testing.T) {
	req := CreateVolumeRequest{Name: "dst", CapacityBytes: 1, SourceSnapshotID: "src@s"}
	if _, err := pl.PlanCreateVolume(NewTable([]Row{vol("src", nil), snap("src@s", nil)}), req); err != nil {
		t.Fatalf("control: owned source must clone: %v", err)
	}
	for name, mut := range map[string]func(*Row){
		"unmanaged":     func(r *Row) { r.Managed = false },
		"renamed copy":  func(r *Row) { r.VolumeName = "someone-else" },
		"uncommitted":   func(r *Row) { r.Committed = false },
		"actually snap": func(r *Row) { r.Type = zfs.TypeSnapshot },
	} {
		tab := NewTable([]Row{vol("src", mut), snap("src@s", nil)})
		if _, err := pl.PlanCreateVolume(tab, req); code(err) != NotFound {
			t.Errorf("%s: got %v, want NotFound", name, err)
		}
	}
}

// M2: a dataset carrying another volume's name marker is not this volume.
func TestMutOwnedVolumeNameMarker(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    Row
		want bool
	}{
		{"match", Row{ID: "a", Managed: true, VolumeName: "a"}, true},
		{"no marker", Row{ID: "a", Managed: true}, true},
		{"foreign marker (zfs send|recv copy, rename)", Row{ID: "a", Managed: true, VolumeName: "b"}, false},
		{"unmanaged", Row{ID: "a", VolumeName: "a"}, false},
		{"snapshot", Row{ID: "a@s", Type: zfs.TypeSnapshot, Managed: true}, false},
	} {
		if got := tc.r.OwnedVolume(); got != tc.want {
			t.Errorf("%s: OwnedVolume = %v", tc.name, got)
		}
	}
	tab := NewTable([]Row{vol("a", func(r *Row) { r.VolumeName = "b" })})
	if _, err := pl.PlanDeleteVolume(tab, "a"); code(err) != FailedPrecondition {
		t.Errorf("delete of a look-alike must be refused: %v", err)
	}
	if _, _, err := pl.PlanExpandVolume(tab, "a", 10, 0); code(err) != NotFound {
		t.Errorf("expand of a look-alike: %v", err)
	}
}

// M3: CreateVolume must not answer "already done" for another volume's dataset.
func TestMutCreateRefusesDatasetOfAnotherVolumeName(t *testing.T) {
	tab := NewTable([]Row{vol("a", func(r *Row) { r.VolumeName = "b" })})
	if _, err := pl.PlanCreateVolume(tab, CreateVolumeRequest{Name: "a"}); code(err) != AlreadyExists {
		t.Errorf("got %v, want AlreadyExists", err)
	}
}

// M4: only a managed snapshot with a CSI name, hanging off its marked source.
func TestMutOwnedSnapshot(t *testing.T) {
	ok := Row{ID: "a@s", Type: zfs.TypeSnapshot, Managed: true, SnapshotName: "s", SnapshotSource: "a"}
	if !ok.OwnedSnapshot() {
		t.Fatal("control")
	}
	for name, mut := range map[string]func(*Row){
		"filesystem with snapshot markers": func(r *Row) { r.Type = zfs.TypeFilesystem },
		"zvol with snapshot markers":       func(r *Row) { r.Type = zfs.TypeVolume },
		"unmanaged":                        func(r *Row) { r.Managed = false },
		"no CSI name (internal/admin)":     func(r *Row) { r.SnapshotName = "" },
		"source marker is another volume":  func(r *Row) { r.SnapshotSource = "b" },
	} {
		r := ok
		mut(&r)
		if r.OwnedSnapshot() {
			t.Errorf("%s must not be an owned snapshot", name)
		}
	}
}

// M5: absence decision. A snapshot already pending deletion is "done" even
// when it is not ours; only a live foreign snapshot is refused.
func TestMutDeleteSnapshotDeferredForeignIsDone(t *testing.T) {
	deferred := NewTable([]Row{vol("a", nil), snap("a@s", func(r *Row) { r.Managed, r.SnapshotName, r.Deferred = false, "", true })})
	if plan, err := pl.PlanDeleteSnapshot(deferred, "a@s"); err != nil || plan != nil {
		t.Errorf("deferred foreign: plan %v err %v", plan, err)
	}
	live := NewTable([]Row{vol("a", nil), snap("a@s", func(r *Row) { r.Managed, r.SnapshotName = false, "" })})
	if _, err := pl.PlanDeleteSnapshot(live, "a@s"); code(err) != FailedPrecondition {
		t.Errorf("live foreign: %v", err)
	}
	owned := NewTable([]Row{vol("a", nil), snap("a@s", nil)})
	if plan, err := pl.PlanDeleteSnapshot(owned, "a@s"); err != nil || len(plan) != 1 || !plan[0].Defer {
		t.Errorf("owned: plan %v err %v", plan, err)
	}
}

// ---- GC predicate ----------------------------------------------------------

// M6: "nothing created less than MinAge before Now is touched": exactly
// MinAge old is collectable, one second younger is not.
func TestMutReconcileMinAgeBoundary(t *testing.T) {
	d := desired("keep")
	for _, tc := range []struct {
		age  int64
		want string
	}{{3600, "x:" + ReasonUndesired}, {3599, "x:" + SkipTooYoung}} {
		tab := table(vol("keep", nil), vol("x", func(r *Row) { r.Creation = recNow - tc.age }))
		plan, err := pl.PlanReconcile(tab, d)
		if err != nil {
			t.Fatal(err)
		}
		acts, skips := summary(plan)
		got := strings.Join(append(acts, skips...), ",")
		if got != tc.want {
			t.Errorf("age %ds: got %q, want %q", tc.age, got, tc.want)
		}
	}
	_ = time.Hour
}

// M7: the fixpoint simulation must not forget other volumes' pinned origins.
func TestMutSimulateDestroyReapsOnlyTheNamedOrigin(t *testing.T) {
	rows := []Row{
		{Name: "tank/v/b", ID: "b"},
		{Name: "tank/v/b@s1", ID: "b@s1", Type: zfs.TypeSnapshot, Deferred: true},
		{Name: "tank/v/b@s2", ID: "b@s2", Type: zfs.TypeSnapshot, Deferred: true},
		{Name: "tank/v/c", ID: "c", Origin: "tank/v/b@s1"},
		{Name: "tank/v/e", ID: "e", Origin: "tank/v/b@s2"},
	}
	rows = simulateDestroy(rows, Plan{{Kind: OpDestroy, Target: "tank/v/c", Recursive: true}})
	var names []string
	for _, r := range rows {
		names = append(names, r.ID)
	}
	if got := strings.Join(names, ","); got != "b,b@s2,e" {
		t.Errorf("rows %s, want b,b@s2,e (s1 reaped, s2 still pinned by e)", got)
	}
	// a non-recursive destroy takes the dataset alone
	rows = simulateDestroy([]Row{{Name: "tank/v/b", ID: "b"}, {Name: "tank/v/b@s", ID: "b@s", Type: zfs.TypeSnapshot}},
		Plan{{Kind: OpDestroy, Target: "tank/v/b"}})
	if len(rows) != 1 || rows[0].ID != "b@s" {
		t.Errorf("non-recursive destroy: %+v", rows)
	}
	// a deferred destroy marks the target even when it is the first row
	rows = simulateDestroy([]Row{{Name: "tank/v/a@s", ID: "a@s", Type: zfs.TypeSnapshot}, {Name: "tank/v/c", ID: "c", Origin: "tank/v/a@s"}},
		Plan{{Kind: OpDestroy, Target: "tank/v/a@s", Defer: true}})
	if !rows[0].Deferred {
		t.Errorf("first row not marked deferred: %+v", rows)
	}
}

// ---- error codes -----------------------------------------------------------

// M8: the exact CSI code of each refusal, not just "an error".
func TestMutPlannerErrorCodes(t *testing.T) {
	long := strings.Repeat("s", 120)
	tab := NewTable([]Row{
		vol("a", func(r *Row) { r.Refquota = 100 }),
		vol("unl", nil),
		vol(long, nil),
	})
	for name, tc := range map[string]struct {
		err  error
		want Code
	}{
		"two content sources": {func() error {
			_, err := pl.PlanCreateVolume(tab, CreateVolumeRequest{Name: "n", CapacityBytes: 1, SourceSnapshotID: "a@s", SourceVolumeID: "a"})
			return err
		}(), InvalidArgument},
		"block without capacity": {func() error {
			_, err := pl.PlanCreateVolume(tab, CreateVolumeRequest{Name: "n", Kind: Block})
			return err
		}(), InvalidArgument},
		"sparse filesystem": {func() error {
			_, err := pl.PlanCreateVolume(tab, CreateVolumeRequest{Name: "n", CapacityBytes: 1, Sparse: true})
			return err
		}(), InvalidArgument},
		"clone source snapshot name too long": {func() error {
			_, err := pl.PlanCreateVolume(tab, CreateVolumeRequest{Name: strings.Repeat("d", 128), CapacityBytes: 1, SourceVolumeID: long})
			return err
		}(), InvalidArgument},
		"existing volume larger than limit": {func() error {
			_, err := pl.PlanCreateVolume(tab, CreateVolumeRequest{Name: "a", CapacityBytes: 50, LimitBytes: 60})
			return err
		}(), AlreadyExists},
		"existing unlimited volume under a limit": {func() error {
			_, err := pl.PlanCreateVolume(tab, CreateVolumeRequest{Name: "unl", LimitBytes: 100})
			return err
		}(), AlreadyExists},
		"expand beyond limit": {func() error { _, _, err := pl.PlanExpandVolume(tab, "a", 200, 100); return err }(), OutOfRange},
		"empty snapshot name": {func() error { _, _, err := pl.PlanCreateSnapshot(tab, "", "a"); return err }(), InvalidArgument},
	} {
		if got := code(tc.err); got != tc.want {
			t.Errorf("%s: got %v (%v), want code %d", name, got, tc.err, tc.want)
		}
	}
}

// M8b: a clone whose source is exactly the limit is fine.
func TestMutCloneSourceEqualToLimitIsAllowed(t *testing.T) {
	tab := NewTable([]Row{
		vol("src", func(r *Row) { r.Type, r.Volsize = zfs.TypeVolume, 1<<20 }),
		snap("src@s", func(r *Row) { r.Volsize = 1 << 20 }),
	})
	if _, err := pl.PlanCreateVolume(tab, CreateVolumeRequest{Name: "d", Kind: Block, CapacityBytes: 1 << 20, LimitBytes: 1 << 20, SourceSnapshotID: "src@s"}); err != nil {
		t.Fatal(err)
	}
}

// M9: zfs failure kind to endpoint code, one row per kind.
func TestMutZfsErrCodes(t *testing.T) {
	for _, tc := range []struct {
		k    zfs.Kind
		want Code
	}{
		{zfs.KindNotFound, NotFound}, {zfs.KindParentNotFound, NotFound}, {zfs.KindExists, AlreadyExists},
		{zfs.KindHasClones, FailedPrecondition}, {zfs.KindHasChildren, FailedPrecondition},
		{zfs.KindPrecondition, FailedPrecondition}, {zfs.KindPermission, FailedPrecondition},
		{zfs.KindBusy, Unavailable}, {zfs.KindUnavailable, Unavailable}, {zfs.KindNoSpace, ResourceExhausted},
		{zfs.KindInvalidProperty, InvalidArgument}, {zfs.KindUnknown, Internal},
	} {
		if got := CodeOf(zfsErr(&zfs.Error{Kind: tc.k, Op: "x", Dataset: "d"}, "what")); got != tc.want {
			t.Errorf("kind %v: code %d, want %d", tc.k, got, tc.want)
		}
	}
	if zfsErr(nil, "x") != nil {
		t.Error("nil in, nil out")
	}
	e := &Error{Code: NotFound, Msg: "m", Err: errors.New("cause")}
	if e.Error() != "m: cause" {
		t.Errorf("Error() = %q", e.Error())
	}
}

// ---- sizing ----------------------------------------------------------------

// M10: a converging create grows a smaller quota and never shrinks a
// larger reservation.
func TestMutSizeOpsGrowQuotaNeverShrinkReservation(t *testing.T) {
	tab := NewTable([]Row{vol("g", func(r *Row) { r.Committed, r.Refquota = false, 50 })})
	plan, err := pl.PlanCreateVolume(tab, CreateVolumeRequest{Name: "g", CapacityBytes: 100})
	if err != nil || len(plan) != 2 || plan[0].Props["refquota"] != "100" {
		t.Errorf("grow: %+v %v", plan, err)
	}
	tab = NewTable([]Row{vol("r", func(r *Row) { r.Committed, r.Refquota, r.Refres = false, 100, 200 })})
	plan, err = pl.PlanCreateVolume(tab, CreateVolumeRequest{Name: "r", CapacityBytes: 100, Reserve: true})
	if err != nil || len(plan) != 1 {
		t.Errorf("shrink reservation: %+v %v", plan, err)
	}
}

// ---- table derivation and props -------------------------------------------

func TestMutRowKindAndCapacity(t *testing.T) {
	for _, tc := range []struct {
		r    Row
		kind VolumeKind
		cap  uint64
	}{
		{Row{Type: zfs.TypeVolume, Volsize: 5}, Block, 5},
		{Row{Type: zfs.TypeSnapshot, Volsize: 5}, Block, 5},
		{Row{Type: zfs.TypeSnapshot, Refquota: 7}, Filesystem, 7},
		{Row{Type: zfs.TypeFilesystem, Refquota: 7}, Filesystem, 7},
	} {
		if tc.r.Kind() != tc.kind || tc.r.Capacity() != tc.cap {
			t.Errorf("%+v: kind %v cap %d", tc.r, tc.r.Kind(), tc.r.Capacity())
		}
	}
	tab := NewTable([]Row{vol("a", nil), snap("a@s", nil)})
	n := 0
	for r := range tab.Volumes() {
		n++
		if r.Type == zfs.TypeSnapshot {
			t.Errorf("Volumes yielded snapshot %s", r.ID)
		}
	}
	if n != 1 {
		t.Errorf("volumes %d", n)
	}
}

func TestMutPropertyAllowlistEdges(t *testing.T) {
	for _, in := range []map[string]string{{"atime": "on"}, {"relatime": "off"}, {"compression": "gzip-9"}, {"recordsize": "512B"}} {
		if _, err := ValidateProperties(Filesystem, in); err != nil {
			t.Errorf("%v: %v", in, err)
		}
	}
	for _, in := range []map[string]string{{"atime": "on"}, {"recordsize": "128K"}} {
		if _, err := ValidateProperties(Block, in); err == nil {
			t.Errorf("zvol must refuse %v", in)
		}
	}
	if v, ok := parseSize("999999999"); !ok || v != 999999999 {
		t.Errorf("9 digits: %d %v", v, ok)
	}
	if _, ok := parseSize("1000000000"); ok {
		t.Error("10 digits must be refused")
	}
}

// A clone has its source's kind: a filesystem cannot come from a zvol.
func TestMutCloneKindMustMatchSource(t *testing.T) {
	tab := NewTable([]Row{
		vol("zv", func(r *Row) { r.Type, r.Volsize = zfs.TypeVolume, 1 << 20 }),
		snap("zv@s", func(r *Row) { r.Volsize = 1 << 20 }),
	})
	for _, r := range []CreateVolumeRequest{
		{Name: "d", CapacityBytes: 1, SourceSnapshotID: "zv@s"},
		{Name: "d", CapacityBytes: 1, SourceVolumeID: "zv"},
	} {
		if _, err := pl.PlanCreateVolume(tab, r); code(err) != InvalidArgument {
			t.Errorf("%+v: got %v, want InvalidArgument", r, err)
		}
	}
}

// Idempotency: a committed filesystem whose reservation differs from the
// request is AlreadyExists (not a generic failure).
func TestMutCompatibleReserveMismatchCode(t *testing.T) {
	tab := NewTable([]Row{vol("a", func(r *Row) { r.Refquota = 100 })})
	if _, err := pl.PlanCreateVolume(tab, CreateVolumeRequest{Name: "a", CapacityBytes: 100, Reserve: true}); code(err) != AlreadyExists {
		t.Errorf("got %v", err)
	}
	tab = NewTable([]Row{vol("a", func(r *Row) { r.Refquota, r.Refres = 100, 100 })})
	if _, err := pl.PlanCreateVolume(tab, CreateVolumeRequest{Name: "a", CapacityBytes: 100}); code(err) != AlreadyExists {
		t.Errorf("unrequested reservation: got %v", err)
	}
}

func TestMutLogbiasDedupAllowedOnBothKinds(t *testing.T) {
	for _, k := range []VolumeKind{Filesystem, Block} {
		if _, err := ValidateProperties(k, map[string]string{"logbias": "throughput", "dedup": "off"}); err != nil {
			t.Errorf("%v: %v", k, err)
		}
	}
}
