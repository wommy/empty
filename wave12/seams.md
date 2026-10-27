# Wave 12: seams and edges (HEAD a02c5b0, claude/gallant-johnson-1qaxp5)

I hunted read-only and checked every claim against the code. The probes live in the scratch copy:

- `wave12/seams-copy/internal/endpoint/seams_probe_test.go` (`go test ./internal/endpoint -run Seam -v`)
- `wave12/seams-copy/cmd/zfs-endpoint/seams_probe_test.go` (`go test ./cmd/zfs-endpoint -run Seam -v`)

Each probe passes while the disagreement it shows is still present. All 10 pass at HEAD.

I checked the OpenZFS 2.4 facts against `openzfs/zfs@zfs-2.4.0` `cmd/zfs/zfs_main.c`. I checked the systemd facts with `systemd-analyze verify` (systemd 255).

## Ranking: (likelihood × blast) / fix cost

| # | id | kind | seam | L | blast | fix cost | pinned |
|---|----|------|------|---|-------|----------|--------|
| 1 | S2 | bug/anomaly | endpoint ↔ zfs (volsize rounding) | M | stuck PVC; an uncommitted clone leaks until reconcile; the error says "a retry converges" | 1 line | no |
| 2 | S6 | bug | endpoint ↔ zfs CLI (create/clone mount step) | M | a committed FILESYSTEM whose `mountpoint` in volume_context is not mounted, so writes land in the parent | small | no |
| 3 | S3 | anomaly | planner ownership rule ↔ OwnedVolume | L | writes `provision_success` on a dataset that is not ours (ADR 0003), then CreateVolume returns ABORTED forever | 1 line | no (the existing test covers Get/Delete only) |
| 4 | S5 | config | flags ↔ runtime | L | `-lock-wait <0` makes every RPC ABORTED; `0` values are silently defaulted | trivial | no |
| 5 | S4 | edge | CSI pagination token ↔ churn | M | a list walk dies with ABORTED when the token's volume is deleted | small | no |
| 6 | S9 | seam | daemon ↔ systemd exit codes | L-M | an over-long socket path or a lock conflict exits 1, which `Restart=on-failure` with `StartLimitIntervalSec=0` restarts forever | trivial | no |
| 7 | S1 | anomaly | zfs CLI output ↔ parseColumns | L | phantom volumes in List; Reconcile plans them; they can flip `stated` | medium (`zfs get -j`) | partly (only the collision case) |
| 8 | S12 | gap | zfs-csi Probe ↔ endpoint health | M | liveness probe says Ready while the pool is suspended | trivial | no |
| 9 | S8 | edge | Reconcile desired set ↔ id rules | L | one foreign-format id fails the whole GC run | trivial | no |
| 10 | S7 | upgrade | nfs_clients → sharenfs-only (ADR 0004) | L now | after a downgrade, ControllerUnpublish fails FAILED_PRECONDITION for good | small | no |
| 11 | S10 | upgrade | `-property-prefix` default changed (7f5cb39) | L | an upgrade without the flag orphans every volume; it fails safe, but it is an outage | small | no |
| 12 | S11 | gap | zfs-csi ↔ csi-driver-nfs, StorageClass ↔ `properties` | known | FILE_EXPORT and PROPERTIES cannot be reached through CSI | roadmap | n/a |
| 13 | S13 | edge | Reconcile ↔ wall clock | L | a forward jump defeats MinAge | n/a | no |

## Anomalies first

### S2. A clone of a zvol is rounded to the default 16K, not to the source's volblocksize

- **What the two sides assume.** `Planner.want` rounds a Block request to `props[volblocksize]`, and falls back to `VolBlockSize` (16K). `cloneBlockSize` forces a clone to keep its source's block size. zfs refuses a `volsize` that is not a multiple of the zvol's own volblocksize.
- **Where it breaks.** Take a source with volblocksize=128K and a clone that grows to a decimal size ("2G" = 2e9). `sizeOps` plans `zfs set volsize=2000011264`.
  - First attempt: `ABORTED "op 3/4 … (earlier ops applied; a retry converges)"`. The retry does not converge.
  - Every later attempt: `INTERNAL "op 1/2 set … must be a multiple of volume block size (131072)"`. The resume branch replans the same op forever.
  - `validateCreate`'s limit check uses the same wrong rounding.
