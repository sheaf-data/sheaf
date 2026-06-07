# Reproducibility — pinned sources for the example gallery

Every report in the [gallery](README.md) is regenerated from a pinned upstream
checkout with a pinned `sheaf` build. This page is the canonical record of *what
produced what*, so a stranger can clone the same commit, point `sheaf` at the same
config, and re-derive the same numbers.

The gallery is deliberately small: **gh / sheaf / envoy** (featured on the README)
plus **pigweed / fuchsia / ffx** (here in `docs/examples/`, off the README).

## sheaf build

| Field | Value |
|---|---|
| Version string | `0.1.0-dev` — [`internal/cli/cli.go`](../../internal/cli/cli.go) (`BuildVersion`, overridable at link time via `-ldflags`) |
| Source | `github.com/sheaf-data/sheaf` @ this repo's `main` |

Reports embed only this version string and a render timestamp; the timestamp is the
sole non-deterministic byte (the self-scan golden test normalizes it before comparing).

## Pinned upstream sources

Each source is pinned to the exact commit the committed reports were rendered against.
Fetch the commit, then verify (below) before regenerating.

| Project | Upstream | Branch | Pinned commit |
|---|---|---|---|
| envoy | [envoyproxy/envoy](https://github.com/envoyproxy/envoy) | `main` | `8492f41012d334a88d321972226af5d33747107c` |
| gh | [cli/cli](https://github.com/cli/cli) | `trunk` | `9a2f33078d324e4ec175a035051436987acff810` |
| sheaf (self-scan) | [sheaf-data/sheaf](https://github.com/sheaf-data/sheaf) | `main` | this repo @ `HEAD` |
| fuchsia + ffx | [fuchsia.googlesource.com/fuchsia](https://fuchsia.googlesource.com/fuchsia) | — | `3a0c9c4d54b6c93ba2c4867ee11fb1b9e42358eb` |
| pigweed | [pigweed.googlesource.com/pigweed](https://pigweed.googlesource.com/pigweed) | `main` | `77591bc8702a1bd37608ffaee9027265cf2f32e7` |

## Per-report provenance

`sheaf` version is `0.1.0-dev` for every row. Config paths are relative to this
directory (`docs/examples/`).

| Report | Source @ pin | Ecosystem | Config |
|---|---|---|---|
| envoy | envoyproxy/envoy @ `8492f41` | proto | `envoy-coverage-config.textproto` |
| gh † | cli/cli @ `9a2f330` | cli | `gh-coverage-config.textproto` |
| sheaf (self-scan) | sheaf-data/sheaf @ `HEAD` | cli | `self-scan/sheaf.textproto` |
| fuchsia.io | fuchsia @ `3a0c9c4` | FIDL | `fuchsia-io-coverage-config.textproto` |
| fuchsia driver-framework | fuchsia @ `3a0c9c4` | FIDL | `fuchsia-driver-framework-coverage-config.textproto` |
| fuchsia driver-framework-family | fuchsia @ `3a0c9c4` | FIDL | `fuchsia-driver-framework-family-coverage-config.textproto` |
| fuchsia.ui.composition | fuchsia @ `3a0c9c4` | FIDL | `fuchsia-ui-composition-coverage-config.textproto` |
| fuchsia.ui.gfx | fuchsia @ `3a0c9c4` | FIDL | `fuchsia-ui-gfx-coverage-config.textproto` |
| ffx | fuchsia @ `3a0c9c4` | cli | `ffx-coverage-config.textproto` (surface synthesized from ffx's CLI goldens — see `scripts/gen-ffx-coverage-inputs.py`) |
| fuchsia-coverage (27-domain omnibus) | fuchsia @ `3a0c9c4` | FIDL | generated from `fuchsia-coverage-configs/` + `fuchsia-coverage-manifest.textproto` |
| Pigweed pw_rpc | pigweed @ `77591bc` | cpp | `pigweed-pw_rpc-coverage-config.textproto` |
| Pigweed pw_log | pigweed @ `77591bc` | cpp | `pigweed-pw_log-coverage-config.textproto` |
| Pigweed pw_transfer | pigweed @ `77591bc` | cpp | `pigweed-pw_transfer-coverage-config.textproto` |

† The `gh` report is not yet rendered into the published gallery; its source + config
are pinned here so the first render is reproducible.

## Verify a checkout matches its pin

Before regenerating, confirm the checkout is at the pinned commit:

```sh
# example: envoy
git -C "$ENVOY_CHECKOUT" rev-parse HEAD
# expect: 8492f41012d334a88d321972226af5d33747107c
```

To pin a checkout to the recorded commit:

```sh
git -C "$ENVOY_CHECKOUT" fetch origin && git -C "$ENVOY_CHECKOUT" checkout 8492f41012d334a88d321972226af5d33747107c
```

## Regenerate

[`scripts/regen-example-reports.sh`](../../scripts/regen-example-reports.sh) drives
the in-repo systems (envoy, ffx, sheaf-self, fuchsia-coverage, pigweed) from these
checkouts; the FIDL single-library reports and `gh` render via `sheaf report` /
`sheaf scan` against the same pinned checkouts and configs. The rendered HTML is
gitignored here and hosted from the separate `sheaf-data/examples` repo (the
self-scan golden is the one exception — committed at
[`utils/scanner/testdata/sheaf-self.html`](../../utils/scanner/testdata/sheaf-self.html)
for the byte-identical CI test).
