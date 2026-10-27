# ZCP (zfs program) for atomic Plans

Source: local OpenZFS checkout `repos/openzfs` (module/zfs sparse-added). Paths below are relative to that checkout.

## FINDINGS

**1. Supported ops (zfs.sync, executed inside one txg)**
- Registry: `module/zfs/zcp_synctask.c:619-640`: destroy, clone, promote, rollback, snapshot, rename_snapshot, inherit_prop, bookmark, set_prop.
- Man page: `man/man8/zfs-program.8:354` clone, `:380` destroy (`defer` only for snapshots), `:412` promote, `:422` rollback, `:433` set_prop, `:446` snapshot, `:458` rename_snapshot, `:471` bookmark.
- Yes: snapshot, clone, destroy (incl. defer), promote, user props.
- No create filesystem or volume. No `zfs.sync.create` exists in the registry.
- No refquota, volsize or refreservation. `zcp_set.c:68-69` returns EINVAL for any non-user property (`zfs_prop_user`). The man page says "Currently only user properties are supported" (`:435`).
- Clone takes no properties (`zcp_synctask.c:129-141`). The clone is not mounted (`zfs-program.8:361-365`).
- Destroy is non-recursive. Recursion is written in Lua with `zfs.list.children` (`zfs-program.8` EXAMPLES).

**2. Limits and behaviour**
- Default and max instructions: 10M and 100M (`include/sys/fs/zfs.h:2041-2042`). Default and max memory: 10 MiB and 100 MiB (`:2043-2044`). Out-of-range values give EINVAL (`module/zfs/zfs_ioctl.c:4575`, range check).
- Root: `zfs_secpolicy_config` calls `secpolicy_sys_config` (`zfs_ioctl.c:1292-1300`). Man page `:40`: "Channel programs may only be run with root privileges."
- Structured output: return values convert to nvlist (`zcp.c:314`, `:423`). Numbers become int64. Nesting depth is capped at 20 (`zcp.c:106`). Only one top-level return value is allowed (`man:126-129`). Oversized returns exit nonzero with no value (`man:164-165`).
- `-n` (`zcp_eval_open`, `zcp.c:999-1013`) runs in open context. It can call `zfs.check.*` and reads, and cannot call `zfs.sync.*` (`zcp_synctask.c:90-95`). Atomicity of an open-context read across datasets: unverified.
- Quote: "No attempt is made to reverse or undo any operations already performed" (`man:158`). `zcp_eval_sync` returns void (`zcp.c:983-997`), so a Lua fatal error still commits the txg. Only the ZFS txg is atomic, not the script.
- Pre-validation inside the same txg is possible through `zfs.check.*` (`man:108-113`).

**3. Production users**
- TrueNAS: `middlewared/plugins/zfs/destroy_impl.py:215-266` and `system_dataset/mount.py:129-160` call `truenas_pylibzfs.lzc.run_channel_program`. The Lua is bundled in pylibzfs, not in middleware (`truenas_pylibzfs` `examples/channel_program.py`).
- zrepl: no matches in GitHits.
- Delphix: repo not resolvable in GitHits. Unverified.
- OpenZFS: `contrib/zcp/autosnap.lua`.

**4. Endpoint fit**
- `plan.go:115` commits `provision_success` as an OpSet.
- `plan.go:155`, `:188-226`, `:305-313` set refquota, refreservation and volsize. ZCP cannot do any of these.
- So no Plan can compile wholly to ZCP. Create and resize are not expressible.

## RECOMMENDATION

- Do not compile the whole Plan to Lua. Keep `Plan` as data and add a second backend in `apply.go`. Use it only for batches that are wholly ZCP-expressible: multi-dataset snapshot, destroy and user-prop writes.
- Run `zfs.check.*` and `zfs.sync.*` in the same program. Both run in the same txg, so the check cannot race the action.
- Keep `provision_success` for create and resize. A CLI tail cannot be made crash-atomic with ZCP. Marker removal is not justified.
- Requires root. Confirm the daemon's privilege before adopting.

## RISKS

- A Lua fatal error commits partial work. Pre-check every call, and treat any fatal as needing reconciliation.
- Instruction and memory limits are deterministic, but the 100M/100 MiB caps must cover the largest batch.
- ZFS version differences: the ZCP set above is from the checked-out tree. Other releases are unverified.
- Endpoint OpSet mixes user and native properties. Splitting them changes the Table/Plan contract.
