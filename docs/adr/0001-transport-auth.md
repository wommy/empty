# 1. Transport authentication and per-caller authorization

Status: proposed · Date: 2026-10-27 · Supersedes the "socket is 0600 until transport auth lands" stopgap in `cmd/zfs-endpoint/main.go`.

## Context

- The daemon manages one parent dataset. It serves `EndpointService` (10 RPCs) over gRPC.
- **Unix listener**: created under `umask 0177`, so only the daemon's uid can connect. A front end that runs as any other uid cannot reach it.
- **TCP listener** (`-listen host:port`): plain `net.Listen` with no credentials at all. Anyone who can reach the port can destroy every volume under the parent. This is a live hole, not a missing feature.
- Front ends: the CSI controller (`cmd/zfs-csi` on branch `csi-frontend`) dials with `insecure.NewCredentials()`. It may run on the same host over the socket, or remote over TCP. Several clusters or drivers may share one parent and must not touch each other's volumes.
- No surveyed project combines per-caller identity with per-RPC dataset scoping (`docs/research/edges2/transport-auth.md`, GAPS).
- Agentless SSH mode (mode b in that note) is out of scope here.

## Decision

**1. One authentication mechanism per listener kind. No unauthenticated mode.**

| Listener | Authenticates by | Principal string |
|---|---|---|
| `unix://` | `SO_PEERCRED` read in a custom gRPC `TransportCredentials.ServerHandshake` on the `*net.UnixConn` | `unix:uid=<n>` (`gid` and `pid` are logged, not authorized) |
| `tcp` | mTLS, TLS 1.3 minimum, `RequireAndVerifyClientCert`. The client cert must carry **exactly one URI SAN**. | that URI, e.g. `spiffe://example.org/csi/cluster-a` |

- The socket becomes mode `0660`, owned by a configured `-socket-gid`. Filesystem permissions are the first gate and peer credentials are the second, as with containerd's gRPC socket plus its ttrpc `UnixSocketRequireSameUser` check.
- The daemon refuses to start in either of these cases:
  - `-listen` is TCP and no TLS source is configured.
  - The policy file is missing, unreadable or invalid.
- The CN is never an identity, and DNS SANs are ignored. SPIFFE and plain x509 (for example cert-manager) produce the same principal shape, so one policy covers both.
- TLS has two sources:
  - **SPIFFE.** `workloadapi.NewX509Source` with `tlsconfig.MTLSServerConfig`. The trust bundle and SVID come from the SPIRE agent.
  - **Files.** Cert, key and CA bundle paths. The trust anchors are an explicit CA file, never the system roots.

**2. Authorization is a static policy of principals, each with an RPC allowlist and a volume scope.**

```json
{"principals": [
  {"id": "spiffe://example.org/csi/cluster-a", "rpcs": ["*"], "volume_prefix": "a-"},
  {"id": "unix:uid=0", "rpcs": ["GetCapabilities", "ListVolumes", "GetCapacity"], "volume_prefix": "*"}
]}
```

- The default is deny: an unknown principal, an unlisted RPC or an empty policy is refused.
- `volume_prefix` is required. Full access is written as `"*"`, never as an empty string.
- The scope is a prefix on the volume id, which is the dataset component under the parent. A snapshot is in scope when its source volume is.
  - CreateVolume checks the name and any `source_volume_id` or `source_snapshot_id`.
  - Cloning from another tenant's snapshot is denied.
- **Where it is enforced:**
  - A unary and stream interceptor authenticates the caller and checks the RPC allowlist.
  - The resolved scope goes into the request context. The endpoint enforces it after the scan, where rows are known. This is required because snapshot ids may not reveal their volume without a lookup (ADR 0002).
  - Rows out of scope are filtered from `List*`. Get, mutate or delete on one returns `PERMISSION_DENIED`. It never returns "missing, so OK": a DeleteVolume that reported success would make the CO forget a volume it does not own.
- The policy reloads on SIGHUP. A reload that fails validation keeps the previous policy and logs an error.

**3. Fail closed everywhere.**

| Situation | Result |
|---|---|
| No `AuthInfo`, or an unknown `AuthInfo` type | `UNAUTHENTICATED` |
| A non-unix conn reaches the unix handshaker | `UNAUTHENTICATED` |
| A TLS conn without exactly one URI SAN | `UNAUTHENTICATED` |
| Not allowed by the policy | `PERMISSION_DENIED` |

