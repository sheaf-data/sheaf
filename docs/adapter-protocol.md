# Sheaf — Runtime adapter protocol

Sheaf ships a fixed set of **in-process** adapters (compiled into the
binary, one Go package per format under `internal/adapters/`). A
**runtime adapter** is an alternative: any executable that speaks the
protocol below over stdio. Sheaf loads it at scan time from your config —
no rebuild of sheaf, no Go, no fork. Write it in any language that can
read and write bytes on stdin/stdout.

This document is the protocol specification. For *why* you'd reach for a
runtime adapter and a copy-pasteable walkthrough, see
[`docs/examples/runtime-adapter/`](examples/runtime-adapter/README.md).
For the in-process (compile-time) path, see
[`docs/examples/sheaf-build-adapter/`](examples/sheaf-build-adapter/README.md).

---

## 1. Model in one paragraph

An adapter is a pure function of a repository: given a checkout path and a
scope, it emits rows — `ContractElement`s (the real surface),
`TestCase`s, or `DocClaim`s (what the docs say). A runtime adapter is that
same function behind a process boundary. The host (sheaf) spawns your
executable **once per `Discover` call**, writes a single `DiscoverRequest`
to its stdin, reads a single `DiscoverResponse` from its stdout, and your
process exits. The rows on the wire are the *exact same protobuf types*
the in-process adapters produce — the schema is the contract, so a runtime
adapter is indistinguishable from a built-in one once its rows land in the
corpus.

The wire types are defined in
[`proto/adapterplugin.proto`](../proto/adapterplugin.proto). The framing,
version handshake, and a ready-made Go serving loop live in
[`internal/adapterplugin`](../internal/adapterplugin).

## 2. Why stdio + protobuf (and not gRPC, or a Go plugin)

- **The output is already protobuf.** Framing the existing
  `ContractElement` / `TestCase` / `DocClaim` messages over a pipe is
  lossless and free; there is no second schema to keep in sync and no
  JSON↔proto drift between the in-process and runtime paths.
- **One-shot, stateless.** An adapter is a pure function of the repo, so
  there is no long-lived server, handshake dance, health check, or
  reconnect logic to get wrong. Spawn, exchange one message pair, exit.
- **Process isolation, for free.** A plugin that crashes, hangs, or runs
  out of memory is a non-fatal per-adapter error, exactly like an
  in-process adapter that returns an error — the scan continues with the
  other adapters.
- **No new dependencies, every platform.** Raw stdio adds nothing to
  sheaf's dependency tree and works on Linux, macOS, and Windows. (Go's
  `plugin` package would constrain you to a matching toolchain and exclude
  Windows; gRPC would drag in a large dependency tree for a call that is
  fundamentally one-shot.)

## 3. Framing

Every message — in both directions — is a single **length-prefixed
frame**:

```
+--------------------+----------------------------------+
| length (4 bytes)   | payload (length bytes)           |
| uint32, big-endian | binary-marshaled protobuf message|
+--------------------+----------------------------------+
```

- The length is the payload size in bytes, unsigned, big-endian.
- The payload is the standard binary protobuf encoding of the message.
- A frame larger than **256 MiB** is rejected on read (guards against a
  corrupt or hostile length prefix).

That is the entire framing. In Go, [`adapterplugin.WriteMessage`
/ `ReadMessage`](../internal/adapterplugin/codec.go) implement it; in any
other language it is "write 4 bytes, write N bytes" and the inverse.

## 4. Lifecycle

For each `Discover` the host invokes against your adapter:

1. The host spawns `command` with any fixed `args`, with stdin and stdout
   as pipes. (stderr is captured and surfaced in error messages — use it
   for human-readable diagnostics.)
2. The host writes one framed `DiscoverRequest` to stdin, then closes it.
3. Your adapter reads that one request, does its work reading files
   directly from `repo_path` on the local filesystem, and writes one
   framed `DiscoverResponse` to stdout.
4. Your adapter exits. A zero exit with a well-formed response is success.

The host bounds each call with a timeout (`timeout_ms`, default 60s). On
timeout or host-side cancellation the process is killed and the adapter
soft-fails.

### 4.1 The info probe

Before (or independently of) a scan, the host may spawn your adapter with
the single argument `--sheaf-adapter-info`. In that mode you write one
framed `PluginInfo` to stdout and exit 0, reading nothing from stdin. This
is used for protocol-version negotiation and for `sheaf doctor`
diagnostics — it lets the host confirm the adapter exists, is runnable,
and speaks a compatible protocol without running a scan. The Go `Serve`
helper handles this flag for you.

## 5. Messages

All fields below are from
[`proto/adapterplugin.proto`](../proto/adapterplugin.proto).

### `DiscoverRequest` (host → plugin)

