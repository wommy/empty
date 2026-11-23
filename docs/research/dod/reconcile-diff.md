# Coordination as desired-vs-actual diffing

## FINDINGS (cited)

1. **Terraform.** The diff is per resource instance: `NodeAbstractResourceInstance.plan` (github:hashicorp/terraform@55988303 `internal/terraform/node_resource_abstract_instance.go:833`). `DiffTransformer.Transform` (`internal/terraform/transform_diff.go:50`) adds graph nodes for each planned change. Correction to the brief: the diff does not supply dependency edges. The transform only orders whole-resource nodes before their instances (`transform_diff.go:63-67`). The plan is a data value, `plans.Plan` (`internal/plans/plan.go:34`), consumed by `Context.Apply` (`internal/terraform/context_apply.go:100`). Unverified: where reference-based edges are added.
2. **controller-runtime.** Level-triggered. The controller calls `c.Do.Reconcile(ctx, req)` with only a key (`pkg/internal/controller/controller.go:223`). The reconciler re-reads current state itself, and retries are expressed as `RequeueAfter` (`pkg/reconcile/reconcile.go:46`). There is no stored diff object.
3. **Crossplane.** `ExternalClient` exposes Observe/Create/Update/Delete (`pkg/reconciler/managed/reconciler.go:399-415`). The diff is `ExternalObservation` with `ResourceExists` and `ResourceUpToDate` (`reconciler.go:503-517`). Branches: up to date means no-op (`:1620`), absent means create (`:1479`), otherwise update. One object per reconcile, no batching. A poll interval catches out-of-band drift (`:1627`).
4. **pillar-csi.** The controller holds desired state in CRDs ("durable desired state", `api/v1alpha1/pillarvolumestate_types.go:701-708`) and fences pushes with `publicationGeneration` (`:719-729`). The agent contract pushes the whole set: `ReconcileState` carries "the complete desired export state of this agent" (`gen/go/pillar_csi/agent/v1/agent.pb.go:1948-1951`), and `VolumeDesiredState` is "the complete desired state for a single volume" (`agent.pb.go:4598`). Agent-side diff code: unverified (not located).
5. **This codebase.** Planners are already pure diffs. `PlanCreateVolume` compares a row to a request and returns an empty plan or an error (`/home/user/empty/internal/endpoint/plan.go:108-130`). Idempotency is "the plan is empty", and recovery is "re-scan and re-plan" (`endpoint.go:8`). Crash convergence for uncommitted rows is `plan.go:127-129`. Ownership lives in ZFS user properties (`endpoint.go:10-11`), so ZFS is the only actual store.

## RECOMMENDATION

**Q2: keep request-scoped plans.** Do not add a persistent desired table or a reconcile loop.
- `CreateVolume` returns the Volume synchronously (`endpoint.go:290`). A loop would only move the wait out of the call.
- The desire is already in the request. A desired table would duplicate ZFS user properties and could drift from out-of-band `zfs` commands.
- Sidecars time out at 10s, and `lockMargin` returns Aborted before the deadline (`endpoint.go:166-175`). A batch tick adds unbounded queueing.
- Batching helps only scan cost, and scans are already per volume (`endpoint.go:294-313`). If needed, coalesce scans. Multi-key batches stay deadlock-free because `lock.Keyed` sorts keys (`/home/user/empty/internal/lock/lock.go:6,74`).

**Q3: the desired row is the existing `CreateVolumeRequest`.** No new type is needed. If a batch entry point is wanted:

```go
// PlanCreateVolumes plans many requests against one scan. Pure.
func (p Planner) PlanCreateVolumes(t Table, rs []CreateVolumeRequest) ([]Plan, []error)
```

Per-request errors preserve per-call CSI semantics.

Orphan GC is a separate pure function over an explicit complete set:

```go
// Orphans returns managed, committed volume rows absent from want.
// want must be the complete desired set for p.Parent.
func Orphans(t Table, want map[string]bool) []Row
```

Each orphan is deleted under its `vol:<id>` lock through `PlanDeleteVolume` (`plan.go:248`), which keeps the snapshot and clone refusals. Unmanaged rows are skipped (`plan.go:257`). Uncommitted rows are skipped, since they may be in flight (`plan.go:127`).

## RISKS
- No complete desired set exists in the CSI API. Deriving one from requests would delete live volumes. GC stays off the request path.
- GC racing an in-flight provision: the per-id lock covers this only if GC takes it before reading the row. Unverified.
- Drift: a per-request scan detects drift only when a request arrives. Crossplane polls for this instead. Scan cost at scale: unverified.
- The working tree changed during this run. `apply.go:17-19` now calls `CreateFilesystem(..., false)` (no `-p`). `plan.go` line numbers shifted. This analysis uses the current state.
