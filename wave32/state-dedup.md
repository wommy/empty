# State modelling and undo-code dedupe: assessment (wave 32)

Read-only. No repo file changed. Line counts are non-comment, non-blank lines
(`grep -cvE '^\s*(//|$)'` over the cited range) unless marked "range".
Paths are under /home/user/empty.

## Short answer

Little to gain by adding a compensation log or a new state machine. The daemon
already has a persistent state model: the marker properties, classified by
`Row` predicates, and the planner's pure functions over a fresh `Table`. Every
undo site re-derives "what is there" from ZFS and guards on it (ADR 0003,
ADR 0010). An in-memory undo log would be net additive, and it would be
wrong on the success path. The honest dedupe is one small helper, worth a
few lines.

## 1. Every place the daemon undoes, compensates, releases or resumes

| Site | Location | How it knows "what was done" |
|---|---|---|
| CreateVolume pass loop + resume | endpoint.go:566-694 (129 range, 99 code) | Fresh scan per pass (`scanForUnproven`, l.602-622). Flags: `changed` (row exists and uncommitted, or a plan op ran), `fresh` (pass 0 found no row), `made` (`n>0` or `mayHaveRun(err)`, l.656). Commit = last op `provision_success`/`committed_at`. |
| `fail` closure | endpoint.go:591-613 (13 code) | Consumes `changed`, `fresh`, `made`. Calls `resumeErr` if changed; `rollbackCreate` if fresh && made && !callerEnded. |
| `callerEnded`, `mayHaveRun` | endpoint.go:689-716 (about 14 code) | Classifies the failure: deadline/cancel (no rollback) and "may have run" (unclassified or killed zfs call). |
| `rollbackCreate` | endpoint.go:718-767 (31 code) | Re-scan after failure (`scanFor`, l.735), then `PlanRollbackCreate`, then `apply`. Result via `beforeChange` (l.762), or `errors.Join` on failure. |
| `PlanRollbackCreate` | plan.go:766-789 (24 code) | Pure. Resume's guards (OwnedVolume, not committed, same kind/source) then `planDelete`. Or, with no row and a source, `releaseCloneSource`. Destroys are guid-tied (`sameDataset` at apply.go:112). |
| Resume of an interrupted create | endpoint.go:566-694 (same as row 1) | Scan shows `exists && !Committed`. `PlanCreateVolume` plans the rest. Commit is still the last op. No explicit record. |
| `resumeErr` / `afterChange` | endpoint.go:839-848 (7 code) | Re-keys after-change codes. Pure function of the error. |
| `releaseCloneSourcesFor` / `releaseCloneSource` | plan.go:715-749 (27 code), callers plan.go:706, reconcile.go (orphan rule) | Re-scan. Stamp `clone_source_for=<id>` (`OwnedCloneSource`), `dependent` check, `Deferred` flag. Used by DeleteVolume, rollback and Reconcile. |
| `rollbackExpand` / `PlanExpandRollback` | endpoint.go:1098-1113 (12 code); plan.go:1011-1025 (10 code) | Caller passes `before Row` (from PlanExpandVolume, l.1067). Post-failure re-scan (`scan`, about l.1108), then pure plan. The check that this grow raised refquota is `row.Refres` vs `before.Refquota`. |
| `restoreShareNFS` (Publish refusal) | endpoint.go:1423-1430 (8 code); call at l.1405 | Uses the `before Row` the caller scanned, with NO re-scan. Issues a direct `Set`/`Inherit`. Gated on `len(plan) > 0` (l.1404). Still under the volume lock. |
| Unpublish | endpoint.go:1449-1520 (no restore) | No compensation. Verifies after applying (l.1487 `Lookup`, l.1491 `covers`) and returns Unavailable/FailedPrecondition. Left in place on error. |
| `CreateSnapshot` after-change | endpoint.go:1218 | `afterChange` re-key (no undo). Nothing to roll back: a cut snapshot is kept, final codes avoided. |
| Error re-keying | errors.go:263-302 (`remapMutated` + `beforeChange` + `noSpaceAfterChange`, 25 code); apply.go:138-177 (`rootErr`, `opErr`, 32 code) | `errCell{op, kind, mutated}` is already the table key. `Error.pre` carries the pre-change error, so `beforeChange` is the undo record in the error value. |
| `applyN` / `destroy` / `sameDataset` | apply.go:19-123 (about 67 code) | Each op is a replayable tolerance (`OkExists`, `OkMissing`). Destroys re-check GUID. No in-memory undo. |
| Reconcile uncommitted rows | reconcile.go:276-292 (17 code); orphan clone sources reconcile.go:370-395 (23 code) | Pure: `ReasonUncommitted` for owned rows not desired and not committed; `ReasonOrphanCloneSource` from the Table. Locks are TryAcquire (l.707-721). |
| Lock release | lock/lock.go:58-203 (127 code; Acquire at l.72) | `Acquire` returns a `sync.Once`-guarded release. Callers `defer release()` (7 sites, all in endpoint.go, counted by grep; none in internal/lock). Reconcile keeps a `releases` map with one deferred loop (reconcile.go:713-723). Already a cleanup stack. |

