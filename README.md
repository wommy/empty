# zfs-endpoint

A Go daemon that owns volume and snapshot lifecycle on one ZFS parent dataset and exposes it over gRPC (`api/zfsendpoint/v1`), so that CSI drivers and other front ends can stay thin translators.

**OpenZFS is the source of truth** (`docs/adr/0003-openzfs-is-the-source-of-truth.md`). Behavior follows OpenZFS's own semantics, as verified in its source and on a real 2.4.x pool, never another driver's choices. [democratic-csi](https://github.com/democratic-csi/democratic-csi) is an **adoption target**: by default the endpoint keeps its state under its own user-property namespace (`zfs-endpoint:*`), and `-property-prefix democratic-csi` is the **adoption mode**, which reads and writes that driver's properties so volumes it provisioned are adopted. democratic-csi is evidence of practice, not of correctness. **OpenZFS 2.4 is the only supported version** (`docs/adr/0004-support-floor-and-node-side.md`).

## Status

| Edge | State |
|---|---|
| ZFS execution (`internal/zfs`) | done: typed `zfs(8)` client, `-Hp` columnar parsing, error classification from OpenZFS's own CLI strings |
| Volume/snapshot lifecycle (`internal/endpoint`) | done: create (plain, from snapshot, from volume), delete, expand, get, list, snapshot create/delete/list, capacity |
| ZFS property passthrough (`internal/endpoint/props.go`) | done: default-deny per-kind allowlist with value checks; requested properties must be set locally for a retry to be compatible (`docs/research/gaps/adv-csi-frontend.md` B3) |
| Northbound API (`api/`, `internal/server`, `cmd/zfs-endpoint`) | done: gRPC over a unix socket, owner-only permissions |
| File export (NFS/SMB) | NFS done: per-client `PublishFileExport`/`UnpublishFileExport` (filesystems; an IPv4 address or IPv4 CIDR, always written as a masked CIDR with `/N`, no broader than `-nfs-min-prefix` (default /16), no overlapping networks on one volume; rw/ro), desired clients in a local `<prefix>:nfs_clients` property with `sharenfs` computed from it, each Publish verified against `/var/lib/nfs/etab` (at the mountpoint's real path) and running nfsd threads; root only (`docs/research/gaps/file-export.md` and its reviews). SMB not started |
| Block export (iSCSI/NVMe-oF) | design v2 done, not implemented: sole configfs owner, minted `block_id` identity, fail-closed build order (`docs/research/gaps/block-export-v2.md`) |
| CSI front end (`internal/csi`, `cmd/zfs-csi`) | controller + identity done; csi-sanity Controller/Identity groups run on the simulator and real ZFS. Node side not started |
| Transport auth | not started; both daemons' sockets are mode 0600 in a 0700 directory until then (`internal/unixsock`, `docs/research/edges2/transport-auth.md`) |

## Design

The core is data-oriented:

```
lock(keys) → scan → Table → plan(Table, request) → Plan → apply → re-read
             one zfs     typed,     pure function,     ops as     each op carries its
             get -Hp     immutable  no I/O             plain data own replay tolerance
```

- **Scan** (`table.go`): one `zfs get -Hp` per call reads the subtree into a flat columnar buffer. That is decoded once into an immutable, ID-sorted `Table` of typed `Row`s, with snapshot-by-volume and clone-by-origin indexes. Ownership is read only from properties with source `local`: user properties inherit, and `received` ones arrive with a `zfs send -p` copy. `Row.OwnedVolume` and `Row.OwnedSnapshot` are the only ownership tests. A table records whether it covers the whole parent or only the volumes it was read for, and planners that conclude from a missing row refuse a table that does not cover it.
- **Plan** (`plan.go`): pure functions take a `Table` and a request and return either a `Plan` (a `[]Op` of plain data) or a typed error.
  - Idempotency means the plan is empty.
  - A provision that was interrupted converges, because uncommitted rows are driven toward the request rather than judged for compatibility.
- **Apply** (`apply.go`): runs ops in order. Each op states whether "already exists" or "already gone" counts as done, so replaying a partially applied plan converges.
- **Locks** (`internal/lock`): every resource an operation touches is locked, including the source snapshot of a clone. A busy resource returns `ABORTED` one second before the caller's deadline, so CSI sidecars retry instead of timing out.

Errors use the CSI/gRPC codes directly:
- `ALREADY_EXISTS` for an incompatible re-create.
- OK when deleting something that is already missing, including an id the endpoint could never have issued. Other RPCs report such an id as `NOT_FOUND`.
- `FAILED_PRECONDITION` for deleting a volume that has snapshots or clones, or snapshots the endpoint did not make, and for "in use" (`dataset is busy`) on a destroy.
- `ABORTED` for a lock that is busy. It is also returned for an unclassified zfs failure after an earlier op of the plan applied, because a retry re-scans and converges. Before any change, that failure stays `INTERNAL`.
- `UNAVAILABLE` for zfs "busy" outside a destroy, a read-only or missing pool, or a missing parent.
- `DEADLINE_EXCEEDED` or `ABORTED` for a zfs command killed by the caller's deadline or cancellation. The outcome is unknown and a retry converges.

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
| Publish also requires nfsd threads (`/proc/fs/nfsd/threads` > 0) | stopping nfs-server empties etab, and the next `exportfs -ra` (any sharenfs change) rewrites it without nfsd, so etab alone reports a dead server as serving |
| NFS clients are IPv4 addresses or CIDRs, never hostnames | nfs-utils resolves a name on every `exportfs -ra` (any sharenfs change on any dataset, `zfs share -a`, an nfs-server reload) and silently drops one it cannot resolve; nothing re-adds it, so a DNS hiccup revoked access |
| Every client is written as a masked CIDR with `/N`, `/32` included | nfs-utils treats `/N` as a subnet: kept verbatim in etab, never resolved, never merged with hostname clients for the same address, so verification is an exact comparison (by normalized `netip.Prefix`) |
| Overlapping client networks on one volume are refused | nfs-utils applies the first matching line of a path, so `rw=10.0.0.0/24` and `ro=10.0.0.5/32` would not both hold |
| Unpublish of an unmounted volume uses `zfs set -u` | a plain `zfs set sharenfs` mounts a `canmount=on` filesystem and shares it (changelist postfix), undoing an operator's unmount. `-u` still changes the value, so the next mount shares the revoked list |
| A received `sharenfs` is refused, not overridden | it is the sending side's export decision; a local value would silently shadow it. `zfs set sharenfs=off` hands the volume to the endpoint |
| Ownership is a locally set user property; `received` is not ownership | ADR 0003. A `zfs send -p \| zfs recv` copy carries the source's markers as `received`. Counting them adopted copies and let reconcile destroy them |
| A volume's markers must name it (`csi_volume_name` = its id) | a renamed or received dataset carries another volume's name, so it is not this volume |
| Clone sources must be committed and owned; a clone is never `OkExists` | an existing dataset at the target is someone else's or a racing create's. Adopting it skipped the ownership check |
| DeleteVolume refuses snapshots the endpoint did not make, unless `-destroy-foreign-snapshots` | ADR 0003: `zfs destroy -r` took sanoid, replication and administrators' snapshots. The baseline did this |
| Default `-property-prefix zfs-endpoint`, validated at startup | ADR 0003, and OpenZFS `zfs_prop_user`: only `a-z 0-9 - _ . :`, and shorter than 256 bytes with the suffix. `democratic-csi` is the adoption mode |
| `xattr` only `sa`/`off`; `snapdir` `hidden`/`visible`/`disabled`; `copies` only on zvols | `xattr=on` is an alias zfs prints as `sa` (2.4 `xattr_table`), and it meant `dir` in 2.2. On a filesystem, extra copies count against `refquota`, which would silently shrink the volume |
| Busy is mapped by operation | CSI: "in use" on DeleteVolume/DeleteSnapshot is `FAILED_PRECONDITION`. On create, expand and clone it clears by itself, so it is retryable |
| A killed zfs command is `DEADLINE_EXCEEDED`/`ABORTED`, never `INTERNAL` | its outcome is unknown, and a final code would strand a half-applied plan. Commands get SIGTERM and then a 5 s `WaitDelay` |
| Never delete a non-socket at the socket path | a misconfigured `-csi-address` removed whatever file it named |
| No struct-of-arrays layout | measured: about 12 µs per 10k rows against about 1.5 ms per `zfs` exec. The real cost was a map per dataset, now one flat buffer |

### Corrections from adversarial review

These claims were made during the work and then disproved. They're kept here so they don't come back:
- **"The baseline destroys CSI snapshots on DeleteVolume."** False. It refuses with `FAILED_PRECONDITION`, and the endpoint matches it.
- **"The baseline sets ownership in a separate step."** False. It uses `zfs create -o`.
- **The baseline's success marker** is written but never read. Its retries rely on "already exists" being treated as success. The endpoint's resume-from-marker is therefore a real behavioural difference.
- **"An in-process lock race in the baseline."** The pattern exists in the code but did not reproduce under grpc-js, because handlers don't interleave across microtasks.
- **"No typed ZFS volume daemon exists."** Only partly true. TrueNAS middleware is a typed ZFS daemon over JSON-RPC, not gRPC.
- **"`Prop.Local()` is ownership."** It counted `received` as local, so a `zfs send -p` copy looked managed (ADR 0002, ADR 0003). It is now `local` only.
- **"`xattr=on` and `compression=gzip-6` read back as sent."** zfs prints the first table name for an index, so they read back as `sa` and `gzip`. The simulator now prints through OpenZFS 2.4's index tables, so read-back comparisons are tested against zfs, not an echo.
- **"A clone that already exists is done."** It skipped the ownership check and adopted a racing or foreign dataset.
- **"A missing parent makes every volume absent."** Only a whole-parent scan saw this. A missing parent is now `UNAVAILABLE` everywhere.

## Testing

```
go test -race ./...                                       # simulator
ZFS_TEST_POOL=testpool go test ./internal/endpoint/...    # real pool, as root
go test -run '^$' -fuzz FuzzCreateConverges ./internal/endpoint
go test -run '^$' -fuzz FuzzReconcileSafe ./internal/endpoint
go test -run '^$' -fuzz FuzzParseColumns ./internal/zfs
```

The contract tests in `internal/endpoint` run against `internal/zfs/zfstest` or, with `ZFS_TEST_POOL` set, against a real pool. CI does the latter on a file-backed pool. Planner tests build `Table`s directly from rows, with no I/O.

`FuzzCreateConverges` asserts the core invariant: for any accepted request, a second identical CreateVolume succeeds and changes nothing.

`FuzzReconcileSafe` builds arbitrary parents on the simulator, including received copies of owned volumes. It asserts that reconcile never targets a desired, unowned, snapshotted or young volume or a CSI snapshot, and that a second reconcile plans nothing.

The simulator injects failures (`Sim.Fail`) and received properties (`Sim.Receive`), and it prints index properties the way zfs 2.4 does.

## Open gaps

The research found these, and nothing ships for them yet:
- **Block exports, SMB** and node attach. For per-LUN detach, see Trident's `RemoveLUNFromSessions` and gobrick.
- **Reconcile/GC.** The on-demand half ships: the `Reconcile` RPC (`reconcile.go`) runs off the request path and collects uncommitted rows from abandoned creates, clone-source snapshots whose clone is gone, and owned volumes outside a desired set the caller supplies. It never touches unowned or received datasets, desired volumes, CSI snapshots, volumes with snapshots (CSI or foreign) or clones (reported as skipped), or anything younger than `min_age_seconds` (default one hour). An empty desired set collects no committed volume. `dry_run` returns the plan as data. Still open: a startup report-only pass, keying clone-source snapshots on a property rather than their reserved name prefix, and reporting unrecorded exports once exports exist.
- **HA exclusivity.** A channel-program fencing epoch was designed and then **dropped** after adversarial review: the epoch can go backwards, and creates, resizes and clones can't be fenced (`docs/research/gaps/adv-fencing.md`). Exclusivity is per host, with a `flock` per parent dataset, and across hosts it belongs to the HA layer (`zpool multihost` plus pacemaker/STONITH).
- **Changed-block tracking** (the CSI SnapshotMetadata service). No ZFS implementation exists anywhere, and the zfs send stream is the only exact source.
- **ControllerModifyVolume.** No ZFS driver implements it.
- **Detached snapshots,** which would let volumes be deleted while their snapshots survive.

## License

Apache-2.0. See `LICENSE`.
