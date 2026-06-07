package indexer

import (
	"testing"

	"github.com/sheaf-data/sheaf/internal/corpus"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
)

// TestBuild_LLMWorkflowDoesNotInflateDeterministicCount is the moat
// guard: an LLM-tier WORKFLOW claim (from internal/workflowextract) must
// NOT enter the deterministic workflow surface. A deterministic WORKFLOW
// claim referencing the same element does. After indexing, the
// "workflows" reference bucket must hold only the deterministic ref.
func TestBuild_LLMWorkflowDoesNotInflateDeterministicCount(t *testing.T) {
	c := corpus.New()
	c.AddElement(&contractpb.ContractElement{
		Id:      "sheaf/apply",
		Kind:    contractpb.ContractElementKind_SUBCOMMAND,
		Library: "sheaf",
	})

	// Deterministic workflow claim (e.g. from the `workflows` adapter).
	c.AddDocClaim(&docclaimpb.DocClaim{
		SourcePath:   "docs/recipe.md",
		Location:     &commonpb.SourceLocation{Path: "docs/recipe.md", Line: 4},
		Kind:         docclaimpb.DocClaimKind_WORKFLOW,
		Adapter:      "workflows",
		ContractRefs: []string{"sheaf/apply"},
	})
	// LLM-tier workflow claim referencing the same element — additive,
	// must be excluded from the deterministic surface.
	c.AddDocClaim(&docclaimpb.DocClaim{
		SourcePath:   "docs/tutorial.md",
		Location:     &commonpb.SourceLocation{Path: "docs/tutorial.md", Line: 9},
		Kind:         docclaimpb.DocClaimKind_WORKFLOW,
		Adapter:      "workflowextract",
		ContractRefs: []string{"sheaf/apply"},
		Provenance:   &commonpb.RowProvenance{Tier: commonpb.RowProvenance_LLM, Source: "fake"},
	})

	idx := New(c, nil)
	idx.Build()

	prof := c.Profile("sheaf/apply")
	if prof == nil {
		t.Fatal("no profile for sheaf/apply")
	}
	wf := prof.GetDocs().GetReference().GetByAdapter()["workflows"].GetRefs()
	if len(wf) != 1 {
		t.Fatalf("deterministic workflow refs = %d, want 1 (LLM claim must be excluded)", len(wf))
	}
	if got := wf[0].GetPath(); got != "docs/recipe.md" {
		t.Errorf("workflow ref path = %q, want docs/recipe.md (the deterministic claim)", got)
	}
}

// TestBuild_LLMExampleDoesNotInflateDeterministicCount is the example moat
// guard: a re-indexed LLM-tier EXAMPLE claim must NOT enter the
// deterministic example surface (in_tree/in_docs/external). Only the
// deterministic EXAMPLE claim populates in_docs.
func TestBuild_LLMExampleDoesNotInflateDeterministicCount(t *testing.T) {
	c := corpus.New()
	c.AddElement(&contractpb.ContractElement{
		Id:      "lib/NewServer",
		Kind:    contractpb.ContractElementKind_METHOD,
		Library: "lib",
	})
	// Deterministic example code-fence claim.
	c.AddDocClaim(&docclaimpb.DocClaim{
		SourcePath:   "docs/guide.md",
		Location:     &commonpb.SourceLocation{Path: "docs/guide.md", Line: 6},
		Kind:         docclaimpb.DocClaimKind_EXAMPLE,
		ContractRefs: []string{"lib/NewServer"},
	})
	// LLM-tier example claim referencing the same element.
	c.AddDocClaim(&docclaimpb.DocClaim{
		SourcePath:   "examples/server_demo.go",
		Location:     &commonpb.SourceLocation{Path: "examples/server_demo.go", Line: 5},
		Kind:         docclaimpb.DocClaimKind_EXAMPLE,
		ContractRefs: []string{"lib/NewServer"},
		Provenance:   &commonpb.RowProvenance{Tier: commonpb.RowProvenance_LLM, Source: "fake"},
	})

	idx := New(c, nil)
	idx.Build()

	ex := c.Profile("lib/NewServer").GetExamples()
	det := len(ex.GetInTree()) + len(ex.GetInDocs()) + len(ex.GetExternal())
	if det != 1 {
		t.Fatalf("deterministic example refs = %d, want 1 (LLM claim must be excluded)", det)
	}
	if got := ex.GetInDocs()[0].GetPath(); got != "docs/guide.md" {
		t.Errorf("example ref path = %q, want docs/guide.md (the deterministic claim)", got)
	}
}
