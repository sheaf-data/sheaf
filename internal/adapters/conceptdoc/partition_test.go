package conceptdoc

import (
	"testing"

	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

// This file pins the clear/ambiguous/silent partition the adapter computes
// from site fan-out degree (the productionized form of the throwaway audit's
// .claude/audit/classify.py). The partition is exhaustive and disjoint:
// clear + ambiguous + silent == total, and referenced == clear + ambiguous.

// verdictOf returns the Verdict for the element with the given id, or fails.
func verdictOf(t *testing.T, res *Result, id string) ElementCoverage {
	t.Helper()
	for _, e := range res.Elements {
		if e.ElementID == id {
			return e
		}
	}
	t.Fatalf("no element %q in result; elements=%+v", id, res.Elements)
	return ElementCoverage{}
}

// assertPartitionIdentity locks the exhaustive/disjoint invariant on every
// Result the partition tests build.
func assertPartitionIdentity(t *testing.T, res *Result) {
	t.Helper()
	p := res.Summary.Partition
	if p.Total != len(res.Elements) {
		t.Fatalf("partition total %d != element count %d", p.Total, len(res.Elements))
	}
	if p.Clear+p.Ambiguous+p.Silent != p.Total {
		t.Fatalf("partition not exhaustive: clear %d + ambiguous %d + silent %d != total %d",
			p.Clear, p.Ambiguous, p.Silent, p.Total)
	}
	if p.Referenced != p.Clear+p.Ambiguous {
		t.Fatalf("referenced %d != clear %d + ambiguous %d", p.Referenced, p.Clear, p.Ambiguous)
	}
	// Cross-check the rollup against the per-element verdicts.
	var c, a, s int
	for _, e := range res.Elements {
		switch e.Verdict {
		case VerdictClear:
			c++
		case VerdictAmbiguous:
			a++
		case VerdictSilent:
			s++
		default:
			t.Fatalf("element %q has no verdict", e.ElementID)
		}
		// Covered must agree with the verdict.
		if (e.Verdict == VerdictSilent) == e.Covered {
			t.Fatalf("element %q verdict %q disagrees with Covered=%v", e.ElementID, e.Verdict, e.Covered)
		}
	}
	if c != p.Clear || a != p.Ambiguous || s != p.Silent {
		t.Fatalf("per-element tally (c=%d a=%d s=%d) != rollup (c=%d a=%d s=%d)",
			c, a, s, p.Clear, p.Ambiguous, p.Silent)
	}
}

// A degree-1 anchored mention (a backtick that resolves to one element) makes
// that element CLEAR; an in-scope element never mentioned is SILENT.
func TestPartition_ClearAndSilent(t *testing.T) {
	res := detect(doc("docs/x.md", "# Drivers\n\nThe `NodeController` manages one child.\n"))

	nc := verdictOf(t, res, lib+"/NodeController")
	if nc.Verdict != VerdictClear {
		t.Fatalf("backticked `NodeController` should be CLEAR; got %q (minDegree=%d)", nc.Verdict, nc.MinDegree)
	}
	if nc.MinDegree != 1 {
		t.Fatalf("a lone backtick is a degree-1 site; got minDegree=%d", nc.MinDegree)
	}
	// The three never-mentioned elements are silent.
	for _, id := range []string{lib + "/Node", lib + "/CompositeNodeManager", lib + "/NodePropertyKey"} {
		if v := verdictOf(t, res, id); v.Verdict != VerdictSilent {
			t.Fatalf("unmentioned %q should be SILENT; got %q", id, v.Verdict)
		}
	}
	p := res.Summary.Partition
	if p.Clear != 1 || p.Ambiguous != 0 || p.Silent != 3 || p.Total != 4 {
		t.Fatalf("want clear=1 ambig=0 silent=3 total=4; got %+v", p)
	}
	assertPartitionIdentity(t, res)
}

// A degree-2 site (one written token that anchors TWO distinct in-scope
// elements) makes BOTH elements AMBIGUOUS — the docs name them but never
// single either out. Two same-named Provider types in different libraries,
// hit by a single bare backtick `Provider` (no library prefix → both match),
// form that shared site.
func TestPartition_Ambiguous_SharedSite(t *testing.T) {
	const la = "fuchsia.ui.activity"
	const lt = "fuchsia.tracing.provider"
	elems := []*contractpb.ContractElement{
		{Id: la + "/Provider", Kind: contractpb.ContractElementKind_PROTOCOL, Library: la},
		{Id: lt + "/Provider", Kind: contractpb.ContractElementKind_PROTOCOL, Library: lt},
	}
	// One bare backtick `Provider` on one line: a single (doc,line,token) site
	// that anchors both Providers — degree 2.
	res := Detect(Options{Library: "", Elements: elems,
		Docs: []Doc{doc("docs/x.md", "# Reg\n\nRegister with the `Provider` protocol.\n")}})

	for _, id := range []string{la + "/Provider", lt + "/Provider"} {
		v := verdictOf(t, res, id)
		if v.Verdict != VerdictAmbiguous {
			t.Fatalf("%q should be AMBIGUOUS (shared degree-2 site); got %q (minDegree=%d)", id, v.Verdict, v.MinDegree)
		}
		if !v.Covered {
			t.Fatalf("%q is referenced, so Covered must be true", id)
		}
		if v.MinDegree != 2 {
			t.Fatalf("%q minDegree should be 2; got %d", id, v.MinDegree)
		}
	}
	p := res.Summary.Partition
	if p.Clear != 0 || p.Ambiguous != 2 || p.Silent != 0 || p.Referenced != 2 {
		t.Fatalf("want clear=0 ambig=2 silent=0 ref=2; got %+v", p)
	}
	assertPartitionIdentity(t, res)
}

// An element with BOTH a shared (degree-2) mention AND a disambiguating
// (degree-1) mention is CLEAR — one unambiguous reference is enough. A
// library-qualified `fuchsia.ui.activity/Provider` resolves to one element
// alone (degree 1), lifting it to clear while its same-named kin, hit only by
// the bare shared backtick, stays ambiguous.
func TestPartition_ClearWins_OverSharedSite(t *testing.T) {
	const la = "fuchsia.ui.activity"
	const lt = "fuchsia.tracing.provider"
	elems := []*contractpb.ContractElement{
		{Id: la + "/Provider", Kind: contractpb.ContractElementKind_PROTOCOL, Library: la},
		{Id: lt + "/Provider", Kind: contractpb.ContractElementKind_PROTOCOL, Library: lt},
	}
	// The disambiguating FQN sits on its OWN line so it is a DISTINCT site
	// (the site key is doc+line+token; a same-line FQN would collapse into the
	// bare token's shared site, exactly as classify.py groups by line).
	body := "# Reg\n\nThe bare `Provider` is shared across libraries.\n\nResolve it via `fuchsia.ui.activity/Provider` to be exact.\n"
	res := Detect(Options{Library: "", Elements: elems, Docs: []Doc{doc("docs/x.md", body)}})

	if v := verdictOf(t, res, la+"/Provider"); v.Verdict != VerdictClear {
		t.Fatalf("ui.activity/Provider has a degree-1 FQN mention -> CLEAR; got %q (minDegree=%d)", v.Verdict, v.MinDegree)
	}
	if v := verdictOf(t, res, lt+"/Provider"); v.Verdict != VerdictAmbiguous {
		t.Fatalf("tracing.provider/Provider only shares the bare site -> AMBIGUOUS; got %q", v.Verdict)
	}
	p := res.Summary.Partition
	if p.Clear != 1 || p.Ambiguous != 1 {
		t.Fatalf("want clear=1 ambig=1; got %+v", p)
	}
	assertPartitionIdentity(t, res)
}

// With no concept docs at all, every in-scope element is SILENT and the
// partition is all-silent (the additive default).
func TestPartition_AllSilent_NoDocs(t *testing.T) {
	res := detect() // no docs
	p := res.Summary.Partition
	if p.Total != 4 || p.Silent != 4 || p.Clear != 0 || p.Ambiguous != 0 || p.Referenced != 0 {
		t.Fatalf("no docs -> all silent; want total=4 silent=4; got %+v", p)
	}
	assertPartitionIdentity(t, res)
}
