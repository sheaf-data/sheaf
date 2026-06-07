package conceptdoc

import (
	"reflect"
	"testing"

	"github.com/sheaf-data/sheaf/internal/grounding"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
)

const lib = "fuchsia.driver.framework"

// driverElems mirrors a slice of the fuchsia.driver.framework surface:
// Node / NodeController / CompositeNodeManager / NodePropertyKey. "node",
// "controller", "manager" are common-noun collision tails; the multi-word /
// PascalCase names are not.
func driverElems() []*contractpb.ContractElement {
	return []*contractpb.ContractElement{
		{Id: lib + "/Node", Kind: contractpb.ContractElementKind_PROTOCOL, Library: lib},
		{Id: lib + "/NodeController", Kind: contractpb.ContractElementKind_PROTOCOL, Library: lib},
		{Id: lib + "/CompositeNodeManager", Kind: contractpb.ContractElementKind_PROTOCOL, Library: lib},
		{Id: lib + "/NodePropertyKey", Kind: contractpb.ContractElementKind_TYPE, Library: lib},
	}
}

// detect runs the engine over the given in-memory docs.
func detect(docs ...Doc) *Result {
	return Detect(Options{
		Library:  lib,
		Elements: driverElems(),
		Docs:     docs,
	})
}

func doc(path, body string) Doc { return Doc{Path: path, Body: []byte(body)} }

// coverageOf returns the ElementCoverage for the element whose display name
// matches, or fails.
func coverageOf(t *testing.T, res *Result, display string) ElementCoverage {
	t.Helper()
	for _, e := range res.Elements {
		if e.Display == display {
			return e
		}
	}
	t.Fatalf("no element coverage for %q; elements=%+v", display, res.Elements)
	return ElementCoverage{}
}

// --- ANCHOR: qualified (backticked) mention attributes -------------------

func TestQualifiedMention_Attributes(t *testing.T) {
	body := "# Drivers\n\nThe parent drives the child through the `NodeController` protocol.\n"
	res := detect(doc("docs/concepts/drivers/topology.md", body))

	nc := coverageOf(t, res, "NodeController")
	if !nc.Covered {
		t.Fatalf("NodeController should be covered by a backticked mention; got %+v", nc)
	}
	if nc.ClaimCount < 1 {
		t.Fatalf("expected >=1 claim, got %d", nc.ClaimCount)
	}
	// The claim must be a PROSE_MENTION attributed to exactly this element.
	c := nc.Claims[0]
	if c.GetKind() != docclaimpb.DocClaimKind_PROSE_MENTION {
		t.Fatalf("claim kind should be PROSE_MENTION, got %v", c.GetKind())
	}
	if got := c.GetContractRefs(); len(got) != 1 || got[0] != lib+"/NodeController" {
		t.Fatalf("claim contract_refs wrong: %v", got)
	}
	if c.GetAdapter() != AdapterName {
		t.Fatalf("claim adapter should be %q, got %q", AdapterName, c.GetAdapter())
	}
}

// --- ANCHOR: link attributes ---------------------------------------------

func TestLink_Attributes(t *testing.T) {
	// A bare-word "node" that would be a bare collision, but it sits inside
	// a markdown link resolving into the fidl reference -> anchored.
	body := "# Topology\n\nEach [node](/reference/fidl/fuchsia.driver.framework#Node) has a parent.\n"
	res := detect(doc("docs/concepts/drivers/topology.md", body))

	n := coverageOf(t, res, "Node")
	if !n.Covered {
		t.Fatalf("Node should be covered via a resolving link; got %+v", n)
	}
}

// --- ANCHOR: defined-term attributes -------------------------------------

func TestDefinedTerm_Attributes(t *testing.T) {
	// The exact name emphasized (**NodeController**) is markdown's
	// defined-term convention on an unambiguous form -> anchored.
	body := "# Concepts\n\nA **NodeController** lets the parent manage one child.\n"
	res := detect(doc("docs/concepts/drivers/defs.md", body))

	nc := coverageOf(t, res, "NodeController")
	if !nc.Covered {
		t.Fatalf("NodeController should be covered via defined-term; got %+v", nc)
	}
}

// --- NO ANCHOR: bare prose collision does NOT attribute ------------------

