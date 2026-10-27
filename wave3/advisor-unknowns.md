# Wave 3 advisor review: unknown gaps, edges and seams

Tree: I started on HEAD ba26fd8. HEAD moved to 1b9270b (Reconcile) while I worked, and an uncommitted parseColumns duplicate-line check appeared in the working tree. Line numbers are against 1b9270b: plan.go, table.go and zfs.go are unchanged except that hunk, and endpoint.go is shifted about +5. The repo was not modified.

All proof tests were re-run on a fresh copy of 1b9270b plus the working-tree zfs.go (`wave3/mod2`). The original copy is `wave3/mod`. To run them:

```
cd /tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave3/mod2
go test -run TestW3 -v ./internal/endpoint/
```

Test files: `mod2/internal/endpoint/w3_test.go`, `w3b_test.go` and `w3c_test.go`. Each W3 test asserts the invariant, so it fails on current code.

I first checked every finding against README "Open gaps" and "Corrections" and against `docs/research/adversarial/code-review*.md`, `csi-spec.md`, `gaps/*` and `simfidelity/*`. Anything already recorded there is excluded, except where noted.

---

## 1. HIGH: clone sources are not ownership-checked, so unmanaged datasets get snapshotted and copied out
- **Where:** plan.go:181-188 (from snapshot: only checks that the row exists, is a snapshot and is not deferred) and plan.go:206-216 (from volume: only checks that the row exists and is not a snapshot). Compare PlanCreateSnapshot at plan.go:406 and PlanExpandVolume at plan.go:348, which both require `Managed`.
- **Scenario:** an admin or another tool owns `tank/p/foreign`, which is unmanaged, inside the parent.
  - `CreateVolume{Name:c1, SourceVolumeID:"foreign"}` cuts `tank/p/foreign@volume-source-for-volume-c1` on the unowned dataset, clones it, and returns success. The caller now has a copy of someone else's data.
  - `CreateVolume{Name:c2, SourceSnapshotID:"foreign@admin"}` clones an admin's snapshot. That snapshot can no longer be destroyed by its owner ("has dependent clones").
  - The same paths accept the internal `v@volume-source-for-volume-x` snapshot and uncommitted (half-provisioned) source volumes.
- **Proof:** TestW3CloneFromUnmanaged. Both calls succeed, and `foreign@volume-source-for-volume-c1` is created.
- **Fix:** in both source branches, require `src.Managed && src.Committed`. For a snapshot source, also require `snap.Managed && snap.SnapshotName != ""`. Return NotFound otherwise, as for an id never issued.

## 2. MEDIUM (was HIGH; partly fixed in the working tree): a newline in a foreign user-property value forges scan rows
- **Where:** zfs.go:428 (parseColumns) and zfs.go:165 (parseGet) split `zfs get -H` output on `\n`. zfs does not escape property values, and a user-property value may contain newlines and tabs. checkProps (zfs.go:79-89) only guards values that *we* write.
- **Scenario:** a principal who can set user properties on `parent/zz` (for example `zfs allow … userprop` delegation) runs:

  `zfs set 'democratic-csi:csi_volume_name=x<TAB>local<LF>parent/victim<TAB>democratic-csi:managed_resource<TAB>true' parent/zz`

  zfs then prints a well-formed extra line: `parent/victim  managed_resource  true  local`. That line sorts after victim's genuine "-" cell and overwrites it. `DeleteVolume("victim")` then runs `destroy -r` on an unmanaged dataset.
  - **DoS variant:** a value with a newline that does not form a 4-field line makes every whole-parent scan fail with Internal. That breaks DeleteVolume, CreateSnapshot, ListVolumes and ListSnapshots.
