# sheaf snapshot

Emit a library's Snapshot JSON in-process — no server — for offline rendering, diffing, or downstream tooling.

## Synopsis

```text
sheaf snapshot --library <name>
               [--config <path>] [--repo <path>]
               [--library-label <label>]
               [--out <path>]
```

## Description

`sheaf snapshot` runs the full pipeline and emits the library **Snapshot**: the exact data the report builder consumes and the MCP server's `library_snapshot` op serves. It is the in-process equivalent of fetching `library_snapshot` from a running `sheaf serve`, with no server to start.

The output is JSON with a `schema_version` field. Pipe it into `scanner --from-snapshot` to render a report offline, diff it across commits to watch contract movement, or feed it to another tool:

```sh
sheaf snapshot --library docker --out docker-snap.json
scanner --from-snapshot docker-snap.json --library docker -o docker.html
```

`--library` accepts a comma-separated list to roll several libraries into a single snapshot; use `--library-label` to name the combined result (defaults to the comma-joined names).

By default the JSON is written to stdout. With `--out <path>` it is written to that file (parent directories are created) and a one-line summary goes to stderr.

### Schema version

Every snapshot is stamped with the producer's `schema_version`. The offline reader (`scanner --from-snapshot`) enforces it:

- A version that matches the reader's build renders normally.
- A mismatched version is rejected — regenerate the snapshot with `sheaf snapshot`.
- A legacy snapshot with no version (written before versioning existed) renders best-effort with a warning.

## Options

| Flag | Default | Notes |
|---|---|---|
| `--config` | `<repo>/sheaf.textproto` | Path to the project config. |
| `--repo` | `.` | Project repo root. |
| `--library` | *(required)* | Library to snapshot. Comma-separated list rolls several into one. |
| `--library-label` | *(comma-joined names)* | Label for a multi-library snapshot. Ignored for a single library. |
| `--out` | *(stdout)* | Write the snapshot JSON here. Parent dirs are created. |

## Examples

Snapshot one library to stdout:

```sh
sheaf snapshot --library docker
```

Snapshot to a file and render offline:

```sh
sheaf snapshot --library docker --out docker-snap.json
scanner --from-snapshot docker-snap.json --library docker -o docker.html
```

Roll several libraries into one labelled snapshot:

```sh
sheaf snapshot --library docker,compose --library-label "Docker CLI" --out docker.json
```

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Snapshot written. |
| `1` | Pipeline failed or write failed. |
| `2` | Missing `--library` or bad flag. |
| `3` | Config failed to load. |

## See also

- [`sheaf render`](sheaf_render.md) — the preferred, in-process consumer: `sheaf render --from-snapshot <file.json>` turns this Snapshot into an HTML report with no server.
- [`scanner`](scanner.md) — also consumes `--from-snapshot` to render the single-file HTML report (the older two-binary path).
- [`sheaf serve`](sheaf_serve.md) — serves the same Snapshot over MCP as `library_snapshot`.
- [`sheaf report`](sheaf_report.md) — bulk-dump coverage profiles as CSV/JSON/HTML.
