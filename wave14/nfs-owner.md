# wave14 nfs-owner: root ownership (N11), the mountpoint pin, and the below-floor report

- **Branch:** local `nfs-owner`, created from `claude/gallant-johnson-1qaxp5` at 863c432. Not pushed.
- **Worktree:** `/home/user/empty/.claude/worktrees/agent-a2ac62479cb0b507d`.
- **Commits:** 4e26cf3 (N11), ea69d56 (pin), ed18594 (below-floor). 32 files, +1424/-42.
- **Evidence:** `/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave14/nfs-owner-ev/` ("ev/" below).

## Anomalies (read first)

1. **No real ZFS or nfsd was run here.** Two new real-pool tests and two new crash cases have only run on the simulator:
   - `TestFileExportCreateRootOwner` (serial, needs nfsd);
   - `TestCreateVolumePinsMountpoint` (parallel);
   - crash cases `create-nfs-root` and `restore-nfs-root-mode` (nfs: true).

   `LocalRoot` (the real chown/chmod) was tested on this host's filesystem, with an injected mount table that makes a temporary directory look like the dataset's zfs mount. Its first run against a real zfs mount will be the VM leg. The weight lines are estimates: 0.90s and 0.44s, from sibling tests. Regenerate them after the first VM run.
2. **Commit 4e26cf3 alone would break the VM crash leg.** It added `mountpoint` to the crash fingerprint as a raw value. On a real pool that value contains the per-run parent name, so the uninterrupted run and every crash run differ. ea69d56 fixes this: mountpoints are now written relative to the parent's, with `(local)` or `(inherited)`. The simulator never showed the problem, because its parent name is fixed. I did not rewrite history. Squash the two commits if you want each one green on the VM.
3. **`golang.org/x/sys` moved from an indirect to a direct requirement** (same v0.47.0, no new module). `LocalRoot` needs `unix.Openat2` with `RESOLVE_NO_SYMLINKS`, which `syscall` does not offer. `go mod tidy -diff` is clean.
4. **`table.go` touched, despite the JSON-scan writer.** It is one clearly separated hunk with no new column:
   - a `MountLocal bool` field on `Row`, after `CanMount`;
   - one line after the `Row` literal: `r.MountLocal = cell(cMount).Local()`. The mountpoint column was already scanned.

   No parsing code changed. Expect a one-line conflict next to the `NFSServerElsewhere` line.
5. **CONTEXT.md does not exist at the repo root**, although AGENTS.md and docs/agents/domain.md point to it. I wrote no glossary entry.
6. **One extra `zfs get mountpoint <parent>` per new filesystem create**, NFS or not. It is a read, not a mutation, so the crash table's "a repeat mutates nothing" still holds. No call-count test changed.
7. **Test fixtures changed to model pinned volumes.** `fsv` (export_test.go) now sets `MountLocal`, and the a4 harness's `vol` (adv4_test.go) creates with `mountpoint` set locally, because CreateVolume now makes volumes that way. Without these changes, eight reconcile tests would also report the new `SkipMountpointNotPinned`.

## What was built

### 1. N11, root ownership (4e26cf3)

- **Proto:** `NFSExport` gains `optional uint32 uid = 4; optional uint32 gid = 5; optional uint32 mode = 6;`.
  - Regenerated with buf. `buf lint` is clean. `buf breaking --against` the 863c432 tree exits 0. The reverse direction reports the three deleted fields, which shows that the check bites.
  - There is no new message. The fields are documented as CreateVolume-only, never set in `Volume.nfs`.
- **CSI:** the StorageClass parameters `nfs.uid`, `nfs.gid` and `nfs.mode` (`ParamNFSUID`, `ParamNFSGID`, `ParamNFSMode`).
  - Ids are decimal, 0 to 2147483647.
  - The mode is octal digits only, at most 7777, with an optional leading 0 ("0775", "775" and "2775" are accepted).
  - Anything else is InvalidArgument for the value, including a sign, "0x", "0o", "_" and 4294967295.
  - Outside NFS mode the keys are refused, with the message naming `-nfs-server`.
- **Endpoint validation:** `validRoot` applies the same ranges (`MaxRootID` = 2^31-1, mode ≤ 07777). Anything else is InvalidArgument, and no dataset is made.
- **Where the change runs: the endpoint**, as a new op, `set-root` (`OpSetRoot`, Props uid/gid/mode, Path = the mountpoint from the scan), behind `Config.Root RootSetter`.
  - `LocalRoot` (Linux) works on the descriptor, never the path twice:
    1. `openat2(mountpoint, O_DIRECTORY|O_NOFOLLOW, RESOLVE_NO_SYMLINKS|RESOLVE_NO_MAGICLINKS)`. A symlink anywhere on the path is FailedPrecondition.
    2. `fstat` the descriptor.
    3. Read `/proc/self/mountinfo`. The mount on top at that point must be `zfs`, from this dataset, with root `/`, on the descriptor's device. Otherwise the call is FailedPrecondition, or Aborted when nothing is mounted there.
    4. `fchown(-1 for unset)`, then `fchmod`.
  - Not Linux: `LocalRoot` refuses with FailedPrecondition.
  - The simulator implements `RootSetter` itself (`Sim.SetRoot`, `Sim.RootOf`). It models root:root 0755 at create, and a snapshot carries the root into a clone or receive. `SetRoot` is refused unless the dataset is mounted at that path.
