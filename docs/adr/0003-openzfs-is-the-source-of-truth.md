# 3. OpenZFS is the source of truth; democratic-csi is one adoption target

Status: accepted · Date: 2026-10-27 · Supersedes: the README's "democratic-csi is the behavioral baseline".

## Context

- The project started as a review of a democratic-csi port. It then took democratic-csi as the *behavioral baseline*: when a question had no obvious answer, the endpoint did what democratic-csi did.
- democratic-csi is itself a projection of ZFS through one driver's history. Several of its behaviors are not ZFS semantics but choices, and some of them contradict ZFS's own model:
  - DeleteVolume runs `zfs destroy -r` after checking only CSI-owned snapshots, so it destroys snapshots other tools took (sanoid, zfs-auto-snapshot, replication).
  - Its success marker is written but never read.
  - Its ids don't enforce the CSI 128-byte limit.
- The owner directed that loyalty belongs to OpenZFS itself.

## Decision

When a behavior question comes up, **OpenZFS decides**: its CLI and libzfs semantics, its ownership model, its property conventions, and what `zfs(8)` actually prints, as verified in the OpenZFS source and on a real pool (the 2.4.x CI VM).

- **democratic-csi is an adoption target, not an arbiter.**
  - Reading its datasets, properties and ids so existing volumes can be adopted stays supported, as an explicit compatibility mode.
  - Where it diverges from OpenZFS semantics, the endpoint does not copy it.
- **Never touch what you don't own.** Ownership is OpenZFS's own: user properties set locally. Anything else under the parent belongs to someone else, such as foreign snapshots, holds or received datasets. The endpoint refuses rather than destroys.
- **Property namespace.** Follow the OpenZFS user-property convention `module:property` with a namespace of our own. The democratic-csi prefix is only what the adoption mode reads.
- **Evidence order:**
  1. OpenZFS source and man pages;
  2. behavior on a real pool;
  3. other implementations, including democratic-csi, TrueNAS middleware and zfs-localpv. These are evidence of practice, never of correctness.

## Consequences

- Earlier decisions justified only by "baseline parity" are reopened, each as its own change:
  - the default `-property-prefix`, today `democratic-csi`;
  - DeleteVolume's `destroy -r` over foreign snapshots;
  - received-property ownership. `zfs.Prop.Local()` counts `received` as local.

  All three are done: the default prefix is `zfs-endpoint` (`democratic-csi` is the adoption mode), DeleteVolume refuses foreign snapshots unless `-destroy-foreign-snapshots` is set, and `Local()` is `local` only.
- Existing decisions grounded in OpenZFS stand, because they were already loyal to it:
  - only a classified not-found counts as absent;
  - zvol reservations are left to ZFS (`refreservation=auto`);
  - snapshots are deleted with `destroy -d`;
  - volumes are never created with `create -p`.
- Docs say "adoption target" where they said "baseline".

## Alternatives considered

- **Keep democratic-csi as the baseline.** Rejected. It imports that driver's accidents, such as destroying foreign snapshots, as requirements.
- **Drop democratic-csi compatibility entirely.** Rejected for now. Adopting existing volumes is cheap, because it means reading properties, and it is valuable for migration.
