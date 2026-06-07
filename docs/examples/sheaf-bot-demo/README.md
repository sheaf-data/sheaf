# Sheaf PR-review bot demo

Sheaf is intentionally **git-agnostic** — it doesn't clone repos, fetch refs, or parse provider-native webhook payloads. The bot logic is exposed two ways:

1. **`sheaf review` CLI** — one-shot, perfect for CI steps (GitHub Actions, Gerrit verify jobs, etc.).
2. **`review_pr` MCP operation** — programmatic, for any caller that can speak JSON-RPC over HTTP.

Either surface takes a **PR reference**, a **pre-checked-out base path**, and a **pre-checked-out head path**. The implementor handles the git plumbing (checkout, fetch, worktree management) in whatever way fits their pipeline; Sheaf does the scan + delta + render + (optional) post.

This demo synthesizes a tiny "widgets" project with a base + head state and shows both surfaces producing the same review comment.

## The synthetic project

- **`base/`** — version 1 of the API: a single well-documented and tested `Widget.Create` method.
- **`head/`** — version 2: adds a new well-documented `Widget.Delete` method but no test for it.

The delta is exactly the kind of change a real PR introduces. We expect Sheaf to flag the new method as `COVERAGE_DELTA` (it's new) + `DOCUMENTED_UNTESTED` (it has substantive docs but no test) + `MISSING_IN_CATEGORY` (no entries in `tests.unit_tests`).

```
sheaf-bot-demo/
├── README.md                          ← this file
├── sheaf.textproto                    ← config used for both base + head scans
├── categorization-rules.textproto     ← source map (per-project bucketing)
├── base/
│   ├── sdk/fidl/widgets/widgets.fidl  ← v1: Widget.Create only
│   ├── src/widgets/widget_test.cc     ← 2 tests for Create
│   ├── sheaf.textproto                ← copy (so the scan finds it at repo root)
│   └── categorization-rules.textproto ← copy
├── head/
│   ├── sdk/fidl/widgets/widgets.fidl  ← v2: Widget.Create + Widget.Delete
│   ├── src/widgets/widget_test.cc     ← still only 2 Create tests
│   ├── sheaf.textproto                ← copy
│   └── categorization-rules.textproto ← copy
├── out/                               ← created at runtime by the `file` adapter;
│                                         holds one comment .md per PR (not committed)
├── cli-stdout.txt                     ← captured `sheaf review` output
├── mcp-stdout.txt                     ← captured `sheaf serve` startup output
└── mcp-review-response.json           ← captured `review_pr` JSON-RPC response
```

## Path 1: CLI (`sheaf review`)

```sh
sheaf review \
  --repo head \
  --base base \
  --pr   github:example/widgets#42 \
  --post --review file --file-out out
```

Output:
```
Posted via file → file:///…/out/github-example-widgets-42.md
---
## Sheaf review · github:example/widgets#42 — touches 1 contract element(s)

### `widgets/Widget.Delete`

- **COVERAGE_DELTA** — new element added in this revision
- **DOCUMENTED_UNTESTED** — element is documented but has no test references
- **MISSING_IN_CATEGORY** — no references in category "tests.unit_tests"
```

The `file:///…/out/github-example-widgets-42.md` path above is written by the `file` adapter **at runtime** when you run the command — the `out/` directory and its comment files are not committed to the repo.

Without `--post`, the markdown body still prints to stdout — implementors can capture it and route however they want (curl into a webhook, email, etc.).

## Path 2: MCP `review_pr` operation

```sh
sheaf serve --repo head --bind 127.0.0.1 --port 17822
# review_pr enabled (adapter=noop)
# MCP server listening on 127.0.0.1:17822
```

```sh
curl -X POST http://127.0.0.1:17822/mcp \
  -H 'Content-Type: application/json' \
  -d '{
    "jsonrpc": "2.0",
    "id":      1,
    "method":  "review_pr",
    "params":  {
      "pr_ref":    "github:example/widgets#44",
      "base_path": "/abs/path/to/base",
      "head_path": "/abs/path/to/head",
      "post":      false
    }
  }'
```

Response (full file: [mcp-review-response.json](mcp-review-response.json)):
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "pr_ref": "github:example/widgets#44",
    "comment_md": "## Sheaf review · …",
    "affected_elements": ["widgets/Widget.Delete"],
    "suggested_reviewers": null,
    "subscribers": null,
    "posted": false
  }
}
```

When `post: true`, the server also writes/posts the comment via the project's configured `review` adapter and returns `posted: true` + `posted_to` + `adapter` fields.

## Adapters

The `--review` flag (CLI) and the project's `review {}` block (config) select where the comment lands:

| Adapter | What it does |
|---|---|
| `noop` | Logs the comment to stderr; doesn't actually post anywhere. Default. |
| `file` | Writes to disk. `--file-out` controls path: directory → one file per PR; file path → single file overwritten per call. |
| `gerrit` | POSTs to a Gerrit server via the REST API (basic auth, password from env var). |
| `github` | POSTs to a GitHub repo's PR via the GitHub REST API (bearer token from env var). |

For demos and CI artifacts, `file` is the most useful. For production, `gerrit` and `github` post real comments.

## How implementors plug git in

Sheaf takes paths — the implementor handles getting those paths populated:

- **GitHub Actions:** [`actions/checkout`](https://github.com/actions/checkout) twice (once for `base`, once for `head`), then `sheaf review --post`.
- **Gerrit Zuul:** fetcher already produces worktrees; pass them as `--base` and `--head`.
- **Custom poller / webhook receiver:** receive the provider's native event, clone/fetch as needed, hit `POST /mcp` `review_pr`.

This composition means Sheaf doesn't depend on any specific git tooling or webhook shape; it's purely "two paths in, one comment out."

## Reproducing the demo

```sh
# From this directory:
sheaf review --repo head --base base --pr "github:example/widgets#42" \
  --post --review file --file-out out
```

The synthetic fixture is self-contained — no external dependencies required.
