# Sheaf — Makefile entry points.
# Other workflows live as standalone scripts under scripts/.

SHELL := /usr/bin/env bash

# Version metadata embedded via -ldflags. GoReleaser sets these on tagged
# releases (.goreleaser.yaml); these defaults cover local/dev builds.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
PKG     := github.com/sheaf-data/sheaf/internal/cli
LDFLAGS := -X $(PKG).BuildVersion=$(VERSION) -X $(PKG).BuildCommit=$(COMMIT) -X $(PKG).BuildDate=$(DATE)

# Build the sheaf binary into bin/ with version metadata stamped in.
.PHONY: build
build:
	go build -ldflags "$(LDFLAGS)" -o bin/sheaf ./cmd/sheaf

# Dry-run the release pipeline locally (no publish). Requires goreleaser.
.PHONY: release-snapshot
release-snapshot:
	goreleaser release --snapshot --clean

# Local mirror of the CI `checks` + `test` jobs (.github/workflows/ci.yml):
# build, gofmt, vet, golangci-lint, then the race-enabled test suite.
.PHONY: check
check:
	go build ./...
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
	  echo "These files are not gofmt-formatted; run 'gofmt -w .':"; \
	  echo "$$unformatted"; \
	  exit 1; \
	fi
	go vet ./...
	golangci-lint run --timeout=5m
	go test ./... -race -count=1
