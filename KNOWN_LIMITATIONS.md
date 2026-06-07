# Known limitations & non-goals

**Scope:** Sheaf v1 (CLI). **Last reviewed:** 2026-06-07.

Sheaf binds your real contract surface — the CLI flags, RPC methods, schema
fields, and public types your project actually exposes — to the docs, tests, and
usage that prove each one, with every link traceable to the file and line behind
it. Knowing what a tool *is* means knowing what it isn't, and where it's still
rough. Sheaf's own thesis is that the gap is the subject — a project earns trust
by naming its absences as plainly as its presences — so this document holds Sheaf
to that standard. Two parts:

- **What Sheaf is not** — the neighbors it deliberately leaves to better-suited
  tools. Stable scope lines, not a backlog.
- **Known limitations** — where v1 is honestly rough. Each is tagged so you can
  tell a permanent design choice from a gap that isn't built yet, and most point
  you to the workaround.

If a boundary you care about is missing here, that's a finding —
[open an issue](https://github.com/sheaf-data/sheaf/issues).

---

## What Sheaf is not

Every tool sits in a ring of neighbors it gets mistaken for. Naming the ring is
how the category stays sharp — and how you know which tool to reach for when the
job is the neighbor's, not Sheaf's.

| Sheaf is not… | What it does instead — or what to reach for |
|---|---|
| **a vector store / RAG database** | It builds a *typed, addressable* index of named contract elements and the evidence bound to each, served exact over MCP — not embeddings for nearest-neighbor recall. It is the ground truth you would point a RAG pipeline *at*. |
| **a code search engine** | It answers "is *this element* documented, tested, and used, and where's the proof?" — not "where does this string appear?" For text or symbol search, reach for `ripgrep`, ctags, or Sourcegraph. |
| **a CI / test runner** | It reads test *sources* statically and attributes them to the elements they exercise; it never compiles or runs them. Whether a test passes is your CI's job — Sheaf reports whether one *exists and points at the contract*. |
| **a docs writer** | It binds the docs you already have, grades whether they're substantive or just restate a signature, and flags the elements with none. The verb is *bind*, not *write*; it won't compose new prose. |
| **an LLM evaluation framework** | It improves the ground truth an agent reads *before* it answers; it doesn't benchmark models, score prompts, or rank agents. An eval harness measures the model — Sheaf measures the project. (It's not the model.) |
| **a SaaS / cloud service** | It runs as a CLI on your machine, against your repo. No account, no upload, no telemetry; your source never leaves the box. |
| **a code-review tool / linter** | Its PR-bot reports the *coverage delta* of a change — which elements gained or lost docs, tests, or usage between two commits — not whether the code is good. Findings are advisory, never a merge gate. |
| **a debugger / runtime introspection tool** | It is entirely static: no execution, stack traces, profiling, or attaching to a live process. Anything that exists only at runtime must be rendered to an artifact first, then scanned. |
| **a security scanner / SAST** | It maps documentation, test, and usage coverage of a contract surface. An undocumented flag is a fragmentation finding, not a vulnerability — point a real SAST tool at the code for that. |
| **a monorepo orchestration tool** | It doesn't build, schedule, cache, or order tasks across a workspace (Bazel, Nx, and Turborepo own that). `sheaf scan --manifest` fans out one independent scan and report per module, and reads build files only to recognize structure, never to drive the build. |

These are refusals by design. A sharp core stays sharp because it says no — every
row above is a scope line that keeps the join honest instead of growing a second
product beside it.

---

## Known limitations

These are current, not eternal. Status tags: `by design` — a consequence of the
approach, unlikely to change · `not yet built` — specified and wanted, not yet
shipped · `under consideration` — wanted, not committed.

### What the coverage numbers do and don't mean

**1. Attribution is heuristic — expect false positives and false negatives.** The
join between a contract element and its tests, docs, and usage is name-token and
reference matching, not semantic understanding. Two failure modes follow.
*Over-match:* a short, common name (`run`, `build`, `status`) attracts tests and
prose that don't actually exercise *that* element. *Under-match:* a test that
drives the contract from a distance — an integration or end-to-end suite, or a
fixture named for a scenario rather than the element — goes unattributed, so a
CLI whose testing is mostly integration will read as under-tested even when it is
not. Read the coverage matrix as a high-signal worklist to spot-check, not an
oracle; per-library precision and recall can be measured by sampling the
attributions. `by design` — matchers improve per adapter, but the approach stays
mechanical-first and checkable rather than a black-box guess.

**2. A scanned surface is an inventory of what the adapter can see — not a proof
that the surface is complete.** Each anchor reports the elements it can resolve
from the inputs you give it: a derive-macro CLI walker sees `#[derive(...)]`
commands but not a builder-style API assembled at runtime; a markdown extractor
reads references only from the code-block languages you configure; a published
schema reflects what its author wrote, which can lag the implementation. The
result is a faithful inventory of *this input*, not a guarantee that every
element the system can expose is present. When completeness matters, point Sheaf
at the most authoritative input available. `by design`

