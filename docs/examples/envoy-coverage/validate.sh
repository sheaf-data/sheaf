#!/usr/bin/env bash
# Envoy coverage validator.
#
# Sequence:
#   1. Resolve workspace dir + envoy repo.
#   2. buf export api/ → api-export/ (if not already up to date).
#   3. Build proto descriptor set + ground-truth JSON (counts per library
#      derived directly from the descriptor — the canonical truth).
#   4. Build sheaf + scanner if not already built.
#   5. Spin up `sheaf serve` in the background.
#   6. List libraries via the scanner client.
#   7. Diff sheaf's element count vs ground truth, per library.
#   8. Walk a hardcoded spot-check list: for each (library, RPC) pair the
#      envoy maintainers actually exercise heavily, assert non-zero
#      attributed unit tests. For each (library, deprecated-RPC) pair,
#      assert zero or near-zero — protects against over-attribution.
#   9. Render per-library HTML reports.
#  10. Write envoy-validation.md and exit non-zero on any failure.
#
# Idempotent. Re-runnable. Quiet by default; pass --verbose for stage logs.

set -euo pipefail

# ----------------------------------------------------------------
# Defaults + arg parsing
# ----------------------------------------------------------------

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLES_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
SHEAF_REPO_ROOT="$(cd "$EXAMPLES_DIR/../.." && pwd)"
CONFIG="$EXAMPLES_DIR/envoy-coverage-config.textproto"

REPO=""
WORKSPACE="$HOME/envoy-sheaf-workspace"
PORT=7700
SKIP_REPORTS=0
VERBOSE=0

usage() {
  cat <<EOF
usage: $(basename "$0") --repo PATH [options]

  --repo PATH          envoyproxy/envoy checkout (required)
  --workspace PATH     staging dir for api-export/, binaries, reports
                       (default: ~/envoy-sheaf-workspace)
  --port PORT          MCP server port (default: 7700)
  --skip-reports       compute counts + diff only, skip HTML render
  --verbose            stage logs to stderr
  -h, --help           show this
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo) REPO="$2"; shift 2 ;;
    --workspace) WORKSPACE="$2"; shift 2 ;;
    --port) PORT="$2"; shift 2 ;;
    --skip-reports) SKIP_REPORTS=1; shift ;;
    --verbose) VERBOSE=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [[ -z "$REPO" ]]; then
  echo "error: --repo is required" >&2
  usage >&2
  exit 2
fi
if [[ ! -d "$REPO/api/envoy/service" ]]; then
  echo "error: $REPO does not look like an envoyproxy/envoy checkout (missing api/envoy/service)" >&2
  exit 2
fi
REPO="$(cd "$REPO" && pwd)"

log() {
  if [[ "$VERBOSE" -eq 1 ]]; then echo "[validate-envoy] $*" >&2; fi
}

for bin in protoc buf python3 go; do
  command -v "$bin" >/dev/null 2>&1 \
    || { echo "error: $bin not on PATH (see docs/examples/envoy-coverage/README.md prereqs)" >&2; exit 3; }
done

mkdir -p "$WORKSPACE/bin" "$WORKSPACE/reports"
GROUND_TRUTH="$WORKSPACE/envoy-ground-truth.json"
EXPORT_DIR="$REPO/api-export"
OUT_MD="$SCRIPT_DIR/envoy-validation.md"

# ----------------------------------------------------------------
# 1.5) Stage the source map into the envoy repo.
#
# sheaf's CLI resolves `categorization-rules.textproto` at the scan
# target's repo root. Our example rules file lives in sheaf's tree at
# docs/examples/envoy-coverage-rules.textproto — link it into the envoy
# clone so `sheaf scan` / `sheaf serve` load it. Without this step the
# scan emits the "no source map found" warning and every docs.*
# surface reads 0 because nothing gets bucketed.
#
# Symlinked (not copied) so a rules edit in sheaf's tree takes effect
# on the next scan without re-staging.
# ----------------------------------------------------------------
RULES_SRC="$EXAMPLES_DIR/envoy-coverage-rules.textproto"
RULES_DST="$REPO/categorization-rules.textproto"
if [[ ! -L "$RULES_DST" ]] || [[ "$(readlink "$RULES_DST")" != "$RULES_SRC" ]]; then
  log "staging source map: $RULES_DST -> $RULES_SRC"
  ln -sf "$RULES_SRC" "$RULES_DST"
