---
name: sheaf-onboard
description: |
  Onboard a repository to Sheaf end to end: deeply understand the repo,
  AUTO-generate a working config (never hand-write it), run the scan, and
  adversarially verify every top-line number against disk BEFORE the report
  is shown — so the human's final spot-check is a confirmation, not a
  discovery. Ships provisos with every number; offers (never silently
  builds) an adapter only when config alone can't reach full coverage.
  Use when a team wants their first Sheaf report, to scan a new repo, to
  wire up sheaf.textproto, or to make a scan trustworthy before sharing.
  MANDATORY TRIGGERS: /sheaf-onboard, onboard repo to sheaf, onboard this
  repo, scan my repo with sheaf, set up sheaf for this repo, first sheaf
  report, wire up sheaf.textproto, sheaf onboarding, get a sheaf report I
  can trust, validate my sheaf scan before sharing
---

# sheaf-onboard

This skill takes a team from a fresh repository to **a first Sheaf report
they can trust**. It is most teams' first contact with Sheaf, so the bar is
not "produce a report" — it is "produce a report whose every shown number
you have already stood behind, with every unknown named."

**The canonical, step-by-step procedure is [`PROCEDURE.md`](PROCEDURE.md)
in this directory. Follow it verbatim.** It is written tool-neutrally so the
same steps run under Claude, Gemini CLI, or OpenAI Codex. This file adds
only the Claude-specific execution notes.

## The five non-negotiables (full detail in PROCEDURE.md)

1. **Automate the config, then challenge it** — drive `sheaf scan --auto`,
   then *tune* what it produced. Never hand-write a config from scratch — but
   treat `--auto`'s output as a **draft to challenge, not a floor to
   defend**. It reliably under-specifies the test-adapter variant (it picks
   stock `gtest`, not `protocpp`), `idl_prefix`/`noisy_words`, and — the
   under-spec that silently inflates every denominator — **public-only scope**:
   it anchors headers on `**/*.h`, but a public-API repo must be scoped to its
   public tree (`**/public/**/*.h`) and to its public modules only. Reconcile
   all of these before trusting any number; **public-only scope is mandatory,
   not optional** (PROCEDURE.md Phase 2).
2. **Validate before you show** — run `sheaf verify` and adjudicate every
   flagged number against disk before presenting anything.
3. **Ship the provisos** — every number carries its caveat; every unknown
   is stated, never hidden. A confident wrong number is the worst output.
4. **Never build an adapter** — config only. Offer a handoff to
   `sheaf-build-adapter` only when an adapter is *genuinely* missing — i.e.
   after the Phase-4 adapter-fit triage has ruled out a mis-wired existing
   adapter (stock `gtest` vs `protocpp`, a missing `idl_prefix`), which is
   the usual culprit behind a "missing adapter" 0%.
5. **Don't chase fine-tuning** — a trustworthy first report is the goal.

## Claude-specific execution notes

- **Inputs.** Ask the user only for what you can't determine: the repo path
  (required), and the binary/library identifier if it's ambiguous. Never
  substitute a placeholder path. Use a **full clone with history** if the Lag
  (doc-staleness) surface matters — a shallow `--depth 1` clone makes lag read
  a false "0 days, all fresh"; `git fetch --unshallow` first (sheaf now
  suppresses Lag with a caveat on a shallow checkout rather than faking zero).
- **LLM-backend pre-flight (cost/perf) — decide before running `--auto`.**
  `--auto`'s LLM tier runs on the frontier API (if `ANTHROPIC_API_KEY` is set)
  or local ollama otherwise. It is **additive, not required** — the
  deterministic adapters give a trustworthy first report on their own. ollama
  on CPU-only is often impractical (tens of minutes per module, may never
  finish — check `ollama ps` for a "100% CPU" `PROCESSOR`); the frontier costs
  $ + source egress. When the cost is material (large repo on frontier $, or
  CPU-only ollama), **tell the user the backend and expected cost and let them
  choose**: run it / bound it (`-attr-max-tests`, `-attr-max-docs`,
  `-scope-library`) / skip it (deterministic-only: drop `llmextract` +
  `attribution{enabled:false}`) / reconfigure (`-llm-backend`, `-model`, or
  set `ANTHROPIC_API_KEY`). Default to deterministic-first; add the LLM tier
  later, scoped. Never silently grind for an hour on the user's machine.
