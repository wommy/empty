# wave13: two NFS decisions before the build (research + adversary)

Read-only. Repo HEAD c73e40d, branch claude/gallant-johnson-1qaxp5. Governing: ADR 0003, ADR 0004, `docs/research/gaps/csi-nfs-design.md` (cited "design §N"), wave9 `csi-diff.md` §4 (N1-N15), wave9 `nfs-diff.md` §5 (always /N).

Sources (pinned commits; local clones in `scratchpad/repos/` and `scratchpad/wave13/dl/`):

| source | commit | used for |
|---|---|---|
| CSI spec v1.13.0 (go.mod pin; copy `wave9/spec-v1.13.md`) | tag v1.13.0 | volume_context rules |
| kubernetes-csi/external-provisioner | ee7abf31 | how volume_context becomes PV attributes |
| kubernetes/kubernetes | dd410c48 | PV source immutability |
| kubernetes-csi/external-health-monitor | b61c6b1 | the only k8s consumer of ListVolumes |
| kubernetes-csi/csi-driver-nfs | 30ac267 | NodePublish keys, id format, IPv6 |
| democratic-csi | 69c7ebc | practice: recorded context property |
| truenas/truenas-csi | 6443642 | practice: driver-wide server, no list context |
| openebs/zfs-localpv | 8759e50 | practice: context from recorded CR state |

## 0. Facts

| # | Fact | Evidence |
|---|---|---|
| S1 | volume_context "SHALL NOT be mutable", "SHALL be safe for the CO to cache", and "A volume uniquely identified by volume_id SHALL always report the same volume_context". | spec.md v1.13.0 L1075-1076, L1082-1083 |
| S2 | An identical CreateVolume retry MUST return the corresponding response; compatibility with `parameters` is the plugin's call. | L807-808 |
| S3 | A volume_context passed to Validate / ControllerPublish / NodePublish "MUST match the volume_context of the volume". | L1386-1387, L1491-1492, L2636, L2815 |
| K1 | external-provisioner copies `rep.Volume.VolumeContext` into `PV.spec.csi.volumeAttributes` **once**, at provisioning, **plus its own key** `storage.kubernetes.io/csiProvisionerIdentity`. | external-provisioner `pkg/controller/controller.go` L315, L915-917, L962-963 |
| K2 | `spec.persistentvolumesource` (which holds `csi.volumeAttributes`) is **immutable after creation**. No component can refresh it; a new value needs a new PV. | kubernetes `pkg/apis/core/validation/validation.go` L2316-2320 |
| K3 | No Kubernetes component reads ListVolumes' volume_context. external-provisioner never calls ListVolumes. external-health-monitor calls it and keys the result by volume handle only. | grep of the provisioner (no call); health-monitor `pkg/csi-handler/pv_checker.go` L116-161 |
| N1 | csi-driver-nfs NodePublish reads `server`, `share` (both required, else InvalidArgument), `subdir`, `mountoptions`, `mountpermissions`, `uid`, `gid` and the pvc/pv-name keys (used only for subdir templating). Other keys, including K1's identity key, are ignored. The mount source is `server:share`, built once per publish from the PV's attributes. | `pkg/nfs/nodeserver.go` L85-140; `nfs.go` L85 |
| N2 | csi-driver-nfs puts an IPv6 `server` in brackets itself. | `pkg/nfs/utils.go` L279-284 |
| N3 | **The reference NFS driver treats the server as identity.** Its own volume id is `server#baseDir#subDir#uuid[#onDelete]`. | `pkg/nfs/controllerserver.go` L915-925, L946-960 |
| P1 | democratic-csi records the whole share context in the user property `democratic-csi:csi_share_volume_context`. ListVolumes reads it and skips a volume without it. On every CreateVolume, retries included, it re-computes the context from the current `shareHost` and overwrites the property, so its "latest wins". | `src/driver/controller-zfs/index.js` L19-20, L362-371, L1295-1298; `controller-zfs-generic/index.js` L144-150 |
| P2 | truenas-csi has one driver-wide `NFSServer`, derived from the API URL host by default. Rebuilt volumes take the **current** value. ListVolumes returns **no volume_context at all**: a quiet S1 deviation. | `pkg/driver/driver.go` L350-355; `controller.go` L588-591, L1377-1380, L1842-1846 |
| P3 | zfs-localpv builds the context from its recorded ZFSVolume CR (pool name). ListVolumes is Unimplemented. | `pkg/driver/controller.go` L623-636, L1221-1226 |
| R1 | The endpoint has no `nfs_servers` yet, and the controller has no NFS mode or flag. `zfs-csi` has only `-endpoint`, `-csi-address`, `-driver-name`. | `cmd/zfs-csi/main.go` L36-38; `api/zfsendpoint/v1/endpoint.proto` |

