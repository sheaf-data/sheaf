// Package html renders a Sheaf scan result as a static HTML site.
//
// Output layout:
//
//	<outdir>/
//	├── index.html              top-level element table + summary cards
//	├── findings.html           all findings, filterable
//	└── elements/<slug>.html    one per ContractElement
//
// Templates are embedded at build time via go:embed so the binary has
// no runtime file dependencies.

package html

import (
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sheaf-data/sheaf/internal/corpus"
	"github.com/sheaf-data/sheaf/internal/slug"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
	coveragepb "github.com/sheaf-data/sheaf/proto/coverage"
	findingpb "github.com/sheaf-data/sheaf/proto/finding"
)

//go:embed templates/*.html
var tmplFS embed.FS

// Writer renders the report.
type Writer struct {
	Project        string
	ProjectDisplay string
	OutDir         string
	GeneratedAt    time.Time
}

// Write writes the full report. Returns the number of pages written.
func (w *Writer) Write(c *corpus.Corpus, findings []*findingpb.Finding) (int, error) {
	if w.OutDir == "" {
		return 0, errors.New("html: OutDir is empty")
	}
	if w.GeneratedAt.IsZero() {
		w.GeneratedAt = time.Now()
	}
	if err := os.MkdirAll(filepath.Join(w.OutDir, "elements"), 0o755); err != nil {
		return 0, err
	}

	elemViews := buildElementSummaries(c, findings)
	libCount := countLibraries(elemViews)
	docCount, testCount := countDocumentedTested(elemViews)
	detCount, llmCount := countByTier(elemViews)
	llmAttributed := 0
	for _, e := range elemViews {
		if e.LLMTestsCount > 0 {
			llmAttributed++
		}
	}

	base := baseView{
		Title:          "Coverage",
		Project:        w.Project,
		ProjectDisplay: w.ProjectDisplay,
		Page:           "index",
		Root:           "",
		ElementCount:   len(elemViews),
		FindingCount:   len(findings),
		GeneratedAt:    w.GeneratedAt.Format("2006-01-02 15:04 MST"),
	}

	indexTmpl, err := parseTemplate("index.html")
	if err != nil {
		return 0, err
	}
	findingsTmpl, err := parseTemplate("findings.html")
	if err != nil {
		return 0, err
	}
	elementTmpl, err := parseTemplate("element.html")
	if err != nil {
		return 0, err
	}

	pages := 0

	// index.html
	idx := indexView{
		baseView:           base,
		Elements:           elemViews,
		LibraryCount:       libCount,
		DocumentedCount:    docCount,
		TestedCount:        testCount,
		DeterministicCount: detCount,
		LLMCount:           llmCount,
		LLMAttributedTests: llmAttributed,
	}
	if err := writeFile(filepath.Join(w.OutDir, "index.html"), func(f io.Writer) error {
		return indexTmpl.ExecuteTemplate(f, "base", idx)
	}); err != nil {
		return pages, err
	}
	pages++

	// findings.html
	findingsViewData := buildFindingsView(base, findings)
	findingsViewData.Page = "findings"
	if err := writeFile(filepath.Join(w.OutDir, "findings.html"), func(f io.Writer) error {
		return findingsTmpl.ExecuteTemplate(f, "base", findingsViewData)
	}); err != nil {
		return pages, err
	}
	pages++

	// Per-element pages.
	findingsBySubject := groupFindingsBySubject(findings)
	for _, e := range c.Elements() {
		view := buildElementView(base, e, c.Profile(e.GetId()), findingsBySubject[e.GetId()])
		view.Page = "" // no nav highlight
		view.Root = "../"
		out := filepath.Join(w.OutDir, "elements", slug.SlugifyUnique(e.GetId())+".html")
		if err := writeFile(out, func(f io.Writer) error {
			return elementTmpl.ExecuteTemplate(f, "base", view)
		}); err != nil {
			return pages, err
		}
		pages++
	}
	return pages, nil
}

// ===========================================================
// Views
// ===========================================================

type baseView struct {
	Title          string
	Project        string
	ProjectDisplay string
	Page           string // "index" | "findings" | ""
	Root           string // "" for top-level pages, "../" for nested
	ElementCount   int
	FindingCount   int
	GeneratedAt    string
}

