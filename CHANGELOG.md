# Changelog

All notable changes to Sheaf will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **In-process render path** — `sheaf snapshot` emits a library's Snapshot
  JSON and `sheaf render --from-snapshot <file>` renders the HTML report
  directly, with no `sheaf serve` / `scanner` two-process round-trip.
- **`sheaf review-html`** — re-renders `comment.html` from a previously
  emitted `delta.json`, separating the render step from the diff step.
- **New format adapters** — `crd` (Kubernetes CustomResourceDefinition
  `openAPIV3Schema` fields), `k8smanifest` (rendered Kubernetes YAML field
  inventory), `helmvalues` (Helm chart `values.schema.json` knobs), and
  `conceptdoc` (concept/overview prose attribution).

## [0.1.0] - 2026-05-30

Initial public release.

### Added

- **`sheaf` CLI** — scans a project's contract surface and reports
  documentation-, test-, and example-coverage gaps. Subcommands include
  `init`, `doctor`, `scan`, `snapshot`, `serve`, `report`, `gaps`,
  `coverage`, `review`, and `version`, plus monorepo fan-out via
  `sheaf scan --manifest`.
- **`scanner` companion binary** — generates a self-contained HTML coverage
  report against a running `sheaf serve` MCP server.
- **HTML report generation** — worklist, coverage matrix, and findings
  (tested-undocumented, thin-reference, stale-doc, …) rendered to a single
  self-contained HTML file.
- **MCP server** (`sheaf serve`) — exposes the in-memory contract index over
  JSON-RPC so coding agents can ground answers in the real contract surface,
  tests, and docs. Configurable bind/port/auth via the `mcp_server { ... }`
  config block.
- **PR-bot** (`sheaf review`) — renders a coverage-delta comment between two
  refs for wiring into CI.
- **Format adapters** for contract surfaces, tests, docs, and rendered
  references, each as a package under `internal/adapters/`:
  - Contract anchors: `fidl`, `proto`, `cobra`, `argh`, `clap`, `cml`,
    `cppheader`.
  - Tests: `gotest`, `gtest`, `rusttest`, `bats`, `pytest`, `protocpp`.
  - Docs: `markdown`, `rst`, `workflows`, `yamlworkflows`.
  - Rendered references: `fidldoc`, `clidoc`, `markdowncli`.
  - Implements mapping and build-graph hints: `implementsmap`, `pwfacade`.

[Unreleased]: https://github.com/sheaf-data/sheaf/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/sheaf-data/sheaf/releases/tag/v0.1.0
