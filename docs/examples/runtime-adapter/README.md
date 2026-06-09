# Example: load an adapter at runtime (the `gotest` plugin)

This walks through sheaf's **runtime adapter** feature end to end by taking
a stock in-process adapter — `gotest` — and running it as an
out-of-process plugin that sheaf loads at scan time, with no rebuild of
sheaf. The same parser, run either way, emits byte-identical rows.

- **Protocol spec:** [`docs/adapter-protocol.md`](../../adapter-protocol.md)
- **In-process (compile-time) adapter authoring:** [`../sheaf-build-adapter/`](../sheaf-build-adapter/README.md)

## When you'd reach for this

A runtime adapter is the right tool when you want to add a parser **without
forking or rebuilding sheaf** — for example to:

- parse a format sheaf doesn't ship an adapter for, kept in your own repo;
- write the adapter in a language other than Go (anything that can read and
  write bytes on stdin/stdout);
- iterate on an adapter independently of sheaf's release cadence.

The trade versus a built-in adapter: a runtime adapter pays one process
spawn per `Discover`, and it can't receive `BuildHints` (the build-graph
callback). In exchange you get language-independence, process isolation (a
crash is a soft per-adapter error, not a dead scan), and no sheaf rebuild.

## 1. The adapter is the stock parser, wrapped

The entire plugin is [`cmd/sheaf-adapter-gotest/main.go`](../../../cmd/sheaf-adapter-gotest/main.go).
It imports the unchanged `internal/adapters/gotest` parser and hands it to
the serving helper:

```go
func main() {
    info := adapterplugin.Info{
        Name:    gotest.Name,
        Version: gotest.Version,
        Roles:   []pluginpb.Role{pluginpb.Role_ROLE_TEST_PARSER},
    }
    adapterplugin.Serve(info, handle)
}

func handle(ctx context.Context, req *pluginpb.DiscoverRequest) (*pluginpb.DiscoverResponse, error) {
    cfg := gotest.Config{
        Include:    req.GetConfig().GetInclude(),
        Exclude:    req.GetConfig().GetExclude(),
        BinaryName: req.GetConfig().GetOption()["binary_name"],
    }
    tests, err := gotest.New(cfg).Discover(ctx, req.GetRepoPath(), adapterplugin.ScopeFromProto(req.GetScope()))
    if err != nil {
        return nil, err
    }
    return &pluginpb.DiscoverResponse{Tests: tests}, nil
}
```

`adapterplugin.Serve` handles the framing, the `--sheaf-adapter-info`
handshake probe, protocol-version checks, and panic recovery. Your code is
just "read config, parse, return rows." In another language you'd
implement the same shape against
[`proto/adapterplugin.proto`](../../../proto/adapterplugin.proto) — see the
[protocol spec](../../adapter-protocol.md) §3–§5.

## 2. Build the plugin

```bash
go build -o ./sheaf-adapter-gotest ./cmd/sheaf-adapter-gotest
```

Put the binary anywhere; the config points at it. (On `PATH` you can refer
to it by bare name.)

## 3. The config change

Selecting a runtime adapter is a one-block edit. The in-process form:

```textproto
test_parser {
  name: "gotest"
  gotest { include: "internal/adapters/gotest/**/*_test.go" }
}
```

becomes the runtime form ([`sheaf.textproto`](sheaf.textproto) in this
directory):

```textproto
test_parser {
  name: "external"
  external {
    command: "./sheaf-adapter-gotest"
    include: "internal/adapters/gotest/**/*_test.go"
    name: "gotest"            # provenance reads identically to the built-in
  }
}
```

`include`/`exclude` map straight through. Any scalar knob the built-in took
(here `gotest`'s `binary_name`) moves into `option { key: ... value: ... }`.

## 4. Preflight with `sheaf doctor`

`sheaf doctor` probes each configured adapter before any scan. For a
runtime adapter it spawns the plugin's `--sheaf-adapter-info` handshake, so
`[OK]` means the executable was found and speaks a compatible protocol —
catch a wrong `command` path here rather than mid-scan:

```console
$ sheaf doctor --config docs/examples/runtime-adapter/sheaf.textproto --repo .
Adapters:
  gotest               [OK]
```

A missing or incompatible plugin reports `[FAIL: …]` with the reason.

## 5. Run a scan

```bash
go build -o ./sheaf ./cmd/sheaf
./sheaf scan --config docs/examples/runtime-adapter/sheaf.textproto --repo .
```

The per-adapter sample block lists the discovered Go tests, provenance
sourced to `gotest` — the same output you'd get from the built-in adapter,
now produced by a separate process sheaf spawned over stdio.

## 6. Prove the rows are identical

The repository ships an equivalence test that runs the in-process `gotest`
and the plugin against the same fixture and asserts the `TestCase`s are
byte-identical (`proto.Equal`):

```bash
go test ./internal/adapters/external/ -run TestEquivalence -v
```

If the wire protocol ever became lossy, this test fails. That is the
guarantee that makes a runtime adapter a drop-in for a built-in one.

## 7. Writing your own

1. Implement the [protocol](../../adapter-protocol.md): read one
   length-prefixed `DiscoverRequest` from stdin, emit the rows for your
   role, write one length-prefixed `DiscoverResponse` to stdout, and answer
   the `--sheaf-adapter-info` probe. In Go, `adapterplugin.Serve` does all
   of this for you.
2. Pick the role by the config block you put `name: "external"` under
   (`test_parser`, `doc_parser`, `contract_anchor`,
   `rendered_reference`, or `implements_map`).
3. Point `command` at your executable and scan.

> **Security.** `command` runs arbitrary code with sheaf's privileges.
> `sheaf.textproto` is trusted input — only configure external adapters you
> trust, exactly as you would a Makefile target.
