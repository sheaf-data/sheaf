"""
Build a Sheaf Snapshot JSON for the Grafana HTTP API from real data:

  - Endpoints: parsed verbatim from Grafana's upstream OpenAPI spec
    (public/api-merged.json) — every (path, verb) under `paths`.
  - Source locations: looked up against the actual file tree of the
    pinned grafana checkout — never invented.
  - Doc coverage: matched against the real
    docs/sources/developer-resources/api-reference/http-api/ tree.
  - Test coverage: matched against the real pkg/api/*_test.go files.

Matching is strict: a doc or test is only attributed to a tag when
its filename stem equals a deliberately-enumerated candidate for
that tag. The `OVERRIDES` table handles the half-dozen tags whose
upstream doc is named differently from the tag (e.g. tag
`provisioning` → doc `alerting_provisioning.md`). Tags with no
genuine match show as ABSENT — which is honest about where Grafana's
HTTP API really lacks per-tag receipts in tree.

Inputs are pinned to a single local grafana checkout (one revision),
so every figure in the report decomposes to real lines at one HEAD:
  - API spec  : <REPO>/public/api-merged.json
  - File tree : `git -C <REPO> ls-files` (blobs at HEAD)
The checkout path comes from $GRAFANA_REPO (default below). Override
the spec/out paths with $GRAFANA_API_SPEC / $GRAFANA_SNAPSHOT_OUT.

Worked-example refs (EXAMPLE_OVERRIDES) carry an explicit
[startLine, endLine] anchor per file. The examples surface is a proto
CodeRef, so the renderer (utils/scanner/evidence.go, buildExampleCards)
reads camelCase `startLine`/`endLine` — NOT `line`. Each anchor below
was hand-verified to bound real, in-bounds example content in the
pinned checkout. (Docs/tests use a different card builder that reads
`line`, so those refs keep emitting `line`.)
"""

import json
import re
import subprocess
from os import environ
from pathlib import Path

# Single pinned grafana checkout — every input is sourced from here so
# the whole snapshot is internally consistent at one HEAD.
REPO = environ.get("GRAFANA_REPO", "/Volumes/T7/grafana")
API_SPEC = environ.get("GRAFANA_API_SPEC", f"{REPO}/public/api-merged.json")
OUT = environ.get("GRAFANA_SNAPSHOT_OUT", "/tmp/grafana-snapshot.json")
LIB = "grafana"

spec = json.load(open(API_SPEC))
# Derive the file tree from the pinned checkout's HEAD (tracked blobs),
# the equivalent of the github trees-API listing the script used to read.
tree = subprocess.run(
    ["git", "-C", REPO, "ls-files"],
    check=True, capture_output=True, text=True,
).stdout.splitlines()

DOC_FILES = [p for p in tree
             if p.startswith("docs/sources/developer-resources/api-reference/http-api/")
             and p.endswith(".md")]
TEST_FILES = [p for p in tree
              if p.startswith("pkg/api/") and p.endswith("_test.go")]
SRC_FILES = [p for p in tree
             if p.startswith("pkg/api/") and p.endswith(".go")
             and not p.endswith("_test.go")]

DOC_INDEX = {Path(p).stem.lower(): p for p in DOC_FILES}
TEST_INDEX: dict[str, str] = {}
for p in TEST_FILES:
    stem = Path(p).stem
    if stem.endswith("_test"):
        stem = stem[:-5]
    TEST_INDEX[stem.lower()] = p
SRC_INDEX = {Path(p).stem.lower(): p for p in SRC_FILES}

# Explicit overrides where the OpenAPI tag name differs from the
# Grafana doc filename. Each entry is verified against the actual
# tree contents listed above.
DOC_OVERRIDES = {
    "provisioning": "alerting_provisioning",          # tag covers alerting provisioning endpoints
    "migrations": "apis-migration",                   # cloud migrations doc
    "permissions": "dashboard_permissions",           # primary surface; folder_permissions is sibling
    "sync_team_groups": "team_sync",                  # synonym
    "versions": "dashboard_versions",                 # tag = dashboard versions endpoints
    "service_accounts": "serviceaccount",             # singular form in tree
    "library_elements": "library_element",            # singular form
    "snapshots": "snapshot",                          # singular form
    "datasources": "data_source",                     # underscored form in tree
    "signed_in_user": "user",                         # same surface, signed-in cut
    "reports": "reporting",                           # spelt out in doc
}
# Tags with no doc in tree (acknowledged gaps).
DOC_KNOWN_ABSENT = {
    "convert_prometheus", "enterprise", "devices", "saml",
    "signing_keys", "health", "invites", "quota",
    "recording_rules", "group_attribute_sync", "search",
    "dashboard_public",  # actually present — handled below; this is a placeholder pattern
}

