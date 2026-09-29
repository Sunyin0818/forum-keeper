# syntax=docker/dockerfile:1
# Works with both docker build and podman build.

FROM golang:1.23-alpine AS build

ARG VERSION=dev
# Override for networks where proxy.golang.org is unreachable:
#   docker build --build-arg GOPROXY=https://goproxy.cn,direct .
ARG GOPROXY=https://goproxy.cn,direct

WORKDIR /src
ENV CGO_ENABLED=0 GOOS=linux GOPROXY=${GOPROXY}

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/notifier ./cmd/notifier


FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 app \
    && adduser -S -u 10001 -G app app \
    && mkdir -p /data \
    && chown app:app /data

COPY --from=build /out/notifier /usr/local/bin/notifier

USER app
VOLUME ["/data"]
ENTRYPOINT ["/usr/local/bin/notifier"]
