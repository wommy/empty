# 2. Snapshot ids that do not cap volume id length

Status: proposed · Date: 2026-10-27 · Related: ADR 0001 (where the scope check runs).

## Context

- A volume id is its dataset component under the parent, up to 128 bytes (`volumeID`, `endpoint.go:145`).
- A snapshot id is the snapshot's parent-relative zfs name, `<volume-id>@<component>`. `snapComponent` (`plan.go:380`) picks the component:
  - the CSI name, when it is a valid zfs component and `vol@name` fits in 128 bytes;
  - otherwise `csi-` plus 32 hex characters of SHA-256(name), 36 bytes in all.
- `PlanCreateSnapshot` rejects an id over 128 bytes (`plan.go:347-351`). So any volume id over 91 bytes cannot be snapshotted, although it can be created.
- The CSI spec (v1.14.0) sets no special limit for `snapshot_id`. The general string limit applies: "128 bytes … SHALL NOT exceed" (spec.md:486-495). The id "MUST contain enough information to uniquely identify this specific snapshot" (spec.md:2237-2246).
- The spec says nothing about lookup tables. "Usable without a lookup table if possible" is our own goal. Today's id parses into a volume, so DeleteSnapshot scans only that one volume (`endpoint.go:497`), and CreateVolume locks `vol:<source volume>` before it scans (`endpoint.go:264-280`).
- **Baseline ids.** democratic-csi issues `<vol>@<cleansed name>` for attached snapshots and `<vol>/<name>` for detached ones (`democratic-csi@69c7ebc src/driver/controller-zfs/index.js:2244-2257`, `:2460-2463`, `:2495`). It does not enforce 128 bytes.
- The endpoint already adopts democratic-csi snapshots. It reads `csi_snapshot_name` and `csi_snapshot_source_volume_id` as locally set properties (`table.go:122-160`).
- `zfs.Prop.Local()` treats `received` as local (`zfs.go:43`). A `zfs send -p | zfs recv` copy of a snapshot therefore also looks managed. (Since fixed: `Local()` is `local` only, per ADR 0003. The ids below still bind the volume.)

## Decision

**Two id forms, chosen by a pure, deterministic rule. No stored id and no new property.**

```
id(vol, name) =
  short := vol + "@" + snapComponent(vol, name)       // unchanged
  if len(short) <= 128: return short                  // every id issued so far
  return "zs1-" + hex(SHA-256("zfs-endpoint/snapshot/v1\x00" + vol + "\x00" + name))   // 68 bytes
```

- **Disjoint forms.** A long id never contains `@` or `/`, and a short id always contains `@`. Parsing the form is a byte test:
  - contains `@`: short;
  - `zs1-` followed by 64 lowercase hex characters: long;
  - anything else: an unknown id, handled as today (DeleteSnapshot OK, CreateVolume `NOT_FOUND`).
- **Hash inputs.**
  - `vol` is the volume id **taken from the snapshot's zfs name**, not from the `csi_snapshot_source_volume_id` property. A received copy under another volume therefore gets a different id.
  - `name` is the `csi_snapshot_name` property, not the zfs component. A rename of the snapshot component keeps the id.
  - The domain-separation prefix and the `v1` tag allow a future `zs2-`.
- **Computed without a table.** CreateSnapshot computes the id from the request alone and returns it before any read-back.
- **Resolving a long id costs one full-parent scan.** For each managed, non-deferred snapshot row whose short form exceeds 128 bytes, compute `id(row)` and match. Only those rows need hashing, and that work is microseconds against the millisecond-scale `zfs` exec.
  - Exactly one match: resolved.
  - No match: absent.
  - More than one match: `FAILED_PRECONDITION` ("ambiguous snapshot id"). The planner never picks one.
- **Lock order for long ids** (DeleteSnapshot, and CreateVolume from a snapshot):
  1. Full scan, then resolve to `vol@component`.
  2. Lock `vol:<vol>` and `snap:<id>`.
  3. Scoped rescan of `<vol>`.
  4. Re-resolve, and require the same zfs name; otherwise `ABORTED`, which the sidecar retries.
  5. Plan.

  Short ids keep today's single scoped scan.
- **One function decides every outgoing id.** `snapshotView` calls `id()` instead of returning `r.ID` (`endpoint.go:237-241`). The same function serves ListSnapshots filtering, CreateSnapshot's idempotency check (`plan.go:353-358`) and the CreateVolume content-source comparison (`row.SourceID == srcID`, `plan.go:136`).
- **Liberal input for legacy ids.** An input that names an existing managed snapshot by its exact parent-relative zfs name resolves, even if it is over 128 bytes. This covers democratic-csi snapshots whose `vol@name` exceeded 128 bytes and were issued anyway. Outputs are always the canonical form.
- **Detached snapshots.** democratic-csi `<vol>/<name>` ids stay unsupported: they are unknown ids, as today, and not part of this decision.
- **Second length bound.** The full zfs name must still be under 256 bytes. `PlanCreateSnapshot` adds `zfs.ValidSnapshot(p.full(vol + "@" + component))` and returns `INVALID_ARGUMENT` before planning. With a 128-byte volume id and a 36-byte hashed component, the parent name may be at most 89 bytes.

## Consequences

