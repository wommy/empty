# Adversarial review: Reconcile/GC (HEAD bdf89d0)

The repo was not modified. The proofs live in a copy at `/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/adv4/reconcile-copy`, in the file `internal/endpoint/adv4_test.go`. Each `TestAdv4*` test asserts the property it is named after, so every one of them **fails on HEAD**. To run them:

```
cd /tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/adv4/reconcile-copy
GOTOOLCHAIN=go1.27.3 go test ./internal/endpoint/ -run TestAdv4 -v
```

Excluded because they are already known and being fixed:
- received props counted as owned;
- clone-from-unowned leaving snapshots;
- partial-table-as-full;
- foreign snapshots on DeleteVolume as a policy. F4 below covers only what Reconcile adds to that.

I also ran `FuzzReconcileSafe` for 60 s (45k execs) and it **passes**. Its generator cannot build any of the shapes below (see F9).

---

## F1. HIGH: MinAge ages a row by its zfs `creation`, not by its commit, so a volume CreateVolume just reported as created is destroyed as "undesired"
- **Where:**
  - reconcile.go:131-141. `Committed` decides the reason, and `tooYoung` reads `r.Creation`.
  - reconcile.go:347-352. The re-plan under locks reclassifies the row without comparing it to the first plan.
  - reconcile.go:318-320. MinAge has no floor; 1 ns is accepted.
- **Scenario:**
  1. CreateVolume("pvc-x") failed more than an hour ago after its create op, for example ENOSPC on the `set`, and left an uncommitted row.
  2. external-provisioner keeps retrying.
  3. The caller reads its PV list (pvc-x is not a PV yet, because CreateVolume has not returned), then calls Reconcile.
  4. Reconcile's first scan sees pvc-x as old and uncommitted, plans it, and waits on `vol:pvc-x`. The retry holds that lock and commits.
  5. Reconcile re-scans: pvc-x is now committed, old by `creation`, and not desired. It is destroyed with `ReasonUndesired`.
  6. The provisioner has already received OK, so it binds a PV to a dataset that no longer exists.
- **Variants:**
  - No lock race is needed if the commit lands between the caller's list and Reconcile's first scan.
  - With a small `min_age_seconds` (any value ≥ 1 is accepted), a first-time in-flight create is hit in the same way.
- **Proof:** `TestAdv4CommitRaceDestroysJustProvisionedVolume` (plan reason "not in the desired set") and `TestAdv4TinyMinAgeInFlightCreate`.
- **Minimal fix:**
  - (a) In the locked re-plan, drop any action whose target and reason were not in the unlocked plan, and report it as `SkipChangedSinceScan`. Pass the first plan's `map[target]reason` in `Desired`, next to `held`.
  - (b) Stamp a commit time (`<prefix>:committed_at`) in the commit `OpSet` (plan.go:127), and have `tooYoung` use max(creation, committed_at) for committed rows.
  - (c) Enforce a MinAge floor (for example 10 min) and reject anything below it.

## F2. HIGH: no guard against a truncated or wrong desired set, so one call deletes everything not listed
- **Where:** reconcile.go:134-139. Only `len(want)==0` is guarded. A set in which **no** id exists (all `SkipDesiredMissing`, reconcile.go:118-122) still collects everything.
- **Scenario:** the caller sends one page of a paginated PV list, a namespace-filtered list, or the list from a different cluster or parent.
- **Proof:** `TestAdv4PartialDesiredSetMassDeletes`.
  - A desired set of `{pvc-00}` destroys 49 of 50 committed volumes.
  - A set naming only a nonexistent id destroys all of them.
  - There is no cap, no confirmation and no dry-run requirement.
- **Minimal fix:**
  - Treat "no desired id exists" like the empty set.
  - Add `max_deletions` to the request, with a conservative default (for example 10, or a percentage), and refuse with FailedPrecondition plus the dry-run plan above it.
  - Better: a two-phase token. The dry run returns `plan_digest`, and a destructive run must echo it; the locked re-plan must produce a subset of it.

## F3. HIGH (liveness and observability): one failing destroy aborts the run, hides what was already destroyed, and starves every later volume forever
- **Where:**
  - reconcile.go:352-354. `apply` stops at the first error and the plan is discarded (`return ReconcilePlan{}, err`).
  - apply.go:16-46.
- **Scenario:** an undesired volume whose `zfs destroy -r` fails at the zfs level. Any of these does it:
  - a clone of one of its snapshots outside the parent. The scan is depth 1 (table.go:208), so `ClonesOf` cannot see it, and real `zfs destroy -r` refuses with "has dependent clones" (zfs_main.c:1448-1460).
  - a `zfs hold`. `userrefs` is not scanned, and dsl_destroy.c:78-79 returns EBUSY.
  - an open zvol, a running send, or "dataset is busy".
