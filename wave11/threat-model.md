# Wave 11: threat model for zfs-endpoint and zfs-csi

- **Target:** `/home/user/empty`, branch `claude/gallant-johnson-1qaxp5`, HEAD `4c1386e`. Review was read-only.
- **Inputs:**
  - ADRs 0001, 0003, 0004 and 0005;
  - the README and `deploy/systemd/zfs-endpoint@.service`;
  - `cmd/zfs-endpoint` and `cmd/zfs-csi`;
  - `internal/{server,unixsock,zfs,endpoint,csi}`;
  - `api/zfsendpoint/v1/endpoint.proto`;
  - scratchpad `wave9/nfs-diff.md`, `wave9/csi-diff.md` and `wave8/adversary.md`.
- **Companion:** `wave11/adr-0001-v2.md`, a draft that supersedes the proposed ADR 0001.

**Scoring.** Likelihood (L) and impact (I) run from 1 to 5. Fix cost (C) runs from 1 (a few lines plus one test) to 5 (a new subsystem). Pareto score = L×I/C. A finding marked "verified" was checked against the code at HEAD. Where something was not checked, the finding says so.

---

## 0. Summary

1. **Every socket caller is a full administrator, and the only real gate is "root on the host".** The design is coherent for one trusted front end per host. It stops being safe as soon as the socket directory is mounted into a pod, which is the only way zfs-csi can reach it. Three cheap changes buy most of the risk reduction:
   - one socket directory per instance, separate from the lock directory (**F1**);
   - per-listener RPC roles, so the CSI socket cannot call Reconcile, PublishFileExport or UnpublishFileExport, none of which zfs-csi uses (**F2**);
   - `SO_PEERCRED` read and logged on every call (**F7**).
2. **The NFS data plane is a weaker boundary than the control plane.**
   - An ancestor export with `crossmnt` exports every volume. This happens at create time, not only at Publish, so wave 9's Publish-side fix does not close it (**F5**).
   - AUTH_SYS combined with CIDR-only access lets any pod whose traffic leaves from an exported node read and write tenant data (**F6**).
   - Host-local users can read volume contents and run setuid binaries that NFS clients planted (**F12**).
3. **Reconcile's desired-set model assumes one container orchestrator (CO) per parent.**
   - ADR 0001's "several clusters share one parent" contradicts that assumption. Each cluster's GC would delete the other's volumes, 10 per run, with no limit on how many runs.
   - Reconcile also destroys volumes that are exported and in use (**F3**).
   - The dry-run digest is a TOCTOU guard. It is not an approval (**F4**).
4. **ADR 0001 is almost entirely unbuilt.** The one built piece is that TCP is refused. ADR 0001 also contradicts itself in places: its Context section still describes a plaintext TCP listener that the code now refuses. The v2 draft proposes **the parent dataset as the tenancy unit** (one instance, one socket directory and one group per tenant) together with **listener roles**. Together these replace in-process `volume_prefix` scoping and remove its fail-open "scope enforcement is split" risk.

---

## 1. Trust boundaries

```
 Kubernetes API ──► external-provisioner / snapshotter / resizer sidecars   (pod, root, shared emptyDir)
                         │ B1: unix /csi/csi.sock, 0600, insecure creds
                         ▼
                      zfs-csi  (pod; must be uid 0 + hostPath /run/zfs-endpoint to reach B2)
                         │ B2: unix /run/zfs-endpoint/<inst>.sock, 0600 root, dir 0700 root,
                         │     no peer identity, no RPC authz, TCP refused
                         ▼
                 zfs-endpoint (root, systemd, one instance per parent; flock in /run/zfs-endpoint)
             B3 │ argv exec (no shell)        B4 │ libshare: /etc/exports.d/zfs.exports,
                ▼                                ▼ exportfs -ra (host-wide), reads etab, /proc/fs/nfsd
          zfs / zpool, /dev/zfs            kernel nfsd / mountd
                                                 │ B5: NFSv3/v4, sec=sys (AUTH_SYS), IP/CIDR ACL
                                                 ▼
                                    NFS clients (nodes, and anything that can source from their IPs)

 Side actors:
   B6  host admins (root: fully trusted), other local users, `zfs allow` delegates
   B7  other drivers and tools on the same pool: democratic-csi (adoption mode), sanoid/syncoid/zrepl
   B8  received datasets and properties (`zfs recv` from a replication source)
   B9  audit: stderr JSON → journald
```

**What each principal can do today** (verified against the code):

| Principal | Reaches | Effective rights |
|---|---|---|
| Host root, or any root container with a hostPath to `/run/zfs-endpoint` | every instance's socket and every lock file | all 11 RPCs on every parent. It can also flock any parent's lock (DoS on restart) and unlink or replace sockets (endpoint spoofing). |
| Non-root local user | nothing in `/run/zfs-endpoint` (directory is 0700) | can read volume files through the parent mountpoint if POSIX permissions allow (F12); can run setuid files in volumes (F12) |
| `zfs allow` delegate on the parent | zfs only | can squat names, can forge ownership only on its own datasets, can DoS scans with a newline value (F11) |
| Replication source (B8) | the property values it sends | markers arrive as `received` and are not counted as ownership (verified, `table.go` `own()`). Received `sharenfs` is refused (verified). A newline value DoSes the scan (F11). |
| NFS client in an exported CIDR | the exported datasets | read and write as any non-root uid (AUTH_SYS) |

