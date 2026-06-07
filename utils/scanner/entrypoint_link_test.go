package scanner

import (
	"bytes"
	"strings"
	"testing"
)

// The link to the sibling Concept Docs report rides on the concept-doc reach
// eyebrow (cd-reach-link), wired from ConceptDocsHref when a concept-doc
// source was configured. The standalone masthead "entry-point" block that
// once carried this link was removed in favor of the inline eyebrow link, so
// this test tracks the surviving consumer and guards against the old block
// reappearing.
func TestRenderHTML_ConceptDocsLink(t *testing.T) {
	// Concept-doc source configured + an href → the reach stat is a link.
	var withLink bytes.Buffer
	if err := RenderHTML(&withLink, &ReportData{
		Library:              "demo",
		ConceptDocConfigured: true,
		ConceptDocClear:      3,
		ConceptDocsHref:      "concept-docs.html",
	}); err != nil {
		t.Fatalf("render with link: %v", err)
	}
	s := withLink.String()
	if !strings.Contains(s, `class="cd-reach-link"`) {
		t.Errorf("expected the reach-stat link when ConceptDocsHref is set")
	}
	if !strings.Contains(s, `href="concept-docs.html"`) {
		t.Errorf("expected the href to be wired into the reach-stat link")
	}

	// Concept-doc source configured but no href → the reach stat renders, but
	// not as a link (no dead link to a sibling that wasn't generated).
	var noLink bytes.Buffer
	if err := RenderHTML(&noLink, &ReportData{
		Library:              "demo",
		ConceptDocConfigured: true,
		ConceptDocClear:      3,
	}); err != nil {
		t.Fatalf("render without link: %v", err)
	}
	if strings.Contains(noLink.String(), `class="cd-reach-link"`) {
		t.Errorf("reach-stat link must be omitted when ConceptDocsHref is empty")
	}

	// The removed masthead concept-link block must not reappear.
	if strings.Contains(s, `class="concept-link"`) {
		t.Errorf("the masthead concept-link block was removed; it must not render")
	}
}
