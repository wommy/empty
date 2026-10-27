# zfs-endpoint

A daemon that owns volume and snapshot lifecycle on one ZFS parent dataset and serves it over gRPC, so CSI drivers and other front ends stay thin translators. OpenZFS is the source of truth (ADR 0003).

Each term names the file that defines it.

## Datasets

**Endpoint**:
The daemon serving one parent: `zfs-endpoint` (`cmd/zfs-endpoint`) around the `Endpoint` type (`internal/endpoint/endpoint.go`). The CSI controller (`zfs-csi`) is a front end of it, not part of it.
_Avoid_: server, driver

**Parent**:
The one ZFS dataset an endpoint serves (`Config.Parent`, e.g. `tank/k8s/vols`); volumes are its direct children (`internal/endpoint/endpoint.go`).
_Avoid_: pool, root dataset

**Volume**:
A direct child of the parent, a filesystem or a zvol (`VolumeKind`), named by its id: `<parent>/<id>` (`internal/endpoint/endpoint.go`).
_Avoid_: dataset (for an owned one), PV

**Snapshot**:
A CSI snapshot, `<volume id>@<name>`, which the endpoint lists and deletes (`Row.OwnedSnapshot`, `internal/endpoint/table.go`). Other snapshots of a volume are foreign snapshots.
_Avoid_: backup

**Clone source**:
The internal snapshot `<src>@volume-source-for-volume-<clone>` a volume-to-volume clone hangs off, stamped at creation with `managed_resource` and `clone_source_for=<clone>` (`Row.OwnedCloneSource`, `internal/endpoint/table.go`). The reserved name alone is not one.
_Avoid_: origin snapshot (for the endpoint's own)

## Ownership

**Owned**:
What the endpoint may act on, read only from its marker properties set locally: a volume whose `managed_resource` and `csi_volume_name` = its id are local (`Row.OwnedVolume`), a snapshot or clone source likewise (`internal/endpoint/table.go`). Received or inherited markers are not ownership. A served volume is owned and committed (`Row.ServedVolume`).
_Avoid_: managed (alone), adopted

**Marker properties**:
The user properties under the property prefix that record what the endpoint made: `managed_resource`, `provision_success`, `committed_at`, `csi_volume_name`, `csi_volume_content_source_*`, `csi_snapshot_name`, `csi_snapshot_source_volume_id`, `clone_source_for`, `nfs_server` (`props`, `internal/endpoint/endpoint.go`).
_Avoid_: tags, labels, metadata

**Property prefix**:
The user-property namespace of the marker properties, `-property-prefix`: `zfs-endpoint` by default, `democratic-csi` in adoption mode (`DefaultPropertyPrefix`, `internal/endpoint/endpoint.go`).
_Avoid_: namespace (alone)

## Planning

**Plan**:
The ordered ops one request needs, computed by a pure function from a scanned `Table` and the request; an empty plan means the request is already satisfied (`Plan`, `internal/endpoint/plan.go`).
_Avoid_: transaction, job

**Op**:
One planned mutation as plain data, which states how a replay tolerates finding its work already done or gone (`Op`, `OpKind`, `internal/endpoint/plan.go`).
_Avoid_: step, command

**Apply**:
Running a plan's ops in order against zfs (`Endpoint.apply`, `internal/endpoint/apply.go`).
_Avoid_: execute, commit (for this)

**Commit**:
The last op of a create: `provision_success=true` and `committed_at`, set only once the volume is complete (and, for an NFS volume, served). A volume without it is uncommitted (`PlanCreateVolume`, `internal/endpoint/plan.go`).
_Avoid_: finalize, mark done

**Resume**:
A CreateVolume pass over an uncommitted volume that drives it toward the request rather than judging compatibility, ending in the commit (`PlanCreateVolume`, `internal/endpoint/plan.go`; `CreateVolume`, `internal/endpoint/endpoint.go`).
_Avoid_: retry (for the planner's branch), repair

**Crash point**:
A mutation of an operation (a zfs call, or the root owner change) before which, or after which but before its answer, the process dies; a fresh endpoint's retry must then converge (`internal/endpoint/crash_test.go`).
_Avoid_: failure point, kill point

## Reconcile and export

**Reconcile**:
The on-demand RPC that collects what the endpoint owns but no longer needs (uncommitted rows, orphan clone sources, volumes outside a caller's desired set) and reports drift it never fixes, in `skipped` (`internal/endpoint/reconcile.go`).
_Avoid_: GC loop, sync, controller loop

**Export**:
An NFS export of a filesystem volume: its local `sharenfs` in the endpoint's grammar (masked IPv4 networks with `/N`, `rw=`/`ro=`), verified against the kernel's etab and running nfsd. Any other local value is foreign (`internal/endpoint/export.go`; ADR 0004).
_Avoid_: share, publish (for the state)
