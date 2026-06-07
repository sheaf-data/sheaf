package analyze

import (
	"context"

	"github.com/sheaf-data/sheaf/internal/corpus"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
	coveragepb "github.com/sheaf-data/sheaf/proto/coverage"
	findingpb "github.com/sheaf-data/sheaf/proto/finding"
)

func init() {
	Register("documented-untested", func(name string) Analyzer {
		return &documentedUntested{name: name}
	})
}

// documentedUntested fires when an element has substantive reference
// documentation but no test references in any category.
//
// Only METHOD-kind elements are reported by default; PROTOCOL/TYPE
// elements often don't have direct tests in this model. Override with
// `include_kinds` Kv (string list) to widen.
type documentedUntested struct {
	name string
}

func (a *documentedUntested) Name() string { return a.name }
func (a *documentedUntested) Description() string {
	return "elements with substantive docs but no test references"
}

func (a *documentedUntested) Analyze(_ context.Context, c *corpus.Corpus, opts Options) ([]*findingpb.Finding, error) {
	wantKinds := kindFilter(opts, contractpb.ContractElementKind_METHOD)
	var out []*findingpb.Finding
	for _, e := range c.Elements() {
		if SuppressedByPath(e.GetLocation().GetPath(), opts.SuppressForPaths) {
			continue
		}
		if !wantKinds[e.GetKind()] {
			continue
		}
		p := c.Profile(e.GetId())
		if p == nil {
			continue
		}
		if !hasSubstantiveDoc(p) {
			continue
		}
		if hasAnyTest(p) {
			continue
		}
		out = append(out, &findingpb.Finding{
			Id:       FindingID(a.name, "doc-no-test", e.GetId()),
			Kind:     findingpb.FindingKind_DOCUMENTED_UNTESTED,
			Subject:  e.GetId(),
			Severity: opts.Severity,
			Analyzer: a.name,
			Message:  "element is documented but has no test references",
			Evidence: []*findingpb.EvidencePointer{{
				Description: "element declaration",
				Location:    e.GetLocation(),
			}},
		})
	}
	return out, nil
}

func kindFilter(opts Options, fallback ...contractpb.ContractElementKind) map[contractpb.ContractElementKind]bool {
	out := make(map[contractpb.ContractElementKind]bool)
	v, ok := opts.Kv["include_kinds"]
	if !ok {
		for _, k := range fallback {
			out[k] = true
		}
		return out
	}
	parse := func(s string) contractpb.ContractElementKind {
		k, ok := contractpb.ContractElementKind_value[s]
		if !ok {
			return contractpb.ContractElementKind_KIND_UNSPECIFIED
		}
		return contractpb.ContractElementKind(k)
	}
	switch x := v.(type) {
	case []string:
		for _, s := range x {
			if k := parse(s); k != contractpb.ContractElementKind_KIND_UNSPECIFIED {
				out[k] = true
			}
		}
	case string:
		if k := parse(x); k != contractpb.ContractElementKind_KIND_UNSPECIFIED {
			out[k] = true
		}
	}
	if len(out) == 0 {
		for _, k := range fallback {
			out[k] = true
		}
	}
	return out
}

func hasSubstantiveDoc(p *coveragepb.CoverageProfile) bool {
	if p == nil || p.GetDocs() == nil {
		return false
	}
	ref := p.GetDocs().GetReference()
	if ref != nil {
		for _, r := range append(ref.GetFidldoc(), ref.GetClidoc()...) {
			if r.GetSubstance() >= commonpb.Substance_PARTIAL {
				return true
			}
		}
	}
	for _, r := range p.GetDocs().GetTutorial() {
		if r.GetSubstance() >= commonpb.Substance_PARTIAL {
			return true
		}
	}
	for _, r := range p.GetDocs().GetConcept() {
		if r.GetSubstance() >= commonpb.Substance_PARTIAL {
			return true
		}
	}
	return false
}

func hasAnyTest(p *coveragepb.CoverageProfile) bool {
	if p == nil || p.GetTests() == nil {
		return false
	}
	t := p.GetTests()
	return len(t.GetUnit())+len(t.GetIntegration())+len(t.GetE2E())+len(t.GetCtf())+
		len(t.GetPerformance())+len(t.GetFuzz())+len(t.GetGolden()) > 0
}
