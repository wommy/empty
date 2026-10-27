# Observability and audit for the ZFS endpoint

## Metrics

**Copy truenas-csi's shape.** It is the closest existing Go CSI driver that wraps a remote storage API. Its `pkg/driver/metrics.go:89-152` and `docs/metrics.md` (github:truenas/truenas-csi) define:

- `truenas_csi_grpc_requests_total{method,code}`: counter, one per CSI RPC, with the gRPC status name as `code`.
- `truenas_csi_grpc_request_duration_seconds{method}`: histogram, buckets 10ms to 300s, since ZFS create and destroy can take minutes.
- `truenas_csi_volume_operations_total{protocol,operation,status}`: counter with `operation` in create/delete/expand/snapshot.
- `truenas_csi_api_calls_total{method,status}` and `truenas_csi_api_call_duration_seconds{method}`: one per backend call. For the endpoint, the backend is the zfs CLI, so these become per-zfs-subcommand metrics.
- `truenas_csi_api_connected` (gauge) and `truenas_csi_api_reconnects_total`: not needed for a local CLI.

The doc's rule is that monitoring never blocks storage: a bind failure is logged and volumes keep working.

**Sidecar metrics.** kubernetes-csi/csi-lib-utils emits `csi_sidecar_operations_seconds`, a histogram with labels `driver_name`, `method_name`, `grpc_status_code` (`metrics/metrics.go:262-270`, `metrics_test.go:155`). It counts calls that never reach the driver. The truenas doc assigns sidecars ports 8081-8084.

**Pool and dataset gauges.** pdf/zfs_exporter (README.md:53-63) exposes pool properties (`--properties.pool`: allocated, free, health, fragmentation, and others), with `--deadline` for cached results and `--exclude` for regex filtering. Its dataset collector maps each ZFS property to a gauge (`collector/dataset.go:265-275`). Use it for capacity; the endpoint should not duplicate it.

zrepl adds `zrepl_zfs_list_unmatched_user_specified_dataset_count` (`internal/zfs/prometheus.go:37-47`) and per-command timing for zfs invocations (`internal/zfs/zfscmd/zfscmd_prometheus.go:43-47`). The per-invocation timing is worth copying: it is the only one of these that times the CLI runner itself.

**Label rule.** Keep dataset names out of labels. Use `method`, `code`, `op`, and the stderr classification (`EEXIST`, `EBUSY`, and so on) as the label set.

## Audit

**ZFS history.** `zpool history -il` (`openzfs/man/man8/zpool-history.8`, lines 25-44) shows user-initiated commands and, with `-i`, internally logged events. With `-l` it adds user name, hostname and zone. The man page does not say whether `zfs destroy` is recorded, nor how long history is kept, so both are **unverified** until tested on a pool. The main problem is identity: the daemon runs as one user, so `-l` will record the daemon, not the CSI caller.

**TrueNAS middleware.** Audit entries carry `audit_id`, a timestamp, `service` (MIDDLEWARE, SMB, SUDO, SYSTEM), `event`, `event_data` and a `success` flag (`src/middlewared/middlewared/api/v27_0_0/audit.py:40-168`, github:truenas/middleware). Retention is 1 to 30 days, and the audit databases sit on a ZFS dataset with a configurable refreservation (lines 40-50). The storage backend itself is not in the local checkout, so it is **unverified** beyond the API model.

**CSI drivers.** truenas-csi attaches a request ID to each RPC and logs it (`pkg/driver/driver.go:674-690`). That is log correlation, not an audit record. I found no audit trail in linstor-server or zfs-localpv within these searches. **Unverified** as a survey.

**Recommendation.** Do not rely on ZFS to record who did what. Keep an append-only log outside the pool, written before each destroy (intent) and after it (result), with the caller identity taken from gRPC metadata. Use ZFS user properties (the `democratic-csi:*` state) for object provenance, but they vanish with the object, so the log is the record of deletion.

## Tracing

**Not propagated today.** truenas-csi's go.mod has Prometheus but no OpenTelemetry (go.mod:11-37). The CSI spec's `csi.proto` has no trace-context field (grep: none).

**Sidecars can, if asked.** csi-lib-utils `connection/connection.go:143-147` adds `WithOtelTracing()`. When set, it installs `grpc.WithStatsHandler(otelgrpc.NewClientHandler())` (lines 187-189), with otelgrpc v0.71.0 (go.mod:9). It was added in v0.15 (CHANGELOG-0.15.md:10). Whether the sidecar binaries enable it by default is **unverified**: the external-provisioner cmd file had no match.

**Decision.** Accept W3C `traceparent` in gRPC metadata and extract it with an otelgrpc server handler, but never require it. The endpoint should start spans for each RPC and each zfs invocation so they link when present. Without an incoming context it still works as a root span.

## Gaps

No existing system pairs a destructive-op audit trail (caller identity, intent, result) with ZFS-native state. The endpoint can add value here by recording who asked for each destroy, in an append-only log that survives the object.