---

## 2. STRIDE per boundary

### B1: sidecars → zfs-csi (`/csi/csi.sock`)

| | Scenario | Status |
|---|---|---|
| S | Another container in the pod connects to the socket. | Pod-internal trust is accepted, as in every CSI driver. The socket is 0600 (verified). |
| T | A malicious CreateVolume parameter. | `parseParams` allows only `sparse` and `reserve`. There is no property passthrough from the CSI layer (verified). |
| R | none at this layer | — |
| I | Volume context leaks the host dataset name and mountpoint to the CO. | Accepted. |
| D | **zfs-csi refuses to start** on the usual `emptyDir` socket directory. `unixsock.SecureDir` rejects a group- or other-writable directory, and a kubelet `emptyDir` is 0777 (from kubelet source as I remember it; **not re-verified here**). | **F10** (availability anomaly) |
| E | none | — |

### B2: zfs-csi → zfs-endpoint (the control plane)

| | Scenario | Status |
|---|---|---|
| S | (a) The caller is unauthenticated beyond "can open the socket". (b) A pod holding the hostPath **replaces the socket** with its own listener, so other front ends talk to a fake endpoint. | (a) by design until ADR 0001; F7. (b) **F1** |
| T | A compromised controller calls `Reconcile{desired:[any one id], unlimited_deletions:true}`. That destroys every owned, committed volume older than 10 minutes that has no snapshots or clones, in **one call**. | **F2**, F3 and F4 (verified in `reconcile.go:535-631`) |
| R | The audit line has `peer=""`: a unix peer has no address, so no uid or pid is recorded. The caller also chooses its own `x-request-id`, so it can reuse another request's id to muddy correlation. | **F7**, **F14** |
| I | A compromised controller calls `PublishFileExport(vol, <attacker>/32, rw)` for every id from ListVolumes, then mounts from its own host. The result is a bulk data exfiltration path. zfs-csi never calls this RPC. | **F2** |
| D | (a) flock on `/run/zfs-endpoint/<parent>.lock` from a pod: on its next restart the endpoint exits 1 and loops. (b) Unlinking the socket. (c) There is no connection limit: 64 streams per connection times 64 MiB messages. | (a)(b) **F1**. (c) low, because callers are root. |
| E | The caller drives a root daemon. Inputs reach zfs argv only through validated names (no leading `-`, no space; `datasetRE`), an allowlisted property set, and `ParseClient`, which emits only digits, `.` and `/`. | Verified sound. No shell is used (`LocalRunner`). |

### B3: endpoint → zfs/zpool

| | Scenario | Status |
|---|---|---|
| T | Argument injection. | Closed: argv only, validated names, `checkProps` refuses `\n`, `\t` and NUL (verified). |
| T | The scan is forged by a newline in a user property. | Detected: a duplicate (dataset, property) pair fails the whole scan, which fails closed. This is a DoS (**F11**). ADR 0004 decided `zfs get -j`, but the code still runs `get -Hp` (verified, `zfs.go:424`). |
| T | TOCTOU between scan and destroy (an admin renames a dataset and re-creates one with the same name). | Accepted residual. Mitigated by non-recursive destroy (wave8 B1/C2). |
| D | A suspended pool hangs zfs. | Bounded by contexts, `-max-zfs`, probes and the drain (verified). |

### B4: endpoint → libshare, exportfs, etab and nfsd

| | Scenario | Status |
|---|---|---|
| T | **A crossmnt ancestor.** The parent or any ancestor has `sharenfs=on` (which gives `rw,crossmnt`), or `/etc/exports` has a crossmnt line. mountd then serves every child volume to that line's clients. Pinning `sharenfs=off` at create (`plan.go:229-232`) does not stop it. | **F5**. Wave 9 D1 fixes Publish only. |
| T | The verifier fails open (wave 9 D2 and D3): foreign lines take precedence, or options widen access. | Being fixed (wave 9). Not on HEAD: `grep crossmnt\|etabOptions` finds only the simulator. |
| D | Every sharenfs change runs `exportfs -ra` for the whole host. That re-resolves hostnames in the admin's exports, so a DNS failure drops them. One malformed line anywhere in `zfs.exports` (including a received sharenfs on an unrelated dataset) stops exportfs reading the rest. | **F16** (accept and report) |
| R | Exports are not recorded outside ZFS. | By design (ADR 0004): `sharenfs` is the record. |

### B5: kernel nfsd → NFS clients

| | Scenario | Status |
|---|---|---|
| S | AUTH_SYS: any client claims any non-root uid. The ACL is the source IP only. Pods are usually SNATed to the node IP. `secure` (a source port below 1024) is satisfied by `CAP_NET_BIND_SERVICE`, which containers get by default. A userspace NFS client (libnfs) needs no `CAP_SYS_ADMIN`. | **F6** |
| T/I | No integrity or confidentiality on the wire (sec=sys). | F6 (krb5p is the long-term answer) |
| I | `.zfs/snapshot` is reachable through an export even with `snapdir=hidden`. Deferred-destroyed CSI snapshots stay readable until they are reaped. | **F15** (low) |
| E | An NFS client plants a setuid-uid-N binary. The host mounts volumes `setuid=on,exec=on` (inherited), so a local host user who runs it becomes uid N on the storage host. | **F12** |
| D | A /16 floor means one export can reach 65k hosts. | F6 |