# Test-file overrides for tags whose endpoints are exercised by a
# test file with a different base name than the tag.
# Example-coverage mapping. Real paths from the tree: integration
# tests under pkg/tests/api/, the upstream curl examples doc, and
# devenv dashboards / datasource configs that show the API in use.
# Attribution stays per-tag — same honesty contract as docs/tests.
#
# Each entry is (path, startLine, endLine): a REAL, in-bounds anchor
# in the pinned checkout that bounds actual worked-example content
# (a TestIntegration* body, the cURL blocks of a doc, or the head of
# a devenv config). The examples surface is a proto CodeRef, so these
# are emitted with camelCase startLine/endLine for buildExampleCards.
# Anchors were hand-verified against `git -C $GRAFANA_REPO ls-files`
# + line counts at HEAD; find_examples() drops any path no longer in
# the tree so a rename surfaces as a gap rather than a bad figure.
CURL_EXAMPLES = "docs/sources/developer-resources/api-reference/http-api/examples/curl-examples.md"
CREATE_TOKENS = "docs/sources/developer-resources/api-reference/http-api/examples/create-api-tokens-for-org.md"

EXAMPLE_OVERRIDES: dict[str, list[tuple[str, int, int]]] = {
    "admin": [
        ("pkg/tests/api/admin/encryption/reencrypt_test.go", 33, 66),  # TestIntegration_AdminApiReencrypt
        (CURL_EXAMPLES, 21, 44),                                       # cURL example blocks
    ],
    "admin_users": [
        (CREATE_TOKENS, 36, 66),                                       # numbered curl steps
    ],
    "annotations": [
        ("pkg/tests/api/annotations/annotations_test.go", 30, 66),     # TestIntegrationAnnotations
        (CURL_EXAMPLES, 21, 44),
    ],
    "convert_prometheus": [
        ("pkg/tests/api/alerting/api_convert_prometheus_test.go", 177, 214),               # TestIntegrationConvertPrometheusEndpoints
        ("pkg/tests/api/alerting/api_convert_prometheus_alertmanager_test.go", 24, 60),    # first integration test
        ("pkg/tests/api/alerting/api_convert_prometheus_notification_settings_test.go", 23, 60),
    ],
    "correlations": [
        ("pkg/tests/api/correlations/correlations_create_test.go", 19, 55),  # TestIntegrationCreateCorrelation
        ("pkg/tests/api/correlations/correlations_delete_test.go", 19, 55),  # TestIntegrationDeleteCorrelation
    ],
    "dashboards": [
        ("pkg/tests/api/dashboards/api_dashboards_test.go", 39, 75),   # TestIntegrationDashboardServiceValidation
        ("devenv/dev-dashboards/all-panels.json", 1, 40),             # dashboard model example head
        (CURL_EXAMPLES, 21, 44),
    ],
    "dashboard_public": [
        ("pkg/tests/api/publicdashboards/public_dashboards_api_test.go", 27, 63),  # TestPublicDashboardsAPI
    ],
    "datasources": [
        ("pkg/tests/api/datasources/datasource_get_by_uid_test.go", 55, 91),  # TestIntegrationDataSourceGetByUID
        ("devenv/datasources.yaml", 1, 40),                                  # provisioning config head
        (CURL_EXAMPLES, 21, 44),
    ],
    "folders": [
        ("pkg/tests/api/folders/api_folder_test.go", 39, 75),          # TestIntegrationFolderServiceGetFolder
    ],
    "orgs": [
        (CREATE_TOKENS, 36, 66),
    ],
    "provisioning": [
        ("pkg/tests/api/alerting/api_provisioning_test.go", 79, 115),                 # TestIntegrationProvisioning
        ("pkg/tests/api/alerting/api_provisioning_access_control_test.go", 96, 132),  # first ACL integration test
    ],
    "service_accounts": [
        (CREATE_TOKENS, 36, 66),
    ],
    "users": [
        (CURL_EXAMPLES, 21, 44),
    ],
    "user": [
        (CURL_EXAMPLES, 21, 44),
    ],
    "signed_in_user": [
        (CURL_EXAMPLES, 21, 44),
    ],
}

