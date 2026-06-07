// Package prbot computes coverage-delta findings between two corpus
// snapshots (PR base + PR head) and renders them as a structured PR
// comment. Adapters for posting to Gerrit or GitHub live in this
// package too.
//
// The CLI surface `sheaf review --pr <ref>` exercises the in-memory
// flow without spinning up a webhook listener; `sheaf bot` is the
// long-running listener that handles patchset/PR events.

package prbot

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/sheaf-data/sheaf/internal/analyze"
	"github.com/sheaf-data/sheaf/internal/corpus"
	"github.com/sheaf-data/sheaf/internal/coverage/refs"
	categorizationpb "github.com/sheaf-data/sheaf/proto/categorization"
	coveragepb "github.com/sheaf-data/sheaf/proto/coverage"
	findingpb "github.com/sheaf-data/sheaf/proto/finding"
)

// RendererVersion is bumped when the comment/HTML renderer's output
// format changes incompatibly. A regeneration script can compare
// this against the version baked into an on-disk delta.json and
// re-render only those that lag.
const RendererVersion = 1

// DeltaSchemaVersion is the schema version of the delta.json
// artifact. Bumped independently from RendererVersion when the
// structured artifact's shape changes incompatibly.
const DeltaSchemaVersion = "1"

// Comment is the rendered structured PR comment.
type Comment struct {
	Title              string
	AffectedElements   []string
	Findings           []*findingpb.Finding
	SuggestedReviewers []string
	Subscribers        []string
	Body               string // markdown
}

// Render produces the markdown body + reviewer list given a base and
// head corpus, the rules (for ownership routing), and the PR ref.
//
// Pass `headFindings` (from the head corpus's analyzer run) to enrich
// each affected element's section with its current static-analysis
// findings (DOCUMENTED_UNTESTED, THIN_REFERENCE, etc.). When nil, only
// COVERAGE_DELTA findings are shown.
func Render(ctx context.Context, prRef string, base, head *corpus.Corpus, rules *categorizationpb.Rules, headFindings ...*findingpb.Finding) (*Comment, error) {
	delta := analyze.NewCoverageDelta(base, head)
	deltaFindings, err := delta.Run(ctx)
	if err != nil {
		return nil, err
	}
	c := &Comment{Title: "Sheaf review · " + prRef}
	c.Findings = append(c.Findings, deltaFindings...)

	// Collect affected element IDs from delta findings.
	seen := make(map[string]bool)
	for _, f := range deltaFindings {
		if !seen[f.GetSubject()] {
			seen[f.GetSubject()] = true
			c.AffectedElements = append(c.AffectedElements, f.GetSubject())
		}
	}

	// Augment with head-side static findings on the affected elements.
	for _, f := range headFindings {
		if !seen[f.GetSubject()] {
			continue // not affected by this PR; not Render's job
		}
		c.Findings = append(c.Findings, f)
	}
	sort.Strings(c.AffectedElements)

	// Reviewer routing — D4 multi-match dedup.
	reviewerSet := make(map[string]bool)
	subscribeSet := make(map[string]bool)
	for _, id := range c.AffectedElements {
		// Find categories that have entries in the head profile.
		hp := head.Profile(id)
		if hp == nil {
			continue
		}
		populatedCats := populatedCategories(hp)
		for _, cat := range populatedCats {
			for _, own := range rules.GetOwnership() {
				if own.GetCategory() != cat {
					continue
				}
				// Match scope (exact string vs element library).
				if own.GetScope() != "" {
					lib := libraryOf(id)
					if own.GetScope() != lib {
						continue
					}
				}
				if own.GetOwner() != "" {
					reviewerSet[own.GetOwner()] = true
					if own.GetSubscribe() {
						subscribeSet[own.GetOwner()] = true
					}
				}
			}
		}
	}
	for owner := range reviewerSet {
		c.SuggestedReviewers = append(c.SuggestedReviewers, owner)
	}
	for owner := range subscribeSet {
		c.Subscribers = append(c.Subscribers, owner)
	}
	sort.Strings(c.SuggestedReviewers)
	sort.Strings(c.Subscribers)

	c.Body = renderMarkdown(c)
	return c, nil
}

func libraryOf(elementID string) string {
	if i := strings.Index(elementID, "/"); i >= 0 {
		return elementID[:i]
	}
	return ""
}

// populatedCategories returns the dotted-path categories that have
// at least one entry in the profile.
func populatedCategories(p *coveragepb.CoverageProfile) []string {
	if p == nil {
		return nil
	}
	var out []string
	if t := p.GetTests(); t != nil {
		if len(t.GetUnit()) > 0 {
			out = append(out, "tests.unit_tests")
		}
		if len(t.GetIntegration()) > 0 {
			out = append(out, "tests.integration_tests")
		}
		if len(t.GetE2E()) > 0 {
			out = append(out, "tests.e2e_tests")
		}
		if len(t.GetCtf()) > 0 {
			out = append(out, "tests.ctf_tests")
		}
		if len(t.GetPerformance()) > 0 {
			out = append(out, "tests.performance_tests")
		}
	}
	if d := p.GetDocs(); d != nil {
		if refs.HasReferenceDocs(d.GetReference()) {
			out = append(out, "docs.reference")
		}
		if len(d.GetConcept()) > 0 {
			out = append(out, "docs.concepts")
		}
		if len(d.GetTutorial()) > 0 {
			out = append(out, "docs.tutorials")
		}
	}
	return out
}

// renderMarkdown formats a Comment as a Gerrit/GitHub-friendly
// markdown blob.
func renderMarkdown(c *Comment) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## %s — touches %d contract element(s)\n\n", c.Title, len(c.AffectedElements))
	if len(c.AffectedElements) == 0 {
		sb.WriteString("_No coverage-relevant changes._\n")
		return sb.String()
	}
	for _, id := range c.AffectedElements {
		fmt.Fprintf(&sb, "### `%s`\n\n", id)
		// List every finding subject to this element (delta + head static).
		var perElement []*findingpb.Finding
		for _, ff := range c.Findings {
			if ff.GetSubject() == id {
				perElement = append(perElement, ff)
			}
		}
		for _, f := range perElement {
			kind := strings.TrimPrefix(f.GetKind().String(), "FINDING_KIND_")
			fmt.Fprintf(&sb, "- **%s** — %s\n", kind, f.GetMessage())
		}
		sb.WriteString("\n")
	}
	if len(c.SuggestedReviewers) > 0 {
		sb.WriteString("**Suggested reviewers:** " + strings.Join(c.SuggestedReviewers, ", ") + "\n")
	}
	if len(c.Subscribers) > 0 {
		sb.WriteString("**Pinging:** " + strings.Join(c.Subscribers, " ") + "\n")
	}
	return sb.String()
}
