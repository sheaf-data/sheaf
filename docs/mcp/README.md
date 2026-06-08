# Sheaf MCP

The MCP server is the integration surface for any client that needs Sheaf's corpus over the network — coding agents (Claude, Cursor, Cline, …), the `scanner` binary, CI bots, custom dashboards.

- **[api.md](api.md)** — wire protocol, every JSON-RPC method, params, return shape, error codes, auth.
- **[schema.md](schema.md)** — the proto messages each result payload carries.
- **[tools.md](tools.md)** — per-tool input JSON Schemas + example prompts (when/why an agent calls each). Machine-readable: [tool-schemas.json](tool-schemas.json).

## At a glance

| Stage | Doc |
|---|---|
| Start the server          | [`sheaf serve`](../cli/reference/sheaf_serve.md) |
| List libraries            | [`list_libraries`](api.md#list_libraries) |
| Bulk-pull one library     | [`library_snapshot`](api.md#library_snapshot) |
| Drill into one element    | [`query_contract`](api.md#query_contract), [`coverage`](api.md#coverage) |
| Find by description       | [`find_examples`](api.md#find_examples) |
| Verify an invocation      | [`verify_invocation`](api.md#verify_invocation) |
| Worklist of gaps          | [`find_coverage_gaps`](api.md#find_coverage_gaps) |
| PR review                 | [`review_pr`](api.md#review_pr) |
| Health probe              | `GET /healthz` |

## Configure

The relevant blocks in `sheaf.textproto`:

```textproto
mcp_server {
  bind: "127.0.0.1"
  port: 7700
  cache_ttl_seconds: 3600
  auth { mode: BEARER bearer_token_env: "SHEAF_TOKEN" }
  operation_cache { op: "library_snapshot" ttl_seconds: 600 }
}

llm {
  embeddings: "ollama-embed"
  ollama_embeddings { host: "127.0.0.1" port: 11434 model: "nomic-embed-text" }
}

review { github { repo: "owner/repo" token_env: "GITHUB_TOKEN" } }
```

See [docs/config.md](../config.md) for the full message.
