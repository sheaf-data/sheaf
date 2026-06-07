#!/usr/bin/env python3
"""Synthesize the sheaf `ffx` scan inputs from Fuchsia's checked-in CLI
goldens. ffx hides most leaf subcommands as `argh` enum variants in shared
`args.rs` files, so a source-walking contract anchor (argh) sees only ~27%
of the surface. The authoritative, complete surface is the golden corpus:

  <ffx>/tests/cli-goldens/goldens/**/*.golden

— one JSON `CliArgsInfo` dump per command (name/description/flags/
positionals/examples), produced by `ffx --machine json-pretty --help` and
diffed in CI. We turn those 680 commands into the three artifacts the scan
needs (exactly the kubectl/kubectl-yamlgen pattern of feeding a generated
bundle to a stock adapter):

  1. ffx-cobra-yaml/<anchor>.yaml — one cobra-schema doc per command
     (command/short/options), consumed by the stock `cobra` contract
     anchor for the COMPLETE 680-command + flags surface. Files are JSON
     (valid YAML; yaml.v3 parses it), named `<anchor>.yaml` so the config's
     url_base `…/ffx#{basename}` yields the deep-anchored reference URL.
  2. clidoc_out.tar.gz (entry `clidoc/ffx.md`) — one anchored
     `### <name> {#ffx_<path>}` section per command, consumed by the
     `clidoc` rendered-reference adapter (docs.reference + fuchsia.dev URLs).
  3. ffx-golden-examples.md — every command's examples[] as posix-terminal
     fences (examples surface, via the markdown doc parser's CLI extractor).

Deterministic + idempotent: commands walked in sorted order, tar mtime=0,
so an unchanged corpus yields byte-identical output. All three land under
<out>/ (default <ffx>/sheaf-ffx-gen/), which the regen ffx arm stages and
tears down per run.

  FFX_CHECKOUT / FUCHSIA_CHECKOUT  fuchsia tree (default /Volumes/T7/fuchsia)
  FFX_GEN_OUT                      output dir (default <ffx>/sheaf-ffx-gen)
  Usage:  python3 scripts/gen-ffx-coverage-inputs.py
"""
import io
import json
import os
import re
import sys
import tarfile

FFX = os.environ.get("FFX_CHECKOUT") or os.environ.get("FUCHSIA_CHECKOUT") or "/Volumes/T7/fuchsia"
GOLDENS = os.path.join(FFX, "src/developer/ffx/tests/cli-goldens/goldens")
OUT_DIR = os.environ.get("FFX_GEN_OUT") or os.path.join(FFX, "sheaf-ffx-gen")
COBRA_DIR = os.path.join(OUT_DIR, "ffx-cobra-yaml")


def golden_files(root):
    out = []
    for dirpath, _dirs, files in os.walk(root):
        for fn in files:
            if fn.endswith(".golden"):
                out.append(os.path.join(dirpath, fn))
    return sorted(out)


def command_tokens(path, root):
    rel = os.path.relpath(path, root)
    rel = rel[: -len(".golden")] if rel.endswith(".golden") else rel
    return rel.split(os.sep)


def flag_descr(flag):
    """Return (long_no_dashes, shorthand, value_type, description) or None to skip."""
    long = (flag.get("long") or "").strip()
    if not long.startswith("--") or long == "--help":
        return None  # skip the universal --help and any malformed entry
    if flag.get("hidden"):
        return None  # hidden flags aren't part of the public contract
    kind = flag.get("kind")
    value_type = "bool" if kind == "Switch" else "string"  # cobra: bool -> SWITCH
    return (long[2:], flag.get("short") or "", value_type, (flag.get("description") or "").strip())


def flag_line(flag):
    long = flag.get("long") or ""
    short = flag.get("short")
    desc = (flag.get("description") or "").strip()
    head = f"{long}, -{short}" if short else long
    kind = flag.get("kind")
    if isinstance(kind, dict) and "Option" in kind:
        arg = kind["Option"].get("arg_name")
        if arg:
            head = f"{head} <{arg}>"
    return f"{head}  {desc}".rstrip()


def positional_line(pos):
    name = pos.get("name") or ""
    desc = (pos.get("description") or "").strip()
    return f"<{name}>  {desc}".rstrip()