func TestBareCollision_DoesNotAttribute(t *testing.T) {
	// "node" appears as a bare common-noun collision: no backtick, no link,
	// no emphasis, no heading/title binding. It must NOT attribute (decision
	// 2 — bare prose collisions do not attribute; no ambiguity grading).
	body := "# Communication\n\n## Adding children\n\nWhen a driver wants a child, it adds a child node and waits.\n"
	res := detect(doc("docs/concepts/drivers/comm.md", body))

	n := coverageOf(t, res, "Node")
	if n.Covered {
		t.Fatalf("bare 'node' collision must NOT attribute Node; got covered=%v claims=%d", n.Covered, n.ClaimCount)
	}
	// And no claims at all should have been emitted for this thin doc.
	if len(res.Claims) != 0 {
		t.Fatalf("bare-collision doc should emit zero claims, got %d: %+v", len(res.Claims), res.Claims)
	}
}

// A heading that binds the term is CONTEXT, not an anchor — still no
// attribution for a bare prose mention under it.
func TestHeadingBoundBareMention_DoesNotAttribute(t *testing.T) {
	body := "# Drivers and nodes\n\n## The node topology\n\nEach node exposes a controller that the parent uses.\n"
	res := detect(doc("docs/concepts/drivers/topo.md", body))

	n := coverageOf(t, res, "Node")
	if n.Covered {
		t.Fatalf("heading-bound bare 'node' must NOT attribute (context, not anchor); got %+v", n)
	}
}

// --- NOT-COVERED: element with zero anchored mentions --------------------

func TestZeroMentions_NotCovered(t *testing.T) {
	// A doc that anchors NodeController but never mentions NodePropertyKey.
	body := "# Drivers\n\nThe `NodeController` protocol is returned by AddChild.\n"
	res := detect(doc("docs/concepts/drivers/x.md", body))

	// Every in-scope element must appear in the rollup exactly once.
	if res.Summary.ElementsTotal != 4 {
		t.Fatalf("expected 4 in-scope elements, got %d", res.Summary.ElementsTotal)
	}
	npk := coverageOf(t, res, "NodePropertyKey")
	if npk.Covered {
		t.Fatalf("NodePropertyKey has no mention; must be not-covered, got %+v", npk)
	}
	if npk.ClaimCount != 0 || len(npk.DocPaths) != 0 {
		t.Fatalf("not-covered element must have 0 claims / 0 docs, got %+v", npk)
	}
	// Exactly one element (NodeController) is covered here.
	if res.Summary.ElementsCovered != 1 {
		t.Fatalf("expected exactly 1 covered element, got %d (%+v)", res.Summary.ElementsCovered, res.Summary)
	}
}

// --- MULTI-DOC DEDUP: one element in two docs = one covered element ------

func TestMultiDocDedup(t *testing.T) {
	// NodeController anchored in TWO different docs. It must count ONCE at
	// the element level (Covered, ElementsCovered) but track BOTH source
	// docs and BOTH claims.
	d1 := doc("docs/concepts/drivers/a.md", "# A\n\nThe `NodeController` protocol.\n")
	d2 := doc("docs/development/drivers/b.md", "# B\n\nUse `NodeController` to manage children.\n")
	res := detect(d1, d2)

	nc := coverageOf(t, res, "NodeController")
	if !nc.Covered {
		t.Fatalf("NodeController should be covered")
	}
	// Element-level: counted once.
	if res.Summary.ElementsCovered != 1 {
		t.Fatalf("multi-doc element must count once at element level; ElementsCovered=%d", res.Summary.ElementsCovered)
	}
	// But both docs tracked, deduped + sorted.
	wantDocs := []string{"docs/concepts/drivers/a.md", "docs/development/drivers/b.md"}
	if !reflect.DeepEqual(nc.DocPaths, wantDocs) {
		t.Fatalf("expected both source docs tracked %v, got %v", wantDocs, nc.DocPaths)
	}
	// And both claims retained.
	if nc.ClaimCount != 2 || len(nc.Claims) != 2 {
		t.Fatalf("expected 2 claims tracked, got count=%d len=%d", nc.ClaimCount, len(nc.Claims))
	}
	if res.Summary.ClaimsTotal != 2 {
		t.Fatalf("expected 2 total claims, got %d", res.Summary.ClaimsTotal)
	}
}

