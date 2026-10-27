# McNamara and survivorship audit: evidence and process (zfs-endpoint)

Tree audited: `b540a2c` (CI run 13) and `1357361` (file-export merge, CI run 14 in progress). The repo was not modified. Clean exports used for measurement: `scratchpad/dod3/head` (b540a2c) and `scratchpad/dod3/head2` (1357361). Coverage profiles: `scratchpad/dod3/cov*.out`, summariser `scratchpad/dod3/covsum.py`.

Evidence kinds used below, from weakest to strongest: **source-read** (someone read OpenZFS or driver code), **simulator** (`zfstest.Sim`), **real-ZFS-2.2h** (the 24.04 hosted runner: zfs 2.2.2 userland with a 2.3.4 kmod, a pairing nobody deploys), **real-ZFS-2.4** (26.10 VM, `current` image, so the version moves), **production** (none exists).

---

## 0. Summary

The green signals are real, but most of what they certify is the simulator and Go-side validation. What touches real ZFS is the happy path of 14 contract tests plus 41 csi-sanity Controller specs. No ZFS **error** ever flows end to end through the endpoint on real ZFS in CI. No crash, no foreign writer, no scale, no NFS server, no suspended pool, no matched 2.2 stack and no upgrade is measured anywhere. Several celebrated numbers measure something other than what they are cited for.