- A table maps every method in `EndpointService_ServiceDesc` to its scope extractor. A test fails when a new RPC has no entry, and the interceptor denies any method missing from the table.

**4. Rotation and revocation.**

- SPIFFE: the SVID and trust bundle rotate inside `X509Source`, read per handshake through `GetCertificate`. No restart is needed.
- Files are reloaded on SIGHUP and on an mtime poll every 30 s:
  - The cert and key are parsed and paired before an atomic swap. On failure the old pair stays in use.
  - The CA file may hold old and new roots during a rollover.
  - Expiry under 7 days is logged at warn.
- Identity is fixed at handshake, both for TLS and for `SO_PEERCRED`. socket(7) says the credentials are those "in effect at the time of the call to connect(2)". So the server sets `keepalive.ServerParameters{MaxConnectionAge: 1h, MaxConnectionAgeGrace: 1m}`. That bounds how long a revoked cert, a removed policy entry or a dropped uid keeps a live connection. Policy changes apply per call, because the interceptor reads the current policy.

**5. Testing.**

- **Pure authorizer.** A table test of `(principal, method, request) → allow | deny` covering every RPC, prefix edges (`a-` against `a`, `ab-`), clone sources across scopes, and `"*"`.
- **Interceptor over `bufconn`.** A fake `TransportCredentials` injects each `AuthInfo` variant. bufconn's address network is `"bufconn"`, and it has no fd, so it cannot carry peer credentials. The peer-credential handshaker is therefore not tested here.
- **Real unix socket in `t.TempDir()`.**
  - Allowed uid: OK.
  - Allowlist excluding `os.Getuid()`: `UNAUTHENTICATED`.
  - In the root CI job (KVM, real ZFS), a child process connects with `SysProcAttr.Credential{Uid: 65534}` and is denied.
- **Real TCP on `127.0.0.1:0`** with certs minted in the test (`crypto/x509`). Cases:
  - no client cert, wrong CA, expired cert, a cert with zero or two URI SANs, and an unknown SPIFFE ID;
  - cert rotation picked up without a restart;
  - `MaxConnectionAge` forcing a new handshake that then fails after revocation.
- The `zfs-csi` client gains the matching client config: mTLS with server-ID authorization (`tlsconfig.AuthorizeID`). Its sanity run uses the unix path with peer credentials on.

## Consequences

- TCP is usable only with a PKI, either SPIRE or files plus a CA. The cost is accepted: an unauthenticated TCP listener is the current hole.
- **Root pods look alike.** The uid seen through `SO_PEERCRED` is translated into the daemon's user namespace, so every uid-0 container on the host is `unix:uid=0`. Tenants that share a host socket must run as distinct uids, or use mTLS over TCP.
- Scope checks move partly into the endpoint core. The core gains a `Scope` value, and each planner entry point filters by it. This adds code to "server only translates", for a reason.
- **The snapshot-name oracle.** CSI snapshot names are global per endpoint (`plan.go` PlanCreateSnapshot). A tenant can learn that another tenant's name exists from `ALREADY_EXISTS`, but cannot read or touch that snapshot.
- Audit logging gains a principal on every call. `observability-audit.md` can key on it.

## Alternatives considered

