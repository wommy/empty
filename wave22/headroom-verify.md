# headroom guard (d845622 / d232d63) vs OpenZFS: verification

Source used: cached OpenZFS sources at scratchpad/wave16/src (dsl_dataset.c, dsl_dir.c, zfs_ioctl.c).
Code: /home/user/empty/internal/endpoint/headroom.go, headroom_test.go. `go test -run TestChildAvail` passes.

## Key source facts
- available(P) = dsl_dir_space_available(P.dir,NULL,0,TRUE) + max(0, ds_reserved - ds_unique_bytes), then
  min(.., refquota - referenced) if ds_quota != 0 (else 0 if referenced >= refquota). dsl_dataset.c:2654-2670 (dsl_get_available), 2912-2930.
- D(dd) = dsl_dir_space_available(dd): parentspace = D(dd->dd_parent) (MAX at root); root quota = min(dd_quota, dsl_pool_adjustedsize(NORMAL)) (slop removed);
  if dd_reserved > dd_used_bytes and not root: parentspace += dd_reserved - used; myspace = used > quota ? 0 : min(parentspace, quota - used). dsl_dir.c:1199-1259.
- Property mapping: used = dd_used_bytes (dsl_dataset.c:2559, dsl_dir.c:1003); quota = dd_quota, reservation = dd_reserved (dsl_dir.c:1015-1024);
  refreservation = ds_reserved, refquota = ds_quota (dsl_dataset.c:2585-2594); usedbyrefreservation = dd_used_breakdown[DD_USED_REFRSRV] (dsl_dir.c:1054-1057, 1117, only with DD_FLAG_USED_BREAKDOWN).
- Refreservation sync charges max(0, R - unique) to DD_USED_REFRSRV of P's own dir (dsl_dataset.c:4614-4619), so it is inside P's dd_used_bytes and excluded from D(P).
- A new zvol's refreservation is checked against dsl_dir_space_available(new child dir) (dsl_dataset.c:4583-4585) = D(P) (child dir: no quota/resv, used 0). So D(P) is exactly the number ZFS itself compares to.
- ds_quota (refquota) only appears in dataset-level paths (available, check_quota 4421-4434, clone swap); never in dsl_dir_space_available or a descendant's path.

## Claims
- (a) CONFIRMED. Unused refreservation is added to available(P) only (2659-2661) and already charged in P's dir (4619) so D(P) excludes it.
- (b) CONFIRMED. refquota caps available(P) only (2663-2670); dsl_dir_space_available never reads ds_quota.
- (c) CONFIRMED. dsl_dir.c:1229-1235: P's unused reservation is added to what P's children get (still capped by P's quota, 1253).
- (d) CONFIRMED (lower bound), but loose. Without refquota: avail - R = D + max(0,R-U) - R = D - min(R,U) <= D. With refquota avail only shrinks. Floor at 0 (headroom.go:189-195) right.
  Shortfall is min(unique, refreservation) -> FALSE REFUSALS, e.g. parent refreservation=100G holding 100G of its own data, D=50G:
  available=50G, guard reads 50-100 -> 0, refuses every create although a child gets 50G. ZFS exposes the exact add-back:
  `usedbyrefreservation` (dsl_dir.c:1117) = max(0,R-unique) exactly, so D = available - usedbyrefreservation whenever refquota is 0
  (or not binding). Comment headroom.go:161-162 says it was skipped only because the simulator lacks it. Caveat if adopted: on a dir without
  DD_FLAG_USED_BREAKDOWN zfs prints "-" and zfs.Prop.Uint (internal/zfs/zfs.go:39-42) reads "-" as 0 -> would OVERestimate; must fall back to the R bound then.
- (e) CONFIRMED as coded. headroom.go:199-207 uses P's own reservation, quota and used (s.resv, s.quota, s.used) with up = bound for the GRANDPARENT dir,
  which matches dsl_dir.c:1211-1253 (dd = P's dir; parentspace = D(P->dd_parent)). The commit text "reservation(up?)" ambiguity resolves correctly.
  Monotone in up, so a lower-bound `up` gives a lower-bound D; max(low, d) of two lower bounds is a lower bound. used > quota -> sub gives 0, matches 1245-1247.
  Root has no reservation add-back in ZFS (parentspace==MAX) and the code never applies one at the root (only applied for s with a level up). Consistent.

## Can it ALLOW what ZFS rejects?
No (space-wise). childAvail <= D(P) in every branch and ZFS's own test for the new child's refreservation is delta > D(P) (4583-4585), so
guard pass with floor>0 implies volsize < D(P). Residuals, all pre-existing/advisory: zvol real refreservation is volsize+metadata (only an issue if
floor < metadata overhead); write-time checks use !ondiskonly (towrite, dsl_dir.c:1220-1221) while available is ondiskonly; no pool lock (documented).
Slop: both available and ZFS's check use dsl_pool_adjustedsize(NORMAL) (1224-1226), so slop is consistently excluded.

## Can it REFUSE what ZFS accepts with room? (yes, three ways)
1. Parent refreservation covering its own data (above, claim d): short by min(unique, R). Fix exactly with usedbyrefreservation.
2. Parent = pool root with a refquota (allowed: cmd/zfs-endpoint/startup.go:91-105 accepts "tank"): headroom.go:196 returns avail - refres, and
   available(root) <= refquota - referenced. E.g. tank refquota=1G, referenced 0, pool 1T free, floor 10G -> guard sees <=1G, refuses every create;
   ZFS gives children ~1T. Test row "a refquota on the pool root" pins this behaviour (0.5G). D(root) = min(quota, adjustedsize) - used needs pool size,
   not readable from zfs get; at least the error message should say why, or it should be documented. Rare config but a hard false refusal.
3. Up-level read failure is fatal: headroom.go:232-234 returns the grandparent's error (zfsErr "inspect parent", or Internal on a parse error)
   instead of falling back to `low`. NotFound on the grandparent is not mapped through parentUnavailable (215 only for name==Parent).
   Low probability (zfs get on ancestors is normally readable, also in zoned/userns delegation), but falling back to low would be strictly safer.

## Arithmetic / units
-p gives bytes; all uint64. sub() floors at 0; d+extra saturates (200-204); min/max builtins. No overflow path. refreservation=auto: -p prints the
number; parents cannot be zvols (zvols have no children), so the zvol-parent case cannot arise (a zvol parent fails create anyway).

## TestChildAvail rows (headroom_test.go:207-225), by hand
1 10G/no resv -> 10G ok. 2 avail 10G, R 4G, U 0 -> D 6G ok. 3 D 5G, R 5G, U 2G -> avail 8G, bound 3G (true D 5G; pins the 2G false-refusal margin) ok.
4 avail 1G < R 4G -> 0 ok (true D in [0,1G]). 5 quota w/o refquota -> avail 4G ok. 6 refquota, up 10G -> 10G ok.
7 -> d = 10G + max(0, 0-4G) = 10G ok; NOTE inputs are inconsistent: avail 0.5G = 5G - referenced => referenced 4.5G > used 4G, impossible (used >= usedbydataset+usedbyrefres); result unaffected.
8 min(10G, 6G-2G) = 4G ok. 9 5G + (3G-1G) = 7G ok. 10 used 3G > quota 2G -> 0 ok (matches 1245-1247). 11 root: 1G - 0.5G = 0.5G ok (false-refusal case 2).
12 max(3G, 1G) = 3G ok (up is a lower bound, so may sit below low).
All 12 expected values correct.
