# Wave 24: did test-forks or sim-facts weaken tests or the simulator?

**Verdict:** neither merge weakened a test or made the simulator less faithful in any way that matters. Every point below is either equivalent or diagnostic only, or it was already there before the merge (shown by running the same probe against the pre-merge sim).

## 1. test-forks (merge 660c967, branch commit 7def33e)

### (a) TestPropertyRoundTrip: one read-back in the parent's cleanup
`internal/endpoint/props_roundtrip_test.go:121-185`

- **Masked by the retry? No.** The read-back now runs after the identical retry instead of before it. A retry could only hide a bad first create if that first create returned OK while leaving the row uncommitted, because only an uncommitted row converges its properties (`plan.go` `propSet`, about line 370). That cannot happen: `readVolume` returns Aborted for any row that is not ServedVolume (`endpoint.go:707`). On a committed row, the retry only compares properties (`propsMismatch` -> AlreadyExists, `plan.go` about line 395) and writes nothing.
- **Misattributed? No.** A mismatch fails the parent test, and the message names the row (`kind/key=value`). No CI step reruns failing tests. `ci/check-skips.sh` requires the parent to pass.
- **Skipped when a subtest fails or skips? Only in the harmless case.** A row is recorded right after its create succeeds. If the create calls Fatal, the row is not recorded, but that subtest has already failed. No subtest calls Skip.
- **Panic (diagnostics only).** When a parallel subtest panics, Go's `tRunner` doPanic runs the parent's cleanups while sibling subtests may still be running. `checkReadBack` iterates `created` without taking `mu` (`props_roundtrip_test.go:150-167`). The result can be "fatal error: concurrent map iteration and map write", which can hide the original panic. The run still fails, so nothing is masked.
  - Fix: take `mu` in `checkReadBack` (copy the map under the lock).
- **Does PROPRT_SHARD still prove each row ran? Yes, for create and retry; the read-back is covered by the parent.** `ci/check-split.sh` still requires that the number of subtests reporting a result equals the k summed over shards. A subtest PASS no longer covers its own read-back, but a passing parent does, because the cleanup checks every created row.

### (b) Crash-test fingerprint: parent mountpoint from the recursive read
`internal/endpoint/crash_test.go:226-250`

**Equivalent.** The old `h.Prop` and the new `ListColumns` cell both read the value column of `zfs get -j -p`. The source is never consulted, so it makes no difference whether the parent's mountpoint is inherited, local or default. The value is the same, including `none` and `legacy`.

- The parent row was already left out (`zfs.List` skips `parent`), so excluding it now is identical.
- The new code is slightly stronger. The old `h.Prop` returned `""` on error, which silently made `Replace` prefix `$PARENT` to every mountpoint. The new code fails with Fatal if the parent is not listed.

### (c) runTrace reuses the last world
`internal/endpoint/modelrun_test.go:839-856, 925-1097`

**Equivalent, and no mutant separates old from new.**

`pre` feeds only `foreignSnapshot(pre)` (line 972), which is compared by `checkForeign`. Nothing runs between the previous step's final read and the next step:

- The endpoint starts no goroutines (grep for `go func` and `AfterFunc` in non-test endpoint code finds nothing).
- `mFaultRunner` is synchronous; killAfter waits for the inner run.
- The clock advances after `pre` is read, in both versions.
- `world` is never mutated after it is kept.
- A masked step and `adm.recvsnap` on a real pool return before `r.last = nil` and run nothing.
- A refused admin step, a failed step and a dry-run Reconcile all clear the cache.

Even a hypothetical endpoint with async side effects would be caught at least as well by the new code: its before-snapshot is older, so its check window contains the old one.

**Evidence.** Scratch copy at `wave24/mut`, differential test `internal/endpoint/zz_diff_test.go`. It runs every seed through `runner.step` with `reuse` true and with `reuse` false and compares the failing step and kind.

