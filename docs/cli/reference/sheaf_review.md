# sheaf review

Render a PR-review comment from a base + head pair of corpora and optionally post it via the configured review adapter.

## Synopsis

```text
sheaf review --base <path> [--repo <path>] [--pr <ref>]
             [--config <path>]
             [--post] [--review noop|file|gerrit|github]
             [--file-out <path>]
             [--emit-json <path>] [--emit-base-ref <sha>] [--emit-head-ref <sha>]
             [--emit-system <name>] [--emit-scanned-at <rfc3339>]
```

## Description

`sheaf review` scans two checkouts of the same project — `--base` (the PR target) and `--repo` (the PR head, defaulting to the current directory) — using the same `sheaf.textproto`, builds a delta, and renders a Markdown comment body summarising what coverage was gained or lost between the two refs.

The rendered comment always goes to stdout. When `--post` is set, the same comment is also delivered via the review adapter resolved as follows:

1. `--review` overrides the config-provided adapter:
   - `noop` — no-op delivery (still prints to stdout).
   - `file` — write to `--file-out`. If `--file-out` is a directory or ends with a path separator, the adapter writes one file per PR into it; otherwise it writes a single file. Defaults to the `SHEAF_REVIEW_FILE_OUT` env var when `--file-out` is not set.
   - `gerrit`, `github` — delegate to the typed `review { gerrit { ... } }` / `review { github { ... } }` block in `sheaf.textproto`.
2. If `--review` is empty, the config's `review { ... }` block is used.

`--pr` is purely cosmetic — the value is interpolated into the comment header so the rendered Markdown reads naturally.

The `--emit-*` flags write a structured `delta.json` alongside the Markdown comment. `--emit-json <path>` turns it on; the other four (`--emit-base-ref`, `--emit-head-ref`, `--emit-system`, `--emit-scanned-at`) stamp identifying metadata into it. That `delta.json` is the input to [`sheaf review-html`](sheaf_review-html.md), which renders it into a standalone `comment.html`.

## Options

| Flag | Default | Notes |
|---|---|---|
| `--base`   | *(required)*             | Base repo root (PR base). |
| `--repo`   | `.`                      | Head repo root (PR head). |
| `--config` | `<repo>/sheaf.textproto` | Shared config for both checkouts. |
| `--pr`      | `PR#unknown`             | PR reference shown in the comment header. |
| `--post`          | `false`                  | Deliver via the adapter in addition to printing. |
| `--review` | from config              | `noop`, `file`, `gerrit`, or `github`. |
| `--file-out`  | `$SHEAF_REVIEW_FILE_OUT` | Sink for `--review=file`. |
| `--emit-json` | _(unset)_                | If set, also write the structured `delta.json` artifact to this path. Consumed by [`sheaf review-html`](sheaf_review-html.md) to render `comment.html`. |
| `--emit-base-ref` | _(unset)_            | Base ref (full SHA) to record in `delta.json`. |
| `--emit-head-ref` | _(unset)_            | Head ref (full SHA) to record in `delta.json`. |
| `--emit-system` | _(unset)_              | System name to record in `delta.json` (e.g. `fd`, `envoy`). |
| `--emit-scanned-at` | _(now)_            | Override the `scanned_at` timestamp in `delta.json` (RFC3339). Pass the head ref's commit time for byte-stable regeneration. |

## Examples

Print the comment for PR #4242 without posting:

```sh
sheaf review --base /tmp/main-checkout --repo . --pr PR#4242
```

Write the rendered body to a directory the CI job uploads:

```sh
sheaf review --base /tmp/main --repo . --pr PR#4242 \
             --review file --file-out artifacts/review/ \
             --post
```

Post to GitHub via the configured `review { github { ... } }` block:

```sh
sheaf review --base /tmp/main --repo . --pr PR#4242 --post
```

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Comment rendered (and, if `--post`, delivered). |
| `1` | Render or post failed. |
| `2` | `--base` missing or `--review` named an unknown adapter. |
| `3` | Config or rules failed to load. |

## See also

- [`sheaf review-html`](sheaf_review-html.md) — renders the `delta.json` from `--emit-json` into a standalone `comment.html`.
- [`sheaf serve`](sheaf_serve.md) — the persistent counterpart; reuses the same review-adapter chain through the `review_pr` MCP op.
