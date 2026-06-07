#!/usr/bin/env python3
"""Sheaf scan validator — extract per-assertion verification records,
then post-process Claude-filled verdicts into a precision/recall report.

Two-phase by design so the smart fuzzy "is this test really exercising
this RPC" call lives in Claude's judgment, while the deterministic
"enumerate elements / sample / find FN candidates" runs in Python.

================================================================
PHASE 1 — extract
================================================================

  validate.py extract \\
      --config PATH        # sheaf.textproto
      --repo PATH          # scanned repo root
      [--library NAME]     # restrict to one library (default: all)
      [--sample-tests N]   # cap tests-per-element to verify (default 10)
      [--sample-elements N]# per-library element cap (default 50; 0 = none)
      [--sample-docs N]    # cap docs-per-element to verify (default 5)
      [--fn-search-limit N]# per-element FN candidate cap (default 20)
      [--fn-categories ...]# restrict FN search to these missing categories
                           #   (default: tests.unit_tests)
      [--out PATH]         # JSONL path (default: ./<library-or-all>.jsonl)
      [--sheaf-bin PATH]   # default: 'sheaf' on PATH
      [--seed N]           # RNG seed for reproducible sampling

Writes JSONL with one assertion per line. Each line has a `verdict`
field set to null — Claude fills it in based on reading the source.

Assertion kinds:
  - "tested_by"     : element claims a specific TestRef.
  - "documented_by" : element claims a specific DocRef.
  - "fn_candidate"  : a grep hit in a category's source globs that
                      MIGHT have been attributed but wasn't.

================================================================
PHASE 2 — summarize
================================================================

  validate.py summarize --jsonl PATH [--out PATH]

Reads the JSONL (now with `verdict` and `reason` filled in by Claude),
computes per-library TP/FP/FN/ambiguous counts and a sampling-aware
precision/recall, writes a markdown report.

Verdict values Claude may set:
  - tested_by / documented_by → "tp" | "fp" | "ambiguous"
  - fn_candidate              → "fn" | "not_fn" | "ambiguous"
"""

from __future__ import annotations

import argparse
import collections
import datetime
import fnmatch
import json
import os
import random
import re
import socket
import subprocess
import sys
import time
from pathlib import Path

# ----------------------------------------------------------------
# protojson stream parser
# ----------------------------------------------------------------

def parse_protojson_stream(s: str):
    """Yield decoded JSON objects from a stream of pretty-printed
    objects separated by whitespace. `sheaf report --format json` emits
    one CoverageProfile per object, each multi-line indented.

    Uses json.JSONDecoder.raw_decode in a loop. Tolerant of trailing
    whitespace between objects.
    """
    dec = json.JSONDecoder()
    i = 0
    n = len(s)
    while i < n:
        # Skip whitespace between objects.
        while i < n and s[i].isspace():
            i += 1
        if i >= n:
            break
        try:
            obj, end = dec.raw_decode(s, i)
        except json.JSONDecodeError as e:
            # Give a context-rich error so the user can see where parsing
            # broke (helpful when sheaf emits a stray log line).
            ctx = s[max(0, i - 80):i + 80]
            raise RuntimeError(
                f"protojson parse failed at offset {i}: {e.msg}\n"
                f"context: ...{ctx!r}..."
            ) from e
        yield obj
        i = end

# ----------------------------------------------------------------
# Categorization-rules parser (textproto, narrow shape only)
# ----------------------------------------------------------------

