# DOD round 3: an adversarial review of the existing scan → Table → plan pipeline

## Setup

**Repo states**
- Most benchmarks ran on a copy of `1b9270b`, which has 27 columns and a 392 B `Row`.
- The repo moved during the review to `bdf89d0` (block-export work, plus the newline fix in `parseColumns`). That version has 30 columns and a 432 B `Row`. The headline baseline was re-measured on a copy of `bdf89d0` (`dod3/head`).
- Line numbers below are for `bdf89d0` unless marked otherwise.

**OpenZFS citations** are from `scratchpad/repos/openzfs` at `a128b8a`.

**Machine:** 4 vCPU Xeon @ 2.8 GHz, 15 GiB RAM, `GOTOOLCHAIN=go1.27.3`. There is no `zfs` binary on the host:
- Every number below is Go-side cost only, or a real pipe fed by `cat`.
- Kernel and libzfs cost comes on top of it and is not measured here.

**Fixture**
- Generator: `dod3/copy/internal/endpoint/dod3_gen_test.go`.
- It emits `zfs get -Hp -o name,property,value,source` in OpenZFS's own order (cited in F5).
- Content: pvc-UUID names, real sources (`local`, `default`, `inherited from …`), and user properties inherited onto snapshots.
- Sizes: "110k" is 10k volumes × 10 snapshots plus the parent (110,001 datasets); "10k" is 1k × 9.
- Output is 331 MiB at 27 columns and 363.5 MiB at 30 columns. That matches the scout's 322 MiB.

**Code and data**
- Bench code: `dod3/copy/internal/endpoint/dod3_*_test.go`, `dod3/copy/internal/zfs/dod3_parse_test.go`, `dod3/head/internal/endpoint/dod3_head_test.go`.
- Raw outputs: `dod3/bench_*.txt`.
- The prototype edits in the copy are `simulateDestroyAll` in `reconcile.go` and a subtree-only `DeleteVolume` in `endpoint.go`. `go test ./internal/...` passes with both.

## Baseline (full-parent `scan(ctx, "")`, bytes already produced by zfs)

| state | size | ns/op | B/op | allocs/op |
|---|---|---|---|---|
| 1b9270b (27 cols) | 10k | 206–249 ms | 166 MB | 275k |
| 1b9270b | 110k | 2.29–3.52 s | 2.20 GB | 3.02M |
| **bdf89d0 (30 cols)** | 10k | 240–250 ms | 252 MB | 305k |
| **bdf89d0** | 110k | **2.43–2.58 s** | **2.28 GB** | **3.35M** |
| 1b9270b, real pipe (`LocalRunner` + `cat`) | 110k | 3.64–3.90 s | 2.20 GB | 3.02M |

Stage breakdown at 110k (1b9270b, `BenchmarkScanStages`):

| stage | time | allocated | allocs |
|---|---|---|---|
| `bytes.Buffer` growth in `LocalRunner` | 0.48–1.12 s | 1.07 GB | 22 |
| `parseColumns` | 1.64–1.70 s | 1.08 GB | 2.97M (one per line: the `SplitN` slice) |
| `decodeColumns` | 84 ms | 43 MB | 4 |
| `NewTable` | 248–301 ms, of which the sort is 219–262 ms | 4 MB | 50k (index maps) |

Peak live Go heap for one 110k scan is **1.23 GiB**, without counting zfs's own memory.

The scout's 1.0 GiB allocation figure left out the `bytes.Buffer` doubling in `runner.go:40`.

---

## Findings, ranked

### F1 — P0: `PlanReconcile` is quadratic; at 16k volumes one plan takes 31 s, and the second plan runs while the locks are held

**Claim**
- `simulateDestroy` (`reconcile.go:256`) runs once per planned volume (`reconcile.go:185-187`).
- Each run is a full `slices.DeleteFunc` over every row, and builds `op.Target+"@"` and `op.Target+"/"` per row (`:273`), which is one allocation per row.
- `reap` → `clones` adds another O(rows) pass for each origin.
- Total cost is O(volumes × rows).
- `Reconcile` plans twice (`reconcile.go:326`, `:351`). The second plan runs **after** `e.lock(...)`, so every targeted volume's lock is held for the whole quadratic plan. CreateVolume and DeleteVolume calls on those volumes return `Aborted` meanwhile.

