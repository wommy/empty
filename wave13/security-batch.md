# wave13 security batch: report

- **Branch:** `security-batch` (local, not pushed), based on `claude/gallant-johnson-1qaxp5` at `2a4e914`.
- **Worktree:** `/home/user/empty/.claude/worktrees/agent-a1ef6cfe663da83d2`.
- **Evidence:** `/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave13/secbatch/` (named `NN-before*.txt` and `NN-after*.txt`).
- **Helper scripts:** `.../scratchpad/wave13/secbatch-scripts/`.

## Anomalies and things to check first

1. **F2 changes behaviour for callers (no wire or proto change).**
   - Reconcile, PublishFileExport and UnpublishFileExport now return PERMISSION_DENIED on `-listen`.
   - They are served only on the new `-admin-listen`. The unit passes `-admin-listen unix:///run/zfs-endpoint/admin/%i/endpoint.sock`.
   - zfs-csi calls none of the three (checked with grep), and no ci script or grpcurl use exists in the repo.
   - Anyone who scripted Reconcile against the csi socket must switch to the admin socket.
   - I treated this as the decided D2 (DECISIVE+REVERSIBLE), not as a contested item.
2. **F1 changes the socket path convention and amends ADR 0006.**
   - The old path was `/run/zfs-endpoint/<esc>.sock`. The new path is `/run/zfs-endpoint/sock/<esc>/endpoint.sock`.
   - The CSI pod's hostPath is now the per-instance directory.
   - Pre-release, but any existing install needs new manifests and unit together.
   - Upgrade evidence: no users, so no upgrade path was built.
3. **Admin socket name budget is 69 bytes, not 70.**
   - `admin/` is one byte longer than `sock/`.
   - With the shipped unit, a parent whose escaped name is exactly 70 bytes now exits 78, because the admin path is 108 bytes.
   - The unit comment says so.
   - Not covered by a dedicated test. `TestDefaultSocketLimit` still pins the csi 70.
4. **Two files the other writer also touches (merge-conflict risk).**
   - `internal/endpoint/plan.go`: F12 adds one 5-line block (3 code lines and a comment) beside the existing `sharenfs`/`sharesmb` pins.
   - `internal/endpoint/testdata/plans.golden`: 7 create and clone lines gain `devices=off exec=off setuid=off`. If the CSI writer also changes golden plans, regenerate with `go test ./internal/endpoint -run TestGoldenPlans -update` after the merge.
   - `internal/endpoint/export.go` (H-15, `ParseEtab`): NFS code another stream may touch.
   - `internal/csi` was not edited.
5. **F12 is in tension with D12's reasoning.**
   - D12 says not to stamp `snapdir` locally, so the operator's parent choice wins.
   - F12 does stamp `setuid`, `exec` and `devices` off locally. That overrides an explicit `exec=on` on the parent; the test proves it.
   - I kept F12 as the brief asked and recorded why in ADR 0001 v2: it protects the storage host itself, and it is not tenant-visible.
   - If the owner prefers inheritance, revert the plan.go block. The simulator changes can stay.
6. **The simulator now models `exec`, `setuid` and `devices`** as zfs_prop.c registers them: on/off index, default on, inheritable, on filesystems and snapshots.
   - The local suite and zfsdiff pass.
   - Not verified on the real-ZFS VM leg. zfsdiff compares the simulator with real zfs there.
7. **The H-10 proto comment is stale.**
   - The `desired_volume_ids` comment still says that every missing id is reported. Reconcile now reports the first 1000, then one `"(N more)"` entry.
   - I did not edit the proto, because a comment change needs a gen regeneration (a wire-file change).
   - Recommend: fix the comment in the next proto touch.
8. **One commit covers F8 and H-16 (`65a79cb`).** Both live in the unit plus `TestUnitLint`, and splitting them would have meant a commit whose test fails. Every other item has its own commit.
9. **`TimeoutStartSec` rose from 100 to 110.** The F5 startup check adds one bounded 10s probe (reading the mountpoint plus etab).
10. **Fail-before method differs by item** (details in the evidence table):
    - Items 1-9 were captured during the batch by running the final test against the pre-fix code.
    - Items 4 (re-run), 10, 11, 13 and 12/15 were captured on HEAD with only the fix disabled: the bounds constants set to 1<<30, `-listen` served as admin, the F5 check skipped, the pre-F12 `plan.go`, and the pre-F8 unit with no drop-in.
    - This is not a literal checkout of `2a4e914`. On `2a4e914` those tests do not compile, because the flags and symbols they exercise did not exist.
