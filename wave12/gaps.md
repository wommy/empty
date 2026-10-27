# Gaps versus production: first real deployment (read-only audit)

Repo /home/user/empty, branch claude/gallant-johnson-1qaxp5 (HEAD a02c5b0). Target: TrueNAS / Ubuntu 26.10, OpenZFS 2.4.
Scoring: (likelihood x blast radius) / fix cost, each 1-5. Citations are file:line in the working tree.

## Part 1. Anomalies (rank above ordinary gaps)

### A1. The CSI controller provisions volumes that no node can mount (ADR 0004's decision is not implemented)
- ADR 0004 (docs/adr/0004-support-floor-and-node-side.md, Decision) says: CreateVolume sets the StorageClass CIDR list with `sharenfs` and returns `server` and `share` in `volume_context`, and csi-driver-nfs does the node side.
- Code: `volumeOut` returns only `dataset`, `kind`, `devicePath`, `mountpoint` (internal/csi/controller.go:36-41, 204-213). `mountpoint` is a path on the ZFS host. There is no `server` or `share`.
- `grep PublishFileExport internal/csi` has no hits. The CSI layer never calls the endpoint's export RPCs (internal/server/server.go:99-113 exist, but nothing in-tree calls them).
- Parameters accepted are only `sparse` and `reserve` (controller.go:26-29, 143-165). `CreateVolumeRequest.Properties` exists (server.go:129) but the CSI layer never fills it, so a StorageClass cannot set compression, recordsize and so on, though README says "property passthrough: done".
- ControllerGetCapabilities never offers PUBLISH_UNPUBLISH (controller.go:66-72).
- Root cause: ADR 0004 was written after the csi-frontend work. The endpoint only has the per-client Publish RPC, and no component knows the NFS server address (no `-nfs-server` flag anywhere).
- Effect: the first PVC binds and the pod sticks in ContainerCreating. Block is worse (A5).

### A2. zfs-csi cannot start in a stock pod
- The default `-csi-address` is `unix:///csi/csi.sock` (cmd/zfs-csi/main.go:37). Listen runs `SecureDir` on `/csi`, which refuses any directory writable by group or others (internal/unixsock/unixsock.go:33, 96). A test asserts the 0777 refusal (unixsock_test.go:104-125).
- kubelet creates emptyDir volumes 0777 (Kubernetes behaviour from memory; not run here). The standard CSI pod layout (emptyDir at /csi shared with sidecars) therefore dies at startup with "writable by group or others".
- The sanity test uses a plain `net.Listen` (internal/csi/sanity_test.go), and cmd/zfs-csi/main_test.go has one test (endpointTarget only). So no test exercises `listen()`.
- The 0600 socket also means sidecars must run as the same uid.

### A3. Unit and CSI client socket names disagree
- The unit serves `/run/zfs-endpoint/%i.sock` (deploy/systemd/zfs-endpoint@.service:31), for example `tank-k8s-vols.sock`.
- zfs-csi dials `/run/zfs-endpoint/endpoint.sock` by default (cmd/zfs-csi/main.go:36). The same default is in zfs-endpoint (main.go:62), but the packaged unit overrides it.
- Anyone following the unit's comment gets Unavailable until `-endpoint` is set.

### A4. Remote topology is impossible, and ADR 0001 is stale
- TCP is refused in the endpoint (cmd/zfs-endpoint/main.go:193-196) and in zfs-csi (cmd/zfs-csi/main.go:74-80, `insecure.NewCredentials()` at :99).
- So zfs-csi must run on the ZFS host, as root, reached through a hostPath. A TrueNAS appliance separate from the k8s nodes cannot be used at all.
- ADR 0001 is `Status: proposed`. Nothing of it exists: no interceptor, no policy file, no TLS, no SO_PEERCRED. Its Context is stale: it calls the TCP listener "plain net.Listen ... a live hole", but code refuses TCP. It says "10 RPCs"; the proto has 13 (`grep -c '^  rpc '`). It says the CSI is "on branch csi-frontend"; it is merged into this branch.
- README "Status" is the accurate one: "Transport auth: not started".

### A5. Block volumes are accepted but unusable
- `kindOf` accepts block access types (controller.go:93-107). `CreateVolume` makes a zvol with `devicePath=/dev/zvol/...` on the storage host. README says block export is "design v2 done, not implemented". ADR 0004 says "Block volumes still need a node story".
- A block StorageClass therefore succeeds silently. It is the same failure shape as A1, but with no planned fix near-term.