type indexView struct {
	baseView
	Elements        []elementSummary
	LibraryCount    int
	DocumentedCount int
	TestedCount     int
	// Two-tier confidence: deterministic rows are reproducible/high-trust;
	// LLM rows are a citation-verified but recall-unverified semantic
	// snapshot. The masthead must not conflate them.
	DeterministicCount int
	LLMCount           int
	// LLMAttributedTests is the number of elements that gained >=1
	// LLM-inferred test edge (the attribution tier). Reported separately
	// from the deterministic Tested count — never folded into it.
	LLMAttributedTests int
}

type elementSummary struct {
	ID                string
	Slug              string
	Kind              string
	Library           string
	TestsCount        int
	DocsCount         int
	ExamplesCount     int
	MissingCategories []string
	// Tier is "deterministic" or "llm" (from RowProvenance); drives the
	// per-row confidence badge.
	Tier string
	// LLMTestsCount is the number of LLM-inferred test edges on this
	// element (the flagged tier; NOT counted in TestsCount).
	LLMTestsCount int
}

type findingsView struct {
	baseView
	Findings       []findingView
	FindingsByKind []findingKindCount
}

type findingView struct {
	Kind         string
	Subject      string
	Slug         string
	Severity     string
	SeverityRank int
	Message      string
	Analyzer     string
}

type findingKindCount struct {
	Kind  string
	Count int
}

type elementView struct {
	baseView
	ID              string
	Kind            string
	Tier            string
	Library         string
	Location        string
	DocExcerpt      string
	Relationships   []relationshipView
	DocsRefs        []docRefView
	DocsTotal       int
	TestRefs        []testRefView
	TestsTotal      int
	LLMTestRefs     []testRefView // LLM-inferred tier (citation-gated, unverified)
	LLMTestsTotal   int
	LLMDocRefs      []docRefView
	LLMDocsTotal    int
	ExampleRefs     []exampleRefView
	ExamplesTotal   int
	Missing         []string
	Thin            []thinView
	ElementFindings []findingView
}

type relationshipView struct {
	Kind       string
	Target     string
	TargetSlug string
	Note       string
}

type docRefView struct {
	Bucket    string
	Adapter   string
	Path      string
	Line      uint32
	URL       string
	Substance string
	Words     uint32
}

type testRefView struct {
	Bucket    string
	TestName  string
	Path      string
	Line      uint32
	Framework string
}

type exampleRefView struct {
	Bucket    string
	Path      string
	StartLine uint32
	EndLine   uint32
	Intent    string
}

type thinView struct {
	Category string
	Reason   string
}

// ===========================================================
// Builders
// ===========================================================

func buildElementSummaries(c *corpus.Corpus, _ []*findingpb.Finding) []elementSummary {
	elems := c.Elements()
	out := make([]elementSummary, 0, len(elems))
	for _, e := range elems {
		p := c.Profile(e.GetId())
		tests, docs, examples := countCoverage(p)
		out = append(out, elementSummary{
			ID:                e.GetId(),
			Slug:              slug.SlugifyUnique(e.GetId()),
			Kind:              shortKind(e.GetKind()),
			Library:           e.GetLibrary(),
			TestsCount:        tests,
			DocsCount:         docs,
			ExamplesCount:     examples,
			MissingCategories: missingOf(p),
			Tier:              tierOf(e.GetProvenance()),
			LLMTestsCount:     len(p.GetTests().GetLlmInferred()),
		})
	}
	return out
}