- **Proof:** TestW3NewlineForgesOwnership. On ba26fd8 the victim is destroyed. With the uncommitted duplicate-line check in zfs.go the scan is refused instead.
- **Residual after that check:** the check only catches a forged line that collides with a real one. A **phantom** row for a name zfs never prints causes no collision. For example, a forged `parent/ghost  origin  parent/v@x` line makes `DeleteVolume(v)` fail FailedPrecondition ("has dependent clone ghost"). That is proven by TestW3PhantomCloneBlocksDelete, which still fails with the fix in place. A phantom snapshot row carrying `csi_snapshot_name=N` likewise blocks `CreateSnapshot(N)`. The DoS variant also remains.
- **Fix:** use `zfs get -j` (JSON, OpenZFS ≥2.3; CI runs 2.4.4). Failing that, extend the check: zfs prints each dataset's lines contiguously and in `-o` order, so reject a name that reappears after its block closed. That catches phantoms, because the injecting dataset's block resumes after the forged line.

## 3. MEDIUM: `OkExists` on OpClone adopts a dataset someone else created in the scan→clone window
- **Where:** plan.go:193 and plan.go:223 (`OkExists: true` on OpClone), apply.go:497, and endpoint.go:325-335 (readVolume does not check Managed or VolumeName).
- **Scenario:** a dataset that is not ours appears at `parent/c` after the scan. Sources include an external tool, an HA split-brain peer, or an admin. Then:
  - `zfs clone` fails with "already exists", and the failure is swallowed.
  - sizeOps sets `refquota` on the foreign dataset.
  - The commit stamps `provision_success=true` on it.
  - CreateVolume returns it as volume `c` with no managed marker.
  
  Afterwards GetVolume returns NotFound, and DeleteVolume returns FailedPrecondition ("not managed"), so the PV is stuck. This is the hazard the README rules out for `zfs create -p`, brought back through clone. OkExists on a clone is never needed for convergence: a replay re-scans and takes the uncommitted-row path.
- **Proof:** TestW3CloneOkExistsAdoptsForeign. The result is managed="", success="true", refquota mutated and err=nil.
- **Fix:** set `OkExists:false` on OpClone. In readVolume, require `row.Managed && (row.VolumeName == "" || row.VolumeName == id)`.

## 4. MEDIUM: a clone from a larger source is returned above `limit_bytes`, and the retry is AlreadyExists
- **Where:** validateCreate checks only `want ≤ limit` (plan.go:107). The clone inherits the source's volsize (plan.go:194 and plan.go:232), sizeOps never shrinks (plan.go:271), and compatible() then rejects the same row (plan.go:297-298).
- **Scenario:** a Block snapshot of 10 MiB is restored with `capacity=4 MiB, limit=5 MiB`. The first call succeeds with capacity 10 MiB, which violates CSI's limit. The identical retry returns ALREADY_EXISTS ("capacity 10485760 > limit 5242880"), which breaks the core idempotency invariant.
  - The same happens through the uncommitted path: plan.go:139-148 never checks the limit.
  - FuzzCreateConverges misses this because it never uses a content source.
- **Proof:** TestW3CloneExceedsLimit.
- **Fix:** in both clone branches, if `limit > 0 && sourceSize > limit`, return OutOfRange before any op. Apply the same check in the uncommitted branch. Extend the fuzz test to cover snapshot and volume sources.

## 5. MEDIUM: a filesystem restore with `Reserve` and no capacity is not idempotent
- **Where:** validateCreate allows Reserve with a source and want==0 (plan.go:98). From a snapshot, sizeOps' `Refres < want` is `0 < 0`, so no reservation is set (plan.go:281). compatible() then requires `Reserve == (Refres>0)` (plan.go:303).
- **Scenario:** `CreateVolume{FS, SourceSnapshotID:"src@s", Reserve:true}` succeeds with refreservation=0. The identical retry returns ALREADY_EXISTS ("reserve=false"). The same happens from a volume source whose refquota is 0.
- **Proof:** TestW3ReserveRestoreNotIdempotent.
- **Fix:** from a snapshot, compute want from the source's refquota as the from-volume branch already does. If the result is still 0 with Reserve set, return InvalidArgument ("reserve needs a capacity or limit") as for a plain create.

