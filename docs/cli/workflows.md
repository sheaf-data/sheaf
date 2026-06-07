# Sheaf workflows

End-to-end recipes that combine the `sheaf` and `scanner` binaries. Each recipe is self-contained and reproducible against a fresh checkout.

## Bootstrap a new project

The shortest path from "Sheaf has never seen this repo" to "I have a corpus and a rendered report":

```sh
cd /path/to/your/repo

# 1. Scaffold sheaf.textproto + the project's source map (categorization-rules.textproto).
sheaf init --template minimal

# 2. Edit project.name and at least one adapter in sheaf.textproto.
$EDITOR sheaf.textproto

# 3. Sanity-check: every adapter resolves, the LLM is reachable, project name is set.
sheaf doctor

# 4. Run the full pipeline and print a summary.
sheaf scan

# 5. Bring up the MCP server, render the HTML report, kill the server.
sheaf serve --port 7700 &
scanner --server http://127.0.0.1:7700 --library yourlib -o report.html
kill %1
```

See [docs/playbooks/onboard-a-new-repo.md](../playbooks/onboard-a-new-repo.md) for the longer walk-through with adapter-by-adapter wiring.

## Investigate a regression

A scan that used to show 80% bridged coverage now shows 50%. To locate what dropped:

```sh
# Re-scan and dump the per-element table.
sheaf report > coverage.csv

# Filter for THIN_REFERENCE findings and inspect the worst:
sheaf gaps --kind THIN_REFERENCE --severity WARNING --format csv | head -20

# Drill into one element's profile:
sheaf coverage --element 'kubectl apply' --format json | jq '.docs, .tests'
```

## Pre-flight a pull request

`sheaf review` renders a Markdown comment showing what coverage was gained or lost between base and head:

```sh
# Render only — print to stdout for eyeballing.
sheaf review --base /tmp/main-checkout --repo . --pr PR#4242

# Deliver into a CI artifacts directory.
sheaf review --base /tmp/main-checkout --repo . --pr PR#4242 \
             --review file --file-out artifacts/review/ --post

# Post to the configured review backend (gerrit or github).
sheaf review --base /tmp/main-checkout --repo . --pr PR#4242 --post
```

## Serve corpus to a coding agent

The MCP server is the integration point for any agent that speaks MCP (Claude, Cursor, Cline, …):

```sh
sheaf serve --repo . --port 7700
```

Point the agent at `http://127.0.0.1:7700`. It can then call `library_snapshot`, `find_examples` (semantic when an embedder is configured), `review_pr`, and the lookup ops without needing direct repo access.

## Render a report after the server is gone

Saved snapshots let you re-render without restanding the server. The preferred path is fully in-process — no `sheaf serve`, no `scanner` — using `sheaf snapshot` to persist the Snapshot JSON and `sheaf render --from-snapshot` to render it:

```sh
# First time — persist the snapshot in-process (no server).
sheaf snapshot --library docker --out docker-snap.json

# Render from disk, now or later.
sheaf render --from-snapshot docker-snap.json --ecosystem cli -o docker.html
```

The older two-binary route — standing up `sheaf serve` and persisting with `scanner --snapshot-out` — still works, but `scanner --snapshot-out` is **deprecated**; prefer `sheaf snapshot` above. The offline `scanner --from-snapshot` reader continues to render any snapshot, however it was produced:

```sh
# Deprecated persist path (kept for back-compat).
sheaf serve --port 7700 &
scanner --library docker --snapshot-out docker-snap.json -o docker.html
kill %1

# Render from disk, no server needed.
scanner --from-snapshot docker-snap.json -o docker.html
```

## Generate cobra YAML for a CLI that lacks it

Some cobra-based CLIs do not ship a `make yamldocs` target. Use `kubectl-yamlgen` to introspect the binary instead:

```sh
kubectl-yamlgen --binary kubectl --out sheaf-cobra-yaml
```

Then point the cobra contract anchor at `yaml_dir: "sheaf-cobra-yaml"`. See [docs/examples/kubectl-coverage-config.textproto](../examples/kubectl-coverage-config.textproto) for the full pattern.

## Sheaf monitoring itself

Sheaf carries a self-scan config at [docs/examples/self-scan/](../examples/self-scan/) that scans the sheaf CLI surface against its own tests and docs. To reproduce the dogfooding report:

```sh
go build -o sheaf ./cmd/sheaf
go build -o scanner ./cmd/scanner

./sheaf doctor --config docs/examples/self-scan/sheaf.textproto --repo .
./sheaf scan   --config docs/examples/self-scan/sheaf.textproto --repo .

./sheaf serve  --config docs/examples/self-scan/sheaf.textproto --repo . --port 7700 &
./scanner --server http://127.0.0.1:7700 --library sheaf -o sheaf-self-report.html
kill %1
```

See [docs/cli/self-monitoring.md](self-monitoring.md) for how the contract surface was modeled.
