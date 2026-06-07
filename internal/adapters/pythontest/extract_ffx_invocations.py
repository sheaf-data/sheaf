#!/usr/bin/env python3
# Copyright 2026 The Sheaf Authors.
#
# Bundled AST helper for the python-test ffx subprocess-invocation
# extractor. Given a Python source file on argv[1], it walks the AST for
# ffx-transport invocations — calls of the form
#
#     <ffx-receiver>.run(["target", "list", "--no-probe"], ...)
#     <ffx-receiver>.popen([...])
#     <ffx-receiver>.run_test_component(...)
#
# where <ffx-receiver> is an ffx-shaped name (ffx / _ffx / ffx_transport /
# *.ffx / *._ffx ...). For each it resolves the COMMAND-ARG (the first
# positional arg, or the cmd= keyword) to the ordered list of *string
# literal* tokens it contains, eliding dynamic elements (variables,
# f-strings, calls). It also resolves a few common idioms statically:
#
#   - inline list literals:        ["session", "start"]
#   - module-level constant lists:  _FFX_SCREENSHOT_CMD  (NAME = [ ... ])
#   - list concatenation:          _FFX_SCREENSHOT_CMD + [temp_dir]
#   - dict-constant subscripts:     _FFX_CMDS["TARGET_SHOW"]   (value is a list)
#   - locally built lists:         cmd = ["trace","start"]; cmd.extend([...]);
#                                  cmd.append("--nocompress"); ffx.run(cmd)
#
# It prints one JSON object to stdout:
#   {"invocations": [{"args": [...], "line": N, "func": "<enclosing fn>",
#                     "dynamic": bool}, ...],
#    "fully_dynamic": K}
# where "dynamic" marks a call whose command-arg had at least one
# un-resolvable element (so the Go side can log that it was lossy), and
# "fully_dynamic" counts calls whose command-arg resolved to NO literal
# token at all (entirely a variable we couldn't trace, or empty).
#
# Anchoring (ffx-receiver recognition) is intentionally provider-specific
# to the Honeydew transport idioms; the literal-resolution + arg-walking
# machinery is generic. On any parse error it prints
# {"error": "..."} and exits non-zero so the Go caller can fall back to its
# pure-Go regex path rather than silently dropping the file.

import ast
import json
import sys


# Attribute method names that run an ffx command via the Honeydew FFX
# transport. run/popen take the command list as their first positional (or
# cmd=) arg. run_test_component takes the component URL + arg lists, which
# always exercise `ffx test run` — handled specially below.
_RUN_METHODS = {"run", "popen"}
_TEST_COMPONENT_METHOD = "run_test_component"


def _receiver_is_ffx(func: ast.Attribute, in_ffx_transport: bool) -> bool:
    """True when the call's receiver looks like an ffx transport handle.

    Accepts:  ffx / _ffx / ffx_transport / _ffx_transport  (Name)
              self.ffx / self._ffx / dut.ffx / x.ffx_transport (Attribute
                  whose trailing attr is ffx-shaped)
    The trailing identifier must be exactly "ffx"/"_ffx" or end with
    "ffx_transport" — so `self.fastboot` / `self.sl4f` never match.

    Additionally, inside the FFX transport module itself (in_ffx_transport
    — the file defines `class FFX`), a bare `self.run(...)` / `self.popen(...)`
    IS an ffx invocation: the transport's own methods run `_FFX_CMDS[...]`
    lists through `self.run`. This is gated on the class definition so an
    unrelated `self.run` in some other module never matches.
    """
    recv = func.value
    name = None
    if isinstance(recv, ast.Name):
        name = recv.id
    elif isinstance(recv, ast.Attribute):
        name = recv.attr
    if name is None:
        return False
    if name in ("ffx", "_ffx") or name.endswith("ffx_transport"):
        return True
    if in_ffx_transport and name == "self":
        return True
    return False


def _str_const(node: ast.AST):
    """Return the str value of a string-literal node, else None."""
    if isinstance(node, ast.Constant) and isinstance(node.value, str):
        return node.value
    return None


