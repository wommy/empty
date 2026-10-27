# JSON scan batch (wave 13): `zfs get -j -p`, S1/H-03, F11, scan allocations

Branch `json-scan` (local, not pushed), from `claude/gallant-johnson-1qaxp5` at 654b864, in worktree
`/home/user/empty/.claude/worktrees/agent-a19aec7306147c3be`. Seven commits. All gates pass at HEAD f967df1.
Evidence files are in `scratchpad/wave13/json-scan-ev/`.

## Anomalies first

1. **The child locale planned in H-12 (`LC_ALL=C`) makes one non-ASCII byte fail every whole-parent scan.** This is the coordination point. I did not change the locale.
   - Evidence: `json-scan-ev/mbr.c` and `run.sh` compile the `mbrtowc` loop of `nvlist_print_json_string` (libnvpair_json.c, zfs-2.4.0), run after `setlocale(LC_ALL, "")` as zfs_main.c does, on this host's glibc 2.39:
     ```
     C       "café"       -> "caf<-1: printer stops, truncated JSON>
     POSIX   "café"       -> "caf<-1: ...>
     C.UTF-8 "café"       -> "café"
     C.UTF-8 "\xff"       -> <-1: ...>          (invalid UTF-8 truncates in any locale)
     C.UTF-8 "a😀b"        -> "ab"               (code points above U+FFFF are dropped)
     ```
   - Consequence under `LC_ALL=C`:
     - zfs prints a truncated document, still prints its `\n`, and **exits 0**.
     - The decoder always rejects the document, because the top-level `}` comes only at the very end. It **fails closed**, so it is correct either way.
     - The trigger is any byte >= 0x80 in any requested property of any dataset under the parent: a foreign user property such as a `café` comment, or a non-ASCII `mountpoint` path. That one dataset fails every whole-parent scan, so ListVolumes, Reconcile, DeleteVolume and the rest all fail. It is a denial of service, not a forgery.
   - Recommendation for the H-12 writer:
     - Use `LC_ALL=C.UTF-8` (plus `LANGUAGE=C`), not `C`. OpenZFS ships no translations, so `classify` still sees English. glibc 2.35 and later has C.UTF-8 built in, which covers Ubuntu 26.10 and TrueNAS 27.
     - Alternatively, use C.UTF-8 only for `get -j`.
     - Under C.UTF-8 only invalid UTF-8 still truncates. `TestRealGetJSONMatchesSim` logs whether `zfs set` even accepts it.
   - The real-pool test logs what the runner's locale actually does as `EVIDENCE` lines in the VM's `-test.v` log. It does not fail on the C-locale outcome; it fails only if a non-ASCII value reads back as anything other than the printed value or a fail-closed error.
2. **The Sim listed `zfs get` output in plain string order. Real zfs uses `zfs_compare` order** (zfs_iter.c, verified in `repos/openzfs`): a dataset, then its snapshots by createtxg, then its children. So `tank/a@s` comes before `tank/a/b`, but the Sim had them the other way round.
   - Fixed in `Sim.get`, with a test.
   - Nothing downstream depends on the order, because the Table sorts. The byte comparison against a real pool does depend on it.
   - Earlier fidelity checks could not catch this: the zfsdiff projection sorts its lines.
3. **CPU cost.** On the in-memory path, the JSON scan costs about 16–20% more CPU at 10k rows than the text parser did. It is at parity at 1k rows. It allocates 82% fewer bytes and about 3,400× fewer objects. Numbers are below.
4. **Race-build cost.** Under the race build the JSON path is noticeably slower, because the race detector instruments every byte a Go loop reads, and jsontext validates byte by byte in Go. The text parser used assembly (`IndexByte`, `Split`).
   - `go test -race ./...` wall time, warm cache, this host: 14.6s at 654b864, 16.3s at HEAD.
   - `internal/endpoint` alone: 7.6s → about 10s.
   - Most of it is TestModel: 1.96s → about 4.7s, because every model step reads a full scan.
   - I trimmed what could go without dropping jsontext validation (commit 4757852). The rest is jsontext's per-byte validation.
   - Plain `go test ./...` is unchanged within noise: endpoint 2.5s, zfs 0.2s.
5. **Ported H tests had to change shape.** The original `TestH_*` tests fed hand-written `-Hp` text through fake runners. Kept that way, they would pass after the switch only because JSON rejects text.
   - I ported them to write the forged value **into the dataset**, through raw Sim argv, or raw `zfs set` on the real pool.
   - Each test therefore holds whichever format the scan reads. Each fails at 654b864 because the forgery works there, and passes at HEAD because the value stays a value.
