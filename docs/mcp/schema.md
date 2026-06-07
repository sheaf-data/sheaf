# Sheaf MCP schema

The MCP server's result payloads are protobuf messages serialised with `protojson` (camelCase field names, `EmitUnpopulated: false`). This page indexes the messages — their canonical definition lives next to the source.

## Canonical sources

| File | Defines |
|---|---|
| [proto/common.proto](../../proto/common.proto)              | `SourceLocation`, `Provenance`, `DocRef`, `TestRef`, `CodeRef`, `UsageAggregate`, `Substance` enum, `Severity` enum. |
| [proto/contract.proto](../../proto/contract.proto)          | `ContractElement`, `Relationship`, `Parameter`, `TypeRef`, `VersionConstraint`, `ContractElementKind`, `RelationshipKind`. |
| [proto/coverage_profile.proto](../../proto/coverage_profile.proto) | `CoverageProfile`, `DocCoverage`, `ExampleCoverage`, `TestCoverage`, `UsageCoverage`, `GapsSummary`, `GapNote`. |
| [proto/finding.proto](../../proto/finding.proto)            | `Finding`, `FindingKind`, `EvidencePointer`. |
| [proto/doc_claim.proto](../../proto/doc_claim.proto)        | `DocClaim`, `DocClaimKind`. (Intermediate ingest type; not part of the MCP wire shape, but the analyzers reference it.) |
| [proto/test_case.proto](../../proto/test_case.proto)        | `TestCase`. (Same intermediate-only note as `DocClaim`.) |
| [proto/categorization.proto](../../proto/categorization.proto) | `Rules`, `Category`, `Ownership`. (Configures the categorizer; not directly exposed via MCP.) |
| [proto/config.proto](../../proto/config.proto)              | `Config` and every nested block (`MCPServerConfig`, `AuthConfig`, …). Defines what `sheaf.textproto` accepts. |

## Wire encoding

| Concern | Behaviour |
|---|---|
| Field naming | `protojson` lower-camel-case (`elementId`, not `element_id`). |
| Enums | Encoded as their declared string names (`"METHOD"`, `"WARNING"`, `"SUBSTANTIVE"`). |
| Default values | Suppressed (`EmitUnpopulated: false`). Absent fields = zero value. |
| Map fields | JSON objects keyed by string. |
| Timestamps | RFC 3339 strings. |
| Bytes | Base64. (Not used by any current MCP payload.) |

## `ContractElement` — the central addressable unit

```proto
message ContractElement {
  string id = 1;                          // canonical ID, ecosystem-specific format
  ContractElementKind kind = 2;
  string ecosystem = 3;                   // "fidl" | "cobra" | "argh" | "proto" | "cml" | ...
  string library = 4;
  sheaf.common.v1.SourceLocation location = 5;
  repeated Parameter parameters = 6;
  repeated TypeRef return_types = 7;
  string doc_comment_excerpt = 8;
  repeated VersionConstraint version_constraints = 9;
  repeated Relationship relationships = 10;
  google.protobuf.Struct ecosystem_meta = 11;
  repeated string aliases = 12;
}
```

ID conventions vary by ecosystem:

| Ecosystem | ID shape | Example |
|---|---|---|
| FIDL      | `<library>/<Protocol>.<Method>` (methods) or `<library>/<Type>` (types) | `fuchsia.io/Directory.Open` |
| proto     | `<package>/<Service>.<Method>` (methods) or `<package>/<Message>` | `grpc.health.v1/Health.Check` |
| cobra     | `<binary> <sub> <sub>` (subcommands) or `<binary> <sub> --<flag>` (flags) | `kubectl get`, `kubectl get --selector` |
| argh      | `<binary> <sub>` / `<binary> <sub> --<flag>` | `ffx component show` |
| cml       | `<component>/<capability>` | `fuchsia-pkg://.../my.cm/svc.Server` |

`aliases` is populated by the cobra adapter's dedup pass when the same command lives at multiple names (e.g. `docker container run` ≡ `docker run`).

### `ContractElementKind`

```
KIND_UNSPECIFIED, METHOD, FLAG, SUBCOMMAND, TYPE, SYSCALL, POSITIONAL,
SWITCH, PROTOCOL, CPP_CLASS, RUST_TYPE, LIBRARY, CONFIG_KNOB, CONFIG_FACET
```

The scanner's two-tier UI buckets primary vs. modifier kinds from this enum:

- **Primary**: `SUBCOMMAND`, `METHOD`, `PROTOCOL`, `TYPE`, `SYSCALL`, `CONFIG_KNOB`.
- **Modifier**: `FLAG`, `SWITCH`, `POSITIONAL`.

## `CoverageProfile` — the per-element coverage matrix

```proto
message CoverageProfile {
  string element_id = 1;
  Provenance provenance = 2;
  DocCoverage docs = 3;
  ExampleCoverage examples = 4;
  TestCoverage tests = 5;
  UsageCoverage usage = 6;
  GapsSummary gaps_summary = 7;
}
```

Buckets:

- **`docs.reference`** — adapter-attributed reference docs. The typed fields (`fidldoc`, `clidoc`, `dockerdoc`) are kept for backwards compatibility; new adapters route refs through `docs.reference.by_adapter[<adapter-name>].refs`.
- **`docs.concept`** / **`docs.tutorial`** / **`docs.release_notes`** / **`docs.faq`** — markdown-attributed doc claims by kind.
- **`docs.guide`** — `migration`, `troubleshooting`, `cookbook` sub-buckets.
- **`docs.proposal`** — `rfc`, `design` sub-buckets.
- **`examples.in_tree`** / **`examples.in_docs`** / **`examples.external`** — `CodeRef`s.
- **`tests.unit`** / **`integration`** / **`e2e`** / **`ctf`** / **`performance`** / **`fuzz`** / **`golden`** — `TestRef`s by test kind.
- **`usage.internal`** / **`usage.sdk_consumer`** / **`usage.bindings`** — usage aggregates.
- **`gaps_summary.missing`** / **`thin`** / **`notable`** — analyzer-attached notes.

