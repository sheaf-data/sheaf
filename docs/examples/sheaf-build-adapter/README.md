# sheaf-build-adapter

A platform-agnostic skill that authors **one** new Sheaf adapter to close
**one** evidenced coverage gap, and proves it with `sheaf verify`.

## Why it's a separate skill

`sheaf-onboard` is config-only by promise: it gets a repo to a trustworthy
first report without writing code, and when config alone can't reach a
surface because an adapter is missing or can't parse the repo's format, it
**offers** to close the gap and hands off here. Keeping adapter-authoring in
its own skill keeps that promise clean and auditable — and means the
risky, code-writing work is an explicit, separate decision.

## The loop

What distinguishes this skill is that it closes a loop with the verifier:
synthetic fixtures → implement → wire in → re-scan → **`sheaf verify
--disk`**. The adapter is "done" only when the engine that flagged the gap
now confirms it's closed, with no new smearing / false positives /
contamination introduced. The tool that found the problem certifies the fix.

## How it's packaged (platform-agnostic)

| File | Role |
|---|---|
| `PROCEDURE.md` | **Canonical** step-by-step procedure (single source of truth) |
| `SKILL.md` | Claude Code skill manifest + Claude-specific notes |
| `AGENTS.md` | Entry doc for Gemini CLI / OpenAI Codex / any agentic CLI |
| `README.md` | This file |
| `install.sh` | Install the skill for Claude Code |

## Requirements

- A built `sheaf` binary with the `verify` subcommand (`go build -o sheaf
  ./cmd/sheaf`), and `protoc` if the new adapter needs a config message.
- An agent host that can run shell commands and read/write files.

## Install (Claude Code)

```sh
./install.sh
```

Copies `SKILL.md`, `PROCEDURE.md`, and `README.md` into
`~/.claude/skills/sheaf-build-adapter/`. To remove: `./install.sh --uninstall`.

## Relationship to other Sheaf assets

- **Invoked by** `sheaf-onboard` (Phase 7) with a structured adapter spec.
- **Validated by** `sheaf verify` — the same engine `sheaf-onboard` uses.
- **Builds on** `docs/playbooks/onboard-a-new-repo.md` (Stage 7 adapter patterns)
  and the adapter interfaces in `internal/adapters/adapters.go`.
