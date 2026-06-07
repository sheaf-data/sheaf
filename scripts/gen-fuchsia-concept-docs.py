#!/usr/bin/env python3
"""Generate the Fuchsia CONCEPT-DOC coverage reports — the doc-centric
clear/ambiguous/silent map of how each library group's narrative docs reference
its FIDL contract (the docs.concepts surface + grounding lens).

Emits, into a sibling fuchsia-coverage-concept-docs/ dir next to the contract
coverage -contents/:
  - one LIBRARY-LEVEL report per covered library group (driver-framework, io,
    ui-composition, ui-gfx), each rendered from a per-library grounding JSON;
  - a combined all-fuchsia.html rolling all of them into one multi-library
    (region=library) report — the cross-library rollup;
  - a small index.html project index linking them.

This is the concept-doc half of `scripts/regen-example-reports.sh
fuchsia-coverage` (which calls it after the 27-domain contract fan-out); it can
also be run standalone. Needs a full Fuchsia checkout — the grounding scan reads
the live docs + FIDL.

The per-library doc-globs are the curated narrative trees for each library
group (concept docs, NOT the auto-generated FIDL reference); they reproduce the
reviewed reports. Each coverage domain's link into these reports is derived by
scripts/gen-fuchsia-coverage.py (CONCEPT_DOC_LIBS) — keep the two in sync.

  FUCHSIA_CHECKOUT   fuchsia tree (default /Volumes/T7/fuchsia)
  CONCEPT_DOCS_OUT   output dir (default docs/examples/fuchsia-coverage-concept-docs)
  SHEAF              prebuilt sheaf binary to reuse (else `go run ./cmd/sheaf`)
  Usage:  python3 scripts/gen-fuchsia-concept-docs.py
"""
import html
import json
import os
import subprocess
import sys
import tempfile

FUCHSIA = os.environ.get("FUCHSIA_CHECKOUT", "/Volumes/T7/fuchsia")
REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT_DIR = os.environ.get(
    "CONCEPT_DOCS_OUT", os.path.join(REPO, "docs/examples/fuchsia-coverage-concept-docs")
)

# (slug, library FQN, config under docs/examples/, [doc-globs]). The slug is the
# report filename stem and MUST match the value in
# gen-fuchsia-coverage.py:CONCEPT_DOC_LIBS so the coverage links resolve.
LIBS = [
    ("driver-framework", "fuchsia.driver.framework",
     "fuchsia-driver-framework-coverage-config.textproto", ["docs/concepts/drivers/**/*.md"]),
    ("io", "fuchsia.io",
     "fuchsia-io-coverage-config.textproto", ["docs/concepts/filesystems/**/*.md"]),
    ("ui-composition", "fuchsia.ui.composition",
     "fuchsia-ui-composition-coverage-config.textproto", ["docs/concepts/ui/**/*.md"]),
    ("ui-gfx", "fuchsia.ui.gfx",
     "fuchsia-ui-gfx-coverage-config.textproto", ["docs/concepts/ui/**/*.md"]),
]
ALL_LABEL = "the Fuchsia FIDL libraries"


def sheaf_cmd():
    """The sheaf invocation: a prebuilt binary via $SHEAF (reused from the regen
    script), else `go run ./cmd/sheaf` for standalone use."""
    sh = os.environ.get("SHEAF", "")
    if sh and os.access(sh, os.X_OK):
        return [sh]
    return ["go", "run", "./cmd/sheaf"]


def run(cmd):
    r = subprocess.run(cmd, cwd=REPO)
    if r.returncode != 0:
        sys.exit(f"gen-fuchsia-concept-docs: command failed:\n  {' '.join(cmd)}")


def build_emit_grounding():
    """Build emit-grounding once to a stable binary and return its path. Beats
    `go run` per library: one compile (faster), and the grounding scans run off
    a fixed binary rather than recompiling four times."""
    out = os.path.join(tempfile.gettempdir(), "sheaf-emit-grounding")
    run(["go", "build", "-o", out, "./cmd/emit-grounding"])
    return out


def read_summary(grounding_json, slug, lib):
    with open(grounding_json) as f:
        d = json.load(f)
    s = d.get("summary", {}) or {}
    return {
        "slug": slug,
        "lib": lib,
        "clear": s.get("elements_grounded", 0),
        "ambiguous": s.get("elements_guessing", 0) + s.get("elements_ungrounded", 0),
        "silent": s.get("elements_not_mentioned", 0),
        "total": s.get("elements_total", 0),
    }


