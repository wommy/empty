# Adversarial verification: democratic-csi baseline claims B1-B6

Repo: /tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/repos/democratic-csi
Paths below are relative to that root. Read-only; nothing was executed from the clone.

## Verdicts (BROKEN first)

### B1 - BROKEN: DeleteVolume silently destroys CSI snapshots
- Guard exists. `src/driver/controller-zfs/index.js:1440-1469`:
  - `zb.zfs.list(datasetName, [..., MANAGED_PROPERTY_NAME], { types: ["snapshot"] })` then `hasManagedSnapshot = snapshots.indexed.some(... == "true")`.
  - `if (hasManagedSnapshot) throw new GrpcError(grpc.status.FAILED_PRECONDITION, "filesystem has dependent snapshots");`
- Native snapshots are stamped managed: `index.js:2240` `snapshotProperties[MANAGED_PROPERTY_NAME] = "true"`, applied at `index.js:2383-2384` via `zb.zfs.snapshot(..., { properties })`.
- Default is native, not detached: `index.js:2169-2176` parses `detachedSnapshots` with `JSON.parse(...)`. A missing parameter makes `JSON.parse(undefined)` throw, the empty catch swallows it, and `detachedSnapshot` stays `false`.
- FAILED_PRECONDITION "has snapshots" does exist (`index.js:1466-1469`).
- Real residual gap: `index.js:1479` `zb.zfs.destroy(datasetName, { recurse: true, force: true })` silently destroys unmanaged snapshots (manual or sanoid-made) that the guard ignores.
- Ordering issue: `deleteShare` (`index.js:1401`) and the origin defer-destroy (`index.js:1419`) run before the guard, so a refused delete still removes the share.

### B2 - BROKEN: ownership props are not a separate post-create step
- `volumeProperties` (`index.js:870-889`, including `MANAGED`, `CSI_NAME`, provisioner props) are passed as creation properties:
  - `zb.zfs.create(datasetName, { properties: volumeProperties })` at `index.js:1148-1150`.
  - `zb.zfs.clone(..., { properties: volumeProperties })` at `index.js:998-1000`.
  - Clone with `cloneProperties` at `index.js:1120-1122` (an alias of `volumeProperties`, so the `delete` calls mutate the same object).
- The wrapper turns these into `-o key=value`: `src/utils/zfs.js:931-935` (create) and `src/utils/zfs.js:1113-1116` (clone).
- Only later sets are separate: detached path `index.js:974` and `1097`, then share context `index.js:1296`, then SUCCESS `index.js:1308`.

### B3 - BROKEN: the success marker is write-only, not a resume gate
- Written last: `index.js:1308` (volume), `index.js:2446` (snapshot).
- Never read to gate anything: `SUCCESS_PROPERTY_NAME` only appears in the list field sets at `index.js:1731` and `1844`. `populateCsiVolumeFromData` (`index.js:351-416`) checks `MANAGED` and share context, not the marker.
- Retry on an existing dataset: CreateVolume does not early-return. It only checks size (`index.js:~795-830`), then runs create/clone.
- Why retry still "works": `src/utils/zfs.js:947` and `:1129` treat `dataset already exists` as success when `idempotent` is set, and the wrapper sets `options.idempotent = true` in `getZetabyte` (`src/driver/controller-zfs-generic/index.js:41-60`). Retries therefore re-run over the existing dataset, with no check for partial state.
- No cleanup or sweep of unmarked datasets exists.

### B4 - HOLDS: CreateVolume-from-snapshot does not lock the source snapshot
- `bin/democratic-csi:212-226` acquires `lockKeysFromRequest(call, serviceMethodName)` keys.
- `src/utils/general.js:68-69`: `case "CreateVolume": return ["create_volume_name_" + call.request.name];`. No snapshot or source key.
- `src/utils/general.js:78-79`: `case "DeleteSnapshot": return ["snapshot_id_" + call.request.snapshot_id];`. No overlap, so the race is real.
- Consequence (inference, not in repo): for native snapshots the delete is deferred and the clone likely fails with NOT_FOUND. The detached path (send/receive from a snapshot that DeleteSnapshot recurse-destroys) is the riskier case.

### B5 - HOLDS, with caveats: ID forms and no hashing
- Native snapshot: `index.js:2315` `fullSnapshotName = datasetName + "@" + name`, where `datasetName = parent + "/" + source_volume_id`. Returned id is the name with the parent prefix stripped (`index.js:2460-2463`), giving `<volume_id>@<snapname>`.
- Detached snapshot: `index.js:2313` `datasetName + "/" + name`, giving `<volume_id>/<name>`, not `@`. The claim's `@` form covers native only.
- Volume ID equals the dataset leaf: `index.js:791` `datasetName = parent + "/" + volume_id`, and `populateCsiVolumeFromData` returns `volume_id = row.name` minus the prefix (`index.js:416`).
- No hashing. The only transform is an optional Handlebars `idTemplate` (`src/driver/index.js:459-468`). It changes `volume_id` before it is used for the dataset name, so the dataset/ID relation still holds, but the ID need not match the PV name.

### B6 - PARTIAL: DeleteSnapshot deferral and clone behavior
- Native uses deferred destroy: `index.js:2516-2520`, `defer: zb.helpers.isZfsSnapshot(snapshot_id)`.
- Detached does not defer, and is destroyed with `recurse`.
- Clone handling: the `"snapshot has dependent clones"` to FAILED_PRECONDITION mapping (`index.js:2521-2527`) is reachable only for the non-deferred detached case. Native deferred destroy with clones does not error here. The snapshot persists until its clones are gone (ZFS `-d` semantics, inferred, not in repo), and the RPC still returns success.
- `destroy` with `dataset does not exist` / `could not find any snapshots` is treated as idempotent (`src/utils/zfs.js:~1000`).
