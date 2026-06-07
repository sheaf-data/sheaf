# dump-profile

Debug helper — run the full pipeline (ingest + index) against a repo and dump one element's `CoverageProfile` as pretty-printed JSON.

## Synopsis

```text
dump-profile [--repo <path>] [--config <path>] [--rules <path>]
             [--element <id>] [--list] [--summary]
```

## Description

`dump-profile` runs the orchestrator end-to-end, then either:

- prints the JSON of the named element's `CoverageProfile` (default),
- prints every element ID matching the supplied glob (`--list`), or
- prints a TABLE summary of profiles with any non-empty test or doc bucket (`--summary`).

Use `--list` to discover the exact element ID you need before drilling in with `--element`.

When `--element` is supplied but no profile matches, the command exits 2 and writes a hint to stderr with the total element count (so you can tell whether ingest produced zero elements vs. simply a typo in the element ID).

## Options

| Flag | Default | Notes |
|---|---|---|
| `--repo <path>`        | `.`                                  | Repo root to scan. |
| `--config <path>`      | `""`                                 | `sheaf.textproto` path; required. |
| `--rules <path>`       | `""`                                 | Source map (`categorization-rules.textproto`) path; optional. |
| `--element <id>`       | `fuchsia.io/Directory.Open`          | ContractElement ID to dump. |
| `--list`               | `false`                              | Print every element ID and exit. |
| `--summary`            | `false`                              | Print a tabular summary of non-empty profiles. |

## Examples

List every element the pipeline produced:

```sh
dump-profile --config /tmp/sheaf.textproto --list
```

Dump the coverage profile for a specific FIDL method:

```sh
dump-profile --config /tmp/sheaf.textproto \
             --element 'fuchsia.io/Directory.Open'
```

Summary of the most-tested elements:

```sh
dump-profile --config /tmp/sheaf.textproto --summary
```

## See also

- [`dump-elements`](dump-elements.md) — sibling debug helper that runs the FIDL adapter alone.
- [`sheaf coverage`](sheaf_coverage.md) — the production version of single-element drill-in.
- [`sheaf report`](sheaf_report.md) — bulk dump of every profile.
