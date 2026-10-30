# Adversarial review: fencing.md

Reviewed: `align/gaps/fencing.md`. It was checked against `/home/user/empty` (README.md, `internal/endpoint/{plan,apply,endpoint,table,errors}.go`, `internal/zfs/{zfs,runner}.go`) and against OpenZFS a128b8a (`man/man8/zfs-program.8`, `module/zfs/zcp*.c`, `dsl_prop.c`, `dsl_destroy.c`, `dsl_synctask.c`, `zfs_ioctl.c`, `module/os/linux/zfs/policy.c`, `cmd/zfs/zfs_main.c`, `tests/.../channel_program`). Nothing was executed, and the repo was not modified.

## Recommendation: DROP

Drop the epoch fence as a safety mechanism. It does not fence the operations that can do damage. Its one real CAS has an ABA hole. Every safe outcome it produces depends on an external "only one starts" guarantee, and that guarantee is already the fence. Use these instead:
- **multihost=on plus pacemaker** (ZFS resource agent, STONITH, the daemon colocated with the pool) for the case across hosts.
- **flock(LOCK_EX|LOCK_NB)** on a host-local lockfile for the case on one host.

If the team still wants something stored in ZFS, keep only a detection tripwire: a random writer-UUID stamp read with plain `zfs get`, labelled as detection and not as fencing. The changes needed to make the epoch design survivable are listed under ADOPT-WITH-CHANGES at the end, for reference only.

## Ranked findings

### 1. CRITICAL: the epoch can go backwards (ABA), so two writers can hold the same epoch
- The design says `zfs inherit` "makes the epoch absent (0), which deposes any writer that holds at least 1". That is only half the effect. The next starter reads absent = 0 and installs **1**. A still-running old writer that holds 1 now passes every fence, alongside the new one.
- The same thing happens after:
  - `zfs set <prefix>:writer_epoch=<any lower number>`;
  - a pool rewind (`zpool import -T`/`-F`, or `--rewind-to-checkpoint`), which restores an older MOS;
  - an admin setting `18446744073709551615`: Go's `+1` wraps to 0, the absent sentinel.
- Clock-free counting is fine. Monotonicity is simply not guaranteed by anything.
- **Fix if kept:** CAS on a unique token, not a counter. Use 128 random bits, or boot-id + pid + nonce, and compare with string equality. Fencing needs uniqueness, not ordering.

### 2. CRITICAL: the fence does not cover the operations that matter, and fence-then-native is an unbounded TOCTOU

This is how each planner op in `plan.go` maps onto `zfs.sync` at a128b8a.

| Op (plan.go) | ZCP-able? | Evidence |
|---|---|---|
| OpCreateFS :208, OpCreateVol :200 | no | `zfs.sync` has no create |
| OpClone with ownership props :159, :186 | no | `zcp_synctask_clone_info` has 2 positional args and no props (`zcp_synctask.c:112-125`). Clone then `set_prop` can partially commit and leave an **unmanaged** clone, which `PlanCreateVolume:127` then rejects with AlreadyExists forever |
| sizeOps volsize/refquota/refreservation :215-235, ExpandVolume :312-322 | no | `zcp_set.c` returns EINVAL for non-user props |
| **DeleteVolume: destroy -r :281** | **no** | `zcp_synctask_destroy` calls `dsl_destroy_head` with no recursion (`zcp_synctask.c:155-235`). `dsl_destroy_head_check_impl` returns EBUSY on long holds (mounted fs, open zvol) and on existing snapshots, and EEXIST on child datasets (`dsl_destroy.c:829-850`). ZCP does not unmount |
| CreateSnapshot with props :355 | no in practice | `ddsa_props = NULL` (`zcp_synctask.c:335`) |
| commit marker :124, DeleteSnapshot defer :375, internal clone-source snapshot :185, deferred origin destroy :283 | yes | these are the only fenced ops |

- So the most destructive operation (recursive volume delete) is unfenced, along with every create and resize. The fenced set is the bookkeeping.
- Fencing the commit while the create is unfenced only turns a stale create into an uncommitted row, which the new writer then adopts.
- **TOCTOU.** A fence segment followed by a separate `zfs create` exec proves only that the writer held the epoch at T1. A GC pause, SIGSTOP, a frozen cgroup or a slow exec makes T2 arbitrarily late. Kleppmann's rule applies: a fencing token protects nothing unless the storage checks it on the write, and native ZFS ioctls do not check it.
- The RISKS line "bounded by one op plus one planner pass" is false in two ways:
  - The bound is per in-flight goroutine. Locks are per key, so many requests run concurrently.
  - The window is unbounded in **time**. It is not bounded per op.
