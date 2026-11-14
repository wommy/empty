# ZFS sim fidelity: destroy, snapshot, promote

Real-ZFS paths are relative to `scratchpad/repos/openzfs/`. Sim paths are `internal/zfs/zfstest/sim.go`.
Verdicts: MATCHES / DIVERGES / MISSING. DIVERGES comes first.

## DIVERGES (fix first)

1. **`destroy -d` on a filesystem is silently accepted and destroys it.** Real: `-d` sets type to SNAPSHOT (cmd/zfs/zfs_main.c:1721), so zfs_open fails with `cannot open 'pool/fs': missing '@' delimiter in snapshot name` (lib/libzfs/libzfs_dataset.c:708, 111-115), exit 1. Sim: `-d` only sets a flag, and the filesystem path ignores it (sim.go:259-262, 271-297). Fix: return that error when `deferred` is set and the target is not a snapshot.
2. **`destroy -n` (dry run) actually destroys in the sim.** Real: `-n` is a dry run (zfs_main.c:1698-1725, cb_dryrun). Sim: `-n` falls into `default: name = a` (sim.go:256-266) and the destroy runs. Fix: treat `-n` as a no-op that prints "would destroy" lines, or reject it.
3. **`destroy -R` is treated as a no-op.** Real: `-R` also destroys clones (zfs_main.c:1715-1719, destroy_clones). Sim: `-R` falls to `default` (sim.go:256-266), so with children it returns the "use -r" error. Fix: add a `-R` case that removes clones before the target.
4. **Pool root without `-r` is destroyed or misreported.** Real: `cannot destroy 'pool': operation does not apply to pools` plus two hint lines (zfs_main.c:1878-1884), exit 1. Sim: no pool check (sim.go:282-285). Fix: refuse any name without '/' when not recursive.
5. **`destroy -r` with outside clones prints only the first clone.** Real: one line per clone after the header (zfs_main.c:1449-1460). Sim: returns on the first clone (sim.go:286-296). Fix: collect all clones and join them, as the snapshot branch already does.
6. **Missing snapshot name gives the wrong error.** Real: when the parent exists, `could not find any snapshots to destroy; check snapshot names.` exit 1 (zfs_main.c:1778-1781, gather_snapshots). Real opens the parent first, as FS|VOL, on the '@' branch (zfs_main.c:1698ff). Sim: `cannot open 'pool/fs@x': dataset does not exist` (sim.go:254-270). Fix: when '@' is present and the parent exists, return the "could not find" text. If the parent is missing, use the `cannot open '<parent>'` form.
7. **`snapshot -o` accepts native properties.** Real: only user properties. Native ones fail with `cannot create snapshots : this property can not be modified for snapshots` (lib/libzfs/libzfs_dataset.c:893-898, errbuf at 4125; note the stray space before the colon), exit 1. Sim: takeOpts stores every key (sim.go:113-130, 191-205). Fix: reject keys without a ':' (user property) with that message.
8. **`snapshot -r` is not supported.** Real: recursive snapshot of the tree (zfs_main.c:4647ff). Sim: `-r` is treated as a positional argument (sim.go:191-205). Fix: add `-r` to snapshot the descendants of the same name.
9. **Promote moves only the origin snapshot.** Real: moves the origin fs's snapshots from the origin snapshot back to the origin's own origin (`shared_snaps`, module/zfs/dsl_dataset.c:3619-3675, 3921-3948). Sim: moves only `oldSnap` (sim.go:316-330). Fix: move every snapshot of the old fs with creation <= oldSnap, which is the prefix of its snapshot list.
10. **Clones of moved older snapshots are not re-pointed.** Real: the dd_clones refs move for each moved snapshot (dsl_dataset.c:3921-3948). Sim: only `other.origin == oldSnap` is rewritten (sim.go:333-336). Fix: rewrite origin for every moved snapshot, not just the origin.
11. **Promote does not check for name conflicts.** Real: any snapshot or bookmark name on the clone that matches a moved name gives `cannot promote 'pool/clone': conflicting snapshot 'NAME' from parent 'pool/origin'` (libzfs_dataset.c:4071-4076, kernel dsl_dataset.c:3677-3749, EEXIST), exit 1, nothing moved. Sim: `s.ds[newSnap] = s.ds[oldSnap]` overwrites silently (sim.go:327). Fix: check `newSnap` names for moved snapshots first and return that error.
12. **Promote of a snapshot name gives the wrong error.** Real: `cannot open 'pool/fs@s': snapshot delimiter '@' is not expected here` (libzfs_dataset.c:104-108 via zfs_open type FS|VOL). Sim: `cannot promote ...: not a cloned filesystem` (sim.go:316-322). Fix: reject names containing '@' with the zfs_open text.
13. **Promote ignores holds and long holds.** Real: a long hold on any moved snapshot (open zvol) gives `cannot promote 'X': dataset is busy`, EBUSY (dsl_dataset.c:3685-3687, libzfs_util.c:466). Sim: no busy model. Fix: needs the busy model in item 17.
14. **Promote ignores bookmark name conflicts.** Real: bookmarks on the origin move with the snapshots and conflict on name (dsl_dataset.c:3700-3712). Sim: no bookmarks. Fix: MISSING unless bookmarks are modeled.
15. **Destroy list order.** Real: children are listed post-order (each child's descendants and snapshots, then the child), then the target's own snapshots (libzfs_iter.c:580-620, iter_dependents_cb). Sim: alphabetical (sim.go children(), sort.Strings). Fix: match the post-order walk if tests compare the list.

## MATCHES

- **1. `destroy` fs with children, no -r.** Text is `cannot destroy 'X': filesystem has children` + `use '-r' to destroy the following datasets:` + one name per line. Exit 1 (zfs_main.c:1430-1440). The list includes the fs's own snapshots, which real also reports. Sim: sim.go:282-285 (order differs, item 15).
- **3. `destroy -d` on a snapshot with clones.** Marked defer_destroy, exit 0, silent (zfs_main.c:1626 skips the clone check; dsl_destroy.c:320-328). Sim: sim.go:271-276 (MATCHES).
- **3. `destroy -d` on a snapshot without clones.** Destroyed immediately (dsl_destroy.c:320 condition false). Sim: sim.go:278-279. MATCHES for no holds; the sim has no holds (item 16).
- **3. Auto-destroy when the last clone is destroyed.** Yes: dsl_destroy.c:1081-1085 (rmorigin: origin deferred, num_children==2, userrefs==0). Sim: remove() sim.go:305-314 (MATCHES).
- **3. Auto-destroy when the last clone is PROMOTED.** No. Promote contains no DEFER or userrefs logic (dsl_dataset.c:3590-4215, grep checked). The mark moves with the snapshot, which stays under the old fs (now a clone). It is reaped only when that old fs is destroyed. Sim: promote leaves defer_ on the node (sim.go:316-334), reaped on the later destroy. MATCHES.
- **4. Missing dataset.** `cannot open 'pool/nope': dataset does not exist`, exit 1 (libzfs_util.c:449; zfs_main.c:1870-1871). Sim: sim.go:254-270 (MATCHES for datasets).
- **5. `snapshot` of an existing name.** `cannot create snapshot 'pool/fs@s': dataset already exists`, exit 1 (libzfs_util.c:460; libzfs_dataset.c:4123-4170, lzc errors). Sim: sim.go:199-201 (MATCHES, single snapshot only).
- **6. Promote of a clone with no conflicts.** The origin's origin becomes "" and the old fs origin becomes `clone@snap` (dsl_dataset.c:3861-3880). Sim: sim.go:324-334 (MATCHES for these two).
- **6. User properties on moved snapshots.** Local props stay on the snapshot (ds_props_obj, dsl_prop.c:182-196). Sim: props stay on the node (MATCHES). Inherited user props now come from the clone's ancestors (dsl_prop.c falls back to ds_dir). Sim: parent(name) chain (sim.go:443-450, MATCHES).
- **6. Clones of the origin snapshot.** Their origin becomes `clone@snap` (sim.go:333-336, MATCHES for this case).
- **7. Destroy zvol that is open (e.g. LIO).** `cannot destroy 'pool/vol': dataset is busy`, exit 1 (libzfs_util.c:466 maps EBUSY; zfs_main.c:1870; libzfs_dataset.c:3816). Kernel: an open zvol holds a long hold (zvol.c:357, module/os/linux/zfs/zvol_os.c:1686), and dsl_destroy.c:829-830 / 877 returns EBUSY. Sim: MISSING (item 17).

## MISSING

16. **Holds.** `zfs hold` / `zfs release` do not exist in the sim. Real: a held snapshot gives `cannot destroy snapshot 'X': it's being held. Run 'zfs holds -r X' to see holders.` (libzfs_dataset.c:3914-3924, kernel dsl_destroy.c:78-79 EBUSY). `-r` does not release holds, and with -d the snapshot is marked deferred instead (dsl_destroy.c:320-326).
17. **Busy model.** No open-count or long-hold model for zvols (needed for item 7 and promote item 13).
18. **`destroy -r` holds.** `-r` does not release holds. Any held snapshot in the tree fails the batch with the hold text above (zfs_main.c:1478, zfs_destroy_snaps_nvl libzfs_dataset.c:3878-3930).

## Summary for the endpoint

Items 1-2 are the most dangerous: the sim accepts `-d` on a filesystem and `-n` as a real destroy, so the endpoint could pass tests that real zfs would reject or dry-run.
