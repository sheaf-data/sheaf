package analyze

import (
	"context"
	"fmt"
	"strings"

	"github.com/sheaf-data/sheaf/internal/corpus"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	coveragepb "github.com/sheaf-data/sheaf/proto/coverage"
	findingpb "github.com/sheaf-data/sheaf/proto/finding"
)

func init() {
	Register("missing-in-category", func(name string) Analyzer {
		return &missingInCategory{name: name}
	})
}

// missingInCategory fires when a configured category has no entries
// for an element. The set of categories to alert on is supplied via
// the `alert_for_categories` Kv key (repeated string value).
type missingInCategory struct {
	name string
}

func (a *missingInCategory) Name() string { return a.name }
func (a *missingInCategory) Description() string {
	return "elements lacking references in specific configured categories"
}

func (a *missingInCategory) Analyze(_ context.Context, c *corpus.Corpus, opts Options) ([]*findingpb.Finding, error) {
	cats := stringSliceFromOpts(opts, "alert_for_categories")
	if len(cats) == 0 {
		return nil, nil
	}
	var out []*findingpb.Finding
	for _, e := range c.Elements() {
		if SuppressedByPath(e.GetLocation().GetPath(), opts.SuppressForPaths) {
			continue
		}
		profile := c.Profile(e.GetId())
		if profile == nil {
			continue
		}
		for _, cat := range cats {
			if !categoryEmpty(profile, cat) {
				continue
			}
			out = append(out, &findingpb.Finding{
				Id:       FindingID(a.name, "missing", e.GetId()+":"+cat),
				Kind:     findingpb.FindingKind_MISSING_IN_CATEGORY,
				Subject:  e.GetId(),
				Severity: opts.Severity,
				Analyzer: a.name,
				Message:  fmt.Sprintf("no references in category %q", cat),
				Evidence: []*findingpb.EvidencePointer{{
					Description: "element declaration",
					Location:    e.GetLocation(),
				}},
			})
		}
	}
	return out, nil
}

// stringSliceFromOpts handles both single-value and slice-value cases
// for a Kv key that holds repeated strings.
func stringSliceFromOpts(opts Options, key string) []string {
	v, ok := opts.Kv[key]
	if !ok {
		return nil
	}
	switch x := v.(type) {
	case []string:
		return x
	case string:
		return []string{x}
	}
	return nil
}

// categoryEmpty returns true if the given dotted-path bucket on the
// CoverageProfile has zero entries. Only the categories we know how
// to inspect are supported; unknown paths return true (alert).
func categoryEmpty(p *coveragepb.CoverageProfile, dottedPath string) bool {
	if p == nil {
		return true
	}
	parts := strings.Split(dottedPath, ".")
	if len(parts) == 0 {
		return true
	}
	switch parts[0] {
	case "tests":
		if p.GetTests() == nil {
			return true
		}
		return testsCategoryEmpty(p.GetTests(), parts[1:])
	case "docs":
		if p.GetDocs() == nil {
			return true
		}
		return docsCategoryEmpty(p.GetDocs(), parts[1:])
	case "examples":
		if p.GetExamples() == nil {
			return true
		}
		return examplesCategoryEmpty(p.GetExamples(), parts[1:])
	}
	return true
}

func testsCategoryEmpty(t *coveragepb.TestCoverage, sub []string) bool {
	if len(sub) == 0 {
		return len(t.GetUnit())+len(t.GetIntegration())+len(t.GetE2E())+len(t.GetCtf())+
			len(t.GetPerformance())+len(t.GetFuzz())+len(t.GetGolden()) == 0
	}
	switch sub[0] {
	case "unit_tests", "unit":
		return len(t.GetUnit()) == 0
	case "integration_tests", "integration":
		return len(t.GetIntegration()) == 0
	case "e2e_tests", "e2e":
		return len(t.GetE2E()) == 0
	case "ctf_tests", "ctf":
		return len(t.GetCtf()) == 0
	case "performance_tests", "performance":
		return len(t.GetPerformance()) == 0
	case "fuzz_tests", "fuzz":
		return len(t.GetFuzz()) == 0
	case "golden_tests", "golden":
		return len(t.GetGolden()) == 0
	}
	return true
}

func docsCategoryEmpty(d *coveragepb.DocCoverage, sub []string) bool {
	if len(sub) == 0 {
		ref := d.GetReference()
		return (ref == nil || len(ref.GetFidldoc())+len(ref.GetClidoc()) == 0) &&
			len(d.GetConcept()) == 0 && len(d.GetTutorial()) == 0 &&
			len(d.GetReleaseNotes()) == 0 && len(d.GetFaq()) == 0
	}
	switch sub[0] {
	case "reference":
		ref := d.GetReference()
		if ref == nil {
			return true
		}
		if len(sub) > 1 {
			switch sub[1] {
			case "fidldoc":
				return len(ref.GetFidldoc()) == 0
			case "clidoc":
				return len(ref.GetClidoc()) == 0
			}
			return true
		}
		return len(ref.GetFidldoc())+len(ref.GetClidoc()) == 0
	case "concept", "concepts":
		return len(d.GetConcept()) == 0
	case "tutorial", "tutorials":
		return len(d.GetTutorial()) == 0
	case "release_notes":
		return len(d.GetReleaseNotes()) == 0
	case "faq":
		return len(d.GetFaq()) == 0
	case "guide":
		g := d.GetGuide()
		if g == nil {
			return true
		}
		if len(sub) > 1 {
			switch sub[1] {
			case "migrations", "migration":
				return len(g.GetMigration()) == 0
			case "troubleshooting", "troubleshooting_guides":
				return len(g.GetTroubleshooting()) == 0
			case "cookbook":
				return len(g.GetCookbook()) == 0
			}
		}
		return len(g.GetMigration())+len(g.GetTroubleshooting())+len(g.GetCookbook()) == 0
	case "proposal":
		p := d.GetProposal()
		if p == nil {
			return true
		}
		if len(sub) > 1 {
			switch sub[1] {
			case "rfcs", "rfc":
				return len(p.GetRfc()) == 0
			case "design":
				return len(p.GetDesign()) == 0
			}
		}
		return len(p.GetRfc())+len(p.GetDesign()) == 0
	}
	return true
}

func examplesCategoryEmpty(e *coveragepb.ExampleCoverage, sub []string) bool {
	if len(sub) == 0 {
		return len(e.GetInTree())+len(e.GetInDocs())+len(e.GetExternal()) == 0
	}
	switch sub[0] {
	case "in_tree":
		return len(e.GetInTree()) == 0
	case "in_docs":
		return len(e.GetInDocs()) == 0
	case "external":
		return len(e.GetExternal()) == 0
	}
	return true
}

// avoid unused-import on Severity
var _ = commonpb.Severity_WARNING