| Celebrated number | What it actually measures |
|---|---|
| "CI green, real ZFS" | 14 of 18 endpoint contract tests (b540a2c) or 15 of 22 (1357361) run on a real pool. The rest **SKIP**, and a skip counts as green. The 3 `internal/server` tests run in both real-ZFS jobs but are hard-wired to the simulator (`server_test.go:27`, `zfstest.New`). `internal/zfs` (parser, classifier, runner) is never run on real ZFS: the VM script builds only `endpoint server csi` (`ci/real-zfs-vm.sh:20`). |
| csi-sanity "41 passed, 0 failed" | 41 of **103** specs in csi-test v5.6 (92 in v5.4). All 41 are Controller specs. **The Identity group never runs.** The focus `^Identity Service\|\[Controller Server\]` (`internal/csi/sanity_test.go:26`) has an anchor that matches nothing. Proof: `-ginkgo.focus='^Identity Service'` runs 0 of 103 specs, and `'Identity Service'` runs 3. `identity.go` coverage is 12.5%: Probe and GetPluginInfo are never called. The 37 Node specs are focused out by design, and 25 in-focus specs skip on capabilities. |
| Fuzz exec counts | Simulator only, with a 20 s and a 10 s smoke run. `FuzzCreateConverges` never sets a content source (`converge_test.go`, no `Source*`), so clone and restore bugs (advisor #4, #6) are outside its domain. Its property read-back check is tautological because the simulator echoes `-o` values verbatim (`adv3/props.md` F2). |
| Benchmark "≈12 µs per 10k rows vs ≈1.5 ms per zfs exec" (README:60) | The 1.5 ms is **spawning a trivial process**, not `zfs`. The source says so: "No `zfs` binary on this host, so zfs's own cost is unmeasured" (`docs/research/dod-go/memory-layout.md:6`). The README turned a proxy into the thing itself. |
| "495 mappings × 13 implementations" | How broad the corpus is, drawn from survivors (§2). It says nothing about correctness against OpenZFS. |
| Findings per adversarial round | Volume of findings, not closure. Of the 19 wave-3 advisor findings plus 4 carried open, only #2 (newline) is partly fixed at HEAD. #1 (clone from unmanaged source; found **twice**, also `adv3/csi.md` #1), #3 (OkExists clone adopts a foreign dataset), #6 (crash after clone converges to a different state), #7 (`received` counts as ownership, `zfs.go:43`) and runtime-ops "DeleteVolume destroys foreign (sanoid) snapshots" are all still open at 1357361 (checked by grep). |
| README "Decisions and their evidence" | 10 rows, all **source-read** or prior-art. None cites a real-ZFS test, and none says what kind of evidence it rests on. README:5 claims behaviour is "verified in its source and on a real 2.4.x pool". Only the values the tests use are verified on a real pool (§1.4). |

---

## 1. McNamara: measured vs. unmeasured

### 1.1 Tests that skip on real ZFS (counted)

| Test | Why it skips on real | What goes unmeasured on real ZFS |
|---|---|---|
| TestConcurrentCreatesSerialize | `h.Sim(t)` (call accounting) | lock serialisation against real exec latency |
| TestCloneResizeFailureConverges | `h.Sim(t).Fail` (fault injection) | partial-failure convergence |
| TestMissingParentIsUnavailableNotAbsent | `h.Sim(t)` | exported or missing pool → UNAVAILABLE, the README's 2nd decision |
| TestReconcileLocksTargets | `h.Sim(t)` (parked runner) | reconcile vs. in-flight request |
| TestFileExportLifecycle (1357361) | `h.NFS(t)`: no nfsd in either job | the whole NFS export path |
| TestFileExportAdoptedVolumes (1357361) | `h.NFS(t)` | inherited sharenfs → `*` export revocation |
| TestFileExportVerifiesKernelTable (1357361) | `h.Sim(t)` (`SetNFSDown`) | "zfs set exits 0 when exportfs fails" (the design's key premise) |

That is 7 of 22 harness tests skipped on real ZFS (32%), plus 3 server tests that silently run on the simulator inside "real" jobs. The job logs show `--- SKIP` lines (run 13, noble job), but nothing counts them or fails on them.

### 1.2 Code paths exercised on real ZFS (quantified)

Method: `go test -coverpkg` over `internal/{endpoint,zfs,lock,server,csi}` (simulator code excluded). The "real-reachable" set re-runs **only the tests that run on a real pool** (harness tests minus skips, plus csi controller tests and sanity in both access types) on the simulator. Real ZFS cannot reach more Go statements than this, so it is an upper bound.

| | b540a2c | 1357361 |
|---|---|---|
| All tests on simulator | 85.5% (1151/1346) | 86.3% (1375/1593) |
| Real-reachable upper bound | **78.3%** (1054/1346) | **71.6%** (1140/1593) |
| `export.go` real-reachable | n/a | **25%** (43/172), against 94% on the simulator |
| `runner.go` (the exec of `zfs`) | 0% on the simulator | runs only on real ZFS |
| Error-return statements (`return …err/errorf/zfsErr…`) | | 224 total: 50% on the simulator, **32%** real-reachable. Most of that 32% is Go-side input validation that fails before any `zfs` call |
| `zfsErr` code mapping (endpoint.go:199-221) | | 12 statements: 6 on the simulator (Internal fallthrough only), **2** real-reachable. NotFound, AlreadyExists, FailedPrecondition, ResourceExhausted, InvalidArgument and Unavailable are reached by **no** test anywhere |
| `apply.go:39` (failing-op index) | | simulator only; on real ZFS no plan op ever fails |

Take the ~72% as the most CI could reach. The defensible figure is lower: about 70% of what real ZFS actually exercises is happy-path statements. The **classified-error design** ("only a classified not-found is absent", the README's first decision) has **zero** end-to-end real-ZFS evidence. Its support is `TestClassify` on literal strings copied from OpenZFS source (source-read), and those strings come from a 2.4.99 checkout, not 2.2.2 or 2.4.4 (`wave3/zfs-ops.md` UNVERIFIED list).

### 1.3 Important properties with zero measurement

| Property | Current evidence | Why it matters |
|---|---|---|
| Crash or kill at each op boundary, on real ZFS | none. `TestCreateVolumeResumesInterruptedProvision` builds the half state by hand. Advisor #6 shows on the simulator that crash-after-clone converges to a *different* end state (quota dropped) | Happens in production by design: `exec.CommandContext` (runner.go:39) SIGKILLs `zfs` when the sidecar's 10 s deadline expires. There is no `WaitDelay` and no Setpgid |
| Scale ≥10k datasets / 100k snapshots | source-read plus a synthetic parse: 2.12 s parse, 1.02 GiB allocated, 322 MiB output (`wave3/zfs-ops.md` row 16). Real `zfs get -r` time is UNVERIFIED | DeleteVolume, CreateSnapshot, ListVolumes and ListSnapshots each do a whole-parent `scan("")` (endpoint.go:350/426/448/475) under the 10 s timeout. Mass delete is O(n²). Unbounded concurrent execs (runtime-ops) |
| Latency against the 10 s sidecar timeout | none. Real CI runs take 0.2-2.4 s per test on an empty pool, and no p99 is recorded | The `lockMargin` design (endpoint.go:169-176) budgets lock wait, not `zfs` exec time |
| Concurrent foreign writers (admin, sanoid, `zfs recv`, delegated userprop) | simulator proofs by adversaries. No real test | newline forgery (phantom variant still open), `received` ownership, foreign-snapshot destroy (data loss), OkExists clone adoption |
| Suspended pool (`failmode=wait`) | source-read only (runtime-ops: HIGH, UNVERIFIED) | `zfs` blocks in D state, the volume lock is pinned, and drain cannot stop handlers |
| 2.2 vs 2.4 read-back | 7 of ~110 allowlisted inputs round-tripped on real 2.2h/2.4 | `xattr=dir` reads back `on` on 2.2, so every retry is AlreadyExists forever (`adv3/props.md` F1). `snapdir=disabled` is rejected by 2.2 and becomes Internal (F3). The tests use exactly the values that print the same on both versions |
| Matched 2.2 stack | none. The "2.2 leg" runs 2.2.2 userland on a 2.3.4 kmod | ioctl and feature behaviour of a real 2.2 host (TrueNAS 24.x, Debian 12 backports, Ubuntu 24.04 HWE-less) |
| NFS export against a real nfsd | none (3 of 4 tests skip, or are simulator-only) | etab parsing, exportfs failure-yet-exit-0, `*` revocation |
| Upgrade paths (democratic-csi-adopted datasets, 2.2 pool → 2.4 import, endpoint vN → vN+1 properties) | source-read on the baseline's property names | adoption is a headline goal |
| Pool features off or pool not upgraded | none | allowlisted values that need a feature become Internal (props F4) |
| Observability | none (7 slog lines) | an operator cannot tell any of the above apart in production |

### 1.4 How narrow "verified on a real 2.4.x pool" is

The allowlist accepts ~85 enumerated inputs (57 for compression alone) plus 25 block sizes. Real-pool tests touch `compression=lz4,zle`, `recordsize=16K`, `atime=off`, `volblocksize=8K`, `sync=always` and `snapdir=visible`, which is **7 inputs, about 6%**. The `zstd`, `zstd-N`, `gzip-N`, `xattr`, `acltype=nfsv4` and `dedup` values were confirmed only by reading `zfs_prop.c`. The CHECKPOINT itself records "zstd read-back + nfsv4 mapping unverified on real pool".

---

## 2. Survivorship

### 2.1 The corpus is survivors

`alignment.md` (495 operations × 13 implementations) and the prior-art reviews draw only on projects that are still alive and public: democratic-csi, zfs-localpv, truenas-csi, the TrueNAS middleware, synology-csi, pve-storage, go-zfs, csi-driver-nfs and csi-lib-iscsi (`scratchpad/repos/`). Failure modes that this corpus cannot show:

- **Failures that killed projects.** A driver that died of per-op whole-pool scans at scale, of zvol/udev races, of `zfs` hangs on suspended pools, or of ownership confusion after `send/recv` would not appear in the corpus. The survivors mostly sidestepped those problems, or their users stayed small, so the corpus under-represents exactly §1.3's scale, hang and foreign-writer rows.
- **Forks and abandoned PRs.** Fixes that were tried and reverted (for example JSON output, caching tables, channel-program fencing elsewhere) appear only where a survivor merged them.
- **Silent data loss.** Survivors' issue trackers show failures that users noticed *and reported* while *staying* users. Destroyed sanoid snapshots or a wrong ownership adoption are noticed late or never. Their absence from trackers is not evidence of absence (runtime-ops: democratic-csi has the same destroy -r behaviour).
- **Survivors used as positive authority.** ADR 0003 rightly demotes democratic-csi to "behavioural baseline only". Even so, "baseline parity" justifies several behaviours (DeleteVolume refusal semantics, the `provision_success` marker name, `<vol>@<name>` ids) that mean only that democratic-csi survived, not that it is correct.

### 2.2 Survivors among agents and claims

- **Only finished agents report.** Agents that ran out of time or context leave no file. Nothing records which briefs produced no output, so the denominator is unknown.
- **"Verified" means "has a path:line".** The brief rule "cite path:line for every claim; 'unverified' otherwise" (`align/gaps/BRIEF.md`) is a filter. Runtime facts, which no source line can prove (latency, hang behaviour, real stderr text per version), are always marked UNVERIFIED. The wave-3 scouts carry 39 UNVERIFIED claims (zfs-ops 14, csi-seams 10, adoption-drift 10, runtime-ops 5). **None of the runtime-ops HIGH rows** (suspended-pool hang, unbounded concurrent execs, whole-parent scan at scale, foreign-snapshot destroy, observability) appears in README "Open gaps". The claims that survive into the README are the source-readable ones. The dropped ones are exactly the real-ZFS ones.
- **Adversaries attacked snapshots of merged code.** code-review.md: "while this review was being written, the endpoint package changed again". advisor-unknowns started on ba26fd8 and finished on 1b9270b. Reconcile (merged 04:23) and file-export (merged 04:26) were merged with **no code-level adversary**. The project went from the first real-ZFS CI commit (cc7eb4b, 03:51) to HEAD in about 35 minutes, across 8 merges.
- **Bugs found are the findable ones.** Each adversary had a copy of the module, the simulator and source access, and no `zfs` binary. Every proof test is a simulator test. Anything the simulator cannot represent is outside what an adversary can prove, so it was either not reported or reported as UNVERIFIED and then dropped.

### 2.3 Why the critical newline-forgery bug survived several rounds

The bug was in the tree from cc7eb4b and fixed only partially in b540a2c. The phantom-row variant is still open (advisor #2 residual, `TestW3PhantomCloneBlocksDelete`).

1. **The simulator cannot hold the input.** `checkProps` rejects `\n\t\x00` in values *we* write (`zfs.go:83`). Every test reaches the simulator only through that client, so no simulator dataset can ever have a newline value. The defence was applied at the write side, which shows the authors knew about newlines. That same defence made the read-side threat impossible to represent in every simulator test, fuzz target and adversary proof.
2. **The fuzz oracle was shape, not provenance.** `FuzzParseColumns` asserts `cells == names × props`. A well-formed forged line satisfies that.
3. **The proposed oracle encoded the false assumption.** `docs/research/dod-go/testing.md:16` proposed `FuzzParseGet` asserting "no newline in values", which treats the attack as an impossible input.
4. **Round 1 saw the neighbour and classified it as robustness.** code-review.md notes "parseGet now tolerates tabs in values". Tabs were handled as a parsing nicety, not as hostile input.
5. **Scope anchoring.** Reviews were scoped to "endpoint semantics" with `zfs` output trusted. The threat model was "us vs. zfs", with no third writer holding delegated `userprop`.
6. **What found it was a different method.** A scout read `libzfs_util.c`'s *printing* code (wave3/zfs-ops row 13) and wrote a raw-bytes harness that bypasses the client (`wave3/bench/zfs/inject_test.go`). That is a new evidence path, not a fourth pass of the same review.

### 2.4 Areas that have never had an adversary

| Area | Status |
|---|---|
| `internal/endpoint/export.go` + simulator NFS/etab model (merged in 1357361) | design adversaries only (gaps/adv-file-export-*); **no code adversary**, real coverage 25% |
| `internal/endpoint/reconcile.go` (destroys data by design) | only a seam note (advisor #19); no dedicated adversary; FuzzReconcileSafe's simulator cannot emit `source=received` |
| `ci/real-zfs-vm.sh` and the CI workflow as a *measurement instrument* | never reviewed for what it fails to measure: skips pass, `current` image drifts, no `zfs version` recorded per result, `internal/zfs` not run, server tests on the simulator. code-review-2 covered "CI" before the VM leg existed |
| The simulator as a whole | simfidelity audited create/clone/set, destroy/promote and get/list. **Not audited:** error strings per version, index-property canonicalisation (props F2), `received` source, snapshot volsize (advisor #17), NFS/etab model, concurrency semantics |
| csi-sanity configuration (focus regex, skipped capabilities) | never; the Identity-group miss above went unnoticed |
| ADR 0001 (transport auth) and ADR 0002 (snapshot ids) | design adversaries before the ADRs; the ADR text itself was never attacked |
| Go 1.27 / grpc 1.84 / csi-test 5.6 bump (5433e27) | a scout recommended it; no adversary; csi-test 5.4→5.6 changed the spec count from 92 to 103 and nobody re-baselined |
| The research corpus and README evidence claims (alignment.md, Decisions table) | prior-art.md attacked prior-art claims; nobody attacked whether each README row's evidence supports it (for example the 1.5 ms proxy) |
| Fixes themselves (b540a2c duplicate-line check) | advisor noted a residual; no adversary on the fix |
| Packaging, privilege, systemd, multi-container flock | scout-level (runtime-ops), UNVERIFIED |

---

## 3. Agent-process biases

- **Hypotheses in prompts become the search space.** The brief says "try to break the specific prior claims you depend on" and asks for "CLAIMS CHECKED (prior claim → HOLDS/BROKEN)". The adversary's work is then a list of the briefer's claims, and output is HOLDS or BROKEN on that list. Unlisted claims (foreign writers, scale) get no row. This audit's own prompt lists 8 unmeasured properties, and my findings cluster on them too; §2.4 and the Identity-focus finding are the parts not prompted.
- **Findings anchor on the prompt's examples.** Two independent adversaries (adv3/csi #1 and advisor #1) found the same clone-from-unmanaged bug. That was treated as corroboration, but they shared the method (simulator, same code paths), so the second find adds little independent evidence.
- **"Verified" is overloaded.** In scout reports it means "read the source at path:line". In the README it is cited as "verified … on a real 2.4.x pool". The ledger below separates these.
- **Simulator proofs are treated as real proofs.** Every adversary proof test is a simulator test. That is fine for logic bugs, but the simulator is the author's model of ZFS, so a simulator proof inherits the model's blind spots (§2.3.1).
- **Counts over closure.** Rounds are summarised by number and severity of findings. Nothing tracks found → fixed → regression-tested on real ZFS.
- **Speed hides staleness.** Reviews outlive the tree they reviewed. Nothing ties a finding to the commit it was proven on and re-runs it later.

---

## 4. Remedy: an evidence ledger as data

One file, `docs/evidence/ledger.jsonl` (or TSV), one row per claim or decision. The README Decisions table and Open gaps are generated from it, not hand-written.

```
{"id":"dec-classified-notfound",
 "claim":"only a classified not-found counts as absent",
 "kind":"decision",                       // decision | invariant | compat | perf | security
 "evidence":[
   {"kind":"source-read","ref":"openzfs lib/libzfs/libzfs_util.c:449","commit":"b540a2c"},
   {"kind":"simulator","test":"internal/zfs.TestClassify","commit":"b540a2c"}],
 "required":"real-ZFS-2.2+real-ZFS-2.4", // the weakest kind that may back this claim
 "strongest":"simulator",                 // derived: max(evidence.kind)
 "gap":true,                              // derived: strongest < required
 "last_verified_commit":"b540a2c",        // written by CI for test-backed rows
 "zfs_versions":["2.2.2/2.3.4k","2.4.4"], // written by CI from `zfs version`
 "owner":"endpoint",
 "findings":["advisor-unknowns#9","advisor-unknowns#10"],
 "status":"open"}
```

Evidence-kind order: `source-read < simulator < real-ZFS-2.2 < real-ZFS-2.4 < production`. Rules:
1. CI writes `last_verified_commit` and `zfs_versions` for every row whose evidence names a test that **passed, not skipped**, on that leg. A skipped test downgrades the row to its next evidence kind.
2. A row is **stale** if `last_verified_commit` is more than N merges old or older than any commit touching the files in its `refs`.
3. Every adversarial finding becomes a row with `status` (open/fixed/wontfix), the commit it was proven on, and its proof test. A round's output is measured by rows closed with real-ZFS evidence, not by rows opened.
4. The README states, per decision, `strongest` and `gap`.

Seed rows (current strongest evidence → required):

| Claim | Strongest now | Required | Gap |
|---|---|---|---|
| classified not-found = absent | simulator (literal strings) | real 2.2+2.4 | yes |
| missing pool → UNAVAILABLE | simulator (skips on real) | real 2.2+2.4 | yes |
| never hand-set zvol refreservation | real-2.4 (thick tests pass) | real 2.2+2.4 | 2.2 is a hybrid |
| `destroy -d` for snapshots | real-2.2h/2.4 (csi-sanity DeleteSnapshot) | same | no |
| never `create -p` | source-read + simulator (M5) | real | yes |
| converges after crash at any op boundary (README:33-34) | simulator; **falsified** on the simulator for clone (advisor #6) | real 2.4 kill-injection | yes, open bug |
| retry is idempotent (FuzzCreateConverges) | simulator, no content sources, tautological read-back | real, all allowlisted values | yes |
| property canonical form = what `zfs get -Hp` prints | real for 7/~110 inputs; **falsified** for xattr on 2.2 | real 2.2+2.4, all inputs | yes, open bug |
| ownership only from locally-set props | source-read; **falsified** (received) | real recv test | yes, open bug |
| never touches unowned datasets (Reconcile, README) | simulator; **falsified** (#19) | real foreign-writer test | yes, open bug |
| zfs output cannot forge rows | simulator unit (duplicate case); phantom open | real userprop-newline test | yes |
| No SoA: zfs exec ≈1.5 ms | **proxy** (process spawn) | real-2.4 at 10k datasets | yes |
| p99 of RPCs < 10 s at target scale | none | real, 10k datasets | yes |
| NFS publish verified against etab | simulator | real nfsd | yes |
| csi-sanity Identity+Controller pass | real (Controller only) | real (both) | yes (focus bug) |

---

## 5. Measurement plan, ranked by risk retired per CI minute

Costs are wall-clock minutes added to a PR run, estimated from run 12/13 timings: noble real job ≈75 s, VM step ≈70 s, endpoint suite 11 s on real ZFS, sanity ≈3 s per mode.

| # | Addition | CI cost | Risk retired | Notes |
|---|---|---|---|---|
| 1 | **Fix the sanity focus anchor and report skipped-on-real tests.** Drop `^` (or use Ginkgo labels). Emit a JSON summary from `go test -json`: tests that SKIP on a real leg, with reasons, and fail when the set grows. Run `./internal/zfs` in the real legs | ≈0.1 min | the green signal stops overstating. Identity group (3 specs) runs. New sim-only tests become visible | cheapest; makes every other number honest |
| 2 | **Runner-level fault injection usable on real ZFS, plus a crash-at-every-op-boundary matrix.** A `FaultRunner` wrapping `LocalRunner` either (a) runs op k and then returns an error, or (b) starts op k and SIGKILLs `zfs` after a delay. For each scenario (create fs/zvol, thick, clone from snapshot, clone from volume, with props, delete with clone-source, snapshot, expand) and each k in 1..len(plan): retry, then assert the end-state Table equals the uninterrupted run. Also un-skips 3 of 4 sim-only tests (CloneResizeFailure, MissingParent through a pool export, ReconcileLocks) | ≈1-1.5 min per real leg (≈10 scenarios × ≈4 ops × ≈0.5 s × 2 kinds) | crash recovery on real ZFS (§1.3 row 1). Would have caught advisor #6 on real ZFS. Exercises zfsErr mapping with real stderr | highest risk-retired per minute after #1 |
| 3 | **Matched 2.2 leg plus an exhaustive property round-trip table.** Run `ci/real-zfs-vm.sh noble` (24.04 VM, its own kernel and matched 2.2.2 userland and kmod) next to 26.10. Add one table test: for every allowlisted input, create, read back, then retry. Run it on both legs. Record `zfs version` in the result | ≈2 min (parallel job; ≈1.5 min boot + ≈20 s tests) | 2.2 vs 2.4 read-back (F1 xattr, F3 snapdir, F4 feature errors), hybrid-kmod blind spot | turns "7 of 110" into "110 of 110 × 2 versions" |
| 4 | **Foreign-writer scenario suite on real ZFS.** In-test admin actions through `zfs` directly: userprop with a newline (collision and phantom), `zfs send -R \| recv` copy under the parent, a sanoid-style snapshot, `zfs rename`, a dataset created in the scan→clone window (FaultRunner hook), a delegated `zfs allow`. Then run every RPC and Reconcile and assert nothing unowned is mutated | ≈0.5 min | open findings #1, #2-residual, #3, #7, #19 and foreign-snapshot destroy (data loss), on real ZFS | security and data-loss class |
| 5 | **NFS leg.** `apt-get install nfs-kernel-server` in the VM and on noble, then run the export tests unskipped. Include a forced exportfs failure (bad client spec or stopped nfsd) to check the "exit 0" premise | ≈0.7 min | NFS export end to end; export.go real coverage 25% → ~90% | the merged feature has no real evidence now |
| 6 | **Latency budget recorder.** Wrap every real-leg RPC with a timer, emit p50/p99 per RPC as a job artifact, and assert p99 < 5 s (half the sidecar timeout) | ≈0 (instrumentation) | baseline for the scale job; catches regressions | |
| 7 | **Nightly scale job** (not per-PR): one VM, a sparse 50G file pool, 10k filesystems/zvols created in parallel (`xargs -P8 zfs create`) plus `zfs snapshot -r` ×10 for 100k snapshots. Then time DeleteVolume, CreateSnapshot, ListVolumes (paged) and ListSnapshots, plus 50 concurrent DeleteVolumes; check RSS and p99 against 10 s. Cache the pool image as an artifact between nights | ≈15-25 min nightly (≈0 on PRs) | scale, the whole-parent scan design, unbounded concurrency, the 4 MB gRPC limit | highest absolute risk but costly, so nightly |
| 8 | **Suspended-pool and hang test** (nightly): file vdev on a dm-flakey/dm-error target, or `zinject -d … -e io -T all`, with `failmode=wait`. Assert RPCs return UNAVAILABLE or ABORTED within the deadline, the volume lock is released, and the process drains | ≈3 min nightly | `zfs` D-state hang (runtime-ops HIGH) | needs WaitDelay/Setpgid first, or it documents the hang |
| 9 | **Upgrade and adoption path** (nightly): the noble leg exports its pool as an artifact. The 26.10 leg imports it and runs adoption plus retry tests on datasets created by 2.2 and by democratic-csi-shaped properties | ≈2 min nightly | 2.2→2.4 pool import, baseline adoption | |

Rough ordering by risk retired per PR minute: **#1 (≈0.1 min) > #2 (≈1.5) > #4 (≈0.5) > #3 (≈2) > #5 (≈0.7) > #6 (≈0)**. #7-9 run nightly. Total PR-time increase for #1-6 is about **4-5 min** of parallel job time (wall clock ≈+2 min, since #3 runs as its own job). The current real-ZFS cost is ≈2.5 job-minutes.

## 6. Process changes (cheap)

- Every adversary brief includes a "**what you could not test**" section that is mandatory and ledger-bound. UNVERIFIED items become ledger rows with `required: real-…`, not dropped text.
- Run at least one adversary per round **without** the briefer's claim list (an open brief), and one with **real `zfs`** (the VM image is already scripted).
- When the same method finds the same bug twice, record it as one finding, not as corroboration.
- Re-run each finding's proof test against HEAD in CI (a `findings/` package of `TestW3*`-style tests marked `t.Skip("open: <ledger id>")`), so a fix flips the row and a regression re-opens it.
- Pin the 26.10 image by serial or date and record `zfs version` per result, so "real-ZFS-2.4" names a version.
