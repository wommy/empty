# wave14 security-2: report

- **Branch:** `security-2` (local, not pushed), from `claude/gallant-johnson-1qaxp5` at `6e2cc66`.
- **Worktree:** `/home/user/empty/.claude/worktrees/agent-a031c2bb39fd727ca`.
- **Evidence:** `/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave14/sec2/` (`*-before.txt`, `*-after.txt`, `gate-race.txt`, `fsm-*.txt`, `sa-security.txt`, `D9-probe.txt`).
- **Fail-before method:** `sec2/failbefore.py` disables only the fix in the working tree (spec in `sec2/*.json`), runs the new test under `-race`, then restores the file and runs it again. D3 swapped in the `6e2cc66` `reconcile.go`. Item 5 used the `6e2cc66` drop-in with the check script removed (`sec2/item5before.py`).

## Anomalies and things to check first

1. **D9 is stopped, not built.** The decision is wrong under static NFS export at create.
   - ADR 0004's static export (commit `d0f3d01`, nine minutes after ADR v2) sets `sharenfs` locally in the same `zfs create`.
   - NFS mode requires `nfs.clients` on every CreateVolume (`internal/csi/controller.go:39`).
   - So every NFS-mode volume is `sharenfs != off` from birth. `sec2/D9-probe.txt` shows the create op carries `sharenfs="rw=10.20.0.0/24"`, and the D9 predicate would skip the volume that HEAD collects.
   - Built as written, Reconcile would never collect an undesired volume in NFS mode. The "client list" in D9 is `nfs_clients`, which ADR 0004 has since removed from the export path.
   - F3 and D9 date from when only PublishFileExport set `sharenfs` (`wave11/threat-model.md` row 9).
   - **Recommendation:** drop D9 and rely on D3 plus `max_deletions` and `min_age` for F3. The alternative is to skip only a volume whose `sharenfs` differs from what the endpoint wrote, which is drift, not use. The ADR now records "stopped" with both options (`22004f9`), and neither is chosen.
2. **D3 changes behaviour for callers (no wire change).**
   - Reconcile without `plan_digest` now fails with FAILED_PRECONDITION whenever its plan would collect any undesired volume, and nothing is applied, including the uncommitted and orphan rows in the same plan.
   - A plan of only uncommitted rows and orphan clone sources still needs no digest.
   - No in-repo caller is affected outside tests: zfs-csi never calls Reconcile, and no ci script does.
   - 13 tests (plus the model rig, the crash table and the nightly FSM explorer) now dry-run first. A dry run mutates nothing, so the fault and crash indices do not move.
3. **The nightly `TestExploreLifecycle` fails at full depth, and the failure predates this branch.**
   - It reports 6 `retry-diverges` findings (DV of a clone crashed at 1/1 leaves `a@volume-source-for-volume-b`).
   - The findings are the same six on an export of `6e2cc66` (`sec2/fsm-base6e.txt` vs `sec2/fsm-sec2.txt`; a diff of the sorted headlines is empty).
   - It is not caused here, but whoever owns nightly should know.
4. **What "uid 65534 refused with PERMISSION_DENIED" means on the VM.**
   - With the shipped modes, uid 65534 never reaches gRPC: `/run/zfs-endpoint` is 0700 and the socket 0600, so connect(2) fails with EACCES.
   - The check therefore asserts both layers:
     - (a) shipped modes: the call fails at connect (`CONNECT`);
     - (b) with `o+x` on each directory on the path and `o+rw` on the socket, the daemon answers PERMISSION_DENIED (SO_PEERCRED). It restores the modes afterwards.
   - Root is also checked to be served (a dry-run Reconcile).
5. **The VM check has not run.** It cannot run locally.
   - Proven locally: the script against stubs, in 5 scenarios including a named failing setting (`deploy` `TestUnitCheckNamesTheFailingSetting`); the probe against a real daemon on unix sockets (`ci/unitprobe` `TestProbeReportsTheCodeItGot`); `bash -n`; and `systemd-analyze verify` and `security --offline` on systemd 255.
   - Not proven: real systemd and zfs behaviour in the guest.
   - The check gates the job: anything other than PASS is a `::error::`, and the verdict line goes into the job summary.
   - Possible first-run issues:
     - `setpriv` (util-linux) is assumed in the guest;
     - the guest copies files to `~ci` and installs them under `/usr/local/{s,}bin`;
     - startup would refuse with exit 78 if a concurrent NFS shard ever exported `/` with crossmnt. I know of none that does.
