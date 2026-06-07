---
name: sheaf-build-adapter
description: |
  Author ONE new Sheaf adapter to close a specific, evidenced coverage gap
  — built against synthetic fixtures first, wired in, and validated with
  `sheaf verify` so its attributions meet the same adversarial bar as the
  rest of the report. This is the only place in the system that writes
  adapter code. Usually invoked as a handoff from the sheaf-onboard skill
  when config alone can't reach coverage because an adapter is missing or
  can't parse the repo's doc/test format (Sphinx rST, HTML tables, a new
  CLI framework, GraphQL/capnproto, …).
  MANDATORY TRIGGERS: /sheaf-build-adapter, build a sheaf adapter, write a
  sheaf adapter, sheaf can't parse my docs, sheaf can't parse my tests,
  add an adapter to sheaf, close the sheaf adapter gap, new contract anchor
  for sheaf, new doc parser for sheaf
---

# sheaf-build-adapter

Write **one** new adapter that closes **one** evidenced coverage gap, and
prove it with `sheaf verify`. `sheaf-onboard` offers the gap and hands off
here; it never writes adapter code itself.

**The canonical, step-by-step procedure is [`PROCEDURE.md`](PROCEDURE.md) in
this directory. Follow it verbatim.** It is tool-neutral so the same steps
run under Claude, Gemini CLI, or OpenAI Codex. This file adds the
Claude-specific notes.

## The four non-negotiables (full detail in PROCEDURE.md)

1. **Synthetic fixtures first** — get the adapter green on tiny fixtures
   before touching the real corpus.
2. **Correctness over coverage** — drop zero-match globs, never emit an
   unverified path, skip what you can't confidently parse.
3. **Validate with `sheaf verify`** — done means the re-scan's verify shows
   the gap closed with no new smearing / FP / contamination.
4. **One adapter, one gap.**

## Claude-specific execution notes

- **Demand the handoff spec.** Before writing code, make sure you have the
  gap's disk evidence, the three 5-line snippets, the expected element
  count, the adapter kind, and the nearest template adapter (PROCEDURE.md).
  If `sheaf-onboard` handed off, that spec is your input; if a user invoked
  you directly, assemble it first (and prove the evidence with
  `sheaf verify --disk` if it isn't already proven).
- **Read the template adapter end to end** with the Read tool before
  writing — `internal/adapters/proto/` (declarative contract) or
  `markdowncli` (doc format) are the canonical references.
- **Red test, then green.** Write fixtures + a failing unit test first; that
  test is the spec. Iterate the implementation until it passes.
- **Close the loop with the same engine.** After wiring + re-scan, run
  `sheaf verify ... --disk` and do not stop until the target surface is
  correct and no new adversarial findings trace to your adapter.

## When NOT to invoke

- The user wants to **onboard a repo / get a first report** — that's
  `sheaf-onboard`; come here only when it hits a real adapter gap.
- The user wants to **validate an existing scan** — that's `sheaf verify` /
  `sheaf-validate-scan`.
- The gap is reachable by **config alone** (a glob fix, an existing adapter
  pointed at the right place) — fix the config; don't write an adapter.