- **Phase 4 adjudication is your judgment, not the binary's.** `sheaf
  verify` flags *which* numbers to distrust (0% surfaces, ≤15% tiers,
  smearing, reconcile mismatches) and reconciles the arithmetic. **You** open
  the actual test/doc source with the Read tool and decide whether each
  flagged claim is real, a format gap, or a wrong number. Read `verify.json`
  and work every finding.
- **Grep before you trust any 0% / ≤15% surface.** Before spending a verdict
  on a low surface, grep the repo for the actual element/test/doc idiom (the
  class name, the test macro, the doc role) and confirm the adapter's matcher
  can see it. This one move catches every under-configured-adapter case below.
- **A disk-confirmed 0% is not automatically an adapter gap — run the
  adapter-fit triage (PROCEDURE.md Phase 4).** When evidence exists on disk
  but the surface reads 0%, walk it in order before concluding anything: (1)
  wrong files → fix globs; (2) **wrong/under-configured adapter** → is there a
  more specific sibling (`protocpp` ⊃ `gtest`, `markdowncli` ⊃ `markdown`),
  is `idl_prefix`/`noisy_words` set for the qualified-name bridge, and on a
  **C++ / header surface** are the two knobs `--auto` never wires set —
  **`cpp_header.ignored_attribute_macros`** (a leading `class PW_LOCKABLE Foo`
  attribute silently drops the primary type, so the module isn't even in the
  surface; pw_status went 15 → 95 elements) and **`protocpp.extra_test_macros`**
  (custom `(suite,name,…)` test macros like `PW_CONSTEXPR_TEST` are invisible —
  but never add helper/assertion macros, which fabricate phantom tests)?; (3)
  only then, unsupported format → Phase 7. A `tests 0%` on a C++/proto
  surface is almost always rung (2), a one-line config fix — not a missing
  adapter. Also **diff your config against the nearest `docs/examples/` config
  for the ecosystem**; `--auto` under-specifies, and the diff surfaces the
  gap in seconds. And **cross-check the element count** (`--expected-elements`
  or a magnitude estimate) — a count several times the public API means the
  globs walk `internal/`/`impl/` and every percentage is against a wrong
  denominator; an *honest* count with a low % is a **granularity floor** (state
  it as a proviso, don't re-glob), not inflation.
- **Search for docs comprehensively — assume the first sources aren't the
  whole story.** Inventory every doc form (rst/markdown prose, in-header doc
  comments like Doxygen `///`/Javadoc, generated reference bundles like Doxygen
  XML/tagfile/OpenAPI, published sites, doc-only repos) and confirm the wired
  adapter captures the *authoritative* docs. If the real docs live in a surface
  no wired adapter renders (Doxygen `///` comments are the usual C++ case), the
  docs number is a **wiring gap, not a coverage gap** — a FLOOR, never
  "undocumented"; the fix is the matching doc adapter (the `doxygen` adapter for
  Doxygen-documented C/C++), not a re-glob. The docs number is trustworthy only
  after you've confirmed no authoritative doc source is silently unparsed.
- **The Usage (examples) surface — never ship a bare "not measured" chip; triage
  it both ways (PROCEDURE Phase 4).** Grep the module for `code-block::` / fenced
  usage first. If there are genuinely none, *eliminate* the surface via
  `scope.surfaces_required` (omit `examples`) rather than showing a 0% that reads
  as missing coverage. If usage examples exist, opt them in with
  `code_block_languages: "cpp"` + `idl_prefix` — the C++ code-block extractor
  (`cppusage`) credits ALL-CAPS macro calls (`PW_TRY`) and prefix-qualified names
  (`pw::OkStatus`) but **not** bare method calls (`status.ok()`), so a
  method-heavy `cppheader` library's Usage reads as a partial FLOOR (state it as
  a proviso), not "no usage." A `0%` after wiring means no macro/qualified usage
  in the docs — recheck, and eliminate the surface if there's truly none.