**Evidence** (`BenchmarkPlanReconcile`: committed old volumes, half desired):

| vols | before | after (`simulateDestroyAll`) |
|---|---|---|
| 1,000 | 70 ms, 49.9 MB, 752k allocs | 3.6 ms, 2.0 MB, 1.6k allocs |
| 4,000 | 1.54 s, 777 MB, 12.0M allocs | 15.0 ms, 9.1 MB, 6.2k allocs |
| 16,000 | **31.0 s, 12.3 GB, 192M allocs** | **86 ms, 46.7 MB, 24k allocs** (360×) |

**Change** (prototype in `dod3/copy/internal/endpoint/reconcile.go`): apply all of a round's plans in one pass.

```go
- for _, plan := range plans { rows = simulateDestroy(rows, plan) }
+ rows = simulateDestroyAll(rows, plans)
// simulateDestroyAll: sets exact/recursive/defer targets; one DeleteFunc that
// checks each '@' or '/' prefix of r.Name against the recursive set (map lookup
// with a substring key, no allocation); collect reap candidates (defer targets
// plus origins of removed rows); count remaining clones per origin in one pass;
// drop candidates that are deferred and have zero clones.
```

**Why it is exact:** destroys only remove rows, so a deferred origin with no clones at the end of the round is reaped by the sequential version too, once its last clone goes, and vice versa. The reap candidates are the same set.

**Verification**
- `TestDod3SimulateDestroyAllEquivalent`: 20,000 random tables, deep-equal to the sequential version.
- `FuzzReconcileSafe` ran 60 s (61,845 execs) with the change wired in.
- All existing tests pass.

**Risk:** low. The sequential function stays as the test oracle.

### F2 — P0: `DeleteVolume` reads the whole parent to repeat a check that OpenZFS already makes before destroying anything

**Claim**
- `DeleteVolume` scans `""` (`endpoint.go:365`) only so that `PlanDeleteVolume` can find dependent clones (`plan.go`, `ClonesOf`).
- `zfs destroy -r` already refuses in that case, and it refuses before it destroys anything:
  - `cmd/zfs/zfs_main.c:1416-1466` `destroy_check_dependent` sets `cb_error` for any dependent outside the target's own subtree.
  - `:1896-1905` returns before `destroy_callback` or `zfs_destroy_snaps_nvl` run.
  - The stderr "has dependent clones" is already classified as `KindHasClones` (`errors.go:111`), which maps to `FailedPrecondition` (`endpoint.go` `zfsErr`). That is the same code the planner returns.
- The full scan also gives a *weaker* check than OpenZFS: a clone outside the parent is invisible to the table, while zfs still sees it.
- The CSI-snapshot refusal and the clone-source release only need the volume's own subtree.

**Evidence**
- Go-side cost at 110k: full scan 2.43–2.58 s, 2.28 GB, 3.35M allocs. Subtree scan (1 volume, 10 snapshots): **0.12–0.27 ms, 0.24 MB, 373 allocs** (`BenchmarkScanNarrow/110k/subtree`).
- Stdout shrinks from 363 MiB to about 1 KiB.
- zfs side: about 110k fewer handles opened (`zfs_ioctl.c` per-snapshot objset open, per `dod/read-path.md`).
- With `scan(ctx, volID)` in `dod3/copy/internal/endpoint/endpoint.go`, `go test ./internal/...` passes. That includes `TestCloneFromVolumeReleasesInternalSnapshot`, which asserts `FailedPrecondition` for a source volume that has a clone (`endpoint_test.go:269`). The simulator emulates the same refusal (`zfstest/sim.go:347-354`).

**Change**

```go
 // DeleteVolume
-	t, err := e.scan(ctx, "")
+	t, err := e.scan(ctx, volID) // dependents anywhere: zfs destroy -r refuses (destroy_check_dependent)
```

If the planner should keep its own refusal message, fetch the snapshot's `clones` property (`zfs_prop.c:580`, a ZFS_TYPE_SNAPSHOT read-only property) in the subtree scan. That is the OpenZFS-native reverse index, and it also sees clones outside the parent.

