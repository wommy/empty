# Sim fidelity audit: create / clone / set / inherit / property defaults

Real source root: /tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/repos/openzfs
Sim: /home/user/empty/internal/zfs/zfstest/sim.go

Real paths below are relative to the OpenZFS root. Sim paths are absolute line numbers in sim.go.

## DIVERGES (in priority order, each with a suggested fix)

1. **Explicit `-o refreservation` is overwritten by auto on `create -V`.** Real keeps an explicit value (`!nvlist_exists`, lib/libzfs/libzfs_dataset.c:1290-1297 region; test tests/zfs-tests/tests/functional/cli_root/zfs_create/zfs_create_refreserv.ksh:49-52). Sim line 183-184 always overwrites.
   Fix: only set `refreservation=autoResv` when the key is absent from `props`.

2. **`create -V <size>` does not parse size suffixes or round up to volblocksize.** Real parses with `zfs_nicestrtonum` (cmd/zfs/zfs_main.c:1210-1213) and rounds up with `P2ROUNDUP` (cmd/zfs/zfs_main.c:1242-1248). Sim stores the raw string (sim.go:182) and `autoResv("64M")` returns `"0"` (sim.go:386-392). Real `-V 64M` gives a real reservation (zfs_create_refreserv.ksh:49).
   Fix: parse the size like `zfs_nicestrtonum`, round up to volblocksize (default 16384), store numeric.

3. **Clone does not copy `volblocksize` from origin.** Real zvol reports the object's data block size (module/zfs/zvol.c:266-270), so an `-b 8k` origin gives an 8K clone. Sim sets only `volsize` (sim.go:225-227), so the clone reports the 16384 default. This also breaks the volsize multiple check (sim.go:355-360).
   Fix: in `clone`, copy origin's `volblocksize` (when set) into `n.props`.

4. **Native inheritable properties are not modelled for clones or snapshots.** Real: a clone's dsl_dir parent is the target's parent (module/zfs/dsl_dataset.c:3535-3545), and `dsl_prop_get_dd` walks `dd_parent` for inheritable props (module/zfs/dsl_prop.c:105-121). Snapshots walk their fs chain the same way (`snapshot=TRUE`). Sim only inherits user props (sim.go:443-449); other props return `default` (sim.go:452-466 region).
   Fix: for inheritable native props, walk the parent chain (`parent(name)`, which handles `@`) and return `inherited from <p>`.

5. **`create -s` without `-V` is accepted.** Real: `'-s' and '-b' can only be used when creating a volume` plus usage, exit 2 (cmd/zfs/zfs_main.c:1179-1184). Sim sets `sparse` and creates a filesystem (sim.go:144-146).
   Fix: error out when `sparse` or `-b` is set and `volsize == ""`.

6. **`zfs inherit` on a native non-inheritable property succeeds silently.** Real: `'<prop>' property cannot be inherited`, exit 1, plus the `set ...=none` hint (cmd/zfs/zfs_main.c:2540-2556; lib/libzfs/libzfs_dataset.c:1871-1872). Official test: tests/zfs-tests/tests/functional/cli_root/zfs_inherit/zfs_inherit_001_neg.ksh:31-45 requires failure for quota, reservation and volsize. Sim deletes the prop and returns 0 (sim.go:374-381). Sim also does not parse `-r` or `-S`.
   Fix: reject quota, refquota, reservation, refreservation, volsize and similar with the real text and exit 1; parse `-r`/`-S`.

7. **`refreservation=auto` is accepted on filesystems and depends on argument order.** Real: `'refreservation=auto' only allowed on volumes` (lib/libzfs/libzfs_util.c:1904-1913). Real uses the new volsize when it is in the same `set` (lib/libzfs/libzfs_dataset.c:1494-1500). Sim uses the old volsize (sim.go:366-368).
   Fix: error on non-volumes; resolve auto after all args are parsed, using the final volsize.

