# TestModel on a real pool: why it is nondeterministic

This is a read-only analysis at 98aef7a, after the 654b864 merge. Nothing in the repo was changed.

Code references are to 98aef7a. The files are copied into `S/flake/`:
- `modelrun_test.go`
- `apply.go`
- `realfacts_test.go`

We cannot read the failure text yet, because it is in the guest log. So the analysis below comes from the code, not from the trace.

Seeds 1 and 2 both failed once and passed once. That rules out any deterministic gap between the model and real ZFS (such as `refreservation` or the `send -R` property set), because a deterministic gap fails on every run. What is left is timing.

## Summary

| # | source | who is wrong | likely? |
|---|---|---|---|
| 1 | The rig gives the endpoint no `Config.Settle`, so a zvol destroy that is busy because udev is probing it is final. | **rig** | **most likely cause** |
| 2 | The rig's real-pool cleanup does a recursive destroy of a parent that holds zvols, with no settle and no retry. | **rig** | likely (a "cleanup" error, not a model trace) |
| 3 | The endpoint does one settle and one retry, but a hold from the ZFS minor taskq is not a udev event. | **endpoint**, mildly | possible, not proven |
| 4 | Shrinking replays a flaky failure, keeps a failure of another kind as "best", and does not say whether it reproduced. | **rig (diagnostics)** | certain, and it made the red runs unreadable |
| 5 | Mount and unmount timing, zed, NFS, MinAge timing, faults and races. | none | ruled out (see below) |

## 1. A busy zvol destroy, with no Settle in the rig (rig wrong)

**What happens on a real pool.** These steps create a block device `/dev/zdN`:
- CreateVolume of a Block volume (`zfs create -V`), and a Block clone from a snapshot or a volume;
- the race action `adm.clone ... v3` and `adm.clone` of a zvol snapshot;
- `adm.recv` of a zvol;
- `adm.rename` of a zvol (udev gets a remove event and an add event).

udev then runs its rules (`zvol_id`, `blkid`) on the new device, and those rules hold it open. A `zfs destroy` that lands during that window returns `dataset is busy`, which is `zfs.ErrBusy` (internal/zfs/errors.go:136).

The model steps are only tens of milliseconds apart: one `zfs get -r` read per step, and nothing else. So within a 25-step sequence it is common for a DeleteVolume, or a Reconcile that collects a Block volume, to follow a few steps after the create.

**What the endpoint does.** In `apply.go destroy` (lines 62-77), the endpoint settles once and retries once only when `e.cfg.Settle != nil`. If that retry is still busy, `opErr` (lines 89-93) maps the busy destroy to `FailedPrecondition`.

**What the rig does.** The rig builds the endpoint with `endpoint.Config{Parent, LockWait, Now}` (modelrun_test.go:495-496). It sets no `Settle`. The other real-pool harnesses do set it:
- the production daemon, at cmd/zfs-endpoint/main.go:34 and :161;
- the endpoint tests, at endpoint_test.go:139-142 (`cfg.Settle = endpoint.UdevSettle`, with the comment "as cmd/zfs-endpoint");
- the CSI harness, at internal/csi/harness_test.go:45.

So on the VM, the model rig runs a configuration no deployment runs: a single udev probe is enough to turn a delete into a final `FailedPrecondition`.

**What the failure would look like.** With a destroy op busy:
- **DeleteVolume** returns `code FailedPrecondition`. That is not `retryable()` (modelrun_test.go:698), so the step is not retried, and `sameResult` fails with `result: returned code 9, model OK`.
- **Reconcile** reports the group as `SkipFailed` and returns OK. Its `Acts` then differ from the model's, or the world still has the zvol (`world: extra vN`).

Both depend on timing, and both fit the timings we saw:
- one failure at 16.6 s: about 5.6 s of sequences, then shrinking;
- one at 24.4 s;
- one pass on the same seeds.

**Smallest fix.** This is test-only, about 4 lines in `newRigOn`, and it keeps every check:

