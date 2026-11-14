# Adversarial fact-check: file-export.md NFS/SMB claims

Pins: openzfs a128b8a0 (checkout), nfs-utils 5090e0bb (GitHits), samba 49e0346e (GitHits).
Paths below are relative to the openzfs checkout unless absolute.

## BROKEN

### B1. Exit status and endpoint notification (claim 3): BROKEN
- `sa_commit_shares()` returns void and discards the protocol `commit_shares()` result: lib/libzfs/libzfs_share.c:84-90. `nfs_commit_shares()` returns the `exportfs -ra` status (lib/libzfs/os/linux/libzfs_share_nfs.c:514-527), and nothing reads it.
- `zfs_commit_shares()` is void: lib/libzfs/libzfs_mount.c:813-821.
- `changelist_postfix()` returns `rc == SA_SYNTAX_ERR ? rc : 0` (lib/libzfs/libzfs_changelist.c:~357-368). `rc` comes from `zfs_share()`, which returns -1 on error (zfs_error_fmt, lib/libzfs/libzfs_util.c:~374-392). So write failures and share failures return 0.
- Result: `zfs set sharenfs=...` exits 0 when the exports write fails, when the share fails, and when exportfs fails. The endpoint gets no error. The design's self-heal (`Mounted && !NFSLive` next plan) is the only recovery, and the apply step reports success.
- `zfs share` (cmd/zfs/zfs_main.c:7282) also exits 0 on an exportfs failure.

### B2. Re-share return value (design's "unverified" item): ANSWERED, returns 1
- `zfs share <ds>` on an already-shared dataset returns 1 when explicit (cmd/zfs/zfs_main.c:~7255-7275; `if (!explicit) return 0`). The design's `OpShareNFS` is safe only because the planner gates it on `!NFSLive`. Do not treat a re-share as idempotent.

### B3. "Re-export is an empty plan" depends on property equality, not line content: PARTLY BROKEN
- Different option strings produce the same exports line:
  - `@10.0.0.0/8` and `10.0.0.0/8`: `get_linux_hostspec` strips `@` (libzfs_share_nfs.c:244-251).
  - `anon=N` and `anonuid=N`: key rewritten (:355-356).
  - `rw` and `rw=*`: bare `rw` becomes `*` (:140-142).
- Consequence: a stored non-canonical string gives a non-empty plan forever (not a broken empty plan). The planner compares the stored property to `Canonical()`, so the canonical form must be the only form ever written.
- `NFSLive` checks only that a line for the mountpoint exists (libzfs_share_nfs.c:300-310). It does not compare content, so drift in the exports line (edited file, another exports.d entry, manual exportfs) is not detected.

### B4. `sharenfs=on` with no client list exports to everyone (claim 6): BROKEN as a risk
- `"on"` is rewritten to `"rw,crossmnt"` (libzfs_share_nfs.c:61-62). A bare `rw` becomes host `*` (:140-142). The exports line is `... *(sec=sys,rw,...)`.
- The design's `Canonical()` rejects empty `Clients`, which covers only the endpoint path. Any `on` set out of band, or inherited from an ancestor, exports to all hosts. The design's mitigation (startup check of the parent) is the only guard. The design does not state this.

## HOLDS / PARTLY HOLDS

### H1. Per-client syntax (claim 1): HOLDS, with notes
- `rw=`/`ro=` take `:`-separated hosts, strip nothing else, accept `[v6]/len` and `[v6]:`, and bare `rw` means `*` (libzfs_share_nfs.c:137-210 callback, :317-353 validator).
- Non-host options are global: `nfs_add_entry` writes one `mp host(sec=..,access,opts)` line per host with the same options (:410-435). Squash cannot vary per client. The design's claim holds.
- `root=` is rejected: `valid_keys` (:303-309) has no `root`, so `get_linux_shareopts_cb` returns SA_SYNTAX_ERR (:355-400).
- Every line also gets `no_subtree_check` and `mountpoint` (:~383-389) and `sec=sys` (`foreach_nfs_host` sets security "sys", :222-226). The design says Canonical never emits `sec`; that is correct, but the output line always has it.
- `sec=` is handled only as a special key (:137-138, :314-315).

### H2. Unmounted datasets (claim 2): DEFERS SILENTLY
- `zfs_is_mountable()` returns false for `mountpoint=none|legacy`, `canmount=off`, or not-mountable (lib/libzfs/libzfs_mount.c:286-291). `zfs_share()` then returns 0 with no line written (:748-749).
- `zfs set sharenfs=` on an unmounted dataset: `changelist_postfix` mounts it only if `canmount=on` (libzfs_changelist.c:~313-336). A mount failure is ignored (`if (zfs_mount(...) == 0) mounted = TRUE`). The result is no share and exit 0.
- So: canmount=off means the property is stored and nothing is exported until mounted. A later mount does not necessarily re-share it; the planner's `Mounted && !NFSLive` branch is the only path.

### H3. Boot restore (claim 4): HOLDS
- etc/systemd/system/zfs-share.service.in:4-17: `Before=nfs-server.service nfs-kernel-server.service`, `After=zfs-mount.service`, `After=smb.service`, `Type=oneshot`, `RemainAfterExit=yes`, `ExecStart=zfs share -a`. `PartOf=nfs-server` means an nfs-server restart also restarts this unit.
- `zfs share -a` truncates `zfs.exports` first (cmd/zfs/zfs_main.c:7533-7535; `nfs_reset_shares` at libzfs_share_nfs.c:279-288 uses `! truncate(...)` and ignores the result). Unmountable datasets are skipped.
- `exportfs -ra` runs at the end (libzfs_mount.c:~1346-1356), but its failure is ignored (see B1).

### H4. nfs-utils reads exports.d (design's "unverified"): HOLDS
- nfs-utils `utils/exportfs/exportfs.man:40-45`: `exportfs -a` initializes the master table from `/etc/exports` and files under `/etc/exports.d`. The `*.exports` suffix filter was not verified.

### H5. SMB (claim 5): HOLDS, with notes
- `smb_available()` requires `/usr/bin/net` and a directory at `/var/lib/samba/usershares` (lib/libzfs/libzfs_share.h:118-119; libzfs_share_smb.c:50-67). If either is missing, `smb_enable_share` returns SA_SYSTEM_ERR (:~260-275). `zfs share` then errors. `zfs set sharesmb=on` exits 0 (see B1 pattern, since the rc is -1).
- Samba default `usershare max shares = 0` (source3/param/loadparm.c:882), so `net usershare add` fails until it is raised. `usershare owner only` defaults to true (loadparm.c:884). Its interaction with root-run `net -S 127.0.0.1` is not verified.
- Share created with `Everyone:F` and no guest access (libzfs_share_smb.c argv; man/man7/zfsprops.7:1849-1857).
- The design file does not mention SMB at all. This claim is out of its scope.

## Not verified
- Inherited `sharenfs` semantics (PROP_INHERIT, module/zcommon/zfs_prop.c:585-587) were not re-read.
- `zfs share -a` restore with the exports file truncated mid-run under real exportfs.