- **Probe:** `TestSeamCloneRoundsToDefaultNotSourceBlockSize`
- **Fix.** In `PlanCreateVolume`, when there is a source, compute `want = p.roundTo(r.CapacityBytes, src.BlockSz)`. Pass the source row into `want` (sourceSize already finds it). Also classify "must be a multiple of" as `KindInvalidProperty`.
- **Test.** Planner level, in `plan_test.go`: a 128K-volblocksize source row, a 2e9 request, and assert that `volsize % 131072 == 0`. Add a sim contract test that runs the clone twice.

### S6. A FILESYSTEM volume is committed while unmounted

- **What OpenZFS 2.4 does.** `zfs_do_create` and `zfs_do_clone` both end with `(void) zfs_mount_and_share(...)` (zfs_main.c:921 and :1342). If the mount fails, zfs prints "filesystem successfully created, but not mounted" (or "…may only be mounted by root") and still exits 0.
- **The second path to the same state.** A caller-deadline SIGTERM usually lands while the create ioctl runs. The ioctl completes, and the process dies before the userland mount. The retry's resume branch then plans only the commit.
- **What the endpoint does.** It never looks at `Row.Mounted` on create. `readVolume` returns `Mountpoint` regardless.
- **Effect.** CSI gets a volume whose `mountpoint` context names a plain directory in the parent dataset.
  - Publish recovers, because `zfs set sharenfs` mounts the volume.
  - Any node-side or hostPath consumer writes into the parent instead.
  - In `-allow-non-root` mode this is expected, but nothing says so in the response.
- **Probe:** `TestSeamResumeCommitsUnmountedFilesystem`. The plan for an uncommitted, unmounted, canmount=on row is `[set provision_success]` only.
- **Fix.** Add an `OpMount` (`zfs mount <ds>`, ok if already mounted) before the commit for root-mode FILESYSTEM rows with canmount=on and `!Mounted`, in both the fresh and the resume plans. Alternatively, after apply, re-read and return UNAVAILABLE (retryable) instead of committing.
- **Test.** Planner unit (the probe, inverted). Add a sim test where `Sim.Unmount` runs after create and before commit.

### S3. The resume branch's ownership rule is not `OwnedVolume`

- **Where it breaks.** `PlanCreateVolume` checks `!row.Managed`, then `row.VolumeName != "" && != r.Name`. A managed row with no name passes that check. `OwnedVolume` says that row is not ours: TestManagedWithoutNameIsNotOwned pins this for Get and Delete only.
- **Effect.** CreateVolume writes `committed_at` and `provision_success` onto the foreign dataset. `readVolume` then returns ABORTED "vanished or changed owner", and does so on every retry.
- **Probe:** `TestSeamResumeStampsRowOwnedVolumeRejects`. The mutation is `set …committed_at …provision_success=true tank/v/m`, and the result is ABORTED.
- **Fix.** Replace both checks with `if !row.OwnedVolume() { AlreadyExists }`.
- **Test.** Extend `TestManagedWithoutNameIsNotOwned` with a CreateVolume call: no mutations, ALREADY_EXISTS.

### S1. Forged newline lines can create a dataset that does not exist

