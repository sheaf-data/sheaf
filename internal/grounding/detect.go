package grounding

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

// Options configures one Grounding run. RepoRoot + DocGlobs select the
// concept docs to scan; Elements is the contract corpus (filtered to
// Library inside). Repo/Commit/LibraryDisplay are stamped into the
// envelope; Suppress is the loaded .sheafignore-style list (nil = none).
type Options struct {
	Library        string
	LibraryDisplay string
	Repo           string
	Commit         string
	GeneratedAt    string // RFC3339 timestamp; caller supplies for reproducibility

	Elements []*contractpb.ContractElement
	Docs     []Doc // concept docs to scan (path + content)

	Suppress *Suppression
}

// Doc is one concept document to scan: a repo-relative path and its bytes.
// Keeping the detector over in-memory Docs (rather than reading files
// itself) makes it trivially unit-testable and keeps file IO at the edge.
type Doc struct {
	Path string
	Body []byte
}

// refState records one reference's bucketed state for per-element rollup.
type refState struct{ state State }

// rawRef is an intermediate detected reference, before bucketing.
type rawRef struct {
	docPath     string
	line        int
	sectionPath []string
	excerpt     string
	token       string // the verbatim surface token as it appears in prose
	tokenSpan   Span   // offset+len within excerpt
	absOffset   int    // byte offset of token start within the doc body
	owners      []int  // lexicon entry indices this token can refer to
	collision   bool   // the matched form is a bare common-English noun
	exactName   bool   // the matched form is the element's full name (lowercased)
	tailOnly    bool   // the matched form is ONLY a partial common-noun tail of a
	// multi-word name (e.g. "manager" of CompositeNodeManager) — never the
	// element's whole name. Such a bare tail is ambiguous by construction
	// and too weak to be a hard Ungrounded; it tops out at Guessing.
	density int // # of other contract-term mentions in the same section
	// softContext marks an ambiguous-leaning mention that must not be a hard
	// Ungrounded (decision 5): a sentence-initial capitalized common word,
	// or a token inside an unresolved markdown link. Routed to Guessing.
	softContext bool
}

// Build runs the full §5 algorithm and returns the Report. Deterministic:
// the same inputs always produce the same output (no map iteration leaks
// into ordering; finding IDs are assigned in a stable scan order).
func Build(opts Options) (*Report, error) {
	if opts.Suppress == nil {
		opts.Suppress = emptySuppression()
	}
	lx := buildLexicon(opts.Library, opts.Elements)

	// 1+2. Scan every doc for lexicon matches -> raw references.
	var refs []rawRef
	for _, d := range opts.Docs {
		refs = append(refs, scanDoc(lx, d)...)
	}

	// 3+4+5+6. Bucket each reference (anchor check), build candidates,
	// rank severity. Findings come out in a deterministic order.
	rep := &Report{
		SchemaVersion:  SchemaVersion,
		Surface:        "grounding",
		GeneratedAt:    opts.GeneratedAt,
		ModelStamp:     nil, // mechanical run -> reproducible
		Repo:           opts.Repo,
		Commit:         opts.Commit,
		Library:        opts.Library,
		LibraryDisplay: opts.LibraryDisplay,
		DisplayMap:     defaultDisplayMap(),
	}

	// Per-doc page-title + section cache so anchor checks don't reparse.
	pageTitles := map[string]string{}
	for _, d := range opts.Docs {
		pageTitles[d.Path] = pageTitle(d.Body)
	}
	// Track first-use per (doc, elementID): the first reference (by offset)
	// to an element on a page is where a define/link belongs.
	firstUse := firstUseIndex(refs)

	// Index docs by path for anchor inspection (link/qualified scans).
	docByPath := map[string][]byte{}
	for _, d := range opts.Docs {
		docByPath[d.Path] = d.Body
	}

	// Section-scoped confirming anchors. Decision 7 says "Grounded = the
	// referent is established somewhere resolvable on the page, NOT every
	// token spelled out" — but PAGE scope is too coarse for a long
	// multi-section concept doc: a single backticked `Driver` in one
	// section must not silently ground 500 bare "driver" mentions spread
	// across every other section (that buries the very signal the feature
	// exists to surface — pages that never bind the term locally). We
	// therefore establish the referent at SECTION granularity (the H1>H2>H3
	// heading stack): a bare mention is grounded when the element is
	// qualified / linked / defined within the SAME section, or at the
	// mention's own site. This honors "not every token spelled out" (one
	// anchor per section suffices, the fix is still one line) while keeping
	// the resolution scope as local as a reader's actually is.
	sectionAnchors := computeSectionAnchors(lx, refs, docByPath)

	// Assemble findings. Each reference that survives suppression and binds
	// to >=1 contract element becomes a finding.
	var findings []Finding
	// elemRefs accumulates per-element reference states for rollup.
	elemRefs := map[string][]refState{}
	elemFindingIDs := map[string][]string{}

	fid := 0
	for ri := range refs {
		r := &refs[ri]
		if len(r.owners) == 0 {
			continue
		}
		// A reference can collide with several elements (e.g. "node" ->
		// Node and NodeController). The PRIMARY owner is the element whose
		// full name matches exactly if any, else the first (sorted) owner;
		// candidates enumerate the rest. competing_contract_refs counts the
		// plausible contract referents — the blast-radius signal.
		primary := primaryOwner(lx, r)
		elem := lx.entries[primary]

		if opts.Suppress.suppressed(r.docPath, r.token, elem.elementID) {
			continue
		}

		checked := checkAnchors(docByPath[r.docPath], pageTitles[r.docPath],
			lx, r, elem, firstUse, sectionAnchors)
		state := bucket(checked, r)

		competing := competingContractRefs(lx, r)
		candidates := buildCandidates(lx, r, primary)
		sev := severity(state, competing, r)
		fixp := buildFix(state, elem, r)

		fid++
		id := fmt.Sprintf("g-%04d", fid)
		findings = append(findings, Finding{
			ID:                    id,
			ElementID:             elem.elementID,
			ElementDisplay:        elem.display,
			State:                 state,
			SourcePath:            r.docPath,
			Line:                  r.line,
			SectionPath:           r.sectionPath,
			Excerpt:               r.excerpt,
			Token:                 r.token,
			TokenSpan:             r.tokenSpan,
			Candidates:            candidates,
			CompetingContractRefs: competing,
			Checked:               checked,
			Fix:                   fixp,
			Severity:              sev,
			Provenance:            Provenance{Tier: "mechanical", Adapter: AdapterName, AdapterVersion: AdapterVersion},
		})
		elemRefs[elem.elementID] = append(elemRefs[elem.elementID], refState{state: state})
		elemFindingIDs[elem.elementID] = append(elemFindingIDs[elem.elementID], id)
	}

	rep.Findings = findings

	// 7. Roll up per element + per library.
	rep.Elements = rollupElements(lx, elemRefs, elemFindingIDs)
	rep.Summary = rollupSummary(rep.Elements, findings)

	return rep, nil
}

