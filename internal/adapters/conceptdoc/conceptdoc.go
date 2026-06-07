// Package conceptdoc is the concept-doc ingestion engine: it walks a
// project's narrative documentation (config-provided doc globs) and
// attributes each in-scope contract element to the docs that ANCHOR it,
// producing a new, additive docs.concepts coverage surface.
//
// It is deliberately distinct from the FIDL `///` reference docs that
// currently feed the scanner's "Concept" tile. Those are rendered API
// reference; this is hand-written narrative (concepts/, development/) that
// explains *why* and *how*, and a contract element is "concept-doc covered"
// iff a narrative doc names it with a high-confidence anchor.
//
// Attribution bar (REQUIREMENTS-concept-ingest.md decision 2): ANCHORED
// MENTIONS ONLY. An element attributes to a doc iff the doc contains a
// qualified/backticked name, a resolving link, or a defined-term-on-first-
// use of that element. Bare prose collisions ("the node exposes a
// controller") do NOT attribute — there is no ambiguity grading and no
// loose matching (the lesson from the killed grounding-clarity experiment).
//
// The anchor detection is not reimplemented here; it is the confirming
// subset of the Grounding detector, reused via grounding.AnchoredMentions
// so the two surfaces never drift on what "anchored" means.
package conceptdoc

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/sheaf-data/sheaf/internal/grounding"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
)

// AdapterName / AdapterVersion stamp every emitted DocClaim's provenance.
const (
	AdapterName    = "conceptdoc"
	AdapterVersion = "0.1.0"
)

// Doc is one narrative document to scan: a repo-relative path and its
// bytes. Aliased to grounding.Doc so the engine and the detector speak the
// same in-memory shape (file IO stays at the edge, in build.go).
type Doc = grounding.Doc

// Options configures one concept-doc ingestion run.
type Options struct {
	// Library scopes attribution: only elements whose Library == Library
	// are considered. Empty means "all libraries in Elements".
	Library        string
	LibraryDisplay string

	// Elements is the contract corpus (the scan's element set). Anchored
	// mentions attribute to these and nothing else — this is not a general
	// prose linter; an element must be in the contract to be attributed.
	Elements []*contractpb.ContractElement

	// Docs is the narrative corpus to scan (path + bytes).
	Docs []Doc

	// Suppress is the loaded .sheafignore-style suppression (nil = none),
	// shared with the Grounding surface.
	Suppress *grounding.Suppression
}

// Verdict is the clear/ambiguous/silent classification of one element on the
// docs.concepts surface — the productionized form of the throwaway audit's
// .claude/audit/classify.py partition. It is computed from SITE FAN-OUT: a
// "site" is one verbatim token at one place (doc_path, line, token), and a
// site's fan-out degree is the number of DISTINCT in-scope elements that token
// attributes to at that site (a bare "node" that anchors both Node and
// NodeController has degree 2; a backticked `NodeController` has degree 1).
//
//   - VerdictClear     — the element has at least one degree-1 anchored
//     mention (a reference that resolves to it alone). The narrative docs
//     point at THIS element unambiguously.
//   - VerdictAmbiguous — the element is referenced (>=1 anchored mention) but
//     every one of its mentions is a shared site (degree >= 2): the docs name
//     it, but never in a way that singles it out from its same-spelled kin.
//   - VerdictSilent    — the element has no anchored mention at all: it is not
//     discussed in the narrative docs (the expected default for most of a
//     large contract).
//
// The partition is exhaustive and disjoint: every in-scope element is exactly
// one of clear / ambiguous / silent, so clear + ambiguous + silent == total.
// This mirrors classify.py's clear-INCLUSIVE definition (degree-1 via ANY
// anchor). After the link/defined_term anchor regressions are closed, the two
// regression-prone anchors no longer manufacture spurious degree-1 edges, so
// the live clear count converges on the audit's clear-CONSERVATIVE figure.
type Verdict string

const (
	VerdictSilent    Verdict = "silent"
	VerdictAmbiguous Verdict = "ambiguous"
	VerdictClear     Verdict = "clear"
)

// ElementCoverage is the per-element verdict on the docs.concepts surface.
// Covered is true iff at least one anchored DocClaim attributes to this
// element. ClaimCount is the total number of anchored claims (across all
// docs); DocPaths is the deduped, sorted set of docs that anchor it (so a
// multi-doc element is one covered element but tracks both source docs).
//
// Verdict is the clear/ambiguous/silent classification (see Verdict). Covered
// == (Verdict != VerdictSilent); the two are kept side by side so existing
// callers that only ask "covered?" are unaffected by the finer partition.
// MinDegree is the smallest site fan-out degree across this element's mentions
// (0 when silent); Verdict is Clear exactly when MinDegree == 1.
type ElementCoverage struct {
	ElementID  string
	Display    string
	Kind       string
	Covered    bool
	Verdict    Verdict
	MinDegree  int
	ClaimCount int
	DocPaths   []string
	Claims     []*docclaimpb.DocClaim
}

