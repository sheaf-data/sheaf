// Package conceptdocs builds the view-model for the Concept Docs report — the
// standalone, doc-centric clear/ambiguous/silent map of how a library's
// narrative docs reference its API contract. It transforms a grounding.Report
// (the per-library GroundingReport emitted by cmd/emit-grounding) into a flat,
// template-ready View. Pure and deterministic: no IO, no model.
//
// Surface mapping (grounding state -> report bucket):
//
//	grounded               -> clear      (the doc names it resolvably)
//	guessing | ungrounded  -> ambiguous  (named via a colliding / unanchored
//	                                       token; shown with its evidence so
//	                                       the reader judges)
//	not_mentioned          -> silent     (no doc names it; never "red")
//
// Region axis is adaptive: a single-library report groups by kind
// (Types/Protocols/Methods); a report spanning more than one library groups
// by library. BuildViewAll merges several per-library GroundingReports into
// one multi-library view.
//
// The report renders from the GROUNDING surface, where the candidate/collision
// set already lives — NOT from the anchored-only docs.concepts / DocClaim
// surface (see the Concept Docs report design, Round 10).
package conceptdocs

import (
	"html/template"
	"path"
	"sort"
	"strings"

	"github.com/sheaf-data/sheaf/internal/grounding"
)

// Bucket is the three-state doc-centric classification.
type Bucket string

const (
	BucketClear     Bucket = "clear"
	BucketAmbiguous Bucket = "ambiguous"
	BucketSilent    Bucket = "silent"
)

// Region modes for the adaptive region axis.
const (
	regionKind    = "kind"
	regionLibrary = "library"
)

// citeCapPerDoc bounds how many cites a doc card shows before the rest fold
// into a per-card "show all" expander. The wall in the Named view is vertical
// inside a few large cards (one fuchsia doc names the contract 302 times), so
// the cap is per-doc, not a doc-count cap.
const citeCapPerDoc = 6

// View is the template-ready model for a Concept Docs report.
type View struct {
	Library     string
	Commit      string
	IconDataURI template.URL // embedded sheaf logo data URI for the header strip
	GeneratedAt string
	Headline    string
	Total       int
	SilentCount int
	RegionNoun  string      // "kind" | "library" — what the chips/silent set group by
	Chips       []Chip      // exactly 3, in order: clear, ambiguous, silent
	Docs        []DocCard   // Named view, sorted by path
	SilentSet   []KindGroup // Silent view, grouped by region, sorted desc by count
}

// Chip is one masthead state chip with its per-region breakdown.
type Chip struct {
	Bucket  Bucket
	Class   string // tile/dot CSS class: "c-clear" | "c-amb" | "c-silent"
	Label   string
	Sub     string // one-line descriptor under the count
	Count   int
	Regions []KindCount // regions with ≥1 element in this bucket, sorted desc by count
}

// KindCount is a per-region tally within one bucket. Kind holds the region
// key — a contract kind ("TYPE") in kind mode, a library FQN in library mode.
type KindCount struct {
	Kind  string
	Label string
	N     int
}

// DocCard is one narrative doc's cited references (Named view). Cites are the
// first citeCapPerDoc; More holds the overflow revealed by the card's expander.
type DocCard struct {
	Title string
	Path  string
	Href  string // source-browser URL for Path; empty when no --source-url-template
	ID    string // slug of Path, for the expander's element id
	Cites []Cite
	More  []Cite
	// Libraries is the sorted set of contract libraries this doc's cites
	// reference (each finding's resolved element + any contract candidates).
	// Drives the multi-library report's client-side ?library= filter: a card
	// survives the filter iff it references at least one requested library.
	// Single-library reports tag every card with the one library (unused).
	Libraries []string
}

// Cite is one reference in a doc card: a highlighted excerpt + what it
// resolves to. The excerpt is pre-split around the colliding/anchoring token
// (Pre + Token + Post) so the template highlights without re-parsing offsets.
type Cite struct {
	Bucket     Bucket
	CiteClass  string // "clear" | "amb"
	TokenClass string // token highlight class: "tok-name" | "tok-amb"
	Line       int
	Pre        string
	Token      string
	Post       string
	Verb       string // "resolves" (clear) | `"foo" matches` (ambiguous)
	Resolves   []Ent
	AnchorNote string // clear only, e.g. "qualified name + link"
}

