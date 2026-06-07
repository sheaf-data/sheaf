# Scan your repo

**Audience:** A staff or senior engineer evaluating Sheaf for a repo you own. You have an afternoon. You want to know whether Sheaf fits your surface, what wiring it up costs, what the first report looks like, and where it will embarrass you before you forward it to a director.

This is the umbrella guide. Pick your route from the matrix below; each route is short and either runs to completion in-line or hands off to a deeper playbook.

---

## Pick your route

| Your contract surface | Contract anchor | Test adapter | Doc / reference adapter | Working example to copy | First report in |
|---|---|---|---|---|---|
| **Cobra CLI** (Go) | `cobra` | `gotest` | `markdowncli` | [gh](examples/gh-coverage-config.textproto) | ≈1 hour |
| **Clap CLI** (Rust, `#[derive(Parser)]`) | `clap` | `rusttest` | `markdown` | *write one* (`clap` adapter) | ≈2 hours |
| **Argh CLI** (Rust, `#[derive(FromArgs)]`) | `argh` | `rusttest` | `markdown` | `sheaf init --template argh-cli` | ≈2 hours |
| **Protobuf API** (gRPC, xDS, anything `.proto`) | `proto` | `gtest` / `gotest` | `markdown` *(optional)* | [envoy](examples/envoy-coverage-config.textproto) | ≈half a day (proto export step) |
| **OpenAPI / Swagger HTTP API** | snapshot script (no adapter) | matched per-tag against your test tree | matched per-tag against your doc tree | *write a snapshot script* (see §OpenAPI below) | ≈half a day (write the snapshot script) |
| **FIDL** (Fuchsia) | `fidl` + `implementsmap` | `gtest` | `fidldoc` *(optional)* | [fuchsia.ui.composition](examples/fuchsia-ui-composition-coverage-config.textproto), [driver-framework](examples/fuchsia-driver-framework-coverage-config.textproto) | ≈half a day |
| **Something else** (Sphinx-only, GraphQL, capnproto, hand-rolled) | *write one* | one of the above | *maybe write one* | — | ≈1 week |

