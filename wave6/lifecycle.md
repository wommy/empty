# zfs-endpoint process lifecycle — adversarial review (HEAD 0c884df)

Scope: cmd/zfs-endpoint/main.go, cmd/zfs-csi/main.go, internal/server/server.go,
internal/lock/lock.go, internal/endpoint/endpoint.go, internal/zfs/runner.go.
Excluded (another builder already has these): zfs-csi socket hardening, exec
WaitDelay/Cancel and "context-killed = outcome unknown", error classification,
the property prefix flag.

## Evidence

All evidence was run in the copy at
`/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave6/life-copy`
with `GOTOOLCHAIN=go1.27.3`.

| File | What it shows |
|---|---|
| `life-copy/cmd/zfs-endpoint/life_test.go` | listener deletes a regular file; socket-dir mode is not enforced; the flock admits nested parents and other lock dirs |
| `life-copy/internal/server/life_test.go` | unbounded zfs fan-out; Reconcile hits the 4 MB receive limit; ListVolumes response size |
| `life-copy/internal/lock/life_test.go` | no leak; a done ctx still acquires a free key; starvation |
| `harness/run-shutdown.sh` + `harness/bin/zfs` (fake zfs) + `harness/lifeclient` | real binary, SIGTERM to the main PID versus the whole process group |

Run them with `go test ./cmd/zfs-endpoint ./internal/server ./internal/lock -run 'Life|Listener|Socket|LockParentSecond' -v`.
For the shutdown runs, use `harness/run-shutdown.sh main|group`.

## Ranked findings

### 1. HIGH: under a default systemd unit, the shutdown drain does nothing
- **Scenario:**
  - `systemctl stop` or a restart, with the default `KillMode=control-group`, sends SIGTERM to every process in the cgroup at the same moment.
  - That includes each running `zfs` child.
  - GracefulStop is "waiting" for an apply whose `zfs create`, `destroy` or `set` has just been terminated.
  - The plan stops between ops, and the RPC returns Internal "Terminated".
- **Code:** cmd/zfs-endpoint/main.go:84-104 (the drain logic assumes only the daemon gets the signal); there is no shipped unit.
- **Evidence:** `run-shutdown.sh group` (SIGTERM to the process group):
  - The fake zfs logged `got SIGTERM`.
  - The client got `code = Internal desc = inspect parent: zfs get tank/k8s: unknown: Terminated`.
  - The daemon exited 55 ms later. The 2 s drain never applied.

  With `run-shutdown.sh main` (SIGTERM to the daemon only), the call stays in flight until the drain bound.
- **Fix:** ship the unit below with `KillMode=mixed` and `TimeoutStopSec` greater than `-drain`. Optionally, put zfs children in their own process group (`SysProcAttr{Setpgid:true}`) so that a Ctrl-C in a terminal does not hit them either.

### 2. HIGH: `-listen host:port` serves the destructive API over plaintext TCP with no auth, on any interface
- **Scenario:** `-listen 0.0.0.0:9000` starts without complaint. Anyone who can reach the port can call DeleteVolume or Reconcile (non-dry-run) as root.
- **Code:** cmd/zfs-endpoint/main.go:110-113 (`net.Listen("tcp", addr)`) and :81 (`grpc.NewServer()` with no credentials).
- **Evidence:** `zfs-endpoint -parent tank/k8s -listen 0.0.0.0:0` logged `INFO serving listen=0.0.0.0:0` and served.
- **Fix:** refuse TCP until ADR 0001 transport auth lands. As an interim, allow it only behind an explicit `-insecure-tcp-loopback`, and only on loopback.

### 3. HIGH: unbounded concurrent RPCs, each forking `zfs`
- **Scenario:**
  - No `MaxConcurrentStreams` is set, and there is no semaphore on the Runner.
  - A retry storm from the CSI sidecars (or a buggy front end) forks one `zfs` per RPC.
  - Full-scan RPCs (ListVolumes, ListSnapshots, CreateSnapshot, Reconcile) each run `zfs get -r -d 2 … <parent>` over the whole subtree and buffer all of its output in memory.
  - The kernel serialises most of these ioctls on `spa_namespace_lock` anyway, so the concurrency buys no throughput.
  - It does cost PIDs: systemd's default `TasksMax` (15% of pid_max) leads to EAGAIN on fork, which surfaces as Internal. It also costs RSS.
