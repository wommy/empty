# wave13: fixes-csi

The work is on local branch `fixes-csi` in worktree `/home/user/empty/.claude/worktrees/agent-a131fcf70b9f3c4be`. It branches from `c1205e9`, the head of `claude/gallant-johnson-1qaxp5`. Nothing is pushed.

The raw before/after logs are in `scratchpad/wave13/fixes-csi-ev/*.log`. The scripts are `ev.sh` and `s12-ev.sh` in the same directory. Each script takes the listed non-test files as they were at c1205e9, runs the new test against them (BEFORE), restores the files and runs the test again (AFTER).

## Anomalies and surprises (read first)

1. **Two "before" runs are build failures, not assertion failures. Both are new APIs.**
   - `TestPlanExpandRollback` calls the new `Planner.PlanExpandRollback`.
   - `TestProbeFollowsEndpointHealth` builds `Identity{Health: ...}`; at c1205e9, `Identity` had only an `EP` field.
   - For S12, the BEFORE run uses the same test body with c1205e9's `Identity{EP: ...}` against c1205e9's `identity.go`, and it fails on the assertion (`s12-ev.sh`).
   - For item 10, the simulator test `TestExpandThickFilesystemRetryAfterENOSPC` fails on its assertion against c1205e9's `endpoint.go`.
2. **Item 10: refreservation before refquota cannot work on a grow.**
   - zfs refuses a refreservation larger than the refquota (`dsl_dataset_set_refreservation_check`). The simulator models this in `spaceCheck`.
   - So I chose rollback. The new pure `PlanExpandRollback(t, before)` resets refquota to its value before the grow. It does so only when this grow raised refquota and the reservation did not follow.
   - `ExpandVolume` applies the rollback under the same lock, on `context.WithoutCancel` with a 30 s timeout. If the rollback itself fails, its error is joined to the original error, and the code stays the original one.
   - **What remains:** a half-applied state that no rollback undid (a kill between the two properties, or a failed rollback). The earlier zfsdiff re-plan still handles it: it reserves `max(want, refquota)` and keeps answering ResourceExhausted. I did not let Expand shrink a stray refquota. A refreservation an administrator lowered on purpose looks exactly the same, and shrinking refquota there would be destructive. The test covers this case.
3. **An existing test pinned the item-10 bug.** `TestExpandThickFilesystemRetryAfterENOSPC` asserted that "a smaller expand is no way out" (ResourceExhausted). I rewrote it to assert the fixed behaviour:
   - After each failed 1 PiB grow, refquota and refreservation are both still 1 GiB, and GetVolume reports 1 GiB.
   - A 2 GiB grow then succeeds.
   - A hand-made half-applied state still answers ResourceExhausted.
   - The test is now `t.Parallel`, since it owns its datasets. Its new assertions hold on a real pool whether zfs half-applies the set or applies neither property.
4. **CSI-06 is about ValidateVolumeCapabilities, not CreateVolume.**
   - The brief says "idempotent CreateVolume compares the issued volume_context keys". But a CSI CreateVolume request carries no volume_context, and the probe (`TestProbe13CSI06ValidateEchoesContext`) is about Validate echoing back whatever context it was sent. So I fixed Validate.
   - Validate now confirms a volume_context only when every key it names has the value `volumeOut` issues for that volume. Keys Kubernetes adds itself are skipped (`storage.kubernetes.io/`, `csi.storage.k8s.io/`; external-provisioner puts `storage.kubernetes.io/csiProvisionerIdentity` into the PV attributes).
   - **Parameters half: DEFERRED.** `pb.Volume` carries no sparse/reserve, so it needs a proto change. A code comment at the check in `controller.go` says so. The proto is unchanged.
   - The probe also needed `EnableBlock` for its zvol, which it lacked. The ported test uses a filesystem volume.
5. **E2 bound.** I refuse a parent longer than 126 bytes (`endpoint.MaxParentLen` = 255 − 1 − 128), so that every 128-byte CSI volume or snapshot id fits under zfs's 255.
   - `zfs-endpoint` startup (`validParent`) refuses it as well, with exit 78, before waiting for the pool.
   - **Side effect:** the endpoint test harness on a real pool named parents `<pool>/ep-<t.Name()>-<ns>`, and a long subtest name could pass 126. The name part is now capped at 64 bytes; the nanosecond suffix keeps each parent unique.
6. **N1 direction.** At c1205e9 the resume converged only thin → thick (`sizeOps` set `refreservation=auto`). It did not converge thick → thin, nor a reserved filesystem → unreserved. It now sets `refreservation=none` in those cases, mirroring `compatible()` both ways. The probe's "commit as-is, then the identical retry is AlreadyExists" is reproduced in BEFORE.
7. **S12 changes the Identity API and behaviour.**
   - `Identity.EP` is replaced by `Identity.Health` (a `healthpb.HealthClient`). `cmd/zfs-csi` builds it from the endpoint connection. The csi test harness now serves a SERVING health service.
   - NOT_SERVING maps to `ready=false` with no error. A failed Check (endpoint unreachable) passes its error through.
   - Nothing in `deploy/` runs a livenessprobe sidecar today. If one is added, a suspended pool would fail its liveness check and restart `zfs-csi`. That is harmless, but worth knowing.