// Ent is one resolved element or candidate on a cite's "resolves ->" line.
type Ent struct {
	Label string
	Class string // "resolved" | "cand" | "eng"
}

// KindGroup is one region's silent elements (Silent view). Kind holds the
// region key; Items carry a per-item kind tag (so a library-grouped group
// can show each element's kind).
type KindGroup struct {
	Kind  string
	Label string
	Count int
	Items []SilentItem
}

// SilentItem is one silent element chip: its display name + a kind tag.
type SilentItem struct {
	Display string
	Tag     string // lowercased kind, e.g. "type"
}

// Option configures BuildView/BuildViewAll. Options are additive and keep the
// callless default byte-stable: a View built with no options renders bare doc
// paths (no source-browser links), so reports generated without
// --source-url-template (e.g. --from-grounding rollups and the committed
// golden) are unchanged.
type Option func(*buildOpts)

type buildOpts struct {
	sourceURLTemplate string // {path}/{line}/{abs_path} pattern for doc-path links
}

// WithSourceURLTemplate sets the source-browser URL pattern used to linkify
// each doc card's path (placeholders {path}, {line}, {abs_path}). Empty leaves
// paths plain.
func WithSourceURLTemplate(tmpl string) Option {
	return func(o *buildOpts) { o.sourceURLTemplate = tmpl }
}

// BuildView transforms a single grounding.Report into the view-model.
// Deterministic for identical input.
func BuildView(rep *grounding.Report, opts ...Option) *View {
	if rep == nil {
		return &View{}
	}
	var o buildOpts
	for _, opt := range opts {
		opt(&o)
	}
	mode := regionMode(rep.Elements)
	v := &View{
		Library:     rep.Library,
		Commit:      rep.Commit,
		IconDataURI: logoDataURI(),
		GeneratedAt: rep.GeneratedAt,
		Total:       rep.Summary.ElementsTotal,
		SilentCount: rep.Summary.ElementsNotMentioned,
		RegionNoun:  mode,
		Headline:    "Where the docs explain " + libName(rep) + " — and where they go dark",
	}
	v.Chips = buildChips(rep, mode)
	v.Docs = buildDocs(rep, o.sourceURLTemplate)
	v.SilentSet = buildSilent(rep, mode)
	return v
}

// BuildViewAll merges several per-library GroundingReports into one
// multi-library view. The region axis becomes the library. subject is the
// headline subject (e.g. "the Fuchsia FIDL libraries").
func BuildViewAll(reps []*grounding.Report, subject string, opts ...Option) *View {
	v := BuildView(mergeReports(reps, subject), opts...)
	v.IconDataURI = logoDataURI()
	return v
}

// mergeReports concatenates several per-library reports into one. Finding IDs
// may repeat across inputs; the renderer keys docs by path and never uses the
// finding ID, so that is harmless.
func mergeReports(reps []*grounding.Report, subject string) *grounding.Report {
	m := &grounding.Report{Library: subject, LibraryDisplay: subject}
	for _, rep := range reps {
		if rep == nil {
			continue
		}
		m.Elements = append(m.Elements, rep.Elements...)
		m.Findings = append(m.Findings, rep.Findings...)
		m.Summary.ElementsTotal += rep.Summary.ElementsTotal
		m.Summary.ElementsMentioned += rep.Summary.ElementsMentioned
		m.Summary.ElementsGrounded += rep.Summary.ElementsGrounded
		m.Summary.ElementsGuessing += rep.Summary.ElementsGuessing
		m.Summary.ElementsUngrounded += rep.Summary.ElementsUngrounded
		m.Summary.ElementsNotMentioned += rep.Summary.ElementsNotMentioned
		if m.Commit == "" {
			m.Commit = rep.Commit
		}
		if m.GeneratedAt == "" {
			m.GeneratedAt = rep.GeneratedAt
		}
	}
	return m
}

