# zfs get / list fidelity: sim vs OpenZFS

Scope: `zfs get -Hp -o name,property,value,source [-t] [-d N|-r] props ds`.
Real refs: `$OZ` = openzfs checkout. Sim: `internal/zfs/zfstest/sim.go`. Parser: `internal/zfs/zfs.go`.

Key mechanism: `get_callback` (cmd/zfs/zfs_main.c:2054) emits a line for every requested
property. If `zfs_prop_get` fails and the property is not `pl_all` (i.e. named explicitly, not via
`all`), it prints value `-` with source `-` (zfs_main.c:2096-2104). Only `all` silently skips.
Unknown names are rejected up front (libzfs_util.c:1993 "invalid property", via zprop_get_list
libzfs_util.c:2021), so zfs exits with usage error.

## DIVERGES (most important first)

1. Type-inapplicable explicit props are omitted; real prints `-` `-`.
   Real: zfs_main.c:2096-2104; zfs_prop_get gate libzfs_dataset.c:2547.
   Sim: sim.go:452-464 (volsize) and sim.go:430 (defer_destroy) return `ok=false`, so the line is dropped.
   Impact: `zfs get volsize,refquota,volblocksize <fs>` returns 1 line (or 2) instead of 3. Get() on a fs with only volsize returns "no output" error (zfs.go:144).
   Fix: in `value()`, return `"-", "-", true` for any prop whose type gate fails; keep omission only for `all`.

2. volblocksize reported on filesystems and snapshots.
   Real: volblocksize is ZFS_TYPE_VOLUME only (zfs_prop.c:640), so `-` `-` (libzfs_dataset.c:2547).
   Sim: sim.go:401-405 defaults map is ungated, so fs gets `16384 default`. This is a wrong value the endpoint will consume.
   Fix: gate `volblocksize` to volumes (return `-`/`-` otherwise).

3. quota / reservation / refquota / refreservation / sharenfs / sharesmb not type-gated.
   Real: quota, refquota, sharenfs, sharesmb are FS only; reservation and refreservation are FS|VOL (zfs_prop.c:690-701, 2547). On a zvol refquota is `-` `-`; on a snapshot all of them are `-` `-`.
   Sim: sim.go:465 returns `0 default` (or `off default`) for every type.
   Fix: add a per-prop type set (`fs`, `vol`, `snap`) and apply it in `value()` before the defaults lookup.

4. `available` shown on snapshots.
   Real: available is FS|VOL (zfs_prop.c:628), so snapshots are `-` `-`.
   Sim: sim.go:419-420 returns PoolSize for every type.
   Fix: gate to filesystem and volume.

5. `used` value and source.
   Real: value is the dataset's used bytes; source is `-` because the nvlist entry has no ZPROP_SOURCE, so get_source gives NONE (libzfs_dataset.c:2847-2858, 1945-1962, 2256-2268). This is an inference from the code, not from a test.
   Sim: falls through to defaults, so `0 default` (sim.go:465). The source is wrong and the value is fake.
   Fix: return a numeric value with source `-`, and never `default`.

6. Unknown property names are silently skipped.
   Real: usage error, exit 2, "invalid property '%s'" (libzfs_util.c:1993, zprop_get_list libzfs_util.c:2021).
   Sim: value() returns ok=false (sim.go:465-470), no error.
   Impact: Get() on an unknown prop returns "no output" (zfs.go:144) instead of a real error class.
   Fix: sim returns `exit("invalid property '%s'")` for names not in its known table.

7. Snapshot depth counting with `-d N`.
   Real: a dataset at level L (root = 0) iterates its snapshots only if L < N (zfs_iter.c:122, 140). So a snapshot of a level-L dataset is at level L+1. Root snapshots appear with -d 1. A child's snapshots need -d 2.
   Sim: sim.go:501-505 gives snapshots the level of their dataset, so `a@s` is level 1.
   Impact: List(Depth=0) maps to `-d 1` (zfs.go:345, 406) and returns child snapshots that real zfs omits.
   Fix: for names containing `@`, level = (level of the part before `@`) + 1.

8. Root dataset included even when its type is excluded by -t.
   Real: zfs_callback adds a dataset only if its type matches `cb_types` (zfs_iter.c:88), so `-t snapshot <fs>` prints nothing.
   Sim: sim.go:515 `nm != root` bypasses the filter.
   Impact: low. List() deletes the root (zfs.go:356), so List output is the same. Get with -t would differ.
   Fix: drop the `nm != root` exemption.

