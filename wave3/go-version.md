# Go version bump: fact sheet and options

Date of research: 2026-10-27. Project: /home/user/empty (read-only; nothing edited).

## 1. Facts table

| # | Fact | Value | Source |
|---|------|-------|--------|
| 1a | Current go.mod | `go 1.24.7`, no `toolchain` line | /home/user/empty/go.mod |
| 1b | Latest Go patch per line (from the module proxy toolchain index) | 1.27.3 (newest), 1.26.10, 1.25.14, 1.24.13 | https://proxy.golang.org/golang.org/toolchain/@v/list (go.dev/dl JSON was unreachable: CONNECT 403) |
| 1c | Go 1.26.0 release date | 2026-02-10 (go1.26.1 2026-03-05) | https://go.dev/doc/devel/release (via search snippet; page not fetchable directly) |
| 1d | Go 1.27 release | Released August 2026 ("arrives in August 2026, six months after Go 1.26"); go1.27.3 exists on the proxy | https://raw.githubusercontent.com/golang/website/master/_content/doc/go1.27.md |
| 1e | Go 1.25.0 release date | 2025-08-12 | https://go.dev/doc/devel/release (search snippet) |
| 1f | Support policy | Two most recent major releases. As of 2026-10-27: **1.27 and 1.26 supported; 1.25 and 1.24 unsupported (EOL)**. Current 1.24.7 pin is out of support. | Go release policy at https://go.dev/doc/devel/release; cross-check https://endoflife.date/go (not fetched) |
| 1g | Ubuntu 26.04 LTS golang-go | 1.26 default (1.23, 1.24, 1.25, 1.26 available) | https://ubuntu.com/developers/docs/reference/availability/go.md |
| 1h | Ubuntu 24.04 LTS golang-go | Default 1.22 (1.21, 1.22, 1.23 available). Not 1.24. Installing 1.24+ on 24.04 needs the go snap or toolchain download. | https://ubuntu.com/developers/docs/reference/availability/go.md |
| 1i | Ubuntu 26.10 golang-go | **Not confirmed.** The availability page above does not list 26.10. Ubuntu 26.10 released 2026-10-15 (7.3 kernel, GNOME 51). Treat its Go version as unknown until checked with `apt show golang-go` on a 26.10 VM. | https://phoronix.com/news/Ubuntu-26.10-Released ; availability page above |
| 1j | Go snap channels | 1.24, 1.25, 1.26 listed; `latest/edge` = 1.26 (page is stale, may not show 1.27) | https://ubuntu.com/developers/docs/reference/availability/go.md |
| 2a | TrueNAS 27 middleware Go usage | **None.** `middlewared` is Python. The clone of truenas/middleware at tag `TS-27.0.0-RC.1` has zero `.go` files and zero `go.mod` files. Nothing constrains our Go version through TrueNAS. | `git clone --branch TS-27.0.0-RC.1 https://github.com/truenas/middleware` (checked, then deleted) ; tag list via `git ls-remote --tags` |
| 2b | TrueNAS 27 tag | `TS-27.0.0-RC.1` exists | git ls-remote on github.com/truenas/middleware |
| 3a | `container-storage-interface/spec` | Ours: v1.12.0 (go 1.23.0). Latest v1.13.0 needs **go 1.25.0**. | https://proxy.golang.org/github.com/container-storage-interface/spec/@v/v1.13.0.mod |
| 3b | `kubernetes-csi/csi-test/v5` | Ours: v5.4.0 (go 1.23.0, toolchain go1.24.7). v5.5.0 needs **go 1.25.0**. Latest v5.6.0 needs **go 1.25.0**. | https://proxy.golang.org/github.com/kubernetes-csi/csi-test/v5/@v/{v5.4.0,v5.5.0,v5.6.0}.mod |
| 3c | `google.golang.org/grpc` | Ours: v1.72.0 (go 1.23). Latest v1.84.0 needs **go 1.25.0** (v1.83.2 also 1.25.0). | https://proxy.golang.org/google.golang.org/grpc/@v/v1.84.0.mod |
| 3d | `google.golang.org/protobuf` | Ours: v1.36.6. Latest v1.36.12 needs **go 1.23** (no constraint). | https://proxy.golang.org/google.golang.org/protobuf/@v/v1.36.12.mod |
| 3e | `onsi/ginkgo/v2` (test dep) | Ours: v2.22.0. Latest v2.33.1 needs **go 1.25.0**. | https://proxy.golang.org/github.com/onsi/ginkgo/v2/@v/v2.33.1.mod |
| 3f | `onsi/gomega` (test dep) | Ours: v1.36.1. Latest v1.44.0 needs **go 1.25.0**. | https://proxy.golang.org/github.com/onsi/gomega/@v/v1.44.0.mod |
| 3g | kubernetes-csi sidecars, latest | external-provisioner/v6 v6.4.0: **go 1.26.0**; external-attacher/v4 v4.13.0: **go 1.26.0**; external-resizer/v2 v2.3.0: **go 1.26.0**; external-snapshotter/v8 v8.7.0: **go 1.26.0**; csi-lib-utils v0.24.0: **go 1.26.0** (v0.25.0 exists, not checked). external-provisioner/v5 v5.2.0: go 1.23.1. | https://proxy.golang.org/<module>/@v/<ver>.mod |
| 3h | Consequence | Latest sidecars and libs need **Go 1.26.0**. Latest CSI spec, csi-test, grpc, ginkgo need **Go 1.25**. Nothing needs 1.27. | derived from 3a-3g |
| 4a | Go 1.25 runtime/tooling we already use or can use | Container-aware GOMAXPROCS (cgroup limit, Linux); `runtime/trace` flight recorder; `testing/synctest` GA; `sync.WaitGroup.Go`; new `go vet` analyzers (waitgroup); `GOEXPERIMENT`-free `synctest` | https://raw.githubusercontent.com/golang/website/master/_content/doc/go25.md (fetched from website repo) |
| 4b | Go 1.25 breaking-ish change | Nil-check ordering fix: code that uses a result before checking `err` (nil-deref) now panics. Also `go.mod` `ignore` directive; go command no longer adds `toolchain` line automatically. | go1.25 notes, same source |
| 4c | Go 1.26 changes | `go fix` rewritten on the `go vet` analysis framework (old fixers removed); goroutine leak profile experimental (`GOEXPERIMENT=goroutineleakprofile`); `crypto/subtle.WithDataIndependentTiming` no longer locks the OS thread; `crypto/rand` behaviour changes (GODEBUG `cryptocustomrand=1` to restore); `testing.B.Loop` no longer blocks inlining; `asynctimerchan` GODEBUG scheduled for removal; SHA3 zero-value fix; `GOARCH=arm` removed for some OS | https://raw.githubusercontent.com/golang/website/master/_content/doc/go26.md |
| 4d | Go 1.27 language | Generic methods (methods may declare type parameters); struct literal keys may be any field selector; function type inference generalized to assignments | https://raw.githubusercontent.com/golang/website/master/_content/doc/go27.md |
| 4e | Go 1.27 go command / vet | `go test` runs `stdversion` vet check by default (flags stdlib symbols newer than the file's `go` directive). `GODEBUG` settings removed upstream are now accepted in go.mod/`//go:debug` if they use the final default; old values fail the build. bzr no longer supported. | go27.md "Go command" section |
| 4f | Go 1.27 runtime | Tracebacks include pprof goroutine labels for modules with `go 1.27`+ (opt-out `tracebacklabels=0`); `asynctimerchan` GODEBUG removed permanently (timer channels always unbuffered); size-specialized small allocations (~1% gain, +60 KB binary; opt-out `GOEXPERIMENT=nosizespecializedmalloc`, to be removed 1.28); goroutineleak profile GA | go27.md "Runtime" section |
| 4g | Go 1.27 stdlib | `encoding/json/v2` and `jsontext` new (experimental `GOEXPERIMENT=jsonv2` in 1.25/1.26); `crypto/mldsa`; new `uuid` package; `math/rand/v2` generic `(*Rand).N`; `os.Process.WithHandle` (pidfd on Linux 5.4+); `os/signal.NotifyContext` cancels with cause; `bytes.CutLast`/`strings.CutLast`; `testing/synctest.Sleep`; Unicode 17; `syscall.Errno` on Plan 9; `go/types` alias changes | go27.md "Standard library" section |
| 4h | Relevant to us: os/exec | No os/exec-specific behaviour change found in 1.25/1.26/1.27 notes. `os.Root` is a Go 1.24 feature (already available to us). | greps of go25/go26/go27 notes (no os/exec entry) |
| 4i | Relevant to us: syscall.Flock / Umask | No change found. `syscall` change in 1.27 is Plan 9 only. | go27.md |
| 4j | Relevant to us: iter/slices | `slices` gets 1.25 "faster slices" compiler stack allocation (perf only). No API break. | go25.md |
| 4k | Relevant to us: fuzzing | No breaking change found in these notes. | greps |
| 5a | CI | `.github/workflows/ci.yml` uses `actions/setup-go` with `go-version-file: go.mod` at 3 places (lines 18, 48, 68). Bumping go.mod moves CI automatically. | /home/user/empty/.github/workflows/ci.yml |
| 5b | Ubuntu 26.10 relevance to CI | The ZFS VM runs Ubuntu 26.10 but CI's Go comes from setup-go, not apt, so 26.10's golang-go version does not gate the bump. | derived |

## 2. Options

### A. Stay on 1.24 (go 1.24.7)
- Pros: zero churn; nothing to test.
- Cons: **1.24 is out of support** (two-majors rule). No stdlib security fixes. Deps stay pinned to old releases (CSI spec 1.12, csi-test 5.4, grpc 1.72), missing newer fixes. Future bumps only get harder.
- Verdict: not recommended.

### B. Bump to 1.26 (go 1.26.0 or 1.26.10), N-1
- Pros: supported line (1.26.10 current patch). Satisfies every dep we would take today, including the latest sidecars and libs (all need 1.26.0 or less). Ubuntu 26.04 LTS golang-go is 1.26, so the LTS host matches. Lower-risk than 1.27 (one release old, more ecosystem exposure).
- Cons: 1.27 is supported too, so this is not "latest". Will need another bump in ~6 months (Go ships every six months). Misses 1.27 runtime and stdlib improvements (json/v2 GA-ish, goroutine labels, allocation speedup).
- Unlocks: CSI spec v1.13, csi-test v5.6, grpc v1.84, protobuf latest, ginkgo v2.33, sidecars.

### C. Bump to 1.27 (go 1.27.3 current patch), latest
- Pros: latest line, supported until 1.29 ships; matches the owner's "track latest" instinct and the ZFS decision. Satisfies everything in B. Gains 1.27 runtime/stdlib items. Ubuntu 26.04 LTS users fall back to the toolchain download (already how we'd get 1.24+ on 24.04 today).
- Cons: 1.27 is about 2.5 months old, so ecosystem and tool compatibility is less battle-tested. New `go test` stdversion vet default may flag code. Must verify `GODEBUG` lines in go.mod or `//go:debug` comments still accept (removed settings with old values now fail the build). Ubuntu 26.10 golang-go version is not verified, so host-level apt Go on 26.10 may be older. Nothing in our deps needs 1.27.
- Risk to check: `asynctimerchan`: only matters if the code sets it. Grep before bumping.

