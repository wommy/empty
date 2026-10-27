# 7. The typed-core migration stops at step 0

Status: accepted (decisive, reversible) · Date: 2026-10-27 · Related: ADR 0003, ADR 0005.

## Context

- A design for a typed core had been proposed: World, handles, `decide.Rules` and `req`. It was to replace `table.go`, `plan.go` and the text parser one RPC at a time behind `-core=`, in steps 0 to 8.
- Since that design, HEAD has moved:
  - The dataset scan reads `zfs get -j -p` through a strict decoder. A 10k-dataset scan now allocates 18.5 MB in 105 allocations, down from 102 MB in 355k.
  - These now gate every PR:
    - a crash table that runs every crash point;
    - golden plans;
    - the model suite;
    - an FSM classifier.
  - A nightly explorer runs as well.
- Step 0 is done: one zfs-error table, which `CodeOf` reads. `TestErrTableMatchesHead` pins all 252 cells against the old mapping. An ownership differential against the spec's OWN rows found no disagreement over about 1,020 rows.
- Measured against HEAD, steps 1 to 8 would mostly replace tests the design itself keeps. The parsing gain is gone. The one advantage left is retained memory: about 31 MiB against 2.2 MiB at 10k datasets.
- Step 1 has two wins that do not need the core:
  - DeleteVolume still scans the whole parent.
  - Nothing guards a destroy against a rename-and-recreate (OWN-15).

## Decision

- The World/handles migration stops at step 0. `-core=` is not built.
- Step 1's two wins are built on HEAD's own types, each with a regression test:
  - a subtree-scoped DeleteVolume scan;
  - a guid check before every destroy.
- The OWN-04 gaps are settled in the spec, not in code:
  - OWN-04 does not require the source volume to be owned;
  - OWN-04 overlaps OWN-13 on reserved clone-source names.

## Consequences

- `table.go` and `plan.go` stay the production path.
- Reverse this if retained memory at the target scale becomes the binding limit, or if a class of bug appears that the Table's untyped rows keep producing. The step-0 table and the differential are the first pieces such a migration would reuse.

## Spec amendment: OWN-04 (2026-10-27)

The wave8 spec catalogue that holds the OWN rows is not in the repository, so the amended row is recorded here. Both points describe what the code already does; neither changes it.

**OWN-04, as amended.** A snapshot is a CSI snapshot only when `managed_resource` is set locally, `csi_snapshot_name` is set locally, and `csi_snapshot_source_volume_id` is empty or names the volume the snapshot is on; anything else is foreign (not listed, DeleteSnapshot FAILED_PRECONDITION).
- **The volume need not be owned.** A CSI-marked snapshot on a volume the endpoint does not own (an alias, or managed without a name) is still a CSI snapshot: ListSnapshots lists it and DeleteSnapshot destroys it with `-d`. `Row.OwnedSnapshot` reads only the snapshot's own markers (`internal/endpoint/table.go`), and ListSnapshots (`internal/endpoint/endpoint.go`) and `PlanDeleteSnapshot` (`internal/endpoint/plan.go`) test nothing else. The endpoint always writes the source marker, so only legacy or hand-set snapshots have this shape. Cloning from such a snapshot is still refused: OWN-07 requires the source volume to be committed and owned (`src.ServedVolume()` in `PlanCreateVolume`).
- **Overlap with OWN-13.** A snapshot named `volume-source-for-volume-<id>` that carries both a clone stamp (`managed_resource`, `clone_source_for=<id>`) and a CSI name satisfies both rows' text. OWN-04 wins: it is a CSI snapshot, not a clone source. `Row.OwnedCloneSource` requires an empty `csi_snapshot_name` (`internal/endpoint/table.go`), and so does Reconcile's orphan clone-source pass (`internal/endpoint/reconcile.go`). The endpoint never cuts such a snapshot, because CreateSnapshot refuses the reserved prefix (`internal/endpoint/plan.go`).

Evidence: the ownership differential (`internal/endpoint/owndiff_test.go`) encodes the spec rows independently of the Table and compares their verdicts with `Row.OwnedSnapshot` and `Row.OwnedCloneSource`. It plants both shapes, `alias@legacy` and `half@legacy` for the first point and `a@volume-source-for-volume-w` for the second, and the two agree on each.
