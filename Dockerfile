# syntax=docker/dockerfile:1

# The builder is pinned to the toolchain in go.mod. TARGETOS and TARGETARCH are
# supplied by buildx, so one Dockerfile covers both amd64 and arm64.
FROM --platform=$BUILDPLATFORM golang:1.26.6-trixie AS build

WORKDIR /src

# Dependencies are downloaded in their own layer so that editing sources does
# not re-fetch the module cache.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/prune-ghcr ./cmd/prune-ghcr

# static rather than scratch: the CA certificates are needed for TLS to the API
# and the registry. The default (root) user rather than :nonroot, because the
# runner mounts $GITHUB_OUTPUT into the container and it has to stay writable.
#
# Pinned by digest rather than tag: distroless publishes no version tags for
# this image, only :latest, and a floating base would undo the point of pinning
# the action itself by digest.
#
# checkov:skip=CKV_DOCKER_3: running as root is the deliberate choice above.
# checkov:skip=CKV_DOCKER_2: a healthcheck is meaningless for a container that
# runs once to completion and exits.
FROM gcr.io/distroless/static-debian13@sha256:9197324ba51d9cd071af8505989365c006adf9d6d2067eada25aef00abbb5278

COPY --from=build /out/prune-ghcr /prune-ghcr

ENTRYPOINT ["/prune-ghcr"]
