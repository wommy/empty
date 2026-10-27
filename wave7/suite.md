# The test suite as data: inventory, speed, validity, reliability, design

Tree: `57ffbf6` (HEAD, the commit after `2246735`). The repo was not touched. All work is in the copy `scratchpad/wave7/suite-copy` (a `git archive HEAD` export with its own git, base commit `c45d0b5`). The diff of everything I changed there is `wave7/suite-data/suite-copy.diff`. Toolchain: `GOTOOLCHAIN=go1.27.3`. Scripts are in `wave7/suite-tools/`, data in `wave7/suite-data/`.

Measurement conditions:
- The host has 4 vCPUs and was shared with a mutation-testing agent (load average 28-50 throughout).
- Wall times on this host are noisy, so I report CPU seconds for build phases and use repeated or interleaved runs for comparisons.
- Real-pool numbers come from CI logs `wave7/logs/run19-{unit,vm}.log` (GitHub run 19, an older commit with fewer tests than HEAD, on an Ubuntu 26.10 VM with OpenZFS 2.4.4).
- There is no ZFS on this host. Real-pool speedups are therefore measured on a **latency model** fitted to run19 (§2.3).

## 0. Answer: ranked changes

Effect columns:
- **V** is validity: what becomes measured that was not.
- **R** is reliability.
- **s** is seconds saved per CI run. A negative number means added time.
- **Cost** is engineering effort, plus CI time when that is the cost.

