# Sheaf onboarding procedure

**This is the platform-agnostic brain of the `sheaf-onboard` skill.** Any
agent that can run shell commands and read/write files — Claude Code,
Gemini CLI, OpenAI Codex CLI — executes this procedure verbatim. The
Claude `SKILL.md` and the `AGENTS.md` entrypoint are thin wrappers that
point here; the steps below are the contract.

Your job: take a team from a fresh repository to **a first Sheaf report
they can trust**, with near-zero hand-written config, validating every
top-line number *before* the report is shown. This report is most teams'
first contact with Sheaf. One wrong number a reviewer catches on first
look and they discard the tool — so the bar is not "produce a report," it
is "produce a report whose every shown number you have already stood
behind, with every unknown named honestly."

---

## Non-negotiables (read before you start)

1. **Automate the config; do not hand-write it.** A newcomer will not
   invest hours hand-tuning globs for a tool they don't yet trust. Lean on
   `sheaf scan --auto` (which auto-detects ecosystems and synthesizes a
   `sheaf.textproto`) plus your own reading of the repo. You *tune* what
   `--auto` produced; you never start from a blank config.

2. **Validate as hard as possible before the first report is shown.** A
   human will always do a final spot-check. Your job is to make that check
   a *confirmation*, not a *discovery*. Run `sheaf verify` and adjudicate
   every flagged number against disk before you present anything.

3. **Ship the provisos.** Every number you present carries its caveat:
   what's bounded, what's a known attribution limitation, what you could
   not verify. The honest unknown is stated, never hidden. A confident
   wrong number is the worst possible output — worse than an honest "not
   yet."

4. **Never build an adapter.** This skill is config-only. If config alone
   cannot reach acceptable coverage because a *needed adapter is missing*,
   you STOP, produce a crisp adapter spec, and OFFER to hand off to the
   `sheaf-build-adapter` skill. You do not write adapter code here.

5. **Ultra-fine-tuning is out of scope.** Getting to a trustworthy first
   report is the goal. Squeezing the last coverage points — per-knob
   provisos, custom matchers — is a separate, later phase, teed up by the
   `sheaf-hardening.md` backlog. Do not chase it here.

---

## Inputs you need

Ask the user only for what you genuinely cannot determine yourself:

- `REPO` — absolute path to the repository to onboard (required).
- `BIN` / `LIB` — the CLI invocation name, or the library/package
  identifier for non-CLI surfaces. Infer it; confirm only if ambiguous.

If you cannot determine `REPO`, stop and ask. **Never** substitute a
placeholder path — the scan will silently under-count against a path that
doesn't exist and look like the adapter is broken.

**`REPO` must be a full clone with history for the Lag (doc-staleness)
surface.** That metric reads `git blame` committer-times to measure how far
docs trail the code. A **shallow clone (`git clone --depth 1`)** has only the
boundary commit, so every file resolves to the *same* timestamp — the lag
distribution collapses to a false "0 days behind, all fresh," and (because the
boundary commit is a real sha, not the uncommitted sentinel) it is NOT
disclosed as the unknown rate. It is a silent wrong number, the worst output.
`sheaf` now detects a shallow checkout and suppresses the Lag surface with a
caveat instead of the fake zero — but to get real lag numbers, onboard against
a full checkout, or `git fetch --unshallow` first. (Shallow is otherwise fine
for the coverage surfaces; it only breaks Lag.)

---

## Phase 0 — Build the tools

From the Sheaf repo (or via `go install`):

```sh
go build -o ./sheaf ./cmd/sheaf
go build -o ./scanner ./cmd/scanner
```

Confirm `sheaf version` runs. Everything below uses `./sheaf`.

---

## Phase 1 — Understand the repository (deeply)

Do not skip this. `--auto`'s detector is a heuristic over file counts; your
reading is the check on it.

1. **Map the contract surface.** What does this project expose? Walk the
   tree and classify:
   - Protobuf / gRPC / xDS (`.proto`) → `proto` anchor
   - FIDL (`.fidl`) → `fidl` anchor (+ `implementsmap` for C++ wire servers)
   - CLI: cobra (Go), clap / argh (Rust) → `cobra` / `clap` / `argh`
   - Kubernetes CRDs / manifests / Helm values → `crd` / `k8smanifest` / `helmvalues`
   - C++ public headers → `cppheader`
   - OpenAPI / Swagger → **no anchor**; this needs the snapshot-script route
     (see `docs/scan-your-repo.md`) — flag it as a likely adapter-gap case.
2. **Find the tests.** What framework, what file naming, where do they
   live? (`*_test.go`, `*_test.cc`, `#[test]`, `*.bats`, pytest …)
