# docs-truth: adversarial review of ADRs 0001-0004, README, CONTEXT.md

Pinned to HEAD 0c884df (an exported copy is in `.../scratchpad/wave6/head/`). The live working tree is not at HEAD: someone is editing it right now. It has staged changes to README, ADR 0002/0003, main.go, endpoint.go, plan.go and others, plus new `foreign_test.go` and `ownership_test.go`. Every line number below is from 0c884df.

Evidence order used (ADR 0003): OpenZFS source and man pages first. That means the clone at `scratchpad/repos/openzfs` (tags zfs-2.2.9 and zfs-2.4.0 only; `lib/libshare` is sparse). Then behaviour, and no real pool exists in this sandbox. I ran the simulator, the Go module cache copies of grpc-go v1.84.0 and csi-test v5.6.0, and a few throwaway Go programs. Then other implementations.

`repos/grpc-go` and `repos/csi-test` are not in the `repos` directory. The module cache copies were used instead.

## 0. Top findings

1. **ADR 0001 never mentions `Reconcile`, `PublishFileExport` or `UnpublishFileExport`.**
   - The ADR says "10 RPCs"; HEAD has 13 (`endpoint.proto:19-51`).
   - `Reconcile` is a global, destructive RPC. It collects every owned volume outside the caller's `desired_volume_ids` that is older than `min_age` (`reconcile.go:129-146`, 1 h default).
   - Under `volume_prefix` scoping, tenant A's desired set would delete tenant B's volumes. The ADR's "rows out of scope are filtered" rule says nothing about it.
   - `PublishFileExport.client` is caller-chosen and unbounded. `ParseClient` accepts `0.0.0.0/0` (confirmed by running it). The comment at `export.go:53-57` says "wildcards refused". So a per-RPC allow is also a "publish to the world" allow.
2. **An abstract unix socket bypasses the permission gate the ADR calls "the first gate".**
   - `listener()` strips `unix://` and passes the rest to `net.Listen("unix", ...)`. `unix://@foo` creates an abstract socket today. I confirmed `net.Listen("unix","@x")` succeeds.
   - An abstract socket has no inode, no umask and no directory mode. It is reachable by every process in the network namespace, so every hostNetwork pod can connect.
   - Relative paths also pass the check.
   - The ADR must require an absolute path.
3. **The `0660` + `-socket-gid` design is unreachable as written.** `listener()` makes the parent directory `0700` (`main.go:114`). A group member cannot traverse it. The ADR specifies the socket mode and says nothing about the directory.
4. **uid is self-asserted on Kubernetes, so `volume_prefix` per uid is not tenancy.**
   - Any pod that may mount the hostPath socket directory chooses its own uid with `runAsUser`.
   - The real control is the admission policy on hostPath and `runAsUser`, not `SO_PEERCRED`.
   - The ADR itself concedes that uid 0 is shared. It does not follow that through to per-uid tenants.
   - The minimal first version is socket permissions plus mTLS plus a per-RPC allowlist, with tenancy as one endpoint per parent.
5. **The TCP listener is still an unauthenticated hole at HEAD** (`main.go:111-112`: plain `net.Listen("tcp")`). README:14 and README:18 say "owner-only permissions" and "socket is mode 0600" and never mention TCP.
6. **grpc-go exposes no peer credentials.**
   - Verified in v1.84.0: `insecure.ServerHandshake` returns `info{NoSecurity}`, and `credentials/local` returns a security level only (`local.go:67-99`, `insecure.go:47-48`).
   - A custom `TransportCredentials` is required. Its `ServerHandshake` receives the raw `*net.UnixConn`.
   - The standard library is enough: `syscall.GetsockoptUcred` inside `SyscallConn().Control` returned `{pid uid gid}` in my test. No new dependency.
   - One `grpc.Server` takes one `Creds`, so the ADR's "one mechanism per listener kind" needs a dispatching credential, or two servers. The ADR does not say which.
7. **ADR 0003 is "accepted" yet violated at HEAD, with data loss.**
   - `PlanDeleteVolume` refuses only CSI-owned snapshots (`plan.go:324-327`), then runs `destroy -r` (`plan.go:334`). That destroys foreign snapshots, and also any child datasets, which `scan` hides (`table.go:234-235`).
   - `Prop.Local()` counts `received` as local (`zfs.go:43`), so a replicated dataset that arrived with `send -p` carries `managed_resource=true`. Reconcile can then destroy it.
   - The default `-property-prefix` is still `democratic-csi` (`main.go:37`).
   - The ADR lists all three as "reopened, each as its own change". There is no issue for any of them: the tracker holds only PR #1.
8. **ADR 0004 contradicts the shipped NFS design, and the contradiction is inside the ADR as well.**
   - It says state lives "in `sharenfs` and nowhere else; no client list". The code stores `<prefix>:nfs_clients` and computes `sharenfs` from it (`export.go:19-22`, `:286-305`; README:15).
   - The ADR also says "Unpublish-all: remove every host we added". That needs a list of which hosts are ours.
   - Details in §3.
