import json, collections
OUT="/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave12/"
# id, sources, kind, status, evidence, L, B, C, queued, batch, title, fix, test
F=[
# ---------------- anomalies ----------------
("AN-01",["wave12-model","README:110","adv4-reconcile"],"anomaly","open","internal/endpoint/reconcile_fuzz_test.go:221-226 checks third.Actions (applied) not the plan; perpetually failing groups hide in Skipped 'failed:'",4,4,1,None,"C",
 "FuzzReconcileSafe convergence oracle passes for the wrong reason","Assert len(plan)==0 AND no Skipped 'failed:' on a dry-run third reconcile","FuzzReconcileSafe (pure, simulator) with a seeded child-dataset/hold corpus entry"),
("AN-02",["wave12-model R9","wave8-adversary"],"anomaly","open","internal/endpoint/ownership_test.go:36-39,118-130,205-231 set democratic-csi: markers but setup(t) uses prefix zfs-endpoint (endpoint_test.go:55-58)",5,3,1,"b-model","C",
 "Vacuous ownership tests (markers under the wrong prefix)","Build the rows with the endpoint's own prefix (or setup with prefix democratic-csi) and assert each test fails when the guard is removed","Same unit tests + a mutation that deletes the owner check must turn them red"),
("AN-03",["wave11 A1"],"anomaly","open",".github/workflows/ci.yml:3-5 push only on main; default branch is the PR head and no schedule, so every cache is PR-scoped",5,2,1,"b-cache-warm","C",
 "CI caches never saved on a default-branch scope","Add push on the default branch + a weekly schedule that warms caches","actions cache list after one scheduled run shows a ref=default entry"),
("AN-04",["wave12"],"anomaly","open",".github/workflows/nightly.yml:105 runs -run '^Test(Scale|Explore)'; no such tests exist (grep), job green on zero tests",5,3,1,None,"C",
 "Nightly explore job runs zero tests","Fail the step when 'go test -json' reports 0 tests run (or add the tests)","CI step check: count of pass events > 0"),
("AN-05",["wave11 A3"],"anomaly","open","wave11 timings: VM test phase ~40% faster after a guest reboot; cause unverified",3,2,3,None,"C",
 "VM tests ~40% faster after reboot (unexplained)","Record per-phase timestamps for 5 cold vs warm runs before acting","ci/real-zfs-vm.sh phase log diff"),
("AN-06",["wave11 A4","wave11 A5"],"anomaly","partial","wave11 logs: 12 s silent gap after 'Image resized', qemu-img resize 1.8-5.1 s; S2 removed resize from warm path, cold path unexplained",2,2,2,"b-cache-warm","C",
 "12 s gap after image resize; resize time varies 3x","Timestamp each cold-boot step; drop resize on cold path too","real-zfs-vm.sh step timing"),
("AN-07",["wave7-suite"],"anomaly","open","TestPagination 1.03 s real vs 0.37 s modelled; unexplained",2,1,2,"speed-tests (a0f85a15)","C",
 "TestPagination 3x slower than modelled","Profile with -cpuprofile; check per-page whole-parent scan","go test -run TestPagination -count=5 -cpuprofile"),
("AN-08",["wave10-nfs","adv4-export"],"anomaly","open","exportfs 3.1.1 double-free crash observed in VM (upstream nfs-utils)",2,3,3,None,"C",
 "exportfs 3.1.1 double-free (upstream)","Pin/verify nfs-utils version in VM; treat exportfs SIGABRT as retryable error","real-zfs NFS leg asserting exportfs exit status"),
("AN-09",["rec/fsm-fs-zfs-endpoint.txt","rec/fsm-block-zfs-endpoint.txt"],"anomaly","open","rec FSM classifies a@volume-source-for-volume-b as IMPOSSIBLE(managed internal snapshot) at depth 2 (CV(a);CV(b<-a,reserve)); 2770 fs / 1533 block states",4,2,1,None,"C",
 "Recovery FSM labels the normal clone-source snapshot IMPOSSIBLE","Teach the classifier the stamped clone-source state (ownership: clone_source_for)","rec FSM rerun: zero IMPOSSIBLE states at depth<=2"),
("AN-10",["adv-mutation","wave11"],"anomaly","stale-claim","mutation kill rate 80.2% (partial run) vs 76.6% (final) -- explained by partial vs full sample",1,1,1,None,"C",
 "Mutation kill-rate discrepancy (explained)","none; cite final 76.6%","n/a"),
# ---------------- create path / CSI (batch A) ----------------
("T1",["wave9-csi","adv3"],"bug","open","internal/csi/controller.go:266-272 empty snapshot_id/volume_id in content_source passed through; restore becomes an empty volume",3,5,1,"b-csi-bugs","A",
 "Empty content source id silently creates an empty volume","Return InvalidArgument when the source oneof is set but its id is empty","controller_test: CreateVolume with Snapshot{SnapshotId:\"\"} -> InvalidArgument"),
("E6",["wave9-edges"],"bug","open","internal/endpoint/plan.go:176-186 fs clone with required<source sets refquota=want: clone smaller than its data",3,4,1,"b-csi-bugs","A",
 "Filesystem clone can be smaller than its source","Use max(want, sourceSize) (or OutOfRange) for filesystem clones","plan_test: planCreate fs clone required<source -> refquota>=source"),
("T3",["wave9-csi"],"bug","open","internal/csi/controller.go:134-136 capSupported confirms Mount on a zvol",3,3,1,"b-csi-bugs","A",
 "ValidateVolumeCapabilities confirms mount on a block volume","Reject mount access type for zvol-backed volumes","controller_test table: zvol + Mount -> not confirmed"),
("T2",["wave9-csi"],"bug","open","internal/csi/controller.go:346-369 ControllerExpandVolume ignores volume_capability",2,3,1,"b-csi-bugs","A",
 "Expand ignores volume_capability","Validate capability vs volume kind, set node_expansion_required for fs on block","controller_test: Expand with mismatched cap -> InvalidArgument"),
("N1",["wave10-core"],"bug","open","internal/endpoint/plan.go:198-211 resume of an uncommitted volume does not compare sparse vs thick (refreservation)",2,3,1,None,"A",
 "Uncommitted resume ignores thin/thick mismatch","Include refreservation/sparse in the resume compatibility check","plan_test: uncommitted sparse row + thick request -> AlreadyExists"),
("D07",["dod3 D07","VOL-06"],"edge","open","internal/endpoint/plan.go:390-405 compatible(): unlimited fs (refquota none) with want>0 -> AlreadyExists",2,3,1,None,"A",
 "Unlimited filesystem incompatible with any sized request","Treat refquota=none as compatible when required<=available or fix up refquota","plan_test compatible() table row"),
("E1",["wave9-edges"],"edge","open","internal/endpoint/endpoint.go:249-254 volumeID strict: legal CSI names -> InvalidArgument",3,3,2,"b-csi-bugs","A",
 "Legal CSI volume names rejected","Hash/encode names outside the zfs-safe alphabet (ADR 0002)","endpoint_test volumeID table with unicode/long names"),
("E5",["wave9-edges","ADR 0002"],"edge","open","internal/endpoint/plan.go:592 91-byte volume id limit; ADR 0002 proposed",2,3,3,"b-csi-bugs","A",
 "91-byte id limit","Adopt ADR 0002 hashed ids","plan_test name-length table"),
("E3",["wave9-edges"],"edge","open","internal/endpoint/plan.go:585 snapshot names refuse unicode.IsControl only",2,2,1,"b-csi-bugs","A",
 "Snapshot name charset check incomplete","Validate against the zfs component alphabet","plan_test snapshot-name table"),
("E4",["wave9-edges"],"edge","open","internal/endpoint/plan.go:588 reserved prefix collision",2,2,1,"b-csi-bugs","A",
 "Reserved snapshot prefix accepted from callers inconsistently","Refuse names starting with volume-source-for-volume- uniformly","plan_test"),
("E2",["wave9-edges"],"edge","open","internal/endpoint/endpoint.go:159-161 New accepts a parent longer than 126 bytes (every create then fails)",1,3,1,None,"A",
 "Over-long parent accepted at startup","Refuse at New when len(parent)+min child > 255","endpoint_test New() table"),
("ERR-12",["wave6-errors","adv4"],"bug","open","internal/endpoint/endpoint.go:314-315 zfsErr maps ErrParentNotFound to NotFound (should be FailedPrecondition/Internal)",2,3,1,"b-core0","A",
 "Missing parent reported as NotFound","Map ErrParentNotFound to FailedPrecondition","endpoint_test zfsErr table"),
("SEAM-DEL-LOCK",["wave12","README:35"],"seam","open","internal/endpoint/endpoint.go:489 DeleteVolume locks only vol:id but releases origin snapshot on src (releasedOrigin)",2,4,1,None,"A",
 "DeleteVolume releases the source's snapshot without the src lock","Also TryAcquire vol:<src> when releasedOrigin is non-empty","endpoint_test: concurrent CreateVolume(clone of src) vs DeleteVolume -> no destroyed in-use origin"),
("PRF-02",["dod3","wave7-suite"],"perf","open","internal/endpoint/endpoint.go:495 DeleteVolume and CreateSnapshot scan the whole parent per call",3,2,2,None,"A",
 "Per-call whole-parent scan on delete/snapshot","Scan only the volume subtree (zfs get -r <parent>/<id>) + origin row","benchmark BenchmarkDeleteVolume rows=10k"),
("DEL-08",["dod3 D06","wave5"],"edge","open","internal/endpoint/plan.go:459-461 absent row returns OK with no rename detection",2,4,2,None,"A",
 "Delete of a renamed volume reports success and leaks it","Look up csi_volume_name==id across the parent before returning OK","plan_test: renamed dataset with matching stamp -> not OK"),
("A3",["adv4-reconcile","wave12"],"seam","open","internal/endpoint/plan.go:300 clone-source OpSnapshot OkExists:true: pre-existing unstamped snapshot adopted",1,4,2,None,"A",
 "Clone-source snapshot adopted on EEXIST","After OkExists, re-read and require clone_source_for stamp","plan/apply test with pre-created snapshot"),
("REL-LEGACY",["rec msg7","core"],"seam","partial","internal/endpoint/plan.go:508-533 releasedOrigin still releases a legacy unstamped origin (documented choice)",1,3,2,None,"A",
 "Legacy unstamped origin released","Keep; document in ADR or gate behind config","plan_test releasedOrigin legacy row"),
("LST-04",["adv3#6"],"gap","open","internal/csi/controller.go:331-337 GetCapacity ignores volume_capabilities",2,2,1,None,"A",
 "GetCapacity ignores capabilities","Return 0 for unsupported caps","controller_test GetCapacity"),
("CSI-06",["adv3#7"],"bug","open","internal/csi/controller.go:309-316 ValidateVolumeCapabilities echoes unchecked context/parameters",2,2,1,None,"A",
 "Validate echoes unverified context","Compare context/params with the volume's stored values","controller_test Validate mismatched params"),
("ADV3-12",["adv3#12"],"gap","open","internal/csi/controller.go:150-172 StorageClass parameters not forwarded",2,2,2,None,"A",
 "Parameters not forwarded","Forward allowlisted params or reject unknown","controller_test CreateVolume params"),
("ADV3-13",["adv3#13"],"gap","open","internal/csi: no topology",1,2,3,None,"A",
 "No topology","Advertise a single-node segment","controller_test GetPluginCapabilities"),
# ---------------- parse boundary / reconcile / zfs (batch B) ----------------
("SCN-02",["wave6-json-scan","D17"],"security","open","internal/zfs/zfs.go:444-488 parseColumns rejects duplicates but a forged row zfs never printed (name with tab/newline via user prop) is still possible",2,5,2,"b-core0","B",
 "Phantom rows at the scan boundary","Move scan to zfs get -j (JSON) or validate row name prefix/type per row","zfs parser fuzz: FuzzParseColumns, row count == names requested"),
("SCN-06",["wave6","F7"],"bug","open","internal/endpoint/table.go decodeColumns: no type/name agreement check (snapshot type without '@', etc.)",2,4,1,"b-core0","B",
 "No type/name agreement check","Reject rows whose type disagrees with '@' presence","table_test decodeColumns table"),
("OWN-12",["SCN-09","wave12-model"],"bug","open","internal/endpoint/table.go:358-364 drops rows deeper than a volume; reconcile.go: a child dataset makes a perpetually failing group",3,3,2,"b-model","B",
 "Child dataset under a volume wedges reconcile forever","Keep depth+1 rows as 'foreign child' and skip with reason (not failure)","reconcile_test: volume with child dataset -> Skipped 'foreign-child', plan stable"),
("HOLD-REPLAN",["wave12-model"],"bug","open","internal/endpoint/reconcile.go: userrefs not read; held snapshot fails destroy every pass",3,3,2,"b-model","B",
 "Held snapshots replanned every pass","Scan userrefs; plan skip 'held'","reconcile_test with userrefs>0"),
("ERR-13",["wave6-errors"],"gap","open","internal/zfs/errors.go: new patterns added without a corpus ratchet",3,3,2,"b-core0","B",
 "No error-corpus ratchet","Golden file of real zfs stderr -> classified error; test fails on unclassified","zfs errors_test over testdata/stderr/*.txt"),
("PROP-UTF",["adv4-zfs-json"],"seam","open","internal/zfs/zfs.go:81-91 checkProps refuses only \\n\\t\\x00; JSON output path drops chars > U+FFFF",1,3,1,"b-core0","B",
 "Property values outside BMP not round-trippable","Refuse non-BMP/invalid UTF-8 in checkProps","zfs_test checkProps table"),
("REC-F10",["adv4-reconcile F10"],"edge","open","internal/endpoint/reconcile.go MinAge compares wall clock with committed_at (clock source)",2,3,1,None,"B",
 "Reconcile age uses wall clock","Inject clock; refuse when committed_at is in the future","reconcile_test with skewed clock"),
("RT-04",["dod3","wave6-lifecycle"],"security","partial","internal/zfs audit log per command, no principal",2,2,2,None,"B",
 "Audit has no principal","Thread peer creds (SO_PEERCRED) into the audit record","runner_test audit record fields"),
("NFS-D1",["wave10 nfs-diff D1"],"bug","open","internal/endpoint/export.go: no crossmnt ancestor check; a crossmnt export on an ancestor exports the child",2,5,2,"nfs-diff builder a9d51a288 (in flight)","B",
 "crossmnt ancestor silently exports a volume","Walk ancestors in etab; refuse/flag crossmnt ancestor","export_test ParseEtab + serves() with crossmnt parent"),
("NFS-D2",["wave10 nfs-diff D2"],"bug","open","internal/endpoint/export.go serves() ignores client precedence and options (ro vs rw)",2,4,2,"nfs-diff builder a9d51a288 (in flight)","B",
 "serves() ignores precedence/options","Evaluate first-match precedence and rw/root_squash","export_test serves() table"),
("NFS-D3",["wave10 nfs-diff D3"],"bug","open","internal/endpoint/export.go Unpublish does not verify the client is no longer covered",2,4,1,"nfs-diff builder a9d51a288 (in flight)","B",
 "Unpublish does not verify coverage removed","Re-read etab and fail if still served","export_test Unpublish with wildcard"),
("NFS-01",["wave6-csi-nfs-design","ADR 0004"],"gap","open","internal/endpoint/export.go still uses nfs_clients user property, conflicting with ADR 0004 decision",3,3,3,"b-nfs-csi","B",
 "nfs_clients property vs ADR 0004","Execute b-nfs-csi (NFS-01/14/15/17-22, N1-N16)","export_test + real-zfs NFS leg"),
# ---------------- CI / tests / docs (batch C) ----------------
("CI-SHA",["wave11"],"security","partial",".github/workflows/ci.yml:159 fetches SHA256SUMS unverified to build the cache key; script refetches with gpg (TOCTOU)",1,3,1,"b-cache-warm","C",
 "Cache key from unverified SHA256SUMS","Key from the gpg-verified file produced by the script","ci dry run: key step consumes verified file"),
("CI-NIGHTLY-CACHE",["wave11"],"ops","open",".github/workflows/nightly.yml: caches restored never saved",4,1,1,None,"C",
 "Nightly caches never saved","Add actions/cache/save","nightly run shows cache saved"),
("CI-GORACE",["wave11"],"ops","open","no GORACE=atexit_sleep_ms=0 in CI",2,2,1,"b-cache-warm","C",
 "GORACE atexit sleep unset","Set GORACE env","CI timing"),
("CI-VULN",["dod3","wave6-ci-gate"],"security","open","no go mod verify / govulncheck in ci.yml",2,3,1,None,"C",
 "No go mod verify / govulncheck","Add both to static job","static job step"),
("TG-PAR",["wave7-suite","speed"],"perf","open","t.Parallel used once across internal/",4,2,2,"speed-tests (a0f85a15)","C",
 "Unit suite serial","Parallelise pure tests","go test -count=1 wall time < budget (ADR 0005 60 s)"),
("TG-SCALE",["dod3"],"gap","open","no TestScale*/TestExplore* tests (see AN-04)",3,2,2,"speed-tests (a0f85a15)","C",
 "No scale tests","Add TestScale 10k rows simulate","nightly"),
("TG-CRASH",["wave7","wave12-model"],"gap","open","no crash-injection table (kill between Op i and i+1)",3,4,3,None,"C",
 "No crash-injection table","Table over every Op boundary in apply with simulator kill","endpoint crash_test (simulator)"),
("TG-GOLDEN",["wave7"],"gap","open","no golden plans",2,3,2,None,"C",
 "No golden plans","testdata/plans/*.golden with -update","plan golden_test"),
("TG-ROUNDTRIP",["adv4-zfs-json"],"gap","open","no property round-trip test (set -> get -Hp -> decode)",2,3,2,"b-core0","C",
 "No props round-trip","Fuzz value through checkProps/parse","zfs FuzzPropRoundTrip"),
("TG-REALFACTS",["wave9 zfs-diff (in flight a5b209d5)"],"gap","open","no recorded real-zfs facts fixture for simulator",2,3,3,None,"C",
 "Simulator not anchored to recorded zfs facts","Record facts in VM; diff simulator in unit tests","realfacts_test"),
("TG-LEAK",["wave7"],"gap","open","no goroutine leakcheck",2,2,1,None,"C",
 "No goroutine leak check","TestMain with goleak-style check (stdlib)","TestMain"),
("TG-LAT",["wave7"],"gap","open","no latency recorder for ADR 0005 budget",2,1,2,"speed-tests (a0f85a15)","C",
 "No per-test latency recorder","go test -json -> slowest-N report","CI step"),
("DOC-R30",["README"],"doc","stale-claim","README.md:30 'one zfs get -Hp per call reads the subtree' -- deeper rows dropped (table.go:358-364), several scans per call",4,2,1,"b-readme","C","README scan claim","Rewrite","doc review"),
("DOC-R35A",["README"],"doc","stale-claim","README.md:35 'every resource locked incl. source snapshot' -- DeleteVolume locks only vol:id (endpoint.go:489)",4,2,1,"b-readme","C","README locking claim","Rewrite (or fix SEAM-DEL-LOCK)","doc review"),
("DOC-R35B",["README"],"doc","stale-claim","README.md:35 'ABORTED 1 s before deadline' -- only when the ctx has a deadline",4,1,1,"b-readme","C","README deadline claim","Qualify","doc review"),
("DOC-R47",["README"],"doc","stale-claim","README.md:47 '495 operations' -- 495 mappings over 108 ops",4,1,1,"b-readme","C","README op count","Correct numbers","doc review"),
("DOC-R81",["README","dod3"],"doc","stale-claim","README.md:81 '12 us per 10k rows' -- ~230 ms measured (dod3)",4,2,1,"b-readme","C","README perf number","Replace with measured figure + benchmark name","BenchmarkScan"),
("DOC-R106",["README"],"doc","stale-claim","README.md:106 'file-backed pool' -- VM uses tmpfs shards",4,1,1,"b-readme","C","README VM pool","Rewrite","doc review"),
("DOC-R110",["README","AN-01"],"doc","stale-claim","README.md:110 FuzzReconcileSafe 'converges and then plans nothing' -- oracle checks applied actions only",4,3,1,"b-readme","C","README convergence claim","Fix AN-01 then reword","FuzzReconcileSafe"),
("DOC-R15",["README","ADR 0004"],"doc","stale-claim","README.md:15 nfs_clients contradicts ADR 0004 decision",4,2,1,"b-readme","C","README nfs_clients","Align with ADR 0004","doc review"),
("DOC-PROTO239",["wave12"],"doc","stale-claim","api/zfsendpoint/v1/endpoint.proto:239-240 'server:export_path ... CSI publish_context' stale vs NFS-22",3,2,1,None,"C","Proto export_path comment","Rewrite + regenerate gen/","buf lint"),
("DOC-PROTO98",["wave12"],"doc","stale-claim","api/zfsendpoint/v1/endpoint.proto:98 Snapshot.id '<volume id>@<name>' stale for hashed ids",3,2,1,None,"C","Proto Snapshot.id comment","Rewrite","buf lint"),
("SC-W8-OWN13",["wave8-adversary"],"doc","stale-claim","wave8 reported OWN-13 open; table.go:78-80,105-110 require the stamp at HEAD",1,1,1,None,"C","wave8 OWN-13 claim stale","none","n/a"),
("SC-W8-D1",["wave8-adversary"],"doc","stale-claim","wave8 D1 claim no longer matches HEAD",1,1,1,None,"C","wave8 D1 claim stale","none","n/a"),
("SC-W7-QEMU",["wave7-suite"],"doc","stale-claim","wave7 'qemu 211 s' superseded by cached guest (ci/real-zfs-vm.sh:120-172)",1,1,1,None,"C","wave7 qemu 211 s stale","none","n/a"),
("SC-W7-LOCK",["wave7-suite"],"doc","stale-claim","wave7 'lock has no tests' -- lock tests exist at HEAD",1,1,1,None,"C","wave7 lock-tests claim stale","none","n/a"),
]
FIXED=[
("OWN-13",["core","wave8-adversary"],"bug","internal/endpoint/table.go:78-80,105-110 stamp required"),
("F2",["adv4-zfs-json"],"bug","internal/endpoint/table.go:261-266,311 strict numerics + zvol invariant"),
("VOL-19",["dod3"],"edge","internal/endpoint/plan.go:121 MaxCapacityBytes"),
("F15",["adv4"],"edge","internal/endpoint/plan.go:101-112 roundTo bs==0 guard"),
("DEL-06/CX1",["wave5","wave7"],"bug","internal/endpoint/plan.go:431-447 ordered delete, never -r"),
("ADV3-1/F8",["adv3#1"],"bug","internal/endpoint/plan.go:248 clone source requires ServedVolume"),
("SCN-08",["wave6-json-scan"],"bug","internal/endpoint/endpoint.go:361-366 local '-' is a value"),
("VOL-09",["dod3"],"bug","internal/endpoint/endpoint.go:748 Publish refuses uncommitted"),
("REC-04/06/12",["adv4-reconcile F1-F9"],"bug","internal/endpoint/reconcile.go stated set, max_deletions, digest, per-group apply"),
("EXP-F1..F8",["adv4-export"],"bug","internal/endpoint/export.go IPv4 /N, overlap, 8191, set -u, received sharenfs, etab modes, realpath"),
("W11-A7",["wave11"],"bug","internal/zfs/runner.go:273,283-285 process-group kill (a02c5b0)"),
("RT-02",["wave6-lifecycle"],"security","cmd: TCP refused"),
("RT-03",["wave6-lifecycle"],"ops","fixed at HEAD"),
("PRP-07",["wave6"],"ops","2.4 startup check (ADR 0004)"),
("PRF-03",["dod3"],"perf","one-pass simulate"),
("ADV3-2/3/4/5/8/9/10/11",["adv3"],"bug","cmd/zfs-csi/main.go unixsock.Listen, backoff, second-signal stop; controller fixes"),
("DOD3-D02/04/05/09/10/12/13/14",["dod3"],"bug","verified at HEAD"),
("MCN-1/5",["mcnamara"],"gap","sanity anchor + NFS leg present"),
("CI-VM-OK",["wave11"],"ops","ci/real-zfs-vm.sh:166-172 ok only after verified boot"),
]
rows=[]
for f in F:
    i,src,k,st,ev,L,B,C,q,b,t,fix,test=f
    sc=round(L*B/C*(2 if k=="anomaly" and st!="stale-claim" else 1),2)
    rows.append(dict(id=i,title=t,sources=src,kind=k,status_at_head=st,evidence=ev,likelihood=L,blast=B,cost=C,score=sc,queued_as=q,batch=b,fix=fix,test=test))