- **Effects:**
  - Every Reconcile then fails at that op.
  - Actions planned before it already ran but are reported as nothing.
  - Actions after it never run.
  - Plan order is by id, so `a-stuck` blocks the whole parent.
- **Real-ZFS aggravation:** `destroy -r` is not atomic. Children and batched snapshots are destroyed in post-order before the held snapshot fails (zfs_main.c:1508-1520, 1909-1922), so a hold does not protect the rest of the tree.
- **Proof:**
  - `TestAdv4OneFailureStarvesAndHidesDestroys`: natural, a foreign clone in `tank/other`. `a` is destroyed, the response is `{}` plus an error, and `c` is never collected.
  - `TestAdv4BusyDestroyAbortsRun`: busy injected.
- **Minimal fix:**
  - Apply per candidate group (a volume plus its released origin).
  - On error, record a `ReconcileSkip{Target, err}` and continue.
  - Return the plan of what actually ran, alongside an aggregate error. The proto then needs `failed[]`, or the error status should carry the partial plan.

## F4. HIGH (policy; Reconcile-specific amplification): foreign snapshots on undesired volumes are destroyed unattended
- **Where:**
  - plan.go:324-327. Only snapshots with `SnapshotName != ""` block, and then `-r` (plan.go:334) runs.
  - reconcile.go:156 inherits this.
  - The fuzz oracle at reconcile_fuzz_test.go:172 protects only CSI snapshots, so it codifies this.
- **Scenario:** sanoid or zfs-auto-snapshot snapshots, or a replication base, on a volume that has dropped out of the desired set. These are destroyed with no per-volume request at all.
- **Proof:** `TestAdv4ForeignSnapshotsDestroyed`.
- **Relation to known work:** the owner's decision (block unless opted in) is not implemented at HEAD. When it is, Reconcile must **not** inherit the opt-in: `ReconcileRequest` has no flag and should not get a blanket one.
- **Fix:**
  - Implement the decision in PlanDeleteVolume. Any non-deferred snapshot that is neither ours nor a clone-source snapshot of ours blocks deletion.
  - Change the fuzz oracle's `keep` so it covers every unowned snapshot.

## F5. HIGH: unowned child datasets under a volume are destroyed by `-r` and never appear in the plan or the dry run
- **Where:**
  - table.go:230-236. The scan drops `vol/child` rows.
  - plan.go:334 uses `Recursive: true`.
  - plan.go:329's `row.Name+"/"` exemption assumes a child is benign.
- **Scenario:** a filesystem volume with a nested dataset that someone else made. Examples: an admin, a delegated `zfs allow` inside a pod, or nested democratic-csi.
- **Proof:** `TestAdv4ChildDatasetsDestroyed`. The dry run lists only `old`, and the apply removes `old/someone-elses` and its snapshot.
- **Minimal fix (OpenZFS enforces "don't touch what you don't own"):**
  - Drop `-r`.
  - Destroy the volume's own snapshots explicitly: deferred CSI snapshots and clone-source snapshots with `-d`.
  - Then destroy the dataset non-recursively. zfs itself then refuses on any child or foreign snapshot ("has children"), and the planner can report that as a skip.
  - Alternatively, scan the candidate at depth -1 and refuse when it has descendants.

## F6. MEDIUM: clone-source snapshots are recognised by name alone, so a user's snapshot named `volume-source-for-volume-*` is collected, even on a desired volume
- **Where:**
  - reconcile.go:198-229. The prefix test is at line 200, and the `Managed` skip at line 203 means an *unowned* snapshot qualifies.
  - plan.go:222 is the root cause: the endpoint cuts the clone-source snapshot with no ownership props.
- **Proof:** `TestAdv4UserSnapshotWithReservedPrefixCollected`. `db@volume-source-for-volume-manual-pre-upgrade` on desired `db` is destroyed. Note that CreateSnapshot reserves the prefix only for CSI names; `zfs snapshot` by hand, or another tool, is unconstrained.
- **Minimal fix:**
  - Stamp `managed=true` plus `<prefix>:clone_source_for=<id>` on the OpSnapshot at plan.go:222.
  - Collect only stamped snapshots whose stamp equals the name suffix.
  - Allow name-only (democratic-csi-made) snapshots only in the adoption mode, or not at all.