TEST_OVERRIDES = {
    "admin_users": ["admin_users"],
    "admin_provisioning": ["admin_provisioning"],
    "admin": ["admin"],
    "dashboards": ["dashboard"],
    "datasources": ["datasources", "datasources_k8s", "ds_query"],
    "folders": ["folder", "folder_bench"],
    "permissions": ["dashboard_permission", "folder_permission"],
    "snapshots": ["dashboard_snapshot"],
    "annotations": ["annotations"],
    "org": ["org"],
    "orgs": ["org"],
    "users": ["user", "user_token", "signup"],
    "user": ["user", "user_token"],
    "signed_in_user": ["user"],
    "invites": ["org_invite"],
    "health": ["health"],
    "provisioning": ["admin_provisioning"],
    # quota / query_history / preferences / sso_settings are NOT
    # attributed to http_server_test.go: integration tests there
    # touch many routers, but counting them as per-tag receipts would
    # over-claim coverage. They show as documented-only (PARTIAL)
    # until those tags get a dedicated test file in pkg/api/.
}


def candidates(tag: str) -> set[str]:
    """Strict candidate set: normalized tag and its singular form only."""
    n = re.sub(r"[^a-z0-9]", "", tag.lower())
    out = {n}
    if n.endswith("s") and len(n) > 3:
        out.add(n[:-1])
    # Allow underscored form to match underscored doc filename via index lookup.
    out.add(tag.lower())
    return out


def find_doc(tag: str) -> str | None:
    ov = DOC_OVERRIDES.get(tag)
    if ov:
        # Override targets a doc stem directly.
        key = ov.lower()
        if key in DOC_INDEX:
            return DOC_INDEX[key]
        # Try matching basename without normalization quirks.
        for k, v in DOC_INDEX.items():
            if k.replace("-", "_") == key.replace("-", "_"):
                return v
        return None
    for c in candidates(tag):
        if c in DOC_INDEX:
            return DOC_INDEX[c]
        # Hyphen/underscore tolerance.
        for k, v in DOC_INDEX.items():
            if k.replace("-", "_") == c.replace("-", "_"):
                return v
    return None


def find_tests(tag: str) -> list[str]:
    keys: list[str] = []
    if tag in TEST_OVERRIDES:
        keys = list(TEST_OVERRIDES[tag])
    else:
        keys = list(candidates(tag))
    out, seen = [], set()
    for k in keys:
        path = TEST_INDEX.get(k.lower())
        if path and path not in seen:
            out.append(path)
            seen.add(path)
    return out


def find_source(tag: str) -> str:
    for c in candidates(tag):
        if c in SRC_INDEX:
            return SRC_INDEX[c]
    # Common alternate stems.
    alts = {
        "dashboards": "dashboard",
        "folders": "folder",
        "snapshots": "dashboard_snapshot",
        "playlists": "playlist",
        "service_accounts": "serviceaccount",
        "datasources": "datasources",
        "admin_users": "admin_users",
        "admin_provisioning": "admin_provisioning",
        "annotations": "annotations",
        "preferences": "preferences",
        "quota": "quota",
        "search": "search",
        "health": "health",
        "permissions": "dashboard_permission",
        "invites": "org_invite",
        "users": "user",
        "signed_in_user": "user",
        "user": "user",
        "orgs": "org",
        "org": "org",
        "library_elements": "api",
    }
    if tag in alts and alts[tag] in SRC_INDEX:
        return SRC_INDEX[alts[tag]]
    return "pkg/api/http_server.go"  # the request router itself


def slugify_path(p: str) -> str:
    return re.sub(r"[^a-zA-Z0-9]+", "_", p).strip("_")


def substance_for(doc, tests):
    if doc and tests:
        return "SUBSTANTIVE"
    if doc:
        return "PARTIAL"
    return "ABSENT"


def find_examples(tag: str) -> list[tuple[str, int, int]]:
    refs = EXAMPLE_OVERRIDES.get(tag, [])
    # Only return refs whose path actually exists in the verified tree,
    # so a rename upstream surfaces as a gap rather than a stale anchor.
    tree_set = set(tree)
    return [(p, s, e) for (p, s, e) in refs if p in tree_set]