| # | Change | V | R | s saved | Cost | Status |
|---|---|---|---|---|---|---|
| 1 | **Crash-injection table** (`crash_test.go`): 17 scenarios. For every mutating zfs call k, the run crashes before or after the call, the endpoint restarts, the request is retried, and the end state is compared with an uninterrupted run, followed by an idle re-run that must make 0 mutations. A wrapping `faultRunner` makes it real-pool capable. | 93 runs. **Found a real divergence:** DeleteVolume of a volume clone that dies between `destroy c` and `destroy -d src@volume-source-for-volume-c` leaks the snapshot, and the retry answers OK. On the real leg, opErr goes 0% → 85.7% and zfsErr 0% → 35.3%. Real-reachable statements +50. | Replaces the 2 sim-only crash tests, so the skip budget shrinks by 2 | sim −0.15 s; real ≈ −2 s (model: −4.2 s at parallel 8 on a 2-vCPU VM) | 1 day, done | prototyped, green (xfail ratchet on the 2 known points) |
| 2 | **`t.Parallel()` on every harness test, plus `-test.parallel=8` in the VM** (the VM has `-smp 2`, so the default is 2) | none | Isolation was already per test (own Sim or own `pool/ep-<test>-<ns>`). Shuffled race runs at `-count=20` are green. | **+5-6 s of the 7.2 s** real endpoint step (model: 13.85 s → 2.0 s; parallel 2 alone: 7.3 s) | 1 h, done. Watch thick-zvol space on the 10 G pool (§4). | prototyped |
| 3 | **Golden plan table** (`golden_plan_test.go` + `testdata/plans.golden`, `Plan.String()`): 48 (Table, request) rows render to stable text. `-update` rewrites the file, and every row must render identically 3× (determinism). | One reviewable file holds every planner decision. It subsumes 10 zero-unique planner tests and gives 21 unique statements, the most of any pure test. It made the delete-clone crash window visible before the crash table found it. | catches map-order leaks into plans | ≈0 (0.00 s) | 2 h, done | prototyped |
| 4 | **Property round-trip table** (`props_roundtrip_test.go`): the allowlist enumerates itself (probed with every zfs_prop.c name and power-of-two size). That gives **179 (kind, prop, value) rows**: create, read back, check value+source, retry. | Real-pool truth for 179 of 179 accepted values, against 7 today (§1.4 of mcnamara). Already corrected one wrong assumption: volblocksize's source is `-`/`default`, never `local`. | | sim −0.1 s; real ≈ −2 s (model −3.9 s at parallel 8) | 2 h, done | prototyped, green on sim |
| 5 | **Cache the qemu install in the VM job** (`apt-get update && install qemu-system-x86 …` takes 211 s of the job's ≈300 s) | none | fewer apt mirror flakes | **≈180-200 s** off the critical path | 1 h (actions/cache of the .debs, or a pinned container) | proposed (CI, not suite code) |
| 6 | **Fuzz smoke off the PR critical path**: a separate parallel job, or nightly with a longer fuzztime. Add the missing `FuzzReconcileSafe`. Seeds keep running in `go test`. | FuzzReconcileSafe (the destroy-safety fuzzer) is fuzzed in CI for the first time | | **≈40 s** off the unit job (20 s + 10 s fuzztime + about 10 s of builds) | 15 min | proposed |
| 7 | **`requireSim(t)` before setup in the 12 sim-only tests** | | Fixes a real-leg flake: TestConcurrentCreatesSerialize ran 16 serialized creates under `LockWait=1s` *before* skipping. Skips no longer create datasets. Needed for synctest (a skip inside a bubble reports PASS). | +0.65 s real (run19 setup cost of the skipped tests) | 15 min, done | prototyped; all 12 SKIP without touching a pool |
| 8 | **testing/synctest for TestReconcileLocksTargets** (lock wait on the fake clock) | same assertions | removes the only real-time lock-wait assertion | 0.21 s → 0.00 s (200 reps: 41.8 s → 1.4 s) | 15 min, done | prototyped |
| 9 | **csi mount and block sanity in parallel in the VM** (two processes, separate `csi-<ns>` parents; Ginkgo forbids two in one process) | | | ≈5 s (5.0 s + 5.3 s today) | 15 min | proposed |
| 10 | **Error-classification corpus ratchet** (`dod3` prototype, made unconditional): `testdata/stderr-2.4.jsonl` from `extract_corpus.py` + labels, with a floor that only goes up. Also an **error-provocation table** on the real pool: destroy a busy dataset, create one that exists, and so on, capturing the real stderr. | Classifier correctness is measured (28/162 = 17% of decisive OpenZFS strings today). It replaces TestClassify and FuzzClassify (both zero-unique). The real stderr table finally reaches the NotFound/AlreadyExists branches of zfsErr on the real leg. | | ≈0 | 0.5 day | designed (dod3 prototype exists, env-gated) |
| 11 | **Goroutine leak gate** (`internal/leakcheck`, a dependency-free goleak equivalent, TestMain in endpoint, csi, server, zfs) | | 0 leaks today. A planted leak is caught, with stacks. | 0 on pass (2 s grace only on a leak) | done | prototyped |
| 12 | **Ginkgo `-count` guard**: TestSanity skips when `-test.count>1` | | `go test -count=N ./internal/csi` used to fail outright ("Only -count=1 is allowed"), so the csi package could not be flake-hunted | | done | prototyped |
| 13 | **Prune redundant tests** once 1, 3 and 4 land (list in §1.3): about 16 tests fold into table rows | none lost (each listed row is reproduced) | fewer sim-only skips (−2 budget entries) | real ≈1.5-2 s (TestThickCloneRetry 0.30, TestCloneFromVolume…/AfterItsClone 0.33, TestReconcileCollectsAbandonedCreate 0.17, …) | 0.5 day | proposed |
| 14 | **Real-pool hygiene**: a per-process run root `pool/zfsep-<pid>-<ns>/…` destroyed in TestMain, a sweep of stale run roots, a 255-byte name-length guard, and smaller thick zvols (or a semaphore for "thick" tests) before raising parallelism | | no leaked datasets on developer pools; no ENOSPC from parallel thick reservations | | 2 h | proposed |

The fourth table, the error-classification ratchet (#10), was already prototyped in `dod3/err-copy` (env-gated). Of the four, I built crash injection, golden plans and property round-trip.

---

## 1. Inventory

### 1.1 Method (mechanical: `suite-tools/inventory.py`)

1. `go test -json -count=1 ./...` gives the wall time per top-level test on the simulator.
2. One cover binary per package: `go test -c -cover -covermode=set -coverpkg=<all production packages>`. The coverpkg excludes `gen/`, the simulator `zfstest` and, in the after-tree, `leakcheck`.
3. Each test runs alone, `<bin> -test.run '^T$' -test.coverprofile=…` (98 profiles), from its package directory.
4. **Unique statements** are statements covered by exactly one test across the whole suite. **Unique on the real leg** counts the same thing among only the tests that run on a real pool. **Real-reachable** is the union of the tests that run on a real pool, executed on the simulator. That is an upper bound on what the VM leg can cover.
5. **Runs on** is read from the source:
   - harness use: `setup`/`setupPrefix`/`endpointClient`, directly or through a test helper;
   - sim-only markers: `h.Sim(t)`, `failOn`, `requireSim`;
   - `h.NFS(t)`;
   - the VM package list `pkgs=(endpoint csi)` in `ci/real-zfs-vm.sh`.

   The result is cross-checked against `ci/expected-skips.txt`: 0 mismatches.
6. The invariant is the only hand-written column (`suite-tools/invariants.json`), together with a class key used to group same-invariant tests. Real seconds come from `run19-vm.log` (blank means the test is newer than run19, or never runs on real).

`runs` legend:
- `both`: runs on the sim and on the real pool.
- `both(nfs)`: also needs nfsd.
- `sim`: skips on a real pool (in the skip budget).
- `sim*`: inside the real test binary but never touches the pool (pure planner or private Sim), so it passes on the VM while measuring the simulator.
- `unit`: in no real-ZFS binary.

Totals at HEAD:
- 94 tests: 28 `both`, 12 `sim`, 36 `sim*`, 18 `unit`.
- 1793 production statements, 1550 covered on the simulator (86.4%). **Real-reachable is 1335 (74.5%).**
- 46 of 94 tests have zero unique statements.

After the prototypes:
- 98 tests, 1813 statements (+20 for `Plan.String`), 1575 on the simulator (86.9%).
- **Real-reachable is 1385 (76.4%).**

### 1.2 Table (HEAD)

| pkg | test | runs | sim s | real s | stmts | unique | unique on real leg | class | invariant | redundant? |
|---|---|---|---|---|---|---|---|---|---|---|
| cmd/zfs-endpoint | TestLockParentIsExclusive | unit | 0 |  | 26 | 10 |  | process-singleton | one endpoint process per parent (flock) |  |
| csi | TestCapabilitiesAdvertised | both | 0.01 | 0.06 | 92 | 0 | 0 | csi-surface | controller advertises exactly the supported RPC set |  |
| csi | TestExpandNodeExpansionRequired | both | 0 | 0.35 | 494 | 0 | 0 | csi-expand | node expansion only for a zvol carrying a filesystem | yes |
| csi | TestExpandUnlimitedFilesystem | both | 0.01 |  | 460 | 2 | 3 | csi-expand | expand of an unlimited fs reports the requested capacity |  |
| csi | TestNeverIssuedIDs | both | 0 |  | 275 | 9 | 9 | csi-ids | ids never issued are NotFound (Delete: OK), never InvalidArgument |  |
| csi | TestParametersAndAccessChecks | both | 0.08 | 0.29 | 517 | 17 | 19 | csi-params | StorageClass parameters and access modes are validated |  |
| csi | TestSanity | both | 0.48 | 4.97 | 850 | 63 | 80 | csi-spec | csi-sanity Identity + Controller specs pass |  |
| endpoint | FuzzCreateConverges | sim* | 0 | 0.00 | 468 | 1 |  | create-idempotency | accepted creates converge; retry mutates nothing (fuzz) |  |
| endpoint | FuzzReconcileSafe | sim* | 0.02 | 0.00 | 265 | 0 |  | reconcile-safety | reconcile never targets kept rows and converges (fuzz) |  |
| endpoint | TestApplyRefusesAnUnknownOpKind | sim* | 0 |  | 56 | 1 |  | apply-safety | unknown op kind refuses the whole plan before any op |  |
| endpoint | TestBusyIsMappedByOperation | sim | 0 |  | 576 | 3 |  | error-mapping | busy maps by op: delete FailedPrecondition, else Unavailable |  |
| endpoint | TestCloneFailureAfterCloneConvergesToSameState | sim | 0.17 |  | 540 | 0 |  | crash-convergence | failure after clone converges to the uninterrupted end state | yes |
| endpoint | TestCloneFromVolumeReleasesInternalSnapshot | both | 0 | 0.33 | 480 | 0 | 0 | clone-source-lifecycle | volume clones release their internal snapshot | yes |
| endpoint | TestCloneLargerThanLimitIsOutOfRange | both | 0.01 |  | 497 | 2 | 3 | create-capacity | a clone larger than limit is OutOfRange before any op |  |
| endpoint | TestCloneNeverAdoptsARacingDataset | sim* | 0 |  | 495 | 1 |  | ownership | a clone never adopts a dataset created in the race window |  |
| endpoint | TestCloneResizeFailureConverges | sim | 0 | 0.18 | 512 | 0 |  | crash-convergence | failure between clone and resize converges on retry | yes |
| endpoint | TestCloneSourcesMustBeOwned | both | 0 |  | 453 | 0 | 0 | ownership | clone sources must be owned, committed, CSI | yes |
| endpoint | TestConcurrentCreatesSerialize | sim | 0 | 0.31 | 392 | 0 |  | locking | concurrent identical creates run zfs create once |  |
| endpoint | TestCreateVolumeIdempotent | both | 0 | 0.18 | 416 | 1 | 2 | create-idempotency | identical create is a no-op; incompatible is AlreadyExists |  |
| endpoint | TestCreateVolumeProperties | both | 0 | 0.93 | 580 | 0 | 52 | props-e2e | properties applied, read back, compared on retry, converged |  |
| endpoint | TestCreateVolumeResumesInterruptedProvision | both | 0 | 0.19 | 371 | 0 | 0 | create-resume | an uncommitted provision is hidden then resumed | yes |
| endpoint | TestDefaultPropertyPrefixIsOurOwn | sim* | 0 |  | 371 | 0 |  | prefix | default property prefix is zfs-endpoint | yes |
| endpoint | TestDeleteVolume | both | 0 | 0.23 | 493 | 0 | 0 | delete-semantics | delete is idempotent and refuses while CSI snapshots exist | yes |
| endpoint | TestDeleteVolumeAfterItsCloneIsGone | both | 0 |  | 458 | 0 | 0 | clone-source-lifecycle | internal clone-source snapshot does not block delete | yes |
| endpoint | TestDeleteVolumeSparesForeignSnapshots | sim | 0 |  | 497 | 2 |  | ownership | foreign snapshots block delete and reconcile unless opted in |  |
| endpoint | TestExpand | both | 0 | 0.21 | 420 | 0 | 0 | expand | expand grows, never shrinks, NotFound when missing | yes |
| endpoint | TestExportConverges | sim* | 0 | 0.00 | 157 | 0 |  | export-plan | publish/unpublish twice converges to an empty plan | yes |
| endpoint | TestFileExportAdoptedVolumes | both(nfs) | 0 | 0.25 | 377 | 0 | 1 | export-adoption | inherited sharenfs is pinned; foreign local sharenfs refused |  |
| endpoint | TestFileExportLifecycle | both(nfs) | 0 | 0.31 | 582 | 0 | 44 | export-e2e | publish/unpublish reaches the kernel table, idempotently |  |
| endpoint | TestFileExportPreconditions | both | 0 | 0.30 | 479 | 3 | 18 | export-input | export refuses zvols, unmounted, missing, bad hosts, disabled |  |
| endpoint | TestFileExportVerifiesKernelTable | sim | 0 | 0.06 | 530 | 2 |  | export-verify | publish/unpublish verify etab, not zfs set's exit code |  |
| endpoint | TestInheritedOwnershipIsNotOwnership | both | 0 | 0.19 | 427 | 0 | 2 | ownership | inherited user props are not ownership | yes |
| endpoint | TestMissingParentIsUnavailableForWholeScans | sim | 0 |  | 189 | 7 |  | missing-parent | whole-parent scans map a missing parent to Unavailable |  |
| endpoint | TestMissingParentIsUnavailableNotAbsent | sim | 0 | 0.05 | 461 | 3 |  | missing-parent | missing parent is Unavailable, not NotFound/absent |  |
| endpoint | TestNeverAdoptsOrDeletesUnmanaged | both | 0 | 0.13 | 331 | 0 | 1 | ownership | unmanaged datasets are never adopted or deleted | yes |
| endpoint | TestOpKindString | sim* | 0 |  | 19 | 0 |  | diagnostics | op kinds render stable names |  |
| endpoint | TestPagination | both | 0 | 1.03 | 405 | 0 | 1 | pagination | list pages in order; bad token Aborted |  |
| endpoint | TestParseClient | sim* | 0 | 0.00 | 47 | 2 |  | export-input | NFS client specs are parsed or InvalidArgument |  |
| endpoint | TestParseEtab | sim* | 0 | 0.00 | 47 | 10 |  | export-etab | etab lines parse to client lookups |  |
| endpoint | TestPlanCreateAppliesProperties | sim* | 0 | 0.00 | 197 | 1 |  | props-apply | validated properties are planned at create |  |
| endpoint | TestPlanCreateConvergesUncommittedClone | sim* | 0 | 0.00 | 104 | 0 |  | create-resume | an uncommitted clone is driven to size, reservation, commit | yes |
| endpoint | TestPlanCreateFilesystemsAreNotShared | sim* | 0 | 0.00 | 141 | 0 |  | export-default | new filesystems are created unshared |  |
| endpoint | TestPlanCreateHonorsLimitOnly | sim* | 0 | 0.00 | 82 | 0 |  | create-capacity | limit-only request sets a quota | yes |
| endpoint | TestPlanCreateIsEmptyWhenSatisfied | sim* | 0 | 0.00 | 93 | 0 |  | create-idempotency | a satisfied create plans nothing | yes |
| endpoint | TestPlanCreateMalformedSourceIsNotFound | sim* | 0 | 0.00 | 99 | 1 |  | create-source | unissuable content-source ids are NotFound |  |
| endpoint | TestPlanCreatePropertiesConvergeUncommitted | sim* | 0 | 0.00 | 141 | 2 |  | create-resume | uncommitted rows are driven to the requested properties |  |
| endpoint | TestPlanCreatePropertiesDenyList | sim* | 0 | 0.00 | 55 | 1 |  | props-allowlist | properties outside the allowlist/kind are InvalidArgument |  |
| endpoint | TestPlanCreatePropertiesRetry | sim* | 0 | 0.00 | 151 | 1 |  | props-compat | retry compatibility compares local canonical values |  |
| endpoint | TestPlanCreateRefusesForeignAndIncompatible | sim* | 0 | 0.00 | 105 | 0 |  | create-compat | foreign or incompatible existing rows are AlreadyExists |  |
| endpoint | TestPlanCreateThickZvolLeavesReservationToZFS | sim* | 0 | 0.00 | 81 | 0 |  | create-capacity | thick zvols keep zfs's auto refreservation | yes |
| endpoint | TestPlanDeleteVolume | sim* | 0 | 0.00 | 79 | 0 |  | delete-semantics | delete refuses snapshotted/cloned volumes, defers clone sources | yes |
| endpoint | TestPlanExpand | sim* | 0 | 0.00 | 69 | 0 |  | expand | expand rounds to volblocksize; unlimited fs gets no quota | yes |
| endpoint | TestPlanPublishExport | sim* | 0 | 0.00 | 161 | 0 |  | export-plan | publish plans the minimal sharenfs set or refuses | yes |
| endpoint | TestPlanReconcile | sim* | 0 | 0.00 | 195 | 3 |  | reconcile-plan | reconcile collects exactly the undesired/uncommitted owned, old rows |  |
| endpoint | TestPlanReconcileHeldLimitsActions | sim* | 0 | 0.00 | 141 | 4 |  | reconcile-locks | actions outside held locks, and dependents, are skipped |  |
| endpoint | TestPlanReconcileLocksEveryTarget | sim* | 0 | 0.00 | 170 | 0 |  | reconcile-locks | every reconcile action locks its volume and served clone | yes |
| endpoint | TestPlanSnapshotLongAndUnusualNames | sim* | 0 | 0.00 | 73 | 1 |  | snapshot-names | long/unusual names hash to a valid <=128-byte id, stably |  |
| endpoint | TestPlanSnapshotReservedPrefix | sim* | 0 | 0.00 | 41 | 1 |  | snapshot-names | internal clone-source prefix is reserved |  |
| endpoint | TestPlanUnpublishExport | sim* | 0 | 0.00 | 162 | 2 |  | export-plan | unpublish plans the minimal sharenfs set or refuses |  |
| endpoint | TestPlannersRequireACoveringTable | sim* | 0 |  | 120 | 9 |  | plan-coverage | planners refuse a table that does not cover what they judge |  |
| endpoint | TestPropertyPrefixIsValidated | sim* | 0 |  | 49 | 3 |  | prefix | invalid property prefixes are refused at startup |  |
| endpoint | TestReceivedCopyIsNotOwned | sim | 0.02 |  | 503 | 0 |  | ownership | received properties are not ownership | yes |
| endpoint | TestReconcileAgainstDesiredSet | both | 0 | 0.76 | 641 | 0 | 22 | reconcile-e2e | reconcile against a desired set collects in dependency order | yes |
| endpoint | TestReconcileCollectsAbandonedCreate | both | 0 | 0.17 | 403 | 0 | 3 | reconcile-e2e | abandoned creates collected when old; dry run applies nothing | yes |
| endpoint | TestReconcileCollectsLeakedCloneSource | both | 0 | 0.36 | 516 | 0 | 8 | reconcile-e2e | leaked clone-source snapshots collected, used ones kept | yes |
| endpoint | TestReconcileLocksTargets | sim | 0.20 | 0.05 | 550 | 6 |  | reconcile-locks | reconcile aborts on a held lock; dry run takes none |  |
| endpoint | TestReconcileSparesReceivedAndRenamed | sim | 0 |  | 508 | 0 |  | ownership | reconcile spares received and renamed datasets | yes |
| endpoint | TestReserveRestoreIsIdempotent | both | 0 |  | 521 | 1 | 2 | create-idempotency | reserve-restores reserve the source size, retry no-op |  |
| endpoint | TestScanRecordsCoverage | sim* | 0 |  | 222 | 0 |  | plan-coverage | scans record exactly which ids they cover | yes |
| endpoint | TestShareNFSAndClientEncoding | sim* | 0 | 0.00 | 77 | 2 |  | export-encoding | sharenfs/client encoding is canonical and round-trips |  |
| endpoint | TestSimulateDestroyReapsDeferredOrigins | sim* | 0 | 0.00 | 40 | 0 |  | reconcile-plan | deferred destroys reap when the last clone goes | yes |
| endpoint | TestSnapshotLifecycleWithClones | both | 0 | 0.68 | 559 | 0 | 1 | snapshot-lifecycle | snapshot ids/global names, restore with growth, deferred delete |  |
| endpoint | TestThickCloneRetryIsIdempotent | both | 0 | 0.30 | 447 | 0 | 0 | create-idempotency | thick clone retry succeeds | yes |
| endpoint | TestUnknownFailureAfterAMutationIsRetryable | sim | 0 |  | 414 | 0 |  | error-mapping | unknown error after a mutation is Aborted, before Internal | yes |
| endpoint | TestValidatePropertyValues | sim* | 0 | 0.00 | 57 | 5 |  | props-canon | allowlisted values canonicalise to what zfs prints |  |
| endpoint | TestVolumeNameMustMatchID | both | 0 |  | 338 | 1 | 1 | ownership | markers naming another volume are not served |  |
| endpoint | TestZfsErrCodes | sim* | 0 |  | 39 | 7 |  | error-mapping | each zfs error Kind maps to its CSI code |  |
| server | TestFileExport | unit | 0.02 | 0.00 | 554 | 7 |  | grpc-surface | gRPC file export gated by capability |  |
| server | TestReconcile | unit | 0 | 0.00 | 461 | 11 |  | grpc-surface | gRPC reconcile: capability, young skip, input validation |  |
| server | TestRoundTripAndCodes | unit | 0.26 | 0.00 | 601 | 3 |  | grpc-surface | gRPC maps endpoint codes and fields |  |
| unixsock | TestListenIsOwnerOnly | unit | 0.26 |  | 7 | 0 |  | unixsock | socket is owner-only | yes |
| unixsock | TestListenNeverDeletesANonSocket | unit | 0 |  | 4 | 1 |  | unixsock | a non-socket path is never deleted |  |
| unixsock | TestListenRefusesALiveSocket | unit | 0.08 |  | 10 | 2 |  | unixsock | a live socket is not replaced |  |
| unixsock | TestListenReplacesAStaleSocket | unit | 0 |  | 9 | 1 |  | unixsock | a stale socket is replaced |  |
| zfs | FuzzClassify | unit | 0 | 0.00 | 4 | 0 |  | zfs-classify | classify returns a valid Kind for any text (fuzz) | yes |
| zfs | FuzzParseColumns | unit | 0 | 0.00 | 28 | 1 |  | zfs-parse | parser output shape is consistent (fuzz) |  |
| zfs | TestClassify | unit | 0 | 0.00 | 26 | 0 |  | zfs-classify | known stderr texts classify to their Kind | yes |
| zfs | TestExistsDoesNotSwallowUnknownErrors | unit | 0 | 0.00 | 34 | 1 |  | zfs-classify | only a classified not-found is absent |  |
| zfs | TestKilledCommandIsInterrupted | unit | 0.28 |  | 45 | 18 |  | zfs-runner | a killed command is Interrupted with the ctx error, bounded |  |
| zfs | TestLifecycleAgainstSim | unit | 0 | 0.00 | 119 | 25 |  | sim-fidelity | client create/get/snapshot/clone/defer-destroy/list on sim |  |
| zfs | TestParseColumnsRejectsForgedLines | unit | 0 | 0.00 | 26 | 1 |  | zfs-parse | duplicate (forged) lines fail the scan |  |
| zfs | TestPropLocalIsOnlyLocal | unit | 0 |  | 1 | 0 |  | ownership | only source=local is local | yes |
| zfs | TestRejectsFlagLikeAndShellNames | unit | 0 | 0.00 | 12 | 3 |  | zfs-argv | flag-like/shell names and newline values refused |  |
| zfs | TestSimPrintsIndexPropertiesLikeOpenZFS | unit | 0 |  | 62 | 0 |  | sim-fidelity | sim prints index props like OpenZFS 2.4 | yes |

Notes on reading it:
- A zero in "unique" does not mean "useless". Statement coverage cannot see an assertion. TestListenIsOwnerOnly checks permission bits, and the sim-fidelity tests cover `zfstest`, which is excluded from coverpkg on purpose.
- "unique on real leg" is the better number for `both` tests. Example: TestCreateVolumeProperties has 0 unique statements suite-wide but 52 on the real leg, because it is the only real-pool test of property application.
- After parallelisation, `go test -json` Elapsed for a `t.Parallel` test **excludes its paused time**. For example, TestPropertyRoundTrip reports 0.04 s inside a package that took 8.6 s under the model. Use package Elapsed for totals.

### 1.3 Redundant tests (no unique statements and the same invariant), after review

The mechanical flag (zero unique + another test in the same class) marks 35 tests. After reading them:

| Fold | Into | Why it is redundant |
|---|---|---|
| TestDeleteVolumeAfterItsCloneIsGone | TestCloneFromVolumeReleasesInternalSnapshot | Same steps (clone from volume, delete clone, delete source). The second test also asserts the snapshot is gone. |
| TestPlanCreateIsEmptyWhenSatisfied, …HonorsLimitOnly, …ThickZvolLeavesReservationToZFS, …ConvergesUncommittedClone, …RefusesForeignAndIncompatible, …MalformedSourceIsNotFound, …FilesystemsAreNotShared, TestPlanDeleteVolume, TestPlanExpand, TestPlanSnapshotReservedPrefix | golden rows `create/*`, `restore/*`, `delete/*`, `expand/*`, `snapshot/*` | each is one or a few (Table, request) → plan or code cases; all are reproduced as rows in `plans.golden` |
| TestSimulateDestroyReapsDeferredOrigins, TestPlanReconcileLocksEveryTarget | golden `reconcile/*` rows plus a per-row invariant check | 0 unique; the lock-cover invariant should run over every reconcile row, not one fixture |
| TestThickCloneRetryIsIdempotent | crash table `clone-volume-zvol-thick` (uninterrupted + idle re-run = 0 mutations) | 0 unique on both legs; the crash row asserts more (0 mutations, not just no error) |
| TestCloneResizeFailureConverges, TestCloneFailureAfterCloneConvergesToSameState | crash table `restore-zvol-grow`, `restore-fs-reserve`, `clone-volume-fs-reserve` (k1/k2 points) | the same convergence, but the crash table runs on the real pool too; −2 skip-budget entries |
| TestReconcileCollectsAbandonedCreate | add an abandoned row to TestReconcileAgainstDesiredSet's setup | 0 unique; 3 unique on the real leg come from the dry run, which the larger test can assert too |
| TestScanRecordsCoverage | TestPlannersRequireACoveringTable | 0 unique; one invariant ("tables know what they cover") |
| TestClassify, FuzzClassify | the corpus ratchet (#10) | 0 unique; the 16 hand-picked strings are a subset of the corpus |

Not redundant despite the flag:
- TestCreateVolumeProperties: 52 statements unique on the real leg.
- TestReconcileAgainstDesiredSet: 22 unique on the real leg.
- TestFileExportLifecycle: 44 unique on the real leg.
- TestInheritedOwnershipIsNotOwnership: real inheritance, which the simulator only models.
- TestReceivedCopyIsNotOwned and TestReconcileSparesReceivedAndRenamed: different entry points of the D05 ownership predicate. They could become one table: entry point × foreign kind.
- TestUnknownFailureAfterAMutationIsRetryable: asserts codes, which the crash table does not. Better: add an expected-code column to the crash table (`k=0, before` → Internal, otherwise Aborted) and then fold it.

### 1.4 Slow tests

| Where | Test | Time | Cause | Fix |
|---|---|---|---|---|
| sim | TestSanity | 0.29-0.46 s | 44 Ginkgo specs over gRPC | fine |
| sim | TestKilledCommandIsInterrupted | 0.25 s | real `sh` child: 50 ms ctx plus 200 ms WaitDelay | could cut WaitDelay to 50 ms (100/100 green at load 30 as is) |
| sim | TestReconcileLocksTargets | 0.21 s | LockWait 200 ms of real time | **synctest: 0.00 s** (#8) |
| real | TestSanity ×2 | 5.0 + 5.3 s | sequential mount, then block | run both concurrently (#9) |
| real | TestPagination | **1.03 s** | the latency model predicts 0.37 s (3 creates, 3 pages); unexplained | have the real leg record per-call durations (§2.3) |
| real | TestCreateVolumeProperties | 0.93 s | 47 zfs calls | fine; the round-trip table makes most of it redundant on real |
| real | TestReconcileAgainstDesiredSet | 0.76 s | 48 calls | fine |
| real | TestSnapshotLifecycleWithClones | 0.68 s | 33 calls, 4 G of thick zvols | shrink zvols |
| CI | fuzz smoke | 40.2 s | fixed `-fuzztime 20s` + `10s` + 2 instrumented builds | #6 |

### 1.5 Invariants with no test (or none on the real leg)

From the merged profiles (`suite-data/inv-*/uncovered-funcs.txt`, `merged-real.cov`) and reading the code:

1. **Crash convergence of DeleteVolume of a clone.** Untested before, and it **fails** (crash table, xfail). The planner only looks at the target volume: once `c` is gone, `PlanDeleteVolume(c)` is empty, so the internal `src@volume-source-for-volume-c` is left for Reconcile's orphan rule after `MinAge`. Possible fix: a delete of an absent id also plans `destroy -d <any>@volume-source-for-volume-<id>`. DeleteVolume already scans the whole parent, so the row is in the table.
2. **`internal/lock` has no test at all.** Untested:
   - sorted acquisition (no deadlock between overlapping key sets);
   - release of partially taken keys when the context ends;
   - release idempotence;
   - `Held`, which is 0% covered.

   A synctest table is natural here: rows of (held keys, requested keys, deadline) → (acquired?, held afterwards).
3. **`lockMargin`**: a busy resource is reported Aborted at least 1 s before the caller's deadline. The deadline branch of `Endpoint.lock` is untested (lock() is 80% covered). This is synctest-friendly.
4. **Error mapping on the real leg**: zfsErr and opErr were at 0% real-reachable, and `parentUnavailable` still is. The NotFound branch of zfsErr (`endpoint.go:280`) is reached by **no** test, even on the simulator.
5. **`reshare`** (export.go:312, the case where the kernel table lags sharenfs) is 0% real-reachable. It is sim-only via SetNFSDown.
6. **`server.grpcErr`** is 66.7% covered, and no test pins the whole mapping. It needs a table: every endpoint.Code → gRPC code, which is a 12-row literal.
7. **`zfs.Client.Inherit` and `Promote` are 0% everywhere**: either dead code or untested API.
8. **`cmd/zfs-endpoint` `main`/`run`/`listener` are 0%**, and `cmd/zfs-csi` has no test file. Flag validation, socket ownership and startup refusals are unmeasured.
9. **The simulator has no conformance test against real ZFS.** The property round-trip table is the first differential (sim vs real, same rows). The same pattern should cover `zfs get` output for every row type.
10. Still open from mcnamara §1.3: scale (10k datasets), latency against the 10 s sidecar deadline, suspended pools, and the matched 2.2 stack.

---

## 2. Speed

### 2.1 Where the time goes

CI, from the run19 logs. Both jobs run in parallel, so the VM job is the critical path:

| Job / step | Wall | Of which tests run |
|---|---|---|
| unit: gofmt + tidy | 2.2 s | |
| unit: `go vet ./...` | 10.4 s | |
| unit: `go test -race -count=1 ./...` | 30.0 s | ≈1.0-1.3 s per package (sum ≈ 5.7 s including process start); **the rest is race-instrumented compile and link** |
| unit: fuzz smoke (2 targets) | 40.2 s | 30 s fixed fuzztime + 2 fuzz-instrumented builds |
| **VM: `apt-get update && install qemu`** | **211 s** | |
| VM: image download + verify + resize | ≈13 s | |
| VM: boot + cloud-init (test binaries build meanwhile) | ≈50 s | |
| VM: endpoint tests on real ZFS | 7.2 s | 17 PASS + 5 SKIP in run19; **sum of per-test times 6.55 s** |
| VM: csi mount (sanity + 4 controller tests) | 5.7 s | TestSanity 4.97 s (44 of 103 specs) |
| VM: csi block (sanity only) | 5.3 s | |

Locally (`suite-tools/measure.py`, `suite-data/measure-*.jsonl`). Cold means an empty GOCACHE; CPU seconds are given because the host is shared:

| Phase | CPU s | Wall s |
|---|---|---|
| compile all test binaries, cold | 150.4 | 149.6 |
| `go test ./...` cold | 146.3 | 190.0 |
| `go vet ./...` cold | 143.3 | 232.3 |
| race compile, cold | 181.8 | 127.3 |
| `go test -race ./...` cold | 176.7 | 184.9 |
| `go test ./...` warm (3 reps) | 4.9-5.5 | 2.5-7.3 |
| `go test -race ./...` warm (3 reps) | 7.1-8.5 | 5.2-10.0 |
| fuzz build overhead, warm (`-fuzztime 1x` both) | 1.3 | 1.7-2.2 |
| summed in-test time on the simulator (all packages) | | ≈2 s |

So:
- **Compilation is ≈98% of a cold run.** The dependencies are grpc, protobuf, Ginkgo and csi-test. Race instrumentation adds ≈20% CPU to the compile, and test execution on the simulator is ≈2 s.
- setup-go's GOCACHE restore makes CI mostly warm, yet the race step still takes 30 s. Ginkgo, grpc and protobuf are relinked into 6 race test binaries.
- csi-sanity costs 0.3-0.5 s on the simulator and ≈10.3 s on real ZFS (two runs).

### 2.2 Parallelism

**On the simulator: yes, per harness.**
- Every `setup(t)` builds its own `zfstest.New("tank")`.
- Package-level variables are read-only tables (`propRules`, `opNames`, `patterns`, `indexTables`, …).
- The only process-global state is Ginkgo, which lives only in TestSanity, and that test stays sequential.

I added `t.Parallel()` to 43 tests and subtests in the copy. `go test -race -shuffle=on -count=20 ./...` is green for every package.

**On a real pool: yes, and names are already isolated.**
- Endpoint parents are `pool/ep-<sanitised t.Name()>-<UnixNano>`, and csi parents are `pool/csi-<UnixNano>`.
- These names are unique per test, per subtest (the `/` becomes `_`) and per rerun.
- Every query the tests assert on is scoped to its own parent.

The shared resources are:
- **Pool space.** Thick zvols reserve space: TestSnapshotLifecycleWithClones holds about 4 G (src 1 G, other 1 G, restored 2 G), TestExpand 3 G and TestThickCloneRetryIsIdempotent 2 G. On a 10 G pool, running them together under `-parallel 8` can hit ENOSPC. Shrink them to MiB, or run "thick" tests under a semaphore.
- **The kernel NFS export table.** Each test looks up only its own path, and libzfs serialises writes to `/etc/exports.d/zfs.exports`.
- **The `zfs` command's global locks** (spa_namespace, txg sync). These reduce speedup but do not break isolation.

Gotcha: the VM has `-smp 2`, so `-test.parallel` defaults to 2. The tests are exec-bound, so pass `-test.parallel=8` explicitly.

### 2.3 Latency model and before/after

I can't run a real pool here, so `suite-tools/calibrate.py` builds a model:
1. It counts zfs calls per endpoint test on the simulator (`ZFS_TEST_CALLS=1` in the copy's harness).
2. It fits `real_s = a + b·get + c·mutation` to run19's 17 real-pool PASS times. Result: get/list ≈ **20.1 ms**, mutation ≈ **11.3 ms**, intercept 49 ms, R² = 0.59. TestPagination is the outlier.
3. `ZFS_TEST_LATENCY=20000,11000` then makes the harness sleep that long per call.

The sleep model assumes zfs calls overlap perfectly, so read real speedups as upper bounds. Results for the endpoint package, `GOMAXPROCS=2` as in the VM, two runs each (`suite-data/model-vm.txt`):

| Configuration | Package s |
|---|---|
| before (HEAD tests, default parallelism) | 13.83 / 13.87 |
| after, same tests, `-parallel=2` | 7.05 / 7.48 |
| after, same tests, `-parallel=8` | **1.88 / 2.09** |
| + property round-trip table (179 rows) | 5.56 / 6.32 |
| + crash table (93 runs) | 5.67 / 6.86 |
| after, everything | 9.33 / 11.04 |

Scaled to run19's 7.2 s real endpoint step (sim-only tests skip there):
- The same tests drop to about **1-2 s**.
- With both new tables the step is about **5-6 s**, so it is still faster than today while running 272 more real-pool cases.

On the simulator, the endpoint package runs 320 more cases (crash 93 runs, round-trip 179, golden 48) for about +0.3 CPU-s (0.61-0.75 → 0.91-1.03). Wall time is noise.

### 2.4 synctest

`grep -n 'time.Sleep\|LockWait' **/*_test.go` at HEAD finds:
- **No `time.Sleep` in any test.**
- `LockWait: time.Second` in the harness and in 4 ad-hoc endpoints (`later()`, foreign, export-off).
- `LockWait: 200ms` in TestReconcileLocksTargets. This is the only test that actually waits for a lock timeout.

Converting TestReconcileLocksTargets (`synctest.Test(t, reconcileLocksTargets)`):
- The parked runner blocks on bubble channels, and `lock.Keyed` waits on bubble channels and a bubble context timer. The fake clock therefore jumps the 200 ms.
- Time drops from 0.21 s to 0.00 s, and 200 iterations from 41.8 s to 1.4 s.
- `-race -count=50` is green.

Two limits, the second verified with a probe in `suite-data/synprobe`:
- **Never wrap exec.** TestKilledCommandIsInterrupted and anything on a real pool block in syscalls, which are not durably blocking, so the bubble clock never advances.
- **`t.Skip` inside `synctest.Test` reports `--- PASS`, not `--- SKIP`.** A sim-only test inside a bubble would have passed silently on the real leg, and `ci/check-skips.sh` would then have failed with "allowed skip did not skip". This is why `requireSim(t)` must run *before* the bubble, and it is the reason for change #7.

Good next synctest targets are the lock package table, the `lockMargin` deadline rule, and the default 30 s LockWait path. None of them have tests today.

---

## 3. Validity: invariants as rows

### 3.1 Golden plans (prototyped)

`Plan.String()` was added to `plan.go` in the copy, and it is useful for logs too. It prints one op per line in the form `kind target [from=] [size=] [+sparse +recursive +defer +ok-exists +ok-missing] k=v…`, with properties sorted. `TestGoldenPlans` has 48 rows of (name, `[]Row` fixture, planner call) and writes `## name\n<plan | error: Code | (empty)>` to `testdata/plans.golden`:
- `go test -run TestGoldenPlans -update` rewrites the file.
- A mismatch prints only the rows that changed.
- Each row is planned three times and must render identically.

It runs in 0.00 s with 21 unique statements. The golden file already shows decisions no assertion pinned before:
- `create/fs-props` canonicalises `compression=gzip-6` to `gzip`.
- `create/fs-props-xattr-on` is refused.
- `delete/clone-releases-source` is two ops, so a crash window exists between them.
- `publish/second-ro` writes `nfs_clients` and `sharenfs` in a single `set`.

Each new planner invariant now costs one row and one reviewed diff.

### 3.2 Crash injection (prototyped)

`faultRunner` wraps the harness runner: the simulator, or `zfs.LocalRunner` on a pool. It counts mutating calls (anything but get/list). At call k it either:
- **before**: returns a lost-answer error without running the call;
- **after**: runs the call, then loses the answer.

Each of the 17 scenarios then goes through these steps:
1. Set up the scenario.
2. Run the op with the crash.
3. Restart with a fresh `endpoint.New`, which has no locks and no memory.
4. Retry, up to 3 times. The retry must succeed.
5. Re-run the op once more through a counting runner: it must make **0 mutations**.
6. Compare a **fingerprint** with the uninterrupted run. The fingerprint is `zfs get` of type, volsize, refquota, refreservation, origin, sharenfs, compression and recordsize, plus the user properties with their source, for everything under the parent, relative and sorted.

The scenarios are:
- create fs, fs+reserve, fs+props, zvol sparse/thick;
- restore fs grow/reserve, zvol grow;
- clone from volume fs+reserve, zvol thick;
- snapshot, expand;
- delete, delete of a volume clone, delete of a restored clone;
- publish, reconcile.

Results:
- 76 crash points plus 17 uninterrupted runs. On the simulator this takes 0.13 s, and 1.7-2.7 s under `-race`.
- **74 converge; 2 do not**: `delete-clone-releases-source` at k0-after and k1-before (§1.5 #1).
- Known divergences go in an `xfail` map. A listed point that starts converging fails the test, so the list can only shrink.
- xfail logs `XFAIL` and passes rather than skipping, so the real leg's skip budget is untouched.

Because the table goes through the harness, it runs on the real pool with no simulator hooks. That is the first time the error mapping runs on the real leg: opErr 0% → 85.7% and zfsErr 0% → 35.3% real-reachable, with 46 statements unique to the real leg.

Next steps:
- An expected-code column (Internal before the first mutation, Aborted after).
- A real-only **during** mode: cancel the context mid-exec, so LocalRunner SIGTERMs zfs. This is the production kill path, and only a real pool has it.

### 3.3 Property round trip (prototyped)

The allowlist is closures, so the table generates its rows by probing `ValidateProperties` with every value name in OpenZFS 2.4's zfs_prop.c index tables, the 2.2 aliases, and every power of two from 512 B to 16 M, in both kinds. That yields **179 rows**:

| Property | Rows |
|---|---|
| compression | 114 |
| recordsize | 31 |
| volblocksize | 17 |
| acltype, dedup, sync | 6 each |
| logbias | 4 |
| copies, snapdir | 3 each |
| atime, relatime, xattr | 2 each |

A guard test fails if any allowlisted key yields no rows.

Each row, as a parallel subtest under one parent:
1. Creates `p<i>` with `{k: v}`.
2. Reads the property back. The value must equal the endpoint's canonical form, with source `local`.
3. Retries the identical request, which must succeed.

The first run corrected the oracle: volblocksize's source is `-` (or `default` at 16K), never `local`. The simulator agrees, and real ZFS should confirm it. On the simulator this is a differential between the endpoint's tables and the simulator's copy of zfs_prop.c. On the VM it is ground truth for every value, and the same rows on a 2.2 leg would catch the `xattr`/`snapdir` drift (props F1/F3).

### 3.4 Error-classification corpus ratchet (designed; a prototype exists in dod3)

Inputs:
- `dod3/tools/extract_corpus.py` turns an OpenZFS checkout into `corpus.json`: 811 stderr templates with file, line, func and EZFS code.
- `label_and_score.py` adds the intended Kind.

The repo would check them in as `internal/zfs/testdata/stderr-<zfs-version>.jsonl` (id, stderr, intended_kind, in_scope). `TestCorpusRatchet` (`dod3/err-copy/internal/zfs/corpus_ratchet_test.go`) classifies every in-scope row and computes the hit rate. It fails if:
- the hit rate drops below a committed floor (0.17 today; raised with every new pattern, never lowered);
- any row in an explicit `mustClassify` set misses (NotFound, HasClones, HasChildren: the fail-closed absence and destroy blockers).

Changes from the dod3 version:
- Drop its `LABELED`/`CORPUS` env skip, so it always runs.
- Print per-Kind hit counts as test output.

It replaces TestClassify and FuzzClassify, which have 0 unique statements. To bring the classification onto the real leg, add a small **error-provocation table** run through the harness, with rows like:
- (destroy a busy zvol: open it with `dd`) → Busy → FailedPrecondition;
- (create an existing dataset) → Exists;
- (clone into a missing parent) → ParentNotFound → NotFound;
- (set an invalid recordsize) → InvalidProperty.

Each row records real stderr, so the corpus gains observed rows next to the source-read ones.

---

## 4. Reliability: every flakiness source found

| Source | Where | Evidence | Status in the copy |
|---|---|---|---|
| Work before a skip under a 1 s LockWait | TestConcurrentCreatesSerialize ran 16 concurrent creates and only then `h.Sim(t)` skipped. On a real pool, 16 serialized creates (each a scan, about 50-100 ms) can exceed 1 s, so a goroutine gets Aborted and the test **fails** instead of skipping. Run19 shows it took 0.31 s on real before skipping. | | **fixed**: `requireSim(t)` first. All 12 sim-only tests skip before setup (verified with `ZFS_TEST_POOL` set and no zfs binary: 12/12 SKIP) |
| Real-time lock-wait assertion | TestReconcileLocksTargets, 200 ms | 200/200 green under load, but slow | **fixed**: synctest |
| Skip inside a synctest bubble = PASS | any future synctest test | probe | guarded by `requireSim` before the bubble; documented |
| Real-process timing | TestKilledCommandIsInterrupted: 50 ms ctx, 200 ms WaitDelay, 5 s bound | 100/100 green at load ≈30 | keep. It cannot use synctest (exec). |
| Clock-dependent `MinAge` | TestReconcileCollectsAbandonedCreate and server TestReconcile expect "young" with the real clock and a 1 h margin; `later()` uses `time.Now()+48h`. FuzzReconcileSafe uses fake clocks (`sim.Clock`, `Config.Now`). | A guest clock step of more than 1 h during the VM run (NTP after boot) would flip "young". This is unlikely but real. | proposed: inject `Config.Now` and `MinAge` explicitly in the young-path tests, or run them in a bubble |
| Goroutine leaks | grpc servers (`srv.Stop` in Cleanup), the parked runner, zfs children | the new leakcheck finds **0** in endpoint, csi, server and zfs; a planted leak is reported with stacks | **added** `internal/leakcheck` (no goleak dependency) |
| Race detector | CI runs only `-race` | `-race -shuffle=on -count=20 ./...` green; Sim.Calls is read after `wg.Wait`/op return (happens-before) | ok |
| Shared global state: Ginkgo | TestSanity: RunSpecs once per process; Ginkgo rejects `-count>1` for the whole binary | `go test -count=2 ./internal/csi` FAILS at HEAD ("Only -count=1 is allowed") | **fixed**: TestSanity skips when `test.count != 1`. Mount and block stay separate processes. |
| Shared global state: package variables | `propRules`, `opNames`, `patterns`, `defaults`, `indexTables`, `DefaultEtab` | read-only | ok, parallel-safe |
| Map iteration order in assertions | TestCreateVolumeProperties, TestParseClient, TestOpKindString, TestPropLocalIsOnlyLocal and TestMissingParent…WholeScans range over maps, but only to report failures (message order varies; the verdict does not). `propArgs` sorts `-o` args; `List` sorts; reconcile sorts the desired set; `fmt` prints maps sorted. | golden test: every row renders identically 3×; 20 shuffled runs | ok. Golden rows now guard plan determinism. |
| `go test -json` Elapsed for parallel tests | excludes paused time | measured | use package Elapsed (§1.2) |
| Real-pool name collisions across reruns | `ep-<test>-<UnixNano>` and `csi-<UnixNano>` | unique per run and per test | ok |
| Real-pool **leaks** across reruns | Cleanup does not run on `-test.timeout` panic or SIGKILL; datasets (and their NFS exports) stay on a developer pool. CI's VM is disposable. | by reading | proposed: run root `pool/zfsep-<pid>-<ns>` + TestMain destroy + sweep of stale roots |
| Dataset name length | `ep-` + the full subtest name + 19 digits + children (`…@volume-source-for-volume-<id>`) must stay under 255 bytes | the longest today is about 150 | proposed: hash names longer than 64 bytes in `setupPrefix` |
| ENOSPC under parallelism | thick zvols (§2.2) on a 10 G pool | by arithmetic | proposed before `-parallel 8`: MiB sizes or a thick-test semaphore |
| Unexplained real slowness | TestPagination 1.03 s vs 0.37 s modeled | run19 | proposed: a timing runner on the real leg logging calls over 100 ms, as an artifact |
| Gate drift | `current` cloud image; skips pass silently unless listed (`check-skips.sh` now guards this) | mcnamara §2.4 | out of scope here |

---

## 5. Files

- `wave7/suite-tools/inventory.py`: the mechanical inventory (json → per-test coverage → unique/real-unique → table). Run as `python3 inventory.py <repo copy> <out dir> [<vm log>]`.
- `wave7/suite-tools/invariants.json`: the hand-written invariant column and class keys.
- `wave7/suite-tools/measure.py`: cold/warm/race/fuzz phase timing (CPU and wall).
- `wave7/suite-tools/calibrate.py`: fits the per-call latency model from a VM log.
- `wave7/suite-data/inv-before/`, `inv-after/`: `inventory.{md,json}`, per-test profiles (`cov/`), `merged.cov`, `merged-real.cov`, `uncovered-funcs.txt`.
- `wave7/suite-data/measure-*.jsonl`, `model-vm.txt`, `calibration.txt`: the numbers above.
- `wave7/suite-data/suite-copy.diff`: every change in the copy.
- `wave7/suite-data/before-lat/`: a git worktree of the baseline plus only the latency hook, used for before/after timing.
- In the copy:
  - `internal/endpoint/{crash_test.go, golden_plan_test.go, testdata/plans.golden, props_roundtrip_test.go}` are the prototypes.
  - `internal/leakcheck/` and `internal/*/main_test.go` are the leak gate.
  - `plan.go` gains `Plan.String`.
  - `endpoint_test.go` gains `requireSim`, the latency/call-count hooks and the skip-first fix.
  - `reconcile_contract_test.go` holds the synctest change; `sanity_test.go` the count guard.
  - `t.Parallel()` was added across endpoint and csi.