9. **ADR 0002's repo line citations are stale, 16 of 19** (table in §2.4); only `zfs.go:43`, `zfs.go:76` and `plan.go:136` still hold. The `zs1-` namespace claim holds at the field level. Three parse sites still treat a no-`@` id as a volume name (§2.2).
10. **ADR 0004's "closes the forged-line hole by switching to JSON" is overstated.**
    - HEAD already fails closed on collisions (`zfs.go:452-459`).
    - `zfs get -j` has its own failure mode: invalid UTF-8 or non-ASCII under the C locale truncates the JSON and still exits 0 (scout `adv4/zfs-json.md` §2). One poisoned property would then break every whole-parent scan.
11. **There is no CONTEXT.md at HEAD** or on any branch, although `AGENTS.md:13` and `docs/agents/domain.md:7` require it. Nothing exists to check a glossary against.
12. **README has 10 stale or false claims**, plus 1 conflict with ADR 0004 (§1). The csi-sanity count is 44 passed of 103 at HEAD (44 ran, 58 skipped by focus, 1 pending), for both access types. "41/103" appears nowhere in the repo at HEAD.

## 1. Corrections table (README, CONTEXT.md, and doc-like text that contradicts them)

| # | file:line | current text | corrected text | evidence |
|---|---|---|---|---|
| 1 | README:5 | "the endpoint can read its ZFS user properties (`democratic-csi:*`), so volumes it provisioned can be adopted" | "...adopts them, and **writes new volumes under the same `democratic-csi:*` prefix by default** (`-property-prefix`). ADR 0003 reopens that default." | `main.go:37`, `endpoint.go:98-100`; ADR 0003 "Property namespace" |
| 2 | README:14 | "gRPC over a unix socket, owner-only permissions" | "gRPC over a unix socket (mode 0600 via umask, dir 0700). **`-listen host:port` serves plaintext gRPC with no authentication.**" | `main.go:111-112`, `:114`, `:129` |
| 3 | README:18 | "not started; the socket is mode 0600 until then (`docs/research/edges2/transport-auth.md`)" | "not started; unix socket is 0600, **TCP listener is unauthenticated**. Design: ADR 0001 (proposed, not yet reviewed)." | `main.go:109-133`; ADR 0001 |
| 4 | README:30 | "one `zfs get -Hp` per call reads the subtree" | "reads the parent's volumes and their snapshots (depth 1), or one volume and its snapshots when the operation names one; anything deeper is dropped" | `table.go:204-236`: `-d depth+1`, then `strings.Contains(vol,"/")` is deleted |
| 5 | README:30 | "Ownership is read only from *locally set* properties" | "...from properties whose source is `local` **or `received`**" | `zfs.go:43`; ADR 0003 Consequences |
| 6 | README:35 | "every resource an operation touches is locked, including the source snapshot of a clone" | "...except `DeleteVolume`, which locks only `vol:<id>` yet destroys the clone-origin snapshot on the **source** volume; Reconcile does lock it" | `endpoint.go:359` vs `plan.go:335-337`; `reconcile.go:164-168` |
| 7 | README:35 | "A busy resource returns ABORTED one second before the caller's deadline" | add "only when the caller sets a deadline; otherwise it waits `-lock-wait` (30 s)" | `endpoint.go:197-210` |
| 8 | README:45 | "`alignment.md` maps 495 operations across 13 implementations" | "495 **mappings** of 108 distinct operations across 13 implementations" | `docs/research/alignment.md:5` |
| 9 | README:60 | "measured: about 12 µs per 10k rows against about 1.5 ms per `zfs` exec" | "SoA would save about 12 µs per 10k-row scan (19 vs 7 µs). 1.5 ms is the spawn cost of a **trivial** process. `zfs`'s own time was **not measured** (no zfs binary on that host). Decoding with a map per dataset cost about 63 ms per 10k datasets." | `docs/research/dod-go/memory-layout.md:5-6`. ADR 0002's own risk section says a 10k+ scan can approach 10 s. |
| 10 | README:63-68, `endpoint.go:10,41,52,56,139,167,353`, `plan.go:310`, `lock.go:3`, `controller.go:124` | "baseline" | "democratic-csi port under review" or "adoption target" | ADR 0003 Consequences: "Docs say adoption target where they said baseline". Low severity. |
| 11 | README:74-75, :80 | `ZFS_TEST_POOL=testpool go test ./internal/endpoint/...`; "CI does the latter on a file-backed pool" | CI runs the `endpoint`, `server` and `zfs` test binaries **and** `csi` (csi-sanity, mount and block) as root in a KVM Ubuntu 26.10 VM with `ZFS_REQUIRE_REAL=1`, under a skip budget | `ci/real-zfs-vm.sh:104-123`, `ci.yml:71-112` |
| 12 | README:17 (no number there) | "csi-sanity ... groups run on the simulator and real ZFS" | The claim is true. If a number is quoted anywhere, use **44 passed of 103** (44 ran, 58 skipped by focus, 1 pending), identical for `mount` and `block`, on the simulator at HEAD. "41/103" is not in the repo at HEAD. | `go test ./internal/csi -run TestSanity -v` against the HEAD export |
| 13 | README:15 vs ADR 0004 | README: clients "in a local `<prefix>:nfs_clients` property with `sharenfs` computed from it" | README matches the code. **ADR 0004 is the document that is wrong** (§3). | `export.go:19-22` |
| 14 | README Status table | no row for ADR 0001/0002, and none for "snapshot ids: a volume id over 91 bytes cannot be snapshotted" | add the 91-byte limit under Open gaps | `plan.go:392-396` |
| 15 | `ci/check-skips.sh:11` | "leg, e.g. vm or noble" | "leg: vm" (the noble leg is gone) | `ci.yml` has no 24.04 real-ZFS job |
| 16 | `endpoint.proto:98` and `endpoint.go:139` | `Snapshot.id` is `"<volume id>@<name>"` | already untrue for hashed components; will be untrue for `zs1-` ids if ADR 0002 lands | `plan.go:424-430` |
| 17 | `main.go:126-128` | "owner-only ... until transport auth lands" | fine, but the TCP path has no such stopgap | `main.go:111` |

