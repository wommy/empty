A Go daemon, `zfs-endpoint`, that owns volume and snapshot lifecycle on one ZFS parent dataset and serves it over gRPC (`api/zfsendpoint/v1`). CSI drivers and other front ends then only translate requests. `zfs-csi`, a CSI controller, is the first of those front ends.

**OpenZFS is the source of truth** (ADR 0003). Behavior follows OpenZFS's semantics, checked against its source and a real 2.4.x pool. **OpenZFS 2.4 is the only supported version** (ADR 0004). democratic-csi is an adoption target, not the baseline. By default, state lives under `zfs-endpoint:*` user properties. `-property-prefix democratic-csi` turns on adoption mode, which reads and writes that driver's properties.

Size: 398 commits ahead of `main` (the empty root commit `2dff442`), 427 files and +68,471 lines against it, and about 20.2k lines of non-generated Go (excluding `*.pb.go` and tests), 29.7k lines of Go tests in 138 test files, and 49 research files.

## What's in it

| Area | Where | State |
|---|---|---|
| ZFS execution | `internal/zfs` | Typed `zfs(8)` client behind a `Runner`. Scan, `Get` and `List` read `zfs get -j -p` through one strict JSON decoder that streams from the pipe. Errors are classified from OpenZFS's own CLI strings, and only a classified not-found counts as absent. |
| Simulator / oracle | `internal/zfs/zfstest`, `internal/zfs/zfsdiff` | CLI simulator with failure injection (`Sim.Fail`) and received properties (`Sim.Receive`). It prints index properties the way 2.4 does. A reference model of `zfs(8)` is diffed against it, and `TestRealFacts` checks both against a real pool. |
| Lifecycle core | `internal/endpoint` | `lock → scan → Table → plan → Plan → apply → re-read`. Covers create (plain, from a snapshot, from a volume), delete, expand, get, list, snapshot create/delete/list, and capacity. A repeated request gives an empty plan, and an interrupted provision converges when retried. A failed fresh CreateVolume rolls back what it made (ADR 0010), so no row is left behind a final code. |
| Property passthrough | `internal/endpoint/props.go` | Allowlist per kind, default deny, with value checks. |
| Space floor | `internal/endpoint/headroom.go` | `-min-free-bytes` (off by default). Advisory: it is not a reservation. |
| NFS export | `internal/endpoint/export.go`, `internal/nfsref` | `PublishFileExport`/`UnpublishFileExport` per client, IPv4 CIDR only. State lives only in `sharenfs`. Each Publish is verified against `/var/lib/nfs/etab` and the running nfsd threads. A static export can also be set at create time. |
| Reconcile / GC | `internal/endpoint/reconcile.go`, `cmd/zfs-endpoint/reconcile.go` | `Reconcile` RPC plus a `zfs-endpoint reconcile` CLI. Dry run first, then `-apply -digest <plan_digest>`, capped by `-max-deletions`. It never touches unowned, received, held or young datasets. Drift is reported, never fixed. |
| Locks | `internal/lock` | Keyed locks on every resource an RPC touches. When the lock wait (`-lock-wait`, 30 s by default) or the caller's deadline runs out, the call returns `ABORTED`. |
| Server / transport | `internal/server`, `internal/unixsock`, `cmd/zfs-endpoint` | gRPC over unix sockets only, with peer credentials (`SO_PEERCRED`) and roles per listener (ADR 0001 v2). The CSI socket is 0600 in a 0700 directory, or 0660 to one group with `-socket-group`. Reconcile and the export RPCs are served only on `-admin-listen` (root or the daemon's own uid). Every call logs an audit line with the peer's uid, gid and pid. |
| Operator signals | `internal/server`, `internal/csi/log.go`, `cmd/zfs-endpoint`, `cmd/zfs-csi` | Both binaries log JSON. Each call carries a request id: zfs-csi sends its id to the endpoint as `x-request-id`, and both sides log it as `request_id`. The endpoint logs failed calls at WARN, and INTERNAL or UNKNOWN at ERROR. A `zfs slow` warning fires once for a zfs command still running past `-zfs-slow` (10 s, reads included). An ABORTED lock refusal names the holder's request id and how long it has held. Reconcile ends with a `summary:` line, and `-fail-on-findings` exits 3 (the timer passes it). README's "Runbook: when a PVC is stuck" and its exit-code table map each error and exit code to a fix. |
| CSI front end | `internal/csi`, `cmd/zfs-csi` | Controller and Identity services. NFS mode turns on when the endpoint advertises `-nfs-server`: StorageClass `nfs.clients`/`nfs.server`/`nfs.uid`/`nfs.gid`/`nfs.mode`, and `server`/`share` in volume_context for csi-driver-nfs. Block access is opt-in (`-enable-block`) and refused in NFS mode. Mount access outside NFS mode is opt-in too (`-enable-local-filesystem`, ADR 0009). |
| Deployment | `deploy/systemd`, `deploy/k8s`, `Dockerfile` | Hyperconverged (ADR 0006). One `zfs-endpoint@<parent>` unit per parent, an optional hardening drop-in, an NFS drop-in (`zfs-endpoint-nfs.conf.example`), and a daily report-only Reconcile timer. Kubernetes manifests in `deploy/k8s`: the zfs-csi controller, whose readiness probe reports the endpoint's health (served by the livenessprobe sidecar), plus RBAC, CSIDriver, StorageClass, and the csi-driver-nfs node DaemonSet under this driver's name (`node.yaml`, no RBAC). The `Dockerfile` builds a static zfs-csi on distroless. The controller is pinned by a node-restriction label (ADR 0008). |
| Research | `docs/research` | Alignment of 108 distinct operations across 13 implementations (495 mappings), plus the edge, seam and gap reports and the adversarial reports behind them. |
| Domain docs | `CONTEXT.md`, `docs/adr/` | Glossary, and the 10 ADRs below. |

### ADRs

| ADR | Status |
|---|---|
| 0001 Transport, tenancy and per-RPC authorization (v2) | accepted (v2). Supersedes v1, which was never built. |
| 0002 Snapshot ids that do not cap volume id length | **proposed** |
| 0003 OpenZFS is the source of truth; democratic-csi is one adoption target | accepted |
| 0004 OpenZFS 2.4 only; exports live in `sharenfs`; the node side is csi-driver-nfs | accepted |
| 0005 Tests and CI finish in under one minute | accepted for the budget (45 s nominal margin). Mechanisms adopted where marked, the rest proposed. |
| 0006 First deployment: hyperconverged over unix sockets, journald metrics, block opt-in | accepted (decisive, reversible) |
| 0007 The typed-core migration stops at step 0 | accepted (decisive, reversible) |
| 0008 Controller pod: pinned by a node-restriction label, socket read-only, no token in zfs-csi | accepted (decisive, reversible) |
| 0009 Non-NFS filesystem volumes are opt-in; the shipped deployment is NFS mode | accepted (decisive, reversible) |
| 0010 A failed fresh CreateVolume rolls back what it made | accepted (decisive, reversible) |

## Run and deploy

Follow README's "Install" section. It is the source for every step, and `deploy/readme_test.go` checks that it names every file in `deploy/systemd`. In short:

- **On the ZFS host, as root:** install `nfs-kernel-server`, build `zfs-endpoint` with `-ldflags -X github.com/wommy/empty/internal/version.Version=...`, and install the unit. Copy `deploy/systemd/zfs-endpoint@.service.d/zfs-endpoint-nfs.conf.example` to `/etc/systemd/system/zfs-endpoint@.service.d/zfs-endpoint-nfs.conf` and replace its placeholder with the address nodes mount from (unedited, the endpoint exits 78). Then enable `zfs-endpoint@<parent>` and, optionally, the reconcile timer.
- **In the cluster:** label the ZFS host, and only it, with `node-restriction.kubernetes.io/zfs-endpoint-host=true`. Install the external-snapshotter CRDs and snapshot-controller first, since `classes.yaml`'s VolumeSnapshotClass is refused without them. Build the zfs-csi image from the new `Dockerfile` and set it in `controller.yaml`, along with `-parent` and the endpoint socket hostPath. In `deploy/k8s/classes.yaml`, fill in the StorageClass placeholders `nfs.clients`, `nfs.uid`, `nfs.gid` and `nfs.mode`: the parser refuses placeholders. Then `kubectl apply -f deploy/k8s/`, where `00-namespace.yaml` applies first.
- **Check:** a pod writes to a volume (README "Install", step 3).
- **Upgrading from an earlier revision of this branch:** the controller's nodeSelector changed from `zfs-endpoint.wommy.github.io/host` to `node-restriction.kubernetes.io/zfs-endpoint-host=true` (ADR 0008). Label the storage node **before** applying the manifests. Otherwise the controller pod stays Pending.
- **Adopting democratic-csi volumes:** start the endpoint with `-property-prefix democratic-csi`. Without that flag, a parent whose volumes carry only `democratic-csi:*` markers exits 78 at startup, and the error names the flag.
- Reconcile runbook: see README, "Runbook: Reconcile".

## What's tested where

| Tier | Where | What |
|---|---|---|
| Simulator (every push) | `ci.yml` `test` | `go test -race ./...` against `zfstest`. Includes the contract tests, planner tests over hand-built `Table`s, and `TestModel` (64 random RPC and out-of-band sequences with injected kills, errors and races, checked against a pure reference model), plus csi-sanity in NFS mode. csi-sanity's passed spec names are pinned per mode in `internal/csi/testdata/sanity-passed-*.txt`. |
| Static (every push) | `ci.yml` `static` | gofmt, `go mod tidy`, vet, buf lint, generated-code drift, and every `Fuzz*` seed corpus. Also checks the CI wiring: every suite and part runs exactly once (`ci/check-suites.sh`). |
| Real ZFS (every push) | `ci.yml` `real-zfs`, three jobs: **`endpoint 0/2`**, **`endpoint 1/2`**, **`csi`** | Ubuntu 26.10 under KVM with OpenZFS 2.4.x (asserted for both userland and kmod), and nfs-server running. The endpoint suite is split into two parts of 3 shards, using a fixed 6-shard plan (`ci/real-zfs-plan.sh`, proven a partition by `ci/check-split.sh`). The csi job runs the csi tests plus csi-sanity on filesystems and on zvols, and checks the sandboxing drop-in under the systemd unit (`ci/real-zfs-unit.sh`). Each shard gets its own pool on a sparse file in guest tmpfs. Skips are budgeted in `ci/expected-skips.txt` (`ci/check-skips.sh`). |
| Nightly | `nightly.yml` | 5 min of fuzzing for each of the 11 targets (`FuzzCreateConverges`, `FuzzReconcileSafe`, `FuzzModel`, the NFS chain, `FuzzDiff`, ...). `real-zfs-full` runs the endpoint and csi suites unsplit on the real pool, with csi-sanity in NFS mode; only the csi job boots through the fallback (initrd) path. `explore` runs the FSM explorer (every world two volumes reach in four steps) and a 20,000-sequence model sweep. `image` builds the Dockerfile and checks `-version`, and never pushes. A failure opens an issue labelled `needs-triage`. |
| Cache warm | `cache-warm.yml`, hourly | Keeps the guest/qemu entry and the four Go cache namespaces warm on the default branch, which is where PRs read caches from. |

## CI budget (ADR 0005)

A PR's checks must finish within 60 s of a push, with a 45 s nominal margin for slower runners. Recent pushes on this branch went green in **38–49 s**. Endpoint parts are planned from weights normalized per part by fork count, which aims at equal guest work per part (ADR 0005). Measured splits so far are 54/46 and 59/41, because the fork count itself grows on slower runners; that bias is still open. A cold PR (no default-branch cache) pays about 115 s for the guest and about 13 s for the qemu tree. Preventing that is `cache-warm.yml`'s job.

## Known limitations and open items

- **Nightly has never run, and cache-warm has run once.** Both trigger only on `schedule`/`workflow_dispatch`, which run from the default branch, and `main` holds only the empty root commit. The one cache-warm run so far is a green scheduled run. Until this merges, every PR run starts with cold caches unless a run on this PR saved some, and the nightly tier has produced no results.
- **The `needs-triage` label does not exist yet.** Nightly's `gh issue create --label needs-triage` will fail until it is created. Of the five canonical triage labels only `wontfix` exists, so create `needs-triage`, `needs-info`, `ready-for-agent` and `ready-for-human` at the same time.
- **Branch protection:** the required checks must use the exact job names `real ZFS 2.4 (endpoint 0/2, Ubuntu 26.10 VM)`, `real ZFS 2.4 (endpoint 1/2, Ubuntu 26.10 VM)` and `real ZFS 2.4 (csi, Ubuntu 26.10 VM)`, plus `static` and `test`. Parts are numbered from 0, so there is no `2/2`.
- **Image:** the Dockerfile is built only by the nightly job, which has not run yet. Nothing is published.
- **Admin socket uid check:** `adminPeer` passes a connection whose peer credentials cannot be read, so the admin role's uid check passes in that case. Unreachable on Linux today; a fix is queued.
- **CSI export reach:** the csi caller names the client networks of an NFS export itself on CreateVolume, clones included, and a compromised controller can call the endpoint socket directly with any `nfs.clients`. The only endpoint-side bound is `-nfs-min-prefix` (default /16). ADR 0001's Consequences say a compromised controller loses export to itself, which this does not hold to. An endpoint-side network allowlist is proposed, pending the owner's decision.
- **Symlinked mountpoint:** once `nfs.uid`, `nfs.gid` or `nfs.mode` is set, CreateVolume fails with FailedPrecondition when the volume's mountpoint path contains a symlink (for example, a parent mounted below one). The root is set only through real directories.
- **No end-to-end deploy test:** no CI job runs Install step 3 (kind, the node plugin's NFS mount of a real CreateVolume, a pod's write); each seam is pinned on its own.
- **Not implemented:** SMB, block export (iSCSI/NVMe-oF; design v2 only), node attach, mTLS/TCP (refused by both binaries), HA exclusivity across hosts (a fencing epoch was designed and then dropped; exclusivity is a `flock` per host), changed-block tracking, ControllerModifyVolume, detached snapshots.
- **Reconcile, still open:** a report-only pass at startup, reporting unrecorded exports, refusing clones outside the parent at plan time instead of at apply, and taking `Now` from the zfs host when the runner is remote.
- **Other:** ADR 0002 (snapshot ids) is still proposed. The `-min-free-bytes` floor is advisory, and concurrent creates can cross it. The hardening drop-in still has its capability, device and memory limits commented out.

## Review notes

- Start with `CONTEXT.md` and ADRs 0003, 0001 and 0006. Then read `internal/endpoint/{table,plan,apply}.go`.
- The README's "Decisions and their evidence" and "Corrections from adversarial review" record the claims that were disproved, so they don't come back.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01GmdReAMVTjoprXaMtzULQa