def load_categorization_rules(path: Path) -> dict[str, dict]:
    """Return {category_dotted_path: {"paths": [...], "exclude_paths": [...]}}.
    Tiny textproto parser, only handles the Rules schema."""
    if not path.exists():
        return {}
    out: dict[str, dict] = {}
    text = path.read_text()
    # Split into category { ... } blocks. Crude but the schema is flat.
    block_rx = re.compile(r"category\s*\{([^}]*)\}", re.MULTILINE | re.DOTALL)
    for m in block_rx.finditer(text):
        block = m.group(1)
        dotted = re.search(r'dotted_path\s*:\s*"([^"]*)"', block)
        if not dotted:
            continue
        cat = dotted.group(1)
        paths = [p.group(1) for p in re.finditer(r'\s+paths\s*:\s*"([^"]*)"', block)]
        excludes = [p.group(1) for p in re.finditer(r'exclude_paths\s*:\s*"([^"]*)"', block)]
        out[cat] = {"paths": paths, "exclude_paths": excludes}
    return out


# ----------------------------------------------------------------
# Server lifecycle
# ----------------------------------------------------------------

def port_in_use(port: int) -> bool:
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.settimeout(0.3)
    try:
        return s.connect_ex(("127.0.0.1", port)) == 0
    finally:
        s.close()


def start_sheaf_serve(sheaf_bin: str, config: str, repo: str, port: int, log_path: Path) -> subprocess.Popen:
    """Spawn sheaf serve in background; return Popen handle."""
    log_f = log_path.open("wb")
    p = subprocess.Popen(
        [sheaf_bin, "serve", "--config", config, "--repo", repo, "--port", str(port)],
        stdout=log_f, stderr=subprocess.STDOUT,
    )
    deadline = time.time() + 600
    while time.time() < deadline:
        if not port_in_use(port):
            if p.poll() is not None:
                raise RuntimeError(
                    f"sheaf serve died (exit {p.returncode}); see {log_path}")
            time.sleep(2)
            continue
        # Confirm /healthz responds.
        try:
            import urllib.request
            urllib.request.urlopen(f"http://127.0.0.1:{port}/healthz", timeout=3).close()
            return p
        except Exception:
            time.sleep(1)
    raise RuntimeError(f"sheaf serve did not come up within 600s; see {log_path}")


# ----------------------------------------------------------------
# Coverage profile harvesting
# ----------------------------------------------------------------

def run_sheaf_report(sheaf_bin: str, config: str, repo: str) -> list[dict]:
    """Call `sheaf report --format json` and parse out CoverageProfile dicts."""
    out = subprocess.check_output(
        [sheaf_bin, "report", "--config", config, "--repo", repo, "--format", "json"],
        text=True, stderr=subprocess.PIPE)
    return list(parse_protojson_stream(out))


def library_of(element_id: str) -> str:
    """element_id is '<library>/<localname>' or just '<localname>' for unscoped."""
    if "/" in element_id:
        return element_id.split("/", 1)[0]
    return ""


# ----------------------------------------------------------------
# TestRef / DocRef harvest
# ----------------------------------------------------------------

def harvest_test_refs(profile: dict) -> list[dict]:
    """Return list of {kind, path, line, test_name, framework} for every
    TestRef in the profile's TestCoverage buckets."""
    out = []
    tests = profile.get("tests") or {}
    for bucket in ("unit", "integration", "e2e", "ctf", "performance", "fuzz", "golden"):
        refs = tests.get(bucket, []) or []
        for r in refs:
            out.append({
                "bucket": f"tests.{bucket}_tests" if bucket == "unit" else f"tests.{bucket}",
                "path": r.get("path", ""),
                "line": int(r.get("line", 0)),
                "test_name": r.get("testName") or r.get("test_name", ""),
                "framework": r.get("framework", ""),
            })
    return out