- **Code:**
  - cmd/zfs-endpoint/main.go:81 (`grpc.NewServer()` without options);
  - internal/zfs/runner.go:38-43 (no limit);
  - internal/endpoint/endpoint.go:440 and :463 (full scans).
- **Evidence:** `TestLifeUnboundedConcurrentZFS` sent 2000 concurrent GetCapacity calls. The result was **2000 zfs runs, with 1882 children running at once.**
- **Fix:**
  - A weighted semaphore in `zfs.Client.run`, for example 8 slots, and 1 slot for a full `-r` scan. Acquiring it must respect ctx, so a waiter returns Unavailable or Aborted.
  - Plus `grpc.MaxConcurrentStreams(64)`.
  - Defense in depth: `TasksMax=` in the unit.

### 4. MEDIUM: no OpenZFS 2.4 assertion at startup (ADR 0004)
- **Scenario:**
  - ADR 0004 makes 2.4 the only supported version, and CI asserts that both userland and kmod are `zfs-2.4.*`.
  - The daemon itself never checks.
  - On a 2.2 userland it runs and returns 2.2 spellings and parse paths, which the ADR explicitly drops.
  - The 24.04 mix of a 2.2 userland and a 2.3 kmod also starts silently.
- **Code:** cmd/zfs-endpoint/main.go:56-64 (the only startup probe is `Exists(parent)`). `grep -rn version internal/zfs` finds nothing.
- **Evidence:** code inspection. The fake zfs answers only `get type` and the daemon serves.
- **Fix:**
  - At startup, run `zfs version -j` (JSON is available since 2.3).
  - Require both `userland` and `kernel` to match `zfs-2.4.`, or fail with exit 78 (EX_CONFIG) so that `RestartPreventExitStatus=78` stops the restart loop.
  - Log both versions.
  - A missing binary already fails clearly: `check parent: … exec: "zfs": executable file not found` (verified). Keep that.

### 5. MEDIUM: a zfs child can outlive the daemon; no Pdeathsig and no wait for handlers
- **Scenario:**
  - When the drain times out, `srv.Stop()` cancels the handler contexts.
  - `Serve` returns at once, because there is no `grpc.WaitForHandlers`, and `main` exits.
  - Exiting races the `exec.CommandContext` kill goroutine, so the child can be re-parented to init and keep running unsupervised. That leaves a half-applied plan nobody records.
  - A `zfs` stuck in D state on a suspended pool cannot be killed at all.
- **Code:** internal/zfs/runner.go:39 (no `SysProcAttr`); cmd/zfs-endpoint/main.go:99-106.
- **Evidence:** `run-shutdown.sh main` in a loop of 8 runs. In **1 of 8**, `harness/bin/zfs get … available` was still alive after the daemon had exited.
- **Fix:**
  - Track handlers in a unary interceptor WaitGroup. After `Stop()`, wait up to about 5 s on it, logging each cut op.
  - Set `SysProcAttr{Pdeathsig: SIGKILL, Setpgid: true}`. Pdeathsig fires on the death of the forking *thread*: fork from a goroutine that holds `runtime.LockOSThread`, or accept the Go issue #27505 caveat.
  - With `KillMode=mixed`, systemd also SIGKILLs any leftovers after the main process exits.
  - The lock fd is O_CLOEXEC (Go default), so an orphan never holds the per-parent flock. A restarted daemon can therefore run beside it, which is one more reason to kill it.

