"""Shared test/impl overlay support for sheaf coverage config generators.

Coverage config generators (gen-<ecosystem>-coverage.py) emit contract-only
configs derived mechanically from an SDK/contract source. Per-domain test and
implementation wiring CANNOT be derived that way — a domain's impl/test source
location does not follow from its contract names (e.g. the fuchsia.driver.*
FIDL is implemented under src/devices/bin/driver_manager). That wiring is
human-curated, so each generator layers it on from its OWN overlay file,
re-applied on every run, which keeps regeneration non-destructive.

Convention: one overlay file per ecosystem, named to match the manifest —
    <ecosystem>-coverage-manifest.textproto -> <ecosystem>-coverage-overlay.json
Each generator passes its overlay path to load_overlay() and calls
render_overlay_blocks() / render_overlay_surfaces() while emitting each config.

Overlay JSON shape — keys are domain slugs (the per-domain config filename
stems); a key beginning with "_" is ignored, so "_README" can document the
file:

    {
      "_README": "...",
      "<domain-slug>": {
        "test_parsers":     [{"name": "...",
                              "kind": "gtest" | "rust_test" | ...,
                              "include": ["path/glob", ...]}, ...],
        "implements_maps":  [{"name": "...",
                              "include": ["path/glob", ...]}, ...],
        "surfaces_required": ["implementations", ...]
      }
    }

The render helpers reproduce the exact byte layout of the committed configs, so
`generate` is idempotent: an unchanged source + overlay yields byte-identical
output (the regression test for any generator using this module).
"""
import json
import os
import sys


def load_overlay(path):
    """Load an overlay file. Required by design: a missing overlay is a hard
    error, so a generator can never silently fall back to bare (un-enriched)
    configs and quietly drop the hand-curated wiring."""
    if not os.path.exists(path):
        sys.exit(f"coverage overlay not found: {path}")
    with open(path) as fh:
        return json.load(fh)


def entry_for(overlay, slug):
    """The overlay entry (dict) for one domain slug, or {} if none."""
    return overlay.get(slug, {})


def render_overlay_blocks(f, ov):
    """Write an entry's test_parser + implements_map blocks to open file f.
    Call this AFTER the contract_anchor and BEFORE the analyzers."""
    for tp in ov.get("test_parsers", []):
        f.write("test_parser {\n")
        f.write(f'  name: "{tp["name"]}"\n')
        f.write(f'  {tp["kind"]} {{\n')
        for inc in tp["include"]:
            f.write(f'    include: "{inc}"\n')
        f.write("  }\n")
        f.write("}\n")
    for im in ov.get("implements_maps", []):
        f.write("implements_map {\n")
        f.write(f'  name: "{im["name"]}"\n')
        for inc in im["include"]:
            f.write(f'  include: "{inc}"\n')
        f.write("}\n")


def render_overlay_surfaces(f, ov):
    """Write an entry's extra surfaces_required lines to open file f.
    Call this AFTER the generator's own surfaces_required lines."""
    for s in ov.get("surfaces_required", []):
        f.write(f'surfaces_required: "{s}"\n')


def warn_stale(overlay, emitted_slugs):
    """Warn (stderr) about overlay keys that match no emitted domain — usually
    a typo or a domain that was renamed/removed."""
    stale = [k for k in overlay
             if not k.startswith("_") and k not in emitted_slugs]
    if stale:
        print(f"WARNING: overlay keys match no domain (stale?): {', '.join(stale)}",
              file=sys.stderr)