def harvest_doc_refs(profile: dict) -> list[dict]:
    """Return list of {bucket, path, line, url, adapter} for every DocRef
    in the DocCoverage buckets we know about."""
    out = []
    docs = profile.get("docs") or {}
    ref = docs.get("reference") or {}
    for sub in ("fidldoc", "clidoc", "dockerdoc"):
        for r in (ref.get(sub) or []):
            out.append({
                "bucket": f"docs.reference.{sub}",
                "path": r.get("path", ""),
                "line": int(r.get("line", 0)),
                "url": r.get("url", ""),
                "adapter": r.get("adapter", sub),
            })
    by_adapter = ref.get("byAdapter") or ref.get("by_adapter") or {}
    for adapter, body in by_adapter.items():
        for r in (body.get("refs") or []):
            out.append({
                "bucket": f"docs.reference.{adapter}",
                "path": r.get("path", ""),
                "line": int(r.get("line", 0)),
                "url": r.get("url", ""),
                "adapter": adapter,
            })
    for top in ("concept", "tutorial", "releaseNotes", "release_notes", "faq"):
        for r in (docs.get(top) or []):
            bucket = "docs." + (top if "_" not in top else top.replace("_", ""))
            out.append({
                "bucket": bucket,
                "path": r.get("path", ""),
                "line": int(r.get("line", 0)),
                "url": r.get("url", ""),
                "adapter": r.get("adapter", ""),
            })
    guide = docs.get("guide") or {}
    for sub in ("migration", "troubleshooting", "cookbook"):
        for r in (guide.get(sub) or []):
            out.append({
                "bucket": f"docs.guide.{sub}",
                "path": r.get("path", ""),
                "line": int(r.get("line", 0)),
                "url": r.get("url", ""),
                "adapter": r.get("adapter", ""),
            })
    return out


# ----------------------------------------------------------------
# FN candidate search
# ----------------------------------------------------------------

def derive_search_terms(element_id: str) -> list[str]:
    """Derive a small set of textual terms a real test would mention.

    For "<lib>/<Service>.<Method>" returns:
      - fully-qualified dotted form ("<lib>.<Service>.<Method>")
      - bare Service.Method
      - bare Method
      - <Service>::<Method> (C++ form)
    For "<lib>/<Type>" (no dot in local):
      - dotted form
      - bare Type
    De-duped, in priority order.
    """
    if "/" not in element_id:
        return [element_id]
    lib, local = element_id.split("/", 1)
    out: list[str] = []
    if "." in local:
        svc, method = local.split(".", 1)
        out.extend([
            f"{lib}.{svc}.{method}",  # canonical dotted form
            f"{svc}.{method}",         # bare
            f"{svc}::{method}",        # C++ qualified
            method,                    # last resort
        ])
    else:
        out.extend([
            f"{lib}.{local}",
            local,
        ])
    seen = set()
    deduped = []
    for t in out:
        if t and t not in seen:
            seen.add(t)
            deduped.append(t)
    return deduped


def walk_files(repo: Path, includes: list[str], excludes: list[str]):
    """Yield (rel_path) for every file under repo matching includes,
    not matching excludes. Skips .git/target/node_modules/out."""
    skip_dir = {".git", "target", "node_modules", "out", "build", "bazel-out"}
    for root, dirs, files in os.walk(repo):
        dirs[:] = [d for d in dirs if d not in skip_dir]
        for f in files:
            full = Path(root) / f
            rel = full.relative_to(repo).as_posix()
            if includes:
                if not any(fnmatch.fnmatch(rel, g) for g in includes):
                    continue
            if any(fnmatch.fnmatch(rel, g) for g in excludes):
                continue
            yield rel