// wordBoundaryClass: a token match must be flanked by non-identifier,
// non-letter characters so "node" doesn't fire inside "nodes" or "anode".
// We allow a trailing 's' (simple English plural) to still match the
// singular form — "nodes" is a mention of the node concept.
func isWordByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9') || b == '_'
}

// scanDoc finds every lexicon surface form in a single doc's prose
// (code blocks stripped) and returns one rawRef per match, longest-match
// first so multi-word forms win over their bare tails. Overlapping matches
// at the same span are collapsed to the longest.
func scanDoc(lx *lexicon, d Doc) []rawRef {
	body := d.Body
	offsets := computeLineOffsets(body)
	// Blank out both code blocks and heading lines before scanning prose:
	// code-block identifiers are not prose mentions, and heading words are
	// structural (captured by the heading/page_title anchors, never emitted
	// as their own finding).
	blanked := append(fenceRanges(body), headingRanges(body)...)
	prose := stripRanges(body, blanked)
	sectionAt := buildSectionIndex(body)
	lowerProse := bytesToLower(prose)

	type hit struct {
		start, end int
		form       string
	}
	var hits []hit
	claimed := make([]bool, len(prose)+1) // byte already consumed by a longer match

	for _, form := range lx.formsByLen {
		fl := len(form)
		if fl == 0 {
			continue
		}
		from := 0
		for {
			idx := indexFrom(lowerProse, form, from)
			if idx < 0 {
				break
			}
			from = idx + 1
			start, end := idx, idx+fl
			// Word boundaries on both sides (allow trailing 's' plural).
			if start > 0 && isWordByte(prose[start-1]) {
				continue
			}
			rend := end
			if rend < len(prose) && (prose[rend] == 's' || prose[rend] == 'S') {
				// allow a single plural 's'
				if rend+1 >= len(prose) || !isWordByte(prose[rend+1]) {
					rend++
				}
			}
			if rend < len(prose) && isWordByte(prose[rend]) {
				continue
			}
			// Skip if any byte of this span was claimed by a longer form.
			overlap := false
			for i := start; i < end; i++ {
				if claimed[i] {
					overlap = true
					break
				}
			}
			if overlap {
				continue
			}
			for i := start; i < end; i++ {
				claimed[i] = true
			}
			hits = append(hits, hit{start: start, end: rend, form: form})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].start < hits[j].start })

	var out []rawRef
	for _, h := range hits {
		owners := lx.byForm[h.form]
		if len(owners) == 0 {
			continue
		}
		line := lineFromOffset(offsets, h.start)
		exStart, exEnd := sentenceBounds(body, h.start)
		excerpt := string(body[exStart:exEnd])
		token := string(body[h.start:h.end])
		// token_span is in CHARACTER (rune) offsets within the excerpt, NOT
		// bytes — the UI indexes the excerpt string by code unit to place
		// the highlight, and Fuchsia prose is full of multibyte runes
		// (curly quotes, em dashes). Count runes from excerpt start to the
		// token, and rune length of the token.
		span := Span{
			Start: utf8.RuneCount(body[exStart:h.start]),
			Len:   utf8.RuneCount(body[h.start:h.end]),
		}
		secPath := sectionAt(h.start)

		ownerIdx := make([]int, 0, len(owners))
		anyCollision, anyExact := false, false
		// matchedExact is true if THIS form is the whole (exact) name of at
		// least one owner; matchedTail is true if it matched as a partial
		// common-noun tail for at least one owner. tailOnly = tail matches
		// only, no exact-name owner.
		matchedExact, matchedTail := false, false
		for _, o := range owners {
			ownerIdx = append(ownerIdx, o.idx)
			if o.collision {
				anyCollision = true
			}
			if o.exactName {
				anyExact = true
				matchedExact = true
			} else {
				matchedTail = true
			}
		}
		tailOnly := matchedTail && !matchedExact
		trimmed := strings.TrimSpace(excerpt)
		adjSpan := adjustSpanForTrim(excerpt, span)
		// Soft-context signals (route to Guessing, never a hard red):
		//  - sentence-initial capitalized single common word ("Key …").
		//  - the token sits inside ANY markdown link bracket on its line
		//    (inline [x](y), reference [x][y], or shortcut [x]) whose target
		//    we did not resolve — TOC entries are referenced, just not bound.
		//  - a partial common-noun tail of a multi-word name ("manager" of
		//    CompositeNodeManager): ambiguous by construction, never red.
		sentenceInitial := adjSpan.Start == 0 && len(token) > 0 &&
			token[0] >= 'A' && token[0] <= 'Z' && isSingleCommonWord(token)
		inLink := tokenInsideLinkBracket(body, h.start)
		out = append(out, rawRef{
			docPath:     d.Path,
			line:        line,
			sectionPath: secPath,
			excerpt:     trimmed,
			token:       token,
			tokenSpan:   adjSpan,
			absOffset:   h.start,
			owners:      ownerIdx,
			collision:   anyCollision,
			exactName:   anyExact,
			tailOnly:    tailOnly,
			softContext: sentenceInitial || inLink || tailOnly,
		})
	}
	// Compute per-section contract-term density (load-bearing signal):
	// how many references share each (doc, section) — proxies "reference
	// section / API-term density / proximity to other contract terms".
	densityBySection := map[string]int{}
	for i := range out {
		key := out[i].docPath + "\x00" + strings.Join(out[i].sectionPath, "›")
		densityBySection[key]++
	}
	for i := range out {
		key := out[i].docPath + "\x00" + strings.Join(out[i].sectionPath, "›")
		out[i].density = densityBySection[key]
	}
	return out
}