## Q1. Which NFS server address goes in a volume's volume_context?

### What the evidence settles before any option is chosen

- **What the node mounts is fixed per PV, forever** (K1+K2). Whatever CreateVolume last returned before the PV was written is what csi-driver-nfs mounts for the PV's whole life (N1). Changing `-nfs-server`, reordering it, or editing an SC never moves an existing PV. **No option makes a readdress transparent**; only a stable name (a VIP or DNS name) does. That is an admin rule, independent of the choice here.
- **So the real question is narrower: what should the plugin *report* afterwards** (ListVolumes N14, identical retries S2, Validate S3), and can it do that without lying about what PVs hold?
- **Design §6 is wrong on the spec**, and the anomaly is weighed, not waved through. "nfs.server … is not volume state" contradicts S1 (L1075 "SHALL NOT be mutable") and N3 (the reference driver puts the server *in the id*). It is harmless in Kubernetes only by luck: PVs are written once (K2), and nobody reads list context (K3). The rationale has to go, whatever the option.
- **Practical cost of a deviation ≈ nil in Kubernetes (K3)**, so this is a correctness and operability question, not an outage one. That favours the cheapest option that is *truthful*.

### Options attacked

| attack | (a) record per volume in a local user property | (b) always the first advertised address | (c) leave `server` out of ListVolumes |
|---|---|---|---|
| S1/N14: same context forever | **holds**: read back from the volume | breaks whenever `-nfs-server` is reordered or edited | breaks by construction (create has it, list doesn't) |
| S2/N9: identical retry after a flag reorder | same server | **different server on an identical retry**, a MUST violation (L807-808 + L1082) | create path same as (b) or (a) |
| Server failover / VIP | VIP recorded once, stable | same, if the VIP is first | same |
| Address change on the NAS | PVs keep the old address (K2). Reconcile can report "volume's server no longer advertised" (new D8) and name the PVs to re-create. | ListVolumes silently reports the *new* address while every PV mounts the old one. Drift becomes invisible. | nothing to compare |
| Multi-homed host, per-SC storage network (design §9.5) | kept: `nfs.server` per SC | **lost**: one address for every SC, so §3/§9.5 are removed | kept, but unreported |
| k8s node IP churn | irrelevant: affects clients (sharenfs), not the server | irrelevant | irrelevant |
| IPv6 later | the raw literal is stored; csi-driver-nfs brackets it (N2) | same | same |
| Migrating PVs between endpoints (`zfs send -p`) | the property arrives with source *received*, so per ADR 0003 (`Local()` = local only) it is not ours. It is ignored and reported, the same as the managed marker. A migrated volume needs re-adoption plus new static PVs anyway (driver and handle are immutable, K2). | reports the *destination's* address while the PV still mounts the source's | n/a |
| Parent sets the property (`zfs set <prefix>:nfs_server=x tank/vols`) | user properties inherit, so it must be read **local-only**; an inherited value is ignored and reported | n/a | n/a |
| Upgrade from `nfs_clients` / pre-NFS volumes | a volume without the property reports no `server` (and no `share`), which is exactly what it was created with. S1 holds for legacy volumes too. | **invents** a `server` for volumes created without one, an S1 violation on every legacy volume | n/a |
| Static PVs | unaffected: the admin's attributes are what mounts; the plugin is not asked | unaffected | unaffected |
| PV immutable after binding | consistent: the plugin's record equals the PV's attribute | inconsistent after any config change | inconsistent |
| Crash safety | set in the same `zfs create`/`clone -o` nvlist as sharenfs and the managed marker, so no new crash point (design §8 C1-C5 cover it) | none needed | none needed |
| ADR 0004 "export state lives only in sharenfs" | the server address is **not export state**. sharenfs authorizes client *source* addresses; nfsd answers on every interface (design §9.5). The address has **no OpenZFS home at all**, so the only OpenZFS place for it is a user property: the rule "derive what OpenZFS knows, record what it doesn't" | n/a | n/a |
| Practice | democratic-csi (P1, overwrite on retry); zfs-localpv records context state in a CR (P3) | truenas-csi uses a single driver-wide address (P2) | truenas-csi omits list context (P2) |

**Better than all three? Considered and rejected:**
- (d) Encode the server in the volume id, as csi-driver-nfs does (N3). That is **irreversible**: ids live in PVs forever, and it collides with ADR 0002's 128-byte id work.
- (e) Record the whole context JSON, as democratic-csi does. That duplicates OpenZFS facts (`share` = mountpoint, which the design pins locally) and creates a second truth for them.
- (f) Have the node side derive the server. Impossible: csi-driver-nfs requires `server` in volume_context (N1).

### Recommendation: (a), sharpened

1. **Property.** `<prefix>:nfs_server`, set **only** with `zfs create`/`zfs clone -o` alongside sharenfs, and **read local-only**. It is a single address: no list, no JSON.
2. **volume_context.** `server` = that property; `share` = the mountpoint as OpenZFS reports it now. Each key is present iff its source is present.
3. **CreateVolume retry** (design §6, replacing its last bullet):
   - **Uncommitted:** converge `nfs_server` to the request, like sharenfs.
   - **Committed, `nfs.server` given and different from the recorded one:** ALREADY_EXISTS, naming the recorded value.
   - **Committed, `nfs.server` omitted:** OK with the recorded value, even if the advertised list has been reordered since.
   - A recorded server that is no longer advertised is still returned. The PV holds it, and lying helps nobody.
4. **ValidateVolumeCapabilities:**
   - Compare the given context **per key**, never the whole map: K1 injects `storage.kubernetes.io/csiProvisionerIdentity`.
   - Return a Message when `server` ≠ the recorded value, when `share` ≠ the current mountpoint, or when the recorded server is no longer advertised.
5. **Reconcile:** report-only drift `D8 server-unadvertised`; also report an inherited or received `nfs_server`.
6. **Endpoint `-nfs-server` docs:** "Advertise stable names (a VIP or DNS name). An address already in PVs can never be changed for them (K2). Removing it from the list does not move them."

### Verdict Q1: **DECISIVE+REVERSIBLE** (the coordinator records it in an ADR)

- **Decisive.** The spec MUST (S1, S2), PV immutability (K2) and the reference driver's own model (N3) all point to "the server is per-volume and immutable". (a) is the only option that satisfies S1, S2 and N14 while keeping the per-SC `nfs.server` that the accepted design (§3, §9.5) already specifies. (b) removes accepted surface. (c) is a known violation with no offsetting benefit.
- **Reversible.** It only adds one user property and a read. Dropping it later means "stop writing, stop reading". The value lives on harmlessly, and PVs are unaffected (K2). Nothing outward changes: the volume_context keys `server` and `share` are already decided by ADR 0004.
- **Not owner-worthy, with one caveat.** The ADR must state explicitly that `<prefix>:nfs_server` is CSI addressing metadata, not export state, which keeps ADR 0004's "nowhere else" intact. **If the coordinator reads ADR 0004 / the owner's "OpenZFS as source of truth" as forbidding *any* recorded CSI metadata**, then it becomes ASK-OWNER, and the question would be:
  > "A volume's NFS server address can't be derived from ZFS, yet CSI and Kubernetes treat it as fixed per volume (the PV keeps it forever). Options: (1, recommended) store it once per volume in a local user property `<prefix>:nfs_server`, read back by ListVolumes and retries; (2) allow one server address per endpoint and drop the per-StorageClass `nfs.server`; (3) keep per-SC choice and accept that ListVolumes reports no server (as truenas-csi does)."

  The existing markers (`managed`, `provision_success`, clone-source stamps) are already recorded state of exactly this kind, so I expect the caveat not to trigger.

## Q2. In NFS mode, may the driver create plain filesystem volumes with no NFS parameters?

### Evidence

- With ADR 0004's node side, **every** volume under our driver name is published by csi-driver-nfs. A volume without `server`/`share` fails NodePublish with InvalidArgument "server is a required parameter" (N1, nodeserver.go L127-131).
- A volume returned **with** `server`/`share` but `sharenfs=off` binds, then fails at mount with "access denied by server". Either way the failure is **late and opaque**: the PVC binds, and the pod hangs in ContainerCreating.
- Design §12 already forbids static and per-node volumes on one driver ("Static and per-node never mix on one driver", the flag being driver-wide because `attachRequired` is per CSIDriver). An unexported volume in a static driver is exactly a per-node-shaped volume in a static driver: the mix §12 bans.
- The legitimate "export later" use case *is* per-node mode, which comes later and behind its own flag. There, the absence of `nfs.clients` is the norm and its presence is refused.
- R1: "NFS mode" does not exist as a switch yet. It has to be defined. Recommendation: NFS mode is on iff the endpoint advertises at least one `nfs_servers` entry (one controller per endpoint, per the socket design), so no new controller flag is needed. Without it, today's behaviour stays: mount volumes, no `nfs.*` parameters accepted.

### Options attacked

| attack | refuse INVALID_ARGUMENT | allow unexported |
|---|---|---|
| Fail fast | at provisioning, as a ProvisioningFailed event naming `nfs.clients`; SC parameters are immutable, so the fix is a corrected SC | binds, then fails at pod start on every node |
| N4 (`server` + `share` always) | holds | needs a carve-out |
| Validate | nothing new | must refuse every capability for an unexported volume, or confirm something unmountable |
| Clone / restore | refused the same way (N13: a clone takes the request's clients) | a clone of an exported volume would come out unexported |
| Per-node mode later | unaffected: that mode inverts the rule | blurs the §12 boundary, since every unexported static volume looks like an unpublished per-node one |
| Static PVs, adopted volumes | unaffected (no CreateVolume) | unaffected |
| Upgrade / pre-NFS volumes | they stay listed with their original context (with Q1(a): no `server`). They aren't mountable via csi-driver-nfs and need static PVs after export. Pre-release, so expect none. | same |
| csi-sanity in NFS mode | needs `TestVolumeParameters` with `nfs.clients` (supported by csi-test) | passes without parameters |
| Two consumers on one endpoint (NFS and non-NFS) | needs a second endpoint/parent or a second driver name; one controller per endpoint already holds | possible, at the cost above |

### Recommendation

**Refuse with INVALID_ARGUMENT** in static NFS mode:
- Every CreateVolume (mount access, any content source) without `nfs.clients` fails with `nfs.clients is required: this driver serves volumes over NFS`.
- The message must not read "unsupported parameter", per the N15 double assertion.

### Verdict Q2: **DECISIVE+REVERSIBLE**

- **Decisive.** The node side cannot mount such a volume (N1), and design §12 already bans the mix.
- **Reversible, in the safe direction.** Refusing now and allowing later is purely additive: SCs that work today keep working. Allowing now and refusing later would break existing StorageClasses, which is the outward-facing, irreversible direction. So the refusal is also the choice that keeps the decision open.

## Acceptance tests (N1-N15) that change

| test | change |
|---|---|
| N3 ServerSelection | also assert the chosen server is recorded at create (`-o <prefix>:nfs_server=<chosen>` in the same create as sharenfs) |
| N4 VolumeContext | `server` comes from the recorded property; a volume without it has no `server` key. Never `subdir`/`mountoptions`/`mountpermissions`/`uid`/`gid` (N1 also lists the pvc/pv-name keys: never emit them) |
| N5 ShareNFSCanonicalAndServed | the create property set includes `<prefix>:nfs_server` |
| N6 NFSDDownNotCommitted | the retry after Unavailable converges `nfs_server` like sharenfs (uncommitted) |
| **N9 Idempotency** | **last assertion inverted.** Committed + a different explicit `nfs.server` → ALREADY_EXISTS (not "OK carrying the new value"). New: committed + `nfs.server` omitted, after the endpoint restarts with a reordered `-nfs-server` → OK with the **recorded** server. Uncommitted + a different `nfs.server` → OK, converged |
| **N10 Validate** | `server` must equal the **recorded** value (not merely "advertised"). Compare per key; the extra key `storage.kubernetes.io/csiProvisionerIdentity` must be tolerated (K1). A recorded-but-unadvertised server → Message |
| N13 CloneTakesRequestClients | also: the clone records the request's server, never the source's |
| **N14 ListVolumesSameContext** | no longer a design conflict: it passes with Q1(a). Add: after the endpoint restarts with a changed or reordered `-nfs-server`, ListVolumes still reports each volume's create-time server. A pre-NFS volume reports no `server` |
| **N15 ClientsRequired** | widened (Q2): a mount CreateVolume with **no** NFS parameters at all → INVALID_ARGUMENT naming `nfs.clients`; also a clone or restore without `nfs.clients` |
| new N16 (suggested) | an inherited (parent-set) or received `<prefix>:nfs_server` is ignored for volume_context and reported by reconcile (D8 / foreign-source) |

N1, N2, N7, N8, N11 and N12 are unchanged.

```json
[
  {
    "question": "Which NFS server address goes in a volume's volume_context?",
    "verdict": "DECISIVE+REVERSIBLE",
    "recommend": "(a) Record the chosen server once per volume in a local user property <prefix>:nfs_server, set in the same zfs create/clone -o as sharenfs and read local-only. volume_context.server comes from it and share from the current mountpoint. On a committed volume, a different explicit nfs.server is ALREADY_EXISTS and an omitted one returns the recorded value. Validate compares per key and tolerates the provisioner's identity key. Reconcile reports a recorded server that is no longer advertised (D8). Design §6's 'not volume state' is retracted. Advertise only stable names (VIP or DNS), because PVs never change.",
    "irreversible": false,
    "evidence": [
      "https://github.com/container-storage-interface/spec/blob/v1.13.0/spec.md#L1074-L1085",
      "https://github.com/container-storage-interface/spec/blob/v1.13.0/spec.md#L807-L808",
      "https://github.com/kubernetes-csi/external-provisioner/blob/ee7abf31c50b49c205dfa151a236af4bc224d4cf/pkg/controller/controller.go#L915-L917",
      "https://github.com/kubernetes-csi/external-provisioner/blob/ee7abf31c50b49c205dfa151a236af4bc224d4cf/pkg/controller/controller.go#L962-L963",
      "https://github.com/kubernetes/kubernetes/blob/dd410c4864ad52093ab74be9f7aa39a8cbc4dc06/pkg/apis/core/validation/validation.go#L2316-L2320",
      "https://github.com/kubernetes-csi/external-health-monitor/blob/b61c6b1da30eaddac124d6c8544f91f7746feb8b/pkg/csi-handler/pv_checker.go#L116-L161",
      "https://github.com/kubernetes-csi/csi-driver-nfs/blob/30ac26785aa1c3f7039b7f9a70dff123a6fe9f6a/pkg/nfs/nodeserver.go#L85-L140",
      "https://github.com/kubernetes-csi/csi-driver-nfs/blob/30ac26785aa1c3f7039b7f9a70dff123a6fe9f6a/pkg/nfs/controllerserver.go#L915-L925",
      "https://github.com/kubernetes-csi/csi-driver-nfs/blob/30ac26785aa1c3f7039b7f9a70dff123a6fe9f6a/pkg/nfs/utils.go#L279-L284",
      "https://github.com/democratic-csi/democratic-csi/blob/69c7ebcd66fe8105e514e3e93e05c555b06d987a/src/driver/controller-zfs/index.js#L362-L371",
      "https://github.com/democratic-csi/democratic-csi/blob/69c7ebcd66fe8105e514e3e93e05c555b06d987a/src/driver/controller-zfs/index.js#L1295-L1298",
      "https://github.com/truenas/truenas-csi/blob/6443642bed8122afd83847c4b81ceb9f50fc1cf8/pkg/driver/controller.go#L1842-L1846",
      "https://github.com/truenas/truenas-csi/blob/6443642bed8122afd83847c4b81ceb9f50fc1cf8/pkg/driver/controller.go#L1377-L1380",
      "https://github.com/openebs/zfs-localpv/blob/8759e5071856db4b1b251d4fa294b0e6816cb7df/pkg/driver/controller.go#L623-L636"
    ]
  },
  {
    "question": "In NFS mode, may the driver still create plain filesystem volumes with no NFS parameters?",
    "verdict": "DECISIVE+REVERSIBLE",
    "recommend": "Refuse with INVALID_ARGUMENT ('nfs.clients is required'), for every mount CreateVolume including clones and restores, in static NFS mode. NFS mode is on iff the endpoint advertises nfs_servers. Per-node mode (later) inverts the rule. Refusing now is the reversible direction; allowing later is additive.",
    "irreversible": false,
    "evidence": [
      "https://github.com/kubernetes-csi/csi-driver-nfs/blob/30ac26785aa1c3f7039b7f9a70dff123a6fe9f6a/pkg/nfs/nodeserver.go#L127-L131",
      "https://github.com/kubernetes/kubernetes/blob/dd410c4864ad52093ab74be9f7aa39a8cbc4dc06/pkg/apis/core/validation/validation.go#L2316-L2320",
      "docs/research/gaps/csi-nfs-design.md §12 (static and per-node never mix on one driver)",
      "docs/adr/0004-support-floor-and-node-side.md (node side is csi-driver-nfs)"
    ]
  }
]
```
