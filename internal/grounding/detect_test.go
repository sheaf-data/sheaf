package grounding

import (
	"strings"
	"testing"

	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

// elem is a tiny helper to build a contract element for the lexicon.
//
//nolint:unused // dormant grounding detector tests, retained (PR #99)
func elem(id string, kind contractpb.ContractElementKind) *contractpb.ContractElement {
	return &contractpb.ContractElement{Id: id, Kind: kind, Library: "lib"}
}

// driverElems mirrors a slice of the fuchsia.driver.framework surface so
// the collision lexicon has Node / NodeController / CompositeNodeManager /
// NodePropertyKey (the fixture's four elements).
func driverElems() []*contractpb.ContractElement {
	const lib = "fuchsia.driver.framework"
	return []*contractpb.ContractElement{
		{Id: lib + "/Node", Kind: contractpb.ContractElementKind_PROTOCOL, Library: lib},
		{Id: lib + "/NodeController", Kind: contractpb.ContractElementKind_PROTOCOL, Library: lib},
		{Id: lib + "/CompositeNodeManager", Kind: contractpb.ContractElementKind_PROTOCOL, Library: lib},
		{Id: lib + "/NodePropertyKey", Kind: contractpb.ContractElementKind_TYPE, Library: lib},
	}
}

// build runs the detector over a single in-memory doc and returns the report.
func build(t *testing.T, elements []*contractpb.ContractElement, library, path, body string) *Report {
	t.Helper()
	rep, err := Build(Options{
		Library:     library,
		GeneratedAt: "2026-06-01T00:00:00Z",
		Elements:    elements,
		Docs:        []Doc{{Path: path, Body: []byte(body)}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return rep
}

// findingFor returns the first finding whose token (lowercased) equals tok
// and whose element display equals display. Fails if none.
func findingFor(t *testing.T, rep *Report, display, tok string) Finding {
	t.Helper()
	for _, f := range rep.Findings {
		if f.ElementDisplay == display && strings.EqualFold(f.Token, tok) {
			return f
		}
	}
	t.Fatalf("no finding for element=%s token=%s; findings=%+v", display, tok, summarize(rep))
	return Finding{}
}

func summarize(rep *Report) string {
	var b strings.Builder
	for _, f := range rep.Findings {
		b.WriteString("\n  ")
		b.WriteString(f.ElementDisplay)
		b.WriteString("/")
		b.WriteString(f.Token)
		b.WriteString("=")
		b.WriteString(string(f.State))
	}
	return b.String()
}

// --- State: GROUNDED via qualified (backticked) mention -----------------

func TestGrounded_QualifiedMention(t *testing.T) {
	body := "# Drivers\n\nThe parent drives the child through the `NodeController` protocol returned by AddChild.\n"
	rep := build(t, driverElems(), "fuchsia.driver.framework", "docs/x.md", body)
	f := findingFor(t, rep, "NodeController", "NodeController")
	if f.State != StateGrounded {
		t.Fatalf("want grounded, got %s (checked=%+v)", f.State, f.Checked)
	}
	if !anchorFound(f, AnchorQualifiedMention) {
		t.Fatalf("expected qualified_mention anchor found; checked=%+v", f.Checked)
	}
	if f.Fix != nil {
		t.Fatalf("grounded finding must have nil fix, got %+v", f.Fix)
	}
	if f.Severity != 0.0 {
		t.Fatalf("grounded severity must be 0.0, got %v", f.Severity)
	}
}

// --- State: GROUNDED via link -------------------------------------------

func TestGrounded_Link(t *testing.T) {
	// A bare-word "node" that would otherwise be an ungrounded collision,
	// but it's inside a markdown link into the fidl reference -> grounded.
	body := "# Topology\n\nEach [node](/reference/fidl/fuchsia.driver.framework#Node) has a parent.\n"
	rep := build(t, driverElems(), "fuchsia.driver.framework", "docs/x.md", body)
	f := findingFor(t, rep, "Node", "node")
	if f.State != StateGrounded {
		t.Fatalf("want grounded via link, got %s (checked=%+v)", f.State, f.Checked)
	}
	if !anchorFound(f, AnchorLink) {
		t.Fatalf("expected link anchor found; checked=%+v", f.Checked)
	}
}

// --- State: GUESSING via heading-bound but unconfirmed -------------------

func TestGuessing_HeadingBoundUnconfirmed(t *testing.T) {
	// The heading binds "node", but the bare prose mention is never
	// qualified/linked/defined -> Guessing (context, no confirmation).
	body := "# Drivers and nodes\n\n## The node topology\n\nEach node exposes a controller that the parent uses.\n"
	rep := build(t, driverElems(), "fuchsia.driver.framework", "docs/x.md", body)
	f := findingFor(t, rep, "Node", "node")
	if f.State != StateGuessing {
		t.Fatalf("want guessing, got %s (checked=%+v)", f.State, f.Checked)
	}
	if !anchorFound(f, AnchorHeading) {
		t.Fatalf("expected heading anchor found; checked=%+v", f.Checked)
	}
	if anchorFound(f, AnchorQualifiedMention) || anchorFound(f, AnchorLink) {
		t.Fatalf("guessing finding must not have a confirming anchor; checked=%+v", f.Checked)
	}
	if f.Fix == nil || f.Fix.Kind != FixDefineTerm {
		t.Fatalf("guessing fix should be define_term, got %+v", f.Fix)
	}
}

// --- State: UNGROUNDED via bare collision in thin prose ------------------

func TestUngrounded_BareCollision(t *testing.T) {
	// No page title binding "node", no heading binding it, bare prose,
	// thin section (single reference) -> Ungrounded.
	body := "# Communication\n\n## Adding children\n\nWhen a driver wants a child, it adds a child node and waits.\n"
	rep := build(t, driverElems(), "fuchsia.driver.framework", "docs/x.md", body)
	f := findingFor(t, rep, "Node", "node")
	if f.State != StateUngrounded {
		t.Fatalf("want ungrounded, got %s (checked=%+v)", f.State, f.Checked)
	}
	// Every anchor recorded false — the "shows its work" audit trail.
	for _, c := range f.Checked {
		if c.Found {
			t.Fatalf("ungrounded finding should have no found anchor; %s found", c.Anchor)
		}
	}
	if len(f.Checked) != 6 {
		t.Fatalf("expected all 6 anchors in audit trail, got %d", len(f.Checked))
	}
	if f.Fix == nil || f.Fix.Kind != FixLinkFirstUse {
		t.Fatalf("ungrounded fix should be link_first_use, got %+v", f.Fix)
	}
	if f.Severity < 0.5 {
		t.Fatalf("ungrounded severity should be >= base 0.6-ish, got %v", f.Severity)
	}
}

// --- Multi-line section: section_path + excerpt are sentence-scoped ------

func TestMultiLineSection(t *testing.T) {
	body := "# Drivers and nodes\n\n## The node topology\n\n" +
		"The driver framework arranges drivers into a tree.\n" +
		"Each node in the driver framework exposes a controller that the parent uses to manage it.\n" +
		"Children are added beneath their parent.\n"
	rep := build(t, driverElems(), "fuchsia.driver.framework", "docs/x.md", body)
	f := findingFor(t, rep, "Node", "node")
	// section_path must be the H1>H2 stack at the mention.
	if len(f.SectionPath) != 2 || f.SectionPath[0] != "Drivers and nodes" || f.SectionPath[1] != "The node topology" {
		t.Fatalf("section_path wrong: %v", f.SectionPath)
	}
	// excerpt must be exactly the sentence containing the mention,
	// not the whole paragraph.
	if !strings.HasPrefix(f.Excerpt, "Each node in the driver framework") {
		t.Fatalf("excerpt should be the sentence, got %q", f.Excerpt)
	}
	if strings.Contains(f.Excerpt, "Children are added") {
		t.Fatalf("excerpt bled into the next sentence: %q", f.Excerpt)
	}
	// token_span must point at the matched token within the excerpt
	// (rune-indexed, matching the UI).
	runes := []rune(f.Excerpt)
	got := string(runes[f.TokenSpan.Start : f.TokenSpan.Start+f.TokenSpan.Len])
	if !strings.EqualFold(got, "node") {
		t.Fatalf("token_span %v points at %q, want 'node'", f.TokenSpan, got)
	}
}

// --- Common-English NON-collision must NOT flag --------------------------

func TestNonCollision_NoFlag(t *testing.T) {
	// "service", "system", "healthy" are common-English words, but NONE is
	// a name or common-noun tail of any element in the driver lexicon
	// (Node/NodeController/CompositeNodeManager/NodePropertyKey). A generic
	// sentence using only such words must produce zero findings — the
	// detector ONLY flags collisions with the KNOWN contract surface
	// (REQUIREMENTS §9: not a general prose linter).
	//
	// (Note: "manager" WOULD legitimately collide here because it is the
	// common-noun tail of CompositeNodeManager — so it is deliberately
	// absent from this non-collision sentence.)
	body := "# Overview\n\nThe service is running and the system is healthy.\n"
	rep := build(t, driverElems(), "fuchsia.driver.framework", "docs/x.md", body)
	if len(rep.Findings) != 0 {
		t.Fatalf("expected zero findings for non-contract common words, got %d:%s",
			len(rep.Findings), summarize(rep))
	}
}

// A word that IS a contract element but whose full multi-word name is
// spelled out is grounded-by-name, not ungrounded.
func TestExactMultiWordName_GroundedByName(t *testing.T) {
	// "node controller" spelled out as the exact (non-colliding multiword)
	// name. Even un-backticked, the agent can resolve it -> grounded.
	body := "# Drivers\n\nThe parent owns the node controller for each child.\n"
	rep := build(t, driverElems(), "fuchsia.driver.framework", "docs/x.md", body)
	f := findingFor(t, rep, "NodeController", "node controller")
	if f.State != StateGrounded {
		t.Fatalf("want grounded by exact name, got %s (checked=%+v)", f.State, f.Checked)
	}
}

// --- Conservative boundary: ambiguous-leaning -> Guessing not Ungrounded -

func TestConservativeBoundary_HeadingBoundLeansGuessing(t *testing.T) {
	// The conservative boundary (decision 5): when a heading (or the page
	// title) NAMES the element, a bare un-anchored mention in that section
	// is Guessing, not Ungrounded — the agent can infer the referent from
	// the section it's reading. This is the fixture's g-0002 shape. A bare
	// "node" under "## The node topology" must NOT be a hard red.
	body := "# Drivers\n\n## The node topology\n\n" +
		"The composite assembles from several parent nodes. The node is then bound.\n"
	rep := build(t, driverElems(), "fuchsia.driver.framework", "docs/x.md", body)
	var bare *Finding
	for i := range rep.Findings {
		f := &rep.Findings[i]
		if f.ElementDisplay == "Node" && strings.EqualFold(f.Token, "node") {
			bare = f
			break
		}
	}
	if bare == nil {
		t.Fatalf("expected a bare 'node' finding; got%s", summarize(rep))
	}
	if bare.State != StateGuessing {
		t.Fatalf("heading-bound bare collision should be Guessing, got %s; checked=%+v", bare.State, bare.Checked)
	}
}

// In-section confirmation grounds the OTHER bare mentions of the same
// element in that section (decision 7: established somewhere resolvable,
// not every token spelled out — but scoped to the section, not the whole
// page).
func TestSectionScopedGrounding(t *testing.T) {
	// "node controller" written out (exact name) in the section establishes
	// NodeController for that section; a later bare reference to it (none
	// here for Node) would be grounded. Here we assert the backticked
	// `NodeController` grounds the bare "NodeController" later in the SAME
	// section, but NOT a bare mention in a DIFFERENT section.
	body := "# Drivers\n\n" +
		"## Controllers\n\nThe `NodeController` is returned by AddChild. Each NodeController is owned by a parent.\n" +
		"## Other\n\nElsewhere a plain node is mentioned with no anchor at all.\n"
	rep := build(t, driverElems(), "fuchsia.driver.framework", "docs/x.md", body)
	// The bare "node" under "## Other" has no anchor and no heading binding
	// -> ungrounded (section scope did not leak the Controllers anchors).
	var other *Finding
	for i := range rep.Findings {
		f := &rep.Findings[i]
		if f.ElementDisplay == "Node" && strings.EqualFold(f.Token, "node") && contains(f.SectionPath, "Other") {
			other = f
		}
	}
	if other == nil {
		t.Fatalf("expected a bare 'node' finding under Other; got%s", summarize(rep))
	}
	if other.State != StateUngrounded {
		t.Fatalf("cross-section anchor must not ground a bare mention; got %s", other.State)
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// The canonical Ungrounded case from the fixture (g-0001): a bare "node"
// in THIN prose — one isolated mention, no title/heading binding, no dense
// surrounding contract terms — is a hard Ungrounded, NOT leaned to
// Guessing. This is the kill-risk boundary; it must stay red so the demo
// is defensible. (Contrast with the dense-section test above.)
func TestConservativeBoundary_ThinBareCollisionStaysUngrounded(t *testing.T) {
	body := "# Misc\n\nThe runtime tracks every node it owns.\n"
	rep := build(t, driverElems(), "fuchsia.driver.framework", "docs/x.md", body)
	f := findingFor(t, rep, "Node", "node")
	if f.State != StateUngrounded {
		t.Fatalf("thin bare collision should stay Ungrounded, got %s (checked=%+v)", f.State, f.Checked)
	}
}

// --- Candidates + competing_contract_refs --------------------------------

func TestCandidates_EnglishGlossOnCollision(t *testing.T) {
	body := "# Communication\n\n## Adding children\n\nWhen a driver wants a child, it adds a child node and waits.\n"
	rep := build(t, driverElems(), "fuchsia.driver.framework", "docs/x.md", body)
	f := findingFor(t, rep, "Node", "node")
	if len(f.Candidates) < 2 {
		t.Fatalf("collision should have >=2 candidates (contract + english), got %+v", f.Candidates)
	}
	var hasContract, hasEnglish bool
	for _, c := range f.Candidates {
		if c.IsContract && c.ElementID != nil {
			hasContract = true
		}
		if !c.IsContract && c.Kind == "english" && c.ElementID == nil {
			hasEnglish = true
		}
	}
	if !hasContract || !hasEnglish {
		t.Fatalf("want a contract candidate and an english gloss; got %+v", f.Candidates)
	}
}

// --- Rollup: per-element best state + ref_counts + not_mentioned ---------

func TestRollup_ElementStatesAndNotMentioned(t *testing.T) {
	// NodeController appears grounded (backtick) AND guessing (heading) ->
	// rolls up Grounded. NodePropertyKey is never mentioned -> not_mentioned.
	body := "# Drivers and nodes\n\n## Controllers\n\n" +
		"The parent drives the child through the `NodeController` protocol.\n" +
		"## The NodeController topology\n\nEach NodeController instance is owned by a node.\n"
	rep := build(t, driverElems(), "fuchsia.driver.framework", "docs/x.md", body)

	var nc, npk *Element
	for i := range rep.Elements {
		switch rep.Elements[i].Display {
		case "NodeController":
			nc = &rep.Elements[i]
		case "NodePropertyKey":
			npk = &rep.Elements[i]
		}
	}
	if nc == nil || npk == nil {
		t.Fatalf("missing rolled-up elements")
	}
	if nc.State != StateGrounded {
		t.Fatalf("NodeController should roll up Grounded, got %s (counts=%+v)", nc.State, nc.RefCounts)
	}
	if nc.RefCounts.Grounded < 1 {
		t.Fatalf("NodeController should have >=1 grounded ref, got %+v", nc.RefCounts)
	}
	if npk.State != StateNotMentioned || npk.Mentioned {
		t.Fatalf("NodePropertyKey should be not_mentioned/unmentioned, got %s mentioned=%v", npk.State, npk.Mentioned)
	}
	if len(npk.FindingIDs) != 0 {
		t.Fatalf("not_mentioned element should have no finding_ids, got %v", npk.FindingIDs)
	}
}

// --- Summary: forces_a_guess + headline ----------------------------------

func TestSummary_ForcesAGuessAndHeadline(t *testing.T) {
	body := "# Communication\n\n## Adding children\n\nWhen a driver wants a child, it adds a child node and waits.\n"
	rep := build(t, driverElems(), "fuchsia.driver.framework", "docs/x.md", body)
	s := rep.Summary
	if s.ForcesAGuess != s.ReferencesGuessing+s.ReferencesUngrounded {
		t.Fatalf("forces_a_guess (%d) != guessing+ungrounded (%d+%d)", s.ForcesAGuess, s.ReferencesGuessing, s.ReferencesUngrounded)
	}
	if !strings.Contains(s.Headline, "force an agent to guess") {
		t.Fatalf("headline malformed: %q", s.Headline)
	}
	// elements_total counts every lexicon element (4), regardless of mention.
	if s.ElementsTotal != 4 {
		t.Fatalf("elements_total should be 4, got %d", s.ElementsTotal)
	}
}

// --- Suppression: a suppressed collision drops out -----------------------

func TestSuppression_DropsFinding(t *testing.T) {
	body := "# Communication\n\n## Adding children\n\nWhen a driver wants a child, it adds a child node and waits.\n"
	supp := emptySuppression()
	supp.add("docs/x.md", "node", "") // suppress any 'node' on this doc
	rep, err := Build(Options{
		Library:     "fuchsia.driver.framework",
		GeneratedAt: "2026-06-01T00:00:00Z",
		Elements:    driverElems(),
		Docs:        []Doc{{Path: "docs/x.md", Body: []byte(body)}},
		Suppress:    supp,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, f := range rep.Findings {
		if strings.EqualFold(f.Token, "node") {
			t.Fatalf("suppressed 'node' finding still present: %+v", f)
		}
	}
}

// --- Code blocks are not scanned as prose -------------------------------

func TestCodeBlockNotProse(t *testing.T) {
	body := "# Example\n\n```cpp\n// a node here is code, not prose\nNode node;\n```\n\nNo prose mention follows.\n"
	rep := build(t, driverElems(), "fuchsia.driver.framework", "docs/x.md", body)
	for _, f := range rep.Findings {
		if strings.Contains(f.Excerpt, "is code, not prose") {
			t.Fatalf("a code-block token leaked into prose findings: %+v", f)
		}
	}
}

// --- Determinism: same input -> identical finding IDs + order ------------

func TestDeterministic(t *testing.T) {
	body := "# Drivers and nodes\n\n## The node topology\n\nEach node exposes a controller. The node is bound.\n"
	r1 := build(t, driverElems(), "fuchsia.driver.framework", "docs/x.md", body)
	r2 := build(t, driverElems(), "fuchsia.driver.framework", "docs/x.md", body)
	if len(r1.Findings) != len(r2.Findings) {
		t.Fatalf("nondeterministic finding count: %d vs %d", len(r1.Findings), len(r2.Findings))
	}
	for i := range r1.Findings {
		if r1.Findings[i].ID != r2.Findings[i].ID || r1.Findings[i].State != r2.Findings[i].State {
			t.Fatalf("nondeterministic finding %d: %+v vs %+v", i, r1.Findings[i], r2.Findings[i])
		}
	}
}

// token_span must be CHARACTER offsets within the excerpt, not bytes —
// the UI indexes the excerpt string by code unit. A multibyte rune before
// the token (curly apostrophe here) must not shift the span.
func TestTokenSpan_UTF8CharOffsets(t *testing.T) {
	// "Fuchsia's" uses U+2019 (3 UTF-8 bytes). The bare "node" that follows
	// must get a span whose excerpt[start:start+len] is exactly the token
	// when the excerpt is indexed as runes.
	body := "# Drivers\n\nFuchsia’s framework adds a child node to the tree.\n"
	rep := build(t, driverElems(), "fuchsia.driver.framework", "docs/x.md", body)
	f := findingFor(t, rep, "Node", "node")
	runes := []rune(f.Excerpt)
	if f.TokenSpan.Start < 0 || f.TokenSpan.Start+f.TokenSpan.Len > len(runes) {
		t.Fatalf("span out of range: %+v len(runes)=%d", f.TokenSpan, len(runes))
	}
	got := string(runes[f.TokenSpan.Start : f.TokenSpan.Start+f.TokenSpan.Len])
	if !strings.EqualFold(got, "node") {
		t.Fatalf("rune-indexed span %v points at %q, want 'node' (excerpt=%q)", f.TokenSpan, got, f.Excerpt)
	}
}

func anchorFound(f Finding, k AnchorKind) bool {
	for _, c := range f.Checked {
		if c.Anchor == k {
			return c.Found
		}
	}
	return false
}

// --- State: GROUNDED via a reference-style link -------------------------

// A bare "node" is a collision (Guessing on its own), but here it sits inside
// a reference-style link [text][label] whose definition points at the Node
// element's FIDL reference — so the link anchor grounds it.
func TestGrounded_ReferenceStyleLink(t *testing.T) {
	body := "# Drivers\n\nSee the [driver node][nd] returned by the call.\n\n" +
		"[nd]: https://fuchsia.dev/reference/fidl/fuchsia.driver.framework#Node\n"
	rep := build(t, driverElems(), "fuchsia.driver.framework", "docs/x.md", body)
	f := findingFor(t, rep, "Node", "node")
	if f.State != StateGrounded {
		t.Errorf("reference-style link should ground Node; got %s%s", f.State, summarize(rep))
	}
	if !anchorFound(f, AnchorLink) {
		t.Errorf("expected the link anchor to fire for a reference-style link")
	}
}

// A reference-style link whose target does NOT resolve to the element (a TOC
// anchor) must not ground it — the link anchor stays a real, followable
// reference, not any bracketed token.
func TestNotGrounded_ReferenceLinkToUnrelatedTarget(t *testing.T) {
	body := "# Drivers\n\nSee the [driver node][toc] section below.\n\n" +
		"[toc]: #table-of-contents\n"
	rep := build(t, driverElems(), "fuchsia.driver.framework", "docs/x.md", body)
	f := findingFor(t, rep, "Node", "node")
	if anchorFound(f, AnchorLink) {
		t.Errorf("a reference link to an unrelated target must not fire the link anchor")
	}
}

func TestParseLinkDefs(t *testing.T) {
	body := []byte("intro [x][lbl]\n\n[LBL]: https://x/#Node\n  [other]:   y.md \"Title\"\nnot a def: nope\n")
	defs := parseLinkDefs(body)
	if defs["lbl"] != "https://x/#Node" {
		t.Errorf("lbl = %q, want the URL (labels are case-insensitive)", defs["lbl"])
	}
	if defs["other"] != "y.md" {
		t.Errorf("other = %q, want y.md (title stripped)", defs["other"])
	}
}
