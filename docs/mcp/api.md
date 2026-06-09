# Sheaf MCP API

The MCP server exposed by [`sheaf serve`](../cli/reference/sheaf_serve.md) is a JSON-RPC 2.0 endpoint that serves Sheaf's in-memory corpus to coding agents, the [`scanner`](../cli/reference/scanner.md) binary, and any other client — over **HTTP** or, for desktop MCP clients that spawn the server as a subprocess, over **stdio**. This page documents the wire protocol and every operation. Result payload schemas are defined in proto and indexed at [schema.md](schema.md).

## Transports

`sheaf serve` speaks the same JSON-RPC operations over two transports. Pick by how your client connects.

### HTTP (default)

| Property | Value |
|---|---|
| Protocol | JSON-RPC 2.0 over HTTP/1.1 |
| Path | `POST /mcp` |
| Content-Type | `application/json` |
| Health probe | `GET /healthz` (not RPC; returns `{"status":"ok", ...}`) |
| Default bind | `127.0.0.1:7700` (configurable via `mcp_server.bind` / `mcp_server.port`) |

Operations are reachable directly as JSON-RPC methods (`"method": "library_snapshot"`). This is what the `scanner` binary and HTTP agents use.

### stdio (`sheaf serve --stdio`)

Desktop MCP clients — Claude Desktop, Cursor, Cline, Continue — don't dial a URL; they launch the server as a child process and exchange messages over its stdin/stdout. Run that transport with:

```bash
sheaf serve --stdio --config sheaf.textproto --repo .
```

| Property | Value |
|---|---|
| Protocol | JSON-RPC 2.0, **one compact JSON message per line** (newline-delimited) |
| Input | one request per line on **stdin** |
| Output | one response per line on **stdout** |
| Diagnostics | logs + banners on **stderr** (stdout is the protocol channel — nothing else is written there) |
| Lifecycle | the MCP envelope: `initialize` handshake, then `tools/call` |

Wire it into Claude Desktop (`claude_desktop_config.json`) or any client that takes an MCP command:

```json
{
  "mcpServers": {
    "sheaf": {
      "command": "sheaf",
      "args": ["serve", "--stdio", "--config", "/abs/path/to/sheaf.textproto", "--repo", "/abs/path/to/repo"]
    }
  }
}
```

Per-client setup — Claude Desktop, Cursor, Cline, Continue (config-file locations, how to apply, troubleshooting) — is in **[clients.md](clients.md)**.

#### MCP envelope (`initialize` + `tools/call`)

Spec-compliant MCP clients negotiate before calling tools, so over any transport the server also accepts the MCP envelope:

- `initialize` → returns `{protocolVersion, capabilities:{tools:{}}, serverInfo:{name:"sheaf", version}}`. The client follows with a `notifications/initialized` notification (no response).
- `tools/list` → the tool catalog (see [`tools/list`](#toolslist) below).
- `tools/call` with `{"name": "<op>", "arguments": {…}}` → runs the op named in the [Operations](#operations) reference and returns its result as MCP `content` (text JSON) plus `structuredContent`. An unknown tool is a `-32602` error; an op-level failure comes back as `{"isError": true, "content": […]}`.

The operation reference below documents each op's params and result. Those are what you pass as `tools/call` `arguments` (stdio / MCP clients) or call directly as the JSON-RPC `method` (HTTP / scanner).

## Auth

Auth is configured via `mcp_server.auth` in `sheaf.textproto`:

```textproto
mcp_server {
  bind: "127.0.0.1"
  port: 7700
  auth {
    mode: BEARER
    bearer_token_env: "SHEAF_TOKEN"
  }
}
```

When `mode: BEARER`, every request to `/mcp` must carry `Authorization: Bearer $SHEAF_TOKEN`. The server reads the expected token from the env var named by `bearer_token_env` at request time, so token rotation works without a restart. `mode: NONE` (or absent) disables auth — the `/healthz` endpoint is always unauthenticated.

## Request shape

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "library_snapshot",
  "params": { "library": "kubectl" }
}
```

`id` may be any JSON value (string / number / null); the server echoes it back unchanged. `jsonrpc` is optional but, if present, must be `"2.0"`.

## Response shape

```json
{ "jsonrpc": "2.0", "id": 1, "result": { ... } }
```

or on error:

```json
{ "jsonrpc": "2.0", "id": 1, "error": { "code": -32601, "message": "...", "data": null } }
```

### Error codes

| Code | Meaning |
|---|---|
| `-32700` | Parse error — the request body is not valid JSON. |
| `-32600` | Invalid request — bad `jsonrpc` value. |
| `-32601` | Method not found — `method` doesn't name a known op (or the op is not enabled, e.g. `review_pr` without `WithReview`). |
| `-32602` | Invalid params — required fields missing or wrong type. |
| `-32004` | Not found — the element or profile named by the request doesn't exist. |
| `-32000` | Server error — the underlying op failed (rendering, embedding, review). |

## Operations

Every operation is dispatched by the `method` field. The table below summarises; per-operation sections follow with full schemas.

| Method | Purpose | Result schema |
|---|---|---|
| [`tools/list`](#toolslist)                    | MCP convention: list the operations this server exposes.       | `{ tools: [{name, description}] }` |
| [`list_libraries`](#list_libraries)           | Enumerate libraries in the corpus with counts.                | `{ libraries: [{library, elements, profiles, findings}], total }` |
| [`library_snapshot`](#library_snapshot)       | Bulk dump of one library: elements + profiles + findings.     | `{ schema_version, library, elements[], profiles[], findings[], analyzers[] }` |
| [`query_contract`](#query_contract)           | Fetch one `ContractElement` plus a configurable profile slice. | `{ element, [coverage \| tests \| docs \| examples \| usage \| gaps] }` |
| [`coverage`](#coverage)                       | Return one `CoverageProfile` verbatim.                        | `CoverageProfile` |
| [`find_coverage_gaps`](#find_coverage_gaps)   | List findings, filterable by library and kind.                | `{ findings: Finding[], total }` |
| [`find_examples`](#find_examples)             | Semantic or token search for elements matching a query.       | `{ matches: [{element_id, score, ...}], scoringMethod }` |
| [`verify_invocation`](#verify_invocation)     | Check whether an invocation string maps to a real element.    | `{ matched, element?, candidate?, confidence, reason? }` |
| [`review_pr`](#review_pr) *(optional)*        | Run the PR-bot flow against two pre-checked-out paths.        | `{ pr_ref, comment_md, affected_elements, ... }` |

---

### `tools/list`

MCP convention. No params. Returns the canonical list of operations this server enables (so an agent can discover what's available without a hard-coded vocabulary). When `review_pr` was wired up via `WithReview`, it appears in the list; otherwise it does not.

```json
{
  "jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": {}
}
```

```json
{
  "tools": [
    { "name": "query_contract",      "description": "Return ContractElement and its CoverageProfile (or a subtree)" },
    { "name": "coverage",            "description": "Return the CoverageProfile for an element" },
    { "name": "find_coverage_gaps",  "description": "List findings, filterable by library and kind" },
    { "name": "find_examples",       "description": "Search for elements matching a query (semantic if embedder configured, else token-overlap)" },
    { "name": "verify_invocation",   "description": "Check whether an invocation string matches a real element" },
    { "name": "list_libraries",      "description": "List libraries in the corpus with element/profile/finding counts" },
    { "name": "library_snapshot",    "description": "Return all ContractElements, CoverageProfiles and Findings for one library — the bulk read used by the report generator" },
    { "name": "review_pr",           "description": "Run a PR review: scan base + head paths, render comment, optionally post via configured review adapter" }
  ]
}
```

---

### `list_libraries`

Enumerate the libraries present in the corpus. One entry per distinct `ContractElement.library`, with per-bucket counts.

**Params:** none.

**Result:**

```json
{
  "libraries": [
    { "library": "docker",  "elements": 412, "profiles": 412, "findings":  17 },
    { "library": "kubectl", "elements": 263, "profiles": 263, "findings":  44 }
  ],
  "total": 2
}
```

Use this as the picker step before `library_snapshot`. The `scanner` binary calls it to render its `--list` output and the interactive picker.

---

### `library_snapshot`

Bulk dump of one library's `ContractElements`, `CoverageProfile`s, `Finding`s, and the configured analyzer names — one round trip, suitable for downstream rendering.

**Params:**

```json
{ "library": "kubectl" }   // required
```

**Result:**

```json
{
  "schema_version": 1,
  "library":   "kubectl",
  "elements":  [ContractElement, ...],
  "profiles":  [CoverageProfile, ...],
  "findings":  [Finding, ...],
  "analyzers": ["thin-reference", "documented-untested", "tested-undocumented"]
}
```

`elements` and `profiles` are aligned by `element_id`. `findings` are filtered to the library by matching `Finding.subject` against the library name with the four separators Sheaf uses across ecosystems (`fuchsia.io/Node`, `grpc.health.v1.Health`, `kubectl annotate`, or the bare library element itself). `analyzers` distinguishes "no anomalies because nothing ran" from "no anomalies because the analyzers ran clean."

`schema_version` is the snapshot schema the server stamped. Offline readers (`scanner --from-snapshot`) reject a snapshot whose version does not match their build; a snapshot with no version is treated as legacy and rendered best-effort.

Generate the same payload offline — no server — with [`sheaf snapshot`](../cli/reference/sheaf_snapshot.md), then re-render it via `scanner --from-snapshot`. (The `scanner` binary's own `--snapshot-out` flag persists this verbatim too, but is deprecated in favor of `sheaf snapshot`.)

---

### `query_contract`

Fetch one `ContractElement` plus a configurable slice of its `CoverageProfile`. The default slice is the full profile; pass `subtree` to narrow the response.

**Params:**

```json
{
  "element_id": "kubectl get",
  "subtree":    "tests"        // optional: tests | docs | examples | usage | gaps
}
```

**Result:**

```json
{
  "element": ContractElement,
  "coverage": CoverageProfile         // when subtree is empty
  // OR one of:
  // "tests":     TestCoverage,
  // "docs":      DocCoverage,
  // "examples":  ExampleCoverage,
  // "usage":     UsageCoverage,
  // "gaps":      GapsSummary
}
```

Returns `-32004` when no element matches `element_id`. When the element exists but has no profile, the response omits the coverage / subtree key.

---

### `coverage`

Return the full `CoverageProfile` for one element. Equivalent to `query_contract` with no `subtree`, but skipping the `element` field — used when the caller already knows the element shape.

**Params:**

```json
{ "element_id": "kubectl get" }
```

**Result:** a `CoverageProfile` (see [schema.md](schema.md)).

Returns `-32004` when no profile is registered for `element_id`.

---

### `find_coverage_gaps`

List `Finding`s, optionally filtered by library and finding-kind. Useful when the agent wants a focused worklist rather than the firehose `library_snapshot` returns.

**Params:**

```json
{
  "library":   "kubectl",                          // optional
  "kinds":     ["TESTED_UNDOCUMENTED",
                "THIN_REFERENCE"],                 // optional; FINDING_KIND_ prefix accepted
  "max_items": 50                                  // optional; 0 = no cap
}
```

The library filter accepts the `<lib>/` and `<lib>.` prefixes. Findings whose `subject` doesn't start with one of those prefixes are dropped when `library` is set.

**Result:**

```json
{
  "findings": [Finding, ...],
  "total":    7
}
```

`total` reflects the post-filter result count (i.e. the length of the returned `findings` array), not the unfiltered corpus size.

---

### `find_examples`

Semantic search for `ContractElement`s matching a free-form description. When an embedder is configured (via the LLM section in `sheaf.textproto`), the server ranks by cosine similarity on element-text embeddings; otherwise it falls back to token-overlap scoring against the element `id` only (dotted IDs are split into tokens on `.`).

**Params:**

```json
{
  "query":     "open a directory and list children",
  "max_items": 10                                    // optional; default 10
}
```

**Result:**

```json
{
  "scoringMethod": "semantic:ollama-embed",
  "matches": [
    {
      "element_id":  "fuchsia.io/Directory.Open",
      "score":       0.847,
      "kind":        "METHOD",
      "location":    SourceLocation,
      "doc_excerpt": "Opens a node inside the directory …"
    },
    ...
  ]
}
```

`scoringMethod` is `semantic:<embedder-name>` when the embedder ranked the results, `token-overlap` otherwise. When the embedder errored mid-call and the server fell back to token overlap, the response also carries `fallbackReason: "<error string>"`. Score range is `[0, 1]` for semantic and `[1, N]` for token-overlap (integer count of overlapping tokens).

The first call after server start triggers a background embedding pass over every element when the semantic engine is in use. Subsequent calls hit the in-memory cache (and, if configured, the on-disk embed cache).

---

### `verify_invocation`

Check whether an invocation string — typically scraped from agent-authored code — names a real `ContractElement`. The op is intended to short-circuit hallucinated method calls before the agent commits to them.

**Params:**

```json
{ "invocation": "fuchsia.io/Directory.Open" }
```

**Result (exact match):**

```json
{ "matched": true, "element": ContractElement, "confidence": 1.0 }
```

**Result (fuzzy / no match):**

```json
{
  "matched":    false,
  "candidate":  "fuchsia.io/Directory.OpenDeprecated",
  "confidence": 0.66,
  "reason":     "fuzzy match only — no exact element id"
}
```

When nothing tokenises against the corpus, `candidate` and `confidence` are omitted and `reason` is `"no token overlap with any element"`.

---

### `review_pr`

Run the PR-bot flow against two pre-checked-out paths. Sheaf is git-agnostic; the caller hands it `base_path` + `head_path` workspaces that already point at the right commits.

This op is **opt-in**: the server only exposes it after the host has called `WithReview` (which the CLI does when `sheaf serve` parses `review { ... }` from `sheaf.textproto`). Otherwise calls return `-32601`.

**Params:**

```json
{
  "pr_ref":    "PR#4242",                                 // optional; default "PR#unknown"
  "base_path": "/var/checkouts/repo@main",                // required
  "head_path": "/var/checkouts/repo@feature-x",           // required
  "post":      true                                       // default false; print-only when false
}
```

**Result:**

```json
{
  "pr_ref":              "PR#4242",
  "comment_md":          "## Sheaf coverage delta …",
  "affected_elements":   ["kubectl get", "kubectl get --selector"],
  "suggested_reviewers": ["@kubectl-maintainers"],
  "subscribers":         ["@cluster-team"],
  "posted":              true,
  "posted_to":           "https://github.com/.../pull/4242#issuecomment-…",
  "adapter":             "github"
}
```

`posted_to` and `adapter` appear only when `posted` is `true`.

## Quick examples

```sh
# Health.
curl -s http://127.0.0.1:7700/healthz | jq

# List libraries.
curl -s -X POST http://127.0.0.1:7700/mcp \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"list_libraries"}' | jq

# Bulk-pull one library (the call the scanner makes).
curl -s -X POST http://127.0.0.1:7700/mcp \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"library_snapshot","params":{"library":"kubectl"}}' \
  | jq '{elements: (.result.elements|length), profiles: (.result.profiles|length), findings: (.result.findings|length)}'

# Semantic search (requires LLM block configured).
curl -s -X POST http://127.0.0.1:7700/mcp \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"find_examples","params":{"query":"watch pods across namespaces","max_items":5}}' \
  | jq

# Authenticated request.
curl -s -X POST http://127.0.0.1:7700/mcp \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $SHEAF_TOKEN" \
  -d '{"jsonrpc":"2.0","id":4,"method":"tools/list"}' | jq
```

## See also

- [tools.md](tools.md) — per-operation input JSON Schemas + the example prompts that route an agent to each tool. Machine-readable export: [tool-schemas.json](tool-schemas.json).
- [schema.md](schema.md) — the proto messages every result payload uses.
- [`sheaf serve`](../cli/reference/sheaf_serve.md) — how to start the server.
- [`scanner`](../cli/reference/scanner.md) — primary consumer of `library_snapshot`.
- [docs/config.md](../config.md) — the `mcp_server`, `auth`, and `llm` config blocks referenced above.
