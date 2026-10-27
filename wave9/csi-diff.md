# wave9: differential fuzzing of the CSI front end

HEAD 1603dad (includes 2524410), Go 1.27.3, simulator only. The copy is `wave9/csi-diff/`, and every new file is in `internal/csi/` there:

| file | what |
|---|---|
| `oracle_test.go` | the CSI spec oracle: one pure function per controller RPC, (request, abstract world) → allowed outcomes, cited to spec lines |
| `difffuzz_test.go` | the differential fuzzer (`TestCSIDiffSeeds`, `FuzzCSIDiff`), the A/B harness and the triage table |
| `probes_test.go` | `TestCSIProbes`: one deterministic call per divergence class, logged, asserts nothing |
| `wave9_bugs_test.go` | `TestWave9_*`: reproducers for the top bugs, **failing** at HEAD |
| `nfs_acceptance_test.go` | `TestNFSAccept_N1..N15`: NFS acceptance tests, **failing** at HEAD |

Citations of the form "spec:N" refer to line N of `spec.md` at container-storage-interface/spec **v1.13.0**, which is the module go.mod pins. A copy is in `wave9/spec-v1.13.md`. The `repos/spec` checkout is 1.14.0, and its line numbers differ.

```
cd wave9/csi-diff
go test ./internal/csi -run TestCSIDiffSeeds -v                 # 2 parents x 300 seeds x 40 ops
CSI_DIFF_SEEDS=1500 CSI_DIFF_REPORT=r.json go test ./internal/csi -run TestCSIDiffSeeds
CSI_DIFF_TRACE=<signature substring> ...                        # attach op history to matching findings
go test ./internal/csi -run '^$' -fuzz FuzzCSIDiff
go test ./internal/csi -run 'TestNFSAccept|TestWave9' -v        # the red list
```

## 1. The oracle

The world is what the CO has been told, and nothing about ZFS:
- live volumes, keyed by name, each with the request that created it and the `Volume` returned;
- live snapshots;
- ids that are gone, and which source volume each snapshot came from;
- every `next_token` ever issued.

Where the spec leaves the choice to the plugin, every allowed outcome is in the set and the choice is noted as an ambiguity. The verdict is the union over all the conditions that apply. OK is in the set unless some condition forces a failure.

| RPC | rules (spec v1.13.0 lines) |
|---|---|
| all | missing REQUIRED field → INVALID_ARGUMENT (525); invalid/unsupported field → INVALID_ARGUMENT allowed (526); strings ≤ 128 B, maps ≤ 4 KiB (494-495) |
| CreateVolume | name REQUIRED (827). **Any Unicode name ≤ 128 B without the banned controls is legal**, so INVALID_ARGUMENT for the name alone is a violation (849-851). Capabilities REQUIRED and non-empty (876, 500). Each capability needs an access type (1027-1028) and a mode (1022-1023, 1034-1035); which modes and fs_types are supported is the plugin's call (863-875). capacity_range: negative → INVALID_ARGUMENT (1044, 1049); required > limit → INVALID_ARGUMENT or OUT_OF_RANGE; all-zero is a CO error that may be read as "unspecified" (1040 vs 1043); no range → the plugin MAY choose (855-857). OK ⇒ required ≤ capacity ≤ limit (1042-1050, 858), 0 = unknown (1056-1057), a block volume must match the range (823-824). Parameters are opaque and plugin-validated (879-882). accessibility_requirements and mutable_parameters SHALL NOT be sent (900-901, 916-917). Content source: one type MUST be set (921-922); an empty snapshot_id/volume_id is a missing REQUIRED field (925-936); a missing source → NOT_FOUND (1295); an incompatible source → INVALID_ARGUMENT allowed (1294); OK must echo the source (1088-1091). A volume from a snapshot is never smaller than it (2229-2230; else OUT_OF_RANGE, 1298). Idempotency: an identical retry MUST be OK with the same id and the same volume_context (807-808, 1082-1083); a different source, or a capacity outside the new range, → ALREADY_EXISTS (1296); other differences are the plugin's call. Ids are unique (1063-1064). No accessible_topology without the capability (1095-1096). |
| DeleteVolume | id REQUIRED (1317-1318); missing, gone or never-issued → OK (1307); with snapshots or clones → OK or FAILED_PRECONDITION (1309-1313, 1340) |
| ValidateVolumeCapabilities | id and caps REQUIRED (1487, 1496-1498); missing → NOT_FOUND (1559); confirmed ⇒ it contains every requested capability (1479, 1522-1523); the capabilities a volume was created with, with the same parameters, MUST be confirmed (863-866 + 1479). A cross-RPC rule: what Validate confirms, CreateVolume must treat as compatible (808). |
| ListVolumes / ListSnapshots | max_entries < 0 → INVALID_ARGUMENT (1578); a token never issued → ABORTED (1636, 2418); a stale token → ABORTED or continue (1566-1567); a page ≤ max (1571-1572). A complete walk lists every live volume or snapshot (1565, 2351), lists no deleted one, and has no duplicates. The listed volume_context and content source equal those at create (1082-1083). The snapshot filters hold (2376-2385). |
| GetCapacity | available ≥ 0 (1979); capabilities and parameters SHALL be considered (1974-1977) — an ambiguity |
| CreateSnapshot | name and source REQUIRED (2172-2177); names as for volumes (2178-2181). A missing source has no table row, so any non-OK code is allowed (2296-2304). Same name + same source → OK with the same id (2133-2134); other source → ALREADY_EXISTS (2302). OK ⇒ id ≤ 128 B (2236-2240), source echoed (2247-2250), creation_time set (2252-2254), ready_to_use SHOULD be true (2156). |
| DeleteSnapshot | id REQUIRED (2322-2323); missing → OK (2313); has clones → OK or FAILED_PRECONDITION (2344) |
| ControllerExpandVolume | id and range REQUIRED (2509, 2512-2513); missing → NOT_FOUND (2546); a capability the volume does not support → INVALID_ARGUMENT (2545), judged by the plugin's own Validate; a malformed capability → INVALID_ARGUMENT. Already ≥ target → OK (2472). OK ⇒ capacity ≥ required (2531-2532), ≤ limit unless already larger, and never smaller than before. |

