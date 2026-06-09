# Upgrade-guide template

Copy this into `docs/releasing/upgrade-vPREV-to-vX.Y.Z.md` whenever a
release carries breaking changes, and link it from that release's notes.

v0.1.0 is the first release — there is nothing to upgrade *from* — so this
file is a template for future releases, not a guide anyone needs yet.

---

# Upgrading sheaf vPREV → vX.Y.Z

## At a glance

<!-- One line: is this a drop-in upgrade, or does it need config/CLI
     changes? If drop-in, say so and keep the rest short. -->

## Config (`sheaf.textproto`)

<!-- One subsection per schema change, each with a before/after:

### `<block>.<field>` renamed / removed / added

```textproto
# before
<block> { old_field: ... }
# after
<block> { new_field: ... }
```

What changed, why, and the exact edit to make. Note whether the old form
still parses (deprecation) or is a hard error. -->

## CLI

<!-- Flag / subcommand / default-behavior changes:

| Old | New | Notes |
|---|---|---|
| `sheaf x --old` | `sheaf x --new` | renamed; old flag removed in vX.Y.Z |

-->

## Migration steps

1. <!-- Ordered, copy-pasteable. -->
2.

## Rollback

<!-- How to pin the previous version if the upgrade goes wrong, e.g.
     `go install github.com/sheaf-data/sheaf/cmd/sheaf@vPREV`. -->