def find_fn_candidates(repo: Path, includes: list[str], excludes: list[str],
                       terms: list[str], limit: int) -> list[dict]:
    """Grep test sources for each term; return up to `limit` distinct
    (path, line, matched_term) hits, biased toward higher-signal terms first.

    Uses ripgrep if available (much faster on big repos), falls back to
    Python line-by-line scan.
    """
    hits: list[dict] = []
    seen = set()

    rg = subprocess.run(["which", "rg"], capture_output=True, text=True).stdout.strip()
    files_iter = list(walk_files(repo, includes, excludes))
    if rg and files_iter:
        # ripgrep is dramatically faster — feed it the file list.
        for term in terms:
            if len(hits) >= limit:
                break
            try:
                p = subprocess.run(
                    [rg, "-n", "--no-heading", "-F", term, "--"] + files_iter,
                    cwd=str(repo), capture_output=True, text=True, timeout=120)
            except subprocess.TimeoutExpired:
                continue
            for line in p.stdout.splitlines():
                if len(hits) >= limit:
                    break
                # ripgrep output: path:line:body
                parts = line.split(":", 2)
                if len(parts) < 3:
                    continue
                path, lineno, body = parts
                try:
                    lineno_i = int(lineno)
                except ValueError:
                    continue
                key = (path, lineno_i)
                if key in seen:
                    continue
                seen.add(key)
                hits.append({"path": path, "line": lineno_i, "matched_term": term,
                             "matched_body": body.strip()[:200]})
        return hits

    # Python fallback.
    for term in terms:
        if len(hits) >= limit:
            break
        for rel in files_iter:
            if len(hits) >= limit:
                break
            try:
                body = (repo / rel).read_text(errors="ignore")
            except OSError:
                continue
            for i, line in enumerate(body.splitlines(), start=1):
                if term in line:
                    key = (rel, i)
                    if key in seen:
                        continue
                    seen.add(key)
                    hits.append({"path": rel, "line": i, "matched_term": term,
                                 "matched_body": line.strip()[:200]})
                    if len(hits) >= limit:
                        break
    return hits


# ----------------------------------------------------------------
# Phase 1 — extract
# ----------------------------------------------------------------