## 2. The differential fuzzer

**Path A** is the shipping stack: CSI request → `internal/csi` Controller → bufconn gRPC → `internal/server` → `internal/endpoint` → sim A. A unary interceptor on the server records whether the request reached the endpoint and what code the server returned. A difference can therefore be placed in the CSI layer, the gRPC server, or below them.

**Path B** sends the same logical operation straight to a second endpoint on sim B, with the mapping a naive translator would use:
- the first capability picks the kind;
- `int64` converts to `uint64`, so negative values wrap;
- `sparse` and `reserve` are parsed;
- everything else is dropped.

B runs live whenever A reached the endpoint, so the two sims stay in lockstep (the clocks are fixed). When the CSI layer refused a request before the endpoint, B runs on a throwaway replica rebuilt from B's log. A divergence in state stops the sequence.

**Generated:**
- **Names:**
  - `pvc-1..4`;
  - 128 B and 129 B names;
  - a 100 B name (beyond the 91 B a snapshot id allows);
  - `""`, `πβ-volume`, `with space`, `a/b`, `a@b`, `tab\tname`, `ctl\x01`, `-leading`;
  - snapshot names: the same kinds of variant, plus the endpoint's reserved prefix `volume-source-for-volume-x`.
- **Capabilities:** 1-2 capabilities, with all 8 modes including UNKNOWN or none, block or mount, fs_type ""/zfs/ext4/nfs/xfs or no access type.
- **Capacity ranges:** 22 ranges, including 0/0, 1, 16383, an exact 16 KiB, 1 GiB+1 against limits, required > limit, limit only, negative values, MaxInt64 and MaxInt64-100.
- **Parameters:** sparse/reserve true/false/"yes", an unknown key, `nfs.clients`, `nfs.server`, and k8s-prefixed keys.
- **Content sources:**
  - none;
  - a known or unknown snapshot or volume;
  - an empty id;
  - an empty `VolumeContentSource{}`;
  - biased well-formed restores, needed to reach E6.
- **Other fields:** topology and mutable_parameters, sent occasionally.
- **Operations:** ListVolumes and ListSnapshots walks (max 0/1/2/3/−1, a garbage token, the snapshot filters), GetCapacity with capabilities and parameters, Expand with or without a capability, and a **retry** of the previous op.
- **Parents:** both `tank/vols` and a 135-byte parent.

**Runs:**
- Seeded: 1,500 seeds × 2 parents × 40 ops, about 52 s. That gave **25,099 findings in 95 signatures, 0 untriaged**. The report is `wave9/csi-diff-report.json`.
- Coverage-guided: `FuzzCSIDiff` ran for 150 s, 5.6k execs and 14 corpus entries, with no untriaged finding. The host's load average was about 88 during the run, so throughput was bursty.

