# Sim vs OpenZFS 2.4: unpinned divergences (c1f00ba)

The endpoint issues create, snapshot, clone, set (including -u), inherit sharenfs, mount, destroy (-r, -d) and get -j. Promote, rename, hold and recv appear only in tests.

1. **available.** The sim (sim.go:838-860) subtracts every reservation, so a child's refreservation is counted twice inside a parent's reservation. It never adds back a dataset's own unused reservation or refreservation, and ignores the refquota cap. Real: dsl_dir.c:1229-1234 and dsl_dataset.c:2916-2930. The headroom guard (headroom.go:133) and spaceCheck's ENOSPC both depend on it; TestMinFreeBytesGuard is sim-only.
   - Fact row: a reservation on P leaves available(P) unchanged; a 32M child refreservation under it lowers it by about 32M.
   - Confidence: high.
2. **canmount=off on a mounted filesystem.** The sim just unmounts it (sim.go:1120). Real ZFS runs the mountpoint changelist (libzfs_dataset.c:1835-1840, changelist.c:709-713), so it fails busy over a pinned mounted child, the same class as today's bug. The endpoint is affected only when an operator does this to the parent.
   - Fact row: `set canmount=off P` over a pinned child → Busy, and P stays mounted.
   - Confidence: high.
3. **Failed prefix.** The sim leaves children it already unmounted unmounted, and still in zfs.exports (sim.go:1664-1690, added today). Real ZFS remounts and reshares them through changelist_postfix (changelist.c:164-165).
   - Fact row: inherited P/a plus pinned P/b; `set mountpoint P` → Busy, and P/a reads mounted=yes.
   - Confidence: high.
4. **Reshare on any set or inherit.** The sim reshares after every one (sim.go:1155, 1234). Real ZFS reshares only for mountpoint, share properties, canmount=off, volsize and zoned (changelist.c:718-722). The endpoint is safe today because verifyExport reshares explicitly, but a refquota or commit-marker set heals a stale export only in the sim.
   - Sim fix: reshare only when the changelist is non-empty.
   - Confidence: high.
5. **autoResv ignores copies and raidz.** sim.go:1262 vs libzfs_dataset.c:5604-5646. `copies` is a parameter callers can set.
   - Fact row: `create -V 1M -o copies=2` gives refreservation ≥ 2M.
   - Confidence: high.
6. **Kernel retry pass.** Real ZFS retries failed space properties (zfs_ioctl.c:2840-2862), so `set refquota=1M refreservation=none` over a 10M refreservation succeeds; the sim fails it (sim.go:1130). Current plans can't reach this.
   - Confidence: high.

Unconfirmed: whether mount ordering diverges (I found nothing), and busy unmounts from open files, which the sim does not model.