**Count of stale or false README claims: 10 clear (rows 1-9 and 11; row 10 is terminology), plus 1 conflict with ADR 0004 (row 13). CONTEXT.md: does not exist at HEAD, so there is no glossary to check.**

README claims I checked and found correct:
- The error-code list (README:37-41).
- The Reconcile description (README:90, against `reconcile.go`).
- `destroy -d`: pyzfs `SnapshotIsCloned` is at `_libzfs_core.py:263` and `_error_translation.py:146`.
- `zfs create -p` exits 0 on an existing dataset: `zfs_main.c:1288-1297`.
- `zvol_auto_resv`: `libzfs_dataset.c:1410`.
- libzfs discards the exportfs status: `nfs_commit_shares` returns the process status (`libzfs_share_nfs.c:514-526`), and the caller `zfs_commit_shares` is void, `libzfs_mount.c:814`.
- The external-provisioner 10 s timeout.
- All `docs/research/...` paths exist.

### CONTEXT.md (glossary check)

No `CONTEXT.md` exists at HEAD or on any branch (`git log --all -- CONTEXT.md` is empty). `AGENTS.md:13` and `docs/agents/domain.md:7-8` say to read it. The agent docs tell agents to proceed silently, so nothing fails. It should still be created: ADRs 0001-0004 now rely on terms the code names differently.

| term used in docs | what the code calls it |
|---|---|
| volume id / "dataset component" | `Row.ID`, `volumeID()`: name relative to the parent. `Volume.Name` is the **CSI name**; `Name` and `ID` are equal for volumes but not for snapshots. |
| snapshot id | `Snapshot.ID`, which is currently `Row.ID`, "vol@component" |
| owned / managed | `Row.Managed` (`managed_resource=true`, local or received) |
| committed | `Row.Committed`, from `provision_success` |
| principal, scope, policy (ADR 0001) | not in code |
| scan / Table / Plan / Op / Planner | `scan`, `Table`, `Plan`, `Op`, `Planner` |
| adoption target / baseline | docs and comments use both |
| desired set | `Desired.VolumeIDs`; proto `desired_volume_ids` |
| clone-source snapshot | `cloneSourceSnapshotPrefix = "volume-source-for-volume-"` |
| export client | `NFSClient`; proto `client` |

## 2. ADR 0002 (proposed)

### 2.1 Hash and domain separation
- The input is `"zfs-endpoint/snapshot/v1\x00" + vol + "\x00" + name`.
  - It is unambiguous. `vol` cannot contain NUL (`datasetRE`, `zfs.go:67`), so the first NUL ends it.
  - `name` is checked for control characters at create time (`plan.go:385`).
  - A property read back from ZFS cannot contain NUL either.
- The domain prefix is sound, and the `v1` tag plus the `zs1-` prefix means the version is stated twice. That is harmless.
- It does **not** bind the parent or endpoint. Two endpoints with the same `(vol, name)` issue the same id. That is fine per driver, but say so.
- The existing component hash `csi-`+hex(sha256(name)[:16]) has **no** domain separation and no volume binding (`plan.go:428-429`). That is safe because the string is only ever used under one volume. The ADR calls the new hash "domain-separated" without noting the two schemes differ.

