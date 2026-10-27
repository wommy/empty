# wave9: differential fuzzing of the ZFS layer

Three implementations of `zfs(8)` were compared:

- **model**: an independent reference state machine of OpenZFS 2.4.4. It was written from the `zfs-2.4.4` tag (`wave9/oz`), not from the simulator.
- **sim**: `internal/zfs/zfstest/sim.go` at HEAD `1603dad`.
- **real**: OpenZFS 2.4.4. It runs only in CI, through a replay harness. That run has **not happened yet**, so every "expected real" cell below is the model's reading of the source.

Everything lives in the copy at `wave9/zfs-diff`. The repo was not touched, and nothing was committed.

## Artefacts

| Path in `wave9/zfs-diff` | Contents |
|---|---|
| `internal/zfs/zfsdiff/model.go`, `cmds.go`, `props.go` | The reference model, which implements `zfs.Runner`. Commands: create (-p -o -V -s -b -u), snapshot (-r -o), clone (-o -p -u), destroy (plain, -r, -R, -d), set, set -u, inherit (-r -S), hold/release (-r), rename, promote, get -Hp, plus `send -p \| recv`. It models: property sources local, default, inherited, received and `$inherit`, with `$hasrecvd`; origin and clones; userrefs; defer_destroy with rmorigin and release reaping; volsize and volblocksize; quota, refquota, reservation and refreservation, including auto, the synthetic reservation, ENOSPC checks and the volsize revert; changelist mount and share rules. Rules carry `file:line` citations. Function-start lines are exact. Inner line numbers can be off by up to about 10 lines, except in `classify.go`, where every line was checked. |
| `classify.go` | 30 error classes, each tied to the OpenZFS string, plus `KindOf`, which gets the production `zfs.Kind` through `zfs.Client` itself. |
| `exec.go`, `render.go` | Sequence format, the step executor, the `get -Hp` projection over 25 properties plus 2 user properties after every step, the comparator (exit code, class, Kind, projection) and the minimiser. |
| `gen.go` | Seeded, state-aware generator: it runs a scratch model so that later steps pick names that exist. |
| `fuzz_test.go` | `FuzzDiff` (added to the CI fuzz matrix) and `TestRandomModelVsSim` (30 fixed seeds). |
| `diff_test.go` | `TestCorpusModelVsSim`, the `Known` ambiguity list, `TestReplayHarnessOnSim` (the replay machinery with the sim standing in for the pool), and `TestRandomSurvey` (an on-demand survey: `ZFSDIFF_SEEDS=N`). |
| `model_test.go` | 22 cited rule checks. |
| `real.go` | Real runner (`bash -c 'zfs send -p \| zfs recv'`), `get -j -p` cross-check, hold-releasing cleanup, `ReplayAll` (4 workers), report. |
| `testdata/zfsdiff/*.txt` | 100-sequence corpus, embedded with go:embed. |
| `internal/endpoint/zfsdiff_replay_test.go` | `TestZFSDiffReplay`: real vs sim vs model when `ZFS_TEST_POOL` is set; model vs sim otherwise. It never skips. |
| `ci/real-zfs-vm.sh`, `.github/workflows/ci.yml` | `ZFSDIFF_REPORT`, a job-summary line, an always-on artifact, and `FuzzDiff` in the fuzz matrix. |
| `wave9/sim.diff` | The simulator fixes, plus the two tests that had pinned unreal behaviour. |

Corpus coverage:

| Area | Sequences |
|---|---|
| refreservation=auto and volume sizing | 15 |
| destroy -r with clones | 11 |
| destroy -d | 10 |
| received vs local | 10 |
| sharenfs or mount on unmounted datasets | 10 |
| create -p | 9 |
| quota and reservation | 9 |
| set -u | 5 |
| holds | 5 |
| inherit | 5 |
| misc | 5 |
| promote | 3 |
| snapshot | 3 |

## Method and runs