6. **"Collision is certain" comment.** The wrong comment lived only in `parseColumns`, which this batch deletes. Its corrected statement is now on `parseGet`, the `-Hp` parser that `Get` and `List` still use: a newline can forge lines for any name, including one zfs never listed, and nothing there detects it. The comment also states what that parser is used for (see the open questions).
7. **F11 has two meanings.**
   - The brief defines F11 as "checkProps refuses non-printable values".
   - `wave11/threat-model.md` defines F11 as "a newline value denies service to the scan".
   - I did both: checkProps, plus `TestF11NewlineValueDoesNotFailTheScan`.

## Commits

| commit | item | what |
|---|---|---|
| 350ed85 | 1 | **Scan with `zfs get -j -p` and a strict streaming JSON decoder.**<br>- New `internal/zfs/json.go`, `decodeGetJSON`, ported from the wave6 prototype: jsontext token streaming, all-or-nothing, output_version 0.1 exactly. Every requested property appears once and nothing else does. name, pool, type, createtxg and the snapshot fields must agree with the key. No member repeats or is unknown. Values are strings and valid UTF-8, followed by EOF.<br>- `ListColumns` argv: `get -j -p -t … -d N props root`.<br>- `zfstest/json.go` holds the byte-level printer model, `AppendGetJSON`, and the Sim answers `-j` with it.<br>- Goldens are in `internal/zfs/testdata/getjson`.<br>- **Deleted:** `parseColumns`, `FuzzParseColumns`, `TestParseColumnsRejectsForgedLines` and `TestParseColumns`.<br>- **New fuzz target:** `FuzzDecodeGetJSON`. Its seeds are the 4 goldens, 1 `f.Add` and 2 files in `testdata/fuzz/FuzzDecodeGetJSON`, and they replay in `go test`. nightly.yml's matrix and the README are updated.<br>- Endpoint test helper `rawOf` now prints JSON through the Sim printer.<br>- Get and List keep `-Hp`. |
| 72fd266 | 2 | **`TestRealGetJSONMatchesSim`**, in `internal/endpoint/getjson_real_test.go`, runs only with `ZFS_TEST_POOL`.<br>- It runs the same 8 commands on the pool and on a Sim whose pool has the same name, then requires `zfs get -j -p` to print **identical bytes**, with createtxg normalised.<br>- Coverage: FILESYSTEM, VOLUME, SNAPSHOT and clone rows; LOCAL, INHERITED and NONE user properties; origin and volsize; a value with every ASCII escape, raw `/` and DEL; and a value written as JSON syntax.<br>- It also logs the non-ASCII and invalid-UTF-8 behaviour as EVIDENCE.<br>- Includes the Sim `zfs_compare` order fix and `TestSimGetOrderIsZFSCompare`.<br>- Weight line `TestRealGetJSONMatchesSim 0.50`. |
| 47db120 | 3 | **Regression tests** for S1/H-03: `TestSeamGhostRowFromForgedNewlines` (endpoint, Sim), `TestH_ForgedLineForNonexistentDataset` (zfs, Sim), `TestH_ScanRejectsRowsZFSNeverListed` (endpoint harness, so it also runs on the real pool). Weight line `TestH_ScanRejectsRowsZFSNeverListed 0.30`. |
| 0152974 | 4 | **F11.**<br>- `checkProps` refuses any value byte outside 0x20–0x7e as `KindInvalidProperty`, which maps to InvalidArgument, before zfs runs. It covers create, create -V, snapshot, clone, set and set -u.<br>- Tests: `TestCheckPropsRefusesNonPrintable` and `TestF11NewlineValueDoesNotFailTheScan`. |
| 947afc8 | 5 | **Allocation cuts.**<br>- Row-count hint (`bytes.Count` of `"properties":{`) that pre-sizes Names, Cells and the name set.<br>- One `ReadValue` per property, matched byte for byte against zfs's single form.<br>- Table snapshot index as a row range in place of `[]int` per volume.<br>- No `ParseInt` on an empty committed_at.<br>- The arena starts at 4 KiB.<br>- `TestTableSnapshotsOf`. |
| 4757852 | 5 | **Race-build trims.** `cutString` uses `IndexByte`; the Sim printer appends runs of plain characters at once; the truncation sweep samples documents over 1 KiB. |
| f967df1 | 5 | **Decode in place.** The decoder reads a `*bytes.Buffer`, which jsontext does not copy; the README numbers are updated. |

