# Envoy xDS coverage example

End-to-end recipe for running sheaf against [envoyproxy/envoy] using the
`protocpp` test-parser adapter. Scopes the scan to the eight v3 xDS
management-plane services (Discovery, Cluster, Listener, Endpoint, Route,
Secret, Runtime, Extension); produces per-library HTML reports; validates
the output against a proto-descriptor ground-truth.

## What you get

- `validate.sh` — one-shot driver: clones (or reuses) an envoy checkout,
  exports the api/ tree via `buf export`, runs the full sheaf pipeline,
  diffs element counts vs. ground truth, walks a spot-check list, and
  prints pass/fail per library to stdout. It does not commit an output
  file.

## Prerequisites

| Tool   | Install                                       | Why                                             |
|--------|-----------------------------------------------|-------------------------------------------------|
| protoc | `brew install protobuf`                       | Proto adapter compiles the .proto inventory.    |
| buf    | `brew install bufbuild/buf/buf`               | Exports envoy api/ with all transitive deps.    |
| python3 | (system)                                     | Ground-truth builder reads the descriptor set.  |
| go     | `brew install go`                             | Builds `sheaf` and the `scanner` HTML renderer. |

## Quick start

From the sheaf repo root:

    git clone --filter=blob:none --depth=1 \
      https://github.com/envoyproxy/envoy.git ~/envoy
    docs/examples/envoy-coverage/validate.sh --repo ~/envoy

The script is idempotent; rerun it whenever you change the config or
the adapter.

## Knobs

    --repo PATH        Path to an envoyproxy/envoy checkout (required).
    --workspace PATH   Where to stage api-export, ground truth, reports.
                       Default: ~/envoy-sheaf-workspace
    --port PORT        MCP server port. Default: 7700
    --skip-reports     Compute ground truth + diff, don't render HTML.

## Layout

    envoy-coverage-config.textproto   # sheaf scan config (sibling of this dir)
    envoy-coverage-rules.textproto    # source map (sibling)
    envoy-coverage/
      README.md                       # this file
      validate.sh                     # driver

[envoyproxy/envoy]: https://github.com/envoyproxy/envoy
