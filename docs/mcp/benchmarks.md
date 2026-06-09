# MCP per-tool benchmarks

Per-tool latency and response size for the sheaf MCP server, measured by
[`cmd/sheaf-mcp-bench`](../../cmd/sheaf-mcp-bench). The harness builds a
real corpus (the same ingest→index pipeline `sheaf serve` runs), then
drives each operation N times over the in-process stdio transport.

## What's measured

- **Latency** is the operation's in-process compute cost. The stdio pipe
  handoff is negligible, and the MCP `tools/call` envelope adds only a
  small constant wrapper over these bare-method calls.
- **Response size** is bytes. Token cost tracks bytes closely (~4
  bytes/token for JSON) and is provider-independent, so bytes are the
  stable proxy; the module deliberately avoids a tokenizer dependency.
- `review_pr` is excluded — it scans two external workspaces per call, so
  its cost is dominated by a full scan, not MCP serving.

## Run it

```bash
go run ./cmd/sheaf-mcp-bench --config sheaf.textproto --repo . --n 50
# the table goes to stdout; redirect to save it:  … > bench.md
```

## Sample results

Against sheaf's own self-scan corpus (77 elements, 842 tests, 1019 doc
claims), 50 iterations each. **Absolute numbers are machine-specific —
regenerate locally; the *shape* is the point.**

| Tool | p50 | p95 | response bytes |
|---|--:|--:|--:|
| `tools/list` | 0.033 ms | 0.146 ms | 829 |
| `list_libraries` | 0.042 ms | 0.099 ms | 123 |
| `find_coverage_gaps` | 0.036 ms | 0.204 ms | 743 |
| `find_examples` | 0.055 ms | 0.105 ms | 85 |
| `query_contract` | 0.078 ms | 0.244 ms | 641 |
| `coverage` | 0.026 ms | 0.092 ms | 182 |
| `library_snapshot` | 9.143 ms | 11.694 ms | 116001 |

## Takeaway

Every **targeted** lookup is sub-millisecond and small (hundreds of
bytes) — cheap to call mid-conversation. The outlier is
**`library_snapshot`**: ~9 ms and ~116 KB because it bulk-pulls an entire
library (it exists for the report generator, which wants everything at
once). An agent answering a specific question should prefer
`query_contract` / `coverage` / `find_examples` and reach for
`library_snapshot` only when it genuinely needs the whole surface — that's
the difference between a few hundred tokens and ~30k.

> `find_examples` here uses the token-overlap path. With an embedder
> configured (`llm` block) it runs a semantic search whose first call also
> pays a one-time embedding pass over the corpus; cached thereafter.
