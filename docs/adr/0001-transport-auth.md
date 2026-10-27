# 1. Transport, tenancy and per-RPC authorization (v2)

Status: accepted (v2) · Date: 2026-10-27 · Supersedes: ADR 0001 v1, "Transport authentication and per-caller authorization" (proposed, never built; read it at commit `2a4e914` or earlier). Related: ADR 0003, ADR 0004, ADR 0005, ADR 0006.

## Context

**What v1 decided and why it stalled.** v1 chose `SO_PEERCRED` for unix sockets, mTLS for TCP, and a policy file of principals. Each principal had an RPC allowlist and a `volume_prefix` scope. It stalled for four reasons:
1. Per-volume scoping must be enforced in every planner entry point. One that forgets fails open (v1's own "known risk").
2. Root pods all look like `uid=0`, so per-uid policy cannot tell the commonest front ends apart.
3. "Several clusters share one parent" contradicts Reconcile. Its desired set is complete per parent, so each cluster's GC would collect the other's volumes.
4. No remote front end exists. zfs-csi needs a hostPath to the socket, so it always runs on the storage host.

**Where "several clusters share one parent" came from (A1).** v1's own Context was its only source. That text was written by an agent in commit `2ed1800`. No owner statement exists in the repository or the working notes. This ADR therefore retires an agent's assumption, not an owner requirement, and that is why the owner was not asked about D1.

**What the wave 11 threat model found.**
- A pod given the socket directory `/run/zfs-endpoint` reached every instance's socket and lock file. It could block a restart by holding a lock, or replace a socket (F1).
- Every caller could call every RPC, including Reconcile and PublishFileExport, which zfs-csi never calls (F2).
- No peer identity was read or logged (F7).
- A crossmnt export of an ancestor exports every volume from creation onward (F5).
- journald rate limiting could drop audit lines (F8).
- An NFS client could plant setuid binaries that host users could run (F12).

## Decision

| D | Decision | Built |
|---|---|---|
| D1 | The tenancy unit is the parent dataset | yes (it is what the daemon already was) |
| D2 | Per-listener roles, not a policy file | yes |
| D3 | Reconcile requires `plan_digest` to collect | yes |
| D4 | Two-person Reconcile: deferred | n/a |
| D5 | TCP stays refused; mTLS is future work | yes (refused) |
| D6 | `SO_PEERCRED`: logged now, authorized later | logged; admin role checks uid |
| D7 | `-socket-group` for the csi listener | **no**: decided, not built |
| D8 | NFS stays static-first (ADR 0004); pod-egress deny is F6's primary control | docs and deployment |
| D9 | Reconcile skips exported volumes | **no**: decided, not built |
| D10 | A crossmnt ancestor at startup: refuse, exit 78 | yes |
| D11 | Audit: journald, unrated | yes |
| D12 | No `snapdir` at create; inherit from the parent | yes (nothing is set) |

### D1. The tenancy unit is the parent dataset

- Each tenant gets its own parent, instance and socket directory. A tenant is a cluster, a driver deployment, or a StorageClass family that must be isolated.
- Isolation comes from what OpenZFS and the kernel already enforce:
  - the dataset subtree;
  - the socket's directory permissions;
  - which hostPath the kubelet mounts into which pod.
- Nothing scopes volumes in-process. CSI ids, Reconcile's desired set and GetCapacity are all per parent, so they are correct by construction.
- One parent has exactly one container orchestrator. Reconcile's desired set is complete for the parent.
- This is reversible. `volume_prefix` scoping can be added later without changing anything outward, because no id encodes tenancy.

### D2. Roles per listener

| Role | Listener | Methods |
|---|---|---|
| `csi` | `-listen` | GetCapabilities, CreateVolume, DeleteVolume, ExpandVolume, GetVolume, ListVolumes, CreateSnapshot, DeleteSnapshot, ListSnapshots, GetCapacity |
| `admin` | `-admin-listen` (optional) | all of the above, plus Reconcile, PublishFileExport, UnpublishFileExport |

- The role belongs to the listener (`server.Daemon.Serve(lis, role)`). The handshake carries it into the connection's `server.PeerInfo`.
- **Unauthenticated:** a connection that came through no listener with a role.
- **PERMISSION_DENIED:**
  - a method the role does not hold;
  - a method missing from the table (`TestEveryMethodHasARole` fails when an RPC is added without a row);
  - every stream other than health.
- Health is served on every listener.
- **Admin callers:** SO_PEERCRED must show uid 0 or the daemon's own uid. This holds whatever mode the socket has been given.
- **Without `-admin-listen`,** Reconcile and the export RPCs are refused. The unit serves the admin role on `/run/zfs-endpoint/admin/%i/endpoint.sock`.
- **When CSI ControllerPublish lands,** `csi` gains Publish and Unpublish with a `/32` constraint (ADR 0004: node_id is one host).

### Socket layout (F1, F2; amends ADR 0006)

```
/run/zfs-endpoint/                  0700 root  per-parent locks; never mounted into a pod
/run/zfs-endpoint/sock/<escaped>/   0700 root  csi socket endpoint.sock (0600); mounted by the CSI pod
/run/zfs-endpoint/admin/<escaped>/  0700 root  admin socket endpoint.sock (0600); never mounted
```

- `<escaped>` is `systemd-escape <parent>`, which is the unit's `%i`.
- **Who creates what:**
  - systemd creates `/run/zfs-endpoint` (`RuntimeDirectory=zfs-endpoint`).
  - The daemon creates each socket directory 0700 itself (`unixsock.Listen`).
  - `%i` is kept out of `RuntimeDirectory=` on purpose. That keeps the escaped name in one place and avoids a second escaping rule.
- **The daemon refuses with exit 78:**
  - a listener whose directory is, or contains, the lock directory;
  - an admin socket whose directory is the csi socket's directory or under it.
- **Path budget:** sun_path leaves 70 bytes of escaped parent name for the csi socket and 69 for the admin socket.

### D3. Reconcile requires a digest to collect

- A run that collects any undesired volume must carry a `plan_digest` from a dry run. Without one it returns FAILED_PRECONDITION.
- The digest guards against the plan changing between the dry run and the apply. It is not an approval (F4).
- As built: "undesired" is the unlocked plan's `not in the desired set` actions. A run without a digest that would collect one applies nothing, not even the uncommitted rows and orphan clone sources in the same plan; a run whose plan holds only those needs no digest. The check sits before `max_deletions`. `TestReconcileNeedsADigestToCollectUndesired` proves it.

### D4. Two-person Reconcile: deferred

- A two-person rule means nothing until callers have distinct principals. Every admin caller is root on a 0600 socket.
- Build `ApproveReconcile` when either is true:
  - Reconcile is called by automation;
  - two admin principals exist (non-root `-allow-uids`, or mTLS).
- The rule is additive and was asked for by no one but the threat model.

### D5. TCP

- TCP stays refused.
- v1's mTLS design applies to a TCP listener with a role once a remote front end exists:
  - TLS 1.3 and `RequireAndVerifyClientCert`;
  - exactly one URI SAN as the principal;
  - an explicit CA or a SPIFFE `X509Source`;
  - reload on SIGHUP plus an mtime poll;
  - `MaxConnectionAge`.
- Nothing of it is built.

### D6. Peer credentials: logged, then authorized

- `server.peerCreds` is the server's TransportCredentials. It reads SO_PEERCRED on each unix connection.
- Every rpc log line carries `role`, `peer_uid`, `peer_gid` and `peer_pid` (F7).
- These are the credentials in effect at connect(2), translated into the daemon's user namespace, so every uid-0 container reads as uid 0.
- **Authorization by uid:**
  - today only the admin role checks it (uid 0 or the daemon's uid);
  - an optional per-listener `-allow-uids` comes later;
  - once uids are authorized, `MaxConnectionAge=1h` bounds how long a revoked uid keeps working.

### D7. `-socket-group` (not built)

- `-socket-group` sets the csi socket to 0660 with that group. The controller pod can then run non-root with `supplementalGroups`.
- Without the flag, the socket stays 0600 root, as it is today.
- The admin socket stays 0600 root either way.

### D8. NFS: static-first stays; pod-egress deny is F6's primary control

- ADR 0004's ordering stands: a static StorageClass CIDR at create comes first. The owner has already answered this question.
- **Per-node `/32` stays later,** behind a mode flag, with these prerequisites:
  - a stale-`/32` GC against VolumeAttachments. external-attacher resolves node_id from the current CSINode, so after an IP change the old `/32` would stay exported forever;
  - a documented csi-driver-nfs `--nodeid` IP override (its shipped node id is a hostname);
  - refusing hostname node ids.
- **F6 (AUTH_SYS plus a CIDR ACL) is closed by deployment,** in both modes:
  - a cluster-scoped deny of pod egress to the storage host's 2049/tcp (and 111 and 20048 for NFSv3);
  - Pod Security baseline or stricter on tenant namespaces;
  - a host firewall limiting 2049 to the node subnet.

  Mounts use host networking, so they are unaffected.
- Static mode keeps `nfs.clients` required, with no default CIDR, plus the AUTH_SYS warning.
- **Revisit when** krb5p lands, or when a deployment cannot enforce pod-egress policy.

### D9. Reconcile skips exported volumes (not built)

An undesired volume whose `sharenfs` is not `off`, or whose client list is not empty, is skipped as "exported: in use" (F3). `sharenfs` is OpenZFS's own in-use signal. A false skip only delays GC.

### D10. A crossmnt ancestor refuses startup

- At startup the daemon reads the parent's mountpoint and etab (`endpoint.ExposedByAncestor`).
- It exits 78 when a crossmnt line covers the mountpoint or any ancestor, and the message names the lines.
- Typical causes are `zfs set sharenfs=on` on the pool (libshare writes that as `rw,crossmnt` to `*`) or a crossmnt line in `/etc/exports`.
- A warning would leave every volume exported.
- Health does not re-check it yet.

### D11. Audit

- The unit sets `LogRateLimitIntervalSec=0`, so a flood of cheap calls cannot push destroy lines out of journald (F8).
- Shipping logs off the host is a deployment recommendation, and `Seal=yes` is optional. Callers are root, so tampering is out of scope.
- **Not built:** the server always minting `request_id`, with the caller's id logged as `client_request_id` (F14).

### D12. No `snapdir` at create

- The endpoint sets nothing, so volumes inherit `snapdir` from the parent. That is OpenZFS's own delegation of the choice to the operator, per tenant parent.
- Setting it locally at create would override an operator's parent-level choice.
- **Documented:**
  - `hidden` is not the same as inaccessible: `.zfs/snapshot/<name>` stays reachable by path over NFS;
  - set `snapdir=disabled` on the tenant parent where erasure or compliance matters;
  - `zfs_admin_snapshot` must stay 0.
- A StorageClass may still set `snapdir` through the existing allowlist.

### Mount options at create (F12)

- Filesystem volumes are created and cloned with `setuid=off`, `exec=off` and `devices=off` set locally, next to `sharenfs=off` and `sharesmb=off`.
- These are server-side mount options. NFS clients are unaffected.
- Unlike `snapdir`, they are stamped rather than inherited. Each guards the storage host itself against what a tenant's client writes, not a tenant-visible behaviour.
- A StorageClass cannot set them.
- They hold only while pods reach filesystems over NFS (ADR 0004). A node plugin that bind-mounts the host's ZFS mount into a pod would inherit `nosuid`, `noexec` and `nodev`, so pods could not run programs from their volume. Such a plugin must revisit this decision before it ships.

### Systemd hardening (H-16)

- `deploy/systemd/zfs-endpoint@.service.d/hardening.conf` turns on only `RestrictNamespaces`, `KeyringMode=private` and `LimitCORE=0`.
- These settings ship commented out until the real-ZFS VM leg runs the suite under the unit:
  - the capability bounding set;
  - `ProtectClock` with the device allow-list (it implies `DeviceAllow=char-rtc`, which would refuse `/dev/zfs`);
  - `ProtectHostname`;
  - the syscall deny-list;
  - the memory limits.
- No setting may create a mount namespace or cut the network. `deploy/deploy_test.go` `TestUnitLint` enforces this.

## Consequences

- **More instances.** Tenants that would have shared a parent now get one each: more units, sockets and datasets. Capacity is still shared at the pool level, so set `quota` on each tenant parent.
- **The zfs-csi deployment** mounts `/run/zfs-endpoint/sock/<escaped>` only, and dials `unix:///run/zfs-endpoint/sock/<escaped>/endpoint.sock`.
- **Operators** call Reconcile and the export RPCs on the admin socket, as root.
- **A compromised controller pod** is confined to its parent and to the `csi` methods. It loses four things: export to itself, mass Reconcile, lock DoS and socket replacement.
- **No policy file,** so there is no source of truth outside ZFS to keep in sync across an HA pair.
- **Shared parents need a new ADR.** Several container orchestrators on one parent need scoped Reconcile, which is deferred.
- **Startup is stricter.** A host that exports a pool with `sharenfs=on` no longer starts an endpoint under it.

## Alternatives considered

| Alternative | Why not now |
|---|---|
| v1: a policy file with principals, RPC allowlists and `volume_prefix` | Enforcement is split and fails open. It is incompatible with desired-set Reconcile, and root pods cannot be told apart. It costs the most and adds a source of truth outside ZFS. |
| One socket, authorized by SO_PEERCRED uid only | Every root pod is uid 0. It does not stop lock or socket tampering through a shared directory. |
| mTLS on the unix socket as well | Every local front end would need a cert, and it does not fix directory sharing. |
| A separate privileged helper daemon for destructive operations | Two root daemons, a second protocol, and drift between them. Listener roles give the same separation in one process. |
| A keyed approval token (HMAC) on the digest | It needs key storage and still does not prove a second person. Deferred with D4. |
| Per-node `/32` as the NFS default | It leaves stale exports after node IP churn, needs a re-templated third-party node plugin, and no comparable ZFS driver ships it as the default (D8). |

## Testing (ADR 0005: the lowest level that proves it)

- **`internal/server`:**
  - `TestEveryMethodHasARole`;
  - `TestRolesPerListener`, over bufconn: role × method;
  - `TestAdminPeerIsRootOrTheDaemonsUser`;
  - `TestPeerCredentialsAreLogged`, on a real unix socket.
- **`cmd/zfs-endpoint`:**
  - `TestAdminRPCsOnlyOnTheAdminSocket`;
  - `TestAdminSocketRefusals`;
  - `TestSocketNeverSharesTheLockDirectory`;
  - `TestStartupRefusesACrossmntAncestor`.
- **`internal/endpoint`:**
  - `TestExposedByAncestor` (pure);
  - `TestFilesystemsMountWithoutSetuidExecOrDevices`;
  - the golden create plans.
- **`deploy`:**
  - `TestUnitServesTheConventionalSocket` (csi and admin);
  - `TestManifests` (no pod mounts the admin directory);
  - `TestUnitLint`.
- **Still owed to the real-ZFS VM job:**
  - a child running as uid 65534 cannot open the 0600 admin socket;
  - the hardening drop-in's commented settings, verified under the unit.
