# Truth probes: AN-01, AN-04, AN-09 (wave 13)

Copy: `/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave13/truth-copy` (repo untouched; rsync is absent, so tar was used).
Mutant tree: `.../wave13/mutant`. Loose copies of probes/diffs: `.../wave13/probe-src/`.

## Anomalies found on the way (ranked)

1. **The PR-tier seed corpus never plans a `ReasonOrphanCloneSource` action.** Logging `first.Actions` over the 8 corpus entries gave 14 actions: 8 undesired, 3 released-with-volume, 2 uncommitted, 1 with-volume, 0 orphan clone source. So the orphan-clone-source group is never applied by the PR tier's fuzz replay (only by directed tests). Leak seeds exist (0x83, 0xff...), but each is young, on an unmanaged source, or hidden behind another skip. Fix: add `f.Add([]byte{0x87, 0x00})` (done in the copy).
2. **The recovery FSM explorer behind AN-09 is not in HEAD.** `TestFSMExplore`, `classify` and `rec/fsm.sh` exist only in the scratchpad (`rec/fsmrun/...`) and the `origin/claude/gallant-johnson-1qaxp5-state` branch outputs. A `grep -rn IMPOSSIBLE` of HEAD finds only unrelated hits. The nightly comment says the explorer should land as `TestExplore*`; today nothing carries that name.
3. **Failed groups are by design permanent in the fuzz.** On unmodified code, seeds `177a8048a5875e8e` and `#5` end the third run with `Skipped: failed: ... has children / has dependent clones` (a child dataset or an outside clone the scan cannot see). So "forever-failing group" is a normal fuzz outcome, and an oracle that only bans failures must whitelist these (see AN-01 fix).
4. The nightly `explore` job's comment admits it passes with no tests ("Until such tests exist this step runs nothing and passes"), so AN-04 is documented-intentional but unguarded; nothing flips it when tests arrive.

## AN-01: real (as an oracle weakness), defensive wording

What the code does (`reconcile.go` `applyReconcile`): a failing group is appended to `Skipped` as `failed: <err>`; only ops that ran (`acts[:n]`) go to `Actions`. When a group's first op fails, n=0, so it adds nothing to `Actions`. Dependents get `SkipDependentFailed` in `Skipped` as well. The fuzz checks only `len(third.Actions) == 0` (line ~226).

Probe: `internal/endpoint/probe_an01_test.go` `TestProbeAN01ForeverFailingGroupPassesWeakConvergence`. An old owned uncommitted volume, destroy busy on every run:
```
third: Actions=0 Skipped=[{v0 failed: op 1/1 destroy tank/v/v0: ... dataset is busy}]
weak oracle (third.Actions==0) holds=true; strong holds=false; dry=1
```
Mutation evidence (`mutant/`): `applyReconcile` made to never apply `ReasonOrphanCloneSource` groups and report `failed: mutant`. Existing `FuzzReconcileSafe` seeds: **pass** (ok). `TestReconcileCollectsLeakedCloneSource` fails, so the suite as a whole catches it, but the fuzz does not. With the strengthened oracle plus the new seed: killed (`third reconcile still failing v0@volume-source-for-volume-gonev0 ... failed: mutant`).

Caveat: the injected busy failure clears after run one (`sim.Fail = nil`), so the fuzz itself does not inject forever-failures; the gap is for real bugs that fail every run. A naive "no failed skip" check is WRONG: it fails on the unmodified code (item 3 above). Strengthened version (diff `probe-src/an01-fuzz-strengthen.diff`, applied in the copy): track `hidden[id]` for volumes with a child dataset or an outside clone of a deferred snapshot; after the third run fail on any `failed:` skip whose target is not hidden, and (when `hidden` is empty) require an empty dry-run plan. Result on unmodified code: seeds pass; 120 s of fuzzing, 15.9k execs, no failure. Not tried: longer fuzz, or the dependent-skip rule when `hidden` is non-empty (relaxed: any dependent skip is allowed then, a deliberate weakening).

## AN-04: real

`go test -tags nightly -count=1 -run '^Test(Scale|Explore)' -v ./...` exits 0; every package prints `warning: no tests to run` / `ok ... [no tests to run]`. `grep -rn "func TestScale\|func TestExplore"` finds none, and the only `go:build nightly` hit is the workflow itself.