- **Corollary.** A sync-context fence before a native op gives nothing that `zfs get -Hp <prop> <parent>` would not. Only the CAS in acquire, and segments that themselves mutate through ZCP, use the txg atomicity.

**Verdict on point 2.** Measured against "zpool multihost=on + pacemaker + one active endpoint", the epoch design has these weaknesses:
- It cannot help across hosts. If a pool is ever imported twice, no property at the dataset layer survives, and MMP plus STONITH is the only real fence there.
- On one host, `flock` held for the life of the process covers native ops too. The kernel releases it on death, it costs nothing in ZFS, and it has no TOCTOU, because a paused holder still holds the lock.
- The epoch adds Lua, segmenting, a sim interpreter, privileged CI and txg-sync latency, in exchange for fencing bookkeeping only.

**DROP.**

### 3. HIGH: the fence fails open on zero values and in unspecified paths
- **Absent matches `held == "0"`.** The script maps a missing prop to `"0"` with `src = ds`. A daemon whose `held` is still the Go zero value, because of a skipped or failed acquire or a code path bug, passes every fence on a pool with no epoch: a fresh pool, or one after `zfs inherit`. The fix is that a fence requires presence, and only acquire may treat absent specially.
- **The return shape decides pass or fail by its default.** `fenced = 0` means pass. If Go decodes `{"return":{...}}` into a struct, a missing or renamed key, or a missing `return`, decodes as `Fenced == 0`, which is a pass. A table with a nil field also silently drops that key (`zfs-program.8` value conversion: `nil -> boolean (no value)`). The fix is a positive token: return `ok = "<held>"`, and have Go require it to be present and equal.
- **Indeterminate before a native op is unspecified.** The design says a non-zero exit means "indeterminate, rescan". For the fence that guards a native op, the spec must say "do not run the op". A rescan cannot tell you whether you are still the writer.
- **Context cancel is another source of indeterminate results.** Killing `zfs program` sets `zri_canceled` (`dsl_synctask.c:80-89`, `zcp.c:781-786`), and the instruction hook can then abort mid-script. It is covered by the indeterminate rule only if that rule is the one above.

### 4. HIGH: "last starter wins" is takeover, not mutual exclusion
- The acquire CAS is correct. Sync tasks run one after another in syncing context, and the second sees the first's ZAP write, so two racers end with distinct epochs.
- But the loser simply retries and **deposes the winner**. Any process that starts takes over. A standby that boots, a mis-scheduled second pod, or a systemd `Restart=always` loop on two daemons produces flapping:
  - each new start deposes the other;
  - each daemon gets an unfenced native window between depositions (finding 2).
- The design's safety therefore rests on something external that ensures only one starts. That external thing (flock, pacemaker) is the fence, and the epoch is redundant.
- **The deposed state returns `Aborted`**, which the README defines as "busy, retry". CSI sidecars will retry a daemon that can never recover. A deposed daemon should exit, and must not be restarted automatically. Doing that needs the HA manager again.

### 5. MEDIUM: Go and Lua disagree about what "local" means, and the plan does not fit the code
- **Received values.** `zfs.Prop.Local()` treats `received` as local (`internal/zfs/zfs.go:43`). The kernel reports a value that is only received with setpoint `"$recvd"` (`dsl_prop.c:144-156`). After `zfs send -R` to a DR pool, Go decodes a valid epoch, the Lua sees `src ~= ds`, and acquire loops "fenced" N times with the misleading reason "someone else installed first".
- **Non-canonical numbers.** `strconv.ParseUint("01")` gives 1, but Lua compares the strings `"01"` and `"1"`, which gives fenced on every attempt and the same livelock. `decodeEpoch` must treat a value as foreign unless `FormatUint(v) == raw`.
- **The parent row does not exist.** `scan` deletes the parent row (`table.go:199-202`), so "decoded from the existing single scan" does not work. Acquire needs its own `Get(parent, prop)`.
- **The temp-file approach breaks the Runner seam.** `Runner` is documented as the seam for "an SSH or libzfs_core runner later" (`runner.go:11-18`). A local temp-file path does not exist on a remote host. Either add stdin to `Runner` and use `-`, or put the script inline over libzfs_core.
- **Rejecting args that start with `-` rejects legal input.** `snapRE` allows a snapshot component that begins with `-` (`zfs.go:68`). Put `--` before the pool so getopt stops parsing there (`zfs_main.c` uses `getopt_long`).

