# Transport and auth: CSI front end to endpoint daemon

## EDGES
- **Mode (a), daemon installed:** gRPC over TCP from the CSI controller to the daemon. Crosses: client identity, server identity, and the per-RPC operation and target.
- **Mode (b), agentless:** SSH from the CSI pod to the host, then a remote command runs the endpoint logic. Crosses: SSH key identity, host key, and a command string the remote shell parses.
- **Baseline risk:** democratic-csi joins name and args with spaces and does not escape them. `github:democratic-csi/democratic-csi@69c7ebcd src/utils/zfs.js:1622-1676` (`ZfsSshProcessManager.buildCommand`, "TODO: escape the arguments") and `src/utils/zfs_ssh_exec_client.js` (`SshClient.buildCommand`). Volume names reaching `sh -c` are an injection surface.

## SEAMS
- **zrepl ssh+stdinserver (MIT):** identity is the socket name. The admin's `authorized_keys` maps PUBKEY to CLIENT_IDENTITY, and the daemon trusts that mapping. `github:zrepl/zrepl docs/configuration/transports.rst:276-309`; `internal/transport/ssh/serve_stdinserver.go:19-53`. Cut: forced-command, where the key's `command=` option pins the endpoint binary.
- **zrepl tls (MIT):** `tls.Config{ClientAuth: RequireAndVerifyClientCert}`, identity is the client cert CN. `github:zrepl/zrepl internal/tlsconf/tlsconf.go:43-49`; `docs/configuration/transports.rst:90-91`.
- **targetd (GPL-3.0):** HTTP Basic auth over TLS with one shared user and password. `github:open-iscsi/targetd@e8affc39 targetd/main.py` (`TargetHandler.do_POST`, `load_config`, `TLSHTTPService`). Weak fit: no per-client identity.
- **LINSTOR (GPL-3.0):** controller and satellite use Java keystore and truststore over SSL, TLSv1.2 default. `github:linbit/linstor-server@e1d99946 satellite/.../SatelliteNetComInitializer.java:112-122`; `controller/.../ControllerNetComInitializer.java:347-351`.
- **Mayastor (Apache-2.0):** gRPC with `Auto` (ephemeral self-signed, verification bypassed) or `Files` (CA-verified, mTLS when configured, reloaded on rotation). `github:openebs/mayastor@1e796a58 io-engine/src/grpc/tls.rs:25-42`. Avoid Auto.
- **Go SSH (BSD-3):** `Client.NewSession` opens a new channel on one reusable connection, so reuse the `Client`. `github:golang/crypto@b853e4b6 ssh/client.go:160-165`. `HostKeyCallback` is mandatory; pin with `knownhosts.New`. `ssh/knownhosts/knownhosts.go:426-444`. Never use `InsecureIgnoreHostKey`.
- **Go SPIFFE (Apache-2.0):** `tlsconfig.MTLSServerConfig(svid, bundle, tlsconfig.AuthorizeID(id))` with SVID rotation from SPIRE. `github:spiffe/go-spiffe@77c6d2e0 examples/spiffe-http/server/main.go:46-47`; `spiffegrpc/grpccredentials/credentials.go:45-47`.

## Recommendation (gRPC, mode a)
Use **SPIFFE/SPIRE** with go-spiffe. Each front end gets an SVID such as `spiffe://<trust>/csi/<cluster>/<driver>`. The daemon authorizes by SPIFFE ID, then authorizes each RPC against a per-ID policy: allowed dataset prefix and operations, so one front end cannot touch another's volumes. Without SPIRE, use **cert-manager** to issue per-front-end client certs with a unique SAN URI, and apply the same allowlist. Avoid shared basic auth (targetd) and unverified auto-TLS (Mayastor Auto).

## Recommendation (agentless, mode b)
Use the zrepl pattern: one SSH key per front end, `authorized_keys` line with `command="<endpoint> --identity=<name>"` and `no-pty,no-port-forwarding`. Send requests as typed data on stdin, not a shell string. If a shell must run, validate names against a strict regex and quote them. Pin host keys via knownhosts. Reuse one `ssh.Client` per host.

## GAPS
- No surveyed project combines per-front-end identity with per-RPC dataset scoping. This is where the endpoint adds value.
- No surveyed project gives mode (a) and mode (b) one identity and one policy source.
- Unverified: exact host-record matching in `knownhosts.checkAddr`. zrepl TLS research was rate-limited; the zrepl SSH claims come from search excerpts, not a full read.