The matrix is the whole decision. If you don't see your row, scroll to [When it doesn't fit](#when-it-doesnt-fit) before doing anything else.

---

## Variables this doc uses

Every command below uses three shell variables instead of inline paths. Set them once before you run anything (or before you hand this doc to an agent):

| Variable | What it is | Example |
|---|---|---|
| `$REPO_ROOT` | Absolute path to the repo you're scanning | `/Users/you/work/yourtool` |
| `$BIN_NAME`  | The CLI's invocation name, or the library identifier for non-CLI surfaces | `yourtool`, `fuchsia.ui.composition` |
| `$LIB_NAME`  | Sheaf `scope.library` — usually the same as `$BIN_NAME` for CLIs, the package prefix for proto/FIDL | `yourtool`, `envoy.service.discovery.v3` |

```sh
export REPO_ROOT=/absolute/path/to/your/repo
export BIN_NAME=yourtool
export LIB_NAME=yourtool
```

If you (or your agent) don't know what to set these to, stop and find out. Don't substitute placeholder paths — the scan will silently under-count against `/path/to/your/repo` and look like the adapter is broken.

---

## Hand to your agent

If you'd rather delegate the initial wiring to a coding agent (Claude, Cursor, Codex, Cline) rather than work through the routes by hand, you have two options.

**Packaged skill (recommended).** The [`sheaf-onboard`](examples/sheaf-onboard/) skill is the rigorous, automated version of this hand-off: it understands the repo, automates the config via `sheaf scan --auto`, runs the scan, and **adversarially validates every top-line number against disk with `sheaf verify` before showing the report** — so your spot-check below is a confirmation, not a discovery. If config alone can't reach a surface, it offers a [`sheaf-build-adapter`](examples/sheaf-build-adapter/) handoff rather than silently under-counting. Install with `docs/examples/sheaf-onboard/install.sh` and invoke `/sheaf-onboard`, or point any agentic CLI (Gemini, Codex) at its `AGENTS.md`.

**Lightweight prompt.** Or paste the following prompt verbatim. The agent does the config plumbing; you do the validation in [Validate the scan](#validate-the-scan).

```text
You are wiring up Sheaf (https://github.com/sheaf-data/sheaf) for the repository
at $REPO_ROOT. Read docs/scan-your-repo.md end-to-end before doing anything.

1. From the "Pick your route" matrix, select exactly one row that matches
   this repo's contract surface. If more than one could apply, ask me which.
2. If $REPO_ROOT, $BIN_NAME, or $LIB_NAME are unset, ask me for them and
   stop until I answer. Do not invent values or substitute placeholder paths.
3. Follow only that route's section. Copy the linked example config to
   $REPO_ROOT/sheaf.textproto and edit the paths, binary name, library
   identifier, and any project-specific knobs (idl_prefix, noisy_words,
   crate_roots, etc.) per the route's instructions.
4. Run: sheaf doctor --repo $REPO_ROOT
   Report the full output. If any adapter reports "matched 0 files",
   fix the globs in sheaf.textproto and re-run. Do not proceed until
   doctor is green.
5. Run: sheaf scan --repo $REPO_ROOT
   Report the element count and the per-adapter summary.
6. STOP. Do not render the HTML report yet. Tell me:
   "Doctor green. Scan found N elements across <surfaces>. Please spot-check
    ten rows per the Validate the scan section before I render the report."
   Wait for my confirmation.
7. After I confirm, render the report and print the cron line I can add
   to a weekly job.
```

The agent does steps 1–5 and 7. **You** do step 6 — see the validation gate below. Receipts in the rendered report are mechanical and unaffected by who wrote `sheaf.textproto`; the failure mode an agent install introduces is silently wrong globs, which is exactly what the spot-check catches.

---

## The five-minute shape (every route)

The flow is the same for every surface; what changes is which adapters land in `sheaf.textproto`.

```sh
go install github.com/sheaf-data/sheaf/cmd/sheaf@latest
go install github.com/sheaf-data/sheaf/utils/scanner@latest

sheaf init   --repo $REPO_ROOT            # scaffold config
sheaf doctor --repo $REPO_ROOT            # validate adapters can reach their inputs
sheaf scan   --repo $REPO_ROOT            # corpus summary, no report yet

sheaf serve  --repo $REPO_ROOT --port 7700 &
scanner --server http://127.0.0.1:7700 --library $LIB_NAME -o report.html
kill %1
```

`sheaf init` writes `sheaf.textproto` plus the project's source map (on disk: `categorization-rules.textproto`) from a built-in template. The templates today are `minimal`, `argh-cli`, and `fuchsia-internal` — see [sheaf init](cli/reference/sheaf_init.md). If your route isn't templated yet, start from the nearest example config in the matrix above; the routes below tell you which file to copy.

`sheaf doctor` is the validation loop. Run it after every config edit. It walks each adapter, names which globs matched nothing, and prints which paths it tried — that's how you find typos in `include:`/`exclude:` patterns before they show up as empty cells in the report.

---

## Route: Cobra CLI

*The most worn path. If your CLI is cobra-based and your reference docs are one-markdown-file-per-subcommand, you're an hour from a rendered report.*

1. Generate per-subcommand YAML with cobra's own `doc.GenYamlTree` (or, for binaries without a yamldocs target, `cmd/kubectl-yamlgen`). If your project already ships this, you're done with step 1.
2. Copy [gh-coverage-config.textproto](examples/gh-coverage-config.textproto) as `sheaf.textproto`; set `binary_name`, `yaml_dir`, and `docs_dir` to your paths.
3. Run the five-minute shape above.

For the long form — generating YAML, handling per-flag joins, customizing the options-table parser, when single-word commands over-match — see the long-form onboarding playbook: **[docs/playbooks/onboard-a-new-repo.md](playbooks/onboard-a-new-repo.md)**.

---

## Route: Clap CLI (Rust)

*The dominant Rust CLI framework. `#[derive(Parser)]`, `#[derive(Args)]`, `#[derive(Subcommand)]` are read directly from source — you don't generate anything.*

1. Write a `sheaf.textproto` with a `clap` contract anchor. A single-crate Rust CLI (`#[derive(Parser)]` in one `src/cli.rs`) is the simplest shape to start from.
2. Set `clap.crate_roots` to the directory containing the `Cargo.toml` whose parser struct you want scanned. For a single-crate repo, `"."` works.
3. Point the `markdown` adapter at your prose (`README.md`, `doc/*.md`).
4. Point `rusttest` at your test files.
5. Run the five-minute shape.

The clap adapter resolves the binary name from `#[command(name = "…")]` on your top-level struct — the crate's directory name doesn't matter.

Per-flag joins via markdown depend on whether your docs have an options table; if they don't, expect command-level joins but not flag-level.

## Route: Argh CLI (Rust)

*Same shape as clap, different macro family — `#[derive(FromArgs)]`. Used inside Fuchsia and a handful of Google projects.*

1. Run `sheaf init --template argh-cli`. The template ships a working `sheaf.textproto` with `argh` + `rusttest` + `markdown` and a starter source map (`categorization-rules.textproto`).
2. Edit the scaffolded paths to point at your crate root, test tree, and doc directory.
3. Run the five-minute shape.

The argh adapter walks Rust source for `#[derive(FromArgs)]` struct declarations; same operational shape as clap from then on.

---

## Route: Protobuf API

*Sheaf reads `.proto` files directly as the contract; tests and docs attribute to RPC methods and message types by name.*

1. Make sure `protoc` is on PATH. If your `.proto` files import outside the repo (most do), you'll also need [`buf`](https://buf.build/) to export the tree with transitive deps — the [envoy example](examples/envoy-coverage-config.textproto) comment block has the exact incantation.
2. Copy the [envoy example](examples/envoy-coverage-config.textproto) — a large xDS-style management plane — and pare it down to your package's service surface.
3. Set `project.idl_prefix` to your project's package prefix (e.g. `envoy`, `grpc`, `google.cloud`). The body-parsing matcher uses this to recognize candidate contract IDs in test bodies.
4. List `noisy_words` for any single-token nouns common in your domain (`request`, `response`, `cluster`, …) so test mentions of them don't over-attribute.
5. Run the five-minute shape; for the report step, pass `--ecosystem proto`.

Doc attribution is optional for proto surfaces — most proto APIs ship comments inline in the `.proto` file itself, which the `proto` adapter already extracts. The `markdown` adapter is only needed if you also ship narrative concept docs.

---

## Route: OpenAPI / Swagger HTTP API

*The on-ramp is different from the other routes. OpenAPI surfaces aren't read by a contract-anchor adapter; instead you write a small snapshot script that walks the OpenAPI spec and emits a Sheaf `Snapshot` JSON, then render it with the `openapi` ecosystem view.*

Why the shape difference: there's no canonical "doc file per endpoint" or "test file per endpoint" convention across OpenAPI projects — Grafana groups by tag, Stripe groups by resource, OpenAI ships docs out-of-band. So you tell Sheaf the mapping once in the snapshot script (which doc files attribute to which tag, which test files attribute to which endpoint) rather than encoding it as adapter globs.

1. Write a snapshot script that pulls your OpenAPI spec (e.g. a published `api-merged.json`), enumerates one element per endpoint (`path × method`), and writes a Sheaf `Snapshot` JSON. Attribution is driven by override tables (`DOC_OVERRIDES`, `TEST_OVERRIDES`, `EXAMPLE_OVERRIDES`) that map each tag / endpoint to the doc and test files covering it.
2. The override tables are the real work — the snapshot wiring around them is mechanical. Point each tag at its doc file(s) and each endpoint at its test file(s); anything left unmapped renders as ABSENT rather than guessing.
3. Render:
   ```sh
   scanner --from-snapshot $REPO_ROOT/sheaf-snapshot.json \
           --library $LIB_NAME --ecosystem openapi \
           --source-url-template 'https://github.com/<org>/<repo>/blob/main/{path}#L{line}' \
           --commit "$(git -C $REPO_ROOT rev-parse --short HEAD)" \
           -o report.html
   ```

The [openapi ecosystem view](../utils/scanner/ecosystem_openapi.go) renders endpoints as the primary METHOD tier and OpenAPI tags as the PROTOCOL container tier. Per-parameter elements are deliberately not emitted — see the file's header comment for why.

Strict matching is the convention here: a doc / test / example only attributes when its filename stem is in a deliberately-enumerated candidate set. Tags with no genuine match render as ABSENT rather than guessing — important for trust on a surface this large.

---

## Route: FIDL (Fuchsia)

*FIDL is structurally similar to proto — the `.fidl` file is the contract — but the C++ wire-server pattern means you also need the `implementsmap` adapter to bridge from a C++ class's tests back to the FIDL methods it implements.*

1. Copy [fuchsia-ui-composition-coverage-config.textproto](examples/fuchsia-ui-composition-coverage-config.textproto).
2. Set `scope.library` to the FIDL library you're scanning.
3. Point `fidl.include` at your `.fidl` glob, `gtest.include` at your `_unittest.cc` / `_test.cc` globs, and `implements_map.include` at the C++ headers + sources that implement the wire-server side.
4. If you ship fidldoc-generated reference, add the `fidldoc` rendered-reference block (see [fuchsia-driver-framework](examples/fuchsia-driver-framework-coverage-config.textproto)).
5. Run the five-minute shape; for the report, pass `--ecosystem fidl`.

The `sheaf init --template fuchsia-internal` template covers most of this for a single library.

---

## Validate the scan

> **⛔ Validation gate — this is the human's job, not the agent's.**
>
> The agent prompt above stops here for a reason. The report's per-claim receipts are mechanically derived and trustworthy regardless of who wrote `sheaf.textproto` — but a wrong glob in the config produces a *silent under-count*, and only a human spot-check catches that. Do not skip this step. Do not let an agent skip it for you.

After the first scan completes, before rendering or sharing the report:

1. **`sheaf doctor` is green.** No adapter reported "matched 0 files."
2. **The corpus summary's element count matches your mental model.** If you own a 200-method API and Sheaf found 12 elements, the contract adapter's globs are wrong. Fix and re-run.
3. **Open the HTML report and pick ten rows at random.** For each, click through to the source file. If a row claims a doc bridges to a method, the doc must actually mention the method. **If two of ten are wrong, stop and fix the adapter config before showing the report to anyone.** A reviewer who spot-checks the report and finds two wrong rows in their first ten will discard the whole scan — and the methodology with it. Calibrate before you share.
4. **The report groups by feature area, not alphabetically.** If your report is one flat list of 200 rows, edit your source map (`categorization-rules.textproto`) — see [Stage 4 of the onboarding playbook](playbooks/onboard-a-new-repo.md) for the pattern.

Only after all four pass should you render the final report, schedule the cron, or forward to anyone.

---

## When it doesn't fit

Three escape hatches, ordered by how much code you'll write:

**1. Your docs aren't markdown.** If your reference docs are Sphinx RST, OpenAPI YAML, hand-written HTML, or a CMS export, you need a new rendered-reference adapter. The shape is small — `markdowncli` is ~400 LOC. Copy it, swap the parser, register in [orchestrator.go](../internal/orchestrator/orchestrator.go), add a proto message in [config.proto](../proto/config.proto).

**2. Your CLI isn't cobra/clap/argh.** Same shape, contract-anchor side. The onboarding playbook's [Stage 7](playbooks/onboard-a-new-repo.md) walks the three implementation patterns (source-parse, structured-doc-bundle, runtime-introspect).

**3. Your contract isn't proto / FIDL / a CLI / OpenAPI.** GraphQL schemas, JSON-Schema, capnproto, hand-rolled ABI definitions — each needs its own contract-anchor adapter. Same interface as the others; start from [proto](../internal/adapters/proto/) as the reference implementation since it's the simplest declarative-surface adapter. (For OpenAPI specifically, you don't write an adapter — write a snapshot script; see the [OpenAPI route](#route-openapi--swagger-http-api) above.)

In all three cases: write the adapter against synthetic fixtures first, get it green, *then* point it at your real corpus. Adapter development against a noisy real codebase wastes a day per bug.

---

## What to expect from your first report

A useful first report has:

- ≥90% doc coverage on your contract elements
- ≥30% test coverage (more for non-single-word command/method names)
- A categorization that groups by feature area, not alphabetical order
- A non-empty findings list — that's the feature, not a failure mode

What a first report **won't** have, and shouldn't:

- High "Bridged" numbers (concept + test + example all present). Bridged is a v2 goal; most repos ship at 5–15% on first scan. The point is to make the gap visible.
- A clean Anomalies section. LLM-derived findings include false positives by design — Sheaf surfaces confidence on every claim and lets you threshold rather than filter silently, because hiding low-confidence findings hides the methodology along with them.
- Stable numbers across adapter versions. When you upgrade `sheaf`, expect the numbers to shift; the delta-since-last-scan view is meaningful only between scans run on the same adapter version. Pin your `sheaf` binary if you cron this.

---

## When Sheaf can't recognize your surface

The failure mode is **silent under-count**, not a crash. If the contract adapter's globs match nothing, `sheaf doctor` says so; if the globs match but the file is malformed (a `.proto` that doesn't compile, a cobra YAML missing the `command:` key), the file is skipped and noted in `--verbose` logs but does not abort the scan. The report renders with whatever elements *did* parse.

This means: **always read `sheaf doctor`'s output before trusting the report.** A 12-element report on a 200-method API is the most common rough edge, and it always traces to an adapter input the doctor flagged.

---

## When it doesn't fit and you want to flag it

The fastest way to get an adapter in is to open an issue with: (a) a 5-line snippet of your contract source, (b) a 5-line snippet of one test that references it, (c) a 5-line snippet of one doc page. That's enough to scope the adapter; everything after is implementation.

---

## Next reads

- **Onboarding playbook (long form):** [docs/playbooks/onboard-a-new-repo.md](playbooks/onboard-a-new-repo.md)
- **All example configs:** [docs/examples/README.md](examples/README.md)
- **Config reference:** [docs/config.md](config.md)
- **End-to-end workflows (CI, PR-bot, scheduled scans):** [docs/cli/workflows.md](cli/workflows.md)
