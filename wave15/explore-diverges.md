# TestExploreLifecycle retry-diverges: root cause (db15ae9)

Run: `FSM_OUT=<prefix> go test -tags nightly -run TestExploreLifecycle -count=1 ./internal/endpoint/`
(FSM_DEPTH default 4 is full depth; ~58 s on 4 cores). Reports: wave15/fsm/r-*.txt.

## Findings: 6 = 1 root cause x 4 configs (fs/block x zfs-endpoint/democratic-csi; fs configs 2 traces each)
Signature: `DV(b) CRASH@1/1 [a:Committed a@volume-source-for-volume-b:OwnCloneSrc-orphan] vs [a:Committed]`

Minimal trace (block/zfs-endpoint):
  CV(a) ; CV(b<-a,reserve) CRASH@1/3 ; CV(b,reserve) ; DV(b) CRASH@1/1 ; retry DV(b)
- CV(b<-a) dies after cutting stamped a@volume-source-for-volume-b, before clone.
- CV(b) (no source) creates plain b; orphan stays.
- DV(b) uncrashed: plan = [destroy b] -> orphan remains. resp=OK
- CRASH@1/1 = all ops done, response lost; retry DV(b) on absent b -> releases orphan. resp=OK
- Diff: orphan present (uncrashed) vs gone (retried). I.e. DV;DV != DV.

## Class: (a) real endpoint idempotency bug (plan-level, simulator-independent); benign direction
plan.go:679-685 PlanDeleteVolume: present volume -> planDelete only releases the clone *origin*
(releasedOrigin, plan.go:815-818, needs row.Origin == the snapshot); absent volume ->
releaseCloneSourcesFor (plan.go:691-707) releases every unused snapshot stamped for id.
A present non-clone b never gets its stamped orphan released. Introduced by 9520322.
Not (b): no zfs semantics involved. Not (c): CRASH@n/n retry == f(f(x))=f(x), a valid CSI check.
Not (d): no doc/allow-list mentions it.

## Fix (verified in throwaway worktree, diff: wave15/explore-diverges-fix.diff)
PlanDeleteVolume: for a present volume, prepend releaseCloneSourcesFor(t,id) ops (minus d.origin)
before d.plan(). Locks already cover it (deleteLocked uses volumesOf). Result: nightly explorer 0
findings in all 4 configs; non-nightly endpoint tests pass. Add a table test in plan_test.go.

## Production impact
Real ZFS behaves identically (pure planner). Effect: a leaked clone-source snapshot on a (space
pinned) until Reconcile's orphan rule (reconcile.go:335-370) collects it after MinAge, or a second
DeleteVolume. No data loss, no wrong response. Needs CreateVolume of the same name re-issued without
the source after a died clone create: unlikely with external-provisioner (pvc-<uid> names, same params).
No real-ZFS repro needed beyond a planner unit test.
