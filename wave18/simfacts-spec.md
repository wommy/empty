# Sim facts, wave18: space accounting, the changelist, and copies (spec)

Base: 7048b51. Input: wave16/sim-gaps.md items 1 to 4, plus the copies row the coordinator asked for (item 5 is settled; see section 5). Sources are OpenZFS 2.4.0: `scratchpad/libzfs_changelist-2.4.0.c`, `scratchpad/libzfs_dataset-2.4.0.c`, `scratchpad/zfs_main-2.4.0.c`, `scratchpad/libzfs_util-2.4.0.c`, `wave16/src/{dsl_dir.c,dsl_dataset.c,libzfs_mount.c}`.

**There is a working prototype of all of it:** `scratchpad/wave18/prototype.diff` (701 lines, against 7048b51). It is applied in the scratch copy `scratchpad/wave18/work` (a git repository; one commit per step). With the prototype, `go test -count=1 ./...` passes. A 3000-seed × 25-step `TestRandomSurvey` reports 0 signatures, and a reduced nightly run (`-tags nightly`, `TestExploreModelSweep` with MODEL_SEQS=3000, plus `TestExploreLifecycle`) also passes. Every new fact row and test passes on the patched Sim and fails on the Sim at 7048b51, so each one discriminates. The builder can apply the diff and review it against this spec. Nothing below has been run on a real pool yet.

## Order (by risk to the endpoint's correctness)

