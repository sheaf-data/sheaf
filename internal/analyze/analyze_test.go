package analyze

import (
	"context"
	"strings"
	"testing"

	"github.com/sheaf-data/sheaf/internal/corpus"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
	coveragepb "github.com/sheaf-data/sheaf/proto/coverage"
	findingpb "github.com/sheaf-data/sheaf/proto/finding"
)

func mkCorpus(elems []*contractpb.ContractElement, profiles map[string]*coveragepb.CoverageProfile) *corpus.Corpus {
	c := corpus.New()
	for _, e := range elems {
		c.AddElement(e)
	}
	for _, p := range profiles {
		c.SetProfile(p)
	}
	return c
}

func TestRegistry_AllExpectedAnalyzersRegistered(t *testing.T) {
	want := []string{
		"missing-in-category", "thin-reference", "documented-untested",
		"tested-undocumented", "external-mention-only", "stale-doc",
	}
	got := RegisteredNames()
	gotSet := make(map[string]bool)
	for _, n := range got {
		gotSet[n] = true
	}
	for _, w := range want {
		if !gotSet[w] {
			t.Errorf("expected analyzer %q registered; got %v", w, got)
		}
	}
}

// ---- missing-in-category ----

func TestMissingInCategory_FiresOnEmptyConfiguredCategory(t *testing.T) {
	c := mkCorpus(
		[]*contractpb.ContractElement{{Id: "lib/M", Kind: contractpb.ContractElementKind_METHOD,
			Location: &commonpb.SourceLocation{Path: "m.fidl"}}},
		map[string]*coveragepb.CoverageProfile{
			"lib/M": {ElementId: "lib/M", Tests: &coveragepb.TestCoverage{}},
		},
	)
	a := Lookup("missing-in-category")("missing-in-category")
	findings, err := a.Analyze(context.Background(), c, Options{
		Severity: commonpb.Severity_WARNING,
		Kv:       map[string]any{"alert_for_categories": "tests.unit_tests"},
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(findings) != 1 || findings[0].GetKind() != findingpb.FindingKind_MISSING_IN_CATEGORY {
		t.Errorf("findings = %+v", findings)
	}
}

func TestMissingInCategory_QuietWhenCategoryPopulated(t *testing.T) {
	c := mkCorpus(
		[]*contractpb.ContractElement{{Id: "lib/M", Kind: contractpb.ContractElementKind_METHOD}},
		map[string]*coveragepb.CoverageProfile{
			"lib/M": {
				ElementId: "lib/M",
				Tests: &coveragepb.TestCoverage{
					Unit: []*commonpb.TestRef{{TestName: "T1"}},
				},
			},
		},
	)
	a := Lookup("missing-in-category")("missing-in-category")
	findings, _ := a.Analyze(context.Background(), c, Options{
		Kv: map[string]any{"alert_for_categories": "tests.unit_tests"},
	})
	if len(findings) != 0 {
		t.Errorf("expected no findings, got %+v", findings)
	}
}

// ---- thin-reference ----

func TestThinReference_FiresOnSignatureOnly(t *testing.T) {
	c := mkCorpus(
		[]*contractpb.ContractElement{{Id: "lib/X"}},
		map[string]*coveragepb.CoverageProfile{
			"lib/X": {
				ElementId: "lib/X",
				Docs: &coveragepb.DocCoverage{Reference: &coveragepb.DocCoverage_Reference{
					Fidldoc: []*commonpb.DocRef{{Substance: commonpb.Substance_SIGNATURE_ONLY, Url: "u"}},
				}},
			},
		},
	)
	a := Lookup("thin-reference")("thin-reference")
	findings, _ := a.Analyze(context.Background(), c, Options{Severity: commonpb.Severity_WARNING})
	if len(findings) != 1 {
		t.Errorf("findings = %+v", findings)
	}
}

func TestThinReference_QuietOnSubstantive(t *testing.T) {
	c := mkCorpus(
		[]*contractpb.ContractElement{{Id: "lib/X"}},
		map[string]*coveragepb.CoverageProfile{
			"lib/X": {
				ElementId: "lib/X",
				Docs: &coveragepb.DocCoverage{Reference: &coveragepb.DocCoverage_Reference{
					Fidldoc: []*commonpb.DocRef{{Substance: commonpb.Substance_SUBSTANTIVE}},
				}},
			},
		},
	)
	a := Lookup("thin-reference")("thin-reference")
	findings, _ := a.Analyze(context.Background(), c, Options{})
	if len(findings) != 0 {
		t.Errorf("expected none, got %+v", findings)
	}
}

// ---- documented-untested ----

func TestDocumentedUntested(t *testing.T) {
	c := mkCorpus(
		[]*contractpb.ContractElement{
			{Id: "lib/M", Kind: contractpb.ContractElementKind_METHOD},
		},
		map[string]*coveragepb.CoverageProfile{
			"lib/M": {
				ElementId: "lib/M",
				Docs: &coveragepb.DocCoverage{Reference: &coveragepb.DocCoverage_Reference{
					Fidldoc: []*commonpb.DocRef{{Substance: commonpb.Substance_SUBSTANTIVE}},
				}},
				Tests: &coveragepb.TestCoverage{},
			},
		},
	)
	a := Lookup("documented-untested")("documented-untested")
	findings, _ := a.Analyze(context.Background(), c, Options{Severity: commonpb.Severity_WARNING})
	if len(findings) != 1 || findings[0].GetKind() != findingpb.FindingKind_DOCUMENTED_UNTESTED {
		t.Errorf("findings = %+v", findings)
	}
}

// ---- tested-undocumented ----

func TestTestedUndocumented(t *testing.T) {
	c := mkCorpus(
		[]*contractpb.ContractElement{
			{Id: "lib/M", Kind: contractpb.ContractElementKind_METHOD},
		},
		map[string]*coveragepb.CoverageProfile{
			"lib/M": {
				ElementId: "lib/M",
				Tests: &coveragepb.TestCoverage{
					Unit: []*commonpb.TestRef{{TestName: "T"}},
				},
			},
		},
	)
	a := Lookup("tested-undocumented")("tested-undocumented")
	findings, _ := a.Analyze(context.Background(), c, Options{Severity: commonpb.Severity_INFO})
	if len(findings) != 1 || findings[0].GetKind() != findingpb.FindingKind_TESTED_UNDOCUMENTED {
		t.Errorf("findings = %+v", findings)
	}
}

// ---- external-mention-only ----

func TestExternalMentionOnly(t *testing.T) {
	c := mkCorpus(
		[]*contractpb.ContractElement{{Id: "lib/M", Kind: contractpb.ContractElementKind_METHOD}},
		map[string]*coveragepb.CoverageProfile{
			"lib/M": {
				ElementId: "lib/M",
				Docs: &coveragepb.DocCoverage{
					Concept: []*commonpb.DocRef{{Substance: commonpb.Substance_SIGNATURE_ONLY}},
				},
			},
		},
	)
	a := Lookup("external-mention-only")("external-mention-only")
	findings, _ := a.Analyze(context.Background(), c, Options{})
	if len(findings) != 1 {
		t.Errorf("findings = %+v", findings)
	}
}

// ---- coverage-delta ----

func TestCoverageDelta_AddedRemovedChanged(t *testing.T) {
	base := corpus.New()
	base.AddElement(&contractpb.ContractElement{Id: "lib/Removed"})
	base.SetProfile(&coveragepb.CoverageProfile{ElementId: "lib/Removed"})
	base.AddElement(&contractpb.ContractElement{Id: "lib/Same"})
	base.SetProfile(&coveragepb.CoverageProfile{
		ElementId: "lib/Same",
		Tests:     &coveragepb.TestCoverage{Unit: []*commonpb.TestRef{{TestName: "T"}}},
	})
	base.AddElement(&contractpb.ContractElement{Id: "lib/Grew"})
	base.SetProfile(&coveragepb.CoverageProfile{ElementId: "lib/Grew"})

	head := corpus.New()
	head.AddElement(&contractpb.ContractElement{Id: "lib/New"})
	head.SetProfile(&coveragepb.CoverageProfile{ElementId: "lib/New"})
	head.AddElement(&contractpb.ContractElement{Id: "lib/Same"})
	head.SetProfile(&coveragepb.CoverageProfile{
		ElementId: "lib/Same",
		Tests:     &coveragepb.TestCoverage{Unit: []*commonpb.TestRef{{TestName: "T"}}},
	})
	head.AddElement(&contractpb.ContractElement{Id: "lib/Grew"})
	head.SetProfile(&coveragepb.CoverageProfile{
		ElementId: "lib/Grew",
		Tests:     &coveragepb.TestCoverage{Unit: []*commonpb.TestRef{{TestName: "T1"}, {TestName: "T2"}}},
	})

	delta := NewCoverageDelta(base, head)
	findings, err := delta.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	bySubject := make(map[string]string)
	for _, f := range findings {
		bySubject[f.GetSubject()] = f.GetMessage()
	}
	if _, ok := bySubject["lib/New"]; !ok {
		t.Errorf("missing finding for lib/New: %+v", bySubject)
	}
	if _, ok := bySubject["lib/Removed"]; !ok {
		t.Errorf("missing finding for lib/Removed: %+v", bySubject)
	}
	if msg, ok := bySubject["lib/Grew"]; !ok || !strings.Contains(msg, "tests 0→2") {
		t.Errorf("Grew message wrong: %q in %+v", msg, bySubject)
	}
	if _, ok := bySubject["lib/Same"]; ok {
		t.Errorf("Same should not produce a finding: %+v", bySubject)
	}
}

// ---- SuppressedByPath ----

func TestSuppressedByPath(t *testing.T) {
	cases := []struct {
		path string
		pats []string
		want bool
	}{
		{"src/internal/x.cc", []string{"src/**/internal/**"}, true},
		{"src/public/x.cc", []string{"src/**/internal/**"}, false},
		{"any.cc", nil, false},
	}
	for _, c := range cases {
		if got := SuppressedByPath(c.path, c.pats); got != c.want {
			t.Errorf("SuppressedByPath(%q, %v) = %v, want %v", c.path, c.pats, got, c.want)
		}
	}
}
