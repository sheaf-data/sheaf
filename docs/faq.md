# FAQ & troubleshooting

Short answers to the questions that come up most, and a symptom → cause → fix table for the failure modes that actually bite. For the design boundaries behind several of these answers, see [KNOWN_LIMITATIONS.md](../KNOWN_LIMITATIONS.md).

## FAQ

### What does Sheaf actually do?

It maps your project's real contract surface (every CLI command, API method, config knob, schema field) and shows, element by element, which ones a doc explains, a test verifies, and a working example demonstrates. Each populated cell links to the file:line behind it. The empty cells are the fragmentation an agent guesses across and a human opens three tabs to resolve.

### Do I need an LLM or an API key?

No. The core join (contract ↔ tests ↔ docs) is fully deterministic and runs with no model. The quick start (`sheaf scan --auto --llm-backend none`) contacts nothing. Two features *optionally* use a model when one is configured: semantic ranking in `find_examples`, and substance/claim extraction. Without a model they fall back to deterministic token-overlap. As of v1 only a local **ollama** backend (embeddings + generation) and the **Anthropic** generation client are wired; other hosted clients are reserved in the config schema but rejected at startup (see [KNOWN_LIMITATIONS.md #10](../KNOWN_LIMITATIONS.md#known-limitations)).

### Does my source code leave my machine?

No. Sheaf runs as a CLI against your local checkout: no account, no upload, no telemetry. The only exception is one you opt into explicitly: if you configure a *hosted* LLM (the Anthropic client) for the attribution pass, the text sent for that pass goes to that API. The deterministic path and the local-ollama path send nothing off the box.

### How do I verify a release download is authentic?

Tagged releases (v0.1.0+) ship signed checksums and SBOMs. Signing is keyless (Sigstore cosign) against the release workflow's GitHub OIDC identity, logged in the public Rekor transparency log, so you verify *what produced the artifact*, not a key you have to obtain. The `cosign verify-blob` + `sha256sum -c` recipe is in [docs/verifying-releases.md](verifying-releases.md).

### How is this different from a vector store / RAG, or from code search?

A RAG database stores embeddings for fuzzy nearest-neighbor recall; Sheaf builds a *typed, addressable* index of named contract elements and the evidence bound to each, served exact over MCP. It's the ground truth you'd point a RAG pipeline *at*. And unlike code search (ripgrep, Sourcegraph), it answers "is *this element* documented, tested, and used, and where's the proof?", not "where does this string appear?" The full set of neighbors Sheaf deliberately isn't (linter, CI runner, SAST, docs writer) is in [KNOWN_LIMITATIONS.md → "What Sheaf is not"](../KNOWN_LIMITATIONS.md#what-sheaf-is-not).

### What languages and formats are supported?

Contract anchors: cobra (Go CLIs), clap and argh (Rust CLIs), protobuf/gRPC, FIDL, C++ public headers, and config/schema surfaces (Kubernetes CRDs, Helm values). Test parsers: `gotest`, `gtest`, `protocpp`, `rust-test`, `pytest`, `bats`. Doc parsers: `markdown`, `markdowncli` (per-command reference), `rst` (Sphinx), `fidldoc`. The current list is always `ls internal/adapters/`.

### My format isn't supported (GraphQL, Thrift, OpenAPI, a custom test macro). What now?

You're writing an adapter, and that's the single best way to contribute: a focused 200-600 LOC package against one interface, not a rewrite. See [CONTRIBUTING.md → Adding a new format adapter](../CONTRIBUTING.md#adding-a-new-format-adapter) and [Stage 7 of the onboarding playbook](playbooks/onboard-a-new-repo.md). For a schemaless one-off, the OpenAPI snapshot-script route in [scan-your-repo.md](scan-your-repo.md) builds the join externally.

### What does a green (populated) cell mean?

That a claim *points at* the element (a doc references it, a test exercises it, an example uses it) with a file:line you can click. It does **not** certify the artifact is correct: a doc can be bound and still teach a wrong signature. Read a green cell as "a receipt exists," then follow the link and judge the artifact yourself ([#3](../KNOWN_LIMITATIONS.md#known-limitations)).

### Why does an element I know is tested read 0 tests?

Under-match. Attribution is name-token and reference matching, not semantic understanding. A test that drives the contract from a distance (an integration/e2e suite, or a fixture named for a scenario rather than the element) goes unattributed. A CLI whose testing is mostly integration will read as under-tested even when it isn't. Confirm with a spot-check; if a whole class of tests is missed, the adapter may need `extra_test_macros` or an explicit `include` (see Troubleshooting).

### Why does a common command like `run` or `build` show tests it shouldn't?

Over-match: the mirror failure. A short, common name attracts tests and prose that don't exercise *that* element. This was tightened for FLAG/METHOD/TYPE/PROTOCOL kinds, but SUBCOMMAND/SERVICE/LIBRARY still use the name-token fallback. Tighten `scope.library`, or suppress known helper-test directories via `analyzer { suppress_for_paths: ... }`.

### Can I run Sheaf in CI as a merge gate?

Treat the report as advisory. Sheaf has no severity-threshold gate; the only fail switch is `sheaf scan --manifest <file> --fail-on-error`, which fails on a scan/render *failure*, not on findings. Gating PRs on coverage findings early surfaces false positives and burns trust; wire the PR-bot delta comment instead, and only after the report has run in cron long enough that the maintainer trusts it ([playbook Stage 8](playbooks/onboard-a-new-repo.md#stage-8-schedule--integrate)).

### How do I reproduce a published report's numbers?

Every sample report ships the config + rules that produced it. Clone the target at the pinned SHA, point Sheaf at the same config, and re-derive in-process (no server):

```sh
sheaf snapshot --config docs/examples/<sample>-coverage-config.textproto --library <lib> --out /tmp/s.json
sheaf render --from-snapshot /tmp/s.json --ecosystem <ecosystem> -o report.html
```

See [docs/examples/REPRODUCIBILITY.md](examples/REPRODUCIBILITY.md) for pinned SHAs and the per-sample recipe.

### How does a coding agent use Sheaf?

Through the MCP server: `sheaf serve --repo .` keeps the scanned corpus live, and the agent queries contract elements, coverage profiles, and worked examples per element before it answers, grounding on the real surface instead of a plausibility. The wire protocol and every operation are in [docs/mcp/api.md](mcp/api.md).

### Can I plug `sheaf serve` into Claude Desktop / Cursor as an MCP server today?

Not yet through their standard config. `sheaf serve` speaks Sheaf's JSON-RPC dialect over HTTP at `POST /mcp` (default `127.0.0.1:7700`); it does **not** yet implement the MCP stdio transport or the `initialize`/`tools/call` handshake those desktop clients expect, so pasting the URL into `claude_desktop_config.json` won't connect. Today you drive it programmatically: the `scanner` binary (`scanner --server http://127.0.0.1:7700 …`), `curl`, or any HTTP client (see the [api.md examples](mcp/api.md#quick-examples)). Stdio transport and MCP wire-conformance are tracked for a post-launch release; first-class desktop-client recipes land with them.

### Is my config reusable across repos? Is there a global config?

v1 is one `sheaf.textproto` per repo. There's no user-global config, no per-environment overlays, and templating is limited to `${ENV_VAR}` in path fields. A source map isn't relocatable: the CLI resolves `categorization-rules.textproto` at the *scan target's* root (this trips people up; see Troubleshooting). These are deliberate v1 scope lines ([#7](../KNOWN_LIMITATIONS.md#known-limitations)).

## Troubleshooting

Symptoms, in rough order of how often they bite. Most "Sheaf is wrong" reports are config-shaped: a glob, a missing source map, or the wrong `--ecosystem`.

| Symptom | Cause | Fix |
|---|---|---|
| `sheaf doctor` shows an adapter **"matched 0 files"** | The adapter's `include` glob doesn't match any path (typo, `docs/yaml` vs `docs/yamls`, absolute vs repo-relative). | `ls` the include pattern by hand. Fix the glob in `sheaf.textproto`, re-run doctor. |
| Files matched but **0 elements extracted** | Wrong adapter for the file format. | Open a matched file; check it for the adapter's markers (`command:` for cobra YAML, `service`/`rpc` for proto, `protocol` for FIDL). |
| Doctor is green, scan completes, but **every `docs.*` surface reads 0** | The source map wasn't staged at the scan target's root. The scan prints `no source map (categorization-rules.textproto) found` and skips bucketing; doctor doesn't flag it as fatal. | Symlink (don't copy) the rules file into the scan target before scanning: `ln -sf $SRC/<project>-coverage-rules.textproto $REPO/categorization-rules.textproto`. Then read the scan's warning lines, not just doctor's summary. |
| Report masthead reads **"Zero Services · Zero Methods"** for a library you know has a surface | Wrong `--ecosystem` at render time. `proto` (services + methods) and `proto-config` (top-level message types only) are not interchangeable. | Render with the ecosystem that matches the library's *primary* surface: `proto-config` for pure data-schema libraries (Listener/Cluster), `proto` for gRPC/xDS services. A repo with both shapes renders twice. |
| Report is a **200-row flat table** nobody can navigate | No source map, or one category matching everything (`paths: "**"`). | Write categories by *feature area* (one per subsystem, many globs each), not one-per-file and not one-for-all. See [playbook Stage 4](playbooks/onboard-a-new-repo.md#stage-4-write-the-source-map-categorization-rulestextproto). |
| `find_examples` returns `token-overlap` scores, not semantic ones | No embedder configured, or the configured ollama host/port is unreachable. | Add the `llm { embeddings: "ollama-embed" ollama_embeddings { ... } }` block; `sheaf doctor` reachability-probes it, so a wrong host/port surfaces there. |
| A standard MCP client (Claude Desktop, Cursor) **won't connect** to `sheaf serve` | The server speaks HTTP JSON-RPC at `/mcp`, not the MCP stdio/`initialize` handshake those clients expect (post-launch work). | Use the HTTP API directly: `scanner --server http://127.0.0.1:7700`, or `curl` per the [api.md examples](mcp/api.md#quick-examples). |
| Headline coverage looks **too good** | You trusted the number before auditing the receipts. | Spot-check ten random rows: click each cited file:line and confirm the bridge holds. Two wrong out of ten means a corpus problem, not a coverage win. |
| A whole class of tests (e.g. `TYPED_TEST`, `TEST_P`) is **missing** | The test adapter drops macros it doesn't know. | Wire `extra_test_macros` under the `gtest`/`protocpp` block (envoy needs this for ~42 `TYPED_TEST`s). For non-conventional paths, set the parser's `include` explicitly. |

Still stuck? Open a [Discussions Q&A thread](https://github.com/sheaf-data/sheaf/discussions/categories/q-a) (config questions) or a [bug report](https://github.com/sheaf-data/sheaf/issues/new?template=bug_report.yml) (a wrong number with the cited file:line that doesn't hold up).