Guard: `ci/check-explore-ran.sh <log>` (in the copy; also `probe-src/`): counts top-level `--- PASS: Test(Scale|Explore)` lines in the `-v` log, fails with `::error::` on zero. Tested locally: empty log exit 1; a temp `TestExploreProbe` (plus a skipped `TestScaleSkipped`) exit 0 with "1 test(s) passed" (skips and subtests do not count); no `-tags nightly` exit 1. Workflow edit: after the `go test ... | tee` step add `- run: ci/check-explore-ran.sh "$RUNNER_TEMP/explore.log"` (pipefail is on under `shell: bash`, so the go exit code still propagates). The guard will fail the job today, which is correct, but it makes the nightly red until a TestExplore*/TestScale* test lands: land them together or gate it with the comment's stated contract.

## AN-09: real

Where: not in HEAD; `classify()` in the explorer `rec/fsmrun/internal/endpoint/fsm_explore_test.go` (line ~313): `if internal { return "IMPOSSIBLE(managed internal snapshot)" }` for any managed snapshot with the reserved `volume-source-for-volume-` name.

Why (data): `CreateVolume(b from a)` stamps `a@volume-source-for-volume-b` with `managed_resource=true` and `clone_source_for=b`. Probe `probe_an09_test.go` (`TestProbeAN09StampedCloneSourceIsLegal`, needs the two fsm files copied into the package):
```
row: Managed=true SnapshotName="" CloneSourceFor="b" OwnedCloneSource=("b",true)
classify = IMPOSSIBLE(managed internal snapshot)
```
`Row.OwnedCloneSource()` is the production definition of this state, and `PlanReconcile` collects it as `ReasonOrphanCloneSource`. The classifier predates the stamp: only the unmanaged branch has CloneSrc-* labels. Counts reproduced at HEAD: depth 2, 42 IMPOSSIBLE observations, trace `CV(a) ; CV(b<-a,reserve)`; depth 4 reported 2770 fs (and 1533 block, per the ledger).

Fix (diff `probe-src/an09-classify.diff`; fixed file `probe-src/fsm_explore_test.fixed.go`): in the managed `internal` branch, if `r.OwnedCloneSource()` return `OwnCloneSrc-deferred|live|orphan` (by `Deferred` / clones), else keep IMPOSSIBLE for unstamped managed ones. Rerun: fs depth 2, 3 and 4: 0 IMPOSSIBLE; block depth 2: 0. States seen at depth 4 included `OwnCloneSrc-live/orphan/deferred`. The probe asserts the wrong label today and is flipped after the fix. Not run: block at depth 4, democratic-csi prefix.

## Summary table

| id | real? | evidence | probe path | fix sketch | test level (ADR 0005) |
|---|---|---|---|---|---|
| AN-01 | Yes (oracle gap; no production bug shown) | third run shows `Actions=0` with a `failed:` skip and a non-empty dry plan; mutant survives old fuzz seeds, dies under new oracle + seed | `truth-copy/internal/endpoint/probe_an01_test.go`; diff `probe-src/an01-fuzz-strengthen.diff` | hidden-set whitelist, no non-hidden `failed:` skip, empty dry plan when `hidden` empty, add seed `{0x87,0x00}` | Simulator-level fuzz seed (PR tier replay); the oracle itself is lowest-level pure on `ReconcilePlan` data |
| AN-04 | Yes (documented-intentional but unguarded) | `[no tests to run]` in every package, exit 0 | `truth-copy/ci/check-explore-ran.sh` | run the script on the tee log after the go test step; land with first TestExplore*/TestScale* | CI script, tested locally on 3 logs |
| AN-09 | Yes (classifier bug in explorer; explorer not in HEAD) | stamped clone source labeled IMPOSSIBLE at depth 2; 0 after fix at fs depth 2/3/4, block depth 2 | `truth-copy/internal/endpoint/probe_an09_test.go` (+ `fsm_explore_test.go`, `fsm_shim_test.go` copied in) | `OwnedCloneSource()` branch in `classify`; diff `probe-src/an09-classify.diff` | Pure: `classify` on a `Row` is a single-row table test, no simulator needed (the traced world is only for context) |
| extra | Yes | seed corpus has 0 orphan-clone-source actions | log run described above | new seed | simulator-level seed |