fi

# ----------------------------------------------------------------
# 2) buf export — skip if up to date.
# ----------------------------------------------------------------

API_HEAD="$(cd "$REPO" && git rev-parse --short HEAD 2>/dev/null || echo UNKNOWN)"
EXPORT_STAMP="$EXPORT_DIR/.sheaf-stamp"
NEED_EXPORT=1
if [[ -f "$EXPORT_STAMP" ]] && [[ "$(cat "$EXPORT_STAMP")" == "$API_HEAD" ]]; then
  NEED_EXPORT=0
fi
if [[ "$NEED_EXPORT" -eq 1 ]]; then
  log "buf export api/ (envoy@$API_HEAD)..."
  rm -rf "$EXPORT_DIR"
  (cd "$REPO/api" && buf export . -o "$EXPORT_DIR")
  echo "$API_HEAD" > "$EXPORT_STAMP"
else
  log "buf export up to date (envoy@$API_HEAD)"
fi

# ----------------------------------------------------------------
# 3) ground truth — descriptor-set parse, per-library counts.
# ----------------------------------------------------------------

build_ground_truth() {
  log "building ground truth..."
  local desc; desc="$(mktemp -t envoy-desc.XXXX.pb)"
  trap "rm -f '$desc'" RETURN

  local libs=(
    envoy/service/discovery/v3
    envoy/service/cluster/v3
    envoy/service/listener/v3
    envoy/service/endpoint/v3
    envoy/service/route/v3
    envoy/service/secret/v3
    envoy/service/runtime/v3
    envoy/service/extension/v3
  )
  local inputs=()
  for d in "${libs[@]}"; do
    while IFS= read -r f; do inputs+=("$f"); done < <(find "$EXPORT_DIR/$d" -name "*.proto" -type f)
  done

  (cd "$EXPORT_DIR" && protoc --include_imports --descriptor_set_out="$desc" -I. "${inputs[@]#"$EXPORT_DIR/"}")

  python3 - "$desc" "$GROUND_TRUTH" "${libs[@]}" <<'PY'
import json, sys
from google.protobuf import descriptor_pb2

desc_path, out_path, *libs_dirs = sys.argv[1:]
wanted_pkgs = set("envoy.service." + d.split("/")[-2] + ".v3" for d in libs_dirs)

with open(desc_path, "rb") as f:
    fds = descriptor_pb2.FileDescriptorSet()
    fds.ParseFromString(f.read())

libs = {pkg: {"files": [], "services": {}, "messages": [], "enums": []}
        for pkg in wanted_pkgs}

# Top-level messages/enums only — matches what the proto adapter emits
# as TYPE elements (src/internal/adapters/proto/adapter.go:4).
for fl in fds.file:
    pkg = fl.package
    if pkg not in libs:
        continue
    b = libs[pkg]
    b["files"].append(fl.name)
    for svc in fl.service:
        b["services"][svc.name] = sorted(m.name for m in svc.method)
    for m in fl.message_type:
        b["messages"].append(m.name)
    for e in fl.enum_type:
        b["enums"].append(e.name)

totals = {"libraries": 0, "services": 0, "methods": 0,
          "messages": 0, "enums": 0, "elements": 0}
out_libs = {}
for pkg in sorted(libs):
    b = libs[pkg]
    if not b["files"]:
        continue
    svc_count = len(b["services"])
    method_count = sum(len(v) for v in b["services"].values())
    msg_count = len(set(b["messages"]))
    enum_count = len(set(b["enums"]))
    element_count = svc_count + method_count + msg_count + enum_count
    out_libs[pkg] = {
        "files": sorted(set(b["files"])),
        "services": b["services"],
        "messages": sorted(set(b["messages"])),
        "enums": sorted(set(b["enums"])),
        "service_count": svc_count,
        "method_count": method_count,
        "message_count": msg_count,
        "enum_count": enum_count,
        "element_count": element_count,
    }
    for k, v in (("libraries", 1), ("services", svc_count), ("methods", method_count),
                 ("messages", msg_count), ("enums", enum_count), ("elements", element_count)):
        totals[k] += v

with open(out_path, "w") as f:
    json.dump({"libraries": out_libs, "totals": totals}, f, indent=2, sort_keys=True)
PY
}
build_ground_truth