- Volume ids from 92 to 128 bytes can be snapshotted. The 91-byte cap is gone, and the only remaining bound is the zfs 255-byte name limit.
- **Backward compatible by construction.** Every id the endpoint has issued is at most 128 bytes, so `id()` returns it unchanged. The same holds for every democratic-csi id that fits CSI's limit. Clones that recorded such an id in `csi_volume_content_source_id` still compare equal.
- Long ids lose parse-to-volume:
  - DeleteSnapshot and CreateVolume-from-snapshot with a long id pay one extra full-parent `zfs get`. CreateSnapshot and ListSnapshots already do a full scan (`endpoint.go:439`, `:466`).
  - The ADR 0001 scope check cannot run in an interceptor for these ids. It must run after resolution in the endpoint, which ADR 0001 already requires.
- `SourceVolumeID` in responses still comes from the zfs name, so it is always correct even when the id is opaque.
- **Rename behaviour.** A rename of the snapshot component keeps long ids, but breaks short ids, as today. A volume rename changes both forms, but it also changes the volume id, which the endpoint does not support out of band.

## Alternatives considered

| Alternative | Why rejected |
|---|---|
| **A minted opaque id** (random UUID) stored as a new user property and set atomically with `zfs snapshot -o` | Same scan cost as the hash, plus new state. The id cannot be recomputed if the property is lost or not sent (`zfs send` without `-p`). Received copies carry the same id, so ambiguity is guaranteed rather than avoided. Idempotent CreateSnapshot needs a read-back before it can answer. |
| **One hashed form for all snapshots**, short or not | It changes every existing id, which breaks COs holding `VolumeSnapshotContent` handles and clones that recorded a source id. It would need a dual-read migration for no gain. |
| **A hash of the full zfs name** (`vol@component`) | It is not stable when the snapshot component is renamed, and it gives no advantage over hashing `(vol, name)`. Both are computable from the request. |
| **Reversible encoding or compression of `vol` into the id** | A 128-byte volume id plus any snapshot discriminator cannot fit 128 bytes in general, because arbitrary bytes are incompressible. A partial prefix of `vol` cannot scope `zfs list`, which needs an exact dataset. |
| **A hash of the CSI name only** (names are unique per endpoint) | Uniqueness is enforced only under the endpoint's lock. Received copies and out-of-band duplicates share the name, and binding `vol` makes those ids distinct. |
| **Capping volume ids at 91 bytes** | It moves the failure to CreateVolume and breaks the csi-sanity max-length volume name test (`controller.go:459`, `docs/research/gaps/csi-frontend.md`). |
| **An in-memory id-to-name cache** | It is extra state that must be rebuilt after a restart and invalidated on out-of-band changes. The scan already happens, so the cache saves at most one `zfs get` on a rare path. |

## Evidence

- **This repo.**
  - `internal/endpoint/plan.go:336-386`: the 128 check and `snapComponent`.
  - `plan.go:388-402`: PlanDeleteSnapshot.
  - `endpoint.go:145-165`: `volumeID` and `parseSnapshotID`.
  - `endpoint.go:237-252`: `snapshotView` returns `r.ID`.
  - `endpoint.go:262-312`: lock keys and `scanFor` derive the volume from the id.
  - `endpoint.go:432-500`: Create, List and DeleteSnapshot scans.
  - `internal/zfs/zfs.go:43`: received counts as local.
  - `zfs.go:76`: `ValidSnapshot` is under 256 bytes.
- **CSI spec v1.14.0** (`scratchpad/repos/spec`): size limits at spec.md:486-495, `snapshot_id` semantics at spec.md:2237-2246, CreateSnapshotRequest.name at spec.md:2177-2181. No "lookup table" wording exists in the spec; that goal is ours.
- **democratic-csi@69c7ebc** `src/driver/controller-zfs/index.js`:
  - name cleanse at `:2244-2257`;
  - properties set at `:2237-2240`;
  - `snapshot_id` is the parent-relative zfs name at `:2460-2463`;
  - detached snapshots are detected by the absence of `@` at `:2495`.
- **csi-test** `pkg/sanity/controller.go:1405-1419` (`MaxNameLength = 128` at `:42`): max-length snapshot names. See `docs/research/gaps/adv-csi-frontend.md` B1.

## Known risks (self-review)

- **A legacy id over 128 bytes.** If a democratic-csi snapshot was issued one, the CO keeps using the old string, which still resolves through liberal input. ListSnapshots, however, reports the `zs1-` form. A CO that reconciles by listed id would see a "new" snapshot. Kubernetes' external-snapshotter does not list for reconciliation by default; this is unverified for other COs.
- **The long-id lock order is optimistic.** It resolves, locks, rescans and verifies. A concurrent delete and re-create of the same name on the same volume between the scan and the lock gives the same id and the same zfs name, so the verify passes. That is correct, because it is the same CSI snapshot.
- **The full scan grows with the parent.** For a long-id DeleteSnapshot under a parent with tens of thousands of datasets, it can approach the 10 s sidecar timeout. The lock clamp returns `ABORTED`, but the scan itself is not clamped. Measure it in the real-ZFS CI job before relying on it at scale.
- **SHA-256 at full 256 bits makes accidental collisions moot.** The ambiguity path exists for received copies and duplicates, not for hash collisions.
- **The `zs1-` prefix is a namespace claim.** A volume named `zs1-…` is fine, because volume ids are a separate namespace from snapshot ids. A future id form must not reuse `zs1-`.
- **Not decided here.** Internal clone-source snapshots (`@volume-source-for-volume-<id>`) still hit the 255-byte limit for long source and target ids (`plan.go:188-191`). They are not CSI ids, but the same hashing could lift that cap later.
