Suggested title: **zfs-endpoint: ZFS volume/snapshot lifecycle over gRPC, NFS export, CSI controller, Reconcile, hyperconverged deploy**

A Go daemon, `zfs-endpoint`, that owns volume and snapshot lifecycle on one ZFS parent dataset and serves it over gRPC (`api/zfsendpoint/v1`). CSI drivers and other front ends then only translate requests. `zfs-csi`, a CSI controller, is the first of those front ends.

**OpenZFS is the source of truth** (ADR 0003). Behavior follows OpenZFS's semantics, checked against its source and a real 2.4.x pool. **OpenZFS 2.4 is the only supported version** (ADR 0004). democratic-csi is an adoption target, not the baseline. By default, state lives under `zfs-endpoint:*` user properties. `-property-prefix democratic-csi` turns on adoption mode, which reads and writes that driver's properties.

Size: 350 commits on `main` (an empty root commit), 397 files, +62,145 lines. About 19k lines of non-generated Go, 25.7k lines of tests in 124 test files, and 49 research files.

## What's in it

| Area | Where | State |
|---|---|---|
| ZFS execution | `internal/zfs` | Typed `zfs(8)` client behind a `Runner`. Scan, `Get` and `List` read `zfs get -j -p` through one strict JSON decoder that streams from the pipe. Errors are classified from OpenZFS's own CLI strings, and only a classified not-found counts as absent. |
| Simulator / oracle | `internal/zfs/zfstest`, `internal/zfs/zfsdiff` | CLI simulator with failure injection (`Sim.Fail`) and received properties (`Sim.Receive`). It prints index properties the way 2.4 does. A reference model of `zfs(8)` is diffed against it, and `TestRealFacts` checks both against a real pool. |
| Lifecycle core | `internal/endpoint` | `lock → scan → Table → plan → Plan → apply → re-read`. Covers create (plain, from a snapshot, from a volume), delete, expand, get, list, snapshot create/delete/list, and capacity. A repeated request gives an empty plan, and an interrupted provision converges when retried. |
| Property passthrough | `internal/endpoint/props.go` | Allowlist per kind, default deny, with value checks. |
| Space floor | `internal/endpoint/headroom.go` | `-min-free-bytes` (off by default). Advisory: it is not a reservation. |
| NFS export | `internal/endpoint/export.go`, `internal/nfsref` | `PublishFileExport`/`UnpublishFileExport` per client, IPv4 CIDR only. State lives only in `sharenfs`. Each Publish is verified against `/var/lib/nfs/etab` and the running nfsd threads. A static export can also be set at create time. |
| Reconcile / GC | `internal/endpoint/reconcile.go`, `cmd/zfs-endpoint/reconcile.go` | `Reconcile` RPC plus a `zfs-endpoint reconcile` CLI. Dry run first, then `-apply -digest <plan_digest>`, capped by `-max-deletions`. It never touches unowned, received, held or young datasets. Drift is reported, never fixed. |
| Locks | `internal/lock` | Keyed locks on every resource an RPC touches. When the lock wait (`-lock-wait`, 30 s by default) or the caller's deadline runs out, the call returns `ABORTED`. |
| Server / transport | `internal/server`, `internal/unixsock`, `cmd/zfs-endpoint` | gRPC over unix sockets only, with peer credentials (`SO_PEERCRED`) and roles per listener (ADR 0001 v2). The CSI socket is 0600 in a 0700 directory, or 0660 to one group with `-socket-group`. Reconcile and the export RPCs are served only on `-admin-listen` (root or the daemon's own uid). Every call logs an audit line with the peer's uid, gid and pid. |
| CSI front end | `internal/csi`, `cmd/zfs-csi` | Controller and Identity services. NFS mode turns on when the endpoint advertises `-nfs-server`: StorageClass `nfs.clients`/`nfs.server`/`nfs.uid`/`nfs.gid`/`nfs.mode`, and `server`/`share` in volume_context for csi-driver-nfs. Block access is opt-in (`-enable-block`) and refused in NFS mode. |
| Deployment | `deploy/systemd`, `deploy/k8s` | Hyperconverged (ADR 0006). One `zfs-endpoint@<parent>` unit per parent, an optional hardening drop-in, and a daily report-only Reconcile timer. Kubernetes controller, RBAC, CSIDriver and StorageClass manifests. The controller is pinned by a node-restriction label (ADR 0008). |
| Research | `docs/research` | Alignment of 108 distinct operations across 13 implementations (495 mappings), plus the edge, seam and gap reports and the adversarial reports behind them. |
| Domain docs | `CONTEXT.md`, `docs/adr/` | Glossary, and the 8 ADRs below. |

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

## Run and deploy

```sh
# on the ZFS host, one instance per parent dataset
systemctl enable --now "zfs-endpoint@$(systemd-escape tank/k8s/vols).service"
#   CSI socket:   /run/zfs-endpoint/sock/<escaped parent>/endpoint.sock
#   admin socket: /run/zfs-endpoint/admin/<escaped parent>/endpoint.sock (root only)

# optional: daily dry-run Reconcile report (never applies)
systemctl enable --now "zfs-endpoint-reconcile@$(systemd-escape tank/k8s/vols).timer"

# Kubernetes: label exactly ONE node, the ZFS host, before applying
kubectl label node <host> node-restriction.kubernetes.io/zfs-endpoint-host=true
# set zfs-csi -parent and the endpoint-dir hostPath in controller.yaml, build/push the image, then
kubectl apply -f deploy/k8s/
```

- **Upgrading from an earlier revision of this branch:** the controller's nodeSelector changed from `zfs-endpoint.wommy.github.io/host` to `node-restriction.kubernetes.io/zfs-endpoint-host=true` (ADR 0008). Label the storage node **before** applying the manifests. Otherwise the controller pod stays Pending.
- **Adopting democratic-csi volumes:** start the endpoint with `-property-prefix democratic-csi`. Without that flag, a parent whose volumes carry only `democratic-csi:*` markers exits 78 at startup, and the error names the flag.
- No image is published yet: build `zfs-csi` with `-ldflags -X github.com/wommy/empty/internal/version.Version=...`. `-version` prints the version.
- Reconcile runbook: see README, "Runbook: Reconcile".

## What's tested where

| Tier | Where | What |
|---|---|---|
| Simulator (every push) | `ci.yml` `test` | `go test -race ./...` against `zfstest`. Includes the contract tests, planner tests over hand-built `Table`s, and `TestModel` (64 random RPC and out-of-band sequences with injected kills, errors and races, checked against a pure reference model). |
| Static (every push) | `ci.yml` `static` | gofmt, `go mod tidy`, vet, buf lint, generated-code drift, and every `Fuzz*` seed corpus. Also checks the CI wiring: every suite and part runs exactly once (`ci/check-suites.sh`). |
| Real ZFS (every push) | `ci.yml` `real-zfs`, three jobs: **`endpoint 0/2`**, **`endpoint 1/2`**, **`csi`** | Ubuntu 26.10 under KVM with OpenZFS 2.4.x (asserted for both userland and kmod), and nfs-server running. The endpoint suite is split into two parts of 3 shards, using a fixed 6-shard plan (`ci/real-zfs-plan.sh`, proven a partition by `ci/check-split.sh`). The csi job runs the csi tests plus csi-sanity on filesystems and on zvols, and checks the sandboxing drop-in under the systemd unit (`ci/real-zfs-unit.sh`). Each shard gets its own pool on a sparse file in guest tmpfs. Skips are budgeted in `ci/expected-skips.txt` (`ci/check-skips.sh`). |
| Nightly | `nightly.yml` | 5 min of fuzzing for each of the 11 targets (`FuzzCreateConverges`, `FuzzReconcileSafe`, `FuzzModel`, the NFS chain, `FuzzDiff`, ...). `real-zfs-full` runs each suite unsplit through the fallback boot path. `explore` runs the FSM explorer (every world two volumes reach in four steps) and a 20,000-sequence model sweep. A failure opens an issue labelled `needs-triage`. |
| Cache warm | `cache-warm.yml`, hourly | Keeps the guest/qemu entry and the four Go cache namespaces warm on the default branch, which is where PRs read caches from. |

## CI budget (ADR 0005)

A PR's checks must finish within 60 s of a push, with a 45 s nominal margin for slower runners. Recent pushes on this branch went green in **40–48 s** with warm caches. Each VM job's fixed cost (cache restore, background boot, test build while the guest boots) is about 22 s, and each endpoint part's test phase is about 7–10 s. A cold PR (no default-branch cache) pays about 115 s for the guest and about 13 s for the qemu tree. Preventing that is `cache-warm.yml`'s job.

## Known limitations and open items

- **Current head is red.** On `f7aa459`, `real ZFS 2.4 (endpoint 0/2, ...)` fails the skip budget. Three simulator-only tests skip on the VM but are not listed in `ci/expected-skips.txt`: `TestCreateVolumeCatchesDatasetReplacedBeforeCommit/block`, `TestMissingParentFailsCreateAndDeleteUnavailable`, `TestRPCCallBudget` (run 40548404256). The other four checks pass.
- **Nightly and cache-warm have never run.** Both trigger only on `schedule`/`workflow_dispatch`, which run from the default branch, and `main` holds only the empty root commit. Until this merges, every PR run starts with cold caches unless a run on this PR saved some, and the nightly tier has produced no results.
- **The `needs-triage` label does not exist yet.** Nightly's `gh issue create --label needs-triage` will fail until it is created. Any of the other canonical labels (`needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`) that are missing should be created at the same time.
- **Branch protection:** the required checks must use the exact job names `real ZFS 2.4 (endpoint 0/2, Ubuntu 26.10 VM)`, `real ZFS 2.4 (endpoint 1/2, Ubuntu 26.10 VM)` and `real ZFS 2.4 (csi, Ubuntu 26.10 VM)`, plus `static` and `test`. Parts are numbered from 0, so there is no `2/2`.
- **No node plugin yet.** The node DaemonSet and the csi-driver-nfs pieces are not in `deploy/k8s` (ADR 0004 delegates the node side to csi-driver-nfs). There is no published image or release build.
- **Not implemented:** SMB, block export (iSCSI/NVMe-oF; design v2 only), node attach, mTLS/TCP (refused by both binaries), HA exclusivity across hosts (a fencing epoch was designed and then dropped; exclusivity is a `flock` per host), changed-block tracking, ControllerModifyVolume, detached snapshots.
- **Reconcile, still open:** a report-only pass at startup, reporting unrecorded exports, refusing clones outside the parent at plan time instead of at apply, and taking `Now` from the zfs host when the runner is remote.
- **Other:** ADR 0002 (snapshot ids) is still proposed. The `-min-free-bytes` floor is advisory, and concurrent creates can cross it. The hardening drop-in still has its capability, device and memory limits commented out.

## Review notes

- Start with `CONTEXT.md` and ADRs 0003, 0001 and 0006. Then read `internal/endpoint/{table,plan,apply}.go`.
- The README's "Decisions and their evidence" and "Corrections from adversarial review" record the claims that were disproved, so they don't come back.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01GmdReAMVTjoprXaMtzULQa
