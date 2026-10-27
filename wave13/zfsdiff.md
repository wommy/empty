# wave13: zfsdiff ported onto HEAD

Branch `zfsdiff` in worktree `/home/user/empty/.claude/worktrees/agent-a4b19369ba5ad6297`, based on `0a0bea3` (HEAD of `claude/gallant-johnson-1qaxp5`). Not pushed.

## Commits

| Commit | Subject |
|---|---|
| `05141d1` | zfs: differential reference model of zfs(8) and 30 simulator fidelity fixes |
| `3fec474` | ci: zfsdiff replay inside the one-minute budget; full replay nightly |
| `e63586e` | zfs: classify the 2.4.4 messages the zfsdiff replay saw as unknown |
| `8337b8c` | endpoint: a thick filesystem's Expand re-plans a half-applied reservation |
| `0352a8e` | zfs, endpoint: `zfs mount` op; an unmounted new filesystem converges |
| `5500395` | zfsdiff: the generator creates some filesystems unmounted |
| `27b0173` | endpoint: the half-applied Expand test holds on a pool that applies atomically |

## Findings (anomalies first)

1. **Endpoint bug, real: a thick filesystem's Expand reports success while under-reserved.**
   - Expand sends `zfs set refquota=N refreservation=N`. zfs applies the space properties one at a time, so refquota (sorted first) lands and refreservation fails with ENOSPC.
   - The retry then saw `want <= Capacity()` (refquota) and returned an empty plan. That is success, with refreservation still at the old size.
   - The simulator's new space model exposes this with no fault injection. Requesting 1 PiB fails on any test pool.
   - Fail before:
     - `TestExpandThickFilesystemRetryAfterENOSPC`: "retry 0 reported {... CapacityBytes:1125899906842624 ...} expanded; refreservation is 1073741824".
     - `TestPlanExpandReplansHalfAppliedReservation`: "capacity 10737418240: [] <nil>".
   - Fix (`PlanExpandVolume`): a thick filesystem (refreservation > 0) whose reservation is below max(request, refquota) gets a refreservation op, plus refquota when the request is larger.
   - Consequence: a half-applied 1 PiB refquota now makes every later Expand on that volume ResourceExhausted. The volume reports 1 PiB of capacity and must reserve it. No rollback was added (kept minimal).
   - On a real pool that applies both properties atomically, the test logs this and the retry assertions still hold.
2. **No endpoint test failed under the 30 simulator fixes at current HEAD.** Every endpoint, csi and server test passed straight after the port, including HEAD's newer NFS/nfsref, S2, S4, S6 and S8 tests. So there is no "endpoint vs old sim expectation" conflict to rank.
   - The flip side: the suite does not exercise most of the infidelities the wave9 report lists, such as holds/EBUSY partial `destroy -r` and received-value inherit. Finding 1 was found only because requirement 5 asked for it.
3. **Merge reconciliation in `sim.go` (5 conflict hunks, every other hunk applied cleanly):**
   - Mount state now comes from the copy's explicit changelist model (`node.mounted`) instead of HEAD's `unmounted` flag.
   - HEAD's libshare, `exportfs -ra` and etab model is kept intact: `Unmount` and `remove` call `unshare`/`exportfs`, and `reshare` skips children with a local sharenfs.
   - HEAD's sharenfs validation moved into `parseValue`, which runs in zprop order for create, clone and set. As a result, a request with two bad properties now reports whichever sorts first. Before, sharenfs was always checked last. The text is unchanged.
4. **Requirement 3 behaviour change:** CreateVolume in root mode no longer returns UNAVAILABLE for a filesystem its create left unmounted. It plans `[..., mount (OkExists), commit]` in the resume pass, and both passes run inside one CreateVolume.
   - If the mount itself fails, nothing is committed and the error is Aborted (retryable, via `resumeErr`). Covered by the test.
   - `zfs mount` does not share. That follows the source: `share_mount_one(OP_MOUNT)` calls `zfs_mount`, not `zfs_mount_and_share`. The endpoint sets `sharenfs=off` at create anyway.
   - Two existing tests pinned the old UNAVAILABLE behaviour and were rewritten:
     - `TestPlanCreateCommitsOnlyMounted`
     - `TestCreateUnmountedFilesystemIsUnavailable`, now `TestCreateUnmountedFilesystemMountsThenCommits`
   - Both failed against the old planner with the new expectations: the planner returned an empty plan plus UNAVAILABLE.
5. **Classification judgement calls (requirement 4)** worth a reviewer's eye:
   - InvalidProperty (final InvalidArgument): `missing '@' delimiter`, and other malformed-name messages if they are added.
   - Precondition: `parent is not a filesystem`, `not a cloned filesystem`.
   - Exists: `conflicting snapshot ... from parent` and `tag already exists`. An `OkExists` op would treat these as done. The endpoint never promotes or holds, so this is safe today, but future callers need to know.
   - NotFound: `no such tag`. For release, "absent" is the goal state.
   - `filesystem already mounted` → Exists, added for the mount op.
   - `TestClassify244Messages`: all 15 rows failed before.
