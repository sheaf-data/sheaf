#!/usr/bin/env bash
# Cross-check the example-gallery reports against committed golden fingerprints.
#
# For each report, sheaf scans its pinned upstream checkout and emits a coverage
# fingerprint — `sheaf report --format csv`, one row per contract element with its
# tests / docs / examples counts and the missing-coverage set. That CSV is
# deterministic and byte-stable (no timestamps, stable ordering), so a plain diff
# against the committed golden flags any change in sheaf's ANALYSIS (scanner,
# adapters, analyzers, categorization) that would alter a published report's
# findings.
#
# Render-layer regressions are covered separately and per-PR by the byte-identical
# sheaf-self HTML golden (utils/scanner/inprocess_test.go) plus the renderer unit
# tests, so this script deliberately fingerprints the analysis, not the HTML.
#
# Usage:
#   scripts/check-example-reports.sh                 # verify all vs committed goldens
#   scripts/check-example-reports.sh envoy fuchsia-io # verify only the named systems
#   scripts/check-example-reports.sh --update [sys…] # (re)write goldens instead of diffing
#
# The default set is the PINNED-UPSTREAM external reports: their source is frozen
# at a recorded commit, so any fingerprint change means SHEAF's analysis changed —
# exactly the drift this guards. Two reports are deliberately excluded (each is
# still selectable by name for debugging):
#   sheaf       — the self-scan tracks sheaf's OWN evolving source, so its
#                 fingerprint churns with every change. It's already guarded
#                 per-PR by TestRender_SelfScanByteIdentical (utils/scanner).
#   gh (cli/cli)— contract surface isn't rendered yet (docs/examples/
#                 REPRODUCIBILITY.md), so a scan yields zero elements.
#
# Checkout locations (override via env; defaults match regen-example-reports.sh):
#   ENVOY_CHECKOUT    default /Volumes/T7/envoy        (github.com/envoyproxy/envoy)
#   PIGWEED_CHECKOUT  default /Volumes/T7/pigweed      (pigweed.googlesource.com/pigweed)
#   FUCHSIA_CHECKOUT  default /Volumes/T7/fuchsia      (fuchsia.googlesource.com/fuchsia)
#   FFX_CHECKOUT      default $FUCHSIA_CHECKOUT        (ffx ships inside the Fuchsia tree)
#
# A system whose checkout is absent is SKIPPED (reported), not failed — so the
# weekly CI job can run the upstreams it can clone and leave the rest to a later
# phase. Pin SHAs live in docs/examples/REPRODUCIBILITY.md.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

ENVOY_CHECKOUT="${ENVOY_CHECKOUT:-/Volumes/T7/envoy}"
PIGWEED_CHECKOUT="${PIGWEED_CHECKOUT:-/Volumes/T7/pigweed}"
FUCHSIA_CHECKOUT="${FUCHSIA_CHECKOUT:-/Volumes/T7/fuchsia}"
FFX_CHECKOUT="${FFX_CHECKOUT:-$FUCHSIA_CHECKOUT}"

GOLDEN_DIR="$REPO_ROOT/testdata/example-reports"
EX="$REPO_ROOT/docs/examples"
mkdir -p "$GOLDEN_DIR"

UPDATE=0
SYSTEMS=()
for arg in "$@"; do
  case "$arg" in
    --update) UPDATE=1 ;;
    -*) echo "unknown flag: $arg" >&2; exit 2 ;;
    *) SYSTEMS+=("$arg") ;;
  esac
done

