#!/usr/bin/env bash
# Reproduces both demo paths against the synthetic fixture. Run from
# the sheaf-bot-demo/ directory.

set -euo pipefail

DEMO=$(cd "$(dirname "$0")" && pwd)
SHEAF="${SHEAF:-sheaf}"

echo "============================================================"
echo " Path 1: CLI  (sheaf review --post --review file)"
echo "============================================================"
rm -rf "$DEMO/out" && mkdir "$DEMO/out"
"$SHEAF" review \
  --repo "$DEMO/head" \
  --base "$DEMO/base" \
  --pr   "github:example/widgets#42" \
  --post --review file --file-out "$DEMO/out"

echo ""
echo "Comment written to:"
ls "$DEMO/out"
echo ""

echo "============================================================"
echo " Path 2: MCP  (sheaf serve + curl POST /mcp review_pr)"
echo "============================================================"
"$SHEAF" serve --repo "$DEMO/head" --bind 127.0.0.1 --port 17822 \
  > "$DEMO/mcp-stdout.txt" 2>&1 &
SERVER_PID=$!
trap "kill $SERVER_PID 2>/dev/null || true" EXIT
sleep 2

cat <<EOF | curl -s -X POST http://127.0.0.1:17822/mcp -H 'Content-Type: application/json' -d @- \
  | tee "$DEMO/mcp-review-response.json" \
  | python3 -m json.tool
{
  "jsonrpc": "2.0",
  "id":      1,
  "method":  "review_pr",
  "params":  {
    "pr_ref":    "github:example/widgets#44",
    "base_path": "$DEMO/base",
    "head_path": "$DEMO/head",
    "post":      false
  }
}
EOF
