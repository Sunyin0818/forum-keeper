BINARY := bin/forum-keeper
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GO ?= go

.PHONY: all build test vet fmt lint clean run-once run dry-run checkin checkin-dry-run image up down logs

all: test build

build:
	$(GO) build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o $(BINARY) ./cmd/forum-keeper

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -w .

clean:
	rm -rf bin

# Loads .env, then runs a single poll without sending anything.
# The local-env.sh helpers adapts .env (written for the container) so a host
# run can resolve the proxy address. See the script for details.
dry-run:
	. ./scripts/local-env.sh && $(GO) run ./cmd/forum-keeper --once --dry-run

run-once:
	. ./scripts/local-env.sh && $(GO) run ./cmd/forum-keeper --once

run:
	. ./scripts/local-env.sh && $(GO) run ./cmd/forum-keeper

# Signs in to every configured site once. The dry-run variant prints only.
checkin:
	. ./scripts/local-env.sh && $(GO) run ./cmd/forum-keeper --checkin

checkin-dry-run:
	. ./scripts/local-env.sh && $(GO) run ./cmd/forum-keeper --checkin --dry-run

# --- containers (docker and podman both work) -------------------------------

image:
	docker build --build-arg VERSION=$(VERSION) -t forum-keeper:latest . \
		|| podman build --build-arg VERSION=$(VERSION) -t forum-keeper:latest .

up:
	docker compose up -d || podman compose up -d

down:
	docker compose down || podman compose down

logs:
	docker compose logs -f || podman compose logs -f
