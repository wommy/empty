# Wave 13: integrating the wave9 NFS verifier fixes

The local branch is `fixes-nfs-verifier` in the worktree `/home/user/empty/.claude/worktrees/agent-a9d51a288d407fe53`. It is based on `origin/claude/gallant-johnson-1qaxp5` at 4c1386e. Nothing has been pushed. All Go commands ran with `GOTOOLCHAIN=go1.27.3`.

## Commits (oldest first)

| Commit | Subject | Files |
|---|---|---|
| 507a8dd | nfsref: reference models of libshare, exportfs and mountd | `internal/nfsref/*`, taken as is from wave9. `TestClientGrammarBareVsCIDR` now calls `t.Parallel()`. |
| 2eeb36a | zfstest: share and export through the nfsref models | `internal/zfs/zfstest/sim.go`. HEAD's endpoint tests pass on it unchanged; this was checked before the verifier commit. |
| c91c2ec | endpoint: export verifier fails closed (wave9 nfs-diff D1-D3) | `export.go` and `endpoint.go` (the wave9 fix as is), plus new and changed tests in `export_test.go` and `export_contract_test.go` |
| 84cd862 | endpoint: differential fuzz targets for the NFS export chain | `nfs_diff_test.go`; the four `FuzzNFS*` targets added to the `.github/workflows/nightly.yml` fuzz matrix |
| 2cc15e1 | endpoint: replay a sharenfs corpus through the real nfsd in the VM | `nfs_replay_test.go`, `ci/real-zfs-guest.sh`, `ci/real-zfs-vm.sh`, `ci/real-zfs-weights.txt` |
| 91ed5e1 | endpoint: D3 tests pin the invariant, not today's ownership model | Test-only follow-up to c91c2ec, for ADR 0004 (see anomaly 5) |

Total: 17 files, +3641/-81. Nothing under `internal/csi` changed.

### Tests added beyond wave9

All of these are at the lowest level that proves the behaviour (ADR 0005):

- **Pure planner and verifier tests** (`internal/endpoint/export_test.go`):
  - `TestEtabServesPrecedence` (new, `t.Parallel`) is a table of 23 cases for D2:
    - an fqdn line, or the client's bare address, beats our subnet line;
    - the first overlapping subnet line wins, including the `/024` and dotted-mask spellings;
    - a per-flavor `ro,sec=sys,rw` line;
    - `no_root_squash`, `insecure`, `anonuid=0`, `crossmnt`, `sec=krb5`, `sec=sys:krb5` and an unknown option;
    - negative controls: a later subnet, `*`, a netgroup, a gss line, and narrowing options.
  - `TestPlanPublishExport` (now `t.Parallel`) gains:
    - for D1: `crossmnt ancestor`, `crossmnt root`, and `ancestor without crossmnt` (also covering sibling and descendant crossmnt lines);
    - for D2: `served with no_root_squash: reshare` and `served to more clients: reshare`.
  - `TestPlanUnpublishExport` (now `t.Parallel`) gains, for D3:
    - `our sharenfs widened to *: rewritten`;
    - `inherited world export after the last client: pinned off`;
    - `foreign sharenfs to * is not ours to revoke`, which guards the deliberate exemption.
- **Contract tests** (`internal/endpoint/export_contract_test.go`). Both run on the simulator **and** on the real VM, so they have no expected-skips entry:
  - `TestFileExportUnpublishStillServed` (D3):
    - a local `sharenfs=rw=*` edit: Unpublish either fails with FailedPrecondition or leaves `*` no longer served;
    - a parent with `sharenfs=on`: Unpublish revokes its own line, then returns FailedPrecondition naming the crossmnt;
    - once the parent is off, Unpublish succeeds.
  - `TestFileExportServedBeyondRequest` (D1, the re-check after apply): an `EtabReader` wrapper adds a foreign `/etc/exports`-style line for the volume's path. Publish returns FailedPrecondition, then succeeds once the line is gone.
- `TestFileExportAdoptedVolumes` is wave9's update: a parent with "on" is refused; a parent with `rw=@10.0.0.0/8` is accepted.
- `TestNFSReplayOnSim` now calls `t.Parallel()`.

## Revert checks

The scripts are in `/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave13/`:

- `revert.py` and `revert-check.sh` revert one fix on top of the fixed tree. Each replacement must match exactly once.
- `head-check.sh` runs the branch's tests against HEAD's `export.go` and `endpoint.go`.