// regionMode picks the region axis: library when the elements span more than
// one library, else kind.
func regionMode(elements []grounding.Element) string {
	seen := map[string]struct{}{}
	for i := range elements {
		seen[libraryOf(elements[i].ElementID)] = struct{}{}
		if len(seen) > 1 {
			return regionLibrary
		}
	}
	return regionKind
}

// libraryOf returns the library/namespace prefix of an element id — the part
// before the final "/". For an id with no "/", the whole id.
func libraryOf(elementID string) string {
	if i := strings.LastIndex(elementID, "/"); i >= 0 {
		return elementID[:i]
	}
	return elementID
}

// regionKeyLabel maps an element to its (region key, display label) under the
// chosen mode.
func regionKeyLabel(mode string, e *grounding.Element) (key, label string) {
	if mode == regionLibrary {
		lib := libraryOf(e.ElementID)
		return lib, lib
	}
	return e.Kind, humanizeKind(e.Kind)
}

// bucketOf maps a grounding state to the report's three buckets. guessing and
// ungrounded both fold into ambiguous: each is a reference named via a
// colliding or unanchored token that does not resolve on its own, which the
// report shows as cited evidence rather than asserting a verdict.
func bucketOf(s grounding.State) Bucket {
	switch s {
	case grounding.StateGrounded:
		return BucketClear
	case grounding.StateNotMentioned:
		return BucketSilent
	default: // guessing, ungrounded
		return BucketAmbiguous
	}
}

var chipClass = map[Bucket]string{
	BucketClear:     "c-clear",
	BucketAmbiguous: "c-amb",
	BucketSilent:    "c-silent",
}

var chipSub = map[Bucket]string{
	BucketClear:     "named so the referent resolves",
	BucketAmbiguous: "named only by a shared word",
	BucketSilent:    "not named in any concept doc",
}

func buildChips(rep *grounding.Report, mode string) []Chip {
	// Tally each region's elements per bucket. Each chip later keeps only the
	// regions with a non-zero count in its bucket, so the count under a chip
	// reflects its own items (e.g. Clear → its clear regions), not the
	// report-wide region set.
	regionLabel := map[string]string{}
	counts := map[Bucket]map[string]int{
		BucketClear:     {},
		BucketAmbiguous: {},
		BucketSilent:    {},
	}
	totals := map[Bucket]int{}
	for i := range rep.Elements {
		e := &rep.Elements[i]
		b := bucketOf(e.State)
		key, label := regionKeyLabel(mode, e)
		regionLabel[key] = label
		counts[b][key]++
		totals[b]++
	}
	keys := make([]string, 0, len(regionLabel))
	for k := range regionLabel {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	mk := func(b Bucket, label string) Chip {
		regions := make([]KindCount, 0, len(keys))
		for _, k := range keys {
			n := counts[b][k]
			if n == 0 {
				continue // omit regions with no elements in this bucket
			}
			regions = append(regions, KindCount{Kind: k, Label: regionLabel[k], N: n})
		}
		sort.SliceStable(regions, func(i, j int) bool {
			if regions[i].N != regions[j].N {
				return regions[i].N > regions[j].N
			}
			return regions[i].Kind < regions[j].Kind
		})
		return Chip{Bucket: b, Class: chipClass[b], Label: label, Sub: chipSub[b], Count: totals[b], Regions: regions}
	}
	return []Chip{
		mk(BucketClear, "Clear"),
		mk(BucketAmbiguous, "Ambiguous"),
		mk(BucketSilent, "Silent"),
	}
}

func buildDocs(rep *grounding.Report, sourceURLTemplate string) []DocCard {
	byPath := map[string]*DocCard{}
	libsByPath := map[string]map[string]bool{}
	for i := range rep.Findings {
		f := &rep.Findings[i]
		b := bucketOf(f.State)
		if b == BucketSilent {
			continue // a finding is, by definition, a detected reference
		}
		dc := byPath[f.SourcePath]
		if dc == nil {
			dc = &DocCard{Title: docTitle(f.SourcePath), Path: f.SourcePath, ID: slugify(f.SourcePath)}
			// Linkify the doc path to the source browser when a template is
			// set. The path is file-level (no single line), so anchor at line 1.
			dc.Href = expandDocURL(sourceURLTemplate, f.SourcePath)
			byPath[f.SourcePath] = dc
			libsByPath[f.SourcePath] = map[string]bool{}
		}
		dc.Cites = append(dc.Cites, citeFor(f, b))
		addCiteLibraries(libsByPath[f.SourcePath], f)
	}
	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]DocCard, 0, len(paths))
	for _, p := range paths {
		dc := byPath[p]
		dc.Libraries = sortedLibraries(libsByPath[p])
		// Clear cites first, then ambiguous; within a bucket, by line.
		sort.SliceStable(dc.Cites, func(i, j int) bool {
			ci, cj := dc.Cites[i], dc.Cites[j]
			if ci.Bucket != cj.Bucket {
				return ci.Bucket == BucketClear
			}
			return ci.Line < cj.Line
		})
		// Cap: first N shown, the rest into the expander.
		if len(dc.Cites) > citeCapPerDoc {
			more := make([]Cite, len(dc.Cites)-citeCapPerDoc)
			copy(more, dc.Cites[citeCapPerDoc:])
			dc.More = more
			dc.Cites = dc.Cites[:citeCapPerDoc]
		}
		out = append(out, *dc)
	}
	return out
}