## 6. MEDIUM: a crash or failure right after the clone op converges to a different end state
- **Where:** the uninterrupted path derives size from the source (plan.go:195-200 adds refquota=src.Refquota, and plan.go:225-228 sets want=src.Refquota). The uncommitted replay path (plan.go:139-148) calls `sizeOps(row, want)` with want still 0, so it adds neither refquota nor reservation and then commits.
- **Scenario A:** an FS restore from a 10 MiB-quota snapshot, with no capacity, fails at `set refquota`, or the process crashes after the clone. The retry commits an **unlimited** volume, where an uninterrupted run gives refquota 10 MiB. This violates the "converges after a crash at any op boundary" claim.
- **Scenario B:** an FS clone from a volume with `Reserve:true` fails after the clone. The retry commits with refreservation=0, and the next identical call returns ALREADY_EXISTS.
- **Proof:** TestW3CrashAfterCloneDropsQuota (capacity 0 vs 10485760) and TestW3CrashAfterCloneReserve.
- **Fix:** compute the source-derived `want` once, before the `t.Get(id)` branch, so the create path and the uncommitted path use the same value. That needs the source row, which scanFor already loads. Add a crash-at-every-op-boundary fuzz: inject a failure at op k, retry, and compare the end state with the uninterrupted run.

## 7. MEDIUM: `received` counts as ownership, so `zfs send -R` copies become adoptable and deletable, and they break CreateSnapshot for the original
- **Where:** zfs.go:43 (`Local()` includes `received`) feeds `own()` at table.go:153-158. The README says "ownership is read only from *locally set* properties". adv-fencing.md mentions this only for the dropped epoch design, not for volume ownership.
- **Scenario:** an admin keeps a backup or DR copy next to the original:

  `zfs send -R parent/pvc-1@x | zfs recv parent/pvc-1-copy`

  Every user property arrives with source `received`, including those on the snapshots. As a result:
  - (a) ListVolumes reports `pvc-1-copy` with Name `pvc-1`, and DeleteVolume("pvc-1-copy") destroys it. DeleteVolume, ExpandVolume and GetVolume never check `VolumeName == ID`.
  - (b) Its snapshot `pvc-1-copy@snapA` carries `csi_snapshot_name=snapA`. PlanCreateSnapshot scans globally by name (plan.go:397-403), and `pvc-1-copy@…` sorts before `pvc-1@…` because '-' < '@'. A retried `CreateSnapshot("snapA","pvc-1")` therefore returns ALREADY_EXISTS ("exists for volume pvc-1") instead of the idempotent success. ListSnapshots also returns two snapshots with one CSI name.
- **Fix:** honour ownership only for `Source == "local"`. Also add consistency checks: a volume row is ours only if `VolumeName == ID`, and a snapshot row only if `SnapshotSource ==` the volume part of its ID. These checks also catch `zfs rename` copies. Legitimate DR adoption under the same name still passes once the properties are re-set locally.

## 8. LOW-MEDIUM (Reconcile now mitigates part of this): a final-coded error after a create's first mutation leaves residue
- **Where:** endpoint.go:297 (apply errors returned as-is) and zfsErr (endpoint.go:199-221): InvalidArgument, Internal, FailedPrecondition and NotFound are final for external-provisioner (`docs/research/edges2/sidecar-conformance.md`). ListVolumes hides uncommitted rows (endpoint.go:432).
- **Scenario:** an FS restore with capacity below the snapshot's referenced data does this:
  - `clone` succeeds.
  - `set refquota` fails with "size is less than current used" (InvalidArgument, final).
  - The provisioner marks the call Finished, and when the PVC is deleted it never calls DeleteVolume.
  - The uncommitted clone stays forever. It pins `src@s`: DeleteSnapshot only defers. It also makes `DeleteVolume(src)` fail permanently with "has dependent clone".
  
  Any unclassified zfs error (Internal) after op 1 behaves the same way. The "residue converges" argument in multi-writer.md assumes a retry that a final code prevents.