- **Ordering and crash resume:**
  - A create that asks for an owner never commits in its first pass. The resume (the second pass, or any retry) plans `mount` if needed, then `set-root`, then the commit.
  - For NFS, the commit is split off and verified, as before, so `set-root` also precedes the NFS verification.
  - `set-root` is planned on every resume, because nothing records that it ran. It is idempotent, so a crash before or after it converges by running it again.
  - An uncommitted volume that is not mountable (mountpoint none/legacy, canmount off), or unmounted on an endpoint that cannot mount, is FailedPrecondition.
- **Decisions, recorded in ADR 0004's implementation notes and design §13 Q1 "As built":**
  - **Defaults: unset leaves that attribute as the create made it.** That is root:root 0755 for a new volume, and the source's for a clone or restore. The endpoint never widens a root on its own. See Decisions 1 for the consequence.
  - **Not volume state.** Nothing records the values, a Volume never reports them, and a repeat of a committed create is not compared on them. A tenant who owns the root may chmod it, and a CreateVolume retry must not then become ALREADY_EXISTS.

### 2. Mountpoint pin (ea69d56)

- **The create:** a new filesystem, created or cloned, NFS or not, gets `mountpoint=<parent's mountpoint>/<id>` set locally. It is in the same `-o` block as F12's `setuid/exec/devices=off`, and F12 is unchanged. A planner test asserts F12 alongside the pin, and mutant B7 drops F12 and is killed.
  - The parent's mountpoint is read once per new filesystem (`parentMountpoint`) and passed to the planner as `Planner.parentMount`, the same way as `now`.
  - A parent at `none` or `legacy` pins nothing.
- **An existing volume is never re-pinned.** No resume or repeat writes a mountpoint.
- **Volumes made before the pin are reported, not rewritten.** Reconcile adds `SkipMountpointNotPinned` for a committed filesystem whose mountpoint is inherited and is a path.
  - The endpoint does not pin these itself, because `zfs set mountpoint` unmounts and remounts the volume, and its export, under its users.
  - The message tells the operator how to pin one, and that zfs remounts it.
- **The CSI N5 test asserts the pin again** (wave13 dropped that assertion). Share is set locally, and ListVolumes' share is unchanged after the parent's mountpoint moves.
- **Docs:** ADR 0004's notes, ADR 0001's F12 section (one bullet), and design §4 and the header.

### 3. Below-floor report (ed18594)

- **The report:** Reconcile adds `SkipNFSBelowFloor + ": <net> <mode>, …"` for a volume whose local sharenfs, in the grammar, has a network broader than `-nfs-min-prefix` (default /16 on a bare planner). Nothing is rewritten.
- **Out of scope for this report:** foreign and inherited values.
- **Docs:** design §2, the §9.1 row and §10 "As built", and ADR 0004's notes.

## Proof (fail-before / pass-after)

- **Base:** the new tests do not compile against 863c432, because the API is absent.
- **Behavioural fail-before, by mutants of the new code.** Each mutant was applied alone, its tests run, and the file restored (`ev/A-mutants.txt`, `ev/B-mutants.txt`, `ev/C-mutants.txt`). All 20 were killed.

  | | Mutant | Killed by |
  |---|---|---|
  | M1 | set-root absent (the feature off) | planner, contract, crash, CSI and server tests |
  | M2 | set-root after the commit | crash table: a crash at the commit leaves root:root |
  | M3 | first pass commits | crash table, k0-after and k1-before |
  | M4 | symlinks followed | `TestLocalRoot` |
  | M5 | any dataset's mount accepted | `TestLocalRoot` |
  | M6 | device not checked | `TestLocalRoot` |
  | M7 | presence lost on the wire (unset read as 0) | `TestNFSRootOwnerPresence`, `TestNFS_N11` |
  | M8 | CSI range unchecked | `TestNFS_N11` |
  | M9 | endpoint range unchecked | `TestValidateNFSRoot` |
  | B1 | no pin | planner, contract, CSI N5 and crash fingerprint |
  | B2 | parent mountpoint never read | planner, contract, CSI N5 and crash fingerprint |
  | B3 | none/legacy pinned | `TestPlanCreateVolumePinsMountpoint` |
  | B4 | no report | `TestPlanReconcileMountpointNotPinned`, contract |
  | B5 | report too broad | `TestPlanReconcileMountpointNotPinned` |
  | B6 | `MountLocal` never read | `TestCreateVolumePinsMountpoint` |
  | B7 | F12 dropped | `TestPlanCreateVolumePinsMountpoint`, `TestFilesystemsMountWithoutSetuidExecOrDevices` |
  | C1 | no report | `TestPlanReconcileNFSBelowFloor` |
  | C2 | configured floor ignored | `TestPlanReconcileNFSBelowFloor` |
  | C3 | inherited value read as ours | `TestPlanReconcileNFSBelowFloor` |
  | C4 | no mode in the report | `TestPlanReconcileNFSBelowFloor` |