### A6. Reconcile has no caller
- The RPC exists (server.go:210). In-tree callers are only tests: `grep Reconcile` over cmd/ and internal/csi shows nothing.
- Abandoned creates (uncommitted rows) and orphan clone-source snapshots accumulate forever unless an operator hand-writes a gRPC client. No gRPC reflection is registered either (grep "reflection" is empty), so grpcurl needs the .proto.
- Nothing builds the desired set from PVs. README lists "a startup report-only pass" as open.
- An empty desired set is safe by construction (reconcile.go: SkipEmptyDesired), so a timer could run it today.

### A7. No release or build path, and the version is "dev"
- `Version = "dev"` is in internal/server/server.go:20 and internal/csi/identity.go:25. `grep -rn ldflags` has no hits. There is no `-version` flag, no Dockerfile, no release workflow, and CI never builds the cmd binaries except through `go vet`.
- The unit expects `/usr/local/sbin/zfs-endpoint` (unit :29); nothing installs it.
- `GetCapabilities.version` and `GetPluginInfo.vendor_version` report "dev", so an operator cannot tell what is running before or after an upgrade.

### A8. Explained, not anomalies
- "13 implementations" (docs/research/alignment.md:5) versus 17 `repo` labels in alignment.json: 11 top-level repos, plus 2 democratic-csi variants, equals 13 if the 4 proxmox sub-backends are collapsed. The count is internally consistent; the counting rule is just undocumented. The 495 count matches.
- Deleting an unmanaged dataset is `FAILED_PRECONDITION "not managed"` (plan.go:463-465), not a silent OK. Good: no silent leak.

## Part 2. Ordinary gaps (verified against code)