for i,src,k,ev in FIXED:
    rows.append(dict(id=i,title=i+" (fixed)",sources=src,kind=k,status_at_head="fixed",evidence=ev,likelihood=0,blast=0,cost=1,score=0,queued_as=None,batch=None,fix="",test="existing"))
with open(OUT+"ledger.jsonl","w") as fh:
    for r in rows: fh.write(json.dumps(r)+"\n")
# ---- md
openish=[r for r in rows if r["status_at_head"] in("open","partial")]
ranked=sorted(openish,key=lambda r:-r["score"])
n=max(1,round(len(ranked)*0.2)); top=ranked[:n]
tot=sum(r["score"] for r in ranked); topsum=sum(r["score"] for r in top)
L=[]
L.append("# Wave 12 findings ledger (HEAD a02c5b0)\n")
L.append("Score = likelihood x blast / cost (1-5 each); anomalies weighted x2. Source: ledger.jsonl (same dir). Read-only; verified against code at HEAD.\n")
L.append("## Counts\n")
ks=collections.Counter(r["kind"] for r in rows); ss=collections.Counter(r["status_at_head"] for r in rows)
L.append(f"Total {len(rows)} findings ({len(F)} open/partial/stale + {len(FIXED)} fixed groups).\n")
L.append("| kind | n |\n|---|---|"); [L.append(f"| {k} | {v} |") for k,v in sorted(ks.items())]
L.append("\n| status | n |\n|---|---|"); [L.append(f"| {k} | {v} |") for k,v in sorted(ss.items())]
L.append("\n## 1. Anomalies (ranked first)\n")
L.append("| id | score | status | queued | finding | evidence |\n|---|---|---|---|---|---|")
for r in sorted([r for r in rows if r["kind"]=="anomaly"],key=lambda r:-r["score"]):
    L.append(f"| {r['id']} | {r['score']} | {r['status_at_head']} | {r['queued_as'] or '**none**'} | {r['title']} | {r['evidence']} |")