// expandDocURL builds the source-browser link for a doc card's path. It mirrors
// utils/scanner's expandSourceURL (kept local to avoid dragging in that package)
// but only needs {path}/{line} for the concept-docs lens; {abs_path} is filled
// blank since the renderer has no repo root here. The path is file-level, so
// line is fixed at 1. Returns "" (no link) when the template is empty, the path
// is empty, or the path is a synthesized doc.
//
// The HTML escape happens in the template, so this returns the raw URL.
func expandDocURL(tmpl, docPath string) string {
	if tmpl == "" || docPath == "" {
		return ""
	}
	// Skip synthesized docs: the ffx regen injects a virtual
	// sheaf-ffx-gen/ffx-golden-examples.md that doesn't exist in the Fuchsia
	// tree, so a cs.opensource.google link to it would 404. Leave it plain.
	if strings.HasPrefix(docPath, "sheaf-ffx-gen/") {
		return ""
	}
	out := strings.ReplaceAll(tmpl, "{path}", docPath)
	out = strings.ReplaceAll(out, "{abs_path}", "")
	out = strings.ReplaceAll(out, "{line}", "1")
	return out
}

// addCiteLibraries records the libraries one finding references — its resolved
// element (clear cites) and any contract candidates (ambiguous cites) — into
// set, so the finding's doc card can be filtered by library.
func addCiteLibraries(set map[string]bool, f *grounding.Finding) {
	if f.ElementID != "" {
		set[libraryOf(f.ElementID)] = true
	}
	for i := range f.Candidates {
		c := &f.Candidates[i]
		if c.IsContract && c.ElementID != nil && *c.ElementID != "" {
			set[libraryOf(*c.ElementID)] = true
		}
	}
}

