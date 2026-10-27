# READ PATH: scan cost, change notification, recommendation

Repo cited: /home/user/empty (read-only). OpenZFS: scratchpad/repos/openzfs. TrueNAS: GitHits github:truenas/middleware@3ffe599a. Sanoid: GitHits github:jimsalterjrs/sanoid@d39b51a0.

## FINDINGS

**Current call path.** `scan` lists parent subtree with `zfs get -Hp -o name,property,value,source -t filesystem,volume,snapshot -d 2` (internal/zfs/zfs.go:340-348; endpoint.go scan, table.go `scan`) and requests 15 props incl. `type,volsize,refquota,creation,mountpoint` plus user props (table.go `columns`).

**1. Cost.**
- Kernel snapshot listing does one objset open per snapshot when not in simple mode: module/zfs/zfs_ioctl.c:2826-2834 (`zc_simple` branch skips `dmu_objset_from_ds`). Full stats are also gathered for datasets at zfs_ioctl.c:2516-2518.
- `zfs list` is only "simple" (fast) when every listed or sorted column is in {name, guid, createtxg, numclones, inconsistent, redacted, origin}: cmd/zfs/zfs_iter.c:209-251; flag set at cmd/zfs/zfs_main.c:4026-4032.
- Therefore `zfs list -o` with a fixed list is cheap only if the list stays in that set. `type`, `volsize`, `refquota`, `creation`, `mountpoint` and user properties are not in it, so the endpoint's column set always takes the expensive path.
- `zfs get` never sets ZFS_ITER_SIMPLE (cmd/zfs/zfs_main.c:2195-2198, 2431): always full cost.
- `-t snapshot -s name`: `name` is fast (zfs_iter.c:213), so sorting by name does not add cost. The `-t` filter does not reduce cost of the non-fast columns.
- Snapshot iteration is per parent: zfs_iter.c:113-147 and lib/libzfs/libzfs_iter.c:149-175 (`zfs_iter_snapshots_v2`). Cost is O(#datasets + #snapshots) per call, scaled by the full property fetch.
- OpenZFS perf issues / benchmarks: **unverified** (GitHits research timed out twice; not searched).

**2. Change notification.**
- zpool-events.8 (man/man8/zpool-events.8:50-216) documents pool/vdev/config/io/scrub classes only; no dataset create, destroy, or property-change class. Its option list is -c, -f, -H, -v (lines 50-58).
- The only dataset-related sysevent is ESC_ZFS_HISTORY_EVENT (include/sys/fs/zfs.h:2072). It is posted from internal history only (module/zfs/spa_history.c:187-232, 302-318) and is emitted once per dataset for destroy/snapshot/rename-style internal entries (dsl_destroy.c:347,794; dsl_dataset.c:1913,3202). User property `zfs set` is not documented as posting it.
- ZED (man/man8/zed.8.in:37-42) runs ZEDLETs per zevent class; it adds no dataset classes.
- `zpool history` (man/man8/zpool-history.8:30-39) offers -i/-l only; no cursor or since-txg option.
- TrueNAS: zfs_events.py:204-237 consumes ZfsHistoryEvent for destroy only and explicitly does not handle create/changed ("takes a toll ... we are now handling create/changed events whenever changes are made via our API"). Its caches: dataset_query_utils.py:543-553 (props cached per type), 931-933 (tier cache).
- Sanoid: TTL snapshot cache refreshed from `zfs get -Hrpt snapshot creation` (sanoid:903-913), `--force-update` to clear (sanoid:1941).

**3. Assessment.** Events cannot give a complete invalidation signal. Only destroy-class internal history is observable, and it is not documented as a stable interface. This favors a single writer holding a table.

## RECOMMENDATION

Choose **(b)**, with the minimum safe design:

1. `Endpoint` owns one `Table` per parent, held by a single owner goroutine (or a `sync.RWMutex`-guarded value replaced wholesale). Mutations are pure: `func (t Table) Apply(results []OpResult) Table` in table.go, replacing `NewTable` reuse.
2. Read-only RPCs (List*) serve from the table. Writes still run plan-on-current-table, and per-key locks (endpoint.go:172) still serialize ops on one volume.
3. Destructive or count-sensitive decisions (DeleteVolume, CreateSnapshot clone check) re-scan only their own subtree (`scan(ctx, vol)`, existing code path) under the per-key lock. This preserves current correctness for those ops.
4. Full re-verify on a timer and after any ZFS error that is NotFound/Exists/Busy (endpoint.go:189-210). Replace the table atomically.
5. Use the fast column set (name, origin, guid, createtxg) for the periodic full-tree existence check (zfs_iter.c:231-251), and full props only for volumes the table flags as changed.

Concrete changes: table.go gains `Apply` and a `verify` path; endpoint.go `scan(ctx, "")` call sites in List* switch to the cached table; `NewTable` stays for scans.

## RISKS

- Out-of-band writers (operators, TrueNAS, `zfs receive`) can change the subtree; the timer re-verify bounds staleness, not eliminates it.
- The owner goroutine becomes a serialization point; scans under it are long for large trees.
- Fast-column verify misses property-only drift (volsize, refquota, origin changes are partly fast; others are not).
- Unverified: OpenZFS perf issues and per-dataset timing numbers.