// Partition is the per-library clear/ambiguous/silent rollup over the in-scope
// element set, mirroring classify.py's per-domain partition. Clear + Ambiguous
// + Silent == Total; Referenced == Clear + Ambiguous.
type Partition struct {
	Total      int
	Clear      int
	Ambiguous  int
	Silent     int
	Referenced int // Clear + Ambiguous (>=1 anchored mention)
}

// Summary is the per-library rollup of the docs.concepts surface.
type Summary struct {
	Library         string
	LibraryDisplay  string
	ElementsTotal   int // in-scope contract elements
	ElementsCovered int // elements with >=1 anchored claim (== Partition.Referenced)
	ElementsPct     int // ElementsCovered / ElementsTotal, rounded
	ClaimsTotal     int // total anchored claims emitted
	DocsScanned     int

	// Partition is the finer clear/ambiguous/silent cut of the same element
	// set. ElementsCovered == Partition.Referenced.
	Partition Partition
}

// Result is the full output of a concept-doc ingestion run: the per-element
// coverage rollup, the per-library summary, and the flat anchored-claim
// list. Deterministic for identical inputs.
type Result struct {
	Summary  Summary
	Elements []ElementCoverage // one per in-scope element, sorted by element_id
	Claims   []*docclaimpb.DocClaim
}

// site identifies one verbatim token at one place — the unit classify.py's
// fan-out degree is computed over. Two mentions share a site iff they are the
// same token at the same doc+line, regardless of which element each attributes
// to; the number of DISTINCT elements sharing a site is that site's degree.
type site struct {
	doc   string
	line  int
	token string
}

// Detect runs the anchored-mention attribution over opts.Docs and returns
// the docs.concepts coverage Result. Deterministic: claims come out in
// document-then-offset order (the detector's scan order); per-element
// rollups and doc-path sets are sorted; finding IDs are stable.
//
// This is the engine phases 2 (config rollout) and 3 (precision audit)
// build on. It performs no file IO — the caller supplies in-memory Docs.
func Detect(opts Options) *Result {
	mentions := grounding.AnchoredMentions(opts.Library, opts.Elements, opts.Docs, opts.Suppress)

	// 1. Site fan-out degree (classify.py step 1): group every mention by its
	// (doc, line, token) site and count the DISTINCT elements at each site.
	// A degree-1 site resolves to exactly one element; degree >= 2 is a shared
	// (ambiguous) site where one written token names several same-spelled kin.
	siteElems := map[site]map[string]bool{}
	for i := range mentions {
		m := &mentions[i]
		s := site{doc: m.DocPath, line: m.Line, token: m.Token}
		set := siteElems[s]
		if set == nil {
			set = map[string]bool{}
			siteElems[s] = set
		}
		set[m.ElementID] = true
	}
	siteDegree := func(s site) int { return len(siteElems[s]) }

	// Flat claim list, in scan order (already deterministic from the
	// scanner). One DocClaim per anchored mention.
	claims := make([]*docclaimpb.DocClaim, 0, len(mentions))
	// Per-element accumulation.
	byElem := map[string]*ElementCoverage{}
	docSetByElem := map[string]map[string]bool{}

	for i := range mentions {
		m := &mentions[i]
		claim := docClaimFor(m)
		claims = append(claims, claim)

		ec := byElem[m.ElementID]
		if ec == nil {
			ec = &ElementCoverage{
				ElementID: m.ElementID,
				Display:   m.ElementDisplay,
				Kind:      m.ElementKind,
				MinDegree: 0,
			}
			byElem[m.ElementID] = ec
			docSetByElem[m.ElementID] = map[string]bool{}
		}
		ec.Covered = true
		ec.ClaimCount++
		ec.Claims = append(ec.Claims, claim)
		docSetByElem[m.ElementID][m.DocPath] = true

		// 2. Track the element's smallest site degree across its mentions.
		// MinDegree == 1 means it has at least one degree-1 (clear) mention.
		deg := siteDegree(site{doc: m.DocPath, line: m.Line, token: m.Token})
		if ec.MinDegree == 0 || deg < ec.MinDegree {
			ec.MinDegree = deg
		}
	}

	// Build the per-element rollup over the FULL in-scope element set so
	// elements with zero anchored mentions appear as not-covered (Covered
	// == false), not silently dropped. Scope-filter mirrors the lexicon:
	// skip LIBRARY synthetics and out-of-library elements.
	elems := make([]ElementCoverage, 0, len(opts.Elements))
	seen := map[string]bool{}
	for _, e := range opts.Elements {
		if e == nil || e.GetId() == "" {
			continue
		}
		if opts.Library != "" && e.GetLibrary() != opts.Library {
			continue
		}
		if e.GetKind() == contractpb.ContractElementKind_LIBRARY {
			continue
		}
		id := e.GetId()
		if seen[id] {
			continue
		}
		seen[id] = true
		if ec := byElem[id]; ec != nil {
			ec.DocPaths = sortedKeys(docSetByElem[id])
			// 3. Classify the referenced element: clear iff it has a degree-1
			// mention (MinDegree == 1), else ambiguous (all mentions shared).
			if ec.MinDegree == 1 {
				ec.Verdict = VerdictClear
			} else {
				ec.Verdict = VerdictAmbiguous
			}
			elems = append(elems, *ec)
		} else {
			elems = append(elems, ElementCoverage{
				ElementID: id,
				Display:   displayOf(id),
				Kind:      e.GetKind().String(),
				Covered:   false,
				Verdict:   VerdictSilent, // no anchored mention at all
				MinDegree: 0,
				DocPaths:  []string{},
				Claims:    []*docclaimpb.DocClaim{},
			})
		}
	}
	sort.Slice(elems, func(i, j int) bool { return elems[i].ElementID < elems[j].ElementID })

	// 4. Roll up the partition (classify.py corpus totals, per library).
	var part Partition
	part.Total = len(elems)
	for i := range elems {
		switch elems[i].Verdict {
		case VerdictClear:
			part.Clear++
		case VerdictAmbiguous:
			part.Ambiguous++
		default:
			part.Silent++
		}
	}
	part.Referenced = part.Clear + part.Ambiguous

	libDisplay := opts.LibraryDisplay
	if libDisplay == "" {
		libDisplay = opts.Library
	}
	sum := Summary{
		Library:         opts.Library,
		LibraryDisplay:  libDisplay,
		ElementsTotal:   len(elems),
		ElementsCovered: part.Referenced,
		ElementsPct:     pct(part.Referenced, len(elems)),
		ClaimsTotal:     len(claims),
		DocsScanned:     len(opts.Docs),
		Partition:       part,
	}

	return &Result{Summary: sum, Elements: elems, Claims: claims}
}

