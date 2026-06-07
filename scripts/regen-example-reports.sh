#!/usr/bin/env bash
# Regenerate the library-coverage reports under example-reports/.
#
# One self-contained pass: build sheaf, walk each (system, library,
# ecosystem) tuple, and render fully in-process — no server, no scanner
# binary. Each report is produced in two steps: `sheaf snapshot` scans
# the checkout and emits the Snapshot JSON, then `sheaf render
# --from-snapshot` renders it to HTML and computes documentation lag
# against the checkout's git tree (--repo-root). Continues on per-system
# failure so a missing checkout doesn't block the rest.
#
# sheaf-self follows the same two-step shape against this repo — the
# exact data step the TestRender_SelfScanByteIdentical golden test
# exercises.
#
# Usage:
#   scripts/regen-example-reports.sh [system ...]
#     systems: envoy envoy-config ffx sheaf-self \
#              fuchsia-coverage  (omnibus: whole FIDL SDK, 27 domains)
#     ffx synthesizes its inputs from the Fuchsia CLI goldens first (see
#     the ffx) arm).
#
# Env knobs:
#   ENVOY_CHECKOUT     default /Volumes/T7/envoy
#   FUCHSIA_CHECKOUT   default /Volumes/T7/fuchsia
#   FFX_CHECKOUT       default $FUCHSIA_CHECKOUT (ffx lives in the Fuchsia tree)

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

ENVOY_CHECKOUT="${ENVOY_CHECKOUT:-/Volumes/T7/envoy}"
FUCHSIA_CHECKOUT="${FUCHSIA_CHECKOUT:-/Volumes/T7/fuchsia}"
# ffx ships inside the Fuchsia tree, so it defaults to the same checkout.
FFX_CHECKOUT="${FFX_CHECKOUT:-$FUCHSIA_CHECKOUT}"
PIGWEED_CHECKOUT="${PIGWEED_CHECKOUT:-/Volumes/T7/pigweed}"

ALL_SYSTEMS=(envoy envoy-config ffx sheaf-self \
             fuchsia-coverage \
             pigweed-pw_rpc pigweed-pw_log pigweed-pw_transfer)

