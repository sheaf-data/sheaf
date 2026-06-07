# sheaf scan

Run the full Sheaf pipeline against a project and print a corpus summary plus a per-kind finding breakdown.

## Synopsis

```text
sheaf scan [--config <path>] [--repo <path>] [--quiet]
sheaf scan --auto [--include <glob>] [--scope-library <name>] [--repo <path>]
           [--llm-backend auto|ollama|anthropic] [--model <tag>]
           [--attr-max-docs <n>] [--attr-max-tests <n>]
sheaf scan --manifest <path> [--output-dir <dir>] [--config-root <dir>] [--repo <path>]
           [--fail-on-error] [--jobs <n>] [--single-file] [--snapshot-cache <dir>]
           [--force-rescan] [--base-url <url>]
```

## Description

`sheaf scan` is the workhorse subcommand. It loads `sheaf.textproto`, optionally loads the project's source map (`categorization-rules.textproto` — a missing source map is tolerated with a warning), dispatches every configured contract-anchor / test-parser / doc-parser / rendered-reference / implements-map adapter, builds the cross-reference index, runs every enabled analyzer, and prints a summary.

The output is intentionally compact: one line per pipeline stage (with elapsed time), one line each for contract elements, test cases, doc claims, and coverage profiles, and a per-kind histogram of findings.

When `--quiet` is not set, `scan` also prints up to five sample doc mentions and up to five sample tests so you can confirm at a glance that the adapters fired.

## Options

| Flag | Default | Notes |
|---|---|---|
| `--auto`       | `false`                  | Zero-config mode: auto-detect ecosystems, synthesize a config, and emit four artifacts (`sheaf-report.html` + `report/` + `sheaf.textproto` + `sheaf-hardening.md`). No `sheaf.textproto` required. |
| `--config`     | `<repo>/sheaf.textproto` | Path to the project config. |
| `--repo`       | `.`                      | Project repo root. In `--manifest` mode this is the default repo root applied to every entry. |
| `--quiet`             | `false`                  | Suppress the per-adapter sample blocks. |
| `--include`    | _(whole repo)_           | In `--auto` mode, restrict detection/scan to these globs (repeatable / comma-separated). |
| `--scope-library` | _(all)_               | In `--auto` mode, restrict the contract surface to these libraries (repeatable). Keeps the LLM tier bounded. |
| `--llm-backend` | `auto`                  | LLM generation backend for `--auto`: `auto` (frontier if `ANTHROPIC_API_KEY` set, else ollama), `ollama`, or `anthropic`. |
| `--model`      | _(per backend)_          | Model tag for the LLM tier in `--auto` mode (default: ollama `qwen2.5:14b-instruct`, anthropic `claude-sonnet-4-6`). |
| `--attr-max-docs` | `0` (all)             | Cap on docs adjudicated by the LLM attribution pass in `--auto` (`0` = all). |
| `--attr-max-tests` | `0` (all)            | Cap on tests adjudicated by the LLM attribution pass in `--auto` (`0` = all). Bounds frontier-API cost on a cold run. |
| `--manifest`   | _(unset)_                | Path to a `MonorepoManifest` textproto. Switches `scan` into fan-out mode (see below). |
| `--output-dir` | manifest's directory     | Output directory for fan-out mode. Per-entry `output` paths resolve relative to it. |
| `--config-root` | manifest's directory    | In fan-out mode, the directory a relative `config_path` resolves against. Lets a manifest written elsewhere (e.g. `/tmp`) reference configs by repo-relative path. Absolute `config_path` values are unaffected. |
| `--fail-on-error` | `false`               | In fan-out mode, exit non-zero if any entry failed. Default is continue-on-failure (exit `0` as long as the manifest parsed). |
| `--jobs` / `-j` | machine-sized           | In `--manifest` mode, max entries scanned/rendered concurrently (`0` = `max(2, NumCPU/2)` capped at 8; `1` reproduces serial behavior). Adapters within an entry are already parallelized, so this sits below `NumCPU`. |
| `--single-file` | `false`                 | In `--manifest` mode, emit one portable `index.html` embedding every report as a hash-routed iframe, instead of a directory of separate files. Best for small/medium runs. |
| `--snapshot-cache` | `<output-dir>/.snapshot-cache` | In `--manifest` mode, directory for the snapshot reuse cache. A warm run with unchanged inputs renders from cached snapshots instead of re-scanning. |
| `--force-rescan` | `false`                | In `--manifest` mode, bypass the snapshot cache and re-scan every entry (still refreshing the cache). Required after changing sheaf's own scanner/adapter code, which the cache key does not cover. |
| `--base-url`   | _(unset)_                | In `--manifest` mode, the base URL the run will be published at (e.g. `https://host/path/`). When set, the index and each report's run-switcher emit absolute links so a single shared report file stays navigable. |

