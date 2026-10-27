# Wave 13: startup seams (S5, S9/H-07, H-13, H-14, S10)

Branch `startup-seams` (local, not pushed), based on `claude/gallant-johnson-1qaxp5` @ bdfe867.

## Anomalies first

1. **The required commit trailer names a model** (`Co-Authored-By: Claude Opus 5.5`). That conflicts with "no model names in commits". I kept the trailer exactly as instructed and kept model names out of the subjects and bodies.
2. **`endpoint.Config` has no named "try once" value.** `LockWait == 0` means "30s default" in `endpoint.New`. Only `Endpoint.lock`'s comment says that a non-positive wait takes the lock with TryAcquire. To stay out of internal/endpoint, cmd passes `-lock-wait 0` as `tryOnce = -1ns` (`configLockWait`). `TestLockWaitZeroTriesOnce` pins the behaviour end to end, but the contract lives only in a comment. Follow-up: add an exported constant or field to `endpoint.Config`, and a test in that package.
3. **Part of the seams.md S5 evidence is stale.** It says `-lock-wait -1s` makes every RPC ABORTED. Since H-01, a negative wait is try-once: free locks are taken and held ones are refused at once. The fix is unchanged: negative values are refused with 78.
4. **S10: I added an escape hatch to the brief's rule.** The check runs only when `-property-prefix` was not given. Otherwise an operator who wants a fresh `zfs-endpoint` namespace beside old democratic-csi volumes has no way to start. seams.md suggested this exception ("unless the prefix was given explicitly"). The error message names both `-property-prefix democratic-csi` (adopt) and `-property-prefix zfs-endpoint`.
5. **`unixsock.CheckPath` now enforces the 107-byte limit for zfs-csi too** (`Target` and `ListenShared`). Such paths failed later before, at dial or bind. No zfs-csi test changed.
6. **A flake fix rode along.** In `TestLockWaitZeroTriesOnce`, a dial before the socket existed sometimes cost grpc's default 1s reconnect backoff. The test now dials with a 1ms base backoff and runs in about 0.01s. This is in commit a1e082d, not the S5 commit.
7. **Files outside cmd/ were touched:** `deploy/systemd/zfs-endpoint@.service` (comments, and `TimeoutStartSec` 90→100 for the new 10s probe) and `README.md` (two sentences). None are in ci/, the planner or internal/csi.
8. **`startupErr` returns nil for any startup-step error once ctx is done**, so a config error that races a SIGTERM is logged at Info and the exit is 0. A stop was asked for, so this is the intended behaviour.

## Commits

| sha | item |
|---|---|
| a61fead | S5: refuse a negative `-lock-wait` and `-nfs-min-prefix` outside 1-32; `-lock-wait 0` = try once |
| 7ff751f | S9/H-07: socket path >107 bytes is 78 (`unixsock.MaxPathLen` in CheckPath); lock conflict and lock name > NAME_MAX are 78; unit comment on the `\x2d` math |
| a1e082d | H-13: `O_NOFOLLOW` on lock files, symlink is 78 (plus the grpc backoff test fix) |
| bb5a417 | H-14: stop during the version probe or parent wait exits 0 (`startupErr`) |
| d06a79a | S10: legacy democratic-csi prefix check, 78; `TimeoutStartSec=100` |
| 475babf | docs: exit-code comment, README |

## Fail before / pass after

The before runs are the new tests against the pre-fix sources (the S9 sources restored from the previous commit; S10 run against bb5a417's main.go). Raw before output for all items: `/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/fail-before.txt`.

| test | before | after |
|---|---|---|
| TestStartupConfigRefusals / negative lock-wait | exit 1 (it reached Listen, so it would have served) | 78 `-lock-wait` |
| … / nfs-min-prefix 0 | exit 1 (would have served at /16) | 78 |
| … / nfs-min-prefix 33 | 78 from endpoint.New, message without the flag name | 78 `-nfs-min-prefix` |
| TestLockWaitZeroTriesOnce | 2nd call waited 5s and was cancelled (30s default used) | Aborted at once while the holder still runs |
| … / long socket path | exit 1 `bind: invalid argument` | 78 |
| … / long default socket (`tank/a-a-…`, `\x2d`) | exit 75 (after the parent wait) | 78 |
| … / long lock name | exit 1 `file name too long` | 78 |
| … / lock conflict | exit 1 | 78 |
| TestDefaultSocketLimit (84-byte escaped name = 107 bytes OK, +1 or one `-` over) | exit 1 / nil | 78 |
| TestSocketPathFitsSunPath (unixsock) | 108 bytes: bind EINVAL; Target accepted | refused, mentions 108; 107 serves |
| TestLockParentDoesNotFollowSymlinks | opened the symlink | refused, 78, target untouched |
| … / symlinked lock (run) | exit 1 (followed it, reached Listen) | 78 `symlink` |
| TestStopDuringStartupIsClean / parent wait | exit 1 `context canceled` | nil |
| … / version probe | exit 75 | nil |
| TestLegacyPrefixRefusal / legacy only | exit 1 (passed startup and reached Listen) | 78 naming `-property-prefix democratic-csi` |
| … / none, ours, both, false, inherited, adopted, explicit | pass the check | pass the check |

Tests that would otherwise serve forever use a technique I call notASocket: `-listen` names a regular file, so a missing check shows as Listen's exit 1 rather than a hang. Timers are injected (`deps.after`) and nothing sleeps. The only wall-clock bound is a failure-path guard (`time.AfterFunc(5s)`) in the try-once test. Every new test is `t.Parallel` except the try-once one, which serves a socket (Listen changes the process umask).

## Decisions

- **`-lock-wait 0` means try once.** H-01 already gives a non-positive wait that meaning, so 0 now says what it reads as, and the default no longer replaces it. **A negative value is 78**: no one means it.
- **`-nfs-min-prefix 0` is refused (78), not honoured.** A /0 floor would let PublishFileExport export to every IPv4 client. Reading 0 as /16 was silent. Refusal is the safer of the two options. The range check (1-32) moved into `run` so the message names the flag.
- **A lock conflict is 78, not 75.** systemd waits for an instance to stop before it restarts it, so a conflict is another endpoint, configured for the same or a nested parent. Retrying cannot clear it.
- **The length check lives in `unixsock.CheckPath` (`MaxPathLen = 107`).** It is the shared socket-path gate, so `Listen` and `Target` fail up front too. When the default socket overflows, the error says it comes from the escaped `-parent` and suggests a shorter `-listen`.
- **The unit's escaping math checks out.** `systemd-escape tank/k8s-vols` = `tank-k8s\x2dvols`, and `%i` keeps it literally, so each `-` costs 4 bytes. `/run/zfs-endpoint/` (18) + `.sock` (5) leaves 84 bytes for the escaped name. TestDefaultSocketLimit pins 84 as accepted and 85 as refused. The unit comment now says this.
- **The S10 probe is one `zfs get -d 1 -t filesystem,volume`** of `<prefix>:managed_resource,democratic-csi:managed_resource` on the parent (`zfs.Client.List`). It is bounded by `probeTimeout` and the startup context, and it counts only local `true` values (ADR 0003). It is skipped when the prefix is democratic-csi or `-property-prefix` was given. A zfs failure is 75, and a missing binary is 78.

## Checks

`gofmt -l` clean, `go vet ./...` clean, `go mod tidy -diff` clean, `go test -race -count=1 ./...` all ok (cmd/zfs-endpoint 1.3s under race).
