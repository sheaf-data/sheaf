# Sheaf architecture

Sheaf scans a software project's *contract surface* — the things other
people call: CLI flags and subcommands, protobuf and FIDL definitions,
C++ headers, documented workflows — and reports where the documentation
under-covers or has drifted away from the real surface. It also runs an
MCP server so coding agents can ask "what is the real shape of this API?"
before they write code against it.

This document is the architecture overview. It explains the data model,
the adapter pattern, the scan pipeline, and the two main consumers (the
HTML report and the MCP server).

## Design principles

A handful of properties shape everything below:

1. **Thin core, fat ecosystem.** The core engine defines the data model
   and the orchestration pipeline. Everything ecosystem-specific — how to
   read a `.fidl` file, a Cobra command tree, a C++ header — lives in an
   adapter. Adding support for a new format must never require editing the
   core.
2. **Proto-first.** Every shape that crosses a process or storage boundary
   is a protobuf message, defined once in `proto/*.proto`. The same schema
   is serialized as binary proto for caches, textproto for human-authored
   config, and JSON (via `protojson`) for the MCP wire. There is no
   separate "on-disk format" versus "wire format."
3. **Provenance is non-negotiable.** Every contract element, every doc
   claim, every finding carries a `SourceLocation` (`file:line`). If Sheaf
   asserts that something exists, it can point at the exact line that says
   so. This is what makes the output trustworthy to an agent and auditable
   to a human.
4. **One join, many surfaces.** The MCP server, the HTML report, and the
   CLI all read the same in-memory coverage data. A new surface is a new
   transport over the same join, not a new pipeline.

## Core data model

Two record types carry the whole idea, and a third is computed by joining
them.

### ContractElement — the real surface

A `ContractElement` (`proto/contract.proto`) is a single addressable item
that a consumer can call: a FIDL method, a protobuf message, a CLI flag, a
subcommand, a C++ class, a config knob. Each element carries:

- `id` — a canonical, ecosystem-specific identifier
  (e.g. `fuchsia.io/Directory.Open`, `ffx component show --json`).
- `kind` — one of a fixed vocabulary (`METHOD`, `FLAG`, `SUBCOMMAND`,
  `TYPE`, `PROTOCOL`, `CPP_CLASS`, `CONFIG_KNOB`, …). Adapters map their
  native concepts onto these.
- `ecosystem` and `library` — which surface it belongs to.
- `location` — a `SourceLocation` (`path`, `line`, `column`), always
  populated. This is the provenance guarantee in practice.
- `parameters`, `return_types`, `doc_comment_excerpt`,
  `version_constraints`, and `aliases` — the structural detail.
- `relationships` — outbound directed edges to other elements
  (`COMPOSED_FROM`, `IMPLEMENTS`, `ACCEPTS_TYPE`, `SAME_AFFORDANCE`, …).
  Only the outbound direction is stored; the indexer materializes the
  inverse at load time, so queries against either end work.

ContractElements are the ground truth. They describe what *is*, derived
mechanically from source — never from prose.

### DocClaim — what the docs say

A `DocClaim` (`proto/doc_claim.proto`) is a reference *from* a
documentation source *to* a contract element. Both human-authored prose
(markdown, reStructuredText) and pre-rendered reference bundles (fidldoc,
clidoc) produce DocClaims; a `kind` field distinguishes a `REFERENCE`
(rendered) from a `PROSE_MENTION`, an `EXAMPLE`, or a multi-step
`WORKFLOW`. Each claim carries its own `location`, a `raw_text` excerpt, a
`substance` grade (`ABSENT` → `SIGNATURE_ONLY` → `PARTIAL` →
`SUBSTANTIVE`), the producing `adapter`, and the heading stack
(`section_path`) it was found under.

Crucially, a DocClaim's `contract_refs` (the element IDs it points at) are
left empty by the adapter. The adapter only knows "this doc page mentions
this name." Resolving that name to an actual ContractElement ID is the
indexer's job.

### CoverageProfile — the join

Coverage is computed, not parsed. The indexer joins DocClaims (and tests,
examples, usage sites) against ContractElements and, for every element,
produces one `CoverageProfile` (`proto/coverage_profile.proto`):

- `docs` — doc references bucketed by kind (`reference`, `concept`,
  `tutorial`, `guide`, `proposal`, …), each ref keyed by the adapter that
  produced it.
- `tests`, `examples`, `usage` — the same idea for test coverage,
  example code, and call-site usage.
- `implementations` — for interface kinds, the elements that implement
  them (materialized from `IMPLEMENTS` edges).
