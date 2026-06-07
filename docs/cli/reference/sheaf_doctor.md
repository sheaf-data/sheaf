# sheaf doctor

Validate `sheaf.textproto`, walk every configured adapter, and probe the LLM embedder. Use this before the first `sheaf scan` on a new project, or when a scan starts producing surprising output.

## Synopsis

```text
sheaf doctor [--config <path>] [--repo <path>]
```

## Description

`sheaf doctor` runs a series of cheap health checks and prints an `[OK]` / `[FAIL]` / `[MISSING]` marker for each:

1. **Config** — `sheaf.textproto` parses cleanly.
2. **Source map** — `categorization-rules.textproto` (the project's source map) parses cleanly (marker is `[MISSING]` if the file is absent; that is tolerated by the rest of the pipeline).
3. **Adapters** — each configured contract anchor, test parser, doc parser, rendered reference, and implements map resolves without error.
4. **LLM** — when `llm.embeddings` is set to anything other than `noop`, the configured embedder is probed via a 3-second context. Failure does not abort the command; it just prints `[FAIL]` with the underlying error.

`doctor` exits non-zero only when the config or source-map files fail to load. A failing adapter or embedder still produces a `0` exit so this stays runnable in CI without flapping on transient LLM outages.

## Options

| Flag | Default | Notes |
|---|---|---|
| `--config` | `<repo>/sheaf.textproto` | Path to the project config. |
| `--repo`   | `.`                      | Project repo root. |

## Example

```sh
sheaf doctor --repo /Volumes/T7/sheaf-workspace/checkouts/envoy
```

Sample output:

```text
Config:                /Volumes/T7/.../sheaf.textproto [OK]
Source map:            /Volumes/T7/.../categorization-rules.textproto [OK] (3 categories, 0 ownership entries)

Adapters:
  proto                [OK]
  gotest               [OK]
  markdown             [OK]

Project: envoy
```

## Exit codes

| Code | Meaning |
|---|---|
| `0` | All `[OK]` or `[MISSING]` only. |
| `2` | Bad flag. |
| `3` | Config or source map failed to load. |

## See also

- [`sheaf scan`](sheaf_scan.md) — the run that doctor pre-flights.
- [`sheaf init`](sheaf_init.md) — generate a `sheaf.textproto` to start from.