- Each sequence runs under a fresh root, set up with `create -p $R` under pool `zfsep-test`, which has root `compression=lz4` as in CI.
- After every step the executor compares:
  - success or failure and the exit code (0, 1, 2, other);
  - the error class;
  - the production `zfs.Kind`;
  - the sorted `get -Hp -r` projection.
- Comparison stops at the first divergence, which is then minimised by dropping steps while the signature still holds.
- Steps the sim does not implement (rename, destroy -R: exit 3) end the comparison.
- Survey runs:
  - 14 rounds of 400 to 4000 seeds of 14 to 40 steps;
  - `FuzzDiff` for 2 minutes;
  - all 100 corpus sequences.
- After the fixes:
  - the final survey (seeds × steps, see the end of this file) shows no divergence beyond `Known`;
  - the corpus shows 1 divergence, the `Known` one;
  - `go test -race ./...` passes, and so do `go vet` and `gofmt`.

## Divergence table

"HEAD sim" is the simulator before the fixes. Every "expected real" cell is the model's reading of 2.4.4 source; the CI replay confirms or refutes it.

| # | Minimised sequence | model (= expected real) | HEAD sim | Verdict | Fix |
|---|---|---|---|---|---|
| 1 | `create $R/a` then `get ... userrefs` | `userrefs` on snapshots, `-` elsewhere | the whole get fails: `bad property list: invalid property 'userrefs'`; `hold` and `release` are unrecognized | sim bug | holds: hold/release (-r); EBUSY "it's being held" (KindBusy); destroy -d defers a held snapshot; the last release destroys it |
| 2 | `create $R/a` | `compression lz4 inherited from zfsep-test` | `on default` (native inheritance only for sharenfs/sharesmb) | sim bug | PROP_INHERIT natives inherit; a snapshot inherits from its dataset first |
| 3 | `create -V 67108864 $R/v` | refreservation 70254592 (`zvol_volsize_to_reservation`) | 69206016 (`n+n/64+1MiB`) | sim bug | exact formula, which depends on volblocksize |
| 4 | `create -s -V 1000000 -o volblocksize=3000 $R/x` | `'volblocksize' must be power of 2 from 512B to 16M` | success | sim bug | block-size validation |
| 5 | `create -V ... $R/v; create -s -V ... -o volblocksize=3000 $R/v` | badprop (volume props are validated before anything else, `zfs_main.c:1200`) | exists | sim bug | validation order. The endpoint's create-retry path is affected: real gives InvalidProperty, not Exists |
| 6 | `create -V ... $R/v; create -p $R/v` | `dataset already exists`: the -p check is type-specific (`zfs_dataset_exists(name, type)`) | success | sim bug | type-specific -p |
| 7 | `create -V ... $R/v; set reservation=none $R/v` (also quota, refquota, refreservation) | `0 local` | `none local` | sim bug | `none` is stored as 0 |
| 8 | `create $R/a; set refquota=0 $R/a` | `use 'none' to disable quota/refquota` | success | sim bug | rejected |
| 9 | `create $R/a; set canmount=noauto $R/a` | stays mounted (no changelist for on/noauto, `libzfs_dataset.c:1814`) | `mounted no` (mount state was derived from canmount) | sim bug | explicit mount state with changelist prefix and postfix rules |
| 10 | `create -u $R/a; set sharenfs=off $R/a` (test `TestSetNoMountLeavesUnmounted`) | stays unmounted (postfix mounts only when a share ends up on) | mounted | sim bug, and the test pinned it | test now sets a share value |
| 11 | `create -o canmount=off $R/a; snapshot; @sendrecv ... $R/r` | received canmount=off, not mounted | mounted (canmount was read from local values only) | sim bug | canmount read through `value()` |
| 12 | `@sendrecv` of `zd:a`, `set zd:a=2`, `inherit zd:a` | `- -`: plain inherit writes `$inherit`, which hides the received value (`dsl_prop.c:795-810`) | received value shows again | sim bug, and the test pinned it | `$inherit` and `$hasrecvd` modelled. `TestFileExportReceivedShareNFS` now uses `inherit -S` |
| 13 | thick zvol received, `set volsize=...` | refreservation tracks volsize and becomes local (the effective value is compared, wherever it was set) | stays `received` | sim bug | effective value compared |
| 14 | `create -V ... $R/v; snapshot $R/v@s1` | snapshot shows `xattr -`, `acltype -`, `volsize ... local` (headcheck `libzfs_dataset.c:2131`) | `sa default`, `off default`, `volsize -` | sim bug | headcheck; snapshot volsize source is local |
| 15 | `create -V 64M; snapshot; set volsize=128M` | the snapshot keeps 64M; a clone of it gets 64M | snapshot follows the live volume (64M→128M); so do clones | sim bug (known gap) | volsize is recorded at snapshot time; clone uses the snapshot's |
| 16 | `create $R/a; set refreservation=21474836480 $R/a` | `size is greater than available space` | success | sim bug | reservation-only space model: pool free, ancestor quotas, refquota/refreservation cross-checks, ENOSPC per property with partial apply, volsize revert |
| 17 | `create -p -o refreservation=21474836480 $R/b` | `cannot create ...: out of space` | success | sim bug | as #16 |
| 18 | `create -V ...; snapshot -o zd:a=s $R/v@s2` and `snapshot -r` | `-r` snapshots every descendant atomically | `-r` taken as an operand, so a single snapshot | sim bug | `snapshot -r`, atomic |
| 19 | `create $R/a; snapshot $R/a@s1; create $R/a/c; snapshot $R/a/c@s1; destroy -rd $R/a@s1` | both snapshots gone | `-r` ignored for snapshots; also `-rd` was parsed as a dataset name | sim bug | flag parsing; recursive snapshot destroy; gathers children even when `fs@snap` is missing |
| 20 | `create -V ... $R/v; destroy -rd $R/v` | `missing '@' delimiter` | **destroyed `$R/v`** (`-rd` read as a name, then a plain destroy) | sim bug | flag parsing |
| 21 | `create $R/a; snapshot; create $R/a/x; hold t1 $R/a@s1; destroy -r $R/a` | `a/x` destroyed; error held (EBUSY); `a` busy (a partial destroy) | at HEAD the hold was ignored and everything destroyed; after the first fix the whole tree was refused | sim bug | children-first walk with atomic per-dataset batches, run to a fixpoint |
| 22 | `create $R/a; snapshot; clone $R/a@s1 $R/a/x; destroy -d $R/a@s1; destroy -r $R/a` | success | **panic**: nil dereference in `remove()`, because the deferred origin was reaped by its clone and then removed again | sim bug (crash) | `remove` tolerates an already-reaped origin |
| 23 | `clone $R/a@s1 $R/x; promote $R/x; snapshot $R/a@s1; promote $R/a` | `conflicting snapshot 's1' from parent ...` | success, **overwriting** `a@s1` | sim bug | promote moves every snapshot up to the origin, checks conflicts and swaps origins |
| 24 | `promote` of a clone of `@s2` with `@s1` older | `x@s1` and `x@s2` move; `a` becomes a clone of `x@s2`; `x` takes `a`'s origin | only the origin moved; the clone's origin was cleared | sim bug | as #23 |
| 25 | `destroy -d $R/x@s1` (x missing) | `cannot open 'x': dataset does not exist` | `could not find any snapshots` (both NotFound) | sim bug (text) | text |
| 26 | `inherit canmount $R/missing`, `inherit bogus $R/a` | property checked first: `cannot be inherited` exit 1, `invalid property` exit 2 | opens the dataset first; bogus property exit 1 | sim bug | validation order; read-only and -S rules |
| 27 | `snapshot $R/missing@s1` | exit 2 (usage) | exit 1 | sim bug (known) | exit 2 |
| 28 | `snapshot -o compression=off`, `set -u dedup=off $R/a@s1` | `this property can not be modified for snapshots` | invented text, or "does not apply" | sim bug (text) | real text |
| 29 | `set atime=off atime=on $R/a` | exit 255 `specified multiple times` | success | sim bug | exit 255 |
| 30 | `release -r t1 $R/a@s1` where a child lacks the snapshot | the last problem wins: `dataset does not exist` | `no such tag` | sim bug | last error, depth first |
| 31 | `rename ...`, `destroy -R ...` | implemented | unrecognized / invalid option | sim gap, not fixed: the endpoint uses neither | the adapter maps these to "unmodelled" |
| 32 | `create -V 64M -o refreservation=auto $R/v` | ENOSPC `out of space`: 2.4.4 has no `zfs_create_fix_auto_resv`, so UINT64_MAX reaches the kernel; master resolves it | resolves to the auto value | **ambiguity** (needs the real run) | sim left as is; `Known` entry; corpus `create-vol-resv-auto` |
| 33 | `set refquota=none` | `0 local` (contradicts wave5 invariant 6, "0 iff default") | (now) `0 local` | **ambiguity** | corpus `refquota-zero`, `recv-quota-none` |
| 34 | `release -r` with several children | which error is reported depends on zap iteration order | name order | **ambiguity** (nondeterministic in real) | corpus has a single child only |
| M1 | generator found a `rename` that panicked the model | rename re-keyed using a name it had just changed | n/a | model bug | fixed |
| M2 | hold/rename `-r` visited heads in Go map order (nondeterministic) | n/a | n/a | model bug | sorted depth-first |
| M3 to M6 | found while triaging #9, #12, #16 against source | mount prefix descended past a local mountpoint; `$hasrecvd` lookup is inheritable; synthetic-reservation ENOSPC must revert volsize; `available` must cap by ancestor quotas | n/a | model bugs | fixed |

