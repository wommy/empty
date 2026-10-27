# wave16 seams review (c1f00ba)

## 1. MEDIUM: the locked re-plan drops the guid that the digest covers (confirmed)
- Where: internal/endpoint/reconcile.go:144 (`Desired.refuses`), :152 (`targets`), :715.
- How it triggers: `Reconcile(apply, digest)` runs an unlocked whole scan, which matches the digest including guids. It then takes the locks, runs a second whole scan and re-plans. `d.planned` compares only target and reason. A rename-and-recreate with copied markers between the two scans gets through: the re-planned op carries the new guid, so `sameDataset` passes and the impostor is destroyed. The window is one whole-parent scan, which takes seconds at 10k datasets. That is wider than the documented guid-to-destroy window, and it is the case D3 says the guid closes.
- Confirmed with a throwaway test (in a /dev/shm copy) that wraps the runner and swaps before the second whole `get`. The impostor (guid 1004) was destroyed and err was nil.
- Fix: make `planned` map target to {reason, guid} and refuse a mismatch as SkipChangedSinceScan.
- Test that fails today: TestReconcileLockedReplanKeepsTheDigestedGuid (the swapper above). Want: the impostor survives, "drop" is in skipped.

## 2. LOW-MEDIUM: the min-free guard ignores `copies` (confirmed)
- Where: internal/endpoint/headroom.go:58-63 (create) and :86 (expand).
- How it triggers: a StorageClass sets `copies=3` (allowed for zvols, props.go:123) on a thick zvol. `refreservation=auto` reserves volsize×copies, but `planReserves` counts volsize. A 1 TiB create with 3.2 TiB available and a 0.5 TiB floor passes and leaves about 0.2 TiB.
- Fix: multiply by `op.Props["copies"]`. For an expand, scale the growth by row.Refres/row.Volsize.
- Test that fails today: planReserves of OpCreateVol{Size: 1 TiB, copies: 3} should be ≥ 3 TiB; today it returns 1 TiB.

## 3. LOW: a stale private bind directory blocks restarts under -socket-group
- Where: unixsock.go:97 and :168.
- How it triggers: the daemon is killed between MkdirTemp and the deferred RemoveAll, which leaves `.NNN` in the socket directory. Listen never sweeps it. ShareWithGroup then refuses ("holds .NNN besides the socket"), exit 78, and RestartPreventExitStatus=78 keeps the daemon down until someone cleans up by hand. The window is tiny and /run is tmpfs, so this is unlikely.
- Fix: Listen removes leftover `.`-prefixed directories it owns, or ShareWithGroup ignores them.
- Test: plant `dir/.x/` and then listenCSI with a gid. Today it is an error.

## Checked and found sound
- Stream decoder: strict, with UTF-8 and shape checks. Values are bounded by zfs, the read is bounded by ctx and WaitDelay, and an early decoder error drains the pipe.
- Headroom arithmetic: inputs are capped at MaxInt64, so there is no wrap. Underflow is guarded. Units are bytes.
- OWN-15 destroy guard: covers DeleteVolume, orphan sources and the settle retry. No destroy uses -r.
- scanDelete: `-d 2` reaches child snapshots. The suffix match is exact. vol:src is taken with TryAcquire.
- set-root: openat2 no-follow, then the mountinfo and device check, then fchown/fchmod on the fd. uid/gid/mode are range-checked in the CSI driver and the endpoint.
- unixsock: the 0600 socket is renamed into place. The admin socket can't sit inside the csi directory.
- reconcile CLI: -apply requires -digest. `-max-deletions` truncates to uint32, which can only lower the cap (a nit).
- Far-fetched: an unstamped `src@volume-source-for-volume-<id>` cut between the scan and OpSnapshot(OkExists) is adopted, and later released as "legacy". This needs a second driver on the same parent, which ADR 0001 D1 excludes.
