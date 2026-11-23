# Adversarial review: internal/zfs, internal/lock, internal/endpoint

Reviewed against the tree as of 03:38 (endpoint.go 796 lines, with the sparse/reserve check in adopt and table.go present but unused).

NOTE: while this review was being written, the endpoint package changed again (apply.go added, endpoint.go reworked, zfs.go parseGet now tolerates tabs in values). Line numbers refer to the 03:38 copy at .../scratchpad/review/mod. Re-run the proof tests against the new tree to see which findings still hold.
Proof tests (all fail on current code): `/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/review/mod/internal/endpoint/review_test.go`, `review2_test.go` (run in a copy of the module: `cd .../review/mod && go test ./internal/endpoint/`).

## Critical

### C1. A successful thick/reserved clone cannot be retried (AlreadyExists)
- endpoint.go:429-436 (adopt), with endpoint.go:341/374 (Clone) and 380-398 (growClone)
- `zfs clone` never carries over refreservation (real zfs and the sim agree), and growClone never sets it. So a clone requested with Block+Sparse=false, or Filesystem+Reserve=true, has refreservation=0. Every identical retry, including the normal CO retry after a lost response, hits `r.Sparse == (reserved > 0)` and gets ALREADY_EXISTS. This is the default for block clones from either a snapshot or a volume. The request's thickness is also silently ignored on the first call.
- Proof: TestThickCloneRetry ("volume dst exists with sparse=true").
- Fix: in growClone, set refreservation=volsize (block, !Sparse) and refreservation=refquota (fs, Reserve). Order it before the marker so adopt and the create path agree.

### C2. A failed growClone (or a crash after Clone) is never completed and is stuck forever
- endpoint.go:347/377 then endpoint.go:423 (adopt)
- The clone already has managed=true plus the source props, but its size is still the source's (block) or 0/unlimited (fs). An identical retry goes to adopt, `v.CapacityBytes < want` returns ALREADY_EXISTS, and the volume can never be provisioned or listed. This contradicts the doc comment "completed on retry". Real zfs makes this likely: `set refquota` below referenced fails with "size is less than current used or reserved space", which is unclassified and maps to Internal.
- Proof: TestGrowCloneRetryStuck.
- Fix: in adopt, when provision_success is not set, re-run the sizing (growClone and the reservation from C1) instead of comparing capacity, then finish.

## High

### H1. A user snapshot named `volume-source-for-volume-<X>` is hijacked, then destroyed
- endpoint.go:368-371 (ErrExists is swallowed), endpoint.go:506-507 (defer-destroy origin by prefix), endpoint.go:622 (no reserved-prefix check)
- CreateSnapshot("volume-source-for-volume-b", a), then CreateVolume b from volume a. b silently clones the old user snapshot, so it gets stale data. DeleteVolume(b) then defer-destroys the user's CSI snapshot. The reverse order also works: CreateSnapshot returns the internal snapshot as "idempotent" (see H2).
- Proof: TestPrefixCollisionDestroysUserSnapshot.
- Fix: reject CSI snapshot names with the internal prefix. Tag internal snapshots with a local property and only reuse or destroy them when that property is present.

### H2. Snapshots inherit managed_resource, so internal and foreign snapshots count as "managed"
- endpoint.go:713 (listSnapshots), 780 (DeleteSnapshot), 403/485 (adopt, DeleteVolume)
- On real zfs (and in the sim), a snapshot inherits user properties from its dataset. `vol@volume-source-for-volume-b`, and any snapshot an admin cuts, shows up in ListSnapshots with Name "", and DeleteSnapshot will destroy it. Likewise, a managed_resource set on Parent makes every child dataset adoptable and deletable.
- Proof: TestInternalSnapshotListed.
- Fix: honour ownership props only when `Prop.Local()` is true (source local/received).

### H3. The internal snapshot leaks on a clone failure or a DeleteVolume crash, then is reused stale
- endpoint.go:369-376; endpoint.go:499-511
- If Clone fails after the snapshot was cut, or the process crashes between destroying the volume and defer-destroying its origin, the internal snapshot stays behind. A retried DeleteVolume returns NotFound and success, so it is never cleaned up. A later CreateVolume with the same name from the same source reuses it (ErrExists is ignored), so the new volume gets old data. The leaked snapshot is also listed (H2).
- Proof: TestStaleInternalSnapshotReuse.
- Fix: destroy the snapshot when Clone fails. On ErrExists with no clones, destroy and recreate it.