**Counts:**

- 31 sim bugs: rows 1–31. Row 31 is a documented gap that was not fixed.
- 6 model bugs: M1–M6. Two were found by fuzzing; four came out of source review while triaging.
- 3 ambiguities: rows 32–34.

Two of the sim bugs were pinned by tests, which now assert the real behaviour (rows 10 and 12).

## Production classification gaps

The comparator records `zfs.Kind` for every real stderr. These OpenZFS 2.4.4 messages classify as `KindUnknown` in `internal/zfs/errors.go`:

- `'volblocksize' is readonly` (clone -o and set)
- `does not apply to datasets of this type`
- `'volsize' cannot be zero`
- `use 'none' to disable quota/refquota`
- `this property can not be modified for snapshots`
- `parent is not a filesystem`
- `conflicting snapshot ... from parent` (EZFS_EXISTS, but no "already exists" text)
- `tag already exists on this dataset`
- `no such tag on this dataset`
- `not a cloned filesystem`
- `missing '@' delimiter in snapshot name`
- `property 'x' specified multiple times` (exit 255)

`size is less than current used or reserved space` maps to InvalidProperty by design.

These are reported, not changed.

## Top 10 simulator infidelities that could hide endpoint bugs

1. **Mount state derived from canmount** (rows 9–11). Real changes mounts only through changelist rules: `noauto` keeps a mounted dataset mounted; `on` does not mount; sharenfs=off does not mount; a received canmount counts. The endpoint's export logic (`export.go:514`, `row.Mounted`/`CanMount`) and `SetNoMount` were tested against the wrong state machine.
2. **Received values after a plain `zfs inherit`** (row 12). Real hides the received value; the sim showed it again. `TestFileExportReceivedShareNFS` built its scenario with that unreal step. Any endpoint path that inherits and expects a received sharenfs to show through is untested.
3. **No space model** (rows 16–17). Expand, Create and clone with a refreservation never hit ENOSPC in the sim, except through `Fail` injection. The endpoint's Expand sends `refquota` and `refreservation` sorted, so refquota goes first. Real then **half-applies a shrink**: refquota fails against the old refreservation, and refreservation is still set. The sim applied both.
4. **zvol snapshot volsize followed the live volume, and clones copied the live size** (row 15). Snapshot and restore capacity, and clone-from-snapshot sizing, were never exercised with a volume expanded after its snapshot.
5. **Holds absent** (rows 1, 21). Destroying a snapshot held by a backup tool succeeded in the sim. Real gives EBUSY (KindBusy), and `destroy -r` **partially destroys** the tree. The endpoint's delete and reconcile of half-deleted trees is untested.
6. **autoResv approximation and the synthetic reservation on received volumes** (rows 3, 13). Any exact comparison of refreservation with the thick value (thick vs sparse detection, the `refreservation=auto` decision for clones) disagreed with real by about 1 MiB per GiB.
7. **Native inheritance missing** (row 2). Sources printed `default` where real prints `inherited from <pool>`. Endpoint drift and adoption logic that branches on the source of compression, recordsize and the rest was never fed real sources.
8. **`none` stored literally and refquota=0 accepted** (rows 7–8). A read-back of `none` fails uint parsing in the sim and is `0 local` on real.
9. **Volume create validation order** (row 5). On real, a CreateVolume retry with an invalid property on an existing name gets InvalidProperty, not Exists. The sim drove the endpoint into the adopt-existing path instead.
10. **`destroy -r` with a deferred origin inside the tree panicked the sim, and `-rd` destroyed its target** (rows 20, 22). The endpoint's own layout, an internal clone origin destroyed with `-d` and then a tree with `-r`, could crash the test process rather than reveal a bug.