// adjustSpanForTrim shifts a token span when the excerpt was left-trimmed
// of leading whitespace by TrimSpace.
func adjustSpanForTrim(rawExcerpt string, span Span) Span {
	trimmedLead := len(rawExcerpt) - len(strings.TrimLeft(rawExcerpt, " \t\n\r"))
	span.Start -= trimmedLead
	if span.Start < 0 {
		span.Start = 0
	}
	return span
}

// firstUseIndex records, per (doc, elementID), the byte offset of the
// earliest reference — used by the first_use anchor check.
func firstUseIndex(refs []rawRef) map[string]int {
	idx := map[string]int{}
	// owners are lexicon indices; we record by (doc, ownerIdx) using a
	// sentinel string key. The detector maps owner->elementID at call time.
	for i := range refs {
		for _, o := range refs[i].owners {
			key := fmt.Sprintf("%s\x00%d", refs[i].docPath, o)
			if cur, ok := idx[key]; !ok || refs[i].absOffset < cur {
				idx[key] = refs[i].absOffset
			}
		}
	}
	return idx
}

// primaryOwner picks the element a reference is primarily about: the owner
// whose full name matches exactly (e.g. token "NodeController" -> the
// NodeController element), else the first owner in sorted order.
func primaryOwner(lx *lexicon, r *rawRef) int {
	lower := strings.ToLower(r.token)
	lower = strings.TrimSuffix(lower, "s")
	for _, idx := range r.owners {
		for _, f := range lx.entries[idx].surfaceForms {
			if f.exactName && f.text == lower {
				return idx
			}
		}
	}
	// fall back to the lowest element index (sorted by element_id).
	best := r.owners[0]
	for _, idx := range r.owners {
		if idx < best {
			best = idx
		}
	}
	return best
}

// competingContractRefs counts the distinct contract elements a reference
// could plausibly bind to — the blast-radius / ranking signal. Minimum 1
// (a reference always has its primary referent).
func competingContractRefs(lx *lexicon, r *rawRef) int {
	seen := map[int]bool{}
	for _, idx := range r.owners {
		seen[idx] = true
	}
	if len(seen) == 0 {
		return 1
	}
	return len(seen)
}

// checkAnchors runs every AnchorKind against the reference and returns the
// full audit trail. The order matches the fixture: page_title, heading,
// first_use, defined_term, qualified_mention, link. A grounded reference
// needs at least one *confirming* anchor (qualified_mention / link /
// defined_term / first_use-with-binding); page_title + heading alone are
// binding CONTEXT, not confirmation (they move a bare collision up to
// Guessing, never to Grounded) — that is the conservative boundary.
func checkAnchors(body []byte, title string, lx *lexicon, r *rawRef, elem lexEntry, firstUse map[string]int, sectionAnchors map[string]*pageAnchor) []CheckedAnchor {
	primaryIdx := -1
	for i := range lx.entries {
		if lx.entries[i].elementID == elem.elementID {
			primaryIdx = i
			break
		}
	}

	// page_title: does the page title contain the element's name/word?
	titleFound, titleDetail := titleBinds(title, elem)
	// heading: does any heading in the section path contain it?
	headFound, headDetail := headingBinds(r.sectionPath, elem)

	// Confirming anchors are SECTION-scoped (see Build). The element is
	// "established in this section" if it is qualified / linked / defined-
	// as-a-term anywhere in the same heading-stack section — including at
	// THIS mention. A bare mention of such an element is grounded.
	pa := sectionAnchors[anchorKey(r.docPath, sectionKey(r.sectionPath), elem.elementID)]

	qualFound, qualDetail := false, ""
	linkFound, linkDetail := false, ""
	definedFound, definedDetail := false, ""
	if pa != nil {
		qualFound, qualDetail = pa.qualified, pa.qualifiedDetail
		linkFound, linkDetail = pa.linked, pa.linkedDetail
		definedFound, definedDetail = pa.defined, pa.definedDetail
	}

	// first_use: is THIS the earliest mention of the element on the page AND
	// written as an unambiguous (exact non-colliding / qualified) form? A
	// bare collision on first use is NOT a binding — it is where the fix
	// goes — so it stays false. Keeps the red boundary honest.
	firstUseFound := isFirstUseBinding(body, r, elem, primaryIdx, firstUse)

	return []CheckedAnchor{
		{Anchor: AnchorPageTitle, Found: titleFound, Detail: nilStr(titleDetail)},
		{Anchor: AnchorHeading, Found: headFound, Detail: nilStr(headDetail)},
		{Anchor: AnchorFirstUse, Found: firstUseFound, Detail: nil},
		{Anchor: AnchorDefinedTerm, Found: definedFound, Detail: nilStr(definedDetail)},
		{Anchor: AnchorQualifiedMention, Found: qualFound, Detail: nilStr(qualDetail)},
		{Anchor: AnchorLink, Found: linkFound, Detail: nilStr(linkDetail)},
	}
}

