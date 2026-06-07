# Self-scan example — Sheaf dogfooding itself

This directory holds the [`sheaf.textproto`](sheaf.textproto) + the source map ([`categorization-rules.textproto`](categorization-rules.textproto)) that Sheaf uses to scan its *own* CLI surface against its own tests, docs, and worked examples. It's the canonical "this is what a complete config looks like for a cobra-style CLI" example.

## What it scans

The config treats the `sheaf` binary as the contract:

| Surface | Adapter | Source |
|---|---|---|
| Subcommands + flags | cobra | [docs/cli/yaml/](../../cli/yaml/) — one hand-authored YAML per subcommand. |
| Reference docs | markdowncli | [docs/cli/reference/](../../cli/reference/) — per-subcommand pages. |
| Concept docs | markdown | `README.md`, `docs/config.md`, `docs/cli/sheaf.md`, etc. |
| Worked examples | workflows | `docs/cli/workflows.md`, `docs/playbooks/`, `docs/examples/`. |
| Tests | gotest | `**/*_test.go` (excluding the gotest adapter's own fixtures). |

`docs/cli/self-monitoring.md` walks through *why* each piece is shaped the way it is.

## Reproducing the report

From the repo root:

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

The rendered report lands at [example-reports/sheaf-self.html](../../../example-reports/sheaf-self.html).

## Why these aren't at the repo root

Originally `sheaf.textproto` and the source map (`categorization-rules.textproto`) lived at the repo root because that's the default location `sheaf doctor` / `sheaf scan` look at. The cost of that default was that any stranger landing on `sheaf.data` saw two config files at the top level and reasonably wondered if they were *the project's own configuration* rather than an example.

Moving them into `docs/examples/self-scan/` makes the intent explicit at the cost of one extra `--config` flag on every self-scan invocation. The flag is small, the clarity gain is meaningful.
