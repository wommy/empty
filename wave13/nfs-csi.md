# wave13 nfs-csi: ADR 0004's plan, static NFS export at create, CSI NFS mode

- **Branch:** local `nfs-csi`, created from `claude/gallant-johnson-1qaxp5` at 654b864. Not pushed.
- **Worktree:** `/home/user/empty/.claude/worktrees/agent-abc547657367f7600`.
- **Size:** seven commits, 26 files, +2424/-451.
- **Evidence:** `/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave13/nfs-csi-ev/` (written "ev/" below).

## Anomalies (read first)

1. **The floor is not in the ownership grammar. This deviates from the wording of ADR 0004 and design §2.**
   - The texts say a canonical CIDR is "at or above the configured floor".
   - As built, `ParseShareNFS` accepts any /N from 1 to 32. The floor bounds new writes only: Publish and CreateVolume.
   - With the floor in the grammar, raising `-nfs-min-prefix` would turn every existing export below it FOREIGN. Such an export can never be rewritten or unpublished. That breaks the Config promise "raising it never strands an export".
   - /0 is still refused, by `ParseClient`. Below-floor values belong in a reconcile report, which is not built.
   - The deviation is recorded in ADR 0004's new implementation notes and in design §2 ("As built").
   - It is reversible: one argument to `ParseShareNFS`.
2. **Unpublish now refuses while a foreign value still serves the host. This flips wave9's exemption.**
   - wave9 let Unpublish succeed on a foreign sharenfs.
   - Now `isLive = Lookup || covers(...)`, whatever the ownership, so a foreign value that still serves the host gives FAILED_PRECONDITION. This matches design §12.
   - `TestFileExportReceivedShareNFS` changed: unpublishing 10.9.0.0/16 under a received /8 is now FAILED_PRECONDITION, because the /8 still serves it. 192.168.9.0/24 stays OK.
3. **The scan file was touched**, despite the JSON-scan writer, in `internal/endpoint/table.go`:
   - the `NFSClients string` field is removed (`NFSClientsSet` stays);
   - one column (`n.nfsServer()`) is added to `columns()`;
   - one `cNFSServer` is added to `decodeColumns`;
   - two assignments come after the Row literal, so gofmt does not realign it.

   No parsing code changed. Expect a small merge conflict in `columns()` and `decodeColumns`.
