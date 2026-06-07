package analyze

import (
	"context"

	"github.com/sheaf-data/sheaf/internal/corpus"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
	findingpb "github.com/sheaf-data/sheaf/proto/finding"
)

func init() {
	Register("external-mention-only", func(name string) Analyzer {
		return &externalMentionOnly{name: name}
	})
}

// externalMentionOnly fires when the only documentation for an element
// is a name-mention in prose — no rendered reference, no substantive
// concept/tutorial/guide. The element appears in docs but only as
// boilerplate, never explained.
type externalMentionOnly struct {
	name string
}

func (a *externalMentionOnly) Name() string { return a.name }
func (a *externalMentionOnly) Description() string {
	return "element appears in prose but only as a name-mention (no behavioral explanation)"
}

func (a *externalMentionOnly) Analyze(_ context.Context, c *corpus.Corpus, opts Options) ([]*findingpb.Finding, error) {
	var out []*findingpb.Finding
	for _, e := range c.Elements() {
		if SuppressedByPath(e.GetLocation().GetPath(), opts.SuppressForPaths) {
			continue
		}
		// We only consider METHOD / FLAG / SUBCOMMAND — the actionable
		// kinds for an "explain better" finding.
		switch e.GetKind() {
		case contractpb.ContractElementKind_METHOD,
			contractpb.ContractElementKind_FLAG,
			contractpb.ContractElementKind_SUBCOMMAND:
		default:
			continue
		}
		p := c.Profile(e.GetId())
		if p == nil {
			continue
		}
		// If the element has a substantive rendered reference, this
		// finding does not apply.
		if hasSubstantiveDoc(p) {
			continue
		}
		// Count prose mentions that don't reach PARTIAL.
		var mentionOnly int
		for _, r := range p.GetDocs().GetConcept() {
			if r.GetSubstance() < commonpb.Substance_PARTIAL {
				mentionOnly++
			}
		}
		for _, r := range p.GetDocs().GetTutorial() {
			if r.GetSubstance() < commonpb.Substance_PARTIAL {
				mentionOnly++
			}
		}
		if mentionOnly == 0 {
			continue
		}
		out = append(out, &findingpb.Finding{
			Id:       FindingID(a.name, "mention-only", e.GetId()),
			Kind:     findingpb.FindingKind_EXTERNAL_MENTION_ONLY,
			Subject:  e.GetId(),
			Severity: opts.Severity,
			Analyzer: a.name,
			Message:  "element appears in prose docs only as a name-mention",
		})
	}
	return out, nil
}