Summary of the "knowing" patterns:
- Re-scan plus guards: rollbackCreate, releaseCloneSource, Reconcile rules. These are pure over a fresh Table.
- Pre-apply `before Row` plus re-scan: rollbackExpand.
- Pre-apply `before Row`, no re-scan: restoreShareNFS only.
- In-memory flags: three booleans in CreateVolume (`changed`, `fresh`, `made`), plus `Error.pre`.

## 2. Existing state model

- Persistent state is the marker properties (CONTEXT.md "Marker properties"):
  `managed_resource`, `csi_volume_name`, `clone_source_for`, `provision_success`, `committed_at`, plus source markers.
- Production classifier: `Row.OwnedVolume` (table.go:117), `Row.ServedVolume` (table.go:126), `Row.Committed`, `Row.OwnedCloneSource` (table.go:144).
- Transition function: `PlanCreateVolume` (plan.go) with the Resume branch. The commit is the last op.
- A test-only state classifier already exists: `classify()` at internal/endpoint/fsm_classify_test.go:23-150 (123 code lines). It returns labels such as `Foreign(recv)`, `Adopted-dcsi`, `CSI-snap-deferred` and `IMPOSSIBLE(...)`. `TestClassify` is at l.162. `fsm_explore_test.go` (1007 lines) is the explorer that drives it. This is an independent oracle, written from the spec (ADR 0003), not from production code. Keeping it independent is the point.
- `modelrun_test.go` (1389) and `model_test.go` (647) hold the reference model. Neither mentions rollback (grep count 0).

So candidate (b), a production state type, would mostly duplicate `Row` predicates and `classify`.

## 3. Candidates, quantified

Counts are code lines. "Replaced" is what each candidate would touch.

### (a) Compensation / undo log (saga, register inverse per op)
- Would replace: `rollbackCreate` (31), `rollbackExpand` (12), `restoreShareNFS` (8), part of `PlanRollbackCreate` (24), `releaseCloneSource` (part of 27). Total touched about 100.
- Would add: an `undo []func(ctx) error` or an `[]Op` log in `applyN`, an inverse per op kind (about 6 kinds), registration in `apply`, unwind with error join, and reconciling with `OkExists`/`OkMissing` semantics. Estimate +50 to +70 code.
- Net: roughly +0 to +40 lines in non-test code, and about +60 to +100 new test lines (a log type and its unit tests, with the existing rollback tests still needed). It is net additive in both.
- Why it is redundant, not a blocker: a verification-driving log only records "I created X" (and maybe "a failed op may have run", the `made` flag). It needs no GUID, because rollback re-scans and guid-checks as `rollbackCreate` does today (apply.go:112 `sameDataset`). What it would add is the list of created names, which `PlanRollbackCreate` already derives from the post-failure scan (its guards: OwnedVolume, not committed, same kind and source). Crash recovery must come from ZFS anyway, so the log can never be the source of truth. A GUID-storing log would add a `zfs get guid` per create. That would change the success path, so `TestRPCCallBudget` (callbudget_test.go:121) would fail, but this variant is not needed.
- Crash consistency: the log is lost on process death. The persistent state would still have to come from a re-scan (PlanRollbackCreate), so the log duplicates the planner, not replaces it. ADR 0010 R2 requires the re-scan anyway.
- Pinned by: rollback_test.go (568 lines, 12 tests), rollback_plan_test.go (91), crash_test.go:400-471 (rollback-crashes sweep), crash_test.go:597 (rollback-create guid swap), callbudget_test.go (0 failure rows; it pins success and retry only, per ADR 0010 consequences).
- Verdict: reject. Net add, and it breaks the success-path budget.