### 6. MEDIUM: `listener()` unlinks whatever non-socket file is at the socket path
- **Scenario:** with a mistyped `-listen unix:///etc/exports` or `unix:///var/lib/nfs/etab`, the dial fails because the path is not a socket, then `os.Remove(path)` deletes the file, running as root.
- **Code:** cmd/zfs-endpoint/main.go:119-125. The same pattern is in cmd/zfs-csi/main.go:82-88; the zfs-csi side belongs to the socket-hardening builder, so it is listed here only for parity.
- **Evidence:** `TestListenerDeletesRegularFile`: after `listener()`, reading the file fails with `no such file or directory`.
- **Fix:** `os.Lstat` first, and remove only if `fi.Mode()&os.ModeSocket != 0`. Otherwise fail with "refusing to replace non-socket".

### 7. MEDIUM: boot race, because a missing parent is a fatal startup error, and the startup probe has no timeout
- **Scenario:**
  - At boot the pool is not imported yet: `zfs-import-cache` is slow, the disks are late, or the pool is encrypted and waiting for a key.
  - The daemon exits 1 with "parent dataset tank/k8s does not exist".
  - With the default `Restart=on-failure` and `RestartSec=100ms`, `StartLimitBurst=5` within 10 s is hit in about 0.5 s. The unit becomes **failed for good** and never comes back after the import.
  - Separately, on a suspended pool `zfs get` at main.go:60 blocks with no deadline. The daemon never serves, while `Type=simple` reports it as started.
- **Code:** cmd/zfs-endpoint/main.go:57-64.
- **Evidence:**
  - A fake zfs that prints "dataset does not exist" gives `zfs-endpoint: parent dataset tank/k8s does not exist`, exit=1.
  - Note also that main.go:60 checks existence only. A zvol as parent is accepted, and `ValidDataset` accepts `..` components: `-parent 'tank/../x'` started and served, and created `tank%..%x.lock`. A real zfs would reject that name at the probe.
- **Fix:**
  - Order the unit after `zfs-import.target` and `zfs-mount.service`, with `StartLimitIntervalSec=0` and backoff (unit below).
  - In the daemon, wrap the probe in `context.WithTimeout(ctx, 30s)`.
  - Better: serve with health `NOT_SERVING`, retry the parent probe with backoff, and flip to `SERVING` and send `READY=1` once it is found.
  - Check `type == filesystem`, and reject `.` and `..` components in `datasetRE`.

### 8. MEDIUM: no health, readiness, sd_notify, or socket activation
- **Scenario:** front ends and systemd cannot tell these states apart:
  - the process is up but the pool is suspended or exported;
  - the daemon is draining;
  - the daemon is ready.

  Liveness would have to be inferred from GetCapabilities, which never touches ZFS (server.go:73-89).
- **Code:** cmd/zfs-endpoint/main.go:81-82. Nothing is registered besides EndpointService.
- **Fix (minimum):**
  - Register `google.golang.org/grpc/health` with service `zfsendpoint.v1.EndpointService`.
    - A goroutine probes `zfs get -Hp type <parent>` every 15 s with a 5 s timeout, and sets `SERVING` or `NOT_SERVING`.
    - At the first signal it sets `NOT_SERVING`, before GracefulStop.
  - sd_notify: write `READY=1` to `$NOTIFY_SOCKET` after `Serve` starts listening, and `STOPPING=1` when draining. That is about 20 lines with no dependency.
  - Socket activation is optional:
    - If `LISTEN_PID == getpid()` and `LISTEN_FDS == 1`, use `net.FileListener(os.NewFile(3, …))`.
    - The gain: zfs-csi connections queue in the kernel across restarts instead of failing.
    - It also moves socket mode and ownership to `SocketMode=0600`.

### 9. MEDIUM: no audit trail: zero log lines per request or per destructive op
- **Scenario:**
  - DeleteVolume, DeleteSnapshot and Reconcile (non-dry-run) destroy datasets and write nothing.
  - After an incident, the questions "who destroyed tank/k8s/pvc-x and why" and "which op of the plan was cut by shutdown" cannot be answered.
  - `grep -rn slog internal/` (excluding csi) gives no hits. The only logs are the 5 lines in main.go.
