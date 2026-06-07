# dump-elements

Debug helper — run the FIDL contract-anchor adapter directly against a repo and print a per-kind summary, a per-library summary, a relationship-kind histogram, and a sample of protocols and their `composes` lists.

## Synopsis

```text
dump-elements [--repo <path>] [--library <pattern>] [--include <glob>]
```

## Description

`dump-elements` is a one-off diagnostic. It bypasses `sheaf.textproto`, instantiates the FIDL adapter with the given include glob, and walks the resulting `ContractElement` list and `DocClaim` list. Use it to confirm adapter behaviour when the elements you expect are missing from a scan, or to size up the contract surface of a new FIDL library before wiring up a full config.

The output sections are:

1. Top-level counts: total elements, doc claims, documented elements, `PARTIAL+` doc claims.
2. Per-kind histogram (PROTOCOL, METHOD, TYPE, …).
3. Per-library histogram (alphabetical).
4. Relationship-kind histogram (`COMPOSED_FROM`, …).
5. A sample of up to 10 protocols and their `composes` targets.

No other adapters run. Tests, docs, examples, indexer, analyzers are all skipped.

## Options

| Flag | Default | Notes |
|---|---|---|
| `--repo <path>`       | `.`                       | Repo root to scan. |
| `--library <pattern>` | `fuchsia.io`              | Library name or wildcard pattern (e.g. `fuchsia.driver.*`). |
| `--include <glob>`    | `sdk/fidl/**/*.fidl`      | Include glob for FIDL source files. |

## Example

```sh
dump-elements --repo /Volumes/T7/sheaf-workspace/checkouts/fuchsia \
              --library 'fuchsia.driver.*' \
              --include 'sdk/fidl/**/*.fidl'
```

## See also

- [`dump-profile`](dump-profile.md) — sibling debug helper that runs the full pipeline against one element.
- [`sheaf scan`](sheaf_scan.md) — the production scan.
