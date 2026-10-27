# Wave 10: CI under one minute (workflow topology and tooling)

Branch `speed-ci` (local, not pushed), based on b5c4ec4 (ADR 0005):
49ca063 setup-go composite action, 9886a44 ci.yml, d2c4f49 nightly.yml.
The same changes are also exported as patches in `wave10/ci-patches/`.
actionlint v1.7.7 reports no findings on both workflows. Nothing here has run on GitHub yet: every
runner-side number below is an estimate from run 40456494050's logs plus local measurements, and
the first real run must confirm them.

## Measurements behind the design

Local (4 cores, Go 1.27.3, shared with other agents so noisy):

| Step | Cold | Warm cache |
|---|---|---|
| `go vet ./...` | 17s | 1.5s |
| `go test -race -count=1 ./...` | 45s | 27s |
| same with `-run XXX` (compile and link only) | n/a | 16s |

So warm `-race` tests are about 16s compile+link and 11s running; a warm cache saves about
15s of vet and about 18s of test. `internal/server` (10s) and `internal/endpoint` (6.6s) are the
slowest packages and belong to the Go-test owner. Fuzz seeds are already replayed by plain
`go test ./...`, so the PR tier needed no new seed run, only an explicit guard.

From the unit job log of run 40456494050 (runner ubuntu-24.04 image 20261012, 4 vCPU):
- Go 1.27.3 is not preinstalled. setup-go downloaded it (0.3s), extracted it (1.8s) and copied
  it into the tool cache ("Adding to the cache", 5.1s). The copy is pure waste.
- Cache restore: 465 MB, 5s. The key was `go.sum` only, and a cache hit never re-saves, so
  the blob never refreshed and `go mod tidy` still downloaded about 45 modules.
- vet 10s and test about 37s (24s of it before the first package result) show a build cache
  that is stale for the repo's own packages.
- buf: 53s was nearly all `go run` compiling buf. The prebuilt tarball is 1.4s to fetch here.

## Target per-job timeline (PR, warm cache from main)

Estimates, to be confirmed. All jobs start together; the PR is green when the slowest finishes.

| t (s) | static | test |
|---|---|---|
| 0-3 | job setup, checkout, start buf download | job setup, checkout |
| 3-11 | setup-go: Go download overlaps cache restore (about 5s), verify and extract (about 3s) | same |
| 11-12 | toolchain assert, buf install (already downloaded) | |
| 12-15 | gofmt 0.3s, tidy 2s | `go test -race` starts |
| 15-18 | vet 3s | |
| 18-21 | buf lint 2s, gen drift 3s (plugins cached) | |
| 21-30 | fuzz seed replay about 8s | tests end about 11+27 = 38-45 |
| end | about 30s | about 45s |

Critical path on a PR is `test` at about 45s, with static about 30s. The real-ZFS VM job
(111s today, 81s of it in the script) is the only job over budget; it belongs to the VM-script
owner. The ADR's 60s cannot be met until that job drops by about 50s (warm image, cached qemu,
overlapped Go build). The workflow pieces I own for that job are covered below.

## Changes ranked by seconds saved on the critical path

1. Fuzz moved out of the PR tier, 30s each x 4 jobs. Removes the ~55s fuzz jobs. PR keeps the
   seed-corpus replay (static job, about 8s, off the critical path). Saves about 25s on wall
   clock versus today's longest non-VM job, and removes 4 runners.
2. buf prebuilt, sha256-pinned instead of `go run`: about 50s (53s lint becomes 2s) on the
   proto job, which was the second-longest job at 75s.
3. Warm build cache saved from main, restored by PRs (`actions/cache/restore` plus
   `actions/cache/save` on pushes to main only): about 8s on vet and about 15-18s on test
   compile.
4. Own Go install instead of `actions/setup-go`: about 7s per job (no tool-cache copy, download
   overlapped with the cache restore). Also drops the per-job 465 MB re-save check. Applies to
   all three jobs, including the VM job.
5. Merging jobs: unit + proto + fuzz x4 (6 jobs) become static + test (2 jobs). Each removed job
   saved about 3s of runner start and checkout, and about 8s of duplicated setup, and frees
   concurrency. vet and the test compile now run in separate jobs, so they no longer serialise
   (about 10s saved versus the old single unit job).
6. Concurrency and timeouts: unchanged `cancel-in-progress` for PRs (already present),
   timeouts cut from 15/10/6 minutes to 5 for the fast jobs, so a hang costs 5 minutes, not 15.
   Saves nothing on a green run.

Rejected or not done:
- Path filter to skip proto when `api/` and `gen/` are untouched: not done. The proto work is
  now about 5s inside the static job, off the critical path, and a filter adds a required-check
  workaround (a skipped job must still report) plus a dependency on go.mod tool versions.
- Larger runners: not free for private repos, and unverifiable from here. 4 vCPU ubuntu-24.04
  stays; `ubuntu-latest` rejected because the pinned image name keeps results reproducible.
- Preinstalled Go: the 20261012 image has no 1.27.3 (log shows the download).
- Caching the Go SDK in the actions cache: no better than the 0.3s download from go-versions.
- Sharding the test job in two: the 16s compile+link is per package and 4 cores already run
  packages in parallel. Revisit if `internal/server` stays at 10s after the test owner's work.

## Validity checks: where each went and why it is still enforced

