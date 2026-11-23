# Adversarial review 2: scan/table, endpoint locking, server/cmd, CI

Tree: 8ceb254 ("Accept any CSI snapshot name up to 128 bytes"). The tree moved while this review ran: 74f9092 already fixed the ENOSPC classification ("size is greater than available space") and the 2G CI pool, so those are not reported.
Proof tests, run in a copy of the module:
- .../scratchpad/review2/mod/internal/endpoint/review3_test.go
- .../scratchpad/review2/mod/internal/zfs/review3_test.go

Run them with `cd .../scratchpad/review2/mod && go test -run TestR2 -v ./internal/...`.

## 1 (High). An exported or missing pool reads as "volume absent", so DeleteSnapshot reports false success
- table.go:192-195 (`only != "" && zfs.IsNotFound(err)` returns an empty Table), endpoint.go:497 (DeleteSnapshot), :381 (GetVolume), :280/:317 (CreateVolume, readVolume)
- `zfs get` opens its root with zfs_open → make_dataset_handle → zfs_standard_error(ENOENT). That prints "cannot open 'tank/vols/v': dataset does not exist" (libzfs_dataset.c:724, libzfs_util.c:447) even when the *pool* is not imported. So this text is identical for "volume missing", "parent missing" and "pool exported". main.go checks the parent only once, at startup.
- Scenario: the pool is exported, or not yet imported after a reboot. DeleteSnapshot("v@s") gets an empty table, plans nothing, and returns OK. The CO deletes its VolumeSnapshotContent and the snapshot leaks forever. GetVolume returns NotFound. CreateVolume plans a create, which then fails with "no such pool" (Unavailable). Only that last case is right.
- Proof: TestR2ExportedPoolDeleteSnapshotFalseSuccess.
- Fix: on not-found from scan(only), Get(Parent) once. Only when the parent exists does an empty table mean absent. Otherwise return Unavailable.

## 2 (Medium). A second daemon silently steals a live socket, and the two then lose mutual exclusion
- cmd/zfs-endpoint/main.go:252 `os.Remove(path)` runs unconditionally before Listen.
- Scenario: an operator runs a second instance, or a restart overlaps with the old process. The new process unlinks the running instance's socket and binds its own. Both processes serve the same parent, each with its own in-memory `lock.Keyed`. Clients that are still connected to the old one, plus new clients on the new one, can then run CreateVolume and DeleteVolume on the same id concurrently. This is exactly what the locks exist to prevent.
- Fix: dial the path first and refuse to start if it answers. Better, take `flock` on `<parent-derived>.lock` (or the socket dir) for the process lifetime.

## 3 (Medium). The socket is world-connectable between Listen and Chmod, and the default socket dir is never created
- main.go:255-260. `net.Listen` creates the socket as 0777&^umask, and chmod 0600 comes afterwards. Under umask 0 the measured mode is `Srwxrwxrwx` (proof: TestR2SocketModeBeforeChmod). Any local user who connects in that window keeps an unauthenticated root-equivalent gRPC connection after the chmod, because chmod does not revoke connections that already exist. Under the common umask 022 the socket is 0755, so others lack the write bit and cannot connect; the window therefore needs a permissive umask (some container runtimes and `UMask=0000` units).
- The default `/run/zfs-endpoint/endpoint.sock` fails at startup with "bind: no such file or directory": /run is tmpfs and nothing creates the dir.
- Fix: `os.MkdirAll(dir, 0700)`, plus `syscall.Umask(0177)` around Listen (or listen in a 0700 dir). Close `l` when chmod fails (today it leaks).

## 4 (Low). Shutdown can hang indefinitely, and repeated SIGTERM/SIGINT are swallowed
- main.go:219,239-245. GracefulStop has no deadline. After the first signal, `signal.NotifyContext`'s `stop` is never called, so later signals stay captured and are ignored.
- Scenario: a pool is suspended under failmode=wait (the default). zfs children sit in D state, and exec.CommandContext's kill cannot reap them, so the handlers never return. Ctrl-C (or a second SIGTERM) does nothing, and only SIGKILL ends the process.
- Fix: call `stop()` after ctx.Done so the default signal behaviour returns, and add `time.AfterFunc(grace, srv.Stop)`.