## How the replay slots into CI

- **Where it runs.** `TestZFSDiffReplay` is in `internal/endpoint`, so it ships inside the `endpoint.test` binary that `ci/real-zfs-vm.sh` already builds statically, copies to the VM and runs as root with `ZFS_TEST_POOL=zfsep-test ZFS_REQUIRE_REAL=1`. The corpus is embedded with `go:embed`, so no testdata needs copying.
- **What a run does.**
  - It creates `zfsep-test/zfsdiff-<pid>-<ns>` and runs the 100 sequences with 4 workers, each under `.../s<i>`.
  - Each sequence is replayed on the real pool, then on a fresh sim and a fresh model under the same names.
  - It cross-checks `zfs get -j -p` against `-Hp` once per sequence.
  - It releases holds and runs `destroy -r` per sequence and on the base. A cleanup failure is the only thing that fails the test.
  - Each sequence gets a verdict: agree, sim-bug, model-bug, both-wrong, all-differ or harness-error.
  - The 3-minute context bounds the run. The expected cost is about 1,500 zfs invocations, roughly 15–30 s with 4 workers (estimated; it has not run on a pool yet).
- **Report-only.** Divergences are reported, not failed. `ZFSDIFF_STRICT=1` makes them fatal once the ambiguities are settled.
- **Script and workflow changes.**
  - The VM env gets `ZFSDIFF_REPORT=$PWD/zfsdiff-report.log`, and the existing `scp *.log` brings the report back.
  - The script puts the verdict-count line in the job summary, and fails the leg if the report is missing (which would mean the test did not run).
  - `ci.yml` uploads `zfsdiff-report.log` with `if: always()`, because the existing log upload runs only on failure.
  - `ci.yml` adds `FuzzDiff` to the fuzz matrix.
- **Skip budget.** The test never skips: with no pool it runs model vs sim. So `ci/check-skips.sh` and `expected-skips.txt` need no change.
- **Local check.** `TestReplayHarnessOnSim` runs the same `ReplayAll` with a shared sim as the "pool": 99 agree, plus 1 model-bug, which is the Known ambiguity seen from the sim's side.
- **After the first CI run.**
  - Read `zfsdiff-report.log`.
  - Settle rows 32–34, then move `Known` entries into the sim or the model.
  - Turn on `ZFSDIFF_STRICT`.