## Medium

### M1. LimitBytes is ignored for filesystems (refquota 0 = unlimited)
- endpoint.go:307-312 (create), 426 (adopt: `0 > limit` is false)
- CreateVolume{Filesystem, LimitBytes: 1GiB} creates an unlimited dataset. Adopting an unlimited fs under a limit also passes.
- Proof: TestLimitIgnored, TestAdoptUnlimitedUnderLimit.
- Fix: set refquota=LimitBytes when Capacity is 0 and a limit is given. In adopt, treat capacity 0 as infinite.

### M2. ExpandVolume puts a quota on an unlimited filesystem
- endpoint.go:543-553. With refquota=0, `want <= 0` is false, so refquota=want is set. That shrinks an unlimited volume, or fails with Internal when used > want.
- Proof: TestExpandUnlimited.
- Fix: if the fs has refquota 0, return as-is. ExpandVolume also never checks managed (it can resize foreign datasets).

### M3. Clones from filesystem snapshots silently drop the quota
- endpoint.go:391. With CapacityBytes 0, no refquota is set, and clones do not inherit refquota, so a clone of a 1GiB volume is unlimited.
- Proof: TestCloneDropsQuota.
- Fix: default to the source refquota.

### M4. A deferred (deleted) snapshot still blocks and still serves
- CreateSnapshot after DeleteSnapshot of a snapshot that has clones returns ALREADY_EXISTS (endpoint.go:653): the old snapshot is filtered out of the list but zfs still has it.
- CreateVolume from the deleted id succeeds (endpoint.go:328-341: defer_destroy is not checked).
- DeleteVolume of the source reports "has snapshot" for the deleted one (endpoint.go:494: defer_destroy is not filtered).
- Proof: TestRecreateDeferredSnapshot.
- Fix: check defer_destroy in all three places. For the name clash, rename the snapshot on deferred delete (e.g. `zfs rename` to a tombstone name) before defer-destroy.

### M5. The sim hides `zfs create -p` succeeding silently on an existing dataset
- zfs.go:192-196, endpoint.go:313, sim.go:153
- Real zfs_do_create with -p returns 0 when the target exists and applies no -o props. If another replica or tool creates the name between Get and create, finish (endpoint.go:446) stamps provision_success on a dataset we do not own and returns it.
- Fix: drop -p (ensure Parent exists in New), and/or re-check managed after create. Make the sim mirror the real -p behaviour.

### M6. The sim hides the volblocksize multiple rule
- endpoint.go:202 rounds to the configured 16K. Adopted baseline zvols with volblocksize 32K-128K fail ExpandVolume/growClone on real zfs ("must be a multiple of volume block size", which is unclassified and maps to Internal). sim.go:338-355 accepts any volsize.
- Fix: read volblocksize per dataset and round to it. Add the check to the sim.

## Low
- L1. The internal snapshot name `parent/src@volume-source-for-volume-<id>` exceeds 255 bytes for long (valid ≤128) CSI names, which fails as InvalidArgument (endpoint.go:368).
- L2. Pagination: tokens are correct, including when exactly maxEntries remain (the token is ""). But if the token's volume or snapshot is deleted between pages, the result is ABORTED (endpoint.go:609/748). Prefer "first id > token". Negative maxEntries is treated as unlimited; CSI requires InvalidArgument.
- L3. Sim divergences that are harmless today: snapshot depth is one less than real `-d` (sim.go:458-463); volsize is omitted for zvol snapshots (sim.go:418); clone volsize comes from the current source, not the snapshot (sim.go:223); promote does not move older snapshots or reparent inherited props (sim.go:322-334).
- L4. Ruled out: lock.go is clean. There is no lost wakeup, the release closure correctly releases only the keys it took, double release is guarded by Once, and a cancellation midway releases the partial set. This was race-stressed with 32 goroutines and timeouts. Security: no argv flag injection path was found. All user names pass ValidDataset/ValidSnapshot, and the full dataset name (Parent/…) is always the positional argument.
