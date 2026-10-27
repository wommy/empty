# Wave 12 findings ledger (HEAD a02c5b0)

Score = likelihood x blast / cost (1-5 each); anomalies weighted x2. Source: ledger.jsonl (same dir). Read-only; verified against code at HEAD.

## Counts

Total 88 findings (69 open/partial/stale + 19 fixed groups).

| kind | n |
|---|---|
| anomaly | 10 |
| bug | 24 |
| doc | 14 |
| edge | 10 |
| gap | 13 |
| ops | 5 |
| perf | 3 |
| seam | 4 |
| security | 5 |

| status | n |
|---|---|
| fixed | 19 |
| open | 50 |
| partial | 4 |
| stale-claim | 15 |

## 1. Anomalies (ranked first)

| id | score | status | queued | finding | evidence |
|---|---|---|---|---|---|
| AN-01 | 32.0 | open | **none** | FuzzReconcileSafe convergence oracle passes for the wrong reason | internal/endpoint/reconcile_fuzz_test.go:221-226 checks third.Actions (applied) not the plan; perpetually failing groups hide in Skipped 'failed:' |
| AN-02 | 30.0 | open | b-model | Vacuous ownership tests (markers under the wrong prefix) | internal/endpoint/ownership_test.go:36-39,118-130,205-231 set democratic-csi: markers but setup(t) uses prefix zfs-endpoint (endpoint_test.go:55-58) |
| AN-04 | 30.0 | open | **none** | Nightly explore job runs zero tests | .github/workflows/nightly.yml:105 runs -run '^Test(Scale|Explore)'; no such tests exist (grep), job green on zero tests |
| AN-03 | 20.0 | open | b-cache-warm | CI caches never saved on a default-branch scope | .github/workflows/ci.yml:3-5 push only on main; default branch is the PR head and no schedule, so every cache is PR-scoped |
| AN-09 | 16.0 | open | **none** | Recovery FSM labels the normal clone-source snapshot IMPOSSIBLE | rec FSM classifies a@volume-source-for-volume-b as IMPOSSIBLE(managed internal snapshot) at depth 2 (CV(a);CV(b<-a,reserve)); 2770 fs / 1533 block states |
| AN-05 | 4.0 | open | **none** | VM tests ~40% faster after reboot (unexplained) | wave11 timings: VM test phase ~40% faster after a guest reboot; cause unverified |
| AN-06 | 4.0 | partial | b-cache-warm | 12 s gap after image resize; resize time varies 3x | wave11 logs: 12 s silent gap after 'Image resized', qemu-img resize 1.8-5.1 s; S2 removed resize from warm path, cold path unexplained |
| AN-08 | 4.0 | open | **none** | exportfs 3.1.1 double-free (upstream) | exportfs 3.1.1 double-free crash observed in VM (upstream nfs-utils) |
| AN-07 | 2.0 | open | speed-tests (a0f85a15) | TestPagination 3x slower than modelled | TestPagination 1.03 s real vs 0.37 s modelled; unexplained |
| AN-10 | 1.0 | stale-claim | **none** | Mutation kill-rate discrepancy (explained) | mutation kill rate 80.2% (partial run) vs 76.6% (final) -- explained by partial vs full sample |

## 2. Pareto top 20% (11 of 54 open/partial; 188/347 = 54% of total score)

