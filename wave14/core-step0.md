# core step 0: one error table and an ownership differential

The work is on branch `core-step0`, created from `claude/gallant-johnson-1qaxp5` at 7f5637f. It was built in the worktree `/home/user/empty/.claude/worktrees/agent-a4f7b5771b25be940`. Nothing was pushed.

| commit | what |
|---|---|
| 22060d6 | endpoint: one zfs error table, read by CodeOf |
| 6c7559a | endpoint: ownership differential against the spec's OWN rows |

**Gate** (all green on 6c7559a):
- `gofmt -l .` is empty;
- `go vet ./...` and `go vet -tags nightly ./...` pass;
- `go mod tidy -diff` is clean;
- `go test -race -count=1 ./...` passes; the endpoint package takes 23 s on a shared host.

## Anomalies first

1. **The mapping was not where the brief put it.**
   - At 7f5637f, `internal/endpoint/errors.go` held only Code, Error and CodeOf.
   - The zfs error → Code mapping lived in four places:
     - `zfsErr` (endpoint.go);
     - `opErr` (apply.go): busy on a destroy, and unknown after a mutation;
     - applyN's own `OpClone && NotFound` case;
     - two more hard-coded mappings: `resumeErr`, which turns Internal into Aborted on a resumed provision, and `parentUnavailable`, which turns a not-found parent into Unavailable.
   - "opcodes" is only `opcodes_test.go`.
   - `classify` in internal/zfs maps stderr to a Kind. It produces the table's *key*, not a code, so I left it alone.
   - Both hidden mappings are now rows of the table:
     - the parent read is its own op class, `opReadParent`;
     - a resume is the `mutated` cell.
2. **CONTEXT.md does not exist at 7f5637f.** AGENTS.md points at it. I read docs/adr and docs/agents instead.
3. **Tolerances stay outside the table.** `OkExists` and `OkMissing` decide whether a failure exists at all. They are per-Op-instance planner data, not per op kind; for example, only some snapshots carry OkExists. The table maps failures only. core.md §7's "destroy NotFound → done" row is HEAD's per-instance `OkMissing`. Every destroy carries it today.
4. **How CodeOf reads the table.**
   - `zfsFail` is the only constructor of a zfs-derived `*Error`. It records the cell, and `CodeOf` reads the code from `errTable` through that cell.
   - The `Code` field is still filled with the same value, for code that reads it directly. The test checks field == CodeOf for every cell.
   - A bare `*zfs.Error` that reaches CodeOf still reads as Internal, as before. Sending it through the table would be a behaviour change.
5. **One theoretical difference.**
   - `kindOf` takes the first `*zfs.Error` in the chain (`errors.As`). HEAD's switch used `errors.Is` over the whole chain, plus `errors.As` for Unavailable.
   - They differ only for a chain holding two `*zfs.Error` of different kinds. Nothing produces one: `Client.run` wraps only runner errors.
6. **Files touched outside the error mapping and the tests.**
   - endpoint.go: three small hunks. zfsErr moved out, and the bodies of `resumeErr` and `parentUnavailable` now call the table.
   - `ci/expected-skips.txt`: the 3 new tests use only the simulator and must be allowed to skip on the VM.
   - `ci/real-zfs-weights.txt`: the 6 new tests weigh 0.00, so they do not unbalance the shards.
   - `zfs.NumKinds` is exported to size the kind axis.
   - plan.go is untouched.
7. **Spec overlap, OWN-04 against OWN-13.**
   - A snapshot with the reserved name `volume-source-for-volume-<id>`, a clone stamp **and** a CSI name satisfies both rows.
   - HEAD makes it a CSI snapshot, not a clone source. The test classifier adopts that and says so in a comment.
   - The endpoint can never write this shape, because CreateSnapshot refuses the reserved prefix. It is a gap in the spec text, not a bug.
8. **Spec gap, OWN-04 does not require an owned volume.**
   - Example: a CSI-marked snapshot with an **empty** `csi_snapshot_source_volume_id`, sitting on a foreign volume (an alias, or managed without a name).
   - HEAD and the spec both call it a CSI snapshot. ListSnapshots lists it, and DeleteSnapshot destroys it with `-d`.
   - The core design downgrades every snapshot of a non-owned volume to Foreign.
   - The endpoint always writes the source marker, so only legacy or hand-set snapshots can have this shape.
   - Not fixed. It is not a clear bug, since the spec allows it. The fixtures pin the current answer (`alias@legacy`, `half@legacy`).
   - Suggested amendment to OWN-04: "…and its volume is owned".
