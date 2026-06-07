# sheaf init

Scaffold a starter `sheaf.textproto` plus the project's source map (`categorization-rules.textproto`) from a built-in template.

## Synopsis

```text
sheaf init [--repo <path>] [--template minimal|argh-cli|fuchsia-internal]
```

## Description

`sheaf init` writes a starter pair of config files into `--repo` (defaulting to the current directory). It refuses to overwrite an existing `sheaf.textproto` — to re-initialise, delete the file first.

Three templates ship with the binary:

- **`minimal`** *(default)* — a FIDL contract anchor plus the `missing-in-category` analyzer. The smallest viable starting point.
- **`argh-cli`** — a Rust CLI built on the `argh` crate, paired with the `rust-test` framework and a markdown doc tree under `docs/`.
- **`fuchsia-internal`** — a fuller config targeted at scanning a single Fuchsia FIDL library: gtest + rust-test for tests, markdown for docs, a `cpp-fidl-wireserver` implements-map block, and four analyzers wired up.

After writing, the command prints both file paths and a one-line next-step hint pointing at `sheaf doctor`.

## Options

| Flag | Default | Notes |
|---|---|---|
| `--repo`     | `.`        | Directory to receive the scaffolded files. |
| `--template` | `minimal`  | One of `minimal`, `argh-cli`, `fuchsia-internal`. |

## Examples

Start from the minimal template in the current directory:

```sh
sheaf init
```

Start from the Fuchsia-internal template in a fresh checkout:

```sh
sheaf init --repo ~/work/fuchsia --template fuchsia-internal
```

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Both files written. |
| `1` | One of the files failed to write. |
| `2` | Unknown template name, or `sheaf.textproto` already exists. |

## See also

- [`sheaf doctor`](sheaf_doctor.md) — the immediate next step after `sheaf init`.
- [docs/config.md](../../config.md) — full reference for the config schema produced.