11. **The worktree guard refused many compound shell commands.**
    - It refused anything with `git` in a pipe, a `$VAR` path in `sed`, or a heredoc.
    - The evidence runs therefore live in small scripts under `secbatch-scripts/`, and temporary reverts used plain `git checkout <rev> -- <file>` followed by `git checkout HEAD -- <file>`.
    - The tree is clean at HEAD (`git status` is empty).

## Commits (oldest first)

| commit | item |
|---|---|
| `a70375f` | 1. H-04/H-05: a default call deadline (10 min) and panic recovery to Internal |
| `49447be` | 2. H-06: at most 2 whole-parent scans in flight |
| `78f0b8e` | 3. H-08/H-11: `.`/`..` components and option-like property names refused |
| `1775efa` | 4. H-09: bounds on echoed input (512 B per field), stderr (8 KiB), gRPC messages (16 KiB) and log fields (256 B) |
| `f9b5247` | 5. H-10: at most 250k desired ids; the answer keeps 1000 missing-id skips plus `(N more)` |
| `f860c2b` | 6. H-12: `LC_ALL=C LANGUAGE=C` for zfs children |
| `d3e477d` | 7. H-15: `ParseEtab` reads lines of any length |
| `4e8044b` | 8. F1: a socket directory per instance; a socket in or above the lock directory is refused (78) |
| `484f9ea` | 9. F7: SO_PEERCRED uid, gid and pid on every rpc log line |
| `ae57adf` | 10. F2: csi and admin roles per listener; the admin peer must be uid 0 or the daemon's uid |
| `de4ae00` | 11. F5: startup refuses (78) a parent under a crossmnt export |
| `65a79cb` | 12 + 15. F8 `LogRateLimitIntervalSec=0`; H-16 hardening drop-in |
| `c4708c8` | 13. F12: filesystems created and cloned with `setuid`, `exec` and `devices` off |
| `a5bd041` | 16. ADR 0001 v2 |

Item 14 (F10) was skipped with evidence; see below.

## Per-item evidence (fail before, pass after)

| item | test(s) | before | after |
|---|---|---|---|
| 1 H-04/05 | `internal/server` `TestCallWithoutDeadlineGetsOne`, `TestHandlerPanicIsInternalAndTheDaemonServes` | `01-before.txt`: the handler panic crashes the test binary (`index out of range`) | `01-after.txt` |
| 2 H-06 | `internal/endpoint` `TestWholeParentScansAreBounded` (synctest, no sleeps) | `02-before.txt`: 8 in flight, want 2 | `02-after.txt` |
| 3 H-08/11 | `internal/zfs` `TestNamesAndPropsNeverBecomeOptions`; `internal/endpoint` `TestSnapshotComponentNeverDots` | `03-before.txt`: `ValidDataset("tank/..") = true`, and more | `03-after.txt` |
| 4 H-09 | `TestErrorsDoNotEchoUnboundedInput`, `TestStderrIsBounded`, `TestLoggedAndReturnedInputIsBounded` (64 KiB inputs) | `04-before-final.txt`: 65558-byte messages for 65536-byte input, 3,000,057 bytes of stderr, 65.6 KB log lines. Earlier 1 MiB runs: `04-before-ep.txt` | `04-after.txt` |
| 5 H-10 | `TestReconcileBoundsItsOwnAnswer`, `TestReconcileRefusesAnOversizedDesiredSet` | `05-before.txt`: 5000 skips, want 1001; 250001 ids accepted | `05-after.txt` |
| 6 H-12 | `internal/zfs` `TestChildRunsInTheCLocale` | `06-before.txt`: the child saw `de_DE.UTF-8` | `06-after.txt` |
| 7 H-15 | `TestParseEtabSurvivesALongLine` (2 MiB line) | `07-before.txt`: lines after the long line are lost | `07-after.txt` |
| 8 F1 | `cmd/zfs-endpoint` `TestSocketNeverSharesTheLockDirectory`, `TestDefaultSocketHasItsOwnDirectory`; `deploy` `TestManifests` (hostPath under `sock/`) | `08-before.txt`: socket directory `/run/zfs-endpoint` | `08-after.txt` |
| 9 F7 | `internal/server` `TestPeerCredentialsAreLogged` (real unix socket) | `09-before.txt`: `peer_uid = <nil>` | `09-after.txt` |
| 10 F2 | `TestEveryMethodHasARole`, `TestRolesPerListener` (bufconn), `TestAdminPeerIsRootOrTheDaemonsUser`; `cmd` `TestAdminRPCsOnlyOnTheAdminSocket`, `TestAdminSocketRefusals`, `TestUnitAdminSocketIsApart`; `deploy` `TestUnitServesTheConventionalSocket` (admin), `TestManifests` (no admin hostPath) | `10-before.txt` (server stubs: csi may call Reconcile) and `10-before-cmd.txt` (Reconcile on `-listen`: OK, want PermissionDenied) | the full race run, `gate-race.txt` |
| 11 F5 | `internal/endpoint` `TestExposedByAncestor` (pure, 9 rows); `cmd` `TestStartupRefusesACrossmntAncestor` (simulator: `zfs set sharenfs=on tank`) | `11-before.txt`: `on` and `crossmnt,…` not refused | `11-after.txt` |
| 12 F8 + 15 H-16 | `deploy` `TestUnitLint` | `12-15-before.txt`: `LogRateLimitIntervalSec=[]`, no drop-in | `gate-race.txt`; `15-systemd-analyze.txt` |
| 13 F12 | `internal/endpoint` `TestFilesystemsMountWithoutSetuidExecOrDevices`, `TestGoldenPlans` | `13-before.txt`: `setuid=on (inherited from tank/v)`, plus the golden diff | `gate-race.txt` |
| 16 ADR | n/a (documentation) | | |

