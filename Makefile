# Sheaf — Makefile entry points.
# Other workflows live as standalone scripts under scripts/.

SHELL := /usr/bin/env bash

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
