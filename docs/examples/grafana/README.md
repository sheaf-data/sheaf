# Grafana scan example — OpenAPI surface from upstream

This directory holds the recipe behind [`example-reports/grafana.html`](../../../example-reports/grafana.html). Unlike the cobra-shaped self-scan or kubectl examples, the Grafana surface is an HTTP REST API described by an OpenAPI 3 spec — so the scanner's normal cobra / FIDL / proto adapters don't apply. Instead, [`build_grafana_snapshot.py`](build_grafana_snapshot.py) reads Grafana's published `api-merged.json` directly, attributes coverage receipts against the live file tree of `github.com/grafana/grafana@main`, and writes a Sheaf `Snapshot` JSON that `sheaf render --from-snapshot` turns into the report.

## What it scans

| Surface | Source |
|---|---|
| Endpoints (321) | `public/api-merged.json` in [grafana/grafana](https://github.com/grafana/grafana) — every `(path, verb)` pair under `paths`. |
| Resources (42 tags) | The OpenAPI `tags` each operation declares. |
| Concept docs | Real files under `docs/sources/developer-resources/api-reference/http-api/` matched per-tag by basename (with an explicit overrides table for cases like `provisioning` → `alerting_provisioning.md`). |
| Tests | Real files under `pkg/api/*_test.go` matched per-tag, plus per-tag overrides for tags whose tests live elsewhere. |
| Worked examples | Real files under `pkg/tests/api/<tag>/`, `docs/.../examples/`, and `devenv/`. |

Matching is **strict** — a doc / test / example only attributes to a tag when its filename stem is in a deliberately-enumerated candidate set for that tag. Tags with no genuine match show as ABSENT rather than guessing.

## Reproducing the report

From the repo root:

```sh
# 1. Pull the upstream OpenAPI spec + file tree (the script reads both).
curl -sL https://raw.githubusercontent.com/grafana/grafana/main/public/api-merged.json -o /tmp/grafana-api.json
curl -sL "https://api.github.com/repos/grafana/grafana/git/trees/main?recursive=1" -o /tmp/grafana-tree.json

# 2. Rebuild the snapshot.
python3 docs/examples/grafana/build_grafana_snapshot.py
# (writes /tmp/grafana-snapshot.json — copy over the committed one if you want it tracked)

# 3. Render the report.
go build -o sheaf ./cmd/sheaf
./sheaf render \
  --from-snapshot docs/examples/grafana/grafana-snapshot.json \
  --library grafana --ecosystem openapi \
  --source-url-template 'https://github.com/grafana/grafana/blob/main/{path}#L{line}' \
  --commit "$(curl -sL https://api.github.com/repos/grafana/grafana/commits/main | python3 -c 'import json,sys; print(json.load(sys.stdin)[\"sha\"][:7])')" \
  -o example-reports/grafana.html
```

The script's `DOC_OVERRIDES`, `TEST_OVERRIDES`, and `EXAMPLE_OVERRIDES` tables are the only manual content — each entry is verified against the actual tree at commit time. Re-running against a future Grafana main will re-derive the same shape; if upstream renames a doc, the matching tag will quietly fall back to ABSENT until the override is updated.
