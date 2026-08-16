# syntax=docker/dockerfile:1.26.0@sha256:ecfaec9ed6d810b56388c508f4121597bfbba70d41a6dfeee4d8cad5f295fc32

# The directive above has to be the first line: a comment before it is not a
# parser directive, and silently turns it into an ordinary comment.
#
# The frontend is pinned by digest rather than left on the floating `1` tag. It
# is a build input like any other - it decides how layers and the image config
# are produced - so a newer frontend can give this source a different digest.
# CI cannot catch that, because both of its builds resolve the same frontend
# seconds apart; it would surface as a failed reproduction months later.

# The builder is pinned to the toolchain in go.mod. TARGETOS and TARGETARCH are
# supplied by buildx, so one Dockerfile covers both amd64 and arm64.
#
# By digest as well as by tag, like the runtime base below. Docker Hub re-pushes
# these tags on security updates, so the tag alone names different bytes on
# different days - which reproduces as a failed rebuild weeks later rather than
# as anything CI can see, since both of its builds resolve the tag seconds apart.
FROM --platform=$BUILDPLATFORM golang:1.26.6-trixie@sha256:ab563819a16cfe5faff0f96a8bb598fbb0e400ab2ac751996e60abcb23b106a3 AS build

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