// sortedLibraries returns the set's keys sorted; nil for an empty set so the
// template renders no data-cd-libraries churn for a card with no resolved lib.
func sortedLibraries(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func citeFor(f *grounding.Finding, b Bucket) Cite {
	pre, tok, post := splitExcerpt(f.Excerpt, f.Token, f.TokenSpan)
	c := Cite{Bucket: b, Line: f.Line, Pre: pre, Token: tok, Post: post}
	if b == BucketClear {
		c.CiteClass = "clear"
		c.TokenClass = "tok-name"
		c.Verb = "resolves"
		c.Resolves = []Ent{{Label: shortLabel(f.ElementDisplay, f.ElementID), Class: "resolved"}}
		c.AnchorNote = anchorNote(f.Checked)
	} else {
		c.CiteClass = "amb"
		c.TokenClass = "tok-amb"
		c.Verb = `"` + f.Token + `" matches`
		c.Resolves = candidateEnts(f.Candidates)
	}
	return c
}

func candidateEnts(cands []grounding.Candidate) []Ent {
	out := make([]Ent, 0, len(cands))
	for _, c := range cands {
		if c.IsContract {
			out = append(out, Ent{Label: shortLabel(c.Label, derefStr(c.ElementID)), Class: "cand"})
		} else {
			out = append(out, Ent{Label: c.Label, Class: "eng"})
		}
	}
	return out
}

func buildSilent(rep *grounding.Report, mode string) []KindGroup {
	type grp struct {
		label string
		items []SilentItem
	}
	groups := map[string]*grp{}
	for i := range rep.Elements {
		e := &rep.Elements[i]
		if bucketOf(e.State) != BucketSilent {
			continue
		}
		key, label := regionKeyLabel(mode, e)
		g := groups[key]
		if g == nil {
			g = &grp{label: label}
			groups[key] = g
		}
		g.items = append(g.items, SilentItem{Display: e.Display, Tag: strings.ToLower(e.Kind)})
	}
	out := make([]KindGroup, 0, len(groups))
	for key, g := range groups {
		sort.SliceStable(g.items, func(i, j int) bool { return g.items[i].Display < g.items[j].Display })
		out = append(out, KindGroup{Kind: key, Label: g.label, Count: len(g.items), Items: g.items})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// splitExcerpt slices the excerpt around the colliding token for highlight.
// TokenSpan is a rune offset+len within the excerpt; if it is out of range,
// fall back to the first literal occurrence of the token, else no highlight.
func splitExcerpt(excerpt, token string, span grounding.Span) (pre, tok, post string) {
	r := []rune(excerpt)
	if span.Len > 0 && span.Start >= 0 && span.Start+span.Len <= len(r) {
		return string(r[:span.Start]), string(r[span.Start : span.Start+span.Len]), string(r[span.Start+span.Len:])
	}
	if token != "" {
		if i := strings.Index(excerpt, token); i >= 0 {
			return excerpt[:i], token, excerpt[i+len(token):]
		}
	}
	return excerpt, "", ""
}

// shortLabel elides a fully-qualified contract id to "…/<tail>" (FIDL-style);
// for an id with no "/" (e.g. a CLI command) it shows the display verbatim.
func shortLabel(display, elementID string) string {
	if i := strings.LastIndex(elementID, "/"); i >= 0 && i+1 < len(elementID) {
		return "…/" + elementID[i+1:]
	}
	if display != "" {
		return display
	}
	return elementID
}

// anchorNote renders the confirming anchors that fired, e.g. a backticked FQN
// plus a resolving link reads as "qualified name + link". Empty when none.
func anchorNote(checked []grounding.CheckedAnchor) string {
	var parts []string
	for _, c := range checked {
		if !c.Found {
			continue
		}
		switch c.Anchor {
		case grounding.AnchorQualifiedMention:
			parts = append(parts, "qualified name")
		case grounding.AnchorLink:
			parts = append(parts, "link")
		case grounding.AnchorDefinedTerm:
			parts = append(parts, "defined term")
		case grounding.AnchorFirstUse:
			parts = append(parts, "first-use")
		}
	}
	return strings.Join(dedupe(parts), " + ")
}

// docTitle humanizes a doc path into a card title: driver_framework.md ->
// "Driver framework".
func docTitle(p string) string {
	base := path.Base(p)
	base = strings.TrimSuffix(base, path.Ext(base))
	base = strings.ReplaceAll(base, "_", " ")
	base = strings.ReplaceAll(base, "-", " ")
	base = strings.TrimSpace(base)
	if base == "" {
		return p
	}
	r := []rune(base)
	r[0] = []rune(strings.ToUpper(string(r[0:1])))[0]
	return string(r)
}

// slugify turns a doc path into a DOM-id-safe slug.
func slugify(p string) string {
	var b strings.Builder
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// humanizeKind turns a raw ContractElementKind ("TYPE") into a plural display
// label ("Types").
func humanizeKind(k string) string {
	if k == "" {
		return "Other"
	}
	low := strings.ToLower(k)
	r := []rune(low)
	r[0] = []rune(strings.ToUpper(string(r[0:1])))[0]
	s := string(r)
	if !strings.HasSuffix(s, "s") {
		s += "s"
	}
	return s
}

// libName is the headline subject: the library FQN ("fuchsia.driver.framework"),
// falling back to the display name.
func libName(rep *grounding.Report) string {
	if rep.Library != "" {
		return rep.Library
	}
	return rep.LibraryDisplay
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
