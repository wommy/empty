# Nightly CI check at HEAD 4a63496 (read-only)

## Run history (GitHub MCP)

- nightly.yml: 0 runs ever. cache-warm.yml: 0 runs ever. All 79 runs in the repo are ci.yml (pull_request, PR #1); the latest, 40537458909 at 4a63496, is green (endpoint 0/2 and 1/2, csi: about 40s each).
- Why: `origin/main` is still `2dff442 Initial empty commit`. The three workflows exist only on `claude/gallant-johnson-1qaxp5`. `schedule` and `workflow_dispatch` only fire for workflows on the default branch, so neither workflow can run, or be dispatched, until PR #1 merges.
- So there are no nightly or cache-warm logs to read. No nightly has run since any of today's changes, and no nightly has ever run at all.
- Also: no cache entry exists in the default branch's scope yet, because cache-warm has never saved one. Every PR run so far has warmed itself through PR-scoped saves.

## Untested end to end (nothing in nightly has ever executed)

- fuzz matrix (9 targets, 5m each)
- real-zfs-full endpoint: part 0/1, 3 shards, ZFSDIFF_FULL=1 + ZFS_CRASH_FULL=1, check-split with n=3 and the propRTRows pin
- real-zfs-full csi with ZFSVM_INITRD=1. This is the GRUB/initrd boot of a v5-slimmed guest, which no run anywhere has done: the cold path boots the image through GRUB *before* slimming, and every warm boot is a direct kernel boot. Within that boot, also untested: the login-prompt ssh gate, an unmasked boot (zfs-import-*, udev-settle, modules-load), and the kernel.release check.
- explore job: `^TestExplore` + ci/check-explore-ran.sh
- every "file issue" step
- cache-warm: provisioning with ZFSVM_PROVISION_ONLY, deleting superseded entries, the go namespace builds

## Static verification done locally (no repo changes)

- `ci/check-suites.sh` passes. It accepts nightly's `suite: [endpoint, csi]` matrix.
- Nightly omits ZFSVM_PART, so real-zfs-vm.sh defaults to `0/1` (per=3). The guest gets ZFSVM_EP_PART=0/1 and PROPRT_SHARD=i/3.
- I built endpoint.test, ran `ZFSDIFF_FULL=1 ci/real-zfs-plan.sh list weights 1 3`, and ran check-split's plan half on the result. Output: "each of 212 listed tests in exactly one of 3 shards". TestZFSDiffReplay goes to shard 0 with the NFS group; the crash sweep goes to shard 1.
- check-split recomputes the plan from the *environment's* ZFSDIFF_FULL (real-zfs-plan.sh:37). Without it, the plan mismatches. It works in nightly because one step runs real-zfs-vm.sh with both the plan and check-split under the same env. This is fragile but correct.
- Cache keys: nightly uses the same `.github/actions/real-zfs-keys` and `setup-go` (namespace real-zfs-<suite>) as ci.yml and cache-warm.yml, so the keys match by construction (zfsvm-v5). A miss means a cold provision (~115s) inside a 30m job, with no save. That is acceptable.
- `go test -tags nightly -run '^$' ./...` compiles. There are 3 TestExplore* functions (TestExploreLifecycle, TestExploreModelSweep, TestExploreModelMasksStillBite), all `//go:build nightly`.
- All 9 fuzz targets exist at the listed packages.
- expected-skips: TestOwnershipDifferentialCrashes uses requireSim, so it still skips under ZFS_CRASH_FULL. No test changes its skip behaviour under the FULL flags.

## Issues

### 1. Nightly cannot run before merge (blocker for "is it tested")
- File: .github/workflows/nightly.yml:7-14 (and cache-warm.yml:22-25)
- Failure scenario: no scheduled run and no dispatch while main is empty, so everything above is untested.
- Fix: after PR #1 merges, dispatch cache-warm once, then dispatch nightly with `fuzztime: 30s` for a quick end-to-end smoke test.

### 2. `needs-triage` label does not exist in wommy/empty (real bug)
- File: nightly.yml:93, 213, 251 (`gh issue create ... --label needs-triage`)
- Checked: `get_label needs-triage` returns "not found".
- Failure scenario: any nightly failure reaches "file issue". `gh issue create` aborts with "could not add label: 'needs-triage' not found", so no issue is filed and the reproducer report is lost (the artifact remains).
- Fix: create the five canonical triage labels in the repo (AGENTS.md / docs/agents/triage-labels.md), or drop `--label` / add `|| gh issue create` without the label.

### 3. Per-shard `-test.timeout=4m` vs the full-replay budget (latent)
- File: ci/real-zfs-guest.sh:68 (`t=(-test.count=1 -test.v -test.timeout=4m)`), internal/endpoint/zfsdiff_replay_test.go:56-62 (nightly: startBudget 150s, budget 180s, ctx budget+30s = 210s)
- Failure scenario: under ZFSDIFF_FULL, shard 0 runs TestZFSDiffReplay *serially with* the NFS tests (TestFileExport*, TestNFSReplay) and about 25 weight-seconds of other tests. If the replay runs toward its own budget, the shard binary hits 4m first and panics "test timed out". That kills every shard-0 result, skips t.Cleanup (pool destroy fails), and gives check-split/check-skips "reported no result" noise instead of the replay's clean budget failure. The crash full sweep in shard 1 adds load too.
- Scaling from the PR sample (21 sequences, 5.85-9.9s), the full replay is about 30-50s, so a pass is likely. It is unmeasured.
- Fix: in real-zfs-guest.sh, after line 68, add `[ -z "${ZFSDIFF_FULL:-}${ZFS_CRASH_FULL:-}" ] || t[2]=-test.timeout=12m` (the job is 30m).

### 4. Nightly artifact lacks the shard plan and lists (debuggability)
- File: nightly.yml:189-193
- ci.yml:280-286 uploads `out/*.list`, `out/*.plan`, `out/load.txt` and `out/vmstat.txt`. Nightly uploads only `out/*.log`, `test-times.tsv` and the console. A nightly check-split failure cannot be reproduced from the artifact.
- Fix: add `${{ runner.temp }}/zfsvm/out/*.plan` and `${{ runner.temp }}/zfsvm/out/*.list` (and `user-data` for a boot failure, as ci.yml's failure upload does).

### 5. Nightly plan balanced with PR weights (timing only)
- File: real-zfs-plan.sh:36-37, real-zfs-weights.txt
- Shard 0 carries NFS plus the full replay, and shard 1 the full crash sweep. Weights come from PR runs, and weights.sh rightly drops mode=full rows. This changes balance only, never coverage; see issue 3 for the timeout consequence.

### 6. Fuzz matrix is not checked against the repo's Fuzz targets (coverage gap, pre-existing)
- File: nightly.yml:40-53
- FuzzModel (internal/endpoint/modelrun_test.go:1305) and FuzzSimulateDestroyAllEquivalent (internal/endpoint/simulate_test.go:91) are never fuzzed nightly. ci.yml's static job does replay their seeds.
- Fix: add them to the matrix, or add a static check (like check-suites) that the matrix lists every `func Fuzz*`.

## Not issues (checked)
- ZFSVM_PHASE split: nightly runs phase `all`, which works. Setup-go runs before the boot, which is slower but fine.
- The login-prompt gate falls back to the old poll after 60s if no prompt appears, so a GRUB console without a prompt costs at most 60s, not a failure.
- The ci.yml artifact rename to `real-zfs-report-*` does not affect nightly, which has its own `zfsdiff-report-full-*` name. real-zfs-weights.sh reads only `real-zfs-report-endpoint-*` and skips mode=full.
- check-explore-ran.sh greps `.` recursively. Locally that includes the untracked `.claude/worktrees`, but those are not in a CI checkout.