def write_index(out_dir, rows):
    tot = {k: sum(r[k] for r in rows) for k in ("clear", "ambiguous", "silent", "total")}

    def row_html(label, href, r, strong=False):
        cls = " strong" if strong else ""
        return (
            f'<a class="row{cls}" href="{html.escape(href)}">'
            f'<span class="lib">{html.escape(label)}</span>'
            f'<span class="nums"><b class="clear">{r["clear"]}</b> clear · '
            f'<b class="amb">{r["ambiguous"]}</b> ambiguous · '
            f'<b class="silent">{r["silent"]}</b> silent '
            f'<span class="of">of {r["total"]}</span></span></a>'
        )

    lib_rows = "\n".join(
        row_html(r["lib"], r["slug"] + ".html", r) for r in rows
    )
    all_row = row_html(ALL_LABEL, "all-fuchsia.html", tot, strong=True)
    doc = f"""<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Sheaf — Fuchsia concept-doc coverage</title>
<style>
:root{{--paper:#faf6ee;--card:#fffdf8;--ink:#16201c;--ink-2:#4a554f;--ink-3:#7d877f;
--line:#ece5d6;--green-700:#0a6b3f;--cyan-700:#0a6e7a;--amber-700:#b85a08;
--f-display:'Junge',Georgia,serif;--f-body:'IBM Plex Sans',system-ui,sans-serif;
--f-mono:'JetBrains Mono',ui-monospace,Menlo,monospace;}}
*{{margin:0;padding:0;box-sizing:border-box;}}
body{{background:var(--paper);color:var(--ink);font-family:var(--f-body);line-height:1.5;}}
.wrap{{max-width:860px;margin:0 auto;padding:56px 32px 100px;}}
.eyebrow{{font-family:var(--f-body);font-size:11px;font-weight:700;letter-spacing:.14em;
text-transform:uppercase;color:var(--cyan-700);margin-bottom:10px;}}
h1{{font-family:var(--f-display);font-weight:400;font-size:34px;letter-spacing:-.01em;
line-height:1.15;margin-bottom:8px;}}
.lede{{font-family:var(--f-body);font-size:14px;color:var(--ink-2);max-width:70ch;margin-bottom:32px;}}
.row{{display:flex;align-items:baseline;justify-content:space-between;gap:18px;flex-wrap:wrap;
text-decoration:none;color:inherit;border:1px solid var(--line);border-radius:10px;
background:var(--card);padding:15px 18px;margin-bottom:11px;}}
.row:hover{{border-color:#d9cfb6;}}
.row.strong{{border-color:#cfeadd;background:#f4fbf7;}}
.lib{{font-family:var(--f-mono);font-size:14px;color:var(--ink);}}
.nums{{font-family:var(--f-body);font-size:12.5px;color:var(--ink-3);}}
.nums b{{font-family:var(--f-mono);}}
.nums .clear{{color:var(--cyan-700);}} .nums .amb{{color:var(--amber-700);}}
.nums .silent{{color:var(--ink-2);}} .nums .of{{color:var(--ink-3);}}
.foot{{margin-top:34px;font-family:var(--f-body);font-size:11.5px;color:var(--ink-3);line-height:1.6;}}
</style>
</head>
<body>
<div class="wrap">
  <div class="eyebrow">Concept docs · Fuchsia</div>
  <h1>How Fuchsia&rsquo;s narrative docs reference its FIDL contract</h1>
  <div class="lede">Per library group: how many contract entities the concept docs
  name clearly, how many they name only ambiguously, and how many they leave
  silent. Mechanical &amp; deterministic — counts, not percentages.</div>
{lib_rows}
{all_row}
  <div class="foot">Generated by <b>scripts/gen-fuchsia-concept-docs.py</b> from a
  Fuchsia checkout. Each coverage domain&rsquo;s report links here via its
  concept-doc reach-stats eyebrow.</div>
</div>
</body>
</html>
"""
    with open(os.path.join(out_dir, "index.html"), "w") as f:
        f.write(doc)


def main():
    fidl_root = os.path.join(FUCHSIA, "sdk/fidl")
    if not os.path.isdir(fidl_root):
        print(f"[fuchsia-concept-docs] checkout missing: {FUCHSIA} — skipping")
        return 0
    os.makedirs(OUT_DIR, exist_ok=True)
    sheaf = sheaf_cmd()
    emit_grounding = build_emit_grounding()
    grounding_args = []
    rows = []
    for slug, lib, cfg, globs in LIBS:
        gj = os.path.join(OUT_DIR, slug + ".grounding.json")
        emit = [emit_grounding,
                "--config", os.path.join("docs/examples", cfg),
                "--repo", FUCHSIA, "--library", lib, "-o", gj, "--quiet"]
        for g in globs:
            emit += ["--doc-glob", g]
        run(emit)
        run(sheaf + ["report", "--lens", "concept-docs",
                     "--from-grounding", gj,
                     "--output", os.path.join(OUT_DIR, slug + ".html")])
        rows.append(read_summary(gj, slug, lib))
        grounding_args += ["--from-grounding", gj]
        print(f"  {slug}.html")

    # Combined cross-library rollup — multiple --from-grounding inputs flip the
    # region axis to library automatically (conceptdocs.BuildViewAll).
    run(sheaf + ["report", "--lens", "concept-docs", *grounding_args,
                 "--library-label", ALL_LABEL,
                 "--output", os.path.join(OUT_DIR, "all-fuchsia.html")])
    print("  all-fuchsia.html")

    write_index(OUT_DIR, rows)
    print("  index.html")
    for r in rows:
        print(f"    {r['lib']:28} {r['total']:4} elements: "
              f"{r['clear']} clear / {r['ambiguous']} ambiguous / {r['silent']} silent")
    return 0


if __name__ == "__main__":
    sys.exit(main())