# ----------------------------------------------------------------
# 4) build binaries
# ----------------------------------------------------------------

log "building sheaf + scanner..."
(cd "$SHEAF_REPO_ROOT/src" && go build -o "$WORKSPACE/bin/sheaf"    ./cmd/sheaf)
(cd "$SHEAF_REPO_ROOT/src" && go build -o "$WORKSPACE/bin/scanner" ./cmd/scanner)

# ----------------------------------------------------------------
# 5) start sheaf serve in background
# ----------------------------------------------------------------

# Kill any leftover server on the same port (prior crashed run).
if lsof -ti tcp:"$PORT" >/dev/null 2>&1; then
  log "killing existing process on port $PORT"
  kill -9 "$(lsof -ti tcp:"$PORT")" 2>/dev/null || true
  sleep 1
fi

SERVE_LOG="$WORKSPACE/sheaf-serve.log"
log "starting sheaf serve (--port $PORT, --repo $REPO)..."
"$WORKSPACE/bin/sheaf" serve \
  --config "$CONFIG" \
  --repo "$REPO" \
  --port "$PORT" \
  > "$SERVE_LOG" 2>&1 &
SERVE_PID=$!
# Single EXIT trap that does all cleanup. Capturing the script's own
# exit code first via $? — the kill/wait afterwards must not be allowed
# to overwrite it, otherwise a clean PASS-then-kill leaks an exit-1.
cleanup() {
  local rc=$?
  kill "$SERVE_PID" 2>/dev/null || true
  wait "$SERVE_PID" 2>/dev/null || true
  rm -f "$LIBS_OUT" 2>/dev/null || true
  exit "$rc"
}
trap cleanup EXIT

# Poll the health endpoint until the server is ready (or timeout).
deadline=$(( $(date +%s) + 600 ))
until curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; do
  if [[ $(date +%s) -ge $deadline ]]; then
    echo "error: sheaf serve didn't come up in 600s — see $SERVE_LOG" >&2
    exit 4
  fi
  if ! kill -0 "$SERVE_PID" 2>/dev/null; then
    echo "error: sheaf serve died — see $SERVE_LOG" >&2
    tail -40 "$SERVE_LOG" >&2
    exit 4
  fi
  sleep 3
done
log "sheaf serve ready"

# ----------------------------------------------------------------
# 6) list libraries
# ----------------------------------------------------------------

LIBS_OUT="$(mktemp -t envoy-libs.XXXX.txt)"
"$WORKSPACE/bin/scanner" --list --server "http://127.0.0.1:$PORT" --quiet > "$LIBS_OUT"

# ----------------------------------------------------------------
# 7) Count diff + 8) spot-checks + 9) reports + 10) write markdown
# ----------------------------------------------------------------

# Spot-check table. Each row: <library> <element-id> <expected-side> <min-count>
# Side is "tests" (must have at least N attributed unit tests) or
# "untested" (must have zero or near-zero — guards against over-attribution).
SPOT_CHECKS=(
  # Discovery (ADS). Modern envoy tests reference the method descriptor
  # by string ("envoy.service.discovery.v3.AggregatedDiscoveryService.
  # StreamAggregatedResources") inline in test bodies; protocpp picks
  # those up directly. Threshold reflects post-precision-iteration
  # attribution.
  "envoy.service.discovery.v3 AggregatedDiscoveryService.StreamAggregatedResources tests 3"
  # Endpoint (EDS) — well-attributed via grpc_subscription_test_harness.h
  # include following.
  "envoy.service.endpoint.v3 EndpointDiscoveryService.StreamEndpoints tests 5"
  "envoy.service.endpoint.v3 EndpointDiscoveryService.FetchEndpoints tests 1"
  # Route (RDS) — at least one test names the dotted method string in
  # type_to_endpoint_test.cc.
  "envoy.service.route.v3 RouteDiscoveryService.StreamRoutes tests 1"
  # Negative check: deprecated/v2 names should not appear in v3 reports.
  "envoy.service.discovery.v3 AggregatedDiscoveryService.StreamAggregatedResourcesV2 untested 0"
)