### (b) Explicit per-volume state machine (absent → uncommitted → committed; one function gives next op and inverse)
- Production already has the states as `Row` predicates plus `PlanCreateVolume`. An enum would replace the `OwnedVolume`/`Committed`/`ServedVolume` checks: 30 non-test mentions (plan.go 11, reconcile.go 10, endpoint.go 8, export.go 1), each a line or two and would need a `switch` in `PlanCreateVolume` and `PlanRollbackCreate` (about 20-30 code lines).
- Promoting `classify()` (123 code) to production would be a net add of about 120, and it would make the test oracle non-independent. The independence is the value.
- Pinned by: fsm_classify_test.go, fsm_explore_test.go, modelrun_test.go, model_test.go, plan_test.go, rollback_plan_test.go.
- Verdict: net add, low gain, and it weakens the oracle. Reject, or keep as a documentation-only table in CONTEXT.md.

### (c) One "what did this call change" tracker, replacing `changed`/`fresh`/`made`
- Replaces: the three booleans and their 15 references inside CreateVolume (lines 566-694), plus the `fail` closure (13 code).
- Would add a struct with methods (`changed`, `fresh`, `made`, `mark`, `failed`) of about 15-20 code lines.
- Net: about 0 (maybe 2 lines saved from `fail`). Clarity gain only. Medium confidence, low risk (no crash-relevant change). Worth doing only with a rename pass, not for the count.
- Pinned by: rollback_test.go (ambiguous first op, deadline, resume), errors_test.go, cov24_internal_test.go.

### (d1) One compensation helper for the three bounded undo calls
- Each of the three undo sites repeats `context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)` plus `defer cancel()` (3 occurrences, endpoint.go:732-733, 1106-1107, 1424-1425). The scan/plan/apply shape is shared only by `rollbackExpand` and `rollbackCreate`.
- A helper `func (e *Endpoint) undoCtx(ctx) (context.Context, func())` saves 2 lines per site (6), and its definition costs about 4. Net saving about 2.
- A fuller `compensate(ctx, ids, plan)` helper saves about 6 lines across the two scan-plan-apply sites, minus about 8 for the closure definition. Net about 0.
- Risk: low. Crash sweeps cover the mutations, and the helper keeps the same mutation order. Must keep `rollbackCreate`'s `undone` and `errors.Join` semantics intact.
- Pinned by: rollback_test.go:310 (deadline), rollback_test.go:378 (joined failure), crash_test.go rollback-create, cov24_test.go and export_contract_test.go for restoreShareNFS.
- Verdict: take it only as part of a larger pass. Alone, it is not worth a change.

### (d2) Duplicate destroy-op literal
- reconcile.go:387 `Op{Kind: OpDestroy, Target: r.Name, Defer: true, OkMissing: true, GUID: r.GUID}` and plan.go:748 `Op{Kind: OpDestroy, Target: s.Name, Defer: true, OkMissing: true, GUID: s.GUID}`. A shared constructor `releaseOp(name, guid string) Op` saves 0 code lines (1 each, plus 4 for the constructor).
- Verdict: no.

### (e) Error re-keying: merge `resumeErr`/`afterChange`/`remapMutated`/`beforeChange`
- `resumeErr` + `afterChange` are 7 code lines. `remapMutated` + `beforeChange` + `noSpaceAfterChange` are 25 code lines. `rootErr` + `opErr` are 32 code lines.
- These already key on `errCell{op, kind, mutated}` and `Error.pre`. Merging the wrappers saves maybe 3-5 lines. Risk: the error table (errors.go errRules) is pinned by errors_test.go (419 lines) and the after-change rules in ADR 0010's list.
- Verdict: no gain worth the risk.

### (f) Locks
- `Acquire` already returns a once-guarded release (lock.go:72-125), used with `defer release()` at 7 sites in endpoint.go (grep count). Reconcile's `releases` map plus deferred loop (reconcile.go:713-723) is a cleanup stack already.
- Verdict: no gain.

