# Crash recovery, reconcile, orphan GC

## ANSWER
Both, with different authority. Startup runs a bounded, non-destructive reconcile that repairs only what is provably safe and reports the rest. On demand is the only path that deletes, and only against a desired set the caller supplies. Compare against ZFS user properties for what the endpoint created and completed. Do not delete from properties alone: they record what was made, not what is still wanted. Orphan = exists in ZFS AND (no property record OR absent from the caller's desired set).

A single-instance daemon has no in-flight operations at startup, so an uncompleted dataset then is a crash leftover, not a race. Still, pillar keeps CreatePartial (backend exists, export failed) for retry or a human, and reaps only Provisioning (github:isac322/pillar-csi internal/csi/volume_reap_test.go:398-408). Follow that split.

## EDGES
- Front end to endpoint: desired volume and export state over gRPC. ZFS and kernel configfs sit behind the endpoint.
- Crash windows: after zfs create before provision_success; after export before the success write; after snapshot before clone (truenas `csi-clone-*`, github:truenas/truenas-csi pkg/driver/snapshot_sweep.go:16).
- Leak point: zfs-localpv DestroyVolume returns nil when its clone-snapshot destroy fails, with "no need to reconcile" (github:openebs/zfs-localpv pkg/zfs/zfs_util.go:800-805).

## SEAMS
- Clone-snapshot sweep: truenas-csi, GPL-3.0, so take the idea only. pkg/driver/snapshot_sweep.go:55-82, started at pkg/driver/driver.go:602. It matches on a name prefix; deferred destroy makes it safe. Key on a user property instead.
- Desired-state push for exports: pillar-csi, Apache-2.0. The agent starts with export_restore_pending (proto/pillar_csi/agent/v1/agent.proto:686-688) and rejects export RPCs until the controller sends the complete desired set in one ReconcileState (internal/agent/server.go:118-120). Endpoint analogue: the front end sends the full desired set.
- Restart re-apply: zfs-localpv, Apache-2.0. SetVolumeProp re-applies all properties on restart (pkg/zfs/zfs_util.go:730-733). Idempotent convergence suffices for properties.
- Report-only health: external-health-monitor, Apache-2.0. The driver's report is authoritative and conditions clear when no longer reported (README.md:14-16). An absent volume maps to INACCESSIBLE/VolumeNotFound (pkg/mock/mock.go:31-41).
- Finalizer ownership: zfs-localpv docs/import-existing-volume.md:166-168. A property such as democratic-csi:managed should decide whether deleting an adopted volume destroys it.

## GAPS
- No project auto-sweeps snapshot-* orphans. truenas documents a manual check and `zfs destroy -d` (docs/snapshots.md:26-55). The endpoint can list by a clone-source property and report.
- No project deletes PVs whose backend is gone. external-health-monitor only reports. The endpoint should return a typed "missing" reason and leave PV deletion to the front end.
- pillar's export-without-record recovery is manual (site/src/content/docs/docs/how-to/recover-export-spec.md:18-23). The endpoint should report unrecorded exports, not remove them.
- linstor-csi and linstor-server: search found only per-request reconcile for snapshot restore (github:piraeusdatastore/linstor-csi pkg/client/linstor.go:1796-1806). No startup orphan sweep found. Unverified beyond search.