The logs are `revert-D1.log`, `revert-D2.log`, `revert-D3.log`, `revert-D1e.log`, `revert-D3e.log` and `revert-head.log` in the same directory. With all fixes in place, every run passes (`ok internal/endpoint`).

| Reverted | What is disabled | Failing tests |
|---|---|---|
| **D1** | The crossmnt-ancestor refusal in `PlanPublishExport`, and the exposure re-check after apply in `PublishFileExport` | `TestFileExportAdoptedVolumes`, `TestFileExportServedBeyondRequest`, `TestPlanPublishExport/crossmnt_ancestor`, `TestPlanPublishExport/crossmnt_root` |
| D1, after-apply part only | The re-check in `endpoint.go` only | `TestFileExportServedBeyondRequest` |
| **D2** | `serves()` back to HEAD's exact-Lookup form, and `etabOptions` never broad | `TestEtabServesPrecedence` (all precedence and option cases), `TestPlanPublishExport/served_with_no_root_squash:_reshare` |
| **D3** | The `covers()` liveness in `PlanUnpublishExport`, and the re-check after apply in `UnpublishFileExport` | `TestFileExportUnpublishStillServed`, `TestPlanUnpublishExport/our_sharenfs_widened_to_*`, `TestPlanUnpublishExport/inherited_world_export_after_the_last_client`, and `FuzzNFSClassifier` seeds #0, #2, #4, #10, #12, #14 |
| D3, after-apply part only | The re-check in `endpoint.go` only | `TestFileExportUnpublishStillServed` (the crossmnt half) |
| All three (HEAD 4c1386e's verifier) | Everything above | Adopted, UnpublishStillServed, ServedBeyondRequest, EtabServesPrecedence, PlanUnpublish (2 cases), PlanPublish (4 cases) |

## Verification on the final tree (91ed5e1)

All of the following pass:

- `gofmt -l .` prints nothing.
- `go vet ./...`.
- `go mod tidy -diff`.
- `go test -race -count=1 ./...`.
- `bash -n` on each of `ci/*.sh`.
- `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7`.

**Fuzzing.** Each target ran for 20s (`wave13/fuzz.log`). There were no failures and no new `testdata` files.

| Target | Executions in 20s |
|---|---|
| `FuzzNFSRoundTrip` | 48k |
| `FuzzNFSClassifier` | 75k |
| `FuzzNFSVerifier` | 67k |
| `FuzzNFSEtabParse` | 183k |

**Shard plan check** (`wave13/shardcheck.sh`). It builds `endpoint.test`, plans it with the guest's own `shard()` function, and then runs `ci/check-skips.sh` on a synthetic VM log: every test passes except the vm/endpoint entries in `expected-skips.txt`, which skip.

- 120 tests are listed and 120 planned, with no duplicates.
- The NFS group (`TestNFSReplay` and all `TestFileExport*`) is entirely in shard 0.
- Estimated shard loads: shard 0 is 6.49s, shards 1 and 2 are 4.51s each.
- The skip budget is 17 skipped of 17 allowed, and the check passes.

## CI wiring

- **`ci/real-zfs-guest.sh`:**
  - The grouping regex is now `^(TestFileExport|TestNFSReplay$)`, so the replay shares a shard with the file-export tests.
  - It sets `export NFS_REPLAY_REPORT=$out/nfs-replay.log`, so the report travels back with the shard logs. The wave9 hook expected `~/nfs-replay.log`, which the sharded runner never copies.
- **`ci/real-zfs-vm.sh`:**
  - After the `out/*` scp, it adds `$o/nfs-replay.log` to the job summary in a `<details>` block whose summary line is the report's last line, the tally.
  - If the log is missing, it emits `::error` and fails the leg.
- **`ci/real-zfs-weights.txt`:** estimated weights, labelled as estimates in a comment block:
  - `TestNFSReplay` 5.00;
  - `TestFileExportUnpublishStillServed` 0.30;
  - `TestFileExportServedBeyondRequest` 0.20.
- **`.github/workflows/nightly.yml`:** the four `FuzzNFS*` targets are in the fuzz matrix. `ci.yml` is unchanged: the `static` job replays every Fuzz target's seeds by discovery.

## Expected-skips changes: none

- No new or changed test calls `h.Sim(t)`. The new contract tests use `setup` plus `h.NFS(t)` and run on both legs.
- `TestNFSReplay` skips only without `ZFS_TEST_POOL` or nfsd. With `ZFS_REQUIRE_REAL` set it fails rather than skips, and the VM has both a pool and nfsd, so it must run and is deliberately not listed.
- `TestNFSReplayOnSim` and the `FuzzNFS*` seeds never skip.
- `TestExportfsMatchesReal` lives in `internal/nfsref`, a package the VM leg does not run.

## Anomalies

1. **Base mismatch.** The brief says the wave9 diff was made against 8cc52ef. The report header and the hunk offsets show the work copy was based on 1603dad, which is older. `sim.go` and `export.go` are identical between 1603dad and 4c1386e. The drift in `endpoint.go` (`ServedVolume`, `committedAt`) does not touch the NFS paths. Everything applied cleanly, and the result passes.
2. **`TestNFSReplay` has never run against a real nfsd.** wave9 only ran `TestNFSReplayOnSim`, and I had no ZFS, nfsd or VM here and may not push. Its first VM run will be its first real execution, and it is a hard gate:
   - it fails on any divergence for a canonical value, and on any unsound verifier verdict;
   - the listed ambiguities are recorded but have never been observed on a real host: glibc `(null)`, the getaddrinfo canonical name, DNS for `node-a.invalid`, and the export_features mask.

   Expect to iterate on the first VM run.
3. **Time budget (ADR 0005).** The replay's own guard is 25s (it stops) and 30s (it fails), against a one-minute PR budget. The 5s weight is a guess (about 46 `zfs set` calls, each running `exportfs -ra`).
   - Even at 5s, shard 0 becomes the VM critical path: 6.5s against 4.5s for the others.
   - A slow DNS answer for `.invalid` inside the guest could add up to about 20s.
   - Measure it on the first run, refresh the weights, and consider lowering the in-test cap.
4. **Shard isolation is now load-bearing.**
   - The replay writes malformed `zfs.exports` lines (a quote, `sec` with no value, a bad `anonuid`). Real exportfs stops reading the file at such a line, which drops every other dataset's exports.
   - Run concurrently in another shard, it would break `TestFileExport*` there; the wave9 hook predates sharding and did not account for this.
   - Grouping fixes it, but any future NFS test that is not named `TestFileExport*` would race. The comment in the guest script says this.
5. **Tension with ADR 0004.**
   - The wave9 fix (`publishWant`, `unpublishWant`, D3's "ours" test) is computed from the `nfs_clients` stored list, which ADR 0004 removes in a follow-up.
   - Rewriting an admin's local `rw=*` (D3) is HEAD's existing "drifted sharenfs is rewritten" behaviour. Under ADR 0004 such a value is foreign and must never be written.
   - I kept the code as wave9 wrote it. Commit 91ed5e1 makes the contract test assert only the invariant: fail, or no longer served. The planner case that pins the rewrite is commented to flip in the follow-up.
   - `docs/research/gaps/csi-nfs-design.md` §9.1 still says an `/etc/exports` line or ancestor crossmnt is "Reported only". Publish and Unpublish now refuse with FailedPrecondition. The docs were not edited.
6. **A refused Publish leaves its own line applied.** When the re-check after apply finds a foreign line on the same path, Publish returns FailedPrecondition but the requested client's `sharenfs` is already set and served. Nothing is wider than requested on our part, and a retry converges.
7. **Unpublish retries churn exportfs.** Under a crossmnt ancestor, each Unpublish retry re-sets `sharenfs=off` (reshare), and so re-runs `exportfs -ra`, until the ancestor stops.
8. **The fuzz seeds catch only D3.** With D1 or D2 reverted alone, every `FuzzNFS*` seed still passes; only D3 trips the `FuzzNFSClassifier` seeds. wave9's "their seeds fail on HEAD" holds only in combination, and the seeds do not compile against HEAD's API anyway. D1 and D2 are now killed by the explicit tests above.
9. **Path handling is inconsistent.**
   - `PlanPublishExport` checks `crossmntAncestors` and `serves` against `row.Mount`, while the re-checks after apply use `realPath(row.Mount)`.
   - On a symlinked mountpoint, the planner's crossmnt refusal can miss, and the re-check after apply catches it instead (fail-closed, but later).
10. **Model validation is not continuous.** `TestExportfsMatchesReal` (the model against a real nfs-utils 3.1.1 build) always skips in CI, so the models are re-validated only through the replay's narrower corpus.
11. **Upstream exportfs bug, from wave9, not ours.** nfs-utils 3.1.1 `exportfs -ra` aborts with a double free on `-squash_uids=... host1 host2(...)`. Only an admin's `/etc/exports` can produce that input, and the endpoint fails closed on it.