## 4. Risks

- Crash consistency: an in-memory log dies with the process. Persistent state must stay re-derived from ZFS (crash_test.go:429 `TestCrashAtEveryMutationConverges`, crash_test.go:400-471 rollback-crashes). Any dedupe must keep every mutation in the same order, so the sweep still crashes at the same points.
- Locks: `rollbackCreate` runs under the call's locks. A helper must not take locks. A rollback on `context.WithoutCancel` must keep its own timeout (rollbackTimeout, endpoint.go:1119).
- TestRPCCallBudget (callbudget_test.go:121): an undo log that records GUIDs changes the success path. Any candidate that adds a zfs call on success fails the budget. Only pure refactors pass.
- TestModel (modelrun_test.go:1268): it does inject faults into random steps, including creates: killBefore, killAfter, errBefore and race (modelrun_test.go:54-61, 281). But its residue rule, adoptResidue (modelrun_test.go:819-830), accepts whatever a failed request left and adopts it. So it checks convergence and states, not that a rollback removed the residue, and it never names rollback (0 matches). The rollback is pinned by rollback_test.go, rollback_plan_test.go and crash_test.go. A change that stops rolling back could pass TestModel.
- restoreShareNFS (endpoint.go:1423): restores a value captured by the scan at endpoint.go:1357, under the lock. An admin edit to sharenfs outside the endpoint between the scan and the restore would be overwritten. This is a window, not a demonstrated race: zfs has no compare-and-set, and sameDataset's pattern does not apply to properties. Flag only. Not a dedupe concern.
- Two endpoints per parent are not covered by the in-process lock (ADR 0010 consequences), so no in-memory mechanism can be the source of truth.

## 5. Recommendation (ranked)

1. (d1) Shared bounded-undo context helper. About 2 to 6 lines saved, high confidence, low risk. Do it only in a pass that touches these functions anyway.
2. (c) Named attempt struct for `changed`/`fresh`/`made`. About 0 lines saved; clarity gain, low risk. Optional.
3. Documentation: add to CONTEXT.md under **Rollback** that the state is the marker properties classified by `Row` predicates, and that `classify()` in fsm_classify_test.go is the spec oracle. Zero code change.
4. Reject: (a) undo log (net add, breaks the call budget, duplicates PlanRollbackCreate); (b) production state enum (duplicates Row predicates; promoting classify() removes the independent oracle); (d2), (e), (f).

Honest answer to the owner's question: the daemon does not lack a state model. It already keeps state in ZFS marker properties and re-derives it on each pass. Cleanup hooks in Go would be net additive, and the only real dedupe is a few lines.

## 6. Summary

1. The daemon already models state: persistent marker properties, classified by Row predicates (table.go:117-144), with PlanCreateVolume as the transition function.
2. Undo sites all re-derive from a fresh scan: rollbackCreate (endpoint.go:718, 31 code), PlanRollbackCreate (plan.go:766, 24), releaseCloneSource (plan.go:737).
3. Exceptions: rollbackExpand (endpoint.go:1098, 12 code) uses a before-Row; restoreShareNFS (endpoint.go:1423, 8 code) uses a before-Row with no re-scan.
4. In-memory state is three booleans in CreateVolume (changed/fresh/made, endpoint.go:601-602) and Error.pre (errors.go:286).
5. An undo log (a) is net additive (about +0 to +40 code), needs a GUID read on success (breaks TestRPCCallBudget), and dies with the process.
6. A production state enum (b) duplicates Row predicates. The test oracle classify() (fsm_classify_test.go:23, 123 code) is independent by design.
7. Tracker (c) and error re-keying (e) are about 0 net lines.
8. Compensation helper (d1) saves about 2 to 6 lines; the best candidate, high confidence, low risk.
9. Pins: rollback_test.go (568 lines), rollback_plan_test.go (91), crash_test.go (713, rollback sweeps at 400-471, 597), callbudget_test.go (239, no rollback rows), fsm_* and modelrun_test.go (no rollback references).
10. Recommendation: little to gain. Do d1 with any touch to these functions, document the state model in CONTEXT.md, and reject the undo log and the state enum.