| Check | Before | Now | Enforced because |
|---|---|---|---|
| gofmt | unit | static | same command, fails the job |
| `go mod tidy -diff` | unit | static | same command; the warm module cache removes the downloads |
| vet | unit | static | same command, `go vet ./...` |
| buf lint | proto, `go run buf@v1.47.2` | static, release binary v1.47.2, sha256 checked with `sha256sum -c` | same version, same command; a wrong hash fails the job |
| gen drift | proto | static | same four lines: `buf generate`, gofmt on `gen`, `git diff --exit-code`, no untracked files |
| fuzz seed corpus | implicit in `go test` and 30s fuzz | explicit step in static: runs `-run '^Fuzz'` on every package that defines one, and fails unless every `func Fuzz*` found by grep printed `--- PASS` | catches a Fuzz target that is renamed, skipped or not replayed |
| exploratory fuzz | fuzz x4 on every PR | nightly.yml | ADR 0005 tier; a failure files an issue with the reproducer, which becomes a seed so PRs replay it |
| simulator tests under `-race` | unit | test | same command |
| toolchain equals go.mod | `GOTOOLCHAIN=local` only | static asserts `go env GOVERSION` equals go.mod's `toolchain` line, and `GOTOOLCHAIN=local` stays | the new SDK pin cannot silently drift from go.mod (new check) |
| skip budget, 2.4 kmod/userland assert, test-list coverage, Ginkgo spec floor | `ci/real-zfs-vm.sh`, `ci/check-skips.sh` | unchanged | I did not touch the script or the `test` step; the real-zfs job still runs it |
| actions pinned by SHA | yes | yes (checkout, upload-artifact, `actions/cache` v4.2.3 = 5a3ec84e); the local composite action is in-repo | |

Pinned downloads (not SHA-pinned actions): Go tarball from actions/go-versions release
1.27.3-39437817176, sha256 d0fbe16a...19fd6 (I downloaded it and computed the hash); buf
v1.47.2 `buf-Linux-x86_64.tar.gz` sha256 39716cfe...b9c116 (matches the release's sha256.txt and
my own download). Bump procedure is in comments in `action.yml` and `ci.yml`.

## What the real-zfs job changed (workflow only)

Only two edits in that job: `actions/setup-go` replaced by the local composite action (cache
namespace `real-zfs`), and a main-only cache save step. Not touched: kvm enable, qemu install
(13s, a candidate for the VM owner's cached-qemu work), the script call and the log upload. If
the VM owner also edits `ci.yml`, expect a trivial conflict on those steps.

## Cache design

- Namespaces `static`, `test`, `real-zfs`, `nightly-fuzz`, `nightly-explore`, because vet,
  `-race`, `CGO_ENABLED=0` and fuzz builds share little compiled output.
- Key `go1.27.3-<ns>-<hash go.sum>-<sha>`, restore-keys fall back to the same go.sum, then to the
  newest of the namespace. PRs can read main's caches (default-branch caches are visible to PR
  refs); only pushes to main save, so PRs pay no upload and no PR-scoped cache accumulates.
- The first main push after merge builds cold (about 45s test) and seeds the caches. The first PR
  before that has no cache and runs about 25s slower.
- Risk: GitHub evicts caches over 10 GB and after 7 days unused; a quiet repo goes cold weekly.
  The nightly run touches only its own namespaces, so it does not keep the PR ones warm. If this
  matters, add a nightly `cache-name: static`/`test` warm-up that saves.

## Nightly workflow design (`.github/workflows/nightly.yml`)

- Triggers: `schedule` 03:17 UTC daily and `workflow_dispatch` with a `fuzztime` input
  (default 5m). One run at a time (`concurrency: nightly`, no cancel).
- Job `fuzz`: matrix of the four targets, `-fuzztime 5m -fuzzminimizetime 30s`, 20 minute
  timeout. On failure: collect the untracked `*/testdata/fuzz/*` files, upload them as the
  artifact `fuzz-repro-<Target>` (30 days), and `gh issue create` (permission `issues: write`,
  label `needs-triage`) with each reproducer file inline.
- Job `explore`: `go test -tags nightly -run '^Test(Scale|Explore)' -timeout 25m ./...`.
  Contract for the Go-test owner: scale tests and the FSM explorer carry `//go:build nightly`
  and are named `TestScale*` or `TestExplore*`. Until they exist the job runs nothing and
  passes. On failure it files an issue with the failing lines.
- Real-pool mutant samples (ADR nightly list) need the VM and `ci/real-zfs-vm.sh`; not
  wired. Add a third job calling the script once its owner exposes a mode for it.
- Deviation from ADR 0005: the ADR says a nightly failure "adds a corpus entry". The workflow
  cannot safely push to main, so it files the issue with the reproducer inline and a human or
  agent commits the seed. A bot PR (needs `contents: write` and `pull-requests: write`) would
  automate it; the ADR text should say "files an issue and the reproducer is committed as a
  seed" or accept the PR option.

## Notes for the ADR

- The 60s budget is met for `static` and `test` by estimate. The VM job decides the total.
- "Few jobs sized to the critical path" is done: two fast jobs plus the VM.
- The ADR lists "tools are fetched, not compiled" for buf. The protoc plugins are still compiled
  through `go tool` (about 5-7s cold, about 1s with the build cache); they must stay go.mod
  tools so they track the protobuf runtime version.
- Unmeasured: actual runner times for the new jobs, cache restore size and time per namespace,
  and whether the `setsid nohup` background downloads behave on the hosted runner (they should;
  the runner only kills orphans at job end). If a download step ever fails, the install step
  errors with "did not finish" after 60s rather than hanging.

## Files

- `.github/actions/setup-go/action.yml` (new)
- `.github/workflows/ci.yml` (rewritten: static, test, real-zfs)
- `.github/workflows/nightly.yml` (new)
- Patches: `wave10/ci-patches/0001-ci-workflow-static-test-jobs.patch`,
  `0002-setup-go-composite-action.patch`, `0003-nightly-workflow.patch` (applied in order 2, 1, 3
  on b5c4ec4; they were produced before the commits and apply cleanly to a clean checkout).