### B6: host admins, local users, delegates

- **Admins (root)** are fully trusted. This is out of scope, and the threat model says so.
- **Local non-root users:**
  - They cannot reach the sockets (directory 0700, verified).
  - They can traverse the parent mountpoint, which zfs creates 0755, and read volume files that are world-readable. That breaks tenant confidentiality on the storage host (**F12**).
- **Delegates:**
  - They can squat a future volume name, which makes CreateVolume refuse. That is ADR 0003 behaviour and is accepted.
  - They can DoS the scan with a newline value (F11).

### B7: other drivers and tools on the same pool

- **A different parent** is isolated by depth-1 scans and ownership markers. `lockParent` refuses nested parents among endpoint instances only.
- **The same parent, adoption mode** (`-property-prefix democratic-csi` with a live democratic-csi):
  - Both drivers claim the same volumes.
  - The endpoint's Reconcile treats every democratic-csi volume missing from the caller's desired set as undesired and destroys up to 10 per run, repeatedly (**F3**).
- **sanoid and syncoid:**
  - Their snapshots are foreign. DeleteVolume refuses them unless `-destroy-foreign-snapshots` is set (verified in the README and the config doc). Reconcile never uses that flag.

### B8: received datasets

- Markers with source `received` are not ownership (verified: `own()` reads only `Local()`, and `Local()` means `local` only).
- A received `sharenfs` is refused (verified: `foreign()`).
- **Residual risks:**
  - The newline DoS (F11).
  - **`zfs recv -o <prefix>:managed_resource=true`** sets the property as *local*. A replication pipeline that uses `-o` for markers would mint ownership. This is an operator error, and I have not verified it on 2.4.
  - A received crossmnt `sharenfs` on an *ancestor* falls under F5.

### B9: audit

| | Scenario | Status |
|---|---|---|
| R | No principal is recorded. | F7 |
| R | A caller-chosen request id. | F14 |
| R | **journald rate limiting drops audit lines.** The defaults are about 10000 lines per 30 s per unit. Each cheap RPC (`GetCapabilities`) logs an Info line, so a flood of calls followed by destroys loses the `zfs exec destroy` lines. | **F8** |
| T | Root can edit the journal. | Accepted while callers are root. Ship logs off the host, and optionally enable `Seal=yes` (FSS). |
| I | Log injection. | Closed: `slog.JSONHandler` escapes control characters, and the request id is limited to `[A-Za-z0-9._-]{1,64}` (verified). The handler is not configurable to text. |
| I | Secrets. | None are handled today: no keys, no tokens, and encryption properties are denied (`props.go` `denied`). TLS key material arrives with v2 and must never be logged. |

---

## 3. ADR 0001: decided vs built

| ADR 0001 decision | Built at HEAD? | Evidence |
|---|---|---|
| Socket mode `0660`, group from `-socket-gid` | **No.** The socket is 0600 (umask 0177 around bind) and the directory 0700. No gid flag exists. | `unixsock.go:25-58`; unit `RuntimeDirectoryMode=0700`, `UMask=0077`; test `TestListenIsOwnerOnly` |
| `SO_PEERCRED` handshaker; principal `unix:uid=N` | **No.** No `Ucred` or `SO_PEERCRED` anywhere. The interceptor logs `peer.Addr`, which is empty for unix. | `grep -ri peercred` returns nothing; `daemon.go` `unary` |
| uid allowlist / policy file; default deny | **No** | no policy code |
| Per-RPC allowlist and `volume_prefix` scope | **No.** Every caller gets all 11 RPCs. | `server.go` |
| TCP: mTLS only, refuse without TLS | **Refused entirely** (stricter than the ADR). The endpoint accepts only `unix:///abs`; zfs-csi dials only `unix:///abs`. Abstract `@` sockets are refused. | `main.go` `socketPath`; `zfs-csi/main.go` `endpointTarget`; `unixsock.CheckPath` |
| `MaxConnectionAge` 1h | **No** (not needed until identity is authorized) | — |
| Principal on every audit line | **No** | F7 |
| Policy reload on SIGHUP | **No** | — |
| systemd hardening (not in ADR 0001; asked in the brief) | **Partial.** Present: `NoNewPrivileges`, `LockPersonality`, `RestrictRealtime`, `RestrictSUIDSGID`, `MemoryDenyWriteExecute`, `SystemCallArchitectures=native`, `RestrictAddressFamilies`, `TasksMax`, `UMask=0077`. Absent but compatible with "no private mount namespace": `RestrictNamespaces=yes`, `ProtectClock=yes`, `ProtectHostname=yes`, `SystemCallFilter=~@clock @cpu-emulation @debug @obsolete @reboot @swap @raw-io`, `KeyringMode=private`, `LogRateLimitIntervalSec=0`, a measured `CapabilityBoundingSet`. | unit file. No test reads the unit file. |

**ADR 0001's own drift.**
- Its Context still says "TCP listener: plain `net.Listen` with no credentials... a live hole". That hole is closed (`socketPath`), and the README's Transport-auth row is accurate.
- Its "several clusters share one parent" premise conflicts with Reconcile's single-CO desired-set model (F3).
- Status is still *proposed*. The v2 draft rewrites it.

---

## 4. AuthZ per RPC

