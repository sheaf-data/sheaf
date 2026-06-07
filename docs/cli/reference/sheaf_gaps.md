# sheaf gaps

List findings from the latest scan, filtered by kind, library, and severity, in text / JSON / CSV.

## Synopsis

```text
sheaf gaps [--config <path>] [--repo <path>]
           [--kind <FINDING_KIND>] [--library <prefix>]
           [--severity INFO|WARNING|ERROR]
           [--format text|json|csv]
```

## Description

`sheaf gaps` runs the full ingest → index → analyze pipeline (the same pipeline `sheaf scan` runs) and then filters and prints the resulting `Finding` list.

Filtering happens client-side after the pipeline:

- `--kind` matches the finding's `FindingKind`. The `FINDING_KIND_` prefix is optional — `--kind THIN_REFERENCE` and `--kind FINDING_KIND_THIN_REFERENCE` are equivalent.
- `--library` accepts a library name or an element-ID prefix. Three separators are tolerated so the filter works for FIDL (`fuchsia.io.`), gRPC (`grpc.health.v1/`) and cobra (`kubectl annotate`) findings without the user having to know which convention the corpus uses.
- `--severity` is the minimum severity that survives the filter; `WARNING` shows `WARNING` and `ERROR`.

## Options

| Flag | Default | Notes |
|---|---|---|
| `--config`     | `<repo>/sheaf.textproto`     | Path to the project config. |
| `--repo`       | `.`                          | Project repo root. |
| `--kind`       | (all kinds)                  | Filter by `FindingKind`. |
| `--library`  | (all)                        | Element-ID prefix to match. |
| `--severity`  | `INFO`                       | Minimum severity. |
| `--format`      | `text`                       | Output as `text`, `json`, or `csv`. |

## Examples

Every WARNING-or-higher thin-reference finding for kubectl:

```sh
sheaf gaps --kind THIN_REFERENCE --library kubectl --severity WARNING
```

JSON, for piping into `jq`:

```sh
sheaf gaps --format json | jq 'select(.kind == "FINDING_KIND_TESTED_UNDOCUMENTED")'
```

CSV, for paste into a sheet:

```sh
sheaf gaps --format csv > gaps.csv
```

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Pipeline ran. Empty-list output is `No findings.`, still exit 0. |
| `1` | Pipeline failed. |
| `2` | Bad flag or bad value. |
| `3` | Config or rules failed to load. |

## See also

- [`sheaf scan`](sheaf_scan.md) — the underlying pipeline.
- [`sheaf coverage`](sheaf_coverage.md) — per-element drill-down for one finding's subject.