L.append(f"\n## 2. Pareto top 20% ({n} of {len(ranked)} open/partial; {topsum:.0f}/{tot:.0f} = {100*topsum/tot:.0f}% of total score)\n")
L.append("| # | id | score | fix (one line) | lowest-level test (ADR 0005) | queued |\n|---|---|---|---|---|---|")
for j,r in enumerate(top,1):
    L.append(f"| {j} | {r['id']} {r['title']} | {r['score']} | {r['fix']} | {r['test']} | {r['queued_as'] or '**none**'} |")
L.append("\nThe remaining 80% of rows hold the other share of score; see ledger.jsonl sorted by score.\n")
L.append("## 3. Not covered by any queued item\n")
L.append("| id | score | batch | finding |\n|---|---|---|---|")
for r in ranked:
    if not r["queued_as"]: L.append(f"| {r['id']} | {r['score']} | {r['batch']} | {r['title']} |")
L.append("\nNFS-D1..D3 are claimed only by the running builder a9d51a288, not by an orch queue item; if that builder lands nothing, they are uncovered.\n")
L.append("## 4. Writer batches (disjoint files; every non-fixed finding mapped)\n")
files={"A":"internal/csi/controller.go(+_test), internal/endpoint/plan.go(+plan_test.go), internal/endpoint/endpoint.go (zfsErr, volumeID, New, DeleteVolume/CreateSnapshot)",
"B":"internal/endpoint/table.go, internal/endpoint/reconcile.go(+reconcile_test.go), internal/endpoint/export.go(+export_test.go; wait for a9d51a288), internal/zfs/zfs.go, internal/zfs/errors.go, internal/zfs/runner.go(audit)",
"C":"internal/endpoint/reconcile_fuzz_test.go, internal/endpoint/ownership_test.go, NEW test files only (crash_test.go, golden_test.go, realfacts_test.go, internal/zfs/roundtrip_test.go), .github/workflows/ci.yml, .github/workflows/nightly.yml, ci/real-zfs-vm.sh, README.md, api/zfsendpoint/v1/endpoint.proto + gen/, rec/ FSM classifier (scratchpad)"}
for b in "ABC":
    ids=[r["id"] for r in sorted([r for r in rows if r["batch"]==b],key=lambda r:-r["score"])]
    L.append(f"**Batch {b}** -- files: {files[b]}\n\nFindings ({len(ids)}): {', '.join(ids)}\n")
