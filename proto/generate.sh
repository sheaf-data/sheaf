#!/usr/bin/env bash
# Regenerate Go bindings from the .proto sources.
# Run from anywhere; this script cds to its own directory first.

set -euo pipefail

cd "$(dirname "$0")"

# Where the generated Go ends up. We use module-relative paths via
# `option go_package` in each .proto file; protoc reads those and
# writes into `../$go_package_relative` so files land under
# proto/{common,contract,...}/*.pb.go.

OUT_ROOT="$(cd .. && pwd)"

# Sanity-check tools.
command -v protoc >/dev/null || { echo "protoc not found"; exit 1; }
command -v protoc-gen-go >/dev/null || { echo "protoc-gen-go not found; install with: go install google.golang.org/protobuf/cmd/protoc-gen-go@latest"; exit 1; }

# All proto sources in this directory.
PROTOS=(*.proto)

echo "Generating Go bindings from ${#PROTOS[@]} proto files…"

protoc \
  --go_out="$OUT_ROOT" \
  --go_opt=module=github.com/sheaf-data/sheaf \
  -I . \
  "${PROTOS[@]}"

echo "Done. Generated files:"
find "$OUT_ROOT/proto" -name '*.pb.go' -type f | sort
