# Go 1.27 testing and CI features for wommy/empty

Toolchain: `GOTOOLCHAIN=go1.27.3` (go version go1.27.3 linux/amd64). Repo untouched (read-only).

Sources and their status:
- `go doc`, `go help test`, `go help testflag`, `go tool test2json -h`, `go tool covdata` help: read locally, verified.
- Probes in `/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave7/probe` (scratch module, outside the repo): verified behaviour.
- Go release notes (go.dev/doc/go1.24 .. go1.27): NOT fetched. The egress proxy returns 403 (policy denial) for go.dev, and the proxy README says not to route around policy denials. The toolchain has no `doc/` directory with release notes. Version numbers below marked "unverified" come from memory and must be checked against the release notes.

---

## 1. testing/synctest

Verified (`go doc testing/synctest`): package is importable with no GOEXPERIMENT gate in the source (`$GOROOT/src/testing/synctest/synctest.go` has no build tag). API:

```
func Sleep(d time.Duration)
func Test(t *testing.T, f func(*testing.T))
func Wait()
```

Doc excerpts:
- "The Test function runs a function in an isolated "bubble"."
- "Within a bubble, the time package uses a fake clock... The initial time is midnight UTC 2000-01-01."
- "Operations not in the above list are not durably blocking. In particular, the following operations may block a goroutine, but are not durably blocking because the goroutine can be unblocked by an event occurring outside its bubble: locking a sync.Mutex or sync.RWMutex; blocking on I/O, such as reading from a network socket; system calls"
- Durably blocking: channel ops on bubble-created channels, select over only bubble channels, sync.Cond.Wait, WaitGroup.Wait (when Add was called in the bubble), time.Sleep.
- "Avoid interacting with external processes."
- "Operating on a bubbled channel, timer, or ticker from outside the bubble panics."

Stability: no experiment flag in 1.27. The "became stable in 1.25" timeline is unverified (release notes not reachable).

Answer for this repo: no, do not use it for tests that exec external processes (ZFS, nfs, csi sanity). Waiting on a child process is a system call and blocking I/O, so it is not durably blocked and the bubble's fake clock will not advance past it. Real exec inside a bubble will hang or give wrong timing. Use synctest only for pure-Go concurrency with fake time (retry/backoff loops, context timeouts, goroutine handoffs). For process waits keep real deadlines via `t.Context()` and `exec.CommandContext`.

## 2. t.Context, t.Chdir, b.Loop, t.Output, t.Attr

Verified in `go doc` on 1.27.3:
- `func (c *T) Context() context.Context`: "canceled just before Cleanup-registered functions are called." Useful: pass to `exec.CommandContext` so children are killed before cleanup waits on them. Use it instead of `context.Background()` in tests.
- `func (t *T) Chdir(dir string)`: "calls os.Chdir and uses Cleanup to restore..."; "cannot be used in parallel tests or tests with parallel ancestors." Process-wide. Not useful here unless a test needs a cwd; prefer passing dirs explicitly.
- `func (c *T) Output() io.Writer`: writes to the same stream as TB.Log. Marginal.
- `func (c *T) Attr(key, value string)`: "Attr emits a test attribute associated with this test." Probe output: `=== ATTR  TestOK kind x`. It is emitted in -v output and is intended for CI consumers. Possibly useful to tag the VM/simulator leg.
- `func (b *B) Loop() bool`: benchmarks only. Not relevant to the test binaries here.

Versions (unverified; from memory): t.Context and t.Chdir and b.Loop are 1.24; t.Output and t.Attr may be newer than 1.24. Confirm in release notes before citing a version.

## 3. test2json on a compiled binary

Verified. The raw `-test.v=test2json` stream is NOT JSON. It is framed output that must be decoded. Probe result:

```
./x.test -test.v=test2json | go tool test2json -t
```

Run in the VM only as `./x.test -test.v=test2json > x.raw`, then convert on the runner (the VM may not have Go installed):

```
go tool test2json -t < x.raw > x.json
```

`go tool test2json -h` usage: `go tool test2json [-p pkg] [-t] [./pkg.test -test.v=test2json]`. The `-p` flag sets the Package field. `-t` adds Time.

Event schema (from `go doc cmd/test2json`):

```
type TestEvent struct {
	Time        time.Time // encodes as an RFC3339-format string
	Action      string
	Package     string
	Test        string
	Elapsed     float64 // seconds
	Output      string
	OutputType  string
	FailedBuild string
}
```

Actions: start, run, pause, cont, pass, bench, fail, output, skip. Probe output included `{"Action":"skip","Test":"TestSkip",...}` for t.Skip and `{"Action":"output","Test":"TestOK","Output":"=== RUN   TestOK\n","OutputType":"frame"}`.

Replacement for the grep in ci/check-skips.sh: select `Action == "skip"` and `Test` from the JSON. Keep the `--- PASS:` check as `Action == "pass"`. The existing script's `--- SKIP:` regex only works on the text format; with JSON it is exact. Note that test2json output for a top-level skip has no subtest-prefix issue, so the allowed-skip logic (`TestX/...` covered by `TestX`) still needs the same prefix match in the new script.

Not verified: how the framed output behaves if the binary is killed by the VM timeout (`-test.timeout=8m`). Test with a deliberately hung test before relying on it.

## 4. Integration coverage (-cover, -coverpkg, VM profile merge)

Verified build and run:

```
CGO_ENABLED=0 go test -c -cover -coverpkg=./... -o endpoint.test ./internal/endpoint
./endpoint.test -test.gocoverdir=$WORK/cov/vm  -test.v=test2json ...
# or, textfmt-only:
./endpoint.test -test.coverprofile=$WORK/vm.cover.out
```

