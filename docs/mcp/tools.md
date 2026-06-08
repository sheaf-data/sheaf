# Sheaf MCP tools: input schemas & example prompts

This page gives, for every operation `sheaf serve` exposes, the **JSON Schema** for its input (the `inputSchema` an MCP client registers per tool) and the **example prompts** that should route an agent to it: the "when and why to call this" an LLM needs to pick the right tool. The wire protocol, params prose, and return shapes are in [api.md](api.md); the proto messages the results carry are in [schema.md](schema.md). A consolidated machine-readable export of every input schema below lives in [tool-schemas.json](tool-schemas.json).

> **Transport note.** Today these operations are dispatched directly as JSON-RPC methods over HTTP at `POST /mcp` (e.g. `"method": "library_snapshot"`), and `tools/list` returns `{name, description}` only. The standard MCP `initialize` handshake and `tools/call` envelope (which would let a desktop client register these `inputSchema`s automatically) are tracked for a post-launch release. Until then, the schemas below are the contract for the params object you send, and the prompts describe when a grounding layer should reach for each.

All schemas are JSON Schema draft-07. Result types named in CapitalCase (`ContractElement`, `CoverageProfile`, `Finding`) are the protobuf messages indexed in [schema.md](schema.md).

---

## `list_libraries`

Enumerate the libraries in the corpus with per-bucket counts. The picker step before `library_snapshot`.

**Input schema:**
```json
{ "type": "object", "properties": {}, "additionalProperties": false }
```

**Output:** `{ "libraries": [{ "library": string, "elements": int, "profiles": int, "findings": int }], "total": int }`

**Call it when the agent asks:**
- "What libraries / projects are in this index?"
- "Which surfaces can I query here?"
- (internally) as the first step before pulling a specific library.

---

## `library_snapshot`

Bulk dump of one library: every `ContractElement`, `CoverageProfile`, `Finding`, plus the analyzer names. One round trip; the call the `scanner` and report generator make.

**Input schema:**
```json
{
  "type": "object",
  "properties": {
    "library": { "type": "string", "description": "Library name as returned by list_libraries." }
  },
  "required": ["library"],
  "additionalProperties": false
}
```

**Output:** `{ "schema_version": int, "library": string, "elements": ContractElement[], "profiles": CoverageProfile[], "findings": Finding[], "analyzers": string[] }`

**Call it when the agent asks:**
- "Give me everything about `kubectl`."
- "I need the full coverage matrix for this library to render / summarize it."
- Prefer `query_contract` or `find_coverage_gaps` when the agent wants one element or a focused worklist; `library_snapshot` is the firehose.

---

## `query_contract`

Fetch one `ContractElement` plus a configurable slice of its `CoverageProfile`. The everyday "tell me about this element" call.

**Input schema:**
```json
{
  "type": "object",
  "properties": {
    "element_id": { "type": "string", "description": "Canonical element ID, e.g. \"kubectl get\" or \"fuchsia.io/Directory.Open\"." },
    "subtree": {
      "type": "string",
      "enum": ["tests", "docs", "examples", "usage", "gaps"],
      "description": "Optional. Narrow the response to one coverage bucket instead of the full profile."
    }
  },
  "required": ["element_id"],
  "additionalProperties": false
}
```

**Output:** `{ "element": ContractElement, "coverage": CoverageProfile }` (or, when `subtree` is set, one of `tests` / `docs` / `examples` / `usage` / `gaps`). Returns error `-32004` when no element matches.

**Call it when the agent asks:**
- "What tests cover `kubectl get`?" (use `subtree: "tests"`)
- "Is `Directory.Open` documented, and where?" (use `subtree: "docs"`)
- "Show me the full coverage for this element."
- Reach here *before* writing a call to an unfamiliar element: it returns the real signature, the docs that explain it, and the tests that prove it.

---

## `coverage`

Return the full `CoverageProfile` for one element, skipping the `element` field. Use when the caller already knows the element shape and just wants the coverage.

**Input schema:**
```json
{
  "type": "object",
  "properties": {
    "element_id": { "type": "string" }
  },
  "required": ["element_id"],
  "additionalProperties": false
}
```

**Output:** a `CoverageProfile`. Returns `-32004` when no profile is registered for `element_id`.

**Call it when the agent asks:**
- "Just the coverage profile for `kubectl get`; I already have the element."
- A leaner `query_contract` for follow-up calls in a loop.

---

## `find_coverage_gaps`

List `Finding`s, filterable by library and finding-kind. The focused worklist alternative to the `library_snapshot` firehose.

