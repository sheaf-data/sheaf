# sheaf review-html

Render a standalone `comment.html` from a `delta.json` previously emitted by [`sheaf review --emit-json`](sheaf_review.md) — no re-scan, no base + head corpora.

## Synopsis

```text
sheaf review-html --delta <delta.json> -o <output.html>
```

## Description

`sheaf review-html` is the rendering half of the PR-review pipeline. Where [`sheaf review`](sheaf_review.md) scans a base + head pair and (with `--emit-json`) writes a structured `delta.json`, `sheaf review-html` takes that `delta.json` and renders it into a self-contained `comment.html`.

Splitting the two steps lets the expensive scan run once (in CI, say) and emit a portable `delta.json` artifact, while the HTML can be regenerated cheaply from that artifact afterward — no repo checkouts required.

Both flags are required: `--delta` names the input `delta.json`, and `-o` names the HTML file to write.

## Options

| Flag | Default | Notes |
|---|---|---|
| `--delta` | *(required)* | Path to a `delta.json` previously written by `sheaf review --emit-json`. |
| `-o`      | *(required)* | Output HTML path for the rendered `comment.html`. |

## Examples

First emit the `delta.json` from a review run, then render it:

```sh
# Scan base + head and emit the structured delta artifact.
sheaf review --base /tmp/main-checkout --repo . --pr PR#4242 \
             --emit-json delta.json \
             --emit-base-ref "$BASE_SHA" --emit-head-ref "$HEAD_SHA" \
             --emit-system fd

# Render the standalone HTML comment from that artifact.
sheaf review-html --delta delta.json -o comment.html
```

## Exit codes

| Code | Meaning |
|---|---|
| `0` | `comment.html` written. |
| `1` | Delta read failed, render failed, or output write failed. |
| `2` | Missing `--delta` / `-o` or bad flag. |

## See also

- [`sheaf review`](sheaf_review.md) — produces the `delta.json` this command consumes (via `--emit-json`).