class Resolver:
    """Resolves list-shaped expressions to their literal string tokens
    using module-level constants and (best-effort) function-local lists."""

    def __init__(self, tree: ast.Module) -> None:
        # Module-level NAME = <list literal>  and  NAME = {<dict of lists>}.
        self.const_lists: dict[str, list] = {}
        self.const_dicts: dict[str, dict] = {}
        for stmt in tree.body:
            self._record_module_const(stmt)

    def _record_module_const(self, stmt: ast.AST) -> None:
        targets = []
        value = None
        if isinstance(stmt, ast.Assign):
            targets = stmt.targets
            value = stmt.value
        elif isinstance(stmt, ast.AnnAssign) and stmt.value is not None:
            targets = [stmt.target]
            value = stmt.value
        else:
            return
        for tgt in targets:
            if not isinstance(tgt, ast.Name):
                continue
            if isinstance(value, ast.List):
                lits, _ = self._list_literals(value, set())
                self.const_lists[tgt.id] = lits
            elif isinstance(value, ast.Dict):
                d = {}
                for k, v in zip(value.keys, value.values):
                    ks = _str_const(k)
                    if ks is not None and isinstance(v, ast.List):
                        lits, _ = self._list_literals(v, set())
                        d[ks] = lits
                self.const_dicts[tgt.id] = d

    def _list_literals(self, node: ast.List, seen):
        """Return (literals, had_dynamic) for an ast.List node."""
        out = []
        dynamic = False
        for el in node.elts:
            s = _str_const(el)
            if s is not None:
                out.append(s)
            elif isinstance(el, ast.Starred):
                # *something — best-effort resolve the inner if it's a name.
                inner, dyn = self.resolve(el.value, seen)
                out.extend(inner)
                dynamic = dynamic or dyn or not inner
            else:
                dynamic = True
        return out, dynamic

    def resolve(self, node: ast.AST, seen=None):
        """Resolve an expression to (literals, had_dynamic).

        Handles list literals, names bound to module-level constant lists,
        `A + B` concatenations, and dict-constant subscripts. Anything else
        contributes no literals and flips had_dynamic.
        """
        if seen is None:
            seen = set()
        if isinstance(node, ast.List):
            return self._list_literals(node, seen)
        if isinstance(node, ast.Name):
            if node.id in self.const_lists:
                return list(self.const_lists[node.id]), False
            # Unknown name (likely a locally built var) — caller may have a
            # better local view; here it's dynamic.
            return [], True
        if isinstance(node, ast.BinOp) and isinstance(node.op, ast.Add):
            left, ld = self.resolve(node.left, seen)
            right, rd = self.resolve(node.right, seen)
            return left + right, ld or rd
        if isinstance(node, ast.Subscript):
            # _FFX_CMDS["TARGET_SHOW"]
            base = node.value
            key = node.slice
            if isinstance(base, ast.Name) and base.id in self.const_dicts:
                ks = _str_const(key)
                if ks is not None and ks in self.const_dicts[base.id]:
                    return list(self.const_dicts[base.id][ks]), False
            return [], True
        # Constant string alone, tuple, call, f-string, etc.
        return [], True


def _local_list_var(func_node: ast.AST, var: str, upto_lineno: int, resolver: Resolver):
    """Best-effort reconstruction of a function-local list variable built
    incrementally before `upto_lineno`:

        cmd = ["trace", "start", "--background"]
        cmd.extend(["--categories", x])      # literal "--categories" kept
        cmd.append("--nocompress")
        ... ffx.run(cmd)                     # <- upto_lineno

    Returns (literals, had_dynamic) or (None, False) if the var was never
    seen as a list assignment in this function.
    """
    found = False
    lits: list = []
    dynamic = False

    def _assign_value(value):
        nonlocal found, lits, dynamic
        found = True
        # `cmd = _FFX_CMDS["X"][:]` — a slice copy of a resolvable list.
        if (
            isinstance(value, ast.Subscript)
            and isinstance(value.slice, ast.Slice)
        ):
            l, d = resolver.resolve(value.value)
        else:
            l, d = resolver.resolve(value)
        lits = list(l)
        dynamic = d

    for node in ast.walk(func_node):
        # Assignment:  cmd = [ ... ]   /   cmd: list[str] = [ ... ]
        if isinstance(node, ast.Assign):
            if getattr(node, "lineno", 1 << 30) >= upto_lineno:
                continue
            for tgt in node.targets:
                if isinstance(tgt, ast.Name) and tgt.id == var:
                    _assign_value(node.value)
        elif isinstance(node, ast.AnnAssign) and node.value is not None:
            if getattr(node, "lineno", 1 << 30) >= upto_lineno:
                continue
            if isinstance(node.target, ast.Name) and node.target.id == var:
                _assign_value(node.value)
        # Method:  cmd.extend([...]) / cmd.append("--x")
        elif isinstance(node, ast.Call) and isinstance(node.func, ast.Attribute):
            recv = node.func.value
            if (
                isinstance(recv, ast.Name)
                and recv.id == var
                and getattr(node, "lineno", 1 << 30) < upto_lineno
            ):
                meth = node.func.attr
                if meth == "append" and node.args:
                    s = _str_const(node.args[0])
                    if s is not None:
                        lits.append(s)
                    else:
                        dynamic = True
                elif meth == "extend" and node.args:
                    l, d = resolver.resolve(node.args[0])
                    lits.extend(l)
                    dynamic = dynamic or d
    if not found:
        return None, False
    return lits, dynamic


