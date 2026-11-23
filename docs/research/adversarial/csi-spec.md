# Adversarial check: endpoint.go against CSI spec

Evidence: spec clone at scratchpad/repos/spec (spec.md, csi.proto); GitHits pins external-provisioner@ee7abf31, external-snapshotter@44c047ad, csi-test@95f23ec4.

## Verdicts (BROKEN first)

**S6 BROKEN (incomplete compatibility rule).** spec.md:1296 says ALREADY_EXISTS applies when an existing volume is incompatible with `capacity_range`, `volume_capabilities`, `parameters`, `accessibility_requirements` or `volume_content_source`. endpoint.go:390-421 (`adopt`) checks managed/name (393-397), kind (399-401), content source (402-410), capacity min (411-413) and limit (414-416). Missing: `parameters` (Sparse/Reserve are not compared, so a thin volume is accepted for a thick request), access mode and fs type within `volume_capabilities`, and `accessibility_requirements` (not present in CreateVolumeRequest). csi-test only checks different capacity (pkg/sanity/controller.go:431); parameter/accessibility conflicts are not verified there.

**S1 PARTIAL.** Spec holds: spec.md:1313 says the volume MUST NOT be altered and must return FAILED_PRECONDITION; spec.md:1340 is the table row. endpoint.go:474-478 matches. But spec.md:1313 also says plugins SHOULD treat volumes independent of snapshots (spec.md:~1305 "CSI plugins SHOULD treat volumes independent"); the endpoint always refuses, a deviation from a SHOULD. PVC delete: external-provisioner delete errors requeue with exponential backoff capped by `--retry-interval-max` (5m) with no attempt limit (cmd/csi-provisioner/csi-provisioner.go:83-84; DeleteVolume error returned at controller.go:1452). So the PV retries indefinitely until snapshots are removed.

**S5 PARTIAL.** ReadyToUse=true is allowed (spec.md:2157). creation_time is set from `creation` read via `zfs get -Hp` (zfs.go:133, endpoint.go:660-662); csi-test asserts it is non-zero (pkg/sanity/controller.go:89). size_bytes is optional and 0 means unknown (spec.md:2235). endpoint.go:664-668 takes volsize (zvol) or refquota (filesystem); for filesystem snapshots this may be 0 (unverified). csi-test does not assert size (controller.go:85-89).

**S7 PARTIAL.** external-provisioner treats both ABORTED and DEADLINE_EXCEEDED as ProvisioningInBackground, i.e. retry (controller.go:2063-2067). Sidecar timeout default is 10s (csi-provisioner.go:89). endpoint.go:83-84 defaults LockWait to 30s, longer than the sidecar deadline, so the client sees DEADLINE_EXCEEDED and never ABORTED. Waiting gives no classification benefit; it holds a worker up to 10s. Set LockWait below the sidecar timeout, or fail fast.

**S2 HOLDS (nit).** spec.md:2303 and csi-test pkg/sanity/controller.go:1388-1403 expect ALREADY_EXISTS for the same name on a different volume. endpoint.go:613-624 scans globally. Nit: the loop returns on the first same-ID match (621-622) before checking later same-name entries; only reachable with pre-existing duplicates.

**S3 HOLDS.** spec.md (DeleteSnapshot): non-existent snapshot MUST return 0 OK. endpoint.go:722-730 returns nil for malformed IDs.

**S4 HOLDS.** spec.md:1636 and spec.md:2419 specify ABORTED. endpoint.go:589-591 and 713-715. csi-test pkg/sanity/controller.go:201-208 asserts ABORTED for ListVolumes. ListSnapshots sanity not verified.