All `repeated` fields default to empty (`EmitUnpopulated: false` strips them from the wire payload), so consumers should treat absent fields as length-zero.

## `Finding` — analyzer output

```proto
message Finding {
  string id = 1;
  FindingKind kind = 2;
  string subject = 3;
  Severity severity = 4;
  repeated EvidencePointer evidence = 5;
  Provenance provenance = 6;
  Timestamp created_at = 7;
  string analyzer = 8;
  string message = 9;
}
```

### `FindingKind`

| Kind | Meaning |
|---|---|
| `DOCUMENTED_UNTESTED`    | An element has docs but no attributed tests. |
| `TESTED_UNDOCUMENTED`    | An element has tests but no attributed docs. |
| `MISSING_IN_CATEGORY`    | A category declared in the project's source map (`categorization-rules.textproto`) is empty for this element. |
| `THIN_REFERENCE`         | A reference doc was found but its prose grades `SIGNATURE_ONLY`. |
| `EXTERNAL_MENTION_ONLY`  | The only attribution is from a doc outside the configured repo / scope. |
| `COVERAGE_DELTA`         | A PR-review-time finding: this element's coverage changed between base and head. |
| `STALE_DOC`              | A doc claim points at an element whose signature has shifted since the doc was last edited. |

### `Severity`

`SEVERITY_UNSPECIFIED, INFO, WARNING, ERROR` — see `proto/common.proto`. The CLI flag `--severity` accepts the bare token (`INFO`, `WARN`/`WARNING`, `ERROR`).

## Shared reference types

### `SourceLocation`

```proto
message SourceLocation {
  string path = 1;
  uint32 line = 2;
  uint32 column = 3;
  string url = 4;
}
```

`path` is always repo-relative with forward slashes. `url` is the canonical clickable URL when the producing adapter has one (e.g. `markdowncli` with `url_base`); otherwise renderers fall back to expanding their `--source-url-template` against `{path}` and `{line}`.

### `DocRef`

```proto
message DocRef {
  string path = 1;
  uint32 line = 2;
  string url = 3;
  Substance substance = 4;     // ABSENT | SIGNATURE_ONLY | PARTIAL | SUBSTANTIVE
  uint32 words = 5;
  string adapter = 6;          // adapter that produced this ref
}
```

The four-step `Substance` scale is the basis for the "% substantive" KPI the scanner renders.

### `TestRef`

```proto
message TestRef {
  string path = 1;
  uint32 line = 2;
  string test_name = 3;        // canonical test ID (e.g. "FooTest.Bar", "src/foo/bar_test::TestX")
  string exercises = 4;        // contract element ID
  string framework = 5;        // "gotest" | "gtest" | "rust-test" | "bats" | ...
}
```

### `CodeRef`

```proto
message CodeRef {
  string path = 1;
  uint32 start_line = 2;
  uint32 end_line = 3;
  string intent = 4;           // free-form: "tutorial step", "doc code block", ...
}
```

### `Provenance`

```proto
message Provenance {
  string commit_hash = 1;
  Timestamp generated_at = 2;
  string llm_call_id = 3;
  map<string, string> adapter_versions = 4;
  string scan_id = 5;
}
```

Every `CoverageProfile` and `Finding` carries one. `adapter_versions` is the map of every adapter that contributed, keyed by adapter `Name()`.

## Compatibility

The v1 schema is **schema-stable**: future changes are additive — new optional fields, new enum values, new map entries. Removing a field or renaming an existing one would be a v2 break.

When the server adds an enum value the client doesn't recognise, `protojson` decodes it as the string name; clients should treat unknown values as `*_UNSPECIFIED` rather than crashing.

## Browsing the schema

Generated Go bindings live alongside each `.proto`:

| Proto | Generated |
|---|---|
| `proto/common.proto`            | [`proto/common/common.pb.go`](../../proto/common/common.pb.go) |
| `proto/contract.proto`          | [`proto/contract/contract.pb.go`](../../proto/contract/contract.pb.go) |
| `proto/coverage_profile.proto`  | [`proto/coverage/coverage_profile.pb.go`](../../proto/coverage/coverage_profile.pb.go) |
| `proto/finding.proto`           | [`proto/finding/finding.pb.go`](../../proto/finding/finding.pb.go) |
| `proto/config.proto`            | [`proto/config/config.pb.go`](../../proto/config/config.pb.go) |

To regenerate after editing a `.proto`, run the checked-in script from the
repo root (it `cd`s to `proto/` itself, so it works from anywhere):

```sh
proto/generate.sh
```

The script shells out to `protoc` with the module-relative invocation below;
run it directly from the `proto/` directory if you'd rather not use the script:

```sh
cd proto
protoc --go_out=.. --go_opt=module=github.com/sheaf-data/sheaf \
       -I . \
       *.proto
```

Both require `protoc` and `protoc-gen-go` on your `PATH` (see
[CONTRIBUTING.md](../../CONTRIBUTING.md#regenerating-proto-bindings)). The
`go_package` option in each `.proto` routes the generated `*.pb.go` files back
under `proto/{common,contract,…}/`.

## See also

- [api.md](api.md) — the operations that return these messages.
- [docs/config.md](../config.md) — `sheaf.textproto` schema.