def cmd_extract(args):
    config = Path(args.config).resolve()
    repo = Path(args.repo).resolve()
    if not config.exists():
        sys.exit(f"error: config not found: {config}")
    if not repo.is_dir():
        sys.exit(f"error: repo not found: {repo}")

    # Auto-locate categorization-rules.textproto next to the config (the
    # convention sheaf's example configs follow).
    rules_path = config.with_name(config.stem.replace("-config", "-rules") + ".textproto")
    rules = load_categorization_rules(rules_path)
    if not rules:
        # Try a parallel filename, e.g. categorization-rules.textproto in
        # the same dir.
        for cand in (config.parent / "categorization-rules.textproto",
                     config.parent / (config.stem + "-rules.textproto")):
            if cand.exists():
                rules = load_categorization_rules(cand)
                break

    random.seed(args.seed)

    sheaf_bin = args.sheaf_bin or "sheaf"
    server: subprocess.Popen | None = None
    if args.start_server:
        log_path = Path.cwd() / "sheaf-serve.log"
        if port_in_use(args.port):
            sys.exit(f"error: port {args.port} already in use; either pass --no-start-server "
                     f"to reuse the running server, or pick another --port")
        print(f"[extract] starting sheaf serve on :{args.port} (log: {log_path})", file=sys.stderr)
        server = start_sheaf_serve(sheaf_bin, str(config), str(repo), args.port, log_path)

    try:
        print(f"[extract] running sheaf report --format json ...", file=sys.stderr)
        profiles = run_sheaf_report(sheaf_bin, str(config), str(repo))
    finally:
        if server is not None:
            server.terminate()
            try:
                server.wait(timeout=10)
            except subprocess.TimeoutExpired:
                server.kill()

    # Group profiles by library.
    by_lib: dict[str, list[dict]] = collections.defaultdict(list)
    for p in profiles:
        eid = p.get("elementId") or p.get("element_id", "")
        by_lib[library_of(eid)].append(p)

    libs_to_emit = [args.library] if args.library else sorted(by_lib.keys())
    out_path = Path(args.out or f"./{(args.library or 'all').replace('/', '_')}.jsonl")
    print(f"[extract] writing assertions to {out_path}", file=sys.stderr)

    # Default to "tests" — matches the parent-bucket form sheaf emits in
    # GapsSummary.missing. Aggregates child rule paths
    # (tests.unit_tests, tests.integration_tests, …) under the parent.
    fn_categories = set(args.fn_categories or ["tests"])
    n_tested = 0
    n_documented = 0
    n_fn = 0
    with out_path.open("w") as out:
        for lib in libs_to_emit:
            profiles_in_lib = by_lib.get(lib) or []
            if args.sample_elements and len(profiles_in_lib) > args.sample_elements:
                sample = random.sample(profiles_in_lib, args.sample_elements)
                sampled_note = f"sampled {args.sample_elements}/{len(profiles_in_lib)}"
            else:
                sample = profiles_in_lib
                sampled_note = f"all {len(profiles_in_lib)}"
            print(f"[extract] {lib}: {sampled_note} elements", file=sys.stderr)

            for prof in sample:
                eid = prof.get("elementId") or prof.get("element_id", "")
                test_refs = harvest_test_refs(prof)
                doc_refs = harvest_doc_refs(prof)
                missing = (prof.get("gapsSummary") or prof.get("gaps_summary") or {}).get("missing", []) or []

                # tested_by sampling
                tests_sampled = (random.sample(test_refs, args.sample_tests)
                                 if len(test_refs) > args.sample_tests
                                 else test_refs)
                for ref in tests_sampled:
                    out.write(json.dumps({
                        "kind": "tested_by",
                        "library": lib,
                        "element": eid,
                        "bucket": ref["bucket"],
                        "test_name": ref["test_name"],
                        "test_path": ref["path"],
                        "test_line": ref["line"],
                        "framework": ref["framework"],
                        "total_in_bucket": len(test_refs),
                        "sample_size": len(tests_sampled),
                        "verdict": None,
                        "reason": None,
                    }) + "\n")
                    n_tested += 1

                # documented_by sampling
                docs_sampled = (random.sample(doc_refs, args.sample_docs)
                                if len(doc_refs) > args.sample_docs
                                else doc_refs)
                for ref in docs_sampled:
                    out.write(json.dumps({
                        "kind": "documented_by",
                        "library": lib,
                        "element": eid,
                        "bucket": ref["bucket"],
                        "doc_path": ref["path"],
                        "doc_line": ref["line"],
                        "doc_url": ref["url"],
                        "adapter": ref["adapter"],
                        "total_in_bucket": len(doc_refs),
                        "sample_size": len(docs_sampled),
                        "verdict": None,
                        "reason": None,
                    }) + "\n")
                    n_documented += 1

                # FN candidates
                terms = derive_search_terms(eid)
                for category in fn_categories:
                    # Sheaf's GapsSummary.missing reports PARENT buckets
                    # ("tests", "docs.reference") rather than the leaf
                    # dotted paths defined in categorization-rules
                    # ("tests.unit_tests", "tests.integration_tests").
                    # Accept either: a missing entry matches `category`
                    # if it equals it OR if it's a prefix.
                    matches_missing = any(
                        m == category or category.startswith(m + ".") or m.startswith(category + ".")
                        for m in missing)
                    if not matches_missing:
                        continue
                    # Gather paths from every rule whose dotted_path
                    # starts with the parent (so "tests" pulls in
                    # tests.unit_tests + tests.integration_tests + …).
                    paths: list[str] = []
                    excludes: list[str] = []
                    for cat_name, cat_data in rules.items():
                        if cat_name == category or cat_name.startswith(category + "."):
                            paths.extend(cat_data.get("paths", []))
                            excludes.extend(cat_data.get("exclude_paths", []))
                    if not paths:
                        continue
                    cands = find_fn_candidates(
                        repo, paths, excludes, terms, args.fn_search_limit)
                    for c in cands:
                        out.write(json.dumps({
                            "kind": "fn_candidate",
                            "library": lib,
                            "element": eid,
                            "category": category,
                            "candidate_path": c["path"],
                            "candidate_line": c["line"],
                            "matched_term": c["matched_term"],
                            "matched_body": c["matched_body"],
                            "candidate_count": len(cands),
                            "verdict": None,
                            "reason": None,
                        }) + "\n")
                        n_fn += 1

    print(f"\n[extract] done: {n_tested} tested_by, {n_documented} documented_by, "
          f"{n_fn} fn_candidate → {out_path}", file=sys.stderr)


