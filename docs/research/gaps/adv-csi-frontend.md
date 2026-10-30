# Adversarial check: csi-frontend.md

Pin: github:kubernetes-csi/csi-test@95f23ec4 (pkg/sanity). Nothing modified.

## BROKEN

### B1. Snapshot max-length name (design section 1, plan.go:332)
- Test: `pkg/sanity/controller.go:1405-1419` "should succeed when creating snapshot with maximum-length name". Uses `UniqueStringWithLength(..., MaxNameLength)` with `MaxNameLength = 128` (`controller.go:42`), creates the snapshot twice (second is the idempotency call at :1418).
- Endpoint: `plan.go:332` rejects when `len(vol)+1+len(name) > 128`. Any volume id of 1+ bytes fails a 128-byte name, so the test fails with InvalidArgument.
- Verdict: design's own BROKEN item is correct. Fix as the design says: bound the name at 128 alone.

### B2. "Node service is run unconditionally" (design section 2)
- Verified true, and worse than stated: there is no csi-sanity flag that skips Node or Identity specs. `DescribeSanity` registrations are unconditional: `node.go:174` (Node Service), `identity.go:33` (Identity Service), `controller.go:112` (Controller Service [Controller Server]).
- Node `BeforeEach` (`node.go:287-324`) calls `GetPluginCapabilities` and `NodeGetCapabilities` on `sc.Conn`. `isNodeCapabilitySupported` (`node.go:36-50`) does `Expect(err).NotTo(HaveOccurred())`. On a controller-only socket that returns Unimplemented, so every Node spec fails in BeforeEach. The design's "unverified whether a missing Node client is a skip" is answered: it is a failure, not a skip.
- Only workable route: ginkgo focus or skip (ginkgo v2 standard flags, passed to the csi-sanity binary). Focus on the Controller describes, for example `-ginkgo.focus="Controller Server"` (matches `controller.go:112` and `:1327`). Identity specs (`identity.go:33`) must also be excluded, or they fail the same way. The design's "record the skip list in the test" is the same mechanism, not a config.

### B3. Proposed `map<string,string> properties = 9` is default-allow in practice (section 5/6)
- The proto comment lists an allowlist (compression, recordsize, volblocksize, copies, sync, logbias, atime), but the design never states default-deny, value validation, or per-kind rules. As written, a tenant can pass arbitrary ZFS keys. Examples that matter:
  - `mountpoint`, `canmount`, `zoned`: mount a dataset over a host path.
  - `sharenfs`, `sharesmb`: export to the network.
  - `exec`, `setuid`, `devices`: enable escalation on the mounted filesystem.
  - `keylocation=file:///...`, `keyformat`, `encryption`: reads host files as key material.
  - `quota`, `refquota`, `reservation`, `refreservation`, `volsize`: override the endpoint's capacity model, and break `compatible()`.
  - `copies=3`: multiplies pool usage.
  - `user:*` and `com.*` namespaces: arbitrary metadata.
- Props also merge into the planner's `own` map (`plan.go:143`, `:158`, `:183`). A key named like `p.Names.managed()` or `volumeName()` could overwrite ownership markers. The allowlist must be checked before the merge.
- Proposed allowlist (default-deny, validate values, per kind):
  - Filesystem: `compression` in {off, lz4, zstd, zle, gzip-1..gzip-9}; `recordsize` a power of two in 512..1048576; `copies` in {1,2,3}; `sync` in {standard, always, disabled}; `logbias` in {latency, throughput}; `atime` in {on, off}.
  - Zvol: `volblocksize` (create-only, power of two 512..131072); `compression`, `copies`, `sync`, `logbias` as above. No `recordsize` (ZFS rejects it on zvols).
  - Denied everywhere, InvalidArgument: anything not listed, any key containing `:`, and every key in the deny list above.
  - Reject values with whitespace or `=`, and cap the map at 8 entries.
- Enforce in the planner (`plan.go`), not only in the front end, because the endpoint is the trust boundary for direct callers.

## HOLDS (tested, not broken)

- **CreateVolume name collision with different capacity** (`controller.go:431-456`): vol1 is Required=Limit=size1. The endpoint stores size1 (`plan.go:156-160` for filesystem, refquota). Second request has want=size2 and have=size1 < want, so `compatible()` (`plan.go:241`) returns AlreadyExists. Passes. Caveat: the test requires `Equal(size1)` exactly (`:447`). For zvol-mode runs (`TestVolumeAccessType=block`), size1 must be a multiple of volblocksize. `TestVolumeSize` default not read.
- **Idempotent CreateVolume, same capacity** (`:410-428`): `>= size or zero` (`:421`, `:427`). Passes.
- **ListVolumes presence check** (`controller.go:211-253`): calls ListVolumes with no max_entries and expects full counts across create/delete. Passes only if the endpoint returns every volume in one page when max_entries=0. `endpoint.go:page` was not read, so this is unverified.
- **ListVolumes pagination with deletes** (`controller.go:262`): `XIt`, disabled at this pin. Not run, so no failure, but the design's list of "pagination with deletes" is moot.
- **ListVolumes values** (`:189-199`): only checks VolumeId non-empty (`verifyVolumeInfo`, `:76-79`). Passes.
- **ValidateVolumeCapabilities** (`:653-731`): uses Mount + SINGLE_NODE_WRITER (`TestVolumeCapabilityWithAccessType`, `:53-67`). `Confirmed` may be nil (`:712`). Nil caps must be InvalidArgument in the front end (`:664-683`). Missing volume must be NotFound (`:717-729`). Passes with the design's mapping.
- **DeleteSnapshot invalid id** (`:1306-1311`): id `reallyfakesnapshotid` has no `@`. `PlanDeleteSnapshot` returns nil on parse error (`plan.go:365-367`). Passes.
- **DeleteVolume invalid id** (`:616-626`): uses `IDGen.GenerateInvalidVolumeID()`, not `fake-vol-id`. The design's example is not the sanity default (default not read). If the generator emits `/` or `@`, `volumeID` returns InvalidArgument and the test fails. Unverified.
- **Snapshot idempotency at max length** (`:1418`): same name, same volume returns the existing id (`plan.go:340-342`). Passes once B1 is fixed.

## UNVERIFIED (not checked in this pass)

- ControllerGetCapabilities unknown-type handling (design cites `:157-159`, not re-read).
- "~350 LOC" front-end estimate: no code written, so it is an estimate, not a measurement.
- `buf breaking`: buf is not installed. On reasoning, adding field 9 to `CreateVolumeRequest` and enum value 8 to `Capability` is additive and should pass FILE rules, but this is not run.
- `endpoint.go:page` default page size (see ListVolumes above).
- Full ZFS property-name validity per kind beyond the allowlist above.
