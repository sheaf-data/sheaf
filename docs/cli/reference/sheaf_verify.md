# sheaf verify

Adversarially re-check a scan's top-line numbers — against the snapshot it was rendered from and (optionally) the repository on disk — **before** the report is shown to anyone.

## Synopsis

```text
sheaf verify --from-snapshot <path>
             [--repo <path>] [--ecosystem <id>]
             [--low-coverage-threshold <f>]
             [--disk] [--check-urls] [--max-disk-elements <n>]
             [--sample-assertions <n>]
             [--json <path>] [--ledger <path>] [--strict]

sheaf verify --config <sheaf.textproto> --repo <path> --library <name> [flags]   # one-shot: scan + verify

sheaf verify summarize --assertions <verdicted.jsonl> [--ledger <path>]
```

## Description

A sheaf number is a join artifact, not a measurement: discovery → attribution → a fraction, and each stage has a characteristic way of lying (a blended percentage hiding a per-tier split, a doc/test format the adapter can't parse rendering a surface as a confident 0%, file-level refs smeared across siblings, name-token over-matching). `sheaf verify` is the rail that makes a reviewer's first contact a *confirmation* rather than a *discovery*.

It reconciles every shown figure to its numerator and denominator, decomposes blended percentages per tier (so the denominator effect can't hide), and flags every surface at or below the low-coverage threshold — including, at highest priority, any surface reading exactly 0%. The output is a human **trust ledger** (Markdown) plus, with `--json`, a machine-readable report.

The verdict rolls up to one word: **trustworthy** (every figure reconciled, nothing flagged), **review** (warnings — numbers flagged for validation), or **broken** (at least one number is provably wrong or could not be verified).

### The on-disk oracle (`--disk`)

With `--disk` and a `--repo`, verify runs the checks that need the source tree:

- **False-negative search** — for reportedly-untested elements with a distinctive name (`Service.Method`, a flag literal), it greps the tracked tree and surfaces unattributed hits in test files as *candidates* for an agent to confirm.
- **Doc-URL resolution** (`--check-urls`) — resolves a bounded sample of published doc URLs (HTTP HEAD, falling back to a ranged GET) and flags dead links. Network is a side effect, so it is gated behind both `--disk` and this explicit opt-in. An unreachable network is reported as a single honest caveat, never as findings.
- **Ground-truth element count** (`--expected-elements N`) — cross-checks the scan's element count (the denominator of every percentage) against an authoritative count you compute with `protoc` / `fidlc --json` / the `--help` tree. A large, unambiguous gap is a provable `error`; a small gap is a warning; an exact match is clean. Without `--expected-elements` (and no built-in auto-checker for the ecosystem) it is an honest caveat, not a finding.

The honesty rail governs everything: a number is called *wrong* only when it is provably wrong (a reconcile mismatch, or a disk-confirmed false claim). A missing tool, an offline network, or an ecosystem with no authoritative parser becomes an explicit caveat, not a finding.

### Attribution precision (`--json` + `summarize`)

With `--json`, the report carries a deterministic, bounded **`assertions`** array: a sample of attributed "X tested by Y" / "X documented by Z" claims, weighted toward high-count and common-name (collision-prone) elements, each with `verdict: null`. An agent reads each cited source and fills the verdict (`tp` | `fp` | `ambiguous`) with a one-line reason, then:

```sh
sheaf verify summarize --assertions verdicted.jsonl --ledger precision.md
```

computes per-library **precision** and the confirmed-false-positive table. The sampling and the arithmetic are the binary's; the semantic true/false-positive call stays the agent's. `--assertions` accepts JSONL (one assertion per line), a bare JSON array, or a `verify.json` with an `assertions` array.

## Options

| Flag | Default | Notes |
|---|---|---|
| `--from-snapshot` | *(one source)* | Snapshot JSON to verify (from `sheaf snapshot`). Provide this or `--config`. |
| `--config` | *(one source)* | One-shot: build the snapshot in-process from this `sheaf.textproto` (with `--repo` + `--library`) and verify it. Auto-locates the source map next to the config. |
| `--library` | *(none)* | Library to scan for the `--config` one-shot. |
| `--repo` | *(none)* | Repo root, for disk-oracle checks, source resolution, and the `--config` one-shot scan. |
| `--ecosystem` | *(scanner default)* | View id: `cli` \| `fidl` \| `proto` \| `cpp` \| … |
| `--low-coverage-threshold` | `0.15` | Flag any per-tier or pooled coverage at or below this fraction. |
| `--disk` | `false` | Run the on-disk oracle. Requires `--repo`. |
| `--check-urls` | `false` | With `--disk`, resolve published doc URLs and flag dead links (network). |
| `--max-disk-elements` | `0` | Cap elements the disk oracle samples (`0` = built-in default). |
| `--sample-assertions` | `0` | Cap elements sampled into the `assertions` array (`0` = default 50). |
| `--expected-elements` | `-1` | Authoritative element count to cross-check the denominator (`-1` = unset). Used with `--disk`. |
| `--json` | *(none)* | Write the machine-readable report (with `assertions`) here. |
| `--ledger` | *(stdout)* | Write the human trust ledger here. |
| `--strict` | `false` | Exit `3` when the verdict is `broken`. For CI gating. |

## Examples

Reconcile a snapshot against the join only (no disk):

```sh
sheaf verify --from-snapshot docker-snap.json --ecosystem cli
```

Full pre-show check against disk, with the ledger and machine report on file:

```sh
sheaf verify --from-snapshot snapshot.json --repo . --ecosystem cli \
  --disk --check-urls --json verify.json --ledger ledger.md
```

Compute attribution precision from agent-verdicted assertions:

```sh
sheaf verify summarize --assertions verdicted.jsonl --ledger precision.md
```

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Verify ran; ledger (and any report) written. |
| `1` | Snapshot load, render, or write failed. |
| `2` | Missing `--from-snapshot` or a bad flag. |
| `3` | `--strict` and the verdict is `broken`. |

## See also

- [`sheaf snapshot`](sheaf_snapshot.md) — emits the Snapshot JSON `verify` consumes.
- [`sheaf render`](sheaf_render.md) — renders the HTML report once the numbers are trusted.
- The `sheaf-onboard` procedure — Phase 4 drives `verify` (and `verify summarize`) to validate a first report.
