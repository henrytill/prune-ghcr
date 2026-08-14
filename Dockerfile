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
FROM gcr.io/distroless/static-debian13

COPY --from=build /out/prune-ghcr /prune-ghcr

ENTRYPOINT ["/prune-ghcr"]