- **`trustworthy` ≠ covered.** `sheaf verify` can return `trustworthy` on a
  library that is 0% on every surface (an all-zero single-tier library emits no
  metrics, so nothing reconciles). Cross-check any all-zero / no-metrics
  library by hand and call the all-0% module out — never present it as clean.
- **Sampling at scale — fan out.** For the true/false-positive and
  false-negative sampling, if there are more than ~80 claims to check, do
  not verify them all in this session: fan out via the Agent tool in chunks
  of ~30. Each subagent prompt must be self-contained (paste the rows
  inline; give the absolute repo path so Read works) and return ONLY a JSON
  object of `{row → {verdict, reason}}` to merge back. This mirrors the
  `sheaf-validate-scan` skill's loop — reuse it rather than reinventing it.
- **Adapter-gap handoff (Phase 7).** When config tops out because an adapter
  is missing — and only after the adapter-fit triage above has cleared a
  mis-wired existing adapter — assemble the spec from PROCEDURE.md Phase 7 and
  invoke the `sheaf-build-adapter` skill with it. State in the spec which
  adapter + hints you already tried. Do not write adapter code here.
- **For a monorepo, the default deliverable is ONE rolled-up multi-library
  report — not N scattered per-module files.** Fan out with
  `sheaf scan --manifest <MonorepoManifest>` (writes N reports + a suite
  `index.html`) and add **`-single-file`** to emit one portable `index.html`
  that embeds every report as a hash-routed iframe; recommend that single
  artifact for the handover. The concept-docs rollup is
  `sheaf report --lens concept-docs --from-grounding g1.json --from-grounding g2.json … --library-label "<set>"`
  (multiple groundings → one region=library report). Produce the rollup by
  default; emit separate per-module files only when the user asks. Note
  `--concept-docs-href` is a **silent no-op** on the grounding-based path (no
  concept-doc source) — route coverage↔concept navigation through the suite
  `index.html` instead.
- **Keep a running notes file** (`onboard-notes.md`) of every config change
  and adjudication — it becomes the provisos you present in Phase 6.

## When NOT to invoke

- The user only wants to **validate an existing scan's attributions** (deep
  TP/FP/FN audit) — that's `sheaf-validate-scan`, or `sheaf verify
  --from-snapshot` directly.
- A **single-element question** ("is `Foo.Bar` tested?") — `sheaf coverage
  --element <id>` is the right tool.
- The user wants to **build an adapter** — that's `sheaf-build-adapter`.

## Failure modes worth flagging back to the user

- `sheaf doctor` won't go green (bad globs, unreachable adapter inputs) —
  report which adapter matched 0 files; don't proceed.
- `sheaf scan --auto` hangs or crawls (minutes with no progress) — almost
  always the LLM tier on CPU-only ollama (or an unreachable/misconfigured
  backend), not a real stall. Check `ollama ps`; switch to deterministic-first
  and/or have the user reconfigure the backend before re-running.
- `sheaf verify` returns a `broken` verdict (a number that doesn't reproduce
  from its inputs) — do not present the report until it's resolved.
- `sheaf verify` returns `trustworthy` on a module that is **0% on every
  surface** — that is "the zeros reproduce," not "covered" (an all-zero
  single-tier library has no metrics to reconcile, and it escapes the
  zero-surface flag). Cross-check it and call the all-0% module out; never
  ship it as clean.
- A surface reads exactly 0% and disk shows the evidence exists — that's a
  wrong-files fix, a **wrong/under-configured-adapter fix** (the common case:
  stock `gtest` vs `protocpp`, missing `idl_prefix`), or — only if neither —
  a true adapter gap (Phase 7). It's the highest-risk thing to get right
  before a reviewer sees it, and the easiest to misdiagnose as "missing
  adapter" when it's really a one-line config change.