**3. A populated cell means a claim points here — not that the artifact is right.**
A green docs or tests cell means an artifact *references* the element.
Substance grading (signature-only / partial / substantive) and stale-doc
detection are heuristics over word counts and last-touched timing — a doc can be
bound and still teach a wrong signature. Sheaf surfaces the *shape* of drift and
ranks it; it does not certify that prose is accurate. Read a green cell as "a
receipt exists," then follow the file:line and judge the artifact yourself.
`by design`

**4. The join is inline — documentation hosted elsewhere is invisible.** The doc
side reads the artifacts you point it at: markdown, rendered reference bundles,
inline schema descriptions. Documentation that lives off to the side — an
external docs portal, a wiki, a hand-kept reference table the adapter doesn't
parse — is not read, so an element documented only there shows as undocumented.
That is an honest finding with respect to *the docs that travel with the
contract* (exactly what an agent reading the repo gets), not a claim that no docs
exist anywhere. `by design` — adapters for specific high-value external sources
are `under consideration`.

### Where the tooling is still rough

**5. Per-claim provenance isn't fully surfaced yet.** Sheaf's trust model is "show
the receipt," and the report doesn't yet show all of it. *Shipped and checkable
today:* the mechanical joins resolve to file:line, and mechanically-derived
findings are separated from LLM-derived ones at the section level. *Not yet
built:* a mechanical (●) / LLM (○) / hybrid (◐) badge on *every* claim,
confidence scores on the drift findings that drive the worklist (not only in the
anomalies section), per-finding analyzer attribution, a "how this number was
computed" disclosure on each headline figure, and a signed JSON export to
re-derive every number independently. This is the gap that most affects a
reviewer auditing the methodology before trusting it. `not yet built`

**6. Format coverage is finite — and teaching Sheaf your stack is the
contribution.** Sheaf understands the formats it has adapters for: cobra / clap /
argh CLIs, protobuf / gRPC, FIDL, C++ public headers, config and schema surfaces,
and a handful of test parsers (gtest, pytest, Rust, Go, bats) and doc parsers
(markdown, reStructuredText, rendered-reference bundles). Surfaces it doesn't yet
read mechanically — GraphQL, Cap'n Proto, Thrift, OpenAPI, builder-style CLI
APIs, and bespoke hand-rolled contracts — need a new adapter. That is the single
best way to contribute: adding one is a focused, self-contained piece of work, not
a rewrite. The [onboarding playbook](docs/playbooks/onboard-a-new-repo.md) is the
on-ramp, and [CONTRIBUTING.md](CONTRIBUTING.md) covers the rest; the `llmextract`
adapter is the escape hatch for schemaless contracts (with the caveats in #1 and
#5). `by design` — new adapters land as adopters bring their stacks.

**7. Setup is real work; the config is hand-tuned textproto.** `sheaf init`
scaffolds a starter `sheaf.textproto` plus a source map and auto-detects common
project shapes — but tuning the source map (which paths produce which kind of
evidence) to a real tree is an afternoon's work, not a five-minute demo, and the
honesty of the report tracks the honesty of those categories. See the
[configuration reference](docs/config.md) and the
[onboarding playbook](docs/playbooks/onboard-a-new-repo.md) to go faster. v1 also
deliberately omits user-global config, per-environment overlays, templating
beyond `${ENV_VAR}` in path fields, hot-reload during a running `sheaf serve`, a
relocatable source map, and glob-based ownership scope. `by design` (v1 config
scope).

**8. The HTML report is a two-process flow today.** The single-file report is
produced by the `scanner` binary talking to a running `sheaf serve` MCP server —
two processes, not one command (the exact invocation is in the
[README quick start](README.md#quick-start)). A one-shot `sheaf report --output
report.html` is the intended on-ramp and not yet the documented path. The MCP
server itself is first-class and stays — see the [MCP API](docs/mcp/api.md); this
is about report convenience for a first-time reader. `not yet built` (the
one-shot convenience).

**9. One machine, one repo, in-memory, rebuilt each run.** A scan builds the
corpus in memory and renders a point-in-time snapshot; there is no persistent
index or server-of-record. The on-disk [cache](docs/config.md) is
content-addressed build cache, not a queryable store. Scale is bounded by what
fits in memory, and a monorepo fan-out is N independent scans — no cross-module
corpus, no incremental re-index between runs. `by design` (the snapshot model is
deliberate) / `under consideration` (scale and incremental indexing).

**10. LLM features are optional, and the hosted-client story is partial.** Nothing
on the core scan → index → report path needs an LLM: the contract anchors and the
inline doc joins are mechanical. The LLM-assisted steps — semantic example search,
substance scoring, `llmextract` — are opt-in and fall back to deterministic
token-overlap when no model is configured. The Anthropic client is wired; other
hosted clients are reserved in the [config schema](docs/config.md) but rejected at
startup. So your semantic features vary with your configuration. `by design`

---

We publish these for the same reason the report names your gaps: a boundary you
can see is one you can plan around, and a tool that hides its rough edges earns
less trust than one that names them. Sheaf scans itself and publishes its own gaps
too — see the [self-scan report](example-reports/sheaf-self.html). Found one we
missed? That's a finding — [open an issue](https://github.com/sheaf-data/sheaf/issues).