8. **D07 consequence.** A sized re-create of an unlimited filesystem now succeeds and returns the volume as it is. Its CreateVolume capacity_bytes is 0 ("unknown" in CSI), as it already was for an unlimited create. Expand reports the requested size in that case; Create does not.
9. **T1 is fixed in the CSI front end only.** `server.CreateVolume` still turns an empty oneof source string into "no source" for any other endpoint-API client. I left that alone because the item named CSI.

None of the ten probe findings was wrong or already fixed at c1205e9. All ten probe tests, run unchanged in this tree before any fix, failed at c1205e9.

## Commits

| # | Commit | Item |
|---|---|---|
| 1 | `738d4c6` csi: an empty content source id is InvalidArgument | T1 |
| 2 | `0e4f804` csi: ControllerExpandVolume checks a given volume_capability | T2 |
| 3 | `baca343` endpoint: a filesystem clone is never smaller than its source | E6 |
| 4 | `b8f233e` endpoint: a resumed provision converges thick to thin as well | N1 |
| 5 | `56d60d2` endpoint: an unlimited filesystem satisfies a sized re-create | D07 |
| 6 | `596e8c0` endpoint: refuse a parent too long for a 128-byte volume name | E2 |
| 7 | `ca40c0b` endpoint: DeleteVolume locks the source whose clone origin it releases | SEAM-DEL-LOCK |
| 8 | `20d8584` csi: Probe asks the endpoint's gRPC health service | S12 |
| 9 | `b18a4b6` csi: ValidateVolumeCapabilities compares the issued volume_context | CSI-06 |
| 10 | `51b0a4b` endpoint: a failed thick-filesystem grow rolls its refquota back | Expand refquota |

