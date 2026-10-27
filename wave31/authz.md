# Wave 31: authorization fail-closed review (zfs-endpoint, zfs-csi)

Read-only review at 08ca83f. Scope: internal/server, internal/unixsock, cmd/zfs-endpoint, internal/csi, deploy/systemd, deploy/k8s, ADR 0001 v2.

## Verdict
On Linux, as shipped, no path was found that gives a caller a role the listener did not grant. The one fail-open is latent: the admin uid check passes when credentials are missing. It is unreachable on Linux today, but nothing pins that. Separately, the CSI role can export data through CreateVolume, which ADR 0001 says it cannot.

## Where credentials can be missing
- The handshake marks credentials as read (HasCred=true) only when the unwrapped connection is a `*net.UnixConn` and `peerCred` succeeds: `internal/server/peercred.go:40-44`. Otherwise it still returns the connection with HasCred=false, so nothing is refused at the handshake.
- The role comes from the listener (`roles.go:170-192`). A connection with role 0 gets Unauthenticated (`roles.go:207-208`).
- Without credentials:
  - CSI role: unaffected. It never checks the uid (by design, ADR D6). All 10 CSI RPCs are reachable.
  - Admin role: `adminPeer` returns true when `!p.HasCred` (`roles.go:223-225`), so all 13 RPCs are reachable, including Reconcile, PublishFileExport and UnpublishFileExport. Only the 0600 socket mode and the 0700 directories stand in the way.
- Can credentials go missing in production? On Linux, not in practice:
  - `unixsock.listener` embeds `*net.UnixListener`, so Accept returns a `*net.UnixConn` (`unixsock.go:132-135`). `roleConn` is unwrapped before the type check (`peercred.go:37-39`).
  - SO_PEERCRED on an accepted AF_UNIX stream socket is set at connect(2) and does not fail.
  - Any build that is not Linux always fails the read (`peercred_other.go:9-11`), which gives admin to anyone who can open the socket.
- What ADR 0001 v2 says: D2 (`docs/adr/0001-transport-auth.md:65`) requires that "SO_PEERCRED must show uid 0 or the daemon's own uid ... whatever mode the socket has been given". The code adds an unwritten exception ("no creds: listener role stands", `roles.go:219-222`), and a test pins it (`roles_internal_test.go:35`, `{PeerInfo{}, 0, true}`). **Code does not match the ADR.**

## Findings
### M1 (medium, latent): the admin uid check fails open when credentials are missing
- Evidence: `roles.go:213`, `roles.go:223-225`, `peercred.go:40-45`, `peercred_other.go`, `roles_internal_test.go:35`.
- Exposure: none on Linux today. It becomes reachable if:
  - the admin listener is ever wrapped (a connection limiter, a proxy, a test helper reused in production); or
  - the daemon is built for a platform other than Linux.

  In either case the 0600 mode is the only gate, and it is exactly what D2 says must not be the only gate.
- Minimal fix, part 1: `return p.HasCred && (p.UID == 0 || p.UID == euid)`.
- Fix, part 2 (optional, stronger): in `ServerHandshake`, when the raw connection is a `*net.UnixConn` and `peerCred` errors, return the error so the connection is refused on every role.
- Test changes:
  - flip the row to `{PeerInfo{}, 0, false}`;
  - in `TestRolesPerListener` (`roles_test.go:63`), expect PermissionDenied on the bufconn admin listener, with health still SERVING. That pins fail-closed over the wire.
- New test `TestAdminSocketCarriesCredentials`:
  - serve `unixsock.Listen(t.TempDir()+...)` with `RoleAdmin`;
  - call Reconcile with dry_run;
  - assert it is served and the rpc line has `peer_uid`.

  Today only a `net.Listen` socket with RoleCSI is checked that way (`peer_test.go:66-85`). The renamed admin listener is not.
### M2 (medium, design gap): the CSI role can export any volume's data to a network it chooses
- Evidence:
  - CreateVolume passes `nfs_export.clients`, `uid`, `gid` and `mode` from the caller (`server.go:412-419`, `server.go:477-485`);
  - the export is set in the same `zfs create` "(or clone)" (`internal/endpoint/endpoint.go:107-108`, `plan.go:440`);
  - CreateVolume is in the CSI role (`roles.go:143`);
  - the only bound on the network is `-nfs-min-prefix`, /16 by default (`endpoint/export.go:92`, `main.go:100`).
