# Sheaf monitoring itself

The sheaf repo carries a dogfood `sheaf.textproto` + source map (`categorization-rules.textproto`) under [docs/examples/self-scan/](../examples/self-scan/) that scans the sheaf CLI against its own tests, docs, and worked examples. This page records how the contract surface was modelled and how to reproduce the dogfood reports.

## What is monitored

The sheaf binary's user-facing surface — the subcommands and flags exposed by `sheaf` — is the contract. Two ecosystem things sheaf otherwise ingests (FIDL libraries, gRPC packages) are deliberately not part of this scan: the proto files under `proto/` define internal data structures, not a user-facing API.

| Surface | Ecosystem | Source |
|---|---|---|
| Subcommands + flags | cobra | [docs/cli/yaml/](yaml/) — one hand-authored YAML per subcommand. |
| Reference docs | markdowncli | [docs/cli/reference/sheaf_*.md](reference/) — per-subcommand pages with `## Options` tables. |
| Concept docs | markdown | `README.md`, `docs/config.md`, `docs/cli/sheaf.md`, `docs/cli/workflows.md`, ancillary-binary reference pages. |
| Worked examples / workflows | workflows | `docs/cli/workflows.md`, `docs/playbooks/`, `docs/examples/`. |
| Tests | gotest | `**/*_test.go` (excluding the gotest adapter's own fixture corpus). |

## Why cobra YAML for a non-cobra binary

The `sheaf` binary uses bare `flag.FlagSet` parsing, not cobra. The cobra contract anchor still fits because what we need is the same shape — one element per subcommand, one element per flag, with `binary_name` carved out as the library. Hand-authoring the YAML (rather than introspecting `--help` à la [`kubectl-yamlgen`](reference/kubectl-yamlgen.md)) is cheap at this size and avoids a runtime dependency on the binary being on `$PATH` to scan.

If sheaf ever grows tens of subcommands, the right move is to wrap the existing `flag.FlagSet` dispatch in a tiny introspection step and regenerate `docs/cli/yaml/` at build time. Until then the manual YAML wins for explainability.

## Test attribution

Tests live in per-subcommand files (`internal/cli/scan_test.go`, `doctor_test.go`, …) rather than a single `cli_test.go`. The reason is the gotest adapter's `cliShapeMatch`: it requires the subcommand name to appear in the test file's *path*, not just the test name. Keeping `scan_test.go` distinct from `doctor_test.go` lands each test on the right subcommand `ContractElement`.

Flag-level attribution is stricter, and the mechanism is worth stating precisely because it's easy to get wrong. The gotest cobra-invocation extractor only attributes a flag to a test when the `--flag` long-form token appears **inside a Go string literal in that subcommand's test file**. A flag named only in a `//` comment, or passed positionally to an internal helper (`runScan(out, err, cfg, repo, true)` — the flag *value* is there, but the flag *name* never is), is invisible to the matcher. That is exactly how the self-scan once read 66% bridged while every flag was, in fact, exercised: the proofs didn't spell the tokens.

The fix is also the better test: drive the **public argv entry point** with a real `--flag` argv rather than the internal `runX` helper — for example `Coverage([]string{"--config", cfg, "--repo", dir, "--element", id, "--format", "json"})`. This exercises the actual flag-parse-and-dispatch path (which the helper-level tests skip) *and* surfaces every token for attribution. The `Test<Cmd>_ArgvFlagParsing` tests (`coverage_test.go`, `gaps_test.go`, `scan_test.go`, …) are that pattern; `serve_test.go` and `review_test.go` already used it. Keeping the helper-level tests too is fine — they assert behaviour via injected writers that the public entry doesn't expose.

The bare-binary root element (`sheaf` with no subcommand) carries no attributed test — `commandPathFromTestFile` never resolves a test file to the bare binary, so the root stays unbridged. This is universal across CLI scans (kubectl, gh, and fd roots are all the same); it is the one expected gap behind the self-scan's bridged figure.

## Reproducing the report

The canonical path regenerates both the coverage report and its concept-doc sibling in one pass:

```sh
scripts/regen-example-reports.sh sheaf-self
```

That builds `sheaf`, renders `example-reports/sheaf-self.html` via the server-free in-process path (`sheaf snapshot` → `sheaf render --from-snapshot --repo-root .`, the step the byte-identical golden test exercises), then builds `emit-grounding` and renders the concept-doc report (below). The `--repo-root` is what enables the report's Lag section — it differences each contract element's git history against its docs.

The equivalent manual coverage-only path (closer to a stranger's first run) is:

```sh
go build -o sheaf ./cmd/sheaf
go build -o scanner ./cmd/scanner

./sheaf doctor --config docs/examples/self-scan/sheaf.textproto --repo .
./sheaf scan   --config docs/examples/self-scan/sheaf.textproto --repo .

./sheaf serve  --config docs/examples/self-scan/sheaf.textproto --repo . --port 7700 &
./scanner --server http://127.0.0.1:7700 --library sheaf \
          --ecosystem cli -o example-reports/sheaf-self.html
kill %1
```

(The `serve`+`scanner` path renders without a git working tree, so its Lag section reads "not available"; the in-process path above is the one that computes lag.)

The `--config` flag points at the dogfood config in [docs/examples/self-scan/](../examples/self-scan/) rather than the default `<repo>/sheaf.textproto` — the example configs were moved out of the repo root during the public-release prep so a stranger landing on `sheaf.data` doesn't see them as project configuration.

At time of writing the scan produces **76 contract elements** (1 root + 12 subcommands + 63 flags/switches). **53 of 76 are bridged** (docs + tests + usage). The `verify` and `render` subcommands were added to the documented surface — YAML + reference page + a qualified narrative mention each, so neither reads as a 0-doc command and all their flags are concept-`clear` — but most of their flags are not yet exercised by a `--flag`-token argv test or named in a worked example, so they are documented-and-clear without being fully bridged yet. Closing that (the `Test<Cmd>_ArgvFlagParsing` argv tests plus a worked example for `sheaf verify` / `sheaf render`) is the remaining hardening step; the bare-binary root stays unbridged by design (see [Test attribution](#test-attribution)). The bridged-floor gate (`>= 47`) still holds.

## Concept-doc report

The same regen step also renders `example-reports/sheaf-self-concept-docs.html` — the doc-centric **concept-doc lens** (the `clear` / `ambiguous` / `silent` map of how sheaf's narrative docs reference its own CLI surface, distinct from the coverage matrix). It grounds against the narrative concept-doc set (`README.md`, `docs/cli/sheaf.md`, `docs/cli/workflows.md`, `docs/config.md`, `docs/mcp/*.md`, `docs/playbooks/**/*.md`) — not the per-subcommand reference pages, which are the *reference* surface. All 76 elements are `clear` (every subcommand and flag is named, in qualified `sheaf <sub> --flag` form, by at least one narrative doc); the per-command tour in [docs/cli/sheaf.md](sheaf.md#commands-in-context) is what carries the flags.

## CI gates (enforced)

A pull request that adds a new subcommand or flag must also add the matching YAML, reference markdown, a worked example, a test that drives the public argv entry (so the flag token is attributed), and a qualified mention in a narrative doc. Three Go tests — run by the CI `test` job (`go test ./... -race`) — keep that honest:

- `utils/scanner.TestRender_SelfScanByteIdentical` — the rendered report must stay byte-identical (modulo timestamp) to the committed golden. Any coverage change forces a deliberate `regen-example-reports.sh sheaf-self` + commit.
- `utils/scanner.TestSelfScan_BridgedFloor` — a hard floor (`bridged >= 47`) you cannot regenerate your way beneath: a flag missing its test/doc/example drops the count and fails the build.
- `internal/cli.TestSelfScan_ConceptDocReachFloor` — the concept-doc reach floor (`0 silent`): a flag named in no narrative doc lands as silent and fails the build.