- **Crash resume:** the crash table (`TestCrashAtEveryMutationConverges`) gains `create-nfs-root` and `restore-nfs-root-mode`.
  - `faultRoot` counts `set-root` as a mutation in the same sequence as the zfs calls. Every point passes, before and after each of create, set-root and commit.
  - The fingerprint now includes each mounted filesystem's root (`root=uid:gid:mode`) and the mountpoint's local/inherited state.
- **Idempotency:**
  - `TestLocalRoot` applies the change twice.
  - The crash table checks that a repeat after success mutates nothing.
  - `TestFileExportCreateRootOwner` checks that a repeat with another owner is OK and leaves the root unchanged.

## Gate (final tree, ed18594)

| Check | Result |
|---|---|
| `gofmt -l .` (ignoring `.claude/`) | empty |
| `go vet ./...` and `go vet -tags nightly ./...` | OK |
| `go mod tidy -diff` | clean |
| `buf lint` | OK |
| `buf breaking` against 863c432 | rc 0 |
| `buf generate` | no drift |
| `go test -race -count=1 ./...` | all ok (`ev/C-race.txt`; baseline `ev/baseline-race.txt`) |
| `GOOS=darwin go vet ./internal/endpoint` | OK (`root_other.go`) |
| Nightly model sweep, reduced (`MODEL_SEQS=1500`) | ok in 33s. The full 20,000-sequence sweep exceeds a 280s local timeout, as documented (3 to 4 minutes on 4 cores). |

## Test-time cost (ADR 0005)

- **Unit job:** about 0.15s in total. The new endpoint tests take under 0.1s together (`TestLocalRoot` 0.02s, two crash cases 0.02s), CSI N11 0.02s, and server 0.01s. The `internal/endpoint` race package time moved 10.9s → 9.3s → 8.5s across runs, which is noise.
- **VM leg, estimated:**
  - `TestFileExportCreateRootOwner`: about 0.9s, serial, because exportfs is global;
  - `TestCreateVolumePinsMountpoint`: about 0.4s, parallel;
  - the two crash cases: on the PR leg each runs uninterrupted plus one rotated crash point, about 4 extra runs of a few hundred ms inside the already-parallel crash test;
  - one extra `zfs get` per filesystem create, a few ms each.

  Total: about 2-3s of test time spread over the shards, so roughly 1s of wall.

## Decisions (contested or irreversible, with recommendations)

1. **Default root ownership (contestable, reversible). Recommend keeping it as built.**
   - Unset leaves root:root 0755, so an NFS StorageClass without `nfs.uid`/`nfs.gid`/`nfs.mode` still provisions volumes that no non-root pod can write. That is wave13's anomaly 5, now fixable per StorageClass but not fixed by default.
   - The alternatives were a world-writable default (0777 or 1777) or squash-uid ownership (65534). Either widens access silently, and a clone default other than "keep the source's" would rewrite restored data's owner.
   - Document `nfs.uid`/`nfs.gid`/`nfs.mode` in the StorageClass examples, which do not exist yet (no NFS-mode deploy, wave13 anomaly 13). Making zfs-csi require one of them in NFS mode is a one-line change if the owner prefers to fail closed.
2. **Proto field numbers 4-6 on NFSExport are irreversible once released.** The project is pre-release. `Volume.nfs` reuses the message but never sets them.
3. **The pin covers every filesystem volume, not only NFS ones.** The `mountpoint` key of volume_context has the same problem as `share`.
   - The pin is written into ZFS on new volumes. An older build ignores it, and reversing it is a `zfs inherit mountpoint` (which remounts).
   - Existing volumes are reported, never pinned automatically. If the owner wants an opt-in "pin now" action, it belongs with Reconcile's planned `reshare`-style actions.
4. **Ownership is not compared on a committed volume.** This is deliberate (file metadata, which the tenant may change). It is reversible.

## Files

- **New:**
  - `internal/endpoint/root.go`, `root_linux.go`, `root_other.go`;
  - tests: `internal/endpoint/nfs_root_test.go`, `root_linux_test.go`, `export_root_contract_test.go`, `mountpin_test.go`, `mountpin_contract_test.go`, `belowfloor_test.go`, `internal/csi/nfs_root_test.go`, `internal/server/root_test.go`.
- **Changed:**
  - `api/zfsendpoint/v1/endpoint.proto` and `gen/`;
  - `internal/endpoint/{plan.go, apply.go, endpoint.go, reconcile.go, table.go (one hunk), plan_string.go}`;
  - `internal/server/server.go`, `internal/csi/controller.go`, `internal/zfs/zfstest/sim.go`;
  - `internal/endpoint/crash_test.go`;
  - `ci/real-zfs-weights.txt` (two lines);
  - the docs: ADR 0001, ADR 0004, the design doc and the README.
