# Mutation testing: pure, high-stakes code of the Go ZFS endpoint

Copy mutated: `wave7/mut-copy` (repo untouched; builders' concurrent edits not in it). Go: `GOTOOLCHAIN=go1.27.3`.
Baseline suite (`go test ./internal/...`) is green in the copy before and after the added tests.

## Tool

- `go-gremlins/gremlins@v0.6.0` installs and runs on Go 1.27.3 (`wave7/bin/gremlins`; dry run on `./internal/endpoint`: 485 runnable mutants). It was **not used for the run**: it only has conditional-negation/arithmetic-style operators, with none of: guard-`return err` deletion, `Code`->`Internal`, bool-field flip, ownership-check drop, classifier-table edits.
- Hand-rolled mutator instead: `wave7/mutgen/main.go` (go/ast, stdlib only, built to `wave7/bin/mutgen`) enumerates single-site byte-range mutants to `wave7/mutants.json`; `wave7/run_mutants.py` applies one at a time in 4 worker copies, runs `go test ./internal/... -count=1 -failfast` (150 s timeout; timeout would count as killed, none occurred), restores, appends to `wave7/results.jsonl`. Mutants that do not compile are discarded and replaced by the next sample (10 discarded).
- Operators: `EQ_FLIP` (==/!=), `REL_FLIP` (</<=, >/>=), `IF_NEGATE`, `ANDOR_SWAP` (&&/||), `DROP_OPERAND` (one operand of &&/|| replaced by true/false: this is also the "drop one ownership check" operator), `OWNED_TRUE` (`x.OwnedVolume()`/`OwnedSnapshot()` call -> true), `GUARD_DELETE` (the `return`/`continue` inside an `if` body removed: the "delete a `return err` guard" operator), `CODE_INTERNAL` (`errorf(X,..)` or `code = X` -> Internal), `BOOL_FLIP` (true/false field, return, assignment), `PATTERN_KIND` / `PATTERN_DROP` (classifier table entry's kind -> KindUnknown / entry removed).
- Scope enumerated: 942 mutants. `plan.go` all (490); `reconcile.go` 1-296, the pure part (114); `table.go` 1-273, Row/Table/decode, not `scan` (86); `errors.go` = `internal/endpoint/errors.go` plus `zfsErr`/`parentUnavailable` in `endpoint.go`, because that is where zfs-kind -> Code actually lives (27); `props.go` (129); `internal/zfs/errors.go` (80); `internal/zfs/zfs.go` `parseColumns` only (16).
- Sampled 303 valid mutants (random, seed 7, per-file quota: plan 90, reconcile 50, props 40, table 40, zfs/errors 40, errors 27 = all, parseColumns 16 = all). Per-file rates on 16-50 samples carry roughly +-10 points; the total (303 of 942) about +-4.

## Kill rate per file

| file | sampled | killed by existing suite | rate | after new tests | equivalent/unreachable |
|---|---|---|---|---|---|
| plan.go | 90 | 69 | **77%** | 85 (94%) | 5 |
| reconcile.go (pure part) | 50 | 45 | **90%** | 49 (98%) | 1 |
| table.go (Row/Table/decode) | 40 | 30 | **75%** | 40 (100%) | 0 |
| errors.go (+zfsErr) | 27 | 24 | **89%** | 27 (100%) | 0 |
| props.go | 40 | 34 | **85%** | 39 (98%) | 1 |
| zfs/errors.go (classify) | 40 | 21 | **52%** | 39 (98%) | 1 |
| zfs/zfs.go parseColumns | 16 | 9 | **56%** | 16 (100%) | 0 |
| **total** | **303** | **232** | **76.6%** (78.6% excluding the 8 equivalents) | **295 (97.4%)** | 8 |

By operator (sampled), survivors concentrate in `DROP_OPERAND` (22 of 63), `PATTERN_KIND`/`PATTERN_DROP` (15 of 27) and `GUARD_DELETE` (10 of 33). `EQ_FLIP`/`IF_NEGATE` are killed almost always (92%/100%): the suite checks main paths, and misses the secondary operand of every compound condition and every alias/fallback.

## Surviving mutants (71), grouped by missing invariant

Ids are `mutants.json` ids. "NEW" = killed by a test added in the copy (section below). "EQUIV" = no observable behaviour change (reason given).

### A. Ownership: nothing pins "a managed-looking dataset that is not this volume is not ours"
| file:line | mutation | why no test caught it |
|---|---|---|
| plan.go:224 (#187) | clone-from-snapshot: `src.OwnedVolume()` -> true | every clone test uses an owned source volume; none has a CSI snapshot on an unmanaged/renamed source. NEW |
| table.go:71 (#624) | `OwnedVolume`: drop `(VolumeName=="" \|\| VolumeName==ID)` | the ADR 0003 rule (a `send\|recv` copy or renamed dataset carries another volume's name and is not owned) has no `Row`-level test and no planner test with a mismatching marker. NEW |
| plan.go:168,169 (#127,#123) | existing-row check `VolumeName != "" && VolumeName != r.Name`: `!=`->`==` / the refusal deleted | CreateVolume would answer "already done" for another volume's dataset; no test has `VolumeName` differing from the request. NEW |
| table.go:80 (#641,#638,#637) | `OwnedSnapshot`: drop `Type==Snapshot`, drop `&& Managed`, `&&`->`\|\|` | only well-formed snapshot rows are ever fed; no filesystem/zvol row carrying snapshot markers, no unmanaged snapshot with a CSI name. NEW |
| table.go:178 (#677) | `Volumes()` also yields snapshots | no test of the iterator; callers also filter with `OwnedVolume()` (which excludes snapshots), so today redundant defence. NEW (iterator test) |

### B. Absence / "already done" decisions
| file:line | mutation | why |
|---|---|---|
| plan.go:533 (#478) | `PlanDeleteSnapshot`: `!ok \|\| row.Deferred` -> drop `Deferred` | a deferred *foreign* snapshot should be "done" (nil, nil); with the mutant it is FailedPrecondition. No test of a deferred snapshot at all. NEW |
| plan.go:356 (#298) | `compatible`: drop `have == 0` in the limit check | EQUIV: the preceding `want > 0 && (have==0 \|\| ...)` already returns AlreadyExists when the volume is unlimited, because `want` becomes `LimitBytes` when no capacity is asked. |
| zfs/errors.go:132,134,135,137 (#876,#877,#881,#882,#887) | NotFound/Exists/Busy aliases ("no such pool or dataset", "could not find any snapshots", "pool or dataset exists", "pool or dataset is busy") dropped or -> Unknown | only the primary phrase of each kind is in `TestClassify`; an absence the classifier misses becomes Unknown -> Internal and a delete of a gone snapshot fails instead of being done. #887 is EQUIV (shadowed by "dataset is busy", same kind). NEW for the rest |

### C. GC predicate and fixpoint simulation (`reconcile.go`)
| file:line | mutation | why |
|---|---|---|
| 244 (#568) | `age < MinAge` -> `<=` | no test at exactly `MinAge`; doc says "less than MinAge is untouched". NEW |
| 269 (#579) | `reap`: `r.Name == origin && r.Deferred` -> drop name test | the only simulate test has a single deferred origin, so "reap all deferred" is indistinguishable. Planned volume deletions could then be judged unblocked by a snapshot still pinned by another clone. NEW |
| 278 (#593) | non-`Recursive` destroy also removes children | no non-recursive case. NEW |
| 278 (#597) | `HasPrefix(r.Name, op.Target+"/")` dropped | EQUIV in practice: `scan` drops everything deeper than a volume, so no row can be a `/` child. |
| 285 (#602) | `i >= 0` -> `> 0` | deferred-destroy target happens to be at index 1 in the only test. NEW |

### D. Error code unasserted: tests check "an error", not the CSI code
| file:line | mutation | why |
|---|---|---|
| plan.go:108 (#29,#23) | "at most one content source": code -> Internal / guard deleted (snapshot silently wins) | no request with both sources. NEW |
| plan.go:111 (#31) | block volume without capacity: guard deleted | no test; creates a zvol with size 0. NEW |
| plan.go:120 (#73) | filesystem + sparse: guard deleted | NEW |
| plan.go:252 (#217), plan.go:228 (#190) | clone kind mismatch: code -> Internal / guard deleted (snapshot path) | the volume-source path is tested, the snapshot path is not. NEW |
| plan.go:366 (#333) | reserve mismatch on committed fs: AlreadyExists -> Internal | tests only check a different mismatch. NEW |
| plan.go:443 (#399) | expand over limit: OutOfRange -> Internal | NEW |
| plan.go:472 (#423) | `name == ""` dropped from snapshot-name validation (empty CSI name accepted; the snapshot would then never satisfy `OwnedSnapshot`, which requires a CSI name) | no empty-name test. NEW (`empty snapshot name` row of `TestMutPlannerErrorCodes`) |
| endpoint.go:280 (#701) | `zfsErr`: NotFound -> Internal | no test maps zfs kinds to Codes at all (callers handle `IsNotFound` first). NEW |
| endpoint.go:275 (#699) | `zfsErr(nil)` returns non-nil | EQUIV today: every caller checks `err != nil` first; contract is untested. Killed by the nil assertion in the new test. |
| errors.go:36 (#691) | `Error()` drops the `: cause` part | message format untested. NEW |
| zfs/errors.go:91,103 (#850,#851,#856,#859) | `Error()` msg fallback; `Is` ignores Op/Dataset check | `Is` strictness (only bare sentinels match) and message fallback untested. NEW |

### E. Parser of untrusted `zfs get` output (`parseColumns`)
| file:line | mutation | why |
|---|---|---|
| zfs.go:445 (#930, #931, #932, #933, #935) | "want 4 fields" check: guard deleted / `\|\|`->`&&` / either operand dropped / `<=`->`<` | no malformed line in any test (fuzz only has 3 valid-ish seeds and runs as seeds only). Without the guard `line[a:last]` panics (slice bounds) on a 1-2 tab line. NEW |
| zfs.go:459 (#938) | `if !ok { continue }` for a property we did not ask for deleted | a stray/forged property line is written into **column 0** instead of ignored. NEW |
| zfs.go:440 (#927) | blank line `continue` deleted | NEW |

### F. Classifier alias phrases (`zfs/errors.go:127-169`)
#867 "snapshot is cloned", #873 "no such parent", #894 "invalid property value" (kind), #896/#897 "bad property", #899 "invalid property", #912 "permission denied" (-> FailedPrecondition), #914/#915 "no such pool" (-> Unavailable, so an exported pool is never read as absent), #916 "pool i/o is currently suspended". Only about half of the 30 patterns appear in `TestClassify`; the others are exercised only by `FuzzClassify`, which checks the result is in range, not which kind. All NEW via one table test.

### G. Sizing and convergence (`plan.go`)
#105 (157) clone source size == limit refused (`>`->`>=`); #280 (340) uncommitted fs with smaller quota never grown; #286 (343) reservation always rewritten (a larger reservation is lowered, "never shrinks" violated). All NEW. #96 (154, `r.Kind==Filesystem` -> true), #238 (300), #253 (309), #259 (321): EQUIV or unreachable (an earlier validation or kind check makes the operand redundant: Block clones already have the source's volsize; the snapshot id always names a snapshot; no-source path is rejected by `validateCreate`; kind equality precedes the block-size check).

### H. Row derivation
table.go:52 (#608,#606) `Capacity`, table.go:60 (#621,#619,#620) `Kind`: snapshot rows (zvol snapshot is Block with volsize, filesystem snapshot is Filesystem with refquota) never asserted directly. NEW.

### I. Property allowlist (`props.go`)
#727/#729 `logbias`/`dedup` `fs: true` -> false (allowed on filesystems untested); #718 `gzip-9`; #767 9-digit size accepted / 10 refused; #775 `B` suffix; #778 EQUIV (`ParseUint` cannot fail on at most 9 digits). All but #778 NEW.

## Top 10 survivors that matter for safety

All tests are in the copy: `mut-copy/internal/endpoint/mutation_kill_test.go` and `mut-copy/internal/zfs/mutation_kill_test.go`. "Killed" was checked by re-running each survivor against the new suite (`wave7/rerun_survivors.py` -> `wave7/rerun.jsonl`): 63 of 71 survivors killed, the other 8 are the EQUIV above.

| # | Risk | Survivor | Minimal killing test | Verified |
|---|---|---|---|---|
| 1 | ownership / deletion | plan.go:224 #187 clone from snapshot of a source volume that is unmanaged, renamed or uncommitted proceeds | `TestMutCloneFromSnapshotNeedsOwnedSourceVolume`: owned source clones (control); with `Managed=false`, foreign `VolumeName`, or `Committed=false` the plan is NotFound | killed |
| 2 | ownership / deletion | table.go:71 #624 a dataset with another volume's name marker (send\|recv copy, rename) is "owned": DeleteVolume and reconcile may destroy it | `TestMutOwnedVolumeNameMarker`: `OwnedVolume()` false for `{Managed, VolumeName:"b"}` with ID "a"; `PlanDeleteVolume` -> FailedPrecondition; expand -> NotFound | killed |
| 3 | ownership | plan.go:168/169 #127/#123 CreateVolume answers success for a managed dataset belonging to a different volume name | `TestMutCreateRefusesDatasetOfAnotherVolumeName`: row `{ID:"a", VolumeName:"b"}`, request "a" -> AlreadyExists | killed |
| 4 | ownership | table.go:80 #641/#638/#637 `OwnedSnapshot` accepts non-snapshot rows or unmanaged snapshots as CSI snapshots (and so as clone sources / delete targets) | `TestMutOwnedSnapshot`: one row per broken condition (fs/zvol type, unmanaged, no CSI name, foreign source marker) must be false | killed (3) |
| 5 | absence decision | plan.go:533 #478 `PlanDeleteSnapshot` forgets a snapshot already pending deletion (foreign one errors, owned one re-issues a destroy) | `TestMutDeleteSnapshotDeferredForeignIsDone`: deferred foreign -> `(nil, nil)`; live foreign -> FailedPrecondition; owned -> one deferred destroy | killed |
| 6 | GC deletion predicate | reconcile.go:244 #568 a volume exactly `MinAge` old / `<=` boundary | `TestMutReconcileMinAgeBoundary`: age 3600 s with MinAge 1 h collected; 3599 s skipped `SkipTooYoung` | killed |
| 7 | GC deletion predicate | reconcile.go:269 #579 (+#593, #602) the simulation reaps every deferred snapshot, so a plan can delete a volume that a still-pinned origin would block | `TestMutSimulateDestroyReapsOnlyTheNamedOrigin`: two deferred origins, destroy one clone; only its origin disappears; plus non-recursive and index-0 cases | killed (3) |
| 8 | untrusted input / deletion | zfs.go:445 #930-935 malformed output line is not an error (slice-bounds panic or misparse) | `TestParseColumnsRejectsMalformedLines`: `tank/a\ttype\tvolume`, `tank/a\ttype`, `tank/a`, `a\tb\tc` each return an error | killed (5) |
| 9 | ownership forgery | zfs.go:459 #938 an unrequested property line is written into column 0 | `TestParseColumnsIgnoresPropertiesNotAskedFor`: extra `bogus` line leaves `type` cell intact | killed |
| 10 | absence decision / retry semantics | zfs/errors.go (15 survivors, e.g. #881 "no such pool or dataset", #882 "could not find any snapshots", #912 "permission denied", #914/#915 "no such pool") classifier aliases never asserted: an absence misread as Unknown (Internal) or an exported pool as something else | `TestClassifyEveryPhrase`: independent hardcoded phrase -> kind list for all 30 patterns (must not range over `patterns`, which would follow the mutation) | killed (all 15 PATTERN_* survivors; #887 is equivalent) |

Also added (code-assertion group D, sizing G, Row H, props I): `TestMutPlannerErrorCodes`, `TestMutZfsErrCodes`, `TestMutCloneKindMustMatchSource`, `TestMutCompatibleReserveMismatchCode`, `TestMutSizeOpsGrowQuotaNeverShrinkReservation`, `TestMutCloneSourceEqualToLimitIsAllowed`, `TestMutRowKindAndCapacity`, `TestMutPropertyAllowlistEdges`, `TestMutLogbiasDedupAllowedOnBothKinds`, `TestErrorIsMatchesKindOnlyForBareSentinels`, `TestErrorTextPrefersStderrThenCause`, `TestParseColumnsSkipsBlankLines`.

Why the existing suite misses these: it is scenario-driven (simulator and FSM) and asserts outcomes on well-formed rows. Compound conditions have one test per conjunct at best; aliases of a classifier phrase are never checked; error tests use `err != nil` or `code(err) == X` for only one of several refusals in a function.

## Recommendation: mutation score in CI

- **Per PR, incremental only**: run the mutator on the changed lines of `plan.go`, `reconcile.go`, `table.go`, `errors.go`, `props.go`, `zfs/errors.go`, `parseColumns`, against `go test ./internal/endpoint ./internal/zfs` (0.7 s warm, about 3-5 s per mutant including recompile and link, 4 parallel). A typical diff is 20-80 mutants: 1-2 minutes. Gate: **0 surviving mutants among `OWNED_TRUE`, ownership `DROP_OPERAND`, `GUARD_DELETE` and `CODE_INTERNAL` on changed lines; 90% overall on changed lines.**
- **Nightly, full scope**: all 942 mutants, sharded over 4 runners, about 15 minutes each (extrapolated: the measured sample was 303 mutants in about 35 minutes wall on 4 workers while builders kept the box at load average 25-48, mean 21 s per mutant, max 68 s; uncontended it is far less). Do not run the full set per PR (too slow, and a 150 s timeout per hung mutant). Thresholds: **>= 90% per file for `plan.go`, `table.go`, `reconcile.go`, `zfs/errors.go`, `parseColumns`; >= 85% overall; fail on any drop of more than 2 points against the committed baseline.** After the added tests the sampled score is 97.4% raw (100% of non-equivalent), so 90% leaves room for noise and new code without being a ratchet that blocks.
- Keep an allow-list of known-equivalent mutants (file:line:operator, the 8 above) so the score is not gamed by them; review it when the line moves.
- Do not put `props.go`/`internal/zfs` I/O code in the gate; they were not part of the high-stakes scope and add runtime for little signal.

## Files in `wave7/`

`bin/gremlins` (v0.6.0, dry run only), `bin/mutgen`, `mutgen/main.go`, `run_mutants.py`, `rerun_survivors.py`, `mutants.json` (942 enumerated), `results.jsonl` (313 rows: 303 valid + 10 build failures), `rerun.jsonl` (survivors vs new tests), `work/w0..w3` (worker copies), `mut-copy/` (copy with new tests, unmutated).