8. **`create -o refreservation=auto` stores the literal string `auto`.** Real resolves it to a number (lib/libzfs/libzfs_dataset.c:1513-1545, zfs_create_fix_auto_resv).
   Fix: resolve `auto` to `autoResv(volsize)` in `create`.

9. **Default `compression` is wrong.** Real default is `on` (include/sys/zio.h:139 `ZIO_COMPRESS_DEFAULT` = `ZIO_COMPRESS_ON`; module/zcommon/zfs_prop.c:422-423). Sim default is `lz4` (sim.go:402).
   Fix: set the default to `on`.

10. **Missing pool without `-p` gives the wrong error.** Real: `cannot create 'x/y': no such pool 'x'` (lib/libzfs/libzfs_dataset.c:3404 and 3416, check_parents). Sim says `parent does not exist` (sim.go:162).
    Fix: when the pool root (before the first `/`) does not exist, return `no such pool '<pool>'`.

11. **`set volsize` error text formats the block size as raw bytes.** Real: `'volsize' must be a multiple of volume block size (16K)`, using `zfs_nicebytes` (lib/libzfs/libzfs_dataset.c:1316-1327). Sim prints `(16384)` (sim.go:359). Real also rejects `volsize=0` with `'volsize' cannot be zero` (lib/libzfs/libzfs_dataset.c:1332-1338). Sim accepts 0 (sim.go:355-360).
    Fix: format the block size like `zfs_nicebytes`; reject zero.

## MATCHES

- **`-p` on an existing dataset exits 0.** zfs_main.c:1288-1297 (`ret = 0`). Sim.go:154-156. Note: real also checks the type (FS vs VOL); sim ignores type.
- **Missing parent without `-p`.** `cannot create '<name>': parent does not exist` (libzfs_dataset.c:3421). Sim.go:162.
- **Already exists.** `cannot create '<name>': dataset already exists` (libzfs_dataset.c:3649). Sim.go:157.
- **`-V` default volblocksize is 16K.** module/zcommon/zfs_prop.c:640-641; include/sys/fs/zfs.h:1573. Sim.go:403. Real CLI can pick a larger pool-derived value for large-ashift pools (zfs_main.c:1024-1057). Sim fixes 16K.
- **Clone copies `volsize`.** Sim.go:225-227. The clone shares the origin's zvol size (zvol.c:254-270).
- **Clone does not copy refreservation.** `dsl_dataset.c:685-693` reads refreservation from the clone's own props only. `dsl_dataset_clone_sync` (dsl_dataset.c:3535-3556) copies no props. Sim.go:216-230 does the same.
- **User props on origin are not visible on clone.** Clone inherits only from its new parent (dsl_prop.c:105-121). Sim.go:443-449 walks `parent(name)` of the clone, so it matches. Source is `-`.
- **Snapshot user props.** Inherited live from its fs and ancestors. Source `inherited from <fs>` (libzfs_util.c:1600-1601; dsl_prop.c:237-238). Sim.go:443-449 matches.
- **`set volsize` auto-tracks thick refreservation.** Real: libzfs_dataset.c:1419-1459 (`zfs_add_synthetic_resv`). Sim.go:362-365 matches.
- **`set volsize` must be a multiple of volblocksize.** Real: module/zfs/zvol.c:282-290; man/man7/zfsprops.7:1993-2000. Sim.go:355-360 matches (text differs, see 11).
- **Volsize can shrink.** Real: zvol.c:282-290 has no shrink check; zfsprops.7:2008-2014 warns only. Sim allows it.
- **`inherit` on a user property.** Real: libzfs_dataset.c:1849-1862 (`ZFS_IOC_INHERIT_PROP`). Sim.go:374-381 delete matches.
- **`set`/`create` for volsize on a filesystem.** Real: `'volsize' does not apply to datasets of this type` (libzfs_dataset.c:1000-1003). Sim.go:352-354 matches.

## NOT VERIFIED

- Whether real `zfs get quota` on an unset dataset prints `none` or `0`. Sim prints `0` with source `default` (sim.go:402). Checked the kernel and libzfs paths, but did not find the zero-to-none step.