| # | item | why this rank | endpoint code changes? |
|---|---|---|---|
| 1 | available / spaceCheck | The headroom guard (`headroom.go:133`) and the ENOSPC classification decide on this number. Sim-only tests validate the endpoint against wrong numbers. | No (see the finding at the end of item 1) |
| 2 | reshare only on a non-empty changelist (gaps #4) | It touches every `set` and `inherit` in every NFS test. Today the Sim heals a stale export as a side effect, which can hide a missing reshare in the endpoint. | No |
| 3 | failed mountpoint prefix: remount and reshare (gaps #3) | Only when an operator moves the parent over a pinned volume. Afterwards the Sim shows inherited (pre-pin and adopted) volumes as unmounted and still in zfs.exports. | No |
| 4 | canmount=off over a pinned child: busy (gaps #2) | Operator-only, on the parent. | No |
| 5 | copies row (fact only) | Pins a settled disagreement. | No |

Items 2, 3 and 4 are a single rewrite of the Sim's changelist (gather, prefix, postfix). Land them together, after item 1.

---

## 1. available, and the space checks that use it

### Semantics (dsl_dir.c:1198-1250 dsl_dir_space_available; dsl_dataset.c:2916-2930 dsl_dataset_space; dsl_dataset.c:4536-4590 refreservation_check; dsl_dir.c:1759-1810 reservation_check)

The Sim takes referenced and unique as 0 throughout (its `used` is already "0").

- `dirUsed(d)` (dd_used_bytes) = d's refreservation + Σ over child dirs c of **max(dirUsed(c), reservation(c))**. Today (sim.go:819-834) it sums every refreservation and reservation in the subtree, so a child's refreservation inside a reservation is counted twice.
- `dirAvail(d)` (dsl_dir_space_available(dd, NULL, 0, TRUE)):
  - parentspace = dirAvail(parent), or ∞ at the root.
  - quota = d's quota, or ∞; at the root it is min(quota, PoolSize).
  - If reservation(d) > dirUsed(d) and parentspace ≠ ∞, add reservation − used to parentspace.
  - The result is 0 if used > quota, otherwise min(parentspace, quota − used).
- `available(d)`, the property (dsl_dataset_space): dirAvail(d) + refreservation(d) (the unused part, all of it since unique = 0), then capped at refquota(d) (minus referenced, which is 0). Today (sim.go:836-860) it subtracts every reservation in the pool, never adds back the dataset's own unused reservation or refreservation, and ignores refquota.
- spaceCheck (sim.go:764-791) splits its `case "refreservation", "reservation"`:
  - **refreservation**: fails when nv > cur and either nv − cur > **dirAvail(name)** (not available(name): the refquota cap and the add-back must not apply), or refquota > 0 and nv > refquota.
  - **reservation**: used = dirUsed(name); delta = max(used, nv) − max(used, cur). The limit is dirAvail(parent), or at the root PoolSize − used. Fails when delta > limit, or quota > 0 and nv > quota.
- `quota` keeps `nv < reservation || nv < dirUsed(name)`, now with the new dirUsed.
- PoolSize keeps its meaning: the available space at an empty pool root.

Exact code: `prototype.diff`, hunks in sim.go `dirUsed`, `dirAvail`, `available` and `spaceCheck` (it imports "math").

### The zfsdiff model must change in the same commit

`internal/zfs/zfsdiff/cmds.go:141-195` (`reserved`, `available`, `dirUsed`) has the same flaw. `setSpecial` uses it at `:202` (refreservation) and `:223` (reservation). Replace them with the same `dirUsed`, `dirAvail` and reservation delta (prototype.diff, cmds.go). If the model is not changed, `TestCorpusModelVsSim` fails on the two new corpus sequences below. `available` stays projected as "0" (model.go:487), so no projection changes.

### Real-pool rows: TestRealFacts (`internal/endpoint/realfacts_test.go`, appended to `facts`)

Values are bytes, because the Sim does not parse size suffixes (`quota=256M` silently reads as 0). Each comparison has a 1 MiB tolerance for the real pool's referenced and unique metadata (tens of KiB); the Sim is exact. The rows sit under a quota so that pool-wide activity cannot move them: every endpoint shard runs parallel tests on the same pool.

| row | commands (f.p = G) | expected available |
|---|---|---|
| SPC-01 | `zfs set quota=268435456 G`; `zfs create -o reservation=67108864 G/p` | G/p ≈ 256 MiB (the reservation does not lower its own available) |
| | `zfs create -o refreservation=33554432 G/p/c` | G/p ≈ 224 MiB (charged once); G/p/c ≈ 256 MiB (own refreservation added back) |
| SPC-02 | `zfs create -o refquota=16777216 G/d` | G/d ≈ 16 MiB |

At 7048b51 the Sim reports G/p = 192 MiB after the reservation, and G/d = 1 TiB.

Add this helper next to the rows:

```go
// availNear: name's available is want within 1 MiB (the real pool's
// metadata: referenced, unique; the Sim takes both as 0).
func availNear(f *factEnv, name string, want uint64) error { /* get available, |got-want| <= 1<<20 */ }
```

SPC-01 could absorb SPC-02 (one fewer f.p create and destroy, about 0.2 s) if CI time is tight.

### Corpus sequences: `internal/zfs/zfsdiff/testdata/zfsdiff/` (ENOSPC outcomes; three-way model/Sim/real)

These are the right home for the spaceCheck outcomes. Both diverge between the model at 7048b51 and the new Sim, and real OpenZFS should agree with the new Sim.

`resv-grow-into-parent-resv.txt`:
```
# covers: quota
zfs create -o quota=104857600 $R/g
zfs create -o reservation=62914560 $R/g/p
zfs create -o refreservation=41943040 $R/g/p/c
zfs set refreservation=83886080 $R/g/p/c     # new: 0 (40M delta <= 20M unused resv + 40M quota left); old: ENOSPC
zfs set refreservation=115343360 $R/g/p/c    # ENOSPC everywhere (70M > 60M), clear of the metadata margin
```

`quota-over-child-resv-and-refresv.txt`:
```
# covers: quota
zfs create $R/g
zfs create -o reservation=10485760 -o refreservation=10485760 $R/g/p
zfs set quota=15728640 $R/g     # new: 0 (charged max(10M,10M)); old: "less than current used or reserved space"
zfs set quota=5242880 $R/g      # ENOSPC everywhere
```

Do not set the last step to exactly 100M: that sits on the metadata boundary, where the model and Sim would say 0 and the real pool ENOSPC. Run both sequences nightly only (full corpus); adding `resv-grow-into-parent-resv` to `PRSample` would cost about 0.3 s on TestZFSDiffReplay.

### Sim-only tests and endpoint behaviour

- `TestMinFreeBytesGuard` (headroom_test.go:86) passes unchanged: it has no nested reservations, and `PoolSize = G/2` still drives the root below the floor. All other `PoolSize` users (zfsdiff/backends.go:28, headroom_test.go:91/116/126) keep their meaning.
- Changes are visible only where reservations nest, or where a dataset's own reservation, refreservation or refquota is read through `available`.
- The endpoint's code is unchanged.

**Finding for the endpoint (not in scope here):** `headroom` reads `available` on the **parent**. On a real pool that number:
- includes the parent's own unused refreservation, which children cannot use, so the guard admits too much;
- is capped by the parent's refquota, which does not limit children, so the guard refuses falsely.

Both only matter when an operator sets refreservation or refquota on the parent. The fixed Sim now shows this, so a test can pin it. Suggest a follow-up issue.

---

## 2-4. The changelist rewrite (gather, prefix, postfix)

### Source facts (libzfs_changelist.c 2.4.0; libzfs_dataset.c:1769-1960)

- `zfs_prop_set_list_flags` runs `changelist_gather` for every property. For anything except mountpoint, sharenfs, sharesmb, and canmount=off on a mounted filesystem (also volsize and zoned, which gather as mountpoint), the list is empty (`changelist_gather` :719-722). `changelist_postfix` on an empty list returns at `uu_avl_last == NULL` (:203) **without `zfs_commit_shares`**, so no exportfs runs.
- canmount=off is gathered only when the filesystem is mounted, with cl_prop=MOUNTPOINT and cl_realprop=CANMOUNT (:710-711).
- With `-u` (CL_GATHER_DONT_UNMOUNT), neither prefix nor postfix does anything (:115, :192).
- **prefix** (:97-169), for a mountpoint list, walks deepest mountpoint first and calls `zfs_unmount` on every node, which does unshare, commit, then umount (libzfs_mount.c:614-625).
  - A not-mounted node is a no-op that keeps needpost.
  - On a failed umount, `zfs_unmount` **re-shares** the node and commits (:620-623), and sets its needpost to false. Every later node also gets needpost = false, and then `changelist_postfix` runs on the nodes before it (:164-165). The set then fails before the ioctl.
  - When an earlier property's changelist in the same `set` has already been prefixed, it is not postfixed (`goto error`).
- **postfix** (:180-302) walks in reverse (parents first) over nodes with needpost:
  - It mounts when the node is not mounted and either `cn_mounted`, or `((cl_prop==MOUNTPOINT && cl_prop==cl_realprop) || sharenfs || sharesmb || waslegacy) && canmount==on`.
  - Then: if sharenfs is set and the node is mounted, `zfs_share`; else if `cn_shared` (or waslegacy), `zfs_unshare`. Either branch marks commit, and commit runs `exportfs -ra` once.
- The sharenfs/sharesmb changelist holds nodes whose sharenfs **or** sharesmb is default or inherited (`change_one` :534-541). So a child with a local sharenfs but default sharesmb is in the list and gets re-shared with its own value. The Sim's `reshare` skipped it.
- inherit (`zfs_prop_inherit`, :2015-2050): one changelist per dataset, the same prefix and postfix.

### sim.go changes (prototype.diff has the code)

- Replace `prefix` and `postfix` (sim.go:1664-1712) with:
  - `type clist struct{ prop, real string; nodes []string; mounted, shared, needpost map[string]bool }`.
  - `gather(name, real)`: canmount maps to mountpoint; snapshot cn_mounted and cn_shared (`s.shared`); sort deepest mountpoint first for mountpoint lists. It reuses `changelist` (sim.go:1639).
  - `prefix(cl) error`: as described above. A successful unmount calls `unshare` and then `exportfs`. The busy node gets `unshare`, `exportfs` and `shareOne`, and on busy, `postfix(cl)` runs over the needpost nodes before it. The error text is unchanged: `cannot unmount '<mp>': pool or dataset is busy`.
  - `postfix(cl)`: as described above. It runs `exportfs()` only if a node took the share or unshare branch (and not while `nfsDown`).
  - `shareOne(name)`: zfs_share for NFS, writing the zfs.exports line only (no exportfs). It is a no-op unless the node is mounted and mountable with sharenfs ≠ off.
- `set` (sim.go:1111-1155):
  - Gather and prefix in list order for mountpoint, sharenfs, sharesmb, and `canmount=off && n.mounted`. Return on the first prefix error.
  - Delete the bare `n.mounted = false` for canmount=off.
  - After the props are applied, `postfix` each list.
  - **Delete `s.reshare(name)`** and the `if noMount` early return before it: with -u nothing is gathered anyway.
- `inherit` (sim.go:1210-1234): gather, prefix and postfix per target. **Delete the trailing `s.reshare(name)`**.
- Keep `reshare` for create, clone, recv (sim.go:261, 460, 561) and rename (sim_ext.go:88). Those are zfs_mount_and_share or changelist_rename paths and are out of scope.
- Not modelled, and documented in comments: waslegacy (it changes only the unshare branch on a filesystem that was legacy or none), sharesmb unshare in prefix, and zoned.

### The zfsdiff model

The model (cmds.go:279-335, 982-1018, 1099-1108) models neither busy nor shares, and its corpus cannot express a pinned (absolute) mountpoint: `$R` is a dataset name, and there is no mountpoint token. Leave the model unchanged. Its canmount=off "only d unmounted" shortcut still matches the success outcome. **TestRealFacts is the home for items 3 and 4** (items 2 and 3 of this section), and an NFS test is the home for the reshare item.

### Real-pool rows

**MNT-02 (gaps #3), TestRealFacts:**
```
zfs create G/p ; zfs create G/p/a ; MP=$(zfs get -Hpo value mountpoint G/p)
zfs create -o mountpoint=$MP/b G/p/b          # pinned, mounted inside P
f.z.Set(G/p, mountpoint=$MP-moved)            # want errors.Is(err, zfs.ErrBusy)
G/p/a mounted == yes ; G/p mountpoint == $MP
```
At 7048b51 the Sim gives `mounted="no"`.

**MNT-01 (gaps #2), TestRealFacts:**
```
zfs create G/p ; MP=... ; zfs create -o mountpoint=$MP/b G/p/b
f.z.Set(G/p, canmount=off)                    # want ErrBusy
G/p mounted == yes ; G/p canmount == on ; G/p/b mounted == yes
```
At 7048b51 the Sim succeeds and unmounts.

Use `f.z.Set` (it classifies the error) rather than `f.zfs`, as `mountpin_contract_test.go:57` does. Cleanup is the harness's `destroy -R`, which unmounts G/p/b first.

**Reshare only on a non-empty changelist (gaps #4): new test `TestFileExportStaleShareOnlySharenfsHeals`**, in `internal/endpoint/export_stale_test.go` (package endpoint_test). The `TestFileExport` prefix places it in the NFS shard group (`ci/real-zfs-guest.sh:59`), so it never shares a shard's export table with the NFS-free shards. It works on both the Sim and a real pool, and skips without nfsd (`h.NFS`).

```
ds=$Parent/stale ; zfs create -o sharenfs=rw=10.0.0.1/32 ds         -> served 10.0.0.1, not .2
zfs set -u sharenfs=rw=10.0.0.2/32 ds    (stale: no postfix)          -> served .1, not .2
zfs set factcheck:x=1 ds ; zfs set atime=off ds ; zfs inherit factcheck:x ds
                                                                      -> still .1, not .2   (7048b51 Sim: healed)
zfs set sharenfs=rw=10.0.0.2/32 ds                                    -> served .2, not .1
```
Reads go through `h.served` (etab). Full code is in prototype.diff. A cheap extra (optional, real-capable, no new dataset): in `TestCreateVolumePinsMountpoint` (mountpin_contract_test.go:57-63), after the refused move, assert `h.Prop(h.Parent+"/legacy","mounted") == "yes"`.

### Sim-only tests that exercise these paths (all pass on the prototype)

- `TestCreateVolumePinsMountpoint` and csi `nfs_test.go:255-265`: the busy move still fails busy. "legacy" is now remounted after the failure; the next move succeeds either way.
- `TestFileExportVerifiesKernelTable`, `TestFileExportCreateNotServed`, `TestFileExportDroppedHost`, `TestFileExportRequiresNFSD` (export_contract_test.go:239-345, export_create_contract_test.go:96): the heal now comes only from verifyExport's explicit `reshare` (`endpoint.go:662`, `export.go:725`, a sharenfs set). These pass, which shows the endpoint does not depend on the side effect.
- zfsdiff corpus: `set-u-*`, `share-*`, `mountpoint-remount`, `canmount-noauto-mounted` and `recv-sharenfs` compare model with Sim on properties and mounts, and stay equal.
- To find what a future change breaks: `grep -rn "SetNFSDown\|DropHost\|Exports()\|ErrBusy" internal --include=*_test.go`.

Endpoint behaviour: no change. The fix removes a Sim side effect that could hide a missing reshare.

---

## 5. copies on a thick zvol: fact row only (the coordinator's request)

This is settled. `zvol_volsize_to_reservation` (libzfs_dataset.c:5604-5646) reads copies with `nvlist_lookup_string`. But `zfs create` passes `real_props` (zfs_main.c:1204, 1253), and there `zfs_valid_proplist` has already turned the index property copies (zfs_prop.c:462) into a uint64 (libzfs_util.c:1900-1914). The lookup therefore fails and ncopies stays 1. The set paths (`zfs_add_synthetic_resv` and `zfs_fix_auto_resv`, :1626/1637/1692) also receive validated nvlists. So the Sim's `autoResv` (sim.go:1275), which ignores copies, is **right**, and gaps item 5 is withdrawn. No Sim change.

**VOL-12, TestRealFacts:**
```
zfs create -V 1048576 G/one
zfs create -V 1048576 -o copies=2 G/two
refreservation(G/two) == refreservation(G/one) != 0     # both 3145728 on a single-vdev pool (Sim: 1M + 8 dnodes*2*128K)
```
Compare the two values for equality rather than hardcoding the number, so the row survives a raidz pool. Put a comment on the row that cites the lookup and the conversion. It passes on the Sim today (no simDiverges).

---

## CI cost (ADR 0005: one-minute budget)

TestRealFacts runs its rows in sequence, so they add straight to its shard. In run 40513522394, the guest timings were 0.22 to 0.77 s per row (OWN-12/zvol 0.22, VOL-11 0.44, DEL-05 0.74); TestRealFacts took 9.51 s in total, against a weight of 8.35.

| addition | estimate |
|---|---|
| SPC-01 | 0.6 s |
| SPC-02 | 0.35 s |
| MNT-01 | 0.6 s |
| MNT-02 | 0.7 s |
| VOL-12 | 0.5 s (two zvols) |
| **TestRealFacts total** | **+2.75 s** (8.35 → ~11.1) |
| TestFileExportStaleShareOnlySharenfsHeals | ~0.8 s (create with share, 2× exportfs, 3 cheap sets) |
| corpus sequences | 0 on PR (nightly full replay only) |

Shard plan, with `real-zfs-guest.sh`'s own awk run over `go test -list` and the weights:
- **Today:** three shards at 24.79 / 24.80 / 24.80 weight-s. TestRealFacts is on shard 2, the NFS group (9.91) on shard 1.
- **After** (TestRealFacts 11.10, new NFS test 0.80): 25.98 on each shard. TestRealFacts moves to shard 1, the NFS group to shard 2.

That is **+1.2 weight-s per shard**. Measured shard wall time was 15.6 / 22.2 / 22.5 s, about 0.9 of weight, so expect **about +1 s** on the endpoint job's critical path. That fits the minute, but it eats into the ci-45 plan (target ≤ 45 s, short by about 3 s without B3). Options:
- merge SPC-02 into SPC-01 (−0.2 s);
- leave VOL-12 out (−0.5 s): the disagreement is settled, but the coordinator asked for it;
- after landing, regenerate `ci/real-zfs-weights.txt` with `ci/real-zfs-weights.sh` from the next run, so the new test's 0.1 default does not skew the balance.

`ci/check-skips.sh` and `ci/expected-skips.txt` need no edits: the new rows do not skip on the Sim or the real pool, and the new NFS test skips only without nfsd, as its siblings do.

## Landing checklist

1. Item 1: Sim, model, SPC-01, SPC-02, the two corpus files. Run `go test ./internal/zfs/... ./internal/endpoint/`.
2. The changelist rewrite: MNT-01, MNT-02, the stale-share test.
3. VOL-12.
4. Real pool (CI): the five rows and the NFS test pass. TestZFSDiffReplay nightly shows no divergence on the two new sequences.
5. Weights refresh. Mark wave16 gaps 1-4 resolved and 5 withdrawn; 6 remains deferred.