Findings:
- The GOCOVERDIR environment variable does NOT make test binaries write data. A probe with `GOCOVERDIR=dir ./cov.test` printed "warning: GOCOVERDIR not set, no coverage data emitted". Its documented use (`go help environment`) is for binaries built with `go build -cover`.
- Test binaries use `-test.gocoverdir DIR` (the flag is listed in the binary's `-h` output; it is not in `go help testflag`). This writes covdata dirs.
- `-test.coverprofile` writes textfmt (mode set) "after all tests have passed" (`go help testflag`). A failing VM run may write nothing, so prefer `-test.gocoverdir` for the VM leg.
- `go tool covdata` reads covdata dirs only. It cannot read textfmt files. So the unit job should also emit covdata dirs.

Merge commands (verified):

```
mkdir -p merged
go tool covdata merge -i=$WORK/cov/vm,$WORK/cov/unit -o merged
go tool covdata textfmt -i=merged -o merged.out
go tool covdata percent -i=merged
```

Probe: `percent` on vm, unit and merged dirs each reported 50.0% (same test in both runs), so the merge mechanics work. This does not prove the numbers add up for real code; check with a real package.

Reporting separately: keep `$WORK/cov/vm` and `$WORK/cov/unit` as separate inputs, and run `go tool covdata percent -i=...` per leg. Report the simulator and real-ZFS legs as two artifacts, and the merged one as a third.

Caveat: `-coverpkg=./...` must be the same in the unit and VM builds for the merge to line up.

## 5. Fuzzing in CI

Verified from `go doc testing` and `go help testflag`:
- Seed corpus: "by storing files in the directory testdata/fuzz/<Name> (where <Name> is the name of the fuzz test) within the package containing the fuzz test." Commit these to git.
- Failing inputs: "the fuzzing engine writes the file to the fuzz cache directory within the build cache instead" when testdata is read-only. So the in-repo testdata is written on failure, and the cache is a fallback.
- Build cache location: `go env GOCACHE` (here /root/.cache/go-build). The fuzz cache is inside it.
- `-fuzzminimizetime t`: "Run enough iterations of the fuzz target during each minimization attempt to take t ... The default is 60s." Also accepts `Nx`. Setting a short value (for example `10s` or `100x`) bounds minimization time on CI.
- `-parallel n`: "While fuzzing, the value of this flag is the maximum number of subprocesses that may call the fuzz function simultaneously." Default GOMAXPROCS.
- `-fuzztime`: default runs forever; the CI smoke uses 20s and 10s.

Recommended CI: `actions/cache` on `$(go env GOCACHE)` (or the cache step in setup-go) plus commit `internal/*/testdata/fuzz/*`. Keep the 20s and 10s smoke fuzz. Move long fuzzing to a scheduled job, with `-fuzzminimizetime 10s`.

## 6. Test caching

Verified from `go help test`:
- "If a run of go test has any test or non-test flags outside this set, the result is not cached." Cacheable flags: `-benchtime, -coverprofile, -cpu, -failfast, -fullpath, -list, -outputdir, -parallel, -run, -short, -skip, -timeout, -v`.
- `-json` is NOT in that set. `go test -json` results are never cached (probe: repeated `-json` runs showed fresh start/Time events).
- "Tests that open files within the package's module or that consult environment variables only match future runs in which the files and environment variables are unchanged."
- "The idiomatic way to disable test caching explicitly is to use -count=1."

Answer:
- `-count=1` is only needed for tests that read state outside the tracked set (for example, files outside the module, network, a real ZFS pool). The VM leg runs a compiled binary, so `go test` caching does not apply there at all. Keep `-test.count=1` as the script already does; it is harmless.
- For `go test -race -count=1 ./...` in the unit job, `-count=1` is correct: the job must actually run.
- Cache key for actions/setup-go: `go-version-file: go.mod` is already used. Caching GOCACHE should key on the Go toolchain and sources, for example `${{ runner.os }}-gocache-${{ hashFiles('go.sum', 'go.mod') }}-go1.27.3`. Not verified: what setup-go v5.6.0 caches by default; check its README before relying on it.

## 7. gotestsum vs test2json

Verified: `go tool test2json` and `go test -json` already produce the event stream (go help test). The only things gotestsum adds are JUnit XML output and rerun-of-failures. This job needs neither. Use `go tool test2json` (no new dependency). Add gotestsum only if the CI dashboard needs JUnit.

---

## Six most useful facts, with commands

1. Parse VM results with test2json, run on the runner:
   `./x.test -test.v=test2json > x.raw; go tool test2json -t < x.raw > x.json`
   Then select `Action == "skip"` and `Test` instead of grepping `--- SKIP:`.

2. Coverage from the VM, written as covdata (not GOCOVERDIR):
   `CGO_ENABLED=0 go test -c -cover -coverpkg=./... -o x.test ./internal/endpoint && ./x.test -test.gocoverdir=$WORK/cov/vm`

3. Merge VM and unit coverage, report separately:
   `go tool covdata merge -i=$WORK/cov/vm,$WORK/cov/unit -o merged && go tool covdata textfmt -i=merged -o merged.out`

4. Do not use synctest around real processes. Use `t.Context()` with `exec.CommandContext` for process deadlines.

5. Fuzz housekeeping: commit `testdata/fuzz/<Name>`; cache `$(go env GOCACHE)`; use `-fuzzminimizetime 10s` in CI (default 60s).

6. `go test -json` is never cached; `-count=1` is only needed for tests reading state outside the tracked set. The VM binary is not cached at all.

Gaps: release notes unreachable (go.dev 403 from proxy), so version numbers for synctest GA, t.Context/t.Chdir, b.Loop, t.Output and t.Attr are unverified. The VM-timeout and killed-process behaviour of test2json output is untested.