func buildFindingsView(base baseView, findings []*findingpb.Finding) findingsView {
	kindCounts := make(map[string]int)
	out := make([]findingView, 0, len(findings))
	for _, f := range findings {
		k := shortKind2(f.GetKind().String(), "FINDING_KIND_")
		kindCounts[k]++
		out = append(out, findingView{
			Kind:         k,
			Subject:      f.GetSubject(),
			Slug:         slug.SlugifyUnique(f.GetSubject()),
			Severity:     shortSeverity(f.GetSeverity()),
			SeverityRank: int(f.GetSeverity()),
			Message:      f.GetMessage(),
			Analyzer:     f.GetAnalyzer(),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SeverityRank != out[j].SeverityRank {
			return out[i].SeverityRank > out[j].SeverityRank // ERROR first
		}
		return out[i].Subject < out[j].Subject
	})
	kc := make([]findingKindCount, 0, len(kindCounts))
	for k, n := range kindCounts {
		kc = append(kc, findingKindCount{Kind: k, Count: n})
	}
	sort.Slice(kc, func(i, j int) bool { return kc[i].Count > kc[j].Count })
	return findingsView{
		baseView:       base,
		Findings:       out,
		FindingsByKind: kc,
	}
}

func buildElementView(base baseView, e *contractpb.ContractElement, p *coveragepb.CoverageProfile, fs []*findingpb.Finding) elementView {
	base.Title = e.GetId()
	v := elementView{
		baseView:   base,
		ID:         e.GetId(),
		Kind:       shortKind(e.GetKind()),
		Tier:       tierOf(e.GetProvenance()),
		Library:    e.GetLibrary(),
		Location:   locString(e.GetLocation()),
		DocExcerpt: e.GetDocCommentExcerpt(),
	}
	for _, r := range e.GetRelationships() {
		v.Relationships = append(v.Relationships, relationshipView{
			Kind:       shortKind2(r.GetKind().String(), "RELATIONSHIP_KIND_"),
			Target:     r.GetTargetElementId(),
			TargetSlug: slug.SlugifyUnique(r.GetTargetElementId()),
			Note:       r.GetNote(),
		})
	}
	if p != nil {
		// Doc refs.
		if d := p.GetDocs(); d != nil {
			if rr := d.GetReference(); rr != nil {
				for _, r := range rr.GetFidldoc() {
					v.DocsRefs = append(v.DocsRefs, docRefFromCommon(r, "docs.reference.fidldoc"))
				}
				for _, r := range rr.GetClidoc() {
					v.DocsRefs = append(v.DocsRefs, docRefFromCommon(r, "docs.reference.clidoc"))
				}
			}
			for _, r := range d.GetConcept() {
				v.DocsRefs = append(v.DocsRefs, docRefFromCommon(r, "docs.concept"))
			}
			for _, r := range d.GetTutorial() {
				v.DocsRefs = append(v.DocsRefs, docRefFromCommon(r, "docs.tutorial"))
			}
			if g := d.GetGuide(); g != nil {
				for _, r := range g.GetMigration() {
					v.DocsRefs = append(v.DocsRefs, docRefFromCommon(r, "docs.guide.migration"))
				}
				for _, r := range g.GetTroubleshooting() {
					v.DocsRefs = append(v.DocsRefs, docRefFromCommon(r, "docs.guide.troubleshooting"))
				}
				for _, r := range g.GetCookbook() {
					v.DocsRefs = append(v.DocsRefs, docRefFromCommon(r, "docs.guide.cookbook"))
				}
			}
			if pp := d.GetProposal(); pp != nil {
				for _, r := range pp.GetRfc() {
					v.DocsRefs = append(v.DocsRefs, docRefFromCommon(r, "docs.proposal.rfc"))
				}
				for _, r := range pp.GetDesign() {
					v.DocsRefs = append(v.DocsRefs, docRefFromCommon(r, "docs.proposal.design"))
				}
			}
			for _, r := range d.GetReleaseNotes() {
				v.DocsRefs = append(v.DocsRefs, docRefFromCommon(r, "docs.release_notes"))
			}
			for _, r := range d.GetFaq() {
				v.DocsRefs = append(v.DocsRefs, docRefFromCommon(r, "docs.faq"))
			}
		}
		v.DocsTotal = len(v.DocsRefs)
		// Test refs.
		if t := p.GetTests(); t != nil {
			for _, r := range t.GetUnit() {
				v.TestRefs = append(v.TestRefs, testRefFromCommon(r, "tests.unit"))
			}
			for _, r := range t.GetIntegration() {
				v.TestRefs = append(v.TestRefs, testRefFromCommon(r, "tests.integration"))
			}
			for _, r := range t.GetE2E() {
				v.TestRefs = append(v.TestRefs, testRefFromCommon(r, "tests.e2e"))
			}
			for _, r := range t.GetCtf() {
				v.TestRefs = append(v.TestRefs, testRefFromCommon(r, "tests.ctf"))
			}
			for _, r := range t.GetPerformance() {
				v.TestRefs = append(v.TestRefs, testRefFromCommon(r, "tests.performance"))
			}
			for _, r := range t.GetFuzz() {
				v.TestRefs = append(v.TestRefs, testRefFromCommon(r, "tests.fuzz"))
			}
			for _, r := range t.GetGolden() {
				v.TestRefs = append(v.TestRefs, testRefFromCommon(r, "tests.golden"))
			}
			// LLM-inferred tier — kept separate from the deterministic
			// refs above (and from TestsTotal) so the trusted counts are
			// never inflated. Rendered as a flagged, unverified section.
			for _, r := range t.GetLlmInferred() {
				v.LLMTestRefs = append(v.LLMTestRefs, testRefFromCommon(r, "tests.llm-inferred"))
			}
		}
		v.TestsTotal = len(v.TestRefs)
		v.LLMTestsTotal = len(v.LLMTestRefs)
		if d := p.GetDocs(); d != nil {
			for _, r := range d.GetLlmInferred() {
				v.LLMDocRefs = append(v.LLMDocRefs, docRefFromCommon(r, "docs.llm-inferred"))
			}
		}
		v.LLMDocsTotal = len(v.LLMDocRefs)
		// Examples.
		if x := p.GetExamples(); x != nil {
			for _, r := range x.GetInTree() {
				v.ExampleRefs = append(v.ExampleRefs, exampleRefFromCommon(r, "examples.in_tree"))
			}
			for _, r := range x.GetInDocs() {
				v.ExampleRefs = append(v.ExampleRefs, exampleRefFromCommon(r, "examples.in_docs"))
			}
			for _, r := range x.GetExternal() {
				v.ExampleRefs = append(v.ExampleRefs, exampleRefFromCommon(r, "examples.external"))
			}
		}
		v.ExamplesTotal = len(v.ExampleRefs)
		// Gaps.
		if g := p.GetGapsSummary(); g != nil {
			v.Missing = g.GetMissing()
			for _, t := range g.GetThin() {
				v.Thin = append(v.Thin, thinView{Category: t.GetCategory(), Reason: t.GetReason()})
			}
		}
	}
	// Element-scoped findings.
	for _, f := range fs {
		v.ElementFindings = append(v.ElementFindings, findingView{
			Kind:         shortKind2(f.GetKind().String(), "FINDING_KIND_"),
			Subject:      f.GetSubject(),
			Slug:         slug.SlugifyUnique(f.GetSubject()),
			Severity:     shortSeverity(f.GetSeverity()),
			SeverityRank: int(f.GetSeverity()),
			Message:      f.GetMessage(),
			Analyzer:     f.GetAnalyzer(),
		})
	}
	return v
}

func docRefFromCommon(r *commonpb.DocRef, bucket string) docRefView {
	return docRefView{
		Bucket:    bucket,
		Adapter:   r.GetAdapter(),
		Path:      r.GetPath(),
		Line:      r.GetLine(),
		URL:       r.GetUrl(),
		Substance: shortSubstance(r.GetSubstance()),
		Words:     r.GetWords(),
	}
}

func testRefFromCommon(r *commonpb.TestRef, bucket string) testRefView {
	return testRefView{
		Bucket:    bucket,
		TestName:  r.GetTestName(),
		Path:      r.GetPath(),
		Line:      r.GetLine(),
		Framework: r.GetFramework(),
	}
}

func exampleRefFromCommon(r *commonpb.CodeRef, bucket string) exampleRefView {
	return exampleRefView{
		Bucket:    bucket,
		Path:      r.GetPath(),
		StartLine: r.GetStartLine(),
		EndLine:   r.GetEndLine(),
		Intent:    r.GetIntent(),
	}
}

// ===========================================================
// Aggregations
// ===========================================================

func countCoverage(p *coveragepb.CoverageProfile) (tests, docs, examples int) {
	if p == nil {
		return
	}
	if t := p.GetTests(); t != nil {
		tests = len(t.GetUnit()) + len(t.GetIntegration()) + len(t.GetE2E()) + len(t.GetCtf()) +
			len(t.GetPerformance()) + len(t.GetFuzz()) + len(t.GetGolden())
	}
	if d := p.GetDocs(); d != nil {
		if r := d.GetReference(); r != nil {
			docs += len(r.GetFidldoc()) + len(r.GetClidoc())
		}
		docs += len(d.GetConcept()) + len(d.GetTutorial()) + len(d.GetReleaseNotes()) + len(d.GetFaq())
		if g := d.GetGuide(); g != nil {
			docs += len(g.GetMigration()) + len(g.GetTroubleshooting()) + len(g.GetCookbook())
		}
	}
	if x := p.GetExamples(); x != nil {
		examples = len(x.GetInTree()) + len(x.GetInDocs()) + len(x.GetExternal())
	}
	return
}

func missingOf(p *coveragepb.CoverageProfile) []string {
	if p == nil || p.GetGapsSummary() == nil {
		return nil
	}
	return p.GetGapsSummary().GetMissing()
}

func countLibraries(elems []elementSummary) int {
	seen := make(map[string]bool)
	for _, e := range elems {
		seen[e.Library] = true
	}
	return len(seen)
}

func countDocumentedTested(elems []elementSummary) (doc, test int) {
	for _, e := range elems {
		if e.DocsCount > 0 {
			doc++
		}
		if e.TestsCount > 0 {
			test++
		}
	}
	return
}

func countByTier(elems []elementSummary) (deterministic, llm int) {
	for _, e := range elems {
		if e.Tier == "llm" {
			llm++
		} else {
			deterministic++
		}
	}
	return
}

// tierOf maps RowProvenance to the report's tier label. Unset provenance
// (adapters predating provenance threading) reads as deterministic.
func tierOf(p *commonpb.RowProvenance) string {
	if p.GetTier() == commonpb.RowProvenance_LLM {
		return "llm"
	}
	return "deterministic"
}

func groupFindingsBySubject(findings []*findingpb.Finding) map[string][]*findingpb.Finding {
	out := make(map[string][]*findingpb.Finding)
	for _, f := range findings {
		out[f.GetSubject()] = append(out[f.GetSubject()], f)
	}
	return out
}

// ===========================================================
// Helpers
// ===========================================================

func shortKind(k contractpb.ContractElementKind) string {
	return shortKind2(k.String(), "KIND_")
}

func shortKind2(s, prefix string) string {
	return strings.TrimPrefix(s, prefix)
}

func shortSubstance(s commonpb.Substance) string {
	return strings.TrimPrefix(s.String(), "")
}

func shortSeverity(s commonpb.Severity) string {
	return strings.TrimPrefix(s.String(), "")
}

func locString(l *commonpb.SourceLocation) string {
	if l == nil || l.GetPath() == "" {
		return ""
	}
	return fmt.Sprintf("%s:%d", l.GetPath(), l.GetLine())
}

func writeFile(path string, fn func(io.Writer) error) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return fn(f)
}