- Exposure: a compromised controller can clone any volume or snapshot under the parent and export the clone read-write to any IPv4 network of /16 or narrower. ADR 0001 Consequences (`:219`) says a compromised controller "loses ... export to itself". It loses only the ability to widen an existing export (PublishFileExport is admin-only). D8 limits the damage: pods are denied egress to 2049, and the host firewall allows only the node subnet.
- Minimal fix, either:
  - an endpoint-side allowlist (for example `-nfs-allowed-networks`) that CreateVolume and Publish clients must fall inside, so the operator bounds the networks rather than the caller; or
  - correct the ADR to say the CSI role keeps export-at-create.
- Test: in `internal/endpoint`, a CreateVolume clone whose clients fall outside the allowlist returns InvalidArgument; plus a row in `TestRolesPerListener` with clients outside the allowlist on the CSI listener.
### L1 (low): the health exemption matches by prefix
- Evidence: `roles.go:195` (`strings.HasPrefix(fullMethod, "/grpc.health.")`) skips authorization (`roles.go:199`), the stream deny (`roles.go:230`) and the audit line (`daemon.go:183`).
- Exposure: none today, since only `grpc.health.v1.Health` is registered (`daemon.go:71`). A service registered later under that prefix would bypass every role and the audit log.
- Minimal fix: build an exact set of names from `healthpb.Health_ServiceDesc` (its Methods and Streams).
- Test: `isHealth("/grpc.health.v2.X/Y") == false`; every name in `Health_ServiceDesc` returns true.
### L2 (low, availability): publishing and removing the socket can hit another daemon's socket
- Evidence:
  - Lstat, then dial, then Remove, then `bindPrivate`, then `os.Rename` (`unixsock.go:69-85`, `unixsock.go:123`): the rename silently replaces a socket another process placed in the meantime;
  - `listener.Close` unlinks `path` without checking that it is still its own socket (`unixsock.go:139-145`).
- Exposure: only when two instances are misconfigured with one `-listen` path but different parents. The parent lock is per parent (`lockparent.go:59`). The later instance takes the path, and the earlier one's shutdown deletes it. This is not an authorization bypass, because the directory is 0700 root.
- Minimal fix:
  - publish with `os.Link(tmp, path)`, which fails with EEXIST and never replaces, then unlink tmp;
  - record dev/ino after publishing, and have Close remove `path` only if it still matches.
- Test: two concurrent `Listen` calls on one path, exactly one succeeds. Closing a listener whose path was replaced leaves the replacement in place.
### L3 (low): SecureDir checks only the last directory, not its ancestors
- Evidence: `unixsock.go:262-303`: Lstat and the owner/mode checks apply to `dir` only.
- Exposure: with the defaults there is none, because `/run/zfs-endpoint` is 0700 (`zfs-endpoint@.service:73-76`) and the daemon creates `sock/` and `admin/` 0700. If an operator sets `-admin-listen` under an ancestor that another user can write and that has no sticky bit, that user could rename the socket's directory and plant a replacement.
- Minimal fix: walk up to `/` and require every ancestor to be owned by root or the daemon's euid, and not writable by group or others unless sticky.
- Test: an ancestor under `t.TempDir()` with mode 0777 makes `Listen` fail; with 1777 (sticky) it is allowed.
### L4 (low, defense in depth): the daemon keeps every root capability
- Evidence:
  - the unit has no `User=` or `CapabilityBoundingSet=` (`zfs-endpoint@.service:46-123`);
  - `hardening.conf` is opt-in ("Install as", `hardening.conf:1-3`), and its `CapabilityBoundingSet` and device allow-list ship commented out (`hardening.conf:49-62`);
  - the syscall deny-list exists only in that drop-in (`hardening.conf:38-39`).
- What the unit already does well: `NoNewPrivileges`, `RestrictSUIDSGID`, `MemoryDenyWriteExecute`, `UMask=0077`, `RestrictAddressFamilies`. The reconcile client unit is tight: `CapabilityBoundingSet=` empty, `AF_UNIX` only, `ProtectSystem=strict` (`zfs-endpoint-reconcile@.service`).
- Minimal fix: in the main unit, ship a deny-form bounding set first. It is lower risk than an allow-list:
  `CapabilityBoundingSet=~CAP_SYS_MODULE CAP_SYS_PTRACE CAP_SYS_BOOT CAP_SYS_TIME CAP_BPF CAP_PERFMON CAP_SYSLOG CAP_NET_RAW CAP_MAC_ADMIN CAP_MAC_OVERRIDE CAP_LINUX_IMMUTABLE CAP_WAKE_ALARM`
  Then move the syscall deny-list from the drop-in into the unit once the VM leg passes with it.
