# Sheaf adapter-authoring procedure

**This is the platform-agnostic brain of the `sheaf-build-adapter` skill.**
Claude `SKILL.md` and `AGENTS.md` are thin wrappers that point here.

You are usually invoked as a **handoff from `sheaf-onboard`**: config alone
couldn't reach acceptable coverage because an adapter is missing or can't
parse the repo's format. Your job is to write **one** new adapter that
closes **one** specific, evidenced coverage gap — built against synthetic
fixtures first, wired in, pointed at the real corpus, and **validated with
`sheaf verify`** so its attributions meet the same adversarial bar as the
rest of the report.

This is the only place in the system that writes adapter code. `sheaf-onboard`
never does — it offers the gap and hands off to you.

---

## The handoff spec you receive

`sheaf-onboard` (Phase 7) hands you — or you assemble — this spec. Do not
write code until you have all of it:

- **The gap**: which surface/tier reads wrong, and the **disk evidence**
  that real coverage exists. Example: "`docs.reference` reads 0% on 148
  methods, but every method is documented in `*.rst` with Sphinx
  `:cpp:func:` roles the `markdown` adapter can't parse."
- **Three 5-line snippets**: the contract source; one test that references a
  contract element; one doc page.
- **The expected element count** from the authoritative parser (`protoc`,
  `fidlc --json`, the `--help` tree).
- **The adapter kind needed**: contract-anchor / test-parser / doc-parser /
  rendered-reference.
- **The nearest existing adapter** to copy.

The gap evidence is the most important field — **never build an adapter on
a hunch**. If a `sheaf verify --disk` run didn't already prove the evidence
exists on disk, prove it first.

---

## Non-negotiables

1. **Synthetic fixtures FIRST.** Write 3–5 tiny fixtures capturing exactly
   the idiom, and get the adapter green on them *before* touching the real
   corpus. Adapter development against a noisy real codebase wastes a day
   per bug.
2. **Correctness over coverage.** Drop any zero-match glob; never emit an
   unverified path; if you can't confidently parse something, skip it —
   don't guess. An adapter that over-attributes is worse than the gap it
   closed, because it poisons trust in every number.
3. **Validate with `sheaf verify`.** The adapter is not done when it
   compiles. It is done when a re-scan + `sheaf verify` shows the gap closed
   *and* introduces no new smearing, false positives, or contamination.
4. **One adapter, one gap.** Resist generalizing mid-build.

---

## Phase 1 — Understand the interface

Read [`internal/adapters/adapters.go`](../../../internal/adapters/adapters.go)
for the interface your adapter implements (`ContractAnchorParser` /
`TestParser` / `DocParser` / `RenderedReferenceParser`). Read the **nearest
existing adapter end to end** as your template:

- declarative contract surface → [`internal/adapters/proto/`](../../../internal/adapters/proto/) (the simplest);
- doc-format adapter → `markdowncli` (~400 LOC);
- test parser → `gotest` / `gtest`.

## Phase 2 — Synthetic fixtures + a failing test

Turn the spec's snippets into minimal fixtures under the adapter's
`testdata/`, capturing exactly the idiom and nothing else. Write a unit test
asserting the adapter emits the expected elements/refs from those fixtures.
It fails — there's no adapter yet. That red test is your spec.

## Phase 3 — Implement against the fixtures

Write `internal/adapters/<name>/`. Parse the fixtures; iterate until the
unit test is green. Keep the parser scoped to the idiom; do not generalize
to formats you have no fixture for.

## Phase 4 — Wire it in

- Add the per-adapter config message to [`proto/config.proto`](../../../proto/config.proto)
  (the relevant `oneof`), and regenerate the Go protos.
- Add the `case` + import in [`internal/orchestrator/orchestrator.go`](../../../internal/orchestrator/orchestrator.go).
- Write down the config block a user adds to `sheaf.textproto`.

## Phase 5 — Point at the real corpus

Add the adapter block to the repo's `sheaf.textproto`. Run
`sheaf doctor` — fix any "matched 0 files". Run `sheaf snapshot`.

## Phase 6 — Validate with `sheaf verify` (close the loop)

Re-run the engine on the new snapshot:

```sh
sheaf verify --from-snapshot <new-snapshot> --repo <repo> --ecosystem <eco> --disk --ledger ledger.md
```

The adapter is done only when:

- the previously-wrong surface now reflects the disk reality (the 0%/low is
  gone, or honestly explained as a real residual gap);
- the element count matches the spec's expected count;
- **no new `test_smearing`, `false_positive`, or `glob_contamination`**
  findings trace to the new adapter;
- the `--disk` false-negative search no longer flags the idiom the adapter
  now handles.

If `verify` isn't satisfied, the adapter isn't done — iterate.

## Phase 7 — Report

Hand back: the adapter, the config block to add, the before/after coverage
on the target surface, and the `sheaf verify` ledger showing the gap closed.
Name any cases you deliberately skipped (correctness over coverage) so they
become provisos, not surprises.

---

## Exit criteria

- Unit test green on synthetic fixtures.
- `sheaf doctor` green; element count matches the authoritative parser.
- `sheaf verify`: the target surface now correct; no new smearing / FP /
  contamination introduced; verdict `trustworthy`, or `review` with every
  residual documented as a proviso.

## References

- [`docs/playbooks/onboard-a-new-repo.md`](../../playbooks/onboard-a-new-repo.md) —
  Stage 7 walks the three contract-adapter implementation patterns.
- [`docs/scan-your-repo.md`](../../scan-your-repo.md) — "When it doesn't fit".
- The adapter interfaces in `internal/adapters/adapters.go`.
