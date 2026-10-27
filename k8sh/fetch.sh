#!/bin/bash
set -eo pipefail
out=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/k8sh/up
mkdir -p "$out"
R=https://raw.githubusercontent.com/kubernetes-csi
curl -sSf -o "$out/resizer-rbac.yaml" $R/external-resizer/v1.13.1/deploy/kubernetes/rbac.yaml
curl -sSf -o "$out/resizer-main.go" $R/external-resizer/v1.13.1/cmd/csi-resizer/main.go
curl -sSf -o "$out/resizer-controller.go" $R/external-resizer/v1.13.1/pkg/controller/controller.go
curl -sSf -o "$out/prov-rbac.yaml" $R/external-provisioner/v5.2.0/deploy/kubernetes/rbac.yaml
curl -sSf -o "$out/snap-rbac.yaml" $R/external-snapshotter/v8.2.0/deploy/kubernetes/csi-snapshotter/rbac-csi-snapshotter.yaml
ls -la "$out"
