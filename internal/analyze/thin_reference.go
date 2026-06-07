package analyze

import (
	"context"
	"fmt"

	"github.com/sheaf-data/sheaf/internal/corpus"
	"github.com/sheaf-data/sheaf/internal/coverage/refs"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	findingpb "github.com/sheaf-data/sheaf/proto/finding"
)

func init() {
	Register("thin-reference", func(name string) Analyzer {
		return &thinReference{name: name}
	})
}

// thinReference fires when an element has a rendered reference entry
// but its substance grade is below PARTIAL — i.e., the reference exists
// in name only.
type thinReference struct {
	name string
}

func (a *thinReference) Name() string { return a.name }
func (a *thinReference) Description() string {
	return "rendered reference present but below PARTIAL substance"
}

func (a *thinReference) Analyze(_ context.Context, c *corpus.Corpus, opts Options) ([]*findingpb.Finding, error) {
	var out []*findingpb.Finding
	for _, e := range c.Elements() {
		if SuppressedByPath(e.GetLocation().GetPath(), opts.SuppressForPaths) {
			continue
		}
		p := c.Profile(e.GetId())
		if p == nil || p.GetDocs() == nil || p.GetDocs().GetReference() == nil {
			continue
		}
		ref := p.GetDocs().GetReference()
		var thinAt string
		var lowest commonpb.Substance
		for _, r := range refs.AllReferenceRefs(ref) {
			if r.GetSubstance() != commonpb.Substance_SUBSTANCE_UNSPECIFIED &&
				r.GetSubstance() <= commonpb.Substance_SIGNATURE_ONLY {
				thinAt = r.GetUrl()
				if thinAt == "" {
					thinAt = r.GetPath()
				}
				lowest = r.GetSubstance()
				break
			}
		}
		if thinAt == "" {
			continue
		}
		out = append(out, &findingpb.Finding{
			Id:       FindingID(a.name, "thin", e.GetId()),
			Kind:     findingpb.FindingKind_THIN_REFERENCE,
			Subject:  e.GetId(),
			Severity: opts.Severity,
			Analyzer: a.name,
			Message:  fmt.Sprintf("reference is %s", commonpb.Substance_name[int32(lowest)]),
			Evidence: []*findingpb.EvidencePointer{{
				Description: "thin reference",
				Url:         thinAt,
			}},
		})
	}
	return out, nil
}
