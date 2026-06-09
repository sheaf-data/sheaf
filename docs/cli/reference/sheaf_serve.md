# sheaf serve

Run the ingest → index pipeline once and then keep the corpus live behind an MCP server so coding agents and the `scanner` binary can query it.

## Synopsis

```text
sheaf serve [--config <path>] [--repo <path>]
            [--bind <host>] [--port <n>] [--stdio]
```

## Description

`sheaf serve` is the long-running counterpart to `sheaf scan`. It runs the same pipeline (so its startup cost is roughly the same as a scan), then binds an HTTP listener that speaks the Sheaf MCP dialect:

- contract-element lookup
- coverage-profile lookup
- worked-example retrieval (semantic when an embedder is configured)
- library snapshot (`library_snapshot` — the operation the `scanner` binary consumes)
- PR review (`review_pr` — base + head paths handed in per-request)

Bind address and port come from the `mcp_server { bind: ... port: ... }` block in `sheaf.textproto`, with sane defaults of `127.0.0.1:7700`. `--bind` and `--port` override the config-provided values.

`serve` is foreground; it returns when the listener errors. Run with `&` (or under a process supervisor) when you want it in the background.

With `--stdio`, `serve` speaks MCP over **newline-delimited JSON-RPC on stdin/stdout** instead of binding HTTP — the transport desktop MCP clients (Claude Desktop, Cursor, Cline, Continue) use when they launch the server as a subprocess. In that mode stdout is the protocol channel, so all banners and logs go to stderr. See [docs/mcp/api.md](../../mcp/api.md#stdio-sheaf-serve---stdio) for the framing and a `claude_desktop_config.json` snippet. `--bind`/`--port` are ignored under `--stdio`.

## Options

| Flag | Default | Notes |
|---|---|---|
| `--config` | `<repo>/sheaf.textproto` | Path to the project config. |
| `--repo`   | `.`                      | Project repo root. |
| `--bind`   | from config              | Override `mcp_server.bind` (HTTP only). |
| `--port`      | from config              | Override `mcp_server.port` (HTTP only). |
| `--stdio`  | `false`                  | Speak MCP over stdin/stdout (newline-delimited JSON-RPC) for desktop clients, instead of HTTP. |

## Examples

Bring the server up on the configured port:

```sh
sheaf serve --repo /path/to/your/repo
```

Background the server, render a report against it, kill it:

```sh
sheaf serve --repo /path/to/your/repo --port 7700 &
scanner --server http://127.0.0.1:7700 --library yourlib -o report.html
kill %1
```

Override the listener bind / port for ad-hoc multi-corpus inspection:

```sh
sheaf serve --repo /tmp/projA --port 7700 &
sheaf serve --repo /tmp/projB --port 7701 &
```

## Wire protocol

For the full JSON-RPC method list, params, return shapes, error codes, and auth model, see [docs/mcp/api.md](../../mcp/api.md). The proto messages each operation returns are indexed at [docs/mcp/schema.md](../../mcp/schema.md).

## MCP-server config knobs

Beyond `--bind` and `--port`, the `mcp_server` config block also accepts:

- `cache_ttl_seconds` — TTL for the in-memory snapshot cache.
- `auth { mode: BEARER bearer_token_env: "ENV_NAME" }` — bearer-token auth for the listener; the token is read from the named environment variable.

See [docs/config.md](../../config.md) for the full message.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Listener exited cleanly (rare; usually killed). |
| `1` | Pipeline succeeded but the listener errored. |
| `2` | Bad flag. |
| `3` | Config or rules failed to load. |

## See also

- [`scanner`](scanner.md) — primary consumer of the server's `library_snapshot` op.
- [`sheaf review`](sheaf_review.md) — uses the same review adapter chain.