### Comparison
| | A: 1.24 | B: 1.26 | C: 1.27 |
|---|---|---|---|
| Supported today | No | Yes | Yes |
| Latest deps (csi spec 1.13, grpc 1.84, sidecars) | No | Yes | Yes |
| Ubuntu 26.04 LTS golang-go | No (1.22 default on 24.04) | Matches (1.26) | Via toolchain download |
| Ubuntu 26.10 golang-go | unknown | unknown | unknown |
| Effort | 0 | low | low-medium (new vet check, GODEBUG check) |
| Next forced bump | now | ~Feb 2027 (1.28) | ~Feb 2027 (1.28) and 1.29 later |

## 3. Recommendation

**Option C: bump go.mod to `go 1.27.0` (toolchain 1.27.3), in the same change that bumps CSI spec to v1.13, csi-test to v5.6, grpc to v1.84, and protobuf to latest.** Matches the ZFS decision (newest supported line), the CI change is automatic through `go-version-file`, and no dep forces a choice below 1.27 except the sidecars which are already satisfied.

Before merging:
1. `grep -rn "asynctimerchan\|go:debug\|godebug" .` to make sure no removed GODEBUG setting with an old value exists.
2. Run `go vet ./...` and `go test ./...` on 1.27.3 to catch the new stdversion default.
3. Confirm the CI setup-go step can fetch 1.27.3 (first CI run after the bump).

