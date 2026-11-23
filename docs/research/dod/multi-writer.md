# Multi-writer coordination: findings and recommendation

## FINDINGS

**Current code.** Keyed mutexes are in-process only (`internal/lock/lock.go:18-61`, `internal/endpoint/endpoint.go:172-186`). Each op scans, plans, applies with no version check (`endpoint.go:279-290`). Nothing fences a second replica.

**client-go Lease.** `LeaseDuration` must exceed `RenewDeadline` (`tools/leaderelection/leaderelection.go:77-78`). A new leader waits a full LeaseDuration without observing change (`leaderelection.go:120-130`). `ReleaseOnCancel` warns that protected work must finish first (`leaderelection.go:153-157`). The Lease carries `LeaseTransitions` (`resourcelock/leaselock.go:127-132`), but client-go never hands it to the protected resource. So the deposed leader can act until its renew fails, which is a window bounded by RenewDeadline (derived, not quoted). The lease is not a fence.

**ZFS channel programs.** A sync channel program runs as one `dsl_sync_task` in one txg (`module/zfs/zcp.c:24-27`) and is atomic (`zfs-program.8:37`). `zfs.get_prop` and `zfs.sync.set_prop` can therefore sit in one script. That makes compare-and-set of a user property possible. Limits:
- `zfs.sync.set_prop` supports user properties only (`zfs-program.8`, set_prop entry near line 433).
- No `zfs.sync.create` exists (sync list, lines 354-446). `create -V`, `refquota` and `volsize` sets stay outside the script.
- GUIDs read as Lua int64 and can wrap (`zfs-program.8`, get_prop entry near line 318).
- `zfs.check.*` gives a dry run (lines 485-512).
- Fatal errors do not roll back (`zcp.c:65-66`). Whether a Lua error aborts the sync task is unverified.

**Races left after a ZFS CAS:**
- Ops outside the script (create, clone, snapshot, size sets) run between scan and commit. They leak uncommitted residue, which the planner already converges (`plan.go:101-115`).
- Admin `zfs set` and `zfs create` never bump the epoch, so they are unfenced.
- A get in one script and a set in another is not CAS.

**Peers.**
- pillar-csi fences at the resource: the agent keeps a per-volume mark and, under one lock, rejects stale `(volume_uid, generation)` tokens (`gen/go/pillar_csi/agent/v1/agent.pb.go:306-324`). Write-once CEL fields are at `api/v1alpha1/pillarvolumestate_types.go:515-530`.
- mayastor fences at the data plane with NVMe reservations: release, then reacquire with its own key (`io-engine/src/bdev/nexus/nexus_child.rs:924-927`).
- LINSTOR's controller uses DB transactions (`VolumeDefinitionDbDriver.java:41-49`), and the satellite uses RW locks per map (`StltApiCallHandler.java:168-172`). Its HA fencing: unverified (not found).

## RECOMMENDATION

Single-writer actor per parent, plus a ZCP-fenced commit. Drop the keyed mutexes.

1. `endpoint.go`: replace `locks lock.Keyed` with `reqs chan request`. `request{fn func(Table)(Plan,error); reply chan result}`. One goroutine scans, plans, applies, replies. Callers batch many items per request.
2. `table.go`: add `Row.Guid uint64` (from `guid`), and `func (r Row) Version() uint64` hashing Guid, Creation and the decoded user-prop values.
3. `plan.go`: `Plan` gains `Epoch uint64` and `Guards []Guard{ID, Version}`. The `commit` op (`plan.go:99`) becomes `OpCommit`.
4. `apply.go`: `OpCommit` runs one `zfs program` (new `zfs.Program`, unverified in `internal/zfs/runner.go`). It get_props the parent epoch and each guard's `provision_success`, compares, then set_props. Mismatch returns `Aborted`.
5. Leader change bumps the parent epoch in its first commit.

## RISKS

- Admin manual edits stay unfenced.
- Creates before the commit leak residue, relying on convergence.
- Single actor serializes one parent. Measure before accepting that.
- Instruction limit and root-only `zfs program`.
- Guid wrap in Lua.
- Two processes must not both think they lead: keep leader-election (Lease) as liveness only.
- research call timed out; mayastor control-plane and LINSTOR HA unverified.
