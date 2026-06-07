# sheaf-onboard

A platform-agnostic skill that onboards a repository to Sheaf and produces a
**first report a team can trust** — automating the config, then
adversarially verifying every top-line number against disk *before* the
report is shown.

## Why

The first Sheaf report a team sees decides whether they trust the platform.
A single wrong number caught on a first spot-check and they discard the
tool. Two failure modes make that happen, and this skill is built to defeat
both:

- **Config friction.** Newcomers won't hand-tune globs for an uncertain
  payoff. So the skill drives `sheaf scan --auto` (auto-detect + synthesize
  a config) and only *tunes* the result.
- **Numbers that lie.** A Sheaf number is a join artifact — discovery →
  attribution → a fraction — and each stage has a characteristic way of
  lying (the denominator effect, a doc format the adapter can't read showing
  a confident 0%, file-level test smearing, a missing source map zeroing the
  docs surfaces). The skill runs `sheaf verify` and adjudicates every
  flagged number against the source tree before anything is shown.

## How it's packaged (platform-agnostic)

The brain is one tool-neutral procedure; the rest are thin wrappers and a
compiled engine, so Claude, Gemini, and OpenAI agents all run the same
steps with the same guardrails:

| File | Role |
|---|---|
| `PROCEDURE.md` | **Canonical** step-by-step procedure (the single source of truth) |
| `SKILL.md` | Claude Code skill manifest + Claude-specific execution notes |
| `AGENTS.md` | Entry doc for Gemini CLI / OpenAI Codex / any agentic CLI |
| `README.md` | This file |
| `install.sh` | Install the skill for Claude Code |

The deterministic checks live in the **`sheaf verify`** subcommand (Go), so
every platform gets identical number-reconciliation, per-tier denominator
decomposition, and ≤15%/0%/smearing detection. The agent's judgment is only
ever applied to *adjudicating* what the binary flags — never to the
arithmetic.

## Requirements

- A built `sheaf` binary (`go build -o sheaf ./cmd/sheaf`) with the `verify`
  subcommand, and `scanner` (`go build -o scanner ./cmd/scanner`).
- An agent host that can run shell commands and read/write files.

## Install (Claude Code)

```sh
./install.sh
```

Copies `SKILL.md`, `PROCEDURE.md`, and `README.md` into
`~/.claude/skills/sheaf-onboard/`. Then invoke in any session:

```
/sheaf-onboard   (or: "onboard this repo to sheaf")
```

To remove: `./install.sh --uninstall`.

## Use (Gemini CLI / OpenAI Codex / other)

Point the agent at `AGENTS.md` (or paste `PROCEDURE.md` as the task), give
it the repo path, and let it run. The steps and guardrails are identical.

## Relationship to other Sheaf assets

- Builds on `sheaf scan --auto` (auto-detect + synthesize config + emit
  `sheaf-hardening.md`) and `sheaf verify` (the adversarial engine).
- Hands off to `sheaf-build-adapter` when an adapter is genuinely missing.
- Subsumes the standalone `sheaf-validate-scan` flow for the onboarding
  case; that skill remains for deep, standalone attribution audits.
- The human-facing route guide is `docs/scan-your-repo.md`; this skill is
  the automated agent path through it.