# Default = pinned-upstream external reports only. sheaf / gh are excluded
# (see header) but remain selectable by name, e.g. for `--update`.
ALL_SYSTEMS=(envoy pigweed-pw_rpc pigweed-pw_log pigweed-pw_transfer fuchsia-io ffx)
if [[ ${#SYSTEMS[@]} -eq 0 ]]; then
  SYSTEMS=("${ALL_SYSTEMS[@]}")
fi

# Build sheaf once unless the caller supplied a binary via $SHEAF.
SHEAF="${SHEAF:-}"
CLEAN_SHEAF=0
if [[ -z "$SHEAF" ]]; then
  SHEAF="$REPO_ROOT/.sheaf-check-bin"
  echo "==> building sheaf ..."
  go build -o "$SHEAF" "$REPO_ROOT/cmd/sheaf" || { echo "build failed" >&2; exit 1; }
  CLEAN_SHEAF=1
fi

PASS=() FAIL=() SKIP=()

# fingerprint <name> <checkout> <rules> <config>  → writes $TMP_CSV; returns nonzero on scan failure.
TMP_CSV=""
fingerprint() {
  local name="$1" checkout="$2" rules="$3" config="$4"
  TMP_CSV="$(mktemp -t "sheaf-$name.XXXXXX")"
  # Stage the system's categorization rules at the checkout root, where the scan
  # looks for them (same convention as regen-example-reports.sh). Don't clobber a
  # pre-existing file.
  local staged="" staged_rules=0
  if [[ -n "$rules" ]]; then
    staged="$checkout/categorization-rules.textproto"
    if [[ -e "$staged" ]]; then
      echo "[$name] $staged already present — not clobbering; skipping" >&2
      return 3
    fi
    cp "$rules" "$staged" && staged_rules=1
  fi
  "$SHEAF" report --format csv --config "$config" --repo "$checkout" >"$TMP_CSV" 2>/dev/null
  local rc=$?
  [[ $staged_rules -eq 1 ]] && rm -f "$staged"
  return $rc
}

# verify_or_update <name>  (TMP_CSV already populated)
verify_or_update() {
  local name="$1" golden="$GOLDEN_DIR/$1.csv"
  if [[ $UPDATE -eq 1 ]]; then
    cp "$TMP_CSV" "$golden"
    echo "[$name] golden updated ($(wc -l <"$golden" | tr -d ' ') rows)"
    PASS+=("$name")
  elif [[ ! -f "$golden" ]]; then
    echo "[$name] NO GOLDEN at $golden — run with --update" >&2
    FAIL+=("$name (no golden)")
  elif diff -u "$golden" "$TMP_CSV" >"/tmp/$name.diff" 2>&1; then
    echo "[$name] ok"
    PASS+=("$name")
  else
    echo "[$name] DRIFT vs golden:"
    head -25 "/tmp/$name.diff"
    FAIL+=("$name (drift)")
  fi
  rm -f "$TMP_CSV"
}

run_system() {
  local name="$1" checkout rules config
  case "$name" in
    sheaf)
      checkout="$REPO_ROOT"
      rules="$EX/self-scan/categorization-rules.textproto"
      config="$EX/self-scan/sheaf.textproto" ;;
    envoy)
      checkout="$ENVOY_CHECKOUT"
      rules="$EX/envoy-coverage-rules.textproto"
      config="$EX/envoy-coverage-config.textproto" ;;
    # gh: see the header note — omitted until cli/cli renders a non-empty surface.
    pigweed-pw_rpc)
      checkout="$PIGWEED_CHECKOUT"
      rules="$EX/pigweed-coverage-rules.textproto"
      config="$EX/pigweed-pw_rpc-coverage-config.textproto" ;;
    pigweed-pw_log)
      checkout="$PIGWEED_CHECKOUT"
      rules="$EX/pigweed-coverage-rules.textproto"
      config="$EX/pigweed-pw_log-coverage-config.textproto" ;;
    pigweed-pw_transfer)
      checkout="$PIGWEED_CHECKOUT"
      rules="$EX/pigweed-coverage-rules.textproto"
      config="$EX/pigweed-pw_transfer-coverage-config.textproto" ;;
    fuchsia-io)
      checkout="$FUCHSIA_CHECKOUT"
      rules="$EX/fuchsia-io-coverage-rules.textproto"
      config="$EX/fuchsia-io-coverage-config.textproto" ;;
    ffx)
      # ffx synthesizes its contract inputs from the Fuchsia CLI goldens into
      # $FFX_CHECKOUT/sheaf-ffx-gen/ first; the config env-expands ${FFX_CHECKOUT}.
      checkout="$FFX_CHECKOUT"
      rules="$EX/ffx-coverage-rules.textproto"
      config="$EX/ffx-coverage-config.textproto"
      if [[ ! -d "$checkout" ]]; then echo "[ffx] checkout missing: $checkout — skipping"; SKIP+=("ffx"); return; fi
      export FFX_CHECKOUT
      FFX_CHECKOUT="$checkout" python3 "$REPO_ROOT/scripts/gen-ffx-coverage-inputs.py" >/dev/null || {
        echo "[ffx] input synthesis failed" >&2; FAIL+=("ffx (gen-inputs failed)"); return; }
      ;;
    *)
      echo "unknown system: $name" >&2; FAIL+=("$name (unknown)"); return ;;
  esac

  if [[ ! -d "$checkout" ]]; then
    echo "[$name] checkout missing: $checkout — skipping"
    SKIP+=("$name")
    [[ "$name" == "ffx" ]] && rm -rf "$checkout/sheaf-ffx-gen" 2>/dev/null
    return
  fi

  if fingerprint "$name" "$checkout" "$rules" "$config"; then
    verify_or_update "$name"
  else
    echo "[$name] sheaf report failed" >&2
    FAIL+=("$name (scan failed)")
    rm -f "$TMP_CSV"
  fi
  [[ "$name" == "ffx" ]] && rm -rf "$checkout/sheaf-ffx-gen" 2>/dev/null
  return 0
}

for sys in "${SYSTEMS[@]}"; do run_system "$sys"; done

[[ $CLEAN_SHEAF -eq 1 ]] && rm -f "$SHEAF"

echo
echo "===== example-report cross-check ====="
echo "ok:      ${#PASS[@]} (${PASS[*]:-})"
echo "skipped: ${#SKIP[@]} (${SKIP[*]:-})   # checkout absent"
echo "failed:  ${#FAIL[@]} (${FAIL[*]:-})"
test ${#FAIL[@]} -eq 0
