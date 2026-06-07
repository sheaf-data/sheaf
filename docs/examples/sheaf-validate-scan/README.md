# sheaf-validate-scan

> **Superseded for onboarding by `sheaf verify` + the `sheaf-onboard` skill.**
> Both halves are now in the engine: the false-negative search is
> `sheaf verify --disk`, and the true/false-positive precision loop is
> `sheaf verify --json`'s `assertions` array + `sheaf verify summarize`. The
> per-claim adjudication is `sheaf-onboard` Phase 4. This skill remains only
> for deep, standalone attribution audits over a very large bespoke corpus.

A Claude Code skill that performs deep validation of any sheaf scan,
classifying every "X is tested by Y" / "X is documented by Z"
attribution as TP / FP / ambiguous, and searching for FN — tests or
docs that should have attributed but were missed by the adapter's
regex set.

## Why

Sheaf's adapters use heuristic regex matching: an envoy gtest matcher
might over-attribute when two services share a method name, or under-
attribute when tests use a mocking idiom the matcher doesn't know
about. Without validation, those failure modes hide inside aggregate
percentages. This skill makes them visible.

- **Precision** of attributions: of the N tests sheaf claims exercise
  RPC X, how many actually do?
- **Sample recall**: of the test files that textually mention RPC X
  but weren't attributed, how many are real FN?

## Install

    ./install.sh

This copies `SKILL.md`, `validate.py`, and `README.md` into
`~/.claude/skills/sheaf-validate-scan/`. The skill becomes available
in every Claude Code conversation. Re-run the script after editing
any source file to refresh the install.

To remove: `./install.sh --uninstall`.

## Use

In any Claude Code session:

    /sheaf-validate-scan --config docs/examples/envoy-coverage-config.textproto \
                         --repo ~/envoy

The skill drives a two-phase flow:
1. `validate.py extract` enumerates elements via `sheaf report`,
   samples them, and writes a JSONL of assertions to verify.
2. Claude reads each assertion's source (test/doc/grep hit) and
   classifies it.
3. `validate.py summarize` reads the verdicted JSONL and writes a
   markdown report with per-library precision and sample recall.

For big runs the skill fans out across Agent invocations so a 500-
assertion scan finishes in a few minutes.

## Defaults

Tuned for hundreds of elements and tens of thousands of tests:

| Flag                | Default | Meaning                                  |
|---------------------|--------:|------------------------------------------|
| `--sample-tests`    | 10      | tests verified per element               |
| `--sample-elements` | 50      | elements sampled per library (0 = all)   |
| `--sample-docs`     | 5       | doc refs verified per element            |
| `--fn-search-limit` | 20      | FN candidates per element                |
| `--fn-categories`   | `tests.unit_tests` | which missing buckets to FN-search |

Override any of them when running the skill. Setting both `--sample-
tests 0` and `--sample-elements 0` switches to exhaustive mode (slow,
but no estimation error).

## Output

For a run against the envoy v3 xDS scan:

    sheaf-validation-<library>.jsonl   # raw verdicted assertions
    sheaf-validation-<library>.md      # human-readable summary

Markdown contents:
- per-library precision/recall table
- confirmed FP table (`element → test name + path:line + reason`)
- confirmed FN table
- ambiguous-cases list for human review
- caveats section explaining the sampling math

## Files

- `SKILL.md` — Claude Code skill manifest + instructions
- `validate.py` — Python helper (extract + summarize)
- `install.sh` — install / uninstall driver
- `README.md` — this file