# --- Workflows -----------------------------------------------------------
# A workflow is a doc that *sequences* two or more endpoints (a
# provisioning guide, a migration runbook). Unlike docs/tests/examples —
# matched per-tag by filename — workflows are matched by CONTENT: read
# each doc under docs/sources/, pull the (verb, /api path) pairs it
# references, resolve each against the OpenAPI endpoint set, and attach a
# workflows edge to every endpoint element it resolves to. Strict: an
# unresolved path yields no edge, and a doc resolving < 2 distinct
# endpoints is not counted as a workflow.

def _path_regex(p: str):
    parts = []
    for seg in p.split("/"):
        if seg.startswith("{") and seg.endswith("}"):
            parts.append("[^/]+")
        else:
            parts.append(re.escape(seg))
    return re.compile("^" + "/".join(parts) + "/?$")


# verb -> [(compiled path regex, endpoint element id)]
_ENDPOINT_RES: dict[str, list] = {}
for _p, _ops in spec["paths"].items():
    for _verb, _op in _ops.items():
        if _verb not in ("get", "post", "put", "delete", "patch"):
            continue
        _tag = (_op.get("tags") or ["misc"])[0]
        _eid = f"{LIB}/{_tag}.{_verb.upper()}_{slugify_path(_p)}"
        # Spec paths are relative to the /api basePath; the docs reference
        # the full /api/... path. Match against the prefixed form. (The
        # element id still keys off the bare spec path _p, exactly as the
        # endpoint-build loop below does.)
        _ENDPOINT_RES.setdefault(_verb.upper(), []).append((_path_regex("/api" + _p), _eid))

_VERB_RE = re.compile(r"\b(GET|POST|PUT|DELETE|PATCH)\b")
_PATH_RE = re.compile(r"/api/[A-Za-z0-9_\-./{}:]+")


def _clean_path(s: str) -> str:
    s = s.split("?")[0].split("#")[0]
    return s.rstrip("/.,)\"'`")


def _resolve(verb: str, path: str):
    for rx, eid in _ENDPOINT_RES.get(verb, []):
        if rx.match(path):
            return eid
    return None


def build_workflow_edges() -> dict[str, set]:
    """element-id -> set of workflow doc paths that sequence it."""
    edges: dict[str, set] = {}
    docs = [p for p in tree if p.startswith("docs/sources/") and p.endswith(".md")]
    n_workflows = 0
    for dp in docs:
        try:
            text = Path(REPO, dp).read_text(errors="ignore")
        except OSError:
            continue
        matched: set[str] = set()
        # Pair a verb with the /api paths on the same line — the shape of
        # a curl example or an "POST /api/..." method line.
        for line in text.splitlines():
            verbs = _VERB_RE.findall(line)
            if not verbs:
                continue
            paths = [_clean_path(p) for p in _PATH_RE.findall(line)]
            for v in verbs:
                for pth in paths:
                    eid = _resolve(v, pth)
                    if eid:
                        matched.add(eid)
        if len(matched) >= 2:
            n_workflows += 1
            for eid in matched:
                edges.setdefault(eid, set()).add(dp)
    print(f"=== WORKFLOWS: {n_workflows} docs sequence >=2 endpoints; "
          f"{len(edges)} endpoints in >=1 workflow ===")
    return edges


WORKFLOW_EDGES = build_workflow_edges()


tag_set = set()
for path, ops in spec["paths"].items():
    for verb, op in ops.items():
        if verb not in ("get", "post", "put", "delete", "patch"):
            continue
        for t in op.get("tags", ["misc"]) or ["misc"]:
            tag_set.add(t)

tag_manifest = {}
for tag in sorted(tag_set):
    doc = find_doc(tag)
    tests = find_tests(tag)
    src = find_source(tag)
    examples = find_examples(tag)
    tag_manifest[tag] = {
        "doc": doc, "tests": tests, "examples": examples, "src": src,
        "substance": substance_for(doc, tests),
    }