### 6. MEDIUM: cost on the request path
- Every fence, including one that only reads, is a `dsl_sync_task_sig`. It waits for a txg sync, which can take up to `zfs_txg_timeout` (5 s by default) under write load (`zcp.c:1155-1160`).
- The ioctl is registered with logging on (`zfs_ioctl.c:8131-8135`), and the logged output is capped at `zfs_history_output_max` = 1 MiB (`zfs_ioctl.c:232-237`). So every request writes the script to pool history, which pushes useful admin history out of the ring buffer.
- A CreateVolume from a volume would run four or more segments: snapshot, fence + clone, fence + set, commit. Each is a txg wait, set against external-provisioner's 10 s timeout (README).

### 7. MEDIUM: the example mutating script matches no real plan
- `fence + snapshot + set_prop` is presented as the example. The only plan that creates a snapshot together with a user-prop set is `PlanCreateSnapshot`, and its props belong on the **new** snapshot.
- `zfs.check.set_prop(newsnap, ...)` runs before `zfs.sync.snapshot`, so it returns ENOENT. The example therefore always returns `phase = "check"`.
- It also contradicts the design's own rule that "snapshot-with-props is native".
- `zfs.sync.set_prop` *after* `zfs.sync.snapshot` would see the new snapshot, but only by giving up the "check everything first" invariant and accepting partial commits.

### 8. LOW: privilege and the environment
- The ioctl policy is `zfs_secpolicy_config` → `secpolicy_sys_config`, which means a global-zone check plus `ns_capable(cr->user_ns, CAP_SYS_ADMIN)` (`policy.c:44-49, 86-92`; `zfs_ioctl.c:1292-1300`).
- The daemon can therefore never run unprivileged with `zfs allow` delegation, which the native ops alone would permit. Channel programs are not delegable.
- The man page's "root" is wrong. The real requirement is CAP_SYS_ADMIN in the caller's user namespace and the global ZFS zone.

### 9. NON-ISSUES (checked)
- **Instruction and memory limits.** Every script is O(1): a fixed number of `get_prop` and `sync` calls, and no `zfs.list` iteration. Volume count does not affect them. The defaults (10M instructions, 10 MiB) are far above what they need; the maxima are 100M and 100 MiB (`include/sys/fs/zfs.h:2041-2044`, `zfs_ioctl.c:4595-4598`).
- **Pool features.** `zfs.sync.snapshot` needs SPA version 27 or later (`zcp_synctask.c:325-328`), which every feature-flag pool meets. A read-only pool gives EROFS (`zfs_ioctl.c:4600-4610`), and a suspended pool is refused by `POOL_CHECK_SUSPENDED`. Both fail closed.
- **The Lua against the zcp API is correct:**
  - The function names, argument order and positional types match `zcp_synctask.c` and `zcp_get.c`.
  - `get_prop` returns nothing (nil, nil) for a missing user prop, through ENOENT → `zcp_handle_error` returns 0 (`zcp_get.c:120-141, 146-176`).
  - `zfs.check.set_prop` is the registered name (`zcp_synctask.c:523, 641`). The man page's `set_property` is wrong.
  - `sync` and `check` return numeric errnos and do not raise.
  - A missing dataset raises a Lua error (`zcp_dataset_hold`), which exits non-zero and fails closed **if** finding 3 is fixed.
- **The CAS within one txg holds,** as the design says (claim a).

## If kept anyway: ADOPT-WITH-CHANGES minimum

1. A unique-token CAS instead of a counter (finding 1).
2. A fence that requires presence, a positive `ok = held` return, and "indeterminate means do not run the following op" (finding 3).
3. Acquire refuses when the current token is held by a live writer, plus external single-start via flock or pacemaker. On deposition, exit; do not return `Aborted` (finding 4).
4. `decodeEpoch` treats received and non-canonical values as foreign, and acquire uses its own `Get(parent, prop)` (finding 5).
5. Replace the sync-context fence before native ops with `zfs get` (finding 6).
6. Document honestly that create, resize, clone and recursive destroy are unfenced (finding 2). Once that is written down, the remaining value is small enough that DROP stands.