9. **Simulator fidelity.** `Sim.SendRecv` drops the snapshot's user properties. Real `zfs send -p`/`-R` carries them, with source received. A received copy's snapshots therefore never carry markers in the simulator, and OWN-06's "do not shadow" half is not exercised by that path. The fixture uses `Sim.Receive` on the dataset instead.

## 1. One error table

**Where it lives:** `internal/endpoint/errors.go`.

- `errRules` is the table as written: 12 first-match rows of (ops, kinds, after-mutation?) → Code.
  - **ops** are the apply OpKinds, plus `opRead` and `opReadParent`.
  - **kinds** are `zfs.Kind`, plus `kindDeadline`, which is Interrupted by the caller's deadline. An error that is not a zfs error at all counts as KindUnknown.
- `errTable [9 ops][14 kinds][2]Code` is the rules expanded once. If any cell has no rule, init panics.
- `zfsFail(op, err, mutated, msg)` is the only constructor.
- `opErr`, `zfsErr`, `resumeErr` and `parentUnavailable` all go through it: `remapMutated` re-keys a cell as after a mutation.

**Tests** (`errors_test.go`):

| test | what it checks |
|---|---|
| `TestZfsErrCodes` | now rows of (op, err, mutated, want) |
| `TestErrTableMatchesHead` | Compares against frozen, verbatim copies of 7f5637f's `zfsErr`, applyN's error switch and `opErr`, `resumeErr`, `parentUnavailable`, and the scan/GetCapacity not-found branches. For every one of the 252 cells it checks the table cell, and also the production path's code **and message**: zfsErr, opErr at i=0 and i=1, resumeErr, parentUnavailable. The unknown kind is covered both as a zfs error and as a plain Go error. |
| `TestErrTableMatchesHeadSeesEveryCell` | Changes each of the 252 cells in a copy of the table and asserts that the diff reports exactly that cell. |
| `TestErrTableIsTotal` | No cell is OK. Every OpKind is in the table. The read ops collide with no OpKind. An uncovered rule set panics. |

**Proof by mutating the real rules** (reverted afterwards):
- A rule `{OpSnapshot, NoSpace, after} → Unavailable` failed `TestErrTableMatchesHead` on the cell `{op:3 kind:7 mutated:true}`, and on the opErr and resumeErr paths: got 14, head 8.
- Widening the `opReadParent` rule to ParentNotFound failed on `{op:8 kind:3}`: table 14, head 5.

## 2. Ownership differential (`owndiff_test.go`, test-only)

**The classifier.** `specOwn` is written from OWN-01/02/03/04/12/13/14/17 and VOL-09. It reads the raw `zfs get -j -p … -r` document with `encoding/json`, so no endpoint decoder, Row or Table code is involved. For every row it must agree with the Table's OwnedVolume, ServedVolume, OwnedSnapshot and OwnedCloneSource (including which clone). The two must also agree on which rows are in scope: the parent and anything below a volume are out (OWN-12), and no Table row may be missing from the raw scan. `ScanWhole`, a `_test.go` seam, exposes the whole-parent scan.

**Corpora:**

| test | what it covers | rows |
|---|---|---|
| `TestOwnershipDifferentialFixtures` | foreign; managed without a name; uncommitted; alias; markers set to `-` and to `TRUE`; democratic-csi markers; received markers; a send/recv copy; a child dataset; clone-source variants (name only, half stamp, stamped for another, stamp plus CSI name, empty suffix, on a foreign volume); CSI snapshots with a foreign, empty or own source; legacy snapshots on foreign volumes. Scanned under the default prefix, again under the adoption prefix (OWN-17), and again after inheriting markers from the parent (OWN-01). | 82 |
| `TestOwnershipDifferentialCrashes` | the state every crash point of every `crashCases` entry leaves, before and after the call, before the retry | 151 |
| `TestOwnershipDifferentialModel` | 8 seeds × 30 steps of `genTrace` (CSI calls, admin snapshot, clone, recv, rename and hold, faults), compared after every step | ≈790 |

**Result: no disagreement across about 1,020 rows, and no bug to fix.**