- **Code:** internal/endpoint/apply.go:13 (the loop runs ops without logging); internal/server/server.go (no interceptor).
- **Fix (minimum):**
  - `slog.NewJSONHandler(os.Stderr)`, which journald stores as-is.
  - A unary interceptor that:
    - takes `x-request-id` from metadata, or mints a 16-byte hex id, and puts it in ctx;
    - logs `method`, `request_id`, `peer` (the UDS peer cred uid via `SO_PEERCRED` once auth lands), `code` and `duration`.
  - In `apply`, before each op, log at Info `request_id`, `op i/n`, `kind`, `target`, `source`, `props` and `recursive/defer`. Afterwards, log `result`.
  - For destructive plans (any OpDestroy, and Reconcile actions), log the **whole plan** before the first op, so a cut plan can be reconstructed.

### 10. MEDIUM: message-size limits at 100k volumes
- **Scenario:**
  - `ListVolumes` with `max_entries=0` returns everything. 100k filesystem volumes come to **21.2 MB**.
  - The zfs-csi client (cmd/zfs-csi/main.go:40) has the default `MaxCallRecvMsgSize` of 4 MiB, so it gets ResourceExhausted, and so does csi-sanity or a health monitor.
  - In the other direction, a `Reconcile` desired set of 100k ids (4.2 MB) is rejected by the server's default 4 MiB receive limit. That fails closed, which is safe, but GC is impossible at that scale.
  - Every ListVolumes page is also a full rescan of the parent, so paging 100k volumes at 500 per page is 200 full `zfs get -r` calls.
- **Code:**
  - internal/endpoint/endpoint.go:418-437 (`page`: when max is 0, everything is returned);
  - :440-451;
  - cmd/zfs-endpoint/main.go:81.
- **Evidence:**
  - `TestLifeListVolumesResponseSize`: 21,200,000 bytes.
  - `TestLifeReconcileDesiredSetSize`: 50k ids pass; 100k fail with `ResourceExhausted … (4200002 vs. 4194304)`.
- **Fix:**
  - Cap the page server-side: when `max_entries == 0` or it exceeds 5000, return 5000 plus `next_token`. CSI permits this.
  - Set `grpc.MaxRecvMsgSize(64<<20)` on the endpoint for Reconcile, or make the desired set client-streamed.

### 11. LOW-MEDIUM: running as non-root
- **Scenario:**
  - The default `-lock-dir` and socket are under `/run`.
  - As `nobody`, startup fails with `mkdir /run/zfs-endpoint: permission denied` (verified).
  - With a `RuntimeDirectory` it starts. However, on Linux, a non-root `zfs create` of a filesystem creates the dataset but cannot mount it. OpenZFS `zfs_do_create` prints "filesystem successfully created, but it may only be mounted by root" and exits 1. This was not re-verified on a 2.4 pool.
  - The plan therefore fails after the create.
  - File export is disabled, the node side is NFS-only (ADR 0004), and so filesystem volumes are useless.
  - The daemon warns only about NFS (main.go:73-75).
- **Fix:**
  - Require euid 0 unless `-allow-unprivileged` is given.
  - When unprivileged, run `zfs allow <parent>` at startup and fail if any needed permission is missing: create, destroy, snapshot, clone, promote, mount, userprop, refquota, refreservation, volsize, sharenfs.
  - In that mode, advertise only block volumes.

### 12. LOW-MEDIUM: the per-parent flock has holes (it exists: main.go:50 and :137-154)
- **What works:**
  - A second instance on the same parent with the same `-lock-dir` is refused.
  - Stale locks are impossible: flock dies with the process, and a re-take after release succeeds.
  - The fd is O_CLOEXEC, so zfs children never inherit it.
  - /run is tmpfs, which is local and correct.
- **Holes (all verified in `TestLockParentSecondInstance`):**
  - An ancestor or descendant parent is admitted: `tank/k8s` beside `tank/k8s/vols`, giving overlapping subtrees under two lock tables.
  - The same parent with a different `-lock-dir` is admitted.
  - On an NFS lock dir, Linux emulates flock with NFS byte-range locks, which are cluster-wide and lost across lease expiry or server reboot. With `local_lock=flock` the lock is host-local. Neither behaviour is intended.
