# Gap: fencing and single-writer guarantees

Checkout: `repos/openzfs` at a128b8a (sparse: `module/zfs`, `man`, `cmd/zfs`, `tests/zfs-tests/tests/functional/channel_program` added). Paths are relative to that checkout unless absolute. Nothing was executed. No Lua interpreter is installed here, so the Lua below is not syntax-checked.

## CLAIMS CHECKED

**(a) One txg, so get_prop + compare + set_prop is a CAS. HOLDS, with limits.**
- The script runs as the syncfunc of one `dsl_sync_task_sig` with a NULL checkfunc: `module/zfs/zcp.c:1157`, `zcp_eval_sync` at `zcp.c:983-997`, `zcp_eval_impl` at `zcp.c:810`. The syncfunc call is `module/zfs/dsl_synctask.c:244-246`, under the config rwlock (`:243`).
- Other user-prop writers also use `dsl_sync_task` (`module/zfs/dsl_prop.c:1043`), so they serialise with the script in the same txg's task list.
- Read-after-write inside one script works: `tests/.../synctask_core/tst.set_props.zcp` sets a prop and then calls `get_prop` to verify it.
- Limit 1: each `zfs.sync.*` call checks and syncs only itself (`module/zfs/zcp_synctask.c:80-103`). Nothing checks across calls, so a dependent pair in one script is not protected.
- Limit 2: `zfs-program.8:37` says "executed atomically". That means no interleaving, not rollback.
- Limit 3: `get_prop` on an inherited user prop returns the ancestor's value and its setpoint (`zcp_get.c:147-175`, `zfs-program.8:318-324`). The CAS must compare the setpoint, not only the value.

**(b) `zfs.sync.set_prop` supports user properties only. HOLDS, but it does not kill the epoch.**
- `module/zfs/zcp_set.c:68-70` returns EINVAL for any non-user prop. Man: `zfs-program.8:435`.
- The epoch is a user property (`<prefix>:writer_epoch` contains `:`), so it is allowed.
- What the restriction does kill: fencing any op that is not user-prop-only. `zfs.sync` has no create, no resize (`refquota`, `volsize`), no clone-with-props (`zcp_synctask_clone_info` takes two string args, `zcp_synctask.c:114-127`) and no snapshot-with-props. See DESIGN, segments.

**(c) A Lua error still commits the txg. HOLDS.**
- Lua errors are caught by `lua_pcall` (`zcp.c:852`). The `LUA_ERRRUN` branch (`zcp.c:897`) only converts the error to a return code. `zcp_eval_sync` is void (`zcp.c:983`). Nothing aborts the tx, so the sync task commits.
- Docs agree: `zcp.c:64-66`, `zcp.c:74-76`, `zfs-program.8:148-158` ("may have partially executed", "No attempt is made to reverse").
- Consequence: any compare must run before the first mutation. A Lua error before the first `zfs.sync` call is clean. A Lua error after it is not.
- No test in the tree asserts partial commit. The lua_core tests check error codes only. Verified by code, not by test.
- Also: ZFS API failures are returned as numbers, not raised (`zcp_synctask.c:80-103`). A script that does not test the return value keeps going after a failed mutation.

**(d) `zfs program` requires root. PARTLY BROKEN.**
- The ioctl policy is `secpolicy_sys_config`, which on Linux is `priv_policy(cr, CAP_SYS_ADMIN, EPERM)` (`module/os/linux/zfs/policy.c:86-92`, `module/zfs/zfs_ioctl.c:1292-1300`). It is not uid 0. The same policy covers `-n` (`zfs_ioctl.c:8131-8132`).
- Man `zfs-program.8:40` says "root privileges". The code checks CAP_SYS_ADMIN in the global zone.

**Other prior-man claims that are wrong:**
- `zfs-program.8:324`: "Throws a Lua error if ... the property doesn't exist." For user props, ENOENT returns nil and no error (`zcp_get.c:121-126`, `zcp_get.c:166-171`). An absent epoch is nil.
- `zfs-program.8:511`: documents `zfs.check.set_property`. The registered name is `set_prop` (`zcp_synctask.c:523`, loader `:641` uses `info->name`). Use `zfs.check.set_prop`.
- CLI arguments reach Lua as strings in `args["argv"]` (`cmd/zfs/zfs_main.c`, `zfs-program.8` Arguments). Compare epochs as strings.
- `zfs program` takes the script as a file path, or `-` for stdin (`cmd/zfs/zfs_main.c`, zfs_do_channel_program). Output with `-j` is the nvlist as JSON, with the return value under `return` (`zfs_main.c`, `nvlist_print_json`).