- **Since 1b9270b:** Reconcile collects uncommitted rows older than `min_age`, but only when some caller runs it. cmd/zfs-csi never does. Until then the residue pins source snapshots and blocks DeleteVolume of the source.
- **Fix:** when a create plan fails after a mutating op with a final code, destroy the uncommitted, owned target best-effort. This is safe: the id was never returned, and the commit marker is absent. Alternatively, return a non-final code (Aborted) so the CO keeps retrying, which keeps the residue reachable.

## 9. LOW-MEDIUM: whole-parent scans still map a missing or exported parent to NOT_FOUND
- **Where:** table.go:204-214. Review-2 #1 fixed this only for `only != ""`, so the `scan("")` path falls through to zfsErr and NotFound.
- **Scenario:** with the pool exported or the parent gone, every one of these returns NOT_FOUND instead of UNAVAILABLE:
  - DeleteVolume
  - CreateSnapshot (final for the snapshotter, which then marks the VolumeSnapshot failed)
  - ListVolumes
  - ListSnapshots
  - GetCapacity (endpoint.go:519, `Get(parent)`)
- **Proof:** TestW3MissingParentWholeScan. All four calls return code 5.
- **Fix:** in scan, map IsNotFound to Unavailable whenever the failing root is the parent. Do the same in GetCapacity.