**Clean, no divergence:**
- Ids and volume_context are identical on both paths.
- Capacity rounding agrees between CSI and the endpoint: zvols round up to 16 KiB, and `i64` clamps above MaxInt64.
- On success the content source is echoed, `ready_to_use` is true, snapshot ids are ≤ 128 B, and accessible_topology is never set.
- Retries return the same id and the same context.
- Full list walks are complete, with no deleted entries and no duplicates, and the pages respect max_entries.
- Delete of a missing or never-issued volume or snapshot is OK.
- The gRPC server never rewrote a code, apart from its own max_entries guard.

## 3. Divergences by verdict

### CSI translation bugs (internal/csi)

| id | finding | evidence | fix |
|---|---|---|---|
| **T1** | A content source with an empty `snapshot_id` or `volume_id` is passed on as `""`, which the endpoint reads as "no source". The result is an **empty volume, reported OK without `content_source`**. The CO asked for a restore. | spec:925-936 (REQUIRED), 525. 130 hits. `TestWave9_T1_EmptySourceIDRefused` | `CreateVolume`: INVALID_ARGUMENT when the chosen source's id is empty |
| **T2** | `ControllerExpandVolume` never looks at `volume_capability`. Block access on a filesystem volume is OK, though Validate refuses the same capability, and a capability with no access type or no mode is OK. | spec:2545 (MUST INVALID_ARGUMENT), 1027-1035, 525. 119 hits. `TestWave9_T2_ExpandChecksCapability` | validate the capability with `capSupported` against the volume's kind; this needs a `GetVolume` first |
| **T3** | ValidateVolumeCapabilities **confirms mount on a zvol** (`capSupported`: "a zvol may also carry a filesystem"). CreateVolume with the same name and that same capability is **ALREADY_EXISTS**, because `kindOf` maps mount to FILESYSTEM. The plugin contradicts itself. Under ADR 0004 no node side formats zvols, so confirming mount is the wrong half. | spec:808 + 1479. 346 hits. `TestWave9_T3_ValidateAgreesWithCreate` | refuse mount on BLOCK in `capSupported` (or only in adoption mode) |
| T4 (minor; not a fuzz finding) | `mutable_parameters` and `accessibility_requirements` are silently dropped, while unknown `parameters` are refused "rather than silently dropped". The CO is the one in breach (spec:900-901, 916-917), so the oracle permits OK. | probe | refuse non-k8s mutable_parameters for consistency |

### Endpoint bugs (internal/endpoint)

| id | finding | evidence |
|---|---|---|
| **E6** | A **filesystem restore or clone with `required_bytes` below the source size is created smaller than the source**: refquota is set to `required`. In the probe, a 2 GiB snapshot restored with 1 GiB gave a 1 GiB volume. `PlanCreateVolume` uses `size = want` and only falls back to the source size when want is 0. zvols are unaffected, because `sizeOps` never shrinks volsize. On real ZFS, once the data exceeds `required`, `zfs set refquota` refuses ("size is less than current used"), after the clone has been made. That case was not verified on a pool. | spec:2229-2230 (MUST NOT be smaller), 1298 (or OUT_OF_RANGE). 12 hits, only after biasing the generator toward restores. `TestWave9_E6_RestoreNotSmallerThanSnapshot` |
| **E1** | Volume names the spec allows are INVALID_ARGUMENT: `πβ-volume`, `with space`, `a/b`, `a@b`, `tab\tname`, `-leading`. `volumeID` uses the CSI name as the zfs component. Snapshot names already fall back to a hash (`snapComponent`), and volumes need the same fallback. Kubernetes' `pvc-<uuid>` is unaffected. | spec:849-851. 343 hits. `TestWave9_E1_LegalNames` |
| **E2** | `endpoint.New` accepts a parent longer than 126 bytes. A legal 128-byte name then overflows the 255-byte dataset name, and it is refused as INVALID_ARGUMENT with a leaky message (`scan …: invalid property: invalid name`). csi-sanity's max-length test would fail on such a deployment. | spec:494. 21 hits on the long parent. `TestWave9_E2_LongParentRefused` |
| E3 | Snapshot names with TAB, LF or CR are refused (`unicode.IsControl`), but the spec bans only U+0000-0008, 000B, 000C, 000E-001F and 007F-009F. | spec:2178-2181. 95 hits |
| E4 | The CSI snapshot name `volume-source-for-volume-*` is refused as a reserved prefix. Every CO name is legal, so the reservation belongs in a property (README already lists "keying clone-source snapshots on a property" as open). | spec:2178. 79 hits |
| E5 | A volume whose id is over 91 bytes (one legal at create) cannot be snapshotted. ADR 0002, still *proposed*, covers it. | spec:494, 2133. 47 hits |
| E7 | A **block** volume with only `limit_bytes` is refused ("block volume needs a capacity"). The range is valid, and filesystems take the limit as the size. | spec:855-858, 1039-1050. 30 hits |