4. **`cmd/zfs-endpoint/main.go` gained `-nfs-server`** (repeatable) and the refusal "-nfs-server needs root". These sit in startup, next to where the security writer's F5 (refuse to start on a crossmnt ancestor) will go. A textual conflict is likely there; there is no semantic one.
5. **Not built: root ownership (wave9 N11, design §13 Q1, the ADR's "root squash stays on"). This blocks real use.**
   - A new NFS volume is `root:root 0755` under root_squash, so **no pod can write to it**.
   - CSI CreateVolume in NFS mode works end to end; the volume just is not writable.
   - This should be the next item.
6. **Not built: §4's local mountpoint pin.**
   - `share` is read fresh from the mountpoint. If the parent's mountpoint moves, ListVolumes reports a new `share` while PVs hold the old one. That is an S1 deviation, though only on that edge.
   - wave9 N5's pin assertion was dropped from the adapted test, and the test comment says so.
7. **New VM tests have no weights** (they get the 0.1s default) and have never run on real ZFS or nfsd here:
   - `TestFileExportCreate`, `TestFileExportCreateServerSurvivesRestart` and `TestFileExportCreateServerElsewhere` (commit D);
   - `TestFileExportLeftoverClientList` and `TestFileExportAdminWorldEditIsForeign` (commit B).

   They are serial, like their siblings, because exportfs is global on a real host. Refresh `ci/real-zfs-weights.txt` after the first VM run. The two simulator-only ones are in `ci/expected-skips.txt` (commit e329352). Without that entry `check-skips.sh` would have failed the VM leg.
8. **A restore window in commit C, for an inherited prior sharenfs.**
   - The pre-check refuses before any change in every case the planner can see. The backstop runs only when an exposure shows up after the apply: a stale etab, or a `realPath`-only crossmnt (verifier item 9).
   - The backstop restores the prior value: it re-sets a local value, or runs `zfs inherit` for an inherited one. Between the apply and the restore, the requested line was served for that one call. Nothing wider than the request is ever served by us.
   - The mutant `ev/C-mutant-norestore.txt` shows the backstop is needed: without it the inherited case leaves `rw=10.0.0.3/32` set.
9. **I amended an accepted ADR rather than writing a new one.**
   - ADR 0004 gains an "Implementation notes" section; its Decision text is unchanged.
   - nfs-decisions.md says "the coordinator records [Q1] in an ADR". If you want Q1 as its own ADR, the notes' fourth bullet ("the NFS server address is addressing metadata, not export state") is the content to move.
10. **A bug I made while writing commit D, found by its own test before the commit.**
    - `ValidNFSServer` took `192.0.2.010` (octal to inet_aton, which mount.nfs uses) and `0xc0000210` (hex) as DNS names.
    - Fixed: the last label must start with a letter (RFC 1123 2.1).
11. **CSI cost:** the controller asks the endpoint's GetCapabilities on every CreateVolume, on every ValidateVolumeCapabilities, and on ControllerExpandVolume when a capability is given. It is one extra unix-socket RPC. It is deliberate: a restarted endpoint may advertise other servers, and no controller flag can then disagree with the endpoint.
12. **Proto smaller than design §4:**
    - one message, `NFSExport{clients, read_only, server}`;
    - three fields: `CreateVolumeRequest.nfs_export=10`, `Volume.nfs=10`, `GetCapabilitiesResponse.nfs_servers=4`.

    No `nfs_state` enum, no raw `sharenfs` and no `nfs_min_prefix`. Drift (D7-D9) goes through the existing `ReconcileResponse.skipped`. `buf lint` is clean, and `buf breaking` against 654b864 exits 0. The reason is in Decisions (Q1).
13. **Deploy not touched.** `deploy/k8s` has no NFS-mode CSIDriver (`attachRequired: false`, `fsGroupPolicy: None`) and no csi-driver-nfs node DaemonSet.

## Commits

| commit | what | proof |
|---|---|---|
| b5e2234 | **S7, the rollback bridge.** A local canonical sharenfs is ours without `nfs_clients`, and `nfs_clients` is still written. Adds `ParseShareNFS` and the S7 rows. | `ev/A-S7-before.txt`: 7 FAIL lines (5 S7 and in-grammar rows); `ev/A-S7-after.txt`: PASS. `ev/A-fuzz-roundtrip.txt`: 15s, 67,667 execs, no failure on `ParseShareNFS(ShareNFS(cs)) == cs`. |
| 54e4842 | **Item 1 and the ADR 0004 tension.** Export state is sharenfs alone; `nfs_clients` is never read for state or written, and Reconcile reports a leftover one. An admin's `rw=*` is FOREIGN and never rewritten. | `ev/B-before-vsA.txt` and `ev/B-before-vs0.txt`: the reconcile row and `TestFileExportLeftoverClientList` fail. At the base, sharenfs became `rw=10.0.0.2/32:10.0.0.9/32` and revoked 10.0.0.1; at A the list was rewritten. `TestFileExportAdminWorldEditIsForeign` also fails. `ev/B-after.txt`: PASS. |
| 0b370db | **A refused Publish changes nothing.** Planner pre-check (`ErrServedBeyond`), one unchanged re-share when the etab is stale, and a post-apply restore as the backstop. | `ev/C-before.txt`: the refused publish left `rw=10.0.0.1/32`, and the planner rows fail. `ev/C-mutant-norestore.txt`. `ev/C-after.txt`: PASS. |
| d0f3d01 | **Q1 and N9/N14/N16 on the endpoint.** `CreateVolume` takes an NFS export, records the server, and commits only once the export is served. Also `-nfs-server`, the proto, and reconcile D8/N16. | `ev/D-before.txt`: the new tests do not compile against commit C (feature absent). `ev/D-mutants.txt`: 5 of 5 mutants killed (M1 commit without verify, M2 server ignored, M3 server recomputed, M4 no unadvertised report, M5 inherited server read). `ev/D-after.txt`: whole suite PASS. |
| 46ef4fe | **Q2, S11, N9/N10/N14/N15 in CSI.** NFS mode iff the endpoint advertises servers. `nfs.clients` (required) and `nfs.server`; ro iff every mode is reader-only; five modes; block refused; `server`/`share` in volume_context; Validate per key, against the recorded server, the ro export and the unadvertised server. | `ev/E-before.txt`: against commit D's controller, 16 of 17 `TestNFS_*` FAIL (N16 passes trivially; its endpoint half is mutant M5). `ev/E-mutants.txt`: 5 of 5 killed. |
| 2720860 | **Docs.** Design §9.1 (stale row replaced, and an `rw=*` row added), §5/§6/§7 (Q1, "not volume state" retracted), §10 (D7-D9 via skipped), "As built" notes, ADR 0004 implementation notes, README rows. | n/a |
| e329352 | `ci/expected-skips.txt`: the two simulator-only create tests. | See anomaly 7. |

## Decisions, with evidence

- **Item 1: implement, not amend.**
  - No data is lost. Every list `nfs_clients` could hold equals `ParseShareNFS(ShareNFS(list))` (FuzzNFSRoundTrip; the classifier fuzz cross-checks it against the wave9 `canonicalShareNFS`).
  - The property itself is never cleared: it is reported and stays on disk.
  - It is reversible, but only as far as b5e2234. Older builds compute sharenfs from a stale list and revoke clients (ev/B-before-vs0.txt shows exactly that revocation). This is recorded in ADR 0004's notes and design §1.
  - Not contested, not irreversible, so I did not stop to ask.
- **The ADR 0004 tension (verifier item 5).** The wanted list now comes from sharenfs. An admin's `rw=*`, or `rw=<ours>:*`, is FOREIGN: Publish and Unpublish are FAILED_PRECONDITION, and it is never rewritten. The planner case that pinned the rewrite is flipped.
- **Q1(a), as decided.**
  - `<prefix>:nfs_server` is set in the same `zfs create`/`clone -o` as sharenfs, and read local-only.
  - Uncommitted volume: converges to the request.
  - Committed volume: an explicit different server is ALREADY_EXISTS naming the recorded one; an omitted server returns the recorded one, even after a reorder.
  - Reconcile reports `SkipNFSServerUnadvertised` (D8) and `SkipNFSServerNotLocal` (N16).
  - The NFS commit is split off: plan without the marker, apply it, `verifyExport`, then commit.
- **Q2, as decided.**
  - NFS mode is on iff `GetCapabilitiesResponse.nfs_servers` is non-empty.
  - The refusal text is "nfs.clients is required: this driver serves volumes over NFS". It is checked before any other key, so `{nfs.uid: ...}` gets it too, not "unsupported parameter".
  - Outside NFS mode, `nfs.*` keys are refused with a message naming `-nfs-server`.
- **S11.** `server` and `share` are both present iff a server is recorded, so a pre-NFS volume's context is unchanged (S1).
- **Minimal proto (for "say why").** CSI needs exactly three things across the wire:
  - the advertised servers;
  - the export at create;
  - the recorded server and export, for volume_context and Validate.

  One message reused three times carries all of them, and drift fits the existing `skipped`.
- **N9's last assertion inverted, N10 against the recorded value, N14 across restarts, N15 widened, and N16 added.** All of this follows the decisions file. N11 is left out (anomaly 5).

## Gate

All run on the final tree, e329352:
- `gofmt -l .` (ignoring `.claude/`): empty;
- `go vet ./...`: OK;
- `go vet -tags nightly ./...`: OK;
- `go mod tidy -diff`: OK;
- `buf lint`: OK;
- `go test -race -count=1 ./...`: all ok, 40.7s wall on this box (`ev/final-race.txt`; baseline per package in `ev/baseline-race.txt`).

Per-package race times move by ±5s between identical runs here; for example `internal/endpoint` measured 17.9s, then 8.5s, against the 11.6s baseline. So the whole-run delta is noise. The new tests measured directly:
- endpoint: 13 tests, under 0.1s in total (planner tables plus simulator contract tests);
- CSI: 17 `TestNFS_*` tests, about 1.3s package wall under race, run in parallel;
- server and cmd: under 0.1s.

**Added cost: about 1.5s of CPU and under 0.5s of wall in the unit job.** The VM leg gets five serial NFS contract tests, each at about the sibling tests' 0.4s.

## Open questions, with recommendations

1. **Root ownership (N11, §13 Q1).** Recommend (a), as the design does:
   - SC `nfs.uid`/`nfs.gid`/`nfs.mode`, carried as `NFSExport` fields;
   - the endpoint chowns the mountpoint before the commit marker;
   - a CreateVolume retry compares them.

   Without it NFS mode provisions unwritable volumes. **Do it next.**
2. **Mountpoint pin (§4).** Recommend pinning `mountpoint` locally at create, alongside sharenfs. It is a native property and touches the same create op as F12, so do it after the security writer lands.
3. **The floor deviation (anomaly 1).** Recommend keeping it, and adding the `below-floor` reconcile report when D1-D6 are built. If the owner reads the ADR literally, the alternative is: stranded exports on a floor raise, plus a documented "never raise the floor" rule.
4. **Q1 as its own ADR (anomaly 9).** Recommend a short ADR 0007, "per-volume recorded NFS server", if the coordinator wants decisions out of implementation notes.
5. **VM weights (anomaly 7).** Run the VM leg once, then regenerate with `ci/real-zfs-weights.sh`.
