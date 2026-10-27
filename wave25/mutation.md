# Mutation sweep: safety guards in internal/endpoint, internal/server, internal/csi

Snapshot: commit 96c49ff, copied to `wave25/mut`. Other builders have changed the live repo since, so all `file:line` references below point at 96c49ff.
Harness: `wave25/run.py` (runner), `wave25/mutants.py` (the 189 mutants with their exact old and new text), `wave25/table.py` (builds the raw table), `wave25/results*.jsonl` (one JSON record per mutant, with the test output tail).
Raw table: `wave25/mutation-raw.md`.

Each mutant applies one edit to one guard: it deletes the check (`if false && (...)`), drops one conjunct, or widens a bound. Then the runner runs `go test -count=1 -failfast ./internal/...`. For an endpoint mutant that survives, it also runs `MODEL_SEQS=200 go test -run TestModel ./internal/endpoint`.
Some mutants failed to build at first: a few of my edits had syntax errors, and an OOM hit while two shards ran in parallel. I fixed those mutants and re-ran them one at a time, so the final count has **0 compile failures**.

## Totals

189 mutants: **154 killed, 35 survived**. 6 of the survivors are equivalent or effectively equivalent.
No mutant was killed only by `MODEL_SEQS=200`: every endpoint mutant that TestModel kills, the default suite run already kills.

| category | mutants | survived |
|---|---|---|
| ownership / marker | 34 | 3 |
| reconcile refusal paths | 17 | 4 |
| guid re-check | 6 | 1 |
| lock acquisition | 15 | 5 |
| containment (parent / path / table coverage) | 18 | 3 |
| idempotency mismatch | 13 | 1 |
| NFS exposure + NFS input validation | 31 | 5 |
| peer / role auth | 13 | 2 |
| other input validation | 42 | 11 |

Two kills are slow. `apply-guid-cmp` (apply.go:117) and `apply-guid-first` (apply.go:85) are caught only because `TestDestroyNeverHitsAReplacedDataset` hangs until the 5-minute test timeout. Giving its subtests a deadline would turn that hang into a quick, named failure.

## Survivors ranked by risk

CONC = in code another builder is editing right now (the reconcile ownership gap, the max-deletions test, the scan paths).

### A. Security (authorization)

| # | file:line | mutation | minimal killing test |
|---|---|---|---|
| 1 | server/roles.go:117 | `pi.Role == RoleAdmin && !adminPeer(...)` → never refuses (the admin uid check is deleted) | Call `authorize(peer.NewContext(ctx, &peer.Peer{AuthInfo: PeerInfo{Role: RoleAdmin, HasCred: true, UID: 4242}}), Reconcile_FullMethodName)` and expect `PermissionDenied`. Today `adminPeer` is unit-tested only by itself, and the socket tests run as root, so the call site is never tested. |
| 2 | server/roles.go:134 | `stream()`: the refusal of non-health streams is deleted | Call `stream(nil, ss, &grpc.StreamServerInfo{FullMethod: "/x.Svc/M"}, h)` and expect `PermissionDenied` with `h` never called. There is no streaming RPC today, so this is latent. |

### B. Data loss (destroy paths)

| # | file:line | mutation | minimal killing test |
|---|---|---|---|
| 3 | endpoint/apply.go:96 | the `sameDataset` guid re-check before the destroy that is retried after settle is skipped | Fake zfs: the first `Destroy` returns `ErrBusy`. `Config.Settle` renames the target away and recreates it, which gives it a new guid. Expect `Aborted` and no second destroy. |
| 4 | endpoint/reconcile.go:382 | orphan clone-source action: the result of `d.refuses(a)` is ignored, so it is not dropped under busy locks or a changed plan | Call `PlanReconcile` with an eligible stamped `v@volume-source-for-volume-c` and `Desired{held: {v: true, c: false}, ...}`. Expect a skip with `SkipBusy` (or `SkipChangedSinceScan` when the guid differs from `planned`) and no action. The volume-group twin (`rec-vol-refuses2`) is killed. |
| 5 | endpoint/reconcile.go:482 | `tooYoung`: `Creation <= 0` → `< 0`, so a row with no creation time ages from 1970 and bypasses min-age | Call `PlanReconcile` on an owned, uncommitted row with `Creation: 0`. Expect skip `SkipNoCreation` and no action. |
| 6 | endpoint/reconcile.go:690 **CONC (known)** | max-deletions gate deleted / `> limit+1` / default effectively unlimited | Run `Reconcile` with N+1 undesired volumes, `MaxDeletions: N` and a valid digest. Expect `FailedPrecondition` and nothing destroyed. With exactly N, the run applies. Repeat with `DefaultMaxDeletions`+1 and `MaxDeletions: 0`. |
| 7 | endpoint/reconcile.go:269 **CONC (known)** | candidate filter `!r.OwnedVolume()` → `!r.Managed` | Use a managed row whose `csi_volume_name` ≠ its ID. Assert no action **and** no skip entry for it. Today `planDelete`'s own `OwnedVolume` check still refuses it: the row shows up as skipped "dataset … is not managed" but is never destroyed. So this is a defence-in-depth gap, not a live deletion bug. (Deleting the check entirely, `rec-owned`, *is* killed.) |
| 8 | endpoint/endpoint.go:1033 | `DeleteSnapshot` takes no lock | Internal test: take `e.locks.TryAcquire("snap:v@s")`, call `DeleteSnapshot` with a short `LockWait`, and expect `Aborted` with the snapshot intact. |
| 9 | endpoint/endpoint.go:548 | `CreateVolume` from a source volume does not lock `vol:<src>` | Hold `vol:src`, then call `CreateVolume(clone of src)`. Expect `Aborted`. |
| 10 | endpoint/table.go:492 **CONC** | `scan` keeps rows whose name is outside `parent/` | Reachable only with a pool-root parent; for any other parent the "deeper" filter drops the row anyway. Scan with `Parent: "tank"` and rows `tank` and `tank@x`. Expect neither in the table or in `ListVolumes`/`ListSnapshots`. |
| 11 | endpoint/endpoint.go:707 | `readVolume` (the result of CreateVolume and Expand) returns a row that is not served | Fake zfs drops the `provision_success` set. `CreateVolume` must then return an error, not a Volume. |