- Test: `TestUnitLint` requires a `CapabilityBoundingSet` that excludes CAP_SYS_MODULE and CAP_SYS_PTRACE. `ci/real-zfs-unit.sh` already names the failing setting.

## Info (checked; no change needed)
- **I1, socket creation is race-free:**
  - the socket is bound in a fresh `MkdirTemp` 0700 directory, set to 0600, then renamed into place (`unixsock.go:97-128`);
  - the umask is never touched (`TestListenLeavesUmaskAlone`);
  - a symlink or other non-socket at the path is refused, never deleted (`:69-75`, `TestListenNeverDeletesANonSocket`);
  - a symlinked or foreign-owned directory is refused (`:284-302`, `TestListenRefusesAnUnsafeDirectory`);
  - stale-socket removal uses unlink, which does not follow symlinks, after Lstat;
  - abstract and relative paths are refused (`CheckPath :229-240`).
- **I2, ShareWithGroup only ever widens, and only before serving:** it runs after Listen and before Serve (`main.go:232` vs `:253`), so the socket only goes from 0600 to 0660. A failure closes the listener (`main.go:514-517`).
- **I3, `-socket-group`:**
  - it is applied to the CSI socket only (`main.go:509-519`); the admin socket always goes through `unixsock.Listen` and stays 0600 (`main.go:238`);
  - the admin check is by uid and ignores the group (`roles.go:223-225`), so a pod with that supplementary group reaches the CSI role at most, and only if it gets the socket directory's hostPath;
  - host users in the group cannot get through `/run/zfs-endpoint` (0700), so ADR D7's "to reach the socket from the host" (`:130`) is moot without a mount;
  - gid 0 is accepted (`admin_test.go` TestSocketGroup "0"). Consider refusing it as wider than intended;
  - neither the unit nor the manifests set the flag.
- **I4, the role map denies by default:**
  - a method missing from the map is allowed to nobody (`roles.go:141-165`);
  - streams other than health are denied (`:229-234`); role 0 gets Unauthenticated (`:207`);
  - an unregistered method never reaches the interceptor (gRPC answers Unimplemented);
  - no reflection, channelz or admin service is registered (no match in `internal/` or `cmd/`);
  - Reconcile and both export RPCs are admin-only.
  - Covered by `TestEveryMethodHasARole`, `TestRolesPerListener`, `TestStreamInterceptorServesHealthOnly`, `TestAuthorizeRefusesAdminPeerOfAnotherUID` and `TestAdminRPCsOnlyOnTheAdminSocket`.
- **I5, log injection:**
  - both binaries log through `slog.NewJSONHandler`, which escapes control characters (`cmd/zfs-endpoint/main.go:31`, `cmd/zfs-csi/main.go:35`);
  - `x-request-id` must match `[A-Za-z0-9._-]{1,64}` or a new one is generated (`daemon.go:283-306`, `csi/log.go:87-111`; covered by `TestRequestIDReachesTheAuditLog`);
  - ids are clipped to 256 bytes (`server.go:493`, `server.go:541`) and error messages to 16 KiB (`server.go:364`);
  - residual: a caller can reuse another call's request id (correlation spoofing; ADR D11 F14, "not built"). zfs-csi logs volume and PVC fields unclipped (`csi/log.go:56-83`), which affects line size only.
  - Suggested test: a volume id with a newline still produces exactly one JSON line.
- **I6, uid 0 equivalence:** every uid-0 container reads as uid 0 (`peercred.go:12-14`), so the admin uid check adds nothing against root pods. The real gate is deployment:
  - no pod mounts `/run/zfs-endpoint/admin` (`deploy_test.go:317-320`);
  - the CSI socket directory is mounted readOnly (`controller.yaml:123-125`, pinned at `k8s_test.go:208`), so the pod cannot unlink or plant a socket there;
  - Pod Security keeps hostPath out of tenant namespaces (D8).

## Test coverage
- The uid refusal is unit-tested on a synthetic PeerInfo (`roles_internal_test.go:45-76`), so removing the uid check is caught even though the Go tests run as root.
- Refusal of a real non-root peer over a socket is covered only by the VM job (`ci/real-zfs-unit.sh:114-129`, uid 65534 via setpriv).
- Gap: no Go test covers it. Suggested: as root, re-exec the test binary with `SysProcAttr.Credential{Uid: 65534}` against an admin socket opened to others, expect PermissionDenied, and skip when not root.
- Gap: the missing-credentials row is pinned to the fail-open result (see M1).