// The SAME element anchored twice in the SAME doc still de-dups to one
// covered element and one tracked doc path (but two claims).
func TestSameDocMultipleMentions_OneDocPath(t *testing.T) {
	body := "# A\n\nThe `NodeController` protocol.\n\nLater, `NodeController` again.\n"
	res := detect(doc("docs/concepts/drivers/a.md", body))

	nc := coverageOf(t, res, "NodeController")
	if len(nc.DocPaths) != 1 || nc.DocPaths[0] != "docs/concepts/drivers/a.md" {
		t.Fatalf("same-doc mentions must track one doc path, got %v", nc.DocPaths)
	}
	if nc.ClaimCount != 2 {
		t.Fatalf("expected 2 claims for two mentions, got %d", nc.ClaimCount)
	}
}

// --- DETERMINISM ---------------------------------------------------------

func TestDeterminism(t *testing.T) {
	docs := []Doc{
		doc("docs/concepts/drivers/a.md", "# A\n\nThe `NodeController` and the `Node` protocols.\n"),
		doc("docs/development/drivers/b.md", "# B\n\nA **CompositeNodeManager** composes nodes; see [Node](/reference/fidl/fuchsia.driver.framework#Node).\n"),
	}
	first := Detect(Options{Library: lib, Elements: driverElems(), Docs: docs})
	for i := 0; i < 5; i++ {
		got := Detect(Options{Library: lib, Elements: driverElems(), Docs: docs})
		if !reflect.DeepEqual(summarize(first), summarize(got)) {
			t.Fatalf("run %d differs from first run:\n first=%v\n got  =%v", i, summarize(first), summarize(got))
		}
	}
}

// summarize reduces a Result to a comparable shape (element coverage +
// claim refs/paths/lines) for determinism assertions.
func summarize(res *Result) []string {
	var out []string
	for _, e := range res.Elements {
		row := e.ElementID + "|" + boolStr(e.Covered) + "|" + itoa(e.ClaimCount)
		for _, p := range e.DocPaths {
			row += "|" + p
		}
		out = append(out, row)
	}
	for _, c := range res.Claims {
		out = append(out, c.GetSourcePath()+"@"+itoa(int(c.GetLocation().GetLine()))+"->"+c.GetContractRefs()[0])
	}
	return out
}

func boolStr(b bool) string {
	if b {
		return "T"
	}
	return "F"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// --- SCOPE: out-of-library + LIBRARY synthetic excluded ------------------

func TestScope_OnlyInLibraryNonSynthetic(t *testing.T) {
	elems := []*contractpb.ContractElement{
		{Id: lib + "/Node", Kind: contractpb.ContractElementKind_PROTOCOL, Library: lib},
		// A LIBRARY synthetic — must be excluded from the rollup.
		{Id: lib, Kind: contractpb.ContractElementKind_LIBRARY, Library: lib},
		// An out-of-library element — must be excluded.
		{Id: "other.lib/Thing", Kind: contractpb.ContractElementKind_PROTOCOL, Library: "other.lib"},
	}
	res := Detect(Options{
		Library:  lib,
		Elements: elems,
		Docs:     []Doc{doc("docs/x.md", "# X\n\nThe `Node` protocol.\n")},
	})
	if res.Summary.ElementsTotal != 1 {
		t.Fatalf("only the in-library non-synthetic element should be in scope, got total=%d (%+v)",
			res.Summary.ElementsTotal, res.Elements)
	}
	if res.Elements[0].Display != "Node" {
		t.Fatalf("expected Node, got %q", res.Elements[0].Display)
	}
}

// Sanity: a generic sentence using common words that are NOT contract tails
// produces zero claims (not a general prose linter).
func TestNonContractCommonWords_NoClaims(t *testing.T) {
	body := "# Overview\n\nThe service is running and the system is healthy.\n"
	res := detect(doc("docs/x.md", body))
	if len(res.Claims) != 0 {
		t.Fatalf("non-contract common words must emit zero claims, got %d", len(res.Claims))
	}
}

// Guard the reuse seam directly: AnchoredMentions returns only anchored
// hits, and a bare collision yields none even when present in prose.
func TestAnchoredMentionsSeam(t *testing.T) {
	anchored := grounding.AnchoredMentions(lib, driverElems(),
		[]grounding.Doc{{Path: "d.md", Body: []byte("The `Node` is bare here: node node node.\n")}}, nil)
	// Exactly one anchored mention: the backticked `Node`. The three bare
	// "node"s are dropped.
	got := 0
	for _, m := range anchored {
		if m.ElementID == lib+"/Node" {
			got++
		}
	}
	if got != 1 {
		t.Fatalf("expected exactly 1 anchored Node mention (the backticked one), got %d: %+v", got, anchored)
	}
}
