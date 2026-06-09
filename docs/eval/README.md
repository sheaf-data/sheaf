# Eval: does sheaf improve grounded answer quality?

The claim sheaf makes is that an agent grounded on the contract surface
(via `sheaf serve`) answers **grounded** questions about a codebase more
correctly and with real citations than the same agent without it. This
directory is the scaffold to measure that: an eval-set format, a run
protocol, and a scoring rubric.

**Status:** this is the harness, not the result. Filling the gold answers
and running the two agent passes needs a maintainer with LLM access — see
"What's left" at the bottom. The example set below is real and runnable as
a starting point.

## What we measure

Per question, for each of two conditions (**baseline** = agent with no
sheaf MCP; **with-sheaf** = same agent + `sheaf serve --stdio`):

| Metric | Definition |
|---|---|
| **Correctness** | 1 / 0.5 / 0 vs the gold answer (judge + spot-check). |
| **Citation grounding** | Did the answer cite a *real* `file:line` / element that supports it? (yes/no) |
| **Hallucination** | Did it invent an API, flag, or path that doesn't exist? (yes/no) |

The headline is the **delta**: correctness and grounding rate
with-sheaf minus baseline, on questions that are actually grounded in the
contract (sheaf is not expected to help open-ended design questions, and
the eval set should not contain them).

## The eval set

[`eval-set.example.yaml`](eval-set.example.yaml) defines the format and
ships a few real, grounded questions about sheaf itself (with gold answers
already filled). For each target repo, author **10-20** questions whose
answers live in the contract surface — flags, methods, config keys,
behaviors — each with:

- `gold_answer`: the correct answer in one or two sentences.
- `gold_citations`: the `file:line` or element IDs a grounded answer
  should rest on (this is what "citation grounding" is scored against).
- `category`: `flag` / `method` / `config` / `behavior` (for slicing results).

## Run protocol

1. **Baseline.** Ask the agent each question with **no** sheaf MCP
   configured. Capture the answer and any sources it cites.
2. **With-sheaf.** Same agent, same questions, with sheaf wired in
   (`sheaf serve --stdio` — see [../mcp/clients.md](../mcp/clients.md)).
   Capture answer + cited sources.
3. **Score.** For each (question, condition), apply the rubric below —
   an LLM judge for the first pass, then spot-check a sample by hand.
4. **Aggregate.** Report correctness rate, grounding rate, and
   hallucination rate per condition, and the deltas, sliced by `category`.

## Judge rubric (first-pass LLM, then spot-check)

```
Given: question, gold_answer, gold_citations, and the agent's answer.
1. Correctness: 1 if it matches the gold answer; 0.5 if partially right
   or incomplete; 0 if wrong or evasive.
2. Grounding: yes only if the answer cites a source consistent with
   gold_citations (the same flag/method/file). A confident answer with
   no/!wrong citation is grounding=no even if correct.
3. Hallucination: yes if it names a flag/method/path not in the repo.
Return {correctness, grounding, hallucination} with a one-line reason.
```

## Caveats (state these with any published result)

- Small N and a single judge model bias the absolute numbers; the delta
  between conditions is more robust than either side alone.
- Repo + question selection can flatter or sandbag sheaf; pick questions
  that are genuinely grounded, and disclose how they were chosen.
- Sheaf helps grounded/contract questions, not open-ended reasoning;
  don't generalize the delta beyond that.

## What's left (8box sg6 act7)

- `583DD21D` — author 10-20 gold questions per target repo (extend the
  example set). Needs human judgment on "grounded".
- `B85275D9` — run the baseline + with-sheaf passes (needs LLM + the agent
  harness).
- `6395CE6D` — score with the rubric (LLM judge + spot-check).
- `AB96D4D3` — write up results + caveats for the launch post.