6. **Settings turned on in the drop-in:** only `ProtectHostname=yes` and `SystemCallFilter=~@clock @cpu-emulation @debug @module @obsolete @raw-io @reboot @swap` with `SystemCallErrorNumber=EPERM`.
   - **ProtectHostname:** systemd's `exec_needs_mount_namespace()` in v255 and v258 `src/core/execute.c` does not consider it. It only unshares UTS and adds a seccomp rule (`exec-invoke.c` `apply_protect_hostname`). Copies are in `sec2/sd/`.
   - **The syscall groups:** none holds a call the Go runtime, zfs, exportfs or udevadm makes (member list in `sec2/syscall-groups.txt`).
   - **Still commented out:**
     - the capabilities: the check covers neither NFS export nor the other writer's chown at create;
     - ProtectClock and the device allow-list: they need `/dev/zfs`, and zvols are not covered;
     - the memory limits: they need sizing.
   - `systemd-analyze security --offline`: 7.6 to 6.4 MEDIUM.
7. **Follow-up commit for H-10 (`4ba2f4c`).** The first H-10 test lived in `internal/endpoint` and read `../../api/...`. The VM leg runs `endpoint.test` without the source tree, so it would have failed there. It moved to `internal/server`, which runs in the unit job only.
8. **Other writers' areas:** not touched. No edits to `plan.go`, `table.go` or `internal/zfs`. I edited `reconcile.go` (`Reconcile()` and the `ReconcileRequest` comments only) and `cmd/zfs-endpoint/main.go` (startup and the probe).
9. **`-socket-group` dir mode.**
   - The csi socket's directory becomes 0710 with the group. That is required for a host process in the group, and harmless for a pod, which mounts the directory itself.
   - It refuses (78) when the directory holds anything but the socket, so it can never chmod a shared directory such as `/run`.
   - Dropping the flag later leaves the directory 0710, but the new socket is 0600.
10. **Gofmt fix by amend.** The last commit (`17696f4`) was amended once, before any report, to gofmt a line. Nothing is pushed.

## Commits (oldest first)

| commit | item |
|---|---|
| `30e769c` | 1 D3: Reconcile needs a dry run's digest to collect undesired volumes (proto comment and gen, README, ADR) |
| `0d80bf1` | 1 D7: `-socket-group` (socket 0660, its directory 0710, group-owned) |
| `22004f9` | 1 D9: stopped; the ADR records why and the options |
| `1cb3feb` | 2 H-10: the `desired_volume_ids` comment states the bound; gen regenerated |
| `da34c99` | 3 F5: the health probe WARNs of a later crossmnt ancestor and stays SERVING |
| `342a5a7` | 4 F12: a startup WARN when the parent's mountpoint is `o+x` |
| `38e43f5` | 5 H-16: `ci/real-zfs-unit.sh`, `ci/unitprobe`, guest and vm wiring; ProtectHostname and the syscall deny-list on |
| `4ba2f4c` | 2 H-10: the comment test moved to `internal/server` (see anomaly 7) |
| `17696f4` | 1 D3: the nightly FSM explorer reconciles with a digest |

## Per item

| item | test(s) | before | after |
|---|---|---|---|
| D3 | `internal/endpoint` `TestReconcileNeedsADigestToCollectUndesired` (contract test: simulator and VM) | `D3-before.txt`: `code = 0, want 9` | `D3-after.txt` |
| D7 | `internal/unixsock` `TestShareWithGroupOpensOnlyTheSocketAndItsDirectory`; `cmd` `TestSocketGroup` | `D7-unixsock-before.txt`: socket `Srw-------` gid 0, want 0660 gid 65534; a shared dir was accepted. `D7-cmd-before.txt`: gids not resolved, an unknown group exits 1 not 78 | `*-after.txt` |
| D9 | none (stopped) | `D9-probe.txt` | |
| H-10 | `internal/server` `TestProtoDocumentsTheMissingIDBound` (the bound is read from a gRPC answer) | `H10-server-before.txt`: lacks "first 1000", "sorted", `"(N more)"`, "more than 250000 ids" | `H10-server-after.txt` |
| F5 | `cmd` `TestHealthWarnsOfALaterCrossmntAncestor` (check function, sim etab, own logger) | `F5-health-before.txt`: 0 warnings for `on` and `crossmnt,...` | `F5-health-after.txt` |
| F12 | `cmd` `TestStartupWarnsOfATraversableMountpoint` | `F12-warn-before.txt`: 0755 and 0701 not warned | `F12-warn-after.txt` |
| 5 | `deploy` `TestUnitCheckNamesTheFailingSetting` (5 stub scenarios), `TestUnitCheckReadsTheDropIn`, `TestUnitLint` (new active/commented lists); `ci/unitprobe` `TestProbeReportsTheCodeItGot` | `item5-before.txt`: script missing, drop-in lacks the settings | `item5-after.txt` |

