# MCP client config examples

Ready-to-copy [`sheaf serve --stdio`](../../cli/reference/sheaf_serve.md) configs, one per client. Copy the file into the location below and **replace `/ABSOLUTE/PATH/TO/your-repo`** with your repo's absolute path (and `--config /ABSOLUTE/PATH/TO/sheaf.textproto` if it isn't at the repo root).

| File | Client | Where it goes |
|---|---|---|
| [`claude_desktop_config.json`](claude_desktop_config.json) | Claude Desktop | `~/Library/Application Support/Claude/claude_desktop_config.json` (macOS) · `%APPDATA%\Claude\claude_desktop_config.json` (Windows) |
| [`cursor_mcp.json`](cursor_mcp.json) | Cursor | `.cursor/mcp.json` (per-project) or `~/.cursor/mcp.json` (global) |
| [`cline_mcp_settings.json`](cline_mcp_settings.json) | Cline (VS Code) | via **MCP Servers → Configure MCP Servers** |
| [`continue_config.yaml`](continue_config.yaml) | Continue | `~/.continue/config.yaml` (merge the `mcpServers:` block) |
| [`codex_config.toml`](codex_config.toml) | OpenAI Codex CLI | `~/.codex/config.toml` (merge the `[mcp_servers.sheaf]` table) |
| [`gemini_settings.json`](gemini_settings.json) | Gemini CLI | `~/.gemini/settings.json` (global) or `.gemini/settings.json` (per-project) |

If a file already has other servers/keys, **merge** the `sheaf` entry in rather than overwriting the file.

**PATH note:** GUI clients often don't inherit your shell `PATH`, so a bare `"command": "sheaf"` can fail. If so, replace it with the absolute path from `which sheaf` (e.g. `/Users/you/go/bin/sheaf`).

Full walkthrough, verification handshake, and troubleshooting: **[../clients.md](../clients.md)**.