**Risk**
- Error text becomes zfs's stderr rather than "has dependent clone X".
- If one parent volume is missing, `scan(only)` adds one `Exists(parent)` exec.
- Confirm on the real pool CI (`ZFS_TEST_POOL`). That was not possible here.

### F3 — P1: no limit on concurrent full scans; four at once peak at 6.4 GiB and take 34.6 s

**Claim:** `ListVolumes`, `ListSnapshots`, `CreateSnapshot`, `DeleteVolume` and `Reconcile` each run their own full scan. Nothing de-duplicates or bounds them.

**Evidence** (`TestDod3PeakHeap`, 110k, 1b9270b):

| | N=1 | N=4 concurrent |
|---|---|---|
| current: peak live heap / wall time | 1.23 GiB / 3.9 s | **6.43 GiB / 34.6 s** (GC-bound on 4 cores) |
| streaming decoder (F4) | 236 MiB / 0.51 s | 631 MiB / 0.72 s |

On this 15 GiB host, about ten concurrent List calls would OOM the daemon, before counting the `zfs` processes, each of which holds its own handles.

**Change:** a single-flight key per (root, column set, type set), plus a semaphore of 1–2 on full-parent scans. This needs no new dependency: an `x/sync/singleflight`-style map under a mutex in `Endpoint`. With single flight, N concurrent list calls cost the N=1 numbers.

**Risk:** a waiter receives a scan that started before its own call. That is acceptable for List* (see F7) and never acceptable for a mutation. Mutations must not join a shared flight that started before they took their locks.

### F4 — P1: the parse path still allocates once per line and copies stdout twice; the "flat buffer" is not flat

**Claim**
- `parseColumns` (`zfs.go:429`, `:433`) does `string(out)` (a 331–363 MiB copy) and `strings.Split` (a 2.97M-entry `[]string`, about 47 MB).
- It also does one `strings.SplitN` per line (a 3-element slice for each of 2.97M lines).
- `LocalRunner` grows a `bytes.Buffer` by doubling (`runner.go:40`): 1.07 GB allocated for 331 MiB.

**Evidence, minimal diff** (`parseColumnsLean`, `dod3/copy/internal/zfs/dod3_parse_test.go`, output deep-equal to `parseColumns`):

| 110k | ns/op | B/op | allocs/op |
|---|---|---|---|
| parseColumns | 1.64–2.47 s | 1.08 GB | 2,970,697 |
| lean | **0.43–0.44 s** | **104 MB** | **586** |
| (10k) parseColumns → lean | 122–145 → 39–41 ms | 94.8 → 9.7 MB | 270k → 85 |

The lean version:
- scans with `IndexByte` and does not split;
- uses `unsafe.String` over `out`, which the Runner hands over and nothing writes again;
- sizes `Cells` from a count of `'\n'`.

**Evidence, full change** (`scanStream`, `dod3_stream_test.go`): a streaming, positional decoder that reads `cmd.StdoutPipe()` directly into `Row`s. Retained strings come from a 256 KiB-chunk arena. It is deep-equal to `scan` on the fixture (`TestDod3StreamEquivalent`).

| 110k, real pipe (`cat`) | ns/op | B/op | allocs/op | peak heap |
|---|---|---|---|---|
| current (LocalRunner + ListColumns + decode + NewTable) | 3.64–3.90 s | 2.20 GB | 3.02M | 1.23 GiB |
| stream + run-sort (F8) | **0.71–0.83 s** | **249 MB** | **150k** | **236 MiB** |
| (in-memory 10k) current → stream | 206–249 → 45–52 ms | 166 → 21.5 MB | 275k → 5k | |

**Safety:** this does not reopen the "partial table" hazard that `dod-go/iterators-indexes-concurrency.md` raises. Rows are returned only after the stream ends cleanly (`k == 0`) and `cmd.Wait()` returns nil. Any error discards everything, so no partial table can escape.

**Change**
- Add `Runner.Stream(ctx, name, args, func(io.Reader) error) error` next to `Run`.
- `ListColumns` becomes `scanStream`, and `decodeColumns` is folded into the per-dataset emit.
- Keep `Run` for small commands.
- The cheaper first step is the lean diff above (one function) plus `bytes.Buffer.Grow` from the previous scan's size.