def _command_arg(call: ast.Call):
    """Return the AST node carrying the ffx command list for a run/popen
    call: the cmd= keyword if present, else the first positional arg."""
    for kw in call.keywords:
        if kw.arg == "cmd":
            return kw.value
    if call.args:
        return call.args[0]
    return None


def _enclosing_func_name(stack):
    for node in reversed(stack):
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
            return node.name
    return ""


def extract(src: str):
    tree = ast.parse(src)
    resolver = Resolver(tree)

    # The FFX transport module defines `class FFX`; inside it, bare
    # `self.run(...)` is an ffx invocation (it runs the _FFX_CMDS lists).
    in_ffx_transport = any(
        isinstance(s, ast.ClassDef) and s.name == "FFX" for s in tree.body
    )

    invocations = []
    fully_dynamic = 0

    # Walk with a parent stack so we can name the enclosing function and
    # reconstruct function-local list vars.
    func_stack: list = []

    class Visitor(ast.NodeVisitor):
        def _visit_func(self, node):
            func_stack.append(node)
            self.generic_visit(node)
            func_stack.pop()

        visit_FunctionDef = _visit_func
        visit_AsyncFunctionDef = _visit_func

        def visit_Call(self, node: ast.Call):
            self.generic_visit(node)
            func = node.func
            if not isinstance(func, ast.Attribute):
                return
            if not _receiver_is_ffx(func, in_ffx_transport):
                return
            line = getattr(node, "lineno", 1)
            fn = _enclosing_func_name(func_stack)

            if func.attr in _RUN_METHODS:
                arg = _command_arg(node)
                if arg is None:
                    return
                lits, dynamic = resolver.resolve(arg)
                # If the arg was a bare name, try local reconstruction.
                if not lits and isinstance(arg, ast.Name) and func_stack:
                    llits, ldyn = _local_list_var(
                        func_stack[-1], arg.id, line, resolver
                    )
                    if llits is not None:
                        lits, dynamic = llits, ldyn
                _record(lits, dynamic, line, fn)

            elif func.attr == _TEST_COMPONENT_METHOD:
                # run_test_component(url, ffx_test_args=[...], ...) always
                # runs `ffx test run`. Seed with the implicit subcommand and
                # fold in any literal flags from ffx_test_args.
                lits = ["test", "run"]
                dynamic = False
                for kw in node.keywords:
                    if kw.arg == "ffx_test_args":
                        l, d = resolver.resolve(kw.value)
                        lits.extend(l)
                        dynamic = dynamic or d
                _record(lits, dynamic, line, fn)

    def _record(lits, dynamic, line, fn):
        nonlocal fully_dynamic
        if not lits:
            fully_dynamic += 1
            return
        invocations.append(
            {"args": lits, "line": line, "func": fn, "dynamic": bool(dynamic)}
        )

    Visitor().visit(tree)
    return {"invocations": invocations, "fully_dynamic": fully_dynamic}


def main(argv):
    if len(argv) != 2:
        print(json.dumps({"error": "usage: extract_ffx_invocations.py <file>"}))
        return 2
    try:
        with open(argv[1], "r", encoding="utf-8", errors="replace") as f:
            src = f.read()
        result = extract(src)
    except SyntaxError as e:
        print(json.dumps({"error": f"syntax error: {e}"}))
        return 1
    except Exception as e:  # pragma: no cover - defensive
        print(json.dumps({"error": f"{type(e).__name__}: {e}"}))
        return 1
    print(json.dumps(result))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