- **Fix:**
  - Take `LOCK_SH` on the lock file of every proper ancestor and `LOCK_EX` on your own. Nesting then conflicts both ways, and siblings do not.
  - Hard-code the directory, or `statfs` it and refuse `NFS_SUPER_MAGIC`.

### 13. LOW: the keyed lock has no FIFO, and a done ctx can still take a free key
- **Leak check (no leak):** `TestLifeCancelledWaitersDoNotLeak` cancelled 1000 waiters. Goroutines went back to the baseline (2 → 2), and no partial keys stayed held.
- **Starvation:** `release` closes the channel and wakes everyone, who race again. In `TestLifeStarvation`, a waiter competing with 8 re-acquirers waited 2 s and never got the lock.
- **Done ctx:** `Acquire` on a free key with a context that is already done succeeds (`TestLifeExpiredCtxStillAcquiresFreeKey`). So `e.lock` with less than `lockMargin` left (wait clamps to 0, endpoint.go:197-211) still starts the zfs op with under 1 s left. That defeats the margin's purpose and feeds the excluded "context-killed" problem.
- **Code:** internal/lock/lock.go:38-58.
- **Fix:**
  - Check `ctx.Err()` before each take.
  - Optionally, use a per-key waiter queue with direct handoff.

### 14. LOW: the socket directory's mode is not enforced when the directory already exists
- **Code:** cmd/zfs-endpoint/main.go:114 and :138. `MkdirAll(…, 0o700)` is a no-op when the directory exists.
- **Evidence:** `TestSocketDirModeNotEnforced`: a pre-existing 0777 directory is kept. The socket itself is 0600. In a writable directory, another user can unlink it and plant their own socket, which zfs-csi would then talk to.
- **Fix:** `Lstat` the directory, then require that it is not a symlink, is owned by the euid, and has `mode&022 == 0`. Otherwise refuse. Or rely on systemd `RuntimeDirectoryMode=0700`.

### 15. LOW: zfs-csi's reconnect backoff and keepalive
- **Reconnect:** zfs-csi dials with gRPC's defaults (cmd/zfs-csi/main.go:40). After an endpoint outage of a few minutes (for example the pool-import race), reconnect backoff grows to 120 s. The front end keeps failing Unavailable for up to 2 minutes after the endpoint is back.
- **Fix:** `grpc.WithConnectParams(grpc.ConnectParams{Backoff: backoff.Config{BaseDelay: 200ms, Multiplier: 1.6, MaxDelay: 5s}})`.
- **Keepalive:** keepalive and idle settings do not matter on a UDS; keep the defaults. If TCP is ever allowed, set `keepalive.EnforcementPolicy{MinTime: 30s}` and `ServerParameters{Time: 2m, Timeout: 20s}`.