// pageAnchor records, per (doc, element), whether the element is bound by a
// confirming anchor anywhere on the page, with the first such detail.
type pageAnchor struct {
	qualified       bool
	qualifiedDetail string
	linked          bool
	linkedDetail    string
	defined         bool
	definedDetail   string
}

func anchorKey(docPath, section, elementID string) string {
	return docPath + "\x00" + section + "\x00" + elementID
}

// sectionKey joins a heading stack into a stable section identifier.
func sectionKey(sectionPath []string) string { return strings.Join(sectionPath, "›") }

// computeSectionAnchors walks every detected reference and records, per
// (doc, section, element), the confirming anchors established within that
// section. A reference whose OWN site is qualified/linked/defined
// establishes the element for its section. We scan the reference set (not
// raw text) so the binding must be an actual mention of the element — a
// stray backtick elsewhere doesn't count.
func computeSectionAnchors(lx *lexicon, refs []rawRef, docByPath map[string][]byte) map[string]*pageAnchor {
	out := map[string]*pageAnchor{}
	for ri := range refs {
		r := &refs[ri]
		body := docByPath[r.docPath]
		sk := sectionKey(r.sectionPath)
		for _, idx := range r.owners {
			elem := lx.entries[idx]
			key := anchorKey(r.docPath, sk, elem.elementID)
			pa := out[key]
			if pa == nil {
				pa = &pageAnchor{}
				out[key] = pa
			}
			if !pa.qualified {
				if ok, d := qualifiedMention(body, r, elem); ok {
					pa.qualified, pa.qualifiedDetail = true, d
				}
			}
			if !pa.linked {
				if ok, d := linkBinds(body, r, elem); ok {
					pa.linked, pa.linkedDetail = true, d
				}
			}
			if !pa.defined {
				if ok, d := definedTermBinds(body, r); ok {
					// A defined-term binding only counts when the token is an
					// unambiguous form (the element's exact name), not a bare
					// emphasized common word like *node* used generically.
					if r.exactName {
						pa.defined, pa.definedDetail = true, d
					}
				}
			}
		}
	}
	return out
}

// nilStr returns nil for an empty detail so the JSON emits null (matching
// the fixture's `"detail": null`), or a *string otherwise.
func nilStr(s string) *string {
	if s == "" {
		return nil
	}
	return strptr(s)
}

// bucket applies decision 4 + 5: a CONFIRMING anchor -> Grounded; binding
// CONTEXT but no confirmation -> Guessing; nothing -> Ungrounded. Bias to
// Guessing on the boundary.
//
//   - Confirming anchors (prove the referent): qualified_mention, link,
//     defined_term, first_use-binding.
//   - Context anchors (lean toward a referent, don't prove it): page_title,
//     heading.
//
// A non-collision exact-name token (e.g. "NodeController" written out in
// prose, even un-backticked) is itself near-unambiguous; we treat an exact
// multi-word/non-common-noun name as a confirming signal so writing the
// real API name counts as grounded. A bare common-English collision
// ("node") never self-confirms — it needs a real anchor.
func bucket(checked []CheckedAnchor, r *rawRef) State {
	confirming := false
	context := false
	for _, c := range checked {
		if !c.Found {
			continue
		}
		switch c.Anchor {
		case AnchorQualifiedMention, AnchorLink, AnchorDefinedTerm, AnchorFirstUse:
			confirming = true
		case AnchorPageTitle, AnchorHeading:
			context = true
		}
	}
	// Writing the element's full, non-colliding name in prose is itself a
	// confirming signal (the agent can resolve "NodeController" unaided).
	if r.exactName && !r.collision {
		confirming = true
	}
	switch {
	case confirming:
		// A confirming anchor in-section (or the exact name written out)
		// establishes the referent — the agent can resolve this mention.
		return StateGrounded
	case context:
		// Binding CONTEXT (page title or a heading names the element) but
		// nothing confirms it: the agent can infer the referent but the
		// page never ties this prose to the API. This is the fixture's
		// g-0002 shape — Guessing.
		return StateGuessing
	case r.softContext:
		// Soft binding context that leans toward the API referent without
		// proving it — currently: a sentence-initial capitalized common
		// word ("Key differences…", "Drivers are…") whose capitalization is
		// ambiguous between the type name and ordinary grammar, or a token
		// sitting inside a markdown link whose target we couldn't resolve
		// (reference-style TOC links). Decision 5 (conservative Ungrounded
		// boundary): these are Guessing, never a hard red.
		return StateGuessing
	default:
		// Nothing on the page ties this mention to the API: no in-section
		// anchor, no title/heading binding. This is the canonical
		// Ungrounded collision (fixture g-0001). We do NOT soften it with a
		// density heuristic — decision 5's "bias to Guessing on the
		// boundary" is realized by the broad CONTEXT branch above (title OR
		// any heading in the stack counts), which already absorbs the
		// genuinely-ambiguous cases. What falls through to here is a bare
		// collision the page never anchors and never even names in a
		// heading — defensibly red, and every red shows its work in
		// checked[].
		return StateUngrounded
	}
}

