# sheaf version

Print the linked build version and exit.

## Synopsis

```text
sheaf version
```

## Description

`sheaf version` writes the build version (set via `-ldflags '-X github.com/sheaf-data/sheaf/internal/cli.BuildVersion=...'` at link time, defaulting to `0.1.0-dev`) to stdout and exits cleanly.

The command takes no flags and runs no pipeline. Use it as a smoke check that the binary is on `$PATH` and runnable, and to record the exact build that produced a report.

## Example

```sh
sheaf version
# sheaf 0.1.0-dev
```

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Always. |