### 2.2 The "zs1-" collision claim
The namespaces are separate **by field**: CSI passes `source_snapshot_id` and `source_volume_id` separately, `ListSnapshots` has separate filters, and the property `csi_volume_content_source_type` records which kind. A volume named `zs1-<64 hex>` is legal (`volumeID` allows it) and does not collide in the Table, because Table keys are zfs names and long ids are never keys.

The claim is only true if nothing treats a no-`@` string as a volume name. At HEAD three places do:
- `scanFor` (`endpoint.go:323`): `strings.Cut(id,"@")` on a no-`@` id returns the whole id as the volume. `CreateVolume` passes `SourceSnapshotID` into it (`:304`), so a long id would scan `parent/<id>`. A volume squatting that name is scanned instead of the snapshot's volume.
- `CreateVolume` lock keys (`:289-294`): `parseSnapshotID` fails on a long id, so `vol:<source>` is not locked. ADR 0002 handles this with its resolve, lock, rescan order, but `scanFor` must be changed too.
- `snapshotView` (`:262`) and `Table.snapsOfVol` (`table.go:79`) derive the volume with `Cut(r.ID,"@")`. That is fine for row ids, which are zfs names.

Rule for the implementation: resolution must filter on `Type==snapshot`. `plan.go:182` already does for `t.Get`. A generic "Get, else fall back to hash scan" would return a squatting **volume** row first and give a false `NOT_FOUND` for the real snapshot.

Other gaps:
- `ReconcileAction.Target` and `ReconcileSkip.Target` (`reconcile.go:71,163`, proto `:54-55`) emit zfs-relative names, not ids. ADR 0002's "one function decides every outgoing id" does not cover them.
- The proto comment `Snapshot.id = "<volume id>@<name>"` needs to change.

### 2.3 Behaviour in the 2.4-only, JSON-scan world
- **Not an issue yet.** ADR 0004's JSON scan does not exist at HEAD: `ListColumns` still runs `zfs get -Hp` (`zfs.go:402-418`), and the simulator emits text.
- **Whole-parent scan cost.** The ADR prices resolution as "microseconds of hashing vs a millisecond-scale exec". The real cost is the O(parent) scan, and the JSON form of it is heavier than `-Hp`: every property is an object with a nested `source`. 10k datasets × 25 properties is tens of MB. ADR 0002 defers measuring this to CI. `ListVolumes`, `ListSnapshots`, `CreateSnapshot`, `DeleteVolume` and `Reconcile` already do a full scan, so this is a general cost, not specific to long ids.
- **Poisoned-scan availability coupling.** If JSON truncation on non-ASCII or invalid UTF-8 is real (scout `adv4/zfs-json.md` §2, from source), one tenant's property breaks every full scan for every tenant. The runner sets no locale (`runner.go`).
- **Received-as-local.**
  - The "ambiguous snapshot id → FAILED_PRECONDITION" path and the "received copy under another volume gets a different id" argument both depend on `Prop.Local()` counting `received`. ADR 0003 puts exactly that up for reopening.
  - If it flips, received copies stop being managed, most ambiguity disappears, and the liberal-input rule changes. The sequencing dependency is not recorded.
  - A permanent `FAILED_PRECONDITION` on `DeleteSnapshot` also leaves a `VolumeSnapshotContent` stuck, in tension with the CSI idempotent-delete rule.
- **Detached snapshots.** democratic-csi detached `<vol>/<name>` stay unsupported. Fine, and consistent with `parseSnapshotID`.

### 2.4 Line citations at HEAD

Every citation in the ADR is stale except `zfs.go:43`, `zfs.go:76` and `plan.go:136`. The CSI spec and csi-test citations are correct: spec.md:486-495 and 2237-2246, and csi-test controller.go:459 and 1405-1419 (the max-length name is at :460 and :1411), with `MaxNameLength` at :42. The democratic-csi `index.js` citations are accurate to within 1-3 lines.

| ADR cites | HEAD |
|---|---|
| `volumeID` endpoint.go:145 | :169-174 |
| `snapComponent` plan.go:380 | :424-430 |
| id over 128: plan.go:347-351 | :392-396 |
| DeleteSnapshot scans one volume: endpoint.go:497 | :521 |
| CreateVolume lock keys: endpoint.go:264-280 | :288-302 |
| `table.go:122-160` snapshot properties | `decodeColumns` :143-202 (snapshot columns :148, :183-184) |
| `snapshotView` endpoint.go:237-241 | :261-276 |
| CreateSnapshot idempotency plan.go:353-358 | :397-404 |
| full scans endpoint.go:439, :466 | CreateSnapshot :463, ListSnapshots :490 |
| evidence plan.go:336-386 | :378-430 |
| plan.go:388-402 | :432-446 |
| endpoint.go:145-165 | :169-189 |
| endpoint.go:237-252 | :261-276 |
| endpoint.go:262-312 | :283-338 |
| endpoint.go:432-500 | :454-530 |
| plan.go:188-191 | :216-219 |
| zfs.go:43, zfs.go:76, plan.go:136 | unchanged |