6. **Replay cleanup on a cancelled context.** The copy's `ReplayAll` ran each sequence's cleanup on the replay context, and the test used `defer cancel()`.
   - Now the per-sequence and base cleanups run on `context.WithoutCancel` with their own 60s timeout, and `t.Cleanup(cancel)` is registered first.
   - Sequences the start budget did not reach are `not-run` and fail the test; they are not harness errors.
   - `TestReplayCleanupOutlivesItsContext` failed against the copy's cleanup ("cleanup of zfsep-test/zd/s0 ran on a dead context: context canceled").
7. **Generator change:** the generator now has a mount branch (in the slot that used to recurse when not Full) and `create -u` at 20%. Seeds no longer reproduce the wave9 survey's exact sequences.
   - Re-survey: 4,000 seeds, 1,795 mount steps (32 succeed on the model), 0 signatures beyond Known (`create-vol-resv-auto`: 161).
   - FuzzDiff ran 60s clean.
8. **Name collision:** zfsdiff's `TestExplore`, a development aid, matched nightly's `^Test(Scale|Explore)` explorer contract. It is renamed `TestTraceScript`.
9. The 3 ambiguities are unchanged (`create-vol-resv-auto`, `refquota-zero`/`recv-quota-none`, release-r order). All three are in the PR sample, so the first VM run settles the first two. `ZFSDIFF_STRICT` stays off.

## Budget decision (ADR 0005)

**Decision:** the full replay does not fit. The PR tier gets a scaled-down replay, and the full replay runs nightly.

- **Full corpus cost:** 102 sequences and about 1,272 zfs invocations, counted as 2×(steps+1) + 3 per sequence. The copy estimated 15–30s with 4 workers, mostly txg-syncing mutations serialised on one pool. This has not been measured on a pool.
- **Shard loads from `ci/real-zfs-weights.txt`**, computed with the guest's own `shard` awk:
  - HEAD today: the slowest endpoint shard is about 6.5s (the NFS group).
  - The full replay as a 15–30s test would put that shard at 15–30s or more. That is at least double, on a job whose boot and setup already use most of the 60s. It does not fit.
- **PR (`ci.yml` real-zfs job):**
  - What runs: `TestZFSDiffReplay` replays `zfsdiff.PRSample`, 21 sequences and 253 of 1,272 invocations (19.9%), so about 3–6s. Its weight is 5.00.
  - Self-enforced budget: it stops starting sequences at 12s and fails past 15s.
  - Shard loads with it: 7.26 / 7.28 / 7.27, against a maximum of about 6.5 today, so +0.8s on the critical endpoint shard by weight.
  - Sharding: no PR-sample step turns a share on (`TestPRSampleNamesCorpusSequences` checks this), so the replay does not have to sit in the NFS shard.
  - With no pool it still runs model vs sim over the whole corpus (0.1s).
  - The FuzzDiff seed corpus runs in the static job's seed step and in `go test ./...`.
- **Nightly (`nightly.yml`):**
  - `real-zfs-full` runs the same VM suite with `ZFSDIFF_FULL=1`: the whole corpus, a 150s start budget and 180s hard. The guest then groups TestZFSDiffReplay with the NFS tests (shard loads 11.49 / 5.17 / 5.15), because its share sequences rewrite `zfs.exports`.
  - It uploads `zfsdiff-report-full` and files a `needs-triage` issue on failure.
  - FuzzDiff joins the nightly fuzz matrix.
- **Reports:**
  - The guest exports `ZFSDIFF_REPORT=out/zfsdiff.log`.
  - `real-zfs-vm.sh` puts the per-sequence verdicts in the job summary and fails the leg if the report is missing.
  - `ci.yml` uploads `zfsdiff-report` with `if: always()`.
- ADR 0005 got a one-bullet note under Consequences.
- **Not verified:** no `/dev/kvm` here, so no VM run. Replace the 5.00 weight and the 15–30s estimate with the first run's numbers.

## Verification

All of these were run at the final commit:

| Check | Result |
|---|---|
| `gofmt -l .` | empty |
| `go vet ./...` | ok |
| `go test -race -count=1 ./...` | all ok |
| `go mod tidy -diff` | ok |
| `bash -n` on each `ci/*.sh` | ok |
| `actionlint` | clean |
| zfsdiff `TestCorpusModelVsSim`, `TestReplayHarnessOnSim` | ok |

`TestReplayHarnessOnSim` reports 101 agree and 1 model-bug, the Known one seen from the sim's side.
