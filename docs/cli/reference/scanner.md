# scanner

Connect to a running `sheaf serve` MCP server, pick a library, and write a self-contained HTML fragmentation report.

## Synopsis

```text
scanner [--server <url>] [--token-env <var>]
        [--library <name>] [--ecosystem <id>] [--api-level <lvl>]
        [--list] [-o <path>] [--quiet]
        [--source-url-template <tmpl>] [--repo-root <path>]
        [--header-style full|hero|minimal] [--commit <hash>]
        [--mock-overlap]
        [--snapshot-out <path>] [--from-snapshot <path>]
```

## Description

`scanner` is the HTML report generator. It is a separate binary from `sheaf` so the heavy report-rendering deps stay out of the main CLI.

Two execution modes are supported:

1. **Server-backed (default).** Connect to `--server`, list libraries, prompt for a selection (skipped when `--library` is set), pull a `library_snapshot`, and render. `--snapshot-out <path>` persists the snapshot for offline replay, but is **deprecated** — prefer `sheaf snapshot --out`, which produces the same JSON in-process without a server.
2. **Offline (`--from-snapshot <path>`).** Skip the server entirely and render directly from a previously-saved snapshot JSON. Use when iterating on the report template, when the source server is no longer running, or when you need a hermetic build.

The renderer turns the snapshot into a single HTML file with the worklist, coverage matrix, finding rollup, and per-element drill-downs.

## Options

| Flag | Default | Notes |
|---|---|---|
| `--server <url>`            | `http://127.0.0.1:7700` | MCP server URL. |
| `--token-env <var>`         | `""`                    | Env var holding a bearer token, when the server requires auth. |
| `--library <name>`          | *(picker)*              | Library to report on; skips the interactive picker. |
| `--ecosystem <id>`          | `fidl`                  | Rendering shape (not the project name). One of: `fidl` (Protocols + Methods + Types), `cli` (Commands + Flags — cobra / argh / any subcommand-tree), `proto` (Services + Methods + Messages — gRPC / envoy xDS / kubernetes API). Unknown ids render as "element / elements" with no tier shape. The footer carries the id verbatim (`{ecosystem} surface`). |
| `--api-level <lvl>`         | `HEAD`                  | Numeric (e.g. `27`), `HEAD`, or `NEXT`. Drives removed-in-the-past detection. |
| `--list`                    | `false`                 | List libraries on the server and exit. |
| `-o <path>`                 | `<library>-report.html` | Output path for the rendered HTML. |
| `--quiet`                   | `false`                 | Suppress progress chatter. |
| `--source-url-template`     | `""`                    | URL pattern; placeholders: `{path}`, `{abs_path}`, `{line}`. |
| `--repo-root <path>`        | `""`                    | Required when `--source-url-template` uses `{abs_path}`. |
| `--header-style <style>`    | `full`                  | `full` (5-KPI numtable), `hero` (single % tile), `minimal` (one-line). |
| `--commit <hash>`           | `""`                    | Short git hash rendered in the minimal / hero header strip. |
| `--mock-overlap`            | `false`                 | Replace UpSet data with a richer fictional set — design-iteration only. |
| `--snapshot-out <path>`     | `""`                    | **DEPRECATED** — persist the fetched snapshot to disk for later replay. Prefer `sheaf snapshot --out` (in-process, no server). |
| `--from-snapshot <path>`    | `""`                    | Render from a saved snapshot; skips the server connect entirely. |

## Examples

Pick a library interactively against `localhost:7700`:

```sh
scanner
```

List the libraries the server offers and exit:

```sh
scanner --list
```

Skip the picker and write to a named file:

```sh
scanner --library fuchsia.io -o fuchsia-io-report.html
```

Save the snapshot for offline replay, then re-render after a template edit:

```sh
scanner --library docker --snapshot-out docker-snap.json -o docker.html
scanner --from-snapshot docker-snap.json -o docker.html
```

Generate a report against an authenticated server:

```sh
SHEAF_TOKEN=abc123 scanner --server https://sheaf.example.com --token-env SHEAF_TOKEN \
    --library envoy -o envoy.html
```

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Report written. |
| `1` | Selection error, library not found, or server returned no libraries. |
| `2` | Flag parse error. |
| `3` | Network, snapshot read, or output write failure. |

## See also

- [`sheaf serve`](sheaf_serve.md) — the MCP server the scanner connects to.
- [`sheaf snapshot`](sheaf_snapshot.md) — the preferred, in-process way to persist a Snapshot JSON (replaces `scanner --snapshot-out`).
- [`sheaf render`](sheaf_render.md) — the preferred, in-process way to render a saved Snapshot JSON.