No changes to `ci/`, `deploy/` or the proto. `README.md` changed in three places:
- the lock paragraph (DeleteVolume's keys and lock order);
- a row for the 126-byte parent limit;
- a row for the Expand rollback.

## Per item: fix, test, evidence

Unless a row says otherwise, BEFORE means the item's non-test files as they were at c1205e9.

**T1** (`controller.go` CreateVolume): an empty `snapshot_id` or `volume_id` is InvalidArgument.
- Test: `internal/csi` `TestCreateEmptyContentSourceID`.
- BEFORE: both cases fail with `snapshot: <nil>, want InvalidArgument` and `a volume was provisioned: <nil>`.
- AFTER: PASS.

**T2** (`controller.go` ControllerExpandVolume): when `volume_capability` is set, GetVolume runs first, then `capSupported` (the rule Validate uses). An unsupported capability is InvalidArgument before any resize.
- Test: `TestExpandValidatesCapability`. It covers block on a filesystem volume, MULTI_NODE_MULTI_WRITER and no access type, and checks that the capacity is unchanged. As controls, a supported capability still expands and a missing volume is NotFound.
- BEFORE: all 3 cases returned capacity 2 GiB and `resized by a refused expand ... capacity_bytes:2147483648`.
- AFTER: PASS.

**E6** (`plan.go` PlanCreateVolume): a filesystem clone's size is `max(requested, source refquota)`. A source with no refquota (unlimited) leaves the request as is.
- Tests:
  - planner table `TestPlanFilesystemCloneSize`: clone from snapshot, clone from volume, no capacity, a larger request, an unlimited source, and the resume of a row a previous build left too small;
  - simulator test `TestFilesystemCloneNeverSmallerThanSource`.
- BEFORE: `refquota "4194304" ... want 10485760` (3 rows), and the simulator clones report CapacityBytes 4194304.
- AFTER: PASS.

**N1** (`plan.go` sizeOps): a sparse request on a thick zvol, or an unreserved request on a reserved filesystem, gets `refreservation=none`.
- Tests:
  - planner table `TestPlanResumeThickness`: 5 rows, each checking that the plan commits and that the identical retry on the committed result is compatible;
  - simulator test `TestResumeConvergesThickToThin`.
- BEFORE: `thick zvol, sparse request: set ...provision_success=true` (no refreservation change), the same for the reserved filesystem, and on the simulator `z attempt 1: volume z exists with sparse=false`.
- AFTER: PASS.

**D07** (`plan.go` compatible): `want > 0 && have != 0 && have < want`. The limit check is unchanged.
- Test: planner table `TestPlanUnlimitedFilesystemIdempotency`. A capacity or no request against unlimited is OK. A limit, or capacity plus limit, against unlimited is AlreadyExists. A 1 MiB refquota against a 1 GiB request is AlreadyExists.
- BEFORE: `volume free exists with capacity 0 < 1073741824`.
- AFTER: PASS.

**E2** (`endpoint.go` New, `MaxParentLen`; `cmd/zfs-endpoint/startup.go` validParent):
- Tests:
  - `TestOverlongParentIsRefused` (endpoint): 127 bytes is refused; at 126 bytes, a 128-byte volume and a 128-byte snapshot id both work;
  - a new `overlong parent` row in `TestStartupRefusals` (cmd).
- BEFORE:
  - `parent of 127 bytes accepted`;
  - the startup row got `exit 75 ... not available` where 78 `over 126` was wanted.
- AFTER: PASS.

**SEAM-DEL-LOCK** (`endpoint.go` DeleteVolume, `deleteLocked`, `volKeys`; `plan.go` `volumesOf`):
- DeleteVolume holds `vol:<v>` for every volume its plan touches. The source shows up only in the scan, which runs under `vol:<id>`.
- **Lock order** (documented at DeleteVolume and in the README):
  - Every waiting acquisition takes its whole key set at once, sorted (`lock.Keyed.Acquire`).
  - A key added while others are held is only tried, never waited for (`TryAcquire`).
  - If that key is busy, everything is released and the full set is waited for, sorted, then scanned and planned again. At most 3 passes, then Aborted.
  - Hence it cannot invert against CreateVolume-from-source, which waits for `{vol:<new>, vol:<src>}` sorted.
  - This also covers releasing an absent volume's clone sources.
- Tests (internal, `deletelock_test.go`, in `synctest` bubbles, so the 30 s lock wait passes in fake time with no real sleeps):
  - `TestDeleteCloneLocksItsSource` (the ported probe): parked on the origin release, `vol:src` is held, and Expand(src) and CreateSnapshot(src) are Aborted;
  - `TestDeleteCloneWaitsForBusySource`: with `vol:src` held, the delete waits and touches nothing; it completes once `vol:src` is released;
  - `TestVolumesOf`.
- BEFORE: `vol:src not held`, Expand and CreateSnapshot returned `<nil>`, and `delete ran while its source was locked`.
- AFTER: PASS.

**S12** (`identity.go`, `cmd/zfs-csi/main.go`): Probe runs a health Check for `zfsendpoint.v1.EndpointService`.
- Test: `TestProbeFollowsEndpointHealth`. It uses a real `server.Daemon` over bufconn and toggles `SetServing` true, false, true.
- BEFORE (c1205e9 `Identity{EP}`): `endpoint serving=false: Probe ready=true err=<nil>`.
- AFTER: PASS. csi-sanity's Identity group passes with the new harness.

**CSI-06** (`controller.go` Validate, `contextMismatch`):
- Test: `TestValidateComparesIssuedContext`. Confirmed: the issued context, none, a subset, and the issued context plus a provisioner-identity key. Not confirmed: another dataset, another kind, an unknown key.
- BEFORE: the last three were confirmed.
- AFTER: PASS. The parameters half is deferred (see anomaly 4).

**Expand refquota (zfsdiff finding 1)** (`plan.go` `PlanExpandRollback`; `endpoint.go` ExpandVolume and `rollbackExpand`):
- Tests:
  - planner table `TestPlanExpandRollback`: the grow plan's properties; refquota raised with the reservation failed → reset to 1 GiB; neither applied, both applied, or the volume gone → no plan; a half-applied state from before this grow → no plan; thin filesystem or zvol → no plan;
  - simulator test `TestExpandThickFilesystemRetryAfterENOSPC` (rewritten, anomaly 3).
- BEFORE (simulator, c1205e9 `endpoint.go`): `attempt 0 left refquota/refreservation 1125899906842624/1073741824, want the old 1 GiB both`.
- BEFORE (planner): build failure, because the API is new.
- AFTER: PASS.

## Gate (at `51b0a4b`)

| Check | Result |
|---|---|
| `gofmt -l .` (ignoring `.claude/`) | empty |
| `go vet ./...` and `go vet -tags nightly ./...` | ok |
| `go mod tidy -diff` | ok |
| `go test -race -count=1 ./...` | all ok, 17 s wall |

## Test-time cost

All 16 new or changed tests run in about 0.1 s together on the simulator: each takes 0.00–0.01 s, and `TestStartupRefusals` gains one row.
- **Simulator:** the synctest tests' 30 s lock waits pass in fake time, so they add no wall time.
- **Real pool:** three tests that use `setup()` run there:
  - `TestFilesystemCloneNeverSmallerThanSource`
  - `TestResumeConvergesThickToThin`
  - the rewritten `TestExpandThickFilesystemRetryAfterENOSPC` (about 8 more zfs calls than before, now parallel)
- Each of these is a handful of zfs calls, an estimated under 1 s each. Unlisted tests weigh 0.1 s in `ci/real-zfs-weights.txt`, which I did not change. Replace that weight with the first VM run's numbers.
- The CSI tests also run on the real-pool leg through `endpointClient` (4 new ones, a few zfs calls each).
