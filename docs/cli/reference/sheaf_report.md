# sheaf report

Bulk-dump every coverage profile produced by the latest scan as CSV, JSON, or a multi-page HTML browser.

## Synopsis

```text
sheaf report [--config <path>] [--repo <path>]
             [--format csv|json|html]
             [--output <dir>]
```

## Description

`sheaf report` runs the full pipeline and writes every coverage profile in the corpus.

- `--format csv` (default) prints one row per element to stdout: `element_id,tests,docs,examples,missing`. The `missing` column joins gap labels with `|`.
- `--format json` emits one protojson document per element, separated by newlines (JSONL-ish).
- `--format html` requires `--output <dir>`. The writer renders an `index.html` plus one page per coverage bucket, with cross-links. The path to the index is printed on completion.

CSV and JSON go to stdout; HTML always lands on disk.

## Options

| Flag | Default | Notes |
|---|---|---|
| `--config` | `<repo>/sheaf.textproto` | Path to the project config. |
| `--repo`   | `.`                      | Project repo root. |
| `--format`  | `csv`                    | `csv`, `json`, or `html`. |
| `--output`  | *(none)*                 | Required when `--format html`. Created if it does not exist. |

## Examples

Quick coverage table for paste:

```sh
sheaf report > coverage.csv
```

Render the local HTML browser:

```sh
sheaf report --format html --output /tmp/sheaf-report
open /tmp/sheaf-report/index.html
```

Note that the per-library HTML produced by the `scanner` binary (consumed from MCP) is richer than the multi-page browser this subcommand produces. Use `sheaf report --format html` when you want the raw per-element pages; use `scanner` when you want the single-file fragmentation report linked from the README.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Output written. |
| `1` | Pipeline failed or HTML write failed. |
| `2` | Bad flag, unknown format, or `--format html` without `--output`. |
| `3` | Config or rules failed to load. |

## See also

- [`scanner`](scanner.md) — the single-file HTML report consumed from MCP.
- [`sheaf coverage`](sheaf_coverage.md) — drill into one element's profile.