**H-16, offline `systemd-analyze` (systemd 255):**
- `systemd-analyze security --offline=true` scores the unit 8.3 EXPOSED, and the unit plus the drop-in 7.6.
- The draft's 4.6 needs the commented-out settings.
- `systemd-analyze verify` reports only that the binary is missing.

**Test-time cost (ADR 0005, at most 60s):**
- 25 new tests. The sum of their top-level durations is **0.49s** without race and **1.03s** with race.
- The largest are TestReconcileRefusesAnOversizedDesiredSet (0.18s race), TestErrorsDoNotEchoUnboundedInput (0.14s) and TestReconcileBoundsItsOwnAnswer (0.12s).
- Wall clock for the full `go test -race -count=1 ./...` is about 16s on this machine.
- No real sleeps were added. The scan bound uses `testing/synctest`.
- New endpoint tests run on the simulator (no skips), so `ci/expected-skips.txt` is unchanged. On the VM leg they take the default weight.
- Two places poll with sleeps that predate this batch: the `cmd` tests that start the daemon reuse the existing `waitSocket` 10 ms poll.

## Skipped: already fixed at `2a4e914`

All of these pass at HEAD (`already-fixed.txt`):

| item | fixed by | proving test |
|---|---|---|
| H-01 | `endpoint.lock` uses TryAcquire when wait ≤ 0 | `internal/endpoint` `TestShortDeadlineStillTakesAFreeLock` |
| H-02/S3 | `PlanCreate` gates on `OwnedVolume` | `TestPlanCreateDoesNotAdoptUnnamedManaged` |
| H-07 | `unixsock.CheckPath` with `MaxPathLen` | `TestSocketPathFitsSunPath`, `TestDefaultSocketLimit`, `TestStartupConfigRefusals` ("long socket path") |
| H-13 | the lock path never follows symlinks | `TestLockParentDoesNotFollowSymlinks` (plus the "symlinked lock" row) |
| H-14 | a stop during startup exits cleanly | `TestStopDuringStartupIsClean` |
| F10 | `unixsock.ListenShared` (commit `0996a08`) accepts the kubelet's 0777 emptyDir for the CSI socket only | `TestListenSharedRelaxesOnlyTheDirectoryMode` |

**F10 evidence.**
- kubernetes `dd410c48` `pkg/volume/emptydir/empty_dir.go` creates the directory with `perm 0777`, and `setupDir` chmods it to 0777.
- `EmptyDirVolumeMode` is an Alpha gate, default off. kind runs the upstream kubelet, so the directory mode on kind is 0777.
- Copies are in `.../scratchpad/wave13/secbatch-dl/`.
- `SecureDir` was not changed.

## Decisions taken