### C. NFS exposure

| # | file:line | mutation | minimal killing test |
|---|---|---|---|
| 12 | endpoint/endpoint.go:650 | `verifyExport`: the "served beyond its export" check is deleted, so CreateVolume commits anyway | NFS CreateVolume with a fake etab that has an ancestor `/<parentmnt> *(rw,crossmnt)` line. Expect `FailedPrecondition` and the volume left uncommitted. The publish-path twin is killed. |
| 13 | endpoint/plan.go:269 | `nfsCompatible`: the inherited/received sharenfs refusal is deleted | Repeat CreateVolume against an existing row with `ShareNFSLocal=false` (inherited) and `ShareNFS` equal to `ShareNFS(requested)`. Expect `AlreadyExists`. |
| 14 | endpoint/endpoint.go:1127 | `PublishFileExport` takes no lock | Hold `vol:<id>`, call Publish, and expect `Aborted`. |
| 15 | endpoint/endpoint.go:632 | `verifyExport`: owner-change check dropped to `!ok` | Race only. A fake scan between create and verify returns the row with another `csi_volume_name`. Expect `Aborted`. |

### D. Validation that another layer masks (low risk)

| file:line | mutation | why it survives / test |
|---|---|---|
| csi/controller.go:441, 496 | empty name / volume_id accepted | The endpoint refuses `""` with the same code. Test the CSI layer directly with a nil-EP Controller. |
| csi/controller.go:364, 368 | negative capacity; required > limit | The endpoint returns `OutOfRange` where CSI expects `InvalidArgument`, so **the code differs**. Test `capacityRange` directly. |
| csi/controller.go:207 | mixed block and mount capabilities accepted | Call `kindOf` with one block and one mount capability. Expect `InvalidArgument`. |
| csi/controller.go:347, 633 | unknown CreateSnapshot parameters are silently accepted | Call CreateSnapshot with `parameters{"foo": "x"}`. Expect `InvalidArgument`. |
| csi/controller.go:279 | `nfs.uid` / `nfs.mode` range unchecked | The endpoint's `validRoot` refuses it. Test `rootParam("nfs.mode", "10000")` directly. |
| endpoint/endpoint.go:429 | `parseSnapshotID` skips `volumeID(vol)` | The scan's depth filter (CONC code) drops nested rows. Expect `DeleteSnapshot("a/b@s")` and `CreateVolume(source "a/b@s")` to fail with the original code. |
| endpoint/export.go:653 | the exportRow zvol refusal is deleted | Publish's mount check still refuses, with another message. Test `PlanPublishExport` on a zvol row and match the message or code. |
| endpoint/plan.go:237 | oversize sharenfs accepted | Use a client list whose `ShareNFS` exceeds `maxPropLen`. |
| endpoint/endpoint.go:221, 298 | invalid parent; NFS server name > 253 bytes | `New` with `Parent: "tank//x"`; `ValidNFSServer(strings.Repeat("a", 254))`. |
| server/daemon.go:297 | request id > 64 bytes accepted (the charset check stays) | `validRequestID(strings.Repeat("a", 65))` should be false. |

### E. Equivalent or effectively equivalent (no test needed)

- `rec-vol-refuses` (reconcile.go:318): `why` is still assigned from the last action, so the edit changes nothing. The stronger `rec-vol-refuses2` is killed.
- `scandel-src` (endpoint.go:813, CONC): the `volumeID(src)` check on the next line rejects `/` and `@`.
- `nfs-empty-host` (plan.go:218): `ParseClient("")` refuses with the same `InvalidArgument`.
- `client-canon` (export.go:120): Go's netip rejects leading zeros and non-decimal bits, and `Is4` excludes IPv4-mapped addresses, so `p.String() != host` is unreachable with current Go.

## Also worth noting

- **Peer credentials fail open by design** (server/peercred.go:41 and roles.go:128). If `SO_PEERCRED` fails, `HasCred` is false, and `adminPeer` then allows the call. The `srv-peercred` mutant is killed, but only incidentally, by `TestPeerCredentialsAreLogged`. The only thing between any caller and the admin role is the socket's 0600 mode, and survivor #1 shows nothing tests the uid gate at the call site.
- Killed CONC guards, still covered at 96c49ff: `own-local-only` (the local-only ownership read in decodeColumns), `scan-deeper`, `scan-children`, `scan-only-parent`, `rec-owned` (OwnedVolume deleted outright).
