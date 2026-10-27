import os
os.chdir('/home/user/empty/.claude/worktrees/agent-ad45c3ab7d0b822f7')
def sub(p, pairs):
    s=open(p).read()
    for old,new in pairs:
        assert s.count(old)==1, (p, old)
        s=s.replace(old,new)
    open(p,'w').write(s)

sub('README.md', [
('''| DeleteVolume refuses snapshots the endpoint did not make, unless `-destroy-foreign-snapshots` | ADR 0003: `zfs destroy -r` took sanoid, replication and administrators' snapshots. The baseline did this |''',
'''| DeleteVolume refuses snapshots the endpoint did not make, unless `-destroy-foreign-snapshots` | ADR 0003: `zfs destroy -r` took sanoid, replication and administrators' snapshots. The baseline did this |
| Never `zfs destroy -r`: release the clone-source origin (`-d`) first, destroy each judged snapshot by name, then the volume plainly | `-r` took child datasets the depth-1 scan cannot see and snapshots cut after the scan. Without it zfs itself refuses ("has children"). Releasing the origin first means no crash point orphans it (wave-5 lifecycle FSM) |
| Clone-source snapshots are stamped at creation (`managed_resource`, `clone_source_for=<clone>`) | the reserved name alone is not ownership: anyone can `zfs snapshot` it. Unstamped ones (democratic-csi cuts them bare) are released only with the clone whose origin names them exactly, under the parent, from an owned volume; otherwise they are reported, never collected |
| Only owned, committed volumes are served (`Row.ServedVolume`) | an uncommitted row is a provision in flight or abandoned; Get, Expand, export and snapshot or clone sources acting on it raced Reconcile |
| A volume needs both `managed_resource` and `csi_volume_name` = its id | both writers set the two together; managed without a name is a hand edit or an inherit |'''),
('''`FuzzReconcileSafe` builds arbitrary parents on the simulator, including received copies of owned volumes. It asserts that reconcile never targets a desired, unowned, snapshotted or young volume or a CSI snapshot, and that a second reconcile plans nothing.''',
'''`FuzzReconcileSafe` builds arbitrary parents on the simulator: received copies, foreign snapshots, child datasets, clones and origins outside the parent, deferred snapshots, unstamped clone sources, just-committed retries, and a busy destroy on the first run. It asserts that reconcile never errors, never targets anything the endpoint does not own (or a desired, young or CSI-snapshotted volume), that everything unowned and everything outside the scan survives, and that once failures clear it converges and then plans nothing.'''),
('''- **Reconcile/GC.** The on-demand half ships: the `Reconcile` RPC (`reconcile.go`) runs off the request path and collects uncommitted rows from abandoned creates, clone-source snapshots whose clone is gone, and owned volumes outside a desired set the caller supplies. It never touches unowned or received datasets, desired volumes, CSI snapshots, volumes with snapshots (CSI or foreign) or clones (reported as skipped), or anything younger than `min_age_seconds` (default one hour). An empty desired set collects no committed volume. `dry_run` returns the plan as data. Still open: a startup report-only pass, keying clone-source snapshots on a property rather than their reserved name prefix, and reporting unrecorded exports once exports exist.''',
'''- **Reconcile/GC.** The on-demand half ships: the `Reconcile` RPC (`reconcile.go`) runs off the request path and collects uncommitted rows from abandoned creates, stamped clone-source snapshots whose clone is gone, and owned volumes outside a desired set the caller supplies. Its guards:
  - It never touches unowned or received datasets, desired volumes, CSI snapshots, volumes with CSI or foreign snapshots or clones (reported as skipped), or anything younger than `min_age_seconds`: default one hour, at least ten minutes (twice external-provisioner's retry backoff cap). A committed volume is aged from its `committed_at` stamp when that is later than its creation.
  - A desired set that is empty, or names none of the endpoint's volumes, collects no committed volume. `max_deletions` (default 10, `unlimited_deletions` to lift) caps undesired volumes per run: above it nothing is applied and the call is `FAILED_PRECONDITION`.
  - `dry_run` returns the plan and a `plan_digest`; a run that echoes the digest applies only if it plans exactly that, else `ABORTED`.
  - It locks per volume without waiting: busy volumes are skipped. Under the locks it re-plans and keeps only actions the unlocked plan made for the same reason, so a row a racing CreateVolume committed is left alone.
  - It applies per group (a volume with its snapshots and released origin, or one orphan). A failing group is reported as skipped with `failed: <error>`, what ran is returned, and the run goes on; a source whose clone failed is not attempted. Destroys never use `-r`, so zfs refuses on child datasets and clones the scan cannot see.

  Still open: a startup report-only pass; reporting unrecorded exports; reading the snapshots' `clones` property so clones outside the parent are refused in the plan rather than by zfs at apply; and taking `Now` from the zfs host when the runner is remote (adv4 F10).'''),
])
