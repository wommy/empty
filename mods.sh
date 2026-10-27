#!/bin/sh
P=https://proxy.golang.org
for v in v1.13.0 v1.12.0 v1.11.0; do
  echo "spec $v:"; curl -s "$P/github.com/container-storage-interface/spec/@v/$v.mod" | grep -E '^go |toolchain|grpc |protobuf '
done
for v in v5.6.0 v5.5.0 v5.4.0 v5.3.1; do
  echo "csi-test $v:"; curl -s "$P/github.com/kubernetes-csi/csi-test/v5/@v/$v.mod" | grep -E '^go |toolchain|spec |grpc |ginkgo|gomega'
done
