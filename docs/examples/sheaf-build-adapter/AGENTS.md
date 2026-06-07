# sheaf-build-adapter — agent entrypoint (Gemini CLI / OpenAI Codex / any agentic CLI)

You are authoring **one** new Sheaf adapter to close **one** evidenced
coverage gap. Execute the procedure in [`PROCEDURE.md`](PROCEDURE.md), step
by step. It is tool-neutral and assumes no Claude-specific tool.

## What you need

- A shell (to build sheaf, run `git grep`/`protoc`, run the unit test).
- File read/write (to read the template adapter, write the new adapter +
  fixtures + proto + orchestrator wiring, edit the config).
- This Sheaf checkout, and the **handoff spec** (from `sheaf-onboard`, or
  assembled yourself): the gap's disk evidence, three 5-line snippets
  (contract / test / doc), the expected element count, the adapter kind,
  and the nearest template adapter.

## The loop that matters

This skill is defined by closing a loop with the verifier:

1. fixtures + failing test → implement → green on fixtures;
2. wire in (proto message, orchestrator case) → point at the real corpus;
3. **re-scan and run `sheaf verify --disk`** — the adapter is done only when
   the previously-wrong surface now matches disk reality, the element count
   matches the authoritative parser, and **no new** smearing / false
   positive / contamination findings trace to your adapter.

If `verify` isn't satisfied, the adapter isn't done.

## The four non-negotiables

1. Synthetic fixtures first — never develop against the noisy real corpus.
2. Correctness over coverage — drop zero-match globs; skip what you can't
   confidently parse; never emit an unverified path.
3. Validate with `sheaf verify`.
4. One adapter, one gap.

## Output

The new adapter, the `sheaf.textproto` config block to add, the before/after
coverage on the target surface, and the `sheaf verify` ledger showing the
gap closed — plus any deliberately-skipped cases named as provisos.