## Fail-before / pass-after

Method: `git archive 654b864` into `json-scan-ev/base/`, copy in only the new test files, and run them there. Full logs: `json-scan-ev/fail-before.txt`, `fail-before-f11.txt`, `pass-after.txt`, `pass-after-f11.txt`.

At 654b864:
```
forgery_test.go:41: ListVolumes returns a volume that does not exist: {ID:ghost Name:ghost Dataset:tank/k8s/vols/ghost Kind:filesystem ...}
--- FAIL: TestH_ScanRejectsRowsZFSNeverListed
forgery_seam_test.go:48: ListVolumes = ["a" "ghost"], want [a]: it serves a dataset zfs never listed
--- FAIL: TestSeamGhostRowFromForgedNewlines
forgery_test.go:42: scan lists ["tank/csi" "tank/csi/pvc-a" "tank/csi/phantom"], want ["tank/csi" "tank/csi/pvc-a"]: a row for a dataset zfs never listed
--- FAIL: TestH_ForgedLineForNonexistentDataset
forgery_seam_test.go:89: one foreign value fails every scan of the parent: scan tank/v: zfs list tank/v: unknown: line 83: tank/v/b zfs-endpoint:managed_resource printed twice: a property value contains a newline
--- FAIL: TestF11NewlineValueDoesNotFailTheScan
checkprops_test.go:43: set with value "a\rb": err <nil>, 1 commands run      (and 35 more: \x01, DEL, café, 😀, \xff on all six write paths)
--- FAIL: TestCheckPropsRefusesNonPrintable
```
At HEAD, all five pass.

## Benchmark (item 5)

`BenchmarkScan` is the Go side of one whole-parent scan: decode, row decode and the Table, with exec excluded.
- Machine: 4 vCPU Xeon @ 2.8 GHz, shared with other work (load average about 2–3).
- Method: 10 interleaved rounds of 10 iterations, using the test binaries for 654b864 and HEAD (`json-scan-ev/bench10.sh`, `bench10.txt`).
- B/op and allocs/op are deterministic; times are wall time.

| rows | | ns/op min | ns/op median | B/op | allocs/op |
|---|---|---|---|---|---|
| 10,000 | 654b864 (`-Hp`, parseColumns) | 147.4 ms | 166.4 ms | 102,065,721 | 355,272 |
| 10,000 | HEAD (`-j -p`, jsontext) | 177.7 ms | 193.2 ms | **18,518,484** | **105** |
| 1,000 | 654b864 | 19.0 ms | ~21 ms | 10,049,229 | 35,657 |
| 1,000 | HEAD | 19.7 ms | ~22 ms | **1,981,280** | **58** |

The first measurement before any change, at count 5, gave 167–221 ms, 102.1 MB and 355,272 allocs/op (`bench-before.txt`). The README's "about 140 ms and 100 MB" was an earlier, faster run.

At 10k: bytes **−82%**, allocations **−99.97%**, CPU **about +16–20%**. The MB/s column is not comparable between the two, because `SetBytes` counts the JSON output, which is a different size from the text.

Where the bytes went, per 10k-row op:

| step | B/op |
|---|---|
| plain switch to JSON | 102 MB → 44.7 MB, 15.2k allocs |
| row hint (no Cells doubling) and committed_at fix | 19.0 MB, 5.1k allocs |
| snapshot index as a range | 18.5 MB, 117 allocs |
| in-place `*bytes.Buffer` | 18.5 MB, 105 allocs |

What remains:
- Cells: 10,001 × 36 properties × 32 B = 11.5 MB.
- Rows: about 5.2 MB.
- Arena: about 1.6 MB.

On CPU:
- One `ReadValue` per property, in place of eleven tokens, cut CPU from about +35% to +16–20%. jsontext's per-byte validation is the floor.
- A hand-written JSON scanner would go lower. I did not write one: the wave6 research advised staying on the reviewed parser.

FuzzDecodeGetJSON ran 90s, 2.08M executions, on the final property path with no failure.

## Test-time cost added

- **Real-pool leg:** two new tests run on the VM.
  - `TestRealGetJSONMatchesSim` (t.Parallel): about 14 zfs commands plus 3 scans, estimated 0.50s.
  - `TestH_ScanRejectsRowsZFSNeverListed` (t.Parallel): about 6 zfs commands, estimated 0.30s.
  - Weight lines are added to `ci/real-zfs-weights.txt` as estimates; `ci/real-zfs-weights.sh` replaces them after the first run.
  - About +0.8s of test time spread over the three endpoint shards, so roughly +0.3s on the slowest shard.
  - Every real-pool scan now decodes JSON, which is negligible next to zfs.