The callers that need each group of RPCs:

| RPC | Needed by | Blast radius | Proposed role |
|---|---|---|---|
| GetCapabilities, GetVolume, ListVolumes, ListSnapshots, GetCapacity | zfs-csi, monitoring | read-only, one parent | `csi`, `read` |
| CreateVolume, ExpandVolume, CreateSnapshot | zfs-csi | capacity exhaustion within the parent | `csi` |
| DeleteVolume, DeleteSnapshot | zfs-csi | one volume per call; unbounded across calls | `csi` |
| PublishFileExport / UnpublishFileExport | **nobody yet**. The future CSI ControllerPublish would publish a node's /32. | exfiltration of any volume to any CIDR at or above the floor | `admin` now; `csi` later with a client restricted to `/32` |
| Reconcile | **nobody yet**. A GC job or an operator. | **every collectable volume in one call** | `admin` only |

**Recommendation** (the v2 ADR has the detail): **roles per listener, not a policy file.**
- The default listener serves role `csi`, which is exactly the 10 RPCs zfs-csi calls (verified with `grep c.EP. internal/csi/controller.go`).
- A second listener, `-admin-listen`, serves `admin` (everything). Its socket is 0600 root in its own directory and is never mounted into a pod.
- This is a **separate socket for destructive RPCs**. It needs no identity code, and it removes the controller-compromise chains in F2.
- `SO_PEERCRED` becomes a second gate later, by uid or gid, when a socket is shared with non-root callers.

**The dry-run digest as a two-person rule.**
- It is not one. `digest()` is an unkeyed SHA-256 over `(kind, target, reason)`, truncated to 128 bits (`reconcile.go:496`). Anyone can compute it. It is optional (an empty value applies the plan), and nothing binds it to a principal or a time.
- What it does provide: "what runs is a subset of what I reviewed". That is a TOCTOU guard and worth keeping.
- **Minimal step (DECISIVE):** require `plan_digest` whenever a plan collects at least one *undesired* volume. A blind destructive run then becomes impossible, and a dry run is always on record in the audit log. No consumer exists yet, so the change is cheap.
- **A real two-person rule (ASK-OWNER, deferred):**
  - An `ApproveReconcile(digest)` RPC on the admin socket records `(digest, approver principal, expiry)` in memory.
  - Reconcile applies only an approved digest, and only when the caller's principal differs from the approver's.
  - This needs principals (F7) first. It is pointless while every caller is root on one socket.

---

## 5. Isolation

- **Between volumes in one parent:** there is no isolation from any caller. Every caller sees and mutates every volume. The isolation that exists is ownership (endpoint vs foreign), not tenancy.
- **Between StorageClasses:** none. Every StorageClass of a driver maps to the same endpoint, parent and volume namespace. Parameters are only `sparse` and `reserve`. Per-StorageClass isolation means a separate driver name, zfs-csi deployment, endpoint instance and parent.
- **The parent as tenancy unit (recommended).**
  - What is already built:
    - one systemd instance per parent;
    - flocks refuse nested or duplicate parents on a host;
    - scans are depth-1 under the parent;
    - ids are relative to the parent, so cross-parent clones and snapshots cannot be addressed.
  - What is missing is only per-instance socket placement (F1). That makes "tenant = parent = socket directory = hostPath" enforceable by the kubelet's own mount permissions.
  - In-process `volume_prefix` scoping (ADR 0001 v1) is then unnecessary, and so is its fail-open split enforcement. It also cannot work with Reconcile's desired-set semantics (F3) unless Reconcile is scope-filtered too, which ADR 0001 v1 does not mention.
- **`-property-prefix`** is a namespace for ownership, not a security boundary:
  - Two endpoints with different prefixes on the *same* parent are refused by `lockParent` on one host. Across hosts they are not refused, which is the HA layer's problem.
  - Adoption mode alongside a live democratic-csi is a shared-ownership hazard (F3). The adoption docs should say "stop democratic-csi first".
- **What a compromised CSI controller pod can do today.** It runs as root with a hostPath to `/run/zfs-endpoint`.
  1. Export every volume to itself and copy the data out (PublishFileExport).
  2. Destroy every collectable volume of **every parent on the host** in one Reconcile per parent (unlimited deletions).
  3. Delete volumes one by one (DeleteVolume). The CO legitimately needs this. Only k8s-side controls apply, or a future opt-in `protect` property.
  4. Block every endpoint instance from restarting (flock), or spoof the endpoint to other front ends (replace the socket).
  5. Fill the pool (CreateVolume without a quota). Capacity is bounded only by the parent's own quota, so recommend `quota` on the parent.

  After F1 and F2 it is limited to item 3 and item 5 **on its own parent**.
- **The root daemon's blast radius.**
  - It is root with `CAP_SYS_ADMIN` and no mount namespace, so a memory-safety-free logic bug can reach the whole host.
  - In practice its reachable actions are confined by argv construction to `zfs create|snapshot|clone|set|destroy` on names under the parent, and `zfs set sharenfs` with a digit-only grammar.
  - Mutations go to `parent/*` only (verified: `e.dataset(id)` is `parent/id`, and the id excludes `/` and `@`).
  - The largest *indirect* effect is the host-wide `exportfs -ra` (F16).

---

## 6. Secrets and audit