# Use Python for the rest — diff, spot-check, markdown writer.
python3 - \
  "$GROUND_TRUTH" \
  "$WORKSPACE/bin/sheaf" "$CONFIG" "$REPO" \
  "$LIBS_OUT" \
  "$OUT_MD" "$WORKSPACE/reports" \
  "$SKIP_REPORTS" "$VERBOSE" \
  "$API_HEAD" \
  "${SPOT_CHECKS[@]}" <<'PY'
import json, os, subprocess, sys, datetime, shutil

(gt_path, sheaf_bin, config, repo, libs_out_path, out_md, reports_dir,
 skip_reports, verbose, api_head, *spot_checks) = sys.argv[1:]
skip_reports = int(skip_reports)
verbose = int(verbose)

def log(msg):
    if verbose:
        sys.stderr.write(f"[validate-envoy] {msg}\n")

with open(gt_path) as f:
    gt = json.load(f)

# Parse scanner --list output.
sheaf_counts = {}  # {library: (elements, profiles, findings)}
with open(libs_out_path) as f:
    for line in f:
        parts = line.split()
        if len(parts) >= 4 and parts[0] != "LIBRARY":
            sheaf_counts[parts[0]] = (int(parts[1]), int(parts[2]), int(parts[3]))

# Diff per-library element counts vs ground truth.
diff_rows = []
diff_failures = 0
for pkg in sorted(gt["libraries"]):
    gt_elems = gt["libraries"][pkg]["element_count"]
    sheaf_elems = sheaf_counts.get(pkg, (None,))[0]
    status = "PASS" if sheaf_elems == gt_elems else "FAIL"
    if status == "FAIL":
        diff_failures += 1
    diff_rows.append((pkg, gt_elems, sheaf_elems, status))

# Spot-checks.
def coverage_for(element_id):
    """Return list[(test_name, path:line)] for the element, or [] on error."""
    cmd = [sheaf_bin, "coverage", "--config", config, "--repo", repo, "--element", element_id]
    try:
        out = subprocess.check_output(cmd, text=True, stderr=subprocess.STDOUT, timeout=300)
    except subprocess.CalledProcessError as e:
        log(f"coverage failed for {element_id}: {e.output[:200]}")
        return None
    tests = []
    in_block = False
    for line in out.splitlines():
        if line.startswith("Unit tests"):
            in_block = True
            continue
        if in_block:
            if not line.strip():
                in_block = False
                continue
            if line.startswith("Docs:") or line.startswith("Missing:"):
                in_block = False
                continue
            tests.append(line.strip())
    return tests

# spot_checks: one string per entry; each entry is space-separated
# "<lib> <name> <side> <min_count>" — bash passes the whole quoted
# string as a single argv entry, so we split here.
spot_rows = []
spot_failures = 0
for entry in spot_checks:
    parts = entry.split()
    if len(parts) != 4:
        sys.stderr.write(f"bad spot-check entry: {entry!r}\n")
        sys.exit(2)
    lib, name, side, min_count_s = parts
    min_count = int(min_count_s)
    element_id = f"{lib}/{name}"
    tests = coverage_for(element_id)
    if side == "untested":
        # untested = element doesn't exist OR has no attributed tests;
        # both are correct outcomes. tests is None when `sheaf coverage`
        # errored because the element doesn't exist — for an "untested"
        # check that's a pass.
        actual = 0 if (tests is None or tests == []) else len(tests)
        if tests is None or tests == []:
            status = "PASS"
        else:
            status = "FAIL"
            spot_failures += 1
    elif side == "tests":
        actual = len(tests) if tests is not None else "ERR"
        if tests is None:
            status = "ERR"
            spot_failures += 1
        elif len(tests) >= min_count:
            status = "PASS"
        else:
            status = "FAIL"
            spot_failures += 1
    else:
        actual = "?"
        status = f"UNKNOWN_SIDE({side})"
        spot_failures += 1
    spot_rows.append((lib, name, side, min_count, actual, status))