| Alternative | Why rejected |
|---|---|
| Filesystem permissions only (Kubernetes CSI sidecars, Docker plugins, containerd's gRPC socket) | There is no caller identity, so there is no per-tenant scope or audit. It is fine for one front end per socket, which is the CSI sidecar's situation, but not for a daemon that several clusters share. Kept as the first gate. |
| grpc-go `credentials/local` | It marks UDS as `PrivacyAndIntegrity` but exposes no uid (`local.go:69-99`), so there is nothing to authorize. |
| Bearer API keys, as in TrueNAS | They need key storage, hashing and revocation in the daemon. They are replayable if logged, and TrueNAS auto-revokes a plain key sent over insecure transport, which shows the hazard. mTLS binds the key to the channel and needs no daemon-side secret store. |
| mTLS on the unix socket as well | One mechanism would be uniform. But every local front end would need a cert, which is heavy for the common single-host case. It can be added later as a listener option without changing the policy model. |
| Authorizing on the TLS CN or a DNS SAN | The CN is deprecated for identity. A DNS SAN names a host, not a workload. A SPIFFE ID is one URI SAN by spec. |
| SPIFFE-only, with no file-based certs | It would force SPIRE on every deployment. cert-manager users get the same principal shape with a URI SAN. |
| Workload attestation by PID (as SPIRE's agent does) | PID reuse is a race. SPIRE needs dedicated machinery to make it safe (`peertracker`). uid is enough for our granularity. |

## Evidence (pins as resolved by GitHits on 2026-10-27)

- **This repo.** `cmd/zfs-endpoint/main.go` `listener()`: TCP has no credentials, unix uses `Umask(0o177)`. On branch `csi-frontend`, `cmd/zfs-csi/main.go:40` dials with insecure creds.
- **TrueNAS** (`truenas/middleware@3ffe599a`):
  - `utils/origin.py:78` reads `SO_PEERCRED` as `(pid, uid, gid)` on its unix socket.
  - `plugins/auth.py:1134-1170`: a non-interactive uid 0 becomes a root session, and other uids are mapped to a user through PAM.
  - `plugins/auth_/login_ex_impl.py:251-261` revokes a plain API key that was sent over insecure transport.
  - `main.py:2032-2045` creates `middlewared.sock` with mode `0666`; authorization is by peer credentials.
- **Kubernetes CSI.**
  - `kubernetes-csi/csi-lib-utils@76e421af connection/connection.go:170` uses `insecure.NewCredentials()` ("usually local Unix domain socket").
  - The kubelet does the same at `kubernetes/kubernetes@dd410c48 pkg/volume/csi/csi_client.go:553-559`.
- **containerd** (`containerd/containerd@c0ed7418`):
  - `pkg/sys/socket_unix.go:46-62` sets the gRPC socket to `0660` with a configured uid and gid.
  - `pkg/shim/shim_linux.go:25` and `plugins/server/ttrpc/server_linux.go:26` use the handshaker `UnixSocketRequireSameUser()`.
  - `containerd/ttrpc@d2ddf049 unixcreds_linux.go:47` calls `GetsockoptUcred(..., SO_PEERCRED)`. Lines 70-91 hold the connect-time caveat and the `RequireUidGid` and `RequireRoot` helpers.
- **Docker** (`moby/moby@4237bc6c`):
  - `daemon/listeners/listeners_linux.go:39-42` and `group_unix.go:12` give the API socket the group `docker`. No per-caller authorization exists without authz plugins.
  - `daemon/daemon_linux.go:31` sets the plugin socket directory to `/run/docker/plugins`.
- **SPIFFE.**
  - `spiffe/spire@a33d43d3 pkg/common/peertracker/uds_linux.go:10`: the Workload API itself authenticates callers with `SO_PEERCRED`.
  - `spiffe/go-spiffe@77c6d2e0`:
    - `spiffetls/tlsconfig/config.go:112-128`: `RequireAnyClientCert`, plus a `GetCertificate` that reads from the source, which is how rotation works.
    - `authorizer.go:20-38`: `AuthorizeID`, `AuthorizeIDPrefix` and `AuthorizeMemberOf`.
    - `spiffegrpc/grpccredentials/credentials.go:45`.
    - `workloadapi/x509source.go:31`.
- **gRPC** (`grpc/grpc-go@529d6e86`):
  - `credentials/local/local.go:69-99`: UDS gives a security level only, no uid.
  - `test/bufconn/bufconn.go:316-317`: the address network is `"bufconn"`.
- **Prior research.** `docs/research/edges2/transport-auth.md`: zrepl uses TLS with the client CN as identity, Mayastor's `Auto` TLS skips verification, and targetd uses shared basic auth.

## Known risks (self-review)

- **Scope enforcement is split.** The interceptor checks the RPC and the endpoint checks the scope. If a new endpoint entry point forgets the scope filter, it fails open. Mitigation: the scope is a required argument, not optional context. The method-table test also asserts a scope-test case exists per RPC.
- **Root pods share one identity.** A shared host socket with several uid-0 front ends gives them the same principal. That is a deployment rule the daemon cannot enforce. It can only warn when `unix:uid=0` has a scope other than `"*"`.
- **Long-lived streams.** `MaxConnectionAge` closes connections gracefully, so an in-flight call finishes under the old identity, up to the grace period.
- **The policy file is a new source of truth** that is not in ZFS. It drifts if it is edited on one host of an HA pair and not the other. HA failover must carry it.
- **`GetCapacity` is parent-wide.** It leaks other tenants' usage in aggregate. Accepted.
- **Unverified here:** the exact `SO_PEERCRED` uid seen across user namespaces with idmapped hostPath mounts. Test it before relying on per-uid tenants in rootless or userns pods.