- **The claim.** The comment on `parseColumns` says "the forged line always collides with that dataset's real one".
- **Where it breaks.** That is true only for datasets that exist. A value of any requested column (our prefix's user properties) containing `\n…` can add a complete block of lines for a name that has no real lines, so nothing collides.
- **Effect.**
  - ListVolumes serves `ghost`.
  - Reconcile plans `destroy ghost`, which is harmless because of OkMissing.
  - A ghost named like an id in the desired set makes `stated` true, which bypasses adv4 F2. That collection is still capped by max_deletions.
  - Ghost snapshots or clones can block real deletes and creates.
- **Who can do it.** Anyone who can set our namespace's properties: root or a delegated user.
- **Probe:** `TestSeamGhostRowFromForgedNewlines`
- **Fix.** Scan with `zfs get -j --json-int`. OpenZFS 2.4 is the only supported version (ADR 0004), and its JSON escapes values. Short of that, correct the comment and treat the risk as accepted.
- **Test.** A parser fuzz property: no `Names` entry that is absent from the input's line-start set, run on JSON input.

## Bugs, edges and gaps

**S5. Config zero and negative values.**
- `-lock-wait -1s` passes flag parsing and `endpoint.New`. `context.WithTimeout(neg)` is already done, and `Acquire` refuses even a free key, so every mutating RPC returns ABORTED.
- `-lock-wait 0` and `-nfs-min-prefix 0` start the daemon and are silently replaced with the defaults (30s and /16).
- Probes: `TestSeamNegativeLockWaitAbortsEverything` and `TestSeamFlagZeroValuesAccepted`.
- Fix: `configErr` (78) in `run` for values ≤ 0.
- Test: add rows to `TestStartupRefusals`.

**S4. The page token is the last id served.**
- `page()` searches for the exact id. If that volume or snapshot is deleted between pages (normal churn while a CO lists), the next page is ABORTED "invalid starting token". A retry with the same token never succeeds; the walk has to start over.
- Probe: `TestSeamPageTokenDeletedBetweenPages`
- Fix: items are already id-sorted, so resume at `sort.Search(id > token)`. Keep ABORTED only for a token that cannot be an id.
- Test: extend `TestPagination` (endpoint package) with a delete between pages.

**S9. Exit codes towards systemd.**
- A socket path of 108 bytes or more passes `CheckPath`, then fails at bind with "bind: invalid argument" and exit 1.
- `lockParent` EWOULDBLOCK also exits 1.
- `StartLimitIntervalSec=0` restarts both forever.
- The unit's `%i` keeps systemd escapes literally in arguments (systemd 255: `tank/k8s-vols` → `/run/zfs-endpoint/tank-k8s\x2dvols.sock`). Each `-` costs 4 bytes, so long dataset names reach the sun_path limit, and the zfs-csi `-endpoint` must carry that literal backslash.
- Probe: `TestSeamLongSocketPathExitsOne`
- Fix: check `len(path) < 108` in `CheckPath` → 78. Make a lock conflict 78 (or 75 if a draining predecessor is expected). Document the `\x2d` form in the unit comment.
- Test: `TestStartupRefusals` rows.

**S12. zfs-csi Probe ignores the endpoint's health service.**
- `Identity.Probe` calls `GetCapabilities`, which never runs zfs.
- `Daemon.Probe` turns the grpc health service NOT_SERVING on a suspended or exported pool, but nothing in zfs-csi reads it.
- Fix: Probe calls `healthpb.Check{Service: server.ServiceName}` and maps NOT_SERVING to `Ready=false` (or FAILED_PRECONDITION).
- Test: csi harness with a fake health server.

**S8. A desired set with one id the endpoint could never issue fails the whole Reconcile.**
- `Reconcile` runs `volumeID` on every desired id and fails the run with INVALID_ARGUMENT, for example on an id from another driver such as `ns/pv`.
- A well-formed but unknown id, by contrast, is only skipped. Everywhere else, a non-issuable id is "not found" (`issuedVolumeID`).
- Probe: `TestSeamDesiredSetOneBadIDFailsRun`
- Fix: skip it with `SkipDesiredMissing`.
- Test: `TestPlanReconcile` row.

**S7. Downgrade across the nfs_clients removal.**
- ADR 0004 / csi-nfs-design §1 plan a version that writes only canonical `sharenfs`.
- If an operator rolls back to HEAD, `foreign()` (`!NFSClientsSet && ShareNFSLocal`) calls those volumes foreign, and Unpublish of a served client returns FAILED_PRECONDITION. A node detach is then stuck.
- Probe: `TestSeamCanonicalShareNFSWithoutClientListIsForeign`
- Fix, to ship before the migration: when `nfs_clients` is unset, treat a local sharenfs that parses as exactly `ShareNFS(cs)` (canonical, CIDR-only) as ours.
- Test: `export_test` planner rows.

**S10. The `-property-prefix` default flipped from `democratic-csi` to `zfs-endpoint` in 7f5cb39.**
- The unit file passes no flag. Any parent provisioned by an earlier build becomes "not managed":
  - DeleteVolume returns FAILED_PRECONDITION.
  - CreateVolume retries return ALREADY_EXISTS.
  - List is empty.
- It fails safe, but it is an outage.
- Fix: a startup check. When children carry a local `democratic-csi:managed_resource` and none carry ours, log loudly or refuse with 78 unless the prefix was given explicitly.
- Test: cmd startup test against a sim.

**S11. The CSI front end cannot reach two endpoint capabilities.**
- FILE_EXPORT: zfs-csi does not report `PUBLISH_UNPUBLISH_VOLUME`, implements no ControllerPublish, and allows only SINGLE_NODE access modes.
- `volume_context` has no `server` or `share`, which csi-driver-nfs requires (csi-nfs-design G).
- PROPERTIES: `parseParams` refuses every StorageClass key except `sparse` and `reserve`, so the proto's "properties (StorageClass parameters)" cannot be reached.
- This is a known roadmap item; it is listed so nobody re-hunts it.

**S13. Wall-clock ageing.**
- `tooYoung` compares `Now()` with zfs `creation` and `committed_at`.
- A forward NTP step after a boot with a bad RTC ages in-flight and just-committed rows at once. Locked calls are still protected by TryAcquire; rows between provisioner retries are not.
- A backward step is safe.
- Fix (cheap guard): refuse to collect when any row's creation is later than Now.
- Test: a `PlanReconcile` row.

## Seams that are well pinned (do not re-hunt)

- **Ownership.** Only local properties count; received or renamed copies are not owned (TestInheritedOwnershipIsNotOwnership, TestReceivedCopyIsNotOwned, TestReconcileSparesReceivedAndRenamed, TestLiteralDashMarkerIsAValue).
- **Missing parent.** A missing or exported parent is UNAVAILABLE, never absent, for both whole and partial scans.
- **Duplicate forged lines.** A forged line for an existing dataset fails the scan (TestParseColumnsRejectsForgedLines).
- **Kill and interrupt.**
  - KindInterrupted maps to ABORTED or DEADLINE_EXCEEDED.
  - A cancel kills the child's whole process group (TestChildRunsInItsOwnProcessGroup, TestCancelKillsTheChildsGrandchildren).
  - A Limit waiter respects its context.
- **opErr.** An unknown failure after the first op is ABORTED; busy on a destroy is FAILED_PRECONDITION.
- **Locks.** FIFO, try-acquire, a done context takes nothing, reconcile never waits.
- **Daemon lifecycle.** Drain, interrupt, cancelled handlers, health, sd_notify, startup refusals, parent wait, version series check (TestCheckVersion, TestVersion).
- **Sockets.** Owner-only, stale socket replaced, never deletes a non-socket, refuses abstract and relative paths.
- **NFS.**
  - CIDR-only clients, canonical encoding, etab parsing and octal escapes, overlap and mode conflicts (adv4 export tests).
  - Lifecycle against the real kernel on the VM leg.
- **Reconcile.**
  - max_deletions and digest, commit race (adv4 F1), partial desired set (F2), per-group failure (F3), busy lock skip (F8).
  - Stamped clone sources, legacy origin released with its clone, no `destroy -r`, a snapshot cut after the scan is spared.
- **Messages and sizes.** Message sizes match across zfs-csi and the endpoint; never-issued ids return NOT_FOUND or OK; capacity wrap.
- **Properties.** The allowlist, canonicalisation, deny list and convergence.

### Fidelity gaps: pinned only in the simulator

These tests skip on the real leg (`ci/expected-skips.txt`), so the libzfs behaviour behind them is unproven:

- TestFileExportUnmountedVolume: a `set sharenfs` mounts an unmounted canmount=on filesystem.
- TestFileExportResolvesSymlinks
- TestSetNoMountLeavesUnmounted
- TestFileExportReceivedShareNFS

Unknown: whether a re-run of `set sharenfs=off` after a kill re-runs `exportfs -ra` (the reshare path in Unpublish).

## JSON

```json
[
{"id":"S2","seam":"endpoint planner <-> zfs volsize/volblocksize","kind":"anomaly","severity":"medium","likelihood":"low-medium","evidence":"plan.go want()/roundTo uses props or 16K default, never the source's BlockSz; sizeOps sets volsize=2000011264 on a 128K clone; first error says 'a retry converges' (ABORTED), later INTERNAL forever","probe":"internal/endpoint/seams_probe_test.go TestSeamCloneRoundsToDefaultNotSourceBlockSize","pinned_by_test":false,"fix":"round want to src.BlockSz when a source exists; classify 'must be a multiple of' as KindInvalidProperty","test":"plan_test planner row: 128K source, 2e9 request, volsize%131072==0; sim test retries clone twice"},
{"id":"S6","seam":"endpoint <-> zfs CLI create/clone mount step","kind":"bug","severity":"medium","likelihood":"medium","evidence":"OpenZFS 2.4 zfs_main.c:921/1342 '(void) zfs_mount_and_share' => exit 0 when mount fails; SIGTERM after ioctl skips mount; PlanCreateVolume never reads Row.Mounted; resume plans only the commit","probe":"internal/endpoint/seams_probe_test.go TestSeamResumeCommitsUnmountedFilesystem","pinned_by_test":false,"fix":"OpMount (zfs mount, ok-if-mounted) before commit for root-mode FILESYSTEM with canmount=on && !Mounted, or UNAVAILABLE instead of commit","test":"planner unit on unmounted uncommitted row; sim test Unmount between create and commit"},
{"id":"S3","seam":"PlanCreateVolume ownership <-> Row.OwnedVolume","kind":"anomaly","severity":"low-medium","likelihood":"low","evidence":"plan.go:189-193 checks Managed and a non-empty VolumeName only; managed row with no name gets provision_success+committed_at written, then readVolume ABORTED every retry","probe":"internal/endpoint/seams_probe_test.go TestSeamResumeStampsRowOwnedVolumeRejects","pinned_by_test":false,"fix":"if !row.OwnedVolume() -> AlreadyExists","test":"extend TestManagedWithoutNameIsNotOwned: CreateVolume makes no mutation, ALREADY_EXISTS"},
{"id":"S5","seam":"flags <-> endpoint.Config","kind":"bug","severity":"medium","likelihood":"low","evidence":"-lock-wait -1s accepted; lock() WithTimeout(neg) => Acquire refuses free keys => every mutating RPC ABORTED; -lock-wait 0 and -nfs-min-prefix 0 silently defaulted","probe":"TestSeamNegativeLockWaitAbortsEverything; cmd/zfs-endpoint TestSeamFlagZeroValuesAccepted","pinned_by_test":false,"fix":"configErr (78) for <=0 in run()","test":"rows in TestStartupRefusals"},
{"id":"S4","seam":"CSI pagination token <-> concurrent deletes","kind":"edge","severity":"low","likelihood":"medium","evidence":"endpoint.go page(): token must equal a current id; deleting that item between pages => ABORTED for the rest of the walk","probe":"internal/endpoint/seams_probe_test.go TestSeamPageTokenDeletedBetweenPages","pinned_by_test":false,"fix":"resume at first id > token (items id-sorted); ABORTED only for an unparseable token","test":"extend TestPagination with a delete between pages"},
{"id":"S9","seam":"daemon <-> systemd restart policy","kind":"seam","severity":"low-medium","likelihood":"low-medium","evidence":"socket path >=108 bytes passes CheckPath then bind EINVAL exit 1; lockParent conflict exit 1; StartLimitIntervalSec=0 restarts forever; %i keeps \\x2d literally (systemd 255 verified), 4 bytes per '-'","probe":"cmd/zfs-endpoint/seams_probe_test.go TestSeamLongSocketPathExitsOne; systemd-analyze verify on a %i unit","pinned_by_test":false,"fix":"CheckPath len<108 -> 78; lock conflict -> 78; document the \\x2d socket name","test":"TestStartupRefusals rows"},
{"id":"S1","seam":"zfs get -Hp output <-> parseColumns","kind":"anomaly","severity":"medium","likelihood":"low","evidence":"zfs.go parseColumns rejects only repeated (name,prop); newline-forged block for a nonexistent name yields a served ghost volume and a reconcile action; comment claims collision is certain","probe":"internal/endpoint/seams_probe_test.go TestSeamGhostRowFromForgedNewlines","pinned_by_test":false,"fix":"scan with zfs get -j --json-int (2.4-only floor); else fix the comment and accept the risk","test":"fuzz: parsed Names subset of real line-start names, JSON scan"},
{"id":"S12","seam":"zfs-csi Identity.Probe <-> endpoint grpc health","kind":"gap","severity":"low","likelihood":"medium","evidence":"csi/identity.go Probe calls GetCapabilities (no zfs); Daemon.Probe sets NOT_SERVING on a suspended pool, unread","probe":"suspend pool: endpoint health NOT_SERVING, CSI Probe Ready=true","pinned_by_test":false,"fix":"Probe calls health Check(server.ServiceName)","test":"csi harness with fake health server"},
{"id":"S8","seam":"Reconcile desired set <-> id validation","kind":"edge","severity":"low","likelihood":"low","evidence":"reconcile.go:536 volumeID on each desired id => INVALID_ARGUMENT for whole run, unlike issuedVolumeID semantics","probe":"internal/endpoint/seams_probe_test.go TestSeamDesiredSetOneBadIDFailsRun","pinned_by_test":false,"fix":"skip non-issuable ids as SkipDesiredMissing","test":"TestPlanReconcile row"},
{"id":"S7","seam":"upgrade/downgrade nfs_clients -> sharenfs-only","kind":"seam","severity":"medium","likelihood":"low","evidence":"export.go foreign(): local sharenfs without nfs_clients is foreign => Unpublish FAILED_PRECONDITION after a rollback from the planned version","probe":"internal/endpoint/seams_probe_test.go TestSeamCanonicalShareNFSWithoutClientListIsForeign","pinned_by_test":false,"fix":"treat a local canonical ShareNFS value as ours when nfs_clients unset (ship before the migration)","test":"export_test planner rows"},
{"id":"S10","seam":"upgrade: -property-prefix default democratic-csi -> zfs-endpoint (7f5cb39)","kind":"seam","severity":"medium","likelihood":"low","evidence":"unit passes no -property-prefix; older builds wrote democratic-csi:*; new build sees nothing owned","probe":"provision with old build, restart with HEAD: ListVolumes empty, DeleteVolume FAILED_PRECONDITION","pinned_by_test":false,"fix":"startup check: other-prefix markers and none of ours => warn or 78 unless prefix given explicitly","test":"cmd startup test against sim"},
{"id":"S11","seam":"zfs-csi <-> csi-driver-nfs volume_context; StorageClass <-> properties","kind":"gap","severity":"low","likelihood":"known","evidence":"no PUBLISH_UNPUBLISH, no server/share in volume_context, parseParams refuses property keys","probe":"ControllerGetCapabilities lacks PUBLISH_UNPUBLISH_VOLUME although FILE_EXPORT advertised","pinned_by_test":false,"fix":"roadmap (csi-nfs-design §5)","test":"csi controller_test once implemented"},
{"id":"S13","seam":"Reconcile ageing <-> wall clock","kind":"edge","severity":"medium","likelihood":"low","evidence":"tooYoung uses cfg.Now vs creation/committed_at; forward step defeats MinAge for unlocked rows","probe":"PlanReconcile with Now jumped +2h over a just-committed row","pinned_by_test":false,"fix":"refuse to collect when any row creation > Now (clock went back) and log large steps","test":"PlanReconcile table row"}
]
```