**Input schema:**
```json
{
  "type": "object",
  "properties": {
    "library": { "type": "string", "description": "Optional. Filter to one library (accepts the <lib>/ and <lib>. prefixes)." },
    "kinds": {
      "type": "array",
      "items": {
        "type": "string",
        "enum": ["DOCUMENTED_UNTESTED", "TESTED_UNDOCUMENTED", "MISSING_IN_CATEGORY", "THIN_REFERENCE", "EXTERNAL_MENTION_ONLY", "COVERAGE_DELTA", "STALE_DOC"]
      },
      "description": "Optional. Filter to these finding kinds; the FINDING_KIND_ prefix is also accepted."
    },
    "max_items": { "type": "integer", "minimum": 0, "description": "Optional cap; 0 = no cap." }
  },
  "additionalProperties": false
}
```

**Output:** `{ "findings": Finding[], "total": int }`, where `total` is the post-filter count.

**Call it when the agent asks:**
- "What's undocumented in `kubectl`?" (`kinds: ["TESTED_UNDOCUMENTED"]`)
- "Which elements have docs but no tests?" (`kinds: ["DOCUMENTED_UNTESTED"]`)
- "Give me a coverage worklist for this library."

---

## `find_examples`

Search for `ContractElement`s matching a free-form description. Semantic (cosine similarity on embeddings) when an embedder is configured, else token-overlap on element IDs.

**Input schema:**
```json
{
  "type": "object",
  "properties": {
    "query": { "type": "string", "description": "Natural-language description of the behavior you want an element for." },
    "max_items": { "type": "integer", "minimum": 1, "default": 10 }
  },
  "required": ["query"],
  "additionalProperties": false
}
```

**Output:** `{ "scoringMethod": string, "matches": [{ "element_id": string, "score": number, "kind": string, "location": SourceLocation, "doc_excerpt": string }], "fallbackReason"?: string }`

**Call it when the agent asks:**
- "Which method opens a directory and lists its children?"
- "Find the element that does rate limiting / token-bucket acquisition."
- The discovery entry point when the agent knows *what it wants to do* but not *which element does it*: the domain-drift bridge ("invite people to a meeting" → `events.insert`).

---

## `verify_invocation`

Check whether an invocation string (typically scraped from agent-authored code) names a real `ContractElement`. Built to short-circuit a hallucinated call before the agent commits to it.

**Input schema:**
```json
{
  "type": "object",
  "properties": {
    "invocation": { "type": "string", "description": "The element ID / call the agent is about to emit, e.g. \"fuchsia.io/Directory.Open\"." }
  },
  "required": ["invocation"],
  "additionalProperties": false
}
```

**Output (match):** `{ "matched": true, "element": ContractElement, "confidence": 1.0 }`
**Output (no/fuzzy match):** `{ "matched": false, "candidate"?: string, "confidence"?: number, "reason": string }`

**Call it when the agent is about to:**
- Emit a call to an element it isn't certain exists ("does `Directory.OpenDeprecated` exist?").
- Self-check generated code before returning it: `matched: false` with a `candidate` is the "did you mean…" that catches a hallucination.

---

## `review_pr` *(optional: only when the server was started with `WithReview`)*

Run the PR-bot flow against two pre-checked-out working trees and render a coverage-delta comment. Sheaf is git-agnostic: the caller hands it `base_path` + `head_path`, not git refs.

**Input schema:**
```json
{
  "type": "object",
  "properties": {
    "pr_ref": { "type": "string", "default": "PR#unknown", "description": "Label for the comment header." },
    "base_path": { "type": "string", "description": "Working tree at the PR base commit." },
    "head_path": { "type": "string", "description": "Working tree at the PR head commit." },
    "post": { "type": "boolean", "default": false, "description": "Post via the configured review adapter; print-only when false." }
  },
  "required": ["base_path", "head_path"],
  "additionalProperties": false
}
```

**Output:** `{ "pr_ref": string, "comment_md": string, "affected_elements": string[], "suggested_reviewers": string[], "subscribers": string[], "posted": bool, "posted_to"?: string, "adapter"?: string }`. Returns `-32601` if the server wasn't started with review enabled.

**Call it when:**
- A CI bot needs the coverage delta for an opened/updated PR.
- "Which contract elements did this PR change the coverage of?"

---

## `tools/list`

MCP convention. No params. Returns the operations this server enables, so a client can discover the vocabulary without hard-coding it.

**Input schema:**
```json
{ "type": "object", "properties": {}, "additionalProperties": false }
```

**Output:** `{ "tools": [{ "name": string, "description": string }] }`

---

## See also

- [api.md](api.md): wire protocol, params prose, return shapes, error codes, auth.
- [schema.md](schema.md): the proto messages every result payload uses.
- [tool-schemas.json](tool-schemas.json): the input schemas above, consolidated for machine consumption.
