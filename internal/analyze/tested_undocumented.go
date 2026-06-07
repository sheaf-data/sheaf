package analyze

import (
	"context"

	"github.com/sheaf-data/sheaf/internal/corpus"
	"github.com/sheaf-data/sheaf/internal/coverage/refs"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
	coveragepb "github.com/sheaf-data/sheaf/proto/coverage"
	findingpb "github.com/sheaf-data/sheaf/proto/finding"
)

func init() {
	Register("tested-undocumented", func(name string) Analyzer {
		return &testedUndocumented{name: name}
	})
}

// testedUndocumented fires when an element has test references but no
// reference documentation (and no substantive prose docs).
type testedUndocumented struct {
	name string
}

func (a *testedUndocumented) Name() string        { return a.name }
func (a *testedUndocumented) Description() string { return "elements with tests but no documentation" }

func (a *testedUndocumented) Analyze(_ context.Context, c *corpus.Corpus, opts Options) ([]*findingpb.Finding, error) {
	wantKinds := kindFilter(opts,
		contractpb.ContractElementKind_METHOD,
		contractpb.ContractElementKind_FLAG,
		contractpb.ContractElementKind_SUBCOMMAND,
	)
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
		if !hasAnyTest(p) {
			continue
		}
		if hasAnyDoc(p) {
			continue
		}
		out = append(out, &findingpb.Finding{
			Id:       FindingID(a.name, "test-no-doc", e.GetId()),
			Kind:     findingpb.FindingKind_TESTED_UNDOCUMENTED,
			Subject:  e.GetId(),
			Severity: opts.Severity,
			Analyzer: a.name,
			Message:  "element has tests but no documentation reference",
			Evidence: []*findingpb.EvidencePointer{{
				Description: "element declaration",
				Location:    e.GetLocation(),
			}},
		})
	}
	return out, nil
}

func hasAnyDoc(p *coveragepb.CoverageProfile) bool {
	if p == nil || p.GetDocs() == nil {
		return false
	}
	d := p.GetDocs()
	if refs.HasReferenceDocs(d.GetReference()) {
		return true
	}
	if len(d.GetConcept())+len(d.GetTutorial())+len(d.GetReleaseNotes())+len(d.GetFaq()) > 0 {
		return true
	}
	if g := d.GetGuide(); g != nil {
		if len(g.GetMigration())+len(g.GetTroubleshooting())+len(g.GetCookbook()) > 0 {
			return true
		}
	}
	if pr := d.GetProposal(); pr != nil {
		if len(pr.GetRfc())+len(pr.GetDesign()) > 0 {
			return true
		}
	}
	return false
}
