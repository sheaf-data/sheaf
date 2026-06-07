// HTML render of the PR-comment artifact.
//
// Reads a DeltaArtifact (typically parsed from a checked-in
// delta.json) and produces a self-contained HTML page styled to
// resemble a GitHub PR comment. Single file, inline CSS, no external
// assets. Per the design (§3.2 / §11), this is a minimal wrapper
// around the markdown body, not a literal mimic of GitHub chrome.

package prbot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"os"
	"strings"
)

// LoadDeltaArtifact reads and unmarshals a delta.json file.
func LoadDeltaArtifact(path string) (*DeltaArtifact, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var a DeltaArtifact
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &a, nil
}

// RenderHTML produces the comment.html bytes from a DeltaArtifact.
//
// The output is deterministic given the same artifact: the only
// non-deterministic bit (a "rendered at" timestamp in the header) is
// derived from `a.ScannedAt` so re-renders of the same JSON produce
// byte-identical output (A5).
func RenderHTML(a *DeltaArtifact) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeHTML(&buf, a); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeHTML(w io.Writer, a *DeltaArtifact) error {
	title := fmt.Sprintf("Sheaf review · %s", a.PRRefDisplayed)
	if a.PRRefDisplayed == "" {
		title = "Sheaf review"
	}
	body := mdToHTML(a.Body)

	const tmpl = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>%s — Sheaf PR review</title>
<style>
  * { box-sizing: border-box; }
  body { font: 14px/1.5 -apple-system, BlinkMacSystemFont, "Segoe UI", system-ui, sans-serif;
         margin: 0; color: #1d1d1f; background: #f6f8fa; }
  .page { max-width: 880px; margin: 0 auto; padding: 24px 16px 48px; }
  .caption { color: #57606a; font-size: 12px; margin: 0 4px 12px; }
  .caption code { background: #eaeef2; padding: 1px 6px; border-radius: 3px; }
  .comment { background: #fff; border: 1px solid #d0d7de; border-radius: 6px; overflow: hidden; }
  .comment header { display: flex; align-items: center; gap: 10px;
                    padding: 10px 16px; background: #f6f8fa;
                    border-bottom: 1px solid #d0d7de; font-size: 13px; color: #57606a; }
  .avatar { width: 28px; height: 28px; border-radius: 50%%; background: #1f6feb;
            color: #fff; display: inline-flex; align-items: center; justify-content: center;
            font-weight: 600; font-size: 13px; }
  .who { color: #1d1d1f; font-weight: 600; }
  .when { color: #57606a; }
  .body { padding: 16px 20px; }
  .body h2, .body h3, .body h4 { line-height: 1.25; }
  .body h2 { font-size: 18px; margin: 0 0 12px; padding-bottom: 8px; border-bottom: 1px solid #eaeef2; }
  .body h3 { font-size: 16px; margin: 18px 0 6px; }
  .body p { margin: 8px 0; }
  .body ul { margin: 6px 0 10px 0; padding-left: 24px; }
  .body li { margin: 2px 0; }
  .body code { font: 13px/1.4 ui-monospace, SFMono-Regular, Menlo, monospace;
               background: #eaeef2; padding: 1px 5px; border-radius: 3px; }
  .body strong { color: #1d1d1f; }
  .meta-row { color: #57606a; font-size: 12px; padding: 8px 20px 16px; border-top: 1px solid #eaeef2; }
  .meta-row .k { color: #1d1d1f; font-weight: 600; margin-right: 4px; }
  footer { color: #8c959f; font-size: 11px; text-align: center; margin-top: 16px; }
  .footer-link { color: #8c959f; }
</style>
</head>
<body>
<div class="page">
  <p class="caption">This is what the bot would render as a PR comment on <code>%s</code>.</p>
  <article class="comment">
    <header>
      <span class="avatar">S</span>
      <span class="who">sheaf-bot</span>
      <span class="when">commented on %s</span>
    </header>
    <div class="body">
%s
    </div>
    <div class="meta-row">
      <span class="k">Base:</span><code>%s</code>
      <span class="k" style="margin-left:14px">Head:</span><code>%s</code>
      <span class="k" style="margin-left:14px">Affected:</span>%d
      <span class="k" style="margin-left:14px">Findings:</span>%d
    </div>
  </article>
  <footer>sheaf · renderer v%d · schema v%s</footer>
</div>
</body>
</html>
`
	fmt.Fprintf(w, tmpl,
		html.EscapeString(title),
		html.EscapeString(a.PRRefDisplayed),
		html.EscapeString(a.ScannedAt),
		body,
		html.EscapeString(a.BaseShort),
		html.EscapeString(a.HeadShort),
		len(a.CoverageDelta.AffectedElements),
		len(a.Findings),
		a.RendererVersion,
		html.EscapeString(a.SchemaVersion),
	)
	return nil
}

// mdToHTML is a tiny, opinionated markdown→HTML converter, sufficient
// for the subset of markdown the prbot renderer actually emits today:
// `## ` / `### ` headers, paragraphs, `- ` bullets, **bold**, `code`.
// It is deliberately not a full CommonMark engine — the renderer's
// output is narrow and stable, and pulling in a third-party renderer
// just for this artifact is more dependency than the savings warrant.
func mdToHTML(md string) string {
	var out strings.Builder
	lines := strings.Split(md, "\n")
	inList := false
	flushList := func() {
		if inList {
			out.WriteString("</ul>\n")
			inList = false
		}
	}
	for _, ln := range lines {
		trim := strings.TrimRight(ln, " \t")
		switch {
		case strings.HasPrefix(trim, "## "):
			flushList()
			out.WriteString("<h2>")
			out.WriteString(inlineMD(strings.TrimPrefix(trim, "## ")))
			out.WriteString("</h2>\n")
		case strings.HasPrefix(trim, "### "):
			flushList()
			out.WriteString("<h3>")
			out.WriteString(inlineMD(strings.TrimPrefix(trim, "### ")))
			out.WriteString("</h3>\n")
		case strings.HasPrefix(trim, "- "):
			if !inList {
				out.WriteString("<ul>\n")
				inList = true
			}
			out.WriteString("  <li>")
			out.WriteString(inlineMD(strings.TrimPrefix(trim, "- ")))
			out.WriteString("</li>\n")
		case trim == "":
			flushList()
		default:
			flushList()
			out.WriteString("<p>")
			out.WriteString(inlineMD(trim))
			out.WriteString("</p>\n")
		}
	}
	flushList()
	return out.String()
}

// inlineMD handles inline `code`, **bold**, and HTML-escaping for
// everything else. Order matters: we escape first, then re-introduce
// the inline markup so user content can't smuggle HTML.
func inlineMD(s string) string {
	esc := html.EscapeString(s)
	// `code` — non-greedy, no nesting.
	esc = replacePairs(esc, "`", "<code>", "</code>")
	// **bold** — non-greedy.
	esc = replacePairs(esc, "**", "<strong>", "</strong>")
	return esc
}

// replacePairs swaps matched pairs of `marker` for the open/close
// tags. Unmatched trailing markers are left in place; the renderer's
// output should not produce them.
func replacePairs(s, marker, open, close string) string {
	var out strings.Builder
	parts := strings.Split(s, marker)
	for i, p := range parts {
		if i == 0 {
			out.WriteString(p)
			continue
		}
		if i%2 == 1 {
			out.WriteString(open)
		} else {
			out.WriteString(close)
		}
		out.WriteString(p)
	}
	// Odd number of markers → trailing unclosed: re-emit the marker.
	if len(parts)%2 == 0 && len(parts) > 1 {
		// Even count of parts means odd count of markers; the last
		// open above is unmatched. Replace it back to the literal
		// marker for visual fidelity rather than emitting broken
		// HTML.
		s2 := out.String()
		idx := strings.LastIndex(s2, open)
		if idx >= 0 {
			s2 = s2[:idx] + marker + s2[idx+len(open):]
		}
		return s2
	}
	return out.String()
}
