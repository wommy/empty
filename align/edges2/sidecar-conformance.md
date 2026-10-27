# Sidecar contract and conformance oracle

## EDGES: retry classification the endpoint must honour

All three sidecars treat the same gRPC codes as "operation may still be in progress" (retry the same name; do not give up). Everything else is final.

- **In-progress set:** Canceled, DeadlineExceeded, Unavailable, Aborted. Snapshotter and resizer also include ResourceExhausted.
- **Final:** FailedPrecondition, Internal, InvalidArgument, AlreadyExists, NotFound and all other codes. Non-gRPC errors are final in the provisioner and in-progress (not final) in the snapshotter and resizer.
- external-provisioner `checkError`: ResourceExhausted gives Reschedule or Finished. The in-progress set gives InBackground. Everything else gives Finished. `github:kubernetes-csi/external-provisioner@ee7abf31 pkg/controller/controller.go:2037-2072`
- external-snapshotter `isCSIFinalError`: `github:kubernetes-csi/external-snapshotter@44c047ad pkg/sidecar-controller/snapshot_controller.go:889-911`
- external-resizer `IsFinalError`: `github:kubernetes-csi/external-resizer@e24dbe9d pkg/util/util.go:242-264`
- **Timeouts:** provisioner `--timeout` defaults to 10s and wraps each CreateVolume (controller.go:878). Snapshotter `--timeout` defaults to 1 min (`cmd/csi-snapshotter/main.go:73-83`). Resizer `--resize-timeout` and `--modify-timeout` default to 10s (`cmd/csi-resizer/main.go:72-78`).
- **Backoff:** provisioner and snapshotter both use retry-interval-start 1s, doubling to retry-interval-max 5m (`cmd/csi-provisioner/csi-provisioner.go:83-84`, `cmd/csi-snapshotter/main.go:86-87`).
- **Idempotency key:** the provisioner sends `Name: pvName` (controller.go:689-690). The snapshotter sends `snapshotName` (`pkg/snapshotter/snapshotter.go:63-65`). Both names are fixed per API object, so they are stable across retries. How pvName and snapshotName are derived was not verified in this pass. The resizer keys on volume ID; the line was not verified.
- **Implication:** return ABORTED only for "operation pending for this name". Return DEADLINE_EXCEEDED or UNAVAILABLE for transient faults. Return ALREADY_EXISTS for the same name with different parameters. Return FAILED_PRECONDITION for a non-retryable state.

## SEAMS: csi-test pkg/sanity as the oracle (`github:kubernetes-csi/csi-test@95f23ec4`)

Each test is gated by `isControllerCapabilitySupported`, so a test is skipped when its capability is not advertised. Advertising an RPC you do not implement makes its tests fail.

- Capability gates in `pkg/sanity/controller.go`:
  - CREATE_DELETE_VOLUME: CreateVolume and DeleteVolume (lines 347, 598)
  - LIST_VOLUMES: 182
  - GET_CAPACITY: 165
  - CREATE_DELETE_SNAPSHOT: 472, 508, 1285, 1337
  - LIST_SNAPSHOTS: 954
  - GET_SNAPSHOT: 1205
  - CLONE_VOLUME: 527, 551
  - EXPAND_VOLUME: 1468
  - MODIFY_VOLUME: 570, 583, 1519
  - PUBLISH_UNPUBLISH_VOLUME: 735, 910, 926
  - PUBLISH_READONLY: 866
  - GET_VOLUME_HEALTH: 1624
  - LIST_VOLUME_HEALTH: 1665
- Always run: ControllerGetCapabilities and ValidateVolumeCapabilities (653), plus identity.go, node.go and groupcontroller.go.
- `ControllerGetCapabilities` fails on any capability type it does not know (controller.go:157-159).
- Idempotency expectation: CreateVolume with an existing name and different capacity must return ALREADY_EXISTS (controller.go:431-457).
- For full coverage the front end must advertise every RPC it implements, including CLONE_VOLUME and EXPAND_VOLUME, which ZFS supports natively.
- License of csi-test was not verified in this pass.

## GAPS

- The Kubernetes e2e storage testsuites (`test/e2e/storage/testsuites`, driven through `test/e2e/storage/external/README.md:30-36` at `github:kubernetes/kubernetes@b3c29caa`) are framed as cluster e2e tests. I found no evidence of a cluster-free mode. This is unverified beyond the README wording.
- Sanity checks RPC shape and idempotency only. It does not check the sidecars' error-code classification. That is covered only by sidecar unit tests with fake servers (for example `snapshotter_test.go:181-428`, which injects DeadlineExceeded and NotFound). The endpoint must test its own code mapping against the EDGES table above.
- Coverage of the search was limited. Newer conformance suites beyond sanity and the e2e storage testsuites were not checked.
