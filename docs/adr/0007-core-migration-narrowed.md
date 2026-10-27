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