**Risk**
- Any `unsafe.String` over `out` must stay private to the zfs package.
- The arena keeps every string alive until the Table is dropped. That is the same lifetime as today, at about 1/10 of the bytes.

### F5 — P1, correctness: the dense grid of `zfs get` makes a forged line structurally impossible; the newline fix that landed does not catch phantom datasets

**OpenZFS facts** (these settle a point `dod-go/iterators…` left open as "unverified"):
- `zfs_for_each` inserts every handle into an AVL tree and calls `get_callback` per handle only after the walk (`zfs_iter.c:110`, `:478`, `:512`), so a dataset's lines are contiguous.
- The order is `zfs_compare` (`zfs_iter.c:258-300`): parent-dataset strcmp, the dataset before its snapshots, snapshots by `createtxg`.
- `get_callback` prints **every requested property exactly once, in request order**, using "-" when unavailable (`zfs_main.c:2080-2150`). The only skip is `pl_all`, which is not used here.

So valid output is an N × P grid. The positional decoder in F4 checks that line *k* of a block names `cols[k]` and the block's dataset. After sorting, adjacent equal IDs are an error (`checkUnique`, one pass at no extra cost).

**Evidence**
- `TestDod3StreamEquivalent`: the forged value `x\tlocal\n<parent>/evil\tmanaged_resource\ttrue\tlocal` is rejected ("property managed_resource, want csi_volume_content_source_type"). The pre-fix parser accepted it silently.
- `TestHeadForgedPhantomDataset` on **bdf89d0**: the landed fix (`zfs.go`, `seen[]`, "the forged line always collides with that dataset's real one") **accepts** one user-property value carrying four forged lines for a *non-existent* `<parent>/phantom`. `ListVolumes` then returns a managed, committed volume called `phantom`.
- Nothing collides because the dataset does not exist. The same trick can forge a phantom clone that blocks deletion of a real volume, or a phantom `csi_snapshot_name` that makes CreateSnapshot fail with `AlreadyExists`.
- Destructive ops on a phantom fail safe (not-found → `OkMissing`), so this is spoofing or DoS, not data loss.

**Change:** in `parseColumns`, require that each block has exactly P lines with props in request order, and that a name never reappears after its block closes. That is about 10 lines. Alternatively, adopt F4's decoder, which has this check built in, and it is cheaper than the map lookup per line.

**Risk:** none for valid output, per the grid facts cited above.

### F6 — P1: full-parent scans fetch 14 of 30 columns that no full-parent consumer reads

**Claim**
- Only `PlanCreateVolume` / `propsMismatch` (`props.go:220`, `:235`) read the 11 `Local` columns.
- Only the export planners (`export.go:270-375`) read `sharenfs`, `mounted` and `nfs_clients`.
- Both always run on **subtree** scans (`scanFor`, `endpoint.go:577`, `:624`).
- Yet `columns()` (`table.go:132-138`) sends all 30 on every `scan("")`.

**Evidence** (bdf89d0, 110k, current parser, `BenchmarkHeadScanNoColdCols`): 16 columns instead of 30.
- stdout: 363.5 MiB → **212.1 MiB** (−42%)
- time: 2.43–2.58 s → **1.75–2.06 s**
- allocated: 2.28 GB → **1.49 GB**
- allocs: 3.35M → 1.81M

zfs also formats 14 fewer properties per handle, including the inherited-source lookups for `compression`, `xattr` and `acltype`. That part was not measured.

**Narrowing further, per operation** (stdout at 110k; Go time with the F4 decoder):

| operation | needs | stdout | stream time |
|---|---|---|---|
| ListVolumes | `-t filesystem,volume`, 9 cols | 8.2 MiB | 26 ms |
| ListSnapshots | 7 cols | 81.9 MiB | 0.35–0.43 s |
| CreateSnapshot global-name check | `-t snapshot`, 3 cols (type, defer_destroy, snapshot_name) | 34.7 MiB | 0.27–0.29 s |
| DeleteVolume / Reconcile | 7 cols | 90.6 MiB | 0.35–0.37 s |

ListVolumes also drops snapshot iteration (100k of the 110k handles) on the zfs side.

**Change**

```go
-func (n props) columns() []string { ... all 30 ... }
+func (n props) columns(full bool) []string // full: + propNames + NFS cols
 // scan: Props: e.names.columns(only != "")
```