// ===========================================================
// Templates
// ===========================================================

// parseTemplate returns a new template tree containing the base
// layout plus the named page-specific template. Each page gets its
// own tree so the multiple {{define "content"}} declarations don't
// collide.
func parseTemplate(pageFile string) (*template.Template, error) {
	t := template.New("").Funcs(template.FuncMap{
		"coverPill":             coverPill,
		"severityPill":          severityPill,
		"substancePillClass":    substancePillClass,
		"severityPillClassRank": severityPillClassRank,
	})
	return t.ParseFS(tmplFS, "templates/base.html", "templates/"+pageFile)
}

// coverPill maps a count to an HTML pill. 0 = red, 1–2 = yellow, 3+ = green.
func coverPill(n int) template.HTML {
	switch {
	case n == 0:
		return template.HTML(`<span class="pill pill-red">0</span>`)
	case n <= 2:
		return template.HTML(fmt.Sprintf(`<span class="pill pill-yellow">%d</span>`, n))
	default:
		return template.HTML(fmt.Sprintf(`<span class="pill pill-green">%d</span>`, n))
	}
}

func severityPill(s string) template.HTML {
	class := "pill-gray"
	switch s {
	case "ERROR":
		class = "pill-red"
	case "WARNING":
		class = "pill-yellow"
	case "INFO":
		class = "pill-blue"
	}
	return template.HTML(fmt.Sprintf(`<span class="pill %s">%s</span>`, class, s))
}

func severityPillClassRank(rank int) string {
	switch rank {
	case 3:
		return "pill-red"
	case 2:
		return "pill-yellow"
	case 1:
		return "pill-blue"
	}
	return "pill-gray"
}

func substancePillClass(s string) string {
	switch s {
	case "SUBSTANTIVE":
		return "pill-green"
	case "PARTIAL":
		return "pill-yellow"
	case "SIGNATURE_ONLY":
		return "pill-red"
	case "ABSENT":
		return "pill-gray"
	}
	return "pill-gray"
}