// titleBinds reports whether the page title names the element (any of its
// surface forms), plural-aware so a "Drivers" title binds the "driver"
// form.
func titleBinds(title string, elem lexEntry) (bool, string) {
	if title == "" {
		return false, ""
	}
	lt := strings.ToLower(title)
	for _, f := range elem.surfaceForms {
		if containsWordPlural(lt, f.text) {
			return true, title
		}
	}
	return false, ""
}

// headingBinds reports whether any heading in the section path names the
// element. Plural-aware (a "Drivers" / "Nodes" heading binds the singular
// form) so a section whose heading is the plural of the term still counts
// as binding context.
func headingBinds(sectionPath []string, elem lexEntry) (bool, string) {
	for _, h := range sectionPath {
		lh := strings.ToLower(h)
		for _, f := range elem.surfaceForms {
			if containsWordPlural(lh, f.text) {
				return true, h
			}
		}
	}
	return false, ""
}

// qualifiedMention reports whether the token is a genuine qualified
// reference to the element at the reference site. The signal must be
// strong, because a bare common word ("driver") is a substring of the
// library FQDN ("fuchsia.driver.framework") and of file paths
// ("driver_communication.md") — neither of which is a reference to the
// Driver protocol. We therefore credit ONLY:
//
//   - Backticked inline code: `Driver`, `Node` — the spelling docs use for
//     a literal API name. This works for collision and non-collision forms.
//   - PascalCase-as-written: the token in the SOURCE is the element's
//     CamelCase identifier (capital first letter, e.g. "Driver" not the
//     prose "driver"). A capitalized identifier in prose is the author
//     naming the type, not using the common noun.
//   - FIDL slash-method / dotted-qualified where the token is a CAPITALIZED
//     segment of a contract-shaped path: "Driver/Bind", "Node.Open",
//     "fuchsia.driver.framework/Driver". A lowercase token wedged inside a
//     dotted identifier (the library name) is rejected.
//
// This deliberately undercredits to keep the Ungrounded boundary honest:
// the cost of missing a real qualified mention is a false Guess/red, which
// Phase 2 validation will catch — far safer than grounding 600 generic
// "driver" mentions off the library name.
func qualifiedMention(body []byte, r *rawRef, elem lexEntry) (bool, string) {
	start := r.absOffset
	end := r.absOffset + len(r.token)

	if backtickedAt(body, start, end) {
		return true, "backticked `" + elem.display + "`"
	}

	// PascalCase-as-written: the source token starts with an uppercase
	// letter (the author wrote the type name, not the common noun). For a
	// collision tail ("driver"), this is the distinguishing signal.
	//
	// Guard against sentence-initial capitalization of a common noun
	// ("Driver authors should…"): if the token is at the very start of its
	// sentence AND is a single common-English word, the capital is
	// grammar, not a type reference — don't credit it.
	if len(r.token) > 0 && r.token[0] >= 'A' && r.token[0] <= 'Z' {
		if !(r.tokenSpan.Start == 0 && isSingleCommonWord(r.token)) {
			return true, "named " + r.token
		}
	}

	// Dotted / slash qualified — only when the token itself is capitalized
	// (so it is a contract segment, not a lowercase middle of an FQDN).
	tokenCapitalized := len(r.token) > 0 && r.token[0] >= 'A' && r.token[0] <= 'Z'
	if tokenCapitalized {
		if start >= 1 {
			if p := body[start-1]; p == '.' || p == '/' {
				return true, "qualified " + elem.display
			}
		}
		if start >= 2 && body[start-1] == ':' && body[start-2] == ':' {
			return true, "qualified " + elem.display
		}
	}
	// A slash/dot/:: immediately AFTER the token, introducing a member
	// ("Node/Open", "Node.Open", "Node::Open"), also qualifies — again only
	// for a capitalized token.
	if tokenCapitalized && end < len(body) {
		if body[end] == '/' || body[end] == '.' {
			// next char must look like a member (capital letter)
			if end+1 < len(body) && body[end+1] >= 'A' && body[end+1] <= 'Z' {
				return true, "qualified " + elem.display
			}
		}
		if end+1 < len(body) && body[end] == ':' && body[end+1] == ':' {
			return true, "qualified " + elem.display
		}
	}
	return false, ""
}

// backtickedAt reports whether [start,end) sits inside a single-backtick
// inline-code span on its line.
func backtickedAt(body []byte, start, end int) bool {
	// Scan left to line start counting backticks; scan right to line end.
	ls := start
	for ls > 0 && body[ls-1] != '\n' {
		ls--
	}
	le := end
	for le < len(body) && body[le] != '\n' {
		le++
	}
	// A backtick immediately adjacent (left of start or right of end)
	// within the same line is the cheap, robust signal docs actually use.
	if start > ls && body[start-1] == '`' {
		return true
	}
	if end < le && body[end] == '`' {
		return true
	}
	// Token wholly within a `...` pair on the line.
	left := strings.LastIndexByte(string(body[ls:start]), '`')
	right := strings.IndexByte(string(body[end:le]), '`')
	return left >= 0 && right >= 0
}

