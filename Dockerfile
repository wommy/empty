# zfs-csi, the CSI controller (deploy/k8s/controller.yaml). Build from the
# repository root and push to a registry the cluster pulls from:
#
#   docker build --build-arg VERSION=v0.1.0 -t <registry>/zfs-csi:v0.1.0 .
#   docker push <registry>/zfs-csi:v0.1.0
#
# then set controller.yaml's zfs-csi image to it (README, "Install").
#
# A static binary (CGO_ENABLED=0: nothing to link at run time) built with
# -trimpath and the version stamped in by -X, as the README says for both
# binaries; `zfs-csi -version` prints it. Both bases are pinned by image
# index digest: a tag can be re-pushed (deploy/image_test.go checks the
# pins and the -X path). The golang tag is go.mod's toolchain.

FROM docker.io/library/golang:1.27.3-trixie@sha256:c124b9ad3452f46061b5054607d26ea51730dae5372c5e469920063ba2bc6dcc AS build
ARG VERSION=dev
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-mod=readonly
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath \
      -ldflags "-X github.com/wommy/empty/internal/version.Version=${VERSION}" \
      -o /out/zfs-csi ./cmd/zfs-csi

# distroless static: no shell, no package manager, CA certificates and
# tzdata only. The :latest variant, whose user is root: zfs-csi runs as
# uid 0 to dial the endpoint's 0600 root socket and to serve csi.sock
# 0600 to the sidecars (ADR 0006, ADR 0008); the pod still drops every
# capability and sets runAsUser: 0 itself.
FROM gcr.io/distroless/static-debian13:latest@sha256:15f8637509792e05b97d83451b0b6ccfa4abd548f997e406ac1e72c87fe1fa78
COPY --from=build /out/zfs-csi /zfs-csi
USER 0:0
ENTRYPOINT ["/zfs-csi"]
