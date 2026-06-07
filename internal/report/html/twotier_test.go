package html

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheaf-data/sheaf/internal/corpus"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
	coveragepb "github.com/sheaf-data/sheaf/proto/coverage"
)

// mkTwoTierCorpus builds a corpus with one deterministic element (a normal
// unit-test edge, no/unset provenance) and one LLM-inferred element (an
// LlmInferred test edge plus RowProvenance_LLM provenance). It is fully
// offline — no LLM client, no network, no API key.
func mkTwoTierCorpus() *corpus.Corpus {
	c := corpus.New()

	// (a) Deterministic element: real unit test, provenance left unset.
	c.AddElement(&contractpb.ContractElement{
		Id:      "demo/Det.Method",
		Kind:    contractpb.ContractElementKind_METHOD,
		Library: "demo",
	})
	c.SetProfile(&coveragepb.CoverageProfile{
		ElementId: "demo/Det.Method",
		Tests: &coveragepb.TestCoverage{
			Unit: []*commonpb.TestRef{{
				TestName:  "Det_MethodWorks",
				Path:      "src/det_test.cc",
				Line:      10,
				Framework: "gtest",
			}},
		},
	})

	// (b) LLM-inferred element: LlmInferred test edge + LLM provenance.
	c.AddElement(&contractpb.ContractElement{
		Id:         "demo/Llm.Method",
		Kind:       contractpb.ContractElementKind_METHOD,
		Library:    "demo",
		Provenance: &commonpb.RowProvenance{Tier: commonpb.RowProvenance_LLM, Source: "llm"},
	})
	c.SetProfile(&coveragepb.CoverageProfile{
		ElementId: "demo/Llm.Method",
		Tests: &coveragepb.TestCoverage{
			LlmInferred: []*commonpb.TestRef{{
				TestName:  "Llm_MethodMaybeExercised",
				Path:      "src/llm_guess_test.cc",
				Line:      42,
				Framework: "gtest",
			}},
		},
		Docs: &coveragepb.DocCoverage{
			LlmInferred: []*commonpb.DocRef{{
				Path: "docs/guessed.md",
				Line: 7,
			}},
		},
	})

	return c
}

// TestTwoTier_IndexSplitsCounts asserts the masthead splits the deterministic
// and LLM tiers and that the deterministic TestedCount excludes LLM edges.
func TestTwoTier_IndexSplitsCounts(t *testing.T) {
	c := mkTwoTierCorpus()

	elems := buildElementSummaries(c, nil)
	det, llm := countByTier(elems)
	if det != 1 {
		t.Errorf("deterministic tier count = %d, want 1", det)
	}
	if llm != 1 {
		t.Errorf("llm tier count = %d, want 1", llm)
	}

	// The deterministic Tested count must NOT include the LLM-inferred edge:
	// only the one element with a real unit test counts.
	_, tested := countDocumentedTested(elems)
	if tested != 1 {
		t.Errorf("deterministic TestedCount = %d, want 1 (LLM edge must not inflate it)", tested)
	}

	// And the LLM element's own TestsCount (deterministic) is zero while its
	// LLMTestsCount is one.
	for _, e := range elems {
		switch e.ID {
		case "demo/Llm.Method":
			if e.TestsCount != 0 {
				t.Errorf("LLM element TestsCount = %d, want 0", e.TestsCount)
			}
			if e.LLMTestsCount != 1 {
				t.Errorf("LLM element LLMTestsCount = %d, want 1", e.LLMTestsCount)
			}
			if e.Tier != "llm" {
				t.Errorf("LLM element Tier = %q, want \"llm\"", e.Tier)
			}
		case "demo/Det.Method":
			if e.TestsCount != 1 {
				t.Errorf("Det element TestsCount = %d, want 1", e.TestsCount)
			}
			if e.Tier != "deterministic" {
				t.Errorf("Det element Tier = %q, want \"deterministic\"", e.Tier)
			}
		}
	}
}

// TestTwoTier_RenderedPages asserts the rendered HTML reflects the tier split:
// the index masthead reports the counts, the LLM element page carries the
// flagged LLM-inferred section, and the deterministic element page does not.
func TestTwoTier_RenderedPages(t *testing.T) {
	dir := t.TempDir()
	w := &Writer{Project: "twotier", OutDir: dir}
	c := mkTwoTierCorpus()
	if _, err := w.Write(c, nil); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Index masthead must surface both tier labels.
	idx, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	for _, want := range []string{"Deterministic", "LLM-inferred"} {
		if !strings.Contains(string(idx), want) {
			t.Errorf("index.html missing tier label %q", want)
		}
	}

	matches, _ := filepath.Glob(filepath.Join(dir, "elements", "*.html"))
	if len(matches) != 2 {
		t.Fatalf("element pages = %d, want 2", len(matches))
	}

	var sawLLM, sawDet bool
	for _, m := range matches {
		body, err := os.ReadFile(m)
		if err != nil {
			t.Fatalf("read %s: %v", m, err)
		}
		s := string(body)
		switch {
		case strings.Contains(s, "Llm.Method"):
			sawLLM = true
			// The flagged, unverified LLM-inferred section must be present.
			for _, want := range []string{
				"LLM-inferred tests",
				"Llm_MethodMaybeExercised",
				"unverified",
			} {
				if !strings.Contains(s, want) {
					t.Errorf("LLM element page missing %q", want)
				}
			}
		case strings.Contains(s, "Det.Method"):
			sawDet = true
			// The deterministic element must NOT show the LLM-inferred section.
			if strings.Contains(s, "LLM-inferred tests") {
				t.Errorf("deterministic element page unexpectedly shows LLM-inferred tests section")
			}
		}
	}
	if !sawLLM {
		t.Errorf("no element page contained Llm.Method")
	}
	if !sawDet {
		t.Errorf("no element page contained Det.Method")
	}
}

// TestTwoTier_TierOf checks tierOf maps provenance to the report's tier label,
// including the unset-provenance case (which must read as deterministic).
func TestTwoTier_TierOf(t *testing.T) {
	if got := tierOf(&commonpb.RowProvenance{Tier: commonpb.RowProvenance_LLM}); got != "llm" {
		t.Errorf("tierOf(LLM) = %q, want \"llm\"", got)
	}
	if got := tierOf(&commonpb.RowProvenance{Tier: commonpb.RowProvenance_DETERMINISTIC}); got != "deterministic" {
		t.Errorf("tierOf(DETERMINISTIC) = %q, want \"deterministic\"", got)
	}
	if got := tierOf(nil); got != "deterministic" {
		t.Errorf("tierOf(nil) = %q, want \"deterministic\"", got)
	}
}