## Monorepo fan-out (`--manifest`)

`sheaf scan --manifest <file>` reads a `MonorepoManifest` textproto and runs a full scan + render for **each entry**, producing one HTML report per entry plus an `index.html` that links them. It is the automated/scheduled counterpart to the interactive `scripts/regen-example-reports.sh`; the manifest mode is additive and does not replace that script.

### When to use it

Use the manifest mode when you want to render many per-module reports across a monorepo in one invocation — e.g. every proto-bearing module in a Pigweed checkout — without standing up a `sheaf serve` instance per module. Each entry is scanned and rendered **in-process** (no HTTP round trip).

### Manifest format

```text
# MonorepoManifest (proto/config.proto)
entries {
  config_path: "docs/examples/pigweed-pw_rpc-coverage-config.textproto"  # see resolution rules below
  library: "pw.rpc"                                                      # which library to render
  ecosystem: "proto"                                                     # rendering shape: proto | fidl | cli
  library_label: "pw_rpc"                                                # optional human-readable scope label
  output: "pigweed-pw_rpc.html"                                          # relative to --output-dir
}
entries {
  config_path: "docs/examples/pigweed-pw_log-coverage-config.textproto"
  library: "pw.log"
  ecosystem: "proto"
  library_label: "pw_log"
  output: "pigweed-pw_log.html"
}
```

`config_path` resolution follows a fixed precedence:

1. **Absolute** `config_path` — used as-is.
2. **`--config-root` set** — relative `config_path` resolves against `--config-root`.
3. **Otherwise** — relative `config_path` resolves against the manifest's own directory (back-compat default).

This lets a manifest written to a scratch location (e.g. `/tmp`) carry repo-relative `config_path` values: pass `--config-root <repo>` to anchor them. `output` resolves relative to `--output-dir`. If a `categorization-rules.textproto` sits next to an entry's config, it is staged into the repo root for that entry's scan and removed afterward (mirroring `regen-example-reports.sh`). The runner creates `--output-dir` if it does not exist.

### Continue-on-failure semantics

A failing entry (bad `config_path`, scan error, …) is logged and recorded but does **not** abort the remaining entries — the failure renders as an inline row in `index.html` with an error tooltip. The run exits `0` as long as the manifest itself parsed. Pass `--fail-on-error` to make any entry failure produce a non-zero exit (useful in CI). A malformed manifest is a hard error before any entry runs.

### Worked example (Pigweed)

```sh
# Generate a manifest from a Pigweed checkout's PIGWEED_MODULES file:
scripts/generate-pigweed-manifest.sh > /tmp/pigweed-manifest.textproto

# Fan out: one report per proto-bearing module + an index.
# The generator emits repo-relative config_path values, so anchor them
# with --config-root (run from the sheaf repo root).
sheaf scan --manifest /tmp/pigweed-manifest.textproto \
           --config-root "$(pwd)" \
           --output-dir /tmp/pigweed-fanout \
           --repo /Volumes/T7/pigweed

open /tmp/pigweed-fanout/index.html
```

Other monorepo ecosystems (Cargo workspaces, Bazel, Lerna) plug into the same runner by supplying their own list-generator that emits a `MonorepoManifest`; only Pigweed's generator ships today.

## Examples

Scan the repo in the current directory:

```sh
sheaf scan
```

Scan a checkout elsewhere and silence the sample lines:

```sh
sheaf scan --repo /Volumes/T7/sheaf-workspace/checkouts/kubernetes --quiet
```

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Scan completed and no adapter reported a fatal error. |
| `1` | Pipeline succeeded but one or more adapters logged an error. |
| `2` | Bad flag. |
| `3` | Config or rules failed to load. |

## See also

- [`sheaf doctor`](sheaf_doctor.md) — verify the config before scanning.
- [`sheaf gaps`](sheaf_gaps.md) — drill into the findings produced by this command.
- [`sheaf serve`](sheaf_serve.md) — keep the indexed corpus live for queries.
