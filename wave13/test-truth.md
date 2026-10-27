# test-truth (wave 13): AN-01 and the stale README/proto claims

Branch `test-truth` (worktree `/home/user/empty/.claude/worktrees/agent-a4a564b240376ea44`), based on `d6e4ac2` (HEAD of claude/gallant-johnson-1qaxp5). Not pushed.

| commit | subject |
|---|---|
| f7e0fb2 | endpoint: FuzzReconcileSafe sees a group that fails on every run |
| 6185732 | api: snapshot id, export path and desired-id comments match the code |
| a4bbf1e | README: scan, locks, perf, CI pool, NFS and fuzz claims match the code |

Gates (at a4bbf1e): `gofmt -l .` is empty, `go vet ./...` passes, `go mod tidy -diff` is clean, `go test -race -count=1 ./...` passes in every package. buf v1.47.2 was downloaded as CI does, sha256 checked (`39716cfe...b116`), into `scratchpad/wave13/tt/buf/` (not the repo). `buf lint`, then `buf generate`, then `gofmt -l gen`, `git diff --exit-code -- gen/` and `git status --porcelain` are all clean.

## Anomalies first

1. **AN-01 is fixed and shown with a mutant.** Logs are in `/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave13/tt/`.
   - The mutant is in `applyReconcile` (`internal/endpoint/reconcile.go:363`): `case acts[0].Reason == ReasonOrphanCloneSource:` marks the group failed with `failed: mutant` and never applies it.

   | oracle | seeds | result | log |
   |---|---|---|---|
   | old | old 7 + 177a8048... | **PASS (survives)** | `mutant-old-oracle.log` |
   | old | + `{0x87,0x00}` | **PASS (survives)** | `mutant-old-oracle-new-seed.log` |
   | new | old seeds only | **PASS (survives)** | `mutant-new-oracle-old-seeds.log` |
   | new | + `{0x87,0x00}` | **FAIL (killed)**: `third reconcile still failing v0@volume-source-for-volume-gonev0, which no hidden child or outside clone explains: failed: mutant` (seed#1) | `mutant-new-oracle.log`, and again at the committed text: `mutant-final-commit.log` |

   - So the oracle and the seed are each needed to kill it. The mutant was reverted with `git checkout`, and the tree was clean before each commit.
   - Unmodified code: the seeds pass. `go test -run NONE -fuzz FuzzReconcileSafe -fuzztime 60s` ran 21,562 execs (29 new interesting) with no failure (`fuzz60.log`). No new corpus file landed in `testdata/`.
   - What the fix changes (adapted from `probe-src/an01-fuzz-strengthen.diff`; it applied cleanly):
     - It adds a `hidden` set: a volume with a child dataset (`x&child` on a filesystem) or an outside clone of a deferred CSI snapshot (`x&deferredSnap`).
     - After the third run it fails on any `failed:` skip whose target is not in `hidden`, and on any `SkipDependentFailed` when `hidden` is empty.
     - When `hidden` is empty it also runs a dry run and requires an empty plan.
     - It adds the seed `{0x87, 0x00}` and updates the doc comment.
   - Known weakening, kept from the probe: when `hidden` is non-empty, dependent skips anywhere are allowed, and the dry-plan check is skipped.
2. **There was no BenchmarkScan.** The ledger's DOC-R81 test column names `BenchmarkScan`, but it did not exist. I added `internal/endpoint/scan_bench_test.go`. It records the simulator's real `zfs get` output for n rows, then replays it, so it measures parse, decode and NewTable without the exec.
   - 10k rows: 141-149 ms/op, 100.8 MB/op, 345k allocs/op.
   - 1k rows: about 19-20 ms/op (`bench-scan.log`).
   - The old README figure (12 µs) was the struct-of-arrays saving on a 10k-row filter (`docs/research/dod-go/memory-layout.md:5`), not a scan cost.
3. **ADR 0004 is not implemented for `nfs_clients`.** The code still reads and writes `<prefix>:nfs_clients` (`internal/endpoint/endpoint.go:101`, `export.go:19-22,693`, `table.go:235,254`). Reconcile does not report a leftover one (there is no hit in `reconcile.go`). The README now says both are pending. Code is out of scope here.
4. **Stale comments remain in out-of-scope files.** `internal/csi/harness_test.go:24` and `internal/endpoint/endpoint_test.go:20` still say "a file-backed pool". That is technically true (a sparse file on tmpfs), so I left them; `internal/csi` was off limits anyway.

## README claims: before and after

Ledger line numbers are 1 lower than at this HEAD. The lines below are at a4bbf1e.

| ledger id | README line | before | after | code evidence |
|---|---|---|---|---|
| DOC-R15 | 15 | "desired clients in a local `<prefix>:nfs_clients` property with `sharenfs` computed from it" (no mention of ADR 0004) | That text is kept, because the code does it. Added: ADR 0004 decided that export state lives only in `sharenfs`; `nfs_clients` is to be removed and reconcile is to report a leftover one (neither is done yet). | `docs/adr/0004-support-floor-and-node-side.md:23-24`; `internal/endpoint/endpoint.go:99-101`; `internal/endpoint/export.go:19-22,693` |
| DOC-R30 | 31 | "one `zfs get -Hp` per call reads the subtree" | "one `zfs get -Hp -d` per scan reads the parent's volumes and their snapshots (or one volume and its snapshots)". The parent and deeper datasets are dropped. An RPC may scan once per touched volume and again to re-read. | `internal/endpoint/table.go:328-372` (depth 1/0, then the DeleteFunc on parent and `/`); `internal/zfs/zfs.go:426-431` (`-d depth+1`); `endpoint.go:495-516` (scanFor: one scan per volume), `:518-519` (readVolume re-scan), `:673,685` (CreateSnapshot scans twice) |
| DOC-R35A | 36 | "every resource an operation touches is locked, including the source snapshot of a clone" | Now lists each RPC's lock keys. DeleteVolume, ExpandVolume and the exports lock only the volume, so DeleteVolume releases a clone-source snapshot on another volume without that volume's lock. Reconcile uses TryAcquire and skips busy volumes. | `endpoint.go:438-448` (CreateVolume), `:541` (Delete), `:563` (Expand), `:667` (CreateSnapshot), `:726` (DeleteSnapshot), `:809,889` (exports); `reconcile.go:597` |
| DOC-R35B | 36 | "A busy resource returns `ABORTED` one second before the caller's deadline" | The wait is bounded by `-lock-wait` (default 30 s) and cut to 1 s before the deadline *when the call has one*; then `ABORTED`. | `endpoint.go:304-333` (`lockMargin`, `ctx.Deadline()` branch); `endpoint.go:190-191` (30 s default); `cmd/zfs-endpoint/main.go:76` |
| DOC-R47 | 48 | "maps 495 operations across 13 implementations" | "maps 108 distinct operations across 13 implementations ... (495 mappings)" | `docs/research/alignment.md:5` |
| DOC-R81 | 82 | "about 12 µs per 10k rows against about 1.5 ms per `zfs` exec. The real cost was a map per dataset, now one flat buffer" | Struct-of-arrays saves about 12 µs on a 10k-row filter. A whole-parent 10k-row scan costs about 140 ms and 100 MB Go-side (`BenchmarkScan`). The cost is in parsing and decoding, not the layout. | `docs/research/dod-go/memory-layout.md:5-6`; `internal/endpoint/scan_bench_test.go` (new); run in `scratchpad/wave13/tt/bench-scan.log` |
| DOC-R106 | 107 | "CI does the latter on a file-backed pool" | An Ubuntu VM with OpenZFS 2.4 (`ci/real-zfs-vm.sh`): six concurrent shards, each on its own pool backed by a sparse file on a guest tmpfs | `ci/real-zfs-guest.sh:9-15,25-32` (tmpfs mount, `truncate -s 10G`, `zpool create` per shard); `ci/real-zfs-vm.sh:1-4,179` |
| DOC-R110 | 111 | "once failures clear it converges and then plans nothing" | The run after convergence applies nothing and reports no failed group, except a volume zfs refuses for something the scan cannot see (a child dataset, an outside clone of a deferred snapshot). With no such volume, a dry run plans nothing. | `internal/endpoint/reconcile_fuzz_test.go:235-248` (new oracle); AN-01 above |

The ledger counts 10 README claims checked, 8 of them stale; the 8 stale ones are the rows above (R35 is two rows).

## Proto comments (`api/zfsendpoint/v1/endpoint.proto`, gen regenerated)

| id | line | before | after | code evidence |
|---|---|---|---|---|
| DOC-PROTO98 | 98-102 | `Snapshot.id // "<volume id>@<name>"` | `"<volume id>@<component>"`: the component is the name when it is a valid zfs name and the id fits 128 bytes, else `csi-` + 32 hex of sha256(name). The id is opaque. | `internal/endpoint/plan.go:663,697-706` (`snapComponent`) |
| DOC-PROTO239 | 247-252 | "path to mount from the storage host (server:export_path) ... pass it on (CSI publish_context)" | It is the volume's mountpoint, a bare path with no `server:` part, read fresh on each call. CSI carries no export in publish_context; ADR 0004 hands the node side `server`/`share` in volume_context. | `endpoint.go:854` (`return row.Mount, nil`); `internal/server/server.go:100-104`; no PublishFileExport caller in `internal/csi`; `docs/adr/0004...md:37` |
| S8 desired ids | 186-194 | (said nothing about unknown ids) | An id that names no volume, including one the endpoint could never issue (`ns/pv`), is reported in skipped ("desired volume does not exist") and the run goes on. An empty id is INVALID_ARGUMENT. | `reconcile.go:540-549` (only an empty id is refused), `:195-199` (SkipDesiredMissing), `:45`; tests `seams_test.go:366-377` (TestReconcileSkipsNonIssuableDesiredID), `reconcile_contract_test.go:175` |

## Not done (out of scope)

AN-04 (nightly guard), AN-09 (explorer classify), and anything in cmd/, internal/csi, deploy/ or the ci/ scripts.
