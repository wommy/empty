# zfs-endpoint

A Go daemon that owns volume and snapshot lifecycle on one ZFS parent dataset and exposes it over gRPC (`api/zfsendpoint/v1`), so that CSI drivers and other front ends can stay thin translators.

**OpenZFS is the source of truth** (`docs/adr/0003-openzfs-is-the-source-of-truth.md`). Behavior follows OpenZFS's own semantics, as verified in its source and on a real 2.4.x pool, never another driver's choices. [democratic-csi](https://github.com/democratic-csi/democratic-csi) is an **adoption target**: the endpoint can read its ZFS user properties (`democratic-csi:*`), so volumes it provisioned can be adopted, but it is evidence of practice, not of correctness.

## Status

| Edge | State |
|---|---|
| ZFS execution (`internal/zfs`) | done: typed `zfs(8)` client, `-Hp` columnar parsing, error classification from OpenZFS's own CLI strings |
| Volume/snapshot lifecycle (`internal/endpoint`) | done: create (plain, from snapshot, from volume), delete, expand, get, list, snapshot create/delete/list, capacity |
| ZFS property passthrough (`internal/endpoint/props.go`) | done: default-deny per-kind allowlist with value checks; requested properties must be set locally for a retry to be compatible (`docs/research/gaps/adv-csi-frontend.md` B3) |
| Northbound API (`api/`, `internal/server`, `cmd/zfs-endpoint`) | done: gRPC over a unix socket, owner-only permissions |
| File export (NFS/SMB) | NFS done: per-client `PublishFileExport`/`UnpublishFileExport` (filesystems; hostname, IPv4 or IPv4 CIDR, rw/ro), desired clients in a local `<prefix>:nfs_clients` property with `sharenfs` computed from it, each change verified against `/var/lib/nfs/etab`; root only (`docs/research/gaps/file-export.md` and its two reviews). SMB not started |
| Block export (iSCSI/NVMe-oF) | not started; the design calls for LIO/nvmet configfs (`docs/research/edges/block-export.md`) |
| CSI front end (`internal/csi`, `cmd/zfs-csi`) | controller + identity done; csi-sanity Controller/Identity groups run on the simulator and real ZFS. Node side not started |
| Transport auth | not started; the socket is mode 0600 until then (`docs/research/edges2/transport-auth.md`) |

## Design

The core is data-oriented:

```
lock(keys) → scan → Table → plan(Table, request) → Plan → apply → re-read
             one zfs     typed,     pure function,     ops as     each op carries its
             get -Hp     immutable  no I/O             plain data own replay tolerance
```

- **Scan** (`table.go`): one `zfs get -Hp` per call reads the subtree into a flat columnar buffer. That is decoded once into an immutable, ID-sorted `Table` of typed `Row`s, with snapshot-by-volume and clone-by-origin indexes. Ownership is read only from *locally set* properties, because user properties inherit.
- **Plan** (`plan.go`): pure functions take a `Table` and a request and return either a `Plan` (a `[]Op` of plain data) or a typed error.
  - Idempotency means the plan is empty.
  - A provision that was interrupted converges, because uncommitted rows are driven toward the request rather than judged for compatibility.
- **Apply** (`apply.go`): runs ops in order. Each op states whether "already exists" or "already gone" counts as done, so replaying a partially applied plan converges.
- **Locks** (`internal/lock`): every resource an operation touches is locked, including the source snapshot of a clone. A busy resource returns `ABORTED` one second before the caller's deadline, so CSI sidecars retry instead of timing out.

Errors use the CSI/gRPC codes directly:
- `ALREADY_EXISTS` for an incompatible re-create.
- OK when deleting something that is already missing.
- `FAILED_PRECONDITION` for deleting a volume that has snapshots or clones.
- `ABORTED` for a resource that is busy.

## Decisions and their evidence

All research is in `docs/research/`. Delegates produced it from pinned clones and GitHits searches, and adversarial rounds then attacked it. `alignment.md` maps 495 operations across 13 implementations onto one vocabulary.

| Decision | Evidence |
|---|---|
| Only a *classified* not-found counts as absent | zfs-localpv treats any `zfs list` error as absent, so transient failures turn into duplicate creates or false delete successes |
| A missing pool is unavailable, never not-found | adversarial ZFS-CLI review: "no such pool" made a dead pool look like an absent dataset |
| Never hand-set a zvol's `refreservation` | ZFS's `zvol_auto_resv` includes metadata overhead and tracks volsize. Setting it by hand under-reserves and stops the tracking |
| Snapshot delete uses `destroy -d` | pyzfs: `lzc_destroy_snaps` with EEXIST means *SnapshotIsCloned*. truenas-csi #69 leaked snapshots because of this |
| Never use `zfs create -p` for volumes | it exits 0 on an existing dataset, which would stamp ownership onto someone else's |
| Request-scoped plans, not a reconcile queue | CSI CreateVolume is synchronous and the external-provisioner times out after 10s |
| GC only on demand, against a caller-supplied desired set | ZFS properties record what was made, not what is still wanted (`docs/research/edges2/reconcile-gc.md`) |
| Keep the `provision_success` marker | ZFS channel programs can't create datasets or set refquota/volsize, so a plan can't run as one atomic transaction |
| Fat-struct `Op`, not a sealed interface | a homogeneous `[]Op` is printable, diffable data for golden tests |
| Unexport sets `sharenfs=off` locally, never `zfs inherit` | `sharenfs` inherits: an ancestor shared to `*` would re-export the volume to every host |
| Exports are verified in the kernel's export table | libzfs discards the `exportfs` status, so `zfs set sharenfs=` exits 0 when the export failed (`docs/research/gaps/adv-file-export-facts.md` B1) |
| No struct-of-arrays layout | measured: about 12 µs per 10k rows against about 1.5 ms per `zfs` exec. The real cost was a map per dataset, now one flat buffer |

### Corrections from adversarial review

These claims were made during the work and then disproved. They're kept here so they don't come back:
- **"The baseline destroys CSI snapshots on DeleteVolume."** False. It refuses with `FAILED_PRECONDITION`, and the endpoint matches it.
- **"The baseline sets ownership in a separate step."** False. It uses `zfs create -o`.
- **The baseline's success marker** is written but never read. Its retries rely on "already exists" being treated as success. The endpoint's resume-from-marker is therefore a real behavioural difference.
- **"An in-process lock race in the baseline."** The pattern exists in the code but did not reproduce under grpc-js, because handlers don't interleave across microtasks.
- **"No typed ZFS volume daemon exists."** Only partly true. TrueNAS middleware is a typed ZFS daemon over JSON-RPC, not gRPC.

## Testing

```
go test -race ./...                                       # simulator
ZFS_TEST_POOL=testpool go test ./internal/endpoint/...    # real pool, as root
go test -run '^$' -fuzz FuzzCreateConverges ./internal/endpoint
go test -run '^$' -fuzz FuzzReconcileSafe ./internal/endpoint
```

The contract tests in `internal/endpoint` run against `internal/zfs/zfstest` or, with `ZFS_TEST_POOL` set, against a real pool. CI does the latter on a file-backed pool. Planner tests build `Table`s directly from rows, with no I/O.

`FuzzCreateConverges` asserts the core invariant: for any accepted request, a second identical CreateVolume succeeds and changes nothing.

`FuzzReconcileSafe` builds arbitrary parents on the simulator and asserts that reconcile never targets a desired, unowned, snapshotted or young volume or a CSI snapshot, and that a second reconcile plans nothing.

## Open gaps

The research found these, and nothing ships for them yet:
- **Block exports, SMB** and node attach. For per-LUN detach, see Trident's `RemoveLUNFromSessions` and gobrick.
- **Reconcile/GC.** The on-demand half ships: the `Reconcile` RPC (`reconcile.go`) runs off the request path and collects uncommitted rows from abandoned creates, clone-source snapshots whose clone is gone, and owned volumes outside a desired set the caller supplies. It never touches unowned datasets, desired volumes, CSI snapshots, snapshotted or cloned volumes (reported as skipped), or anything younger than `min_age_seconds` (default one hour). An empty desired set collects no committed volume. `dry_run` returns the plan as data. Still open: a startup report-only pass, keying clone-source snapshots on a property rather than their reserved name prefix, and reporting unrecorded exports once exports exist.
- **HA exclusivity.** A channel-program fencing epoch was designed and then **dropped** after adversarial review: the epoch can go backwards, and creates, resizes and clones can't be fenced (`docs/research/gaps/adv-fencing.md`). Exclusivity is per host, with a `flock` per parent dataset, and across hosts it belongs to the HA layer (`zpool multihost` plus pacemaker/STONITH).
- **Changed-block tracking** (the CSI SnapshotMetadata service). No ZFS implementation exists anywhere, and the zfs send stream is the only exact source.
- **ControllerModifyVolume.** No ZFS driver implements it.
- **Detached snapshots,** which would let volumes be deleted while their snapshots survive.
