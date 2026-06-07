package analyze

import (
	"context"
	"fmt"

	"github.com/sheaf-data/sheaf/internal/corpus"
	coveragepb "github.com/sheaf-data/sheaf/proto/coverage"
	findingpb "github.com/sheaf-data/sheaf/proto/finding"
)

// CoverageDelta computes the difference between two corpus snapshots
// and emits COVERAGE_DELTA findings for elements whose coverage moved.
// This is the PR-time analyzer: head corpus is `c`, base corpus is
// supplied via DeltaOptions.
//
// Unlike the other analyzers, CoverageDelta requires both a base and
// head corpus, so it isn't a vanilla Analyzer. The PR-bot constructs
// it directly with both snapshots.

type CoverageDelta struct {
	base *corpus.Corpus
	head *corpus.Corpus
}

// NewCoverageDelta constructs a delta analyzer over two snapshots.
func NewCoverageDelta(base, head *corpus.Corpus) *CoverageDelta {
	return &CoverageDelta{base: base, head: head}
}

// Run produces findings: one per element whose coverage shape changed.
func (c *CoverageDelta) Run(_ context.Context) ([]*findingpb.Finding, error) {
	var out []*findingpb.Finding
	headIDs := c.head.ElementIDs()
	seen := make(map[string]bool, len(headIDs))
	for _, id := range headIDs {
		seen[id] = true
		hp := c.head.Profile(id)
		bp := c.base.Profile(id)
		if d := profileDelta(bp, hp); d != "" {
			out = append(out, &findingpb.Finding{
				Id:       FindingID("coverage-delta", "delta", id),
				Kind:     findingpb.FindingKind_COVERAGE_DELTA,
				Subject:  id,
				Analyzer: "coverage-delta",
				Message:  d,
			})
		}
	}
	for _, id := range c.base.ElementIDs() {
		if seen[id] {
			continue
		}
		out = append(out, &findingpb.Finding{
			Id:       FindingID("coverage-delta", "removed", id),
			Kind:     findingpb.FindingKind_COVERAGE_DELTA,
			Subject:  id,
			Analyzer: "coverage-delta",
			Message:  "element removed in this revision",
		})
	}
	SortFindings(out)
	return out, nil
}

func profileDelta(base, head *coveragepb.CoverageProfile) string {
	if base == nil && head == nil {
		return ""
	}
	if base == nil {
		return "new element added in this revision"
	}
	if head == nil {
		return "element removed in this revision"
	}
	baseT := countTests(base)
	headT := countTests(head)
	baseD := countDocs(base)
	headD := countDocs(head)
	if baseT == headT && baseD == headD {
		return ""
	}
	return fmt.Sprintf("coverage changed: tests %d→%d, docs %d→%d", baseT, headT, baseD, headD)
}

func countTests(p *coveragepb.CoverageProfile) int {
	t := p.GetTests()
	if t == nil {
		return 0
	}
	return len(t.GetUnit()) + len(t.GetIntegration()) + len(t.GetE2E()) + len(t.GetCtf()) +
		len(t.GetPerformance()) + len(t.GetFuzz()) + len(t.GetGolden())
}

func countDocs(p *coveragepb.CoverageProfile) int {
	d := p.GetDocs()
	if d == nil {
		return 0
	}
	n := 0
	if r := d.GetReference(); r != nil {
		n += len(r.GetFidldoc()) + len(r.GetClidoc())
	}
	n += len(d.GetConcept()) + len(d.GetTutorial()) + len(d.GetReleaseNotes()) + len(d.GetFaq())
	if g := d.GetGuide(); g != nil {
		n += len(g.GetMigration()) + len(g.GetTroubleshooting()) + len(g.GetCookbook())
	}
	return n
}
