# 6. First deployment: hyperconverged over unix sockets, journald metrics, block opt-in

Status: accepted (decisive, reversible) · Date: 2026-10-27 · Related: ADR 0001 (transport auth), ADR 0004 (node side), `deploy/`.

## Context

- **Topology.** Both binaries refuse TCP until ADR 0001's transport auth exists (`cmd/zfs-endpoint` `socketPath`, `cmd/zfs-csi` `endpointTarget`). zfs-csi can therefore only reach an endpoint on its own host, through a hostPath to `/run/zfs-endpoint`. A storage appliance separate from the Kubernetes nodes cannot be used today.
- **Metrics.** Neither binary exports metrics. Both log structured JSON (slog) to stderr, which journald (the unit) and the container runtime (the pod) already collect. A Prometheus endpoint means a new dependency, a listener and its own access control.
- **Block.** zfs-csi provisioned zvols for block access, but no node plugin attaches a zvol to a node (ADR 0004: "Block volumes still need a node story"; `docs/research/gaps/block-export-v2.md` is design only). A block PVC bound and its pod never started.

## Decision

- **The first deployment is hyperconverged.** zfs-endpoint runs under systemd on the ZFS host (`deploy/systemd/zfs-endpoint@.service`). The zfs-csi controller pod runs on the same host (nodeSelector, `deploy/k8s/controller.yaml`) and dials the endpoint over its unix socket. Both sockets are unix sockets only; nothing listens on TCP.
  - One socket convention: `/run/zfs-endpoint/<systemd-escape parent>.sock`. The unit serves it, zfs-endpoint's default `-listen` is it, and `zfs-csi -parent <dataset>` dials it (`internal/unixsock` `EndpointAddr`, pinned by `deploy/deploy_test.go`).
  - The CSI socket lives in the pod's emptyDir, which kubelet makes 0777. zfs-csi alone accepts that directory mode (`unixsock.ListenShared`); the socket is still 0600, and the endpoint socket keeps the strict 0700-directory check.
- **Metrics are journald structured logs first.** Operators read state from the JSON log lines (`journalctl -u 'zfs-endpoint@*' -o json`, `kubectl logs`). A Prometheus endpoint comes later, as its own change, with its own listener and an owner decision on the dependency.
- **Block volumes are opt-in.** `zfs-csi -enable-block` is off by default. Off, CreateVolume refuses a block capability with InvalidArgument, and ValidateVolumeCapabilities never confirms one. It stays off until a node plugin can attach zvols.

## Consequences

- No remote storage host, no TrueNAS appliance separate from the cluster, until ADR 0001 is implemented. Reversing this means building ADR 0001's mTLS slice; nothing here has to be undone.
- The controller pod and its sidecars run as uid 0 (the sockets are 0600 root). They need no capabilities.
- No dashboards or alerts on counters. Anything an operator must see has to be a log line until the metrics change lands.
- An existing block StorageClass stops provisioning when this ships. Volumes already provisioned are untouched. `-enable-block` restores the old behaviour.
- Reversal is a flag or a manifest change in each case: drop the nodeSelector once remote transport exists, add a metrics listener, turn `-enable-block` on by default once a node plugin ships.