## DESIGN

**State.** Epoch = local user property `<prefix>:writer_epoch` on the parent dataset, decimal uint64. Absent means 0. Inherited means foreign.

**Row (`internal/endpoint/table.go`).** Add to the parent's row, decoded from the existing single scan by adding the prop to the column list:
```go
WriterEpoch  uint64 // parent only: local <prefix>:writer_epoch, 0 if absent
EpochForeign bool   // present but inherited or not parseable as uint64
```
Pure decode: `func decodeEpoch(p zfs.Prop) (epoch uint64, foreign bool)`, using `Prop.Local()` (`internal/zfs/zfs.go:43`).

**Op kinds (`plan.go`).** Add `OpFence`: `Target` = parent, `Epoch` = held, `Next` = epoch to install (0 = none).

**Planners (pure).**
- `planAcquire(parent Row, prefix string) (Plan, error)`: error if `EpochForeign`. Otherwise `Plan{{Kind: OpFence, Target: parent.Name, Epoch: parent.WriterEpoch, Next: parent.WriterEpoch + 1}}`.
- `fence(plan Plan, held uint64) Plan`: prepends `OpFence{Epoch: held}`. The `commit` op (`plan.go:124`) is user-prop only and stays ZCP.
- `segments(plan Plan) []Segment`: splits into maximal ZCP runs and native runs. Native ops are creates, clone with props, snapshot with props, and OpSets that touch native keys (`plan.go:164`, `:235`, `:322`). Each ZCP run gets its own fence. A fence followed by a native op is its own segment, so the fence is not atomic with that op.

**Compiler (pure).** `compileZCP(prefix string, seg Segment) (script string, argv []string, err error)`. The script is a constant header plus a fixed per-OpKind snippet. All user data goes in argv, never in script text. Golden tests cover it.

**Go helper (`internal/zfs/program.go`).**
```go
func (c *Client) Program(ctx context.Context, pool, script string, args ...string) (Result, error)
```
Writes the script to a 0600 file in a fresh `os.MkdirTemp` dir, because `Runner` has no stdin. Then runs `zfs program -j <pool> <file> <args...>` through `c.Runner`. Parses `{"return": {...}}`. A non-zero exit becomes an `*Error`, and the caller must treat it as indeterminate and rescan. Reject any arg beginning with `-`, since getopt permutes arguments.

**Fence script, verbatim (Lua 5.2 syntax; zcp runs 5.2).** argv: 1 dataset, 2 epoch prop, 3 held epoch, 4 next epoch or "".
```lua
local args = ...
local argv = args["argv"]
local ds, prop, held, nxt = argv[1], argv[2], argv[3], argv[4]

local cur, src = zfs.get_prop(ds, prop)
if cur == nil then
    cur, src = "0", ds
end
if src ~= ds or tostring(cur) ~= held then
    return { fenced = 1, have = tostring(cur) }
end

if nxt == nil or nxt == "" then
    return { fenced = 0, epoch = tostring(cur) }
end
local err = zfs.sync.set_prop(ds, prop, nxt)
return { fenced = 0, epoch = nxt, err = err }
```

**Mutating segment, verbatim example (fence + snapshot + user-prop set).** argv: 1 to 4 as above (argv[4] = ""), 5 snapshot full name, 6 dataset, 7 prop, 8 value. Check everything first, then sync.
```lua
local args = ...
local argv = args["argv"]
local ds, prop, held = argv[1], argv[2], argv[3]

local cur, src = zfs.get_prop(ds, prop)
if cur == nil then
    cur, src = "0", ds
end
if src ~= ds or tostring(cur) ~= held then
    return { fenced = 1, have = tostring(cur) }
end

local ck_snap = zfs.check.snapshot(argv[5])
local ck_set = zfs.check.set_prop(argv[6], argv[7], argv[8])
if ck_snap ~= 0 or ck_set ~= 0 then
    return { fenced = 0, phase = "check", snapshot = ck_snap, set = ck_set }
end

local sy_snap = zfs.sync.snapshot(argv[5])
if sy_snap ~= 0 then
    return { fenced = 0, phase = "sync", snapshot = sy_snap }
end
local sy_set = zfs.sync.set_prop(argv[6], argv[7], argv[8])
return { fenced = 0, phase = "sync", snapshot = sy_snap, set = sy_set }
```
Invariant: planner never puts the epoch prop itself in a body op, and never puts two dependent ops in one segment. The check phase cannot see the sync phase's effects.