### 2.5 Other findings
- **Error text leaks across volumes.** `plan.go:402` returns `snapshot %q exists for volume %s` with the *other* volume's id. This matters for ADR 0001 (see §4).
- **Locks.** `CreateSnapshot` locks `snap:<vol>@<name>` (the CSI name); `DeleteSnapshot` locks `snap:<id>` (the component). They differ for hashed names. Both also take `vol:<vol>`, so it is safe today. With a legacy-input plus canonical-id pair on the same snapshot, the `snap:` keys differ again; `vol:` still serializes them.
- **91-byte cap math checks out.** 91+1+36=128 and parent ≤ 89 (89+1+128+1+36=255). Compatibility holds: ids ≤128 are returned unchanged.
- **Cap on clone-source snapshot names** (255 bytes, `plan.go:216-219`) is correctly left undecided.

## 3. ADR 0003 and ADR 0004

### 3.1 ADR 0003 (accepted)
- **The Decision is currently false of the code.**
  - "Never touch what you don't own ... the endpoint refuses rather than destroys." HEAD destroys foreign snapshots and child datasets in `DeleteVolume` and Reconcile (`plan.go:324-334`). The scan drops children, so the planner cannot even see them (`table.go:234-235`).
  - The ADR does list this under "reopened", so it is tracked in prose, not as work. It is data loss with sanoid or zfs-auto-snapshot snapshots.
  - Uncommitted work in the live tree adds a `DestroyForeignSnapshots` flag. It is not in HEAD.
- **"Ownership is OpenZFS's own: user properties set locally."** OpenZFS has no ownership model. It has property sources: `local`, `received`, `inherited`, `default`. The ownership rule is this project's convention. The ADR text claims otherwise, and the next sentence admits `received` is counted.
- **"Reading its datasets ... as an explicit compatibility mode."** No mode exists. There is only a default prefix. Adoption and new-volume writes share one flag.
- **"As verified on a real 2.4.x pool."** No real-pool output is stored in the repo; CI shows only pass or fail. `docs/research/dod-go/testing.md:7` says the classify ordering is "unverified against a live pool". README:5 claims verification the repo cannot show.
- **Untracked consequences.** Each of the three reopened items (property prefix default, `destroy -r` over foreign data, received-as-local) has no issue and no owner. The GitHub tracker contains only PR #1.
- **Evidence order vs. README.** The README decisions table cites other implementations (zfs-localpv, truenas-csi) as the evidence for several rows, in the tier the ADR ranks last. Fine as practice, but not as the "correctness" evidence the ADR demands.
- **Same-day accepted.** ADRs 0001 and 0002 are "proposed" while 0003 and 0004 are "accepted" on the same date. ADR 0004 itself rests on an owner statement the repo does not record.

### 3.2 ADR 0004 (accepted)

**Contradictions inside the ADR**
- "Export state lives in `sharenfs` ... **nowhere else**. There is no client list" vs "Unpublish-all: **remove every host we added**." Telling "ours" from foreign hosts in a `sharenfs` value needs a record of what the endpoint added. That record is exactly what `nfs_clients` is.
- "Per-node publish can follow ... **without new state**" vs the shipped per-client `Publish/Unpublish` RPCs, which add `<prefix>:nfs_clients`.
- "Static export at create comes first" vs the code. `CreateVolume` forces `sharenfs=off sharesmb=off` (`plan.go:170`) and has no export field (proto `CreateVolumeRequest` 106-128). Per-client RPCs came first and static export does not exist.
- "A `node_id` used as an export client must be validated as a single host, never a CIDR." The RPC deliberately accepts IPv4 CIDRs (README:15, `export.go:71-81`). The rule must live in the CSI front end, which has no ControllerPublish at all (`controller.go` has no Publish).
- "Costs of supporting 2.2 ... dropped: the hardened text parser for 2.2." The text parser is the **only** parser at HEAD, hardened, and will stay until a JSON scan exists. The ADR describes the target as if it were the present.

**Task 3c: is "OpenZFS as source of truth" consistent with a `nfs_clients` user property?**
- The owner was asked "static at create, or per node at ControllerPublish?" The answer, "OpenZFS as source of truth", chooses neither. It is a principle. ADR 0004 turns it into "sharenfs only, no list" and "static and per-node are one mechanism". That step is inference, and the ADR does not mark it as inference.
- A user property **is** OpenZFS state: stored in the pool, visible to `zfs get`, replicated by `send -p`, and following ADR 0003's own `module:property` convention. So `nfs_clients` honours the principle.
- In the code, though, `sharenfs` is not the source of truth. `exportOps` rewrites `sharenfs` whenever it differs from the value computed from `nfs_clients` (`export.go:298-300`). A hand edit of `sharenfs` on a managed volume is reverted on the next publish, or refused as "foreign" when no list exists (`export.go:282-284`). `nfs_clients` outranks both `sharenfs` and the kernel etab.
- **Conclusion:** the code is consistent with the owner's principle and inconsistent with ADR 0004's *text*. Fix the ADR (it must state that `nfs_clients` is the desired state and `sharenfs` is derived), or get the owner to say that hand edits of `sharenfs` must win. It is not a code bug.

