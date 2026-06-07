package conceptdocs

import (
	"os"
	"strings"
	"testing"

	"github.com/sheaf-data/sheaf/internal/grounding"
)

func TestRender_FuchsiaContent(t *testing.T) {
	v := BuildView(loadSample(t))
	out, err := RenderString(v)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := []string{
		`<title>Sheaf — Concept docs · fuchsia.driver.framework</title>`,
		`Where the docs explain fuchsia.driver.framework — and where they go dark`,
		`<span class="strip-lib">fuchsia.driver.framework</span>`,
		`<div class="s c-clear">`,
		`<div class="s c-amb">`,
		`<div class="s c-silent">`,
		`<small> / 67</small>`,        // total denominator, never a %
		`Driver framework`,            // a humanized doc-card title
		`…/Node`,                      // a resolved-element chip
		`48 silent`,                   // the Types silent-group header
		`56 of 67`,                    // the silent view-meta scope clause
		`<span class="k">type</span>`, // a silent chip kind tag
		`class="schip-overflow"`,      // Types (48 > 12) folds the rest into an expander
		`class="silent-more"`,         // ...revealed by a green arrow link
		`onclick="toggleSilent(this)"`,
	}
	for _, s := range want {
		if !strings.Contains(out, s) {
			t.Errorf("rendered HTML missing %q", s)
		}
	}
	// This lens has no percentage and no failure color: the % glyph must not
	// appear on a count, and the red --gap token must not be referenced.
	if strings.Contains(out, "<small>%</small>") || strings.Contains(out, "--gap") {
		t.Errorf("rendered HTML leaked a percentage or a red/failure token")
	}
}

// The header strip embeds the real sheaf logo (a base64 PNG data URI) — not the
// old CSS-gradient placeholder — and renders the git sha when the report carries
// a commit, mirroring the coverage report's header.
func TestRender_HeaderLogoAndSha(t *testing.T) {
	rep := &grounding.Report{
		Library: "x.y",
		Commit:  "9f3c2a1",
		Summary: grounding.Summary{ElementsTotal: 1, ElementsNotMentioned: 1},
		Elements: []grounding.Element{
			{ElementID: "x.y/Z", Display: "Z", Kind: "TYPE", State: grounding.StateNotMentioned},
		},
	}
	out, err := RenderString(BuildView(rep))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(out, `<img class="strip-logo" src="data:image/png;base64,`) {
		t.Errorf("header missing embedded logo <img> (expected the base64 data URI, not the gradient placeholder)")
	}
	if strings.Contains(out, `<span class="strip-logo">`) {
		t.Errorf("header still renders the gradient placeholder span")
	}
	if !strings.Contains(out, `sha:9f3c2a1`) {
		t.Errorf("header missing sha for a report carrying a commit")
	}
}

// Untrusted excerpt/token text must be HTML-escaped — never injected raw.
func TestRender_EscapesUntrustedText(t *testing.T) {
	sp := func(s string) *string { return &s }
	rep := &grounding.Report{
		Library: "x.y",
		Summary: grounding.Summary{ElementsTotal: 1, ElementsGuessing: 1},
		Elements: []grounding.Element{
			{ElementID: "x.y/Z", Display: "Z", Kind: "TYPE", State: grounding.StateGuessing},
		},
		Findings: []grounding.Finding{{
			ID: "f1", ElementID: "x.y/Z", ElementDisplay: "Z", State: grounding.StateGuessing,
			SourcePath: "docs/a.md", Line: 3,
			Excerpt: `see <script>alert(1)</script> here`,
			Token:   `<script>`,
			Candidates: []grounding.Candidate{
				{ElementID: sp("x.y/Z"), Label: "Z", Kind: "contract", IsContract: true},
			},
		}},
	}
	out, err := RenderString(BuildView(rep))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(out, "<script>alert") {
		t.Errorf("excerpt was not escaped — raw <script> leaked into output")
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Errorf("expected escaped token in output")
	}
}

func TestRender_EmptyView(t *testing.T) {
	out, err := RenderString(BuildView(nil))
	if err != nil {
		t.Fatalf("Render(empty): %v", err)
	}
	if !strings.Contains(out, "<!DOCTYPE html>") {
		t.Errorf("empty render is not a complete document")
	}
}

// Gated helper: dump the rendered sample to a path for visual review.
//
//	SHEAF_DUMP_CONCEPTDOCS=/path/out.html go test ./internal/report/conceptdocs/ -run DumpSample
func TestRender_DumpSample(t *testing.T) {
	out := os.Getenv("SHEAF_DUMP_CONCEPTDOCS")
	if out == "" {
		t.Skip("set SHEAF_DUMP_CONCEPTDOCS=<path> to dump the rendered sample")
	}
	s, err := RenderString(BuildView(loadSample(t)))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := os.WriteFile(out, []byte(s), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("wrote %s (%d bytes)", out, len(s))
}