9. mountpoint never inherited.
   Real: an unset child inherits its parent's local mountpoint, with source `inherited from <parent>` and value re-rooted (libzfs_dataset.c:2577-2640).
   Sim: sim.go:421-428 checks only local props, else `/<name> default`.
   Fix: walk ancestors for mountpoint, reporting `inherited from <ancestor>` and `/<ancestor-path>/<rest>`.

10. `-t` aliases not accepted.
    Real: `fs`, `vol`, `snap` are aliases (zfs_main.c type_opts, around line 2330-2360; man zfs-get.8).
    Sim: sim.go:515 compares raw strings only.
    Fix: map aliases before comparison. Low priority; the client uses canonical names.

Parser note (zfs.go:35-41): Prop.Uint maps `-` to 0. Real `-` means "not applicable" (volsize on fs), and the endpoint must not read that as 0. Prefer checking Value == "-" before calling Uint.

## MATCHES

- Unset user prop: `-` `-` (zfs_main.c:2145; sim.go:444-450 returns `-` `-` when no ancestor sets it). MATCH.
- User prop set locally: `local`; inherited: `inherited from <ds>` (libzfs_util.c:1550-1630). Snapshots inherit user props from their dataset (`dsl_prop.c:1223-1230`), and sim.go:444-450 does the same via parent(). MATCH.
- refquota / refreservation on fs, unset: `0` `default` with -p (libzfs_dataset.c:2671-2690, 0 becomes "0" when literal). Sim defaults give `0 default`. MATCH for fs.
- volsize on zvol: raw number, `local` (sim.go:452-456 with props set at create). MATCH.
- volsize on zvol snapshot: value from head type, source `-` (libzfs_dataset.c:2033-2040). Sim.go:457-461. MATCH (mild inference on source).
- origin: non-clone `-` `-` (libzfs_dataset.c:2646-2656 returns -1 when no origin; zfs_main.c:2103). Clone: name, source `-`. Sim sim.go:414-420. MATCH.
- defer_destroy on snapshot: `on`/`off`, source `-` (zfs_prop.c:543; boolean_table zfs_prop.c:308-311 is off/on; index path libzfs_dataset.c:2923-2930). Sim sim.go:429-436. MATCH.
- creation: -p gives raw unix seconds (libzfs_dataset.c:2557-2572), source `-`. Format matches; sim value is the constant 1700000000.
- type: `filesystem`/`volume`/`snapshot`, source `-` (libzfs_dataset.c:2738). Sim sim.go:412. MATCH.
- name: name, source `-`. Sim sim.go:410. MATCH.
- mountpoint: `-` `-` for zvol and snapshot (zfs_prop.c:582 is FS only). Sim sim.go:421-428. MATCH.
- source strings: real emits `-`, `default`, `local`, `temporary`, `inherited from X`, `received` (libzfs_util.c:1580-1610). Never `none`. Sim emits `-`, `default`, `local`, `inherited from X`. MATCH for what the sim models.
  MISSING: `received` (requires zfs receive, see libzfs_dataset.c:2262-2270) and `temporary` (mount-option props such as atime, exec, readonly, libzfs_dataset.c:2110-2130).

## Answers to the numbered questions

1. -p values: raw numbers; `0` for zero quota/reservation (not `none`); `-` only for "not applicable or unset". Above.
2. Always `-` source: name, type, creation, origin (non-clone), used/available (inference from libzfs_dataset.c:2847-2858). Sources that appear: local, default, inherited from X, received, temporary.
3. Non-clone origin: `-` value and `-` source. defer_destroy on snapshots: `on`/`off`; on filesystems: line `-` `-` (not omitted).
4. creation under -p: raw unix seconds.
5. -d N and snapshots: a snapshot counts one level below its dataset (see DIVERGES 7). `-t filesystem,volume,snapshot` includes the root only if the root's type is in the list (zfs_iter.c:88). The client's List deletes the root, so that is harmless.
6. Output order: real order is zfs_compare (zfs_iter.c:258): full name by strcmp, a dataset before its snapshots, snapshots by createtxg. The man page does not promise an order. Within a dataset, property lines follow the requested order (zprop_print_one_property). The parser keys by name and List sorts, so order does not matter to this client.
7. Invalid for type: prints `-` `-`, no error. Unknown name: usage error exit. Sim: silent skip for both (DIVERGES 1, 6).
8. mountpoint `-` for zvols and snapshots: MATCH.
