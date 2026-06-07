---
name: sheaf-validate-scan
description: |
  Deep validation of a sheaf scan report. Classifies every "X is tested
  by Y" / "X is documented by Z" attribution as true positive, false
  positive, or ambiguous, and searches for false negatives — tests or
  docs that should have been attributed but were missed by the
  adapter's regex set. Samples per-element + per-library when the
  corpus is huge so a run stays bounded. Produces a markdown report
  with per-library precision / sample-recall, plus tables of confirmed
  FPs and FNs.
  MANDATORY TRIGGERS: /sheaf-validate-scan, validate sheaf scan, deep
  validate scan, sheaf precision recall, sheaf FP FN, audit attributed
  tests, audit sheaf coverage, check for false positives in scan, find
  missed tests sheaf, sheaf scan audit, validate coverage profiles
---

# sheaf-validate-scan

> **Superseded for onboarding by `sheaf verify` + the `sheaf-onboard` skill.**
> Both halves of this loop are now built into the engine: the false-negative
> search is `sheaf verify --disk`, and the true/false-positive precision loop
> is `sheaf verify --json`'s `assertions` array — verdict each claim, then
> `sheaf verify summarize` computes precision and the confirmed-FP table. The
> per-claim adjudication lives in `sheaf-onboard` Phase 4 — use those to
> onboard a repo and validate its first report. **This skill remains** only
> for a deep, standalone attribution audit over a *very large bespoke corpus*
> (its own `sheaf serve` lifecycle, ripgrep FN search, full sample-recall
> math) — the one job not folded into the onboarding flow.

A sheaf scan claims things like "RPC `Foo.Bar` is exercised by 47 unit
tests" or "Element `Baz` is undocumented." This skill spot-checks every
such assertion — classifying it as TP / FP / ambiguous — and goes
looking for FN: tests or docs that *should* have attributed but were
missed by the adapter's regex set.

The validation work splits into two layers:

- **Python helper** (`validate.py` in this skill's dir): enumerates
  elements, samples, greps for FN candidates, manages the sheaf serve
  lifecycle, emits a JSONL of assertions to verify.
- **You** (Claude, when this skill is active): read each assertion's
  source via the Read tool, decide TP/FP/FN/ambiguous from the actual
  test/doc body, write the verdict back into the JSONL.

The helper then summarizes the verdicted JSONL into a markdown report.

## What the user provides

Required:
- `--config PATH` to sheaf.textproto
- `--repo PATH` to the scanned project root

Optional (with sensible defaults):
- `--library NAME` (default: all libraries)
- `--sample-tests N` per-element cap (default 10)
- `--sample-elements N` per-library cap (default 50; pass 0 for none)
- `--sample-docs N` per-element doc cap (default 5)
- `--fn-search-limit N` per-element FN-candidate cap (default 20)
- `--fn-categories ...` which missing buckets to search (default
  `tests.unit_tests`)
- `--port N` (default 7700)
- `--out PATH` JSONL output path

If the user gives you a partial command, ask only for genuinely missing
required args. Don't ask about defaults the user didn't mention.

## What you do, step by step

### 1) Bootstrap

If sheaf serve isn't already running on the target port, pass
`--start-server` so `validate.py extract` spawns one for the duration
of the extract. Otherwise extract attaches to the running server.

### 2) Extract

Run:

    validate.py extract --config <X> --repo <Y> --out <PATH> \
        [--library <L>] [--sample-tests N] [...]

This writes a JSONL where every line is one of three shapes:

```
{"kind": "tested_by",     "element": "<lib>/<Service.Method>",
 "test_name": "...", "test_path": "...", "test_line": N,
 "verdict": null, "reason": null, ...}

{"kind": "documented_by", "element": "...",
 "doc_path": "...", "doc_line": N, "doc_url": "...",
 "verdict": null, "reason": null, ...}

{"kind": "fn_candidate",  "element": "...", "category": "tests.unit_tests",
 "candidate_path": "...", "candidate_line": N, "matched_term": "...",
 "matched_body": "...", "verdict": null, "reason": null, ...}
```

`verdict` and `reason` are null — your job is to fill them in.

### 3) Verify each assertion

For each row, decide a verdict by reading the actual source.

**Tested-by row** (`kind == "tested_by"`):

1. Use Read on `test_path` around `test_line` — read enough context to
   see the entire test body (usually ±60 lines).
2. Decide if the test actually exercises `element`. Look for:
   - **Strong TP**: the element's fully-qualified dotted form, the
     bare `Service.Method`, the `Service::Method` C++ form, the
     generated proto class name, or a clear stub call into the service
     appears in the test body.
   - **Weak TP / ambiguous**: the test imports/includes a header for
     the package, calls a method whose name matches the element's
     method, but it's plausibly a different overload — flag as
     `ambiguous`.
   - **FP**: none of the above. The test got attributed only because
     of name-token overlap or a stray mention in a comment / string
     constant unrelated to the element.
3. Set `verdict` ∈ `{"tp", "fp", "ambiguous"}` and write a one-line
   `reason`.

**Documented-by row** (`kind == "documented_by"`):

1. Read `doc_path` around `doc_line` (smaller window — docs are
   typically markdown headings + paragraphs).
2. Decide: is `element` the genuine subject of this doc section?
   - **TP**: the section heading or first paragraph names the element
     and the prose describes its behavior.
   - **FP**: the element is mentioned in passing in a list, in a code
     fence intended to document something else, or as a "see also"
     pointer.
   - **ambiguous**: the section is about a parent / sibling and
     mentions the element without giving it dedicated treatment.
3. Set verdict + reason.

**FN-candidate row** (`kind == "fn_candidate"`):

1. Read `candidate_path` around `candidate_line` — typically a wider
   window because the relevant test fixture may be elsewhere in the
   file.
2. Decide: would a correct adapter have attributed this test to
   `element`?
   - **FN**: the test plainly exercises `element` (uses the dotted
     name, a stub call into the service, etc.) but the existing
     adapter didn't pick it up. This is the most useful kind of
     finding — tells the user the adapter's regex set has a gap.
   - **not_fn**: the matched term is in a comment, an unrelated
     string, or refers to a different element with a similar name.
   - **ambiguous**: in code context but not clearly exercising
     `element`.
3. Set verdict + reason.

### 4) Fan out for big runs

If the JSONL has more than ~80 rows, do not verify them all in this
session — fan out via the Agent tool. Chunk by roughly 30 rows. Each
subagent prompt should:
- be self-contained (paste the JSONL chunk inline; tell it the repo
  root absolute path so Read calls work)
- ask the agent to return ONLY a JSON object: `{"<row-index>":
  {"verdict": "...", "reason": "..."}, ...}` — easy to merge back
- not write any files itself

After all agents return, you (the parent) merge their verdicts back
into the JSONL by index.

### 5) Summarize

When every row has a `verdict` filled, run:

    validate.py summarize --jsonl <PATH> --out <MARKDOWN-PATH>

The helper computes per-library precision (TP / (TP + FP) over
sampled attributions) and sample recall (FN / (FN + not_FN) over the
grep-found candidates), then writes a markdown report.

Tell the user the markdown path and the headline numbers
(`overall: X assertions verified, Y FPs, Z FNs, K ambiguous`).

## Sampling guidance

The defaults are tuned for repos with hundreds of elements and tens
of thousands of tests:
- 10 tests per element gives ±15% precision estimate at 95%
  confidence for elements with ≥50 attributed tests.
- 50 elements per library covers most of the head of the
  distribution; tail elements (with few attributed tests) get
  exhaustive verification anyway.
- 20 FN candidates per element catches the common adapter blind
  spots without grinding the user's machine through a 5-million-line
  test tree.

For a quick spot-check, lower the caps; for an exhaustive audit, raise
`--sample-elements 0 --sample-tests 0` (helper treats 0 as "no cap")
**but warn the user that this can take a long time on large repos.**

## When NOT to invoke

- The user has a single-element question — `sheaf coverage --element
  <id>` is the right tool, not this skill.
- The user wants to *run* the scan (rather than validate an existing
  one) — they want `sheaf scan` or the project's own example config.
- The scan has fewer than ~20 assertions total (just verify them
  inline, no need to invoke validate.py).

## Failure modes worth flagging back to the user

- `sheaf serve` won't start (typically: bad config, port in use, repo
  doesn't have the things the config expects). Tell the user; don't
  retry blindly.
- High FP rate (>20%) → the adapter's matcher is too loose. Worth
  telling the user.
- High FN rate (>50% of candidates are real FNs) → the adapter is
  missing a common idiom in this codebase. Worth telling the user.
- The `unverified` count is non-zero in `summarize` output → your fan-
  out missed some rows. Re-run on the unverified subset.
