# syntax=docker/dockerfile:1
# Works with docker build, podman build, and docker buildx (multi-arch).

# The builder runs on the *build* platform and cross-compiles for the target,
# so a linux/arm64 image does not need QEMU to run the whole Go toolchain.
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS build

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
# Override for networks where proxy.golang.org is unreachable:
#   docker build --build-arg GOPROXY=https://goproxy.cn,direct .
ARG GOPROXY=https://goproxy.cn,direct

WORKDIR /src
ENV CGO_ENABLED=0 GOPROXY=${GOPROXY}

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# TARGETOS/TARGETARCH are supplied by BuildKit (and by podman with --platform).
# When they are absent, fall back to the build host so a plain
# `podman build .` keeps working.
RUN GOOS="${TARGETOS:-linux}" GOARCH="${TARGETARCH:-$(go env GOARCH)}" \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/notifier ./cmd/notifier


FROM alpine:3.20

# UID/GID 10001: any value works, /data just has to be writable by it.
ARG UID=10001
ARG GID=10001

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g "$GID" app \
    && adduser -S -u "$UID" -G app app \
    && mkdir -p /data \
    && chown app:app /data

COPY --from=build /out/notifier /usr/local/bin/notifier

USER app
VOLUME ["/data"]
ENTRYPOINT ["/usr/local/bin/notifier"]
