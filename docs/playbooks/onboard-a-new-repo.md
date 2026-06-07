# Playbook: Onboard a new repo to Sheaf

**Audience:** A coding agent (Claude, Cursor, Codex, Cline) — or a human working with one — wiring Sheaf up against a repo it has never seen. This is the long form. `docs/scan-your-repo.md` is the human-facing index. **This** doc is the agent cookbook: it assumes you're starting from zero, asks you to do reconnaissance before configuring anything, and walks every realistic variant of "what shape is this repo?"

**Why it exists:** every repo we have onboarded so far has been a snowflake. No two have looked alike. The matrix in `docs/scan-your-repo.md` covers the *contract surface* axis, but in practice agents also stumble on:

- doc layouts the matrix doesn't enumerate (Sphinx-RST, MkDocs, a single giant README, Notion exports)
- build systems that hide proto/FIDL inputs behind generated trees (`bazel build //...`, `make yamldocs`, `buf export`)
- test frameworks the adapter set has but the agent didn't recognise (gtest macros wearing project-specific names; pytest with custom collectors; Rust integration vs unit splits)
- monorepos where the target *library* is one of forty things in the tree
- source maps written with the alphabetical-default mindset instead of grouping by feature area

The fix is not "more matrix rows." The fix is a procedure that always starts with "look first," carries a decision tree at each step, and refuses to render a report until the corpus shape has been validated by a human eye.

> **Bias for stopping when you're guessing.** Sheaf's failure mode is *silent under-count*, not crash. If you write globs that match nothing, the doctor flags it; if you write globs that match *the wrong things*, the report renders cleanly and the reviewer who spot-checks ten rows discards everything. Agents tend to commit to a guess and move on. Don't. Re-read [Stage 0](#stage-0-reconnaissance) every time you feel certain after thirty seconds of looking.

---

## What "onboard" means here

End state of a successful onboarding:

1. `sheaf.textproto` and `categorization-rules.textproto` live at the target repo's root (or in a `docs/examples/<project>/` subdirectory if the repo is sheaf itself).
2. `sheaf doctor --repo $REPO_ROOT` is green — every adapter reports >0 matched files and >0 extracted elements.
3. `sheaf scan --repo $REPO_ROOT` reports an element count consistent with the maintainer's mental model. (A 200-method API yielding 12 elements means you're done with the agent work and starting over.)
4. The rendered HTML report's first ten random rows survive a click-through audit: every claimed bridge actually exists in the cited file.
5. The source map groups elements by feature area, not alphabetical bucket.
6. The findings list is non-empty (that's the feature) but doesn't dominate (≥80% of findings should be ones the maintainer agrees with).

If you ship before all six, the maintainer's first reviewer kills the project.

---

## Stage 0: reconnaissance

**Do not edit a single file in this stage. Do not run `sheaf init`. The point is to understand the repo shape before letting a template push you toward the wrong adapter.**

Spend 10–20 minutes answering these questions, in order. Record the answers in a scratch note you'll reference when you write the config — the act of writing them out catches "I'm guessing" before it becomes "I committed a wrong glob."

### Q1. What is the contract this repo defines?

The contract is the artifact that downstream users *bind against* — the surface whose stability the project commits to. It's almost always one of:

- **CLI** — the binary's subcommand tree + flags. Stable across releases (`yourtool foo --bar` keeps working).
- **RPC / API schema** — `.proto`, `.fidl`, OpenAPI, GraphQL, capnproto. Wire-level contract; breaking changes are versioned.
- **Library API** — exported functions/types of a Go package, Rust crate, Python module. Stable per semver.
- **Data model / schema** — a SQL schema, a JSON-Schema, a Pydantic model published as authoritative input shape.
- **Configuration surface** — a YAML / textproto schema (Helm chart values, GitHub Actions workflow YAML, k8s CRD). Stable in the sense that user files keep working.

You can tell which by asking: *what would a user file a bug against if it changed?* If the answer is "the CLI flag I rely on disappeared," it's a CLI. If it's "the gRPC method I call started returning a different type," it's RPC. If it's "my Helm values stopped parsing," it's configuration.

**Many repos have more than one contract.** A repo that ships a CLI *and* a library *and* a JSON-output schema has three. Pick the one the maintainer cares about most for the first scan and stage the others. Don't try to wire all three at once — adapter debugging is per-surface and you'll deadlock yourself.

**If the repo has no obvious contract** — it's a service binary, an app, a script collection — then Sheaf is the wrong tool. Sheaf joins contracts to docs/tests; without a contract there is nothing to join. Say so to the maintainer instead of inventing one.

### Q2. Where does the contract live in the source tree?

For each contract surface you identified in Q1, find the actual files. Examples:

- CLI on cobra: the file that constructs the root `*cobra.Command`. Usually `cmd/<tool>/main.go` or `cmd/<tool>/root.go`.
- CLI on clap: the file with `#[derive(Parser)]` on the top-level struct. Usually `src/cli.rs` or `src/main.rs`.
- CLI on argh: any file with `#[derive(FromArgs)]`. Often spread across `src/commands/*.rs`.
- Proto: `.proto` files. Watch for `api/`, `proto/`, `protos/`, `idl/`, or per-service subdirs.
- FIDL: `.fidl` files. Almost always under `sdk/fidl/<library>/`.
- OpenAPI: a single `openapi.yaml` / `openapi.json`, or a Stoplight-style multi-file split. Sometimes generated from code annotations and published to a separate docs repo.

**Watch for "generated tree" indirection.** A docker-style repo emits cobra YAML via `make yamldocs` into `_output/yaml/`. An envoy-style repo emits a flattened proto bundle via `buf export api/ -o api-export/`. The adapter often reads the *generated* tree, not the source. Find the generator command and make sure you can run it before you write the config — if it requires a build container, a custom toolchain, or a CI artifact, that becomes a prereq in the playbook.

If the contract lives in a sibling repo (gRPC's `.proto` defined upstream in `googleapis` and vendored here as `third_party/grpc-proto/`), pick which copy is canonical. Scan the canonical one.

### Q3. Where do tests live, and what framework do they use?

Walk the test tree by hand. Look for:

- File suffix conventions: `_test.go`, `*_test.cc`, `*_unittest.cc`, `test_*.py`, `tests/*.rs`, `*.bats`.
- Macro vocabulary: `TEST(`, `TEST_F(`, `TEST_P(`, `TYPED_TEST(`, `#[test]`, `#[tokio::test]`, `func Test`, `it(`, `describe(`.
- Whether tests sit next to code (`foo.go` + `foo_test.go`) or under a parallel tree (`src/foo.rs` + `tests/foo.rs`).
- Whether there's a *conformance suite* separate from unit tests (Fuchsia's `src/storage/conformance/`, gRPC's `interop_test`). These deserve their own bucket in the source map.
- Whether the project uses tags / build constraints to gate tests (`//go:build integration`, `#[cfg(feature = "slow")]`). The adapter usually doesn't care, but the source map might want to bucket them separately.

The Sheaf test parsers available today are `gotest`, `gtest`, `rust-test`, `protocpp` (proto-aware gtest variant), `pytest`, and `bats`. If your project's tests use anything else, you're writing a new adapter — see [Stage 7](#stage-7-when-you-need-a-new-adapter).

### Q4. Where do docs live, and in what format?

The hardest axis. Possible shapes:

- **One markdown file per command/method** (docker, kubectl, gh). `markdowncli` adapter, easy.
- **Single README + per-package READMEs** (most Rust crates, many Go libraries). `markdown` adapter, but expect command-level joins only, not per-flag.
- **Sphinx / reStructuredText tree** (envoy, most Python projects). `rst` adapter. Look for `docs/source/conf.py` or `docs/root/`.
- **MkDocs / Hugo / Jekyll site** under `docs/` or `site/`. Usually markdown but with custom frontmatter; treat as `markdown` adapter and inspect a couple of pages.
- **Generated reference site** (fidldoc, doxygen, rustdoc). The `fidldoc` adapter exists for the Fuchsia case; for doxygen / rustdoc / generic HTML you're writing a new rendered-reference adapter.
- **External docs site** (Grafana hosts docs at grafana.com/docs/; the source isn't in the repo). You'll either git-clone the docs repo too, or write a snapshot script that builds the join externally — see the OpenAPI route in `docs/scan-your-repo.md`.
- **No docs.** Plenty of repos ship a README and call it done. That's fine; the report will surface that as a finding, which is itself useful.

**Note both the format and the convention.** Two markdown trees can have wildly different join shapes — one with a `## Options` table per page (per-flag joins work) and one with bullet lists (per-flag joins don't, until you write a custom parser).

### Q4b. If the project has yaml / json config examples, are they fully-typed?

Run `grep -rl "type.googleapis.com\|'@type':\|\"@type\":" $DOCS_ROOT | wc -l` against the doc tree. If the count is in the dozens, the `yaml-workflows` adapter will surface the composition layer correctly. If it's near zero, the project ships short-form yaml (`listeners:`, `clusters:`, `cluster_name:` — field names without `@type:` annotations on each block) and `yaml-workflows` will silently find almost nothing, because it relies on `protomatch` which extracts dotted FQDNs only. Note this in the onboarding ticket — workflow coverage will be a known under-count, and you may want to hand-write a yaml→type map (Stage 7) or skip the workflow surface entirely.

Envoy is the canonical case: ~8 rst files compose `listeners:` + `clusters:` in yaml blocks, but only `docs/root/configuration/overview/examples.rst` (the one page that types every block) currently surfaces as a workflow.

### Q5. Does the repo have a data-model surface worth scanning?

This is the question the matrix in `docs/scan-your-repo.md` doesn't ask explicitly. Many repos publish a data model that *is* the contract — and which is invisible to a CLI-shaped or RPC-shaped scan:

- A SQL migrations directory (`migrations/*.sql`, `db/schema.sql`) — the schema is the contract; tests are ORM model tests; docs are usually a schema doc page.
- A JSON-Schema directory (`schemas/*.json`) — used to validate inputs in a CLI or HTTP API.
- A protobuf *type* surface (not just RPC methods) — the message types are themselves the contract for control planes (envoy's `Listener`, `Cluster`, `RouteConfiguration`).
- A Pydantic / dataclass / TypeScript-types module that's `__all__`-exported and depended on by users.

Sheaf has no first-class data-model adapter today (this is a known gap). Two workarounds:

1. **Treat the schema as proto.** If the project already generates proto from the schema, point the proto adapter at the generated `.proto`. envoy's config-TYPE-as-contract pattern is the worked example.
2. **Treat the schema as a docs surface.** Set up the contract anchor against whatever does have a stable surface (the CLI, the API), and let the `markdown` / `rst` adapter pull schema docs into `docs.concepts`. The schema won't appear as a contract element, but mentions of it in tests will surface as evidence on the CLI commands that consume it. This is a degraded mode but useful.

If neither workaround fits and the data model is the *primary* contract, this is a "write an adapter" project. File an issue with five lines of the schema and five lines of one test before doing anything else.

### Q6. Is this a monorepo, and if so what's the unit of scan?

A monorepo (Fuchsia, k8s, the Google-style Bazel tree) has many libraries. The unit of a Sheaf scan is one project = one `sheaf.textproto`. Don't try to wire one config that scans everything.

For monorepos:

- Pick **one library** as the first scan target. Usually the one the maintainer asked about, or the one with the cleanest surface.
- Use `scope.library` to filter — see the fuchsia-io / fuchsia-driver-framework configs.
- Plan to wire additional libraries as separate `sheaf.textproto` files (under `docs/examples/<library>/` or wherever the project conventionally puts these), each with their own source map.
- Resist the urge to make a "monorepo super-config." Per-library configs cost a tiny amount of duplication and buy clean separation of findings — if you cron a weekly scan, each library gets its own report URL.

### Q7. What does the maintainer think "useful" looks like?

If you have access to the maintainer, ask one question before configuring anything:

> When the first report renders, what's the *one thing* you want it to surface that you can't currently see?

The answers cluster:

- "Which commands are undocumented" → emphasize `tested-undocumented` analyzer; make sure the doc adapter is robust.
- "Which features have no integration tests" → emphasize `documented-untested`; make sure the test adapter catches integration tests, not just unit tests.
- "Whether our API is internally consistent" → emphasize the cross-reference findings (thin-reference, missing-in-category).
- "How much of the surface is actually in production examples" → make sure `examples/` is wired into the source map as its own surface.

If you don't have access to the maintainer, default to the docker/cli or kubectl shape — it's the most-validated configuration and tends to surface useful findings on day one.

### Q8. What backs the LLM-dependent steps — and is anything installed already?

The LLM is **optional**. Sheaf's core join (contract ↔ tests ↔ docs) is fully deterministic and runs with no model at all. Two features lean on a model *when one is configured*:

- **`find_examples` semantic ranking** (v1) — embeddings rank candidate example snippets by relevance. With no model, Sheaf falls back to deterministic token-overlap scoring.
- **HITL claim extraction** (v2+) — text generation proposes claims for human review.

Decide the backend before you wire the config — and check what's already on the machine first, since that decides whether there's an install step at all:

1. **A local model is already installed → use it.** If the operator already runs ollama or has a qwen model pulled, point Sheaf at it: no extra setup, no API key, nothing leaves the machine. This is the preferred path whenever it's available. Check with `ollama list` or `curl -s localhost:11434/api/tags`.
2. **Nothing installed → install ollama locally, *or* use a hosted account.** Either `brew install ollama && ollama pull qwen2.5:7b-instruct` for a local backend, or plug in an existing **Gemini / Claude / OpenAI** account. **Caveat — as of v1 only the local ollama backend is wired in code** (`local-llama` for generation, `ollama-embed` for embeddings). The hosted-API clients were stubbed out of the schema (config.proto reserves their field numbers) and `sheaf` rejects any `client:` other than `local-llama` / `noop`. If the operator wants to use a hosted account, that's a "wire the adapter" task (Stage 7), not a config tweak — flag it rather than writing a config that errors at build time.
3. **Neither, and that's fine → `noop`.** Leave the `llm` block out (or set `client: "noop"`). You lose semantic example ranking; every other surface works unchanged.

See Stage 3's optional edits for the `llm` config block, and Stage 5 for the doctor's reachability probe.

---

## Stage 1: pick the adapter set

After Stage 0 you have answers to Q1–Q7. Now choose adapters. The decision tree:

```
Contract anchor (one)
├── CLI on cobra ............... cobra        (Go, reads doc.GenYamlTree output)
├── CLI on clap ................ clap         (Rust, reads source #[derive(Parser)])
├── CLI on argh ................ argh         (Rust, reads source #[derive(FromArgs)])
├── Proto / gRPC / xDS ......... proto        (reads .proto via protoc)
├── FIDL ....................... fidl         (reads .fidl)
├── OpenAPI .................... [no adapter; write a snapshot script]
└── Anything else .............. [write an adapter; see Stage 7]

Test parser (one or more)
├── Go ......................... gotest
├── C++ on googletest .......... gtest (or protocpp if it's proto-aware C++ tests)
├── Python via pytest .......... pytest    (Python source, AST-free regex scanner)
├── Rust ....................... rust-test
└── Bash via bats .............. bats

Rendered-reference parser (zero or one)
├── One-markdown-per-cmd ....... markdowncli
├── Sphinx RST ................. rst
├── fidldoc tree ............... fidldoc
└── Anything else .............. markdown (degraded — command-level only)

Doc parser (zero or more) — for concept docs
├── Markdown ................... markdown
└── Sphinx RST ................. rst

Implements-map (zero or one) — bridges interface elements to impl classes
├── C++ wireserver pattern ..... cpp-fidl-wireserver via implements_map block
└── (other patterns) ........... not generic yet; ad-hoc for each new shape
```

**Rules of thumb:**

- Pick the *smallest* working set. Adding adapters always adds findings; adding the wrong adapter adds noise.
- The contract anchor is non-negotiable — without it, no elements, no report.
- The test parser is required if any "tested" claim should ever surface. (You can skip it for a docs-only audit but the report will be lopsided.)
- The rendered-reference parser is what lets you make per-flag / per-method bridges. Without it you get command-level / service-level joins only.
- The doc parser (concept docs) is what lets `docs.concepts` populate — usually the README, CONTRIBUTING, narrative guides. Often optional for v1.
- The implements-map adapter only matters for interface contracts (FIDL, proto) where tests live on the *impl class*, not the interface. Think carefully before deciding to wire this — it's been a major source of false positives historically.

**Analyzer set — start with the four-pack:**

```textproto
analyzer { name: "thin-reference"      severity: WARNING }
analyzer { name: "documented-untested" severity: INFO }
analyzer { name: "tested-undocumented" severity: INFO }
analyzer { name: "missing-in-category" severity: WARNING
  config { key: "alert_for_categories" string_value: "tests.unit_tests" }
}
```

`missing-in-category` needs the source map to be wired first — defer wiring it until Stage 4.

---

## Stage 2: produce the inputs

If the contract anchor's input doesn't already exist in the repo (cobra YAML, buf-exported proto bundle), generate it before writing `sheaf.textproto`. Failing this step before adapter wiring saves you from chasing "matched 0 files" errors that are actually missing-input errors.

### Cobra YAML

```go
// In your CLI's repo, add a docsgen entry point (or use the project's existing one).
package main

import (
	"os"
	"github.com/spf13/cobra/doc"
)

func generateYAML() {
	rootCmd := newRootCmd()
	if err := os.MkdirAll("docs/yaml", 0o755); err != nil { panic(err) }
	if err := doc.GenYamlTree(rootCmd, "docs/yaml"); err != nil { panic(err) }
}
```

Run it. Confirm `docs/yaml/<binary>_<subcmd>.yaml` exists for at least one subcommand. Open one and check for the keys `command:`, `short:`, `long:`, `options:`, `inherited_options:`.

### Buf-exported proto

```sh
cd $REPO_ROOT/api
buf export . -o ../api-export
```

The export bundles the transitive closure (googleapis, xds, validate, …) so `protoc` can resolve every import with a single `-I api-export`. If buf isn't installed (`brew install bufbuild/buf/buf`), tell the user; don't proxy past it.

If the project ships proto without a buf config, you'll need to write one (`buf.yaml` with the right `deps`) or fall back to manually populating `proto_path` with every import root. The buf path is much less brittle.

### Rust source for clap / argh

Nothing to generate. The adapter reads source. Find the file(s) with the derive macros — `grep -rEl '#\[derive\(.*(Parser|FromArgs).*\)\]' src/`.

### fidldoc / generated reference

These usually live in a separate build-system output tree. The Fuchsia integration documents the path conventions; for a non-Fuchsia project using doxygen-style generated reference, you're writing a new rendered-reference adapter.

### OpenAPI snapshot

Read the [grafana example](../examples/grafana/build_grafana_snapshot.py) end-to-end. The snapshot script is project-specific by design; don't try to share one across projects. The mechanical parts are the schema walk and the file emission — the *override tables* (which doc file attributes to which tag, which test file attributes to which endpoint) are the actual work, and they vary per repo.

### Stage the source map

If your config + source-map files live anywhere other than the scan target's repo root (the common case for example/template configs in sheaf's own tree, vendored shared configs, monorepo-of-configs layouts), symlink `categorization-rules.textproto` into the scan target before running anything:

```sh
ln -sf $SHEAF_REPO/docs/examples/<project>-coverage-rules.textproto \
       $REPO_ROOT/categorization-rules.textproto
```

Symlink, not copy — so edits in the source tree take effect on the next scan without re-staging. The sheaf CLI hard-resolves this path at the scan target's root; without staging, the warning "no source map found" appears and every `docs.*` surface silently reads 0. See the Stage 5 "What the doctor does NOT catch" note for the full failure mode.

---

## Stage 3: write `sheaf.textproto`

Start by copying the closest example config (see the matrix in `docs/scan-your-repo.md`). Don't write from scratch unless you're adding a new ecosystem — the examples encode lessons that aren't documented elsewhere.

**Mandatory edits** before running anything:

1. `project.name` and `project.display_name` — what the report header says.
2. `project.idl_prefix` — for proto/FIDL surfaces, the package prefix the body-token matcher will recognise. For envoy: `envoy`. For grpc: `grpc`. For Fuchsia FIDL: the library name (`fuchsia.io`).
3. `project.noisy_words` — single-token nouns common in the domain that should NOT participate in name-token attribution. For envoy: `cluster`, `listener`, `route`, `service`, `endpoint`, `config`, `request`, `response`. For a CLI: usually unnecessary; flags are dotted-path identifiers that don't collide. Audit the body-token false positives after the first scan and add as needed.
4. `scope.library` — the library to filter elements to. For a CLI: the binary name. For a FIDL library: the FIDL library name. For an open scope (envoy's "emit every package") leave empty.
5. The adapter's `include`/`exclude` globs — these are the values that produce "matched 0 files" or worse, silent under-counts. Verify them by `ls`-ing one of the matched paths before running doctor.
6. `binary_name` for `cobra` + `markdowncli` — must match the invocation name exactly. `yourtool`, not `your-tool-cli`. Element IDs key off this.
7. `url_base` for `markdowncli` — only set if the docs are published. Empty string is fine.
8. `surfaces_required` — what the report's "bridged" metric will compute against. For CLIs: usually `docs.reference`, `docs.concepts`, `examples`, `tests`. For FIDL: `docs.reference`, `docs.concepts`, `examples`, `implementations` (NOT `tests` — see the design note linked in Stage 1). Getting this wrong inflates or deflates the headline metric.

**Optional but high-value edits:**

- `analyzer { suppress_for_paths: ... }` — once you've identified specific paths the thin-reference analyzer should ignore. Don't pre-populate; let the first scan surface the noise.
- `options_table { ... }` under `markdowncli` — only if your reference markdown's tables use non-default column names or section headers. The auto-detector handles `Name`/`Description` headers; override only when it's misfiring.
- `extra_test_macros` under `gtest` / `protocpp` — for projects using `TYPED_TEST`, `TEST_P`, or project-specific test macros. envoy uses `TYPED_TEST` for ~42 tests; without this flag, those tests are dropped.
- `llm { ... }` — only if you decided in [Q8](#q8-what-backs-the-llm-dependent-steps--and-is-anything-installed-already) to wire a model. The only backend wired in v1 is local ollama; omit the block entirely for the deterministic `noop` path.
  ```textproto
  llm {
    client: "local-llama"
    local_llama { host: "127.0.0.1" port: 11434 model: "qwen2.5:7b-instruct" }
    embeddings: "ollama-embed"
    ollama_embeddings { host: "127.0.0.1" port: 11434 model: "nomic-embed-text" }
  }
  ```
  `sheaf doctor` reachability-probes the embedder, so a wrong host/port surfaces there rather than mid-scan.

---

## Stage 4: write the source map (`categorization-rules.textproto`)

This is the file most agents get wrong. The default behavior with no source map is "everything in a single alphabetical bucket," which renders 200 rows in a single flat table. That's never useful.

**The source map answers one question per element:** *which feature area does this belong to?* The report renders the source-map categories as section headers and groups elements under them.

### The pattern that works

For a CLI organized by noun-then-verb (`yourtool image create`, `yourtool container ps`):

```textproto
version: 1

category { dotted_path: "docs.reference" }
category { dotted_path: "docs.concepts" }
category { dotted_path: "examples" }
category { dotted_path: "tests.unit_tests" }

category {
  dotted_path: "subcommands.image"
  paths: "**/image_*"
  paths: "**/yourtool_image_*"
}

category {
  dotted_path: "subcommands.container"
  paths: "**/container_*"
  paths: "**/yourtool_container_*"
}

category {
  dotted_path: "subcommands.global"
  paths: "**/yourtool.yaml"
  paths: "**/yourtool.md"
}
```

For a proto API organized by service:

```textproto
category {
  dotted_path: "services.discovery"
  paths: "api/envoy/service/discovery/v3/**"
}
category {
  dotted_path: "services.cluster"
  paths: "api/envoy/service/cluster/v3/**"
}
```

For a FIDL library organized by protocol:

```textproto
category {
  dotted_path: "protocols.directory"
  paths: "**/directory.fidl"
  paths: "**/fuchsia.io/directory*.fidl"
}
```

### The pattern that doesn't work

```textproto
# Don't do this.
category { dotted_path: "everything" paths: "**" }
```

Or, equally bad: writing one category per element. The source map is for *grouping*. If you have 200 elements and 200 categories, the source map is doing nothing.

### Group related artifacts into one category, not one-per-file

The unit of a source-map category is a **feature area**, not a file. On a large repo this distinction *is* the report: a category per file yields hundreds of single-row sections nobody can navigate; a category per coherent subsystem yields a report that mirrors how the maintainer thinks about the codebase. The grouping you pass to the configuration is what gives the report logical coherence — and on a large monorepo it's also what keeps the report from fragmenting into too many tiny sections.

A category can carry as many `paths:` globs as it needs — that's the grouping mechanism. Use it to pull scattered-but-related files under one heading. The Fuchsia driver-framework config is the worked example: the framework spans three daemons (`driver_manager`, `driver_host`, `driver_index`) across dozens of test files, and rather than splitting them apart, the source map folds the whole framework's test surface into a single category by listing every subsystem glob together:

```textproto
category {
  dotted_path: "tests.unit_tests"
  paths: "src/devices/bin/driver_manager/**/*_test.cc"
  paths: "src/devices/bin/driver_manager/**/*_unittest.cc"
  paths: "src/devices/bin/driver_host/**/*_test.cc"
  paths: "src/devices/bin/driver_host/**/*_unittest.cc"
  paths: "src/devices/bin/driver_index/**/*_test.cc"
  paths: "src/devices/bin/driver_index/**/*_unittest.cc"
}
```

The judgment call is: *would the maintainer think of these as one thing?* If yes, one category. Split into sub-categories (`subcommands.driver_manager`, `subcommands.driver_host`, …) only once a single grouping grows large enough that a reviewer would want to jump between its sub-areas — that's a navigability decision made *after* you see the counts, not a default to reach for on day one.

### Source map lessons from this repo's validation log

Lessons distilled from running the example reports against real repos:

- **README / CHANGELOG mentions should route to `docs.concepts`, not `docs.reference`.** A flag mentioned in a README is a narrative reference, not the canonical reference doc. Default markdown adapter behavior routes everything to `docs.reference`, which inflates that surface. The fd config patches this with `section_excludes`; future versions of Sheaf default it. If you're using `markdown` for a README, add a rule.
- **Tests organized by subsystem deserve their own subcommand category.** Fuchsia's driver-framework config has separate `subcommands.driver_manager`, `subcommands.driver_host`, `subcommands.driver_index` paths because the tests are bucketed by daemon. Source maps that mirror the source-tree organisation produce reports a reviewer can actually navigate.
- **Don't try to enumerate every category on day one.** Write the top-level families, run the scan, inspect the "uncategorized" bucket, add as needed. Two or three iteration cycles is normal.
- **Single-word commands need an `examples` category routing rule.** Without one, every workflow doc that mentions `run` or `ps` will over-attribute. The kubectl config handles this by routing workflow examples to a specific category that scopes attribution.
- **Source-map presence is load-bearing on every `docs.*` surface.** Discovered while running this playbook against envoy: without `categorization-rules.textproto` staged into the scan target, the scan completes, the doctor doesn't flag it as fatal, every adapter produces claims — and every `docs.*` surface reads 0 because the claims never get bucketed. The diff for envoy was `925 → 3921` doc claims and `0 → 278` doc refs once the source map landed. Read the scan's warning lines, not just the corpus summary.
- **For proto repos with both RPC services and config types: write the source map once, render twice.** envoy's source map handles `envoy.service.*/v3` (xDS, rendered with `--ecosystem proto`) and `envoy.config.*/v3` (Listener / Cluster / RouteConfiguration, rendered with `--ecosystem proto-config`) from the same `categorization-rules.textproto`. The category paths are the same — only the masthead view differs.

---

## Stage 5: run the doctor, fix until green

```sh
sheaf doctor --repo $REPO_ROOT
```

What the doctor reports:

- **Per-adapter file counts.** If any adapter shows "matched 0 files," the glob is wrong. Open the config, find the adapter block, `ls` against the include pattern by hand. The most common cause is a typo (`docs/yaml` vs `docs/yamls`) or an absolute-vs-relative path confusion.
- **Per-adapter element counts.** If files matched but no elements extracted, the file format is wrong. Open one of the matched files and check it against the adapter's expected schema. For cobra YAML: look for `command:`. For `.proto`: look for `service` and `rpc`. For `.fidl`: look for `protocol`.
- **Categorization coverage.** The doctor reports how many elements fell into the default bucket vs your declared categories. If 90% are in the default bucket, your category paths don't match the element source paths — re-read Stage 4.

**Loop:** edit `sheaf.textproto` or `categorization-rules.textproto`, re-run doctor, repeat. Do NOT proceed to scan until doctor is green on every adapter.

### What the doctor does NOT catch — sanity-check after the first scan

The doctor only verifies file presence + adapter-glob hits. It does not run the indexer, so several silent-failure modes can land you on "doctor green, report unusable":

- **Source map at the wrong path.** The CLI resolves `categorization-rules.textproto` at the SCAN TARGET's repo root, not at the config file's directory. If your example/template configs live in a sibling repo (sheaf's `docs/examples/` is the canonical case), the doctor reports `MISSING` and the scan emits "no source map (categorization-rules.textproto) found; categorization will be skipped." The scan still completes — but every `docs.*` surface silently reads 0 because nothing gets bucketed. **Fix:** symlink (don't copy) the rules file into the scan target's root before running:
  ```sh
  ln -sf $SHEAF_REPO/docs/examples/<project>-coverage-rules.textproto \
         $REPO_ROOT/categorization-rules.textproto
  ```
  Symlink rather than copy so edits in the source tree take effect on the next scan. Wire this into whatever validate.sh-style script drives the recurring scan.

- **Wrong ecosystem view at render time.** Doctor doesn't know which `--ecosystem` flag you'll pass to `sheaf render`. Picking the wrong one is silent — the report renders, just with the wrong nouns and tiers (see Stage 6 below).

- **All elements ending up in `tests.unit_tests` fallback.** Doctor reports the count, but a missing-category pass that lands 100% of test cases in the default bucket can still look fine if you don't read the number. If you see a single category dominating, your category paths in the source map don't match the actual test paths.

The post-scan sanity check is: look at the warning lines printed before the corpus summary, then look at the corpus summary itself (doc claims, doc refs, per-surface element counts). If "no source map found" appears, or if doc refs are 0 while doc claims are nonzero, fix and re-run *before* you render anything.

---

## Stage 6: scan, validate by hand, then render

```sh
sheaf scan --repo $REPO_ROOT
```

The scan prints a corpus summary: total elements, per-surface breakdown, per-adapter contribution.

**This is the validation gate.** Before rendering HTML, do the following:

1. **Element count sanity check.** Compare the reported count against your mental model. If the maintainer says they own ~180 subcommands and Sheaf says 12, your contract adapter's globs are wrong — back up to Stage 3.

2. **Open one element file from each adapter** (the scan output names them or you can `sheaf gaps --repo $REPO_ROOT --library $LIB_NAME | head`). Spot-check that the element ID, kind, and source path are sensible.

3. **Render the report:**
   ```sh
   sheaf snapshot --repo $REPO_ROOT --library $LIB_NAME --out snap.json
   sheaf render --from-snapshot snap.json --ecosystem <ecosystem> \
     --repo-root $REPO_ROOT -o report.html
   ```

   This is the in-process path — `sheaf snapshot` emits the library's Snapshot JSON and `sheaf render` turns it into a self-contained HTML file, no server. (The older `sheaf serve --port 7700 &` + `scanner --server http://127.0.0.1:7700 …` two-process flow still works and produces the same report, but the snapshot→render path is the lead recommendation.)

   **Pick the right `--ecosystem` for the library's shape.** Render uses the ecosystem id to choose tier labels and the masthead's primary-tier noun. The recurring footgun: for proto libraries, `proto` and `proto-config` are not interchangeable.
   - `--ecosystem proto` — services + methods are the primary surface. Masthead shows "N Services · M Methods". Use for gRPC / xDS management-plane libraries like `envoy.service.discovery.v3`.
   - `--ecosystem proto-config` — top-level proto `message` declarations are the primary surface (no services exist). Masthead shows "N Messages". Use for pure data-schema libraries like `envoy.config.listener.v3`, `envoy.config.cluster.v3`. Picking `proto` here renders the masthead as "Zero Services · Zero Methods" because PROTOCOL/METHOD are genuinely empty — a header that misrepresents the corpus.
   - `--ecosystem fidl` for FIDL libraries (Types tier), `--ecosystem cli` for CLIs (cobra/clap/argh all render under `cli`), etc.

   If a project has both shapes (envoy ships xDS services AND config-message libraries), render two reports — one per ecosystem.

4. **Spot-check ten random rows.** For each, click through to the cited file. If the row claims a test bridges to a method, the test body must mention the method. If two of ten are wrong, the report is not ready to share — back up to whichever adapter is over-attributing.

The validation gate is *the* line between "agent finished the wiring" and "report is shareable." Do not skip. Do not let the maintainer waive it on your behalf.

### Known false-positive sources (from the validation log)

These are the patterns to look for when you see a wrong row:

- **Strategy 3 (name-token fallback) over-matching.** A test whose name contains the command's name will attribute, even if the test body never invokes the command. Fixed for FLAG/SWITCH/CONFIG_KNOB/METHOD/TYPE/PROTOCOL kinds in 2026-Q2 (see `internal/indexer/indexer.go::testCaseRefsElement`), but SUBCOMMAND/SERVICE/LIBRARY still uses it. If you see a single-word command (`run`, `ps`, `build`) over-matching, this is why.
- **Cross-FIDL-library namesakes.** A test of `fuchsia.ui.composition.ImportToken` will attribute to `fuchsia.ui.gfx.ImportToken` if both libraries declare the same type. The current matcher can't disambiguate without library-scope context. Workaround: tighten `scope.library`.
- **Helper-only Go tests.** Tests that construct a `cobra.Command` for setup but never `Execute` it will attribute to that command even though they're not testing it. No mechanical fix yet; suppress via `analyzer { suppress_for_paths: ... }` for known helper test directories.
- **Implements-map over-attribution.** Demoted from a coverage strategy to a relationship-only signal in the 2026-05-26 redesign. If you see this in an older config, update `surfaces_required` to use `implementations` instead of `tests` for interface kinds.

### Known false-negative sources

- **Custom test macros.** If your project uses `MY_TEST(...)` instead of `TEST(...)`, the gtest adapter drops them silently. Wire `extra_test_macros`.
- **Tests in non-conventional paths.** `gotest` looks for `*_test.go`. If your project's integration tests live in `integration/*.go` without the `_test` suffix, set `gotest.include` explicitly.
- **Doc files without an H1.** The `markdowncli` adapter falls back to filename if no H1 / frontmatter — but the fallback is for `binary_subcmd.md`, so `getting-started.md` won't resolve to any command. Make sure your reference markdown has H1s like `# yourtool create volume`.
- **`yaml-workflows` blind to short-form yaml.** The adapter relies on `protomatch`, which extracts dotted FQDNs and `@type:` annotations. Projects whose yaml examples use short-form field names (`listeners:`, `clusters:`, etc.) — envoy is the canonical case — will see workflow counts that are an order of magnitude lower than the true number of composed examples. Envoy ships ~8 rst files with both `listeners:` and `clusters:` yaml blocks; the adapter currently finds 1 (the one page that types every block). If you need real workflow coverage on a short-form-yaml project, hand-write a field-name → contract-type map and wire it into the adapter (tracked follow-up).
- **Sphinx domain labels the adapter doesn't strip.** The `rst` adapter parses generic Sphinx roles (`:ref:`, `:py:class:`, `:repo:`) and extracts targets from `:role:`label <target>`` form, but project-specific label naming schemes — envoy's `envoy_v3_api_msg_<FQDN>`, sphinx-cpp's `cpp:func_<FQDN>` — won't strip back to plain FQDNs without per-project knowledge. Symptom: lots of `:ref:` calls in the docs, but `docs.reference` still reads low. Mitigation today is per-project; a config-driven label-prefix-strip option is the right long-term fix.

---

## Stage 7: when you need a new adapter

If Stage 1's decision tree didn't have a row for your contract surface, doc format, or test framework, you're writing an adapter. Before you start:

1. **Convince yourself this is a real adapter, not a config tweak.** Re-read Stage 1 — the existing adapters cover more than they look like at first glance. `markdown` handles most doc shapes; `gtest` covers any C++ project that uses googletest macros even if the file naming is custom.

2. **Pick the closest existing adapter as your template.** Each adapter is 200–600 LOC; they all implement the same interface (`adapters.ContractAnchorParser`, `adapters.TestParser`, `adapters.RenderedReferenceParser`, or `adapters.DocParser`). The interface is defined in `internal/adapters/adapters.go`.

3. **Write against synthetic fixtures first.** Make a `testdata/` directory with three or four hand-rolled examples covering the shapes you need to handle. Get the adapter green against those. Only then point it at the real corpus.

4. **Add a proto config message** in `proto/config.proto` for the adapter's configuration. Regenerate (`make proto`).

5. **Register the adapter** in `internal/orchestrator/orchestrator.go`. The orchestrator dispatches on the config message's adapter name.

6. **Add a fixture-based unit test.** Each existing adapter has one; mirror the pattern.

The three adapter patterns:

- **Source-parse** (clap, argh, fidl): walk the source AST, extract contract declarations. Fast, deterministic, brittle to syntax changes. ~400-600 LOC.
- **Structured-doc-bundle** (cobra, proto): consume YAML/JSON/protobuf descriptor output from the project's own docs/codegen pipeline. Easier to maintain. ~250-350 LOC. Requires the project to ship a generator step.
- **Runtime-introspection** (escape hatch, no current example): exec the binary with `--help --format json` and parse the output. Avoid unless A and B are blocked.

---

## Stage 8: schedule + integrate

After the report passes the validation gate, wire the recurring scan and any integrations the maintainer wants.

### Weekly cron

```sh
# /etc/cron.d/sheaf-weekly
0 6 * * 1 yourname cd $REPO_ROOT && sheaf scan && sheaf report --output /var/www/sheaf-reports/$REPO/ 2>&1 | logger -t sheaf
```

Pin the `sheaf` binary version — numbers shift across adapter versions, and a cron that auto-updates produces unmeaningful delta charts.

### CI gate (optional, opinionated)

Sheaf has no severity-threshold gate. The one fail gate is `sheaf scan --manifest <file> --fail-on-error`, which returns a non-zero exit code if any manifest entry failed to scan/render (it does not fail on findings). Treat the report as advisory in the PR pipeline — surface the delta comment via the PR bot rather than blocking the merge — and only after the report has been in cron for at least four weeks and the maintainer trusts the findings. Failing PRs on coverage findings earlier produces blockers from false positives and burns trust.

### PR bot integration

If the project wants per-PR commentary, see [docs/cli/workflows.md](../cli/workflows.md) for the `prbot` integration. The bot reads the same `sheaf.textproto` and posts a delta comment per PR.

### MCP server integration

If the maintainer's team uses Claude / Cursor day-to-day, expose the scan via `sheaf serve --port 7700` and document the MCP endpoint. Their agents can then query "what tests cover `yourtool foo create`?" inline. See `docs/mcp/schema.md`.

---

## Common pitfalls — checked against this repo's history

The patterns that have actually burned past onboardings, in rough order of frequency:

1. **Substituting placeholder paths in the agent prompt.** The `docs/scan-your-repo.md` template uses `$REPO_ROOT`, `$BIN_NAME`, `$LIB_NAME`. An agent that runs the doctor against `/path/to/your/repo` without substitution sees "0 files matched" and proceeds anyway. Always stop and ask if these are unset.
2. **Wiring all adapters before validating any one of them.** Doctor first, every time you add an adapter. Bisecting "the report looks weird" is much harder than "this one adapter has bad globs."
3. **Letting `markdown` route everything to `docs.reference`.** Inflates the reference surface, deflates concepts. README-as-concept routing is the canonical fix.
4. **Using `implements-map` as a tests surface.** Demoted; use `implementations`.
5. **Skipping the source map.** "I'll add categories later" produces a 200-row flat report and a maintainer who closes the tab. Source map first, polish later.
6. **Trusting the headline metric before reading the receipts.** "92% documented" with two of ten random rows wrong means you have a corpus problem, not a coverage win. Spot-check first, screenshot the headline second.
7. **One config trying to scan a monorepo.** Per-library configs; per-library scans; per-library reports. No exceptions.
8. **Wiring `gotest` for a project that doesn't have `_test.go` files where Sheaf expects them.** Walk the test tree by hand in Stage 0; don't assume conventions.
9. **Reading workflow coverage as ground truth on short-form-yaml projects.** `yaml-workflows` only finds composition in yaml blocks that carry dotted FQDNs or `@type:` annotations. Envoy and similar projects use short-form field names in most of their config examples — the workflow tile will read low not because the project lacks compositional examples but because the adapter can't see them. Cross-check by grepping the doc tree for `listeners:.*clusters:` (or the analogous co-occurrence for your project) before drawing conclusions from the workflow column. See the Stage 6 false-negative entry for the canonical envoy numbers.
10. **"Doctor green" mistaken for "scan ready."** Doctor only confirms file presence + adapter globs. If you skipped Stage 2's "stage the source map" step, the scan emits a "no source map found" warning and every `docs.*` surface silently reads 0 — but doctor still reports OK. The scan output is the actual gate, not doctor's summary. Read the warnings, read the doc-refs count.
11. **Wrong `--ecosystem` flag on the scanner.** For proto libraries, `proto` (services + methods) and `proto-config` (TYPE messages only) are not interchangeable. Rendering a config-message library under `--ecosystem proto` produces a "Zero Services · Zero Methods" header for a library whose actual surface is 10 Messages. The data is correct; the framing is wrong. Always pick the ecosystem that matches the library's *primary surface*, not its build system. If a project has both shapes (envoy ships xDS services AND config-message libraries), render two reports — one per ecosystem.

---

## What this playbook deliberately doesn't cover

- **Choosing whether to scan at all.** That's a Stage -1 conversation with the maintainer about whether Sheaf is the right tool for their surface.
- **Tuning the analyzer thresholds.** Default severities are intentional; tune only after a month of real-world usage data.
- **Customising the HTML report's appearance.** Out of scope; templates live in `utils/scanner/templates/`.
- **Multi-repo joins.** Sheaf v1 is one config = one repo. Cross-repo coverage (e.g., a CLI tested in one repo, documented in a sibling repo) requires either a snapshot script or copying the docs into the scan tree. There is no "remote adapter" option.

---

## Quick reference: minimal viable file layout

After a successful onboarding, the target repo should look like:

```
$REPO_ROOT/
├── sheaf.textproto                    ← Stage 3
├── categorization-rules.textproto     ← Stage 4
├── docs/                              ← whatever was already there
│   ├── yaml/                          ← if cobra (Stage 2)
│   └── reference/                     ← if rendered-reference
├── api-export/                        ← if proto (Stage 2; gitignored)
└── ... (source tree unchanged)
```

And in the sheaf repo, if the example is worth landing as a reference:

```
sheaf.data/
├── docs/examples/<project>-coverage-config.textproto
├── docs/examples/<project>-coverage-rules.textproto
└── example-reports/<project>.html        (regenerated on demand)
```

---

## Next reads

- **Index of routes:** [docs/scan-your-repo.md](../scan-your-repo.md)
- **Config reference:** [docs/config.md](../config.md)
- **All worked example configs:** [docs/examples/README.md](../examples/README.md)
- **CI / cron / PR-bot integration:** [docs/cli/workflows.md](../cli/workflows.md)