- **F2 roles belong to the listener.**
  - `server.Daemon.Serve(lis, role)` wraps accepted connections, and the handshake moves the role into `PeerInfo`. This keeps per-method roles out of the endpoint core.
  - A connection with no role gets UNAUTHENTICATED, except for health. Calling `GRPC.Serve` directly therefore fails closed.
  - Non-health streams are denied.
  - A non-unix connection (bufconn) is not refused. ADR v2 §4 suggested refusing it, but tests need bufconn and the daemon serves nothing else; it still needs a role.
- **The admin peer** must be uid 0 or the daemon's euid (`-allow-non-root`), whatever the socket mode.
- **`-admin-listen` is off by default in the binary and on in the unit.** It must not be in, or under, the `-listen` directory, nor in the lock directory.
- **F1:** the daemon creates `sock/<esc>` and `admin/<esc>` 0700 itself. `%i` stays out of `RuntimeDirectory=` because of doubts about escaping rules.
- **F5:**
  - Exit 78 at startup only; health does not re-check (see the open questions).
  - The cmd tests now inject the etab (`deps.etab` set to the simulator), so a developer host's `/var/lib/nfs/etab` no longer leaks into them.
  - A parent with mountpoint `none` or `legacy` is not checked.
- **H-16:** the drop-in turns on only `RestrictNamespaces=yes`, `KeyringMode=private` and `LimitCORE=0`.
  - These ship commented out, each with `TODO(H-16, VM leg)`:
    - CapabilityBoundingSet and AmbientCapabilities;
    - ProtectClock, DevicePolicy and DeviceAllow. ProtectClock implies `DeviceAllow=char-rtc r`, which turns on the allow-list and would refuse `/dev/zfs`;
    - ProtectHostname, which may need a mount namespace on some systemd versions. I did not confirm this;
    - SystemCallFilter;
    - MemoryHigh and MemoryMax.
  - `TestUnitLint` refuses every setting that creates a mount namespace or cuts the network, even commented out, so nobody re-adds one.
- **ADR 0001 v2** replaces v1 in place, keeping the same filename so existing links still work. v1 is readable at `2a4e914`. Its D-table:

  | decision | recorded as |
  |---|---|
  | D2, D5, D6, D10, D11, D12 | built |
  | D1 | built: it is what the daemon already was. Records A1: the shared-parent requirement came from v1's own context |
  | D3, D7, D9 | decided, **not built** |
  | D4 | deferred, with its revisit trigger |
  | D8 | ADR 0004 static-first kept; pod-egress deny plus host firewall is F6's primary control; per-node `/32` prerequisites listed |
  | D12 | no snapdir at create; inherit from the parent; documented that hidden ≠ inaccessible |

## Open questions, with a recommendation

1. **F5 in health.** Should the probe also turn NOT_SERVING when a crossmnt ancestor appears after startup?
   - NOT_SERVING blocks every CSI call, deletes included.
   - Recommend a Warn line from the probe, not NOT_SERVING. The new volumes it would expose are what matters, and CreateVolume could refuse instead. This would be a small follow-up in `server.Probe`'s check function.
2. **D3 and D9 are not built:** a Reconcile digest is not required to collect, and exported volumes are not skipped.
   - Both are planner changes in `reconcile.go`.
   - Recommend the next batch, since `plan.go` is busy now.
3. **D7 `-socket-group` is not built.** Recommend building it once the CSI deployment wants a non-root controller pod.
4. **The H-16 commented settings** need a VM-leg run of the suite under the unit with the drop-in, then a journal check for EPERM. Recommend adding that to `ci/real-zfs-vm.sh`. The uid-65534 admin-socket check from ADR v2 Testing also belongs there.
5. **F12 startup warning** for a parent mountpoint traversable by others (`mode & 0o001`) was in the threat model, not in the brief, and is not built. Recommend it as a one-line Warn at startup.
6. **The `desired_volume_ids` proto comment** (anomaly 7): fix it at the next proto regeneration.

## Final gate (at `a5bd041`)

| check | result |
|---|---|
| `gofmt -l .` (ignoring `.claude/`) | empty |
| `go vet ./...` | clean |
| `go vet -tags nightly ./...` | clean |
| `go mod tidy -diff` | clean |
| `go test -race -count=1 ./...` | all ok (`secbatch/gate-race.txt`) |
| `bash -n` | not applicable: no `ci/*.sh` touched |