3. **Find the docs — comprehensively. Assume what you first find is NOT the
   whole story.** The doc format is the single most common cause of a false
   0% docs surface, but the deeper trap is docs that live in a surface no
   wired adapter even looks at. Before any docs number is trusted, inventory
   **every** doc form the project uses:
   - rst/markdown **prose** trees (Sphinx, mkdocs, plain `docs/`);
   - **in-header doc comments** — Doxygen `///` / `/** */`, Javadoc, rustdoc
     `///` — these are usually the *authoritative* API reference and are NOT
     the prose surface the markdown/rst adapter parses (Pigweed: 327 `///`
     lines in pw_string's 8 headers, but the rendered "Reference docs" surface
     measured only ~5%, the rst narrative mentions);
   - **generated / structured reference bundles** — Doxygen XML/tagfile,
     fidldoc, OpenAPI/Swagger — the real docs.reference often lives here, not
     in prose;
   - **published doc sites**, and crucially whether they are built FROM this
     repo or live only externally;
   - **separate `docs/` trees or doc-only repos** the contract repo references.

   Then **confirm the wired doc adapter actually captures the authoritative
   docs.** If the real docs are in a form no configured adapter parses, the
   docs surface is a **wiring gap, not a coverage gap**, and the number is not
   yet trustworthy. State the rule plainly: *the docs number is only
   trustworthy after you have inventoried the doc sources and confirmed none
   are silently unparsed.* For C/C++ documented with Doxygen, the right fix is
   the **`doxygen` doc adapter** (reads the generated Doxygen XML/tagfile → the
   real `docs.reference` surface) — wire it instead of trusting a prose-only
   number; a low rst/markdown docs% on a Doxygen project is a FLOOR, not
   "undocumented." If the authoritative docs are in a structured form and no
   adapter reaches them, that is a Phase-7 doc-adapter case, not a real 0%.
4. **Detect monorepo structure.** Multiple independent libraries? Note it;
   you may scope to one library first.
5. **Note hazards** that have produced wrong numbers before:
   - vendored / generated / build trees (`vendor/`, `node_modules/`,
     `target/`, `bazel-out/`, `.git/`, and **git worktrees under
     `.claude/`**) — these must be excluded or they inflate counts.
   - common single-word element names (`run`, `get`, `list`, `Event`,
     `Value`) — collision-prone in name-token matching.

Write down a one-paragraph model of the repo before touching config.

---

## Phase 2 — Generate the config (automated), then reconcile it