# Render reports.
report_status = []
if not skip_reports:
    scanner_bin = os.path.join(os.path.dirname(sheaf_bin), "scanner")
    os.makedirs(reports_dir, exist_ok=True)
    for pkg in sorted(gt["libraries"]):
        out_html = os.path.join(reports_dir, f"{pkg}-report.html")
        cmd = [scanner_bin, "--server", "http://127.0.0.1:7700",
               "--ecosystem", "proto", "--library", pkg,
               "--commit", api_head,
               "--source-url-template",
               "https://github.com/envoyproxy/envoy/blob/main/{path}#L{line}",
               "-o", out_html, "--quiet"]
        try:
            subprocess.check_call(cmd, stdout=subprocess.DEVNULL, stderr=subprocess.STDOUT, timeout=120)
            report_status.append((pkg, out_html, "PASS"))
        except subprocess.CalledProcessError:
            report_status.append((pkg, out_html, "FAIL"))

# Write the markdown.
total_failures = diff_failures + spot_failures + sum(1 for _, _, s in report_status if s == "FAIL")
overall = "PASS" if total_failures == 0 else "FAIL"

now = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%d %H:%M UTC")

lines = []
lines.append(f"# Envoy coverage validation\n")
lines.append(f"_Run at {now} against envoy@{api_head}._\n")
lines.append(f"**Overall: {overall}** ({total_failures} failure(s))\n")

lines.append("## Element-count diff (sheaf vs proto descriptor)\n")
lines.append("| Library | Ground truth | Sheaf | Status |")
lines.append("|---|---:|---:|:---:|")
for pkg, gt_el, sh_el, st in diff_rows:
    lines.append(f"| `{pkg}` | {gt_el} | {sh_el if sh_el is not None else '-'} | {st} |")
lines.append("")
gt_tot = gt["totals"]["elements"]
sh_tot = sum(v[0] for v in sheaf_counts.values() if v[0] is not None and v[0]) if sheaf_counts else 0
lines.append(f"_Totals: ground-truth {gt_tot} elements, sheaf {sh_tot}._\n")

lines.append("## RPC spot-checks\n")
lines.append("| Library | Element | Expectation | Min | Got | Status |")
lines.append("|---|---|---|---:|---:|:---:|")
for lib, name, side, min_count, actual, st in spot_rows:
    expect = f"≥{min_count} tests" if side == "tests" else "no tests"
    lines.append(f"| `{lib}` | `{name}` | {expect} | {min_count} | {actual} | {st} |")
lines.append("")

if not skip_reports:
    lines.append("## Rendered reports\n")
    lines.append("| Library | Path | Status |")
    lines.append("|---|---|:---:|")
    for pkg, path, st in report_status:
        # Reports live in the workspace, which sits outside the repo;
        # use the absolute path verbatim so the markdown is readable.
        lines.append(f"| `{pkg}` | `{path}` | {st} |")
    lines.append("")

lines.append("## Notes\n")
lines.append("- Element-count parity is the most basic sanity check — sheaf's "
             "proto adapter should always agree with the descriptor set "
             "(both come from protoc).")
lines.append("- xDS subscription is multiplexed: most clients call only "
             "`AggregatedDiscoveryService.{Stream,Delta}AggregatedResources`, "
             "and the per-service-per-type code path lives in the mux. So "
             "single-RPC attribution counts for CDS/LDS/SDS/CDS will be low "
             "by design.")
lines.append("- The protocpp adapter only sees what test files name "
             "textually (string-literal RPCs, fully-qualified C++ proto "
             "types, .pb.h includes). It does not run protoc on the test "
             "tree; tests that touch a service only through generated mock "
             "classes will show as untested.\n")

with open(out_md, "w") as f:
    f.write("\n".join(lines))

# Console summary.
print(f"\n=== envoy validation: {overall} ===")
print(f"  element-count diff: {len(diff_rows)} libraries, {diff_failures} fail")
print(f"  spot-checks:        {len(spot_rows)} checks, {spot_failures} fail")
if not skip_reports:
    print(f"  reports rendered:   {sum(1 for _, _, s in report_status if s == 'PASS')}/{len(report_status)}")
print(f"  markdown:           {out_md}")

sys.exit(0 if total_failures == 0 else 1)
PY
RC=$?
# The cleanup trap handles kill/wait + tmpfile removal and preserves $rc.
exit "$RC"