- `gaps_summary` — the derived verdict: which categories are `missing`
  (zero entries), which are `thin` (present but `SIGNATURE_ONLY`), and any
  `notable` warnings (orphan elements, etc.).

The CoverageProfile is the primary artifact. Every consumer — the MCP
server, the HTML report, the CLI — reads it. "Documentation under-covers
the surface" is, concretely, a CoverageProfile whose `docs` bucket is
empty or thin for an element that exists.

## The one-adapter-per-format pattern

All ecosystem knowledge is isolated in adapters under
`internal/adapters/`, one package per format: `fidl`, `proto`, `cobra`,
`clap`, `argh`, `cppheader`, `cml`, `markdown`, `rst`, `workflows`,
`gtest`, `pytest`, and so on. The core (`internal/adapters/adapters.go`)
defines a small set of interfaces; the orchestrator only ever talks to
those interfaces.

There are two sides to an adapter, matching the two sides of the data
model:

- **The contract side — `Discover`.** A `ContractAnchorParser` reads
  contract source and emits `[]*ContractElement`. The `fidl`, `proto`,
  `cobra`, `cppheader`, and `cml` adapters live here. This is "what is the
  real surface?"
- **The doc side — `Parse`.** A `DocParser` (for prose) or a
  `RenderedReferenceParser` (for pre-rendered bundles) reads documentation
  and emits `[]*DocClaim`. The `markdown`, `rst`, `markdowncli`,
  `workflows`, `fidldoc`, and `clidoc` adapters live here. This is "what
  do the docs say?"

Two more interfaces round out the set: `TestParser` (`Discover` →
`[]*TestCase`, for `gtest`/`pytest`/`bats`/etc.) and `ImplementsMapper`
(`Discover` → impl elements carrying `IMPLEMENTS` edges, bridging C++/Rust
classes back to the interface they serve).

Every interface requires `Name()` and `Version()`. The name is how the
adapter is selected in config and how its output is keyed throughout the
pipeline (a DocRef remembers which adapter produced it). The version
stamps provenance.

**Adding a format is local.** Implement `Discover` (contract) or `Parse`
(doc) against the relevant interface, return canonical proto types with
populated `SourceLocation`s, and register the adapter by name in the
orchestrator's resolver. No core change, no schema change. New
rendered-reference adapters route their refs through the
`CoverageProfile.docs.reference.by_adapter` map rather than adding a typed
field, so the indexer, report, and CLI pick them up without edits.

## The pipeline

The orchestrator (`internal/orchestrator/`) wires a parsed config into a
working scan. At construction it resolves the configured adapter names
into concrete adapter instances (failing fast on an unknown name), and
`Run` drives the stages:

```
  config + categorization rules
            │
            ▼
   ┌──────────────────┐
   │  Orchestrator    │   resolves adapters by name
   └──────────────────┘
            │  (build-graph hints first, then parallel fan-out)
   ┌────────┴────────────────────────────────┐
   ▼            ▼            ▼                 ▼
 contract     test         doc            rendered-ref
 anchors      parsers      parsers        parsers
 Discover     Discover     Parse          Parse
   │            │            │                 │
   └────────────┴─────┬──────┴─────────────────┘
                      ▼
              ┌──────────────┐
              │   Corpus     │  ContractElements + TestCases + DocClaims
              └──────────────┘
                      │
                      ▼   affordance matching (cross-source SAME_AFFORDANCE edges)
                      ▼   categorize (path-rules → taxonomy buckets)
              ┌──────────────┐
              │   Indexer    │  join refs ↔ elements; materialize inverse
              │              │  edges; emit one CoverageProfile per element
              └──────────────┘
                      │
                      ▼   analyze (mechanical gap analyzers → Findings)
              ┌──────────────┐
              │   Result     │  Corpus + CoverageProfiles + Findings
              └──────────────┘
                      │
          ┌───────────┴───────────┐
          ▼                       ▼
     MCP server              HTML report
   (agent grounding)        (humans / CI)
```

Stage by stage:

1. **Ingest (parallel).** Build-graph recognizers run first so their
   hints can inform later adapters (e.g. a header's public/private
   status). Then every contract, test, doc, and rendered-reference adapter
   runs concurrently, draining its output into a shared `Corpus`. A
   per-adapter failure is recorded on the result, not fatal — the scan
   continues with the other adapters.
2. **Affordance matching.** A pass over the assembled elements adds
   `SAME_AFFORDANCE` relationships linking rows from different adapters
   that describe the same underlying capability (e.g. a config knob
   declared both as an env var and a CLI flag).