// definedTermBinds reports whether the token is emphasized (*node* /
// **node** / _node_) — markdown's defined-term convention — at the site.
func definedTermBinds(body []byte, r *rawRef) (bool, string) {
	start := r.absOffset
	end := r.absOffset + len(r.token)
	emph := func(b byte) bool { return b == '*' || b == '_' }
	if start > 0 && emph(body[start-1]) && end < len(body) && emph(body[end]) {
		return true, "emphasized " + r.token
	}
	if start > 1 && body[start-1] == '*' && body[start-2] == '*' {
		return true, "bold " + r.token
	}
	return false, ""
}

// linkBinds reports whether the token sits inside a markdown link
// [text](target) whose target RESOLVES to this element — the link's path
// tail or #fragment is the element's display name as a whole segment
// (e.g. "…#Node", "…/Node", "fuchsia.driver.framework#Driver"). A link to
// an unrelated page that merely happens to contain the word as a substring
// (the glossary "#board-driver") does NOT ground the element. This keeps
// the link anchor a real, followable reference rather than any nearby URL.
func linkBinds(body []byte, r *rawRef, elem lexEntry) (bool, string) {
	start := r.absOffset
	ls := start
	for ls > 0 && body[ls-1] != '\n' {
		ls--
	}
	le := start
	for le < len(body) && body[le] != '\n' {
		le++
	}
	line := string(body[ls:le])
	rel := start - ls
	name := strings.ToLower(elementDisplay(elem.elementID))
	if name == "" {
		name = strings.ToLower(elem.display)
	}
	for _, m := range mdLinkRx.FindAllStringSubmatchIndex(line, -1) {
		textStart, textEnd := m[2], m[3]
		targetStart, targetEnd := m[4], m[5]
		if rel >= textStart && rel < textEnd {
			target := line[targetStart:targetEnd]
			if linkTargetResolvesTo(target, name) {
				return true, target
			}
		}
	}
	// Reference-style links: [text][label] (and collapsed [text][], label =
	// text). The [label]: target definitions live elsewhere in the doc, so
	// resolve them through the whole-body definition table. Parsed lazily —
	// most lines carry no reference link.
	var defs map[string]string
	for _, m := range mdRefLinkRx.FindAllStringSubmatchIndex(line, -1) {
		textStart, textEnd := m[2], m[3]
		if rel < textStart || rel >= textEnd {
			continue
		}
		label := normalizeLabel(line[m[4]:m[5]])
		if label == "" {
			label = normalizeLabel(line[textStart:textEnd]) // collapsed [text][]
		}
		if defs == nil {
			defs = parseLinkDefs(body)
		}
		if target, ok := defs[label]; ok && linkTargetResolvesTo(target, name) {
			return true, target
		}
	}
	return false, ""
}

// mdRefLinkRx matches a reference-style link [text][label] (label may be empty
// for the collapsed form [text][]). Disjoint from mdLinkRx (which requires
// "(target)").
var mdRefLinkRx = regexp.MustCompile(`\[([^\]]+)\]\[([^\]]*)\]`)

// linkDefRx matches a reference-link definition line: "[label]: target" (up to
// three leading spaces, optional title after the target).
var linkDefRx = regexp.MustCompile(`(?m)^[ \t]{0,3}\[([^\]]+)\]:[ \t]*(\S+)`)

// parseLinkDefs collects a doc's reference-link definitions, keyed by
// normalized label (lowercased, whitespace-collapsed). First definition wins.
func parseLinkDefs(body []byte) map[string]string {
	defs := map[string]string{}
	for _, m := range linkDefRx.FindAllSubmatch(body, -1) {
		label := normalizeLabel(string(m[1]))
		if label == "" {
			continue
		}
		if _, exists := defs[label]; !exists {
			defs[label] = string(m[2])
		}
	}
	return defs
}

// normalizeLabel canonicalizes a reference-link label for matching:
// lowercased, with internal whitespace runs collapsed to single spaces.
func normalizeLabel(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// linkTargetResolvesTo reports whether a link target's final path segment
// or #fragment equals the element name as a whole word. Segment separators
// are "/", "#", "."; the name must be a complete token within the tail, not
// a substring (so "#board-driver" does not resolve "driver", but
// "#Driver", "/Driver", and "fuchsia.driver.framework#Driver" do).
func linkTargetResolvesTo(target, name string) bool {
	if name == "" {
		return false
	}
	lt := strings.ToLower(target)
	// Prefer the fragment if present; else the last path segment.
	tail := lt
	if i := strings.LastIndexByte(tail, '#'); i >= 0 {
		tail = tail[i+1:]
	} else if i := strings.LastIndexByte(tail, '/'); i >= 0 {
		tail = tail[i+1:]
		// strip a file extension
		if d := strings.LastIndexByte(tail, '.'); d >= 0 {
			tail = tail[:d]
		}
	}
	// Whole-word match within tail, where word chars are [a-z0-9_]; this
	// treats "-" and "." as separators ("board-driver" -> ["board","driver"]).
	return containsWordLoose(tail, name)
}

// containsWordLoose reports whether needle appears in haystack delimited by
// non-alphanumeric-underscore boundaries (so "-" and "." split words).
func containsWordLoose(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	from := 0
	for {
		i := strings.Index(haystack[from:], needle)
		if i < 0 {
			return false
		}
		i += from
		leftOK := i == 0 || !isWordByte(haystack[i-1])
		r := i + len(needle)
		rightOK := r >= len(haystack) || !isWordByte(haystack[r])
		if leftOK && rightOK {
			return true
		}
		from = i + 1
	}
}

var mdLinkRx = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)