| Field | Type | Meaning |
|---|---|---|
| `protocol_version` | `uint32` | The version the host speaks. Reject what you don't implement. |
| `role` | `Role` | Which adapter interface is being invoked (see §6). |
| `repo_path` | `string` | Absolute path to the checkout to scan. Empty for `ROLE_RENDERED_REFERENCE`. |
| `scope` | `Scope` | `library` / `also_include` / `exclude` — the libraries to limit discovery to. |
| `config` | `AdapterConfig` | `include` / `exclude` globs and a string→string `option` map. |

### `DiscoverResponse` (plugin → host)

| Field | Type | Meaning |
|---|---|---|
| `protocol_version` | `uint32` | The version you speak. The host checks it. |
| `elements` | `repeated ContractElement` | For contract-anchor / implements-map roles. |
| `tests` | `repeated TestCase` | For the test-parser role. |
| `doc_claims` | `repeated DocClaim` | For doc-parser / rendered-reference roles (and inline docs from a contract anchor). |
| `error` | `string` | Non-empty marks the call failed; the host records it and drops the rows. |
| `warnings` | `repeated string` | Non-fatal diagnostics surfaced to the host. |

Populate only the output field(s) matching the request `role`. A contract
anchor may populate **both** `elements` and `doc_claims` when its source
carries inline documentation (e.g. FIDL `///` comments).

### `PluginInfo` (plugin → host, info probe only)

`protocol_version`, `name`, `version`, and the `roles` you implement.

## 6. Roles

The role tells you which kind of row to emit. It is fixed per configured
adapter by **which config block you appear under** — a plugin listed under
`test_parser` is always invoked with `ROLE_TEST_PARSER`.

| `Role` | Config block | Emit |
|---|---|---|
| `ROLE_CONTRACT_ANCHOR` | `contract_anchor` | `elements` (+ optional inline `doc_claims`) |
| `ROLE_TEST_PARSER` | `test_parser` | `tests` |
| `ROLE_DOC_PARSER` | `doc_parser` | `doc_claims` |
| `ROLE_RENDERED_REFERENCE` | `rendered_reference` | `doc_claims` (bundle paths come from `config.option`) |
| `ROLE_IMPLEMENTS_MAP` | `implements_map` | `elements` (implementing classes) |

## 7. Configuration

You configure a runtime adapter with `name: "external"` inside any adapter
role block, plus an `external { ... }` block. The host projects that block
onto the `AdapterConfig` your plugin receives:

```textproto
test_parser {
  name: "external"
  external {
    command: "sheaf-adapter-gotest"   # PATH-resolved if not absolute
    args: ["--verbose"]               # fixed argv before the protocol
    include: "**/*_test.go"           # → AdapterConfig.include
    exclude: "**/vendor/**"           # → AdapterConfig.exclude
    option { key: "binary_name" value: "docker" }  # → AdapterConfig.option
    timeout_ms: 30000                 # per-call timeout (0 = 60s default)
    name: "gotest"                    # provenance/display name (default: basename of command)
  }
}
```

- `include` / `exclude` are first-class because nearly every adapter takes
  file globs.
- Everything else is a string-keyed `option`. Lists other than
  include/exclude aren't expressible here by design — pass them via `args`
  and parse your own argv.
- `name` is what the row's provenance records. Set it to a stock adapter's
  name (e.g. `gotest`) when wrapping one, and the report reads identically
  to the in-process run.

See [`docs/config.md` §4.20](config.md) for the full field reference.

## 8. Versioning and errors

- **Protocol version.** v1 requires an exact match. If
  `request.protocol_version` is not one you implement, return a response
  with `error` set (don't attempt a best-effort decode). The host applies
  the same rule to your response's `protocol_version`.
- **Soft-fail contract.** A failed runtime adapter never aborts a scan.
  Any of: a missing/un-spawnable command, a non-zero exit, a torn or
  unparseable stdout, a protocol-version mismatch, or a populated
  `error` — is recorded as a per-adapter error on the result and the scan
  continues. Report expected failures via `DiscoverResponse.error` (with a
  clean exit) for the clearest host-side message; captured stderr is
  appended to host errors when a spawn or decode fails.

## 9. Security model

`command` runs **arbitrary code** with the privileges of the sheaf
process. `sheaf.textproto` is therefore trusted input — treat configuring
an external adapter exactly as you would adding a target to a Makefile or a
step to CI. Only point `command` at executables you trust. Sheaf does not
sandbox the plugin; if you need isolation, wrap the command yourself (a
container entrypoint, a restricted user, etc.).

## 10. Known limitations (v1)

- **No build-graph hints.** In-process contract anchors can receive a
  `BuildHints` callback (e.g. Pigweed facade mapping) injected by the
  orchestrator before `Discover`. That is a stateful, bidirectional
  channel with no equivalent in the one-shot stdio model, so runtime
  adapters always run as if hints were absent (`NopHints`). Adapters that
  depend on build-graph hints must stay in-process.
- **One spawn per Discover.** There is no warm/long-lived mode. For the
  current adapter set — each invoked once per scan — this is a non-issue;
  it is a deliberate simplification, not a performance tuning knob.
- **Local filesystem only.** The plugin reads `repo_path` directly; the
  host does not stream file contents over the wire.