```go
cfg := endpoint.Config{Parent: g.parent, LockWait: time.Second, Now: ...}
if g.sim == nil {
	cfg.Settle = endpoint.UdevSettle // as cmd/zfs-endpoint: a zvol udev is probing is not in use
}
```

The model stays as it is. The contract says a delete of a volume nobody uses succeeds, so accepting `FailedPrecondition` would widen the model for a configuration that never ships.

The fault runner sits below the endpoint, so the settle's retried `destroy` counts as one more mutating command. That is harmless:
- On the simulator, `Settle` stays nil, so fault positions and replays do not change.
- On a real pool, the retry is a second `destroy` call, and a fault already fired cannot fire again (`!r.fired`).

## 2. A busy recursive destroy in the cleanup (rig wrong)

`destroyReal` (modelrun_test.go:504-515) releases holds, then runs `admin.Destroy(parent, Recursive)` and reports `t.Errorf("cleanup ...")` on any error. The parent may hold zvols created moments earlier, by the last step or by the end-of-trace Reconcile, so this can be `dataset is busy` too.

This would show up as a red run with no model trace. It may be what happened in 40494731295 (24.4 s). The `t.Cleanup`s of the many shrink rigs all run at the end of the test, and each one is another chance to hit it.

**Fix.** In `destroyReal`, on `zfs.ErrBusy`, run `endpoint.UdevSettle(ctx)` and retry once. That is the same policy the endpoint uses. Keep the `Errorf` if the retry also fails, because a cleanup that leaks datasets is worth failing on.

TestRealFacts has a related gap: its cleanup (realfacts_test.go `f.zfs("destroy", "-R", f.p)`) ignores the error. The zvols that OWN-12/zvol, VOL-11 and SNP-11 create can leak quietly on the VM pool. Apply the same settle and retry there, and report what is left over.

## 3. A taskq hold that one settle cannot see (endpoint, possible gap)

On Linux, OpenZFS creates zvol minors asynchronously (on the spa zvol taskq). `zfs create -V` and `zfs clone` return before `/dev/zdN` exists, and the minor work holds the dataset while it runs. If a destroy lands in that window:
- `udevadm settle` may return at once, because the kernel has not raised the add event yet;
- the one retry can then meet udev's `blkid` and be busy again.

OpenZFS's own create error path waits on the minor work itself (the endpoint.go:153-165 comment cites zfs_ioctl.c `zfs_ioc_create`). The endpoint has no such wait.

**Impact on users.** A user can hit this only if a DeleteVolume arrives milliseconds after a create. CSI never does that in practice. external-provisioner also retries a failed DeleteVolume with backoff, so the cost is a delayed delete, not a lost volume. That makes it a robustness gap, not a contract violation.

Fix 1 should make the flake go away. If it then comes back as a busy destroy, this is the cause.

**Smallest fix, only if the evidence shows it.** In `destroy`, settle and retry up to 3 times within the ctx deadline (or about 2 s), not once. Each settle then catches the udev event the previous attempt raced. Prove it with a variant of `TestDeleteVolumeSettlesDeviceEventsOnBusy` (settle_test.go:28) whose fake runner is busy twice.

## 4. Shrinking and reporting (rig, diagnostics)

Reading `shrink` (modelrun_test.go:1119-1155) and `modelSweep` (1242-1258) turns up four problems:

- **"best" is not filtered by kind.** `best := runTrace(tb, newG(), trace)` is not checked against `kind`. A flaky replay that fails differently (or not at all) becomes `best`. If no candidate reproduces, `shrink` returns that `best`, and `t.Fatalf` prints it as though it were the original failure. When `best` is nil, the original `first` is used (line 1249), but nothing says that the replay did not reproduce.
- **Shrinking a flaky failure is mostly noise on a real pool.** Every candidate is a fresh real-pool run, so about 11 s and up to 400 rigs go into a search whose oracle is a coin flip. The minimal trace can be a different flake from the one that was detected.
- **`mResult.String()` prints only the code** (`code 9`). The zfs stderr (`dataset is busy`) is dropped in `rig.rpc` (line 726: `res.Code = endpoint.CodeOf(err)`), so the trace cannot tell busy from anything else.
- **The step's zfs command log is not shown.** `g.fr.log` (line 930) is collected but never printed on a failure.

