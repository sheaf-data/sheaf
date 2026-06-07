# sheaf-onboard — agent entrypoint (Gemini CLI / OpenAI Codex / any agentic CLI)

You are onboarding a repository to **Sheaf**. Execute the procedure in
[`PROCEDURE.md`](PROCEDURE.md) in this directory, step by step. It is
written tool-neutrally; it does not assume any Claude-specific tool.

## What you need

- A shell (to run `sheaf` / `go build` / `grep` / `protoc` …).
- File read/write (to read source for adjudication, edit the config, and
  write the notes + ledger).
- This Sheaf checkout, so you can build the binaries:
  `go build -o ./sheaf ./cmd/sheaf && go build -o ./scanner ./cmd/scanner`.
- The **target repo** as a **full clone with history** if the Lag
  (doc-staleness) surface matters: lag reads `git blame` committer-times, so a
  shallow `--depth 1` clone makes it a false "0 days, all fresh." Run
  `git fetch --unshallow` first. (sheaf detects a shallow checkout and
  suppresses Lag with a caveat rather than faking zero.)

## The division of labor (important)

The **deterministic** verification lives in the `sheaf verify` binary — it
reconciles every top-line number to its numerator/denominator, decomposes
blended percentages per tier (defeating the denominator effect), and flags
every surface at or below 15% (0% at highest priority). It runs identically
on every platform, so your judgment is never the thing reconciling
arithmetic.

**Your** job is the part a binary can't do:

1. Understand the repo well enough to check `--auto`'s detector (Phase 1).
2. Reconcile and minimally tune the auto-generated config (Phase 2).
3. **Adjudicate every flagged number against disk** (Phase 4): open the
   actual test/doc source and decide whether a 0%/low surface is a real gap,
   a format the adapter can't parse, a **wrong/under-configured adapter**, or
   a wrong number; whether an attributed test really exercises its element;
   whether a "missing" edge is a real adapter blind spot. **Grep before you
   trust any 0% / ≤15% surface** — confirm the matcher can even see the
   element/test/doc idiom. For a disk-confirmed 0%, run the adapter-fit triage
   **in order**: wrong files → wrong/under-configured adapter (more-specific
   sibling like `protocpp` ⊃ `gtest`? `idl_prefix`/`noisy_words` set? on a
   **C++ / header surface**, the two knobs `--auto` never wires:
   **`cpp_header.ignored_attribute_macros`** — a leading `class PW_LOCKABLE Foo`
   attribute silently drops the primary type, so it reads 0% because it isn't
   in the surface at all; and **`protocpp.extra_test_macros`** for custom
   `(suite,name,…)` test macros like `PW_CONSTEXPR_TEST`, but never helper/
   assertion macros, which fabricate phantom tests) → only then an unsupported
   format (Phase 7). A `tests 0%` on a qualified-name ecosystem is usually a
   config fix, not a missing adapter. A low % on an *honest* count is a
   **granularity floor** (state it as a proviso), not a denominator bug.
4. **Search for docs comprehensively, and don't trust a `trustworthy`
   verdict blindly.** Assume the doc sources you first find are not the whole
   story: inventory every form (rst/markdown prose, in-header doc comments like
   Doxygen `///`/Javadoc, generated reference bundles like Doxygen
   XML/tagfile/OpenAPI, published sites) and confirm the wired adapter captures
   the *authoritative* docs — if the real docs live in a surface no adapter
   renders (Doxygen `///` is the usual C++ case), the docs number is a wiring
   gap and a FLOOR, never "undocumented" (fix: the `doxygen` doc adapter, not a
   re-glob). And `sheaf verify` can return **`trustworthy` on a library that is
   0% on every surface** (no metrics to reconcile) — "trustworthy" there means
   "the zeros reproduce," not "covered"; cross-check any all-zero module and
   call it out, never present it as clean.
5. **Triage the Usage (examples) surface both ways — never present a bare
   "not measured" Usage chip.** Grep the module for `code-block::` / fenced
   usage. If there is genuinely none, *eliminate* the surface
   (`scope.surfaces_required`, omitting `examples`) so absence-of-a-surface is
   not shown as missing coverage. If usage examples exist, opt them in
   (`code_block_languages: "cpp"` + `idl_prefix`) — the C++ code-block extractor
   (`cppusage`) credits ALL-CAPS macro calls (`PW_TRY`) and prefix-qualified
   names (`pw::OkStatus`) but not bare method calls (`status.ok()`), so a
   method-heavy `cppheader` library's Usage reads as a partial FLOOR (a proviso),
   not "no usage." A `0%` after wiring means no macro/qualified usage in the
   docs — recheck, and eliminate the surface if there is truly none.

## The five non-negotiables

1. Automate the config (`sheaf scan --auto`), then tune — never hand-write.
   Treat `--auto` as a draft to challenge: it under-specifies the test-adapter
   variant (picks stock `gtest`, not `protocpp`), `idl_prefix`/`noisy_words`,
   and **public-only scope** — it anchors headers on `**/*.h`, but a public-API
   repo MUST be scoped to its public tree (`**/public/**/*.h`) and public
   modules only (mandatory; not `**/*.h` plus excludes). Diff it against the
   nearest `docs/examples/` config for the ecosystem.
2. Validate against disk **before** showing the report. Cross-check the
   element count — a count several times the public API means inflated globs.
3. Ship the provisos; name every unknown.
4. Never build an adapter — offer the `sheaf-build-adapter` handoff instead,
   and only after the adapter-fit triage rules out a mis-wired existing one.
5. A trustworthy first report is the goal; don't chase fine-tuning.

## Output

A first report (`sheaf-report.html`), a trust ledger (`ledger.md` from
`sheaf verify`), a machine-readable `verify.json`, and a provisos section
that states every caveat and every unknown. Lead your summary with the
ledger's verdict (`trustworthy` / `review` / `broken`) and the next action.

For a **monorepo / multi-module** target the default deliverable is **one
rolled-up multi-library report, not N scattered files**: fan out with
`sheaf scan --manifest <MonorepoManifest>` (writes N reports + a suite
`index.html`) and add **`-single-file`** for one portable `index.html` that
embeds every report as a hash-routed iframe. The concept-docs rollup is
`sheaf report --lens concept-docs --from-grounding g1.json --from-grounding g2.json … --library-label "<set>"`.
Emit separate per-module files only when the user asks. `--concept-docs-href`
is a silent no-op on the grounding-based path (no concept-doc source) — route
coverage↔concept navigation through the suite `index.html`.

If config alone cannot reach acceptable coverage because an adapter is
missing, stop at PROCEDURE.md Phase 7: assemble the adapter spec and offer
the handoff. Do not write adapter code.