**Consequences not tracked anywhere**
- `CreateVolume` does not return `server` and `share` in `volume_context` (`controller.go:207-218` sets dataset, kind, devicepath, mountpoint).
  - csi-driver-nfs reads lowercase `server` and `share` and fails with `InvalidArgument` if either is empty (`nodeserver.go:85-124`, `nfs.go:77-83`).
  - The endpoint has no flag for the address it is reachable at.
- No ControllerPublish exists, so there is no per-node export caller. `node_id` from csi-driver-nfs is `--nodeid` (typically the Kubernetes node name). NFS matches clients by IP, so a node-id to IP mapping is missing.
- Static export to a CIDR exposes every volume to every host in it, a security regression against per-client export.
- No startup check enforces "2.4 only". `GetCapabilities` reports no zfs version.
- The JSON migration needs:
  - a rewritten simulator (`zfstest/sim.go` speaks text);
  - new fuzz targets;
  - a locale decision (`LC_ALL=C.UTF-8`) for the runner;
  - a strict-truncation test.
- `zfs get -j` on 2.2.9 does not exist (confirmed: no `json-int` in zfs-2.2.9's `zfs_main.c`). The "since 2.3.0" claim could not be checked from the local clones (no 2.3.0 tag). Scout `adv4/zfs-json.md` read it via GitHits.
- `snapdir=disabled` is real in 2.4 (`zfs_prop.c:242` in the 2.4.0 extract) and absent in 2.2.9. The ADR's "Costs of supporting 2.2" bullet is accurate.
- The forged-line "hole" is already partly closed at HEAD. `parseColumns` returns an error when any (dataset, property) pair is printed twice (`zfs.go:452-459`), and real output always has one line per requested pair. **Residual:** a forged line naming a dataset with no real line creates a phantom row. A forged `managed_resource=true`, `provision_success=true` would make `ListVolumes` report a volume that does not exist. It needs the ability to set a property value on a dataset under the parent, for example via a `send -p` stream. Low severity; it is the real argument for JSON, not the one the ADR makes.
- CI facts in the ADR's Context ("tested on two systems", 24.04 pairing 2.2.2 userland with 2.3.4 kmod) are past tense and cannot be re-verified here. HEAD's CI has the one VM leg.

## 4. ADR 0001 (proposed): findings

### 4.1 Is SO_PEERCRED enough?
Not as a tenant boundary. It is fine as the second gate and as an audit identity.

- **pid reuse.**
  - Authorization uses uid only, so reuse cannot grant access. But `pid` is "logged, not authorized", and the number can be stale or recycled after the connecting process exits.
  - Say that the logged pid is advisory. If a stable handle is wanted, Linux 6.5+ has `SO_PEERPIDFD`; this sandbox kernel is 6.18.
  - The ADR's rejection of pid-based attestation is correct.
- **Connect-time creds.**
  - The kernel records creds when `connect()` is called. A process that connects and then calls `setuid`, or passes the fd over `SCM_RIGHTS`, keeps the original identity.
  - The ADR's `MaxConnectionAge` mitigation covers this only for uid-based principals.
  - For TLS, per-call policy already revokes a removed principal. The remaining value of `MaxConnectionAge` is an **expired certificate**, which TLS checks only at handshake, and CA-level revocation. The ADR's stated reason ("a removed policy entry or a dropped uid") is already covered by the per-call policy read.
- **Containers and user namespaces.**
  - Without a user namespace, container uid N equals host uid N. All root containers are `unix:uid=0`. The ADR says this.
  - With a user namespace, the kernel translates the uid into the *reader's* namespace. An unmapped uid reads as the overflow uid 65534 (my reading of `net/core/sock.c`; the kernel source is not in the provided sources, so verify on the VM).
  - The ADR's CI test denies by running as uid 65534. If the daemon ever ran inside a user namespace, unmapped uids would show up as 65534. Pick a different uid for the denial test, and never put `unix:uid=65534` in a policy.
  - Idmapped mounts change only the permission check on the socket inode. They do not change `SO_PEERCRED`. This narrows the ADR's "Unverified" risk; test it in the VM.
  - **The bigger gap is self-assertion on Kubernetes.** A pod that is allowed a hostPath mount of the socket directory sets `runAsUser`. The uid proves nothing the pod author did not choose.
- **The kubelet and sidecars run as root.** The only caller of this endpoint is `zfs-csi`. It talks to the endpoint, not to the kubelet. A root caller can already run `zfs` itself, so uid 0 gains nothing from policy. Unix principals are therefore meaningful only for non-root, host-local services, a deployment the ADR doesn't actually describe.
- **Abstract sockets.** See top finding 2. The ADR must reject `@name` and relative paths. The abstract namespace is per network namespace, so it ignores filesystem permissions entirely.
- **Socket file permissions are the real control.**
  - `umask 0177` is process-wide and, as the code comment itself notes, racy against anything else that creates files. Prefer `Listen` under a private 0700 directory, then `fchmod` and `fchown`; restrictive-first is safe. Or use systemd socket activation (`SocketMode=`, `SocketGroup=`).
  - The directory mode is part of the control (finding 3).
  - `lockParent` shares `/run/zfs-endpoint` (default) with the socket (`main.go:40-41`, `:138`).
- **Stock grpc-go.** Answered in top finding 6. Peer info comes from `peer.FromContext(ctx).AuthInfo`, which is `nil` or an `insecure.info` for the stock credentials.
- **mTLS, identity from the SAN.**
  - grpc-go's `TLSInfo.SPIFFEID` (`credentials/tls.go:45,161`) is set only for scheme `spiffe` and exactly one URI SAN (`internal/credentials/spiffe.go`). The ADR's "any URI SAN, cert-manager style" principal is not filled in by grpc, so parse `VerifiedChains[0][0].URIs` yourself.
  - The ADR's table says `RequireAndVerifyClientCert`. Its own Evidence section says go-spiffe's `MTLSServerConfig` uses `RequireAnyClientCert` and verifies in a callback. The two sources have different verification code. A test must cover both.
  - **The CA is the trust root, not the policy.**
    - Any holder of a certificate from a trusted CA can pick any URI SAN. With a shared cert-manager issuer, whoever may create a `Certificate` object chooses the identity.
    - Require a dedicated CA for the endpoint, or a name-constrained CA (a permitted URI domain).
    - Compare the URI canonically (lowercase host, no userinfo, query or fragment) and match exactly.
  - **TLS 1.3 timing.** The client finishes its handshake before the server validates the client certificate. A bad certificate shows up on the first read as `Unavailable`, not at dial time. The ADR's `UNAUTHENTICATED` expectation is for the server side only. Test with the first RPC, not `WithBlock`.
  - **Two listener kinds on one server:** see top finding 6.
  - **No revocation for new handshakes.** Removing a principal from the policy is the revocation lever.
- **Unary and stream interceptors.** All 13 RPCs are unary (no `stream` in the proto). The "long-lived streams" risk is empty today. Keep the stream interceptor as a default-deny.
- **New error codes.** `endpoint.Code` has no `PermissionDenied` or `Unauthenticated` (`errors.go:12-23`). The interceptor can use gRPC codes directly, but the endpoint-side scope check will need them.

### 4.2 Policy granularity: per-RPC vs per-volume
- **Per-RPC is cheap and enough for the first version.** Group RPCs as roles instead of a list of names:
  - `read`: GetCapabilities, GetVolume, ListVolumes, ListSnapshots, GetCapacity
  - `provision`: Create/Delete/Expand volume, snapshot create/delete
  - `admin`: Reconcile, Publish/UnpublishFileExport
- **Per-volume scope has these defects.**
  1. Reconcile and the export client are unscoped (finding 1).
  2. A name prefix is a *convention*, not recorded in ZFS. ADR 0003 says OpenZFS is the source of truth, and the ADR itself lists the policy file as "a new source of truth not in ZFS". A `<prefix>:tenant` user property set at create (`create -o`) would live with the dataset, survive policy edits, and show in `zfs get`.
  3. Prefix overlap: `a-` and `a-b-` give one tenant a view into the other's names. The ADR tests `a-` vs `a` and `ab-`, not overlapping prefixes across principals. Reject a policy where one principal's prefix is a prefix of another's (unless `"*"`).
  4. **Snapshot name namespace is global** (`plan.go:397-403`, lock `snapname:`). It is worse than an "oracle". The error message names the other tenant's volume (`plan.go:402`). A tenant can pre-claim a predictable name (Velero-style) and permanently block another tenant's CreateSnapshot with `ALREADY_EXISTS`. Uniqueness has to become per scope, and the message must not name the other volume.
  5. Pagination tokens are ids of the last visible item (`endpoint.go:416-437`). With filtering applied before paging this is fine. Filtering after paging would leak or loop.
  6. `GetCapacity` is parent-wide, so quota is not isolated either. ZFS-native isolation is a separate parent (and `refquota`/`quota` on it) per tenant, which the existing flock-per-parent design already assumes.
- **ADR text errors:** "10 RPCs" → 13; "cmd/zfs-csi on branch csi-frontend" → on HEAD (`cmd/zfs-csi/main.go:40`).

### 4.3 Minimal secure first version

See the next section.

## 5. Recommended minimal ADR 0001 v2

**Status: proposed (v2).** Replaces the plan that ships per-uid, per-volume-prefix policy.

**Context (corrections).**
- `EndpointService` has 13 unary RPCs.
- The TCP listener is unauthenticated today and must stop being so.
- Tenancy is not provided by uid.
- `Reconcile` and file export are admin operations.

**Decision.**

1. **Unix listener.**
   - The path must be absolute (`unix:///abs/path`). Reject `@name` and relative paths at startup.
   - Create the socket inside a private directory. Set the directory to `0750` and the socket to `0660` with `-socket-gid` (default: the daemon's gid and `0600`/`0700`). Apply the mode with `fchmod`/`fchown` while the directory is still restrictive, or use systemd socket activation.
   - Identity is `unix:uid=<n>` from `SO_PEERCRED` through a small `TransportCredentials` (stdlib `syscall.GetsockoptUcred`, Linux build tag). The pid is logged as advisory, or taken from `SO_PEERPIDFD` where available.
   - **This is not a tenant boundary.** Socket permissions are the gate and the uid is an audit label. One front end per socket.
2. **TCP listener.**
   - It refuses to start unless `-tls-cert`, `-tls-key` and `-tls-client-ca` are given. Use TLS 1.3, `RequireAndVerifyClientCert`, and an explicit CA file (never the system roots). The CA must be dedicated to this endpoint or name-constrained.
   - The principal is the **single URI SAN** of `VerifiedChains[0][0]`, matched exactly after canonicalisation. Zero or more than one URI SAN is refused. CN and DNS SANs are ignored.
   - SPIFFE workload API support (`go-spiffe`) is a later add-on, not in v1. It brings a dependency and a second verification path.
3. **A dispatching `TransportCredentials`** selects unix or TLS by the connection type. Any other connection type, or a missing or unknown `AuthInfo`, gives `UNAUTHENTICATED`. There is no unauthenticated mode, including on loopback.
4. **Policy (static JSON, default deny, reload on SIGHUP, last-good kept).**
   `{"principals":[{"id":"spiffe://example.org/csi/a","roles":["read","provision"]}, {"id":"unix:uid=0","roles":["read","provision","admin"]}]}`
   - Roles are fixed sets of RPCs: `read`, `provision`, `admin`. `admin` is Reconcile and the export RPCs.
   - A method table maps every entry in `EndpointService_ServiceDesc` to a role. A test fails if an RPC has no entry, and the interceptor denies any method not in the table.
   - An empty or invalid policy refuses to start. A failed reload keeps the old policy and logs.
5. **Tenancy.** One endpoint per tenant parent dataset, each with its own socket or port and its own policy. ZFS `quota`/`refquota` on the parent gives capacity isolation. **No volume-prefix scope in v1.**
   - If a shared parent is later required, add scope as a separate ADR. It must cover: Reconcile restricted to the scope; export clients constrained to allowed CIDRs; per-scope snapshot-name uniqueness; error text that does not name foreign ids; the tenant recorded as a ZFS user property rather than a name prefix; non-overlapping scopes validated.
6. **Revocation.** Remove the principal from the policy (per-call). `MaxConnectionAge: 1h` bounds the life of a connection whose certificate has expired. Certificate rotation reads from files on SIGHUP with an atomic swap.
7. **Tests.**
   - A pure authorizer table: every RPC × role.
   - An interceptor over `bufconn` with a fake credential for each `AuthInfo` variant. `bufconn` has no fd, so the peer-cred handshake is not covered there.
   - A real unix socket in `t.TempDir()`: allowed uid OK; a child process under another uid denied by permissions. **Use a uid other than 65534 for the denial.** An `@abstract` path and a relative path are refused at startup.
   - Real TCP on `127.0.0.1:0`. Cases: no client cert, wrong CA, expired, zero or two URI SANs, and a non-allowlisted URI. Assert on the **first RPC's** error, because TLS 1.3 reports client-cert failure there.
   - `zfs-csi` gains mTLS client flags. Its sanity run keeps the unix path.

**Consequences.**
- It closes the live TCP hole and the abstract-socket bypass immediately.
- Local multi-tenancy by uid is explicitly not offered.
- The ADR 0002 interaction disappears in v1: the scope check needs no id resolution.
- Open risk: the exact uid translation across user namespaces is unverified here; test it in the VM before documenting any userns guidance.

## 6. Method notes and limits
- HEAD sanity numbers come from running `TestSanity` against the HEAD export, not the live tree.
- I could not run `zfs` here, so nothing marked "from source" has been seen on a pool. `grpc-go` and `csi-test` were read from the Go module cache because `repos/` has no copies.
- The `SO_PEERCRED` user-namespace translation and `SO_PEERPIDFD` details are from kernel man-page knowledge, not from a kernel tree in the sandbox.
- Two throwaway programs live in `.../scratchpad/wave6/p/` (netip CIDR parse, abstract socket, and `GetsockoptUcred`). The repo was not modified.