| # | id | score | fix (one line) | lowest-level test (ADR 0005) | queued |
|---|---|---|---|---|---|
| 1 | AN-01 FuzzReconcileSafe convergence oracle passes for the wrong reason | 32.0 | Assert len(plan)==0 AND no Skipped 'failed:' on a dry-run third reconcile | FuzzReconcileSafe (pure, simulator) with a seeded child-dataset/hold corpus entry | **none** |
| 2 | AN-02 Vacuous ownership tests (markers under the wrong prefix) | 30.0 | Build the rows with the endpoint's own prefix (or setup with prefix democratic-csi) and assert each test fails when the guard is removed | Same unit tests + a mutation that deletes the owner check must turn them red | b-model |
| 3 | AN-04 Nightly explore job runs zero tests | 30.0 | Fail the step when 'go test -json' reports 0 tests run (or add the tests) | CI step check: count of pass events > 0 | **none** |
| 4 | AN-03 CI caches never saved on a default-branch scope | 20.0 | Add push on the default branch + a weekly schedule that warms caches | actions cache list after one scheduled run shows a ref=default entry | b-cache-warm |
| 5 | AN-09 Recovery FSM labels the normal clone-source snapshot IMPOSSIBLE | 16.0 | Teach the classifier the stamped clone-source state (ownership: clone_source_for) | rec FSM rerun: zero IMPOSSIBLE states at depth<=2 | **none** |
| 6 | T1 Empty content source id silently creates an empty volume | 15.0 | Return InvalidArgument when the source oneof is set but its id is empty | controller_test: CreateVolume with Snapshot{SnapshotId:""} -> InvalidArgument | b-csi-bugs |
| 7 | E6 Filesystem clone can be smaller than its source | 12.0 | Use max(want, sourceSize) (or OutOfRange) for filesystem clones | plan_test: planCreate fs clone required<source -> refquota>=source | b-csi-bugs |
| 8 | T3 ValidateVolumeCapabilities confirms mount on a block volume | 9.0 | Reject mount access type for zvol-backed volumes | controller_test table: zvol + Mount -> not confirmed | b-csi-bugs |
| 9 | SEAM-DEL-LOCK DeleteVolume releases the source's snapshot without the src lock | 8.0 | Also TryAcquire vol:<src> when releasedOrigin is non-empty | endpoint_test: concurrent CreateVolume(clone of src) vs DeleteVolume -> no destroyed in-use origin | **none** |
| 10 | SCN-06 No type/name agreement check | 8.0 | Reject rows whose type disagrees with '@' presence | table_test decodeColumns table | b-core0 |
| 11 | NFS-D3 Unpublish does not verify coverage removed | 8.0 | Re-read etab and fail if still served | export_test Unpublish with wildcard | nfs-diff builder a9d51a288 (in flight) |

Honest Pareto check: the top 20% (11 rows) holds 54% of score, not 80%. The distribution is flat because most open rows are cost-1 fixes. Reaching 80% of score takes 30 of 54 rows (56%). The 11 rows above are still the best value per unit of writer effort, and 8 of them cost 1. Full ranking: ledger.jsonl sorted by score.

## 3. Not covered by any queued item

| id | score | batch | finding |
|---|---|---|---|
| AN-01 | 32.0 | C | FuzzReconcileSafe convergence oracle passes for the wrong reason |
| AN-04 | 30.0 | C | Nightly explore job runs zero tests |
| AN-09 | 16.0 | C | Recovery FSM labels the normal clone-source snapshot IMPOSSIBLE |
| SEAM-DEL-LOCK | 8.0 | A | DeleteVolume releases the source's snapshot without the src lock |
| N1 | 6.0 | A | Uncommitted resume ignores thin/thick mismatch |
| D07 | 6.0 | A | Unlimited filesystem incompatible with any sized request |
| REC-F10 | 6.0 | B | Reconcile age uses wall clock |
| CI-VULN | 6.0 | C | No go mod verify / govulncheck |
| AN-05 | 4.0 | C | VM tests ~40% faster after reboot (unexplained) |
| AN-08 | 4.0 | C | exportfs 3.1.1 double-free (upstream) |
| DEL-08 | 4.0 | A | Delete of a renamed volume reports success and leaks it |
| LST-04 | 4.0 | A | GetCapacity ignores capabilities |
| CSI-06 | 4.0 | A | Validate echoes unverified context |
| CI-NIGHTLY-CACHE | 4.0 | C | Nightly caches never saved |
| TG-CRASH | 4.0 | C | No crash-injection table |
| TG-LEAK | 4.0 | C | No goroutine leak check |
| E2 | 3.0 | A | Over-long parent accepted at startup |
| PRF-02 | 3.0 | A | Per-call whole-parent scan on delete/snapshot |
| TG-GOLDEN | 3.0 | C | No golden plans |
| A3 | 2.0 | A | Clone-source snapshot adopted on EEXIST |
| ADV3-12 | 2.0 | A | Parameters not forwarded |
| RT-04 | 2.0 | B | Audit has no principal |
| TG-REALFACTS | 2.0 | C | Simulator not anchored to recorded zfs facts |
| REL-LEGACY | 1.5 | A | Legacy unstamped origin released |
| ADV3-13 | 0.67 | A | No topology |

NFS-D1..D3 are claimed only by the running builder a9d51a288, not by an orch queue item; if that builder lands nothing, they are uncovered.

## 4. Writer batches (disjoint files; every non-fixed finding mapped)

