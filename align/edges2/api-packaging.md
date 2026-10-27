# API evolution, packaging, upgrade: findings

Two GitHits `research` calls timed out or were rate-limited, so the findings below come from direct `grep` and `read`. Mayastor pins are from `1e796a58` and may not match the default branch.

## EDGES

- Front ends (CSI sidecars, controllers) cross a gRPC boundary to the host daemon. The boundary carries typed requests and a plugin/daemon version handshake.
- Config and state cross via ZFS user properties (already decided).
- Daemon lifecycle crosses via systemd (start, stop, restart) and gRPC in-flight calls.

## SEAMS

**Protobuf versioning**
- Mayastor puts versioned packages in `v1` directories: I/O Engine at `apis/io-engine/protobuf/v1` in mayastor-dependencies, and agents at `control-plane/grpc/proto/v1` in mayastor-control-plane (`github:openebs/mayastor@1e796a58 doc/design/control-plane.md:254-263`). No `buf breaking` config or CI step was found; the `.github/` grep returned nothing, so this is **unverified**.
- LINSTOR uses protobuf via Gradle and protoc 31.1 (`github:linbit/linstor-server@e1d99946 build.gradle:36-37`). Its API is typed under `com.linbit.linstor.proto.*`. No `buf` or CI breaking check was found. Field reservation and capability negotiation were not found in either repo: **unverified**.
- CSI pattern: `Identity.GetPluginCapabilities` returns a list of capability enums, and `Probe` gives readiness (`github:container-storage-interface/spec@25b3f102 csi.proto:52-61`). Recommendation: expose an equivalent `GetCapabilities` RPC returning a daemon version and feature flags, so front ends feature-detect instead of version-sniffing.

**Packaging**
- zrepl: systemd unit `dist/systemd/zrepl.service` uses `ExecStartPre=... configcheck`, `RuntimeDirectory=zrepl`, `RuntimeDirectoryMode=0700`. It has no `User=` and no `ProtectSystem` or `CapabilityBoundingSet` (`github:zrepl/zrepl@d916c93c`, per research). Packages use native `dpkg-buildpackage` (`packaging/deb/debian/rules`) and `rpmbuild` (`packaging/rpm/zrepl.spec`), not goreleaser/nfpm.
- LINSTOR controller: `scripts/linstor-controller.service` uses `Type=notify`, `DynamicUser=true`, `User=linstor-controller`, `ReadWritePaths`, `RestartForceExitStatus=70`, and a `migrate-h2` ExecStartPre (`github:linbit/linstor-server@e1d99946`).
- LINSTOR satellite: `scripts/linstor-satellite.service` runs as `User=root` with `PrivateTmp=yes`, `RuntimeDirectoryPreserve=restart`, and `KillMode=mixed`. It has no ProtectSystem or CapabilityBoundingSet.
- Mayastor io-engine: `doc/run.md:233,248` sets `AmbientCapabilities` (CAP_SYS_ADMIN, CAP_IPC_LOCK, and others) rather than dropping capabilities.
- targetd: no systemd unit found; packaging is `setup.py` only. It runs as root (`targetd.8`), and the API user/password are config-driven (`targetd.yaml.5`). No sandboxing directives found: absence **unverified**.
- Packaging-tool note: no nfpm or goreleaser config found in zrepl, linstor-server, or targetd. Mayastor returned no results: **unverified**.

**Upgrade safety**
- zrepl: `internal/rpc/rpc_server.go:62-68` calls `GracefulStop()` and falls back to `Stop()` at the context deadline. This is the reference drain pattern.
- LINSTOR: `RestartForceExitStatus=70` treats critical errors as restart-forcing, and the satellite preserves its runtime dir across restarts.
- Idempotent replay after restart: no direct evidence found in these repos: **unverified**.

## GAPS

- No surveyed daemon combines a versioned protobuf package, a capability RPC, `buf breaking` in CI, and a hardened unit. A host daemon that ships all four is a clear gap.
- Hardening is absent or minimal in zrepl, targetd, and the LINSTOR satellite. Mayastor relies on ambient caps. A unit with ProtectSystem=strict, a minimal CapabilityBoundingSet, and a dedicated DynamicUser is undersupplied.
- Drain plus idempotent replay across upgrade is only shown for zrepl's GracefulStop. Replay after restart is unproven in all surveyed repos.