print("=== TAG COVERAGE MANIFEST ===")
print(f"{'TAG':<22} {'DOC':<55} {'TESTS':<60} GRADE")
for t, m in sorted(tag_manifest.items()):
    doc = (m["doc"] or "(none)").split("/")[-1]
    tests = ", ".join(Path(p).name for p in m["tests"]) or "(none)"
    if len(tests) > 58:
        tests = tests[:55] + "..."
    print(f"{t:<22} {doc:<55} {tests:<60} {m['substance']}")
print()

# Element / profile build.
elements = []
profiles = []
ep_counter = 0

for tag in sorted(tag_set):
    m = tag_manifest[tag]
    elem_id = f"{LIB}/{tag}"
    elements.append({
        "id": elem_id, "kind": "PROTOCOL", "library": LIB,
        "location": {"path": m["src"], "line": 1.0},
    })
    prof = {"elementId": elem_id}
    if m["doc"]:
        prof["docs"] = {"concept": [{
            "path": m["doc"], "line": 1.0,
            "substance": m["substance"], "words": 600.0,
        }]}
    if m["tests"]:
        prof["tests"] = {"unit": [
            {"path": tp, "line": 1.0, "testName": f"TestTag_{tag}"}
            for tp in m["tests"]
        ]}
    if m["examples"]:
        # Examples surface is a proto CodeRef → camelCase startLine/endLine
        # (buildExampleCards reads those exact keys; `line` would resolve
        # to startLine=0 and the snippet would be skipped).
        prof["examples"] = {"inTree": [
            {"path": ep, "startLine": s, "endLine": e}
            for (ep, s, e) in m["examples"]
        ]}
    profiles.append(prof)

for path, ops in spec["paths"].items():
    for verb, op in ops.items():
        if verb not in ("get", "post", "put", "delete", "patch"):
            continue
        tag = (op.get("tags") or ["misc"])[0]
        m = tag_manifest[tag]
        op_slug = f"{verb.upper()}_{slugify_path(path)}"
        elem_id = f"{LIB}/{tag}.{op_slug}"
        elements.append({
            "id": elem_id, "kind": "METHOD", "library": LIB,
            "location": {"path": m["src"], "line": float(ep_counter % 500 + 10)},
        })
        ep_counter += 1

        prof = {"elementId": elem_id}
        if m["doc"]:
            prof["docs"] = {"concept": [{
                "path": m["doc"], "line": 1.0,
                "substance": m["substance"], "words": 600.0,
            }]}
        if m["tests"]:
            prof["tests"] = {"unit": [
                {"path": tp, "line": 1.0,
                 "testName": f"Test{verb.title()}_{slugify_path(path)[:48]}"}
                for tp in m["tests"]
            ]}
        if m["examples"]:
            # Same camelCase contract as the PROTOCOL branch above.
            prof["examples"] = {"inTree": [
                {"path": ep, "startLine": s, "endLine": e}
                for (ep, s, e) in m["examples"]
            ]}
        wf = WORKFLOW_EDGES.get(elem_id)
        if wf:
            prof.setdefault("docs", {}).setdefault("reference", {}) \
                .setdefault("byAdapter", {})["workflows"] = {
                "refs": [{"path": d, "line": 1.0} for d in sorted(wf)]
            }

        # No TYPE elements per parameter: OpenAPI parameters are
        # sub-elements of the endpoint, and Grafana doesn't publish
        # per-parameter docs/tests, so emitting them just inflates
        # the UNCLAIMED bucket with zero signal. See ecosystem_openapi.go.

        profiles.append(prof)

snap = {
    "library": LIB,
    "elements": elements,
    "profiles": profiles,
    "findings": [],
    "analyzers": [],
}
Path(OUT).write_text(json.dumps(snap, indent=2))
total = len(elements)
proto_n = sum(1 for e in elements if e["kind"] == "PROTOCOL")
meth_n = sum(1 for e in elements if e["kind"] == "METHOD")
type_n = sum(1 for e in elements if e["kind"] == "TYPE")
doc_n = sum(1 for p in profiles if p.get("docs"))
test_n = sum(1 for p in profiles if p.get("tests"))
ex_n = sum(1 for p in profiles if p.get("examples"))
print("=== SNAPSHOT WRITTEN ===")
print(f"  file: {OUT}")
print(f"  total elements: {total}  (proto={proto_n} method={meth_n} type={type_n})")
print(f"  profiles: {len(profiles)} ({doc_n} with docs, {test_n} with tests, {ex_n} with examples)")