## 5. Ruled out, with the reasons

- **Asynchronous mount and unmount.** `zfs create` and `zfs clone` mount synchronously. `OpMount` is OkExists (plan.go:244). The rig sets no `RequireMounted` and no `FileExport`. `zfs destroy` of a filesystem unmounts synchronously, and it fails busy only if a process has files open in it, which nothing in the rig does.
- **NFS share timing.** The rig sets no `FileExport` or Etab, the model never publishes, and no step sets `sharenfs`.
- **zed.** zed reacts to events (history, scrubs, faults) and takes no dataset holds. zvol links are made by udev, which is covered in 1.
- **MinAge and creation time.** The clock is `time.Now() + 2h*clock` (line 496), and `clock` goes up before every RPC (line 917). Any dataset is therefore at least 2 h old next to `MinAge` = 1 h (`tooYoung`, reconcile.go:385-397). This includes ones made by admin steps, and `CommittedAt` from the endpoint's own clock. Creation having 1-second resolution on real zfs cannot change a decision.
- **The faults.**
  - killBefore, killAfter and errBefore are synchronous in `mFaultRunner.Run` (lines 106-118), and killAfter runs the command under `WithoutCancel`.
  - A race runs its admin action in the same goroutine before the op (line 120).
  - Linearization is decided from the action's actual outcome.
- **Parallel workers.** Each sequence has its own parent, `model-<run>-s<seed>`. `udevadm settle` is global, but that only makes it wait longer.
- **`adm.recvsnap`.** It is skipped on a real pool (line 868).

## 6. Diagnostics that make the next red run explain itself

All are test-only.

1. **Log the first divergence when it is found.** In `modelSweep`, as soon as `first` is set, `t.Logf` the seed, the step, the kind, the message, and the trace prefix `trace[:step+1]`. Do this before any shrinking. The streamed output (98aef7a check-skips.sh) then shows the real failure even if shrinking times out or changes the story.
2. **Keep the error text.** Add `Err string` to `mResult`. Fill it in `rig.rpc` from `err.Error()`, leave it out of `sameResult`, and print it in `String()`. Then a busy destroy reads as `code 9 (op 2/2 destroy pool/model-.../v1: dataset is busy)`.
3. **Print the failing step's zfs commands.** On a failure, add `g.fr.log` to the message, and on a real pool also add each command's exit status. Return the log from `step` through `failure`.
4. **Check reproduction before shrinking.** Replay the failing prefix R times (3 on a real pool) and log "reproduced k/R".
   - If k = 0, report the failure as **nondeterministic**: no shrinking, the original trace, and the error text from 2.
   - If 0 < k < R, still shrink, but on a real pool cap `MODEL_SHRINK` at about 30 runs and print k/R in the Fatalf.
   - Filter `best` by `kind` (fixing the bug in 4), and print `shrink: reproduced=true|false, runs=N` in the final message.
5. **Dump the real-pool state on a failure.** Before the rig is cleaned up, log:
   - `zfs list -t all -r -o name,type,userrefs,defer_destroy,origin <parent>`;
   - for each zvol, `ls /sys/class/block/$(basename $(readlink -f /dev/zvol/<name>))/holders` and `udevadm settle --timeout=0; echo $?`, which says whether udev was still busy.

## Order of work

1. Ship fix 1 (the rig's `Settle`) together with diagnostics 1, 2 and 4.
2. Then fix 2 (settle and retry in the cleanup) and the TestRealFacts cleanup.
3. Re-run the real leg several times on seeds 1 and 2.
4. If the flake survives as a busy destroy, its trace will now say so; apply fix 3 with its unit test.

None of these needs a skip or a wider model.