- Mutating the classifier proves the test bites. Making it ignore the source type flags received and inherited rows. Dropping its CSI-name exclusion flags `a@volume-source-for-volume-w`, which is anomaly 7.

**Cost:**

| item | race build |
|---|---|
| error tests | ≈0.05 s |
| differential | ≈0.3 s wall at top level and ≈5 s CPU spread over parallel subtests; the endpoint package's wall time moved by less than the host's ±1 s noise (12.3–14.3 s without, 13.8–14.3 s with) |
| VM leg | 0: all three are simulator-only and listed as expected skips |

All use t.Parallel and none sleep.

## 3. Decision input: do core.md §14 steps 1–8 still pay?

**What HEAD has now that core.md's premise lacked**, against 8cc52ef:
- **The JSON scan.** A strict `jsontext` decoder that refuses anything zfs never prints. BenchmarkScan/10k is **18.5 MB and 105 allocs/op** (947afc8, f967df1), against core's 8.9 MB and 30k allocs. Core's parse advantage is gone. Only retained memory is still in core's favour: about 31 MiB against 2.2 MiB at 10k, bounded by `maxWholeScans=2`.
- **The crash table.** 17 cases, every mutation point, before and after.
- **Golden plans.** 175 lines.
- **The model suite.** TestModel, FuzzModel, and one knownOpen with an expiry date.
- **A row classifier on every PR** (fsm_classify), plus a nightly exhaustive FSM explorer. It runs to depth 4, with crashes at every boundary, partial `set` subsets, out-of-band races, and both prefixes.
- **Test volume.** 56 test files, 14.3k LOC and 194 Test/Fuzz functions, against 40, 6.9k and 161.
- **N1 is fixed** (plan.go:617 refuses a resume where the sparse setting differs).
- **The differential above finds no ownership bug** for World's typed ownership to remove.

| step | what it would replace | bug or test classes it removes | rough size | pays? |
|---|---|---|---|---|
| 0 | the scattered error mapping, plus a World parse beside `-Hp` | the second-mapping class (D3) | done here (≈+130 net production LOC); the World parse is moot: the JSON scan landed | done |
| 1 DeleteVolume | Full-parent scan per delete (endpoint.go:703, still whole at HEAD). The delete planner tests. | **O(parent) per delete**: 178 ms and 18.5 MB at 10k, N scans for a namespace teardown; core measured 350–5,000×. **OWN-15** (rename-and-recreate), since HEAD has no guid guard (open-finding). | core path: world + decide + apply guard + dual run behind `-core=` + plan-equality harness, ≈1.5k LOC; the same two wins on HEAD's types, ≈150–250 LOC plus 3–4 tests | the **wins** pay; the **type migration** does not |
| 2 CreateVolume | plan_test, clonesize and converge, replaced by TestCreateCells and TestCrashConvergence | little that is new: the crash table, golden plans, explorer and model already cover cells and crash convergence; N1 is fixed. D07 and "cut Exists → Aborted" are single rows. | ≈1k LOC + `req.Now`, plus the allowlist moved | no |
| 3 snapshots, 4 expand, 5 list | the keep-class tests: core.md §11 kept 110 of 161, and these are among them | none measured | each a fresh port; not built in core | no |
| 6 reconcile | reconcile, FuzzReconcileSafe | none; the model suite and explorer exercise it | large (694 LOC at HEAD), not built | no |
| 7 export | the NFS surface: 879 LOC, many contract tests | none; it is the highest regression risk | largest, not built | no |
| 8 delete table.go, plan.go, `-Hp` | — | `-Hp` already serves only single-dataset Get/List | only after 1–7 | no |

**Recommendation: narrow.**
- Stop the World/handles strangler.
- Keep step 0 as landed here.
- Take step 1's two concrete wins as ordinary HEAD changes, each with a regression test:
  - **DeleteVolume subtree scope.** Add the `clones` column, scan the volume plus its origin's volume, and keep the whole-parent rescan only as a fallback.
  - **A guid guard before every destroy** (OWN-15). This is `TestRenameAndRecreateIsNotDestroyed` from core.
- Separately, amend OWN-04 (anomaly 8) and decide whether HEAD should downgrade snapshots of non-owned volumes. That is a one-line change in `OwnedSnapshot` plus a row in the differential, and is behavioural, so it needs its own review.