| Run | Seeds | Result |
|---|---|---|
| Unmutated | 1000 | 1000 pass both |
| `MODEL_UNMASK=all` | 1000 | 999 pass both, 1 fails both at the same step and kind |
| M1: `reconcile.go:269` `!r.OwnedVolume()` -> `!r.Managed` | 1000 | 0 failures in either (the generator never makes managed-without-name) |
| M2: `plan.go` PlanDeleteSnapshot drops the `OwnedSnapshot` check | 600 | 240 fail both, 0 differ in step or kind, 0 caught only by old |
| `MODEL_SEQS=1000 go test -tags nightly -run TestExploreModelMasksStillBite` | 1000 | new runTrace: GAP-RECRACE still bites at seed 709. Old runTrace (`wave24/old`): same seed 709 |

## 2. sim-facts (merge f571a3d, tip 85238de)

Checked against OpenZFS 2.4: `dsl_dir.c` (`dsl_dir_space_available`, `set_reservation_check`, `set_quota_check`), `dsl_dataset.c` (`set_refreservation_check`, `dsl_dataset_space`), and `libzfs_changelist.c`, `libzfs_dataset.c`, `libzfs_mount.c`, `libshare/nfs.c` fetched at tag zfs-2.4.0 into `wave24/src`.

**Faithful, with empty datasets (unique and referenced taken as 0):**

- `dirUsed`: own refreservation, plus the larger of each child's used and its reservation.
- `dirAvail`: matches line for line, including that the root gets no reservation slack (the `parentspace != UINT64_MAX` guard).
- refreservation check against the dataset's own dir avail and refquota.
- reservation check: MAX(used, new) - MAX(used, old), against the parent's avail, or the pool's size less used at the root.
- quota check: refused below the reservation or below `dd_used`.
- `available` = dir avail + refreservation, capped by refquota.
- Changelists:
  - gathered for every property, but non-empty only for mountpoint, sharenfs and sharesmb, plus canmount=off on a mounted filesystem;
  - `-u` does neither prefix nor postfix;
  - on a busy unmount: unshare, then reshare, `needpost` cleared from the failing node on, and the earlier nodes remounted;
  - postfix mount and share conditions match;
  - a failed ioctl runs no postfix.

Against the old sim, every changed behaviour moved toward the real one: the old sim reshared after any set, set canmount=off without a busy check or an unshare, and left nodes unmounted after a busy failure.

**No regression found.** The remaining gaps all predate the merge: I ran the same probes (`wave24/{mut,old}/internal/zfs/zfstest/zz_probe_test.go`) against the pre-merge sim and got identical results.

1. **Received space properties are ignored by the space math. The sim accepts what real ZFS refuses.**
   - Cause: `sim.go` `num()` and `spaceCheck`'s `get()` read only `n.props`; a received value lives in `n.recv`.
   - Probe: `SendRecv` a thick 64 MiB zvol (refreservation 70254592, source received) into `tank/g` with quota=100M. The sim then accepts `zfs set quota=10M tank/g` and a second 64M thick zvol. Real ZFS returns ENOSPC for both, because `dd_used` already holds the 64M or more.
   - Relevance: the model's `adm.recv` makes exactly such datasets.
   - Fix: read the effective value (local, else received unless `inh`) in `num` and `get`.
2. **`gather` records `cn_shared` from `s.shared` even when the filesystem is unmounted** (`sim.go:1705`). Real `zfs_is_shared` returns false when the filesystem is unmounted.
   - Probe: SetNFSDown, Unmount, SetNFSDown(false), then `set sharenfs=off`. The sim removes the stale zfs.exports line; real ZFS keeps it (no `cn_shared`, and `zfs_unshare` finds no mnttab entry).
   - Reachable only through SetNFSDown, and the old sim's `reshare` did the same.
   - Fix: `cl.shared[k] = n.mounted && inShared`.
3. **`zfs inherit -S canmount` gathers no changelist in the sim** (`sim.go` about line 1229). Real `zfs_prop_inherit` gathers canmount as a mountpoint changelist, so it unmounts and remounts, and it fails busy under a pinned mounted child. The sim accepts it. Nothing in the repo issues it.
4. **Cosmetic:** sharenfs changelist nodes are not sorted by mountpoint as the real AVL tree is, which changes only the order of lines in zfs.exports. Also, recursive inherit stops at the first prefix failure, where `zfs_for_each` carries on to the other datasets (this predates the merge).