L.append("Ordering: C first (AN-01/AN-02/AN-04 make later fixes provable), then A and B in parallel. B touches export.go only after a9d51a288 lands. Fixed rows need no batch.\n")
L.append("## 5. Stale-claim rate per source report (feeds lanes/README.md reader scoring)\n")
L.append("stale = the report's claim did not hold at HEAD when checked (not 'fixed since'). Fixed-since counts are credit, not penalty.\n")
L.append("| source | claims checked | stale | fixed since | still open | stale rate |\n|---|---|---|---|---|---|")
SR=[("README.md",10,8,0,2),("wave8-adversary",9,2,4,3),("wave7-suite",8,2,2,4),("wave11 (CI/VM)",10,1,3,6),("wave9 (csi+edges)",11,0,0,11),("wave10 (core/nfs-diff)",5,0,0,5),
("adv3",13,0,8,5),("adv4 (reconcile/export/zfs-json)",21,0,18,3),("dod3",14,0,10,4),("wave5/wave6",10,0,6,4),("wave12-model",5,0,0,5),("rec (FSM)",2,1,1,0),("align",3,0,2,1),("docs/research/gaps/*",6,0,2,4),("api proto comments",2,2,0,0)]
for s,c,st,fx,op in SR: L.append(f"| {s} | {c} | {st} | {fx} | {op} | {100*st/c:.0f}% |")
L.append("\nNotes: README 8 of 10 checked claims are stale (worst). rec counts AN-09 (classifier calls a legal state IMPOSSIBLE) as its stale claim. adv3/adv4/dod3 are high-precision: their items were true when written and are mostly fixed now. AN-10 (80.2% vs 76.6%) is explained, not charged to any reader.\n")
L.append("## 6. Not yet verifiable (in flight)\n\n- aa10b675 mutation-kill tests, a0f85a15 speed-tests, a9d51a288 nfs-diff fixes, a5b209d5 wave9/zfs-diff.md: not in the repo at HEAD; rescore TG-*, AN-07, NFS-D1..D3 when they land.\n")
open(OUT+"ledger.md","w").write("\n".join(L))
print(len(rows),n,round(100*topsum/tot),[r["id"] for r in top])