// anyBracketRx matches any markdown bracketed text [ ... ] on a line —
// link text for inline, reference, or shortcut links. Used only to detect
// that a token is *referenced* (soft context), not to resolve the target.
var anyBracketRx = regexp.MustCompile(`\[([^\]]+)\]`)

// tokenInsideLinkBracket reports whether the byte at off sits within a
// markdown [ ... ] bracket on its own line.
func tokenInsideLinkBracket(body []byte, off int) bool {
	ls := off
	for ls > 0 && body[ls-1] != '\n' {
		ls--
	}
	le := off
	for le < len(body) && body[le] != '\n' {
		le++
	}
	rel := off - ls
	for _, m := range anyBracketRx.FindAllSubmatchIndex(body[ls:le], -1) {
		if rel >= m[2] && rel < m[3] {
			return true
		}
	}
	return false
}

// isSingleCommonWord reports whether s is one word that is a common-English
// noun (ignoring a trailing plural 's'). Used to suppress sentence-initial
// capitalization as a false "named type" signal.
func isSingleCommonWord(s string) bool {
	l := strings.ToLower(s)
	if strings.ContainsAny(l, " \t") {
		return false
	}
	if isCommonNoun(l) {
		return true
	}
	return isCommonNoun(strings.TrimSuffix(l, "s"))
}

// isFirstUseBinding reports whether this reference is BOTH the earliest
// mention of the element on its page AND written as a confirming form
// (exact non-colliding name, or qualified/backticked). A bare collision on
// first use is where the fix goes, not a binding, so it returns false. The
// firstUse map is keyed "<doc>\x00<lexiconIdx>" -> earliest byte offset.
func isFirstUseBinding(body []byte, r *rawRef, elem lexEntry, primaryIdx int, firstUse map[string]int) bool {
	if primaryIdx < 0 {
		return false
	}
	key := fmt.Sprintf("%s\x00%d", r.docPath, primaryIdx)
	earliest, ok := firstUse[key]
	if !ok || earliest != r.absOffset {
		return false // not the first mention of this element on this page
	}
	// The first mention only *binds* if it is itself unambiguous.
	if r.exactName && !r.collision {
		return true
	}
	if qok, _ := qualifiedMention(body, r, elem); qok {
		return true
	}
	return false
}

// containsWordPlural is containsWord but tolerant of a single trailing
// plural 's' on the matched haystack word — so "drivers" in a heading
// binds the "driver" form, and "nodes" binds "node". The needle itself is
// the singular surface form.
func containsWordPlural(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	from := 0
	for {
		i := strings.Index(haystack[from:], needle)
		if i < 0 {
			return false
		}
		i += from
		leftOK := i == 0 || !isWordByte(haystack[i-1])
		r := i + len(needle)
		// allow a single trailing plural 's'
		if r < len(haystack) && (haystack[r] == 's' || haystack[r] == 'S') {
			r++
		}
		rightOK := r >= len(haystack) || !isWordByte(haystack[r])
		if leftOK && rightOK {
			return true
		}
		from = i + 1
	}
}

// containsWord reports whether haystack contains needle as a whole word.
//
//nolint:unused // dormant grounding detector, retained (PR #99)
func containsWord(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	from := 0
	for {
		i := strings.Index(haystack[from:], needle)
		if i < 0 {
			return false
		}
		i += from
		leftOK := i == 0 || !isWordByte(haystack[i-1])
		r := i + len(needle)
		rightOK := r >= len(haystack) || !isWordByte(haystack[r])
		if leftOK && rightOK {
			return true
		}
		from = i + 1
	}
}

// buildCandidates renders the "Could mean ->" set: the contract element(s)
// the token collides with (primary first), plus the plain-English gloss
// when the token is a common-noun collision.
func buildCandidates(lx *lexicon, r *rawRef, primary int) []Candidate {
	var out []Candidate
	// Primary contract candidate first.
	pe := lx.entries[primary]
	out = append(out, Candidate{
		ElementID:  strptr(pe.elementID),
		Label:      pe.display,
		Kind:       "contract",
		IsContract: true,
	})
	// Other competing contract referents, in sorted element order.
	others := make([]int, 0, len(r.owners))
	for _, idx := range r.owners {
		if idx != primary {
			others = append(others, idx)
		}
	}
	sort.Ints(others)
	for _, idx := range others {
		e := lx.entries[idx]
		out = append(out, Candidate{
			ElementID:  strptr(e.elementID),
			Label:      e.display,
			Kind:       "contract",
			IsContract: true,
		})
	}
	// English gloss — only when the matched form is a bare common noun.
	if r.collision {
		out = append(out, Candidate{
			ElementID:  nil,
			Label:      englishGloss(r.token),
			Kind:       "english",
			IsContract: false,
		})
	}
	return out
}

// englishGloss produces the plain-English reading shown opposite the
// contract candidate. Deterministic, deliberately generic.
func englishGloss(token string) string {
	t := strings.ToLower(strings.TrimSuffix(token, "s"))
	return "a generic " + t + " (plain English)"
}

