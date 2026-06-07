# Contributing to Sheaf

Thanks for your interest in Sheaf. Sheaf scans a project's contract surface
(CLI flags, protobuf, FIDL, C++ headers, and more) and reports
documentation-coverage gaps; it also ships an MCP server. This guide covers
how to build it, run the checks, and add new functionality.

## Prerequisites

- **Go** — the version pinned in [`go.mod`](go.mod) (currently `go 1.26.2`).
  Use that release or newer.
- **golangci-lint** — for the lint gate (CI uses `v1.64.8`). See the
  [install guide](https://golangci-lint.run/usage/install/).
- **protoc + protoc-gen-go** — only needed if you change `.proto` files.
  Install the Go plugin with:

  ```sh
  go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
  ```

## Build

From a clone:

```sh
go build ./...
```

To build the two main binaries locally:

```sh
go build -o sheaf ./cmd/sheaf
go build -o scanner ./cmd/scanner
```

Or install them directly:

```sh
go install github.com/sheaf-data/sheaf/cmd/sheaf@latest
go install github.com/sheaf-data/sheaf/utils/scanner@latest
```

See the [README](README.md) for the full quick-start and how to run a scan.

## Tests, lint, and formatting

The CI gate ([`.github/workflows/ci.yml`](.github/workflows/ci.yml)) runs the
following. Run them locally before opening a PR so you don't round-trip on CI:

```sh
go build ./...                  # everything compiles
gofmt -l .                      # must print nothing; run `gofmt -w .` to fix
go vet ./...
golangci-lint run --timeout=5m
go test ./... -race -count=1     # race detector on, no test caching
```

The `gofmt` check fails if `gofmt -l .` lists any file. The test suite must
pass with `-race`; it's the same command CI runs.

As a convenience, `make check` runs all of the above (build, gofmt, vet,
golangci-lint, race tests) in one shot — it mirrors the CI jobs locally:

```sh
make check
```

## Regenerating proto bindings

If you edit any `.proto` file under [`proto/`](proto/), regenerate the Go
bindings and commit them alongside your change:

```sh
proto/generate.sh
```

The script requires `protoc` and `protoc-gen-go` on your `PATH` (see
Prerequisites). It writes the generated `*.pb.go` files back under `proto/`.
Generated bindings are committed to the repo, so a `.proto` change and its
regenerated output should land in the same PR.

## Adding a new format adapter

Adapters are the primary extension point. Each ecosystem-specific parser is
one package under [`internal/adapters/`](internal/adapters/), and the
orchestrator works only against the interfaces declared in
[`internal/adapters/adapters.go`](internal/adapters/adapters.go):

- `ContractAnchorParser` — discovers contract elements (FIDL libs, protobuf,
  C++ headers, CLI flag structs, …).
- `TestParser` — discovers test cases from a test framework.
- `DocParser` — parses human-authored prose (markdown, rST, …).
- `RenderedReferenceParser` — parses pre-rendered reference bundles
  (fidldoc, clidoc, per-subcommand markdown).
- `ImplementsMapper` — bridges implementation classes back to the contract
  elements they serve.

To add a new format, follow the one-package-per-format pattern of an existing
adapter (e.g. [`internal/adapters/proto/`](internal/adapters/proto/) or
[`internal/adapters/cobra/`](internal/adapters/cobra/)):

1. Create a new package under `internal/adapters/<yourformat>/` that
   implements the relevant interface, including `Name()` and `Version()`.
   Expose a `New(cfg Config) *Adapter` constructor.
2. Register it in the orchestrator
   ([`internal/orchestrator/orchestrator.go`](internal/orchestrator/orchestrator.go)).
   The orchestrator maps a config-declared adapter name to its constructor in
   a `switch` on `GetName()` (e.g. `case "proto":`). Add a `case` for your
   adapter's name that calls your `New(...)`.
3. Add table-driven tests next to the adapter (`<yourformat>_test.go`), in
   line with the existing adapters.

For end-to-end onboarding of a repo to Sheaf, the agent cookbook is in
[`docs/playbooks/onboard-a-new-repo.md`](docs/playbooks/onboard-a-new-repo.md).

## Pull requests

- **Branch off `main`.** Create your feature branch from an up-to-date `main`.
- **CI must be green.** All checks above (build, gofmt, vet, golangci-lint,
  race tests) must pass before a PR can merge.
- **Keep changes focused.** One logical change per PR; avoid mixing unrelated
  refactors with feature work. Smaller PRs review faster.
- **Include tests** for new behavior, and update any affected docs under
  [`docs/`](docs/).
- **Keep `KNOWN_LIMITATIONS.md` honest.** If your change closes a known
  limitation, adds a new rough edge, or draws a new scope line, update
  [`KNOWN_LIMITATIONS.md`](KNOWN_LIMITATIONS.md) in the same PR so the
  honesty doc never drifts from the tool.
- If you touched `.proto` files, make sure the regenerated bindings are
  committed.

## License

By contributing, you agree that your contributions are licensed under the
Apache 2.0 License — see [LICENSE](LICENSE).