- **Unit job, `-race`:** about +1.7s wall for `./...` here, and the endpoint package about +2–3s (see anomaly 4).
- **Unit job, without race:** within noise.
- **Fuzz seed run:** about 0.01s.

## Open questions and recommendations

1. **Locale.** See anomaly 1. Recommend `LC_ALL=C.UTF-8` in H-12. It needs a decision between the two writers. My code is correct under either locale, but under `C` availability depends on every value under the parent being ASCII.
2. **The VM run is needed to confirm the shape.** No zfs binary exists here.
   - The goldens and the Sim printer are derived from the 2.4.0 source.
   - The first `real-zfs` run of `TestRealGetJSONMatchesSim` is the confirmation, a byte-for-byte diff.
   - Things it could disagree on:
     - member order or escapes;
     - the volsize source on a zvol (expected `LOCAL`) and on its snapshot (expected `NONE`);
     - `zfs_compare` order;
     - `zfs set` accepting tab or newline in a user property (assumed: the 2.4 source has no character check).
   - If it fails, the message shows the first differing byte and the full zfs output.
3. **Get and List still read `-Hp`.** `Get` takes one dataset and few properties; `List` serves only the startup legacy-prefix check (`cmd/zfs-endpoint/startup.go`).
   - A forged line there can overwrite a value of the queried dataset (Get), or invent a child (List: a false "legacy markers present" warning, or a missed one).
   - Recommend moving both to JSON in a follow-up. Get needs a non-strict "any properties" mode for `Get(name)` with no properties (the `all` case).
   - Not done here: it touches more call sites, and the health check.
4. **Streaming from the pipe.** The decoder reads tokens as a stream, but over the whole captured stdout: `LocalRunner.Run` still buffers it.
   - The wave6 `Streamer` seam would decode straight from the pipe. That is about 2× fewer peak bytes at 110k, and it needs exit-status handling.
   - It changes `runner.go`, which H-12 is editing, so I left it for after that merge.
5. **Version pinning.** `vers_minor != 1` fails every scan. That is intended by ADR 0004, but an OpenZFS upgrade that bumps the schema is a hard outage until a release follows. The VM gate catches it first.
6. **Stricter than before.**
   - Property aliases such as `compress` are no longer accepted by ListColumns; the scan uses only canonical names.
   - A property object with whitespace or reordered members is refused; zfs prints neither.
   - A dataset missing a requested property fails the scan, where it used to read as `-`. zfs always prints every requested property, as `-`/NONE when unset.
7. **The weights are estimates.** Refresh them from the first run's `test-times.tsv`.

## Files

- **New:**
  - `internal/zfs/json.go`
  - `internal/zfs/json_test.go`
  - `internal/zfs/forgery_test.go`
  - `internal/zfs/checkprops_test.go`
  - `internal/zfs/export_test.go`
  - `internal/zfs/zfstest/json.go`
  - `internal/zfs/testdata/getjson/*.json`
  - `internal/zfs/testdata/fuzz/FuzzDecodeGetJSON/*`
  - `internal/endpoint/getjson_real_test.go`
  - `internal/endpoint/forgery_test.go`
  - `internal/endpoint/forgery_seam_test.go`
- **Changed:**
  - `internal/zfs/zfs.go`
  - `internal/zfs/fuzz_test.go`
  - `internal/zfs/zfstest/sim.go`
  - `internal/endpoint/table.go`
  - `internal/endpoint/table_test.go`
  - `internal/endpoint/props.go` (comments)
  - `internal/endpoint/reach_helpers_test.go`
  - `internal/endpoint/scan_bench_test.go` (comment)
  - `ci/real-zfs-weights.txt`
  - `.github/workflows/nightly.yml`
  - `README.md`
- **Not touched:** `internal/csi`, NFS code, `runner.go` and the child locale. No proto change was needed.

## Gate at HEAD f967df1

- `gofmt -l .` is empty, ignoring `.claude/`.
- `go vet ./...` and `go vet -tags nightly ./...` are clean.
- `go mod tidy -diff` is clean.
- `GORACE=atexit_sleep_ms=0 go test -race -count=1 ./...` passes in every package (`json-scan-ev/race-final.txt`).