| # | Area | Finding | Evidence |
|---|---|---|---|
| G1 | Metrics | None. No Prometheus, expvar or pprof anywhere in cmd/ or internal/. `lock.Held` says "for metrics" but is only used in tests. | internal/lock/lock.go:138 |
| G2 | Health | gRPC health only, over the unix socket. SERVING is set before the first probe, and the first probe runs after 30s (main.go:138-141). Nothing acts on NOT_SERVING (no systemd WatchdogSec; only READY and STOPPING are sent, notify.go). No HTTP /healthz. | daemon.go:56-85 |
| G3 | AuthN/Z | Filesystem mode only: socket 0600 in a 0700 dir (unixsock.go:29-52). Any root process on the host is fully trusted. No principal in audit logs (daemon.go:158 logs `peer` as the unix addr, useless). No tenancy. | ADR 0001 unimplemented |
| G4 | Capacity | `MaxCapacityBytes = math.MaxInt64` (plan.go:92). CreateVolume never consults pool `available`. Filesystems are thin refquota, so oversubscription is unbounded. GetCapacity reports parent `available` only (endpoint.go:673). ZFS at 100% full makes deletes fail. No parent quota or headroom guard. | endpoint.go:405-440 |
| G5 | Property or prefix migration | `-property-prefix` is a flag (main.go:64). Startup never scans, so changing it silently strands every volume as "not managed" (Create gives ALREADY_EXISTS, Delete gives FAILED_PRECONDITION). No schema or version stamp (grep for `:schema` or `:version` finds none). Adoption mode is manual. | table.go:271, plan.go:463 |
| G6 | Upgrade | Drain and restart semantics are good (main.go:150-175, unit :44-55) but undocumented. Nothing detects a downgrade or a mixed prefix. Version is "dev" (A7). | |
| G7 | Backup or DR | No snapshot schedule, replication or export tooling. A `zfs send -p` replica carries markers as `received`, which is by design not ownership (README; table.go:271). After failover to a replica every volume is "not managed": Create gives ALREADY_EXISTS, Delete gives FAILED_PRECONDITION, forever. No adopt or repair path (`zfs inherit -S`/`zfs set` locally per property). | ADR 0003 |
| G8 | HA fencing | One flock per parent on tmpfs (lockparent.go:25-65), per host only. No check of `zpool get multihost`. Fencing epoch was deliberately dropped (docs/research/gaps/adv-fencing.md). Acceptable for a single node. A pool imported on two hosts is the HA layer's problem and is not even detected. | main.go:100-109 |
| G9 | Reconcile observability | Only log lines: "reconcile" and "reconcile destroyed" (server.go:215, 231). No gauge of uncommitted rows, no last-run time, no skipped-reason counters. Operators cannot see a growing orphan count. | |
| G10 | Docs or runbooks | The README is a design doc. No install guide, flag reference, exit-code table (75 and 78 live only in startup.go comments), or runbook for the cases: stuck uncommitted volume, foreign snapshot blocking delete, pool suspended, key not loaded. docs/agents/* is for agents. | |
| G11 | K8s manifests | None. No CSIDriver (attachRequired:false), RBAC, StatefulSet or Deployment, hostPath, nodeSelector, StorageClass, VolumeSnapshotClass, or image. The CSI pod must be pinned to the ZFS host. | `ls deploy` = systemd only |
| G12 | TrueNAS fit | Unverified: no TrueNAS-specific code or doc beyond a CI comment ("what TrueNAS 27 ships", ci.yml:128; ADR 0004:8). The appliance rootfs is image-based. The unit path `/usr/local/sbin` and `/etc/systemd/system` may not survive upgrades. Middleware owns /etc/exports and NFS, which collides with this design's `sharenfs` + etab verification. If TrueNAS ships OpenZFS other than 2.4.x, the daemon exits 78 and never retries (startup.go:60-72, unit RestartPreventExitStatus=78). | |
| G13 | Node-side prereqs | Publish needs root, nfsd threads and /var/lib/nfs/etab (endpoint.go:689-695, 780-787). Non-root runs have no NFS (main.go:80-86). Fine, but unstated for operators. | |
| G14 | Unit hardening | Intentionally no mount namespace (unit :64-74). Fine. `TasksMax=512` with `-max-zfs 8` is fine. | |

## Part 3. Pareto: top gaps that remove about 80% of first-deployment risk

Score = (likelihood x blast) / cost.

| Rank | Gap | L | B | C | Score | Class | One-line fix | Lowest test level (ADR 0005) |
|---|---|---|---|---|---|---|---|---|
| 1 | A2 zfs-csi dies on 0777 emptyDir | 5 | 5 | 1 | 25 | decisive, reversible | In zfs-csi, listen on the CSI socket without `SecureDir` (the dir is the pod's private volume, sidecars are the only peers); keep the stale-socket and non-socket-refusal logic. | Unit: `unixsock` test with a 0777 dir and a new `ListenIn(shared dir)` variant, plus `cmd/zfs-csi` test that `listen()` works there. Simulator tier, sub-second. |
| 2 | A5 block accepted but unusable | 4 | 3 | 1 | 12 | decisive, reversible | Add `-enable-block` (default false) to zfs-csi; `kindOf` refuses Block with a clear InvalidArgument unless set. | Unit: `internal/csi` controller_test; sanity block specs run only with the flag. |
| 3 | A3 + A7 + G11 packaging (socket names, build, manifests) | 5 | 4 | 2 | 10 | decisive, reversible | Unit uses `%i.sock`: ship `deploy/k8s/` (CSIDriver attachRequired:false, hostPath dir mount, root, nodeSelector, `-endpoint` explicit) and a Makefile/CI step building static binaries with `-ldflags -X` for both Version vars, plus a `-version` flag. | Static tier: `go build` plus `zfs-endpoint -version` in the CI static job; manifests checked with `kubeconform` (static, seconds). Add a unit test that `-version` prints the injected value. |
| 4 | A1 no mountable volumes | 5 | 5 | 3 | 8.3 | owner decision on mechanism; recommend one | Add zfs-csi `-nfs-server` and StorageClass `nfsClients` (CIDRs). CreateVolume calls PublishFileExport per CIDR and returns `volume_context{server,share}`, per ADR 0004. | `internal/csi` test on the simulator asserting volume_context and etab contents; the existing real-ZFS VM leg (nfs-server running) covers the real export. |
| 5 | G5 prefix mismatch at startup | 2 | 4 | 1 | 8 | decisive, reversible | At startup, one `zfs get` over the subtree; if there are 0 owned rows under the configured prefix but N>0 under `democratic-csi` (or another known prefix), log an error and exit 78. | `cmd/zfs-endpoint` main_test with the simulator, like TestStartupRefusals. |
| 6 | G4 no pool headroom guard | 3 | 5 | 2 | 7.5 | decisive, reversible | `-min-free-bytes` (default 0 = off, so no behaviour change; recommend 10% in the unit) checked in CreateVolume/Expand/Snapshot against parent `available`; returns RESOURCE_EXHAUSTED (code 8). | Simulator unit in internal/endpoint (the sim already has `available`). |
| 7 | A6 Reconcile never runs | 4 | 2 | 2 | 4 | decisive, reversible | `zfs-endpoint reconcile` subcommand (dial the local socket, empty desired set, `min_age` 1h) plus a systemd timer; empty desired is safe. Desired-set sync from PVs is a later job. | `cmd/zfs-endpoint` unit with the simulator runner; one golden check that nothing committed is touched. |
| 8 | A4 / G3 remote and auth | 4 | 5 | 5 | 4 | **owner decision** | If the topology is "k8s separate from ZFS host": implement the ADR 0001 slice (mTLS file-source + static URI-SAN allowlist, `rpcs:["*"]`) and defer SPIFFE and volume_prefix scoping. If hyperconverged: keep unix only, and mark ADR 0001 deferred. | `internal/server` test over real TCP 127.0.0.1:0 with minted certs (ADR 0001 section 5): no client cert, wrong CA, two URI SANs, unknown principal. |

Items G1/G2/G9 (metrics, watchdog, reconcile gauges) score about 3 each and are below the cut; take them in one change (`-metrics-listen 127.0.0.1:port`, a Prometheus dependency, which is an owner call, or just journald counters). G7 DR scores about 2.7: ship a runbook first (`zfs get -s received` then `zfs set` locally), an `adopt` subcommand later (the simulator's `Sim.Receive` supports testing it).

## Owner decisions vs decisive choices

Owner decisions (evidence-backed recommendation in brackets):
1. Topology: remote storage host (needs mTLS, ADR 0001) or hyperconverged (unix only). [Recommend hyperconverged for the first deployment: it is the only topology the code supports, and TrueNAS-as-appliance (G12) is unverified.]
2. NFS mechanism for A1: static at create (ADR 0004 text) versus per-node ControllerPublish. [Recommend static: ADR 0004 already decides it and it needs no attacher sidecar.]
3. Block: build the iSCSI/NVMe-oF node side (large, docs/research/gaps/block-export-v2.md) or keep it off. [Recommend off by default now (rank 2).]
4. TrueNAS appliance versus plain Ubuntu host. [Recommend Ubuntu 26.10 for the first deployment; it is the only OS exercised in CI, ci/real-zfs-vm.sh.]
5. Metrics dependency (Prometheus client) versus journald only.

Decisive and reversible: ranks 1, 2, 3, 5, 6, 7 above.

## Summary (under 300 words)

The daemon core is production-grade. Lifecycle is idempotent, shutdown is bounded, start-up refusals have distinct exit codes, and the CI gate exercises a real OpenZFS 2.4 VM. The gaps are all at the edges.

Anomalies. (1) The CSI controller provisions PVCs that nothing can mount. ADR 0004 says CreateVolume returns NFS `server` and `share`, but the code returns only host-local paths and never calls the export RPCs. Block volumes are accepted but have no node story. (2) zfs-csi refuses its own default socket directory (`/csi`) whenever kubelet makes it 0777, which is the stock emptyDir layout. (3) The packaged unit serves `<escaped>.sock` while both binaries default to `endpoint.sock`. (4) Remote topology is impossible: TCP is refused on both sides and ADR 0001 is unimplemented. It is also stale: it says "10 RPCs" (the proto has 13), calls TCP "plain" though it is refused, and puts the CSI on a separate branch. (5) Reconcile has no in-tree caller. (6) Nothing builds or installs a release, and the version reports "dev".

Ordinary gaps: no metrics, no watchdog, no pool-headroom guard (`MaxCapacityBytes` is MaxInt64), no prefix-mismatch detection, a DR replica that arrives as `received` and is therefore unmanaged forever, per-host-only HA fencing, no runbooks, no k8s manifests.

Fix order: unblock zfs-csi startup, gate block off by default, then packaging (socket names, build with ldflags, manifests). After that come static NFS export at create, the prefix guard, the headroom guard, and a reconcile timer. Topology and mTLS are the one real owner decision. I recommend a hyperconverged first deployment on Ubuntu, which is the only configuration the code and CI support.