3. **Categorize.** The `internal/config`-loaded categorization rules
   (path-pattern matchers) assign each reference to one or more taxonomy
   buckets (`tests.integration`, `docs.tutorial`, …). This step is
   mechanical; project-specific ambiguity is resolved by adding rules.
4. **Index.** The indexer (`internal/indexer/`) is the join. For each
   TestCase and DocClaim it resolves the ContractElement(s) it references
   — by canonical name, by name-token overlap, by URL anchor, or by
   following the implements-map — and populates the right bucket of each
   element's CoverageProfile. It materializes inherited methods through
   `COMPOSED_FROM` edges, the inverse of `IMPLEMENTS`, and finally computes
   each element's `gaps_summary`.
5. **Analyze.** Mechanical gap analyzers run over the CoverageProfiles and
   emit `Finding`s (`proto/finding.proto`): documented-but-untested,
   tested-but-undocumented, thin-reference, missing-in-category, and so
   on. No model is required on this path.

The result — corpus, coverage profiles, and findings — is then served.

## Scan → report pipeline

The HTML report (`internal/report/html/`) renders a static site from a
scan: an `index.html` element table with summary cards, a
`findings.html`, and one `elements/<slug>.html` per ContractElement.
Templates are embedded with `go:embed`, so the binary has no runtime file
dependency. Every rendered element and finding links back to its source
`file:line`.

There are two ways to drive a scan into a report, sharing the same
projection of the corpus into a render snapshot:

- **In-process** (`utils/scanner/` `Render`). Loads config, runs the
  orchestrator, projects the corpus, and renders the HTML — all in one
  process, no server. This is the default for a single scan and for the
  monorepo fan-out runner that renders many libraries in one pass.
- **Server-backed.** A long-running `sheaf serve` holds the scan result in
  memory and answers JSON-RPC over HTTP. The scanner CLI
  (`utils/scanner/client.go`) is a thin client against it. The same
  snapshot shape the MCP server emits is what the renderer consumes, so
  the two paths produce identical reports.

## MCP server

The MCP server (`internal/mcp/`) is the agent-facing surface. Its job is
**grounding**: when a coding agent is about to write code against an API,
it can ask Sheaf for the real field set, the real flags, the real method
signatures — rather than guessing from a model's training data and
hallucinating an option that does not exist.

It exposes a small set of tools over JSON-RPC (responses are proto
messages serialized via `protojson`):

- `query_contract` — return a ContractElement and its CoverageProfile (or
  a named subtree: `docs`, `examples`, `usage`, `gaps`).
- `coverage` — return the CoverageProfile for an element.
- `find_coverage_gaps` — list findings, filterable by library and kind.
- `find_examples` — search for elements matching a query (semantic when an
  embedder is configured, else token overlap).
- `verify_invocation` — check whether a concrete invocation string
  (e.g. a CLI command line) matches a real element. This is the direct
  "does this flag actually exist?" check.
- `list_libraries` / `library_snapshot` — enumerate scanned libraries and
  fetch a whole library's surface at once.

Because every element carries a `SourceLocation`, an agent can not only
learn the real shape but cite the exact line that defines it. And because
the same CoverageProfile drives the gap summary, an agent that asks about
an element with empty `tests` or `docs` buckets gets that signal directly
— a cue to flag uncertainty rather than invent coverage.

## Configuration

A scan is driven by two textproto files at the project root, both defined
in `proto/config.proto` and loaded by `internal/config/`:

- **`sheaf.textproto`** — which adapters to run, their per-adapter config,
  and scoping (which libraries, include/exclude globs).
- **`categorization-rules.textproto`** — path-pattern rules mapping files
  to taxonomy buckets.

Adapters are selected by name; the orchestrator validates every name at
construction and constructs the matching adapter with its typed config.
This is the single point where the otherwise format-agnostic core learns
which formats a given project actually uses.

## Where things live

| Concern | Location |
| --- | --- |
| Proto schemas (the data model) | `proto/*.proto` |
| Adapter interfaces | `internal/adapters/adapters.go` |
| One adapter per format | `internal/adapters/<format>/` |
| Scan orchestration | `internal/orchestrator/` |
| The join (CoverageProfile build) | `internal/indexer/` |
| Config loading | `internal/config/` |
| HTML report | `internal/report/html/` |
| Scan + render entry points | `utils/scanner/` |
| MCP server | `internal/mcp/` |
| Binaries | `cmd/sheaf/`, `cmd/scanner/`, `cmd/dump-*` |

Generated Go bindings for the protos are imported as
`github.com/sheaf-data/sheaf/proto/...` and shared by every package, so
the contract between core and adapters is the schema itself.