1. **Run auto-detection + synthesis:**

   ```sh
   ./sheaf scan --auto --repo "$REPO" --output-dir "$REPO/sheaf-auto"
   ```

   This emits four artifacts into `sheaf-auto/`: the detected adapters
   (stdout), a synthesized `sheaf.textproto`, a two-tier report under
   `report/`, the canonical `sheaf-report.html`, and **`sheaf-hardening.md`**
   (the ranked "what would improve coverage/precision" backlog — your
   provisos runway). Capture the stdout: it lists every detected adapter,
   its role, and its file count.

   **Know your LLM backend before you lean on `--auto`'s LLM tier (cost &
   performance).** `--auto` wires an LLM tier (`llmextract` + `attribution`);
   its backend is set by `-llm-backend` (default `auto`): the frontier API if
   `ANTHROPIC_API_KEY` is set, otherwise local **ollama**. The deterministic
   adapters are the trustworthy core — **the LLM tier is additive, never
   required** (if the backend is unreachable the orchestrator just warns and
   the LLM rows come back empty). Treat the backend as a pre-flight decision,
   not a mid-scan surprise:
   - **Frontier (`anthropic`, or `auto` + key):** fast, but per-token $ and
     network egress (your source leaves the box). Bound it on large repos with
     `-attr-max-tests` / `-attr-max-docs` / `-scope-library`.
   - **ollama (the keyless default):** local, free, private — but speed is
     dominated by GPU vs CPU. GPU-accelerated is workable; **CPU-only
     inference is often impractical** — a single module can take tens of
     minutes and may never finish a multi-module run. Check `ollama ps`: a
     `PROCESSOR` reading of "100% CPU" means no GPU acceleration.
   - **If the backend is slow, costly, or absent, run deterministic-first:**
     drop the `llmextract` anchor and set `attribution { enabled: false }` (or
     bound with `-attr-max-*` / `-scope-library` / `-include`), produce the
     trustworthy report from the deterministic adapters, and **document the
     LLM-tier omission as a proviso**. Add the LLM tier back later, scoped,
     once the deterministic report stands. (The shipped pigweed example
     configs reach good coverage with **no** LLM tier — proof it's optional.)
   - **Surface the choice to the user when the cost is material** (a large
     repo on the frontier $, or CPU-only ollama): name the backend and the
     expected time/cost and let them decide — run it, bound it, skip it, or
     point the agent at a different backend/model (`-llm-backend` / `-model`,
     or set `ANTHROPIC_API_KEY`). Don't silently grind for an hour on someone
     else's machine.

2. **Reconcile the detection against your Phase-1 model.** Open the
   generated `sheaf-auto/sheaf.textproto` and check, adversarially:
   - Did it pick the **right primary contract surface**? (If your repo is a
     proto API but it anchored on C++ headers, the scope is wrong.)
   - Is **`scope.library`** the library you intend to report on?
   - Do the **include globs** reach the real sources, and do the **exclude
     globs** keep out vendored/generated/worktree trees? Add excludes for
     any hazard from Phase 1 §5.
   - Is there a **source map** (`categorization-rules.textproto`)? Without
     one, every `docs.*` surface silently reads 0. If `--auto` didn't stage
     one, that is a known trap — note it for Phase 4. **The location resolves
     differently per command**, and a misplaced file gives a silent 0% docs:
     `sheaf doctor` and `sheaf snapshot` look for it at the **repo root**
     (`<repo>/categorization-rules.textproto`), while `sheaf verify --config`
     and the `--manifest` fan-out use the **sibling-of-config** copy. If
     doctor reports the source map MISSING, stage it at the repo root (staging
     it in **both** places is the safe move when you run a mix of commands).
   - **Did `--auto` pick the most *specific* adapter, or just a working
     one?** `--auto` defaults to the broadest adapter that matches; sheaf
     ships paired general/specific variants and the specific one is often
     required for real attribution. Known pairs:
     - C++ tests → prefer **`protocpp`** over `gtest` (protocpp is a gtest
       superset that adds direct-reference + qualified-name matching).
     - CLI docs → `markdowncli` over `markdown`; Python tests → check
       `pythontest` vs `pytest`.
     If the contract surface is C++/proto/FIDL and the test adapter is stock
     `gtest`, that is almost always wrong — switch it now, before Phase 4.
   - **Is the qualified-name bridge configured?** Whenever contract IDs are
     namespace/package-qualified (`pw::StringBuilder`, `pkg.Svc.Method`) but
     tests/docs name the bare local token (`TEST(StringBuilder, …)`), the
     matcher needs **`idl_prefix`** set to the prefix and domain-common
     nouns in **`noisy_words`**. This is **not** proto/FIDL-only — C++
     (`cppheader` + `protocpp`) needs it too. Missing `idl_prefix` is the
     single most common cause of a false `tests 0%` on a qualified-name
     ecosystem.
   - **Scope the contract to the *public* API tree — mandatory, the default,
     not a fallback.** `--auto` anchors the cppheader include on `**/*.h`, which
     wires up *every* header (backend/impl, test, example, internal) — so the
     element count comes in several times the real public API and every
     percentage reads low against that inflated denominator. For any repo that
     declares its public API under a convention directory — `public/` for
     Pigweed/Fuchsia-style modules, `include/` for the classic C++ layout — the
     include **must** anchor on it: `**/public/**/*.h` (resp.
     `**/include/**/*.h`). Do **not** settle for `**/*.h` plus
     `exclude internal/`/`impl/`: excluding the obvious plumbing still admits
     test, example, and top-level non-API headers, so it is **not** public-only.
     Fall back to `**/*.h` (minus `internal/`, `impl/`, `*_private`, generated,
     and test trees) **only** when the repo has no public-header convention at
     all — and say so in the notes.
   - **Wire up only the public *modules*, too.** In a monorepo, enumerate the
     libraries from the ones that actually expose a public API tree (a populated
     `<module>/public/`), **not** every top-level directory — internal tooling,
     build, and test-only dirs are not public modules and do not belong in the
     manifest (see the Phase-6 monorepo fan-out, which derives the same list).
   - **Verify the scope took — don't trust the glob blind.** After scoping, the
     element count must land within ~1× of a public-API magnitude survey
     (`grep -rhoE '^(class|struct) [A-Z_]+ [A-Z]' <public headers>`). A count
     that is a *multiple* of that estimate means the include is still walking
     non-public trees — fix the anchor before Phase 4 (the denominator effect,
     see Phase 3).
   - **C++ / header ecosystems: set the two adapter knobs `--auto` won't.**
     The proto/FIDL bridges above (`idl_prefix`, `noisy_words`) have C++
     counterparts that `--auto` never wires, and each one silently corrupts a
     headline number if left unset. Survey and set them now, before Phase 4:
     - **`cpp_header.ignored_attribute_macros` — a LEADING attribute macro
       hides the primary type.** A macro between `class`/`struct` and the type
       name (`class _PW_STATUS_NO_DISCARD Status {…}`, `class PW_LOCKABLE Foo`)
       poisons cppheader's "next token is the class name" heuristic and
       **silently drops the primary type** from the contract surface — the
       module's main type goes missing and the whole module reads 0% tested.
       Survey with `grep -rhoE '^(class|struct) [A-Z_]+ [A-Z]' <public headers>`;
       set `cpp_header.ignored_attribute_macros: "<MACRO>"` for each. In the
       Pigweed run this turned pw_status from 15 elements (macros + helpers
       only) into 95 — the real `Status` class and its ~80 methods. (Trailing
       attributes like `PW_PRINTF_FORMAT` don't block extraction; only leading
       ones do.)
     - **`protocpp.extra_test_macros` — custom test-DECLARING macros are
       invisible.** Projects declare tests with macros beyond
       `TEST`/`TEST_F`/`TEST_P` (Pigweed's `PW_CONSTEXPR_TEST(suite, name,
       body)`, used 110×) — protocpp ignores them by default, so test% reads
       far too low. Survey with
       `grep -rhoE '[A-Z_]+_TEST[A-Z0-9_]*\(' <test files>`, then add the ones
       whose signature is `(suite, name, …)` to `extra_test_macros`.
       **CRITICAL — check each macro's `#define` signature first.** Do NOT add
       intra-test *helper/assertion* macros whose arguments are not suite/name
       (`TEST_STRING`, `TEST_RUNTIME_STRING`, `DATA_DRIVEN_TEST`,
       `ENCODED_SIZE_TEST`, `PW_TEST_EXPECT_*`) — adding them **fabricates
       phantom tests**.
   - **Diff against the nearest shipped example.** If sheaf ships an example
     config for this *ecosystem* (look under `docs/examples/`; it need not
     be the same repo), open it and diff your synthesized config against it.
     `--auto` reliably under-specifies what a tuned config sets — the
     test-adapter variant, `idl_prefix`, `noisy_words`, public-only scope,
     analyzers. Treat `--auto`'s output as a draft to challenge, not a floor
     to defend.

3. **Tune minimally and document each change as a proviso.** Edit the
   generated config in place; for every edit, record *why* in a running
   notes file (`onboard-notes.md`): "excluded `third_party/**` — vendored
   protobuf, not this project's contract." These notes become the provisos
   you present. Do **not** rewrite the config from scratch.

4. **Doctor it:**

   ```sh
   ./sheaf doctor --config "$REPO/sheaf-auto/sheaf.textproto" --repo "$REPO"
   ```

   If any adapter reports **"matched 0 files"**, the globs are wrong — fix
   and re-run until doctor is green. A green doctor is necessary but not
   sufficient; the verify pass in Phase 4 is the real gate.

---

## Phase 3 — Scan and snapshot

```sh
./sheaf snapshot \
  --config "$REPO/sheaf-auto/sheaf.textproto" \
  --repo   "$REPO" \
  --library "$LIB" \
  --out    "$REPO/sheaf-auto/snapshot.json"
```

Read the stderr summary. **Grep it for `warning:` and `no source map`** — a
green exit with `no source map … categorization will be skipped` means
every docs surface is about to read 0. If you see it, stage the rules file
at the scan root and re-snapshot.

Sanity-check the element count against your Phase-1 model — **both
directions**. Too few (a 200-method API showing 12 elements) means the
contract globs miss the sources — or, on a C++ header surface, that a leading
attribute macro silently dropped the primary type (see Phase 2's
`ignored_attribute_macros`). **Too many is just as wrong**: if the count
is multiples of what the public API should be, the globs are walking
`internal/`/`impl/`/generated/test trees, and every coverage percentage will
read low against the inflated denominator (the denominator effect). Either
way, **list what actually entered the snapshot** (the per-adapter file
counts) and confirm the trees are the ones you intend — then go back to
Phase 2 if not.

**Distinguish denominator inflation from an honest granularity floor — they
look identical but have opposite fixes.** A low percentage downstream can mean
the denominator is *inflated* (junk/internal/generated trees pulled in → the
fix is to re-glob) OR it can mean the denominator is *honest* and the low %
is a **granularity floor**: the contract is enumerated per-method/per-symbol
while tests and docs operate at the class/headline level (e.g. ~85% of
elements are methods and free functions exercised through class instances
`s.append(...)` or bare names, which name-token matching can't attribute).
Inflation → re-glob. Floor → **state it as a proviso and do NOT re-glob** —
the number is a real lower bound, not a bug. Tell them apart before you touch
anything: is the element count honest against the *public* surface (not a
multiple of it)? Are the "uncovered" elements real public API, or plumbing?
If the count is honest and the uncovered set is genuine public methods, it's
a floor, not inflation.

---

## Phase 4 — Verify, then adjudicate against disk (the adversarial core)

This is the step that earns the trust. Run the engine:

```sh
./sheaf verify \
  --from-snapshot "$REPO/sheaf-auto/snapshot.json" \
  --repo "$REPO" \
  --ecosystem "<cli|proto|fidl|cpp|...>" \
  --json   "$REPO/sheaf-auto/verify.json" \
  --ledger "$REPO/sheaf-auto/ledger.md"
```

**One-shot.** To collapse Phase 3 and this step into one command, point verify
at the config directly — it scans in-process (auto-locating the source map
next to the config) and verifies, no separate `sheaf snapshot`:

```sh
./sheaf verify \
  --config  "$REPO/sheaf-auto/sheaf.textproto" \
  --repo    "$REPO" \
  --library "$LIB" \
  --ecosystem "<cli|proto|fidl|cpp|...>" \
  --json    "$REPO/sheaf-auto/verify.json" \
  --ledger  "$REPO/sheaf-auto/ledger.md"
```

`sheaf verify` reconciles every headline number to its numerator/
denominator, decomposes blended percentages per tier (so the denominator
effect can't hide), and flags every surface at or below 15% — including, at
highest priority, any surface reading exactly 0%. It is deterministic and
runs identically on every platform. **But it cannot read intent.** The
binary tells you *which* numbers to distrust; **you** confirm or refute each
one against disk. Read `verify.json` and work every finding:

- **`zero_surface` / `low_coverage`** — a surface at 0% or ≤15%. **Pre-flight:
  grep before you trust any 0% or ≤15% surface.** Before you spend a verdict
  on a low surface, `grep` the repo for the actual element/test/doc idiom and
  confirm the adapter's matcher can even see it — the class name, the test
  macro, the doc role. This one move catches every under-configured-adapter
  case below in seconds. Then open the actual doc/test source for a *sample*
  of the "uncovered" elements. If the evidence is genuinely absent, it's an
  honest gap: keep the number, add the proviso. If the evidence **exists**,
  the report is overstating absence — and before you call that an adapter gap,
  walk this triage **in order**; it is a true Phase-7 gap only if all three
  fail:
  1. **Wrong files.** Is the adapter pointed at the tree the evidence lives
     in? Fix the globs (config).
  2. **Wrong or under-configured adapter — check this before concluding a
     gap.** Is `--auto` using the broadest adapter when a more *specific*
     sibling exists for this surface (`protocpp` ⊃ `gtest`; `markdowncli` ⊃
     `markdown`; `pythontest` vs `pytest`)? Does the matcher need its
     qualified-name bridge — **`idl_prefix`** + **`noisy_words`** — to link
     qualified contract IDs to bare test/doc tokens? And on a **C++ / header
     surface**, are the two C++ knobs set: **`cpp_header.ignored_attribute_macros`**
     (a leading `class PW_LOCKABLE Foo` attribute silently dropped the type, so
     it reads 0% because it isn't even in the surface) and
     **`protocpp.extra_test_macros`** (custom `(suite, name, …)` test macros
     like `PW_CONSTEXPR_TEST` are invisible, so test% reads far too low)? A
     `tests 0%` on a C++/proto/FIDL surface using stock `gtest`, no
     `idl_prefix`, or an unset C++ knob is this case, not a missing adapter.
     Fix the config and re-snapshot.
  3. **Unsupported format / wrong surface.** Only if the right adapter is
     wired with its hints and the evidence is in a form **no existing adapter
     parses** is this a real adapter gap → Phase 7. Two shapes recur: the
     adapter parses the *format* but not the *construct* (the `rst` adapter
     reads `:cpp:func:` *roles* but not `.. cpp:function::` *directives*); or
     the authoritative docs live in a **surface the wired adapter doesn't
     render at all** — Doxygen `///` header comments / a generated reference
     bundle rather than the rst/markdown prose the doc adapter parses. The
     latter is the comprehensive-doc-search finding from Phase 1: a low docs%
     there is a FLOOR measuring prose mentions, never "undocumented," and the
     right fix is a doc adapter that reads the real surface (e.g. the
     **`doxygen`** adapter over the Doxygen XML/tagfile), not a re-glob.

- **The examples / Usage surface — triage it *both* ways; never ship a bare
  Usage `0%`/"not measured" chip.** It is the surface most often shown as a
  finding when it is really *inapplicable* or *unservable*. Decide which, on
  disk:
  1. **Do usage examples exist anywhere?** `grep -rln 'code-block::' <module>/`
     (plus fenced ```` ``` ```` blocks, a `guide.rst`/quickstart page, or a
     separate sample-project repo). Pigweed ships them per module in
     `guide.rst`.
  2. **None anywhere → eliminate the surface, don't show 0%.** A "not measured"
     Usage card reads to a reviewer as *missing coverage* when it is really
     *missing-a-surface*. Declare the surfaces that apply via
     **`scope.surfaces_required`** and omit `examples` (e.g.
     `surfaces_required: "docs.reference"`, `surfaces_required: "tests"`) — the
     Usage tile then doesn't render at all. (Same intent as the Implementations
     N/A collapse, applied at config level.)
  3. **Examples exist → populate it, then re-check.** The `rst`/`markdown` doc
     adapters only emit example claims for languages you opt into: set
     **`code_block_languages`** (`code_block_languages: "cpp"`, mirroring the
     shipped `docs/examples/pigweed-*` configs) plus `idl_prefix`, and
     re-snapshot.
  4. **Usage may still read *low* on a C++ (`cppheader`) contract — that is a
     FLOOR, not "no usage."** The code-block extractor (`cppusage`, the plain-C++
     complement to `fidlmatch`) credits the two unambiguous shapes — ALL-CAPS
     macro calls (`PW_TRY(...)`) and prefix-qualified names (`pw::OkStatus`,
     `pw::Status::Update`) — but deliberately **not** bare lowercase method calls
     (`status.ok()`), which collide with common words and would smear credit
     across unrelated elements. So a method-heavy API (a class whose usage is
     mostly `obj.method()`) shows a *partial* Usage number that undercounts the
     real usage. State it as a proviso ("Usage counts macro + qualified-name
     demos; bare method-call examples aren't credited"). A genuine `0%` here
     *after* wiring means the docs show no macro/qualified usage at all — recheck
     step 1, and eliminate the surface (step 2) if there is truly none.

- **`test_smearing`** — an element with many test refs from very few files.
  Open a cited file and count the call sites that *actually* exercise the
  element versus the claimed count. If inflated, the per-element test count
  is a file-level artifact (the classic 10–30× over-count). Note it; it is
  an adapter-precision issue, not something to fix by hand here.

- **`reconcile`** — a shown number that doesn't reproduce from its inputs.
  This is provably broken regardless of disk. Investigate the join/renderer;
  do not present the report until it's resolved.

- **A low (non-zero) surface — inflation or floor?** Carry the Phase-3
  distinction into the adjudication. If the evidence exists but at a coarser
  granularity than the contract (tests/docs at the class/headline level, the
  contract at method/symbol level), the low % is an honest **granularity
  floor** — state it as a proviso next to the number, name the class-level
  rows that *do* attribute well as the trustworthy signal, and do NOT re-glob.
  If instead the denominator is inflated by non-public trees, that's a config
  fix (re-glob), not a proviso.

- **`trustworthy` ≠ covered — cross-check every all-zero or no-metrics
  library.** `sheaf verify` can return `trustworthy` on a library that is 0%
  on **every** surface, because an all-zero single-tier library emits no
  findings and so has no metrics to reconcile — "trustworthy" there means
  *the zeros reproduce*, not *well covered*. A `trustworthy` verdict on an
  all-zero / no-metrics library must be cross-checked by hand: an all-0%
  module must still be called out explicitly, never presented as clean. (This
  is also a verify blind spot — an all-zero single-tier module escapes the
  `zero_surface` flag — so the cross-check is on you.)

Then run the disk oracle — the source-tree checks, now built into the binary
and bounded by sampling so a huge corpus stays tractable. Pass `--disk` (and
`--check-urls` for the network check); your job is to adjudicate what the
binary surfaces:

- **Attribution true/false-positives (now in `sheaf verify`).** The
  `verify.json` above carries a deterministic, bounded **`assertions`** array:
  attributed "X tested by Y" / "X documented by Z" claims, weighted toward
  high-count and common-name (collision-prone) elements, each with
  `verdict: null`. Read each cited test/doc body and fill the verdict
  (`tp` | `fp` | `ambiguous`) with a one-line reason, writing the rows to
  `verdicted.jsonl`. Then let the binary do the precision arithmetic:

  ```sh
  ./sheaf verify summarize \
    --assertions "$REPO/sheaf-auto/verdicted.jsonl" \
    --ledger     "$REPO/sheaf-auto/precision.md"
  ```

  It computes per-library precision and the confirmed-false-positive table.
  The sampling and the math are the binary's; the semantic call — does the
  test really exercise the element, or did it match a shared token — is yours.
- **False negatives (now in `sheaf verify --disk`).** `--disk` greps the
  tracked tree for each reportedly-untested element's distinctive name
  (`Service.Method`, `Service::Method`, a flag literal) and surfaces
  unattributed hits in test files as candidates. Open a few cited lines: a
  real hit is an adapter blind spot worth naming; a coincidental mention
  leaves the untested verdict standing.
- **Ground-truth element count (now in `sheaf verify --disk`) — do this, do
  not skip it.** A wrong denominator silently corrupts *every* percentage in
  the report, so the count gets a mandatory cross-check, not an optional one.
  Where an authoritative parser exists, compute the count — `protoc`
  descriptor for proto, `fidlc --json` for FIDL, the `--help` command tree
  for a CLI — and pass it as `sheaf verify --disk --expected-elements N`.
  verify compares it to the scan's count: a large, unambiguous gap is a
  provable error (every percentage is against the wrong denominator), a small
  gap a warning, an exact match clean. **When no authoritative parser is
  cheap, you still owe a magnitude sanity-check** (Phase 3): estimate the
  expected order of magnitude from the public surface (e.g. public headers ×
  typical decls per header) and confirm the scan's count is in the same
  ballpark — a count several times larger means the globs are walking
  non-public/impl/generated trees. Treat "no `--expected-elements`" as a
  *deferred check you must discharge*, not a free pass. Running the parser /
  estimate stays your job; the compare is the binary's.
- **Doc URLs resolve (now in `sheaf verify --disk --check-urls`).** Add
  `--check-urls` to resolve a bounded sample of published doc URLs (HTTP
  HEAD, falling back to a ranged GET) and flag dead links — an
  underscore/slug convention mismatch produces 404s even when the join is
  correct. When *every* sampled URL fails, verify reports one
  base/publication issue rather than N per-link findings; an unreachable
  network is an honest caveat, never a finding.
- **The docs surface is authoritative — confirm it before trusting the docs
  number.** Close the loop on the Phase-1 comprehensive doc inventory: confirm
  the wired doc adapter actually captures the *authoritative* docs, not a
  prose proxy for them. `grep` the real doc idiom for a sample of "undocumented"
  elements across **every** form you inventoried — header doc comments
  (`grep -rn '///' <public headers>` for Doxygen density), generated reference
  bundles, the published site. If the authoritative docs live in a form no
  wired adapter parses (Doxygen `///` comments, a Doxygen XML/tagfile, fidldoc,
  OpenAPI), the docs number is a **wiring gap, not a coverage gap** — it is a
  FLOOR, never "undocumented," and the fix is the matching doc adapter (the
  **`doxygen`** adapter for Doxygen-documented C/C++) or a Phase-7 offer. Do
  not trust a docs% until you have confirmed no authoritative doc source is
  silently unparsed.

Record a verdict for every finding and every sample. This adjudication is
the difference between "the tool printed numbers" and "I checked the
numbers."

---

## Phase 5 — Complete the provisos

Open `sheaf-auto/sheaf-hardening.md` and your `onboard-notes.md`. Make sure
the final report ships with an honest provisos section that states:

- every config decision you made and why (Phase 2 notes);
- every number you flagged and how you adjudicated it (Phase 4);
- every known attribution limitation (smearing, name collisions, a format
  the adapter only partially reads);
- everything you could **not** verify, named as an explicit unknown.

If a number is real-but-bounded ("flag-level tests are 0% because tests
exercise commands, not individual flags — a real characteristic, not a
bug"), say exactly that next to it.

---

## Phase 6 — Present (BLUF first)

Lead with the bottom line, then the detail:

1. **Verdict** from the ledger: `trustworthy` / `review` / `broken`, and the
   one next action. Never present a `broken` report as finished. (Remember the
   Phase-4 cross-check: a `trustworthy` verdict on an all-zero / no-metrics
   library is *not* "clean" — call the all-0% module out.)
2. **The trust ledger** (`ledger.md`) — every headline number with its
   formula, its per-tier decomposition, and your disk adjudication beside
   each flagged figure.
3. **The report** (`sheaf-report.html`) and **provisos**.
4. A one-line note that a human's final spot-check should now *confirm*
   these numbers — point them at the three rows most worth checking.

### The deliverable for a monorepo: ONE rolled-up multi-library report (default)

Single-library targets ship the single `sheaf-report.html` above. But when the
target is a **monorepo / multi-module** set (Pigweed's ~150 modules, a CRD set,
a multi-package repo), the DEFAULT recommended deliverable is **one rolled-up
multi-library report, not N scattered per-module files.** Produce the rollup by
default; only emit separate per-module files when the user explicitly asks.

- **Build the manifest from the public modules only.** Derive the entry list
  from the libraries that expose a public API tree — for a `public/`-convention
  monorepo, the modules with a populated `<module>/public/`
  (`for d in */; do [ -d "${d}public" ] && echo "${d%/}"; done`), not every
  top-level directory. Build, tooling, generated, vendored, and test-only dirs
  are not public modules; keeping them out is the manifest-level half of "scope
  to the public surface" (the glob-level half is the `**/public/**/*.h` anchor
  in Phase 2), and it is what keeps the rolled-up report a map of the public API
  rather than of the repo's plumbing.
- **Coverage side — fan out with a manifest, roll up with `-single-file`.**
  `sheaf scan --manifest <MonorepoManifest>` fans out a scan + render for every
  entry, writing N reports **plus an `index.html`** suite page over them. Add
  **`-single-file`** to emit one portable `index.html` that embeds every report
  as a hash-routed iframe — a single self-contained artifact to hand over.
  Recommend `-single-file` for the rolled-up deliverable (best for small/medium
  runs; for very large suites the multi-file `index.html` + sibling reports is
  lighter).
- **Concept-docs side — multiple groundings roll into one region=library
  report.** The concept-docs rollup is
  `sheaf report --lens concept-docs --from-grounding g1.json --from-grounding g2.json … --library-label "<set>"`:
  multiple `--from-grounding` inputs roll into one report with region=library.
- **Concept↔coverage navigation: `--concept-docs-href` is necessary but not
  sufficient.** The render flag `--concept-docs-href` only surfaces the
  in-report concept-doc reach-line link when a concept-doc **source** is
  configured *and scanned* (the snapshot's `ConceptDocSource` is set). On the
  deterministic grounding-based path (no concept-doc source), the flag is a
  **silent no-op** — provide the coverage↔concept navigation through the suite
  **`index.html`** instead. (Forcing a concept-doc source onto prose that
  yields ~0 anchored concepts would only surface a misleading ~0 reach line —
  don't.)

---

## Phase 7 — Adapter-gap offer (only if config can't get there)

If — and only if — Phase 4 showed that a surface is genuinely
under-covered because **a needed adapter does not exist or cannot parse the
repo's format** (e.g., docs are Sphinx rST and only `markdown` is wired; the
**authoritative docs live in a surface no wired adapter renders** — Doxygen
`///` header comments / a Doxygen XML/tagfile, for which the **`doxygen`** doc
adapter is the right fix; the contract surface is GraphQL/capnproto with no
anchor; an OpenAPI API that needs the snapshot-script route), then:

1. Get the config as good as it can be first. Only surface this after
   config tuning is exhausted. **Explicitly clear the Phase-4 adapter-fit
   triage before you enter here:** confirm the surface is on the most
   specific available adapter (not stock `gtest` where `protocpp` exists),
   that `idl_prefix`/`noisy_words` are set for any qualified-name ecosystem,
   and that the globs hit the public surface. Most "missing adapter"
   conclusions are really a mis-wired existing adapter — if a config change
   closes the gap, it was never a Phase-7 case. State in the offer which
   adapter + hints you already tried.
2. Produce a crisp **adapter spec** — the `sheaf-build-adapter` handoff
   package:
   - the gap (which surface reads wrong, and the disk evidence that proves
     real coverage exists);
   - a **5-line snippet of the contract source**, a **5-line snippet of one
     test** that references it, and a **5-line snippet of one doc page**;
   - the **expected element count** from the authoritative parser;
   - the nearest existing adapter to copy (`markdowncli` ≈ 400 LOC for a
     doc-format adapter; `proto` for a declarative contract surface).
3. **Offer**, explicitly: "Config alone tops out at X% on the <surface>
   because <reason>. I can hand this to the adapter-authoring skill with a
   build-and-validate spec — want me to?" On yes, invoke `sheaf-build-adapter`
   with the spec. **Do not build the adapter in this skill.**

---

## Exit criteria

You are done when:

- `sheaf doctor` is green (no "matched 0 files");
- the snapshot's element count matches your authoritative cross-check;
- `sheaf verify`'s verdict is `trustworthy`, **or** `review` with every
  flagged finding adjudicated against disk and documented as a proviso —
  **but a `trustworthy` verdict on an all-zero / no-metrics library does not
  count as done**: it means "the zeros reproduce," not "well covered," so
  cross-check it and call the all-0% module out explicitly (Phase 4);
- the docs number is trusted only after the Phase-1 doc inventory confirmed no
  authoritative doc source (Doxygen `///`, generated reference bundle) is
  silently unparsed;
- no `reconcile` (provably-wrong) findings remain;
- the report ships with its provisos and an honest list of unknowns.

If you cannot reach those without building an adapter, you stop at Phase 7
with the offer — that is a successful, honest outcome, not a failure.

---

## Appendix — the failure-mode catalog

These are the ways a Sheaf number has lied in practice. `sheaf verify`
detects the structural ones; you adjudicate the semantic ones.

| Symptom | Likely cause | Confirm by |
|---|---|---|
| A blended % looks catastrophically low | denominator effect (commands pooled with flags, methods with fields) | read the per-tier rows in the ledger |
| A surface reads exactly 0% | format the adapter can't parse; missing source map; **or the wrong/under-configured adapter for a supported format** | open one source file; check markup; check which adapter + hints are wired |
| `tests 0%` but suite/role names obviously match the elements | **stock `gtest` where a qualified-name variant (`protocpp`) is needed; or no `idl_prefix`** to bridge `pw::Foo` ↔ `TEST(Foo, …)` | swap to `protocpp` + set `idl_prefix`; re-snapshot and watch tests jump |
| A module's **primary type is missing** from the surface (reads 0% tested; only macros/helpers present) | **leading attribute macro** between `class`/`struct` and the name (`class PW_LOCKABLE Foo`) poisons cppheader's "next token is the class name" heuristic and drops the type | `grep -rhoE '^(class\|struct) [A-Z_]+ [A-Z]' <public headers>`; set `cpp_header.ignored_attribute_macros: "<MACRO>"` (pw_status: 15 → 95 elements) |
| `tests` reads far too low on C++ but the suites are dense | **custom test-DECLARING macros invisible** to protocpp (`PW_CONSTEXPR_TEST(suite,name,body)`, not `TEST`/`TEST_F`) | `grep -rhoE '[A-Z_]+_TEST[A-Z0-9_]*\(' <test files>`; add `(suite,name,…)` macros to `protocpp.extra_test_macros` — but NOT helper/assertion macros (`TEST_STRING`, `DATA_DRIVEN_TEST`) or you fabricate phantom tests |
| An element shows 100s of tests | file-level smearing (refs attributed at file granularity) | grep the cited file; count real call sites |
| "Undocumented" but you know docs exist | adapter blind spot (e.g. `rst` reads `:cpp:func:` roles but not `.. cpp:function::` directives) | grep the doc tree for the element name; check the markup *form* |
| Low docs% on a project you know is meticulously documented | **the authoritative docs live in a surface the wired adapter doesn't render** — Doxygen `///` header comments / a generated reference bundle, not the rst/markdown prose (pw_string: 327 `///` lines, prose surface ~5%) | `grep -rn '///' <public headers>` for doc density; wire the `doxygen` adapter (Doxygen XML/tagfile) — the prose number is a FLOOR, not "undocumented" |
| Tests attributed to `run`/`get`/`Event` | name-token collision on a common word | read the test body; is it really this element? |
| Coverage much lower than your prior | over-broad exclude, or globs walking vendored/worktree trees | list what actually entered the snapshot |
| Element count is multiples of the public API | globs anchor on every header, not `**/public/**` (internal/impl/generated pulled in) → denominator inflation | list per-adapter file counts; scope to the public tree |
| Doc links 404 | URL slug/underscore convention mismatch | open 5 generated URLs |
| Element count ≠ authoritative parser | contract globs miss or over-reach | diff against `protoc` / `fidlc --json` / `--help` |
| Lag / doc-staleness reads **0 days, all fresh** on a repo you know has drift | **shallow clone** (`--depth 1`) — history truncated, so `git blame` attributes every file to the boundary commit and lag collapses to 0 (a real sha, so NOT disclosed as the unknown rate) | `git rev-parse --is-shallow-repository`; `git fetch --unshallow` then re-render. `sheaf` now detects this and suppresses Lag with a caveat instead of the fake zero |
| `sheaf verify` says **`trustworthy`** on a module that is 0% on every surface | an all-zero single-tier library emits no metrics, so there is nothing to reconcile — "trustworthy" means "the zeros reproduce," not "covered" | cross-check by hand; an all-0% module must be called out, never shown as clean |
