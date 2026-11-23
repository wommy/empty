# Iterators, indexes, concurrency (Go 1.24)

## FINDINGS

**Iterators**
- Range-over-func (Go 1.23): iteration stops when yield returns false (`src/iter/iter.go:11-14`). Rationale: go.dev/blog/range-functions.
- Stdlib adoption, present in go1.24.0: `slices.Values/Collect/Sorted` (`src/slices/iter.go:37,57,63`), `maps.Keys/Values/Collect` (`src/maps/iter.go:25,38,58`), `strings.Lines` (`src/strings/iter.go:18`).
- Current `Volumes()`, `SnapshotsOf()`, `ClonesOf()` build slices by full scan (`internal/endpoint/table.go:71,82,93`).
- Streaming `zfs get` is unsafe here. `runner.go:38-50` buffers all stdout before exit status is known, and `parseGet` returns a complete map (`internal/zfs/zfs.go:163`). Yielding rows early could give the planner a truncated table, and `PlanDeleteVolume` would then miss a clone (`plan.go:262-270`) and allow an unsafe destroy.
- Pitfalls: early break must still reap the child process; yield after false panics; `defer` in an iterator runs on return, not per element; partial rows plus an error need `iter.Seq2[Row, error]` or a result struct.

**Indexing**
- `Table` keeps a sorted slice plus `idx map[string]int` (`table.go:76-79,104-110`). `NewTable` sorts the caller's slice in place (`:105`).
- Every ID with prefix `vol@` is contiguous in sorted order, so `SnapshotsOf` can be a `slices.BinarySearchFunc` range (`src/slices/sort.go:152`).
- `ClonesOf` runs inside a loop over snapshots (`plan.go:262-268`), so it is O(volumes x snapshots x rows).

**Concurrency**
- Current: `lock.Keyed` with sorted key acquisition and deadline-aware waits (`internal/lock/lock.go`, `Acquire`), called via `Endpoint.lock` (`endpoint.go:170-187`). Planners are pure; the table is rebuilt per request.
- etcd: `EtcdServer.run` owns the loop (`server/etcdserver/server.go:751`). Requests wait with `select` on a result, `ctx.Done()`, and `s.stopping` (`server.go:1760-1785`).
- tailscale: eventbus uses workers with explicit Stop/Wait (`util/eventbus/bus.go:269-304`), while `wgengine/userspace.go` uses `e.mu.Lock()` widely (`:423`, `:834`).

## RECOMMENDATION

1. Keep `lock.Keyed`. Do not add an owner goroutine: the authoritative state is in ZFS, not in process memory, so an owner would only serialize multi-second execs.
2. In `NewTable`, build `snapsByVol map[string][]int` (key = part before `@`) and `clonesByOrigin map[string][]int`. Make `Table` immutable after construction.
3. Drop `idx`. `Get` uses `slices.BinarySearchFunc` on `Rows` by `ID`. `SnapshotsOf` and `ClonesOf` return `iter.Seq[Row]` over the indexes.
4. `Volumes()` returns `iter.Seq[Row]`. Only `ListVolumes` (`endpoint.go:416-423`) needs a slice, via `slices.Collect`.
5. Copy the input slice in `NewTable` before sorting.
6. Keep whole-output parsing for `zfs get`.

## RISKS
- API change: `iter.Seq` return types need `slices.Collect` at test call sites (`plan_test.go`).
- Index maintenance adds a second invariant; keep tests covering unsorted input.
- Performance claims are reasoning only; no benchmarks run.
- Unverified: whether `zfs get -Hp` output ordering allows grouping during streaming. `cmd/zfs/zfs_main.c:2054` (get_callback) suggests per-dataset output, not fully verified.
