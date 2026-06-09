# Connecting MCP clients to sheaf

Coding agents — Claude Desktop, Cursor, Cline, Continue, OpenAI Codex CLI, Gemini CLI — connect to an MCP server by **spawning it as a subprocess and talking over stdin/stdout**. Sheaf speaks that transport with [`sheaf serve --stdio`](../cli/reference/sheaf_serve.md). This page has copy-paste config for each; the wire protocol behind them is in [api.md](api.md#stdio-sheaf-serve---stdio).

The setup is the same everywhere — a named server with a `command` and `args`. Most clients write it as JSON under an `mcpServers` object; Codex uses the TOML equivalent. Only the config file's location and format differ.

Prefer to copy a file rather than a snippet? Ready-to-edit configs for every client are in **[examples/](examples/README.md)**.

## Prerequisites

1. **`sheaf` installed and runnable.** `go install github.com/sheaf-data/sheaf/cmd/sheaf@latest` or a [release binary](https://github.com/sheaf-data/sheaf/releases). Check: `sheaf version`.
2. **A configured repo** — a `sheaf.textproto` at the repo root (or anywhere, passed via `--config`). Check: `sheaf doctor --repo /abs/path/to/repo`.
3. **Use absolute paths.** A GUI client spawns `sheaf` from *its* working directory, not your repo, so relative paths won't resolve. Have your repo's absolute path ready.

The command every client ends up running:

```bash
sheaf serve --stdio --repo /abs/path/to/repo
# add --config /abs/path/to/sheaf.textproto if it isn't at the repo root
```

> **PATH gotcha (read this first if nothing works).** GUI apps often don't inherit your shell's `PATH`, so a bare `"command": "sheaf"` may fail with "server not found". The reliable fix in every config below is to use the **absolute path to the binary** — run `which sheaf` (e.g. `/Users/you/go/bin/sheaf`) and use that as `command`.

## Claude Desktop

Config file (open it from **Settings → Developer → Edit Config**):

- **macOS:** `~/Library/Application Support/Claude/claude_desktop_config.json`
- **Windows:** `%APPDATA%\Claude\claude_desktop_config.json`

Add sheaf under `mcpServers`:

```json
{
  "mcpServers": {
    "sheaf": {
      "command": "/abs/path/to/sheaf",
      "args": ["serve", "--stdio", "--repo", "/abs/path/to/repo"]
    }
  }
}
```

Restart Claude Desktop. **Verify:** the tools (🔨) control lists sheaf's tools (`query_contract`, `coverage`, `find_coverage_gaps`, …). Ask: *"Use sheaf to list the libraries in this project."*

Logs (when something's off): `~/Library/Logs/Claude/mcp-server-sheaf.log` (macOS).

## Cursor

Config file — pick the scope:

- **Per-project:** `.cursor/mcp.json` in the repo (commit it to share with collaborators).
- **Global:** `~/.cursor/mcp.json`.

```json
{
  "mcpServers": {
    "sheaf": {
      "command": "/abs/path/to/sheaf",
      "args": ["serve", "--stdio", "--repo", "/abs/path/to/repo"]
    }
  }
}
```

Open **Cursor → Settings → MCP** (a.k.a. *Tools & Integrations*) and confirm **sheaf** is listed and toggled on. **Verify:** the MCP panel shows sheaf's tools, or ask the agent to call one.

## Cline (VS Code)

In the Cline panel, open **MCP Servers → Configure MCP Servers** (it opens `cline_mcp_settings.json`) and add:

```json
{
  "mcpServers": {
    "sheaf": {
      "command": "/abs/path/to/sheaf",
      "args": ["serve", "--stdio", "--repo", "/abs/path/to/repo"],
      "disabled": false
    }
  }
}
```

Cline reloads its MCP servers on save; sheaf's tools appear in the server's tool list.

## Continue

Edit `~/.continue/config.yaml` and add an `mcpServers` entry:

```yaml
mcpServers:
  - name: sheaf
    command: /abs/path/to/sheaf
    args:
      - serve
      - --stdio
      - --repo
      - /abs/path/to/repo
```

Reload Continue (or restart your IDE). Sheaf's tools become available to the agent.

## OpenAI Codex CLI

Codex configures MCP servers in **TOML**, in `~/.codex/config.toml`. Add a table:

```toml
[mcp_servers.sheaf]
command = "/abs/path/to/sheaf"
args = ["serve", "--stdio", "--repo", "/abs/path/to/repo"]
```

(Equivalently: `codex mcp add sheaf -- /abs/path/to/sheaf serve --stdio --repo /abs/path/to/repo`, if your Codex version ships the `mcp add` subcommand.) Start `codex`; it launches the server on demand. **Verify:** ask Codex to use a sheaf tool, e.g. *"with sheaf, find coverage gaps in this repo."*

## Gemini CLI

Gemini configures MCP servers as **JSON** in `~/.gemini/settings.json` (global) or `.gemini/settings.json` (per-project) — the same `mcpServers` shape as above:

```json
{
  "mcpServers": {
    "sheaf": {
      "command": "/abs/path/to/sheaf",
      "args": ["serve", "--stdio", "--repo", "/abs/path/to/repo"]
    }
  }
}
```

Restart `gemini` (or run `/mcp` inside it to list servers). **Verify:** `/mcp` shows `sheaf` connected and lists its tools.

## Verifying the wiring

Before blaming the client, confirm the command itself works — pipe a handshake straight in:

```bash
printf '%s\n%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
  | sheaf serve --stdio --repo /abs/path/to/repo
```

Two JSON-RPC responses on stdout (an `initialize` result, then the tool list) means the binary and config are good — any remaining failure is in the client wiring, not sheaf.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Client shows no sheaf tools / "server failed to start" | `sheaf` not on the GUI app's `PATH` | Use the absolute binary path as `command` (`which sheaf`). |
| Immediate exit / "config not found" | relative `--config`/`--repo`, or wrong path | Use **absolute** paths — the client's working directory is not your repo. |
| Tools appear, but every call errors | repo has no / an invalid `sheaf.textproto` | Run `sheaf doctor --repo /abs/path/to/repo` and fix the config first. |
| Client logs show "unexpected token" / framing errors | something wrote to **stdout** | Stdout is the protocol channel. If you wrapped `sheaf` in a launcher script, make sure the wrapper prints nothing to stdout (send any echoes to stderr). |
| First response is slow | the server runs a full scan at startup | Expected — same cost as `sheaf scan`; larger repos take longer. |

Sheaf writes all diagnostics to **stderr**, which these clients capture in their MCP logs — that's the first place to look.

> Exact menu paths and config-file locations move between client releases; the `{command, args}` shape above is stable. If a path here is stale for your version, search the client's docs for "MCP" — the server entry is the same.
