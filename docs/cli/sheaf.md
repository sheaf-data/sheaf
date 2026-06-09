# sheaf — command reference

The `sheaf` binary is the entry point for every contract-coverage operation: scanning a repository, serving its corpus over MCP, listing gaps, dumping coverage profiles, and rendering PR-review comments. This page is the canonical reference; per-subcommand pages live alongside it under [reference/](reference/).

Run `sheaf <command> --help` to get the same flag list locally.

## Synopsis

```text
sheaf <command> [options]
```

## Commands

| Command | What it does | Reference |
|---|---|---|
| [`scan`](reference/sheaf_scan.md)     | Run the full ingest → index → analyze pipeline against a project and print a corpus summary. |
| [`doctor`](reference/sheaf_doctor.md) | Validate `sheaf.textproto`, walk every configured adapter, and probe the LLM embedder. |
| [`version`](reference/sheaf_version.md) | Print the linked build version. |
| [`gaps`](reference/sheaf_gaps.md)     | List findings (filterable by kind / library / severity / format). |
| [`coverage`](reference/sheaf_coverage.md) | Print the `CoverageProfile` for one element. |
| [`report`](reference/sheaf_report.md) | Dump every coverage profile (CSV / JSON / multi-page HTML). |
| [`snapshot`](reference/sheaf_snapshot.md) | Emit a library's Snapshot JSON for the server-free render path. |
| [`render`](reference/sheaf_render.md) | Render the HTML report from a saved Snapshot JSON, in-process (no server). |
| [`verify`](reference/sheaf_verify.md) | Adversarially re-check a snapshot's numbers (vs the join + disk) before a report is shown. |
| [`serve`](reference/sheaf_serve.md)   | Start the MCP server. |
| [`review`](reference/sheaf_review.md) | Render a PR-review comment from base + head corpora. |
| [`review-html`](reference/sheaf_review-html.md) | Render `comment.html` from a previously-emitted `delta.json`. |
| [`init`](reference/sheaf_init.md)     | Scaffold a starter `sheaf.textproto` plus the project's source map (`categorization-rules.textproto`). |

## Common flags

Almost every subcommand accepts these:

| Flag | Default | Notes |
|---|---|---|
| `--config <path>` | `<repo>/sheaf.textproto` | Path to the project config. |
| `--repo <path>` | `.` | Project repo root. The source map (`categorization-rules.textproto`) is resolved relative to this. |

`sheaf scan`, `sheaf gaps`, `sheaf coverage`, `sheaf report`, `sheaf snapshot`, `sheaf serve`, and `sheaf review` all run the full pipeline (config load → adapter dispatch → indexer → analyzers) before producing their output. `sheaf render` and `sheaf review-html` are pipeline-free — they render from a Snapshot JSON / `delta.json` that an earlier pipeline run already produced — as are `sheaf doctor`, `sheaf version`, and `sheaf init`.

## Commands in context

The per-flag notes below show each subcommand with the options you reach for most often. The canonical, exhaustive option table for any command still lives on its [reference page](reference/); this section is the narrative tour.

**Scan.** A bare `sheaf scan` runs against `<repo>/sheaf.textproto`. Point `sheaf scan --config` at a config that lives elsewhere, set the project root with `sheaf scan --repo`, and pass `sheaf scan --quiet` to drop the per-adapter sample blocks when you only want the corpus summary.

**Doctor.** `sheaf doctor` validates the config and walks every adapter. `sheaf doctor --config` checks a config outside the repo root, and `sheaf doctor --repo` resolves the source map relative to a different tree.

**Coverage.** `sheaf coverage --element` is required — it names the contract element whose profile you want. `sheaf coverage --format` switches between the human-readable text dump and `json`, while `sheaf coverage --config` and `sheaf coverage --repo` locate the project the same way scan does.

**Gaps.** `sheaf gaps` lists findings. Narrow them with `sheaf gaps --severity` (minimum `INFO`/`WARNING`/`ERROR`), `sheaf gaps --kind` (a specific finding kind), and `sheaf gaps --library` (one library or element-ID prefix). `sheaf gaps --format` emits `text`, `json`, or `csv`, and `sheaf gaps --config` selects a non-default config.

**Report.** `sheaf report` bulk-dumps every coverage profile. `sheaf report --format` chooses `csv`, `json`, or `html`; when it is `html`, `sheaf report --output` names the directory the multi-page report is written to. `sheaf report --config` and `sheaf report --repo` locate the project.