# ----------------------------------------------------------------
# Phase 2 — summarize
# ----------------------------------------------------------------

def cmd_summarize(args):
    jsonl_path = Path(args.jsonl)
    out_path = Path(args.out or jsonl_path.with_suffix(".md"))

    rows = [json.loads(line) for line in jsonl_path.read_text().splitlines() if line.strip()]
    if not rows:
        sys.exit(f"error: no assertions in {jsonl_path}")

    # Aggregate by library, then by kind, then by verdict.
    agg: dict[str, dict[str, collections.Counter]] = collections.defaultdict(
        lambda: collections.defaultdict(collections.Counter))
    confirmed_fp: list[dict] = []
    confirmed_fn: list[dict] = []
    ambiguous: list[dict] = []
    unverified: list[dict] = []

    for r in rows:
        lib = r.get("library") or "(no library)"
        kind = r.get("kind", "?")
        verdict = (r.get("verdict") or "").lower()
        agg[lib][kind][verdict or "unverified"] += 1
        if verdict == "fp":
            confirmed_fp.append(r)
        elif verdict == "fn":
            confirmed_fn.append(r)
        elif verdict == "ambiguous":
            ambiguous.append(r)
        elif not verdict:
            unverified.append(r)

    now = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%d %H:%M UTC")
    lines = [
        f"# Sheaf scan validation",
        "",
        f"_Generated at {now} from `{jsonl_path}`._",
        "",
        f"- Total assertions: **{len(rows)}**",
        f"- Verified: **{len(rows) - len(unverified)}**  ·  Unverified: **{len(unverified)}**",
        f"- Confirmed false positives: **{len(confirmed_fp)}**",
        f"- Confirmed false negatives: **{len(confirmed_fn)}**",
        f"- Ambiguous (needs human): **{len(ambiguous)}**",
        "",
        "## Per-library precision / recall (sampling-aware)",
        "",
        "| Library | Sampled tested_by | TP | FP | Ambig | Precision | Sampled fn_candidate | FN | Not FN | Sample recall |",
        "|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|",
    ]
    for lib in sorted(agg):
        tb = agg[lib]["tested_by"]
        fn = agg[lib]["fn_candidate"]
        tb_total = sum(tb.values())
        fn_total = sum(fn.values())
        tp, fp, ambig = tb["tp"], tb["fp"], tb["ambiguous"]
        precision = (tp / (tp + fp)) if (tp + fp) > 0 else float("nan")
        is_fn, not_fn = fn["fn"], fn["not_fn"]
        # Sample recall: of the candidates that grep found, what
        # fraction are true FN. Caveat: doesn't account for elements
        # we didn't search; that's why we call it "sample recall".
        rec_denom = is_fn + not_fn
        sample_recall = (is_fn / rec_denom) if rec_denom > 0 else float("nan")

        def fmt_pct(x):
            return "—" if x != x else f"{x:.0%}"  # NaN check
        lines.append(
            f"| `{lib}` | {tb_total} | {tp} | {fp} | {ambig} | "
            f"{fmt_pct(precision)} | {fn_total} | {is_fn} | {not_fn} | {fmt_pct(sample_recall)} |"
        )
    lines.append("")

    if confirmed_fp:
        lines.append("## Confirmed false positives")
        lines.append("")
        lines.append("| Library | Element | Test (path:line) | Reason |")
        lines.append("|---|---|---|---|")
        for r in confirmed_fp:
            lines.append(
                f"| `{r.get('library','')}` | `{r.get('element','')}` | "
                f"`{r.get('test_name','')}` (`{r.get('test_path','')}:{r.get('test_line','')}`) | "
                f"{(r.get('reason') or '').replace('|', '\\|')} |"
            )
        lines.append("")

    if confirmed_fn:
        lines.append("## Confirmed false negatives")
        lines.append("")
        lines.append("| Library | Element | Candidate (path:line) | Matched term | Reason |")
        lines.append("|---|---|---|---|---|")
        for r in confirmed_fn:
            lines.append(
                f"| `{r.get('library','')}` | `{r.get('element','')}` | "
                f"`{r.get('candidate_path','')}:{r.get('candidate_line','')}` | "
                f"`{r.get('matched_term','')}` | "
                f"{(r.get('reason') or '').replace('|', '\\|')} |"
            )
        lines.append("")

    if ambiguous:
        lines.append("## Ambiguous (needs human review)")
        lines.append("")
        for r in ambiguous[:50]:
            who = (r.get("test_name") or r.get("candidate_path") or "")
            lines.append(f"- `{r.get('element','')}` ← `{who}`: {r.get('reason') or '(no reason)'}")
        if len(ambiguous) > 50:
            lines.append(f"- _…and {len(ambiguous) - 50} more (see {jsonl_path})._")
        lines.append("")

    if unverified:
        lines.append("## Unverified")
        lines.append("")
        lines.append(f"{len(unverified)} assertions have no verdict. Re-run Claude on those rows "
                     f"or treat this run as partial.")
        lines.append("")

    lines.append("## Caveats")
    lines.append("")
    lines.append("- **Precision** is computed only over sampled, Claude-verified attributions; "
                 "the true precision over the whole population is approximately this value, "
                 "within sampling error.")
    lines.append("- **Sample recall** is the fraction of grep-derived FN candidates that turned "
                 "out to be real FN. It is *not* the recall over all possible missed tests — "
                 "tests that don't textually mention the element are invisible to this validator.")
    lines.append("- Ambiguous verdicts are not failures; they're a queue for human review.")

    out_path.write_text("\n".join(lines))
    print(f"summary → {out_path}", file=sys.stderr)
    print(f"  total={len(rows)}  verified={len(rows)-len(unverified)}  "
          f"FP={len(confirmed_fp)}  FN={len(confirmed_fn)}  ambig={len(ambiguous)}")


