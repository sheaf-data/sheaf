# sheaf coverage

Print the `CoverageProfile` for one named contract element.

## Synopsis

```text
sheaf coverage --element <id> [--config <path>] [--repo <path>]
               [--format text|json]
```

## Description

`sheaf coverage` is the focused inverse of `sheaf report`. It runs the full pipeline, then writes the `CoverageProfile` for the single element named by `--element`. Use it when you want to know exactly which doc bullets, which tests, and which examples got attributed to one ContractElement.

The text output starts with the element's ID, kind, and the doc-comment excerpt, then lists reference docs, tutorial docs, concept docs, and tests bucketed by kind. The JSON output is the protobuf message serialized via `protojson` — use it when you want to feed downstream tooling.

`--element` is required; the command exits 2 if it is missing or if no element with that ID exists in the corpus. Run `sheaf report --format csv` first to find the exact ID you need.

## Options

| Flag | Default | Notes |
|---|---|---|
| `--element`  | *(required)*             | Full ContractElement ID. |
| `--config` | `<repo>/sheaf.textproto` | Path to the project config. |
| `--repo`   | `.`                      | Project repo root. |
| `--format`  | `text`                   | Output as `text` or `json`. |

## Examples

Inspect one FIDL method's docs and tests:

```sh
sheaf coverage --element 'fuchsia.io/Directory.Open'
```

Dump the proto profile for downstream consumption:

```sh
sheaf coverage --element 'kubectl get' --format json > profile.json
```

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Profile printed. |
| `1` | Pipeline failed. |
| `2` | `--element` missing or unknown. |
| `3` | Config or rules failed to load. |

## See also

- [`sheaf report`](sheaf_report.md) — bulk dump of every profile.
- [`sheaf gaps`](sheaf_gaps.md) — the findings produced from these profiles.