SYSTEMS=("$@")
if [[ ${#SYSTEMS[@]} -eq 0 ]]; then
  SYSTEMS=("${ALL_SYSTEMS[@]}")
fi

echo "==> Building sheaf ..."
go build -o "$REPO_ROOT/sheaf" "$REPO_ROOT/cmd/sheaf"

PASS=()
FAIL=()

render_one() {
  local system="$1"
  local checkout="$2"
  local rules="$3"
  local config="$4"
  local lib="$5"
  local lib_label="$6"
  local eco="$7"
  local source_url="$8"
  local out="$9"
  local concept_docs_href="${10:-}"

  if [[ ! -d "$checkout" ]]; then
    echo "[$system] checkout missing: $checkout — skipping"
    FAIL+=("$system (no checkout)")
    return
  fi

  echo
  echo "==> [$system] $out (in-process; no serve)"
  # Stage the system's categorization rules at the checkout root, where
  # BuildSnapshot (behind `sheaf snapshot`) looks for them — same place
  # `sheaf serve` read them from before.
  cp "$rules" "$checkout/categorization-rules.textproto"

  local snap="/tmp/sheaf-$system.json"

  # Step 1 — scan the checkout in-process and emit the Snapshot JSON.
  local snap_args=(snapshot --config "$config" --repo "$checkout" --out "$snap")
  if [[ -n "$lib" ]]; then
    snap_args+=(--library "$lib")
  fi
  if [[ -n "$lib_label" ]]; then
    snap_args+=(--library-label "$lib_label")
  fi
  if ! "$REPO_ROOT/sheaf" "${snap_args[@]}" 2>&1 | tail -2; then
    echo "[$system] sheaf snapshot failed"
    rm -f "$checkout/categorization-rules.textproto"
    FAIL+=("$system (snapshot failed)")
    return
  fi

  # Step 2 — render the snapshot to HTML, computing lag against the
  # checkout's git tree (--repo-root) and reading the evidence-rail
  # fragments from it. The rolled-up Library label is carried by the
  # snapshot, so render inherits it without --library.
  local render_args=(render --from-snapshot "$snap" --ecosystem "$eco" --repo-root "$checkout")
  if [[ -n "$source_url" ]]; then
    render_args+=(--source-url-template "$source_url")
  fi
  # When a sibling Concept Docs report exists, link the coverage report's
  # concept-doc reach line to it (relative href, resolved at view time).
  if [[ -n "$concept_docs_href" ]]; then
    render_args+=(--concept-docs-href "$concept_docs_href")
  fi
  render_args+=(-o "$out")

  if ! "$REPO_ROOT/sheaf" "${render_args[@]}" 2>&1 | tail -3; then
    echo "[$system] sheaf render failed"
    FAIL+=("$system (render failed)")
  else
    PASS+=("$system")
  fi

  rm -f "$checkout/categorization-rules.textproto" "$snap"
}

# render_self_inprocess regenerates example-reports/sheaf-self.html via the
# server-free in-process path: `sheaf snapshot` produces the Snapshot JSON,
# then `sheaf render --from-snapshot` renders it. This is the exact data step
# the TestRender_SelfScanByteIdentical golden test exercises, so the committed
# report stays reproducible in CI without standing up `sheaf serve`. The
# self-scan's categorization rules must be staged at the repo root (where
# BuildSnapshot looks) for the duration of the run.
render_self_inprocess() {
  local config="$REPO_ROOT/docs/examples/self-scan/sheaf.textproto"
  local rules="$REPO_ROOT/docs/examples/self-scan/categorization-rules.textproto"
  local out="$REPO_ROOT/example-reports/sheaf-self.html"
  local snap="/tmp/sheaf-self-snapshot.json"
  local source_url="https://github.com/sheaf-data/sheaf/blob/main/{path}#L{line}"

  echo
  echo "==> [sheaf-self] $out (in-process; no serve)"
  cp "$rules" "$REPO_ROOT/categorization-rules.textproto"

  if ! "$REPO_ROOT/sheaf" snapshot --library sheaf --config "$config" \
      --repo "$REPO_ROOT" --out "$snap" 2>&1 | tail -1; then
    echo "[sheaf-self] sheaf snapshot failed"
    rm -f "$REPO_ROOT/categorization-rules.textproto"
    FAIL+=("sheaf-self (snapshot failed)")
    return
  fi

  if ! "$REPO_ROOT/sheaf" render --from-snapshot "$snap" --library sheaf \
      --ecosystem cli --repo-root "$REPO_ROOT" \
      --source-url-template "$source_url" -o "$out" 2>&1 | tail -2; then
    echo "[sheaf-self] sheaf render --from-snapshot failed"
    rm -f "$REPO_ROOT/categorization-rules.textproto" "$snap"
    FAIL+=("sheaf-self (render failed)")
    return
  fi
  PASS+=("sheaf-self")

  # Concept-doc coverage (the docs.concepts / grounding lens): the doc-centric
  # clear / ambiguous / silent map of how sheaf's own narrative docs reference
  # its CLI surface. Co-generated next to the regular coverage report so a
  # single `regen-example-reports.sh sheaf-self` refreshes both — the self-scan
  # counterpart to the fuchsia-coverage arm's gen-fuchsia-concept-docs.py step.
  # The doc-glob set is sheaf's narrative concept-doc surface (the same files
  # the self-scan's `markdown` doc_parser draws on, minus the per-subcommand
  # reference pages, which are the reference surface, not concept docs).
  local cd_out="$REPO_ROOT/example-reports/sheaf-self-concept-docs.html"
  local cd_grounding="/tmp/sheaf-self-grounding.json"
  local emit_grounding="/tmp/sheaf-emit-grounding"
  echo
  echo "==> [sheaf-self] $cd_out (concept-doc lens)"
  go build -o "$emit_grounding" "$REPO_ROOT/cmd/emit-grounding"
  if ! "$emit_grounding" -config "$config" -repo "$REPO_ROOT" -library sheaf \
      -doc-glob "README.md" -doc-glob "docs/cli/sheaf.md" -doc-glob "docs/cli/workflows.md" \
      -doc-glob "docs/config.md" -doc-glob "docs/mcp/*.md" -doc-glob "docs/playbooks/**/*.md" \
      -o "$cd_grounding" -quiet 2>&1 | tail -1; then
    echo "[sheaf-self] emit-grounding failed"
    FAIL+=("sheaf-self-concept-docs (grounding failed)")
  elif ! "$REPO_ROOT/sheaf" report --lens concept-docs --from-grounding "$cd_grounding" \
      --library sheaf --library-label "Sheaf (self-scan)" --output "$cd_out" 2>&1 | tail -1; then
    echo "[sheaf-self] concept-docs render failed"
    FAIL+=("sheaf-self-concept-docs (render failed)")
  else
    PASS+=("sheaf-self-concept-docs")
  fi

  rm -f "$REPO_ROOT/categorization-rules.textproto" "$snap" "$cd_grounding"
}

for sys in "${SYSTEMS[@]}"; do
  case "$sys" in
    envoy)
      render_one envoy "$ENVOY_CHECKOUT" \
        "$REPO_ROOT/docs/examples/envoy-coverage-rules.textproto" \
        "$REPO_ROOT/docs/examples/envoy-coverage-config.textproto" \
        envoy.service.discovery.v3 "" proto \
        "https://github.com/envoyproxy/envoy/blob/main/{path}#L{line}" \
        "$REPO_ROOT/example-reports/envoy.html"
      ;;
    envoy-config)
      render_one envoy-config "$ENVOY_CHECKOUT" \
        "$REPO_ROOT/docs/examples/envoy-coverage-rules.textproto" \
        "$REPO_ROOT/docs/examples/envoy-coverage-config.textproto" \
        "envoy.config.listener.v3,envoy.config.cluster.v3,envoy.config.route.v3" \
        "envoy.config.v3 (Listener+Cluster+Route)" proto-config \
        "https://github.com/envoyproxy/envoy/blob/main/{path}#L{line}" \
        "$REPO_ROOT/example-reports/envoy-config.html"
      ;;
    ffx)
      # ffx's complete command surface (680 commands) lives in its CLI
      # goldens, not a single source tree — leaf subcommands are argh enum
      # variants a source walker can't follow. gen-ffx-coverage-inputs.py
      # synthesizes the cobra contract YAML + clidoc reference bundle +
      # worked-examples markdown from those goldens into
      # $FFX_CHECKOUT/sheaf-ffx-gen/ (regenerated + torn down each run). The
      # config's clidoc bundle_path is ${FFX_CHECKOUT}-rooted, so export it
      # for the config's env-expansion before snapshotting.
      if [[ ! -d "$FFX_CHECKOUT" ]]; then
        echo "[ffx] checkout missing: $FFX_CHECKOUT — skipping"
        FAIL+=("ffx (no checkout)")
      else
        export FFX_CHECKOUT
        FFX_CHECKOUT="$FFX_CHECKOUT" python3 "$REPO_ROOT/scripts/gen-ffx-coverage-inputs.py" >/dev/null
        # ffx coverage report lives next to its sibling concept-docs report
        # (generated just below) in docs/examples/, so the coverage report's
        # concept-doc reach line links to it with the bare relative href
        # "ffx-concept-docs.html".
        render_one ffx "$FFX_CHECKOUT" \
          "$REPO_ROOT/docs/examples/ffx-coverage-rules.textproto" \
          "$REPO_ROOT/docs/examples/ffx-coverage-config.textproto" \
          ffx "" cli \
          "https://cs.opensource.google/fuchsia/fuchsia/+/main:{path};l={line}" \
          "$REPO_ROOT/docs/examples/ffx.html" \
          "ffx-concept-docs.html"
        # Concept-doc coverage (the docs.concepts / grounding lens): the
        # doc-centric clear/ambiguous/silent map of how ffx's narrative guides
        # reference its command/flag contract. ffx is a single library, so one
        # `sheaf report --lens concept-docs` does the whole thing — no
        # emit-grounding / rollup / index (cf. the fuchsia-coverage arm, which
        # needs those only because it spans four libraries). It MUST run here,
        # inside the synth/teardown window: the doc_parser globs include the
        # synthesized sheaf-ffx-gen/ffx-golden-examples.md and the contract
        # corpus it checks the docs against is the just-synthesized cobra+clidoc
        # surface. Uses the config's doc_parser globs automatically (no
        # --doc-glob). grounding.BuildConfig runs with nil categorization rules,
        # so no categorization-rules.textproto staging is needed. Reuses the
        # already-built $REPO_ROOT/sheaf; continues on failure like the rest.
        echo
        echo "==> [ffx-concept-docs] docs/examples/ffx-concept-docs.html (in-process; concept-docs lens)"
        if "$REPO_ROOT/sheaf" report --lens concept-docs \
             --library ffx \
             --config "$REPO_ROOT/docs/examples/ffx-coverage-config.textproto" \
             --repo "$FFX_CHECKOUT" \
             --library-label "Fuchsia ffx (developer CLI)" \
             --source-url-template "https://cs.opensource.google/fuchsia/fuchsia/+/main:{path};l={line}" \
             --output "$REPO_ROOT/docs/examples/ffx-concept-docs.html" 2>&1 | tail -2; then
          PASS+=("ffx-concept-docs")
        else
          FAIL+=("ffx-concept-docs (report failed)")
        fi
        rm -rf "$FFX_CHECKOUT/sheaf-ffx-gen"
      fi
      ;;
    sheaf-self)
      # In-process (snapshot + --from-snapshot), no serve — see
      # render_self_inprocess. Keeps the self-scan golden reproducible in CI.
      render_self_inprocess
      ;;
    fuchsia-coverage)
      # Omnibus: the whole Fuchsia FIDL SDK grouped into 27 domains, rendered
      # as a navigable index + one page per domain (load-on-demand) — replaces
      # the old per-library and driver-family single-file reports. Regenerates
      # the per-domain config set + manifest from the SDK, then fans out.
      # gen-fuchsia-coverage.py re-applies the committed test/impl overlay
      # (fuchsia-coverage-overlay.json) on every run, so regeneration is
      # non-destructive + idempotent: an unchanged SDK leaves the configs
      # byte-identical, an SDK update refreshes them (commit the diff).
      # Generator, overlay, configs, and manifest are committed; the rendered
      # <root>.html + <root>-contents/ output is gitignored. After the contract
      # fan-out it also co-generates the sibling concept-doc coverage
      # (fuchsia-coverage-concept-docs/: per-library reports + an all-fuchsia
      # rollup + an index) via gen-fuchsia-concept-docs.py — likewise gitignored.
      # Each domain report's concept-doc reach-stats eyebrow links into it,
      # wired through the manifest's per-entry concept_docs_href.
      if [[ ! -d "$FUCHSIA_CHECKOUT" ]]; then
        echo "[fuchsia-coverage] checkout missing: $FUCHSIA_CHECKOUT — skipping"
        FAIL+=("fuchsia-coverage (no checkout)")
      else
        echo
        echo "==> [fuchsia-coverage] docs/examples/fuchsia-coverage.html + -contents/ (27-domain fan-out)"
        FUCHSIA_CHECKOUT="$FUCHSIA_CHECKOUT" python3 "$REPO_ROOT/scripts/gen-fuchsia-coverage.py" >/dev/null
        if "$REPO_ROOT/sheaf" scan \
             --manifest "$REPO_ROOT/docs/examples/fuchsia-coverage-manifest.textproto" \
             --repo "$FUCHSIA_CHECKOUT" \
             --output-dir "$REPO_ROOT/docs/examples/fuchsia-coverage" 2>&1 | tail -3; then
          PASS+=("fuchsia-coverage")
        else
          FAIL+=("fuchsia-coverage (fan-out failed)")
        fi
        # Concept-doc coverage (the docs.concepts / grounding lens): library-level
        # reports + the all-fuchsia rollup + an index, into the sibling
        # fuchsia-coverage-concept-docs/ dir the domain reports link to. Reuses
        # the freshly-built $REPO_ROOT/sheaf via $SHEAF.
        echo
        echo "==> [fuchsia-coverage] docs/examples/fuchsia-coverage-concept-docs/ (library-level + all-fuchsia + index)"
        if SHEAF="$REPO_ROOT/sheaf" FUCHSIA_CHECKOUT="$FUCHSIA_CHECKOUT" \
             python3 "$REPO_ROOT/scripts/gen-fuchsia-concept-docs.py"; then
          PASS+=("fuchsia-concept-docs")
        else
          FAIL+=("fuchsia-concept-docs (gen failed)")
        fi
      fi
      ;;
    pigweed-pw_log)
      # pw_log is a facade: roll the facade module + its backend modules
      # (pw_log_basic / pw_log_tokenized / pw_log_string / pw_log_null)
      # into one report via a comma-separated --library list, so the
      # facade->backend IMPLEMENTS edges render as cross-links. The
      # backend libraries come from cppheader stamping the path segment
      # after public/ (see the config's scope.also_include).
      render_one pigweed-pw_log "$PIGWEED_CHECKOUT" \
        "$REPO_ROOT/docs/examples/pigweed-coverage-rules.textproto" \
        "$REPO_ROOT/docs/examples/pigweed-pw_log-coverage-config.textproto" \
        "pw_log,pw_log_basic,pw_log_tokenized,pw_log_string,pw_log_null" \
        "pw_log (facade + backends)" cpp \
        "https://pigweed.googlesource.com/pigweed/pigweed/+/main/{path}#{line}" \
        "$REPO_ROOT/example-reports/pigweed-pw_log.html"
      ;;
    pigweed-pw_rpc|pigweed-pw_transfer)
      # mod = "pw_rpc" / "pw_transfer"; the cppheader library == the
      # module name, rendered under the cpp ecosystem.
      mod="${sys#pigweed-}"
      render_one "$sys" "$PIGWEED_CHECKOUT" \
        "$REPO_ROOT/docs/examples/pigweed-coverage-rules.textproto" \
        "$REPO_ROOT/docs/examples/pigweed-${mod}-coverage-config.textproto" \
        "$mod" "" cpp \
        "https://pigweed.googlesource.com/pigweed/pigweed/+/main/{path}#{line}" \
        "$REPO_ROOT/example-reports/pigweed-${mod}.html"
      ;;
    *)
      echo "unknown system: $sys" >&2
      FAIL+=("$sys (unknown)")
      ;;
  esac
done

rm -f "$REPO_ROOT/sheaf"

echo
echo "===== summary ====="
echo "ok:     ${#PASS[@]} (${PASS[*]:-})"
echo "failed: ${#FAIL[@]} (${FAIL[*]:-})"
test ${#FAIL[@]} -eq 0