## 5 (Low). A missing zfs binary produces an error that hides the cause
- runner.go:26-28 (ExitError.Error omits Underlay) with errors.go:74-78. Exists() correctly returns an error rather than false. The message, though, is `zfs get tank/vols: unknown: [zfs-does-not-exist get ...]: exit -1: ` and does not contain "executable file not found" (proof: TestR2MissingBinaryMessage).
- Fix: when Code == -1, append `Underlay` in ExitError.Error, or don't wrap non-exit errors as ExitError.

## 6 (Low). Snapshot ids still exceed 128 bytes for long volume ids (new in 8ceb254)
- plan.go:369-375. The hashed form is `vol@csi-<32hex>`, which is len(vol)+37 bytes. volumeID accepts up to 128 bytes, so any volume id over 91 bytes yields a snapshot_id over CSI's 128-byte limit. That is the same failure the commit set out to fix, moved to a different place.
- Fix: also hash or shorten when len(vol)+37 > 128 (e.g. a 16-hex hash), or cap volume ids at 91 bytes.

## 7 (Low). A pool-root parent makes the pool's own snapshots look like snapshots of a volume
- table.go:154 and :199-202. With Parent "tank", `tank@x` keeps ID `tank@x` (TrimPrefix does not match). Its vol is "tank", which has no "/", so DeleteFunc keeps the row.
- Scenario: there is a volume named "tank" and an admin runs `zfs snapshot -r tank@nightly`. Two rows then have ID `tank@nightly`. CreateSnapshot("nightly","tank") returns AlreadyExists ("managed=false") (proof: TestR2PoolRootParentDuplicateIDs). The binary search in Get can return either row.
- Fix: drop rows whose Name does not start with Parent+"/".

## Checked and ruled out
- `-d` depth: Depth+1 means parent=0, child=1, child@snap=2 (snapshots count one level below their dataset). scan("") with `-d 2` and scan(vol) with `-d 1` therefore include the intended rows, and the parent row is removed.
- Sort order: Get's binary search and NewTable use the same strings.Compare. SnapshotsOf and ClonesOf use maps rather than contiguity, so '@'(0x40) vs '/'(0x2F) or '-'/'.' ordering cannot break grouping.
- An origin outside the parent is harmless: clonesOfSrc is keyed by the full name, and external clones surface as HasClones from `destroy -r`.
- Locks: keys are sorted and deduped, so there is no deadlock. CreateVolume from a snapshot or volume holds `vol:<source>`, which DeleteVolume, CreateSnapshot and DeleteSnapshot also take. DeleteVolume(b) defer-destroys `src@volume-source-for-volume-b` without `vol:src`. Every interleaving with DeleteVolume(src) either refuses (clone present) or ends with OkMissing. Hashed snapshot ids lock a different `snap:` key in CreateSnapshot than in DeleteSnapshot, but both take `vol:`.
- scanFor across ids: per-volume scans under held locks, deduped by vol. CreateSnapshot's re-read via scan(source) finds the new row.
- CI: main exists and PR #1 exists, so `push: [main]` plus `pull_request` runs. The ubuntu-24.04 kernel is 6.17.0-1021-azure, and its `linux-modules-6.17.0-1021-azure` .deb contains kernel/zfs/zfs.ko.zst and spl.ko.zst (checked with dpkg-deb -c), so `modprobe zfs` works. Userland is zfsutils-linux 2.2.2-0ubuntu9.5 against a 2.3.x kmod, a mismatch that works (commit 74f9092 reports a real run on OpenZFS 2.3.4). `sudo -E` with an absolute go path works. GOCACHE/GOMODCACHE under /home/runner get root-owned entries, which stay readable, so setup-go's cache save is unaffected.
- Simulator vs real: the 16K volblocksize default holds on OpenZFS ≥2.2, and real zvol snapshots report volsize. The thick-zvol space failures were already fixed in 74f9092.
