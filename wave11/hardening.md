# Hardening review: zfs-endpoint (wave 11)

HEAD 088e69b (`claude/gallant-johnson-1qaxp5`), Go 1.27.3. Repo untouched.
Skipped as instructed: NFS export verification (wave9/nfs-diff.md) and CSI request translation (wave9/csi-diff.md).

- Scratch copy with regression tests, **failing today**: `/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave11/hardening-copy/`
- Same tree with minimal fixes applied, all tests green incl. `-race`: `.../wave11/hardening-fixed/`, diff `.../wave11/hardening-fixes.patch` (404 lines)
- Run: `cd hardening-copy && go test ./... 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'` (19 `TestH_*` tests across 5 files; all 19 fail today). In `hardening-fixed` everything passes.
- All tests run against the simulator or fakes. Nothing here ran against real OpenZFS, nfs-utils or a booted systemd. Claims about zfs behaviour come from reading the OpenZFS source in `wave9/oz` and are marked where it matters.

Ranking is (likelihood x impact) / fix cost, with unexplained behaviour first.

## Findings

### H-01 A free lock is refused when the caller has under 1 s left (unexplained, wrong message)
`endpoint.lock` (endpoint.go ~l.321) subtracts `lockMargin` (1 s) from the caller's remaining time. When that is <= 0 it builds a context with a zero timeout, and `Keyed.Acquire` refuses a context that has already ended, "not even a free key". Result: `Aborted "operation pending on [vol:x]"` with nothing pending. Any mutating call arriving with under 1 s of budget fails this way, every time, and the message sends the operator hunting for a competing operation that does not exist.
- Test: `internal/endpoint/hardening_test.go` `TestH_ShortDeadlineStillTakesAFreeLock` (800 ms deadline, uncontended CreateVolume; fails with `operation pending on [vol:quick]`).
- Fix: when `wait <= 0`, use `e.locks.TryAcquire(keys...)` and return Aborted only if that fails (about 6 lines).
- Proof: same test passes; add a contended variant to check Aborted still comes back.