## 10. LOW: `OkMissing` destroy can report success while the pool is exported
- **Where:** apply.go:498. Real zfs prints the same "dataset does not exist" for a missing dataset and for a pool that is not imported (review-2 #1). A pool export between the scan and the destroy makes DeleteVolume or DeleteSnapshot return OK while the data still exists, so it is orphaned on re-import.
- **Fix:** when an OkMissing tolerance fires, call `Exists(Parent)` once. If the parent does not exist, return Unavailable.

## 11. LOW: uncommitted volumes are invisible to List but usable everywhere else
- **Where:** GetVolume (endpoint.go:395), ExpandVolume (plan.go:348) and CreateSnapshot (plan.go:406) check only `Managed`. ValidateVolumeCapabilities confirms through GetVolume. ListVolumes requires `Committed`.
- **Effect:** a half-provisioned id, for example one guessed or seen in logs, can be snapshotted or expanded, or used as a clone source (see #1).
- **Fix:** require `Committed` on every path that serves a volume to callers.

## 12. LOW: adopted baseline detached-snapshot ids are falsely reported as deleted
- **Where:** endpoint.go:497-500. The baseline issues `<vol>/<name>` ids for detached snapshots (adversarial/baseline.md:41). parseSnapshotID fails on them, and DeleteSnapshot returns OK, so the snapshot leaks after migration. README lists detached snapshots as unsupported but not this false success.
- **Fix:** return FailedPrecondition (or Unimplemented) for ids of the `<vol>/<name>` form.

## 13. LOW: CSI CreateVolume refuses the zvol+filesystem shape that Validate and Expand support
- **Where:** controller.go:107-110 refuses mount access with `fs_type` other than ""/zfs. capSupported (controller.go:134-137) and ControllerExpandVolume (controller.go:365) explicitly support "a zvol that carries a filesystem (adopted from the baseline)".
- **Effect:** a migrated democratic-csi iSCSI+ext4 StorageClass cannot provision. The CreateVolume retry for an adopted PV of that class gets InvalidArgument, which is final.
- **Fix:** map mount+non-zfs fs_type to BLOCK, or document the asymmetry.

## 14. LOW: the API's unspecified kind silently becomes a filesystem
- **Where:** server.go:37 (`kindIn`). A non-CSI front end that omits `kind` gets a filesystem.
- **Fix:** return InvalidArgument for VOLUME_KIND_UNSPECIFIED.

## 15. LOW: transport exposure beyond the 0600 endpoint socket
- **Where:**
  - cmd/zfs-endpoint/main.go:104-106: `-listen host:port` serves unauthenticated plaintext TCP today. The README implies a 0600 unix socket is the only exposure until auth lands.
  - cmd/zfs-csi/main.go:226-236: the CSI socket gets neither the umask hardening nor the 0700 directory that review-2 #3 added to the endpoint. Under umask 0 it is world-connectable and proxies to the root endpoint.
- **Fix:** refuse TCP unless an explicit `-insecure-tcp` flag is given. Apply the same Umask(0177) treatment to the CSI socket.

## 16. LOW: PropertyPrefix is not validated
- **Where:** endpoint.go:86-88 accepts any prefix, but checkProps (zfs.go:79-89, `^[a-z0-9_.:\-]+$`) refuses uppercase and other characters.
- **Effect:** with `-property-prefix Democratic-CSI`, scans work, but every create or snapshot fails InvalidArgument at apply. Startup does not catch it.
- **Fix:** validate `prefix + ":x"` against propRE in `New`.

## 17. LOW (sim fidelity): a snapshot's volsize tracks the live zvol
- **Where:** sim.go:600-603 reads the parent's *current* volsize for a snapshot. Real zfs reports the size at snapshot time. This is distinct from the known L3, which is about the clone's volsize.
- **Effect:** snapshot SizeBytes and restore sizing after an ExpandVolume diverge from real zfs. No current test expands after a snapshot, so the divergence hides #4-style bugs instead of failing tests.
- **Fix:** store volsize on the snapshot node at creation.

## 18. LOW: inherited `mountpoint=none` or `legacy` is passed to CSI as a path
- **Where:** endpoint.go:241 and controller.go:217-219 put `Mount` in the volume context verbatim.
- **Fix:** omit the key, or flag it, when the value is not absolute.

## 19. MEDIUM (new seam in 1b9270b): Reconcile inherits #7 and #1, so the README's "never touches unowned datasets" does not hold
- **Where:** reconcile.go:130 (`!r.Managed`, where Managed comes from `own()` and so includes `received`) and reconcile.go:209-211.
- **Scenario A (#7):** an admin's `zfs recv` backup copy under the parent arrives Managed and Committed with source `received`. Any Reconcile whose desired set is the cluster's PV list destroys it as "undesired".
- **Scenario B (#1):** `CreateVolume{SourceVolumeID:"foreign"}` leaves `foreign@volume-source-for-volume-c` on an unmanaged dataset. After c is deleted, Reconcile skips it as SkipUnmanagedSource, so the endpoint's own snapshot on someone else's dataset is never collected.
- **Fix:** the ownership fix in #7, which makes `own()` local-only and checks `VolumeName == ID`, and the source check in #1. Add a received-copy row to FuzzReconcileSafe; this needs the sim to emit `source=received`.

---

## Known findings that are still open at HEAD (not counted above)
- **H3 (code-review.md):** the internal snapshot still leaks and is still reused stale.
  - OpSnapshot keeps `OkExists:true` (plan.go:222).
  - PlanDeleteVolume returns an empty plan when the row is missing (plan.go:318-320), even though DeleteVolume already has a whole-parent table that could reap `*@volume-source-for-volume-<id>` with no clones.
- **M4 (code-review.md):** re-creating a CSI snapshot name after a deferred delete is still ALREADY_EXISTS (plan.go:409-411).
- **L2 (code-review.md):** deleting the token item between pages still makes the token ABORTED (endpoint.go:414).
- **H3:** Reconcile now collects orphan clone-source snapshots on demand, but the stale reuse on a name-reusing CreateVolume remains.

## Checked and ruled out
- **Lock coverage:**
  - Every clone path takes `vol:<source>`, and DeleteVolume, ExpandVolume, CreateSnapshot and DeleteSnapshot all take `vol:`. The `snap:` key mismatch for hashed ids is covered by `vol:`.
  - The global snapshot-name check is serialized by `snapname:`.
  - No uncovered interleaving among endpoint calls was found.
- **classify:** a dataset name cannot spoof a higher-priority pattern, because our regex forbids spaces and the patterns that list names (children, clones) rank first.
- **Ids:** parseSnapshotID is strict about `@`, `/` and length.