// severity is the worklist rank in [0,1]. It is f(state, competing
// contract refs, load-bearing-ness). The ranker is the product, so it is
// spelled out explicitly:
//
//   - Base by state: ungrounded 0.60, guessing 0.30, grounded 0.0. The
//     state is the dominant term but NOT absolute — decision 6 ("rank by
//     blast radius, not bucket") lets a high-ambiguity Guess outrank a flat
//     Ungrounded miss.
//   - Blast radius: +0.12 per competing contract referent beyond the first
//     (capped at +0.24). Several plausible referents is worse than one.
//   - Load-bearing-ness: +0.04 per other contract term in the same section
//     beyond the first (capped at +0.16). Dense reference prose that forces
//     a guess hurts more than an aside.
//
// Grounded findings are pinned to 0.0 (nothing to fix). The sum is clamped
// to [0,1].
func severity(state State, competing int, r *rawRef) float64 {
	if state == StateGrounded {
		return 0.0
	}
	base := 0.30
	if state == StateUngrounded {
		base = 0.60
	}
	blast := 0.0
	if competing > 1 {
		blast = 0.12 * float64(competing-1)
		if blast > 0.24 {
			blast = 0.24
		}
	}
	load := 0.0
	if r.density > 1 {
		load = 0.04 * float64(r.density-1)
		if load > 0.16 {
			load = 0.16
		}
	}
	s := base + blast + load
	if s > 1.0 {
		s = 1.0
	}
	if s < 0.0 {
		s = 0.0
	}
	return roundTo(s, 2)
}

func roundTo(f float64, places int) float64 {
	p := 1.0
	for i := 0; i < places; i++ {
		p *= 10
	}
	return float64(int64(f*p+0.5)) / p
}

// buildFix returns the suggested edit for a non-grounded finding (nil for
// grounded). The kind follows the situation: a bare collision under no
// binding -> link the first use; binding context (heading/title) but no
// confirmation -> define the term on first use; a near-miss qualified form
// -> qualify.
func buildFix(state State, elem lexEntry, r *rawRef) *Fix {
	if state == StateGrounded {
		return nil
	}
	ref := fmt.Sprintf("[`%s`](/reference/fidl/%s#%s)", elem.display, libraryOf(elem.elementID), elem.display)
	if state == StateGuessing {
		return &Fix{
			Kind:       FixDefineTerm,
			Suggestion: fmt.Sprintf("The section is about %s but never binds “%s” to the %s. Define it on first use.", elem.display, r.token, kindNoun(elem.kind)),
			Markdown:   fmt.Sprintf("the %s %s", ref, kindNoun(elem.kind)),
		}
	}
	return &Fix{
		Kind:       FixLinkFirstUse,
		Suggestion: fmt.Sprintf("Link the first mention of “%s” in this page to the %s reference.", r.token, elem.display),
		Markdown:   ref,
	}
}

// libraryOf returns the library segment of an element ID
// ("fuchsia.driver.framework/Node" -> "fuchsia.driver.framework").
func libraryOf(id string) string {
	if i := strings.LastIndex(id, "/"); i > 0 {
		return id[:i]
	}
	return id
}

// kindNoun maps a contract kind to the prose noun used in fix copy.
func kindNoun(kind string) string {
	switch kind {
	case "PROTOCOL":
		return "protocol"
	case "TYPE":
		return "type"
	case "METHOD", "CPP_METHOD":
		return "method"
	default:
		return "API"
	}
}

// rollupElements builds the per-element rollup: best state across refs (or
// not_mentioned for elements with zero references), ref_counts, finding_ids.
// Every lexicon element appears exactly once, in element_id order.
func rollupElements(lx *lexicon, elemRefs map[string][]refState, elemFindingIDs map[string][]string) []Element {
	out := make([]Element, 0, len(lx.entries))
	for i := range lx.entries {
		e := lx.entries[i]
		refs := elemRefs[e.elementID]
		var rc RefCounts
		for _, r := range refs {
			switch r.state {
			case StateGrounded:
				rc.Grounded++
			case StateGuessing:
				rc.Guessing++
			case StateUngrounded:
				rc.Ungrounded++
			}
		}
		state := bestElementState(rc, len(refs) > 0)
		fids := elemFindingIDs[e.elementID]
		if fids == nil {
			fids = []string{}
		}
		out = append(out, Element{
			ElementID:  e.elementID,
			Display:    e.display,
			Kind:       e.kind,
			State:      state,
			Mentioned:  len(refs) > 0,
			RefCounts:  rc,
			FindingIDs: fids,
		})
	}
	return out
}

// bestElementState picks the element's rollup state. "Best" = the most
// grounded state present (worst-state weighting is for the bar, not the
// per-element verdict): an element with any grounded ref is Grounded; else
// any guessing -> Guessing; else Ungrounded; no refs -> not_mentioned.
// This mirrors the fixture (NodeController has a guessing ref but rolls up
// Grounded because it also has grounded refs).
func bestElementState(rc RefCounts, mentioned bool) State {
	if !mentioned {
		return StateNotMentioned
	}
	if rc.Grounded > 0 {
		return StateGrounded
	}
	if rc.Guessing > 0 {
		return StateGuessing
	}
	return StateUngrounded
}

// rollupSummary aggregates the library-level numbers + the headline.
func rollupSummary(elements []Element, findings []Finding) Summary {
	var s Summary
	s.ElementsTotal = len(elements)
	for _, e := range elements {
		if e.Mentioned {
			s.ElementsMentioned++
		} else {
			s.ElementsNotMentioned++
		}
		switch e.State {
		case StateGrounded:
			s.ElementsGrounded++
		case StateGuessing:
			s.ElementsGuessing++
		case StateUngrounded:
			s.ElementsUngrounded++
		}
	}
	for _, f := range findings {
		s.ReferencesTotal++
		switch f.State {
		case StateGrounded:
			s.ReferencesGrounded++
		case StateGuessing:
			s.ReferencesGuessing++
		case StateUngrounded:
			s.ReferencesUngrounded++
		}
	}
	s.ForcesAGuess = s.ReferencesGuessing + s.ReferencesUngrounded
	s.Headline = fmt.Sprintf("%d references force an agent to guess or worse", s.ForcesAGuess)
	return s
}