`decodeColumns` must also tolerate an absent column. Today `c.Col` returns -1, and `c.Cell(i, -1)` reads the previous dataset's last cell (or panics at i=0), so `cell(-1)` must return `{"-","-"}`.

**Risk**
- A row from a full scan has empty `Local`/NFS fields. A future planner that reads them from a full-scan table would see "unset". Guard this with a `Table.full bool` assertion in those planners.

### F7 — P2: a Table cache for reads only, keyed by a generation counter, is safe; every mutation rescans

**Claim:** ListVolumes and ListSnapshots page by rescanning everything (`endpoint.go:441`, `:490`; `page()` slices afterwards).

**Measured cost:** paging 100k snapshots at `max_entries=100` is 1,000 full scans. At the measured 2.4–3.9 s each, that is **40–65 min of Go-side CPU** for one listing, plus zfs time.

**Foreign-writer analysis**
- OpenZFS has no dataset change notification (`dod/read-path.md` §2: zpool events carry no dataset create or set class).
- So a generation counter bumped by our own applies sees only our writes.
- An admin or sanoid can create or destroy datasets unseen.
- Sanoid's snapshots carry no locally set `csi_snapshot_name`, so they never appear in ListSnapshots anyway. Only out-of-band destroys and renames of managed datasets would show up stale.

**Safe variant**

```go
type cached struct{ t Table; gen uint64; at time.Time }
// apply(): e.inflight.Add(1); defer func(){ e.gen.Add(1); e.inflight.Add(-1) }()
// listTable(): g := e.gen.Load(); if c := e.cache.Load(); c != nil && c.gen == g &&
//     e.inflight.Load() == 0 && time.Since(c.at) < ttl { return c.t }
//   t := singleflight(scan(""));  if e.gen.Load() == g && inflight was 0 throughout { store }
```

- Use it **only** in ListVolumes, ListSnapshots and the pagination continuation, with a TTL of about 2 s, which bounds foreign staleness.
- Every mutation and every `Get` keeps its own fresh subtree scan under its lock: CreateVolume, Expand, DeleteSnapshot, Delete (F2), CreateSnapshot (narrow scan, F6), Reconcile (it already rescans under the locks).
- There is one table per (column set, type set). The F6 narrow scans make the cached table small (8.2 MiB of stdout for ListVolumes).

**Risk**
- For up to one TTL, a List can show a volume an admin just destroyed, or miss one an admin just created. CSI List* is advisory, so this is acceptable.
- The generation check must also cover applies that fail half-way: bump the generation in `defer`.
- A cache that mutations read is **not** proposed. It would turn foreign writers into a correctness bug, against ADR 0003.

### F8 — P2: sorting 110k fat rows costs about 230 ms; zfs order is already nearly ID order

**Evidence** (110k):