# ----------------------------------------------------------------
# argparse wiring
# ----------------------------------------------------------------

def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawTextHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)

    e = sub.add_parser("extract", help="dump assertions to JSONL")
    e.add_argument("--config", required=True)
    e.add_argument("--repo", required=True)
    e.add_argument("--library", default=None)
    e.add_argument("--sample-tests", type=int, default=10)
    e.add_argument("--sample-elements", type=int, default=50)
    e.add_argument("--sample-docs", type=int, default=5)
    e.add_argument("--fn-search-limit", type=int, default=20)
    e.add_argument("--fn-categories", nargs="*", default=None,
                   help="missing categories to FN-search (default: tests, the parent bucket). "
                        "Accepts a parent like 'tests' (aggregates all tests.* child rule paths) "
                        "or a leaf like 'tests.unit_tests'.")
    e.add_argument("--out", default=None)
    e.add_argument("--sheaf-bin", default=None)
    e.add_argument("--port", type=int, default=7700)
    e.add_argument("--start-server", action="store_true",
                   help="spawn sheaf serve for this run; otherwise assume one's running")
    e.add_argument("--seed", type=int, default=42)
    e.set_defaults(func=cmd_extract)

    s = sub.add_parser("summarize", help="read verdicted JSONL → markdown")
    s.add_argument("--jsonl", required=True)
    s.add_argument("--out", default=None)
    s.set_defaults(func=cmd_summarize)

    args = p.parse_args()
    args.func(args)


if __name__ == "__main__":
    main()