**Acquisition at startup.**
```
for attempt := 0; attempt < N; attempt++ {
    row := scan(parent)                       // one scan, decodes WriterEpoch
    if row.EpochForeign { return errForeignEpoch }
    res := Program(fenceScript, parent, prop, itoa(row.WriterEpoch), itoa(row.WriterEpoch+1))
    if res.Fenced { backoff; continue }        // someone else installed first
    if res.Err != 0 || indeterminate { return err }
    held = row.WriterEpoch + 1; return nil
}
```
This is a CAS loop. Two starters that read E: one installs E+1, the other is fenced and re-reads. Each process start bumps the epoch, so the last start wins. Leader election (Lease) stays liveness-only.

**Fenced writer.** `held` lives in memory and changes only in acquire. A fenced segment (`fenced=1`) marks the endpoint deposed. It refuses mutations until restart and returns `Aborted` (existing code 10, `internal/endpoint/errors.go:14`). No automatic re-acquire, since that would fight the new writer.

**Proto (`api/zfsendpoint/v1/endpoint.proto`).** No change. The fenced error reuses `Aborted`. The enum was not otherwise opened here.

**Admin running zfs by hand.**
- `zfs create|destroy|set|inherit|rename` on data are unfenced. The next scan sees them, and the planner converges. A stale writer can still create or resize once after deposition (see RISKS).
- `zfs set <prefix>:writer_epoch=<other>` deposes the daemon at its next ZCP segment. Restarting it bumps again. Setting a non-decimal value like `frozen` makes acquire refuse (EpochForeign), which works as a manual freeze. `zfs inherit` makes the epoch absent (0), which deposes any writer that holds at least 1.
- A hand-run `zfs program` that does not use the fence is unfenced.

**Does (b) kill the idea?** No for the epoch, which is a user prop. It does kill fencing of create, resize, clone-with-props and snapshot-with-props. Those stay outside the fence.

## TEST PLAN

- **Planner tables (pure, no ZFS).** `decodeEpoch` (absent, local, inherited, non-numeric). `planAcquire` (fresh, held, foreign). `fence` prepends exactly one OpFence. `segments` splits known plans (create, then commit, then snapshot). `compileZCP` golden test: script text and argv, with no user text in the script.
- **Sim.** In-memory pool, serialised sync tasks, and an interpreter for the three script shapes. Two writers plus an admin edit. Properties: no mutation applies unless `held` equals the epoch at that segment's start; restart always re-acquires; no lost update in the acquire loop.
- **Real ZFS CI (privileged, file-backed pool, root or CAP_SYS_ADMIN).**
  - Stale held epoch: fenced, zero mutations.
  - Two concurrent acquirers: distinct epochs, one fenced.
  - Lua error after a sync op: the op stays committed. This documents (c).
  - `zfs set` bump: deposes at the next segment.
  - `zfs inherit`: absent counts as 0.
  - Unprivileged user with CAP_SYS_ADMIN dropped: EPERM. This tests (d).
  - Parent setpoint: a child with an inherited epoch is fenced.

## RISKS

- **Unfenced native ops.** Create, resize, clone-with-props and snapshot-with-props run outside the fence. A deposed writer can finish one in-flight native op, possibly with a stale size. Convergence needs the new writer's next scan to correct it, so the window is bounded by one op plus one planner pass.
- **Check does not see sync.** Dependent ops in one segment will fail the check or act on stale state. The planner must segment them.
- **Indeterminate results.** A Lua error or a non-zero exit after a sync call may leave part of a segment committed. Treat it as indeterminate and rescan before the next op. Replay must be idempotent, as the existing `OkExists` and `OkMissing` tolerances are.
- **Uint64 in Lua.** Compare epochs as strings. Do not convert them to Lua numbers, because int64 wraps.
- **Replication and rename.** Local user props travel with `zfs send` and `zfs receive`, so a receiver could inherit a foreign epoch. A rename moves the epoch while the config still names the old parent. Both unverified in this checkout.
- **Version.** Only the a128b8a tree was checked. Other OpenZFS releases are unverified.
- **Privilege.** CAP_SYS_ADMIN, not root, is the real requirement (see (d)). The daemon's capability set must be checked at deploy time.
