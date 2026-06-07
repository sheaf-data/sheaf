<p align="center">
  <img src="docs/social-preview.png" alt="Sheaf — Never been easier to generate code. Never been harder to know which calls are real." width="100%">
</p>

# Sheaf

**Your coding agent hallucinates APIs because your project is fragmented — not because the model isn't smart enough.** The docs, tests, and examples exist; they just don't point at the same things. Sheaf maps your real contract surface — every CLI command, API method, config knob — and shows, element by element, which ones a doc explains, a test verifies, and a working example demonstrates. The empty cells are where agents guess and humans open three tabs and a grep window.

It runs as a CLI and ships an [MCP server](#mcp-server), so the same map your team reads is the ground truth your agent reads *before* it answers.

## Getting started

```sh
go install github.com/sheaf-data/sheaf/cmd/sheaf@latest
sheaf scan --auto --llm-backend none --repo .   # zero-config, deterministic, no model
open sheaf-auto/sheaf-report.html               # the report + the sheaf.textproto it generated
```

`--auto` autodetects the common deterministic surfaces — Rust (clap) CLIs, protobuf / gRPC, FIDL, and C++ headers — runs the scan, and writes a self-contained report next to the `sheaf.textproto` it generated, so a second run is just `sheaf scan`. `--llm-backend none` keeps the first run fast and dependency-free: the contract↔test↔doc join is deterministic, so no model is contacted. (Drop the flag to add a citation-gated LLM attribution pass — `ANTHROPIC_API_KEY` for a fast frontier pass, or a local ollama model.)

**That first report is a starting point, not the finished product.** Other contract surfaces — cobra CLIs (kubectl, gh, docker), Kubernetes CRDs, Helm values, OpenAPI — aren't autodetected; they're wired by config. And either way, a scan you'd forward to your team is real configuration work: the right include/exclude globs, a source map that groups by feature area, and a spot-check that every claimed bridge is real — a wrong glob *silently* under-counts. That's a job worth handing to a coding agent.

> **Onboard your repo with the `sheaf-onboard` skill (recommended).** Your agent does the reconnaissance and config, runs the scan, then runs `sheaf verify` to check every headline number against disk *before* it shows you the report — so your spot-check is a confirmation, not a discovery. If config alone can't reach a surface, it offers a `sheaf-build-adapter` handoff instead of quietly under-counting.
>
> ```sh
> docs/examples/sheaf-onboard/install.sh   # then, in Claude / Cursor / Cline:  /sheaf-onboard
> ```
>
> Any agentic CLI (Gemini, Codex) drives it via [`AGENTS.md`](docs/examples/sheaf-onboard/AGENTS.md). Prefer to wire it by hand? See [docs/scan-your-repo.md](docs/scan-your-repo.md).

From a clone, build instead of install: `go build -o sheaf ./cmd/sheaf` — same flags, `./sheaf` instead of `sheaf`. Known rough edges are tracked in [KNOWN_LIMITATIONS.md](KNOWN_LIMITATIONS.md).

## Sample reports

Each row links to a single self-contained HTML report produced by Sheaf against a real project. Click the sample name to open the report; the worklist, coverage matrix, and findings reflect that project's actual state at scan time. The config and rules used to produce each report are linked alongside — clone, point Sheaf at the same config, and you should reproduce the same numbers.

| Sample | Ecosystem | Contract surface | Config |
|---|---|---|---|
| **[envoy ↗](example-reports/envoy.html)** | proto | Envoy xDS v3 protobuf API | [textproto](docs/examples/envoy-coverage-config.textproto) |
| **[envoy config ↗](example-reports/envoy-config.html)** | proto | Envoy operator-facing config TYPEs — `Listener`, `Cluster`, `RouteConfiguration` (one combined report; sheaf-side rST adapter decodes Sphinx `:ref:` slugs to land docs on these surfaces) | [textproto](docs/examples/envoy-coverage-config.textproto) |
| **[gRPC ↗](example-reports/grpc.html)** | proto | Core gRPC service definitions (health, reflection, channelz) | [textproto](docs/examples/grpc-coverage-config.textproto) |
| **[kubectl (Kubernetes CLI) ↗](example-reports/kubectl.html)** | cli | kubectl subcommands + flags | [textproto](docs/examples/kubectl-coverage-config.textproto) |
| **[cert-manager ↗](example-reports/cert-manager-crd.html)** | crd | cert-manager `CustomResourceDefinition` fields — `Certificate`/`Issuer`/`ClusterIssuer`/`CertificateRequest`/… The `crd` anchor walks each CRD's `spec.versions[].schema.openAPIV3Schema`; the OpenAPI `description` kubebuilder writes inline is the doc source, so the doc-coverage join is mechanical (no LLM). 686 elements, 99% documented — the handful of gaps are the bare `apiVersion`/`kind`/`metadata` stubs. | [textproto](docs/examples/cert-manager-crd-coverage-config.textproto) |
| **[Prometheus Operator ↗](example-reports/prometheus-operator-crd.html)** | crd | Prometheus Operator `CustomResourceDefinition` fields — `Prometheus`/`Alertmanager`/`ServiceMonitor`/`ThanosRuler`/… A large surface (5,020 elements) parsed straight from the release CRD bundle; 4,973 carry an inline OpenAPI `description`, leaving 47 undocumented fields as the actionable queue. | [textproto](docs/examples/prometheus-operator-crd-coverage-config.textproto) |
| **[ingress-nginx (rendered) ↗](example-reports/ingress-nginx-manifest.html)** | manifest | A `helm template`-rendered ingress-nginx stream, scoped to the `apps` group's `Deployment` (101 elements: 1 group, 1 kind, 99 fields incl. nested `spec.template.spec.containers[].image`, probes, security context). The `k8smanifest` anchor is the universal fallback for charts with no schema — it reports the fields *actually present* in valid, already-rendered YAML and never parses raw Helm templates (rendering is the user's `helm template` step). No inline docs ship in a manifest, so this is a contract-surface inventory: every field links to file:line in the rendered YAML. | [textproto](docs/examples/ingress-nginx-manifest-coverage-config.textproto) |
| **[cert-manager (Helm values) ↗](example-reports/cert-manager-helm.html)** | helm | The cert-manager chart's **values** surface, read from its published `values.schema.json` (Helm 3's formal JSON-Schema values contract). The `helmvalues` anchor walks the schema's `properties` recursively — resolving JSON-Schema `$ref`/`$defs` — and emits 274 elements: 1 chart `LIBRARY` + 273 `CONFIG_KNOB` value keys (`replicaCount`, `image.tag`, `webhook.*`, `cainjector.*`, …). Each key's schema `description` is the doc source (no LLM); a key with none is a real undocumented finding. The adapter reads only `values.schema.json`/`values.yaml`, never `templates/*.yaml`. Caveat: a chart's published values surface is not a *proof* of completeness — the true surface is whatever the templates actually reference (see [KNOWN_LIMITATIONS.md](KNOWN_LIMITATIONS.md#known-limitations)). Every key links to file:line in `values.schema.json`. | [textproto](docs/examples/cert-manager-helm-coverage-config.textproto) |
| **[gh ↗](example-reports/gh.html)** | cli | GitHub CLI subcommands + flags | [textproto](docs/examples/gh-coverage-config.textproto) |
| **[fd ↗](example-reports/fd.html)** | cli | fd's clap-derive flag surface (1 binary + 48 flags / switches / config knobs / positionals) | [textproto](docs/examples/fd-coverage-config.textproto) |
| **[ffx (Fuchsia CLI) ↗](example-reports/ffx.html)** | cli | Fuchsia's `ffx` developer tool — the complete **680-command + ~1,100-flag** surface. ffx hides most leaf subcommands as `argh` enum variants packed into shared `args.rs` files, so a source walker sees only ~27%; the contract is instead synthesized from ffx's checked-in CLI goldens (the CI-diffed `ffx --machine json-pretty --help` dumps) into cobra-schema YAML — the same generate-a-bundle pattern kubectl uses. `clidoc` gives every command a deep-anchored fuchsia.dev reference (99%), and the 29 `ffx` workflow recipes + the goldens' per-command `examples[]` feed the workflow/examples surfaces. A deliberately honest case: per-flag prose and command-level tests run sparse — most ffx testing is integration/e2e rather than co-located unit tests the name-token matcher can attribute — so the report surfaces that gap instead of hiding it. | [textproto](docs/examples/ffx-coverage-config.textproto) |
| **[fuchsia.ui.composition ↗](example-reports/fuchsia-ui-composition.html)** | FIDL | Fuchsia UI composition library | [textproto](docs/examples/fuchsia-ui-composition-coverage-config.textproto) |
| **[fuchsia driver-framework ↗](example-reports/fuchsia-driver-framework.html)** | FIDL | A composed Fuchsia component | [textproto](docs/examples/fuchsia-driver-framework-coverage-config.textproto) |
| **[fuchsia driver-framework family ↗](example-reports/fuchsia-driver-framework-family.html)** | FIDL | The 10 `fuchsia.driver.*` libraries (DFv2 framework + adjacent: host, indexer, loader, compat, registrar, test harness, token, logger) joined into one combined report | [textproto](docs/examples/fuchsia-driver-framework-family-coverage-config.textproto) |
| **[fuchsia.ui.gfx ↗](example-reports/fuchsia-ui-gfx.html)** | FIDL | The predecessor of `fuchsia.ui.composition`, mid-migration — 132 elements deprecated, 1 removed. Showcases how the Worklist rolls deprecated/removed buckets into a single caption so they don't dominate the actionable queue. | [textproto](docs/examples/fuchsia-ui-gfx-coverage-config.textproto) |
| **[Pigweed pw_rpc ↗](example-reports/pigweed-pw_rpc.html)** | cpp | Pigweed `pw_rpc` C++ public API (148 elements: `pw::rpc::Server`, `Client`, `Channel`, `ChannelOutput`, the call objects, …). The richest of the three Pigweed examples — the `cppheader` anchor enumerates the real C++ contract, Sphinx `:cpp:func:`/`:cpp:class:` doc roles land on it, and namespace-scoped `TEST(Class, …)` fixtures attribute back to the class under test. | [textproto](docs/examples/pigweed-pw_rpc-coverage-config.textproto) |
| **[Pigweed pw_log ↗](example-reports/pigweed-pw_log.html)** | cpp | Pigweed `pw_log` C++ API — a facade rolled together with its backends (`pw_log_basic`, `pw_log_tokenized`, `pw_log_string`, `pw_log_null`). The `build_graph{pw_facade{}}` recognizer links them: each backend's `PW_HANDLE_LOG` handler macro gets an IMPLEMENTS edge to the facade's `PW_LOG` (Pigweed's `PW_HANDLE_<X> → PW_<X>` convention). Still a deliberately honest low-coverage case — the user-facing API is mostly macros (`PW_LOG_DEBUG`, …) whose *usage* in tests/docs isn't attributed yet, so the report surfaces that gap rather than hiding it. | [textproto](docs/examples/pigweed-pw_log-coverage-config.textproto) |
| **[Pigweed pw_transfer ↗](example-reports/pigweed-pw_transfer.html)** | cpp | Pigweed `pw_transfer` C++ API (101 elements: `Client`, transfer handlers, `TransferThread`, …). Moderate coverage — the module's test fixtures are scenario-named (`ReadTransfer`, `…HandlerTest`) rather than class-named, which is exactly the signal that drives the attribution rate down. | [textproto](docs/examples/pigweed-pw_transfer-coverage-config.textproto) |

*Run Sheaf on your own repo — [docs/scan-your-repo.md](docs/scan-your-repo.md).*

To reproduce any sample locally (in-process, no server):

```sh
sheaf snapshot --config docs/examples/envoy-coverage-config.textproto --library envoy --out /tmp/envoy.json
sheaf render --from-snapshot /tmp/envoy.json --ecosystem proto -o envoy-report.html
```

Pass the `--ecosystem` that matches the sample (the **Ecosystem** column above). All sample configs and rules live under [docs/examples/](docs/examples/).

## MCP server

Sheaf exposes its in-memory index as an MCP server so coding agents (Claude, Cursor, Cline, anything that speaks MCP) can ground their answers in the real contract surface, real tests, and real docs — not plausibilities. Start it with `sheaf serve --repo .` and point your agent at `http://127.0.0.1:7700`; queries include contract elements, coverage profiles, and worked examples per element. Server config (bind, port, auth) lives in the `mcp_server { ... }` block — see [docs/config.md](docs/config.md).

Full wire protocol, every JSON-RPC operation, return payload shapes, and proto schema indexes are at [docs/mcp/api.md](docs/mcp/api.md) and [docs/mcp/schema.md](docs/mcp/schema.md).

## PR-bot

`sheaf review --base <base-repo-root> --repo <head-repo-root>` renders a coverage-delta comment for a pull request: which contract elements gained or lost docs, tests, or usage between the two corpora. You point it at two repo roots (the PR base and head working trees), not bare git refs — the refs are recorded separately via the `--emit-base-ref` / `--emit-head-ref` flags. Wire it into CI to post on PR open and on push.

A worked end-to-end example lives under [docs/examples/sheaf-bot-demo/](docs/examples/sheaf-bot-demo/) — a base/head commit pair with the request, the rendered comment, and the demo script checked in side-by-side.

## Monorepo fan-out

`sheaf scan --manifest <file>` reads a `MonorepoManifest` textproto and runs a scan + render for every entry, producing one report per module plus an `index.html` linking them — all in-process, no `sheaf serve` per module. It is the automated counterpart to the interactive `scripts/regen-example-reports.sh` (which stays the exploratory path).

The manifest format is generic: Cargo workspaces, Bazel monorepos, and Lerna packages plug into the same runner by supplying their own list-generator. Pigweed's ships as a worked example — [`scripts/generate-pigweed-manifest.sh`](scripts/generate-pigweed-manifest.sh) emits a manifest with repo-relative `config_path` values from a checkout's `PIGWEED_MODULES`:

```sh
scripts/generate-pigweed-manifest.sh > /tmp/pigweed-manifest.textproto
sheaf scan --manifest /tmp/pigweed-manifest.textproto \
           --config-root "$(pwd)" \
           --output-dir /tmp/pigweed-fanout \
           --repo /Volumes/T7/pigweed
```

The manifest lives in `/tmp` but its `config_path` entries point into the sheaf repo, so `--config-root "$(pwd)"` (run from the sheaf repo root) anchors them. Without it, relative `config_path` values resolve against the manifest's own directory.

See [docs/cli/reference/sheaf_scan.md](docs/cli/reference/sheaf_scan.md) for the manifest schema and the continue-on-failure semantics.

## CLI reference

Every subcommand and helper binary has its own reference page:

- [docs/cli/sheaf.md](docs/cli/sheaf.md) — overview of the `sheaf` binary.
- [docs/cli/reference/](docs/cli/reference/) — one page per subcommand (`scan`, `gaps`, `coverage`, `report`, `snapshot`, `render`, `serve`, `review`, `review-html`, `init`, `doctor`, `version`) plus the companion binaries (`scanner`, `dump-elements`, `dump-profile`, `kubectl-yamlgen`).
- [docs/cli/workflows.md](docs/cli/workflows.md) — end-to-end recipes that combine the binaries.
- [docs/cli/self-monitoring.md](docs/cli/self-monitoring.md) — how sheaf is configured to scan its own CLI surface.

## MCP reference

- [docs/mcp/README.md](docs/mcp/README.md) — at-a-glance map for picking the right operation.
- [docs/mcp/api.md](docs/mcp/api.md) — JSON-RPC wire protocol, every method, params, return shape, error codes, auth.
- [docs/mcp/schema.md](docs/mcp/schema.md) — proto messages the operations return.

## Repo layout

```
.                       Go module (github.com/sheaf-data/sheaf)
├── cmd/sheaf/          Main binary
├── cmd/dump-elements/  Debug helper (FIDL adapter)
├── cmd/dump-profile/   Debug helper (coverage dump)
├── cmd/kubectl-yamlgen/  Generates per-subcommand YAML for kubectl scans
├── internal/adapters/  Contract / test / doc / rendered-reference adapters
│   ├── argh/           Rust argh-derived CLI surface
│   ├── bats/           Bash test framework
│   ├── clap/           Rust clap-derived CLI surface
│   ├── clidoc/         Fuchsia clidoc tarball
│   ├── cobra/          spf13/cobra YAML reference (docker, kubectl, gh, …)
│   ├── conceptdoc/     Concept/overview prose attribution
│   ├── crd/            Kubernetes CRD openAPIV3Schema fields
│   ├── fidl/           FIDL contract source
│   ├── fidldoc/        Fuchsia fidldoc bundle
│   ├── gotest/         Go test functions + cobra-invocation extractor
│   ├── gtest/          C++ googletest
│   ├── helmvalues/     Helm chart values.schema.json knobs
│   ├── implementsmap/  C++ class → FIDL element bridging
│   ├── k8smanifest/    Rendered Kubernetes YAML field inventory
│   ├── markdown/       Generic markdown prose
│   ├── markdowncli/    Per-subcommand markdown reference (docker/cli style)
│   ├── pwfacade/       Pigweed pw_facade() GN parser (build-graph hint)
│   ├── pytest/         Python pytest discovery + ref extraction
│   ├── rusttest/       Rust #[test] attributes
│   └── …               and more (cml, cppheader, protocpp, rst, workflows, …) — `ls internal/adapters/` for the full set
├── internal/buildgraph/  Build-graph recognizer framework
├── internal/indexer/   Cross-reference engine (join logic)
├── internal/analyze/   Findings: tested-undocumented, thin-reference, stale-doc, …
├── internal/cli/       Sub-command implementations
├── internal/mcp/       MCP server
├── internal/prbot/     PR-bot comment renderer + adapters
├── internal/report/    HTML report writer
├── internal/orchestrator/  Pipeline driver
├── proto/              Schema (.proto + generated bindings)
└── utils/scanner/      HTML report generator (consumes MCP)

docs/
├── playbooks/onboard-a-new-repo.md  How a team extends Sheaf to their repo
├── examples/                      Working configs (Fuchsia FIDL, docker CLI)
├── cli/                           Per-binary + per-subcommand reference
├── mcp/                           MCP wire protocol + schema
└── config.md                      Top-level config reference
```

## Extending to a new CLI

See [docs/playbooks/onboard-a-new-repo.md](docs/playbooks/onboard-a-new-repo.md) for an end-to-end walk-through.

The pattern (for cobra-based CLIs):

1. Generate per-subcommand YAML via `doc.GenYamlTree`
2. Write `sheaf.textproto` with `contract_anchor { name: "cobra" ... }`, `rendered_reference { name: "markdowncli" ... }`, and `test_parser { name: "gotest" binary_name: "..." }`
3. Add the project's source map (`categorization-rules.textproto`) bucketing subcommands by family
4. `sheaf scan` and iterate

A reference config for docker CLI is at [docs/examples/docker-cli-coverage-config.textproto](docs/examples/docker-cli-coverage-config.textproto).

## License

Apache 2.0 — see [LICENSE](LICENSE).