**buf.**
- `buf lint` is clean.
- `buf breaking --against <6e2cc66 export>` is clean (exit 0).
- The `.git#ref=` input was refused by the worktree guard, so the comparison was against a `git archive 6e2cc66` copy.
- Control: renaming `plan_digest` in a copy reports 2 breaking changes (exit 100), so the comparison bites.

**F5 design.**
- The probe logs a WARN when the covering lines appear or change, and an INFO when they are gone, so journald, with rate limiting off (F8), gets no line every 30s.
- An etab or mountpoint read failure is logged at Debug and leaves health as is.

**F12.**
- The check stats the mountpoint (following symlinks) and warns on `mode & 0o001`.
- It does not check ancestors.
- A mountpoint that is missing, `none` or `legacy` is quiet.
- `checkExposure` returns the mountpoint, so the check costs no extra zfs call.

## Item 5: the guest check

- `ci/real-zfs-guest.sh` starts `bash real-zfs-unit.sh "$PWD" > out/unit-hardening.log` in the background before the shards, waits for it after them, and folds its exit into rc.
- It uses its own 1G pool `zunit` on the guest tmpfs and instance `zfs-endpoint@zunit-k8s.service`.
- `zfs create` with `sharenfs=off` turns no share on (the shards' creates already run libshare's commit), so it does not disturb the NFS shard.
- `ci/real-zfs-vm.sh` builds `cmd/zfs-endpoint` and `ci/unitprobe` in parallel with the test binaries, copies them over with the unit, the drop-in and the script, prints the log, and adds the verdict to the summary.

**Steps (success path):**
1. install;
2. pool;
3. daemon-reload;
4. `systemctl start` (Type=notify);
5. health;
6. CreateVolume (64 MiB filesystem) and DeleteVolume on the csi socket;
7. admin: root dry-run Reconcile OK, uid 65534 `CONNECT`, modes opened, then uid 65534 PERMISSION_DENIED, modes restored;
8. EPERM grep of the unit's journal;
9. stop and destroy.

**Failure path:**
- It starts the unit alone ("the unit fails without the drop-in too: not a hardening setting").
- It then starts the unit with each active setting alone, and the verdict names them: `unit hardening: FAIL: drop-in settings that stop the endpoint serving: <Key=value ...>`.
- Each failing start prints `systemctl status` (15 lines) and the last 30 journal lines.
- Every bounded output uses awk, never head, `grep -q` or `grep -m`.

**Cost:**
- **VM:** estimated at about 1.5s of wall time for the success path: reload ~0.2s, start ~0.3s, create and delete ~0.5s, admin probes ~0.1s, stop ~0.1s, pool create and destroy ~0.3s. These are estimates, not measured; the verdict line prints the measured time on the first run. The check runs beside the shards, so the added critical-path time should be close to 0.
- **Host:** `go build` of the two binaries took 2.5s cold and 0.4s warm locally. It runs in parallel with `go test -c`, so on a warm cache it is hidden. The scp carries about 34 MB more (the two static binaries).
- **Unit job (ADR 0005):** the nine new top-level tests sum to 0.16s under race. The `deploy` package went to 0.72-0.95s (stub script runs in parallel). One new package, `ci/unitprobe`, is compiled and linked: about 0.09s to run plus its link. The full `go test -race ./...` took about 16s locally, as before.
- **VM endpoint shards:** one new contract test, `TestReconcileNeedsADigestToCollectUndesired`, at the default weight 0.1. Several existing reconcile tests do one more scan each.

## Gate (at `17696f4`)

| check | result |
|---|---|
| `gofmt -l .` (ignoring `.claude/`) | empty |
| `go vet ./...` / `go vet -tags nightly ./...` | clean |
| `go mod tidy -diff` | clean |
| `buf lint` | clean; `buf breaking` vs `6e2cc66` clean |
| `go test -race -count=1 ./...` | all ok (`sec2/gate-race.txt`) |
| `bash -n` | `ci/real-zfs-unit.sh`, `ci/real-zfs-guest.sh`, `ci/real-zfs-vm.sh` ok |
| actionlint | not run: no workflow touched |
| nightly `TestExploreLifecycle` | fails with the same 6 findings as `6e2cc66` (anomaly 3) |
