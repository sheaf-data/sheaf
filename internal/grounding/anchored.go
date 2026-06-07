package grounding

import (
	"strings"

	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

// This file exposes the anchored-mention half of the Grounding detector as
// a reusable, exported surface for the concept-doc ingestion engine
// (internal/adapters/conceptdoc). Grounding's own pipeline classifies EVERY
// collision (grounded / guessing / ungrounded) so it can show the cold
// stranger what forces a guess. Concept-doc attribution wants the opposite
// cut: take ONLY the high-confidence anchored hits and drop the
// guessing/ungrounded bucket entirely (REQUIREMENTS-concept-ingest.md
// decision 2 — "anchored mentions only; bare prose collisions do NOT
// attribute").
//
// Rather than copy the lexicon + scanner + anchor predicates into a second
// package (and let the two drift), conceptdoc calls AnchoredMentions here.
// The anchor predicates reused are exactly the detector's CONFIRMING
// anchors, evaluated at the MENTION SITE:
//
//   - qualified_mention  — backticked `Node`, PascalCase-as-written, or a
//     dotted/slash-qualified contract segment (qualifiedMention).
//   - link               — inside a markdown link whose target resolves to
//     the element (linkBinds).
//   - defined_term       — emphasized *Node* / **Node** / _Node_, but ONLY
//     when the emphasized token is the element's exact name, never a bare
//     generic word (definedTermBinds gated on exactName, matching
//     computeSectionAnchors).
//   - exact_name         — the element's full, non-colliding name written
//     out in prose ("NodeController"): the agent can resolve it unaided, so
//     the detector's bucket() treats it as confirming. We mirror that here.
//
// Page-title and heading anchors are deliberately NOT included: in the
// detector they are binding CONTEXT that tops out at Guessing, never
// Grounded. A concept-doc attribution must be a real anchored reference, so
// a section merely titled "Nodes" does not attribute the Node element off a
// bare "node" in its prose.
//
// Note on scope: the detector grounds at SECTION granularity (one anchor
// grounds every bare mention in the same heading stack) because it is
// answering "can a reader in this section resolve this token". Concept-doc
// attribution asks a coarser question — "does any doc anchor this element at
// least once" — so AnchoredMentions evaluates each anchor strictly at the
// mention's own site. That is STRICTER than the detector (a section's lone
// backticked `Node` grounds sibling bare mentions for grounding, but only
// the backticked mention itself is reported as anchored here), which keeps
// attribution conservative: a bare collision never attributes, even when a
// confirming anchor for the same element exists elsewhere in the section.

// AnchoredMention is one high-confidence reference from a doc to a contract
// element: the element it binds, where it sits, and which confirming anchor
// fired. Emitted only when an anchor is present at the mention site — bare
// collisions are dropped, not downgraded.
type AnchoredMention struct {
	ElementID      string
	ElementDisplay string
	ElementKind    string // ContractElementKind string ("PROTOCOL", "TYPE", …)

	DocPath      string
	Line         int
	SectionPath  []string
	Excerpt      string
	Token        string     // verbatim surface token as written in prose
	TokenSpan    Span       // rune offset+len within Excerpt (UI highlight)
	Anchor       AnchorKind // which confirming anchor fired
	AnchorDetail string     // human-readable "why" (e.g. "backticked `Node`")
}

// AnchoredMentions runs the Grounding scanner over the given docs and
// returns ONLY the anchored mentions (the confirming-anchor subset),
// attributed to in-scope elements of `library`. Deterministic: mentions
// come out in document-then-offset order, matching the detector's scan
// order, so finding/claim IDs assigned downstream are stable.
//
// Suppression (.sheafignore-style) is applied identically to the detector:
// a (doc, token, elementID) tuple on the suppress list is dropped. Pass nil
// to suppress nothing.
func AnchoredMentions(library string, elements []*contractpb.ContractElement, docs []Doc, suppress *Suppression) []AnchoredMention {
	if suppress == nil {
		suppress = emptySuppression()
	}
	lx := buildLexicon(library, elements)

	var refs []rawRef
	for _, d := range docs {
		refs = append(refs, scanDoc(lx, d)...)
	}

	docByPath := map[string][]byte{}
	for _, d := range docs {
		docByPath[d.Path] = d.Body
	}

	var out []AnchoredMention
	for ri := range refs {
		r := &refs[ri]
		if len(r.owners) == 0 {
			continue
		}
		body := docByPath[r.docPath]
		// One surface token can collide with several elements ("node" ->
		// Node + NodeController). Emit an anchored mention for EVERY owner
		// the token anchors to — attribution is per element, and a single
		// `NodeController` backtick should attribute NodeController, not its
		// bare-tail collision sibling. Each owner is anchor-checked on its
		// own terms.
		for _, idx := range r.owners {
			elem := lx.entries[idx]
			if suppress.suppressed(r.docPath, r.token, elem.elementID) {
				continue
			}
			anchor, detail, ok := anchoredAtSite(body, r, elem)
			if !ok {
				continue
			}
			out = append(out, AnchoredMention{
				ElementID:      elem.elementID,
				ElementDisplay: elem.display,
				ElementKind:    elem.kind,
				DocPath:        r.docPath,
				Line:           r.line,
				SectionPath:    r.sectionPath,
				Excerpt:        r.excerpt,
				Token:          r.token,
				TokenSpan:      r.tokenSpan,
				Anchor:         anchor,
				AnchorDetail:   detail,
			})
		}
	}
	return out
}

// anchoredAtSite reports whether the reference is anchored to elem AT ITS
// OWN SITE by a confirming anchor, returning the anchor kind + detail.
//
// This is the concept-doc attribution gate, and it is deliberately STRICTER
// than the Grounding detector's own anchor check (qualifiedMention/bucket).
// The Phase 3 precision audit found the detector's anchor definition, applied
// as a coverage signal, is ~33% precise — inflated ~9x by false positives
// from three mechanisms: (a) the exact-name / PascalCase-as-written branch
// firing off a single bare word ("the Configuration", "we can use…"); (b) a
// backticked/qualified hit firing off a bare common-noun TAIL embedded in a
// path/keyword/phrase (`//build/drivers/…`, “ `protocol` “); (c) a
// dotted FQN from ANOTHER library tail-matching this domain's same-named
// element (`fuchsia.tracing.provider.Registry` -> ui.activity/Provider).
//
// Two fixes close those, scoped to THIS seam only (the shared
// qualifiedMention / lexicon / scanner are untouched, so the Grounding
// surface keeps its existing behavior and the fixture parity tests stay
// green):
//
//   - Fix 1 (bareProseAttributes): a BARE-PROSE anchor (not backticked, not a
//     link, not a defined term) attributes ONLY when the matched surface is
//     the element's FULL MULTI-WORD name written out — its identifier
//     decomposes into >=2 CamelCase/separator components AND the prose token
//     spells out that whole compound (the spaced split "node controller" or
//     the concatenated/PascalCase "NodeController"). A SINGLE-WORD element
//     name NEVER attributes via bare prose — regardless of length or a
//     stoplist (long dictionary words like "Configuration"/"Presentation"
//     collide just as readily as short ones). Single-word elements still
//     attribute via backtick / link / defined term.
//   - Fix 2 (identifierAnchorAttributes): a BACKTICKED or DOTTED/SLASH-
//     QUALIFIED hit attributes ONLY when (a) the prose token IS the element's
//     own identifier — the whole backtick/identifier content equals the
//     element name, or the final dotted/`::`/slash segment does, in the
//     element's PascalCase identifier case — and (b) if the identifier is a
//     fully-qualified name carrying a library prefix, that prefix matches the
//     element's own library. A lowercase path component, a manifest/FIDL
//     keyword, an embedded word in a backticked phrase, or a foreign-library
//     FQN tail does NOT attribute.
//
// Precedence (qualified-identifier > link > defined_term > bare-prose) is
// presentation only — any one firing is sufficient.
func anchoredAtSite(body []byte, r *rawRef, elem lexEntry) (AnchorKind, string, bool) {
	// Fix 2: backticked or dotted/slash-qualified identifier. Must be the
	// element's OWN identifier (whole, or final segment) in PascalCase, and
	// same-library when the identifier is a foreign-prefixed FQN.
	if ok, d := identifierAnchorAttributes(body, r, elem); ok {
		return AnchorQualifiedMention, d, true
	}
	// Link anchor — STRICTER than the detector's linkBinds (which the
	// Grounding surface keeps): for attribution the resolved tail must be the
	// element's WHOLE name, not a hyphen/dot-delimited word inside a longer
	// heading slug. This kills the same single-bare-word leakage Fix 1/Fix 2
	// close on the prose/identifier side — e.g. a link to the heading
	// "#mapping-a-vmo-to-a-vmar-in-a-driver" or the glossary "#board-driver"
	// must NOT attribute the Driver protocol off the trailing word "driver".
	if ok, d := linkAnchorAttributes(body, r, elem); ok {
		return AnchorLink, d, true
	}
	// defined_term — emphasized *Name* / **Name** / _Name_. STRICTER than the
	// detector's r.exactName gate on TWO axes:
	//
	//   (1) the emphasized token must be THIS element's own whole name, not a
	//       bare common-noun tail it happens to share. Without this, a single
	//       bold **Driver** lights up every element whose name ends in "Driver"
	//       (Manager.DisableDriver, NodeController.WaitForDriver, …) — the
	//       Class B bare-tail collision leaking through the emphasis anchor.
	//   (2) the emphasis must delimit EXACTLY the token — open immediately
	//       before it AND close immediately after it — so the emphasized SPAN
	//       is the element name alone, not a longer bold/italic English phrase
	//       that merely begins (or contains) the name. The detector's
	//       definedTermBinds only checks the OPENING marker, so a bold phrase
	//       like "**Device administrators**", "**Block Map**" or
	//       "**Condition statements**" fires "Device"/"Block"/"Condition" off
	//       the leading word. emphasisTightlyWraps closes that: a defined term
	//       is a term, not a sentence fragment that starts with one.
	//
	// A single-word emphasized name (**Driver**) IS allowed (the audit keeps
	// defined-term as a valid anchor for single-word names), but only for the
	// element actually named "Driver" and only when the whole emphasized span
	// is "Driver".
	if r.exactName && tokenIsElementName(r.token, elem.display) && emphasisTightlyWraps(body, r) {
		if ok, d := definedTermBinds(body, r); ok {
			return AnchorDefinedTerm, d, true
		}
	}
	// Fix 1: the element's full MULTI-WORD name written out in bare prose.
	if ok, d := bareProseAttributes(r, elem); ok {
		return AnchorQualifiedMention, d, true
	}
	return "", "", false
}

// emphasisTightlyWraps reports whether markdown emphasis delimits EXACTLY the
// reference's token: an opening marker run sits immediately before the token
// start and the MATCHING closing run sits immediately after the token end, so
// the emphasized span is the token alone. This is the boundary the concept-doc
// defined_term anchor requires, and it is stricter than detect.go's
// definedTermBinds (which only inspects the opening marker and so fires the
// leading word of a multi-word emphasized phrase).
//
// Supported forms, with marker M in {'*','_'}:
//   - single:   M token M        (*Driver*  / _Driver_)
//   - bold:     MM token MM      (**Driver**)
//
// A trailing closing run LONGER than the opening run (e.g. opened with '*' but
// the next char after the token is "**") is treated as not-tight: the span
// boundaries must be a matched pair. Surrounding text on either side of the
// emphasized span is fine — only the chars adjacent to the token matter.
func emphasisTightlyWraps(body []byte, r *rawRef) bool {
	start := r.absOffset
	end := r.absOffset + len(r.token)
	if start <= 0 || end >= len(body) {
		// Need at least one delimiter byte on each side.
		if start == 0 || end >= len(body) {
			return false
		}
	}
	isEmph := func(b byte) bool { return b == '*' || b == '_' }
	// Closing marker run immediately AFTER the token.
	if end >= len(body) || !isEmph(body[end]) {
		return false
	}
	closeCh := body[end]
	closeLen := 0
	for end+closeLen < len(body) && body[end+closeLen] == closeCh {
		closeLen++
	}
	// Opening marker run immediately BEFORE the token, using the SAME marker
	// char, of the SAME length (matched pair: * with *, ** with **).
	if start-closeLen < 0 {
		return false
	}
	for i := 0; i < closeLen; i++ {
		if body[start-1-i] != closeCh {
			return false
		}
	}
	// The opening run must not extend further than closeLen (so "**" opening
	// against a single "*" close is rejected): the char just past the opening
	// run must not be the same marker.
	if start-closeLen-1 >= 0 && body[start-closeLen-1] == closeCh {
		return false
	}
	// Only single (closeLen==1) and bold (closeLen==2) emphasis are markdown
	// defined-term forms; a run of 3+ is not emphasis we attribute on.
	return closeLen == 1 || closeLen == 2
}

// camelComponents splits an element's display name into its CamelCase /
// separator components, reusing the lexicon's camelSplitRx so the
// decomposition matches the surface forms the scanner built ("NodeController"
// -> ["Node","Controller"], "DriverHost" -> ["Driver","Host"]). A name that
// decomposes into >=2 components is a "multi-word" identifier for Fix 1.
func camelComponents(display string) []string {
	return camelSplitRx.FindAllString(display, -1)
}

// isMultiWordName reports whether the element's identifier is a compound of
// >=2 CamelCase/separator components — the only shape Fix 1 lets attribute
// from bare prose.
func isMultiWordName(display string) bool {
	return len(camelComponents(display)) >= 2
}

// bareProseAttributes implements Fix 1. It returns true when the reference is
// a bare-prose mention (NOT backticked, NOT a link, NOT a defined term — the
// caller checks those first) AND the matched surface is the element's full
// multi-word name written out. A single-word element name never attributes
// here; a bare common-noun tail of a multi-word name never attributes here.
//
// "Full name written out" means the token, normalized (lowercased, internal
// whitespace collapsed), equals the element's concatenated lowercased name
// (the "nodecontroller"/"NodeController" spelling) or its space-separated
// CamelCase split ("node controller"). The scanner already only emits a
// rawRef whose matched form is one of the element's surface forms, so
// r.exactName tells us the token is the element's WHOLE name (not a tail) for
// this owner; we additionally require the name to be multi-word.
func bareProseAttributes(r *rawRef, elem lexEntry) (bool, string) {
	// Bare prose can only attribute on an exact-name match for this token. A
	// bare common-noun tail (exactName == false on every owner of this form,
	// surfaced as r.tailOnly) or any non-exact match is rejected outright —
	// "driver"/"socket"/"manager" never attribute from prose.
	if !r.exactName || r.collision {
		return false, ""
	}
	// Single-word identifier (e.g. Configuration, Presentation, Flatland,
	// View, Driver): NEVER via bare prose. Length and stoplists do not help —
	// the audit proved long dictionary words collide as readily as short
	// ones. Such elements still attribute via backtick / link / defined term.
	if !isMultiWordName(elem.display) {
		return false, ""
	}
	// Confirm the token really spells out the whole compound name, not just a
	// coincidental exactName flag from a sibling owner of a shorter form.
	if tokenIsElementName(r.token, elem.display) {
		return true, "named " + r.token
	}
	return false, ""
}

// tokenIsElementName reports whether the prose token names the element's OWN
// whole identifier — its concatenated lowercased form ("nodecontroller"), its
// space-separated CamelCase split ("node controller"), or its PascalCase
// identifier as written ("NodeController"). A single trailing plural 's' is
// tolerated. It is NOT satisfied by a bare common-noun TAIL of a multi-word
// name (the lexicon only emits such a tail as a non-exact surface form, and
// the token "driver" normalizes to "driver" which never equals the whole
// "driverhost"/"driver host"). Used to confirm a prose / defined-term mention
// is really about this element, not a same-tailed sibling.
func tokenIsElementName(token, display string) bool {
	if tokenIsIdentifierName(token, display) {
		return true // exact PascalCase identifier (case-sensitive), plural ok
	}
	norm := normalizeProseToken(token)
	concat := strings.ToLower(display)
	spaced := strings.ToLower(strings.Join(camelComponents(display), " "))
	return norm == concat || norm == spaced
}

// normalizeProseToken lowercases a prose token and collapses internal
// whitespace runs to single spaces (the scanner's spaced surface forms are
// single-spaced), trimming a single trailing plural 's' so "driver hosts"
// matches the "driver host" form.
func normalizeProseToken(tok string) string {
	t := strings.ToLower(strings.Join(strings.Fields(tok), " "))
	t = strings.TrimSuffix(t, "s")
	return t
}

// identifierAnchorAttributes implements Fix 2. It fires only for a backticked
// or dotted/slash-qualified mention, and only when the prose token is the
// element's OWN identifier (the whole code span, or its final
// dotted/`::`/slash segment, in PascalCase) AND — when the identifier is a
// foreign-prefixed FQN — the library prefix matches the element's library.
//
// For a Type::member / Protocol/Method form, the head element is credited
// when the head identifier matches the element's name and the library (if
// the head is itself qualified) matches.
func identifierAnchorAttributes(body []byte, r *rawRef, elem lexEntry) (bool, string) {
	start := r.absOffset
	end := r.absOffset + len(r.token)

	backticked := backtickedAt(body, start, end)
	// The qualifier shape: a '.'/'/'/'::' immediately before the token (the
	// token is a trailing segment) or immediately after (the token is a head
	// introducing a member).
	qualBefore := false
	if start >= 1 {
		if p := body[start-1]; p == '.' || p == '/' {
			qualBefore = true
		}
	}
	if !qualBefore && start >= 2 && body[start-1] == ':' && body[start-2] == ':' {
		qualBefore = true
	}
	qualAfter := false
	if end < len(body) {
		if n := body[end]; n == '.' || n == '/' {
			// next char must look like a member (capital letter) to be a
			// Type.Method / Type/Method qualifier, not a sentence period.
			if end+1 < len(body) && body[end+1] >= 'A' && body[end+1] <= 'Z' {
				qualAfter = true
			}
		}
		if !qualAfter && end+1 < len(body) && body[end] == ':' && body[end+1] == ':' {
			qualAfter = true
		}
	}

	// Not an identifier anchor at all -> let bare-prose (Fix 1) decide.
	if !backticked && !qualBefore && !qualAfter {
		return false, ""
	}

	// The prose token must be written in the element's identifier case: the
	// element's PascalCase display name (allowing a trailing plural 's'). This
	// alone rejects a lowercase path component ("driver" in `…/driver.h`) and
	// a lowercase manifest keyword (`protocol`, `expose`) — they are not the
	// element's PascalCase identifier even when backticked.
	if !tokenIsIdentifierName(r.token, elem.display) {
		return false, ""
	}

	// Extract the full enclosing identifier (the whole backtick content if
	// backticked, else the maximal dotted/slash/`::`-qualified run around the
	// token) so we can (1) confirm the token is the WHOLE token or its FINAL
	// segment — not a middle path component — and (2) read any library prefix.
	ident := enclosingIdentifier(body, start, end, backticked)
	seg, lib, ok := finalSegmentAndLibrary(ident, r.token)
	if !ok {
		return false, ""
	}
	// The matched segment must be the element's own name (PascalCase, plural
	// tolerated) — i.e. the token is the whole identifier or its final
	// dotted/slash segment, not an embedded interior word.
	if !tokenIsIdentifierName(seg, elem.display) {
		return false, ""
	}
	// Library-prefix rule (kills the cross-library FQN tail collision). When
	// the identifier carries a library prefix (a dotted FQN like
	// "fuchsia.tracing.provider.Registry" or "fuchsia.net.http/Loader"),
	// require that prefix to equal the element's own library. A bare backtick
	// with no library prefix ("`NodeController`") carries no prefix and is
	// allowed; a same-library FQN is allowed; a foreign-library FQN is not.
	if lib != "" {
		elemLib := libraryOf(elem.elementID)
		if !libraryPrefixMatches(lib, elemLib) {
			return false, ""
		}
	}

	switch {
	case backticked:
		return true, "backticked `" + elem.display + "`"
	default:
		return true, "qualified " + elem.display
	}
}

// tokenIsIdentifierName reports whether tok, written in source case, is the
// element's identifier display name — exact match, or the display name plus a
// single trailing plural 's'. The comparison is case-SENSITIVE on the leading
// run so a lowercase prose "driver" or "protocol" does NOT match the
// PascalCase identifier "Driver"/"Protocol" (the author wrote the common noun,
// not the type). A trailing 's' is tolerated only when the base already
// matches.
func tokenIsIdentifierName(tok, display string) bool {
	if tok == display {
		return true
	}
	if strings.HasSuffix(tok, "s") && tok[:len(tok)-1] == display {
		return true
	}
	return false
}

// enclosingIdentifier returns the identifier string that wholly contains the
// token. When backticked, it is the content between the surrounding backticks
// on the line. Otherwise it is the maximal run of identifier/qualifier bytes
// ([A-Za-z0-9_], '.', '/', ':') around the token — the dotted/slash FQN the
// token sits in.
func enclosingIdentifier(body []byte, start, end int, backticked bool) string {
	if backticked {
		// Find the backtick pair on the line bounding the token.
		ls := start
		for ls > 0 && body[ls-1] != '\n' {
			ls--
		}
		le := end
		for le < len(body) && body[le] != '\n' {
			le++
		}
		// Nearest backtick to the left of start (the opening tick of the span)
		// and to the right of end (the closing tick), within the line.
		left := -1
		for i := ls; i < start; i++ {
			if body[i] == '`' {
				left = i + 1
			}
		}
		right := -1
		for i := end; i < le; i++ {
			if body[i] == '`' {
				right = i
				break
			}
		}
		if left >= 0 && right >= 0 && left <= start && right >= end {
			return string(body[left:right])
		}
		// Backtick immediately adjacent on one side only (token is the whole
		// inline code, e.g. `Driver`): fall through to the identifier-run scan.
	}
	isIdentByte := func(b byte) bool {
		return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') ||
			(b >= '0' && b <= '9') || b == '_' || b == '.' || b == '/' || b == ':'
	}
	s := start
	for s > 0 && isIdentByte(body[s-1]) {
		s--
	}
	e := end
	for e < len(body) && isIdentByte(body[e]) {
		e++
	}
	return string(body[s:e])
}

// finalSegmentAndLibrary splits a qualified identifier into its final segment
// and the library prefix that precedes it, if any. It locates the token
// within the identifier and returns:
//   - seg: the trailing identifier segment that the token belongs to. For
//     "fuchsia.driver.framework/NodeController" -> "NodeController"; for
//     "Driver::Stop" with token "Driver" (a head) -> "Driver"; for
//     "fuchsia.net.http.Loader" -> "Loader".
//   - lib: the library prefix when the identifier is a dotted/slashed FQN
//     ("fuchsia.driver.framework" for both ".../NodeController" and
//     "fuchsia.driver.framework.Driver"); "" when the identifier is a bare
//     name with no qualifier (a plain "`NodeController`").
//   - ok: false when the token cannot be located as a whole segment (e.g. it
//     is an interior path component like "drivers" in "//build/drivers/x").
//
// The token is the head of a member call (qualAfter) when it is followed by a
// '/'/'.'/'::' member separator; then seg is the token itself and the library
// is whatever dotted prefix precedes the token.
func finalSegmentAndLibrary(ident, token string) (seg, lib string, ok bool) {
	if ident == "" {
		return "", "", false
	}
	// Split into segments on '/' first (FIDL "library/Type" or
	// "Protocol/Method"), keeping the dotted library part intact as one chunk.
	// Then the last slash-chunk may itself be a dotted FQN
	// ("fuchsia.net.http.Loader").
	slashParts := strings.Split(ident, "/")

	// Locate which slash-part contains the token as a whole word.
	for pi, sp := range slashParts {
		// Within a slash part, the token may be the whole part, the final
		// dotted segment, or a head before "::".
		// Strip a "::member" tail so "Driver::Stop" head-matches "Driver".
		head := sp
		if i := strings.Index(head, "::"); i >= 0 {
			head = head[:i]
		}
		// Try whole-part / final-dotted-segment match against the token.
		dotSegs := strings.Split(head, ".")
		last := dotSegs[len(dotSegs)-1]
		base := strings.TrimSuffix(token, "s")
		matchLast := last == token || last == base
		matchWhole := sp == token || strings.TrimSuffix(sp, "s") == token
		if !matchLast && !matchWhole {
			// token is not the final/whole segment of this slash-part; it
			// might be an interior path component — keep scanning other parts.
			// If the token appears only as an interior dotted segment (not the
			// last), reject (e.g. "drivers" inside "//build/drivers/x.txt").
			continue
		}
		seg = last
		if matchWhole && !matchLast {
			seg = strings.TrimSuffix(sp, "s")
		}
		// Library prefix:
		//  - if there is a slash and the token is in the LAST slash-part, the
		//    library is everything before the last '/' (FIDL "library/Type").
		//  - else if the matched dotted segment is the LAST of a dotted run,
		//    the library is the dotted prefix before it
		//    ("fuchsia.net.http" for ".Loader").
		switch {
		case len(slashParts) > 1 && pi == len(slashParts)-1:
			lib = strings.Join(slashParts[:pi], "/")
			// the library chunk itself may end with a trailing '.'? no — join
			// is exact. Strip any leading "//" build-path noise: such a path
			// is not a FIDL library, but in that case the token would be an
			// interior component and we'd have `continue`d already.
		case len(dotSegs) > 1 && last == dotSegs[len(dotSegs)-1]:
			lib = strings.Join(dotSegs[:len(dotSegs)-1], ".")
		default:
			lib = ""
		}
		// A library made only of empty parts (leading "//…") is not a real
		// library prefix; treat as none so we don't falsely reject.
		if strings.Trim(lib, "/.") == "" {
			lib = ""
		}
		return seg, lib, true
	}
	return "", "", false
}

// libraryPrefixMatches reports whether a library prefix read off a qualified
// identifier names the same FIDL library as the element. The element library
// is the canonical dotted form ("fuchsia.driver.framework"); the prefix from
// the identifier may be the same dotted form, or (rarely) a slash form. Exact
// dotted equality is required — a different library with the same final type
// name ("fuchsia.tracing.provider" vs "fuchsia.ui.activity") must not match.
func libraryPrefixMatches(prefix, elemLibrary string) bool {
	if prefix == "" || elemLibrary == "" {
		return false
	}
	p := strings.ToLower(strings.Trim(prefix, "/."))
	e := strings.ToLower(strings.Trim(elemLibrary, "/."))
	return p == e
}

// linkAnchorAttributes is the concept-doc link anchor. It fires only when the
// token sits inside a markdown [text](target) link whose target RESOLVES to
// this element by a WHOLE-SEGMENT, CASE-SENSITIVE identifier match (the
// detector's linkBinds is looser — case-insensitive, accepts the name as any
// hyphen/dot-delimited word in the tail — and is left untouched for the
// Grounding surface). Resolution rules:
//
//   - The resolved tail is the target's #fragment if present, else the last
//     '/'-separated path segment (with any file extension still attached — see
//     below).
//   - The tail must EQUAL the element's identifier in the element's OWN case
//     (the PascalCase display name), with NO plural tolerance and NO slug /
//     partial-segment match. "#Node" / "/Node" / "fuchsia.driver.framework#Node"
//     resolve Node. The leakers the re-audit found do NOT resolve:
//   - a lowercase path FILENAME — "host.fidl"→Host, "debug.h"→Debug,
//     "process.md"→Process, "session.fidl"→Session — because the FIDL
//     filename is lowercase and the element identifier is PascalCase, and
//     because we no longer strip the extension before comparing (a file is
//     a path, not an anchor to the type);
//   - a PLURAL path segment — "/src/devices"→Device, "#resolvers"→Resolver,
//     "#logs"→Log — plural tolerance is gone;
//   - a hyphenated heading slug — "#board-driver"→Driver,
//     "#device-vs-node"→Node — the name is only an embedded word.
//   - Library-prefix rule: when the target carries a library prefix before a
//     '#'/'/' boundary (e.g. "…/fuchsia.driver.framework#Node"), that prefix
//     must name the element's own library — a foreign "lib#SameTail" link
//     does not attribute.
func linkAnchorAttributes(body []byte, r *rawRef, elem lexEntry) (bool, string) {
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
	name := elementDisplay(elem.elementID)
	if name == "" {
		name = elem.display
	}
	if name == "" {
		return false, ""
	}
	for _, m := range mdLinkRx.FindAllStringSubmatchIndex(line, -1) {
		textStart, textEnd := m[2], m[3]
		targetStart, targetEnd := m[4], m[5]
		if rel < textStart || rel >= textEnd {
			continue
		}
		target := line[targetStart:targetEnd]
		if ok := linkTargetIsExactly(target, name, libraryOf(elem.elementID)); ok {
			return true, target
		}
	}
	return false, ""
}

// linkTargetIsExactly reports whether a link target resolves to the element by
// a WHOLE-SEGMENT, CASE-SENSITIVE identifier match, enforcing the library-prefix
// rule when the target carries a library before the type segment.
//
// name is the element's identifier in its own case (PascalCase display name).
// The resolved tail must EQUAL name exactly: no case folding, no trailing
// plural 's', no extension stripping, no embedded-word match. This is what
// distinguishes a real anchor (a "#NodeController" fragment the author wrote in
// the type's case) from a path component that merely shares the spelling (a
// lowercase "node_controller.fidl" filename, a plural "/devices" directory).
func linkTargetIsExactly(target, name, elemLibrary string) bool {
	lt := strings.TrimSpace(target)
	if lt == "" || name == "" {
		return false
	}
	var tail, prefix string
	if i := strings.LastIndexByte(lt, '#'); i >= 0 {
		tail = lt[i+1:]
		prefix = lt[:i]
	} else if i := strings.LastIndexByte(lt, '/'); i >= 0 {
		tail = lt[i+1:]
		prefix = lt[:i]
	} else {
		tail = lt
	}
	// Whole-segment, case-SENSITIVE equality. No plural tolerance, no file
	// extension stripping: "devices" != "Device", "host.fidl" != "Host",
	// "board-driver" != "Driver". Only "Node" == "Node" attributes.
	if tail != name {
		return false
	}
	// Library-prefix rule: when the prefix carries a dotted FIDL library name
	// (the last '/'-segment of the prefix is a dotted token like
	// "fuchsia.driver.framework"), it must match the element's library. A
	// prefix that is a plain doc path ("/reference/fidl/…", "/docs/…") carries
	// no FIDL library token, so it imposes no constraint.
	libTok := prefix
	if i := strings.LastIndexByte(libTok, '/'); i >= 0 {
		libTok = libTok[i+1:]
	}
	if strings.Contains(libTok, ".") && libTok != "" {
		// Looks like a dotted FIDL library qualifier — enforce the match.
		return libraryPrefixMatches(libTok, elemLibrary)
	}
	return true
}