If any of step 1-3 fails in a way that is not a quick fix, fall back to **Option B (`go 1.26.10`)**, which still unlocks all the same deps (all of them need 1.26.0 or less), and revisit 1.27 later.

## 4. Key facts (short)
1. Supported lines today: 1.27 (latest 1.27.3) and 1.26 (1.26.10). 1.24 (we are on 1.24.7) and 1.25 are out of support.
2. Ubuntu: 26.04 golang-go = 1.26; 24.04 golang-go = 1.22; 26.10 golang-go not confirmed. TrueNAS 27 middleware has no Go code (Python), so nothing from TrueNAS constrains us.
3. Deps: CSI spec v1.13.0, csi-test v5.5+/v5.6.0, grpc v1.84.0, ginkgo v2.33 need go 1.25; latest sidecars (external-provisioner v6.4.0, attacher v4.13.0, resizer v2.3.0, snapshotter v8.7.0, csi-lib-utils v0.24.0) need go 1.26.0; protobuf v1.36.12 needs only go 1.23.
4. Go 1.27 changes that could bite us: `go test` default `stdversion` vet check; GODEBUG-removal enforcement in go.mod / `//go:debug`; `asynctimerchan` removed. Runtime/stdlib wins: goroutine labels in tracebacks, ~1% allocation speedup, json/v2, goroutine leak profile GA. No os/exec, os.Root, Flock or Umask change found in 1.25-1.27.
5. CI: `.github/workflows/ci.yml` uses `setup-go` with `go-version-file: go.mod` in 3 jobs, so changing go.mod moves CI.

## Caveats
- go.dev and golang.google.cn were not reachable from this sandbox (CONNECT 403 / DNS failure). Go versions and dep `go` directives come from proxy.golang.org; release dates come from search snippets of go.dev/doc/devel/release.
- Go 1.27 notes were fetched from the golang/website master branch, so they may differ slightly from the final published page.
- Ubuntu 26.10 golang-go version was not found in any source checked.
