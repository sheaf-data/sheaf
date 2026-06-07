package prbot

import (
	"context"
	"strings"
	"testing"

	"github.com/sheaf-data/sheaf/internal/corpus"
	categorizationpb "github.com/sheaf-data/sheaf/proto/categorization"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
	coveragepb "github.com/sheaf-data/sheaf/proto/coverage"
)

func mkPair() (*corpus.Corpus, *corpus.Corpus) {
	base := corpus.New()
	base.AddElement(&contractpb.ContractElement{Id: "lib/X", Kind: contractpb.ContractElementKind_METHOD})
	base.SetProfile(&coveragepb.CoverageProfile{ElementId: "lib/X"})
	base.AddElement(&contractpb.ContractElement{Id: "lib/Y", Kind: contractpb.ContractElementKind_METHOD})
	base.SetProfile(&coveragepb.CoverageProfile{ElementId: "lib/Y"})

	head := corpus.New()
	head.AddElement(&contractpb.ContractElement{Id: "lib/X", Kind: contractpb.ContractElementKind_METHOD})
	head.SetProfile(&coveragepb.CoverageProfile{
		ElementId: "lib/X",
		Tests:     &coveragepb.TestCoverage{Unit: []*commonpb.TestRef{{TestName: "T1"}}},
	})
	head.AddElement(&contractpb.ContractElement{Id: "lib/Y", Kind: contractpb.ContractElementKind_METHOD})
	head.SetProfile(&coveragepb.CoverageProfile{ElementId: "lib/Y"})
	head.AddElement(&contractpb.ContractElement{Id: "lib/Z", Kind: contractpb.ContractElementKind_METHOD})
	head.SetProfile(&coveragepb.CoverageProfile{ElementId: "lib/Z"})
	return base, head
}

func TestRender_FindingsAndAffectedElements(t *testing.T) {
	base, head := mkPair()
	rules := &categorizationpb.Rules{Version: 1}
	c, err := Render(context.Background(), "PR#42", base, head, rules)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if c.Title != "Sheaf review · PR#42" {
		t.Errorf("Title = %q", c.Title)
	}
	// X changed (added test); Z is new. Y unchanged. -> 2 affected.
	if len(c.AffectedElements) != 2 {
		t.Errorf("affected = %v, want 2", c.AffectedElements)
	}
}

func TestRender_EmptyBody_WhenNoChanges(t *testing.T) {
	base := corpus.New()
	base.AddElement(&contractpb.ContractElement{Id: "lib/X"})
	base.SetProfile(&coveragepb.CoverageProfile{ElementId: "lib/X"})
	head := corpus.New()
	head.AddElement(&contractpb.ContractElement{Id: "lib/X"})
	head.SetProfile(&coveragepb.CoverageProfile{ElementId: "lib/X"})
	rules := &categorizationpb.Rules{Version: 1}
	c, err := Render(context.Background(), "PR#1", base, head, rules)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(c.Body, "No coverage-relevant changes") {
		t.Errorf("expected empty-changes message; got %q", c.Body)
	}
}

func TestRender_BodyMentionsAffected(t *testing.T) {
	base, head := mkPair()
	rules := &categorizationpb.Rules{Version: 1}
	c, _ := Render(context.Background(), "PR#1", base, head, rules)
	if !strings.Contains(c.Body, "lib/X") || !strings.Contains(c.Body, "lib/Z") {
		t.Errorf("body missing affected elements: %s", c.Body)
	}
}
