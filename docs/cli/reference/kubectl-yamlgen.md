# kubectl-yamlgen

Introspect a `kubectl` binary via its own `--help` output and emit per-subcommand YAML files in the schema the `cobra` contract-anchor adapter consumes.

## Synopsis

```text
kubectl-yamlgen [--binary <name>] [--out <dir>] [-v]
```

## Description

`kubectl-yamlgen` sidesteps the fact that the kubernetes/kubernetes source tree does not ship a docker/cli-style `make yamldocs` target. Where docker's cobra adapter consumes the YAML written by `doc.GenYamlTree`, kubectl's in-tree generator emits a different schema (`Name` / `Synopsis` / `Description` vs. `command` / `short` / `long`) and only walks one level deep.

Instead of writing a kubernetes-specific contract anchor, this helper drives `kubectl <path> --help` recursively, parses the textual help format, and writes `kubectl_<sub>_<sub>.yaml` files into `--out`. The output schema matches docker/cli, so the existing cobra adapter consumes it unchanged.

Mechanics:

- `kubectl options` is consulted once for the global-flag block. Those options are copied verbatim into every command's `inherited_options`.
- Each command's local `Options:` block becomes its `options` array. Option types are inferred from default-value literals: `false` / `true` → `bool`; `''` / `'foo'` → `string`; `[]` → `stringSlice`; numeric (with optional duration suffix) → `int` or `duration`.
- Subcommands are picked up under any section header containing the word `Command` (kubectl splits subcommands across Basic / Deploy / Cluster Management / etc.). User-installed plugins are skipped.

## Options

| Flag | Default | Notes |
|---|---|---|
| `--binary <name>` | `kubectl`        | Binary to introspect (path or name on `$PATH`). |
| `--out <dir>`     | `kubectl-yaml`   | Output directory; created if missing. |
| `-v`              | `false`          | Log each command walked. |

## Example

```sh
kubectl-yamlgen --binary kubectl \
                --out /Volumes/T7/sheaf-workspace/checkouts/kubernetes/sheaf-cobra-yaml
```

Then point the cobra contract anchor at that directory:

```textproto
contract_anchor {
  name: "cobra"
  cobra {
    yaml_dir: "sheaf-cobra-yaml"
    binary_name: "kubectl"
    include: "**/*.yaml"
  }
}
```

A full example config is at [docs/examples/kubectl-coverage-config.textproto](../../examples/kubectl-coverage-config.textproto).

## See also

- [`sheaf scan`](sheaf_scan.md) — the consumer of the generated YAML via the cobra adapter.
- [docs/playbooks/onboard-a-new-repo.md](../../playbooks/onboard-a-new-repo.md) — end-to-end walk-through for onboarding a repo (incl. CLIs).
