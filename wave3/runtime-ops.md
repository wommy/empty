# Runtime/ops seams (haiku scout; source-read only, unverified claims marked)

| seam | evidence | our behavior | sev | fix |
|---|---|---|---|---|
| SIGKILL mid-create | libzfs_dataset.c:3733 lzc_create(path,ost,props) one ioctl; dmu_objset.c:1359 one sync task | all -o in one argv (zfs.go:187-223); commit marker + resume (plan.go:127,139-148) | low | fault-inject kill per op boundary; doc cancel-but-exists |
| hung zfs child (suspended pool, D state) | exec Wait blocks until exit; txg waits non-signal (txg.c:416, dmu_tx.c:1384) | runner.go:39-61 no WaitDelay/Setpgid; vol keys held (endpoint.go:278-282); drain → srv.Stop doesn't stop handlers | HIGH | cmd.WaitDelay ~5s, cmd.Cancel; health Unavailable on suspended pool; doc lock pinned |
| full-parent scan per op, fully buffered | DeleteVolume/CreateSnapshot scan("") endpoint.go:345,~444; List :420,:469; 27 props | ESTIMATE ~25MB/scan at 10k vols; mass delete quadratic | HIGH | narrow Delete scan; cache Table w/ invalidation; stream parse; benchmark 10k |
| unbounded concurrent execs | no limiter anywhere; grpc.NewServer() no opts | N deletes = N full scans | HIGH | semaphore in runner (4-8); singleflight scans |
| DeleteVolume destroys foreign snapshots | plan.go:324-327 blocks only CSI snaps; plan.go:334 destroy -r; democratic-csi same (controller-zfs ~1460) | sanoid/syncoid snaps silently destroyed; holds → EBUSY (text UNVERIFIED) | HIGH (data loss) | refuse on any non-CSI snapshot unless opt-in; check holds. Deviation from baseline → owner decision |
| zfs recv into parent | zfs.go:43 Local() counts source=received as local | received managed datasets adopted as Managed+Committed; send -R carrying user props UNVERIFIED | MED | ignore received for ownership or stamp pool/host identity |
| admin rename | plan.go:317-320 missing row → nil plan → success | PV deleted, renamed data orphaned | MED | lookup by csi_volume_name too; reconcile flags |
| OkExists on clone/snapshot accepts foreign same-name | plan.go:193,222-223; apply.go:34; readVolume no Managed check (endpoint.go:320-330) | narrow race w/ writer outside lock → stamps provision_success on foreign dataset | MED-LOW | require Managed after OkExists; readVolume checks Managed |
| crash between create and commit, no GC | uncommitted filtered from List | leaks unless retried | MED | reconcile (builder in flight) |
| observability | no health/metrics/request ids; 7 slog lines | blind ops | HIGH(ops) | grpc health per-parent probe; prom counters by method/code; per-RPC slog w/ request id; log destructive plans |
| unauthenticated TCP listen | main.go:102-106; zfs-csi insecure creds | default unix 0600 | HIGH if TCP | refuse TCP w/o mTLS (ADR in flight) |
| gRPC 4MB default, no keepalive | grpc defaults | ESTIMATE ~18k vols overflow ListVolumes(max_entries=0) | LOW-MED | server-side page cap; explicit MaxCallRecvMsgSize |
| flock per host doesn't span containers | main.go:40,130-147 /run/zfs-endpoint | separate /run → no exclusion | MED if containerized | host-path lock; doc |
| no unit/manifest/privilege docs | — | implicit root, /dev/zfs, mount(2) | MED | systemd unit + capability docs |
| zfs from PATH | zfs.go:103-108 | distro PATH | LOW | -zfs-bin flag |
| destroy -r not atomic | zfs_main.c:1907,1920; lzc_destroy per dataset | partial; retry idempotent (UNVERIFIED) | LOW-MED | test kill after snapshot batch |
| zvol DevicePath may not exist yet | endpoint.go:234 constant; udev async (UNVERIFIED) | path may be missing briefly | LOW-MED | wait or resolve at publish |
| shutdown leaves children | no Pdeathsig | in-flight zfs continues | LOW | doc or Pdeathsig |