def build():
    if not os.path.isdir(GOLDENS):
        sys.exit(f"gen-ffx-coverage-inputs: goldens not found: {GOLDENS} (set FFX_CHECKOUT)")

    files = golden_files(GOLDENS)
    ref_sections = []        # clidoc/ffx.md
    example_sections = []    # ffx-golden-examples.md
    n_examples = 0
    n_flags = 0

    # Reset the cobra yaml dir so removed commands don't linger across runs.
    if os.path.isdir(COBRA_DIR):
        for fn in os.listdir(COBRA_DIR):
            if fn.endswith(".yaml"):
                os.remove(os.path.join(COBRA_DIR, fn))
    os.makedirs(COBRA_DIR, exist_ok=True)

    for path in files:
        tokens = command_tokens(path, GOLDENS)
        anchor = "_".join(tokens)            # ffx_target_add
        cmd_path = " ".join(tokens)          # ffx target add
        header = tokens[-1]                  # add  (root -> "ffx")
        try:
            with open(path, encoding="utf-8") as fh:
                data = json.load(fh)
        except (OSError, ValueError) as e:
            print(f"  skip {path}: {e}", file=sys.stderr)
            continue

        desc = (data.get("description") or "").strip()
        flags = data.get("flags") or []

        # 1. cobra-schema doc (JSON is valid YAML) -> complete contract surface.
        options = []
        for fl in flags:
            fd = flag_descr(fl)
            if fd is None:
                continue
            opt, short, vtype, fdesc = fd
            options.append({
                "option": opt,
                "shorthand": short,
                "value_type": vtype,
                "description": fdesc,
            })
        n_flags += len(options)
        cobra_doc = {"command": cmd_path, "short": desc, "options": options}
        with open(os.path.join(COBRA_DIR, anchor + ".yaml"), "w", encoding="utf-8") as fh:
            json.dump(cobra_doc, fh, indent=1, sort_keys=True)

        # 2. clidoc reference section.
        lines = [f"### {header} {{#{anchor}}}", ""]
        if desc:
            lines += [desc, ""]
        for fl in flags:
            lines.append(flag_line(fl))
        for ps in data.get("positionals") or []:
            lines.append(positional_line(ps))
        lines.append("")
        ref_sections.append("\n".join(lines))

        # 3. worked examples. Collapse any embedded triple-backtick run to a
        # single backtick so a golden example that itself contains a fenced
        # block can't break the posix-terminal fence pairing in our output
        # (an unbalanced fence cascades and mis-captures every later block).
        examples = [re.sub(r"`{3,}", "`", e.strip()) for e in (data.get("examples") or []) if e and e.strip()]
        if examples:
            n_examples += len(examples)
            ex = [f"## {cmd_path} {{#{anchor}}}", ""]
            for e in examples:
                ex += ["```posix-terminal", e, "```", ""]
            example_sections.append("\n".join(ex))

    # clidoc_out.tar.gz with a single deterministic entry clidoc/ffx.md.
    ffx_md = "# ffx\n\n" + "\n".join(ref_sections)
    payload = ffx_md.encode("utf-8")
    tar_path = os.path.join(OUT_DIR, "clidoc_out.tar.gz")
    with tarfile.open(tar_path, "w:gz") as tar:
        info = tarfile.TarInfo("clidoc/ffx.md")
        info.size = len(payload)
        info.mtime = 0
        info.mode = 0o644
        tar.addfile(info, io.BytesIO(payload))

    examples_md = (
        "# ffx — worked examples\n\n"
        "Generated from the `examples[]` arrays of the ffx CLI goldens.\n\n"
        + "\n".join(example_sections)
    )
    examples_path = os.path.join(OUT_DIR, "ffx-golden-examples.md")
    with open(examples_path, "w", encoding="utf-8") as fh:
        fh.write(examples_md)

    print(
        f"gen-ffx-coverage-inputs: {len(files)} commands, {n_flags} flags -> "
        f"{COBRA_DIR}/*.yaml (cobra contract); clidoc/ffx.md ({len(payload)} bytes) -> "
        f"{tar_path}; {n_examples} examples across {len(example_sections)} commands -> {examples_path}",
        file=sys.stderr,
    )


if __name__ == "__main__":
    build()