## F7. MEDIUM: the released origin is destroyed wherever it lives, outside the parent too, and whatever clone id it names
- **Where:** plan.go:335-337 (`strings.Contains(row.Origin, "@"+prefix)`) and reconcile.go:164-168. Here `rel()` leaves a full name, so the lock key is `vol:tank/other/golden`.
- **Scenario:** an adopted or hand-made clone whose origin is `tank/other/golden@volume-source-for-volume-someone`. Collecting the clone `destroy -d`s a snapshot outside the parent that the endpoint never made, with a suffix that does not even match the clone's id.
- **Proof:** `TestAdv4ReleasedOriginOutsideParent`. The action targets `tank/other/golden@…` and locks `[adopted tank/other/golden]`.
- **Minimal fix:** release the origin only when all of these hold:
  - `origin == p.full(src)+"@"+prefix+id`, where `src` is a valid volume id under the parent;
  - `src` is Managed;
  - after F6, the snapshot is stamped.

## F8. MEDIUM-LOW: all-or-nothing locking, so one busy volume aborts the whole GC and Reconcile convoys CSI calls
- **Where:**
  - reconcile.go:331-345. It makes one `Acquire` of every key, waits up to LockWait (30 s), and then returns `Aborted`.
  - lock.go `Acquire` holds the keys it has already taken while it waits, in sorted order.
- **Effects:**
  - Any single operation longer than LockWait on any targeted volume fails the run, and nothing is collected.
  - While Reconcile waits, and through the whole multi-destroy apply, CSI calls on held volumes fail with Aborted. That includes *desired* volumes held only as clone-source owners (reconcile.go:167, 217).
  - There is no deadlock, because keys are taken in sorted order.
- **Proof:** `TestAdv4OneBusyLockAbortsWholeRun`. `vol:z` is held, and `a` is not collected: "operation pending on [vol:a vol:z]".
- **Minimal fix:**
  - Lock per candidate group, with a short try-acquire.
  - Skip a busy group as `SkipChangedSinceScan` ("busy").
  - Release each group's locks right after its apply. This combines naturally with F3.

## F9. MEDIUM (test gap): FuzzReconcileSafe cannot generate the dangerous shapes, and its oracle encodes two of the bugs
- **Shapes the generator never builds:**
  - foreign snapshots on owned volumes (F4);
  - child datasets (F5);
  - clones or origins outside the parent (F3, F7);
  - holds, busy, or any destroy failure (F3);
  - concurrency with CreateVolume (F1);
  - deferred snapshots, promote, clone-from-snapshot, received.
- **Shapes it does build:** clone-of-clone (via `cloned`) and the prefix-named unowned snapshot (`leak`).
- **Oracle bugs:**
  - It *requires* the prefix-named unowned snapshot to be collectible (lines 140-147), which codifies F6.
  - It protects only CSI snapshots (line 172), which codifies F4.
- **Evidence:** 60 s of fuzzing (45k execs) passes.
- **Fix:** add bits for each of the shapes above, inject a `Fail` for destroy, and replace the snapshot rule with "every row the endpoint does not own still exists afterwards".

## F10. LOW: clock source
- `Now` is the endpoint's clock, and `creation` is the zfs host's (table.go:196).
- With LocalRunner they are the same clock, so there is no issue today.
- With the SSH runner the comment anticipates (zfs/runner.go:12), an endpoint clock ahead of the host by more than MinAge makes young rows look old.
- Rows created during a boot window before NTP synced (RTC behind) look decades old.
- Backward skew is safe, because the row is treated as too young.
- **Fix:** with a remote runner, take `Now` from the host. F1(b) and F1(c) also blunt this.

## Checked and dropped
- **min_age=0 meaning 1 h:** this is documented, and the large-value overflow is rejected at server.go:200. It is not a footgun on its own; the real problem is the missing *floor* (F1c).
- **Lock-order deadlock with CreateVolume:** sorted acquisition rules out cycles. The CreateVolume clone path locks `vol:src`, so it is serialised against collecting `src` and against orphan collection of `src@…-<clone>`.
- **Clones of clones and promoted clones:** the fixpoint orders them correctly, and simulateDestroy matches dsl_destroy.c:1081-1085 rmorigin reaping for these shapes.
- **Ids that are prefixes of each other:** all lookups split on `@` exactly or compare `name+"/"`, so there is no confusion.
- **Deferred CSI snapshot with a clone elsewhere:** the volume is refused through the clone check, and reaped in a later round once the clone is collected. Correct.
- **simulateDestroy divergence:** none was found for the shapes the planner admits. The divergences that matter (children, holds, out-of-parent clones, non-atomic `-r`) arise because the table does not contain those rows, which is F3 and F5, not a modelling bug in simulateDestroy.