### Intentional (cited)

| behaviour | source |
|---|---|
| Unknown parameters and non-boolean sparse/reserve are refused (CSI-only refusals; B would succeed) | `controller.go` parseParams: "refused rather than silently dropped" |
| Only SINGLE_NODE_* modes; mixed block+mount refused | `controller.go` modeSupported: "No export or attach exists yet" |
| Mount fs_type must be "" or zfs | `controller.go` kindOf |
| Required CSI fields, negative/empty/inverted ranges, empty `VolumeContentSource{}`, DeleteSnapshot without id are checked in the CSI layer. Path B shows why: negative capacities wrap to huge `uint64` and *succeed* on the endpoint | `identity.go` package doc; spec:525, 921-922, 1040, 1044, 1049 |
| `max_entries < 0` is refused by the gRPC server. The endpoint's Go API reads it as unlimited, which gRPC cannot reach | `server.go` maxEntries; spec:1578 |
| Expanding an unlimited filesystem leaves it unlimited and reports the requested size, so successive expands report whatever was asked last, smaller sizes included | `endpoint.go` ExpandVolume comment; spec:2531-2532 |
| DeleteVolume with CSI snapshots → FAILED_PRECONDITION | README "Corrections" (matches the baseline); spec:1313 allows it |
| ListSnapshots lists only snapshots the endpoint cut, though the spec says "regardless of how they were created" (2351) | ADR 0003 |

### Oracle ambiguities (the spec permits both)

| id | behaviour | spec |
|---|---|---|
| A1 | **Precedence**: the CSI layer refuses a malformed request (INVALID_ARGUMENT) before the endpoint would report NOT_FOUND, ALREADY_EXISTS or OUT_OF_RANGE. This class accounts for 10.5k of the findings. | no order defined |
| A2 | required > limit: the CSI layer says INVALID_ARGUMENT, while the endpoint's own rounding check says OUT_OF_RANGE (e.g. a block volume of 1 GiB+1 against a 1 GiB+1 limit). | 526 vs 1298 |
| A3 | A filesystem without capacity reports 0 (unknown). A retry that asks for 1 GiB is ALREADY_EXISTS (`compatible`: have = 0), though an unlimited filesystem satisfies it. | 1056-1057 vs 808 |
| A4 | A block volume without capacity is refused. | 856 "MAY choose" |
| A5 | A ListVolumes token names the last id. Once that volume is deleted the token is ABORTED, and the walk restarts. csi-test's disabled `XIt` (controller.go:262) would fail. | 1566-1567 vs 1636 |
| A6 | Validate confirms, and echoes, a `volume_context` that is not the volume's. | 1492 (a CO MUST, the plugin may check) |
| A7 | GetCapacity ignores capabilities and parameters, so it reports full capacity for MULTI_NODE/ext4/unknown-param requests that CreateVolume refuses. | 1974-1977 |
| A8 | A retry for an existing volume whose source snapshot has since been deleted → OK. | 808 vs 1295 |
| A9 | Expand to a limit below the current size → OK with a capacity above the limit. | 2472 vs 1047-1050 |
| A10 | An all-zero capacity_range is read as unspecified. | 1040 vs 1043 |

Simulator-fidelity caveats, not verified on a pool:
- The sim does not refuse a refquota below usage (E6).
- The sim accepts a volsize of 2^63 (required = MaxInt64 rounds up to it). Real zfs would refuse it, and the CSI layer reports it clamped to MaxInt64.

## 4. NFS gap list and acceptance tests

Each test is in `nfs_acceptance_test.go` and fails at HEAD. A refusal is asserted twice: by its code, **and** by the message not saying "unsupported parameter". At HEAD every `nfs.*` key is refused as unknown, so without the second assertion the negative tests would pass vacuously. The fixture runs the endpoint with `FileExport: true` and the simulator as etab and nfsd. The advertised server list has a single `TODO(nfs-csi)` hook, because neither `endpoint.Config` nor `GetCapabilitiesResponse` has `nfs_servers` yet (§4 proto).

