# sheaf render

Render the canonical self-contained HTML report from a saved Snapshot JSON, entirely in-process — no server, no rescan.

## Synopsis

```text
sheaf render --from-snapshot <path>
             [--library <label>] [--ecosystem <id>] [--api-level <level>]
             [--repo-root <path>] [--header-style <style>] [--commit <sha>]
             [--source-url-template <pattern>] [--concept-docs-href <href>]
             [-o <path>] [--quiet]
```

## Description

`sheaf render` loads a previously-saved **Snapshot** (the shape `sheaf snapshot` and the MCP server's `library_snapshot` op emit) and renders the single-file HTML report from it. It is the in-binary replacement for `scanner --from-snapshot`, sharing the exact same core (`scanner.RenderSnapshotFileReport`) so the two paths cannot diverge.

When `--repo-root` points at a real git working tree, the report's **Lag** section is computed and rendered — the headline reason to render a snapshot against a checkout rather than the bare snapshot alone. When `--repo-root` is set but `--commit` is not, the scanned repo's short HEAD is derived from it (an explicit `--commit` always wins; a non-git dir stays empty).

The `--ecosystem` flag selects the rendering *shape* — the masthead and the noun vocabulary (e.g. `cli` → commands/flags, `fidl` → protocols/methods/types). The `--source-url-template` turns `path:line` labels into clickable links (`{path}`, `{abs_path}`, `{line}` placeholders; `{abs_path}` needs `--repo-root`).

## Options

| Flag | Default | Notes |
|---|---|---|
| `--from-snapshot` | *(required)* | Path to a Snapshot JSON (e.g. from `sheaf snapshot --out`). |
| `--library` | *(snapshot's own)* | Library label for the report. |
| `--ecosystem` | `fidl` | Rendering shape: `fidl` \| `cli` \| `proto` \| `cpp` \| `openapi` \| … |
| `--api-level` | `HEAD` | Target API level for removed-in-the-past detection (numeric, `HEAD`, or `NEXT`). |
| `-o`, `--output` | `<library>-report.html` | Output HTML path. |
| `--quiet` | `false` | Suppress chatty progress output. |
| `--source-url-template` | *(none)* | URL pattern for `path:line` links (`{path}`, `{abs_path}`, `{line}`). |
| `--repo-root` | *(none)* | Scanned repo root. Required for `{abs_path}` links; enables the Lag section on a git tree. |
| `--header-style` | `full` | Masthead: `full` (5-KPI numtable), `hero` (single % tile), `minimal` (one-line strip). |
| `--commit` | *(derived)* | Short git hash for the minimal/hero header strip. |
| `--concept-docs-href` | *(none)* | Relative href to the library's Concept Docs report; links the reach line. |
| `--mock-overlap` | `false` | Design iteration only: replace the Overlap (UpSet) data with a fictional set. |

## Examples

Snapshot a library and render it offline:

```sh
sheaf snapshot --library docker --out docker-snap.json
sheaf render --from-snapshot docker-snap.json --ecosystem cli \
  --repo-root /path/to/docker -o docker.html
```

Render with a clickable source-link template:

```sh
sheaf render --from-snapshot snapshot.json --ecosystem cli \
  --repo-root /path/to/repo \
  --source-url-template 'https://github.com/org/repo/blob/main/{path}#L{line}' \
  -o report.html
```

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Report written. |
| `1` | Render or write failed. |
| `2` | Missing `--from-snapshot` or a bad flag. |
| `3` | Snapshot schema version mismatch — regenerate with `sheaf snapshot`. |

## See also

- [`sheaf snapshot`](sheaf_snapshot.md) — emits the Snapshot JSON `render` consumes.
- [`sheaf verify`](sheaf_verify.md) — check the snapshot's numbers before rendering.
- [`scanner`](scanner.md) — the standalone `--from-snapshot` renderer `sheaf render` replaces in-binary.