### 16. INFO: metrics are missing
The minimum, as a Prometheus text endpoint on a second UDS or loopback:
- `zfs_exec_seconds{op,result}` histogram;
- `zfs_exec_inflight` gauge (pairs with #3);
- `grpc_server_handled_total{method,code}`;
- `lock_wait_seconds` and `lock_aborted_total`;
- `endpoint_health` (0 or 1);
- `build_info{version,zfs_userland,zfs_kmod}`.

### Verified as correct (no finding)
- **Drain:** SIGTERM to the main PID only: GracefulStop does wait for the in-flight RPC.
  - The wait is bounded by `-drain` (the daemon exited at 2.02 s with `-drain 2s`).
  - New RPCs during the drain get Unavailable or GOAWAY.
  - A second signal stops at once.
  - Exit status is 0.
- **Stale socket after a crash:** a dead socket file is detected by the failed dial and replaced. A live one is refused.
- **Socket mode:** the socket is created 0600 under umask 0177 (main.go:129-131).

## Recommended systemd unit

This is a template, one instance per parent dataset. The instance name is the
`systemd-escape`d dataset: `systemctl enable --now zfs-endpoint@$(systemd-escape tank/k8s/vols).service`.

```ini
# /etc/systemd/system/zfs-endpoint@.service
[Unit]
Description=ZFS endpoint for dataset %I
Documentation=https://github.com/wommy/empty
# the pool must be imported and its datasets mounted before the parent check
After=zfs-import.target zfs-mount.service zfs.target
Wants=zfs-import.target zfs-mount.service
# NFS file export: sharenfs needs a running nfs-server, and etab is read back
After=nfs-server.service
Wants=nfs-server.service
# keep retrying across a late or encrypted pool import instead of giving up
StartLimitIntervalSec=0

[Service]
# Type=notify once READY=1/STOPPING=1 is implemented (finding 8); until then:
Type=exec
# NotifyAccess=main
ExecStart=/usr/local/sbin/zfs-endpoint \
    -parent %I \
    -listen unix:///run/zfs-endpoint/%i.sock \
    -lock-dir /run/zfs-endpoint \
    -drain 30s
Environment=PATH=/usr/sbin:/usr/bin:/sbin:/bin

# /run/zfs-endpoint, 0700, shared by all instances (the flock must be shared);
# preserve it so stopping one instance doesn't delete the others' sockets
RuntimeDirectory=zfs-endpoint
RuntimeDirectoryMode=0700
RuntimeDirectoryPreserve=yes
UMask=0077

# shutdown: SIGTERM to the daemon only, so in-flight zfs children finish
# inside -drain; then SIGKILL anything left (orphans, finding 5)
KillMode=mixed
KillSignal=SIGTERM
TimeoutStopSec=45
TimeoutStartSec=90
# FinalKillSignal / SendSIGKILL default to yes

# restart with backoff (systemd >= 254 for RestartSteps/RestartMaxDelaySec)
Restart=on-failure
RestartSec=2s
RestartSteps=8
RestartMaxDelaySec=60s
# exit 78 = config error (bad flags, wrong ZFS version): don't loop
RestartPreventExitStatus=78

# resource caps (defense in depth for finding 3)
TasksMax=512
LimitNOFILE=65536
OOMScoreAdjust=-500

# Hardening. Only options that do NOT create a private mount namespace:
# zfs mounts datasets and exportfs edits host state from inside this
# service, and a private namespace (PrivateTmp, ProtectSystem, ProtectHome,
# PrivateDevices, ReadOnlyPaths, ProtectKernel*, ...) would hide every
# mount from the host and from nfsd. zfs-mount.service and zed use none.
NoNewPrivileges=yes
LockPersonality=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
MemoryDenyWriteExecute=yes
SystemCallArchitectures=native
# exportfs resolves client hostnames, so keep INET
RestrictAddressFamilies=AF_UNIX AF_NETLINK AF_INET AF_INET6

[Install]
WantedBy=multi-user.target zfs.target
```

The optional socket activation needs the LISTEN_FDS support from finding 8:

```ini
# /etc/systemd/system/zfs-endpoint@.socket
[Unit]
Description=ZFS endpoint socket for %I
[Socket]
ListenStream=/run/zfs-endpoint/%i.sock
SocketMode=0600
DirectoryMode=0700
RemoveOnStop=yes
[Install]
WantedBy=sockets.target
```

### Notes on the unit
- **`zfs-import.target` is reached even when an import fails.** The ordering narrows the boot race but does not close it. `StartLimitIntervalSec=0` with backoff does: the unit keeps retrying until the parent appears. Finding 7's in-process wait-for-parent with `NOT_SERVING` closes it properly.
- **Encrypted parent:** add `After=zfs-load-key.service`, or the matching per-dataset `zfs-load-key@` unit.
- **`Type=notify` + `WatchdogSec=`:**
  - Ping the watchdog from the Go event loop, not from a zfs probe.
  - Restarting cannot fix a suspended pool, and a D-state child cannot be killed.
  - Pool health belongs in the gRPC health service.