| test | requirement (csi-nfs-design.md / ADR 0004) | what the CSI layer lacks today |
|---|---|---|
| N1 ParamsAccepted | §3: `nfs.clients`, `nfs.server` are SC parameters | `parseParams` refuses them |
| N2 ClientsValidated | §2/§3: comma-separated IPv4 CIDRs with explicit `/N`, host bits zero, ≥ floor (/16), no overlap, never masked. 13 bad values: no `/N`, host bits, /8, /0, overlap, hostname, `*`, IPv6, `:` and option smuggling, empty entry, inet_aton spelling | No parser. `endpoint.ParseClient` is close but **accepts a bare IPv4 as /32** and does not check the floor or overlap, so it is not the grammar |
| N3 ServerSelection | §3: `nfs.server` ∈ advertised list, default first; otherwise INVALID_ARGUMENT | no `nfs_servers` in proto/Config; no selection |
| N4 VolumeContext | §5, Fact G: `server` + `share` (= mountpoint); never `subdir`/`mountOptions`/`mountPermissions`/`uid`/`gid` | `volumeOut` emits only dataset/kind/devicePath/mountpoint |
| N5 ShareNFSCanonicalAndServed | §2-§4: `sharenfs=rw=<sorted, :-joined>` at create (`ro=` iff every mode is *_READER_ONLY); mountpoint pinned locally; etab serves every client before OK | `pb.CreateVolumeRequest` has no `nfs_export`; plan.go sets `sharenfs=off` at create |
| N6 NFSDDownNotCommitted | §4 step 2-3, §8 C3: nfsd down → UNAVAILABLE, not committed (not listed); the retry succeeds | no verify-before-commit on create |
| N7 BlockRefused | §3, ADR 0004: block + NFS → INVALID_ARGUMENT | no notion of NFS volumes |
| N8 MultiNodeModes | §7: MULTI_NODE_* for NFS filesystems | `modeSupported` allows only SINGLE_NODE_* |
| N9 Idempotency | §6: same set in any order → OK; other clients or other mode → ALREADY_EXISTS; `nfs.server` not compared, and the response carries the new value | compatibility knows nothing of the export |
| N10 Validate | §7: confirmed iff filesystem, mount with fs_type ""/"nfs", no writer mode on a ro-only value, a given context's `share` = mountpoint and `server` advertised; otherwise a Message | `capSupported` refuses fs_type nfs and MULTI_NODE, and never checks volume_context (A6) |
| N11 RootOwnershipParams | ADR 0004 "root squash stays on", §13 Q1(a): `nfs.uid`/`nfs.gid`/`nfs.mode`, validated, applied before commit | nothing (no endpoint field; the sim has no file ownership, so the test checks parameter handling only) |
| N12 DeleteUnexports | §7, Fact E: delete needs no explicit unshare, and the path is gone from etab afterwards | blocked by N1 (zfs destroy already unshares) |
| N13 CloneTakesRequestClients | §4: a clone or restore takes the request's clients, never the source's | as N5, for `zfs clone -o` |
| N14 ListVolumesSameContext | spec:1082-1083: ListVolumes reports the create-time context, `server` included | **design conflict**, see below |
| N15 ClientsRequired | §3: `nfs.clients` is required in static mode, so nfs.server or uid without it → INVALID_ARGUMENT | none |

**Design conflicts the tests expose. The owner should settle these before the build.**
1. **§6 against the spec.** §6 says a changed `nfs.server` "is not compared … the response carries the new value". spec:1082-1083 says a volume "SHALL always report the same volume_context". ListVolumes (N14), and any context check in Validate, also need `server`, which §6 declares not to be volume state. N9's last assertion encodes §6 as written, and N14 encodes the spec. One of them has to give. The options:
   - (a) derive `server` deterministically, e.g. always the first advertised address, and drop per-SC `nfs.server`;
   - (b) record the chosen server as a local native-style user property. That is state of our own, but not a client list, so ADR 0004's wording permits it;
   - (c) leave `server` out of ListVolumes and accept the spec deviation.
2. **Whether an NFS-mode driver may still make plain (non-NFS) filesystem volumes.** §3 makes `nfs.clients` "required" in static mode, but nothing says whether a mount volume with no NFS parameters is refused. N15 tests only "other NFS parameters without clients".
3. **ParseClient against the §2 grammar.** File export (Publish) accepts a bare `10.0.0.5`, while §2 requires `/N` everywhere. Pick one parser for both.

Further gaps that are not tests, because they are deployment or proto-only work:
- `GetCapabilitiesResponse.nfs_servers` and `nfs_min_prefix` (§4);
- the CSIDriver object: `attachRequired: false`, `fsGroupPolicy: None` (§7);
- removal of the `<prefix>:nfs_clients` property, with reconcile's D1-D7 drift reporting (§1, §10);
- refusing `nfs.clients` in per-node mode (§12).