**Batch A** -- files: internal/csi/controller.go(+_test), internal/endpoint/plan.go(+plan_test.go), internal/endpoint/endpoint.go (zfsErr, volumeID, New, DeleteVolume/CreateSnapshot)

Findings (21): T1, E6, T3, SEAM-DEL-LOCK, T2, N1, D07, ERR-12, E1, E3, E4, DEL-08, LST-04, CSI-06, E2, PRF-02, E5, A3, ADV3-12, REL-LEGACY, ADV3-13

**Batch B** -- files: internal/endpoint/table.go, internal/endpoint/reconcile.go(+reconcile_test.go), internal/endpoint/export.go(+export_test.go; wait for a9d51a288), internal/zfs/zfs.go, internal/zfs/errors.go, internal/zfs/runner.go(audit)

Findings (12): SCN-06, NFS-D3, REC-F10, SCN-02, NFS-D1, OWN-12, HOLD-REPLAN, ERR-13, NFS-D2, PROP-UTF, NFS-01, RT-04

**Batch C** -- files: internal/endpoint/reconcile_fuzz_test.go, internal/endpoint/ownership_test.go, NEW test files only (crash_test.go, golden_test.go, realfacts_test.go, internal/zfs/roundtrip_test.go), .github/workflows/ci.yml, .github/workflows/nightly.yml, ci/real-zfs-vm.sh, README.md, api/zfsendpoint/v1/endpoint.proto + gen/, rec/ FSM classifier (scratchpad)

Findings (36): AN-01, AN-02, AN-04, AN-03, AN-09, DOC-R110, DOC-R30, DOC-R35A, DOC-R81, DOC-R15, CI-VULN, DOC-PROTO239, DOC-PROTO98, AN-05, AN-06, AN-08, CI-NIGHTLY-CACHE, CI-GORACE, TG-PAR, TG-CRASH, TG-LEAK, DOC-R35B, DOC-R47, DOC-R106, CI-SHA, TG-SCALE, TG-GOLDEN, TG-ROUNDTRIP, AN-07, TG-REALFACTS, AN-10, TG-LAT, SC-W8-OWN13, SC-W8-D1, SC-W7-QEMU, SC-W7-LOCK

Ordering: C first (AN-01/AN-02/AN-04 make later fixes provable), then A and B in parallel. B touches export.go only after a9d51a288 lands. Fixed rows need no batch.

## 5. Stale-claim rate per source report (feeds lanes/README.md reader scoring)

stale = the report's claim did not hold at HEAD when checked (not 'fixed since'). Fixed-since counts are credit, not penalty.

| source | claims checked | stale | fixed since | still open | stale rate |
|---|---|---|---|---|---|
| README.md | 10 | 8 | 0 | 2 | 80% |
| wave8-adversary | 9 | 2 | 4 | 3 | 22% |
| wave7-suite | 8 | 2 | 2 | 4 | 25% |
| wave11 (CI/VM) | 10 | 1 | 3 | 6 | 10% |
| wave9 (csi+edges) | 11 | 0 | 0 | 11 | 0% |
| wave10 (core/nfs-diff) | 5 | 0 | 0 | 5 | 0% |
| adv3 | 13 | 0 | 8 | 5 | 0% |
| adv4 (reconcile/export/zfs-json) | 21 | 0 | 18 | 3 | 0% |
| dod3 | 14 | 0 | 10 | 4 | 0% |
| wave5/wave6 | 10 | 0 | 6 | 4 | 0% |
| wave12-model | 5 | 0 | 0 | 5 | 0% |
| rec (FSM) | 2 | 1 | 1 | 0 | 50% |
| align | 3 | 0 | 2 | 1 | 0% |
| docs/research/gaps/* | 6 | 0 | 2 | 4 | 0% |
| api proto comments | 2 | 2 | 0 | 0 | 100% |

Notes: README 8 of 10 checked claims are stale (worst). rec counts AN-09 (classifier calls a legal state IMPOSSIBLE) as its stale claim. adv3/adv4/dod3 are high-precision: their items were true when written and are mostly fixed now. AN-10 (80.2% vs 76.6%) is explained, not charged to any reader.

## 6. Not yet verifiable (in flight)

- aa10b675 mutation-kill tests, a0f85a15 speed-tests, a9d51a288 nfs-diff fixes, a5b209d5 wave9/zfs-diff.md: not in the repo at HEAD; rescore TG-*, AN-07, NFS-D1..D3 when they land.