- **What is logged:**
  - one line per RPC: method, code, duration, error message and request id;
  - for every mutating zfs command (`zfs.Audit`): argv before it runs and the outcome after;
  - targets of DeleteVolume, DeleteSnapshot and Reconcile;
  - startup facts (OpenZFS versions).

  No secrets exist to log today.
- **Tampering:** stderr goes to journald, so root can tamper with it. Journal sealing is off by default. Losing lines to the rate limiter is the practical hole (F8).
- **Log injection:** closed (JSON handler, validated request ids, and inputs quoted with `%q` in errors).
- **Attribution:** none (F7, F14).
- **Information returned to callers:** error messages carry zfs argv and stderr, and full dataset and mountpoint paths (F13, low). Once the tenancy unit is the parent, a caller learns only about its own parent and pool-level free space.

---

## 7. Findings, Pareto-ranked by L×I/C

| # | Id | Boundary | STRIDE | Sev | L | I | C | Score | Minimal fix | Lowest-level test (ADR 0005) |
|---|---|---|---|---|---|---|---|---|---|---|
| 1 | **F1** | B2 | S,D,E | High | 4 | 4 | 1 | 16 | Give each instance its own socket directory: `RuntimeDirectory=zfs-endpoint zfs-endpoint/sock/%i`, `-listen unix:///run/zfs-endpoint/sock/%i/endpoint.sock`. The daemon refuses a socket whose directory is `lockDir` or contains it. Document that a pod mounts only `sock/<inst>`. | `cmd/zfs-endpoint` `TestStartupRefusals` row: `-listen unix:///run/zfs-endpoint/x.sock` gives exit 78 ("socket must not share the lock directory"). A pure `socketPath` check. |
| 2 | **F7** | B2/B9 | R | Med | 3 | 3 | 1 | 9 | A `credentials.TransportCredentials` whose `ServerHandshake` reads `GetsockoptUcred` on `*net.UnixConn` and returns `AuthInfo{UID,GID,PID}`. `unary` logs `peer_uid`, `peer_gid` and `peer_pid`. A non-unix connection is refused. | `internal/server` test on a real unix socket in `t.TempDir()`: one call, and the captured slog record has `peer_uid == os.Getuid()`. |
| 3 | **F10** | B1 | D | Med (avail.) | 4 | 2 | 1 | 8 | zfs-csi: for `-csi-address`, create a private 0700 subdirectory under the shared volume, or relax `SecureDir` for the CSI socket only when the directory is sticky. Ship a manifest that uses `/csi/zfs/csi.sock`. **Check the emptyDir mode on kind first.** | `internal/unixsock` table test: `Listen` in a 0777 parent through the chosen API gives an owner-only socket. |
| 4 | **F2** | B2 | E,I,T | **Critical** | 3 | 5 | 2 | 7.5 | Listener roles. A unary interceptor holds a `map[fullMethod]role` covering every method in `EndpointService_ServiceDesc`. The default listener role is `csi` (the 10 RPCs zfs-csi calls). `-admin-listen` (0600 root, its own directory) serves `admin`. Reconcile, Publish and Unpublish are `admin` only. | `internal/server` table test over bufconn, `role × method → allow/deny`. A method missing from the table fails the test. A `csi` caller gets `PERMISSION_DENIED` for Reconcile. |
| 5 | **F5** | B4/B5 | I,T | High | 3 | 5 | 2 | 7.5 | At startup and in each health probe, parse etab and fail (exit 78 at startup, NOT_SERVING in health) when any export with `crossmnt` (or `sharenfs=on`) covers the parent's mountpoint or one of its ancestors. Wave 9's `crossmntAncestors` can be reused. | A pure function table test: `exposedByAncestor(etab, "/tank/k8s/vols")` over `/ (crossmnt)`, `/tank (rw,crossmnt)`, `/tank/k8s/vols2`, and no crossmnt. Plus one `cmd/zfs-endpoint` startup row with a fake etab. |
| 6 | **F8** | B9 | R | Med | 2 | 3 | 1 | 6 | Unit: `LogRateLimitIntervalSec=0`. Log `GetCapabilities` at Debug. | `deploy/systemd` "unit lint" Go test: parse the unit and assert required directives (`LogRateLimitIntervalSec=0`, the hardening set from F9, `RuntimeDirectory` with `sock/%i`). |
| 7 | **F12** | B5→B6 | E,I | Med | 2 | 3 | 1 | 6 | At create, filesystems also get `setuid=off`, `exec=off` and `devices=off` set locally. These are server-side mount options only; NFS clients are unaffected. At startup, warn when the parent's mountpoint directory can be traversed by others (`mode & 0o001`). | Planner golden test: `PlanCreateVolume` for a filesystem includes the three props. Startup unit test with a temp directory at 0755 logs a warning. |
| 8 | **F6** | B5 | S,I,T | High | 4 | 4 | 3 | 5.3 | Make per-node `/32` publish the default CSI mode (ControllerPublish with the node's IP). Static StorageClass CIDR becomes opt-in, with a documented AUTH_SYS warning. Docs cover a host firewall and NetworkPolicy blocking pod egress to 2049/tcp of the storage host. Long term: `sec=krb5p`. | CSI layer table test: ControllerPublish maps `node_id` to `/32` only, and `10.0.0.0/24` as a node id gives INVALID_ARGUMENT (ADR 0004 consequence). The network side is a doc and a deploy check. |
| 9 | **F3** | B2/B7 | T,D | High | 2 | 5 | 2 | 5 | `PlanReconcile` skips an undesired volume whose `sharenfs` is not `off` or whose `nfs_clients` is not `none` (reason "exported: in use"). The ADR records one CO per parent (v2). Optional: a per-daemon rolling 24 h budget for undesired collections. | Planner table test: an aged, committed, undesired row with local `ShareNFS="rw=10.0.0.5/32"` ends up in `Skipped`, with no action. |
| 10 | **F4** | B2 | E,R | Med | 2 | 4 | 2 | 4 | Reconcile returns FAILED_PRECONDITION when `undesired() > 0` and `plan_digest == ""`. Update the proto comment. | Endpoint test on the simulator: without a digest, an undesired collection is refused. After a dry run and an echo of the digest, it applies. |
| 11 | **F14** | B9 | R | Low | 2 | 2 | 1 | 4 | Always mint the server-side `request_id`. Log the caller's id as `client_request_id`. | `internal/server` unit test: the incoming `x-request-id: X` gives a log record whose `request_id != X` and whose `client_request_id == X`. |
| 12 | **F15** | B5 | I | Low | 2 | 2 | 1 | 4 | Decide whether filesystems default to `snapdir=disabled` (2.4) at create. Document that `hidden` still allows access by path. | Planner golden test, if adopted. |
| 13 | **F11** | B3/B8 | D | Med | 2 | 3 | 2 | 3 | Implement ADR 0004: switch the scan to `zfs get -j -p` with strict decoding. | Simulator test: `Sim.Receive` sets a user property value containing `\n`, and `ListVolumes` still succeeds. `FuzzParseColumns` is replaced by a JSON fuzz target. |
| 14 | **F16** | B4 | D | Low | 2 | 3 | 3 | 2 | Accept. The health probe reports committed exports missing from etab (README open item "reporting unrecorded exports"). | Planner test on a fake etab. |
| 15 | **F9** | host | E | Low | 1 | 3 | 2 | 1.5 | Add `RestrictNamespaces=yes`, `ProtectClock=yes`, `ProtectHostname=yes`, `KeyringMode=private` and `SystemCallFilter=~@clock @cpu-emulation @debug @obsolete @reboot @swap @raw-io`. Measure a `CapabilityBoundingSet` in the VM (`systemd-analyze security`, plus the real-ZFS suite under the unit). | Unit lint test (F8) for the directives. The real-ZFS VM runs the suite under the unit. |
| 16 | **F13** | B2 | I | Low | 2 | 1 | 2 | 1 | Accept once the parent is the tenancy unit. Otherwise trim stderr and argv from messages returned to non-admin roles. | — |

**Cross-references, not re-scored:**
- **Wave 9 D1-D3** (export verifier fail-open) are being fixed now. F5 widens D1 from Publish-time to create-time exposure.
- **Wave 9 T1** (an empty source id gives an empty volume, reported OK) is an integrity bug, not a security boundary. Fix it as wave 9 says.

---

## 8. Owner decisions (researched)

| # | Question | Research / attack | Verdict | Recommendation |
|---|---|---|---|---|
| D1 | What is the tenancy unit: the parent dataset (one instance and socket directory per tenant) or in-process `volume_prefix` scoping (ADR 0001 v1)? | Parent tenancy is already 90% built (instances, flocks, depth-1 scans, relative ids). `volume_prefix` has to be enforced in every planner entry point (a fail-open split) and is incompatible with desired-set Reconcile (F3). It also depends on every cluster's provisioner setting `--volume-name-prefix`. Cost of the parent model: more datasets and instances. Pool free space still leaks through GetCapacity, as before. | **ASK-OWNER** (changes the deployment contract) | Parent = tenant. Keep `volume_prefix` as a possible later extension, unbuilt. |
| D2 | Should destructive RPCs get a separate credential or socket? | A policy file plus `SO_PEERCRED` needs identity code, a policy format and reload handling. A separate socket gives the same effect for the realistic deployment (one CSI controller per parent and one operator) at a tenth of the cost. Attack on the alternative: per-uid policies collapse because all root pods are `uid=0` (ADR 0001's own risk). | **DECISIVE+REVERSIBLE** | Per-listener roles: `csi` by default, `admin` on `-admin-listen`. |
| D3 | Should `plan_digest` be required for a Reconcile that collects undesired volumes? | No consumers exist, and blind runs are the mass-destroy path. A dry run costs one scan. | **DECISIVE+REVERSIBLE** | Require it. |
| D4 | A true two-person rule: approval by a distinct principal? | It needs principals (F7) and somewhere to keep approvals; the in-memory state is lost on restart, which is acceptable. It is meaningless while every caller is root. | **ASK-OWNER** (process, outward) | Defer until F2 and F7 are in. Revisit if Reconcile is automated cluster-wide. |
| D5 | TCP/mTLS: build now or keep refusing? | No remote front end exists, and zfs-csi runs on the storage host by necessity: a hostPath is needed for the socket. mTLS brings PKI, rotation and SPIFFE dependencies. | **DECISIVE+REVERSIBLE** | Keep TCP refused. Move the mTLS design into v2 as "future, when a remote front end exists". |
| D6 | `SO_PEERCRED`: log only, or authorize? | Logging is cheap and fixes repudiation. Authorizing by uid is weak for root pods (all `uid=0`) and has the userns caveat. It is useful with a gid-shared socket and non-root front ends. | **DECISIVE+REVERSIBLE** | Log now (F7). Authorize later via an optional per-listener `allowed_uids`. |
| D7 | Socket mode: keep 0600 root, or 0660 with `-socket-group`? | 0660 lets the controller pod run **non-root** with `supplementalGroups`, which shrinks the pod's blast radius (no root, so no flock or rename on host files once F1 lands). The risk is that any member of the group is a full `csi` caller. | **DECISIVE+REVERSIBLE** | Add `-socket-group` for the `csi` listener only. The admin socket stays 0600 root. |
| D8 | The NFS default: a static StorageClass CIDR (ADR 0004 "first"), or per-node `/32` publish? | Static mode exports every volume of a StorageClass to every node. Combined with AUTH_SYS, that means any pod on any node can reach every volume (F6). Per-node `/32` needs the attacher sidecar and ControllerPublish, but the endpoint RPCs already exist. | **ASK-OWNER** (outward, reverses ADR 0004's ordering) | Per-node `/32` as the default. Static mode is opt-in with a warning. Plan krb5p. |
| D9 | Should Reconcile skip exported volumes? | `sharenfs` is OpenZFS's own "in use" signal (ADR 0003-aligned). A false skip only delays GC. | **DECISIVE+REVERSIBLE** | Yes (F3). |
| D10 | A crossmnt ancestor at startup: refuse or warn? | Warning leaves every volume exported to the ancestor's clients. Refusing breaks deployments that are already exposing everything. | **DECISIVE+REVERSIBLE** | Refuse with exit 78, with a message naming the export line. |
| D11 | Audit sink: journald only, or a dedicated tamper-evident sink? | Callers are root today, so tampering is out of scope. Rate-limit loss is in scope (F8). | **DECISIVE+REVERSIBLE** | journald with `LogRateLimitIntervalSec=0`. Document shipping logs off the host and optional `Seal=yes`. |
| D12 | Default `snapdir=disabled` for filesystems (F15)? | This is user-visible: users lose self-service restore through `.zfs`. | **ASK-OWNER** (user-facing default) | Keep `hidden` and document it. Offer `disabled` through the existing property allowlist. |

---

## 9. Machine-readable

```json
{
  "findings": [
    {"id":"F1","boundary":"B2 zfs-csi→endpoint socket dir","stride":"S,D,E","severity":"high","likelihood":4,"score":16,
     "fix":"Per-instance socket dir /run/zfs-endpoint/sock/%i (RuntimeDirectory=zfs-endpoint zfs-endpoint/sock/%i); refuse -listen whose dir equals or contains lockDir; pods mount only sock/<inst>",
     "test":"cmd/zfs-endpoint TestStartupRefusals row: -listen unix:///run/zfs-endpoint/x.sock -> exit 78","verified_in_code":true},
    {"id":"F7","boundary":"B2/B9","stride":"R","severity":"medium","likelihood":3,"score":9,
     "fix":"TransportCredentials reading SO_PEERCRED (GetsockoptUcred) into AuthInfo; unary logs peer_uid/gid/pid; non-unix conn refused",
     "test":"internal/server real unix socket in t.TempDir(): log record peer_uid == os.Getuid()","verified_in_code":true},
    {"id":"F10","boundary":"B1 sidecars→zfs-csi","stride":"D","severity":"medium","likelihood":4,"score":8,
     "fix":"zfs-csi uses a private 0700 subdir of the shared emptyDir (or SecureDir accepts sticky dirs for the CSI socket); manifest path /csi/zfs/csi.sock; confirm emptyDir mode on kind",
     "test":"internal/unixsock table test: listen under a 0777 parent via the chosen API yields owner-only socket","verified_in_code":true},
    {"id":"F2","boundary":"B2","stride":"E,I,T","severity":"critical","likelihood":3,"score":7.5,
     "fix":"Per-listener roles via unary interceptor over every EndpointService method; default role csi = the 10 RPCs zfs-csi calls; -admin-listen (0600 root, own dir) for Reconcile/Publish/Unpublish",
     "test":"internal/server bufconn table test role×method→allow/deny; missing method entry fails; csi caller Reconcile → PERMISSION_DENIED","verified_in_code":true},
    {"id":"F5","boundary":"B4/B5","stride":"I,T","severity":"high","likelihood":3,"score":7.5,
     "fix":"Startup (exit 78) and health (NOT_SERVING) refuse when etab has a crossmnt/sharenfs=on export of the parent mountpoint or an ancestor",
     "test":"pure table test exposedByAncestor(etab, mount); one startup row with fake etab","verified_in_code":true},
    {"id":"F8","boundary":"B9","stride":"R","severity":"medium","likelihood":2,"score":6,
     "fix":"Unit LogRateLimitIntervalSec=0; GetCapabilities logged at Debug",
     "test":"Go unit-lint test parsing deploy/systemd/zfs-endpoint@.service for required directives","verified_in_code":true},
    {"id":"F12","boundary":"B5→B6","stride":"E,I","severity":"medium","likelihood":2,"score":6,
     "fix":"Create filesystems with local setuid=off,exec=off,devices=off; warn if the parent mountpoint is traversable by others",
     "test":"planner golden test PlanCreateVolume includes the three props; startup warn test on 0755 temp dir","verified_in_code":true},
    {"id":"F6","boundary":"B5 nfsd→clients","stride":"S,I,T","severity":"high","likelihood":4,"score":5.3,
     "fix":"Per-node /32 publish as CSI default; static SC CIDR opt-in with AUTH_SYS warning; doc firewall/NetworkPolicy for 2049; krb5p later",
     "test":"csi table test: ControllerPublish node_id → /32 only, CIDR node_id → INVALID_ARGUMENT","verified_in_code":false},
    {"id":"F3","boundary":"B2/B7","stride":"T,D","severity":"high","likelihood":2,"score":5,
     "fix":"PlanReconcile skips undesired volumes with sharenfs≠off or nfs_clients≠none ('exported: in use'); ADR: one CO per parent; optional rolling 24h budget",
     "test":"planner table test: aged committed undesired row with local sharenfs → Skipped, no action","verified_in_code":true},
    {"id":"F4","boundary":"B2","stride":"E,R","severity":"medium","likelihood":2,"score":4,
     "fix":"Reconcile FAILED_PRECONDITION when undesired()>0 and plan_digest empty; proto comment updated",
     "test":"sim endpoint test: undesired collection without digest refused; dry-run+echo applies","verified_in_code":true},
    {"id":"F14","boundary":"B9","stride":"R","severity":"low","likelihood":2,"score":4,
     "fix":"Always mint request_id; log caller's as client_request_id",
     "test":"internal/server unit test on incomingRequestID/unary log record","verified_in_code":true},
    {"id":"F15","boundary":"B5","stride":"I","severity":"low","likelihood":2,"score":4,
     "fix":"Decide snapdir default (D12); document hidden≠inaccessible",
     "test":"planner golden test if adopted","verified_in_code":false},
    {"id":"F11","boundary":"B3/B8","stride":"D","severity":"medium","likelihood":2,"score":3,
     "fix":"Implement ADR 0004: scan via zfs get -j -p, strict decode",
     "test":"sim test: Sim.Receive user prop with \\n, ListVolumes still OK; JSON fuzz target","verified_in_code":true},
    {"id":"F16","boundary":"B4","stride":"D","severity":"low","likelihood":2,"score":2,
     "fix":"Accept; health reports committed exports missing from etab",
     "test":"planner test on fake etab","verified_in_code":true},
    {"id":"F9","boundary":"host/systemd","stride":"E","severity":"low","likelihood":1,"score":1.5,
     "fix":"Add RestrictNamespaces, ProtectClock, ProtectHostname, KeyringMode=private, SystemCallFilter deny-list; measure CapabilityBoundingSet in VM",
     "test":"unit-lint test + real-ZFS VM suite under the unit","verified_in_code":true},
    {"id":"F13","boundary":"B2","stride":"I","severity":"low","likelihood":2,"score":1,
     "fix":"Accept under parent tenancy; else trim argv/stderr for non-admin roles",
     "test":"none","verified_in_code":true}
  ],
  "decisions": [
    {"question":"Tenancy unit: parent dataset (instance+socket dir per tenant) vs in-process volume_prefix","verdict":"ASK-OWNER","recommend":"parent = tenant; volume_prefix deferred, unbuilt","irreversible":false},
    {"question":"Separate credential/socket for destructive RPCs","verdict":"DECISIVE+REVERSIBLE","recommend":"per-listener roles: csi default, admin on -admin-listen (0600 root)","irreversible":false},
    {"question":"Require plan_digest for Reconcile collecting undesired volumes","verdict":"DECISIVE+REVERSIBLE","recommend":"require","irreversible":false},
    {"question":"True two-person rule (approval by distinct principal)","verdict":"ASK-OWNER","recommend":"defer until roles + SO_PEERCRED land","irreversible":false},
    {"question":"Build TCP/mTLS now","verdict":"DECISIVE+REVERSIBLE","recommend":"keep TCP refused; mTLS design kept as future section","irreversible":false},
    {"question":"SO_PEERCRED log-only vs authorize","verdict":"DECISIVE+REVERSIBLE","recommend":"log now; optional allowed_uids per listener later","irreversible":false},
    {"question":"Socket 0600 root vs 0660 -socket-group","verdict":"DECISIVE+REVERSIBLE","recommend":"-socket-group for csi listener so the controller runs non-root; admin stays 0600 root","irreversible":false},
    {"question":"NFS default: static SC CIDR vs per-node /32 publish","verdict":"ASK-OWNER","recommend":"per-node /32 default; static opt-in with AUTH_SYS warning; krb5p roadmap","irreversible":false},
    {"question":"Reconcile skips exported volumes","verdict":"DECISIVE+REVERSIBLE","recommend":"yes","irreversible":false},
    {"question":"crossmnt ancestor at startup: refuse or warn","verdict":"DECISIVE+REVERSIBLE","recommend":"refuse, exit 78","irreversible":false},
    {"question":"Audit sink","verdict":"DECISIVE+REVERSIBLE","recommend":"journald, LogRateLimitIntervalSec=0, document off-host shipping and Seal=yes","irreversible":false},
    {"question":"Default snapdir=disabled for filesystems","verdict":"ASK-OWNER","recommend":"keep hidden, document; disabled via allowlist","irreversible":false}
  ]
}
```
