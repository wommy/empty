# zfs-endpoint

A Go daemon that owns volume and snapshot lifecycle on one ZFS parent dataset and exposes it over gRPC (`api/zfsendpoint/v1`), so that CSI drivers and other front ends can stay thin translators.

**OpenZFS is the source of truth** (`docs/adr/0003-openzfs-is-the-source-of-truth.md`). Behavior follows OpenZFS's own semantics, as verified in its source and on a real 2.4.x pool, never another driver's choices. [democratic-csi](https://github.com/democratic-csi/democratic-csi) is an **adoption target**: by default the endpoint keeps its state under its own user-property namespace (`zfs-endpoint:*`), and `-property-prefix democratic-csi` is the **adoption mode**, which reads and writes that driver's properties so volumes it provisioned are adopted. democratic-csi is evidence of practice, not of correctness. **OpenZFS 2.4 is the only supported version** (`docs/adr/0004-support-floor-and-node-side.md`).

## Status

| Edge | State |
|---|---|
| ZFS execution (`internal/zfs`) | done: typed `zfs(8)` client, strict `zfs get -j -p` scan decoding, error classification from OpenZFS's own CLI strings |
| Volume/snapshot lifecycle (`internal/endpoint`) | done: create (plain, from snapshot, from volume), delete, expand, get, list, snapshot create/delete/list, capacity |
| ZFS property passthrough (`internal/endpoint/props.go`) | done: default-deny per-kind allowlist with value checks; requested properties must be set locally for a retry to be compatible (`docs/research/gaps/adv-csi-frontend.md` B3) |
| Northbound API (`api/`, `internal/server`, `cmd/zfs-endpoint`) | done: gRPC over a unix socket, owner-only permissions |
| File export (NFS/SMB) | NFS done: per-client `PublishFileExport`/`UnpublishFileExport` (filesystems; an IPv4 address or IPv4 CIDR, always written as a masked CIDR with `/N`, no broader than `-nfs-min-prefix` (default /16), no overlapping networks on one volume; rw/ro), desired clients in a local `<prefix>:nfs_clients` property with `sharenfs` computed from it, each Publish verified against `/var/lib/nfs/etab` (at the mountpoint's real path) and running nfsd threads; root only (`docs/research/gaps/file-export.md` and its reviews). ADR 0004 since decided that export state lives only in `sharenfs`: `nfs_clients` is to be removed in a follow-up and reconcile is to report a leftover one (neither is done yet). SMB not started |
| Block export (iSCSI/NVMe-oF) | design v2 done, not implemented: sole configfs owner, minted `block_id` identity, fail-closed build order (`docs/research/gaps/block-export-v2.md`) |
| CSI front end (`internal/csi`, `cmd/zfs-csi`) | controller + identity done; csi-sanity Controller/Identity groups run on the simulator and real ZFS. Node side not started, so block access is opt-in (`-enable-block`, default off) |
| Deployment (`deploy/`) | hyperconverged, unix sockets only (`docs/adr/0006-first-deployment-shape.md`): systemd unit per parent serving `/run/zfs-endpoint/<systemd-escape parent>.sock` (also zfs-endpoint's default `-listen`; `zfs-csi -parent` dials it), Kubernetes manifests in `deploy/k8s/`. Version set at build with `-ldflags -X github.com/wommy/empty/internal/version.Version=...`, printed by `-version`. No image or release build yet |
| Transport auth | not started; the endpoint socket is mode 0600 in a 0700 directory until then, the CSI socket 0600 in the pod's emptyDir (`internal/unixsock`, `docs/research/edges2/transport-auth.md`) |

## Design

The core is data-oriented:

```
lock(keys) → scan → Table → plan(Table, request) → Plan → apply → re-read
             one zfs     typed,     pure function,     ops as     each op carries its
             get -j -p   immutable  no I/O             plain data own replay tolerance
```

- **Scan** (`table.go`): one `zfs get -j -p -d` per scan reads the parent's volumes and their snapshots (or one volume and its snapshots) into a flat columnar buffer. The parent itself and anything deeper, such as a dataset someone made below a volume, are dropped, so planners never see them. An RPC may scan more than once: once per volume it touches, and again to re-read after applying. Each scan is decoded once into an immutable, ID-sorted `Table` of typed `Row`s, with snapshot-by-volume and clone-by-origin indexes. Ownership is read only from properties with source `local`: user properties inherit, and `received` ones arrive with a `zfs send -p` copy. `Row.OwnedVolume` and `Row.OwnedSnapshot` are the only ownership tests. A table records whether it covers the whole parent or only the volumes it was read for, and planners that conclude from a missing row refuse a table that does not cover it.
- **Plan** (`plan.go`): pure functions take a `Table` and a request and return either a `Plan` (a `[]Op` of plain data) or a typed error.
  - Idempotency means the plan is empty.
  - A provision that was interrupted converges, because uncommitted rows are driven toward the request rather than judged for compatibility.
- **Apply** (`apply.go`): runs ops in order. Each op states whether "already exists" or "already gone" counts as done, so replaying a partially applied plan converges.
- **Locks** (`internal/lock`): CreateVolume locks the new volume and, for a clone, its source volume and snapshot. CreateSnapshot and DeleteSnapshot lock the snapshot and its volume. ExpandVolume and the export calls lock only the volume. DeleteVolume locks the volume and, when its plan releases a clone-source snapshot on another volume, that volume too: the extra lock is only tried while the first is held, and when busy everything is released and the whole set is waited for in sorted order, so it cannot deadlock against a CreateVolume from that source. Reconcile takes volume locks without waiting and skips busy volumes. A busy lock is waited for up to `-lock-wait` (default 30 s; `0` takes a free lock and never waits, a negative value is refused), cut to one second before the caller's deadline when the call has one, and then returns `ABORTED`, so CSI sidecars retry instead of timing out.

Errors use the CSI/gRPC codes directly:
- `ALREADY_EXISTS` for an incompatible re-create.
- OK when deleting something that is already missing, including an id the endpoint could never have issued. Other RPCs report such an id as `NOT_FOUND`.
- `FAILED_PRECONDITION` for deleting a volume that has snapshots or clones, or snapshots the endpoint did not make, and for "in use" (`dataset is busy`) on a destroy.
- `ABORTED` for a lock that is busy. It is also returned for an unclassified zfs failure after an earlier op of the plan applied, because a retry re-scans and converges. Before any change, that failure stays `INTERNAL`.
- `UNAVAILABLE` for zfs "busy" outside a destroy, a read-only or missing pool, or a missing parent.
- `DEADLINE_EXCEEDED` or `ABORTED` for a zfs command killed by the caller's deadline or cancellation. The outcome is unknown and a retry converges.

## Decisions and their evidence

All research is in `docs/research/`. Delegates produced it from pinned clones and GitHits searches, and adversarial rounds then attacked it. `alignment.md` maps 108 distinct operations across 13 implementations onto one vocabulary (495 mappings).

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
| Never `zfs destroy -r`: release the clone-source origin (`-d`) first, destroy each judged snapshot by name, then the volume plainly | `-r` took child datasets someone made below a volume and snapshots cut after the scan. Without it zfs itself refuses ("has children"). Releasing the origin first means no crash point orphans it (wave-5 lifecycle FSM) |
| Clone-source snapshots are stamped at creation (`managed_resource`, `clone_source_for=<clone>`) | the reserved name alone is not ownership: anyone can `zfs snapshot` it. Unstamped ones (democratic-csi cuts them bare) are released only with the clone whose origin names them exactly, under the parent, from an owned volume; otherwise they are reported, never collected |
| Only owned, committed volumes are served (`Row.ServedVolume`) | an uncommitted row is a provision in flight or abandoned; Get, Expand, export and snapshot or clone sources acting on it raced Reconcile |
| A volume needs both `managed_resource` and `csi_volume_name` = its id | both writers set the two together; managed without a name is a hand edit or an inherit |
| Default `-property-prefix zfs-endpoint`, validated at startup | ADR 0003, and OpenZFS `zfs_prop_user`: only `a-z 0-9 - _ . :`, and shorter than 256 bytes with the suffix. `democratic-csi` is the adoption mode. Without the flag, a parent whose volumes carry only `democratic-csi:managed_resource` (provisioned before the default changed) exits 78 at startup, naming the flag |
| A thick filesystem's failed grow puts its refquota back | zfs sets refquota, then refreservation fails ENOSPC; the raised refquota stayed, so the volume reported a size it could not reserve and every later Expand was `RESOURCE_EXHAUSTED`. Reserving first is impossible: zfs refuses a refreservation above the refquota |
| `-parent` at most 126 bytes, refused at startup (exit 78) | zfs names are at most 255 bytes and a volume or snapshot id is up to 128 more; a longer parent accepted startup and then failed creates of valid CSI names |
| `xattr` only `sa`/`off`; `snapdir` `hidden`/`visible`/`disabled`; `copies` only on zvols | `xattr=on` is an alias zfs prints as `sa` (2.4 `xattr_table`), and it meant `dir` in 2.2. On a filesystem, extra copies count against `refquota`, which would silently shrink the volume |
| Busy is mapped by operation | CSI: "in use" on DeleteVolume/DeleteSnapshot is `FAILED_PRECONDITION`. On create, expand and clone it clears by itself, so it is retryable |
| A killed zfs command is `DEADLINE_EXCEEDED`/`ABORTED`, never `INTERNAL` | its outcome is unknown, and a final code would strand a half-applied plan. Commands get SIGTERM and then a 5 s `WaitDelay` |
| Never delete a non-socket at the socket path | a misconfigured `-csi-address` removed whatever file it named |
| No struct-of-arrays layout | measured: struct-of-arrays saves about 12 µs on a filter over 10k rows (`docs/research/dod-go/memory-layout.md`), while a whole-parent scan of 10k rows costs about 180-190 ms and 19 MB in about 100 allocations Go-side (`BenchmarkScan`: JSON decode, row decode and index, zfs exec excluded; the text parser it replaced took about 150-170 ms and 102 MB in 355k allocations). The cost is in parsing and decoding, not in the row layout |

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
go test -run '^$' -fuzz FuzzDecodeGetJSON ./internal/zfs
```

The contract tests in `internal/endpoint` run against `internal/zfs/zfstest` or, with `ZFS_TEST_POOL` set, against a real pool. CI does the latter in an Ubuntu VM running OpenZFS 2.4 (`ci/real-zfs-vm.sh`): six concurrent shards, each on its own pool backed by a sparse file on a guest tmpfs (`ci/real-zfs-guest.sh`). Planner tests build `Table`s directly from rows, with no I/O.

`FuzzCreateConverges` asserts the core invariant: for any accepted request, a second identical CreateVolume succeeds and changes nothing.

`FuzzReconcileSafe` builds arbitrary parents on the simulator: received copies, foreign snapshots, child datasets, clones and origins outside the parent, deferred snapshots, unstamped clone sources, just-committed retries, and a busy destroy on the first run. It asserts that reconcile never errors, never targets anything the endpoint does not own (or a desired, young or CSI-snapshotted volume), that everything unowned and everything outside the scan survives, and that once injected failures clear it converges: the run after that applies nothing and reports no failed group, except a volume zfs refuses for something the scan cannot see (an outside clone of a deferred snapshot); with no such volume, a dry run then plans nothing.

`TestModel` is a model-based test: random sequences of RPCs and out-of-band actions (snapshots, clones, child datasets, receives, renames, holds), with a kill, an error or a racing action injected at a plan step, run against the endpoint and a pure reference model of the contract (`model_test.go`). After every step it compares codes, results and the world read back with plain zfs, checks that nothing unowned changed and that a second reconcile plans nothing, and shrinks a failure to a replayable trace (`MODEL_SEED=<n>`). A PR runs 64 sequences on the simulator and 50 steps on the VM's pool; the nightly tier sweeps 20,000 sequences. A divergence that is known and not yet fixed is masked with a spec row and an expiry date: `TestModel` fails once a mask expires, and the nightly `TestExploreModelMasksStillBite` fails once a mask hides nothing. `TestRealFacts` checks the OpenZFS facts the model and the simulator share against the real pool, and the simulator against them.

The recovery FSM explorer (`fsm_explore_test.go`, nightly) walks every world two volumes reach in four steps, crashing, killing and racing every mutation, and fails on any unnamed state, foreign touch, divergent retry or non-idempotent reconcile.

The simulator injects failures (`Sim.Fail`) and received properties (`Sim.Receive`), and it prints index properties the way zfs 2.4 does.

## Open gaps

The research found these, and nothing ships for them yet:
- **Block exports, SMB** and node attach. For per-LUN detach, see Trident's `RemoveLUNFromSessions` and gobrick.
- **Reconcile/GC.** The on-demand half ships: the `Reconcile` RPC (`reconcile.go`) runs off the request path and collects uncommitted rows from abandoned creates, stamped clone-source snapshots whose clone is gone, and owned volumes outside a desired set the caller supplies. Its guards:
  - It never touches unowned or received datasets, desired volumes, CSI snapshots, volumes with CSI or foreign snapshots or clones, user-held snapshots (`userrefs`) or datasets below them (reported as skipped, so no run plans a destroy zfs refuses every time), or anything younger than `min_age_seconds`: default one hour, at least ten minutes (twice external-provisioner's retry backoff cap). A committed volume is aged from its `committed_at` stamp when that is later than its creation.
  - A desired set that is empty, or names none of the endpoint's volumes, collects no committed volume. `max_deletions` (default 10, `unlimited_deletions` to lift) caps undesired volumes per run: above it nothing is applied and the call is `FAILED_PRECONDITION`.
  - `dry_run` returns the plan and a `plan_digest`; a run that echoes the digest applies only if it plans exactly that, else `ABORTED`.
  - It locks per volume without waiting: busy volumes are skipped. Under the locks it re-plans and keeps only actions the unlocked plan made for the same reason, so a row a racing CreateVolume committed is left alone.
  - It applies per group (a volume with its snapshots and released origin, or one orphan). A failing group is reported as skipped with `failed: <error>`, what ran is returned, and the run goes on; a source whose clone failed is not attempted. Destroys never use `-r`, so zfs refuses on a dataset or clone that appeared after the scan, or a clone the scan cannot see.

  Still open: a startup report-only pass; reporting unrecorded exports; reading the snapshots' `clones` property so clones outside the parent are refused in the plan rather than by zfs at apply; and taking `Now` from the zfs host when the runner is remote (adv4 F10).
- **HA exclusivity.** A channel-program fencing epoch was designed and then **dropped** after adversarial review: the epoch can go backwards, and creates, resizes and clones can't be fenced (`docs/research/gaps/adv-fencing.md`). Exclusivity is per host, with a `flock` per parent dataset, and across hosts it belongs to the HA layer (`zpool multihost` plus pacemaker/STONITH).
- **Changed-block tracking** (the CSI SnapshotMetadata service). No ZFS implementation exists anywhere, and the zfs send stream is the only exact source.
- **ControllerModifyVolume.** No ZFS driver implements it.
- **Detached snapshots,** which would let volumes be deleted while their snapshots survive.

## License

Apache-2.0. See `LICENSE`.