// docClaimFor renders one anchored mention as a DocClaim on the
// docs.concepts surface. kind = PROSE_MENTION (the concept/narrative kind);
// the element is the sole contract_ref; provenance stamps the mechanical
// tier and this adapter. The anchor kind + detail are folded into raw_text
// as the audit trail, and the URL is left empty (canonical URL templating
// is a per-config concern handled at the emit edge if needed).
func docClaimFor(m *grounding.AnchoredMention) *docclaimpb.DocClaim {
	excerpt := sanitizeUTF8(m.Excerpt)
	return &docclaimpb.DocClaim{
		SourcePath:   m.DocPath,
		Location:     &commonpb.SourceLocation{Path: m.DocPath, Line: uint32(m.Line)},
		RawText:      excerpt,
		ContractRefs: []string{m.ElementID},
		Substance:    commonpb.Substance_SUBSTANCE_UNSPECIFIED,
		WordCount:    uint32(len(strings.Fields(excerpt))),
		Kind:         docclaimpb.DocClaimKind_PROSE_MENTION,
		Adapter:      AdapterName,
		SectionPath:  m.SectionPath,
		Provenance: &commonpb.RowProvenance{
			// DETERMINISTIC = the reproducible, schema/grammar (non-LLM)
			// tier. This engine is purely mechanical (no model), so every
			// claim it emits is deterministic-tier, source = this adapter.
			Tier:   commonpb.RowProvenance_DETERMINISTIC,
			Source: AdapterName,
		},
	}
}

// sanitizeUTF8 makes s safe to carry in a proto3 string field. Excerpts are
// raw byte slices lifted out of the source doc (detect.go: string(body[...])),
// so a doc with a stray non-UTF-8 byte — or a binary file that slipped past
// the doc globs — yields an excerpt that protojson refuses to marshal
// ("contains invalid UTF-8"). Replacing each invalid run with U+FFFD keeps
// the claim emittable without dropping the (otherwise valid) attribution. A
// well-scoped *.md glob never hits this; it is belt-and-suspenders so one bad
// byte cannot fail an entire snapshot or emit.
func sanitizeUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "�")
}

// displayOf returns the human-facing short name for an element ID — the
// segment after the final "/", else the whole ID.
func displayOf(id string) string {
	if i := strings.LastIndex(id, "/"); i >= 0 && i+1 < len(id) {
		return id[i+1:]
	}
	return id
}

// sortedKeys returns the map keys sorted ascending.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// pct rounds covered/total to a whole percent (0 when total == 0).
func pct(covered, total int) int {
	if total <= 0 {
		return 0
	}
	return int(float64(covered)*100.0/float64(total) + 0.5)
}