| sort variant | time |
|---|---|
| `slices.SortFunc` on `[]Row` (`table.go:76`) | 219–262 ms |
| int32 permutation | 79–85 ms |
| **`sortZFSOrder`** (sort each volume's snapshot run, then `IsSortedFunc`, falling back to a full sort) | **66–73 ms** |

- Only snapshots within a dataset are out of ID order: zfs orders them by `createtxg`, per `zfs_compare` (F5).
- A smaller row (224 B, with `Local` behind a pointer) does **not** speed the sort: 203–207 ms against 211–235 ms. The cost is in comparisons, not data movement.

**Change:** `NewTable` calls `sortZFSOrder`. Correctness never depends on zfs's order, because of the fallback.

**Risk:** none.

### F9 — P3: the secondary indexes are built on every scan; only Delete and Reconcile read them

**Evidence** (110k): building `snapsOfVol` and `clonesOfSrc` takes **33 ms, 4.2 MB and 51k allocs** per `NewTable` (`table.go:75-80`).

**Readers:** only `PlanDeleteVolume` (with F2 that becomes a subtree table of about 11 rows) and `PlanReconcile`. ListVolumes, ListSnapshots, CreateSnapshot, Get and Expand never read them.

**Alternative for `SnapshotsOf`:** a binary-search range over the sorted IDs costs 7.8 µs per query against 1.4 µs for the map. That saves 33 ms of build time for a few µs per query.

**Change:** `SnapshotsOf` becomes a range over `rows` sorted by `id+"@"`. `clonesOfSrc` is built lazily (`sync.OnceValue`) or only inside `PlanReconcile`.

**Risk:** low. `NewTable`'s contract is unchanged.

### F10 — P3: the hot/cold `Row` split saves about 40% of row-slice allocation; nothing else measured changes

**Sizes:** `Row` is 392 B at 1b9270b and **432 B at bdf89d0**. `LocalProps` alone is 176 B, read by one planner.

**Evidence:** appending 110k rows allocates 223 MB at 392 B and 133 MB at 224 B (`LocalProps` behind a pointer), with 150 ms against 77 ms of append time. The sort is unchanged (F8).

**Change:** `Local *LocalProps`, set only by subtree scans (this pairs with F6). Pre-grow `rows` by doubling (`slices.Grow(rows, len(rows))`): Go's 1.25× growth for large slices allocated about 5× the final 43 MB in the F4 stream.

**Risk:** a nil `Local` in a full-scan row. Use the same guard as F6.

---

## Prior claims these measurements falsify

| Claim | Where | Measured |
|---|---|---|
| "No struct-of-arrays: ~12 µs per 10k rows against ~1.5 ms per `zfs` exec. The real cost was a map per dataset, now one flat buffer" | README.md:60 | Exec of `/bin/true` here takes 1.7–5 ms, so the denominator holds. But the Go-side scan of 10k datasets costs **206–250 ms and 166–252 MB**: about 100× an exec, not 1/100. The "flat buffer" still allocates **one object per line** (`SplitN`, 2.97M at 110k) plus two full copies of stdout. Layout was never the issue; the per-line parse and buffering are (F4). The SoA verdict stands, for the wrong reason. |
| "Scan: one `zfs get -Hp` per call reads **the subtree**" | README.md:30 | DeleteVolume, CreateSnapshot, ListVolumes, ListSnapshots and Reconcile (twice) read the **whole parent**, all 30 columns (`endpoint.go:365,441,463,490`; `reconcile.go:322,347`). |
| "Row is 216 B … Decode dominates (map per dataset)" | dod-go/memory-layout.md §1–2 | Row is 392 B (1b9270b) and 432 B (bdf89d0). Decode is now 84 ms of a 2.3–3.9 s scan. Buffering (0.5–1.1 s) and line parsing (1.6–1.7 s) dominate. |
| "Change, cheap: remove the second full scan in CreateSnapshot" | dod-go/memory-layout.md §3 | The second scan is a **subtree** scan: 0.12–0.27 ms. The cost is the *first* scan, which reads 30 columns of every dataset to check one property on snapshots (F6: 3 columns, snapshots only). |
| "Streaming `zfs get` is unsafe here … could give the planner a truncated table" | dod-go/iterators-indexes-concurrency.md | Streaming *parse* with all-or-nothing return is safe. It is tested, and it is 5× faster with 9× less allocation (F4). The ordering it called "unverified" is verified in zfs_iter.c (F5). Caveat: zfs prints only after its full AVL walk, so streaming removes Go-side buffering but cannot overlap the kernel walk. |
| "`ClonesOf` … O(volumes × snapshots × rows)" | dod-go/iterators-indexes-concurrency.md | Stale: it is map-indexed now. The real quadratic is `simulateDestroy` in Reconcile (F1: 31 s at 16k volumes). |
| "Batching helps only scan cost, and scans are already per volume" | dod/reconcile-diff.md, Q2 | False for 5 of 11 call sites (above). |
| "The forged line always collides with that dataset's real one" | zfs.go comment in the landed newline fix (bdf89d0) | False for a non-existent dataset name. `TestHeadForgedPhantomDataset` creates a phantom managed, committed volume (F5). |

## What was not measured

- The kernel and libzfs side of every scan. There is no zfs here, so the zfs-side benefit of F2 and F6 is argued from source, not timed.
- Peak memory of the `zfs get` process itself. It holds every handle in an AVL tree before printing (`zfs_iter.c:110/512`), so it grows with dataset count. F6 narrows both the handles' properties (`cb_props_table`, `zfs_iter.c:441-470`) and the output.