### H-02 CreateVolume writes to a dataset that `OwnedVolume()` says is not ours
`PlanCreateVolume` accepts `Managed && VolumeName == ""` as ours (`if !row.Managed {...}; if row.VolumeName != "" && ...`), while `Row.OwnedVolume` and `TestManagedWithoutNameIsNotOwned` say that state is not owned. For an uncommitted dataset with `managed_resource=true` and no name marker, `CreateVolume(name)` plans `refquota`/`volsize`/`refreservation` raises and stamps `provision_success`, then `readVolume` fails Aborted "changed owner". The dataset was modified and the call failed. Ownership rule appears in two places and they disagree (ADR 0003: never touch what you don't own). Increase-only, but still a write.
- Tests: `internal/endpoint/hardening_internal_test.go` `TestH_CreateDoesNotAdoptUnnamedManaged` (planner); `internal/endpoint/hardening_test.go` `TestH_CreateVolumeLeavesUnnamedManagedDatasetAlone` (end to end: refquota 1 MiB -> 64 MiB, provision_success=true).
- Fix: `if !row.Managed || row.VolumeName == "" { AlreadyExists }` and compare `VolumeName != r.Name` unconditionally. One line. Both tests pass.
- Check before shipping: democratic-csi adoption mode. Its driver writes both markers in one create (table.go comment), so no legitimate state is lost.

### H-03 A newline in a user-property value can forge a row for a dataset zfs never listed
`zfs get -Hp` prints values raw (`zprop_print_one_property`, `printf("%s", value)`), and libzfs places no character restriction on user property values (only length < 8192). `parseColumns` defends against forged lines only by noticing that a forged (dataset, property) pair collides with the real line. A forged line for a name that does not exist cannot collide. Worked example in the test: pvc-a's `csi_volume_name` is `"\tlocal\ntank/csi/phantom\tmanaged_resource\ttrue\tlocal\n..."`. The scan then holds a phantom owned, committed volume:
- `ListVolumes` returns it, and `CreateVolume("ghost")` reports success for a volume that does not exist (the PV binds, the mount fails).
- A phantom snapshot makes `DeleteVolume` of the real volume fail FailedPrecondition "has snapshot" (a denial of service on deletes).
- Who can do it: anyone who can set a user property on any dataset in the scan scope (a `zfs allow userprop` tenant) or who sends a replication stream carrying properties (`recv` stores them; the output ignores the source).
- Why block framing does not fix it: the forged block can sit at the end of the last property of a dataset and look contiguous.
- Tests: `internal/zfs/hardening_test.go` `TestH_ForgedLineForNonexistentDataset` (ListColumns with a fake zfs); `internal/endpoint/hardening_test.go` `TestH_ScanRejectsRowsZFSNeverListed` (ListVolumes returns `ghost`).
- Minimal fix (done in hardening-fixed): after parsing, run a second `get -t ... type` over the same scope (no user text in it) and refuse any name in the main output that is not in it. Fails closed with an error; the race (dataset destroyed between the two calls) gives a spurious error that a retry clears. Cost: one extra cheap zfs call per scan.
- Better long-term: `zfs get -j` (OpenZFS 2.3+, so available under ADR 0004's 2.4 floor) is unforgeable by construction, but the simulator needs a JSON mode.

### H-04 A call with no client deadline has no deadline
Nothing server-side bounds a call; `LocalRunner` has no per-command timeout. A zfs hung on a suspended pool (failmode=wait) then holds its limiter slot, its volume lock and its goroutine until restart. CSI sidecars set deadlines, so this bites on a direct gRPC client (grpcurl, a script) or a buggy caller.
- Test: `internal/server/hardening_test.go` `TestH_CallWithoutDeadlineGetsOne`.
- Fix: in `Daemon.unary`, if `ctx.Deadline()` is absent, wrap with `context.WithTimeout(ctx, DefaultCallTimeout)`. 10 minutes is above the slowest planned Reconcile (a 16k-volume plan took 31 s before the O(rows) fix).

### H-05 A panic in any handler kills the daemon
grpc-go does not recover handler panics. The daemon dies, every other call in flight is cut and their zfs children get SIGKILL via Pdeathsig, mid-operation. The system is idempotent, so it self-heals, but an avoidable blast radius for one bad zfs output.
- Test: `internal/server/hardening_test.go` `TestH_HandlerPanicDoesNotKillTheDaemon` (child process, exits 2 with the panic today).
- Fix: `recover()` in `Daemon.unary` returning `codes.Internal` and logging the stack (done in hardening-fixed, 8 lines).

### H-06 Whole-parent scans are unbounded in memory under concurrency
`DeleteVolume`, `CreateSnapshot`, `ListVolumes`, `ListSnapshots` and Reconcile all scan the whole parent (`zfs get -r`, 33 properties, roughly 3.6 KB of output per dataset, parsed into a second copy). Locks are per volume and `zfs.Limit` bounds processes, not the buffers behind them. A namespace deletion with 100 provisioner workers across 10k volumes is about 36 MB x 100 x 2 of live scan data. At 100k volumes one scan is about 0.7 GB. Not measured against real zfs; sizes are an estimate from the property list.
- Test: `internal/endpoint/hardening_test.go` `TestH_WholeParentScansAreBounded` (32 concurrent DeleteVolume, limiter 8: 8 whole-parent scans in flight).
- Fix: a small semaphore (2) around `scan(ctx, "")` (done, 8 lines). Longer term, DeleteVolume only needs the target's snapshots plus their `clones` property, not every dataset.

### H-07 A long socket path fails late, with a restartable exit code
`sockaddr_un.sun_path` is 108 bytes. The unit builds `/run/zfs-endpoint/%i.sock` from the escaped dataset name (`-` in a name becomes `\x2d`, 4 bytes). A parent of roughly 85+ characters passes every check, takes the parent flock, waits for the pool, then fails at `bind` with exit 1. `RestartPreventExitStatus=78` does not apply, so systemd restarts it forever. Lock file names have the same cliff at 251 characters (`ENAMETOOLONG`).
- Test: `cmd/zfs-endpoint/hardening_test.go` `TestH_LongSocketPathIsAConfigError`.
- Fix: in `socketPath`, `len(path) > 107` -> `configErr`. 3 lines.

### H-08 CSI snapshot names "." and ".." become zfs snapshot names zfs rejects
`snapComponent` uses the CSI name as the snapshot component whenever `ValidSnapshot` accepts it, and `snapRE` accepts `.` and `..`. OpenZFS `entity_namecheck` rejects both (NAME_ERR_SELF_REF / PARENT_REF), so a legal CSI name (any Unicode up to 128 bytes) fails the create. Same root as H-11.
- Test: `internal/endpoint/hardening_internal_test.go` `TestH_SnapshotComponentNeverDots`.
- Fix: the H-11 fix to `ValidSnapshot` makes `snapComponent` hash these names. No separate change.

### H-09 Errors and logs echo caller input without bound
`errorf("... %q", name)` copies the whole input into the status message that is returned and logged. With `MaxMsgSize` at 64 MiB a 1 MiB name yields a 1 MiB error (4x worse with escapes). `Server.DeleteVolume` logs `volume_id` before validation. Lock keys and the "operation pending on %v" message carry the raw name too. `ExitError.Stderr` is unbounded (a destroy that fails with a long clone list prints it all). The socket is 0600 so the caller is trusted, but one buggy client turns into log and memory amplification.
- Tests: `internal/endpoint/hardening_internal_test.go` `TestH_ErrorsDoNotEchoUnboundedInput`; `internal/endpoint/hardening_test.go` `TestH_APIErrorsAreBounded`; `internal/zfs/hardening_test.go` `TestH_StderrIsBounded`.
- Fix: clamp in `errorf` (512 B) and in `LocalRunner` (stderr 8 KiB). The `slog` fields in `server.go` (`volume_id`, `snapshot_id`) need the same clamp; not done in hardening-fixed.
- Log injection itself is not a problem: `slog.NewJSONHandler` escapes control characters. Bidi and format characters (U+202E) in CSI snapshot names pass `unicode.IsControl` and reach the journal; cosmetic.

### H-10 Reconcile answers with one Skipped entry per desired id that names nothing
No cap on `desired_volume_ids`; a set naming nothing we own (another cluster's, a page of a list) gets a `SkipDesiredMissing` for each id. 128-byte ids fit about 450k in the 64 MiB request, and the answer (id + reason) then exceeds the 64 MiB send limit after any destructive apply, or on every dry run (so the digest can never be obtained).
- Test: `internal/endpoint/hardening_test.go` `TestH_ReconcileBoundsItsOwnAnswer` (200k ids -> 200k skips).
- Fix: keep the first 1000 such skips plus one `(N more)` entry, and cap the set at 250k (`MaxDesiredVolumeIDs`). Done in hardening-fixed.
- Also consider lowering `MaxMsgSize`: 64 streams x 64 MiB is 4 GiB of possible receive buffers per connection. 100k ids is 4.2 MB, so 16 MiB leaves ample room.

### H-11 zfs name and property validation is looser than zfs, and than its own comments
Reachable today only through `snapComponent` (H-08), because RPC ids cannot contain `/` and property keys come from an allowlist. Defence in depth for the package that builds the command line:
- `ValidDataset("tank/..")`, `("a/./b")` and `ValidSnapshot("x@..")` are true; zfs refuses them. `cmd/zfs-endpoint` works around it for `-parent` only.
- `propRE` allows a leading `-`. `Set` passes `k=v` without `-o`, so key `-u` produces argv `set -u=1 ds`, which getopt reads as a flag cluster (an error, not an exploit: `zfs set` has only `-u`). `Inherit(ds, "-r")` becomes `inherit -r ds`.
- `Get` and `ListColumns` join caller-supplied property names into one argv word with no check, so `Get(ds, "-r")` becomes a recursive get. Internal callers pass constants.
- Tests: `internal/zfs/hardening_test.go` `TestH_NamesAndPropsNeverBecomeOptions`.
- Fix: reject `.`/`..` components in both validators; make `propRE` start with `[a-z0-9]`; validate Get/ListColumns property names (allow `all`). Done in hardening-fixed.
- Verified OK (no change needed): leading `-`, `=`, `,`, space, newline, NUL, non-ASCII and over-255 names are all rejected by `datasetRE`/`snapRE` (Go `$` has no trailing-newline allowance). Property values reject `\n`, `\t`, NUL; `-`, `=`, `,` are safe because they follow `k=` inside one argv word. Values have no length cap, but they are allowlist-canonical or IP lists with their own 8191 check.

### H-12 Children inherit the daemon's locale
`classify` matches libzfs's English literals; `LocalRunner` passes the full environment, and systemd hands `/etc/locale.conf` to services. OpenZFS ships no translations so this is latent, but a distro that adds them turns every classified failure into `Internal`.
- Test: `internal/zfs/hardening_test.go` `TestH_ChildRunsInTheCLocale`. Fix: `cmd.Env = append(os.Environ(), "LC_ALL=C", "LANGUAGE=C")`.

### H-13 Lock files are opened without O_NOFOLLOW
`SecureDir` already limits who can plant a symlink to root or the daemon's uid, so this is hygiene. Test: `cmd/zfs-endpoint/hardening_test.go` `TestH_LockParentDoesNotFollowSymlinks`. Fix: `| syscall.O_NOFOLLOW`.

### H-14 SIGTERM while waiting for the pool exits 1
`waitParent` returns `ctx.Err()`, exit code 1; a `systemctl stop` during a slow import leaves the unit in `failed`. Test: `cmd/zfs-endpoint/hardening_test.go` `TestH_SignalWhileWaitingForParentIsNotAFailure`. Fix: treat a cancelled startup as a clean stop.

### H-15 ParseEtab silently stops at a line over 1 MiB
`bufio.Scanner` returns `ErrTooLong`, `sc.Err()` is not read, and every later line is dropped. Unpublish then sees "not served" and reports done. exportfs does not write such lines; low likelihood. Test: `internal/endpoint/hardening_internal_test.go` `TestH_ParseEtabSurvivesLongLine`. Fix: raise the buffer and fail the read (Unavailable) if `sc.Err() != nil`. The tested fix only raises the buffer; checking `sc.Err()` is still to do. Note that NFS verification code is being changed in another stream, so fold this in there.

### H-16 systemd unit: sandboxing that does not break mounts
Not covered by a test. Offline `systemd-analyze security` (systemd 255 here): current unit 8.3 EXPOSED. With the additions below, 4.6 OK, and `systemd-analyze verify` finds no syntax errors. Drop-in used: `.../wave11/unit/hardened/zfs-endpoint@.service`.

What cannot be added, and why (so nobody re-adds it): `ProtectSystem`, `ProtectHome`, `PrivateTmp`, `PrivateDevices`, `ReadWritePaths`/`ReadOnlyPaths`, `ProtectKernel*`, `ProtectControlGroups`, `ProtectProc`/`ProcSubset`, `PrivateMounts`. All create a private mount namespace, and systemd.exec(5) (MountFlags=, from memory; please confirm) downgrades propagation to slave, so `zfs create`/`zfs mount` mounts would never reach the host or nfsd. The unit's existing comment is right. `ProcSubset=pid` would also hide `/proc/fs/nfsd/threads`, which Publish reads. `PrivateNetwork` and `IPAddressDeny=any` break exportfs, which resolves names in other exports on every `-ra` (the failure mode the NFSClient comment describes). `PrivateUsers` breaks root-owned kernel interfaces.

Safe additions (none uses a mount namespace):
```
CapabilityBoundingSet=CAP_SYS_ADMIN CAP_DAC_OVERRIDE CAP_DAC_READ_SEARCH CAP_FOWNER CAP_CHOWN CAP_SETUID CAP_SETGID
AmbientCapabilities=
RestrictNamespaces=yes
ProtectClock=yes
ProtectHostname=yes
KeyringMode=private
DevicePolicy=closed
DeviceAllow=/dev/zfs rw
SystemCallFilter=~@clock @cpu-emulation @debug @module @obsolete @raw-io @reboot @swap
SystemCallErrorNumber=EPERM
LimitCORE=0
MemoryHigh=1G
MemoryMax=2G
```
Caveats, to settle in `ci/real-zfs-vm.sh` before shipping (not run here: no zfs, nfs-utils or booted systemd in this sandbox):
- The capability list is from reading, not from tracing. CAP_SYS_ADMIN covers /dev/zfs ioctls and mount(2); DAC_OVERRIDE and DAC_READ_SEARCH cover mountpoints and export paths under other users' directories. Run once with `AmbientCapabilities=` unchanged and `CapabilityBoundingSet=` set, and watch the journal for EPERM from zfs and exportfs.
- Dropping CAP_SYS_MODULE means libzfs cannot lazy-load the module; the unit already orders After=zfs-import.target.
- `SystemCallFilter` is the deny-list form on purpose; `~@privileged` and `~@mount` would break zfs. Tighten later with `SystemCallLog=` evidence.
- `MemoryMax` values are guesses; size them from H-06 (about 1 GB per 100k volumes of scan data), and note `OOMScoreAdjust=-500` is inherited by every zfs child.
- `RestrictAddressFamilies` keeps `AF_NETLINK AF_INET AF_INET6` as is.

## Checked, no finding
- **Area 2, NFS options.** `ParseClient` was probed with 30 inputs: leading zeros (`010.0.0.1`, `/08`), `::ffff:` mapped forms, `/0`, `/33`, `/+24`, zones, whitespace, newline, `,`, `:`, full-width digits, `0x`, short forms, IPv6, a trailing `/`, double prefixes. All rejected. The text comes out as digits, `.` and `/` only, so `ShareNFS` cannot gain an option beyond `rw=`/`ro=` lists. StorageClass parameters cannot reach `sharenfs` (denied explicitly, user properties refused, values canonicalised from enumerations). Overlaps are caught in both directions and on decode. `0.0.0.0/1` is reachable only if the operator sets `-nfs-min-prefix 1`. Accepted but harmless: `127.0.0.1/32`, `0.0.0.0/32`, `255.255.255.255/32`, multicast prefixes with a floor <= 4.
- **Area 3, filesystem.** Socket is created 0600 under umask 0177 (not chmod-after-bind), abstract sockets refused, existing non-socket paths and symlinks at the socket path refused, a live socket refused, directory must be a real directory owned by euid/root and not group/other-writable, unit has `RuntimeDirectoryMode=0700` and `UMask=0077`. Residual: only the leaf directory is checked (a writable grandparent could swap it; not the case under `/run`). `realPath(mountpoint)` follows symlinks on the parent filesystem. A symlink planted where an unmounted volume's directory lives can make Publish treat another export's etab entry as its own (a false pass) or make Unpublish fail forever, never an escalation; not tested. The `mountpoint` property is not constrained to lie under the parent's mountpoint, so a privileged actor retargeting a managed volume's mountpoint gets that path exported; privileged already, not tested.
- **Area 4, concurrency.** The key lock hands over FIFO, removes cancelled waiters and passes a key on if handover and cancel race; reviewed, no defect. `zfs.Limit` waits on a channel (FIFO) and gives up with the caller's context. Probe shares the limiter, so a saturated pool flips health to NOT_SERVING, which is intended. Paging: server caps `max_entries` at 1000 and rejects negatives; `page` treats 0 as unbounded but only the server calls it with a capped value.
- **Area 5, shutdown.** Drain, GracefulStop, Stop, `stopWait` and the 45 s `TimeoutStopSec` fit together (30 + 10 + the 5 s SIGTERM->SIGKILL gap). `grpc.Server.Serve` blocks until Stop completes, so `<-served` is sound. A second signal arriving before `secondSignal()` registers is swallowed by NotifyContext (microseconds, ignored). SIGHUP is unhandled and would exit without a drain; systemd never sends it. `LocalRunner` sends SIGKILL to `-pid` after `Wait` has reaped the child whenever the context has ended, even if the command finished first; a recycled pid group would be hit. Needs a pid wrap within microseconds, so not filed.
- **Area 7, ownership.** Beyond H-02 all mutating paths gate on `OwnedVolume`/`ServedVolume`/`OwnedSnapshot` and read markers with `Local()` only, so inherited and received values do not count. Renames break the `VolumeName == ID` match. Clones outside the parent surface as zfs refusals, not as destroys. `DeleteVolume` never uses `-r`. The clone-source snapshot create uses `OkExists` and relies on the lock plus the pre-check; an administrator racing the same name between scan and apply would be treated as ours (existing test `cloneRacer` covers the clone target but not the snapshot name). Moving a dataset with markers from another endpoint's parent adopts it by design.
- **Deployment note, not verified.** `unixsock.SecureDir` refuses a directory writable by group/other, and kubelet creates emptyDir volumes 0777, so `zfs-csi -csi-address unix:///csi/csi.sock` probably cannot start in a stock CSI pod unless the socket directory is made 0750 or similar. Not tested in a cluster.

## Index

```json
[
  {"id":"H-01","area":"4 resource limits / locks","severity":"medium","likelihood":"medium","regression_test":"/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave11/hardening-copy/internal/endpoint/hardening_test.go::TestH_ShortDeadlineStillTakesAFreeLock","fails_today":true,"fix":"endpoint.lock: when wait<=0 use locks.TryAcquire instead of a zero-timeout Acquire","test":"same test passes; plus a contended variant still returns Aborted"},
  {"id":"H-02","area":"7 ownership","severity":"medium","likelihood":"low","regression_test":"/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave11/hardening-copy/internal/endpoint/hardening_test.go::TestH_CreateVolumeLeavesUnnamedManagedDatasetAlone","fails_today":true,"fix":"PlanCreateVolume: treat Managed with empty VolumeName as not ours (AlreadyExists), compare VolumeName != r.Name unconditionally","test":"TestH_CreateDoesNotAdoptUnnamedManaged (planner level, no zfs)"},
  {"id":"H-03","area":"1 input / 7 ownership (property values)","severity":"high","likelihood":"low","regression_test":"/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave11/hardening-copy/internal/endpoint/hardening_test.go::TestH_ScanRejectsRowsZFSNeverListed","fails_today":true,"fix":"ListColumns cross-checks every name against a second `zfs get -t ... type` over the same scope (or switch to zfs get -j)","test":"/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave11/hardening-copy/internal/zfs/hardening_test.go::TestH_ForgedLineForNonexistentDataset"},
  {"id":"H-04","area":"4 resource limits / 5 shutdown","severity":"medium","likelihood":"low","regression_test":"/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave11/hardening-copy/internal/server/hardening_test.go::TestH_CallWithoutDeadlineGetsOne","fails_today":true,"fix":"Daemon.unary: wrap ctx with DefaultCallTimeout (10m) when the client set no deadline","test":"same test passes"},
  {"id":"H-05","area":"5 shutdown / robustness","severity":"medium","likelihood":"low","regression_test":"/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave11/hardening-copy/internal/server/hardening_test.go::TestH_HandlerPanicDoesNotKillTheDaemon","fails_today":true,"fix":"recover() in Daemon.unary, return codes.Internal, log the stack","test":"same test passes (child process exits 0)"},
  {"id":"H-06","area":"4 resource limits","severity":"high","likelihood":"medium","regression_test":"/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave11/hardening-copy/internal/endpoint/hardening_test.go::TestH_WholeParentScansAreBounded","fails_today":true,"fix":"semaphore (2) around scan(ctx, \"\"); longer term, per-volume clone lookup in DeleteVolume","test":"same test passes (peak whole-parent scans <= 2)"},
  {"id":"H-07","area":"3 filesystem / 6 unit","severity":"medium","likelihood":"low","regression_test":"/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave11/hardening-copy/cmd/zfs-endpoint/hardening_test.go::TestH_LongSocketPathIsAConfigError","fails_today":true,"fix":"socketPath: reject paths over 107 bytes with configErr (exit 78); same for lock file names over 255 bytes","test":"same test passes"},
  {"id":"H-08","area":"1 input validation","severity":"low","likelihood":"low","regression_test":"/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave11/hardening-copy/internal/endpoint/hardening_internal_test.go::TestH_SnapshotComponentNeverDots","fails_today":true,"fix":"fixed by the H-11 change to ValidSnapshot (snapComponent then hashes . and ..)","test":"same test passes"},
  {"id":"H-09","area":"4 log volume / message size","severity":"medium","likelihood":"low","regression_test":"/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave11/hardening-copy/internal/endpoint/hardening_test.go::TestH_APIErrorsAreBounded","fails_today":true,"fix":"clamp errorf messages (512 B), ExitError.Stderr (8 KiB) and the volume_id/snapshot_id log fields","test":"TestH_ErrorsDoNotEchoUnboundedInput and zfs TestH_StderrIsBounded pass"},
  {"id":"H-10","area":"4 very large Reconcile desired-sets","severity":"medium","likelihood":"low","regression_test":"/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave11/hardening-copy/internal/endpoint/hardening_test.go::TestH_ReconcileBoundsItsOwnAnswer","fails_today":true,"fix":"cap desired ids (250k) and report at most 1000 SkipDesiredMissing entries plus a count","test":"same test passes"},
  {"id":"H-11","area":"1 input validation (zfs command line)","severity":"low","likelihood":"low","regression_test":"/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave11/hardening-copy/internal/zfs/hardening_test.go::TestH_NamesAndPropsNeverBecomeOptions","fails_today":true,"fix":"reject . and .. components in ValidDataset/ValidSnapshot; propRE must start with [a-z0-9]; validate Get/ListColumns property names","test":"same test passes"},
  {"id":"H-12","area":"1 / 4 robustness","severity":"low","likelihood":"low","regression_test":"/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave11/hardening-copy/internal/zfs/hardening_test.go::TestH_ChildRunsInTheCLocale","fails_today":true,"fix":"LocalRunner: cmd.Env = append(os.Environ(), \"LC_ALL=C\", \"LANGUAGE=C\")","test":"same test passes"},
  {"id":"H-13","area":"3 filesystem","severity":"low","likelihood":"low","regression_test":"/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave11/hardening-copy/cmd/zfs-endpoint/hardening_test.go::TestH_LockParentDoesNotFollowSymlinks","fails_today":true,"fix":"add syscall.O_NOFOLLOW to the lock file open","test":"same test passes"},
  {"id":"H-14","area":"5 shutdown","severity":"low","likelihood":"medium","regression_test":"/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave11/hardening-copy/cmd/zfs-endpoint/hardening_test.go::TestH_SignalWhileWaitingForParentIsNotAFailure","fails_today":true,"fix":"run(): when waitParent fails because ctx ended, return nil","test":"same test passes"},
  {"id":"H-15","area":"4 resource limits","severity":"low","likelihood":"low","regression_test":"/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave11/hardening-copy/internal/endpoint/hardening_internal_test.go::TestH_ParseEtabSurvivesLongLine","fails_today":true,"fix":"larger scanner buffer and fail the read when sc.Err() != nil (the second half is not in hardening-fixed)","test":"same test passes; add a case asserting an Unavailable error when a line exceeds the new limit"},
  {"id":"H-16","area":"6 systemd unit","severity":"medium","likelihood":"medium","regression_test":null,"fails_today":true,"fix":"add the no-namespace directives listed under H-16 to deploy/systemd/zfs-endpoint@.service","test":"CI step: systemd-analyze --offline=true security on the instantiated unit, exposure <= 5.0 (currently 8.3, 4.6 with the drop-in); then ci/real-zfs-vm.sh with the hardened unit for the real-zfs and NFS cases"}
]
```