**Serve.** `sheaf serve` starts the MCP server. `sheaf serve --bind` sets the listen address (default `127.0.0.1`) and `sheaf serve --config` selects the config whose corpus is served. `sheaf serve --stdio` switches from the HTTP listener to newline-delimited JSON-RPC on stdin/stdout, the transport desktop MCP clients (Claude Desktop, Cursor, Cline) use when they spawn the server as a subprocess.

**Review.** `sheaf review` renders a PR-coverage-delta comment. It needs a base tree via `sheaf review --base`; `sheaf review --repo` is the PR head and `sheaf review --pr` is the reference printed in the comment header. `sheaf review --review` overrides the configured review adapter (`noop`/`file`/`gerrit`/`github`), `sheaf review --file-out` is where the `file` adapter writes, and `sheaf review --post` actually posts the comment instead of printing it. `sheaf review --config` selects the config used for both base and head.

**Init.** `sheaf init` scaffolds a starter config. `sheaf init --repo` chooses where the files are written.

**Snapshot.** `sheaf snapshot` emits a library's Snapshot JSON for the server-free render path. `sheaf snapshot --library` names the library (a comma-separated list rolls several into one snapshot, labelled by `sheaf snapshot --library-label`), `sheaf snapshot --out` is the destination file, and `sheaf snapshot --config` / `sheaf snapshot --repo` locate the project.

**Render.** `sheaf render` turns a saved snapshot into the HTML report in-process — no server. `sheaf render --from-snapshot` names the snapshot and `sheaf render --library` overrides its label; `sheaf render --ecosystem` picks the rendering shape and `sheaf render --api-level` the target level for removed-element detection. `sheaf render --output` (alias `-o`) is the destination and `sheaf render --quiet` drops progress chatter. `sheaf render --repo-root` points at the git working tree — it enables the Lag section and the `{abs_path}` links that `sheaf render --source-url-template` can emit — while `sheaf render --commit` overrides the derived short hash and `sheaf render --header-style` chooses the masthead layout. `sheaf render --concept-docs-href` links the concept-doc sibling report, and `sheaf render --mock-overlap` swaps in fictional Overlap data for design iteration only.

**Verify.** `sheaf verify` adversarially re-checks a scan's headline numbers before the report is shown. It reads a snapshot via `sheaf verify --from-snapshot` and reconciles it — or, as a one-shot, `sheaf verify --config` builds the snapshot in-process (with `sheaf verify --library`) and verifies it in a single step. `sheaf verify --repo` points at the source tree, `sheaf verify --ecosystem` selects the view shape, and `sheaf verify --low-coverage-threshold` sets the suspicion line. `sheaf verify --disk` runs the on-disk oracle — `sheaf verify --check-urls` adds dead-link resolution, `sheaf verify --max-disk-elements` caps its sample, and `sheaf verify --expected-elements` cross-checks the element count against an authoritative parser — while `sheaf verify --sample-assertions` bounds the attribution `assertions` array that `sheaf verify --json` writes for the precision workflow. `sheaf verify --ledger` writes the human trust ledger and `sheaf verify --strict` exits non-zero when the verdict is broken. The verdicted assertions feed `sheaf verify summarize` for per-library precision.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Success. |
| `1` | Runtime error during ingest / index / analyze, or adapter errors surfaced after a successful scan. |
| `2` | Usage error — missing flag, bad value, refused overwrite. |
| `3` | Config load failure or unreachable resource (config path, rules path, embedder probe). |

## Companion binaries

| Binary | What it does | Reference |
|---|---|---|
| [`scanner`](reference/scanner.md) | Talk to a running `sheaf serve` and write a self-contained HTML fragmentation report. |
| [`dump-elements`](reference/dump-elements.md) | Debug helper — run the FIDL adapter directly and print per-kind / per-library summaries. |
| [`dump-profile`](reference/dump-profile.md) | Debug helper — run the full pipeline and dump one element's `CoverageProfile`. |
| [`kubectl-yamlgen`](reference/kubectl-yamlgen.md) | Introspect a kubectl binary via `--help` and emit per-subcommand YAML files in the cobra schema. |

See [docs/cli/workflows.md](workflows.md) for end-to-end recipes that combine these binaries.

## MCP API

`sheaf serve` exposes the corpus over JSON-RPC. The full operation list, params, return shapes, error codes, and authentication model are documented at [docs/mcp/api.md](../mcp/api.md); the proto messages each operation returns are indexed at [docs/mcp/schema.md](../mcp/schema.md). See [docs/mcp/README.md](../mcp/README.md) for the at-a-glance map.
